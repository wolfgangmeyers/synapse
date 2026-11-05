package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

// This exercises the actual router executor and Python ledger, not manager
// admission. In particular it does not claim ops.worker replay is reachable.
func TestRetiredExecutorPythonLedgerBoundary(t *testing.T) {
	python, err := exec.LookPath("python3")
	require.NoError(t, err)
	tools, err := filepath.Abs(filepath.Join("..", "..", "..", "tools"))
	require.NoError(t, err)
	for _, status := range []string{"ok", "error"} {
		t.Run(status, func(t *testing.T) {
			home := t.TempDir()
			e := mailboxRouterRootControlExecutor{python: python, toolsDir: tools, managedHome: home, routerURL: "http://127.0.0.1:18775"}
			inv := managed.RootControlInvocation{RequestID: "saved", OperationID: "ops.worker", Arguments: map[string]any{"text": "<tag>&", "integer": 42, "float": 1.5, "small": 1e-7}, Fence: store.ManagedRuntimeFence{AgentID: "agent", SessionEpoch: 1}}
			// Generate the fingerprint with the production Python canonicalizer. The
			// JSON round trip is the same boundary as the executor's subprocess input.
			input, err := json.Marshal(map[string]any{"operation_id": inv.OperationID, "arguments": inv.Arguments, "fence": map[string]any{"agent_id": "agent", "pi_session_id": "", "session_epoch": 1, "process_pid": 0, "process_start_marker": "", "process_incarnation": "", "runtime_generation": "", "manifest_revision": 0, "manifest_hash": ""}})
			require.NoError(t, err)
			script := `import sys,json,hashlib,time,pathlib
sys.path.insert(0,sys.argv[1]); from managed_root_control import canonical
v=json.load(sys.stdin); result={'status':sys.argv[3], 'content':'historical', 'details':{}}
if sys.argv[3]=='error': result.update(error_code='OLD_ERROR',error_message='old error')
p=pathlib.Path(sys.argv[2])/'root-control'; p.mkdir(); (p/'requests.json').write_text(json.dumps({'version':1,'settings':{},'requests':{'saved':{'fingerprint':hashlib.sha256(canonical(v).encode()).hexdigest(),'recorded_at':time.time(),'result':result}}}))`
			cmd := exec.Command(python, "-c", script, tools, home, status)
			cmd.Stdin = bytes.NewReader(input)
			out, err := cmd.CombinedOutput()
			require.NoError(t, err, string(out))
			identity := managed.RootControlHandlerIdentity{OperationID: inv.OperationID}
			got, err := e.ReconcileRetiredRootControl(context.Background(), identity, inv)
			require.NoError(t, err)
			require.Equal(t, status, got.Status)
			require.Equal(t, "historical", got.Content)
			got, err = e.ReconcileRetiredRootControl(context.Background(), identity, inv)
			require.NoError(t, err)
			require.Equal(t, "historical", got.Content)
			inv.Arguments = map[string]any{"text": "different"}
			got, err = e.ReconcileRetiredRootControl(context.Background(), identity, inv)
			require.NoError(t, err)
			require.Equal(t, "ROOT_OPERATION_UNKNOWN", got.ErrorCode)
			inv.Arguments = map[string]any{"text": "<tag>&", "integer": 42, "float": 1.5, "small": 1e-7}
			inv.Fence.SessionEpoch++
			got, err = e.ReconcileRetiredRootControl(context.Background(), identity, inv)
			require.NoError(t, err)
			require.Equal(t, "ROOT_OPERATION_UNKNOWN", got.ErrorCode)
			// The manager must not call the host when arguments are lost: the
			// runner rejects null rather than returning a recovery result.
			inv.Arguments = nil
			_, err = e.ReconcileRetiredRootControl(context.Background(), identity, inv)
			require.Error(t, err)
		})
	}
}
