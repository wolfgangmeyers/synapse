package managed

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

func TestClaudeChildAnswerBlockAndAcknowledgement(t *testing.T) {
	cases := []struct{ name, script, status, result string }{
		{"same message separate text blocks", `{"turns":[[{"emit":{"type":"assistant","message":{"id":"msg-1","role":"assistant","model":"sonnet","content":[{"type":"text","text":"first "}]}}},{"emit":{"type":"assistant","message":{"id":"msg-1","role":"assistant","model":"sonnet","content":[{"type":"text","text":"second"}]}}}]]}`, "completed", "first second"},
		{"checked tool without acknowledgement", `{"skip_mcp_result":true,"turns":[[{"mcp_call":{"id":"read-1","name":"read","input":{"path":"note.txt"}}},{"text":"done"}]]}`, "failed", ""},
		{"acknowledged tool then answer", `{"turns":[[{"mcp_call":{"id":"read-1","name":"read","input":{"path":"note.txt"}}},{"text":"done"}]]}`, "completed", "done"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := claudeTestConfig(t, tc.script)
			require.NoError(t, os.WriteFile(filepath.Join(cfg.Cwd, "note.txt"), []byte("hello"), 0600))
			t.Setenv("MECHA_CLAUDE_BIN", cfg.Binary)
			for _, v := range cfg.Environment {
				if strings.HasPrefix(v, "FAKE_CLAUDE_") {
					k, val, _ := strings.Cut(v, "=")
					t.Setenv(k, val)
				}
			}
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "state.db")
			db, err := store.Open(ctx, path, store.Options{})
			require.NoError(t, err)
			defer db.Close()
			parent := subagentTestParent(t, db, "answer-parent")
			raw, err := sql.Open("sqlite", path)
			require.NoError(t, err)
			defer raw.Close()
			_, err = raw.Exec(`UPDATE managed_agents SET cwd=?, selected_model_provider='claude-code',selected_model_id='sonnet' WHERE agent_id=?`, cfg.Cwd, parent.AgentID)
			require.NoError(t, err)
			require.NoError(t, db.SetDefaultModel(ctx, &store.ManagedModel{Provider: "claude-code", ID: "sonnet"}))
			require.NoError(t, db.CreateManagedSubagent(ctx, parent, store.ManagedSubagentRecord{AgentID: "answer-child", ParentAgentID: parent.AgentID, Status: "running"}))
			_, err = raw.Exec(`UPDATE managed_agents SET selected_model_provider='claude-code',selected_model_id='sonnet' WHERE agent_id='answer-child'`)
			require.NoError(t, err)
			reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
			defer reg.Unregister()
			mgr := makeSubagentTestManager(t, db, reg, 1)
			defer mgr.Close(ctx)
			task := &subagentTask{parent: parent, childID: "answer-child", attempt: 1, status: "running", prompt: "test", timeout: 10 * time.Second}
			mgr.subagentsMu.Lock()
			mgr.subagentsActive[task.childID] = task
			mgr.subagentsRegistry[task.childID] = task
			mgr.subagentsMu.Unlock()
			go mgr.runSubagent(task)
			require.Eventually(t, func() bool {
				rec, e := db.ManagedSubagent(ctx, parent, task.childID)
				return e == nil && (IsTerminalSubagentStatus(rec.Status) || rec.Status == "failed")
			}, 8*time.Second, 20*time.Millisecond)
			rec, err := db.ManagedSubagent(ctx, parent, task.childID)
			require.NoError(t, err)
			require.Equal(t, tc.status, rec.Status, rec.Error)
			if tc.status == "completed" {
				require.Equal(t, tc.result, rec.ResultText)
			} else {
				require.Contains(t, rec.Error, "unacknowledged tool result")
				require.Empty(t, rec.ResultText, "unacknowledged answer must not settle")
			}
		})
	}
}
