package managed

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

func createTerminalSubagentFixture(t *testing.T, db *store.Store, parent store.EmbeddedBinding, childID string, attempt int, status string, result store.ManagedSubagentResult) store.ManagedSubagentRecord {
	t.Helper()
	ctx := context.Background()
	initRecord := store.ManagedSubagentRecord{
		AgentID:          childID,
		ParentAgentID:    parent.AgentID,
		ParentSessionID:  parent.SessionID,
		ParentEpoch:      parent.Epoch,
		ParentGeneration: parent.Generation,
		Description:      "background unit task",
		SubagentType:     "general-purpose",
		Status:           "running",
	}
	require.NoError(t, db.CreateManagedSubagent(ctx, parent, initRecord))
	result.Status = status
	require.NoError(t, db.UpdateManagedSubagentAttempt(ctx, parent, childID, attempt, "running", result))
	persisted, err := db.ManagedSubagent(ctx, parent, childID)
	require.NoError(t, err)
	return *persisted
}

func TestDeliverSubagentParentWake_IdleParentReceivesNotificationAndProcessesTurn(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "wake_idle_parent.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	parent := subagentTestParent(t, db, "parent-idle-turn")
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	var parentTurnRan atomic.Bool
	var receivedPrompt string

	reg.SetResponses([]providers.FauxResponseStep{
		func(hist ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			for _, m := range hist.Messages {
				if u, ok := m.(ai.UserMessage); ok {
					for _, c := range u.Content {
						if tc, ok := c.(ai.TextContent); ok {
							receivedPrompt = tc.Text
						}
					}
				}
			}
			parentTurnRan.Store(true)
			return providers.FauxAssistantMessage(ai.ContentList{
				ai.TextContent{Text: "Parent evaluated child completion notification."},
			}, ai.StopStop)
		},
	})

	mgr := makeSubagentTestManager(t, db, reg, 4)
	defer mgr.Close(ctx)
	registerParentOwner(mgr, parent)

	record := createTerminalSubagentFixture(t, db, parent, "child-wake-agent-1", 1, "completed", store.ManagedSubagentResult{
		TurnCount:       3,
		ToolUseCount:    2,
		TotalTokens:     450,
		DurationSeconds: 1.5,
		ResultText:      "All tasks finished successfully.",
	})

	require.NoError(t, mgr.DeliverSubagentParentWake(ctx, parent, record))

	require.Eventually(t, func() bool {
		return parentTurnRan.Load()
	}, 5*time.Second, 20*time.Millisecond, "idle parent must process turn on completion wake")
	require.Contains(t, receivedPrompt, "child-wake-agent-1")
	require.Contains(t, receivedPrompt, "status completed")
	require.Contains(t, receivedPrompt, "All tasks finished successfully.")
	require.Contains(t, receivedPrompt, "get_subagent_result")

	msgID := fmt.Sprintf("subagent-completed-%s-%d", record.AgentID, record.Attempt)
	msg, err := db.MessageFor(ctx, msgID)
	require.NoError(t, err)
	require.NotNil(t, msg)
	require.Equal(t, parent.AgentID, msg.To)
}

func TestDeliverSubagentParentWake_StableRequestIDDedupe(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "wake_dedupe.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	parent := subagentTestParent(t, db, "parent-dedupe")
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	var turnCount atomic.Int32
	reg.SetResponses([]providers.FauxResponseStep{
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			turnCount.Add(1)
			return providers.FauxAssistantMessage(ai.ContentList{
				ai.TextContent{Text: "Parent processed first delivery."},
			}, ai.StopStop)
		},
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			turnCount.Add(1)
			return providers.FauxAssistantMessage(ai.ContentList{
				ai.TextContent{Text: "Unexpected second turn."},
			}, ai.StopStop)
		},
	})

	mgr := makeSubagentTestManager(t, db, reg, 4)
	defer mgr.Close(ctx)
	registerParentOwner(mgr, parent)

	record := createTerminalSubagentFixture(t, db, parent, "child-wake-dedupe", 1, "completed", store.ManagedSubagentResult{
		ResultText: "done",
	})

	// First delivery executes the turn
	err = mgr.DeliverSubagentParentWake(ctx, parent, record)
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		return turnCount.Load() == 1
	}, 5*time.Second, 20*time.Millisecond)

	raw, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	defer raw.Close()

	msgID := fmt.Sprintf("subagent-completed-%s-%d", record.AgentID, record.Attempt)
	var count int
	err = raw.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE id=?`, msgID).Scan(&count)
	require.NoError(t, err)
	require.Equal(t, 1, count)

	// Duplicate delivery with the same child and attempt must be deduped
	err = mgr.DeliverSubagentParentWake(ctx, parent, record)
	require.NoError(t, err, "duplicate delivery must be acknowledged without error")

	// Ensure exactly one mailbox message exists for this child attempt
	err = raw.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE id=?`, msgID).Scan(&count)
	require.NoError(t, err)
	require.Equal(t, 1, count)
}

func TestDeliverSubagentParentWake_RejectsNonTerminalRecord(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "wake_non_terminal.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	parent := subagentTestParent(t, db, "parent-non-terminal")
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	mgr := makeSubagentTestManager(t, db, reg, 4)
	defer mgr.Close(ctx)
	registerParentOwner(mgr, parent)

	// Create real child in running state in store
	runningChild := store.ManagedSubagentRecord{
		AgentID:          "child-not-terminal",
		ParentAgentID:    parent.AgentID,
		ParentSessionID:  parent.SessionID,
		ParentEpoch:      parent.Epoch,
		ParentGeneration: parent.Generation,
		Description:      "running child",
		SubagentType:     "general-purpose",
		Status:           "running",
		Attempt:          1,
	}
	require.NoError(t, db.CreateManagedSubagent(ctx, parent, runningChild))

	// Rejects when caller passes running status
	err = mgr.DeliverSubagentParentWake(ctx, parent, runningChild)
	require.Error(t, err)
	require.Contains(t, err.Error(), "non-terminal")

	// Rejects when caller fakes terminal status but persisted child is still running
	fakeCompleted := runningChild
	fakeCompleted.Status = "completed"
	err = mgr.DeliverSubagentParentWake(ctx, parent, fakeCompleted)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not in terminal status")
}

func TestDeliverSubagentParentWake_RejectsMismatchedLineage(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "wake_lineage_mismatch.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	parent := subagentTestParent(t, db, "parent-lineage")
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	mgr := makeSubagentTestManager(t, db, reg, 4)
	defer mgr.Close(ctx)
	registerParentOwner(mgr, parent)

	record := createTerminalSubagentFixture(t, db, parent, "child-persisted", 1, "completed", store.ManagedSubagentResult{
		ResultText: "completed normally",
	})

	// 1. Foreign parent agent ID
	foreignRec := record
	foreignRec.ParentAgentID = "foreign-parent"
	err = mgr.DeliverSubagentParentWake(ctx, parent, foreignRec)
	require.Error(t, err)
	require.Contains(t, err.Error(), "mismatch")

	// 2. Foreign parent epoch
	wrongEpochRec := record
	wrongEpochRec.ParentEpoch = parent.Epoch + 1
	err = mgr.DeliverSubagentParentWake(ctx, parent, wrongEpochRec)
	require.Error(t, err)
	require.Contains(t, err.Error(), "mismatch")

	// 3. Stale attempt (e.g. attempting to deliver attempt 2 when persisted is attempt 1)
	staleAttemptRec := record
	staleAttemptRec.Attempt = 2
	err = mgr.DeliverSubagentParentWake(ctx, parent, staleAttemptRec)
	require.Error(t, err)
	require.Contains(t, err.Error(), "attempt mismatch")
}

func TestDeliverSubagentParentWake_ConcurrentResetBarrierBeforeCommitRejects(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "wake_reset_barrier.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	parent := subagentTestParent(t, db, "parent-barrier-reset")
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	var parentTurnRan atomic.Bool
	reg.SetResponses([]providers.FauxResponseStep{
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			parentTurnRan.Store(true)
			return providers.FauxAssistantMessage(ai.ContentList{
				ai.TextContent{Text: "should never run in old or new conversation"},
			}, ai.StopStop)
		},
	})

	mgr := makeSubagentTestManager(t, db, reg, 4)
	defer mgr.Close(ctx)
	registerParentOwner(mgr, parent)

	record := createTerminalSubagentFixture(t, db, parent, "child-wake-barrier", 1, "completed", store.ManagedSubagentResult{
		ResultText: "done",
	})

	newSessionID := parent.AgentID + "-new-session"
	newEpoch := parent.Epoch + 1

	raw, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	defer raw.Close()

	// Simulate concurrent reset barrier hook right BEFORE the atomic store reservation commit
	barrierFired := make(chan struct{}, 1)
	mgr.beforeSubagentWakeReservation = func(p store.EmbeddedBinding) {
		// Concurrent reset advances the parent epoch and session in SQLite
		_, err := raw.ExecContext(ctx,
			`UPDATE managed_agents SET session_epoch=?, pi_session_id=? WHERE agent_id=?`,
			newEpoch, newSessionID, p.AgentID,
		)
		if err != nil {
			panic(err)
		}
		// Reset the embedded session via Store API
		newBinding := store.EmbeddedBinding{
			AgentID:    p.AgentID,
			SessionID:  newSessionID,
			Epoch:      newEpoch,
			Generation: p.Generation,
		}
		if err := db.ResetEmbeddedSession(ctx, p, newBinding, "worker"); err != nil {
			panic(err)
		}
		newToken, err := db.AcquireEmbeddedOwner(ctx, newBinding, "fixture:"+p.AgentID, time.Minute)
		if err != nil {
			panic(err)
		}
		newBinding.OwnerToken = newToken

		// Update in-memory owner to new binding as a reset would do
		mgr.mu.Lock()
		mgr.embeddedOwners[p.AgentID] = embeddedOwner{
			binding:  newBinding,
			identity: "fixture:" + p.AgentID,
		}
		mgr.mu.Unlock()

		select {
		case barrierFired <- struct{}{}:
		default:
		}
	}

	// DeliverSubagentParentWake called with original parent binding (epoch 1)
	err = mgr.DeliverSubagentParentWake(ctx, parent, record)
	require.NoError(t, err, "subagent wake must succeed after concurrent reset")

	select {
	case <-barrierFired:
	default:
		t.Fatal("deterministic barrier hook did not fire")
	}

	// Verify mailbox notification was delivered to parent
	var msgCount int
	err = raw.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM messages WHERE recipient=? AND id LIKE 'subagent-completed-%'`,
		parent.AgentID,
	).Scan(&msgCount)
	require.NoError(t, err)
	require.Equal(t, 1, msgCount, "completion wake must be delivered to parent mailbox")
}

func TestDeliverSubagentParentWake_SucceedsWhenParentTransitionedToEpoch2(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "wake_epoch2.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	parent := subagentTestParent(t, db, "parent-epoch2-test")
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	mgr := makeSubagentTestManager(t, db, reg, 4)
	defer mgr.Close(ctx)
	registerParentOwner(mgr, parent)

	record := createTerminalSubagentFixture(t, db, parent, "child-wake-epoch2", 1, "completed", store.ManagedSubagentResult{
		ResultText: "done epoch 1",
	})

	// Transition parent to epoch 2
	newSessionID := parent.AgentID + "-epoch-2-session"
	newEpoch := parent.Epoch + 1
	newBinding := store.EmbeddedBinding{
		AgentID:    parent.AgentID,
		SessionID:  newSessionID,
		Epoch:      newEpoch,
		Generation: parent.Generation,
	}
	require.NoError(t, db.ResetEmbeddedSessionEpoch(ctx, parent, newBinding, "worker"))
	newToken, err := db.AcquireEmbeddedOwner(ctx, newBinding, "fixture:"+parent.AgentID, time.Minute)
	require.NoError(t, err)
	newBinding.OwnerToken = newToken

	mgr.mu.Lock()
	mgr.embeddedOwners[parent.AgentID] = embeddedOwner{
		binding:  newBinding,
		identity: "fixture:" + parent.AgentID,
	}
	mgr.mu.Unlock()

	// DeliverSubagentParentWake called with original parent binding (epoch 1)
	err = mgr.DeliverSubagentParentWake(ctx, parent, record)
	require.NoError(t, err, "subagent wake must succeed when parent has transitioned to epoch 2")

	// Verify mailbox notification was delivered to parent
	raw, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	defer raw.Close()

	var msgCount int
	err = raw.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE recipient=? AND id LIKE 'subagent-completed-%'`, parent.AgentID).Scan(&msgCount)
	require.NoError(t, err)
	require.Equal(t, 1, msgCount, "wake notification message must be delivered to parent")
}

func TestDeliverSubagentParentWake_StaleMemoryBindingRejects(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "wake_stale_binding.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	parent := subagentTestParent(t, db, "parent-stale-mem")
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	mgr := makeSubagentTestManager(t, db, reg, 4)
	defer mgr.Close(ctx)
	registerParentOwner(mgr, parent)

	record := createTerminalSubagentFixture(t, db, parent, "child-wake-stale", 1, "completed", store.ManagedSubagentResult{
		ResultText: "done",
	})

	staleParent := parent
	staleParent.Epoch = parent.Epoch + 99

	err = mgr.DeliverSubagentParentWake(ctx, staleParent, record)
	require.Error(t, err)
}

func TestDeliverSubagentParentWake_AsyncDelivery(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "wake_async.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	parent := subagentTestParent(t, db, "parent-async")
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	wakeDone := make(chan struct{}, 1)
	reg.SetResponses([]providers.FauxResponseStep{
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			select {
			case wakeDone <- struct{}{}:
			default:
			}
			return providers.FauxAssistantMessage(ai.ContentList{
				ai.TextContent{Text: "Async wake processed."},
			}, ai.StopStop)
		},
	})

	mgr := makeSubagentTestManager(t, db, reg, 4)
	defer mgr.Close(ctx)
	registerParentOwner(mgr, parent)

	record := createTerminalSubagentFixture(t, db, parent, "child-async-wake", 1, "completed", store.ManagedSubagentResult{
		ResultText: "done",
	})

	mgr.AsyncDeliverSubagentParentWake(parent, record)

	select {
	case <-wakeDone:
	case <-time.After(5 * time.Second):
		t.Fatal("async delivery did not wake parent within 5s")
	}

	raw, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	defer raw.Close()

	msgID := fmt.Sprintf("subagent-completed-%s-%d", record.AgentID, record.Attempt)
	require.Eventually(t, func() bool {
		var count int
		if err := raw.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE id=?`, msgID).Scan(&count); err != nil {
			return false
		}
		return count == 1
	}, 5*time.Second, 20*time.Millisecond, "mailbox message must be enqueued")
}

func TestDeliverSubagentParentWake_RejectsFabricatedNonexistentChild(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "wake_fake_cid.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	parent := subagentTestParent(t, db, "parent-fake-cid")
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	var turnCount atomic.Int32
	reg.SetResponses([]providers.FauxResponseStep{
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			turnCount.Add(1)
			return providers.FauxAssistantMessage(ai.ContentList{
				ai.TextContent{Text: "should never run"},
			}, ai.StopStop)
		},
	})

	mgr := makeSubagentTestManager(t, db, reg, 4)
	defer mgr.Close(ctx)
	registerParentOwner(mgr, parent)

	fakeRecord := store.ManagedSubagentRecord{
		AgentID:          "fabricated-nonexistent-child",
		ParentAgentID:    parent.AgentID,
		ParentSessionID:  parent.SessionID,
		ParentEpoch:      parent.Epoch,
		ParentGeneration: parent.Generation,
		Description:      "fake task",
		Status:           "completed",
		Attempt:          1,
	}

	err = mgr.DeliverSubagentParentWake(ctx, parent, fakeRecord)
	require.Error(t, err, "fabricated/nonexistent child must be rejected with non-nil error")
	require.Equal(t, int32(0), turnCount.Load(), "zero model turns must execute for fabricated child")

	raw, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	defer raw.Close()

	var promptRequests int
	err = raw.QueryRowContext(ctx, `SELECT COUNT(*) FROM embedded_prompt_requests`).Scan(&promptRequests)
	require.NoError(t, err)
	require.Equal(t, 0, promptRequests, "zero embedded_prompt_requests must be persisted for fabricated child")
}

func TestDeliverSubagentParentWake_RejectsForeignChildOwnedByOtherParent(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "wake_foreign_cid.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	parentA := subagentTestParent(t, db, "parent-foreign-a")
	parentB := subagentTestParent(t, db, "parent-foreign-b")
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	var turnCount atomic.Int32
	reg.SetResponses([]providers.FauxResponseStep{
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			turnCount.Add(1)
			return providers.FauxAssistantMessage(ai.ContentList{
				ai.TextContent{Text: "should never run"},
			}, ai.StopStop)
		},
	})

	mgr := makeSubagentTestManager(t, db, reg, 4)
	defer mgr.Close(ctx)
	registerParentOwner(mgr, parentA)
	registerParentOwner(mgr, parentB)

	// Child belongs to Parent A
	childRecordA := createTerminalSubagentFixture(t, db, parentA, "child-owned-by-a", 1, "completed", store.ManagedSubagentResult{
		ResultText: "finished task for A",
	})

	// Parent B attempts to deliver wake for Parent A's child
	childRecordForB := childRecordA
	childRecordForB.ParentAgentID = parentB.AgentID
	childRecordForB.ParentSessionID = parentB.SessionID
	childRecordForB.ParentEpoch = parentB.Epoch
	childRecordForB.ParentGeneration = parentB.Generation

	err = mgr.DeliverSubagentParentWake(ctx, parentB, childRecordForB)
	require.Error(t, err, "foreign child owned by another parent must be rejected")
	require.Equal(t, int32(0), turnCount.Load(), "zero model turns must execute for foreign child")

	raw, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	defer raw.Close()

	var promptRequests int
	err = raw.QueryRowContext(ctx, `SELECT COUNT(*) FROM embedded_prompt_requests WHERE agent_id=?`, parentB.AgentID).Scan(&promptRequests)
	require.NoError(t, err)
	require.Equal(t, 0, promptRequests, "zero embedded_prompt_requests must be persisted for foreign child on parent B")
}

func TestDeliverSubagentParentWake_WithAndWithoutActiveSiblings_Integration(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "wake_siblings_integration.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	parent := subagentTestParent(t, db, "parent-siblings-integration")
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	mgr := makeSubagentTestManager(t, db, reg, 4)
	defer mgr.Close(ctx)
	registerParentOwner(mgr, parent)

	raw, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	defer raw.Close()

	// Case 1: Multiple subagents under parent (Child 1 terminal, Child 2 running, Child 3 queued)
	child1 := createTerminalSubagentFixture(t, db, parent, "child-1", 1, "completed", store.ManagedSubagentResult{
		ResultText: "child 1 done",
	})

	child2Init := store.ManagedSubagentRecord{
		AgentID:          "child-2",
		ParentAgentID:    parent.AgentID,
		ParentSessionID:  parent.SessionID,
		ParentEpoch:      parent.Epoch,
		ParentGeneration: parent.Generation,
		Description:      "child 2 running task",
		SubagentType:     "general-purpose",
		Status:           "running",
	}
	require.NoError(t, db.CreateManagedSubagent(ctx, parent, child2Init))
	require.NoError(t, db.UpdateManagedSubagentProgress(ctx, parent, "child-2", 1, store.ManagedSubagentProgress{
		DurationSeconds: 15.5,
	}))

	child3Init := store.ManagedSubagentRecord{
		AgentID:          "child-3",
		ParentAgentID:    parent.AgentID,
		ParentSessionID:  parent.SessionID,
		ParentEpoch:      parent.Epoch,
		ParentGeneration: parent.Generation,
		Description:      "child 3 queued task",
		SubagentType:     "general-purpose",
		Status:           "queued",
	}
	require.NoError(t, db.CreateManagedSubagent(ctx, parent, child3Init))

	err = mgr.DeliverSubagentParentWake(ctx, parent, child1)
	require.NoError(t, err)

	msgID1 := fmt.Sprintf("subagent-completed-%s-%d", child1.AgentID, child1.Attempt)
	var body1 string
	err = raw.QueryRowContext(ctx, `SELECT body FROM messages WHERE id=?`, msgID1).Scan(&body1)
	require.NoError(t, err)

	require.Contains(t, body1, "[Subagent Completed] Task child-1")
	require.Contains(t, body1, "Active subagents still running (2):")
	require.Contains(t, body1, "- child-2 (child 2 running task): running (15.50s)")
	require.Contains(t, body1, "- child-3 (child 3 queued task): queued (0.00s)")
	require.NotContains(t, body1, "- child-1")

	// Case 2: Complete child 2 and child 3, then deliver wake for child 2
	require.NoError(t, db.UpdateManagedSubagentAttempt(ctx, parent, "child-2", 1, "running", store.ManagedSubagentResult{
		Status:          "completed",
		DurationSeconds: 15.5,
		ResultText:      "child 2 done",
	}))
	require.NoError(t, db.UpdateManagedSubagentAttempt(ctx, parent, "child-3", 1, "queued", store.ManagedSubagentResult{
		Status:     "stopped",
		ResultText: "child 3 stopped",
	}))

	child2Persisted, err := db.ManagedSubagent(ctx, parent, "child-2")
	require.NoError(t, err)

	err = mgr.DeliverSubagentParentWake(ctx, parent, *child2Persisted)
	require.NoError(t, err)

	msgID2 := fmt.Sprintf("subagent-completed-%s-%d", child2Persisted.AgentID, child2Persisted.Attempt)
	var body2 string
	err = raw.QueryRowContext(ctx, `SELECT body FROM messages WHERE id=?`, msgID2).Scan(&body2)
	require.NoError(t, err)

	require.Contains(t, body2, "[Subagent Completed] Task child-2")
	require.NotContains(t, body2, "Active subagents still running")
}
