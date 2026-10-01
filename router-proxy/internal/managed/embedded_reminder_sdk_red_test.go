package managed

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

type reminderSDKObservation struct {
	tools  []ai.Tool
	result *ai.ToolResultMessage
}

// The public reminders HTTP API accepts a caller-selected recipient. This
// fixed SDK tool must instead derive its identity from the saved embedded
// reservation. A provider-proposed tool call is not proof of registration.
func TestEmbeddedReminderOwnAgentSDKRED(t *testing.T) {
	for _, tc := range []struct {
		name string
		root bool
	}{
		{name: "reminder-sdk-root", root: true},
		{name: "reminder-sdk-independent-worker"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			path := filepath.Join(root, "router.db")
			db, err := store.Open(ctx, path, store.Options{})
			require.NoError(t, err)
			defer db.Close()
			manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
			require.NoError(t, err)
			reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
			defer reg.Unregister()
			const toolName = "mecha_reminder"
			const callID = "reminder-create-call"
			body := "check my own work"
			due := time.Now().UTC().Add(30 * time.Minute).Format("2006-01-02T15:04:05.000Z")
			create := map[string]any{"action": "create", "body": body, "due_at": due}
			seen := make(chan reminderSDKObservation, 7)
			cancelID := make(chan string, 1)
			reg.SetResponses([]providers.FauxResponseStep{
				func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
					var first reminderSDKObservation
					for _, msg := range h.Messages {
						if system, ok := msg.(ai.SystemMessage); ok {
							first.tools = append(first.tools, system.ToolsAdded...)
						}
					}
					seen <- first
					return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall(toolName, create, callID)}, ai.StopToolUse)
				},
				func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
					seen <- reminderSDKResult(h, callID)
					return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall(toolName, map[string]any{"action": "list"}, "reminder-list-call")}, ai.StopToolUse)
				},
				func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
					seen <- reminderSDKResult(h, "reminder-list-call")
					return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall(toolName, create, callID)}, ai.StopToolUse)
				},
				func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
					seen <- reminderSDKResult(h, callID)
					changed := map[string]any{"action": "create", "body": "different body", "due_at": due}
					return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall(toolName, changed, callID)}, ai.StopToolUse)
				},
				func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
					seen <- reminderSDKResult(h, callID)
					return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall(toolName, map[string]any{"action": "delete", "id": <-cancelID}, "reminder-cancel-call")}, ai.StopToolUse)
				},
				func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
					seen <- reminderSDKResult(h, "reminder-cancel-call")
					return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "done"}}, ai.StopStop)
				},
			})
			m, err := NewManager(ManagerOptions{Store: db, Manifest: manifest,
				Config:                Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}},
				CatalogSource:         CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
				EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() },
				CodexCredentialStore:  testfixture.CodexAdmission(t),
			})
			require.NoError(t, err)
			defer m.Close(ctx)
			request := LaunchRequest{Name: tc.name, Cwd: root, Runtime: "embedded", Model: &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"}, InitialInstruction: "remind me", RequestID: "a5555555-5555-4555-8555-555555555572"}
			var launched LaunchResult
			if tc.root {
				launched, err = m.LaunchEmbeddedRoot(ctx, request)
			} else {
				launched, err = m.Launch(ctx, request)
			}
			require.NoError(t, err)
			binding := store.EmbeddedBinding{AgentID: launched.Agent.AgentID, SessionID: launched.Agent.PiSessionID, Epoch: launched.Agent.SessionEpoch, Generation: launched.Agent.RuntimeGeneration}
			role, err := db.EmbeddedSessionRole(ctx, binding)
			require.NoError(t, err)
			if tc.root {
				require.Equal(t, "root", role)
			} else {
				require.Equal(t, "worker", role)
			}
			diagnoseTimeout := func() { reminderSDKTimeoutDiagnostic(t, path, launched.Agent.AgentID) }
			first := waitReminderSDK(t, seen, diagnoseTimeout)
			var fixed *ai.Tool
			for i := range first.tools {
				if first.tools[i].Name == toolName {
					fixed = &first.tools[i]
				}
			}
			require.NotNil(t, fixed, "fixed mecha_reminder absent from real SDK turn; later assertions not reached")
			require.True(t, fixed.Parameters.Check(create))
			for _, forbidden := range []string{"target", "creator", "owner", "recipient", "role"} {
				args := map[string]any{"action": "create", "body": body, "due_at": due, forbidden: "another-agent"}
				require.False(t, fixed.Parameters.Check(args), "%s must not be provider-supplied", forbidden)
			}
			created := waitReminderSDK(t, seen, diagnoseTimeout)
			require.NotNil(t, created.result)
			require.Equal(t, callID, created.result.ToolCallID)
			require.False(t, created.result.IsError)
			require.NotEmpty(t, created.result.Content)
			var intent []byte
			var savedAgent, savedSession, savedGeneration string
			var savedEpoch int64
			check, err := sql.Open("sqlite", path)
			require.NoError(t, err)
			defer check.Close()
			require.NoError(t, check.QueryRowContext(ctx, `SELECT agent_id,session_id,epoch,generation,intent FROM embedded_effects WHERE effect_id=?`, callID).Scan(&savedAgent, &savedSession, &savedEpoch, &savedGeneration, &intent))
			require.Equal(t, binding.AgentID, savedAgent)
			require.Equal(t, binding.SessionID, savedSession)
			require.Equal(t, binding.Epoch, savedEpoch)
			require.Equal(t, binding.Generation, savedGeneration)
			var original struct {
				ID        string         `json:"id"`
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			require.NoError(t, json.Unmarshal(intent, &original))
			require.Equal(t, callID, original.ID)
			require.Equal(t, toolName, original.Name)
			require.Equal(t, create, original.Arguments)
			var id, target, creator, storedBody, storedDue string
			require.NoError(t, check.QueryRowContext(ctx, `SELECT id,target,creator,body,next_due_at FROM reminders WHERE body=?`, body).Scan(&id, &target, &creator, &storedBody, &storedDue))
			require.Equal(t, binding.AgentID, target)
			require.Equal(t, binding.AgentID, creator)
			require.Equal(t, body, storedBody)
			require.Equal(t, due, storedDue)
			listed := waitReminderSDK(t, seen)
			require.NotNil(t, listed.result)
			require.False(t, listed.result.IsError)
			require.Contains(t, listed.result.Content[0].(ai.TextContent).Text, id)
			replay := waitReminderSDK(t, seen, diagnoseTimeout)
			require.NotNil(t, replay.result)
			require.False(t, replay.result.IsError)
			require.Equal(t, created.result.Content, replay.result.Content, "exact SDK call ID and args must return the same result")
			conflict := waitReminderSDK(t, seen)
			require.NotNil(t, conflict.result)
			require.True(t, conflict.result.IsError, "same SDK call ID with changed args cannot mutate")
			var count int
			require.NoError(t, check.QueryRowContext(ctx, `SELECT COUNT(*) FROM reminders WHERE target=?`, binding.AgentID).Scan(&count))
			require.Equal(t, 1, count)
			cancelID <- id
			cancelled := waitReminderSDK(t, seen)
			require.NotNil(t, cancelled.result)
			require.False(t, cancelled.result.IsError)
			require.NoError(t, check.QueryRowContext(ctx, `SELECT COUNT(*) FROM reminders WHERE id=?`, id).Scan(&count))
			require.Zero(t, count)
		})
	}
}

func reminderSDKResult(h ai.TranscriptContext, id string) (out reminderSDKObservation) {
	for _, msg := range h.Messages {
		if result, ok := msg.(ai.ToolResultMessage); ok && result.ToolCallID == id {
			copy := result
			out.result = &copy
		}
	}
	return
}

func waitReminderSDK(t *testing.T, seen <-chan reminderSDKObservation, onTimeout ...func()) reminderSDKObservation {
	t.Helper()
	select {
	case result := <-seen:
		return result
	case <-time.After(4 * time.Second):
		for _, diagnostic := range onTimeout {
			diagnostic()
		}
		t.Fatal("embedded provider did not reach SDK turn")
		return reminderSDKObservation{}
	}
}

// Only fixed lifecycle/source/effect metadata is emitted; never print tool arguments,
// reminder bodies, intent bytes, result bytes, or provider transcript contents.
func reminderSDKTimeoutDiagnostic(t *testing.T, path, agentID string) {
	t.Helper()
	check, err := sql.Open("sqlite", path)
	if err != nil {
		t.Log("timeout diagnostic unavailable")
		return
	}
	defer check.Close()
	var lifecycle string
	var errorCode, errorMessage sql.NullString
	if err := check.QueryRow(`SELECT lifecycle_state,last_error_code,last_error_message FROM managed_agents WHERE agent_id=?`, agentID).Scan(&lifecycle, &errorCode, &errorMessage); err != nil {
		t.Log("timeout manager metadata unavailable")
	} else {
		t.Logf("timeout manager lifecycle_state=%q last_error_code=%q last_error_message=%q", lifecycle, errorCode.String, errorMessage.String)
	}
	for _, callID := range []string{"reminder-create-call", "reminder-list-call"} {
		var state string
		var intentBytes, resultBytes int
		if err := check.QueryRow(`SELECT state,length(intent),coalesce(length(result),0) FROM embedded_effects WHERE effect_id=?`, callID).Scan(&state, &intentBytes, &resultBytes); err != nil {
			t.Logf("timeout embedded_effects callID=%q metadata unavailable", callID)
		} else {
			t.Logf("timeout embedded_effects callID=%q state=%q intent_bytes=%d result_bytes=%d", callID, state, intentBytes, resultBytes)
		}
	}
}
