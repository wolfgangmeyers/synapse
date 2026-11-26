package sdui

import (
	"fmt"
	"strconv"
	"strings"
)

// Block types
const (
	BlockTypeSection     = "section"
	BlockTypeCard        = "card"
	BlockTypeCollapsible = "collapsible"
	BlockTypeActions     = "actions"
	BlockTypeInput       = "input"
	BlockTypeImage       = "image"
	BlockTypeRemoteSlot  = "remote_slot"
	BlockTypeDivider     = "divider"
)

// Element types
const (
	ElementTypePlainTextInput = "plain_text_input"
	ElementTypeToggleSwitch   = "toggle_switch"
	ElementTypeDropdownSelect = "dropdown_select"
	ElementTypeButton         = "button"
)

// ResponseAction types
const (
	ResponseActionUpdate      = "update"
	ResponseActionUpdateSlot  = "update_slot"
	ResponseActionPush        = "push"
	ResponseActionClearInputs = "clear_inputs"
	ResponseActionErrors      = "errors"
)

// Surface represents a top-level or modal UI screen composed of blocks.
type Surface struct {
	SurfaceID string  `json:"surface_id"`
	Title     string  `json:"title,omitempty"`
	Blocks    []Block `json:"blocks"`
}

// Block represents a generic Block Kit layout primitive.
type Block struct {
	Type         string    `json:"type"`
	BlockID      string    `json:"block_id,omitempty"`
	Text         string    `json:"text,omitempty"`
	Fields       []Field   `json:"fields,omitempty"`
	Accessory    *Element  `json:"accessory,omitempty"`
	Title        string    `json:"title,omitempty"`
	Subtitle     string    `json:"subtitle,omitempty"`
	Badge        string    `json:"badge,omitempty"`
	Expanded     bool      `json:"expanded,omitempty"`
	Style        string    `json:"style,omitempty"`
	Blocks       []Block   `json:"blocks,omitempty"`
	Elements     []Element `json:"elements,omitempty"`
	Label        string    `json:"label,omitempty"`
	Element      *Element  `json:"element,omitempty"`
	Hint         string    `json:"hint,omitempty"`
	Optional     bool      `json:"optional,omitempty"`
	ImageURL     string    `json:"image_url,omitempty"`
	AltText      string    `json:"alt_text,omitempty"`
	AspectRatio  string    `json:"aspect_ratio,omitempty"`
	ThumbnailURL string    `json:"thumbnail_url,omitempty"`
	SlotID       string    `json:"slot_id,omitempty"`
	ViewURL      string    `json:"view_url,omitempty"`
}

// Field represents a key-value text pair inside a section.
type Field struct {
	Title string `json:"title"`
	Value string `json:"value"`
}

// Element represents an interactive widget inside blocks or accessories.
type Element struct {
	Type          string         `json:"type"`
	ActionID      string         `json:"action_id"`
	Placeholder   string         `json:"placeholder,omitempty"`
	InitialValue  any            `json:"initial_value,omitempty"`
	Multiline     bool           `json:"multiline,omitempty"`
	Text          string         `json:"text,omitempty"`
	Style         string         `json:"style,omitempty"`
	Value         string         `json:"value,omitempty"`
	Options       []SelectOption `json:"options,omitempty"`
	InitialOption string         `json:"initial_option,omitempty"`
}

// SelectOption represents an entry in a dropdown select menu.
type SelectOption struct {
	Text  string `json:"text"`
	Value string `json:"value"`
}

// ActionEnvelope encapsulates user interaction dispatches.
type ActionEnvelope struct {
	ActionID  string      `json:"action_id"`
	BlockID   string      `json:"block_id,omitempty"`
	AgentID   string      `json:"agent_id,omitempty"`
	SurfaceID string      `json:"surface_id,omitempty"`
	Value     string      `json:"value,omitempty"`
	SlotID    string      `json:"slot_id,omitempty"`
	State     ActionState `json:"state,omitempty"`
}

// ActionPayload is an alias for ActionEnvelope.
type ActionPayload = ActionEnvelope

// ActionState holds ephemeral form and input values indexed by block_id and action_id.
type ActionState struct {
	Values map[string]map[string]any `json:"values,omitempty"`
}

// ActionResponse communicates UI mutations back to the client.
type ActionResponse struct {
	ResponseAction string            `json:"response_action"`
	Surface        *Surface          `json:"surface,omitempty"`
	SlotID         string            `json:"slot_id,omitempty"`
	ViewURL        string            `json:"view_url,omitempty"`
	ClearInputs    []string          `json:"clear_inputs,omitempty"`
	Errors         map[string]string `json:"errors,omitempty"`
}

// PeerInfo represents an agent peer displayed in sidebar cards.
type PeerInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	Active      bool   `json:"active"`
	SubagentCnt int    `json:"subagent_count,omitempty"`
}

// MessageInfo represents a timeline or conversation message item.
type MessageInfo struct {
	ID         string `json:"id"`
	Sender     string `json:"sender"`
	Role       string `json:"role"`
	Content    string `json:"content"`
	Timestamp  string `json:"timestamp,omitempty"`
	IsTool     bool   `json:"is_tool,omitempty"`
	ToolName   string `json:"tool_name,omitempty"`
	ToolArgs   string `json:"tool_args,omitempty"`
	ToolOutput string `json:"tool_output,omitempty"`
}

// AgentConfig holds agent settings for configuration views.
type AgentConfig struct {
	Name             string  `json:"name"`
	ModelProvider    string  `json:"model_provider"`
	ModelID          string  `json:"model_id"`
	Temperature      float64 `json:"temperature"`
	MaxTokens        int     `json:"max_tokens"`
	AllowInlineTools bool    `json:"allow_inline_tools"`
	AutoRestart      bool    `json:"auto_restart"`
}

// GenerateBootstrapView returns the root workspace surface with independent remote slots.
func GenerateBootstrapView(baseURL string) Surface {
	prefix := strings.TrimRight(baseURL, "/")

	return Surface{
		SurfaceID: "root",
		Title:     "Mecha Workspace",
		Blocks: []Block{
			{
				Type:    BlockTypeSection,
				BlockID: "root_header",
				Text:    "# MECHA WORKSPACE\n*Cyberpunk Server-Driven UI*",
			},
			{
				Type:    BlockTypeRemoteSlot,
				BlockID: "sidebar_slot_block",
				SlotID:  "sidebar_slot",
				ViewURL: prefix + "/v1/ui/views/sidebar",
			},
			{
				Type:  BlockTypeDivider,
				Style: "glow",
			},
			{
				Type:    BlockTypeRemoteSlot,
				BlockID: "main_slot_block",
				SlotID:  "main_slot",
				ViewURL: prefix + "/v1/ui/views/agents/default/conversation",
			},
		},
	}
}

// GenerateSidebarView generates a surface displaying active peer agents.
func GenerateSidebarView(peers []PeerInfo) Surface {
	blocks := []Block{
		{
			Type:    BlockTypeSection,
			BlockID: "sidebar_header",
			Text:    "## Active Agents",
		},
	}

	if len(peers) == 0 {
		blocks = append(blocks, Block{
			Type:    BlockTypeSection,
			BlockID: "empty_peers",
			Text:    "*No active peers found.*",
		})
	} else {
		for _, peer := range peers {
			peerName := peer.Name
			if peerName == "" {
				peerName = peer.ID
			}

			statusText := peer.Status
			if statusText == "" {
				statusText = "idle"
			}

			selectStyle := "default"
			if peer.Active {
				selectStyle = "primary"
			}

			childBlocks := []Block{
				{
					Type: BlockTypeSection,
					Text: fmt.Sprintf("**%s**\nStatus: `%s`", peerName, statusText),
				},
			}

			if peer.SubagentCnt > 0 {
				childBlocks = append(childBlocks, Block{
					Type:     BlockTypeCollapsible,
					BlockID:  fmt.Sprintf("subagents_%s", peer.ID),
					Title:    fmt.Sprintf("%d Subagents", peer.SubagentCnt),
					Badge:    "active",
					Expanded: false,
					Blocks: []Block{
						{
							Type: BlockTypeSection,
							Text: fmt.Sprintf("%d subagents actively executing under %s.", peer.SubagentCnt, peerName),
						},
					},
				})
			}

			childBlocks = append(childBlocks, Block{
				Type:    BlockTypeActions,
				BlockID: fmt.Sprintf("peer_actions_%s", peer.ID),
				Elements: []Element{
					{
						Type:     ElementTypeButton,
						ActionID: "select_peer",
						Text:     "Connect",
						Style:    selectStyle,
						Value:    peer.ID,
					},
				},
			})

			blocks = append(blocks, Block{
				Type:     BlockTypeCard,
				BlockID:  fmt.Sprintf("peer_card_%s", peer.ID),
				Title:    peerName,
				Subtitle: "Status: " + statusText,
				Badge:    statusText,
				Blocks:   childBlocks,
			})
		}
	}

	blocks = append(blocks,
		Block{
			Type:  BlockTypeDivider,
			Style: "hairline",
		},
		Block{
			Type:    BlockTypeActions,
			BlockID: "sidebar_footer_actions",
			Elements: []Element{
				{
					Type:     ElementTypeButton,
					ActionID: "refresh_sidebar",
					Text:     "Refresh",
					Style:    "default",
				},
			},
		},
	)

	return Surface{
		SurfaceID: "sidebar",
		Title:     "Active Agents",
		Blocks:    blocks,
	}
}

// GenerateConversationView renders the conversation feed, message cards, and composer.
func GenerateConversationView(agentID string, messages []MessageInfo) Surface {
	blocks := []Block{
		{
			Type:     BlockTypeCard,
			BlockID:  "conversation_header",
			Title:    "Agent: " + agentID,
			Subtitle: "Session timeline",
			Badge:    "online",
			Blocks: []Block{
				{
					Type:    BlockTypeActions,
					BlockID: "header_actions",
					Elements: []Element{
						{
							Type:     ElementTypeButton,
							ActionID: "open_config",
							Text:     "Configure Agent",
							Style:    "default",
							Value:    agentID,
						},
					},
				},
			},
		},
		{
			Type:  BlockTypeDivider,
			Style: "glow",
		},
	}

	if len(messages) == 0 {
		blocks = append(blocks, Block{
			Type:    BlockTypeSection,
			BlockID: "empty_conversation",
			Text:    "*No messages yet. Send a prompt below to start the conversation.*",
		})
	} else {
		for _, msg := range messages {
			if msg.IsTool {
				toolBlocks := []Block{
					{
						Type: BlockTypeSection,
						Text: fmt.Sprintf("**Input:** `%s`\n\n**Output:**\n```\n%s\n```", msg.ToolArgs, msg.ToolOutput),
					},
				}
				blocks = append(blocks, Block{
					Type:     BlockTypeCollapsible,
					BlockID:  "tool_" + msg.ID,
					Title:    "Tool: " + msg.ToolName,
					Badge:    "executed",
					Expanded: false,
					Blocks:   toolBlocks,
				})
			} else {
				sender := msg.Sender
				if sender == "" {
					sender = msg.Role
				}
				blocks = append(blocks, Block{
					Type:     BlockTypeCard,
					BlockID:  "msg_" + msg.ID,
					Title:    sender,
					Badge:    msg.Role,
					Blocks: []Block{
						{
							Type: BlockTypeSection,
							Text: msg.Content,
						},
					},
				})
			}
		}
	}

	blocks = append(blocks,
		Block{
			Type:  BlockTypeDivider,
			Style: "hairline",
		},
		Block{
			Type:    BlockTypeInput,
			BlockID: "composer_input",
			Label:   "Send Message",
			Element: &Element{
				Type:        ElementTypePlainTextInput,
				ActionID:    "message_text",
				Placeholder: "Type a prompt or instruction...",
				Multiline:   true,
			},
		},
		Block{
			Type:    BlockTypeActions,
			BlockID: "composer_actions",
			Elements: []Element{
				{
					Type:     ElementTypeButton,
					ActionID: "send_message",
					Text:     "Send",
					Style:    "primary",
					Value:    agentID,
				},
				{
					Type:     ElementTypeButton,
					ActionID: "clear_draft",
					Text:     "Clear",
					Style:    "default",
					Value:    agentID,
				},
			},
		},
	)

	return Surface{
		SurfaceID: "conversation_" + agentID,
		Title:     "Conversation: " + agentID,
		Blocks:    blocks,
	}
}

// GenerateConfigView renders an agent's configuration panel with form inputs.
func GenerateConfigView(agentID string, config AgentConfig) Surface {
	blocks := []Block{
		{
			Type:    BlockTypeSection,
			BlockID: "cfg_title",
			Text:    fmt.Sprintf("### Configuration for **%s**", agentID),
		},
		{
			Type:    BlockTypeInput,
			BlockID: "cfg_name",
			Label:   "Agent Name",
			Element: &Element{
				Type:         ElementTypePlainTextInput,
				ActionID:     "name_input",
				InitialValue: config.Name,
				Placeholder:  "Enter agent name",
			},
		},
		{
			Type:    BlockTypeInput,
			BlockID: "cfg_provider",
			Label:   "Model Provider",
			Element: &Element{
				Type:          ElementTypeDropdownSelect,
				ActionID:      "provider_select",
				InitialOption: config.ModelProvider,
				Options: []SelectOption{
					{Text: "Google Vertex", Value: "google-vertex"},
					{Text: "Google Gemini", Value: "google"},
					{Text: "OpenAI Codex", Value: "openai-codex"},
					{Text: "Claude Code", Value: "claude-code"},
				},
			},
		},
		{
			Type:    BlockTypeInput,
			BlockID: "cfg_model_id",
			Label:   "Model ID",
			Element: &Element{
				Type:         ElementTypePlainTextInput,
				ActionID:     "model_id_input",
				InitialValue: config.ModelID,
				Placeholder:  "e.g. gemini-3.8-flash",
			},
		},
		{
			Type:    BlockTypeInput,
			BlockID: "cfg_temperature",
			Label:   "Temperature (0.0 to 1.0)",
			Element: &Element{
				Type:         ElementTypePlainTextInput,
				ActionID:     "temperature_input",
				InitialValue: fmt.Sprintf("%.2f", config.Temperature),
				Placeholder:  "0.7",
			},
		},
		{
			Type:    BlockTypeInput,
			BlockID: "cfg_allow_inline_tools",
			Label:   "Inline Tools",
			Element: &Element{
				Type:         ElementTypeToggleSwitch,
				ActionID:     "inline_tools_toggle",
				InitialValue: config.AllowInlineTools,
				Text:         "Allow execution of inline tools",
			},
		},
		{
			Type:    BlockTypeInput,
			BlockID: "cfg_auto_restart",
			Label:   "Auto Restart",
			Element: &Element{
				Type:         ElementTypeToggleSwitch,
				ActionID:     "auto_restart_toggle",
				InitialValue: config.AutoRestart,
				Text:         "Automatically restart on crash or exit",
			},
		},
		{
			Type:  BlockTypeDivider,
			Style: "glow",
		},
		{
			Type:    BlockTypeActions,
			BlockID: "cfg_actions",
			Elements: []Element{
				{
					Type:     ElementTypeButton,
					ActionID: "save_config",
					Text:     "Save Changes",
					Style:    "primary",
					Value:    agentID,
				},
				{
					Type:     ElementTypeButton,
					ActionID: "cancel_config",
					Text:     "Cancel",
					Style:    "default",
					Value:    agentID,
				},
			},
		},
	}

	return Surface{
		SurfaceID: "config_" + agentID,
		Title:     "Agent Configuration",
		Blocks:    blocks,
	}
}

// HandleAction processes an action payload and returns the appropriate mutation response.
func HandleAction(payload ActionPayload) ActionResponse {
	switch payload.ActionID {
	case "select_peer":
		targetID := payload.Value
		if targetID == "" {
			targetID = payload.AgentID
		}
		if targetID == "" {
			targetID = "default"
		}
		slotID := payload.SlotID
		if slotID == "" {
			slotID = "main_slot"
		}
		return ActionResponse{
			ResponseAction: ResponseActionUpdateSlot,
			SlotID:         slotID,
			ViewURL:        fmt.Sprintf("/v1/ui/views/agents/%s/conversation", targetID),
		}

	case "send_message", "clear_draft":
		return ActionResponse{
			ResponseAction: ResponseActionClearInputs,
			ClearInputs:    []string{"composer_input"},
		}

	case "open_config":
		targetID := payload.Value
		if targetID == "" {
			targetID = payload.AgentID
		}
		if targetID == "" {
			targetID = "default"
		}
		cfgView := GenerateConfigView(targetID, AgentConfig{
			Name:             targetID,
			ModelProvider:    "google-vertex",
			ModelID:          "gemini-3.8-flash",
			Temperature:      0.7,
			MaxTokens:        4096,
			AllowInlineTools: true,
			AutoRestart:      true,
		})
		return ActionResponse{
			ResponseAction: ResponseActionPush,
			Surface:        &cfgView,
		}

	case "save_config":
		errors := make(map[string]string)
		if payload.State.Values != nil {
			if nameVals, ok := payload.State.Values["cfg_name"]; ok {
				if val, exists := nameVals["name_input"]; exists {
					if strVal, ok := val.(string); ok && strings.TrimSpace(strVal) == "" {
						errors["cfg_name"] = "Agent name cannot be empty"
					}
				}
			}
			if tempVals, ok := payload.State.Values["cfg_temperature"]; ok {
				if val, exists := tempVals["temperature_input"]; exists {
					var tempFloat float64
					var err error
					switch v := val.(type) {
					case float64:
						tempFloat = v
					case string:
						tempFloat, err = strconv.ParseFloat(v, 64)
					default:
						err = fmt.Errorf("invalid type")
					}
					if err != nil || tempFloat < 0.0 || tempFloat > 1.0 {
						errors["cfg_temperature"] = "Temperature must be between 0.0 and 1.0"
					}
				}
			}
		}

		if len(errors) > 0 {
			return ActionResponse{
				ResponseAction: ResponseActionErrors,
				Errors:         errors,
			}
		}

		updated := GenerateConversationView(payload.AgentID, nil)
		return ActionResponse{
			ResponseAction: ResponseActionUpdate,
			Surface:        &updated,
		}

	case "refresh_sidebar":
		sidebar := GenerateSidebarView(nil)
		return ActionResponse{
			ResponseAction: ResponseActionUpdate,
			Surface:        &sidebar,
		}

	case "trigger_update":
		view := GenerateBootstrapView("")
		return ActionResponse{
			ResponseAction: ResponseActionUpdate,
			Surface:        &view,
		}

	case "trigger_update_slot":
		return ActionResponse{
			ResponseAction: ResponseActionUpdateSlot,
			SlotID:         "main_slot",
			ViewURL:        "/v1/ui/views/agents/alpha/conversation",
		}

	case "trigger_push":
		view := GenerateSidebarView(nil)
		return ActionResponse{
			ResponseAction: ResponseActionPush,
			Surface:        &view,
		}

	case "trigger_clear_inputs":
		return ActionResponse{
			ResponseAction: ResponseActionClearInputs,
			ClearInputs:    []string{"input_a", "input_b"},
		}

	case "trigger_errors":
		return ActionResponse{
			ResponseAction: ResponseActionErrors,
			Errors: map[string]string{
				"block_field_1": "Field is required",
			},
		}

	default:
		return ActionResponse{
			ResponseAction: ResponseActionUpdate,
			Surface: &Surface{
				SurfaceID: "action_acknowledged",
				Title:     "Action Acknowledged",
				Blocks: []Block{
					{
						Type: BlockTypeSection,
						Text: fmt.Sprintf("Action `%s` processed successfully.", payload.ActionID),
					},
				},
			},
		}
	}
}
