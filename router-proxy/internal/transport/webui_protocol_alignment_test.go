package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Exercise real HTTP launch/read projections and the real WebUI strict guards.
// Node's built-in TypeScript stripping needs no frontend install or network.
func TestWebUIManagedProtocolAlignment(t *testing.T) {
	guardPath, err := filepath.Abs("../../../web/router-ui-react/src/protocol/guards.ts")
	require.NoError(t, err)
	for _, runtime := range []string{"embedded", "rpc"} {
		t.Run(runtime, func(t *testing.T) {
			f := newEmbeddedHTTPG2FixtureWithDefault(t, runtime == "embedded")
			status, launch := f.call(http.MethodPost, "/v1/managed-agents", "f1111111-1111-4111-8111-111111111111", fmt.Sprintf(`{"name":"api-root","cwd":%q}`, f.root))
			require.Equal(t, http.StatusAccepted, status, string(launch))
			var projections []json.RawMessage
			projections = append(projections, launch)
			for _, path := range []string{"/v1/managed-agents", "/v1/managed-agents/api-root"} {
				status, body := f.call(http.MethodGet, path, "", "")
				require.Equal(t, http.StatusOK, status, string(body))
				projections = append(projections, body)
			}
			snapshot, err := f.router.uiLive.snapshot(context.Background())
			require.NoError(t, err)
			live, err := json.Marshal(snapshot.frame())
			require.NoError(t, err)
			projections = append(projections, live)
			input, err := json.Marshal(projections)
			require.NoError(t, err)
			script := `
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
const {isManagedAgent, isUILiveFrame} = await import(process.argv[1]);
const [launch, list, detail, live] = JSON.parse(readFileSync(0, 'utf8'));
assert.equal(isManagedAgent(launch.agent), true, 'HTTP launch rejected');
assert.equal(isManagedAgent(detail.agent), true, 'HTTP detail rejected');
assert.equal(list.agents.length, 1);
assert.equal(list.agents.every(isManagedAgent), true, 'HTTP list rejected');
assert.equal(isUILiveFrame(live), true, 'live snapshot rejected');
assert.equal(isUILiveFrame({type:'managed_agent_delta', protocol:'mecha.web/v1', projection_version:2, base_version:1, agent_id:detail.agent.agent_id, changes:detail.agent}), true, 'live delta rejected');
assert.equal(detail.agent.runtime, process.argv[2]);
// Routers predating the runtime field must still parse.
const legacy = {...detail.agent}; delete legacy.runtime;
assert.equal(isManagedAgent(legacy), true);
for (const runtime of ['other', '', null, 1, {}]) assert.equal(isManagedAgent({...detail.agent, runtime}), false);
assert.equal(isManagedAgent({...detail.agent, unexpected:true}), false);
if (detail.agent.goal_state) {
 for (const patch of [{desired_revision:-1}, {applied_revision:1, desired_revision:0}, {projection_state:'other'}, {unexpected:true}])
  assert.equal(isManagedAgent({...detail.agent, goal_state:{...detail.agent.goal_state, ...patch}}), false);
 const goal = {...detail.agent.goal_state}; delete goal.outcome_counts;
 assert.equal(isManagedAgent({...detail.agent, goal_state:goal}), false);
}
console.log('HTTP launch/list/detail and live snapshot/delta accepted; legacy accepted; malformed fields rejected');
`
			cmd := exec.Command("node", "--experimental-strip-types", "--input-type=module", "-e", script, guardPath, runtime)
			cmd.Stdin = strings.NewReader(string(input))
			output, err := cmd.CombinedOutput()
			require.NoError(t, err, string(output))
			t.Log(string(output))
		})
	}
}
