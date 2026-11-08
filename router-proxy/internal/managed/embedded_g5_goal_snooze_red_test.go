package managed

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// Phase 1: real SDK calls persist a 070 preference and a checked tool result.
// Delayed 069 reservation and due-time provider scheduling are not asserted here.
func TestEmbeddedG5GoalSnoozeFixedClockSDKRED(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "router.db")
	clock := deterministic.NewFixedClock(time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC))
	db, err := store.Open(ctx, path, store.Options{Clock: clock})
	require.NoError(t, err)
	defer db.Close()
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	type observation struct {
		tools  []ai.Tool
		result *ai.ToolResultMessage
	}
	type caller struct {
		name    string
		reg     *providers.FauxProviderRegistration
		seen    chan observation
		release chan struct{}
		starts  atomic.Int32
		binding store.EmbeddedBinding
	}
	callers := []*caller{
		{name: "g5-snooze-root", seen: make(chan observation, 4), release: make(chan struct{})},
		{name: "g5-snooze-worker", seen: make(chan observation, 4), release: make(chan struct{})},
	}
	for _, c := range callers {
		c := c
		c.reg = providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
		defer c.reg.Unregister()
		c.reg.SetResponses([]providers.FauxResponseStep{
			func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
				c.starts.Add(1)
				var o observation
				for _, msg := range h.Messages {
					if system, ok := msg.(ai.SystemMessage); ok {
						o.tools = append(o.tools, system.ToolsAdded...)
					}
				}
				c.seen <- o
				return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("goal", map[string]any{"action": "set", "goal": "finish " + c.name}, c.name+"-set")}, ai.StopToolUse)
			},
			func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
				c.starts.Add(1)
				var o observation
				for _, msg := range h.Messages {
					if r, ok := msg.(ai.ToolResultMessage); ok && r.ToolCallID == c.name+"-set" {
						copy := r
						o.result = &copy
					}
				}
				c.seen <- o
				return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("goal_snooze", map[string]any{"duration_ms": 1000}, c.name+"-snooze")}, ai.StopToolUse)
			},
			func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
				c.starts.Add(1)
				var o observation
				for _, msg := range h.Messages {
					if r, ok := msg.(ai.ToolResultMessage); ok && r.ToolCallID == c.name+"-snooze" {
						copy := r
						o.result = &copy
					}
				}
				c.seen <- o
				<-c.release // Persisted preference must be visible before checked final.
				return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "initial final"}}, ai.StopStop)
			},
			func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
				c.starts.Add(1)
				return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "one delayed continuation"}}, ai.StopStop)
			},
		})
	}
	m, err := NewManager(ManagerOptions{Store: db, Manifest: manifest, Clock: clock,
		Config:               Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}},
		CatalogSource:        CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
		CodexCredentialStore: testfixture.CodexAdmission(t),
		EmbeddedModelResolver: func(saved store.ManagedAgentRecord) *ai.Model {
			for _, c := range callers {
				if c.name == saved.AgentID {
					return c.reg.GetModel()
				}
			}
			return nil
		},
	})
	require.NoError(t, err)
	defer func() {
		// Assertions may fail while both provider turns are blocked. Unblock them
		// before Close waits for the sessions, even on require's FailNow path.
		for _, c := range callers {
			select {
			case <-c.release:
			default:
				close(c.release)
			}
		}
		_ = m.Close(ctx)
	}()
	setEmbeddedRecoveryGeneration(t, ctx, db, m)
	for i, c := range callers {
		request := LaunchRequest{Name: c.name, Cwd: root, Runtime: "embedded", Model: &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"}, InitialInstruction: "set and snooze your own goal in this turn", RequestID: []string{"a5555555-5555-4555-8555-555555555591", "a5555555-5555-4555-8555-555555555592"}[i]}
		var launched LaunchResult
		if i == 0 {
			launched, err = m.LaunchEmbeddedRoot(ctx, request)
		} else {
			launched, err = m.Launch(ctx, request)
		}
		require.NoError(t, err)
		c.binding = store.EmbeddedBinding{AgentID: launched.Agent.AgentID, SessionID: launched.Agent.PiSessionID, Epoch: launched.Agent.SessionEpoch, Generation: launched.Agent.RuntimeGeneration}
		role, err := db.EmbeddedSessionRole(ctx, c.binding)
		require.NoError(t, err)
		if i == 0 {
			require.Equal(t, "root", role)
		} else {
			require.Equal(t, "worker", role)
		}
	}
	require.NotEqual(t, callers[0].binding.SessionID, callers[1].binding.SessionID)
	for _, c := range callers {
		var first observation
		select {
		case first = <-c.seen:
		case <-time.After(4 * time.Second):
			t.Fatalf("%s: setup failure: SDK provider never reached tool list", c.name)
		}
		advertised := false
		for _, tool := range first.tools {
			if tool.Name == "goal_snooze" {
				advertised = true
			}
		}
		require.True(t, advertised, "%s: goal_snooze absent from real SDK tool list", c.name)
	}
	ro, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	require.NoError(t, err)
	defer ro.Close()
	for _, c := range callers {
		var set, snooze observation
		select {
		case set = <-c.seen:
		case <-time.After(4 * time.Second):
			t.Fatalf("%s: goal set continuation missing", c.name)
		}
		require.NotNil(t, set.result)
		require.Equal(t, c.name+"-set", set.result.ToolCallID)
		require.Equal(t, "goal", set.result.ToolName)
		require.False(t, set.result.IsError)
		select {
		case snooze = <-c.seen:
		case <-time.After(4 * time.Second):
			t.Fatalf("%s: goal_snooze result missing", c.name)
		}
		require.NotNil(t, snooze.result)
		require.Equal(t, c.name+"-snooze", snooze.result.ToolCallID)
		require.Equal(t, "goal_snooze", snooze.result.ToolName)
		require.False(t, snooze.result.IsError)
		require.Len(t, snooze.result.Content, 1)
		text, ok := snooze.result.Content[0].(ai.TextContent)
		require.True(t, ok)
		var result store.EmbeddedGoalSnoozePreference
		require.NoError(t, json.Unmarshal([]byte(text.Text), &result))

		// Tie the 070 preference to this exact saved SDK user entry and revision.
		var userSeq int64
		var userDigest string
		require.NoError(t, ro.QueryRowContext(ctx, `SELECT seq,digest FROM embedded_entries WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND kind='user'`, c.binding.AgentID, c.binding.SessionID, c.binding.Epoch, c.binding.Generation).Scan(&userSeq, &userDigest))
		require.EqualValues(t, 2, userSeq)
		require.NotEmpty(t, userDigest)
		var revision, preferenceSeq, active int64
		var preferenceDigest, generation, role, callID, due string
		require.NoError(t, ro.QueryRowContext(ctx, `SELECT generation,role,user_seq,user_digest,goal_revision,call_id,due_at,active FROM embedded_goal_snooze_preferences WHERE agent_id=? AND session_id=? AND epoch=?`, c.binding.AgentID, c.binding.SessionID, c.binding.Epoch).Scan(&generation, &role, &preferenceSeq, &preferenceDigest, &revision, &callID, &due, &active))
		require.Equal(t, c.binding.Generation, generation)
		if c == callers[0] {
			require.Equal(t, "root", role)
		} else {
			require.Equal(t, "worker", role)
		}
		require.Equal(t, userSeq, preferenceSeq)
		require.Equal(t, userDigest, preferenceDigest)
		require.EqualValues(t, 1, revision)
		require.Equal(t, c.name+"-snooze", callID)
		require.Equal(t, clock.Now().Add(time.Second).Format("2006-01-02T15:04:05.000Z"), due)
		require.EqualValues(t, 1, active)
		require.Equal(t, store.EmbeddedGoalSnoozeTurn{UserSeq: userSeq, UserDigest: userDigest, GoalRevision: revision}, result.EmbeddedGoalSnoozeTurn)
		require.Equal(t, c.binding.AgentID, result.AgentID)
		require.Equal(t, c.binding.SessionID, result.SessionID)
		require.Equal(t, c.binding.Generation, result.Generation)
		require.Equal(t, role, result.Role)
		require.Equal(t, callID, result.CallID)
		require.Equal(t, due, result.DueAt)
		require.EqualValues(t, 1000, result.DurationMS)
		require.EqualValues(t, 1, result.PolicyVersion)
		require.True(t, result.Active)

		// The SDK saw the checked result, not just a successful Store write.
		var checkedBody []byte
		require.NoError(t, ro.QueryRowContext(ctx, `SELECT body FROM embedded_entries WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND kind='toolResult' ORDER BY seq DESC LIMIT 1`, c.binding.AgentID, c.binding.SessionID, c.binding.Epoch, c.binding.Generation).Scan(&checkedBody))
		checked, err := ai.UnmarshalMessage(checkedBody)
		require.NoError(t, err)
		checkedResult, ok := checked.(ai.ToolResultMessage)
		require.True(t, ok)
		require.Equal(t, *snooze.result, checkedResult)
		close(c.release)
	}
	// Later phase (unexecuted): 069 NOT_DONE reservation, due_at and timer/provider behavior.

}
