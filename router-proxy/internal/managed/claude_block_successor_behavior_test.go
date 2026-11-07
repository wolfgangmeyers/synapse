package managed

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// A local ambiguity hold belongs to the exact reservation and owner, not the
// message forever. Exercise real dispatch, rollback, reset and successor delivery.
func TestClaudeLocalBlockFencesResetSuccessor(t *testing.T) {
	ctx := context.Background()
	cfg := claudeTestConfig(t, `{"turns":[[{"text":"successor delivered"}]]}`)
	t.Setenv("MECHA_CLAUDE_BIN", cfg.Binary)
	t.Setenv("FAKE_CLAUDE_SCRIPT", filepath.Join(cfg.Cwd, "script.json"))
	record := filepath.Join(cfg.Cwd, "record.jsonl")
	t.Setenv("FAKE_CLAUDE_RECORD", record)
	f := newFailureFixture(t)
	owner := f.launch(t, "")
	waitGateStarts(t, f.m)
	id := "e9999999-7777-4777-8777-777777777777"
	_, err := f.raw.Exec(`UPDATE managed_agents SET selected_model_provider='claude-code', selected_model_id='sonnet' WHERE agent_id=?`, owner.binding.AgentID)
	require.NoError(t, err)
	token, err := f.db.EnterEmbeddedSDKExecution(ctx, owner.binding, nil)
	require.NoError(t, err)
	f.enqueue(t, id)
	f.m.dispatchEmbeddedMail(owner.binding.AgentID)
	lane := f.m.laneFor(owner.binding.AgentID)
	require.NotNil(t, lane.claudeUnclassifiedBlocked)
	old := *lane.claudeUnclassifiedBlocked
	require.Equal(t, owner.binding, old.Binding)
	checkBlocked := func() {
		t.Helper()
		require.Equal(t, old, *lane.claudeUnclassifiedBlocked)
		f.m.mu.Lock()
		current := f.m.embeddedOwners[owner.binding.AgentID]
		f.m.mu.Unlock()
		require.Equal(t, owner.binding, current.binding)
		require.Equal(t, 1, f.count(t, `SELECT count(*) FROM embedded_attempts WHERE attempt_id=? AND message_id=? AND message_incarnation_id=? AND agent_id=? AND session_id=? AND epoch=? AND generation=?`, old.AttemptID, id, old.MessageIncarnationID, old.Binding.AgentID, old.Binding.SessionID, old.Binding.Epoch, old.Binding.Generation))
		require.Equal(t, 1, f.count(t, `SELECT count(*) FROM embedded_sessions WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND current=1 AND owner_id IS NOT NULL AND owner_token=?`, owner.binding.AgentID, owner.binding.SessionID, owner.binding.Epoch, owner.binding.Generation, owner.binding.OwnerToken))
		data, err := os.ReadFile(record)
		if err != nil {
			require.True(t, os.IsNotExist(err))
			return
		}
		require.NotContains(t, string(data), `"kind":"start"`)
		require.NotContains(t, string(data), `"kind":"stdin"`)
	}
	checkBlocked()
	require.NoError(t, f.db.ClearEmbeddedExecution(ctx, owner.binding, token))
	f.m.dispatchEmbeddedMail(owner.binding.AgentID)
	require.Zero(t, f.count(t, `SELECT count(*) FROM embedded_acks WHERE message_id=?`, id), "same owner must remain blocked after the transient failure clears")
	checkBlocked()

	_, err = f.raw.Exec(`CREATE TRIGGER block_reset_fault BEFORE INSERT ON embedded_sessions BEGIN SELECT RAISE(ABORT,'reset fault'); END`)
	require.NoError(t, err)
	lane.serial.Lock()
	err = f.m.resetEmbeddedContextAfterDrain(ctx, owner.binding, "block rollback")
	lane.serial.Unlock()
	require.ErrorContains(t, err, "reset fault")
	f.m.dispatchEmbeddedMail(owner.binding.AgentID)
	require.Zero(t, f.count(t, `SELECT count(*) FROM embedded_acks WHERE message_id=?`, id), "failed reset must not lift the old block")
	checkBlocked()
	_, err = f.raw.Exec(`DROP TRIGGER block_reset_fault`)
	require.NoError(t, err)
	lane.serial.Lock()
	err = f.m.resetEmbeddedContextAfterDrain(ctx, owner.binding, "block successor")
	lane.serial.Unlock()
	require.NoError(t, err)
	f.m.mu.Lock()
	next := f.m.embeddedOwners[owner.binding.AgentID]
	f.m.mu.Unlock()
	require.Greater(t, next.binding.Epoch, old.Binding.Epoch)
	f.m.dispatchEmbeddedMail(owner.binding.AgentID)
	require.Equal(t, 1, f.count(t, `SELECT count(*) FROM embedded_acks WHERE message_id=?`, id), "successor must deliver the previously blocked unsubmitted mail")
	require.Equal(t, 1, f.count(t, `SELECT count(*) FROM embedded_acks k JOIN embedded_attempts a ON a.attempt_id=k.attempt_id JOIN embedded_entries e ON e.agent_id=k.agent_id AND e.session_id=k.session_id AND e.epoch=k.epoch AND e.generation=k.generation AND e.seq=k.entry_seq WHERE k.message_id=? AND k.agent_id=? AND k.session_id=? AND k.epoch=? AND k.generation=? AND a.message_incarnation_id=? AND a.accepted_body=? AND e.kind='user' AND instr(e.body,a.attempt_id)>0 AND instr(e.body,a.accepted_body)>0`, id, next.binding.AgentID, next.binding.SessionID, next.binding.Epoch, next.binding.Generation, old.MessageIncarnationID, old.Body))
	data, err := os.ReadFile(record)
	require.NoError(t, err)
	foundInput := false
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var entry struct {
			Kind string `json:"kind"`
			Line string `json:"line"`
		}
		require.NoError(t, json.Unmarshal([]byte(line), &entry))
		if entry.Kind == "stdin" && strings.Contains(entry.Line, old.Body) {
			foundInput = true
		}
	}
	require.True(t, foundInput, "successor must submit the accepted mail to Claude")
}
