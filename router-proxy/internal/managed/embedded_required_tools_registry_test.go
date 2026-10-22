package managed

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/domain"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

// Real Manager launch, migrations, ordered SDK persistence and mailbox admission.
// Only the external model boundary is replaced. No router or live provider starts.
func requiredToolsFixture(t *testing.T, role string) (*Manager, *store.Store, *providers.FauxProviderRegistration, string, string) {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	db, err := store.Open(ctx, filepath.Join(root, "router.db"), store.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	callerProvider := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	targetProvider := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	t.Cleanup(callerProvider.Unregister)
	t.Cleanup(targetProvider.Unregister)
	settle := func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "initial settled"}}, ai.StopStop)
	}
	callerProvider.SetResponses([]providers.FauxResponseStep{settle})
	targetProvider.SetResponses([]providers.FauxResponseStep{settle, settle})
	caller := "required-caller-" + role
	m, err := NewManager(ManagerOptions{Store: db, Manifest: manifest,
		Config:               Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}},
		CatalogSource:        CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
		CodexCredentialStore: testfixture.CodexAdmission(t),
		EmbeddedModelResolver: func(saved store.ManagedAgentRecord) *ai.Model {
			if saved.AgentID == caller {
				return callerProvider.GetModel()
			}
			return targetProvider.GetModel()
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, m.Close(ctx)) })
	generation, err := db.MintEmbeddedProcessGeneration(ctx, "required-tools-fixture")
	require.NoError(t, err)
	require.NoError(t, m.SetEmbeddedStartupGeneration(generation))
	launch := m.Launch
	if role == "root" {
		launch = m.LaunchEmbeddedRoot
	}
	_, err = launch(ctx, LaunchRequest{Name: caller, Cwd: root, Runtime: "embedded", Model: &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"}, InitialInstruction: "settle before mail"})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		row, e := db.ManagedAgent(ctx, caller)
		m.mu.Lock()
		owner := m.embeddedOwners[caller]
		m.mu.Unlock()
		return e == nil && row.LifecycleState == "running" && row.AcceptingPrompts && owner.binding.OwnerToken > 0
	}, 4*time.Second, 10*time.Millisecond)
	return m, db, callerProvider, caller, root
}

func requiredToolsCall(t *testing.T, m *Manager, db *store.Store, provider *providers.FauxProviderRegistration, caller, tool, callID string, args map[string]any) ai.ToolResultMessage {
	t.Helper()
	ctx := context.Background()
	results := make(chan ai.ToolResultMessage, 1)
	provider.SetResponses([]providers.FauxResponseStep{
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall(tool, args, callID)}, ai.StopToolUse)
		},
		func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			for _, msg := range h.Messages {
				if result, ok := msg.(ai.ToolResultMessage); ok && result.ToolCallID == callID {
					results <- result
					break
				}
			}
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "sender-bound final"}}, ai.StopStop)
		},
	})
	_, err := db.Enqueue(ctx, protocol.Envelope{ID: "inbound-" + callID, From: "original-sender", To: caller, Body: "perform the requested tool call", CreatedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z")})
	require.NoError(t, err)
	_, err = db.ResolveMessageTarget(ctx, "inbound-"+callID)
	require.NoError(t, err)
	m.OfferDirect(caller)
	var result ai.ToolResultMessage
	select {
	case result = <-results:
	case <-time.After(4 * time.Second):
		t.Fatal("SDK did not continue with a correlated tool result")
	}
	require.Equal(t, tool, result.ToolName)
	require.Equal(t, callID, result.ToolCallID)
	require.False(t, result.IsError, "tool must execute through the real registered SDK capability: %v", result.Content)
	require.Eventually(t, func() bool {
		mail, e := db.MessageFor(ctx, "inbound-"+callID)
		return e == nil && mail.State == domain.MessageAcknowledged
	}, 4*time.Second, 10*time.Millisecond)
	return result
}

func requiredToolsPayload(t *testing.T, result ai.ToolResultMessage) map[string]json.RawMessage {
	t.Helper()
	require.Len(t, result.Content, 1)
	text, ok := result.Content[0].(ai.TextContent)
	require.True(t, ok)
	var payload map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(text.Text), &payload))
	return payload
}

func TestEmbeddedRequiredMailboxRegistry(t *testing.T) {
	for _, role := range []string{"root", "worker"} {
		t.Run(role, func(t *testing.T) {
			m, db, provider, caller, _ := requiredToolsFixture(t, role)
			callID := "addressed-" + role
			result := requiredToolsCall(t, m, db, provider, caller, "mecha_mailbox_send", callID, map[string]any{"recipient": "independent-recipient", "body": "explicit body", "subject": "explicit subject"})
			payload := requiredToolsPayload(t, result)
			var id string
			require.NoError(t, json.Unmarshal(payload["message_id"], &id))
			require.NotEmpty(t, id)
			mail, err := db.MessageFor(context.Background(), id)
			require.NoError(t, err)
			require.Equal(t, caller, mail.From)
			require.Equal(t, "independent-recipient", mail.To)
			require.Equal(t, "explicit body", mail.Body)
			require.NotNil(t, mail.Subject)
			require.Equal(t, "explicit subject", *mail.Subject)
			require.Equal(t, domain.MessageQueued, mail.State)
			replies, err := db.PendingEmbeddedReplies(context.Background(), caller)
			require.NoError(t, err)
			require.Empty(t, replies, "automatic reply outbox was eliminated; outbound messages require explicit mecha_mailbox_send")
		})
	}
}

func TestEmbeddedRequiredWorkerActionsRegistry(t *testing.T) {
	for _, role := range []string{"root", "worker"} {
		for _, action := range []string{"list", "launch", "send", "cleanup"} {
			t.Run(role+"/"+action, func(t *testing.T) {
				ctx := context.Background()
				m, db, provider, caller, root := requiredToolsFixture(t, role)
				target := "independent-target"
				if action != "launch" {
					_, err := m.Launch(ctx, LaunchRequest{Name: target, Cwd: root, Runtime: "embedded", Model: &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"}, InitialInstruction: "settle independently"})
					require.NoError(t, err)
					require.Eventually(t, func() bool {
						row, e := db.ManagedAgent(ctx, target)
						return e == nil && row.LifecycleState == "running"
					}, 4*time.Second, 10*time.Millisecond)
				}
				args := map[string]any{"action": action}
				if action != "list" {
					args["name"] = target
				}
				if action == "launch" {
					args["cwd"], args["instruction"] = root, "independent initial task"
					args["model"] = map[string]any{"provider": "openai-codex", "id": "gpt-5.6-terra"}
				}
				if action == "send" {
					args["body"] = "independent worker mail"
				}
				result := requiredToolsCall(t, m, db, provider, caller, "mecha_worker", role+"-"+action, args)
				payload := requiredToolsPayload(t, result)
				switch action {
				case "list":
					require.Contains(t, string(payload["workers"]), target)
				case "launch":
					require.JSONEq(t, `"accepted"`, string(payload["status"]))
					row, err := db.ManagedAgent(ctx, target)
					require.NoError(t, err)
					callerRow, err := db.ManagedAgent(ctx, caller)
					require.NoError(t, err)
					require.NotEqual(t, callerRow.PiSessionID, row.PiSessionID)
					require.NotEqual(t, callerRow.RuntimeGeneration, row.RuntimeGeneration)
					savedRole, err := db.SavedEmbeddedSessionRole(ctx, store.EmbeddedBinding{AgentID: row.AgentID, SessionID: row.PiSessionID, Epoch: row.SessionEpoch, Generation: row.RuntimeGeneration})
					require.NoError(t, err)
					require.Equal(t, "worker", savedRole)
				case "send":
					var id string
					require.NoError(t, json.Unmarshal(payload["message_id"], &id))
					mail, err := db.MessageFor(ctx, id)
					require.NoError(t, err)
					require.Equal(t, caller, mail.From)
					require.Equal(t, target, mail.To)
					require.Equal(t, "independent worker mail", mail.Body)
					require.Eventually(t, func() bool {
						mail, err := db.MessageFor(ctx, id)
						return err == nil && mail.State == domain.MessageAcknowledged
					}, 4*time.Second, 10*time.Millisecond, "worker send must route through the target's own mailbox lane")
				case "cleanup":
					require.JSONEq(t, `"accepted"`, string(payload["status"]))
					require.Eventually(t, func() bool {
						_, err := db.ManagedAgent(ctx, target)
						return errors.Is(err, sql.ErrNoRows)
					}, 4*time.Second, 10*time.Millisecond)
				}
			})
		}
	}
}
