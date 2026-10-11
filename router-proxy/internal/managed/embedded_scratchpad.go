package managed

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
)

func embeddedScratchpadSchema() *ai.Schema {
	closed := false
	schema := ai.Object(
		ai.Prop("action", ai.EnumOf("append", "fetch", "paginate", "search", "pin", "unpin", "delete")),
		ai.Opt("content", ai.String("Content of the scratchpad entry (for append, maximum 4000 characters).")),
		ai.Opt("pinned", ai.Boolean("Whether the entry is pinned (for append / pin).")),
		ai.Opt("id", ai.String("Entry ID (for pin / unpin / delete).")),
		ai.Opt("query", ai.String("Search query string (for search).")),
		ai.Opt("limit", ai.Integer("Maximum entries to return (optional, default 5, max 20).")),
		ai.Opt("offset", ai.Integer("Pagination offset (optional, default 0).")),
	)
	schema.AdditionalAllowed = &closed
	return schema
}

func (m *Manager) embeddedScratchpadTool(owner embeddedOwner) agent.AgentTool {
	return agent.AgentTool{
		Name:        "scratchpad",
		Description: "Persistent scratchpad memory for notes, milestones, and context across session resets. Supports append, fetch (recent + pinned), paginate, search, pin, unpin, and delete.",
		Parameters:  embeddedScratchpadSchema(),
		Execute: func(ctx context.Context, id string, args map[string]any, _ agent.ToolUpdateFunc) (agent.AgentToolResult, error) {
			if m == nil || m.store == nil {
				return agent.AgentToolResult{}, errors.New("store unavailable for scratchpad")
			}
			if owner.binding.AgentID != "" && owner.identity != "" {
				_ = m.store.RenewEmbeddedOwner(ctx, owner.binding, owner.identity, embeddedOwnerTTL)
			}

			action, ok := args["action"].(string)
			if !ok || action == "" {
				return agent.AgentToolResult{}, errors.New("action is required for scratchpad")
			}

			limit := 5
			if val, ok := args["limit"]; ok && val != nil {
				switch v := val.(type) {
				case int:
					limit = v
				case float64:
					limit = int(v)
				case int64:
					limit = int(v)
				}
			}
			if limit <= 0 {
				limit = 5
			}
			if limit > 20 {
				limit = 20
			}

			offset := 0
			if val, ok := args["offset"]; ok && val != nil {
				switch v := val.(type) {
				case int:
					offset = v
				case float64:
					offset = int(v)
				case int64:
					offset = int(v)
				}
			}
			if offset < 0 {
				offset = 0
			}

			agentID := owner.binding.AgentID

			switch action {
			case "append":
				content, ok := args["content"].(string)
				if !ok || strings.TrimSpace(content) == "" {
					return agent.AgentToolResult{}, errors.New("content is required for append")
				}
				if runeLen := len([]rune(content)); runeLen > 4000 {
					return agent.AgentToolResult{}, fmt.Errorf("scratchpad entry exceeds maximum allowed length of 4000 characters (got %d); save large content to a file and record the path", runeLen)
				}
				pinned := false
				if p, ok := args["pinned"].(bool); ok {
					pinned = p
				}
				entry, err := m.store.AppendScratchpadEntry(ctx, agentID, content, pinned)
				if err != nil {
					if errors.Is(err, store.ErrPinnedLimitReached) {
						return agent.AgentToolResult{}, err
					}
					return agent.AgentToolResult{}, fmt.Errorf("append scratchpad entry: %w", err)
				}
				output := fmt.Sprintf("Appended scratchpad entry %s (pinned: %t)", entry.ID, entry.Pinned)
				return agent.AgentToolResult{
					Content: ai.ContentList{
						ai.TextContent{Text: output},
					},
				}, nil

			case "fetch":
				entries, totalCount, err := m.store.FetchScratchpadRecent(ctx, agentID, limit)
				if err != nil {
					return agent.AgentToolResult{}, fmt.Errorf("fetch scratchpad entries: %w", err)
				}
				if len(entries) == 0 {
					return agent.AgentToolResult{
						Content: ai.ContentList{
							ai.TextContent{Text: "No scratchpad entries found."},
						},
					}, nil
				}
				var sb strings.Builder
				sb.WriteString(fmt.Sprintf("Fetched %d scratchpad entries:\n", len(entries)))
				for _, e := range entries {
					status := "Recent"
					if e.Pinned {
						status = "Pinned"
					}
					sb.WriteString(fmt.Sprintf("- [%s] (%s) %s\n", status, e.ID, e.Content))
				}
				unshown := totalCount - len(entries)
				if unshown > 0 {
					sb.WriteString(fmt.Sprintf("\nShowing %d of %d total entries (%d older entries not shown. Use 'paginate' or 'search' to view them).\n", len(entries), totalCount, unshown))
				} else {
					sb.WriteString(fmt.Sprintf("\nShowing %d of %d total entries (all entries shown).\n", len(entries), totalCount))
				}
				return agent.AgentToolResult{
					Content: ai.ContentList{
						ai.TextContent{Text: strings.TrimRight(sb.String(), "\n")},
					},
				}, nil

			case "paginate":
				entries, total, err := m.store.PaginateScratchpad(ctx, agentID, limit, offset)
				if err != nil {
					return agent.AgentToolResult{}, fmt.Errorf("paginate scratchpad entries: %w", err)
				}
				if total == 0 || len(entries) == 0 {
					return agent.AgentToolResult{
						Content: ai.ContentList{
							ai.TextContent{Text: fmt.Sprintf("No scratchpad entries found (total: %d, offset: %d).", total, offset)},
						},
					}, nil
				}
				var sb strings.Builder
				sb.WriteString(fmt.Sprintf("Scratchpad entries (showing %d of %d, offset %d):\n", len(entries), total, offset))
				for _, e := range entries {
					status := "Recent"
					if e.Pinned {
						status = "Pinned"
					}
					sb.WriteString(fmt.Sprintf("- [%s] (%s) %s\n", status, e.ID, e.Content))
				}
				return agent.AgentToolResult{
					Content: ai.ContentList{
						ai.TextContent{Text: strings.TrimRight(sb.String(), "\n")},
					},
				}, nil

			case "search":
				query, _ := args["query"].(string)
				entries, err := m.store.SearchScratchpad(ctx, agentID, query, limit)
				if err != nil {
					return agent.AgentToolResult{}, fmt.Errorf("search scratchpad entries: %w", err)
				}
				if len(entries) == 0 {
					return agent.AgentToolResult{
						Content: ai.ContentList{
							ai.TextContent{Text: fmt.Sprintf("No scratchpad entries matching %q found.", query)},
						},
					}, nil
				}
				var sb strings.Builder
				sb.WriteString(fmt.Sprintf("Found %d scratchpad entries matching %q:\n", len(entries), query))
				for _, e := range entries {
					status := "Recent"
					if e.Pinned {
						status = "Pinned"
					}
					sb.WriteString(fmt.Sprintf("- [%s] (%s) %s\n", status, e.ID, e.Content))
				}
				return agent.AgentToolResult{
					Content: ai.ContentList{
						ai.TextContent{Text: strings.TrimRight(sb.String(), "\n")},
					},
				}, nil

			case "pin":
				entryID, ok := args["id"].(string)
				if !ok || strings.TrimSpace(entryID) == "" {
					return agent.AgentToolResult{}, errors.New("id is required for pin")
				}
				pinnedVal := true
				if p, ok := args["pinned"].(bool); ok {
					pinnedVal = p
				}
				if err := m.store.SetScratchpadPinned(ctx, agentID, entryID, pinnedVal); err != nil {
					if errors.Is(err, store.ErrPinnedLimitReached) {
						return agent.AgentToolResult{}, err
					}
					return agent.AgentToolResult{}, fmt.Errorf("pin scratchpad entry: %w", err)
				}
				actionLabel := "pinned"
				if !pinnedVal {
					actionLabel = "unpinned"
				}
				return agent.AgentToolResult{
					Content: ai.ContentList{
						ai.TextContent{Text: fmt.Sprintf("Scratchpad entry %s %s.", entryID, actionLabel)},
					},
				}, nil

			case "unpin":
				entryID, ok := args["id"].(string)
				if !ok || strings.TrimSpace(entryID) == "" {
					return agent.AgentToolResult{}, errors.New("id is required for unpin")
				}
				if err := m.store.SetScratchpadPinned(ctx, agentID, entryID, false); err != nil {
					return agent.AgentToolResult{}, fmt.Errorf("unpin scratchpad entry: %w", err)
				}
				return agent.AgentToolResult{
					Content: ai.ContentList{
						ai.TextContent{Text: fmt.Sprintf("Scratchpad entry %s unpinned.", entryID)},
					},
				}, nil

			case "delete":
				entryID, ok := args["id"].(string)
				if !ok || strings.TrimSpace(entryID) == "" {
					return agent.AgentToolResult{}, errors.New("id is required for delete")
				}
				if err := m.store.DeleteScratchpadEntry(ctx, agentID, entryID); err != nil {
					return agent.AgentToolResult{}, fmt.Errorf("delete scratchpad entry: %w", err)
				}
				return agent.AgentToolResult{
					Content: ai.ContentList{
						ai.TextContent{Text: fmt.Sprintf("Deleted scratchpad entry %s", entryID)},
					},
				}, nil

			default:
				return agent.AgentToolResult{}, fmt.Errorf("unsupported scratchpad action %q", action)
			}
		},
	}
}

// registerEmbeddedTools registers embedded tools including scratchpad for an embedded session.
func (m *Manager) registerEmbeddedTools(owner embeddedOwner) []agent.AgentTool {
	return []agent.AgentTool{
		m.embeddedScratchpadTool(owner),
		m.embeddedKBSearchTool(owner),
		m.embeddedKBReadTool(owner),
		m.embeddedKBWriteTool(owner),
		m.embeddedKBListTool(owner),
	}
}

// RegisterEmbeddedTools is the exported version of registerEmbeddedTools.
func RegisterEmbeddedTools(m *Manager, owner embeddedOwner) []agent.AgentTool {
	return m.registerEmbeddedTools(owner)
}

// embeddedToolsForManifest lists embedded tool names recognized for agent manifests.
func embeddedToolsForManifest() []string {
	return []string{"scratchpad", "context_reset", "reset"}
}

// EmbeddedToolsForManifest is the exported version of embeddedToolsForManifest.
func EmbeddedToolsForManifest() []string {
	return embeddedToolsForManifest()
}
