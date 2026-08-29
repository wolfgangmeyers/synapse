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
	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
	"github.com/stretchr/testify/require"
)

// TestNodeAndGoShareMessageRows exercises the same persistent message fixture
// from both implementations. The test deliberately compares durable fields;
// generated IDs are supplied by the Go fixture source.
func TestNodeAndGoShareMessageRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "router.db")
	clock := deterministic.NewFixedClock(time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))
	store, err := Open(context.Background(), path, Options{Clock: clock, IDSource: deterministic.NewSequenceIDSource(testUUID("0001"))})
	require.NoError(t, err)
	_, err = store.Enqueue(context.Background(), testEnvelope("go-message", "fixture body"))
	require.NoError(t, err)
	require.NoError(t, store.Close())

	node, err := exec.LookPath("node")
	require.NoError(t, err)
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	require.NoError(t, err)
	moduleURL := (&url.URL{Scheme: "file", Path: filepath.Join(root, "harness", "db.js")}).String()
	const script = `
const { openMessageRepository } = await import(process.env.MECHA_NODE_STORE_MODULE);
const repository = openMessageRepository({ databasePath: process.env.MECHA_NODE_STORE_DATABASE, clock: () => new Date("2026-08-29T12:00:00.000Z") });
console.log(JSON.stringify(repository.messageFor("go-message")));
repository.close();
`
	command := exec.Command(node, "--input-type=module", "-e", script)
	command.Env = append(os.Environ(), "MECHA_NODE_STORE_MODULE="+moduleURL, "MECHA_NODE_STORE_DATABASE="+path)
	output, err := command.Output()
	require.NoError(t, err)
	var message struct {
		ID    string `json:"id"`
		From  string `json:"from"`
		To    string `json:"to"`
		Body  string `json:"body"`
		State string `json:"state"`
	}
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(string(output))), &message))
	require.Equal(t, "go-message", message.ID)
	require.Equal(t, "sender", message.From)
	require.Equal(t, "agent-a", message.To)
	require.Equal(t, "fixture body", message.Body)
	require.Equal(t, "queued", message.State)

	// Also prove the Node-readable row remains valid under the Go protocol
	// validator, which is the boundary used by the future transport layer.
	require.NoError(t, protocol.ValidateEnvelope(protocol.Envelope{ID: message.ID, From: message.From, To: message.To, Body: message.Body, CreatedAt: "2026-08-29T12:00:00.000Z"}, protocol.DefaultMaxBodyBytes))
}
