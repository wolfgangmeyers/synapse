package managed

import (
	"context"
	"database/sql"
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
		ai.Prop("action", ai.EnumOf("append", "fetch", "paginate", "search", "pin", "unpin", "delete", "truncate")),
		ai.Opt("content", ai.String("Content of the scratchpad entry (for append, maximum 4000 characters).")),
		ai.Opt("pinned", ai.Boolean("Whether the entry is pinned (for append / pin).")),
		ai.Opt("id", ai.String("Single entry ID (for pin / unpin / delete).")),
		ai.Opt("ids", ai.ArrayOf(ai.String("Entry ID"), "Batch entry IDs (for pin / unpin / delete).")),
		ai.Opt("query", ai.String("Search query string (for search).")),
		ai.Opt("limit", ai.Integer("Maximum entries to return (optional, default 5, max 20).")),
		ai.Opt("offset", ai.Integer("Pagination offset (optional, default 0).")),
		ai.Opt("before_id", ai.String("Truncate unpinned entries created strictly before this entry's timestamp (for truncate). Mutually exclusive with keep_last.")),
		ai.Opt("keep_last", ai.Integer("Number of most recent unpinned entries to keep (for truncate, must be > 0). Mutually exclusive with before_id.")),
	)
	schema.AdditionalAllowed = &closed
	return schema
}

func extractScratchpadEntryIDs(args map[string]any, action string) ([]string, error) {
	var rawIDs []string
	if idVal, ok := args["id"].(string); ok && strings.TrimSpace(idVal) != "" {
		rawIDs = append(rawIDs, idVal)
	}
	if idsVal, ok := args["ids"]; ok && idsVal != nil {
		switch v := idsVal.(type) {
		case []string:
			rawIDs = append(rawIDs, v...)
		case []any:
			for _, item := range v {
				if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
					rawIDs = append(rawIDs, s)
				}
			}
		case string:
			for _, part := range strings.Split(v, ",") {
				if trimmed := strings.TrimSpace(part); trimmed != "" {
					rawIDs = append(rawIDs, trimmed)
				}
			}
		}
	}

	seen := make(map[string]struct{}, len(rawIDs))
	var ids []string
	for _, id := range rawIDs {
		trimmed := strings.TrimSpace(id)
		if trimmed == "" {
			continue
		}
		if _, exists := seen[trimmed]; !exists {
			seen[trimmed] = struct{}{}
			ids = append(ids, trimmed)
		}
	}

	if len(ids) == 0 {
		return nil, fmt.Errorf("id or ids is required for %s", action)
	}
	return ids, nil
}

func (m *Manager) embeddedScratchpadTool(owner embeddedOwner) agent.AgentTool {
	return agent.AgentTool{
		Name:        "scratchpad",
		Description: "Persistent scratchpad memory for notes, milestones, and context across session resets. Supports append, fetch (recent + pinned), paginate, search, pin, unpin, delete, and truncate.",
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
				ids, err := extractScratchpadEntryIDs(args, "pin")
				if err != nil {
					return agent.AgentToolResult{}, err
				}
				pinnedVal := true
				if p, ok := args["pinned"].(bool); ok {
					pinnedVal = p
				}
				actionLabel := "pinned"
				if !pinnedVal {
					actionLabel = "unpinned"
				}

				if len(ids) == 1 {
					if err := m.store.SetScratchpadPinned(ctx, agentID, ids[0], pinnedVal); err != nil {
						if errors.Is(err, store.ErrPinnedLimitReached) {
							return agent.AgentToolResult{}, err
						}
						return agent.AgentToolResult{}, fmt.Errorf("pin scratchpad entry: %w", err)
					}
					return agent.AgentToolResult{
						Content: ai.ContentList{
							ai.TextContent{Text: fmt.Sprintf("Scratchpad entry %s %s.", ids[0], actionLabel)},
						},
					}, nil
				}

				updatedIDs, err := m.store.SetScratchpadEntriesPinned(ctx, agentID, ids, pinnedVal)
				if err != nil {
					if errors.Is(err, store.ErrPinnedLimitReached) {
						return agent.AgentToolResult{}, err
					}
					return agent.AgentToolResult{}, fmt.Errorf("pin scratchpad entries: %w", err)
				}
				return agent.AgentToolResult{
					Content: ai.ContentList{
						ai.TextContent{Text: fmt.Sprintf("Scratchpad entries %s (%d): %s", actionLabel, len(updatedIDs), strings.Join(updatedIDs, ", "))},
					},
				}, nil

			case "unpin":
				ids, err := extractScratchpadEntryIDs(args, "unpin")
				if err != nil {
					return agent.AgentToolResult{}, err
				}

				if len(ids) == 1 {
					if err := m.store.SetScratchpadPinned(ctx, agentID, ids[0], false); err != nil {
						return agent.AgentToolResult{}, fmt.Errorf("unpin scratchpad entry: %w", err)
					}
					return agent.AgentToolResult{
						Content: ai.ContentList{
							ai.TextContent{Text: fmt.Sprintf("Scratchpad entry %s unpinned.", ids[0])},
						},
					}, nil
				}

				updatedIDs, err := m.store.SetScratchpadEntriesPinned(ctx, agentID, ids, false)
				if err != nil {
					return agent.AgentToolResult{}, fmt.Errorf("unpin scratchpad entries: %w", err)
				}
				return agent.AgentToolResult{
					Content: ai.ContentList{
						ai.TextContent{Text: fmt.Sprintf("Scratchpad entries unpinned (%d): %s", len(updatedIDs), strings.Join(updatedIDs, ", "))},
					},
				}, nil

			case "delete":
				ids, err := extractScratchpadEntryIDs(args, "delete")
				if err != nil {
					return agent.AgentToolResult{}, err
				}

				if len(ids) == 1 {
					if err := m.store.DeleteScratchpadEntry(ctx, agentID, ids[0]); err != nil {
						return agent.AgentToolResult{}, fmt.Errorf("delete scratchpad entry: %w", err)
					}
					return agent.AgentToolResult{
						Content: ai.ContentList{
							ai.TextContent{Text: fmt.Sprintf("Deleted scratchpad entry %s", ids[0])},
						},
					}, nil
				}

				deletedIDs, err := m.store.DeleteScratchpadEntries(ctx, agentID, ids)
				if err != nil {
					return agent.AgentToolResult{}, fmt.Errorf("delete scratchpad entries: %w", err)
				}
				return agent.AgentToolResult{
					Content: ai.ContentList{
						ai.TextContent{Text: fmt.Sprintf("Deleted %d scratchpad entries: %s", len(deletedIDs), strings.Join(deletedIDs, ", "))},
					},
				}, nil

			case "truncate":
				// Extract BeforeID argument if provided
				var beforeID string
				if bVal, ok := args["before_id"].(string); ok {
					beforeID = strings.TrimSpace(bVal)
				}

				// Extract KeepLast argument if provided, supporting int, float64, and int64 numeric types from JSON decoding
				keepLastProvided := false
				var keepLast int
				if kVal, ok := args["keep_last"]; ok && kVal != nil {
					keepLastProvided = true
					switch v := kVal.(type) {
					case int:
						keepLast = v
					case float64:
						keepLast = int(v)
					case int64:
						keepLast = int(v)
					default:
						return agent.AgentToolResult{}, errors.New("invalid parameter: keep_last must be an integer")
					}
				}

				// Validate mutual exclusivity and value constraints before attempting truncation
				if beforeID != "" && keepLastProvided {
					return agent.AgentToolResult{}, errors.New("scratchpad truncate requires either before_id or keep_last, not both")
				}
				if beforeID == "" && !keepLastProvided {
					return agent.AgentToolResult{}, errors.New("scratchpad truncate requires either before_id or keep_last > 0")
				}
				if keepLastProvided {
					if keepLast < 0 {
						return agent.AgentToolResult{}, errors.New("scratchpad truncate keep_last must be greater than 0")
					}
					if keepLast == 0 {
						return agent.AgentToolResult{}, errors.New("scratchpad truncate requires either before_id or keep_last > 0")
					}
				}

				// Execute truncation atomically in the durable store
				deletedCount, err := m.store.TruncateScratchpadEntries(ctx, agentID, store.ScratchpadTruncateOptions{
					BeforeID: beforeID,
					KeepLast: keepLast,
				})
				// Handle specific error cases (e.g. entry not found when before_id does not exist for agent)
				if err != nil {
					if errors.Is(err, sql.ErrNoRows) || errors.Is(err, store.ErrScratchpadEntryNotFound) {
						return agent.AgentToolResult{}, fmt.Errorf("entry not found: %s", beforeID)
					}
					return agent.AgentToolResult{}, err
				}

				entryWord := "entries"
				if deletedCount == 1 {
					entryWord = "entry"
				}
				return agent.AgentToolResult{
					Content: ai.ContentList{
						ai.TextContent{Text: fmt.Sprintf("Truncated %d scratchpad %s.", deletedCount, entryWord)},
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
		m.embeddedKBDeleteTool(owner),
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
