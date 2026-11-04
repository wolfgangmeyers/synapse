package managed

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/domain"
	"github.com/stretchr/testify/require"
)

type headroomLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *headroomLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *headroomLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

func TestClaudeDispatcherPostConsumptionRetryHeadroom(t *testing.T) {
	ctx := context.Background()
	cfg := claudeTestConfig(t, `{"turns":[[{"emit":{"type":"assistant","message":{"id":"high-usage","role":"assistant","model":"sonnet","content":[{"type":"text","text":"usage marker"}],"usage":{"input_tokens":1000000}}}},{"emit":{"type":"assistant","message":{"id":"flush-marker","role":"assistant","model":"sonnet","content":[{"type":"text","text":"flush"}],"usage":{"input_tokens":1}}}},{"rate_limit":{"resetsAt":1}}],[{"text":"unexpected retry"}]]}`)
	t.Setenv("MECHA_CLAUDE_BIN", cfg.Binary)
	t.Setenv("FAKE_CLAUDE_SCRIPT", filepath.Join(cfg.Cwd, "script.json"))
	t.Setenv("FAKE_CLAUDE_RECORD", filepath.Join(cfg.Cwd, "record.jsonl"))
	f := newFailureFixture(t)
	owner := f.launch(t, "")
	f.m.startWG.Wait()
	_, err := f.raw.Exec(`UPDATE managed_agents SET selected_model_provider=?, selected_model_id=? WHERE agent_id=?`, claudeCodeProvider, "sonnet", owner.binding.AgentID)
	require.NoError(t, err)
	var diagnostics headroomLog
	previousLog := log.Writer()
	log.SetOutput(&diagnostics)
	defer log.SetOutput(previousLog)
	id := "e8888888-8888-4888-8888-888888888888"
	f.enqueue(t, id)
	f.m.dispatchEmbeddedMail(owner.binding.AgentID)
	f.laneBarrier(t, owner.binding.AgentID)
	assertConsumed := func() {
		t.Helper()
		require.Equal(t, 1, f.count(t, `SELECT count(*) FROM embedded_acks WHERE message_id=?`, id))
		require.Equal(t, 1, f.count(t, `SELECT count(*) FROM embedded_attempts WHERE message_id=?`, id))
		require.Equal(t, 1, f.count(t, `SELECT count(*) FROM embedded_entries WHERE agent_id=? AND kind='user'`, owner.binding.AgentID))
		require.Equal(t, 1, f.count(t, `SELECT count(*) FROM embedded_entries WHERE agent_id=? AND kind='assistant' AND CAST(body AS TEXT) LIKE ?`, owner.binding.AgentID, "%usage marker%"))
		mail, err := f.db.MessageFor(ctx, id)
		require.NoError(t, err)
		require.Equal(t, domain.MessageAcknowledged, mail.State)
		next, err := f.db.NextEmbeddedMail(ctx, owner.binding.AgentID)
		require.NoError(t, err)
		require.Empty(t, next)
		data, err := os.ReadFile(filepath.Join(cfg.Cwd, "record.jsonl"))
		require.NoError(t, err)
		inputs := 0
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			var row struct{ Kind, Line string }
			require.NoError(t, json.Unmarshal([]byte(line), &row))
			if row.Kind != "stdin" {
				continue
			}
			var input struct{ Type string }
			require.NoError(t, json.Unmarshal([]byte(row.Line), &input))
			if input.Type == "user" {
				inputs++
			}
		}
		require.Equal(t, 1, inputs)
	}
	assertConsumed()
	f.m.OfferDirect(owner.binding.AgentID)
	f.m.startWG.Wait()
	assertConsumed()
	found := false
	for _, line := range strings.Split(diagnostics.String(), "\n") {
		if strings.Contains(line, owner.binding.AgentID) && strings.Contains(line, id) &&
			strings.Contains(line, "Claude retry after consumption:") &&
			strings.Contains(line, "Claude submission cannot fit fixed headroom") {
			found = true
			break
		}
	}
	require.True(t, found, "missing post-consumption headroom diagnostic: %s", diagnostics.String())
}
