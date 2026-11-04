package managed

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/sky-valley/pi/agent"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

type countingWakeRuntime struct {
	embeddedRuntime
	wakes *atomic.Int32
}

func (r *countingWakeRuntime) Run(ctx context.Context, text string, images ...ai.ImageContent) (*embeddedOutcome, error) {
	if text == ContextResetWakePrompt {
		r.wakes.Add(1)
	}
	return r.embeddedRuntime.Run(ctx, text, images...)
}
func (r *countingWakeRuntime) RunMessages(ctx context.Context, messages []agent.AgentMessage) (*embeddedOutcome, error) {
	return r.embeddedRuntime.RunMessages(ctx, messages)
}

func TestCommittedToolResetBrowserHandoff(t *testing.T) {
	for _, name := range []string{"reservation_failure", "replay", "success"} {
		fail := name == "reservation_failure"
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "handoff.db")
			db, err := store.Open(ctx, path, store.Options{})
			require.NoError(t, err)
			defer db.Close()
			raw, err := sql.Open("sqlite", path)
			require.NoError(t, err)
			defer raw.Close()
			reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
			defer reg.Unregister()
			reg.SetResponses([]providers.FauxResponseStep{providers.FauxStatic(providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "wake complete"}}, ai.StopStop))})
			m := makeSubagentTestManager(t, db, reg, 4)
			defer m.Close(ctx)
			binding := subagentTestParent(t, db, "handoff-"+name)
			registerParentOwner(m, binding)
			saved, err := db.ManagedAgent(ctx, binding.AgentID)
			require.NoError(t, err)
			if fail {
				_, err = raw.Exec(`CREATE TRIGGER reject_handoff BEFORE INSERT ON managed_http_operations BEGIN SELECT RAISE(ABORT,'reservation fault'); END`)
				require.NoError(t, err)
			}
			checked := make(chan error, 1)
			release := make(chan struct{})
			var once sync.Once
			var wakes atomic.Int32
			m.embeddedRuntimeTestWrapper = func(base embeddedRuntime, authority *embeddedTurnToolAuthority) embeddedRuntime {
				r := embeddedRuntime(&countingWakeRuntime{embeddedRuntime: base, wakes: &wakes})
				once.Do(func() {
					r = &resetTurnRuntime{embeddedRuntime: r, authority: authority, checked: checked, release: release}
				})
				return r
			}
			lane := m.laneFor(binding.AgentID)
			turnDone := make(chan error, 1)
			ownerCtx := context.WithValue(ctx, embeddedOwnerContextKey{}, embeddedOwner{binding: binding, identity: "fixture:" + binding.AgentID})
			go func() {
				lane.serial.Lock()
				defer lane.serial.Unlock()
				turnDone <- m.runEmbeddedSessionPrompt(ownerCtx, *saved, "controlled turn")
			}()
			require.NoError(t, <-checked)
			entered, proceed := make(chan struct{}), make(chan struct{})
			m.embeddedResetReservationTestHook = func() { close(entered); <-proceed }
			type browserResult struct {
				result ResetResult
				err    error
			}
			browser := make(chan browserResult, 1)
			go func() {
				result, err := m.reserveEmbeddedReset(ctx, ResetRequest{AgentID: binding.AgentID, Method: "POST", CanonicalPath: "/reset", RequestID: "handoff", CanonicalBodyHash: "hash"}, saved)
				browser <- browserResult{result, err}
			}()
			<-entered
			require.True(t, lane.embeddedResetRunning())
			close(release)
			select {
			case <-turnDone:
			case <-time.After(5 * time.Second):
				t.Fatal("turn waited for provisional reservation")
			}
			require.True(t, lane.embeddedResetPending())
			if name == "replay" {
				op := store.ManagedOperation{OperationID: "handoff", Method: "POST", CanonicalPath: "/reset", RequestID: "handoff", CanonicalBodyHash: "hash", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
				_, replayed, e := db.ReserveEmbeddedReset(ctx, binding.AgentID, op)
				require.NoError(t, e)
				require.False(t, replayed)
			}
			close(proceed)
			browserOutcome := <-browser
			if fail {
				require.Error(t, browserOutcome.err)
			} else {
				require.NoError(t, browserOutcome.err)
				require.Equal(t, name == "replay", browserOutcome.result.Replayed)
			}
			require.Eventually(t, func() bool {
				current, e := db.ManagedAgent(ctx, binding.AgentID)
				return e == nil && current.SessionEpoch == binding.Epoch+1 && !lane.embeddedResetPending() && !lane.embeddedResetRunning()
			}, 10*time.Second, 10*time.Millisecond)
			var rotations int
			require.NoError(t, raw.QueryRow(`SELECT count(*) FROM managed_complete_history WHERE agent_id=? AND category='context_reset'`, binding.AgentID).Scan(&rotations))
			require.Equal(t, 1, rotations)
			require.EqualValues(t, 1, wakes.Load(), "exactly one actual fresh wake")
		})
	}
}

func TestResetHandoffTransfersOnlyMatchingDeferredGate(t *testing.T) {
	old := store.EmbeddedBinding{AgentID: "agent", SessionID: "old", Epoch: 1, Generation: "gen"}
	fresh := old
	fresh.SessionID = "new"
	fresh.Epoch++
	oldReq := &contextResetRequest{handoffBinding: &old}
	freshReq := &contextResetRequest{handoffBinding: &fresh}
	pendingReq := &contextResetRequest{}
	lane := &managedAgentLane{resetPending: true, resetToolGates: map[*contextResetRequest]struct{}{oldReq: {}, freshReq: {}, pendingReq: {}}}
	lane.reopenEmbeddedResetAdmissionsForBinding(old)
	require.NotContains(t, lane.resetToolGates, oldReq)
	require.Contains(t, lane.resetToolGates, freshReq)
	require.Contains(t, lane.resetToolGates, pendingReq)
	require.True(t, lane.embeddedResetPending())
}
