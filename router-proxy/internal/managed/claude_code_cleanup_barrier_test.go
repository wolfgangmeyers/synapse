package managed

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

type cleanupBarrierStdin struct {
	entered chan struct{}
	release chan struct{}
}

func (*cleanupBarrierStdin) Write(b []byte) (int, error) { return len(b), nil }
func (s *cleanupBarrierStdin) Close() error              { close(s.entered); <-s.release; return nil }
func cleanupTestProcess(s io.WriteCloser) *ClaudeCodeProcess {
	done := make(chan struct{})
	close(done)
	return &ClaudeCodeProcess{stdin: s, done: done, exited: done, closeDone: make(chan struct{})}
}
func TestClaudeCleanupStoppingEntryJoins(t *testing.T) {
	s := &cleanupBarrierStdin{make(chan struct{}), make(chan struct{})}
	p := cleanupTestProcess(s)
	revoked := make(chan struct{})
	b := store.EmbeddedBinding{AgentID: "a"}
	e := &claudeCodeProcessEntry{binding: b, process: p, revoke: func() { close(revoked) }}
	pool := &claudeCodeProcessPool{entries: map[string]*claudeCodeProcessEntry{"a": e}}
	first := make(chan struct{})
	go func() { pool.stop("a"); close(first) }()
	<-s.entered
	select {
	case <-revoked:
	default:
		t.Error("grant not revoked before close")
	}
	pool.mu.Lock()
	present := pool.entries["a"] == e
	pool.mu.Unlock()
	if !present {
		t.Error("stopping entry removed")
	}
	acquire := make(chan error, 1)
	go func() {
		_, err := pool.acquire(context.Background(), b, ClaudeCodeProcessConfig{Binary: "/nonexistent/claude"}, nil)
		acquire <- err
	}()
	select {
	case <-acquire:
		t.Error("acquire passed stopping process")
	case <-time.After(50 * time.Millisecond):
	}
	second := make(chan struct{})
	go func() { pool.stop("a"); close(second) }()
	select {
	case <-second:
		t.Error("second stop did not join")
	case <-time.After(50 * time.Millisecond):
	}
	close(s.release)
	<-first
	<-second
	if err := <-acquire; err == nil {
		t.Error("expected failed preflight")
	}
}
func TestClaudeCleanupClosedDuringPreflight(t *testing.T) {
	dir := t.TempDir()
	entered := filepath.Join(dir, "entered")
	release := filepath.Join(dir, "release")
	bin := filepath.Join(dir, "claude")
	script := "#!/bin/sh\ntouch '" + entered + "'\nwhile [ ! -e '" + release + "' ]; do sleep .01; done\necho '{\"loggedIn\":true}'\n"
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	b := store.EmbeddedBinding{AgentID: "a"}
	pool := &claudeCodeProcessPool{}
	t.Cleanup(func() { _ = os.WriteFile(release, nil, 0600) })
	result := make(chan error, 1)
	go func() {
		_, err := pool.acquire(context.Background(), b, ClaudeCodeProcessConfig{Binary: bin, Cwd: dir, SessionID: "test-session", Model: "test-model"}, nil)
		result <- err
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(entered); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("preflight not reached")
		}
		time.Sleep(time.Millisecond)
	}
	pool.mu.Lock()
	pool.closed = true
	pool.mu.Unlock()
	os.WriteFile(release, nil, 0600)
	if err := <-result; err == nil {
		t.Fatal("acquire succeeded after close")
	}
}
func TestClaudeCleanupUnconfirmedAfterProtocolError(t *testing.T) {
	p := cleanupTestProcess(&cleanupBarrierStdin{make(chan struct{}), closedChannel()})
	p.err = errors.New("protocol")
	p.recordWaitError(errClaudeCodeGroupUnconfirmed)
	if err := p.Close(); !errors.Is(err, errClaudeCodeGroupUnconfirmed) {
		t.Fatalf("Close lost unconfirmed error: %v", err)
	}
}
func closedChannel() chan struct{} { c := make(chan struct{}); close(c); return c }
