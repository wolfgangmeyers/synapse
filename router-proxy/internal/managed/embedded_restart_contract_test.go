package managed

import (
	"context"
	"testing"
	"time"

	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

type restartInterruptProbe struct {
	embeddedRuntime
	interrupted chan struct{}
}

func (r *restartInterruptProbe) Interrupt() { r.embeddedRuntime.Interrupt(); close(r.interrupted) }

func TestEmbeddedRestartCompletionAndFutureInputs(t *testing.T) {
	for _, active := range []bool{false, true} {
		t.Run(map[bool]string{false: "idle", true: "active"}[active], func(t *testing.T) {
			f := newFailureFixture(t)
			owner := f.launch(t, "")
			ctx := context.Background()
			started, release := make(chan struct{}), make(chan struct{})
			interrupted := make(chan struct{})
			f.m.embeddedRuntimeTestWrapper = func(base embeddedRuntime, _ *embeddedTurnToolAuthority) embeddedRuntime {
				return &restartInterruptProbe{embeddedRuntime: base, interrupted: interrupted}
			}
			if active {
				f.reg.SetResponses([]providers.FauxResponseStep{func(ai.TranscriptContext, *ai.SimpleStreamOptions, *providers.FauxState, *ai.Model) *ai.AssistantMessage {
					close(started)
					<-release
					return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "late"}}, ai.StopStop)
				}})
				done := make(chan error, 1)
				go func() {
					done <- f.m.AdmitPromptWithRequestID(ctx, owner.binding.AgentID, "interrupt me", "restart-active-prompt")
				}()
				select {
				case <-started:
				case <-time.After(5 * time.Second):
					t.Fatal("prompt did not start")
				}
				defer func() {
					select {
					case <-done:
					case <-time.After(5 * time.Second):
						t.Error("prompt did not finish")
					}
				}()
			}
			req := RestartRequest{AgentID: owner.binding.AgentID, Method: "POST", CanonicalPath: "/restart/" + owner.binding.AgentID, RequestID: "restart-contract", CanonicalBodyHash: "empty"}
			result, err := f.m.ReserveRestart(ctx, req)
			require.NoError(t, err)
			require.Equal(t, "terminal", result.Operation.State) // terminal response, not restart settlement
			require.Equal(t, 0, f.count(t, `SELECT count(*) FROM managed_lifecycle_actions WHERE agent_id=? AND kind='restart'`, owner.binding.AgentID))
			if active {
				select {
				case <-interrupted:
				case <-time.After(5 * time.Second):
					close(release)
					t.Fatal("restart did not interrupt active turn")
				}
				require.True(t, f.m.laneFor(owner.binding.AgentID).embeddedResetRunning(), "restart must wait for active turn")
				close(release)
			}
			lane := f.m.laneFor(owner.binding.AgentID)
			require.Eventually(t, func() bool { return !lane.embeddedResetRunning() }, 5*time.Second, time.Millisecond)
			saved, err := f.db.ManagedAgent(ctx, owner.binding.AgentID)
			require.NoError(t, err)
			require.Equal(t, owner.binding.SessionID, saved.PiSessionID)
			require.Equal(t, owner.binding.Epoch, saved.SessionEpoch)
			require.True(t, saved.AcceptingPrompts)
			f.reg.SetResponses([]providers.FauxResponseStep{f.response("prompt after restart"), f.response("mail after restart")})
			require.NoError(t, f.m.AdmitPromptWithRequestID(ctx, owner.binding.AgentID, "after restart", "restart-future-prompt"))
			f.enqueue(t, "e5555555-5555-4555-8555-555555555555")
			f.m.dispatchEmbeddedMail(owner.binding.AgentID)
			require.Eventually(t, func() bool { return f.calls.Load() >= 2 }, 5*time.Second, 10*time.Millisecond)
			replay, err := f.m.ReserveRestart(ctx, req)
			require.NoError(t, err)
			require.True(t, replay.Replayed)
		})
	}
}

func TestEmbeddedRestartRejectsStaleOwnerWithoutInterrupt(t *testing.T) {
	f := newFailureFixture(t)
	owner := f.launch(t, "")
	lane := f.m.laneFor(owner.binding.AgentID)
	runtime := &resetBarrierRuntime{interrupted: make(chan struct{}), closed: make(chan struct{})}
	require.True(t, lane.installEmbeddedRuntime(runtime, nil))
	defer lane.removeEmbeddedRuntime(runtime)
	f.m.mu.Lock()
	stale := f.m.embeddedOwners[owner.binding.AgentID]
	stale.binding.Epoch--
	f.m.embeddedOwners[owner.binding.AgentID] = stale
	f.m.mu.Unlock()
	_, err := f.m.ReserveRestart(context.Background(), RestartRequest{AgentID: owner.binding.AgentID, Method: "POST", CanonicalPath: "/restart", RequestID: "stale-restart", CanonicalBodyHash: "empty"})
	require.Error(t, err)
	select {
	case <-runtime.interrupted:
		t.Fatal("stale restart interrupted runtime")
	default:
	}
	require.Equal(t, 0, f.count(t, `SELECT count(*) FROM managed_http_operations WHERE request_id=?`, "stale-restart"))
}
