package managed

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

// One saved SDK create, then the real owner and termination boundaries. The
// first failed assertion is the RED; later lifecycle checks must not execute.
func TestEmbeddedOwnReminderPendingTerminationLifecycleRED(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "router.db")
	clock := deterministic.NewFixedClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	due := clock.Now().Add(10 * time.Minute)
	const callID = "pending-own-create"
	const unknownID = "unresolved-own-create"
	create := map[string]any{"action": "create", "body": "pending own reminder", "due_at": due.Format("2006-01-02T15:04:05.000Z")}
	aDB, err := store.Open(ctx, path, store.Options{Clock: clock})
	require.NoError(t, err)
	defer aDB.Close()
	bDB, err := store.Open(ctx, path, store.Options{Clock: clock})
	require.NoError(t, err)
	defer bDB.Close()
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()
	seen := make(chan reminderSDKObservation, 1)
	reg.SetResponses([]providers.FauxResponseStep{
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("mecha_reminder", create, callID)}, ai.StopToolUse)
		},
		func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			seen <- reminderSDKResult(h, callID)
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "done"}}, ai.StopStop)
		},
	})
	options := func(db *store.Store, fail *bool) ManagerOptions {
		o := ManagerOptions{Store: db, Manifest: manifest,
			Config:                Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}},
			CatalogSource:         CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
			EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() },
			CodexCredentialStore:  testfixture.CodexAdmission(t),
		}
		if fail != nil {
			o.BeforeManagedTerminationArtifactCleanup = func(context.Context, string) error {
				if *fail {
					return errors.New("hold authoritative delete")
				}
				return nil
			}
		}
		return o
	}
	a, err := NewManager(options(aDB, nil))
	require.NoError(t, err)
	defer a.Close(ctx)
	launched, err := a.Launch(ctx, LaunchRequest{Name: "pending-own-worker", Cwd: root, Runtime: "embedded", Model: &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"}, InitialInstruction: "create a reminder", RequestID: "d1111111-1111-4111-8111-111111111111"})
	require.NoError(t, err)
	created := waitReminderSDK(t, seen)
	require.NotNil(t, created.result)
	require.False(t, created.result.IsError, "the fixed SDK create must succeed before lifecycle testing")
	old := inspectManagedOwnerBinding(t, path, store.EmbeddedBinding{AgentID: launched.Agent.AgentID, SessionID: launched.Agent.PiSessionID, Epoch: launched.Agent.SessionEpoch, Generation: launched.Agent.RuntimeGeneration})
	check, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer check.Close()
	var reminderID string
	require.NoError(t, check.QueryRowContext(ctx, `SELECT id FROM reminders WHERE target=? AND body=?`, old.AgentID, create["body"]).Scan(&reminderID))
	count := func(query string, args ...any) int {
		t.Helper()
		var n int
		require.NoError(t, check.QueryRowContext(ctx, query, args...).Scan(&n))
		return n
	}
	require.Equal(t, 1, count(`SELECT count(*) FROM embedded_own_reminder_calls WHERE call_id=? AND state='observed'`, callID))
	// Save an unresolved SDK intent through the production effect method, not
	// an invented epoch or fabricated observed result.
	intent := []byte(`{"id":"unresolved-own-create","name":"mecha_reminder","arguments":{"action":"create","body":"never create","due_at":"2026-09-01T00:10:00.000Z"}}`)
	require.NoError(t, aDB.RecordEmbeddedEffectIntent(ctx, old, unknownID, intent))
	require.NoError(t, a.Close(ctx))
	require.NoError(t, clock.Advance(2*time.Minute))
	token, err := bDB.AcquireEmbeddedOwner(ctx, old, "replacement-owner", 30*time.Second)
	require.NoError(t, err)
	current := old
	current.OwnerToken = token
	require.Greater(t, current.OwnerToken, old.OwnerToken)
	_, _, err = aDB.EmbeddedOwnReminderCall(ctx, old, "stale-cancel", []byte(`{"action":"cancel","id":"`+reminderID+`"}`))
	require.Error(t, err)
	_, _, err = aDB.EmbeddedOwnReminderCall(ctx, old, "stale-create", []byte(`{"action":"create","body":"stale","due_at":"2026-09-01T00:10:00.000Z"}`))
	require.Error(t, err)
	_, err = aDB.RecoverEmbeddedEffect(ctx, old, unknownID)
	require.Error(t, err)
	_, err = bDB.ReopenEmbedded(ctx, current)
	require.NoError(t, err)
	effect, err := bDB.RecoverEmbeddedEffect(ctx, current, unknownID)
	require.NoError(t, err)
	require.Equal(t, "unknown", effect.State)
	require.False(t, effect.ShouldReplay)
	require.Empty(t, effect.Result)
	require.Equal(t, 1, count(`SELECT count(*) FROM reminders WHERE target=?`, old.AgentID))
	_, _, err = bDB.EmbeddedOwnReminderCall(ctx, current, unknownID, []byte(`{"action":"create","body":"never create","due_at":"2026-09-01T00:10:00.000Z"}`))
	require.Error(t, err, "an unresolved create cannot become a new effect on takeover")
	pre, err := bDB.FireDueReminders(ctx, nil)
	require.NoError(t, err)
	require.Empty(t, pre)

	failCleanup := true
	b, err := NewManager(options(bDB, &failCleanup))
	require.NoError(t, err)
	defer b.Close(ctx)
	request := TerminateRequest{AgentID: old.AgentID, Method: "DELETE", CanonicalPath: "/v1/managed-agents/" + old.AgentID, RequestID: "d2222222-2222-4222-8222-222222222222", CanonicalBodyHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	_, err = b.Terminate(ctx, request)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		action, e := bDB.ManagedLifecycleAction(ctx, request.RequestID)
		return e == nil && action.State == "failed" && action.Stage == "agent_cleanup"
	}, 3*time.Second, time.Millisecond)
	require.Equal(t, 1, count(`SELECT count(*) FROM embedded_own_reminder_calls WHERE call_id=?`, callID), "immutable receipt survives failed cleanup")
	require.Zero(t, count(`SELECT count(*) FROM reminders WHERE id=? AND state IN ('scheduled','failed','message_queued')`, reminderID), "authoritative terminate must cancel pending own reminder before due")
	require.NoError(t, clock.Advance(8*time.Minute))
	fired, err := bDB.FireDueReminders(ctx, nil)
	require.NoError(t, err)
	require.Empty(t, fired)
	require.Zero(t, count(`SELECT count(*) FROM messages WHERE source_kind='reminder' AND recipient=?`, old.AgentID))
	failCleanup = false
	retry := request
	retry.RequestID = "d3333333-3333-4333-8333-333333333333"
	_, err = b.Terminate(ctx, retry)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		_, e := bDB.ManagedAgent(ctx, old.AgentID)
		return errors.Is(e, sql.ErrNoRows)
	}, 3*time.Second, time.Millisecond)
	require.Zero(t, count(`SELECT count(*) FROM embedded_own_reminder_calls WHERE agent_id=?`, old.AgentID), "ledger cleanup is atomic with authoritative agent deletion")
	fired, err = bDB.FireDueReminders(ctx, nil)
	require.NoError(t, err)
	require.Empty(t, fired)
}
