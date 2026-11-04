package managed_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

// CC-013: real HTTP goal control, managed sweeps, SQLite receipts and fake CLI.
// No model transport is invoked. Browser coverage exercises the configured
// loopback Codex transport and successful NOT_DONE/DONE recovery separately.
func TestClaudeIdleGoalDoesNotBypassUnavailableEvaluator(t *testing.T) {
	for _, providerError := range []bool{false, true} {
		name := "missing-default"
		if providerError {
			name = "evaluator-error"
		}
		t.Run(name, func(t *testing.T) {
			bin, err := filepath.Abs("../../../tests/fixtures/fake-claude/claude.mjs")
			require.NoError(t, err)
			root := t.TempDir()
			record := filepath.Join(root, "record.jsonl")
			script := filepath.Join(root, "script.json")
			require.NoError(t, os.WriteFile(script, []byte(`{"turns":[[{"text":"first goal final"}],[{"text":"user recovery final"}],[{"text":"new goal final"}]]}`), 0600))
			t.Setenv("MECHA_CLAUDE_BIN", bin)
			t.Setenv("FAKE_CLAUDE_RECORD", record)
			t.Setenv("FAKE_CLAUDE_SCRIPT", script)
			h := newBehavioralHarness(t, nil, func(opts *managed.ManagerOptions) {
				if providerError {
					opts.EmbeddedGoalEvaluator = func(context.Context, store.EmbeddedGoalEvaluationKey) (string, string, error) {
						return "", "", errors.New("deterministic evaluator unavailable")
					}
				}
			})
			launched, err := h.manager.Launch(h.ctx, managed.LaunchRequest{Name: "claude-idle-goal", Cwd: h.root, Runtime: "embedded", Model: &managed.Model{Provider: "claude-code", ID: "sonnet"}, RequestID: "22222222-2222-4222-8222-222222222222"})
			require.NoError(t, err)
			id := launched.Agent.AgentID
			require.Eventually(t, func() bool {
				a, e := h.db.ManagedAgent(h.ctx, id)
				return e == nil && a.LifecycleState == "running" && a.ActivityState == "idle"
			}, 10*time.Second, 10*time.Millisecond)
			raw := h.sqlDB(t)
			setGoal := func(goal string) {
				body, err := json.Marshal(map[string]string{"action": "set", "goal": goal})
				require.NoError(t, err)
				req, err := http.NewRequest("PUT", h.httpURL+"/v1/managed-agents/"+id+"/goal", strings.NewReader(string(body)))
				require.NoError(t, err)
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("X-Request-ID", uuid.NewString())
				res, err := http.DefaultClient.Do(req)
				require.NoError(t, err)
				defer res.Body.Close()
				require.Equal(t, http.StatusOK, res.StatusCode)
			}
			inputCount := func() int {
				data, _ := os.ReadFile(record)
				n := 0
				for _, line := range strings.Split(string(data), "\n") {
					var row struct {
						Kind string `json:"kind"`
						Line string `json:"line"`
					}
					if json.Unmarshal([]byte(line), &row) == nil && row.Kind == "stdin" {
						var input struct {
							Type string `json:"type"`
						}
						if json.Unmarshal([]byte(row.Line), &input) == nil && input.Type == "user" {
							n++
						}
					}
				}
				return n
			}
			waitUnavailable := func(count int) {
				require.Eventually(t, func() bool {
					var n int
					_ = raw.QueryRow(`SELECT count(*) FROM managed_complete_history WHERE agent_id=? AND message LIKE '%goal evaluation unavailable%'`, id).Scan(&n)
					a, e := h.db.ManagedAgent(h.ctx, id)
					return n >= count && e == nil && a.ActivityState == "idle"
				}, 10*time.Second, 10*time.Millisecond)
			}
			setGoal("First goal")
			waitUnavailable(1)
			require.Equal(t, 1, inputCount(), "initial pending goal must wake once")
			require.NoError(t, h.manager.ResumeIdleEmbeddedGoals(h.ctx))
			require.Never(t, func() bool { return inputCount() != 1 }, 300*time.Millisecond, 10*time.Millisecond, "idle sweep must not invent NOT_DONE")
			var continuations int
			require.NoError(t, raw.QueryRow(`SELECT count(*) FROM embedded_goal_continuations WHERE agent_id=?`, id).Scan(&continuations))
			require.Zero(t, continuations)

			client := h.registerWebSocketClient(t, "idle-user", "idle-user-instance")
			defer client.Close(websocket.StatusNormalClosure, "done")
			require.Equal(t, http.StatusCreated, h.postMessage(t, "idle-user-mail", "idle-user", id, "User input must still run"))
			waitUnavailable(2)
			require.Equal(t, 2, inputCount())
			setGoal("Replacement goal must still wake")
			waitUnavailable(3)
			require.Equal(t, 3, inputCount())
		})
	}
}
