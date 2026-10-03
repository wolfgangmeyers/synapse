package managed

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSubagentNotificationFormatting(t *testing.T) {
	// N2: Structured notification includes child ID, description, terminal status,
	// duration, lifetime tokens, tool uses, result preview <= 500 Unicode chars,
	// and explicit full-pull pointer.
	// N4: Full output is NOT in notification.
	childRecord := store.ManagedSubagentRecord{
		AgentID:          "subagent-worker-42",
		ParentAgentID:    "parent-agent-1",
		ParentSessionID:  "sess-abc",
		ParentEpoch:      2,
		ParentGeneration: "gen-xyz",
		Description:      "Run database migration checks",
		SubagentType:     "general-purpose",
		Status:           "completed",
		TurnCount:        3,
		ToolUseCount:     7,
		TotalTokens:      4200,
		DurationSeconds:  12.34,
		ResultText:       "All 15 schema migrations verified successfully without errors.",
	}

	notification := FormatSubagentNotification(childRecord)

	// Check child ID, description, status, tool uses, tokens, duration
	assert.Contains(t, notification, "subagent-worker-42")
	assert.Contains(t, notification, "Run database migration checks")
	assert.Contains(t, notification, "completed")
	assert.Contains(t, notification, "12.34s")
	assert.Contains(t, notification, "4200")
	assert.Contains(t, notification, "7")
	assert.Contains(t, notification, "All 15 schema migrations verified successfully without errors.")

	// Check explicit pointer to get_subagent_result
	assert.Contains(t, notification, "get_subagent_result")
	assert.Contains(t, notification, "subagent-worker-42")

	// Verify truncation for long result text (> 500 Unicode characters)
	// Create 800 runes of Japanese text to verify Unicode rune (not byte) boundary handling
	var b strings.Builder
	for i := 0; i < 800; i++ {
		b.WriteRune('語') // 3-byte UTF-8 rune
	}
	longJapaneseText := b.String()
	require.Equal(t, 800, utf8.RuneCountInString(longJapaneseText))

	longRecord := childRecord
	longRecord.ResultText = longJapaneseText

	longNotification := FormatSubagentNotification(longRecord)

	// Full output must NOT be in notification
	assert.NotContains(t, longNotification, longJapaneseText)
	// Preview must be bounded to <= 500 Unicode runes
	preview := SubagentResultPreview(longJapaneseText, 500)
	require.Equal(t, 500, utf8.RuneCountInString(preview))
	assert.Contains(t, longNotification, preview)

	// Explicit duration input option
	explicitDur := 45 * time.Second
	opts := SubagentNotificationOptions{ExplicitDuration: &explicitDur}
	customNotification := FormatSubagentNotificationWithOptions(childRecord, opts)
	assert.Contains(t, customNotification, "45.00s")

	// Error formatting when subagent failed
	errRecord := childRecord
	errRecord.Status = "error"
	errRecord.Error = "tool execution timed out after 30s"
	errNotification := FormatSubagentNotification(errRecord)
	assert.Contains(t, errNotification, "error")
	assert.Contains(t, errNotification, "tool execution timed out after 30s")
}

func TestSubagentResultReportFormatting(t *testing.T) {
	// P1: Running snapshot warning mandatory exact text:
	// "**MANDATORY**!!! This is a snapshot only. Do NOT repeatedly pull a running result!!! Rest and wait for completion; a completion notification will arrive automatically."
	const expectedMandatoryWarning = "**MANDATORY**!!! This is a snapshot only. Do NOT repeatedly pull a running result!!! Rest and wait for completion; a completion notification will arrive automatically."
	require.Equal(t, expectedMandatoryWarning, SubagentSnapshotWarning)

	runningRecord := store.ManagedSubagentRecord{
		AgentID:          "subagent-running-1",
		ParentAgentID:    "parent-1",
		ParentSessionID:  "sess-1",
		ParentEpoch:      1,
		ParentGeneration: "gen-1",
		Description:      "Processing dataset",
		Status:           "running",
		TurnCount:        2,
		ToolUseCount:     4,
		TotalTokens:      1500,
		DurationSeconds:  5.5,
		ResultText:       "In progress: 50% parsed",
	}

	runningReport := FormatSubagentResultReport(runningRecord)
	assert.Contains(t, runningReport, SubagentSnapshotWarning)
	assert.Contains(t, runningReport, "subagent-running-1")
	assert.Contains(t, runningReport, "running")
	assert.Contains(t, runningReport, "In progress: 50% parsed")

	queuedRecord := runningRecord
	queuedRecord.Status = "queued"
	queuedReport := FormatSubagentResultReport(queuedRecord)
	assert.Contains(t, queuedReport, SubagentSnapshotWarning)
	assert.Contains(t, queuedReport, "queued")

	// P3: Terminal state report has NO snapshot warning, includes full result text
	fullOutput := strings.Repeat("Detailed execution logs line.\n", 50)
	terminalRecord := store.ManagedSubagentRecord{
		AgentID:          "subagent-done-1",
		ParentAgentID:    "parent-1",
		ParentSessionID:  "sess-1",
		ParentEpoch:      1,
		ParentGeneration: "gen-1",
		Description:      "Build complete report",
		Status:           "completed",
		TurnCount:        5,
		ToolUseCount:     12,
		TotalTokens:      8900,
		DurationSeconds:  24.5,
		ResultText:       fullOutput,
	}

	terminalReport := FormatSubagentResultReport(terminalRecord)
	assert.NotContains(t, terminalReport, SubagentSnapshotWarning)
	assert.Contains(t, terminalReport, "completed")
	assert.Contains(t, terminalReport, fullOutput) // Full output present, not truncated

	// Optional transcript support
	reportWithTranscript := FormatSubagentResultReportWithOptions(terminalRecord, SubagentReportOptions{
		Transcript: "User: start\nAssistant: done",
	})
	assert.Contains(t, reportWithTranscript, "User: start\nAssistant: done")
}

func TestSubagentThrottleRollingWindow(t *testing.T) {
	// P2: Fourth pull in rolling 60s rejects; 3 pulls allowed
	start := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
	clock := deterministic.NewFixedClock(start)
	throttle := NewSubagentThrottle(clock)

	key := SubagentThrottleKey{
		ParentSessionID:  "sess-1",
		ParentEpoch:      1,
		ParentGeneration: "gen-1",
		ChildID:          "child-worker-1",
	}

	// 1st pull: allowed
	err := throttle.Allow(key, "running")
	require.NoError(t, err)

	// Advance 10s: 2nd pull: allowed
	require.NoError(t, clock.Advance(10*time.Second))
	err = throttle.Allow(key, "running")
	require.NoError(t, err)

	// Advance 15s: 3rd pull: allowed
	require.NoError(t, clock.Advance(15*time.Second))
	err = throttle.Allow(key, "running")
	require.NoError(t, err)

	// Advance 5s (total elapsed 30s < 60s): 4th pull: REJECTED!
	require.NoError(t, clock.Advance(5*time.Second))
	err = throttle.Allow(key, "running")
	require.Error(t, err)
	require.ErrorIs(t, err, ErrSubagentPollThrottled)
	assert.Contains(t, err.Error(), "stop polling")

	// Advance another 5s: 5th pull: STILL REJECTED!
	require.NoError(t, clock.Advance(5*time.Second))
	err = throttle.Allow(key, "running")
	require.Error(t, err)
	require.ErrorIs(t, err, ErrSubagentPollThrottled)

	// Advance 30s more (total 65s from start):
	// The 1st pull at t=0 has aged out of rolling 60s window (now - 60s = 5s).
	// Only 2 pulls remain in window (at t=10s and t=25s).
	// Pull at t=65s must now be ALLOWED!
	require.NoError(t, clock.Advance(30*time.Second))
	err = throttle.Allow(key, "running")
	require.NoError(t, err)

	// Queued status is also throttled identically
	queuedKey := SubagentThrottleKey{
		ParentSessionID:  "sess-2",
		ParentEpoch:      1,
		ParentGeneration: "gen-1",
		ChildID:          "child-queued-1",
	}
	require.NoError(t, throttle.Allow(queuedKey, "queued"))
	require.NoError(t, throttle.Allow(queuedKey, "queued"))
	require.NoError(t, throttle.Allow(queuedKey, "queued"))
	err = throttle.Allow(queuedKey, "queued")
	require.Error(t, err)
	require.ErrorIs(t, err, ErrSubagentPollThrottled)
}

func TestSubagentThrottleTerminalUnlimited(t *testing.T) {
	// P3: Terminal states are never throttled
	start := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
	clock := deterministic.NewFixedClock(start)
	throttle := NewSubagentThrottle(clock)

	key := SubagentThrottleKey{
		ParentSessionID:  "sess-1",
		ParentEpoch:      1,
		ParentGeneration: "gen-1",
		ChildID:          "child-term-1",
	}

	terminalStatuses := []string{
		"completed",
		"steered",
		"aborted",
		"stopped",
		"error",
		"interrupted",
	}

	for _, status := range terminalStatuses {
		// Pull 10 times in a row without advancing clock; must never reject
		for i := 0; i < 10; i++ {
			err := throttle.Allow(key, status)
			require.NoError(t, err, "status %q must never be throttled", status)
		}
	}
}

func TestSubagentThrottleKeyExactIsolation(t *testing.T) {
	// Throttle key is exact: parent session + epoch + generation + childID
	start := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
	clock := deterministic.NewFixedClock(start)
	throttle := NewSubagentThrottle(clock)

	base := SubagentThrottleKey{
		ParentSessionID:  "sess-A",
		ParentEpoch:      1,
		ParentGeneration: "gen-1",
		ChildID:          "child-1",
	}

	// Exhaust 3 pulls on base key
	require.NoError(t, throttle.Allow(base, "running"))
	require.NoError(t, throttle.Allow(base, "running"))
	require.NoError(t, throttle.Allow(base, "running"))
	require.ErrorIs(t, throttle.Allow(base, "running"), ErrSubagentPollThrottled)

	// Different child ID: not throttled
	diffChild := base
	diffChild.ChildID = "child-2"
	require.NoError(t, throttle.Allow(diffChild, "running"))

	// Different session ID: not throttled
	diffSession := base
	diffSession.ParentSessionID = "sess-B"
	require.NoError(t, throttle.Allow(diffSession, "running"))

	// Different epoch: not throttled
	diffEpoch := base
	diffEpoch.ParentEpoch = 2
	require.NoError(t, throttle.Allow(diffEpoch, "running"))

	// Different generation: not throttled
	diffGen := base
	diffGen.ParentGeneration = "gen-2"
	require.NoError(t, throttle.Allow(diffGen, "running"))
}

func TestSubagentThrottleBoundedKeyCleanup(t *testing.T) {
	// Bounded key cleanup prevents leaks
	start := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
	clock := deterministic.NewFixedClock(start)
	throttle := NewSubagentThrottleWithOptions(SubagentThrottleOptions{
		Clock:    clock,
		Window:   60 * time.Second,
		MaxPulls: 3,
		MaxKeys:  10, // Small capacity for test
	})

	// Add 10 keys
	for i := 0; i < 10; i++ {
		k := SubagentThrottleKey{
			ParentSessionID:  "sess-1",
			ParentEpoch:      1,
			ParentGeneration: "gen-1",
			ChildID:          fmt.Sprintf("child-%d", i),
		}
		require.NoError(t, throttle.Allow(k, "running"))
	}
	require.Equal(t, 10, throttle.Len())

	// Advance clock past 60s so all existing entries expire
	require.NoError(t, clock.Advance(70*time.Second))

	// Adding 11th key triggers cleanup of expired keys
	newKey := SubagentThrottleKey{
		ParentSessionID:  "sess-1",
		ParentEpoch:      1,
		ParentGeneration: "gen-1",
		ChildID:          "child-new",
	}
	require.NoError(t, throttle.Allow(newKey, "running"))

	// Old keys were cleaned up; map count is bounded and does not grow indefinitely
	assert.LessOrEqual(t, throttle.Len(), 10)

	// Explicit prune
	throttle.Prune(clock.Now())
	assert.Equal(t, 1, throttle.Len()) // Only child-new remains
}

func TestSubagentThrottleConcurrency(t *testing.T) {
	// Small concurrency-safe rolling throttle
	clock := deterministic.NewFixedClock(time.Now().UTC())
	throttle := NewSubagentThrottle(clock)

	var wg sync.WaitGroup
	workers := 20
	pullsPerWorker := 30

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			key := SubagentThrottleKey{
				ParentSessionID:  fmt.Sprintf("sess-%d", workerID%4),
				ParentEpoch:      int64(workerID % 2),
				ParentGeneration: "gen-concurrent",
				ChildID:          fmt.Sprintf("child-%d", workerID),
			}
			for j := 0; j < pullsPerWorker; j++ {
				// Alternating running and completed
				status := "running"
				if j%2 == 0 {
					status = "completed"
				}
				_ = throttle.Allow(key, status)
			}
		}(i)
	}

	wg.Wait()
	assert.Greater(t, throttle.Len(), 0)
}
