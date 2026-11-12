package managed

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

// BB-PROMOTE-001/002: actual managed SDK dispatch and SQLite owner binding;
// the faux provider is the only substituted boundary. No router is started.
func TestPromotionTDDManagedRoles(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("promotion is Linux-only")
	}
	for _, role := range []string{"root", "worker"} {
		t.Run(role, func(t *testing.T) {
			m, db, provider, caller, dir := requiredToolsFixture(t, role)
			fast := requiredToolsCall(t, m, db, provider, caller, "bash", "fast-"+role, map[string]any{"command": "printf fast-promotion-check", "timeout": 1})
			require.Contains(t, fmt.Sprint(fast.Content), "fast-promotion-check")
			m.mu.Lock()
			owner := m.embeddedOwners[caller]
			jobs := m.backgroundBash
			m.mu.Unlock()
			require.Empty(t, jobs.List(backgroundBashOwnerKey(owner)), "fast command must not create a job")
			starts, release, finished := filepath.Join(dir, "starts"), filepath.Join(dir, "release"), filepath.Join(dir, "finished")
			t.Cleanup(func() { _ = os.WriteFile(release, []byte("release"), 0600) })
			// Paths are generated in a temporary directory controlled by the fixture.
			command := fmt.Sprintf("echo $$ >> %q; echo before-promotion; while [ ! -f %q ]; do sleep 0.05; done; echo $$ > %q; echo after-promotion", starts, release, finished)
			result := requiredToolsCall(t, m, db, provider, caller, "bash", "slow-"+role, map[string]any{"command": command, "timeout": 1})
			require.Len(t, result.Content, 1)
			text, ok := result.Content[0].(ai.TextContent)
			require.True(t, ok)
			var payload struct {
				JobID string `json:"job_id"`
			}
			require.NoError(t, json.Unmarshal([]byte(text.Text), &payload), "slow bash must return a job handle, not a timeout: %s", text.Text)
			require.NotEmpty(t, payload.JobID)
			job, err := jobs.Get(backgroundBashOwnerKey(owner), payload.JobID)
			require.NoError(t, err)
			require.Equal(t, "running", job.Status)
			require.Contains(t, job.Output, "before-promotion")
			require.NoError(t, os.WriteFile(release, []byte("release"), 0600))
			require.Eventually(t, func() bool {
				j, e := jobs.Get(backgroundBashOwnerKey(owner), payload.JobID)
				job = j
				return e == nil && j.Status == "succeeded"
			}, 4*time.Second, 20*time.Millisecond)
			require.Contains(t, job.Output, "after-promotion")
			started, err := os.ReadFile(starts)
			require.NoError(t, err)
			ended, err := os.ReadFile(finished)
			require.NoError(t, err)
			require.Equal(t, strings.TrimSpace(string(started)), strings.TrimSpace(string(ended)), "same shell PID must cross the handoff")
			require.Len(t, strings.Fields(string(started)), 1, "command must start exactly once")
		})
	}
}

// The faux model drives a real managed child through its registered bash and
// get tools. SQLite and process execution are real; no router is started.
func TestPromotionTDDSubagentExecutor(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("promotion is Linux-only")
	}
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "child.db"), store.Options{})
	require.NoError(t, err)
	defer db.Close()
	parent := subagentTestParent(t, db, "promotion-parent")
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()
	mgr := makeSubagentTestManager(t, db, reg, 1)
	defer mgr.Close(ctx)
	registerParentOwner(mgr, parent)
	mgr.mu.Lock()
	parentOwner := mgr.embeddedOwners[parent.AgentID]
	mgr.mu.Unlock()
	dir := t.TempDir()
	release, starts, finished := filepath.Join(dir, "release"), filepath.Join(dir, "starts"), filepath.Join(dir, "finished")
	t.Cleanup(func() { _ = os.WriteFile(release, nil, 0600) })
	command := fmt.Sprintf("echo $$ >> %q; echo before; while [ ! -f %q ]; do sleep 0.05; done; echo $$ > %q; echo after", starts, release, finished)
	promoted := make(chan ai.ToolResultMessage, 1)
	observed := make(chan ai.ToolResultMessage, 1)
	proceed := make(chan string, 1)
	reg.SetResponses([]providers.FauxResponseStep{
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("bash", map[string]any{"command": command, "timeout": 1}, "child-bash")}, ai.StopToolUse)
		},
		func(req ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			for _, msg := range req.Messages {
				if r, ok := msg.(ai.ToolResultMessage); ok && r.ToolCallID == "child-bash" {
					promoted <- r
					break
				}
			}
			id := <-proceed
			return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("get_background_bash_job", map[string]any{"job_id": id}, "child-get")}, ai.StopToolUse)
		},
		func(req ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			for _, msg := range req.Messages {
				if r, ok := msg.(ai.ToolResultMessage); ok && r.ToolCallID == "child-get" {
					observed <- r
					break
				}
			}
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "done"}}, ai.StopStop)
		},
	})
	_, err = mgr.SpawnSubagent(ctx, parent, SubagentSpawnRequest{AgentID: "promotion-child", Description: "promotion", Prompt: "run command"})
	require.NoError(t, err)
	// Always unblock the model if an assertion fails.
	defer func() {
		select {
		case proceed <- "invalid":
		default:
		}
	}()
	var result ai.ToolResultMessage
	select {
	case result = <-promoted:
	case <-time.After(10 * time.Second):
		t.Fatal("child bash did not return")
	}
	require.False(t, result.IsError, "%+v", result)
	require.Len(t, result.Content, 1)
	text, ok := result.Content[0].(ai.TextContent)
	require.True(t, ok)
	var payload struct {
		JobID string `json:"job_id"`
	}
	require.NoError(t, json.Unmarshal([]byte(text.Text), &payload), "%s", text.Text)
	require.NotEmpty(t, payload.JobID)
	mgr.mu.Lock()
	child := mgr.embeddedOwners["promotion-child"]
	jobs := mgr.backgroundBash
	mgr.mu.Unlock()
	job, err := jobs.Get(backgroundBashOwnerKey(child), payload.JobID)
	require.NoError(t, err)
	require.Equal(t, "running", job.Status)
	require.Contains(t, job.Output, "before")
	_, err = jobs.Get(backgroundBashOwnerKey(parentOwner), payload.JobID)
	require.Error(t, err, "parent must not access child job")
	require.NoError(t, os.WriteFile(release, nil, 0600))
	require.Eventually(t, func() bool {
		j, e := jobs.Get(backgroundBashOwnerKey(child), payload.JobID)
		return e == nil && j.Status == "succeeded"
	}, 10*time.Second, 20*time.Millisecond)
	proceed <- payload.JobID
	select {
	case result = <-observed:
	case <-time.After(10 * time.Second):
		t.Fatal("child get did not return")
	}
	require.False(t, result.IsError, "%+v", result)
	require.Len(t, result.Content, 1)
	text, ok = result.Content[0].(ai.TextContent)
	require.True(t, ok)
	require.Contains(t, text.Text, `"status":"succeeded"`)
	require.Contains(t, text.Text, "after")
	started, err := os.ReadFile(starts)
	require.NoError(t, err)
	ended, err := os.ReadFile(finished)
	require.NoError(t, err)
	require.Len(t, strings.Fields(string(started)), 1)
	require.Equal(t, strings.TrimSpace(string(started)), strings.TrimSpace(string(ended)))
	require.Eventually(t, func() bool {
		rec, e := db.ManagedSubagent(ctx, parent, "promotion-child")
		return e == nil && rec.Status == "completed"
	}, 5*time.Second, 20*time.Millisecond)
}
