package managed

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/systemone"
	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
)

// resolveKBRoot returns the path to the knowledge base directory.
// Priority:
// 1. MECHA_KB_DIR env var if set and non-empty.
// 2. ~/.mecha-developer/kb/ if it exists.
// 3. ~/.mecha-wolfgang/kb/ if it exists.
// 4. Default to ~/.mecha-developer/kb/ (will be created if needed on write).
func resolveKBRoot() string {
	if custom := strings.TrimSpace(os.Getenv("MECHA_KB_DIR")); custom != "" {
		return filepath.Clean(custom)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		home = os.Getenv("HOME")
	}
	if home == "" {
		home = "."
	}

	primary := filepath.Join(home, ".mecha-developer", "kb")
	if info, err := os.Stat(primary); err == nil && info.IsDir() {
		return primary
	}

	fallback := filepath.Join(home, ".mecha-wolfgang", "kb")
	if info, err := os.Stat(fallback); err == nil && info.IsDir() {
		return fallback
	}

	return primary
}

// sanitizeKBPath ensures the path or topic slug is safe, prevents path traversal,
// and returns the clean absolute path inside kbRoot.
func sanitizeKBPath(kbRoot, input string) (string, error) {
	cleanInput := strings.TrimSpace(input)
	if cleanInput == "" {
		return "", fmt.Errorf("path or topic cannot be empty")
	}

	// Normalize if user provided a file ending in .md or a slug
	rel := filepath.Clean(cleanInput)
	if !strings.HasSuffix(strings.ToLower(rel), ".md") {
		rel = rel + ".md"
	}

	// Ensure no path traversal (e.g. "../")
	target := filepath.Join(kbRoot, rel)
	target = filepath.Clean(target)

	// Check that target stays within kbRoot
	cleanRoot := filepath.Clean(kbRoot)
	if target != cleanRoot && !strings.HasPrefix(target, cleanRoot+string(filepath.Separator)) {
		return "", fmt.Errorf("path traversal denied: %s is outside KB root %s", cleanInput, cleanRoot)
	}

	return target, nil
}

// toSlug converts a topic title to a slug (e.g. "JWT Authentication" -> "jwt-authentication").
func toSlug(topic string) string {
	slug := strings.ToLower(strings.TrimSpace(topic))
	re := regexp.MustCompile(`[^a-z0-9]+`)
	slug = re.ReplaceAllString(slug, "-")
	slug = strings.Trim(slug, "-")
	if slug == "" {
		slug = "topic"
	}
	return slug
}

// schemas for KB tools

func embeddedKBSearchSchema() *ai.Schema {
	closed := false
	schema := ai.Object(
		ai.Prop("query", ai.String("Search query to evaluate relevance against the knowledge base documents.")),
		ai.Opt("threshold", ai.Number("Relevance probability threshold between 0.0 and 1.0 (default: 0.5).")),
		ai.Opt("limit", ai.Integer("Maximum number of results to return (default: 10).")),
	)
	schema.AdditionalAllowed = &closed
	return schema
}

func embeddedKBReadSchema() *ai.Schema {
	closed := false
	schema := ai.Object(
		ai.Opt("topic", ai.String("Topic slug or name to read (e.g. 'jwt-authentication' or 'jwt-authentication.md').")),
		ai.Opt("path", ai.String("Relative or clean path to the markdown file inside the KB directory.")),
	)
	schema.AdditionalAllowed = &closed
	return schema
}

func embeddedKBWriteSchema() *ai.Schema {
	closed := false
	schema := ai.Object(
		ai.Prop("topic", ai.String("Topic title or slug to write (e.g. 'JWT Authentication').")),
		ai.Prop("content", ai.String("Markdown content for the topic.")),
		ai.Opt("domain", ai.String("Optional domain section for grouping in INDEX.md (e.g. 'Auth', 'Infrastructure').")),
	)
	schema.AdditionalAllowed = &closed
	return schema
}

func embeddedKBListSchema() *ai.Schema {
	closed := false
	schema := ai.Object(
		ai.Opt("domain", ai.String("Optional domain section to filter by.")),
	)
	schema.AdditionalAllowed = &closed
	return schema
}

// KBSearchResult represents a scored document in search results.
type KBSearchResult struct {
	Filename string  `json:"filename"`
	Score    float64 `json:"score"`
	Title    string  `json:"title,omitempty"`
	Summary  string  `json:"summary,omitempty"`
}

// kbFileEntry holds discovered file metadata for scoring.
type kbFileEntry struct {
	relPath string
	absPath string
	title   string
	summary string
	content string
}

// parseTopicHeader extracts title and summary from a markdown topic file if available.
func parseTopicHeader(content string) (title string, summary string) {
	lines := strings.Split(content, "\n")
	inSummary := false
	var summaryLines []string

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if title == "" && strings.HasPrefix(trimmed, "# ") {
			title = strings.TrimSpace(strings.TrimPrefix(trimmed, "# "))
			continue
		}
		if trimmed == "## Summary" {
			inSummary = true
			continue
		}
		if inSummary {
			if strings.HasPrefix(trimmed, "#") {
				break
			}
			if trimmed != "" {
				summaryLines = append(summaryLines, trimmed)
			}
		}
	}
	summary = strings.Join(summaryLines, " ")
	return title, summary
}

// embeddedKBSearchTool executes search over KB markdown files using System 1 or keyword fallback.
func (m *Manager) embeddedKBSearchTool(owner embeddedOwner) agent.AgentTool {
	return agent.AgentTool{
		Name:        "kb_search",
		Label:       "Knowledge Base Search",
		Description: "Search the persistent knowledge base for documents relevant to answering a query. Uses calibrated System 1 decision evaluation with keyword fallback.",
		Parameters:  embeddedKBSearchSchema(),
		Execute: func(ctx context.Context, id string, args map[string]any, _ agent.ToolUpdateFunc) (agent.AgentToolResult, error) {
			if owner.binding.AgentID != "" && owner.identity != "" {
				_ = m.store.RenewEmbeddedOwner(ctx, owner.binding, owner.identity, embeddedOwnerTTL)
			}

			rawQuery, _ := args["query"].(string)
			query := strings.TrimSpace(rawQuery)
			if query == "" {
				return agent.AgentToolResult{
					Content: ai.ContentList{ai.TextContent{Text: "error: query argument must be a non-empty string"}},
					Details: map[string]any{"error": "empty query"},
				}, nil
			}

			threshold := 0.5
			if tVal, ok := args["threshold"]; ok && tVal != nil {
				switch v := tVal.(type) {
				case float64:
					threshold = v
				case int:
					threshold = float64(v)
				case int64:
					threshold = float64(v)
				}
			}

			limit := 10
			if lVal, ok := args["limit"]; ok && lVal != nil {
				switch v := lVal.(type) {
				case int:
					limit = v
				case float64:
					limit = int(v)
				case int64:
					limit = int(v)
				}
			}
			if limit <= 0 {
				limit = 10
			}

			kbRoot := resolveKBRoot()
			if _, err := os.Stat(kbRoot); os.IsNotExist(err) {
				return agent.AgentToolResult{
					Content: ai.ContentList{ai.TextContent{Text: fmt.Sprintf("Knowledge base directory does not exist at %s (no documents found).", kbRoot)}},
					Details: map[string]any{"results": []any{}},
				}, nil
			}

			var entries []kbFileEntry

			_ = filepath.Walk(kbRoot, func(p string, info os.FileInfo, err error) error {
				if err != nil || info.IsDir() {
					return nil
				}
				if strings.HasSuffix(strings.ToLower(info.Name()), ".md") && !strings.EqualFold(info.Name(), "INDEX.md") {
					rel, relErr := filepath.Rel(kbRoot, p)
					if relErr == nil {
						data, readErr := os.ReadFile(p)
						content := ""
						if readErr == nil {
							content = string(data)
						}
						title, summary := parseTopicHeader(content)
						if title == "" {
							title = strings.TrimSuffix(info.Name(), filepath.Ext(info.Name()))
						}
						entries = append(entries, kbFileEntry{
							relPath: rel,
							absPath: p,
							title:   title,
							summary: summary,
							content: content,
						})
					}
				}
				return nil
			})

			if len(entries) == 0 {
				return agent.AgentToolResult{
					Content: ai.ContentList{ai.TextContent{Text: "No knowledge base documents found to search."}},
					Details: map[string]any{"results": []any{}},
				}, nil
			}

			// Try System 1 evaluation first in batches of up to 15-20 filenames
			scoredResults, err := m.scoreWithSystemOne(ctx, query, entries)
			usedFallback := false
			if err != nil || len(scoredResults) == 0 {
				// Fall back to keyword/substring match
				usedFallback = true
				scoredResults = scoreWithKeywordFallback(query, entries)
			}

			// Filter by threshold
			var filtered []KBSearchResult
			for _, r := range scoredResults {
				if r.Score >= threshold {
					filtered = append(filtered, r)
				}
			}

			// Sort descending by score
			sort.Slice(filtered, func(i, j int) bool {
				if filtered[i].Score == filtered[j].Score {
					return filtered[i].Filename < filtered[j].Filename
				}
				return filtered[i].Score > filtered[j].Score
			})

			// Limit results
			if len(filtered) > limit {
				filtered = filtered[:limit]
			}

			if len(filtered) == 0 {
				note := ""
				if usedFallback {
					note = " (using keyword matching fallback)"
				}
				return agent.AgentToolResult{
					Content: ai.ContentList{ai.TextContent{Text: fmt.Sprintf("No relevant documents found matching query %q (threshold: %.2f)%s.", query, threshold, note)}},
					Details: map[string]any{"query": query, "results": []any{}},
				}, nil
			}

			var sb strings.Builder
			sb.WriteString(fmt.Sprintf("Knowledge base search results for: %q\n", query))
			if usedFallback {
				sb.WriteString("(Evaluation method: keyword matching fallback)\n\n")
			} else {
				sb.WriteString("(Evaluation method: System 1 calibrated relevance)\n\n")
			}

			for idx, res := range filtered {
				sb.WriteString(fmt.Sprintf("%d. **%s** (`%s`) — Score: %.3f\n", idx+1, res.Title, res.Filename, res.Score))
				if res.Summary != "" {
					sb.WriteString(fmt.Sprintf("   Summary: %s\n", res.Summary))
				}
			}

			return agent.AgentToolResult{
				Content: ai.ContentList{ai.TextContent{Text: strings.TrimRight(sb.String(), "\n")}},
				Details: map[string]any{
					"query":    query,
					"results":  filtered,
					"fallback": usedFallback,
				},
			}, nil
		},
	}
}

// scoreWithSystemOne calls System 1 via local router or configured backend in batches of up to 15 files.
func (m *Manager) scoreWithSystemOne(ctx context.Context, query string, entries []kbFileEntry) ([]KBSearchResult, error) {
	// Discover System 1 connection via Store
	sysCfg, cfgErr := m.store.GetSystemOneConfig(ctx)
	if cfgErr != nil || !sysCfg.Enabled || sysCfg.Host == "" || sysCfg.Port <= 0 {
		return nil, fmt.Errorf("system one not configured")
	}

	client := systemone.NewClient(&http.Client{Timeout: 5 * time.Second})
	cfg := systemone.Config{
		Host:    sysCfg.Host,
		Port:    sysCfg.Port,
		Enabled: sysCfg.Enabled,
	}

	const batchSize = 15
	var allResults []KBSearchResult

	for i := 0; i < len(entries); i += batchSize {
		end := i + batchSize
		if end > len(entries) {
			end = len(entries)
		}
		batch := entries[i:end]

		// Build questions map: key is index or relPath, question type is "noul"
		questions := make(map[string]any)
		for idx, entry := range batch {
			qKey := fmt.Sprintf("q_%d", idx)
			// State or question prompt contains document metadata (title, summary, filename)
			questions[qKey] = map[string]any{
				"type": "noul",
				"instructions": fmt.Sprintf(
					"Determine if the knowledge base document '%s' (title: %q, summary: %q) is relevant to answering the query: %s",
					entry.relPath, entry.title, entry.summary, query,
				),
			}
		}

		// State context incorporates query and general context
		statePayload := fmt.Sprintf("Query: %s\nEvaluating %d documents.", query, len(batch))
		reqPayload := systemone.Request{
			Model:     "clef",
			State:     statePayload,
			Questions: questions,
		}

		resp, err := client.Invoke(ctx, cfg, reqPayload)
		if err != nil {
			return nil, err
		}

		for idx, entry := range batch {
			qKey := fmt.Sprintf("q_%d", idx)
			score, sErr := resp.Noul(qKey)
			if sErr == nil {
				allResults = append(allResults, KBSearchResult{
					Filename: entry.relPath,
					Score:    score,
					Title:    entry.title,
					Summary:  entry.summary,
				})
			}
		}
	}

	return allResults, nil
}

// scoreWithKeywordFallback scores documents based on word/substring matches in title, summary, and content.
func scoreWithKeywordFallback(query string, entries []kbFileEntry) []KBSearchResult {
	words := strings.Fields(strings.ToLower(query))
	var results []KBSearchResult

	for _, entry := range entries {
		titleLower := strings.ToLower(entry.title)
		summaryLower := strings.ToLower(entry.summary)
		relLower := strings.ToLower(entry.relPath)
		contentLower := strings.ToLower(entry.content)

		matches := 0
		totalScore := 0.0

		for _, word := range words {
			if len(word) <= 1 {
				continue
			}
			matched := false
			// Title match weight: 0.5
			if strings.Contains(titleLower, word) || strings.Contains(relLower, word) {
				totalScore += 0.5
				matched = true
			}
			// Summary match weight: 0.3
			if strings.Contains(summaryLower, word) {
				totalScore += 0.3
				matched = true
			}
			// Content match weight: 0.2
			if strings.Contains(contentLower, word) {
				totalScore += 0.2
				matched = true
			}
			if matched {
				matches++
			}
		}

		// Normalize score to [0.0, 1.0]
		score := 0.0
		if len(words) > 0 {
			score = totalScore / float64(len(words))
		}
		if score > 1.0 {
			score = 1.0
		}

		results = append(results, KBSearchResult{
			Filename: entry.relPath,
			Score:    score,
			Title:    entry.title,
			Summary:  entry.summary,
		})
	}

	return results
}

// embeddedKBReadTool reads a document from the KB with path traversal protection.
func (m *Manager) embeddedKBReadTool(owner embeddedOwner) agent.AgentTool {
	return agent.AgentTool{
		Name:        "kb_read",
		Label:       "Knowledge Base Read",
		Description: "Read the content of a topic or markdown document from the knowledge base.",
		Parameters:  embeddedKBReadSchema(),
		Execute: func(ctx context.Context, id string, args map[string]any, _ agent.ToolUpdateFunc) (agent.AgentToolResult, error) {
			if owner.binding.AgentID != "" && owner.identity != "" {
				_ = m.store.RenewEmbeddedOwner(ctx, owner.binding, owner.identity, embeddedOwnerTTL)
			}

			topic, _ := args["topic"].(string)
			pathArg, _ := args["path"].(string)

			targetParam := strings.TrimSpace(pathArg)
			if targetParam == "" {
				targetParam = strings.TrimSpace(topic)
			}
			if targetParam == "" {
				return agent.AgentToolResult{
					Content: ai.ContentList{ai.TextContent{Text: "error: topic or path parameter must be specified"}},
					Details: map[string]any{"error": "missing parameter"},
				}, nil
			}

			kbRoot := resolveKBRoot()
			filePath, err := sanitizeKBPath(kbRoot, targetParam)
			if err != nil {
				return agent.AgentToolResult{
					Content: ai.ContentList{ai.TextContent{Text: fmt.Sprintf("Error: %v", err)}},
					Details: map[string]any{"error": err.Error()},
				}, nil
			}

			// If file does not exist directly, try finding by slug
			if _, statErr := os.Stat(filePath); os.IsNotExist(statErr) {
				slug := toSlug(targetParam) + ".md"
				altPath := filepath.Join(kbRoot, slug)
				if _, altStatErr := os.Stat(altPath); altStatErr == nil {
					filePath = altPath
				} else {
					return agent.AgentToolResult{
						Content: ai.ContentList{ai.TextContent{Text: fmt.Sprintf("Knowledge base file not found: %s", targetParam)}},
						Details: map[string]any{"error": "file not found", "path": targetParam},
					}, nil
				}
			}

			data, err := os.ReadFile(filePath)
			if err != nil {
				return agent.AgentToolResult{
					Content: ai.ContentList{ai.TextContent{Text: fmt.Sprintf("Error reading knowledge base file: %v", err)}},
					Details: map[string]any{"error": err.Error()},
				}, nil
			}

			rel, _ := filepath.Rel(kbRoot, filePath)
			return agent.AgentToolResult{
				Content: ai.ContentList{ai.TextContent{Text: string(data)}},
				Details: map[string]any{"path": rel, "bytes": len(data)},
			}, nil
		},
	}
}

// embeddedKBWriteTool writes or updates a topic in the KB and updates INDEX.md.
func (m *Manager) embeddedKBWriteTool(owner embeddedOwner) agent.AgentTool {
	return agent.AgentTool{
		Name:        "kb_write",
		Label:       "Knowledge Base Write",
		Description: "Write or update a markdown document in the knowledge base and update INDEX.md.",
		Parameters:  embeddedKBWriteSchema(),
		Execute: func(ctx context.Context, id string, args map[string]any, _ agent.ToolUpdateFunc) (agent.AgentToolResult, error) {
			if owner.binding.AgentID != "" && owner.identity != "" {
				_ = m.store.RenewEmbeddedOwner(ctx, owner.binding, owner.identity, embeddedOwnerTTL)
			}

			rawTopic, _ := args["topic"].(string)
			topic := strings.TrimSpace(rawTopic)
			if topic == "" {
				return agent.AgentToolResult{
					Content: ai.ContentList{ai.TextContent{Text: "error: topic parameter is required"}},
					Details: map[string]any{"error": "missing topic"},
				}, nil
			}

			rawContent, _ := args["content"].(string)
			content := strings.TrimSpace(rawContent)
			if content == "" {
				return agent.AgentToolResult{
					Content: ai.ContentList{ai.TextContent{Text: "error: content parameter is required"}},
					Details: map[string]any{"error": "missing content"},
				}, nil
			}

			domain, _ := args["domain"].(string)
			domain = strings.TrimSpace(domain)

			kbRoot := resolveKBRoot()
			if err := os.MkdirAll(kbRoot, 0o755); err != nil {
				return agent.AgentToolResult{
					Content: ai.ContentList{ai.TextContent{Text: fmt.Sprintf("Error creating KB directory: %v", err)}},
					Details: map[string]any{"error": err.Error()},
				}, nil
			}

			slug := toSlug(topic)
			filename := slug + ".md"
			filePath, err := sanitizeKBPath(kbRoot, filename)
			if err != nil {
				return agent.AgentToolResult{
					Content: ai.ContentList{ai.TextContent{Text: fmt.Sprintf("Error: %v", err)}},
					Details: map[string]any{"error": err.Error()},
				}, nil
			}

			// Format topic content if missing title or standard frontmatter
			finalContent := content
			if !strings.HasPrefix(strings.TrimSpace(finalContent), "# ") {
				today := time.Now().Format("2006-01-02")
				finalContent = fmt.Sprintf("# %s\n\n**Created**: %s\n**Updated**: %s\n\n---\n\n%s\n", topic, today, today, content)
			}

			if err := os.WriteFile(filePath, []byte(finalContent), 0o644); err != nil {
				return agent.AgentToolResult{
					Content: ai.ContentList{ai.TextContent{Text: fmt.Sprintf("Error writing file %s: %v", filePath, err)}},
					Details: map[string]any{"error": err.Error()},
				}, nil
			}

			// Update INDEX.md
			indexPath := filepath.Join(kbRoot, "INDEX.md")
			updateIndex(indexPath, topic, filename, domain)

			return agent.AgentToolResult{
				Content: ai.ContentList{ai.TextContent{Text: fmt.Sprintf("Successfully saved knowledge base topic %q to %s and updated INDEX.md", topic, filename)}},
				Details: map[string]any{
					"topic":    topic,
					"filename": filename,
					"domain":   domain,
				},
			}, nil
		},
	}
}

// updateIndex adds or updates an entry in INDEX.md under the specified domain or "Ungrouped".
func updateIndex(indexPath, topic, filename, domain string) {
	if domain == "" {
		domain = "Ungrouped"
	}

	content := ""
	if data, err := os.ReadFile(indexPath); err == nil {
		content = string(data)
	}

	if content == "" {
		content = "# Knowledge Base Index\n\n## Ungrouped\n"
	}

	entryLine := fmt.Sprintf("- [%s](%s)", topic, filename)
	// If the file already exists in index, return
	if strings.Contains(content, fmt.Sprintf("(%s)", filename)) {
		return
	}

	domainHeader := fmt.Sprintf("## %s", domain)
	if strings.Contains(content, domainHeader) {
		// Insert entry under the domain section
		idx := strings.Index(content, domainHeader)
		rest := content[idx+len(domainHeader):]
		// Find next line after header
		if nextNewline := strings.Index(rest, "\n"); nextNewline != -1 {
			insertPos := idx + len(domainHeader) + nextNewline + 1
			content = content[:insertPos] + entryLine + "\n" + content[insertPos:]
		} else {
			content = content + "\n" + entryLine + "\n"
		}
	} else {
		// Append new domain section
		content = strings.TrimRight(content, "\n") + fmt.Sprintf("\n\n## %s\n\n%s\n", domain, entryLine)
	}

	_ = os.WriteFile(indexPath, []byte(content), 0o644)
}

// embeddedKBListTool lists topics from INDEX.md or directory.
func (m *Manager) embeddedKBListTool(owner embeddedOwner) agent.AgentTool {
	return agent.AgentTool{
		Name:        "kb_list",
		Label:       "Knowledge Base List",
		Description: "List topics available in the knowledge base, optionally filtered by domain section.",
		Parameters:  embeddedKBListSchema(),
		Execute: func(ctx context.Context, id string, args map[string]any, _ agent.ToolUpdateFunc) (agent.AgentToolResult, error) {
			if owner.binding.AgentID != "" && owner.identity != "" {
				_ = m.store.RenewEmbeddedOwner(ctx, owner.binding, owner.identity, embeddedOwnerTTL)
			}

			domainFilter, _ := args["domain"].(string)
			domainFilter = strings.TrimSpace(domainFilter)

			kbRoot := resolveKBRoot()
			indexPath := filepath.Join(kbRoot, "INDEX.md")

			if data, err := os.ReadFile(indexPath); err == nil {
				indexText := string(data)
				if domainFilter != "" {
					// Filter for the specific domain section
					domainHeader := fmt.Sprintf("## %s", domainFilter)
					idx := strings.Index(strings.ToLower(indexText), strings.ToLower(domainHeader))
					if idx == -1 {
						return agent.AgentToolResult{
							Content: ai.ContentList{ai.TextContent{Text: fmt.Sprintf("No domain section %q found in INDEX.md.", domainFilter)}},
							Details: map[string]any{"domain": domainFilter, "topics": []string{}},
						}, nil
					}
					rest := indexText[idx:]
					endIdx := strings.Index(rest[len(domainHeader):], "\n## ")
					sectionText := ""
					if endIdx != -1 {
						sectionText = rest[:len(domainHeader)+endIdx]
					} else {
						sectionText = rest
					}
					return agent.AgentToolResult{
						Content: ai.ContentList{ai.TextContent{Text: strings.TrimSpace(sectionText)}},
						Details: map[string]any{"domain": domainFilter},
					}, nil
				}

				return agent.AgentToolResult{
					Content: ai.ContentList{ai.TextContent{Text: indexText}},
					Details: map[string]any{"path": indexPath},
				}, nil
			}

			// If no INDEX.md, list all .md files in kbRoot
			files, err := os.ReadDir(kbRoot)
			if err != nil {
				return agent.AgentToolResult{
					Content: ai.ContentList{ai.TextContent{Text: fmt.Sprintf("Knowledge base directory %s is empty or not found.", kbRoot)}},
					Details: map[string]any{"topics": []string{}},
				}, nil
			}

			var topics []string
			for _, f := range files {
				if !f.IsDir() && strings.HasSuffix(strings.ToLower(f.Name()), ".md") {
					topics = append(topics, f.Name())
				}
			}

			if len(topics) == 0 {
				return agent.AgentToolResult{
					Content: ai.ContentList{ai.TextContent{Text: "No knowledge base documents found."}},
					Details: map[string]any{"topics": []string{}},
				}, nil
			}

			return agent.AgentToolResult{
				Content: ai.ContentList{ai.TextContent{Text: fmt.Sprintf("Found %d topics in knowledge base:\n- %s", len(topics), strings.Join(topics, "\n- "))}},
				Details: map[string]any{"topics": topics},
			}, nil
		},
	}
}
