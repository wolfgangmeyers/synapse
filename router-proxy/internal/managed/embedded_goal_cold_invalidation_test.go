package managed

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestEmbeddedColdReservedNotDoneInvalidatedBeforeDue(t *testing.T) {
	for _, tc := range []struct {
		name    string
		command store.EmbeddedGoalCommand
	}{
		{name: "goal off", command: store.EmbeddedGoalCommand{Action: "off"}},
		{name: "superseding revision", command: store.EmbeddedGoalCommand{Action: "set", Goal: "new goal", Settings: store.DefaultManagedGoalSettings()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			path := filepath.Join(root, "router.db")
			clock := deterministic.NewFixedClock(time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC))
			db, err := store.Open(ctx, path, store.Options{Clock: clock})
			require.NoError(t, err)
			manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
			require.NoError(t, err)
			reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
			defer reg.Unregister()
			blocked := make(chan struct{})
			var unblock sync.Once
			var starts atomic.Int32
			neutral := make(chan ai.UserMessage, 1)
			reg.SetResponses([]providers.FauxResponseStep{
				func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
					starts.Add(1)
					return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("goal", map[string]any{"action": "set", "goal": "original goal"}, "cold-set")}, ai.StopToolUse)
				},
				func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
					starts.Add(1)
					return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("goal_snooze", map[string]any{"duration_ms": 60000}, "cold-snooze")}, ai.StopToolUse)
				},
				func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
					starts.Add(1)
					<-blocked
					return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "initial final"}}, ai.StopStop)
				},
				func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
					starts.Add(1)
					var latest ai.UserMessage
					for _, msg := range h.Messages {
						if user, ok := msg.(ai.UserMessage); ok {
							latest = user
						}
					}
					neutral <- latest
					return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "neutral awareness final"}}, ai.StopStop)
				},
				func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
					starts.Add(1)
					return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "stale continuation"}}, ai.StopStop)
				},
			})
			newManager := func(s *store.Store) *Manager {
				m, e := NewManager(ManagerOptions{Store: s, Manifest: manifest, Clock: clock,
					Config:                Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}},
					CatalogSource:         CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
					EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() },
					CodexCredentialStore:  testfixture.CodexAdmission(t),
				})
				require.NoError(t, e)
				return m
			}
			m := newManager(db)
			setEmbeddedRecoveryGeneration(t, ctx, db, m)
			t.Cleanup(func() {
				unblock.Do(func() { close(blocked) })
				_ = m.Close(ctx)
				_ = db.Close()
			})
			launched, err := m.LaunchEmbeddedRoot(ctx, LaunchRequest{Name: "cold-invalidation", Cwd: root, Runtime: "embedded", Model: &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"}, InitialInstruction: "set and snooze", RequestID: "a5555555-5555-4555-8555-555555555594"})
			require.NoError(t, err)
			binding := store.EmbeddedBinding{AgentID: launched.Agent.AgentID, SessionID: launched.Agent.PiSessionID, Epoch: launched.Agent.SessionEpoch, Generation: launched.Agent.RuntimeGeneration}
			require.Eventually(t, func() bool {
				policy, e := db.ManagedGoalPolicyFor(ctx, binding.AgentID)
				return e == nil && policy.Config.Enabled && starts.Load() == 3
			}, 3*time.Second, 10*time.Millisecond, "setup failure: SDK did not set and snooze")
			unblock.Do(func() { close(blocked) })
			var saved store.EmbeddedGoalContinuation
			require.Eventually(t, func() bool {
				rows, e := db.ReservedEmbeddedGoalSnoozes(ctx, binding)
				if e != nil || len(rows) != 1 {
					return false
				}
				saved = rows[0]
				return saved.State == "reserved" && saved.DueAt.Valid && saved.GoalRevision == 1
			}, 3*time.Second, 10*time.Millisecond, "setup failure: cold NOT_DONE reservation absent")
			due, err := time.Parse("2006-01-02T15:04:05.000Z", saved.DueAt.String)
			require.NoError(t, err)
			require.Equal(t, clock.Now().Add(time.Minute), due)
			require.NoError(t, m.Close(ctx))
			require.NoError(t, db.Close())

			// Let the old owner lease expire without moving past the saved due.
			require.NoError(t, clock.Advance(embeddedOwnerTTL+time.Second))
			db, err = store.Open(ctx, path, store.Options{Clock: clock})
			require.NoError(t, err)
			m = newManager(db)
			setEmbeddedRecoveryGeneration(t, ctx, db, m)
			require.NoError(t, m.RecoverEmbeddedRoots(ctx))
			require.Eventually(t, func() bool {
				m.mu.Lock()
				defer m.mu.Unlock()
				owner, owned := m.embeddedOwners[binding.AgentID]
				timer := m.embeddedGoalTimers[binding.AgentID]
				return owned && owner.binding.OwnerToken > 0 && timer != nil && timer.cancel != nil
			}, 3*time.Second, 10*time.Millisecond, "setup failure: new Manager did not claim root and register cold due timer")
			// Recovery awareness is a separate claimed wake, not the saved goal
			// continuation. Ready/owner publication follows its checked completion.
			select {
			case user := <-neutral:
				require.Equal(t, ai.ContentList{ai.TextContent{Text: "Continue working from your saved context."}}, user.Content)
			case <-time.After(3 * time.Second):
				t.Fatal("setup failure: neutral recovery awareness absent")
			}
			ro, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
			require.NoError(t, err)
			defer ro.Close()
			var kind, wakeState string
			var finalBody []byte
			require.NoError(t, ro.QueryRowContext(ctx, `SELECT w.kind,w.state,e.body FROM embedded_restart_wake_slots w JOIN embedded_restart_wake_checked_origins o ON o.agent_id=w.agent_id AND o.slot=w.slot AND o.wake_id=w.wake_id AND o.process_generation=w.process_generation JOIN embedded_entries e ON e.agent_id=o.agent_id AND e.session_id=o.session_id AND e.epoch=o.epoch AND e.generation=o.runtime_generation AND e.seq=o.seq WHERE w.agent_id=? AND w.session_id=? AND w.epoch=? AND w.runtime_generation=? AND w.slot='current'`, binding.AgentID, binding.SessionID, binding.Epoch, binding.Generation).Scan(&kind, &wakeState, &finalBody))
			require.Equal(t, "root-idle", kind)
			require.Equal(t, "checked-complete", wakeState)
			var awarenessFinal ai.AssistantMessage
			require.NoError(t, json.Unmarshal(finalBody, &awarenessFinal))
			require.Equal(t, ai.ContentList{ai.TextContent{Text: "neutral awareness final"}}, awarenessFinal.Content)
			require.Equal(t, ai.StopStop, awarenessFinal.StopReason)
			require.EqualValues(t, 4, starts.Load(), "only the neutral awareness may run before invalidation")
			reserved, err := db.EmbeddedGoalContinuationFor(ctx, binding, saved.EmbeddedGoalEvaluationKey)
			require.NoError(t, err)
			require.Equal(t, "reserved", reserved.State)
			require.Equal(t, "pending", reserved.Disposition)
			require.Empty(t, reserved.StartedAt)
			m.mu.Lock()
			ownerBinding := m.embeddedOwners[binding.AgentID].binding
			m.mu.Unlock()
			policy, replay, err := db.ApplyEmbeddedGoalCall(ctx, ownerBinding, "invalidate-"+tc.name, tc.command)
			require.NoError(t, err)
			require.False(t, replay)
			require.EqualValues(t, 2, policy.DesiredRevision)
			require.Equal(t, tc.command.Action == "set", policy.Config.Enabled)
			require.NoError(t, clock.Advance(due.Sub(clock.Now())-time.Millisecond))
			require.EqualValues(t, 4, starts.Load(), "unexpected provider after neutral awareness and before due")
			before, err := db.EmbeddedGoalContinuationFor(ctx, ownerBinding, saved.EmbeddedGoalEvaluationKey)
			require.NoError(t, err)
			require.Equal(t, "invalidated", before.State)
			require.Equal(t, "invalidated", before.Disposition)
			require.Empty(t, before.StartedAt, "off/revision must invalidate an unstarted continuation before due")
			require.NoError(t, clock.Advance(2*time.Millisecond))
			require.Eventually(t, func() bool {
				row, e := db.EmbeddedGoalContinuationFor(ctx, ownerBinding, saved.EmbeddedGoalEvaluationKey)
				return e == nil && row.State == "invalidated" && row.Disposition == "invalidated"
			}, 3*time.Second, 10*time.Millisecond, "stale reservation not invalidated after due")
			require.EqualValues(t, 4, starts.Load(), "stale continuation reached provider after neutral awareness")
			final, err := db.EmbeddedGoalContinuationFor(ctx, ownerBinding, saved.EmbeddedGoalEvaluationKey)
			require.NoError(t, err)
			require.Empty(t, final.StartedAt)
		})
	}
}
