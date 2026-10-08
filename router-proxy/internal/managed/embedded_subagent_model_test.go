package managed

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

func TestSubagentStreamContextNormalizedSystemMessageAndTools(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "subagent_model_adapter.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	parent := subagentTestParent(t, db, "parent-model-adapter")
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	var mu sync.Mutex
	var defaultReqMessages []ai.Message
	var planReqMessages []ai.Message
	defaultDone := make(chan struct{}, 1)
	planDone := make(chan struct{}, 1)

	capture := func(req ai.TranscriptContext) {
		sysPrompt := ai.GetCurrentSystemPrompt(req.Messages)
		mu.Lock()
		defer mu.Unlock()
		if strings.Contains(sysPrompt, "CRITICAL: READ-ONLY MODE") {
			planReqMessages = req.Messages
			select {
			case planDone <- struct{}{}:
			default:
			}
		} else {
			// Only capture the child subagent's request, not the parent wake notification
			for _, m := range req.Messages {
				if u, ok := m.(ai.UserMessage); ok {
					for _, c := range u.Content {
						if t, ok := c.(ai.TextContent); ok && t.Text == "run default check" {
							defaultReqMessages = req.Messages
							select {
							case defaultDone <- struct{}{}:
							default:
							}
							return
						}
					}
				}
			}
		}
	}

	steps := make([]providers.FauxResponseStep, 10)
	for i := range steps {
		steps[i] = func(req ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			capture(req)
			return providers.FauxAssistantMessage(ai.ContentList{
				ai.TextContent{Text: "task finished"},
			}, ai.StopStop)
		}
	}
	reg.SetResponses(steps)

	mgr := makeSubagentTestManager(t, db, reg, 4)
	defer mgr.Close(ctx)
	registerParentOwner(mgr, parent)

	// 1. Test default subagent
	resDefault, err := mgr.SpawnSubagent(ctx, parent, SubagentSpawnRequest{
		AgentID:     "subagent-model-default",
		Description: "default model stream check",
		Prompt:      "run default check",
	})
	require.NoError(t, err)
	require.Equal(t, "running", resDefault.Status)

	select {
	case <-defaultDone:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for default subagent stream callback")
	}

	mu.Lock()
	capturedDefault := defaultReqMessages
	mu.Unlock()

	require.NotEmpty(t, capturedDefault)

	var defaultSysMessages []ai.SystemMessage
	for _, m := range capturedDefault {
		if sys, ok := m.(ai.SystemMessage); ok {
			defaultSysMessages = append(defaultSysMessages, sys)
		}
	}
	require.Equal(t, 1, len(defaultSysMessages), "must have exactly one leading system message for default subagent")
	firstDefaultSys := defaultSysMessages[0]
	defaultToolNames := make([]string, 0, len(firstDefaultSys.ToolsAdded))
	for _, tool := range firstDefaultSys.ToolsAdded {
		defaultToolNames = append(defaultToolNames, tool.Name)
	}
	require.ElementsMatch(t, []string{"read", "edit", "write", "bash"}, defaultToolNames, "initial system message must contain all 4 tools")
	require.NotEmpty(t, ai.GetCurrentSystemPrompt(capturedDefault), "system prompt must be present")

	// 2. Test Plan subagent
	resPlan, err := mgr.SpawnSubagent(ctx, parent, SubagentSpawnRequest{
		AgentID:      "subagent-model-plan",
		Description:  "plan model stream check",
		SubagentType: "Plan",
		Prompt:       "run plan check",
	})
	require.NoError(t, err)
	require.Equal(t, "running", resPlan.Status)

	select {
	case <-planDone:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for plan subagent stream callback")
	}

	mu.Lock()
	capturedPlan := planReqMessages
	mu.Unlock()

	require.NotEmpty(t, capturedPlan)

	var planSysMessages []ai.SystemMessage
	for _, m := range capturedPlan {
		if sys, ok := m.(ai.SystemMessage); ok {
			planSysMessages = append(planSysMessages, sys)
		}
	}
	require.Equal(t, 1, len(planSysMessages), "must have exactly one leading system message for Plan subagent")
	firstPlanSys := planSysMessages[0]
	planToolNames := make([]string, 0, len(firstPlanSys.ToolsAdded))
	for _, tool := range firstPlanSys.ToolsAdded {
		planToolNames = append(planToolNames, tool.Name)
	}
	require.ElementsMatch(t, []string{"read", "edit", "write", "bash"}, planToolNames, "initial system message must contain all 4 tools for Plan subagent")
	require.Contains(t, ai.GetCurrentSystemPrompt(capturedPlan), "CRITICAL: READ-ONLY MODE - NO FILE MODIFICATIONS")
}
