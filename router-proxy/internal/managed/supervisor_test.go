package managed

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

type readinessRPC struct {
	calls    []string
	active   []string
	envelope map[string]any
}

func (r *readinessRPC) GetState(context.Context) (map[string]any, error) {
	r.calls = append(r.calls, "get_state")
	return map[string]any{"sessionId": "session"}, nil
}
func (r *readinessRPC) GetCommands(context.Context) ([]string, error) {
	r.calls = append(r.calls, "get_commands")
	return append(append([]string{}, hiddenCommands...), legacyCommands...), nil
}
func (r *readinessRPC) Bootstrap(_ context.Context, envelope map[string]any) error {
	r.calls = append(r.calls, "bootstrap")
	r.envelope = envelope
	return nil
}
func (r *readinessRPC) WaitForReceipt(context.Context, string, string, int, int64, string) error {
	r.calls = append(r.calls, "receipt")
	return nil
}
func (r *readinessRPC) GetActiveTools(context.Context) ([]string, error) {
	r.calls = append(r.calls, "get_active_tools")
	return r.active, nil
}
func (r *readinessRPC) Prompt(context.Context, string) error {
	r.calls = append(r.calls, "prompt")
	return nil
}
func (r *readinessRPC) Close() error { return nil }

func TestReadinessGateOrdersEveryProofBeforePrompt(t *testing.T) {
	path := filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json")
	manifest, _, err := LoadManifest(path)
	require.NoError(t, err)
	rpc := &readinessRPC{active: expectedManagedActiveTools(manifest)}
	require.NoError(t, readinessGate(context.Background(), rpc, manifest, "session", 1, "generation"))
	require.Equal(t, []string{"get_state", "get_commands", "bootstrap", "receipt", "get_active_tools"}, rpc.calls)
	require.Equal(t, "mecha.bootstrap/v1", rpc.envelope["protocol"])
	require.Equal(t, manifest.Raw, rpc.envelope["desired_manifest"])
	require.Equal(t, manifest.CanonicalSHA256, rpc.envelope["manifest_sha256"])
}

func TestReadinessGateRejectsExtraCommandOrActiveTool(t *testing.T) {
	commands := append(append([]string{}, hiddenCommands...), legacyCommands...)
	commands = append(commands, hiddenCommands[0])
	require.Error(t, requireManagedCommands(commands))
	commands = append(append([]string{}, hiddenCommands...), legacyCommands...)
	commands[0] = "/" + commands[0] + ":2"
	require.Error(t, requireManagedCommands(commands))
	path := filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json")
	manifest, _, err := LoadManifest(path)
	require.NoError(t, err)
	rpc := &readinessRPC{active: expectedManagedActiveTools(manifest)[:18]}
	require.Error(t, readinessGate(context.Background(), rpc, manifest, "session", 1, "generation"))
	require.NotContains(t, rpc.calls, "prompt")
}
