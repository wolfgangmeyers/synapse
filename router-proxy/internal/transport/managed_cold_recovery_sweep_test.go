package transport

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

type coldRecoverySweepProcess struct {
	identity managed.ProcessIdentity
	done     chan struct{}
	once     sync.Once
}

func (p *coldRecoverySweepProcess) Identity() managed.ProcessIdentity { return p.identity }
func (p *coldRecoverySweepProcess) Stdin() io.WriteCloser             { return coldRecoverySweepWriteCloser{} }
func (p *coldRecoverySweepProcess) Stdout() io.ReadCloser             { return io.NopCloser(strings.NewReader("")) }
func (p *coldRecoverySweepProcess) Wait() error                       { <-p.done; return nil }
func (p *coldRecoverySweepProcess) Stop(context.Context) error {
	p.once.Do(func() { close(p.done) })
	return nil
}

type coldRecoverySweepWriteCloser struct{}

func (coldRecoverySweepWriteCloser) Write(value []byte) (int, error) { return len(value), nil }
func (coldRecoverySweepWriteCloser) Close() error                    { return nil }

type coldRecoverySweepFactory struct {
	mu      sync.Mutex
	process managed.Process
	specs   []managed.CommandSpec
	rpc     *coldRecoverySweepRPC
}

func (f *coldRecoverySweepFactory) Start(_ context.Context, spec managed.CommandSpec) (managed.Process, error) {
	f.mu.Lock()
	f.specs = append(f.specs, spec)
	f.mu.Unlock()
	f.rpc.mu.Lock()
	f.rpc.session = spec.SessionID
	f.rpc.mu.Unlock()
	return f.process, nil
}

func (f *coldRecoverySweepFactory) specsSnapshot() []managed.CommandSpec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]managed.CommandSpec(nil), f.specs...)
}

type coldRecoverySweepRPC struct {
	mu      sync.Mutex
	session string
	active  []string
}

func (r *coldRecoverySweepRPC) GetState(context.Context) (map[string]any, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return map[string]any{"sessionId": r.session}, nil
}
func (r *coldRecoverySweepRPC) GetCommands(context.Context) ([]string, error) {
	return []string{
		"mecha-bootstrap-v1", "mecha-install-tools-v1", "mecha-control-result-v1", "mecha-mailbox-deliver-v1",
		"mecha-policy-apply-v1", "mecha-child-snapshot-v1", "mecha-post-compaction-notification-v1",
		"goal", "goal-lock", "goal-snooze", "goal-snooze-turn-limit", "goal-snooze-budget", "goal-snooze-budget-refill",
		"mecha-parent-message", "inline-tools", "mecha-reply",
	}, nil
}
func (*coldRecoverySweepRPC) Bootstrap(context.Context, map[string]any) error { return nil }
func (*coldRecoverySweepRPC) WaitForReceipt(context.Context, string, string, int, int64, string) error {
	return nil
}
func (r *coldRecoverySweepRPC) GetActiveTools(context.Context) ([]string, error) {
	return append([]string(nil), r.active...), nil
}
func (*coldRecoverySweepRPC) Prompt(context.Context, string) error { return nil }
func (*coldRecoverySweepRPC) Abort(context.Context) error          { return nil }
func (*coldRecoverySweepRPC) Close() error                         { return nil }
func (*coldRecoverySweepRPC) ApplyPolicy(context.Context, map[string]any) (map[string]any, error) {
	return map[string]any{"success": true, "command": "prompt"}, nil
}
func (*coldRecoverySweepRPC) WaitForPolicyApplied(_ context.Context, generation string, epoch int64) (map[string]any, error) {
	return map[string]any{
		"protocol":           "mecha.policy/v1",
		"runtime_generation": generation,
		"session_epoch":      epoch,
		"applied": map[string]any{
			"inline_enabled":      false,
			"inline_credits":      map[string]any{},
			"recall_content":      "",
			"settled_enabled":     false,
			"goal_lock_enabled":   false,
			"compaction_deferred": false,
		},
	}, nil
}
func (*coldRecoverySweepRPC) RequestChildSnapshot(context.Context, map[string]any) (map[string]any, error) {
	return map[string]any{"success": true, "command": "prompt"}, nil
}
func (r *coldRecoverySweepRPC) WaitForChildSnapshot(context.Context) (map[string]any, error) {
	r.mu.Lock()
	session := r.session
	r.mu.Unlock()
	return map[string]any{"protocol": "mecha.children/v1", "snapshot_id": "sweep-snapshot", "owner_session_id": session, "sequence": int64(0), "runs": []any{}}, nil
}

type coldRecoverySweepRPCFactory struct{ rpc *coldRecoverySweepRPC }

func (f coldRecoverySweepRPCFactory) Open(context.Context, managed.Process) (managed.RPC, error) {
	return f.rpc, nil
}

type coldRecoverySweepOps struct{ oldPID, targetPID int }

func (o coldRecoverySweepOps) Probe(identity managed.ProcessIdentity) managed.ProcessProbe {
	if identity.PID == o.oldPID {
		return managed.ProcessProbe{State: managed.ProcessAbsent}
	}
	if identity.PID == o.targetPID {
		return managed.ProcessProbe{State: managed.ProcessOwned}
	}
	return managed.ProcessProbe{State: managed.ProcessUnknown}
}
func (coldRecoverySweepOps) CloseProcessInput(managed.Process) error { return nil }
func (coldRecoverySweepOps) SignalVerifiedProcessGroup(managed.ProcessIdentity, syscall.Signal) error {
	return nil
}
func (coldRecoverySweepOps) SignalVerifiedProcessGroupAfterLeaderExit(managed.ProcessIdentity) error {
	return nil
}
func (coldRecoverySweepOps) WaitForProcessGroupEmpty(context.Context, managed.ProcessIdentity, <-chan error) managed.ProcessProbe {
	return managed.ProcessProbe{State: managed.ProcessAbsent}
}

func TestRouterSweepRecoversManagedColdStartAtPersistedDeadline(t *testing.T) {
	ctx := context.Background()
	clock := deterministic.NewFixedClock(time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC))
	root, err := filepath.Abs(t.TempDir())
	require.NoError(t, err)
	home, err := filepath.Abs(t.TempDir())
	require.NoError(t, err)
	sessionRoot := filepath.Join(home, "sessions")
	piAgentDir := filepath.Join(home, ".pi", "agent")
	require.NoError(t, os.MkdirAll(sessionRoot, 0o700))
	require.NoError(t, os.MkdirAll(piAgentDir, 0o700))
	manifestPath, err := filepath.Abs(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	manifest, _, err := managed.LoadManifest(manifestPath)
	require.NoError(t, err)
	durable, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{Clock: clock})
	require.NoError(t, err)
	defer durable.Close()

	config := managed.Config{
		Enabled: true, PiExecutable: "/bin/sh", ManagedHome: home, PiAgentDir: piAgentDir, SessionRoot: sessionRoot,
		RouterURL: "http://127.0.0.1:18775", ManifestPath: manifestPath, MechaRPCExtension: manifestPath,
		PiSubagentsExtension: manifestPath, AllowedWorkingDirectoryRoots: []string{root}, CanonicalQuietRecipientID: "supervisor",
	}
	process := &coldRecoverySweepProcess{identity: managed.ProcessIdentity{PID: 2002, StartMarker: "target-start", Incarnation: "target-incarnation"}, done: make(chan struct{})}
	active := append([]string{"read", "bash", "edit", "write", "subagent", "get_subagent_result", "steer_subagent"}, make([]string, 0, len(manifest.Tools))...)
	for _, tool := range manifest.Tools {
		active = append(active, tool.Name)
	}
	sort.Strings(active)
	rpc := &coldRecoverySweepRPC{active: active}
	factory := &coldRecoverySweepFactory{process: process, rpc: rpc}
	manager, err := managed.NewManager(managed.ManagerOptions{
		Store: durable, Config: config, Manifest: manifest, Clock: clock,
		IDSource: deterministic.NewSequenceIDSource("target-generation"), ProcessFactory: factory,
		RPCFactory: coldRecoverySweepRPCFactory{rpc: rpc}, CatalogSource: managed.CatalogFromModels(managed.Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
		ProcessGroupLeaderVerifier: func(managed.ProcessIdentity) bool { return true }, ProcessGroupOps: coldRecoverySweepOps{oldPID: 2001, targetPID: 2002},
	})
	require.NoError(t, err)
	defer manager.Close(ctx)

	agent := store.ManagedAgentRecord{
		AgentID: "sweep-agent", Name: "sweep-agent", SlotHeld: true, Cwd: root,
		SelectedModel: store.ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-terra"}, DesiredState: "running",
		LifecycleState: "starting", ActivityState: "starting", PiSessionID: "original-session", SessionEpoch: 1,
		ProcessPID: 2001, ProcessStartMarker: "old-start", ProcessIncarnation: "old-incarnation", RuntimeGeneration: "old-generation",
		ManifestRevision: manifest.Revision, ManifestHash: manifest.CanonicalSHA256,
	}
	op := store.ManagedOperation{OperationID: "123e4567-e89b-42d3-a456-426614174101", Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "123e4567-e89b-42d3-a456-426614174101", CanonicalBodyHash: "body", State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
	require.NoError(t, durable.ReserveManagedLaunch(ctx, agent, op))
	require.NoError(t, durable.UpdateManagedAgentStarting(ctx, agent.AgentID, agent.PiSessionID, store.ProcessIdentityRecord{PID: agent.ProcessPID, StartMarker: agent.ProcessStartMarker, Incarnation: agent.ProcessIncarnation}, agent.RuntimeGeneration))
	fence := store.ManagedRuntimeFence{AgentID: agent.AgentID, PiSessionID: agent.PiSessionID, SessionEpoch: agent.SessionEpoch, ProcessPID: agent.ProcessPID, ProcessStartMarker: agent.ProcessStartMarker, ProcessIncarnation: agent.ProcessIncarnation, RuntimeGeneration: agent.RuntimeGeneration, ManifestRevision: agent.ManifestRevision, ManifestHash: agent.ManifestHash}
	require.NoError(t, durable.ConfirmManagedPolicyApplied(ctx, fence, store.DefaultManagedReadinessPolicy()))
	canonical := []byte(`{"owner_session_id":"original-session","protocol":"mecha.children/v1","runs":[],"sequence":0,"snapshot_id":"seed"}`)
	sum := sha256.Sum256(canonical)
	require.NoError(t, durable.ConfirmManagedChildSnapshot(ctx, fence, store.ManagedChildSnapshot{SnapshotID: "seed", OwnerSessionID: agent.PiSessionID, Sequence: 0, Canonical: canonical, SHA256: hex.EncodeToString(sum[:])}))
	require.NoError(t, durable.MarkManagedReady(ctx, agent.AgentID))
	require.NoError(t, durable.MarkManagedOperationTerminal(ctx, op.OperationID))
	recipeEnv := make([]string, 0)
	for _, value := range managed.ChildEnvironment(config) {
		if !strings.HasPrefix(value, "MECHA_ROUTER_URL=") {
			recipeEnv = append(recipeEnv, value)
		}
	}
	recipe, err := store.ManagedLaunchRecipeDigest(store.ManagedLaunchRecipe{PiExecutable: config.PiExecutable, SessionRoot: config.SessionRoot, Cwd: agent.Cwd, Provider: agent.SelectedModel.Provider, ModelID: agent.SelectedModel.ID, MechaRPCExtension: config.MechaRPCExtension, PiSubagentsExtension: config.PiSubagentsExtension, Environment: recipeEnv})
	require.NoError(t, err)
	require.NoError(t, durable.PutManagedLaunchRecipe(ctx, agent.AgentID, recipe))
	_, err = durable.RecordManagedUnexpectedExit(ctx, fence, "", "test process exited")
	require.NoError(t, err)

	router, err := New(Options{Store: durable, Clock: clock, Managed: manager})
	require.NoError(t, err)
	require.NoError(t, router.runSweep(ctx))
	require.Empty(t, factory.specsSnapshot(), "periodic recovery must honor the persisted retry deadline")
	require.NoError(t, clock.Advance(time.Second-time.Millisecond))
	require.NoError(t, router.runSweep(ctx))
	require.Empty(t, factory.specsSnapshot(), "recovery must wait for the exact deadline")
	require.NoError(t, clock.Advance(time.Millisecond))
	require.NoError(t, router.runSweep(ctx))

	specs := factory.specsSnapshot()
	require.Len(t, specs, 1)
	require.Equal(t, managed.ResumeExisting, specs[0].LaunchMode)
	require.Equal(t, agent.PiSessionID, specs[0].SessionID)
	current, err := durable.ManagedAgent(ctx, agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, "running", current.LifecycleState)
	require.Equal(t, "idle", current.ActivityState)
	require.True(t, current.AcceptingPrompts)
	require.NoError(t, router.runSweep(ctx))
	require.Len(t, factory.specsSnapshot(), 1)
}
