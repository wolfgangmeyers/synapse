package managed

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClaudeMCPMixedBatchExecutesOnlyCheckedCall(t *testing.T) {
	db, binding, _, _ := wakePersistenceFixtureWithClaim(t, false)
	s := (&Manager{store: db}).ClaudeCodeMCP()
	var calls atomic.Int32
	token, revoke, err := s.Issue(context.Background(), ClaudeCodeMCPGrant{
		Binding: binding, Owner: "wake-fixture", Incarnation: "batch",
		Tools: []ClaudeCodeMCPTool{{Name: "scratchpad", InputSchema: json.RawMessage(`{"type":"object"}`)}},
		ExecuteChecked: func(_ context.Context, call ClaudeCodeMCPCall) (json.RawMessage, error) {
			calls.Add(1)
			require.Equal(t, "checked", call.ToolUseID)
			return json.RawMessage(`{"content":[]}`), nil
		},
	})
	require.NoError(t, err)
	defer revoke()
	body := `[{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"scratchpad","arguments":{}}},{"jsonrpc":"2.0","id":2,"method":"unsupported"},{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"scratchpad","arguments":{},"_meta":{"claudecode/toolUseId":"checked"}}}]`
	r := httptest.NewRequest("POST", "/", bytes.NewBufferString(body))
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	require.Equal(t, 200, w.Code)
	var results []struct {
		ID    int `json:"id"`
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
		Result json.RawMessage `json:"result"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &results))
	require.Len(t, results, 3)
	require.Equal(t, 1, results[0].ID)
	require.NotNil(t, results[0].Error)
	require.Equal(t, -32602, results[0].Error.Code)
	require.Equal(t, 2, results[1].ID)
	require.NotNil(t, results[1].Error)
	require.Equal(t, -32601, results[1].Error.Code)
	require.Equal(t, 3, results[2].ID)
	require.Nil(t, results[2].Error)
	require.JSONEq(t, `{"content":[]}`, string(results[2].Result))
	require.EqualValues(t, 1, calls.Load())
}
