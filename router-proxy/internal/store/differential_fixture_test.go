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
func TestNodeAndGoReceiptExpiryTransitionMatch(t *testing.T) {
	ctx := context.Background()
	nodePath := filepath.Join(t.TempDir(), "node.db")
	nodeResult := runNodeExpiryFixture(t, nodePath)

	goPath := filepath.Join(t.TempDir(), "go.db")
	clock := deterministic.NewFixedClock(time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))
	store, err := Open(ctx, goPath, Options{Clock: clock, IDSource: deterministic.NewSequenceIDSource(testUUID("0001"), "batch-1")})
	require.NoError(t, err)
	_, err = store.Enqueue(ctx, testEnvelope("message-1", "body"))
	require.NoError(t, err)
	_, err = store.AcquireAgentLease(ctx, LeaseRequest{AgentID: "agent-a", InstanceID: "instance-a", LeaseID: "lease-a", LeaseDuration: 30 * time.Second})
	require.NoError(t, err)
	delivery, err := store.ClaimNextMessage(ctx, ClaimRequest{AgentID: "agent-a", InstanceID: "instance-a", LeaseID: "lease-a", DeliveryLeaseDuration: time.Second})
	require.NoError(t, err)
	require.NoError(t, clock.Advance(2*time.Second))
	_, err = store.RecordInjectionRequested(ctx, ReceiptRequest{AgentID: "agent-a", InstanceID: "instance-a", LeaseID: "lease-a", MessageID: "message-1", AttemptNumber: delivery.AttemptNumber, DeliveryItems: delivery.Items})
	require.Equal(t, "delivery_not_active", errorCode(err))
	goResult := expirySnapshot{Error: errorCode(err)}
	var messageState string
	require.NoError(t, store.db.QueryRow("SELECT state FROM messages WHERE id = 'message-1'").Scan(&messageState))
	goResult.MessageState = messageState
	require.NoError(t, store.db.QueryRow("SELECT state FROM delivery_attempts WHERE message_id = 'message-1'").Scan(&goResult.AttemptState))
	require.Equal(t, nodeResult, goResult)
}

type expirySnapshot struct {
	Error        string `json:"error"`
	MessageState string `json:"message_state"`
	AttemptState string `json:"attempt_state"`
}

func errorCode(err error) string {
	if err == nil {
		return ""
	}
	if value, ok := err.(interface{ Code() string }); ok {
		return value.Code()
	}
	return "unknown"
}

func runNodeExpiryFixture(t *testing.T, databasePath string) expirySnapshot {
	t.Helper()
	node, err := exec.LookPath("node")
	require.NoError(t, err)
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	require.NoError(t, err)
	moduleURL := (&url.URL{Scheme: "file", Path: filepath.Join(root, "harness", "db.js")}).String()
	const script = `
import { DatabaseSync } from "node:sqlite";
const { openMessageRepository } = await import(process.env.MECHA_NODE_STORE_MODULE);
let now = "2026-08-29T12:00:00.000Z";
const repository = openMessageRepository({ databasePath: process.env.MECHA_NODE_STORE_DATABASE, clock: () => new Date(now) });
repository.enqueue({id:"message-1", from:"sender", to:"agent-a", body:"body", created_at:now});
repository.acquireAgentLease({agentId:"agent-a", instanceId:"instance-a", leaseId:"lease-a", leaseDurationMs:30000});
const delivery = repository.claimNextMessage({agentId:"agent-a", instanceId:"instance-a", leaseId:"lease-a", deliveryLeaseDurationMs:1000});
now = "2026-08-29T12:00:02.000Z";
let error = "";
try { repository.recordInjectionRequested({agentId:"agent-a", instanceId:"instance-a", leaseId:"lease-a", messageId:"message-1", attemptNumber:delivery.attempt_number, deliveryItems:delivery.items}); } catch (caught) { error = caught.code ?? "unknown"; }
repository.close();
const database = new DatabaseSync(process.env.MECHA_NODE_STORE_DATABASE, { enableForeignKeyConstraints: true });
const message = database.prepare("SELECT state FROM messages WHERE id = 'message-1'").get();
const attempt = database.prepare("SELECT state FROM delivery_attempts WHERE message_id = 'message-1'").get();
console.log(JSON.stringify({error, message_state:message?.state ?? "", attempt_state:attempt?.state ?? ""}));
database.close();
`
	command := exec.Command(node, "--input-type=module", "-e", script)
	command.Env = append(os.Environ(), "MECHA_NODE_STORE_MODULE="+moduleURL, "MECHA_NODE_STORE_DATABASE="+databasePath)
	output, err := command.Output()
	require.NoError(t, err)
	var raw struct {
		Error        string `json:"error"`
		MessageState string `json:"message_state"`
		AttemptState string `json:"attempt_state"`
	}
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(string(output))), &raw))
	return expirySnapshot{Error: raw.Error, MessageState: raw.MessageState, AttemptState: raw.AttemptState}
}

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
