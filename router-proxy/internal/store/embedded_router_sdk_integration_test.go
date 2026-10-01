package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/sky-valley/pi/coding"
	"github.com/stretchr/testify/require"
)

// The only authority is the router Store. This adapter exists only in tests.
// Its serialized head maps SDK ordinal sequence to the Store's digest chain;
// a stable key identifies exactly one append at the observed head.
type sdkStoreAdapter struct {
	s      *Store
	b      EmbeddedBinding
	seq    int64
	digest string
}

func adapter(s *Store, b EmbeddedBinding) *sdkStoreAdapter {
	return &sdkStoreAdapter{s: s, b: b, digest: embeddedGenesis}
}
func (a *sdkStoreAdapter) AppendMessage(ctx context.Context, m agent.AgentMessage) error {
	body, err := json.Marshal(m)
	if err != nil {
		return err
	}
	kind := string(m.MessageRole())
	e, err := a.s.AppendEmbeddedEntryAt(ctx, a.b, a.seq, a.digest, fmt.Sprintf("sdk:%s:%d", embeddedHash([]byte(a.b.AgentID), []byte(a.b.SessionID), []byte(fmt.Sprint(a.b.Epoch)), []byte(a.b.Generation)), a.seq+1), kind, body)
	if err != nil {
		return err
	}
	a.seq = e.Sequence
	a.digest = e.Digest
	return nil
}
func (a *sdkStoreAdapter) CommitCheckpoint(ctx context.Context, c coding.OrderedCheckpoint) error {
	if c.ThroughSequence != a.seq || c.FirstKeptSequence < 1 || c.FirstKeptSequence > c.ThroughSequence || c.Summary == "" {
		return fmt.Errorf("invalid SDK checkpoint: %+v", c)
	}
	snap, err := a.s.ReopenEmbedded(ctx, a.b)
	if err != nil {
		return err
	}
	body, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return a.s.CommitEmbeddedCheckpoint(ctx, a.b, c.ThroughSequence, snap.Entries[c.ThroughSequence-1].Digest, body)
}
func (a *sdkStoreAdapter) Restore(ctx context.Context, s *coding.Session) error {
	snap, err := a.s.ReopenEmbedded(ctx, a.b)
	if err != nil {
		return err
	} // digest validation BEFORE SDK restore
	out := coding.OrderedSnapshot{SessionID: a.b.SessionID}
	for _, e := range snap.Entries {
		m, err := ai.UnmarshalMessage(e.Body)
		if err != nil {
			return err
		}
		if string(m.MessageRole()) != e.Kind {
			return fmt.Errorf("role mismatch at %d", e.Sequence)
		}
		out.Messages = append(out.Messages, coding.OrderedMessage{Sequence: e.Sequence, ParentSequence: e.Sequence - 1, Message: m})
	}
	if snap.Checkpoint != nil {
		var c coding.OrderedCheckpoint
		if err := json.Unmarshal(snap.Checkpoint.Body, &c); err != nil {
			return err
		}
		if c.ThroughSequence != snap.Checkpoint.Sequence || c.FirstKeptSequence < 1 || c.FirstKeptSequence > c.ThroughSequence || c.Summary == "" {
			return fmt.Errorf("checkpoint payload mismatch")
		}
		out.Checkpoint = &c
	}
	if err := s.RestoreOrdered(ctx, out); err != nil {
		return err
	}
	a.seq = int64(len(snap.Entries))
	a.digest = embeddedGenesis
	if a.seq > 0 {
		a.digest = snap.Entries[a.seq-1].Digest
	}
	return nil
}
func TestEmbeddedCombinedAdapterKeysScopedToBinding(t *testing.T) {
	s, _ := embeddedRedStore(t)
	first := redBind(t, s, "root", "shared-session", 1, "g1", "root")
	second := redBind(t, s, "worker", "shared-session", 1, "g1", "worker")
	m := ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "same position"}}}
	require.NoError(t, adapter(s, first).AppendMessage(context.Background(), m))
	require.NoError(t, adapter(s, second).AppendMessage(context.Background(), m), "independent bindings cannot share an append key")
	require.Len(t, redReopen(t, s, first).Entries, 1)
	require.Len(t, redReopen(t, s, second).Entries, 1)
}

func TestEmbeddedCombinedFirstPromptFault(t *testing.T) {
	ctx := context.Background()
	s, _ := embeddedRedStore(t)
	b := redBind(t, s, "root", "combined-first", 1, "g1", "root")
	a := adapter(s, b)
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()
	called := 0
	reg.SetResponses([]providers.FauxResponseStep{func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		called++
		return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "ok"}}, ai.StopStop)
	}})
	session := coding.NewSession(coding.SessionOptions{SessionID: b.SessionID, Model: reg.GetModel(), Cwd: t.TempDir(), NoTools: coding.NoToolsAll, OrderedPersistence: a})
	embeddedRedFault(t, s, "combined_first_fault", "embedded_entries")
	_, err := session.Run(ctx, "first")
	require.ErrorContains(t, err, "injected embedded red fault")
	require.Zero(t, called)
	require.Empty(t, redReopen(t, s, b).Entries)
}

// The first-prompt fault above rejects the system declaration. This fault
// instead lets that declaration commit, then rejects the actual user prompt.
func TestEmbeddedCombinedUserPromptAppendFailure(t *testing.T) {
	ctx := context.Background()
	s, path := embeddedRedStore(t)
	b := redBind(t, s, "root", "combined-user-fault", 1, "g1", "root")
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()
	called := 0
	reg.SetResponses([]providers.FauxResponseStep{func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		called++
		return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "unexpected"}}, ai.StopStop)
	}})
	session := coding.NewSession(coding.SessionOptions{SessionID: b.SessionID, Model: reg.GetModel(), Cwd: t.TempDir(), NoTools: coding.NoToolsAll, OrderedPersistence: adapter(s, b)})
	_, err := s.db.Exec(`CREATE TEMP TRIGGER combined_user_fault BEFORE INSERT ON main.embedded_entries WHEN NEW.kind='user' BEGIN SELECT RAISE(ABORT,'injected user prompt append fault'); END`)
	require.NoError(t, err)
	result, err := session.Run(ctx, "rejected user prompt")
	require.ErrorContains(t, err, "injected user prompt append fault")
	if result != nil {
		require.Equal(t, "error", string(result.StopReason), "failed prompt cannot return a successful run")
		require.Contains(t, result.ErrorMessage, "injected user prompt append fault")
	}
	require.Zero(t, called, "provider must not see an uncommitted user prompt")
	require.Zero(t, embeddedRedCount(t, s, "embedded_acks"), "failed run cannot claim a processing ACK")
	require.Equal(t, 1, embeddedRedCount(t, s, "embedded_append_keys"), "only the committed system declaration has a durable key")
	snap := redReopen(t, s, b)
	require.Len(t, snap.Entries, 1, "only the committed system declaration may survive")
	require.Equal(t, "system", snap.Entries[0].Kind)
	_, err = s.db.Exec(`DROP TRIGGER temp.combined_user_fault`)
	require.NoError(t, err)
	require.NoError(t, s.Close())
	reopened, err := Open(ctx, path, Options{})
	require.NoError(t, err)
	defer reopened.Close()
	a2 := adapter(reopened, b)
	restored := coding.NewSession(coding.SessionOptions{SessionID: b.SessionID, Model: reg.GetModel(), Cwd: t.TempDir(), NoTools: coding.NoToolsAll, OrderedPersistence: a2})
	require.NoError(t, a2.Restore(ctx, restored))
	require.Len(t, restored.History(), 1)
	require.Equal(t, "system", string(restored.History()[0].MessageRole()))
	require.Zero(t, embeddedRedCount(t, reopened, "embedded_acks"))
}

func TestEmbeddedCombinedUnknownEffectAndIntentFailure(t *testing.T) {
	for _, failIntent := range []bool{false, true} {
		t.Run(fmt.Sprint("intent_failure=", failIntent), func(t *testing.T) {
			ctx := context.Background()
			s, path := embeddedRedStore(t)
			b := redBind(t, s, "root", "combined-effect", 1, "g1", "root")
			a := adapter(s, b)
			reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
			defer reg.Unregister()
			providerCalls := 0
			reg.SetResponses([]providers.FauxResponseStep{
				func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
					providerCalls++
					return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("effect", map[string]any{}, "effect-1")}, ai.StopToolUse)
				},
				func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
					providerCalls++
					return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "unexpected continuation"}}, ai.StopStop)
				},
			})
			executes, admits := 0, 0
			tool := agent.AgentTool{Name: "effect", Description: "test-only", Parameters: &ai.Schema{Type: "object"}, Execute: func(_ context.Context, _ string, _ map[string]any, _ agent.ToolUpdateFunc) (agent.AgentToolResult, error) {
				executes++
				return agent.AgentToolResult{Content: ai.ContentList{ai.TextContent{Text: "effect result"}}}, nil
			}}
			session := coding.NewSession(coding.SessionOptions{SessionID: b.SessionID, Model: reg.GetModel(), Cwd: t.TempDir(), Tools: []agent.AgentTool{tool}, OrderedPersistence: a,
				BeforeToolExecute: func(callCtx context.Context, c agent.BeforeToolCallContext) error {
					admits++
					require.Equal(t, "effect-1", c.ToolCall.ID)
					require.Equal(t, "effect", c.ToolCall.Name)
					require.Equal(t, "assistant", redReopen(t, s, b).Entries[len(redReopen(t, s, b).Entries)-1].Kind, "assistant tool call must be durable before intent")
					return s.RecordEmbeddedEffectIntent(callCtx, b, c.ToolCall.ID, []byte("intent"))
				},
			})
			if failIntent {
				_, err := s.db.Exec(`CREATE TEMP TRIGGER combined_intent_fault BEFORE INSERT ON main.embedded_effects BEGIN SELECT RAISE(ABORT,'injected intent fault'); END`)
				require.NoError(t, err)
			} else {
				_, err := s.db.Exec(`CREATE TEMP TRIGGER combined_result_fault BEFORE INSERT ON main.embedded_entries WHEN NEW.kind='toolResult' BEGIN SELECT RAISE(ABORT,'injected result fault'); END`)
				require.NoError(t, err)
			}
			result, err := session.Run(ctx, "run effect")
			require.Error(t, err)
			if failIntent {
				require.NotNil(t, result)
				require.Equal(t, ai.StopError, result.StopReason)
				require.Contains(t, result.ErrorMessage, "injected intent fault")
				require.Zero(t, executes, "intent rejection cannot execute external effect")
				require.Equal(t, 1, admits)
				require.Equal(t, 1, providerCalls, "intent rejection cannot continue model")
				require.Zero(t, embeddedRedCount(t, s, "embedded_acks"))
				for _, m := range session.History() {
					require.NotEqual(t, ai.RoleToolResult, m.MessageRole(), "intent error cannot become a tool result")
				}
			} else {
				require.Equal(t, 1, executes)
				require.Equal(t, 1, admits)
				require.Equal(t, 1, providerCalls)
			}
			if failIntent {
				_, err = s.db.Exec(`DROP TRIGGER temp.combined_intent_fault`)
			} else {
				_, err = s.db.Exec(`DROP TRIGGER temp.combined_result_fault`)
			}
			require.NoError(t, err)
			require.NoError(t, s.Close())
			reopened, err := Open(ctx, path, Options{})
			require.NoError(t, err)
			defer reopened.Close()
			a2 := adapter(reopened, b)
			resumed := coding.NewSession(coding.SessionOptions{SessionID: b.SessionID, Model: reg.GetModel(), Cwd: t.TempDir(), Tools: []agent.AgentTool{tool}, OrderedPersistence: a2,
				BeforeToolExecute: func(callCtx context.Context, c agent.BeforeToolCallContext) error {
					return reopened.RecordEmbeddedEffectIntent(callCtx, b, c.ToolCall.ID, []byte("intent"))
				},
			})
			require.NoError(t, a2.Restore(ctx, resumed))
			effect, err := reopened.RecoverEmbeddedEffect(ctx, b, "effect-1")
			if failIntent {
				require.ErrorIs(t, err, sql.ErrNoRows, "rejected intent cannot leave a replayable effect")
				require.Zero(t, embeddedRedCount(t, reopened, "embedded_acks"))
			} else {
				require.NoError(t, err)
				require.Equal(t, "unknown", effect.State)
				require.False(t, effect.ShouldReplay)
				require.Empty(t, effect.Result)
				require.Equal(t, 1, executes)
				require.Equal(t, "assistant", redReopen(t, reopened, b).Entries[len(redReopen(t, reopened, b).Entries)-1].Kind)
				require.Error(t, reopened.RecordEmbeddedEffectIntent(ctx, b, "effect-1", []byte("intent")), "resume cannot re-admit the same external effect")
			}
			require.Error(t, resumed.Continue(ctx), "restore must not auto-replay an assistant tool call")
			if failIntent {
				require.Zero(t, executes)
			} else {
				require.Equal(t, 1, executes)
			}
			require.Equal(t, 1, providerCalls)
		})
	}
}

func TestEmbeddedCombinedCheckpointFaultsAndPayload(t *testing.T) {
	for _, automatic := range []bool{false, true} {
		t.Run(fmt.Sprint("automatic=", automatic), func(t *testing.T) {
			ctx := context.Background()
			s, path := embeddedRedStore(t)
			b := redBind(t, s, "root", "combined-checkpoint", 1, "g1", "root")
			a := adapter(s, b)
			reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
			defer reg.Unregister()
			reg.SetResponses([]providers.FauxResponseStep{providers.FauxStatic(providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "first"}}, ai.StopStop)), providers.FauxStatic(providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "summary"}}, ai.StopStop))})
			session := coding.NewSession(coding.SessionOptions{SessionID: b.SessionID, Model: reg.GetModel(), Cwd: t.TempDir(), NoTools: coding.NoToolsAll, OrderedPersistence: a})
			_, err := session.Run(ctx, strings.Repeat("old context ", 400))
			require.NoError(t, err)
			prior := len(session.History())
			session.EnableCompaction(coding.CompactionSettings{Enabled: true, ReserveTokens: 1, KeepRecentTokens: 1})
			_, e := s.db.Exec(`CREATE TEMP TRIGGER combined_checkpoint_fault BEFORE INSERT ON main.embedded_checkpoints BEGIN SELECT RAISE(ABORT,'injected checkpoint fault'); END`)
			require.NoError(t, e)
			if automatic {
				session.Model.ContextWindow = 10
				_, err = session.Run(ctx, "trigger")
				require.ErrorContains(t, err, "checkpoint")
			} else {
				ok, e := session.Compact(ctx)
				require.False(t, ok)
				require.ErrorContains(t, e, "checkpoint")
			}
			require.Nil(t, redReopen(t, s, b).Checkpoint)
			require.GreaterOrEqual(t, len(session.History()), prior)
			_, e = s.db.Exec(`DROP TRIGGER temp.combined_checkpoint_fault`)
			require.NoError(t, e)
			require.NoError(t, s.Close())
			reopened, e := Open(ctx, path, Options{})
			require.NoError(t, e)
			defer reopened.Close()
			a2 := adapter(reopened, b)
			next := coding.NewSession(coding.SessionOptions{SessionID: b.SessionID, Model: reg.GetModel(), Cwd: t.TempDir(), NoTools: coding.NoToolsAll, OrderedPersistence: a2})
			require.NoError(t, a2.Restore(ctx, next))
			require.GreaterOrEqual(t, len(next.History()), prior)
		})
	}
}

func TestEmbeddedCombinedCheckpointPayloadAndDigestBeforeRestore(t *testing.T) {
	ctx := context.Background()
	s, path := embeddedRedStore(t)
	b := redBind(t, s, "root", "combined-payload", 1, "g1", "root")
	a := adapter(s, b)
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()
	reg.SetResponses([]providers.FauxResponseStep{providers.FauxStatic(providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "first"}}, ai.StopStop)), providers.FauxStatic(providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "summary"}}, ai.StopStop))})
	session := coding.NewSession(coding.SessionOptions{SessionID: b.SessionID, Model: reg.GetModel(), Cwd: t.TempDir(), NoTools: coding.NoToolsAll, OrderedPersistence: a})
	_, err := session.Run(ctx, strings.Repeat("old context ", 400))
	require.NoError(t, err)
	session.EnableCompaction(coding.CompactionSettings{Enabled: true, ReserveTokens: 1, KeepRecentTokens: 1})
	ok, err := session.Compact(ctx)
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, s.Close())
	reopened, err := Open(ctx, path, Options{})
	require.NoError(t, err)
	defer reopened.Close()
	snap := redReopen(t, reopened, b)
	require.NotNil(t, snap.Checkpoint)
	var payload coding.OrderedCheckpoint
	require.NoError(t, json.Unmarshal(snap.Checkpoint.Body, &payload))
	require.Equal(t, snap.Checkpoint.Sequence, payload.ThroughSequence)
	require.Greater(t, payload.FirstKeptSequence, int64(0))
	require.NotEmpty(t, payload.Summary)
	a2 := adapter(reopened, b)
	next := coding.NewSession(coding.SessionOptions{SessionID: b.SessionID, Model: reg.GetModel(), Cwd: t.TempDir(), NoTools: coding.NoToolsAll, OrderedPersistence: a2})
	require.NoError(t, a2.Restore(ctx, next))
	require.Equal(t, len(session.History()), len(next.History()))
	_, err = reopened.db.Exec(`UPDATE embedded_checkpoints SET body=X'00'`)
	require.NoError(t, err)
	untouched := coding.NewSession(coding.SessionOptions{SessionID: b.SessionID, Model: reg.GetModel(), Cwd: t.TempDir(), NoTools: coding.NoToolsAll})
	require.Error(t, adapter(reopened, b).Restore(ctx, untouched))
	require.Empty(t, untouched.History())
}

func TestEmbeddedCombinedFenceMailAcceptanceAndRollback(t *testing.T) {
	ctx := context.Background()
	s, _ := embeddedRedStore(t)
	b := redBind(t, s, "root", "combined-mail", 1, "g1", "root")
	a := adapter(s, b)
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()
	reg.SetResponses([]providers.FauxResponseStep{providers.FauxStatic(providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "ok"}}, ai.StopStop))})
	session := coding.NewSession(coding.SessionOptions{SessionID: b.SessionID, Model: reg.GetModel(), Cwd: t.TempDir(), NoTools: coding.NoToolsAll, OrderedPersistence: a})
	_, err := session.Run(ctx, "first")
	require.NoError(t, err)
	envelope := testEnvelope("combined-mail-id", "message")
	envelope.To = "root"
	accepted, err := s.Enqueue(ctx, envelope)
	require.NoError(t, err)
	require.Equal(t, "queued", accepted.Message.State)
	require.Zero(t, embeddedRedCount(t, s, "embedded_acks"))
	attempt := reserveEmbeddedMailTest(t, s, b, "combined-mail-id")
	for _, bad := range []EmbeddedBinding{{AgentID: "root", SessionID: b.SessionID, Epoch: 2, Generation: "g1"}, {AgentID: "root", SessionID: b.SessionID, Epoch: 1, Generation: "stale"}} {
		require.Error(t, s.AppendEmbeddedMailAndACK(ctx, bad, attempt.AttemptID, []byte("message")))
		require.Error(t, adapter(s, bad).Restore(ctx, coding.NewSession(coding.SessionOptions{SessionID: b.SessionID})))
	}
	before := len(redReopen(t, s, b).Entries)
	embeddedRedFault(t, s, "combined_ack_fault", "embedded_acks")
	require.Error(t, s.AppendEmbeddedMailAndACK(ctx, b, attempt.AttemptID, []byte("message")))
	require.Len(t, redReopen(t, s, b).Entries, before)
	require.Zero(t, embeddedRedCount(t, s, "embedded_acks"))
	_, err = s.db.Exec(`DROP TRIGGER temp.combined_ack_fault`)
	require.NoError(t, err)
	require.NoError(t, s.AppendEmbeddedMailAndACK(ctx, b, attempt.AttemptID, []byte("message")))
	require.NoError(t, s.AppendEmbeddedMailAndACK(ctx, b, attempt.AttemptID, []byte("message")))
	require.Equal(t, 1, embeddedRedCount(t, s, "embedded_acks"))
	msg, err := s.MessageFor(ctx, "combined-mail-id")
	require.NoError(t, err)
	require.Equal(t, "acknowledged", msg.State)
}
