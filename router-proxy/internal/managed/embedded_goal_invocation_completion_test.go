package managed

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

func completionFixture(t *testing.T) (*store.Store, store.EmbeddedBinding, string) {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0700))
	cwd := filepath.Join(root, "project")
	require.NoError(t, os.Mkdir(cwd, 0700))
	path := filepath.Join(root, "completion.db")
	clock := deterministic.NewFixedClock(time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC))
	db, err := store.Open(ctx, path, store.Options{Clock: clock})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	agent := store.ManagedAgentRecord{AgentID: "completion-agent", Name: "completion-agent", Cwd: cwd,
		SelectedModel: store.ManagedModel{Provider: "openai-codex", ID: "gpt-5"}, Runtime: "embedded",
		PiSessionID: "completion-session", SessionEpoch: 1, RuntimeGeneration: "completion-runtime",
		ManifestRevision: manifest.Revision, ManifestHash: manifest.CanonicalSHA256}
	op := store.ManagedOperation{OperationID: "completion-launch", Method: "POST", CanonicalPath: "/v1/managed-agents",
		RequestID: "a5555555-5555-4555-8555-555555555590", CanonicalBodyHash: fmt.Sprintf("%x", sha256.Sum256([]byte(`{}`))),
		ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
	// Reservation creates the history boundary and disabled goal policy through
	// production APIs. A raw managed_agents insert does not establish either.
	require.NoError(t, db.ReserveManagedEmbeddedLaunch(ctx, agent, op, 1, "root"))
	require.NoError(t, db.UpdateManagedAgentStarting(ctx, agent.AgentID, agent.PiSessionID,
		store.ProcessIdentityRecord{PID: 321, StartMarker: "completion-start", Incarnation: "completion-process"}, agent.RuntimeGeneration))
	b := store.EmbeddedBinding{AgentID: agent.AgentID, SessionID: agent.PiSessionID, Epoch: agent.SessionEpoch, Generation: agent.RuntimeGeneration}
	b.OwnerToken, err = db.AcquireEmbeddedOwner(ctx, b, "wake-fixture", time.Minute)
	require.NoError(t, err)
	require.NoError(t, db.ReadyEmbeddedRoot(ctx, b, op.OperationID))
	policy, err := db.ManagedGoalPolicyFor(ctx, b.AgentID)
	require.NoError(t, err)
	require.Zero(t, policy.DesiredRevision)
	require.False(t, policy.Config.Enabled)
	saved, err := db.ManagedAgent(ctx, b.AgentID)
	require.NoError(t, err)
	require.Equal(t, "running", saved.LifecycleState)
	require.Equal(t, agent.RuntimeGeneration, saved.RuntimeGeneration)
	require.Equal(t, manifest.CanonicalSHA256, saved.ManifestHash)
	return db, b, path
}

// Real ordered persistence and Host Execute, but no network or active database.
func TestEmbeddedGoalInvocationSDKCompletionObserved(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0700))
	path := filepath.Join(root, "completion.db")
	db, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	defer db.Close()
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()
	var ordinary, evaluations atomic.Int32
	respond := func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		if embeddedIsGoalEvaluation(h) {
			evaluations.Add(1)
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "DONE"}}, ai.StopStop)
		}
		switch ordinary.Add(1) {
		case 1:
			return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("goal", map[string]any{"action": "status"}, "current-status-before")}, ai.StopToolUse)
		case 2:
			return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("goal", map[string]any{"action": "set", "goal": "finish current work"}, "current-set")}, ai.StopToolUse)
		case 3:
			return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("goal_snooze", map[string]any{"duration_ms": 1000}, "current-snooze")}, ai.StopToolUse)
		case 4:
			return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("goal", map[string]any{"action": "status"}, "current-status-after")}, ai.StopToolUse)
		default:
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "ordinary final"}}, ai.StopStop)
		}
	}
	reg.SetResponses([]providers.FauxResponseStep{respond, respond, respond, respond, respond, respond})
	// Real constructor, pinned Codex identity, private credential callback and
	// joined lifecycle. No evaluator injection or completion-authority substitute.
	m := productionPolicyManager(t, db, root, reg)
	defer m.Close(ctx) // Must join before provider registration and Store cleanup.
	hostEntries, domainCommits := 0, 0
	m.embeddedAfterIntent = func(context.Context) { hostEntries++ }
	m.embeddedAfterGoalCommit = func(context.Context) { domainCommits++ }
	launched, err := m.LaunchEmbeddedRoot(ctx, LaunchRequest{Name: "completion-agent", Cwd: root, Runtime: "embedded", Model: &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"}, InitialInstruction: "do current work", RequestID: "a5555555-5555-4555-8555-555555555590"})
	require.NoError(t, err)
	read, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro")
	require.NoError(t, err)
	defer read.Close()
	require.Eventually(t, func() bool {
		var verdict string
		return read.QueryRow(`SELECT state FROM embedded_goal_evaluations WHERE agent_id=?`, launched.Agent.AgentID).Scan(&verdict) == nil && verdict == "DONE"
	}, 3*time.Second, 10*time.Millisecond)
	m.mu.Lock()
	owner := m.embeddedOwners[launched.Agent.AgentID]
	m.mu.Unlock()
	b := owner.binding
	require.NoError(t, m.Close(ctx))
	require.EqualValues(t, 5, ordinary.Load())
	require.EqualValues(t, 1, evaluations.Load())
	require.Equal(t, 4, hostEntries, "both status reads and both mutations must reach Host Execute")
	require.Equal(t, 2, domainCommits, "both mutations must reach the domain commit hook")
	completionAssertDomain(t, b, path, "goal", "current-set", true)
	completionAssertDomain(t, b, path, "goal_snooze", "current-snooze", true)
	snap, err := db.ReopenEmbedded(ctx, b)
	require.NoError(t, err)
	for _, id := range []string{"current-status-before", "current-status-after"} {
		_, err := db.RecoverEmbeddedEffect(ctx, b, id)
		require.ErrorIs(t, err, sql.ErrNoRows, "read-only status must not poison continuation with UNKNOWN")
	}
	for _, id := range []string{"current-set", "current-snooze"} {
		effect, err := db.RecoverEmbeddedEffect(ctx, b, id)
		require.NoError(t, err)
		require.Equal(t, "observed", effect.State)
		matches := 0
		for _, entry := range snap.Entries {
			if bytes.Equal(entry.Body, effect.Result) {
				matches++
			}
		}
		require.Equal(t, 1, matches, "observed bytes must be the actual checked SDK entry")
		message, err := ai.UnmarshalMessage(effect.Result)
		require.NoError(t, err)
		result, ok := message.(ai.ToolResultMessage)
		require.True(t, ok)
		require.Equal(t, id, result.ToolCallID)
		require.False(t, result.IsError)
		require.Positive(t, result.Timestamp)
	}
}

func TestEmbeddedGoalInvocationCompletionBoundaries(t *testing.T) {
	for _, name := range []string{"goal", "goal_snooze", "workflow_snooze"} {
		for _, fault := range []string{"success-restart", "before-domain", "after-domain", "before-result", "after-result", "stale-owner-append", "stale-process-append", "stale-owner-observe", "stale-process-observe", "wrong-content", "wrong-name", "wrong-id", "error-result", "wrong-entry-observe"} {
			if name == "workflow_snooze" && fault != "after-domain" {
				continue
			}
			t.Run(name+"/"+fault, func(t *testing.T) {
				ctx := context.Background()
				db, b, path := completionFixture(t)
				g, err := db.MintEmbeddedProcessGeneration(ctx, "current-completion")
				require.NoError(t, err)
				userBody, err := json.Marshal(ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "current ordinary user"}}})
				require.NoError(t, err)
				user, err := db.AppendEmbeddedEntry(ctx, b, "user", userBody)
				require.NoError(t, err)
				require.NoError(t, db.EnterEmbeddedNonMailProviderTurn(ctx, b, user.Sequence, user.Digest))
				args := map[string]any{"action": "set", "goal": "current goal"}
				if name == "goal_snooze" || name == "workflow_snooze" {
					saved, err := db.ManagedAgent(ctx, b.AgentID)
					require.NoError(t, err)
					fence := store.ManagedRuntimeFence{AgentID: saved.AgentID, PiSessionID: saved.PiSessionID, SessionEpoch: saved.SessionEpoch,
						ProcessPID: saved.ProcessPID, ProcessStartMarker: saved.ProcessStartMarker, ProcessIncarnation: saved.ProcessIncarnation,
						RuntimeGeneration: saved.RuntimeGeneration, ManifestRevision: saved.ManifestRevision, ManifestHash: saved.ManifestHash}
					policy, err := db.UpdateManagedGoalPolicy(ctx, fence, store.ManagedGoalConfig{Enabled: true, Goal: "current goal", Settings: store.DefaultManagedGoalSettings()})
					require.NoError(t, err)
					require.EqualValues(t, 1, policy.DesiredRevision)
					if name == "workflow_snooze" {
						args = map[string]any{"justification": "Waiting for CI build pipeline", "duration_ms": 1000}
					} else {
						args = map[string]any{"duration_ms": 1000}
					}
				}
				if name == "workflow_snooze" {
					wf, err := db.CreateWorkflow(ctx, store.WorkflowRequest{Name: "Completion"})
					require.NoError(t, err)
					state, err := db.CreateWorkflowState(ctx, store.WorkflowStateRequest{Name: "Work", WorkflowID: &wf.ID})
					require.NoError(t, err)
					require.NoError(t, db.SetManagedAgentActiveWorkflowWithVariables(ctx, b.AgentID, &wf.ID, &state.ID, nil))
				}
				intent, err := json.Marshal(map[string]any{"id": "current-call", "name": name, "arguments": args})
				require.NoError(t, err)
				w, err := db.RecordEmbeddedGoalInvocation(ctx, b, g, user.Sequence, user.Digest, "", "current-call", intent)
				require.NoError(t, err)
				require.Error(t, db.CompleteEmbeddedGoalInvocation(ctx, w, ai.ToolResultMessage{ToolCallID: "current-call", ToolName: name, Content: ai.ContentList{ai.TextContent{Text: "copied DOMAIN text"}}}), "admission without actual Host Execute cannot certify a result")
				m := &Manager{store: db}
				executeCtx, cancel := context.WithCancel(ctx)
				defer cancel()
				beforeDomain, afterDomain := false, false
				m.embeddedAfterIntent = func(context.Context) {
					beforeDomain = true
					if fault == "before-domain" {
						cancel()
					}
				}
				m.embeddedAfterGoalCommit = func(context.Context) {
					afterDomain = true
					if fault == "after-domain" {
						cancel()
					}
				}
				var expected *ai.ToolResultMessage
				admitted := func(string) *store.EmbeddedGoalInvocation { current := w; w = nil; return current }
				var executed *store.EmbeddedGoalInvocation
				completed := func(_ string, invocation *store.EmbeddedGoalInvocation, result ai.ToolResultMessage) {
					executed = invocation
					expected = &result
				}
				tool := m.embeddedGoalTool(embeddedOwner{binding: b, identity: "wake-fixture"}, admitted, completed)
				if name == "goal_snooze" || name == "workflow_snooze" {
					tool = m.embeddedGoalSnoozeTool(embeddedOwner{binding: b, identity: "wake-fixture"}, func() store.EmbeddedGoalSnoozeTurn {
						return store.EmbeddedGoalSnoozeTurn{UserSeq: user.Sequence, UserDigest: user.Digest}
					}, admitted, completed)
				}
				if name == "workflow_snooze" {
					tool = m.embeddedWorkflowSnoozeTool(embeddedOwner{binding: b, identity: "wake-fixture"}, func() store.EmbeddedGoalSnoozeTurn {
						return store.EmbeddedGoalSnoozeTurn{UserSeq: user.Sequence, UserDigest: user.Digest}
					}, admitted, completed)
				}
				_, err = tool.Execute(executeCtx, "current-call", args, nil)
				require.True(t, beforeDomain, "Host Execute must reach its after-intent hook")
				if fault == "before-domain" || fault == "after-domain" {
					require.Error(t, err)
					require.ErrorIs(t, executeCtx.Err(), context.Canceled, "selected hook must cancel this Execute")
					require.Equal(t, fault == "after-domain", afterDomain, "only the committed domain transaction reaches the after-goal-commit hook")
					completionAssertDomain(t, b, path, name, "current-call", fault == "after-domain")
					require.Nil(t, expected)
					completionAssertUnknown(t, db, b, path, user.Sequence)
					if name == "workflow_snooze" && fault == "after-domain" {
						raw, err := sql.Open("sqlite", path)
						require.NoError(t, err)
						defer raw.Close()
						var due, policyDue string
						require.NoError(t, raw.QueryRow(`SELECT due_at FROM embedded_goal_snooze_preferences WHERE agent_id=?`, b.AgentID).Scan(&due))
						require.Equal(t, "2030-01-02T03:04:06.000Z", due)
						require.NoError(t, raw.QueryRow(`SELECT snoozed_until FROM managed_agent_policies WHERE agent_id=?`, b.AgentID).Scan(&policyDue))
						require.Equal(t, due, policyDue)
						effect, err := db.RecoverEmbeddedEffect(ctx, b, "current-call")
						require.NoError(t, err)
						require.Equal(t, "unknown", effect.State)
						// A fresh Execute with no newly admitted witness cannot replay.
						_, err = tool.Execute(ctx, "current-call", args, nil)
						require.Error(t, err)
						require.Nil(t, expected)
						require.NoError(t, raw.QueryRow(`SELECT snoozed_until FROM managed_agent_policies WHERE agent_id=?`, b.AgentID).Scan(&policyDue))
						require.Equal(t, due, policyDue)
						completionAssertUnknown(t, db, b, path, user.Sequence)
					}
					_, err = db.RecordEmbeddedGoalInvocation(ctx, b, g, user.Sequence, user.Digest, "", "current-call", intent)
					require.Error(t, err, "saved domain or intent must not mint completion")
					if name == "workflow_snooze" {
						recoveredGeneration, err := db.MintEmbeddedProcessGeneration(ctx, "recovered-workflow-completion")
						require.NoError(t, err)
						_, err = db.RecordEmbeddedGoalInvocation(ctx, b, recoveredGeneration, user.Sequence, user.Digest, "", "current-call", intent)
						require.Error(t, err, "new process generation cannot replay uncertain committed snooze")
						completionAssertUnknown(t, db, b, path, user.Sequence)
					}
					return
				}
				require.NoError(t, err)
				require.True(t, afterDomain, "successful Host Execute must cross the domain commit hook")
				completionAssertDomain(t, b, path, name, "current-call", true)
				require.NotNil(t, executed)
				require.NotNil(t, expected)
				result := *expected
				result.Timestamp = 12345 // SDK envelope metadata is not Host expected content.
				if fault == "wrong-content" {
					result.Content = ai.ContentList{ai.TextContent{Text: "different"}}
				}
				if fault == "wrong-name" {
					result.ToolName = "bash"
				}
				if fault == "wrong-id" {
					result.ToolCallID = "historical"
				}
				if fault == "error-result" {
					result.IsError = true
				}
				body, err := json.Marshal(result)
				require.NoError(t, err)
				invalidateOwner := func() {
					raw, err := sql.Open("sqlite", path)
					require.NoError(t, err)
					_, err = raw.Exec(`UPDATE embedded_sessions SET owner_token=owner_token+1 WHERE agent_id=?`, b.AgentID)
					require.NoError(t, err)
					require.NoError(t, raw.Close())
				}
				advanceProcess := func() {
					_, err := db.MintEmbeddedProcessGeneration(ctx, "new-current-process")
					require.NoError(t, err)
				}
				if fault == "stale-owner-append" {
					invalidateOwner()
				}
				if fault == "stale-process-append" {
					advanceProcess()
				}
				appendCtx := ctx
				if fault == "before-result" {
					cancel()
					appendCtx = executeCtx
				}
				entry, err := db.AppendEmbeddedGoalInvocationResult(appendCtx, executed, user.Sequence, user.Digest, "current-result", result, body)
				if fault == "before-result" || fault == "stale-owner-append" || fault == "stale-process-append" || fault == "wrong-content" || fault == "wrong-name" || fault == "wrong-id" || fault == "error-result" {
					require.Error(t, err)
					completionAssertUnknown(t, db, b, path, user.Sequence)
					return
				}
				require.NoError(t, err)
				require.Equal(t, body, entry.Body, "checked append must contain the exact supplied SDK envelope")
				require.Equal(t, string(ai.RoleToolResult), entry.Kind)
				snapshot, err := db.ReopenEmbedded(ctx, b)
				require.NoError(t, err)
				require.Equal(t, entry, snapshot.Entries[len(snapshot.Entries)-1], "observation must follow a genuinely persisted checked entry")
				completionAssertUnknown(t, db, b, path, entry.Sequence)
				if fault == "stale-owner-observe" {
					invalidateOwner()
				}
				if fault == "stale-process-observe" {
					advanceProcess()
				}
				observeCtx := ctx
				if fault == "wrong-entry-observe" {
					entry.Body = append(bytes.Clone(entry.Body), ' ')
				}
				if fault == "after-result" {
					cancel()
					observeCtx = executeCtx
				}
				err = db.ObserveEmbeddedGoalInvocation(observeCtx, executed, entry)
				if fault != "success-restart" {
					require.Error(t, err)
					completionAssertUnknown(t, db, b, path, entry.Sequence)
					require.Error(t, db.ObserveEmbeddedGoalInvocation(ctx, executed, entry), "failed observation cannot be replayed")
					return
				}
				require.NoError(t, err)
				cold, err := store.Open(ctx, path, store.Options{Clock: deterministic.NewFixedClock(time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC))})
				require.NoError(t, err)
				defer cold.Close()
				_, err = cold.MintEmbeddedProcessGeneration(ctx, "new-current-process")
				require.NoError(t, err)
				effect, err := cold.RecoverEmbeddedEffect(ctx, b, "current-call")
				require.NoError(t, err)
				require.Equal(t, "observed", effect.State)
				require.Equal(t, entry.Body, effect.Result)
				require.Error(t, db.ObserveEmbeddedGoalInvocation(ctx, executed, entry))
				_, err = db.RecordEmbeddedGoalInvocation(ctx, b, g+1, user.Sequence, user.Digest, "", "current-call", intent)
				require.Error(t, err)
			})
		}
	}
}

func completionAssertDomain(t *testing.T, b store.EmbeddedBinding, path, name, id string, committed bool) {
	t.Helper()
	raw, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro")
	require.NoError(t, err)
	defer raw.Close()
	table, complete := "embedded_goal_calls", " AND state='complete'"
	if name == "goal_snooze" || name == "workflow_snooze" {
		table, complete = "embedded_goal_snooze_calls", ""
	}
	var rows int
	require.NoError(t, raw.QueryRow(`SELECT count(*) FROM `+table+` WHERE call_id=? AND agent_id=? AND session_id=? AND epoch=? AND generation=? AND role='root' AND json_valid(result_json)=1`+complete,
		id, b.AgentID, b.SessionID, b.Epoch, b.Generation).Scan(&rows))
	if committed {
		require.Equal(t, 1, rows, "selected domain API must have committed its exact bound result")
	} else {
		require.Zero(t, rows)
		require.NoError(t, raw.QueryRow(`SELECT count(*) FROM `+table+` WHERE call_id=?`, id).Scan(&rows))
		require.Zero(t, rows, "before-domain fault must leave no domain call row")
	}
}

func completionAssertUnknown(t *testing.T, _ *store.Store, b store.EmbeddedBinding, path string, head int64) {
	t.Helper()
	raw, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro")
	require.NoError(t, err)
	defer raw.Close()
	var state string
	var result []byte
	require.NoError(t, raw.QueryRow(`SELECT state,result FROM embedded_effects WHERE effect_id='current-call'`).Scan(&state, &result))
	require.Equal(t, "unknown", state)
	require.Empty(t, result)
	var actual int64
	require.NoError(t, raw.QueryRow(`SELECT max(seq) FROM embedded_entries WHERE agent_id=?`, b.AgentID).Scan(&actual))
	require.Equal(t, head, actual)
}

func TestEmbeddedGoalInvocationProcessFencesAdmissionAndExecute(t *testing.T) {
	for _, stage := range []string{"zero-process", "admission", "execute"} {
		t.Run(stage, func(t *testing.T) {
			ctx := context.Background()
			db, b, path := completionFixture(t)
			g, err := db.MintEmbeddedProcessGeneration(ctx, "original-process")
			require.NoError(t, err)
			body, err := json.Marshal(ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "current user"}}})
			require.NoError(t, err)
			user, err := db.AppendEmbeddedEntry(ctx, b, "user", body)
			require.NoError(t, err)
			require.NoError(t, db.EnterEmbeddedNonMailProviderTurn(ctx, b, user.Sequence, user.Digest))
			args := map[string]any{"action": "set", "goal": "must not commit"}
			intent, err := json.Marshal(map[string]any{"id": "current-call", "name": "goal", "arguments": args})
			require.NoError(t, err)
			if stage == "admission" {
				_, err = db.MintEmbeddedProcessGeneration(ctx, "new-process")
				require.NoError(t, err)
			}
			if stage == "zero-process" {
				g = 0
			}
			w, err := db.RecordEmbeddedGoalInvocation(ctx, b, g, user.Sequence, user.Digest, "", "current-call", intent)
			if stage != "execute" {
				require.Error(t, err)
				require.Nil(t, w)
				_, err = db.RecoverEmbeddedEffect(ctx, b, "current-call")
				require.ErrorIs(t, err, sql.ErrNoRows)
				return
			}
			require.NoError(t, err)
			_, err = db.MintEmbeddedProcessGeneration(ctx, "new-process")
			require.NoError(t, err)
			m := &Manager{store: db}
			attempted := false
			m.embeddedAfterIntent = func(context.Context) { attempted = true }
			completed := false
			tool := m.embeddedGoalTool(embeddedOwner{binding: b, identity: "wake-fixture"}, func(string) *store.EmbeddedGoalInvocation { return w }, func(string, *store.EmbeddedGoalInvocation, ai.ToolResultMessage) { completed = true })
			_, err = tool.Execute(ctx, "current-call", args, nil)
			require.Error(t, err)
			require.True(t, attempted, "stale-process Execute denial must reach the actual Host boundary")
			require.False(t, completed)
			completionAssertUnknown(t, db, b, path, user.Sequence)
			raw, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro")
			require.NoError(t, err)
			defer raw.Close()
			var calls int
			require.NoError(t, raw.QueryRow(`SELECT count(*) FROM embedded_goal_calls WHERE call_id='current-call'`).Scan(&calls))
			require.Zero(t, calls, "old process must not reach the domain transaction")
		})
	}
}

// Exercise the window AFTER successful Execute admission and BEFORE the
// actual DOMAIN immediate transaction, not the earlier Check-only denial.
func TestEmbeddedGoalInvocationDomainProcessFence(t *testing.T) {
	for _, action := range []string{"set", "off", "snooze"} {
		t.Run(action, func(t *testing.T) {
			ctx := context.Background()
			db, b, path := completionFixture(t)
			g, err := db.MintEmbeddedProcessGeneration(ctx, "domain-original-process")
			require.NoError(t, err)
			saved, err := db.ManagedAgent(ctx, b.AgentID)
			require.NoError(t, err)
			fence := store.ManagedRuntimeFence{AgentID: saved.AgentID, PiSessionID: saved.PiSessionID, SessionEpoch: saved.SessionEpoch,
				ProcessPID: saved.ProcessPID, ProcessStartMarker: saved.ProcessStartMarker, ProcessIncarnation: saved.ProcessIncarnation,
				RuntimeGeneration: saved.RuntimeGeneration, ManifestRevision: saved.ManifestRevision, ManifestHash: saved.ManifestHash}
			_, err = db.UpdateManagedGoalPolicy(ctx, fence, store.ManagedGoalConfig{Enabled: true, Goal: "unchanged goal", Settings: store.DefaultManagedGoalSettings()})
			require.NoError(t, err)
			userBody, err := json.Marshal(ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "current domain user"}}})
			require.NoError(t, err)
			user, err := db.AppendEmbeddedEntry(ctx, b, "user", userBody)
			require.NoError(t, err)
			require.NoError(t, db.EnterEmbeddedNonMailProviderTurn(ctx, b, user.Sequence, user.Digest))
			name := "goal"
			args := map[string]any{"action": action}
			if action == "set" {
				args["goal"] = "must not replace goal"
			}
			if action == "snooze" {
				name, args = "goal_snooze", map[string]any{"duration_ms": 1000}
			}
			intent, err := json.Marshal(map[string]any{"id": "current-call", "name": name, "arguments": args})
			require.NoError(t, err)
			invocation, err := db.RecordEmbeddedGoalInvocation(ctx, b, g, user.Sequence, user.Digest, "", "current-call", intent)
			require.NoError(t, err)
			require.NoError(t, db.CheckEmbeddedGoalInvocation(ctx, invocation), "original process must pass the actual Execute admission seam before Mint")
			beforePolicy, err := db.ManagedGoalPolicyFor(ctx, b.AgentID)
			require.NoError(t, err)
			beforeProjection, err := db.GetProjectionVersion(ctx)
			require.NoError(t, err)
			raw, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro")
			require.NoError(t, err)
			defer raw.Close()
			counts := func() []int64 {
				var out []int64
				for _, table := range []string{"embedded_goal_calls", "embedded_goal_snooze_calls", "embedded_goal_snooze_preferences", "embedded_goal_applied_receipts", "embedded_goal_continuations", "reminders", "embedded_entries"} {
					var count int64
					require.NoError(t, raw.QueryRow(`SELECT count(*) FROM `+table).Scan(&count))
					out = append(out, count)
				}
				return out
			}
			beforeCounts := counts()
			next, err := db.MintEmbeddedProcessGeneration(ctx, "domain-new-process")
			require.NoError(t, err)
			require.Greater(t, next, g)
			require.NoError(t, db.RenewEmbeddedOwner(ctx, b, "wake-fixture", time.Minute), "owner token must remain live: denial must be the DOMAIN process guard")
			if action == "snooze" {
				rawArgs, argsErr := embeddedGoalSnoozeArguments(args)
				require.NoError(t, argsErr)
				preference, replay, domainErr := db.SaveCurrentEmbeddedGoalSnoozePreference(ctx, b,
					store.EmbeddedGoalSnoozeTurn{UserSeq: user.Sequence, UserDigest: user.Digest, GoalRevision: beforePolicy.DesiredRevision}, "current-call", rawArgs, invocation)
				require.Zero(t, preference)
				require.False(t, replay)
				err = domainErr
			} else {
				command, commandErr := embeddedGoalCommand(args)
				require.NoError(t, commandErr)
				policy, replay, domainErr := db.ApplyCurrentEmbeddedGoalCall(ctx, b, "current-call", command, invocation)
				require.Nil(t, policy)
				require.False(t, replay)
				err = domainErr
			}
			var denial *store.EmbeddedGoalInvocationDomainDenied
			require.ErrorAs(t, err, &denial, "must reach the DOMAIN transaction guard, not merely fail an earlier Execute Check")
			require.EqualError(t, denial.Cause, "stale goal invocation process")
			completionAssertDomain(t, b, path, name, "current-call", false)
			rejected := ai.ToolResultMessage{ToolCallID: "current-call", ToolName: name, Content: ai.ContentList{ai.TextContent{Text: "no committed domain result"}}, Timestamp: 12345}
			require.Error(t, db.CompleteEmbeddedGoalInvocation(ctx, invocation, rejected), "DOMAIN denial cannot yield successful current completion")
			body, err := json.Marshal(rejected)
			require.NoError(t, err)
			_, err = db.AppendEmbeddedGoalInvocationResult(ctx, invocation, user.Sequence, user.Digest, "denied-result", rejected, body)
			require.Error(t, err)
			require.Error(t, db.ObserveEmbeddedGoalInvocation(ctx, invocation, store.EmbeddedEntry{}))
			completionAssertUnknown(t, db, b, path, user.Sequence)
			afterPolicy, err := db.ManagedGoalPolicyFor(ctx, b.AgentID)
			require.NoError(t, err)
			require.Equal(t, beforePolicy, afterPolicy)
			afterProjection, err := db.GetProjectionVersion(ctx)
			require.NoError(t, err)
			require.Equal(t, beforeProjection, afterProjection)
			require.Equal(t, beforeCounts, counts(), "no domain receipt, preference, continuation, reminder or checked SDK entry may be written")
			_, err = db.RecordEmbeddedGoalInvocation(ctx, b, next, user.Sequence, user.Digest, "", "current-call", intent)
			require.Error(t, err, "UNKNOWN original intent must remain non-replayable in the new process")
		})
	}
}

func TestEmbeddedGoalInvocationCannotMintHistoricalOrExternalCompletion(t *testing.T) {
	ctx := context.Background()
	db, b, path := completionFixture(t)
	g, err := db.MintEmbeddedProcessGeneration(ctx, "current-admission")
	require.NoError(t, err)
	body, err := json.Marshal(ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "current user"}}})
	require.NoError(t, err)
	user, err := db.AppendEmbeddedEntry(ctx, b, "user", body)
	require.NoError(t, err)
	require.NoError(t, db.EnterEmbeddedNonMailProviderTurn(ctx, b, user.Sequence, user.Digest))
	args := map[string]any{"action": "set", "goal": "old domain"}
	intent, err := json.Marshal(map[string]any{"id": "historical", "name": "goal", "arguments": args})
	require.NoError(t, err)
	historicalAdmission, err := db.RecordEmbeddedGoalInvocation(ctx, b, g, user.Sequence, user.Digest, "", "historical", intent)
	require.NoError(t, err)
	m := &Manager{store: db}
	domainCommitted, hostCompleted := false, false
	m.embeddedAfterGoalCommit = func(context.Context) { domainCommitted = true }
	tool := m.embeddedGoalTool(embeddedOwner{binding: b, identity: "wake-fixture"},
		func(string) *store.EmbeddedGoalInvocation { return historicalAdmission },
		func(_ string, invocation *store.EmbeddedGoalInvocation, result ai.ToolResultMessage) {
			require.Same(t, historicalAdmission, invocation)
			require.Equal(t, "historical", result.ToolCallID)
			require.False(t, result.IsError)
			hostCompleted = true
		})
	_, err = tool.Execute(ctx, "historical", args, nil)
	require.NoError(t, err)
	require.True(t, domainCommitted)
	require.True(t, hostCompleted)
	completionAssertDomain(t, b, path, "goal", "historical", true)
	// Lose current memory before any checked SDK result, as on restart. The
	// saved DOMAIN and UNKNOWN intent cannot reconstruct this admission.
	historicalAdmission = nil
	g, err = db.MintEmbeddedProcessGeneration(ctx, "reopened-admission")
	require.NoError(t, err)
	w, err := db.RecordEmbeddedGoalInvocation(ctx, b, g, user.Sequence, user.Digest, "", "historical", intent)
	require.Error(t, err)
	require.Nil(t, w)
	effect, err := db.RecoverEmbeddedEffect(ctx, b, "historical")
	require.NoError(t, err)
	require.Equal(t, "unknown", effect.State)
	external, err := json.Marshal(map[string]any{"id": "external", "name": "bash", "arguments": map[string]any{"command": "true"}})
	require.NoError(t, err)
	require.NoError(t, db.RecordEmbeddedEffectIntent(ctx, b, "external", external))
	w, err = db.RecordEmbeddedGoalInvocation(ctx, b, g, user.Sequence, user.Digest, "", "external", external)
	require.Error(t, err)
	require.Nil(t, w)
	effect, err = db.RecoverEmbeddedEffect(ctx, b, "external")
	require.NoError(t, err)
	require.Equal(t, "unknown", effect.State)
}
