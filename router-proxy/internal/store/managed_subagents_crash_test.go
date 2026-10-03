package store

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/stretchr/testify/require"
)

// TestManagedSubagentAbruptCrashSIGKILLReconciliation verifies acceptance criterion 9 / D2:
// when the router process is abruptly terminated by SIGKILL, any subagent left in
// "running" status transitions to "interrupted" upon restart with the exact error
// "router process terminated abruptly". Settled "completed" results and their statistics
// remain fully preserved and queryable by the current parent session after reopen.
func TestManagedSubagentAbruptCrashSIGKILLReconciliation(t *testing.T) {
	if os.Getenv("MECHA_STORE_CRASH_SUBPROCESS") == "1" {
		runManagedSubagentCrashChild(t)
		return
	}

	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "router.db")
	readyMarker := filepath.Join(tempDir, "ready.marker")

	cmd := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$", "-test.v")
	cmd.Env = append(os.Environ(),
		"MECHA_STORE_CRASH_SUBPROCESS=1",
		"MECHA_STORE_CRASH_DB="+dbPath,
		"MECHA_STORE_CRASH_READY="+readyMarker,
	)

	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output

	require.NoError(t, cmd.Start())

	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()

	t.Cleanup(func() {
		if cmd.Process != nil && (cmd.ProcessState == nil || !cmd.ProcessState.Exited()) {
			_ = cmd.Process.Signal(syscall.SIGKILL)
			_ = cmd.Process.Kill()
			select {
			case <-waited:
			case <-time.After(2 * time.Second):
			}
		}
	})

	// Deterministic readiness barrier: wait for the child to flush its persisted records
	// and write the marker file. Under -race with all SQLite migrations, startup can take
	// ~15-20 seconds.
	require.Eventually(t, func() bool {
		select {
		case err := <-waited:
			t.Fatalf("helper subprocess exited unexpectedly before readiness: %v; output:\n%s", err, output.String())
			return false
		default:
		}
		data, err := os.ReadFile(readyMarker)
		return err == nil && string(data) == "ready\n"
	}, 45*time.Second, 20*time.Millisecond, "helper subprocess did not reach readiness; output:\n%s", &output)

	// Send actual SIGKILL to the running helper process.
	require.NoError(t, cmd.Process.Signal(syscall.SIGKILL))
	waitErr := <-waited
	require.Error(t, waitErr, "subprocess must exit with error on SIGKILL")

	var exitErr *exec.ExitError
	require.ErrorAs(t, waitErr, &exitErr, "subprocess exit must be an ExitError")
	status, ok := exitErr.Sys().(syscall.WaitStatus)
	require.True(t, ok, "exit status must be syscall.WaitStatus")
	require.True(t, status.Signaled(), "subprocess must have been terminated by a signal")
	require.Equal(t, syscall.SIGKILL, status.Signal(), "subprocess must have been terminated by SIGKILL")

	ctx := context.Background()

	// Reopen the exact same SQLite database with an advanced clock so any old owner lease
	// is past expiration, allowing deterministic new owner acquisition without sleeping.
	reopenClock := deterministic.NewFixedClock(time.Now().Add(2 * time.Minute))
	reopened, err := Open(ctx, dbPath, Options{Clock: reopenClock})
	require.NoError(t, err)
	t.Cleanup(func() { _ = reopened.Close() })

	// Retrieve durable parent session coordinates from SQLite.
	var session, generation string
	var epoch int64
	require.NoError(t, reopened.db.QueryRowContext(ctx,
		`SELECT pi_session_id, session_epoch, runtime_generation FROM managed_agents WHERE agent_id=?`,
		"crash-parent").Scan(&session, &epoch, &generation))

	currentParent := EmbeddedBinding{
		AgentID:    "crash-parent",
		SessionID:  session,
		Epoch:      epoch,
		Generation: generation,
	}

	// Claim ownership with a new owner identity.
	newToken, err := reopened.AcquireEmbeddedOwner(ctx, currentParent, "crash-recovery-owner", time.Minute)
	require.NoError(t, err)
	currentParent.OwnerToken = newToken

	// Assert in-flight child transitioned from running -> interrupted with exact error message.
	interruptedChild, err := reopened.ManagedSubagent(ctx, currentParent, "subagent-crash-running")
	require.NoError(t, err)
	require.Equal(t, "interrupted", interruptedChild.Status)
	require.Equal(t, "router process terminated abruptly", interruptedChild.Error)
	require.NotEmpty(t, interruptedChild.TerminalAt)
	require.Equal(t, 1, interruptedChild.TurnCount)
	require.Equal(t, 2, interruptedChild.ToolUseCount)
	require.Equal(t, 50, interruptedChild.TotalTokens)
	require.Equal(t, 1.2, interruptedChild.DurationSeconds)
	require.Equal(t, "in-flight crash child", interruptedChild.Description)
	require.Equal(t, "general-purpose", interruptedChild.SubagentType)

	// Assert completed child retains exact pre-crash terminal result and final text/stats.
	completedChild, err := reopened.ManagedSubagent(ctx, currentParent, "subagent-crash-completed")
	require.NoError(t, err)
	require.Equal(t, "completed", completedChild.Status)
	require.Equal(t, "exact persisted terminal result", completedChild.ResultText)
	require.Empty(t, completedChild.Error)
	require.NotEmpty(t, completedChild.TerminalAt)
	require.Equal(t, 3, completedChild.TurnCount)
	require.Equal(t, 4, completedChild.ToolUseCount)
	require.Equal(t, 120, completedChild.TotalTokens)
	require.Equal(t, 3.8, completedChild.DurationSeconds)
	require.Equal(t, "settled child before crash", completedChild.Description)
	require.Equal(t, "general-purpose", completedChild.SubagentType)

	// Assert parent retrieval of all children succeeds for currentParent.
	allChildren, err := reopened.ManagedSubagents(ctx, currentParent)
	require.NoError(t, err)
	require.Len(t, allChildren, 2)

	// Fencing assertions: stale epoch, stale generation, and foreign parent must be rejected.
	staleEpochParent := currentParent
	staleEpochParent.Epoch++
	_, err = reopened.ManagedSubagent(ctx, staleEpochParent, "subagent-crash-running")
	require.Error(t, err)
	_, err = reopened.ManagedSubagent(ctx, staleEpochParent, "subagent-crash-completed")
	require.Error(t, err)
	_, err = reopened.ManagedSubagents(ctx, staleEpochParent)
	require.Error(t, err)

	staleGenParent := currentParent
	staleGenParent.Generation = "stale-generation"
	_, err = reopened.ManagedSubagent(ctx, staleGenParent, "subagent-crash-running")
	require.Error(t, err)
	_, err = reopened.ManagedSubagent(ctx, staleGenParent, "subagent-crash-completed")
	require.Error(t, err)
	_, err = reopened.ManagedSubagents(ctx, staleGenParent)
	require.Error(t, err)

	foreignParent := currentParent
	foreignParent.AgentID = "foreign-parent"
	_, err = reopened.ManagedSubagent(ctx, foreignParent, "subagent-crash-running")
	require.Error(t, err)
	_, err = reopened.ManagedSubagent(ctx, foreignParent, "subagent-crash-completed")
	require.Error(t, err)
	_, err = reopened.ManagedSubagents(ctx, foreignParent)
	require.Error(t, err)
}

func runManagedSubagentCrashChild(t *testing.T) {
	dbPath := os.Getenv("MECHA_STORE_CRASH_DB")
	readyMarker := os.Getenv("MECHA_STORE_CRASH_READY")
	require.NotEmpty(t, dbPath, "missing MECHA_STORE_CRASH_DB")
	require.NotEmpty(t, readyMarker, "missing MECHA_STORE_CRASH_READY")

	ctx := context.Background()
	s, err := Open(ctx, dbPath, Options{})
	require.NoError(t, err)

	parent := subagentParent(t, s, "crash-parent")

	// 1. Create child in running state
	runningChild := ManagedSubagentRecord{
		AgentID:       "subagent-crash-running",
		ParentAgentID: parent.AgentID,
		Description:   "in-flight crash child",
		SubagentType:  "general-purpose",
		Status:        "queued",
		MaxTurns:      5,
	}
	require.NoError(t, s.CreateManagedSubagent(ctx, parent, runningChild))
	require.NoError(t, s.UpdateManagedSubagent(ctx, parent, runningChild.AgentID, "queued", ManagedSubagentResult{
		Status:          "running",
		TurnCount:       1,
		ToolUseCount:    2,
		TotalTokens:     50,
		DurationSeconds: 1.2,
	}))

	// 2. Create child in completed state with final text and stats persisted
	completedChild := ManagedSubagentRecord{
		AgentID:       "subagent-crash-completed",
		ParentAgentID: parent.AgentID,
		Description:   "settled child before crash",
		SubagentType:  "general-purpose",
		Status:        "queued",
		MaxTurns:      5,
	}
	require.NoError(t, s.CreateManagedSubagent(ctx, parent, completedChild))
	require.NoError(t, s.UpdateManagedSubagent(ctx, parent, completedChild.AgentID, "queued", ManagedSubagentResult{
		Status:          "running",
		TurnCount:       2,
		ToolUseCount:    3,
		TotalTokens:     80,
		DurationSeconds: 2.5,
	}))
	require.NoError(t, s.UpdateManagedSubagent(ctx, parent, completedChild.AgentID, "running", ManagedSubagentResult{
		Status:          "completed",
		TurnCount:       3,
		ToolUseCount:    4,
		TotalTokens:     120,
		DurationSeconds: 3.8,
		ResultText:      "exact persisted terminal result",
	}))

	// Verify both child rows are persisted in the SQLite store before signaling readiness
	rRunning, err := s.ManagedSubagent(ctx, parent, runningChild.AgentID)
	require.NoError(t, err)
	require.Equal(t, "running", rRunning.Status)

	rCompleted, err := s.ManagedSubagent(ctx, parent, completedChild.AgentID)
	require.NoError(t, err)
	require.Equal(t, "completed", rCompleted.Status)

	// Write and sync readiness marker to signal parent
	markerFile, err := os.OpenFile(readyMarker, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	require.NoError(t, err)
	_, err = markerFile.WriteString("ready\n")
	require.NoError(t, err)
	require.NoError(t, markerFile.Sync())
	require.NoError(t, markerFile.Close())

	// Block until parent delivers SIGKILL.
	select {}
}
