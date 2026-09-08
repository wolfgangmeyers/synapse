package managed

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func TestResetRetriesAReapedTargetReadinessFailureWithOneFreshSession(t *testing.T) {
	ctx := context.Background()
	durable, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = durable.Close() })
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	root, sessionRoot := t.TempDir(), ""
	sessionRoot = filepath.Join(root, "sessions")
	require.NoError(t, os.MkdirAll(sessionRoot, 0o700))
	sourceFile := filepath.Join(sessionRoot, "source.json")
	targetFile := filepath.Join(sessionRoot, "target.json")
	require.NoError(t, os.WriteFile(sourceFile, []byte("{}"), 0o600))
	require.NoError(t, os.WriteFile(targetFile, []byte("{}"), 0o600))
	processes := []*launchProcess{
		{identity: ProcessIdentity{PID: 301, StartMarker: "123e4567-e89b-42d3-a456-426614174001", Incarnation: "source"}, stopped: make(chan struct{})},
		{identity: ProcessIdentity{PID: 302, StartMarker: "123e4567-e89b-42d3-a456-426614174003", Incarnation: "failed-target"}, stopped: make(chan struct{})},
		{identity: ProcessIdentity{PID: 303, StartMarker: "123e4567-e89b-42d3-a456-426614174003", Incarnation: "retry-target"}, stopped: make(chan struct{})},
	}
	factory := &resetProcessFactory{processes: processes}
	rpcs := &resetRPCFactory{rpcs: []*resetRPC{
		{launchRPC: &launchRPC{}, file: sourceFile},
		{launchRPC: &launchRPC{failAt: "policy-command"}, file: targetFile},
		{launchRPC: &launchRPC{}, file: targetFile},
	}}
	ids := &resetTestIDs{values: []string{
		"123e4567-e89b-42d3-a456-426614174001", "123e4567-e89b-42d3-a456-426614174002",
		"123e4567-e89b-42d3-a456-426614174003", "123e4567-e89b-42d3-a456-426614174004", "123e4567-e89b-42d3-a456-426614174005", "123e4567-e89b-42d3-a456-426614174006",
	}}
	ops := &resetProcessOps{present: map[int]bool{301: true, 302: true, 303: true}}
	manager, err := NewManager(ManagerOptions{Store: durable, Config: Config{Enabled: false, SessionRoot: sessionRoot, AllowedWorkingDirectoryRoots: []string{root}}, Manifest: manifest, ProcessFactory: factory, RPCFactory: rpcs, CatalogSource: CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}), IDSource: ids, ProcessGroupLeaderVerifier: func(ProcessIdentity) bool { return true }, ProcessGroupOps: ops})
	require.NoError(t, err)
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	_, err = manager.Launch(ctx, LaunchRequest{Name: "retry-agent", Cwd: root, RequestID: "123e4567-e89b-42d3-a456-426614174021"})
	require.NoError(t, err)
	require.Eventually(t, func() bool { a, e := durable.ManagedAgent(ctx, "retry-agent"); return e == nil && a.AcceptingPrompts }, 3*time.Second, time.Millisecond)
	_, err = manager.Reset(ctx, ResetRequest{AgentID: "retry-agent", Method: "POST", CanonicalPath: "/v1/managed-agents/retry-agent/reset", RequestID: "123e4567-e89b-42d3-a456-426614174022", CanonicalBodyHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		a, e := durable.ManagedLifecycleAction(ctx, "123e4567-e89b-42d3-a456-426614174022")
		return e == nil && a.State == "failed" && a.TerminalCode == "RESET_TARGET_START_FAILED" && a.Retryable
	}, 3*time.Second, time.Millisecond)
	failed, err := durable.ManagedAgent(ctx, "retry-agent")
	require.NoError(t, err)
	require.Equal(t, "123e4567-e89b-42d3-a456-426614174003", failed.PiSessionID, "the failed action retains the exact target fence until retry verifies absence")
	failedAction, err := durable.ManagedLifecycleAction(ctx, "123e4567-e89b-42d3-a456-426614174022")
	require.NoError(t, err)
	require.NotNil(t, failedAction.TargetFence)
	require.NotEmpty(t, failedAction.TargetArtifactID)
	sourceStateCalls := len(rpcs.rpcs[0].calls)
	_, err = manager.Reset(ctx, ResetRequest{AgentID: "retry-agent", Method: "POST", CanonicalPath: "/v1/managed-agents/retry-agent/reset", RequestID: "123e4567-e89b-42d3-a456-426614174023", CanonicalBodyHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		a, e := durable.ManagedAgent(ctx, "retry-agent")
		return e == nil && a.LifecycleState == "running" && a.AcceptingPrompts && a.PiSessionID == "123e4567-e89b-42d3-a456-426614174003"
	}, 5*time.Second, time.Millisecond)
	retry, err := durable.ManagedLifecycleAction(ctx, "123e4567-e89b-42d3-a456-426614174023")
	require.NoError(t, err)
	require.Equal(t, "123e4567-e89b-42d3-a456-426614174022", retry.RetryOf)
	firstAttempt, err := durable.ManagedLifecycleAction(ctx, "123e4567-e89b-42d3-a456-426614174022")
	require.NoError(t, err)
	require.Nil(t, firstAttempt.TargetFence, "absence proof clears the retained all-or-null target fence before retry reservation")
	require.Equal(t, "target_start", firstAttempt.Stage)
	after, err := durable.ManagedAgent(ctx, "retry-agent")
	require.NoError(t, err)
	require.Equal(t, int64(2), after.SessionEpoch, "ResumeExisting retains the failed target epoch")
	require.Equal(t, sourceStateCalls, len(rpcs.rpcs[0].calls), "target-start retry performs no additional source RPC work")
	require.Len(t, factory.specs, 3)
	require.Equal(t, CreateNew, factory.specs[1].LaunchMode)
	require.Equal(t, ResumeExisting, factory.specs[2].LaunchMode)
	require.Equal(t, factory.specs[1].SessionID, factory.specs[2].SessionID)
	require.Equal(t, factory.specs[1].Environment, factory.specs[2].Environment)
}
