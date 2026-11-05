package managed

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

// CC-011: exercise the real threshold coordinator and SQLite warning/history
// path. Only the external runtime's reported usage is deterministic here.
// These are supporting regression tests, not browser acceptance evidence.
type claudeFreshThresholdRuntime struct {
	tokens     int
	interrupts int
}

func (*claudeFreshThresholdRuntime) Run(context.Context, string, ...ai.ImageContent) (*embeddedOutcome, error) {
	return nil, nil
}
func (*claudeFreshThresholdRuntime) RunMessages(context.Context, []agent.AgentMessage) (*embeddedOutcome, error) {
	return nil, nil
}
func (*claudeFreshThresholdRuntime) Continue(context.Context) error { return nil }
func (*claudeFreshThresholdRuntime) Steer(agent.AgentMessage)       {}
func (r *claudeFreshThresholdRuntime) Interrupt()                   { r.interrupts++ }
func (*claudeFreshThresholdRuntime) Close()                         {}
func (r *claudeFreshThresholdRuntime) Snapshot() embeddedSnapshot {
	return embeddedSnapshot{ContextTokens: r.tokens, ContextWindow: 1000}
}

func TestClaudeFreshThresholdWarningAndNextOrdinaryTurn(t *testing.T) {
	for _, tokens := range []int{150, 200, 250} {
		t.Run(fmt.Sprintf("tokens_%d", tokens), func(t *testing.T) {
			ctx := context.Background()
			s, err := store.Open(ctx, filepath.Join(t.TempDir(), "threshold.db"), store.Options{})
			require.NoError(t, err)
			defer s.Close()
			binding := subagentTestParentWithModel(t, s, "fresh-claude", store.ManagedModel{Provider: claudeCodeProvider, ID: "sonnet"})
			require.NoError(t, s.SetContextThresholdConfig(ctx, store.ContextThresholdConfig{WarningPercent: 10, ResetPercent: 20}))
			// No background dispatch: assertions observe the committed warning and
			// its publication directly, without sleeps or a real Claude process.
			m := &Manager{store: s, closed: true}
			var warnings []store.ManagedHistoryEntry
			m.completeHistoryPublisher = func(entry store.ManagedHistoryEntry) { warnings = append(warnings, entry) }
			lane := m.laneFor(binding.AgentID)
			runtime := &claudeFreshThresholdRuntime{tokens: tokens}
			require.True(t, lane.installEmbeddedRuntime(runtime, nil))
			_, ok := lane.beginEmbeddedReset()
			require.True(t, ok)
			lane.reopenEmbeddedResetAdmissions() // fresh wake runs before coordinator release
			model := &ai.Model{Provider: claudeCodeProvider, ID: "sonnet", ContextWindow: 1000}
			m.checkContextResetAfterTurn(ctx, binding, model, runtime)
			require.Len(t, warnings, 1, "fresh wake must visibly warn even while reset is running")
			require.NotNil(t, warnings[0].Presentation)
			require.Equal(t, "reminder", warnings[0].Presentation.Kind)
			require.NotNil(t, warnings[0].MessageContent)
			require.Contains(t, *warnings[0].MessageContent, "scratchpad")
			lane.endEmbeddedReset()

			// Cancel before any potential epoch write. A wrongly attempted reset
			// still interrupts the active runtime before checking cancellation.
			canceled, cancel := context.WithCancel(ctx)
			cancel()
			m.checkContextResetAfterTurn(canceled, binding, model, runtime)
			require.Zero(t, runtime.interrupts, "unchanged fresh context must not immediately reset again")
			m.checkContextResetAfterTurn(ctx, binding, model, runtime)
			require.Len(t, warnings, 1, "warning remains once per epoch")
			require.Zero(t, runtime.interrupts)
		})
	}
}

func TestClaudeFreshThresholdHonorsProviderModelOverride(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, filepath.Join(t.TempDir(), "override.db"), store.Options{})
	require.NoError(t, err)
	defer s.Close()
	binding := subagentTestParentWithModel(t, s, "override-claude", store.ManagedModel{Provider: claudeCodeProvider, ID: "sonnet"})
	require.NoError(t, s.SetContextThresholdConfig(ctx, store.ContextThresholdConfig{
		WarningPercent: 10, ResetPercent: 20,
		ModelThresholds: map[string]store.ModelContextThreshold{
			"sonnet":             {WarningPercent: 20, ResetPercent: 30},
			"claude-code/sonnet": {WarningPercent: 30, ResetPercent: 40},
		},
	}))
	m := &Manager{store: s, closed: true}
	warnings := 0
	m.completeHistoryPublisher = func(store.ManagedHistoryEntry) { warnings++ }
	lane := m.laneFor(binding.AgentID)
	runtime := &claudeFreshThresholdRuntime{tokens: 250}
	require.True(t, lane.installEmbeddedRuntime(runtime, nil))
	_, ok := lane.beginEmbeddedReset()
	require.True(t, ok)
	lane.reopenEmbeddedResetAdmissions()
	model := &ai.Model{Provider: claudeCodeProvider, ID: "sonnet", ContextWindow: 1000}
	m.checkContextResetAfterTurn(ctx, binding, model, runtime)
	require.Zero(t, warnings, "full provider/model override wins over bare ID and global")
	runtime.tokens = 350
	m.checkContextResetAfterTurn(ctx, binding, model, runtime)
	require.Equal(t, 1, warnings, "effective warning threshold must apply during fresh wake")
	lane.endEmbeddedReset()
}

func TestClaudeFreshThresholdGuardRearmsOnlyWithHeadroomOrNewBinding(t *testing.T) {
	for _, change := range []string{"growth", "exact_boundary", "below_boundary", "usable_config", "unusable_config", "new_epoch", "new_session"} {
		t.Run(change, func(t *testing.T) {
			ctx := context.Background()
			s, err := store.Open(ctx, filepath.Join(t.TempDir(), "rearm.db"), store.Options{})
			require.NoError(t, err)
			defer s.Close()
			binding := subagentTestParentWithModel(t, s, "rearm-claude", store.ManagedModel{Provider: claudeCodeProvider, ID: "sonnet"})
			require.NoError(t, s.SetContextThresholdConfig(ctx, store.ContextThresholdConfig{WarningPercent: 10, ResetPercent: 20}))
			m := &Manager{store: s, closed: true}
			lane := m.laneFor(binding.AgentID)
			runtime := &claudeFreshThresholdRuntime{tokens: 250}
			require.True(t, lane.installEmbeddedRuntime(runtime, nil))
			_, ok := lane.beginEmbeddedReset()
			require.True(t, ok)
			lane.reopenEmbeddedResetAdmissions()
			model := &ai.Model{Provider: claudeCodeProvider, ID: "sonnet", ContextWindow: 1000}
			m.checkContextResetAfterTurn(ctx, binding, model, runtime)
			lane.endEmbeddedReset()
			require.NotNil(t, lane.claudeFreshResetGuard)

			wantReset := false
			switch change {
			case "growth":
				runtime.tokens = 300
			case "exact_boundary":
				runtime.tokens = 200
			case "below_boundary":
				runtime.tokens = 199
				m.checkContextResetAfterTurn(ctx, binding, model, runtime)
				require.Nil(t, lane.claudeFreshResetGuard)
				runtime.tokens = 200
				wantReset = true
			case "usable_config":
				require.NoError(t, s.SetContextThresholdConfig(ctx, store.ContextThresholdConfig{WarningPercent: 10, ResetPercent: 30}))
				m.checkContextResetAfterTurn(ctx, binding, model, runtime)
				require.Nil(t, lane.claudeFreshResetGuard)
				runtime.tokens = 300
				wantReset = true
			case "unusable_config":
				require.NoError(t, s.SetContextThresholdConfig(ctx, store.ContextThresholdConfig{WarningPercent: 10, ResetPercent: 25}))
			case "new_epoch":
				binding.Epoch++
				wantReset = true
			case "new_session":
				binding.SessionID += "-replacement"
				wantReset = true
			}
			canceled, cancel := context.WithCancel(ctx)
			cancel()
			m.checkContextResetAfterTurn(canceled, binding, model, runtime)
			if wantReset {
				require.Equal(t, 1, runtime.interrupts, "rearmed threshold must attempt reset")
			} else {
				require.Zero(t, runtime.interrupts, "oversized baseline must remain suppressed")
			}
		})
	}
}

func TestClaudeFreshThresholdGuardDoesNotChangePi(t *testing.T) {
	// This fixture checks the synchronous threshold decision, not dispatch.
	m := &Manager{closed: true}
	binding := store.EmbeddedBinding{AgentID: "pi-parity", SessionID: "pi-session", Epoch: 1}
	lane := m.laneFor(binding.AgentID)
	runtime := &claudeFreshThresholdRuntime{tokens: 250}
	require.True(t, lane.installEmbeddedRuntime(runtime, nil))
	_, ok := lane.beginEmbeddedReset()
	require.True(t, ok)
	lane.reopenEmbeddedResetAdmissions()
	model := &ai.Model{Provider: "pi-test", ID: "model", ContextWindow: 1000}
	m.checkContextResetAfterTurn(context.Background(), binding, model, runtime)
	require.Nil(t, lane.claudeFreshResetGuard)
	require.False(t, lane.warningEmitted, "Pi still skips monitoring during reset wake")
	lane.endEmbeddedReset()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	m.checkContextResetAfterTurn(canceled, binding, model, runtime)
	require.Equal(t, 1, runtime.interrupts, "Pi retains ordinary absolute-threshold reset")
}
