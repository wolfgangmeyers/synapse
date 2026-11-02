package managed

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestEmbeddedAgentUrgencyActualRegistry(t *testing.T) {
	for _, role := range []string{"root", "worker"} {
		t.Run(role, func(t *testing.T) {
			m, db, provider, caller, _ := requiredToolsFixture(t, role)
			// The second logical identity exists without a transport connection.
			target := "never-connected-target"
			require.NoError(t, db.ReserveManagedLaunch(context.Background(), store.ManagedAgentRecord{AgentID: target, Name: target, Cwd: "/tmp", SelectedModel: store.ManagedModel{Provider: "openai", ID: "test"}, ManifestRevision: 1, ManifestHash: "test"}, store.ManagedOperation{OperationID: "urgency-target", Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "urgency-target", CanonicalBodyHash: "body", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}))
			for i, args := range []map[string]any{{"urgent": true}, {"urgent": true, "agent_id": target}, {"urgent": false}, {"urgent": false, "agent_id": target}} {
				result := requiredToolsCall(t, m, db, provider, caller, "mecha_agent_urgency", string(rune('a'+i))+role, args)
				payload := requiredToolsPayload(t, result)
				id := caller
				if explicit, ok := args["agent_id"].(string); ok {
					id = explicit
				}
				var gotID string
				var urgent bool
				require.NoError(t, json.Unmarshal(payload["agent_id"], &gotID))
				require.NoError(t, json.Unmarshal(payload["urgent"], &urgent))
				require.Equal(t, id, gotID)
				require.Equal(t, args["urgent"], urgent)
				// Shared projection must agree with the checked SDK result.
				if id == caller {
					row, err := db.ManagedAgent(context.Background(), id)
					require.NoError(t, err)
					require.Equal(t, urgent, row.Urgent)
				}
			}
		})
	}
}

func TestEmbeddedAgentUrgencyPolicyAndSchema(t *testing.T) {
	ctx := context.Background()
	m, db, _, caller, _ := requiredToolsFixture(t, "root")
	tool := m.embeddedAgentUrgencyTool(embeddedOwner{}, nil, nil)
	require.False(t, isSystemEscapeTool(tool.Name))
	require.False(t, store.IsToolAllowedInWorkflowState(tool.Name, []string{"read"}))
	require.True(t, store.IsToolAllowedInWorkflowState(tool.Name, []string{tool.Name}))
	require.NoError(t, db.SetToolDefault(ctx, tool.Name, false))
	filtered, err := m.filterToolsForAgent(ctx, caller, []agent.AgentTool{tool})
	require.NoError(t, err)
	require.Empty(t, filtered)
	require.NoError(t, db.SyncNativeToolDefaults(ctx)) // does not reset an explicit disabled choice
	filtered, err = m.filterToolsForAgent(ctx, caller, []agent.AgentTool{tool})
	require.NoError(t, err)
	require.Empty(t, filtered)
	state, err := db.CreateWorkflowState(ctx, store.WorkflowStateRequest{Name: "restricted", AllowedTools: []string{"read"}})
	require.NoError(t, err)
	require.NoError(t, db.SetManagedAgentActiveWorkflowState(ctx, caller, &state.ID))
	filtered, err = m.filterToolsForAgent(ctx, caller, []agent.AgentTool{tool})
	require.NoError(t, err)
	require.Empty(t, filtered)
	for _, args := range []map[string]any{{}, {"urgent": nil}, {"urgent": "true"}, {"urgent": 1}, {"urgent": true, "agent_id": nil}, {"urgent": true, "agent_id": ""}, {"urgent": true, "agent_id": " "}, {"urgent": true, "from": "spoof"}, {"urgent": true, "agent_id": strings.Repeat("x", 129)}} {
		_, err := store.AgentUrgencyArguments(args, caller)
		require.Error(t, err, "%v", args)
	}
	require.True(t, tool.Parameters.Check(map[string]any{"urgent": false}))
	require.False(t, tool.Parameters.Check(map[string]any{"urgent": "false"}))
	require.False(t, tool.Parameters.Check(map[string]any{"urgent": true, "from": "spoof"}))
}

func TestEmbeddedAgentUrgencySDKProcessFence(t *testing.T) {
	ctx := context.Background()
	m, db, provider, caller, _ := requiredToolsFixture(t, "root")
	provider.SetResponses([]providers.FauxResponseStep{providers.FauxStatic(providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("mecha_agent_urgency", map[string]any{"urgent": true}, "urgency-process-race")}, ai.StopToolUse))})
	var raced bool
	m.embeddedAfterIntent = func(c context.Context) {
		raced = true
		_, err := db.MintEmbeddedProcessGeneration(c, "replacement")
		require.NoError(t, err)
	}
	m.mu.Lock()
	owner := m.embeddedOwners[caller]
	m.mu.Unlock()
	saved, err := db.ManagedAgent(ctx, caller)
	require.NoError(t, err)
	err = m.runEmbeddedSessionPrompt(context.WithValue(ctx, embeddedOwnerContextKey{}, owner), *saved, "mark urgent")
	// Runner may settle its retry turn after preserving the failed call as UNKNOWN.
	_ = err
	require.True(t, raced)
	saved, err = db.ManagedAgent(ctx, caller)
	require.NoError(t, err)
	require.False(t, saved.Urgent)
	effect, err := db.RecoverEmbeddedEffect(ctx, owner.binding, "urgency-process-race")
	require.NoError(t, err)
	require.Equal(t, "unknown", effect.State)
	require.Empty(t, effect.Result)
}

// Exercise original provider arguments through actual session admission and checked
// persistence, not just the schema/validator helpers.
func TestEmbeddedAgentUrgencySDKNullTarget(t *testing.T) {
	ctx := context.Background()
	m, db, provider, caller, _ := requiredToolsFixture(t, "root")
	m.mu.Lock()
	owner := m.embeddedOwners[caller]
	m.mu.Unlock()
	saved, err := db.ManagedAgent(ctx, caller)
	require.NoError(t, err)
	const id = "urgency-null-target"
	var continued bool
	provider.SetResponses([]providers.FauxResponseStep{
		providers.FauxStatic(providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("mecha_agent_urgency", map[string]any{"urgent": true, "agent_id": nil}, id)}, ai.StopToolUse)),
		func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			for _, msg := range h.Messages {
				if r, ok := msg.(ai.ToolResultMessage); ok && r.ToolCallID == id {
					require.True(t, r.IsError)
					continued = true
				}
			}
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "denial checked"}}, ai.StopStop)
		},
	})
	err = m.runEmbeddedSessionPrompt(context.WithValue(ctx, embeddedOwnerContextKey{}, owner), *saved, "try malformed target")
	require.NoError(t, err)
	require.True(t, continued, "invalid original argument must yield a checked error and provider continuation")
	saved, err = db.ManagedAgent(ctx, caller)
	require.NoError(t, err)
	require.False(t, saved.Urgent)
	snapshot, err := db.ReopenEmbedded(ctx, owner.binding)
	require.NoError(t, err)
	var checked bool
	for _, entry := range snapshot.Entries {
		if entry.Kind == "toolResult" {
			msg, e := ai.UnmarshalMessage(entry.Body)
			require.NoError(t, e)
			if r, ok := msg.(ai.ToolResultMessage); ok && r.ToolCallID == id {
				require.True(t, r.IsError)
				checked = true
			}
		}
	}
	require.True(t, checked)
}

func urgencySDKPrompt(t *testing.T, m *Manager, db *store.Store, provider *providers.FauxProviderRegistration, caller, id string, args map[string]any) (error, *ai.ToolResultMessage) {
	t.Helper()
	ctx := context.Background()
	m.mu.Lock()
	owner := m.embeddedOwners[caller]
	m.mu.Unlock()
	saved, err := db.ManagedAgent(ctx, caller)
	require.NoError(t, err)
	var result *ai.ToolResultMessage
	provider.SetResponses([]providers.FauxResponseStep{
		providers.FauxStatic(providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("mecha_agent_urgency", args, id)}, ai.StopToolUse)),
		func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			for _, msg := range h.Messages {
				if r, ok := msg.(ai.ToolResultMessage); ok && r.ToolCallID == id {
					copy := r
					result = &copy
				}
			}
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "checked final"}}, ai.StopStop)
		},
	})
	err = m.runEmbeddedSessionPrompt(context.WithValue(ctx, embeddedOwnerContextKey{}, owner), *saved, "perform explicit urgency request")
	return err, result
}

func TestEmbeddedAgentUrgencySDKAvailability(t *testing.T) {
	for _, policy := range []string{"configured-disabled", "workflow-denied"} {
		t.Run(policy, func(t *testing.T) {
			ctx := context.Background()
			m, db, provider, caller, _ := requiredToolsFixture(t, "worker")
			if policy == "configured-disabled" {
				require.NoError(t, db.SetToolDefault(ctx, "mecha_agent_urgency", false))
				require.NoError(t, db.SyncNativeToolDefaults(ctx))
			} else {
				state, err := db.CreateWorkflowState(ctx, store.WorkflowStateRequest{Name: "read-only", AllowedTools: []string{"read"}})
				require.NoError(t, err)
				require.NoError(t, db.SetManagedAgentActiveWorkflowState(ctx, caller, &state.ID))
			}
			err, result := urgencySDKPrompt(t, m, db, provider, caller, "denied-urgency", map[string]any{"urgent": true})
			require.NoError(t, err)
			require.NotNil(t, result)
			require.True(t, result.IsError)
			for _, tool := range m.laneFor(caller).getActiveTools() {
				require.NotEqual(t, "mecha_agent_urgency", tool.Name)
			}
			saved, err := db.ManagedAgent(ctx, caller)
			require.NoError(t, err)
			require.False(t, saved.Urgent)
			m.mu.Lock()
			owner := m.embeddedOwners[caller]
			m.mu.Unlock()
			_, err = db.RecoverEmbeddedEffect(ctx, owner.binding, "denied-urgency")
			require.ErrorIs(t, err, sql.ErrNoRows, "denial creates no effect intent")
			snapshot, err := db.ReopenEmbedded(ctx, owner.binding)
			require.NoError(t, err)
			var checked bool
			for _, entry := range snapshot.Entries {
				if entry.Kind == "toolResult" {
					msg, e := ai.UnmarshalMessage(entry.Body)
					require.NoError(t, e)
					if r, ok := msg.(ai.ToolResultMessage); ok && r.ToolCallID == "denied-urgency" {
						require.True(t, r.IsError)
						checked = true
					}
				}
			}
			require.True(t, checked, "denial must be durable, not merely a provider response")
		})
	}
}

func TestEmbeddedAgentUrgencySDKLostHandoffReplay(t *testing.T) {
	ctx := context.Background()
	m, db, provider, caller, _ := requiredToolsFixture(t, "root")
	const id = "urgency-lost-handoff"
	var committed bool
	m.SetEmbeddedUrgencyCoordinator(func(mutate func() (store.AgentUrgencyResult, bool, error)) (store.AgentUrgencyResult, error) {
		result, publish, err := mutate()
		require.NoError(t, err)
		require.True(t, publish)
		saved, e := db.ManagedAgent(ctx, caller)
		require.NoError(t, e)
		require.True(t, saved.Urgent, "fault strictly after durable domain commit")
		committed = true
		return result, errors.New("injected post-commit publication loss")
	})
	err, result := urgencySDKPrompt(t, m, db, provider, caller, id, map[string]any{"urgent": true})
	require.True(t, committed)
	_ = err // the runner may settle recovery; durable effect state below is authoritative
	require.Nil(t, result, "no checked result after lost handoff")
	m.mu.Lock()
	owner := m.embeddedOwners[caller]
	m.mu.Unlock()
	effect, err := db.RecoverEmbeddedEffect(ctx, owner.binding, id)
	require.NoError(t, err)
	require.Equal(t, "unknown", effect.State)
	require.Empty(t, effect.Result)
	snapshot, err := db.ReopenEmbedded(ctx, owner.binding)
	require.NoError(t, err)
	for _, entry := range snapshot.Entries {
		if entry.Kind == "toolResult" {
			msg, e := ai.UnmarshalMessage(entry.Body)
			require.NoError(t, e)
			if r, ok := msg.(ai.ToolResultMessage); ok {
				require.NotEqual(t, id, r.ToolCallID, "lost result must not become checked error/success")
			}
		}
	}
	_, err = db.SetAgentUrgency(ctx, caller, false)
	require.NoError(t, err)
	// Remove the fault. Original ID still has no authority over the newer write.
	m.SetEmbeddedUrgencyCoordinator(nil)
	err, result = urgencySDKPrompt(t, m, db, provider, caller, id, map[string]any{"urgent": true})
	_ = err // recovery may settle without checking the replayed call
	require.Nil(t, result)
	saved, err := db.ManagedAgent(ctx, caller)
	require.NoError(t, err)
	require.False(t, saved.Urgent)
	effect, err = db.RecoverEmbeddedEffect(ctx, owner.binding, id)
	require.NoError(t, err)
	require.Equal(t, "unknown", effect.State)
	require.Empty(t, effect.Result)
	err, result = urgencySDKPrompt(t, m, db, provider, caller, "urgency-fresh-retry", map[string]any{"urgent": true})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.IsError)
	saved, err = db.ManagedAgent(ctx, caller)
	require.NoError(t, err)
	require.True(t, saved.Urgent)
}

func TestEmbeddedAgentUrgencyNestedRuntimeAbsent(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	parent := subagentTestParent(t, db, "urgency-parent")
	provider := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	t.Cleanup(provider.Unregister)
	m := makeSubagentTestManager(t, db, provider, 1)
	t.Cleanup(func() { require.NoError(t, m.Close(ctx)) })
	registerParentOwner(m, parent)
	parentProvider := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	t.Cleanup(parentProvider.Unregister)
	m.embeddedModelResolver = func(row store.ManagedAgentRecord) *ai.Model {
		if row.AgentID == parent.AgentID {
			return parentProvider.GetModel()
		}
		// The existing child restore fallback errors on a completely empty
		// identity. Seed a checked system entry before restore, not a fake tool
		// registry or SDK result; actual child construction still owns tools.
		binding := store.EmbeddedBinding{AgentID: row.AgentID, SessionID: row.PiSessionID, Epoch: row.SessionEpoch, Generation: row.RuntimeGeneration}
		message := ai.SystemMessage{Content: ai.ContentList{ai.TextContent{Text: "nested fixture bootstrap"}}}
		body, e := json.Marshal(message)
		require.NoError(t, e)
		_, e = db.AppendEmbeddedEntryAt(ctx, binding, 0, "genesis", "nested-bootstrap", "system", body)
		require.NoError(t, e)
		return provider.GetModel()
	}
	checked := make(chan ai.ToolResultMessage, 1)
	provider.SetResponses([]providers.FauxResponseStep{
		func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			for _, msg := range h.Messages {
				if sys, ok := msg.(ai.SystemMessage); ok {
					for _, tool := range sys.ToolsAdded {
						require.NotEqual(t, "mecha_agent_urgency", tool.Name)
					}
				}
			}
			return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("mecha_agent_urgency", map[string]any{"urgent": true, "agent_id": parent.AgentID}, "nested-urgency")}, ai.StopToolUse)
		},
		func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			for _, msg := range h.Messages {
				if r, ok := msg.(ai.ToolResultMessage); ok && r.ToolCallID == "nested-urgency" {
					checked <- r
				}
			}
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "nested final"}}, ai.StopStop)
		},
	})
	_, err = m.SpawnSubagent(ctx, parent, SubagentSpawnRequest{AgentID: "urgency-child", Description: "test nested absence", Prompt: "attempt urgency"})
	require.NoError(t, err)
	select {
	case result := <-checked:
		require.True(t, result.IsError)
		require.Contains(t, result.Content[0].(ai.TextContent).Text, "not found")
	case <-time.After(4 * time.Second):
		row, e := db.ManagedSubagent(ctx, parent, "urgency-child")
		t.Fatalf("nested continuation missing: %+v err=%v", row, e)
	}
	require.Eventually(t, func() bool {
		row, e := db.ManagedSubagent(ctx, parent, "urgency-child")
		return e == nil && row.Status == "completed"
	}, 4*time.Second, 10*time.Millisecond)
	saved, err := db.ManagedAgent(ctx, parent.AgentID)
	require.NoError(t, err)
	require.False(t, saved.Urgent)
	binding := store.EmbeddedBinding{AgentID: "urgency-child", SessionID: "urgency-child-session", Epoch: 1, Generation: parent.Generation}
	role, err := db.SavedEmbeddedSessionRole(ctx, binding)
	require.NoError(t, err)
	require.Equal(t, "subagent", role)
	snapshot, err := db.ReopenEmbedded(ctx, binding)
	require.NoError(t, err)
	var durable bool
	for _, entry := range snapshot.Entries {
		if entry.Kind == "toolResult" {
			msg, e := ai.UnmarshalMessage(entry.Body)
			require.NoError(t, e)
			if r, ok := msg.(ai.ToolResultMessage); ok && r.ToolCallID == "nested-urgency" {
				require.True(t, r.IsError)
				durable = true
			}
		}
	}
	require.True(t, durable)
}

func TestEmbeddedAgentUrgencySDKUnknownTargetCheckedError(t *testing.T) {
	ctx := context.Background()
	m, db, provider, caller, _ := requiredToolsFixture(t, "root")
	err, result := urgencySDKPrompt(t, m, db, provider, caller, "urgency-unknown-target", map[string]any{"urgent": true, "agent_id": "absent-target"})
	require.NoError(t, err)
	require.NotNil(t, result, "known absent-target rejection must continue provider with checked error")
	require.True(t, result.IsError)
	require.Contains(t, result.Content[0].(ai.TextContent).Text, "not found")
	saved, err := db.ManagedAgent(ctx, caller)
	require.NoError(t, err)
	require.False(t, saved.Urgent)
	m.mu.Lock()
	owner := m.embeddedOwners[caller]
	m.mu.Unlock()
	effect, err := db.RecoverEmbeddedEffect(ctx, owner.binding, "urgency-unknown-target")
	require.NoError(t, err)
	require.Equal(t, "observed", effect.State)
	require.NotEmpty(t, effect.Result)
	_, err = db.ManagedAgent(ctx, "absent-target")
	require.ErrorIs(t, err, sql.ErrNoRows)
}
