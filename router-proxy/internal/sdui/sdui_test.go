package sdui_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/sdui"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBlockKitJSONSerialization(t *testing.T) {
	t.Run("serialize all block primitives and elements", func(t *testing.T) {
		surface := sdui.Surface{
			SurfaceID: "test_surface",
			Title:     "Test Cyberpunk Surface",
			Blocks: []sdui.Block{
				{
					Type:    sdui.BlockTypeSection,
					BlockID: "sec_1",
					Text:    "Markdown *text* with `code`",
					Fields: []sdui.Field{
						{Title: "Status", Value: "Operational"},
						{Title: "Latency", Value: "12ms"},
					},
					Accessory: &sdui.Element{
						Type:     sdui.ElementTypeButton,
						ActionID: "btn_action",
						Text:     "Click Me",
						Style:    "primary",
						Value:    "test_val",
					},
				},
				{
					Type:     sdui.BlockTypeCard,
					BlockID:  "card_1",
					Title:    "Cyberpunk Card",
					Subtitle: "Glassmorphic Border",
					Badge:    "v1.0",
					Style:    "glow",
					Blocks: []sdui.Block{
						{
							Type: sdui.BlockTypeSection,
							Text: "Card inner content",
						},
					},
				},
				{
					Type:     sdui.BlockTypeCollapsible,
					BlockID:  "col_1",
					Title:    "Tool Output Details",
					Badge:    "3 results",
					Expanded: true,
					Blocks: []sdui.Block{
						{
							Type: sdui.BlockTypeSection,
							Text: "Collapsible inner section",
						},
					},
				},
				{
					Type:    sdui.BlockTypeActions,
					BlockID: "act_1",
					Elements: []sdui.Element{
						{
							Type:     sdui.ElementTypeButton,
							ActionID: "save",
							Text:     "Save",
							Style:    "primary",
						},
						{
							Type:     sdui.ElementTypeButton,
							ActionID: "cancel",
							Text:     "Cancel",
							Style:    "default",
						},
					},
				},
				{
					Type:    sdui.BlockTypeInput,
					BlockID: "inp_1",
					Label:   "Text Input",
					Element: &sdui.Element{
						Type:         sdui.ElementTypePlainTextInput,
						ActionID:     "txt_val",
						Placeholder:  "Enter something...",
						InitialValue: "Sample",
						Multiline:    true,
					},
				},
				{
					Type:    sdui.BlockTypeInput,
					BlockID: "inp_2",
					Label:   "Toggle Switch",
					Element: &sdui.Element{
						Type:         sdui.ElementTypeToggleSwitch,
						ActionID:     "toggle_val",
						InitialValue: true,
						Text:         "Enable Feature",
					},
				},
				{
					Type:    sdui.BlockTypeInput,
					BlockID: "inp_3",
					Label:   "Dropdown Selection",
					Element: &sdui.Element{
						Type:          sdui.ElementTypeDropdownSelect,
						ActionID:      "drop_val",
						Placeholder:   "Choose...",
						InitialOption: "opt_b",
						Options: []sdui.SelectOption{
							{Text: "Option A", Value: "opt_a"},
							{Text: "Option B", Value: "opt_b"},
						},
					},
				},
				{
					Type:         sdui.BlockTypeImage,
					BlockID:      "img_1",
					ImageURL:     "https://example.com/cyberpunk.png",
					AltText:      "Cyberpunk City",
					AspectRatio:  "16:9",
					ThumbnailURL: "https://example.com/cyberpunk_thumb.png",
				},
				{
					Type:    sdui.BlockTypeRemoteSlot,
					BlockID: "slot_1",
					SlotID:  "main_slot",
					ViewURL: "/v1/ui/views/main",
				},
				{
					Type:  sdui.BlockTypeDivider,
					Style: "glow",
				},
			},
		}

		data, err := json.Marshal(surface)
		require.NoError(t, err)

		var unmarshaled sdui.Surface
		require.NoError(t, json.Unmarshal(data, &unmarshaled))

		assert.Equal(t, surface.SurfaceID, unmarshaled.SurfaceID)
		assert.Equal(t, surface.Title, unmarshaled.Title)
		assert.Len(t, unmarshaled.Blocks, len(surface.Blocks))

		types := make([]string, len(unmarshaled.Blocks))
		for i, b := range unmarshaled.Blocks {
			types[i] = b.Type
		}

		expectedTypes := []string{
			sdui.BlockTypeSection,
			sdui.BlockTypeCard,
			sdui.BlockTypeCollapsible,
			sdui.BlockTypeActions,
			sdui.BlockTypeInput,
			sdui.BlockTypeInput,
			sdui.BlockTypeInput,
			sdui.BlockTypeImage,
			sdui.BlockTypeRemoteSlot,
			sdui.BlockTypeDivider,
		}
		assert.Equal(t, expectedTypes, types)
	})
}

func TestGenerateBootstrapView(t *testing.T) {
	t.Run("bootstrap view has root slots", func(t *testing.T) {
		view := sdui.GenerateBootstrapView("http://127.0.0.1:8765")
		assert.Equal(t, "root", view.SurfaceID)
		assert.NotEmpty(t, view.Blocks)

		var slotIDs []string
		var viewURLs []string
		for _, b := range view.Blocks {
			if b.Type == sdui.BlockTypeRemoteSlot {
				slotIDs = append(slotIDs, b.SlotID)
				viewURLs = append(viewURLs, b.ViewURL)
			}
		}

		assert.Contains(t, slotIDs, "sidebar_slot")
		assert.Contains(t, slotIDs, "main_slot")
		assert.Contains(t, viewURLs, "http://127.0.0.1:8765/v1/ui/views/sidebar")
		assert.Contains(t, viewURLs, "http://127.0.0.1:8765/v1/ui/views/agents/default/conversation")
	})

	t.Run("bootstrap view with empty base url", func(t *testing.T) {
		view := sdui.GenerateBootstrapView("")
		for _, b := range view.Blocks {
			if b.Type == sdui.BlockTypeRemoteSlot {
				assert.True(t, strings.HasPrefix(b.ViewURL, "/v1/ui/views/"))
			}
		}
	})
}

func TestGenerateSidebarView(t *testing.T) {
	t.Run("renders peer list with cards and subagents", func(t *testing.T) {
		peers := []sdui.PeerInfo{
			{ID: "alpha", Name: "Alpha Worker", Status: "running", Active: true, SubagentCnt: 2},
			{ID: "beta", Name: "Beta Worker", Status: "idle", Active: false, SubagentCnt: 0},
		}

		view := sdui.GenerateSidebarView(peers)
		assert.Equal(t, "sidebar", view.SurfaceID)

		cardCount := 0
		collapsibleCount := 0
		for _, b := range view.Blocks {
			if b.Type == sdui.BlockTypeCard {
				cardCount++
				for _, cb := range b.Blocks {
					if cb.Type == sdui.BlockTypeCollapsible {
						collapsibleCount++
					}
				}
			}
		}

		assert.Equal(t, 2, cardCount)
		assert.Equal(t, 1, collapsibleCount)
	})

	t.Run("renders empty peers view", func(t *testing.T) {
		view := sdui.GenerateSidebarView(nil)
		assert.Equal(t, "sidebar", view.SurfaceID)

		foundEmptyNotice := false
		for _, b := range view.Blocks {
			if b.Type == sdui.BlockTypeSection && strings.Contains(b.Text, "No active peers") {
				foundEmptyNotice = true
			}
		}
		assert.True(t, foundEmptyNotice)
	})
}

func TestGenerateConversationView(t *testing.T) {
	t.Run("renders conversation with standard and tool messages", func(t *testing.T) {
		messages := []sdui.MessageInfo{
			{
				ID:      "msg_1",
				Sender:  "user",
				Role:    "user",
				Content: "Run tests",
			},
			{
				ID:         "msg_2",
				Sender:     "assistant",
				Role:       "assistant",
				IsTool:     true,
				ToolName:   "bash",
				ToolArgs:   "go test ./...",
				ToolOutput: "PASS\nok",
			},
			{
				ID:      "msg_3",
				Sender:  "assistant",
				Role:    "assistant",
				Content: "All tests passed cleanly.",
			},
		}

		view := sdui.GenerateConversationView("agent-123", messages)
		assert.Equal(t, "conversation_agent-123", view.SurfaceID)

		hasCollapsible := false
		hasInput := false
		hasActions := false
		cardCount := 0

		for _, b := range view.Blocks {
			switch b.Type {
			case sdui.BlockTypeCollapsible:
				hasCollapsible = true
				assert.Equal(t, "Tool: bash", b.Title)
			case sdui.BlockTypeInput:
				hasInput = true
				assert.Equal(t, "composer_input", b.BlockID)
			case sdui.BlockTypeActions:
				if b.BlockID == "composer_actions" {
					hasActions = true
				}
			case sdui.BlockTypeCard:
				cardCount++
			}
		}

		assert.True(t, hasCollapsible)
		assert.True(t, hasInput)
		assert.True(t, hasActions)
		assert.GreaterOrEqual(t, cardCount, 2) // header card + message cards
	})
}

func TestGenerateConfigView(t *testing.T) {
	t.Run("renders form elements for agent configuration", func(t *testing.T) {
		cfg := sdui.AgentConfig{
			Name:             "CyberAgent",
			ModelProvider:    "google-vertex",
			ModelID:          "gemini-3.8-flash",
			Temperature:      0.85,
			MaxTokens:        8192,
			AllowInlineTools: true,
			AutoRestart:      true,
		}

		view := sdui.GenerateConfigView("cyber-1", cfg)
		assert.Equal(t, "config_cyber-1", view.SurfaceID)

		inputsFound := make(map[string]string)
		for _, b := range view.Blocks {
			if b.Type == sdui.BlockTypeInput && b.Element != nil {
				inputsFound[b.BlockID] = b.Element.Type
			}
		}

		assert.Equal(t, sdui.ElementTypePlainTextInput, inputsFound["cfg_name"])
		assert.Equal(t, sdui.ElementTypeDropdownSelect, inputsFound["cfg_provider"])
		assert.Equal(t, sdui.ElementTypePlainTextInput, inputsFound["cfg_model_id"])
		assert.Equal(t, sdui.ElementTypePlainTextInput, inputsFound["cfg_temperature"])
		assert.Equal(t, sdui.ElementTypeToggleSwitch, inputsFound["cfg_allow_inline_tools"])
		assert.Equal(t, sdui.ElementTypeToggleSwitch, inputsFound["cfg_auto_restart"])
	})
}

func TestHandleAction(t *testing.T) {
	t.Run("handles select_peer with update_slot", func(t *testing.T) {
		resp := sdui.HandleAction(sdui.ActionPayload{
			ActionID: "select_peer",
			Value:    "worker-x",
		})
		assert.Equal(t, sdui.ResponseActionUpdateSlot, resp.ResponseAction)
		assert.Equal(t, "main_slot", resp.SlotID)
		assert.Equal(t, "/v1/ui/views/agents/worker-x/conversation", resp.ViewURL)
	})

	t.Run("handles send_message with clear_inputs", func(t *testing.T) {
		resp := sdui.HandleAction(sdui.ActionPayload{
			ActionID: "send_message",
			AgentID:  "worker-x",
		})
		assert.Equal(t, sdui.ResponseActionClearInputs, resp.ResponseAction)
		assert.Contains(t, resp.ClearInputs, "composer_input")
	})

	t.Run("handles open_config with push", func(t *testing.T) {
		resp := sdui.HandleAction(sdui.ActionPayload{
			ActionID: "open_config",
			AgentID:  "worker-x",
		})
		assert.Equal(t, sdui.ResponseActionPush, resp.ResponseAction)
		require.NotNil(t, resp.Surface)
		assert.Equal(t, "config_worker-x", resp.Surface.SurfaceID)
	})

	t.Run("handles save_config validation failure with errors", func(t *testing.T) {
		resp := sdui.HandleAction(sdui.ActionPayload{
			ActionID: "save_config",
			AgentID:  "worker-x",
			State: sdui.ActionState{
				Values: map[string]map[string]any{
					"cfg_name": {
						"name_input": "",
					},
					"cfg_temperature": {
						"temperature_input": "2.5",
					},
				},
			},
		})
		assert.Equal(t, sdui.ResponseActionErrors, resp.ResponseAction)
		assert.Equal(t, "Agent name cannot be empty", resp.Errors["cfg_name"])
		assert.Equal(t, "Temperature must be between 0.0 and 1.0", resp.Errors["cfg_temperature"])
	})

	t.Run("handles save_config success with update", func(t *testing.T) {
		resp := sdui.HandleAction(sdui.ActionPayload{
			ActionID: "save_config",
			AgentID:  "worker-x",
			State: sdui.ActionState{
				Values: map[string]map[string]any{
					"cfg_name": {
						"name_input": "Valid Name",
					},
					"cfg_temperature": {
						"temperature_input": 0.5,
					},
				},
			},
		})
		assert.Equal(t, sdui.ResponseActionUpdate, resp.ResponseAction)
		require.NotNil(t, resp.Surface)
	})

	t.Run("handles explicit trigger action types", func(t *testing.T) {
		updateResp := sdui.HandleAction(sdui.ActionPayload{ActionID: "trigger_update"})
		assert.Equal(t, sdui.ResponseActionUpdate, updateResp.ResponseAction)

		slotResp := sdui.HandleAction(sdui.ActionPayload{ActionID: "trigger_update_slot"})
		assert.Equal(t, sdui.ResponseActionUpdateSlot, slotResp.ResponseAction)

		pushResp := sdui.HandleAction(sdui.ActionPayload{ActionID: "trigger_push"})
		assert.Equal(t, sdui.ResponseActionPush, pushResp.ResponseAction)

		clearResp := sdui.HandleAction(sdui.ActionPayload{ActionID: "trigger_clear_inputs"})
		assert.Equal(t, sdui.ResponseActionClearInputs, clearResp.ResponseAction)

		errResp := sdui.HandleAction(sdui.ActionPayload{ActionID: "trigger_errors"})
		assert.Equal(t, sdui.ResponseActionErrors, errResp.ResponseAction)
	})
}
