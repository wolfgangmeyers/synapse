package managed

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

// An independently owned coordinator must not release a tool request's gate.
func TestResetGateCoordinatorCannotReleaseToolRequest(t *testing.T) {
	m := &Manager{}
	owner := embeddedOwner{binding: store.EmbeddedBinding{AgentID: "reset-gate-ownership"}}
	tool := m.embeddedContextResetTool(owner, func(*contextResetRequest) {}, "context_reset")
	_, err := tool.Execute(context.Background(), "call-1", map[string]any{}, nil)
	require.NoError(t, err)
	lane := m.laneFor(owner.binding.AgentID)
	_, ok := lane.beginEmbeddedReset()
	require.True(t, ok)
	lane.endEmbeddedReset()
	require.True(t, lane.embeddedResetPending(), "coordinator must not clear another request's admission gate")
}

func TestResetGateCheckedAppendFailureReleasesOnlyToolGate(t *testing.T) {
	ctx := context.Background()
	db, binding, _, _ := wakePersistenceFixtureWithClaim(t, false)
	m := &Manager{store: db, runCtx: ctx}
	p := &embeddedPersistence{db: db, binding: binding, digest: "genesis"}
	a, err := m.newEmbeddedTurnToolAuthority(ctx, embeddedOwner{binding: binding, identity: "wake-fixture"}, p, SavedAgentSession{Cwd: t.TempDir()}, false)
	require.NoError(t, err)
	var tool agent.AgentTool
	for _, candidate := range a.Tools() {
		if candidate.Name == "context_reset" {
			tool = candidate
		}
	}
	result, err := tool.Execute(ctx, "reset-failed", map[string]any{}, nil)
	require.NoError(t, err)
	lane := m.laneFor(binding.AgentID)
	_, ok := lane.beginEmbeddedReset()
	require.True(t, ok)
	p.binding.OwnerToken++ // deterministic checked append failure
	err = a.AppendCheckedMessage(ctx, ai.ToolResultMessage{ToolCallID: "reset-failed", ToolName: "context_reset", Content: result.Content})
	require.Error(t, err)
	require.False(t, m.contextResetRequested[binding.AgentID].committed)
	m.finishToolContextReset(ctx, binding, true)
	drained := make(chan struct{})
	go func() { m.startWG.Wait(); close(drained) }()
	select {
	case <-drained: // Drain the reoffer while the coordinator gate is still held.
	case <-time.After(3 * time.Second):
		t.Fatal("reoffer did not drain while coordinator gate was held")
	}
	require.True(t, lane.embeddedResetPending(), "coordinator still owns gate")
	lane.endEmbeddedReset()
	require.False(t, lane.embeddedResetPending())
	_, err = db.ReopenEmbedded(ctx, binding)
	require.NoError(t, err)
}

// A controlled engine runs through the actual turn boundary and checked store;
// it returns a non-StopError only after its checked-result callback has drained.
type resetTurnRuntime struct {
	embeddedRuntime
	authority *embeddedTurnToolAuthority
	checked   chan error
	release   chan struct{}
}

func (r *resetTurnRuntime) Run(ctx context.Context, text string, _ ...ai.ImageContent) (*embeddedOutcome, error) {
	p := r.authority
	if err := p.AppendCheckedMessage(ctx, ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: text}}}); err != nil {
		return nil, err
	}
	call := ai.ToolCall{ID: "controlled-reset", Name: "context_reset", Arguments: map[string]any{}}
	if err := p.AppendCheckedMessage(ctx, ai.AssistantMessage{Content: ai.ContentList{call}, StopReason: ai.StopToolUse}); err != nil {
		return nil, err
	}
	var tool agent.AgentTool
	for _, candidate := range p.Tools() {
		if candidate.Name == call.Name {
			tool = candidate
			break
		}
	}
	result, err := tool.Execute(ctx, call.ID, call.Arguments, nil)
	if err == nil {
		err = p.AppendCheckedMessage(ctx, ai.ToolResultMessage{ToolCallID: call.ID, ToolName: call.Name, Content: result.Content})
	}
	select {
	case r.checked <- err:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case <-r.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, err
	}
	return &embeddedOutcome{StopReason: ai.StopStop}, errors.New("controlled non-StopError after checked callback")
}

func TestResetGateFullTurnCheckedResultAndNonStopError(t *testing.T) {
	for _, failAppend := range []bool{false, true} {
		name := "committed"
		if failAppend {
			name = "checked_append_failed"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			path := filepath.Join(t.TempDir(), "turn.db")
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
			binding := subagentTestParent(t, db, "full-reset-"+name)
			registerParentOwner(m, binding)
			saved, err := db.ManagedAgent(ctx, binding.AgentID)
			require.NoError(t, err)
			if failAppend {
				_, err = raw.Exec(`CREATE TRIGGER reject_reset_result BEFORE INSERT ON embedded_entries WHEN NEW.kind='toolResult' BEGIN SELECT RAISE(ABORT, 'checked result rejected'); END`)
				require.NoError(t, err)
			}
			checked := make(chan error, 1)
			release := make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			var once sync.Once
			m.embeddedRuntimeTestWrapper = func(base embeddedRuntime, authority *embeddedTurnToolAuthority) embeddedRuntime {
				var replacement embeddedRuntime = base
				once.Do(func() {
					replacement = &resetTurnRuntime{embeddedRuntime: base, authority: authority, checked: checked, release: release}
				})
				return replacement
			}
			ownerCtx := context.WithValue(ctx, embeddedOwnerContextKey{}, embeddedOwner{binding: binding, identity: "fixture:" + binding.AgentID})
			lane := m.laneFor(binding.AgentID)
			done := make(chan error, 1)
			finished := make(chan struct{})
			defer func() {
				cancel()
				unblock()
				select {
				case <-finished:
				case <-time.After(5 * time.Second):
					t.Error("turn cleanup did not finish")
				}
			}()
			go func() {
				defer close(finished)
				lane.serial.Lock()
				defer lane.serial.Unlock()
				done <- m.runEmbeddedSessionPrompt(ownerCtx, *saved, "controlled turn")
			}()
			var appendErr error
			select {
			case appendErr = <-checked:
			case err := <-done:
				t.Fatalf("turn returned before checked callback: %v", err)
			case <-time.After(5 * time.Second):
				t.Fatal("checked callback did not arrive")
			}
			if failAppend {
				require.ErrorContains(t, appendErr, "checked result rejected")
			} else {
				require.NoError(t, appendErr)
			}
			require.True(t, lane.embeddedResetPending(), "gate remains closed while callback is live")
			var results int
			require.NoError(t, raw.QueryRow(`SELECT count(*) FROM embedded_entries WHERE agent_id=? AND kind='toolResult'`, binding.AgentID).Scan(&results))
			if failAppend {
				require.Zero(t, results)
			} else {
				require.Equal(t, 1, results)
			}
			current, err := db.ManagedAgent(ctx, binding.AgentID)
			require.NoError(t, err)
			require.Equal(t, binding.Epoch, current.SessionEpoch, "rotation must wait for callback drain")
			unblock()
			var turnErr error
			select {
			case turnErr = <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("turn did not complete")
			}
			if failAppend {
				require.ErrorContains(t, turnErr, "checked result rejected")
			} else {
				require.ErrorContains(t, turnErr, "controlled non-StopError")
			}
			require.False(t, lane.embeddedResetPending(), "turn must not strand its gate")
			current, err = db.ManagedAgent(ctx, binding.AgentID)
			require.NoError(t, err)
			want := binding.Epoch
			if !failAppend {
				want++
			}
			require.Equal(t, want, current.SessionEpoch)
			var rotations int
			require.NoError(t, raw.QueryRow(`SELECT count(*) FROM managed_complete_history WHERE agent_id=? AND category='context_reset'`, binding.AgentID).Scan(&rotations))
			if failAppend {
				require.Zero(t, rotations)
			} else {
				require.Equal(t, 1, rotations)
			}
		})
	}
}
