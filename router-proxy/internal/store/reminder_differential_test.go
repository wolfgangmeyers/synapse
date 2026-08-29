package store

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/stretchr/testify/require"
)

type reminderSnapshot struct {
	ReminderState string `json:"reminder_state"`
	LinkedID      string `json:"linked_id"`
	MessageState  string `json:"message_state"`
	MessageBody   string `json:"message_body"`
	SourceKind    string `json:"source_kind"`
}

func TestNodeAndGoOneShotReminderTransitionMatch(t *testing.T) {
	nodePath := filepath.Join(t.TempDir(), "node.db")
	node := runNodeReminderFixture(t, nodePath)
	clock := deterministic.NewFixedClock(time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "go.db"), Options{Clock: clock, IDSource: deterministic.NewSequenceIDSource(testUUID("reminder-incarnation"))})
	require.NoError(t, err)
	_, _, err = store.CreateReminder(context.Background(), reminderRequest("one-shot", nil))
	require.NoError(t, err)
	outcomes, err := store.FireDueReminders(context.Background(), nil)
	require.NoError(t, err)
	require.Len(t, outcomes, 1)
	goResult := reminderSnapshot{ReminderState: outcomes[0].Reminder.State, LinkedID: *outcomes[0].Reminder.LinkedMessageID, MessageState: outcomes[0].Message.State, MessageBody: outcomes[0].Message.Body, SourceKind: outcomes[0].Message.Source.Kind}
	require.Equal(t, node, goResult)
}

func runNodeReminderFixture(t *testing.T, databasePath string) reminderSnapshot {
	t.Helper()
	node, err := exec.LookPath("node")
	require.NoError(t, err)
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	require.NoError(t, err)
	moduleURL := (&url.URL{Scheme: "file", Path: filepath.Join(root, "harness", "db.js")}).String()
	const script = `
const { openMessageRepository } = await import(process.env.MECHA_NODE_STORE_MODULE);
const now = "2026-08-29T12:00:00.000Z";
const repository = openMessageRepository({ databasePath: process.env.MECHA_NODE_STORE_DATABASE, clock: () => new Date(now) });
repository.createReminder({id:"one-shot", target:"agent-a", creator:"creator", body:"reminder body", created_at:now, next_due_at:now, interval_ms:null});
repository.fireDueReminders();
const reminder = repository.reminderFor("one-shot");
const message = repository.messageFor("reminder-one-shot-0");
console.log(JSON.stringify({reminder_state:reminder.state, linked_id:reminder.linked_message_id, message_state:message.state, message_body:message.body, source_kind:message.source.kind}));
repository.close();
`
	command := exec.Command(node, "--input-type=module", "-e", script)
	command.Env = append(os.Environ(), "MECHA_NODE_STORE_MODULE="+moduleURL, "MECHA_NODE_STORE_DATABASE="+databasePath)
	output, err := command.Output()
	require.NoError(t, err)
	var result reminderSnapshot
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(string(output))), &result))
	return result
}
