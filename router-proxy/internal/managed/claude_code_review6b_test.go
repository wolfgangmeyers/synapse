package managed

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

// CC-011: enter through the actual manager reset coordinator, fresh epoch,
// process adapter and runner epilogue, not checkContextResetAfterTurn directly.
// The warning must be published after settlement but before coordinator release.
func TestClaudeReview6bManagerResetWakeThreshold(t *testing.T) {
	for _, stopError := range []bool{false, true} {
		t.Run(fmt.Sprint(stopError), func(t *testing.T) {
			ctx := context.Background()
			result := map[string]interface{}{"type": "result", "subtype": "success"}
			if stopError {
				result = map[string]interface{}{"type": "result", "subtype": "error_during_execution", "is_error": true, "errors": []string{"deterministic stop"}}
			}
			// 250k exceeds the real Claude 1M window's configured 20% boundary.
			script, err := json.Marshal(map[string]interface{}{"turns": [][]interface{}{{map[string]interface{}{"emit": map[string]interface{}{"type": "assistant", "message": map[string]interface{}{"id": "fresh", "role": "assistant", "model": "sonnet", "content": []interface{}{map[string]interface{}{"type": "text", "text": "fresh wake settled"}}, "usage": map[string]interface{}{"input_tokens": 250000}}}}, map[string]interface{}{"emit": result}}}})
			require.NoError(t, err)
			cfg := claudeTestConfig(t, string(script))
			t.Setenv("MECHA_CLAUDE_BIN", cfg.Binary)
			t.Setenv("FAKE_CLAUDE_SCRIPT", filepath.Join(cfg.Cwd, "script.json"))
			t.Setenv("FAKE_CLAUDE_RECORD", filepath.Join(cfg.Cwd, "record.jsonl"))
			dbPath := filepath.Join(t.TempDir(), "reset.db")
			db, err := store.Open(ctx, dbPath, store.Options{})
			require.NoError(t, err)
			defer db.Close()
			raw, err := sql.Open("sqlite", dbPath)
			require.NoError(t, err)
			defer raw.Close()
			reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
			defer reg.Unregister()
			m := makeSubagentTestManager(t, db, reg, 4)
			defer m.Close(ctx)
			m.config.RouterURL = "http://127.0.0.1:8765"
			old := subagentTestParentWithModel(t, db, "review6b-reset", store.ManagedModel{Provider: claudeCodeProvider, ID: "sonnet"})
			registerParentOwner(m, old)
			require.NoError(t, db.SetContextThresholdConfig(ctx, store.ContextThresholdConfig{WarningPercent: 10, ResetPercent: 20}))
			lane := m.laneFor(old.AgentID)
			warnings := 0
			m.completeHistoryPublisher = func(entry store.ManagedHistoryEntry) {
				if entry.Presentation != nil && entry.Presentation.Kind == "reminder" {
					warnings++
					require.True(t, lane.embeddedResetRunning(), "fresh guard must arm while coordinator owns reset")
					require.False(t, lane.embeddedResetPending())
					var active int
					require.NoError(t, raw.QueryRow(`SELECT count(*) FROM embedded_sessions WHERE agent_id=? AND execution_json IS NOT NULL`, old.AgentID).Scan(&active))
					require.Zero(t, active, "monitor must run only after execution settlement")
				}
			}
			lane.serial.Lock()
			defer lane.serial.Unlock()
			err = m.resetEmbeddedContextAfterDrain(ctx, old, "review6b")
			require.NoError(t, err)
			require.Equal(t, 1, warnings)
			require.NotNil(t, lane.claudeFreshResetGuard, "runner must actually reach fresh-wake monitor")
			require.Equal(t, old.Epoch+1, lane.claudeFreshResetGuard.Epoch)
			require.False(t, lane.embeddedResetRunning())
		})
	}
}
