//go:build linux

package managed

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

func TestChildBackgroundWaitSurvivesInactivityAndResumes(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "state.db"), store.Options{})
	require.NoError(t, err)
	defer db.Close()
	parent := subagentTestParent(t, db, "wait-parent")
	rec, err := db.ManagedAgent(ctx, parent.AgentID)
	require.NoError(t, err)
	release := filepath.Join(rec.Cwd, "release")
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()
	reg.SetResponses([]providers.FauxResponseStep{
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("launch_background_bash", map[string]any{"command": "while [ ! -f release ]; do sleep 0.05; done; exit 7"}, "launch")}, ai.StopToolUse)
		},
		func(req ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			for _, msg := range req.Messages {
				if r, ok := msg.(ai.ToolResultMessage); ok && r.ToolCallID == "launch" {
					for _, part := range r.Content {
						if text, ok := part.(ai.TextContent); ok {
							var j struct {
								ID string `json:"job_id"`
							}
							if json.Unmarshal([]byte(text.Text), &j) == nil && j.ID != "" {
								return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("wait_background_bash_job", map[string]any{"job_id": j.ID}, "wait")}, ai.StopToolUse)
							}
						}
					}
				}
			}
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "launch failed"}}, ai.StopStop)
		},
		func(req ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			for _, msg := range req.Messages {
				if r, ok := msg.(ai.ToolResultMessage); ok && r.ToolCallID == "wait" && !r.IsError {
					return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "wait completed"}}, ai.StopStop)
				}
			}
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "wait failed"}}, ai.StopStop)
		},
	})
	mgr := makeSubagentTestManager(t, db, reg, 1)
	defer mgr.Close(ctx)
	registerParentOwner(mgr, parent)
	child := "wait-child"
	_, err = mgr.SpawnSubagent(ctx, parent, SubagentSpawnRequest{AgentID: child, Description: "wait", Prompt: "wait", Timeout: 10 * time.Minute})
	require.NoError(t, err)
	var task *subagentTask
	require.Eventually(t, func() bool {
		mgr.subagentsMu.Lock()
		task = mgr.subagentsActive[child]
		mgr.subagentsMu.Unlock()
		if task == nil {
			return false
		}
		task.mu.Lock()
		defer task.mu.Unlock()
		return task.lastStage == "executing_tool:wait_background_bash_job" && task.backgroundWaits > 0
	}, 10*time.Second, 10*time.Millisecond)
	task.mu.Lock()
	task.lastActivity = time.Now().Add(-100 * time.Second)
	task.mu.Unlock()
	mgr.TimeoutExpiredSubagents(10 * time.Minute)
	task.mu.Lock()
	stopped := task.stopped
	inactive := task.inactivityTimedOut
	task.mu.Unlock()
	require.False(t, stopped)
	require.False(t, inactive)
	require.NoError(t, os.WriteFile(release, nil, 0600))
	require.Eventually(t, func() bool {
		r, e := db.ManagedSubagent(ctx, parent, child)
		return e == nil && r.Status == "completed" && r.ResultText == "wait completed"
	}, 10*time.Second, 20*time.Millisecond)
}

func TestChildWaitWatchdogCancellationAndDeadline(t *testing.T) {
	for _, mode := range []string{"cancel", "deadline", "shutdown"} {
		t.Run(mode, func(t *testing.T) {
			jobs := NewBackgroundBashManager(nil)
			defer jobs.Close()
			job, err := jobs.Launch("child", BackgroundBashLaunch{Command: "sleep 30"})
			require.NoError(t, err)
			task := &subagentTask{lastActivity: time.Now().Add(-time.Minute), startTime: time.Now(), timeout: time.Hour}
			tool := task.wrapBackgroundWait([]agent.AgentTool{backgroundBashWaitTool(jobs, "child")}, time.Now)[0]
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := tool.Execute(ctx, "wait", map[string]any{"job_id": job.ID}, nil); done <- err }()
			require.Eventually(t, func() bool { task.mu.Lock(); defer task.mu.Unlock(); return task.backgroundWaits == 1 }, time.Second, time.Millisecond)
			switch mode {
			case "cancel":
				cancel()
			case "shutdown":
				jobs.Close()
			case "deadline":
				task.mu.Lock()
				task.startTime = time.Now().Add(-2 * time.Hour)
				task.cancel = cancel
				task.mu.Unlock()
				mgr := &Manager{subagentsActive: map[string]*subagentTask{"child": task}, clock: deterministic.NewFixedClock(time.Now())}
				mgr.TimeoutExpiredSubagents(time.Hour)
			}
			select {
			case err := <-done:
				if mode != "shutdown" {
					require.ErrorIs(t, err, context.Canceled)
				} else {
					require.NoError(t, err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("wait did not unblock")
			}
			task.mu.Lock()
			require.Zero(t, task.backgroundWaits)
			require.WithinDuration(t, time.Now(), task.lastActivity, time.Second)
			task.mu.Unlock()
		})
	}
}
