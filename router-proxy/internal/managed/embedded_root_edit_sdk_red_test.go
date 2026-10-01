package managed

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// RED: a real root SDK turn must edit its own saved project, then read the result.
// Stop at the first missing advertisement; a proposed provider call is not execution.
func TestEmbeddedRootEditReadSDKRED(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	project := filepath.Join(base, "project")
	require.NoError(t, os.Mkdir(project, 0700))
	note := filepath.Join(project, "note.txt")
	require.NoError(t, os.WriteFile(note, []byte("before\n"), 0600))
	db, err := store.Open(ctx, filepath.Join(base, "router.db"), store.Options{})
	require.NoError(t, err)
	defer db.Close()
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()
	const editID, readID = "root-edit-1", "root-read-1"
	editArgs := map[string]any{"path": "note.txt", "edits": []any{map[string]any{"oldText": "before", "newText": "after"}}}
	type observation struct {
		tools  []ai.Tool
		result *ai.ToolResultMessage
	}
	seen := make(chan observation, 3)
	resultFor := func(h ai.TranscriptContext, id string) *ai.ToolResultMessage {
		for _, msg := range h.Messages {
			if r, ok := msg.(ai.ToolResultMessage); ok && r.ToolCallID == id {
				copy := r
				return &copy
			}
		}
		return nil
	}
	reg.SetResponses([]providers.FauxResponseStep{
		func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			var tools []ai.Tool
			for _, msg := range h.Messages {
				if sys, ok := msg.(ai.SystemMessage); ok {
					tools = append(tools, sys.ToolsAdded...)
				}
			}
			seen <- observation{tools: tools}
			return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("edit", editArgs, editID)}, ai.StopToolUse)
		},
		func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			seen <- observation{result: resultFor(h, editID)}
			return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("read", map[string]any{"path": "note.txt"}, readID)}, ai.StopToolUse)
		},
		func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			seen <- observation{result: resultFor(h, readID)}
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "done"}}, ai.StopStop)
		},
	})
	m, err := NewManager(ManagerOptions{Store: db, Manifest: manifest,
		Config:                Config{SessionRoot: filepath.Join(base, "sessions"), AllowedWorkingDirectoryRoots: []string{project}},
		CatalogSource:         CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
		EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() },
		CodexCredentialStore:  testfixture.CodexAdmission(t),
	})
	require.NoError(t, err)
	defer m.Close(ctx)
	_, err = m.LaunchEmbeddedRoot(ctx, LaunchRequest{Name: "edit-root", Cwd: project, Runtime: "embedded", Model: &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"}, InitialInstruction: "edit and read note.txt", RequestID: "a5555555-5555-4555-8555-555555555591"})
	require.NoError(t, err)
	next := func() observation {
		select {
		case got := <-seen:
			return got
		case <-time.After(4 * time.Second):
			t.Fatal("SDK continuation timed out")
			return observation{}
		}
	}
	first := next()
	advertised := false
	for _, tool := range first.tools {
		if tool.Name == "edit" {
			advertised = true
		}
	}
	require.True(t, advertised, "edit not advertised to root (first genuine RED; assertions below unexecuted)")

	// The owning root's persisted cwd and role, not a worker or caller cwd,
	// must fence both SDK calls and their durable intents.
	agents, err := db.ManagedAgents(ctx)
	require.NoError(t, err)
	require.Len(t, agents, 1)
	require.Equal(t, project, agents[0].Cwd)
	require.Eventually(t, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		_, ok := m.embeddedOwners[agents[0].AgentID]
		return ok
	}, 4*time.Second, 10*time.Millisecond)
	m.mu.Lock()
	owner := m.embeddedOwners[agents[0].AgentID]
	m.mu.Unlock()
	require.Equal(t, agents[0].AgentID, owner.binding.AgentID)
	role, err := db.EmbeddedSessionRole(ctx, owner.binding)
	require.NoError(t, err)
	require.Equal(t, "root", role)
	for _, call := range []struct {
		id, name, want string
		args           map[string]any
	}{
		{editID, "edit", "Successfully edited note.txt", editArgs},
		{readID, "read", "after\n", map[string]any{"path": "note.txt"}},
	} {
		got := next().result
		require.NotNil(t, got)
		require.Equal(t, call.id, got.ToolCallID)
		require.Equal(t, call.name, got.ToolName)
		require.False(t, got.IsError)
		require.Len(t, got.Content, 1)
		require.Equal(t, call.want, got.Content[0].(ai.TextContent).Text)
		effect, err := db.RecoverEmbeddedEffect(ctx, owner.binding, call.id)
		require.NoError(t, err)
		var intent ai.ToolCall
		require.NoError(t, json.Unmarshal(effect.Intent, &intent))
		require.Equal(t, call.id, intent.ID)
		require.Equal(t, call.name, intent.Name)
		require.Equal(t, call.args, intent.Arguments)
	}
	bytes, err := os.ReadFile(note)
	require.NoError(t, err)
	require.Equal(t, "after\n", string(bytes))
	fd, err := pinEmbeddedReadRoot(project)
	require.NoError(t, err)
	defer unix.Close(fd)
	stale := owner
	stale.binding.OwnerToken++
	_, err = m.embeddedProjectEditTool(stale, fd, func(string) bool { return true }).Execute(ctx, editID, editArgs, nil)
	require.Error(t, err, "stale root cannot edit")
	bytes, err = os.ReadFile(note)
	require.NoError(t, err)
	require.Equal(t, "after\n", string(bytes))
}

func TestEmbeddedRootEditSDKDenials(t *testing.T) {
	for _, tc := range []struct {
		name, path, original, old, replacement string
		preIntent                              bool
	}{
		{"no-match", "note.txt", "before\n", "absent", "after", false},
		{"multiple-match", "note.txt", "before before", "before", "after", false},
		{"traversal", "../outside.txt", "before\n", "before", "after", true},
		{"symlink", "escape.txt", "before\n", "before", "after", false},
		{"oversize", "note.txt", "before\n", "before", strings.Repeat("x", embeddedReadLimit), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			base := t.TempDir()
			project := filepath.Join(base, "project")
			require.NoError(t, os.Mkdir(project, 0700))
			note := filepath.Join(project, "note.txt")
			require.NoError(t, os.WriteFile(note, []byte(tc.original), 0600))
			outside := filepath.Join(base, "outside.txt")
			require.NoError(t, os.WriteFile(outside, []byte("before\n"), 0600))
			require.NoError(t, os.Symlink(outside, filepath.Join(project, "escape.txt")))
			db, err := store.Open(ctx, filepath.Join(base, "router.db"), store.Options{})
			require.NoError(t, err)
			defer db.Close()
			manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
			require.NoError(t, err)
			reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
			defer reg.Unregister()
			const id = "root-edit-denial"
			args := map[string]any{"path": tc.path, "edits": []any{map[string]any{"oldText": tc.old, "newText": tc.replacement}}}
			seen := make(chan *ai.ToolResultMessage, 1)
			reg.SetResponses([]providers.FauxResponseStep{
				func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
					return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("edit", args, id)}, ai.StopToolUse)
				},
				func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
					for _, msg := range h.Messages {
						if result, ok := msg.(ai.ToolResultMessage); ok && result.ToolCallID == id {
							copy := result
							seen <- &copy
						}
					}
					return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "done"}}, ai.StopStop)
				},
			})
			m, err := NewManager(ManagerOptions{Store: db, Manifest: manifest,
				Config:                Config{SessionRoot: filepath.Join(base, "sessions"), AllowedWorkingDirectoryRoots: []string{project}},
				CatalogSource:         CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
				EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() },
				CodexCredentialStore:  testfixture.CodexAdmission(t),
			})
			require.NoError(t, err)
			defer m.Close(ctx)
			_, err = m.LaunchEmbeddedRoot(ctx, LaunchRequest{Name: "denial-root", Cwd: project, Runtime: "embedded", Model: &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"}, InitialInstruction: "try edit", RequestID: "a5555555-5555-4555-8555-555555555592"})
			require.NoError(t, err)
			var result *ai.ToolResultMessage
			select {
			case result = <-seen:
			case <-time.After(4 * time.Second):
				t.Fatal("SDK denial continuation timed out")
			}
			require.Equal(t, id, result.ToolCallID)
			require.Equal(t, "edit", result.ToolName)
			require.True(t, result.IsError)
			agents, err := db.ManagedAgents(ctx)
			require.NoError(t, err)
			require.Len(t, agents, 1)
			require.Eventually(t, func() bool {
				m.mu.Lock()
				defer m.mu.Unlock()
				_, ok := m.embeddedOwners[agents[0].AgentID]
				return ok
			}, 4*time.Second, 10*time.Millisecond)
			m.mu.Lock()
			binding := m.embeddedOwners[agents[0].AgentID].binding
			m.mu.Unlock()
			effect, err := db.RecoverEmbeddedEffect(ctx, binding, id)
			if tc.preIntent {
				require.True(t, errors.Is(err, sql.ErrNoRows), "traversal must be denied before intent: %v", err)
			} else {
				require.NoError(t, err)
				var intent ai.ToolCall
				require.NoError(t, json.Unmarshal(effect.Intent, &intent))
				require.Equal(t, args, intent.Arguments)
			}
			bytes, err := os.ReadFile(note)
			require.NoError(t, err)
			require.Equal(t, tc.original, string(bytes))
			bytes, err = os.ReadFile(outside)
			require.NoError(t, err)
			require.Equal(t, "before\n", string(bytes))
		})
	}
}
