package managed

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

// Exercises SDK dispatch and host persistence with the real Codex adapter. The
// host does not advertise mecha_worker.launch, so this covers unknown names,
// not selection of the SDK's worker alias.
func TestCodexUnknownAliasNamesHostLifecycle(t *testing.T) {
	ctx := context.Background()
	m, db, project, _, _ := securityBoundaryManager(t, securityDoneSSE)
	require.NoError(t, os.WriteFile(filepath.Join(project, "fixture.txt"), []byte("host-read-marker"), 0600))
	_, err := m.LaunchEmbeddedRoot(ctx, LaunchRequest{Name: "alias-host", Cwd: project, Runtime: "embedded", RequestID: "alias-host-launch"})
	require.NoError(t, err)
	owner := securityReady(t, m, db, "alias-host")
	saved, err := db.ManagedAgent(ctx, owner.binding.AgentID)
	require.NoError(t, err)
	saved.SelectedModel = store.ManagedModel{Provider: "openai-codex", ID: "gpt-6-sol"}
	var calls atomic.Int32
	m.codexHTTPTransport = codexTestRoundTrip(func(r *http.Request) (*http.Response, error) {
		phase := calls.Add(1)
		body, e := io.ReadAll(r.Body)
		require.NoError(t, e)
		var request map[string]any
		require.NoError(t, json.Unmarshal(body, &request))
		events := []string{`{"type":"response.created","response":{"id":"resp_alias"}}`}
		add := func(index int, name, args string) {
			events = append(events, fmt.Sprintf(`{"type":"response.output_item.added","output_index":%d,"item":{"type":"function_call","id":"fc_%d","call_id":"call_%d","name":%q}}`, index, index, index, name), fmt.Sprintf(`{"type":"response.output_item.done","output_index":%d,"item":{"type":"function_call","id":"fc_%d","call_id":"call_%d","name":%q,"arguments":%q}}`, index, index, index, name, args))
		}
		switch phase {
		case 1:
			for i := 0; i < 32; i++ {
				name := "mecha_worker_launch"
				if i == 0 {
					name = "mecha_worker.launch"
				} else {
					name = fmt.Sprintf("%s_%d", name, i)
				}
				add(i, name, `{}`)
			}
		case 2:
			require.Equal(t, 32, strings.Count(string(body), `"type":"function_call_output"`))
			require.Contains(t, strings.ToLower(string(body)), "unavailable")
			for i := 0; i < 32; i++ {
				_, e := db.RecoverEmbeddedEffect(ctx, owner.binding, fmt.Sprintf("call_%d|fc_%d", i, i))
				require.ErrorIs(t, e, sql.ErrNoRows)
			}
			add(32, "read", `{"path":"fixture.txt"}`)
		case 3:
			require.Contains(t, string(body), "host-read-marker")
			events = append(events, `{"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg_done","role":"assistant","content":[]}}`, `{"type":"response.output_text.delta","item_id":"msg_done","output_index":0,"content_index":0,"delta":"done"}`, `{"type":"response.output_item.done","output_index":0,"item":{"type":"message","id":"msg_done","role":"assistant","content":[{"type":"output_text","text":"done"}]}}`)
		default:
			t.Errorf("unexpected provider request %d", phase)
		}
		events = append(events, `{"type":"response.completed","response":{"id":"resp_alias","status":"completed"}}`)
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: " + strings.Join(events, "\n\ndata: ") + "\n\n")), Request: r}, nil
	})
	err = m.runEmbeddedSessionPrompt(context.WithValue(ctx, embeddedOwnerContextKey{}, owner), *saved, "test alias isolation")
	require.NoError(t, err)
	require.EqualValues(t, 3, calls.Load())
	after, err := db.ReopenEmbedded(ctx, owner.binding)
	require.NoError(t, err)
	count := 0
	for _, entry := range after.Entries {
		if entry.Kind != "toolResult" {
			continue
		}
		msg, e := ai.UnmarshalMessage(entry.Body)
		require.NoError(t, e)
		result, ok := msg.(ai.ToolResultMessage)
		if ok && (result.ToolName == "mecha_worker.launch" || strings.HasPrefix(result.ToolName, "mecha_worker_launch")) {
			require.True(t, result.IsError)
			require.Contains(t, strings.ToLower(fmt.Sprint(result.Content)), "unavailable")
			count++
		}
	}
	require.Equal(t, 32, count)
}
