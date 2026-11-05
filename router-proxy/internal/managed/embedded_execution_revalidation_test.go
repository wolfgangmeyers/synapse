package managed

import (
	"context"
	"testing"
	"time"

	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

// Exercise the actual checked user of a running manager, not a fabricated slot.
func TestEmbeddedRunningExecutionRevalidatesOwnerWithoutRevisionRebind(t *testing.T) {
	f := newFailureFixture(t)
	ctx := context.Background()
	entered, release := make(chan struct{}), make(chan struct{})
	f.reg.SetResponses([]providers.FauxResponseStep{func(ai.TranscriptContext, *ai.SimpleStreamOptions, *providers.FauxState, *ai.Model) *ai.AssistantMessage {
		close(entered)
		<-release
		return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "settled"}}, ai.StopStop)
	}})
	owner := f.launch(t, "")
	f.m.startWG.Wait()
	saved, err := f.db.ManagedAgent(ctx, owner.binding.AgentID)
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() {
		done <- f.m.runEmbeddedSessionPrompt(context.WithValue(ctx, embeddedOwnerContextKey{}, owner), *saved, "checked work")
	}()
	released := false
	defer func() {
		if !released {
			close(release)
		}
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("running SDK did not settle")
		}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("provider not entered")
	}
	lane := f.m.laneFor(owner.binding.AgentID)
	lane.activeMu.RLock()
	p := lane.activePersistence
	lane.activeMu.RUnlock()
	require.NotNil(t, p)
	var before string
	require.NoError(t, f.raw.QueryRow(`SELECT execution_json FROM embedded_sessions WHERE agent_id=? AND current=1`, owner.binding.AgentID).Scan(&before))
	_, err = f.raw.Exec(`UPDATE managed_agent_policies SET desired_revision=desired_revision+1 WHERE agent_id=?`, owner.binding.AgentID)
	require.NoError(t, err)
	require.NoError(t, p.bindExecutionUser(ctx), "same running user must not rebind its frozen revision")
	var after string
	require.NoError(t, f.raw.QueryRow(`SELECT execution_json FROM embedded_sessions WHERE agent_id=? AND current=1`, owner.binding.AgentID).Scan(&after))
	require.Equal(t, before, after)
	next, err := f.db.AcquireEmbeddedOwner(ctx, owner.binding, "replacement-runtime", time.Minute)
	require.NoError(t, err)
	require.Greater(t, next, owner.binding.OwnerToken)
	require.Error(t, p.bindExecutionUser(ctx), "provider/tool admission must revalidate cached user against durable owner")
	close(release)
	released = true
}
