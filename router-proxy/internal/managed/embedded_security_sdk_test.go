package managed

import (
	"context"
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

// Real pinned SDK, Codex adapter, tools, owner/intent gates and SQLite. Only
// HTTP is synthetic. Returns the actual checked SDK tool result, or nil when
// the caller explicitly expects an uncompleted addressed invocation to stay UNKNOWN.
func securitySDKTool(t *testing.T, m *Manager, db *store.Store, owner embeddedOwner, name string, args map[string]any, wantUnknown ...bool) *ai.ToolResultMessage {
	t.Helper()
	ctx := context.Background()
	saved, err := db.ManagedAgent(ctx, owner.binding.AgentID)
	require.NoError(t, err)
	before, err := db.ReopenEmbedded(ctx, owner.binding)
	require.NoError(t, err)
	var calls atomic.Int32
	m.codexHTTPTransport = codexTestRoundTrip(func(r *http.Request) (*http.Response, error) {
		phase := calls.Add(1)
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.NotContains(t, string(body), "security-private-secret-sentinel")
		require.Equal(t, embeddedCodexResponsesURL, r.URL.String())
		script := securityDoneSSE
		if phase == 1 {
			encoded, err := json.Marshal(args)
			require.NoError(t, err)
			events := []string{
				`{"type":"response.created","response":{"id":"resp_security"}}`,
				fmt.Sprintf(`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_security_%s","call_id":"call_security_%s","name":%q}}`, name, name, name),
				fmt.Sprintf(`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"fc_security_%s","call_id":"call_security_%s","name":%q,"arguments":%q}}`, name, name, name, string(encoded)),
				`{"type":"response.completed","response":{"id":"resp_security","status":"completed"}}`,
			}
			script = "data: " + strings.Join(events, "\n\ndata: ") + "\n\n"
		} else {
			require.EqualValues(t, 2, phase, "one checked continuation only")
			require.Contains(t, string(body), "function_call_output")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(script)), Request: r}, nil
	})
	err = m.runEmbeddedSessionPrompt(context.WithValue(ctx, embeddedOwnerContextKey{}, owner), *saved, "execute the requested project tool")
	unknown := len(wantUnknown) == 1 && wantUnknown[0]
	if unknown {
		require.ErrorContains(t, err, "managed tool outcome UNKNOWN: no producer completion")
		require.EqualValues(t, 1, calls.Load(), "no continuation after an uncompleted invocation")
		effect, effectErr := db.RecoverEmbeddedEffect(ctx, owner.binding, "call_security_"+name+"|fc_security_"+name)
		require.NoError(t, effectErr)
		require.Equal(t, "unknown", effect.State)
		require.Empty(t, effect.Result)
	} else {
		require.NoError(t, err)
		require.EqualValues(t, 2, calls.Load())
	}
	after, err := db.ReopenEmbedded(ctx, owner.binding)
	require.NoError(t, err)
	encoded, err := json.Marshal(after)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "security-private-secret-sentinel")
	for _, entry := range after.Entries[len(before.Entries):] {
		if entry.Kind != "toolResult" {
			continue
		}
		message, err := ai.UnmarshalMessage(entry.Body)
		require.NoError(t, err)
		result, ok := message.(ai.ToolResultMessage)
		require.True(t, ok)
		if result.ToolName == name {
			require.False(t, unknown, "UNKNOWN must not mint a checked tool result")
			return &result
		}
	}
	if unknown {
		return nil
	}
	t.Fatal("missing checked SDK tool result")
	return nil
}

func TestEmbeddedDisjointProjectRealSDKReadGrepEditWriteAndWorkerAdmission(t *testing.T) {
	for _, root := range []bool{true, false} {
		t.Run(fmt.Sprintf("root-%t", root), func(t *testing.T) {
			ctx := context.Background()
			m, db, project, credentialPath, _ := securityBoundaryManager(t, securityDoneSSE)
			original, err := os.ReadFile(credentialPath)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(project, "note.txt"), []byte("public-before\n"), 0600))
			request := LaunchRequest{Name: "sdk-project", Cwd: project, Runtime: "embedded", RequestID: "sdk-project-launch"}
			if root {
				_, err = m.LaunchEmbeddedRoot(ctx, request)
			} else {
				_, err = m.Launch(ctx, request)
			}
			require.NoError(t, err)
			owner := securityReady(t, m, db, request.Name)
			read := securitySDKTool(t, m, db, owner, "read", map[string]any{"path": "note.txt"})
			require.False(t, read.IsError)
			readBytes, err := json.Marshal(read.Content)
			require.NoError(t, err)
			require.Contains(t, string(readBytes), "public-before")
			grep := securitySDKTool(t, m, db, owner, "bash", map[string]any{"command": "grep -F -n -- public-before note.txt"})
			require.False(t, grep.IsError)
			grepBytes, err := json.Marshal(grep.Content)
			require.NoError(t, err)
			require.Contains(t, string(grepBytes), "public-before")
			edit := securitySDKTool(t, m, db, owner, "edit", map[string]any{"path": "note.txt", "edits": []any{map[string]any{"oldText": "public-before", "newText": "public-after"}}})
			require.False(t, edit.IsError)
			written := securitySDKTool(t, m, db, owner, "write", map[string]any{"path": "created.txt", "content": "created-public-file"})
			require.False(t, written.IsError)
			content, err := os.ReadFile(filepath.Join(project, "note.txt"))
			require.NoError(t, err)
			require.Equal(t, "public-after\n", string(content))
			content, err = os.ReadFile(filepath.Join(project, "created.txt"))
			require.NoError(t, err)
			require.Equal(t, "created-public-file", string(content))
			unchanged, err := os.ReadFile(credentialPath)
			require.NoError(t, err)
			require.Equal(t, original, unchanged)
			for _, suffix := range []string{"", "-wal"} {
				bytes, err := os.ReadFile(filepath.Join(filepath.Dir(project), "router.db") + suffix)
				if os.IsNotExist(err) {
					continue
				}
				require.NoError(t, err)
				require.NotContains(t, string(bytes), "security-private-secret-sentinel")
			}
		})
	}
}
