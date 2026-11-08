package managed

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

// steerRecordStdin records every stdin write so a test can prove whether a
// steered record reached the process.
type steerRecordStdin struct{ writes [][]byte }

func (s *steerRecordStdin) Write(b []byte) (int, error) {
	s.writes = append(s.writes, append([]byte(nil), b...))
	return len(b), nil
}
func (s *steerRecordStdin) Close() error { return nil }

// A steer that passed its pre-check must not reach stdin after the turn's
// result has closed steering. The writeMu hold parks the steer exactly between
// its pre-check and its start callback, which is the window the race needs.
func TestClaudeSteerWriteAfterResultSettleIsRefused(t *testing.T) {
	ctx := context.Background()
	db, b, _, _ := wakePersistenceFixture(t)
	p := &embeddedPersistence{db: db, binding: b, digest: "genesis"}
	stdin := &steerRecordStdin{}
	proc := &ClaudeCodeProcess{stdin: stdin, inputs: make(map[string]claudeCodePendingInput), done: make(chan struct{}), closeDone: make(chan struct{}), notify: make(chan struct{}, 1)}
	r := &claudeCodeRuntime{
		p:            p,
		process:      proc,
		beforeSubmit: func(context.Context, int) error { return nil },
		active:       true,
		steerOpen:    true,
		snapshot:     embeddedSnapshot{ContextWindow: 1000000},
	}

	proc.writeMu.Lock()
	type steerResult struct {
		outcome claudeSteerOutcome
		err     error
	}
	done := make(chan steerResult, 1)
	go func() {
		outcome, err := r.steerInput(ctx, ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "steer"}}, Timestamp: time.Now().UnixMilli()}, nil, "req-steer")
		done <- steerResult{outcome, err}
	}()
	// inputCancels is set only after the pre-check passes, right before the write
	// blocks on writeMu. Wait for that point, then deliver the result.
	require.Eventually(t, func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		return len(r.inputCancels) != 0
	}, 5*time.Second, time.Millisecond)
	answered, uncertain := r.settleSteersAtResult()
	require.Empty(t, answered)
	require.Zero(t, uncertain)
	proc.writeMu.Unlock()

	res := <-done
	require.Equal(t, claudeSteerRefused, res.outcome, "steer write after result settle must be refused, got err=%v", res.err)
	require.Empty(t, stdin.writes, "no steered record may reach stdin after the turn's result")
	r.mu.Lock()
	require.Empty(t, r.steers, "refused steer must not remain registered")
	r.mu.Unlock()
}

// blockingStdin parks the first stdin write until released, so a test can run a
// result settle while a steer's write is started but still unechoed.
type blockingStdin struct {
	entered chan struct{}
	release chan struct{}
	writes  [][]byte
	once    sync.Once
}

func (s *blockingStdin) Write(b []byte) (int, error) {
	s.writes = append(s.writes, append([]byte(nil), b...))
	s.once.Do(func() { close(s.entered) })
	<-s.release
	return len(b), nil
}
func (s *blockingStdin) Close() error { return nil }

// A result that settles while a steered write is in stdin (started, not echoed)
// must not deadlock on the write's writeMu and must count the input as
// uncertain, not release it. The steer stays registered for a later checked echo.
func TestClaudeSteerResultDuringStartedWriteCountsUncertain(t *testing.T) {
	ctx := context.Background()
	db, b, _, _ := wakePersistenceFixture(t)
	p := &embeddedPersistence{db: db, binding: b, digest: "genesis"}
	stdin := &blockingStdin{entered: make(chan struct{}), release: make(chan struct{})}
	proc := &ClaudeCodeProcess{stdin: stdin, inputs: make(map[string]claudeCodePendingInput), done: make(chan struct{}), closeDone: make(chan struct{}), notify: make(chan struct{}, 1)}
	r := &claudeCodeRuntime{
		p:            p,
		process:      proc,
		beforeSubmit: func(context.Context, int) error { return nil },
		active:       true,
		steerOpen:    true,
		snapshot:     embeddedSnapshot{ContextWindow: 1000000},
	}

	type steerResult struct {
		outcome claudeSteerOutcome
		err     error
	}
	done := make(chan steerResult, 1)
	go func() {
		outcome, err := r.steerInput(ctx, ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "steer"}}, Timestamp: time.Now().UnixMilli()}, nil, "req-steer")
		done <- steerResult{outcome, err}
	}()

	// The write is in stdin: the start callback has marked it started and released r.mu.
	select {
	case <-stdin.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("steer write never reached stdin")
	}

	settled := make(chan struct{})
	var answered []*claudeSteer
	var uncertain int
	go func() {
		answered, uncertain = r.settleSteersAtResult()
		close(settled)
	}()
	select {
	case <-settled:
	case <-time.After(2 * time.Second):
		close(stdin.release)
		t.Fatal("settleSteersAtResult blocked while a steered write was in stdin")
	}
	require.Empty(t, answered, "an unechoed started write must not be answered by the result")
	require.Equal(t, 1, uncertain, "a started unechoed write must be counted as uncertain")

	close(stdin.release)
	res := <-done
	require.NoError(t, res.err)
	require.Equal(t, claudeSteerAccepted, res.outcome, "a write that started before settle is accepted, not refused")
	require.Len(t, stdin.writes, 1)

	r.mu.Lock()
	defer r.mu.Unlock()
	require.Len(t, r.steers, 1, "an uncertain steer must stay registered, not be released")
	for _, st := range r.steers {
		require.True(t, st.started)
		require.False(t, st.echoed)
	}
}

// Two steered writes can be in flight at once. Interrupt must cancel both, not
// only the most recent registration: the first write is parked in a full pipe
// and holds writeMu, while the second waits on writeMu. Interrupt has to unblock
// the first write so the second can finish and the control write can proceed.
func TestClaudeInterruptCancelsEveryInFlightInputWrite(t *testing.T) {
	ctx := context.Background()
	db, b, _, _ := wakePersistenceFixture(t)
	p := &embeddedPersistence{db: db, binding: b, digest: "genesis"}
	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	defer reader.Close()
	proc := &ClaudeCodeProcess{stdin: writer, inputs: make(map[string]claudeCodePendingInput), controls: make(map[string]chan claudeCodeControlResponse), notify: make(chan struct{}, 1), done: make(chan struct{}), closeDone: make(chan struct{})}
	r := &claudeCodeRuntime{
		p:               p,
		pool:            &claudeCodeProcessPool{},
		process:         proc,
		beforeSubmit:    func(context.Context, int) error { return nil },
		active:          true,
		steerOpen:       true,
		interruptNotify: make(chan struct{}),
		snapshot:        embeddedSnapshot{ContextWindow: 100000000},
	}
	now := time.Now().UnixMilli()
	big := ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: strings.Repeat("x", 1<<20)}}, Timestamp: now}
	small := ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "pending"}}, Timestamp: now}

	first := make(chan error, 1)
	go func() {
		_, err := r.steerInput(ctx, big, nil, "req-first")
		first <- err
	}()
	// Reading one byte proves the first write is inside the pipe write.
	var one [1]byte
	_, err = reader.Read(one[:])
	require.NoError(t, err)

	second := make(chan error, 1)
	go func() {
		_, err := r.steerInput(ctx, small, nil, "req-second")
		second <- err
	}()
	require.Eventually(t, func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		return len(r.inputCancels) == 2
	}, 5*time.Second, time.Millisecond)

	interrupted := make(chan struct{})
	go func() { r.Interrupt(); close(interrupted) }()
	select {
	case <-interrupted:
	case <-time.After(3 * time.Second):
		t.Fatal("interrupt blocked behind an in-flight steered write")
	}
	select {
	case <-first:
	case <-time.After(2 * time.Second):
		t.Fatal("interrupt did not cancel the first in-flight steered write")
	}
	select {
	case err := <-second:
		require.Error(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("interrupt did not cancel the second in-flight steered write")
	}
}
