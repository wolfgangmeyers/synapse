package managed

import (
	"context"
	"testing"

	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/sky-valley/pi/coding"
	"github.com/stretchr/testify/require"
)

// RE-001/006: use the real SDK and checked SQLite persistence. Only the
// provider is fake. A refused Continue must not manufacture another user.
func TestPiEmbeddedRuntimeCheckedRunContinueAndClose(t *testing.T) {
	ctx := context.Background()
	db, binding, _, _ := wakePersistenceFixtureWithClaim(t, false)
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()
	input := ai.UserMessage{Content: ai.ContentList{
		ai.TextContent{Text: "ordered input"},
		ai.ImageContent{Data: "aW1hZ2U=", MimeType: "image/png"},
	}, Timestamp: 1234}
	var observed []agent.AgentMessage
	reg.SetResponses([]providers.FauxResponseStep{
		func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			observed = h.Messages
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "first answer"}}, ai.StopStop)
		},
	})
	p := &embeddedPersistence{db: db, binding: binding, digest: "genesis"}
	session := coding.NewSession(coding.SessionOptions{SessionID: binding.SessionID, Model: reg.GetModel(), Cwd: t.TempDir(), NoTools: coding.NoToolsAll, OrderedPersistence: p})
	var runtime embeddedRuntime = &piEmbeddedRuntime{Session: session}
	outcome, err := runtime.RunMessages(ctx, []agent.AgentMessage{input})
	require.NoError(t, err)
	require.Equal(t, ai.StopStop, outcome.StopReason)
	require.Equal(t, input, observed[len(observed)-1])
	snapshot := runtime.Snapshot()
	require.True(t, snapshot.HasUser)
	require.Equal(t, "first answer", snapshot.LastAssistantText)
	require.Equal(t, embeddedProjectedTokens(session.History()), snapshot.ContextTokens)
	require.Equal(t, session.Model.ContextWindow, snapshot.ContextWindow)
	before, err := db.ReopenEmbedded(ctx, binding)
	require.NoError(t, err)
	runtime.Close() // Pi owns no persistent engine resource; close is not abort/reset.
	after, err := db.ReopenEmbedded(ctx, binding)
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.ErrorContains(t, runtime.Continue(ctx), "Cannot continue from message role: assistant")
	after, err = db.ReopenEmbedded(ctx, binding)
	require.NoError(t, err)
	users := 0
	for _, entry := range after.Entries {
		if entry.Kind == "user" {
			users++
		}
	}
	require.Equal(t, 1, users)
	require.Equal(t, "first answer", runtime.Snapshot().LastAssistantText)
}

func TestPiEmbeddedRuntimeStopErrorPreservesOutcomeAndError(t *testing.T) {
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()
	reg.SetResponses([]providers.FauxResponseStep{providers.FauxStatic(func() *ai.AssistantMessage {
		message := providers.FauxAssistantMessage(nil, ai.StopError)
		message.ErrorMessage = "deterministic failure"
		return message
	}())})
	var runtime embeddedRuntime = &piEmbeddedRuntime{Session: coding.NewSession(coding.SessionOptions{Model: reg.GetModel(), Cwd: t.TempDir(), NoTools: coding.NoToolsAll})}
	outcome, err := runtime.Run(context.Background(), "fail deterministically")
	require.Error(t, err)
	require.NotNil(t, outcome)
	require.Equal(t, ai.StopError, outcome.StopReason)
}

// RE-003: shared interruption must use the active handle, not a concrete Pi
// Agent. Embedding supplies unused methods without a second fake engine.
type interruptRuntimeProbe struct {
	embeddedRuntime
	interrupted bool
}

func (p *interruptRuntimeProbe) Interrupt() { p.interrupted = true }
func TestEmbeddedRuntimeRegistryInterrupt(t *testing.T) {
	runtime := &interruptRuntimeProbe{}
	manager := &Manager{activeEmbeddedSessions: map[string]embeddedRuntime{"worker": runtime}}
	require.NoError(t, manager.InterruptAgent(context.Background(), "worker"))
	require.True(t, runtime.interrupted)
	require.Same(t, runtime, manager.activeEmbeddedSessions["worker"])
	require.NoError(t, manager.InterruptAgent(context.Background(), "absent"))
}

func TestPiEmbeddedRuntimeContinueRestoredUser(t *testing.T) {
	ctx := context.Background()
	db, binding, _, _ := wakePersistenceFixtureWithClaim(t, false)
	initial := &embeddedPersistence{db: db, binding: binding, digest: "genesis"}
	require.NoError(t, initial.AppendMessage(ctx, ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "checked before restart"}}}))
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()
	reg.SetResponses([]providers.FauxResponseStep{providers.FauxStatic(providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "restored answer"}}, ai.StopStop))})
	p := &embeddedPersistence{db: db, binding: binding, digest: "genesis"}
	session := coding.NewSession(coding.SessionOptions{SessionID: binding.SessionID, Model: reg.GetModel(), Cwd: t.TempDir(), NoTools: coding.NoToolsAll, OrderedPersistence: p})
	require.NoError(t, p.restore(ctx, session))
	var runtime embeddedRuntime = &piEmbeddedRuntime{Session: session}
	before := p.seq
	require.NoError(t, runtime.Continue(ctx))
	after, err := db.ReopenEmbedded(ctx, binding)
	require.NoError(t, err)
	require.Len(t, after.Entries, int(before+1))
	require.Equal(t, "assistant", after.Entries[len(after.Entries)-1].Kind)
	require.Equal(t, "restored answer", runtime.Snapshot().LastAssistantText)
}
