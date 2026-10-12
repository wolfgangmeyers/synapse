package managed_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

func TestCalculateContextUsage(t *testing.T) {
	// Normal calculation
	tokens := 440120
	window := 1048576
	u := managed.CalculateContextUsage(&tokens, window)
	require.NotNil(t, u)
	require.NotNil(t, u.Tokens)
	require.Equal(t, 440120, *u.Tokens)
	require.Equal(t, 1048576, u.ContextWindow)
	require.NotNil(t, u.Percent)
	require.InDelta(t, 42.0, *u.Percent, 0.1)

	// Nil tokens
	uNil := managed.CalculateContextUsage(nil, 1048576)
	require.NotNil(t, uNil)
	require.Nil(t, uNil.Tokens)
	require.Equal(t, 1048576, uNil.ContextWindow)
	require.Nil(t, uNil.Percent)

	// Invalid window (<= 0)
	uInvalidWin := managed.CalculateContextUsage(&tokens, 0)
	require.NotNil(t, uInvalidWin)
	require.NotNil(t, uInvalidWin.Tokens)
	require.Equal(t, 0, uInvalidWin.ContextWindow)
	require.Nil(t, uInvalidWin.Percent)

	// Negative tokens
	neg := -5
	uNeg := managed.CalculateContextUsage(&neg, 1000)
	require.NotNil(t, uNeg)
	require.Nil(t, uNeg.Percent)
}

func TestPublicAgentContextUsageProjection(t *testing.T) {
	tokens := 50000
	window := 100000
	expectedPct := 50.0

	// 1. Embedded agent with attached ContextUsage struct
	embeddedAgent := &store.ManagedAgentRecord{
		AgentID:       "worker-emb",
		Name:          "worker-emb",
		Runtime:       "embedded",
		Cwd:           "/tmp",
		SelectedModel: store.ManagedModel{Provider: "google", ID: "gemini-3.8-flash"},
		ContextUsage: &managed.ContextUsage{
			Tokens:        &tokens,
			ContextWindow: window,
			Percent:       &expectedPct,
		},
	}
	proj := managed.PublicAgent(embeddedAgent)
	require.NotNil(t, proj["context_usage"])
	usage, ok := proj["context_usage"].(*managed.ContextUsage)
	require.True(t, ok)
	require.Equal(t, 50000, *usage.Tokens)
	require.Equal(t, 100000, usage.ContextWindow)
	require.Equal(t, 50.0, *usage.Percent)

	// Also verify PublicAgentWithGoal preserves it
	withGoal := managed.PublicAgentWithGoal(embeddedAgent, nil)
	require.NotNil(t, withGoal["context_usage"])

	// 2. RPC agent should NOT have context_usage attached automatically
	rpcAgent := &store.ManagedAgentRecord{
		AgentID:       "worker-rpc",
		Name:          "worker-rpc",
		Runtime:       "rpc",
		Cwd:           "/tmp",
		SelectedModel: store.ManagedModel{Provider: "google", ID: "gemini-3.8-flash"},
	}
	projRPC := managed.PublicAgent(rpcAgent)
	require.Nil(t, projRPC["context_usage"])

	// 3. Embedded agent with registered context usage via SetAgentContextUsage
	agentID := "worker-registered"
	managed.SetAgentContextUsage(agentID, managed.CalculateContextUsage(&tokens, window))
	defer managed.ClearAgentContextUsage(agentID)

	regAgent := &store.ManagedAgentRecord{
		AgentID:       agentID,
		Name:          agentID,
		Runtime:       "embedded",
		Cwd:           "/tmp",
		SelectedModel: store.ManagedModel{Provider: "google", ID: "gemini-3.8-flash"},
	}
	projReg := managed.PublicAgent(regAgent)
	require.NotNil(t, projReg["context_usage"])
	regUsage := projReg["context_usage"].(*managed.ContextUsage)
	require.Equal(t, 50000, *regUsage.Tokens)

	// 4. Embedded agent with no recorded usage: falls back to model context window
	freshAgent := &store.ManagedAgentRecord{
		AgentID:       "worker-fresh",
		Name:          "worker-fresh",
		Runtime:       "embedded",
		Cwd:           "/tmp",
		SelectedModel: store.ManagedModel{Provider: "google", ID: "gemini-3.8-flash"},
	}
	projFresh := managed.PublicAgent(freshAgent)
	require.NotNil(t, projFresh["context_usage"])
	freshUsage := projFresh["context_usage"].(*managed.ContextUsage)
	require.Nil(t, freshUsage.Tokens)
	require.Greater(t, freshUsage.ContextWindow, 0)
	require.Nil(t, freshUsage.Percent)

	// Verify JSON serialization format
	marshaled, err := json.Marshal(proj)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(marshaled, &decoded))
	rawUsage, ok := decoded["context_usage"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, float64(50000), rawUsage["tokens"])
	require.Equal(t, float64(100000), rawUsage["context_window"])
	require.Equal(t, 50.0, rawUsage["percent"])
}

func TestEmbeddedContextUsageTurnAndUILiveDelta(t *testing.T) {
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	reg.SetResponses([]providers.FauxResponseStep{
		func(tc ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			return providers.FauxAssistantMessage(ai.ContentList{
				ai.TextContent{Text: "Task started and completed successfully."},
			}, ai.StopStop)
		},
	})

	harness := newBehavioralHarness(t, func(r store.ManagedAgentRecord) *ai.Model {
		m := reg.GetModel()
		m.ContextWindow = 272000
		return m
	})

	ctx := harness.ctx

	// Subscribe to UI-live websocket
	wsURL := "ws" + strings.TrimPrefix(harness.httpURL, "http") + "/v1/ui-live"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(t, err)
	defer conn.Close(websocket.StatusNormalClosure, "done")

	// Read initial snapshot frame
	_, initialMsg, err := conn.Read(ctx)
	require.NoError(t, err)
	var initialFrame map[string]any
	require.NoError(t, json.Unmarshal(initialMsg, &initialFrame))
	require.Equal(t, "managed_agents_snapshot", initialFrame["type"])

	// Launch embedded worker
	agentID := "ctx-worker-1"
	_, err = harness.manager.Launch(ctx, managed.LaunchRequest{
		Name:               agentID,
		Cwd:                harness.root,
		Runtime:            "embedded",
		InitialInstruction: "Hello world",
		Model: &managed.Model{
			Provider: "openai-codex",
			ID:       "gpt-5.6-terra",
		},
	})
	require.NoError(t, err)

	// Wait for worker to complete its turn and return to idle
	require.Eventually(t, func() bool {
		agent, err := harness.db.ManagedAgent(ctx, agentID)
		return err == nil && agent.LifecycleState == "running" && agent.ActivityState == "idle"
	}, 5*time.Second, 20*time.Millisecond)

	// Look for UI live frame reflecting the agent and context usage
	deadline := time.Now().Add(5 * time.Second)
	var foundContextUsage bool
	for time.Now().Before(deadline) {
		readCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		_, msg, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			break
		}
		var frame map[string]any
		if json.Unmarshal(msg, &frame) != nil {
			continue
		}
		if frame["type"] == "managed_agent_delta" {
			changes, _ := frame["changes"].(map[string]any)
			if changes != nil && changes["agent_id"] == agentID {
				if cu, ok := changes["context_usage"].(map[string]any); ok {
					require.Equal(t, float64(272000), cu["context_window"])
					foundContextUsage = true
					break
				}
			}
		} else if frame["type"] == "managed_agents_snapshot" {
			agents, _ := frame["agents"].([]any)
			for _, a := range agents {
				if am, ok := a.(map[string]any); ok && am["agent_id"] == agentID {
					if cu, ok := am["context_usage"].(map[string]any); ok {
						require.Equal(t, float64(272000), cu["context_window"])
						foundContextUsage = true
						break
					}
				}
			}
			if foundContextUsage {
				break
			}
		}
	}
	require.True(t, foundContextUsage, "UI live must broadcast frame containing context_usage for embedded agent")

	// Verify manager GetContextUsage API
	usage := harness.manager.GetContextUsage(agentID)
	require.NotNil(t, usage, "Manager must return ContextUsage for active embedded agent")
	require.Equal(t, 272000, usage.ContextWindow)
	fmt.Printf("Context usage for %s: tokens=%v, window=%d, percent=%v\n", agentID, usage.Tokens, usage.ContextWindow, usage.Percent)
}
