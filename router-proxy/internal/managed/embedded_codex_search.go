package managed

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
)

var (
	codexSearchAPIURL = "https://chatgpt.com/backend-api/codex/responses"
)

func embeddedCodexSearchSchema() *ai.Schema {
	closed := false
	s := ai.Object(
		ai.Prop("query", ai.String("The search query to search via OpenAI Codex Responses API.")),
		ai.Opt("instruction", ai.String("Specific directive or guidance for search synthesis and focus.")),
		ai.Opt("urls", ai.ArrayOf(ai.String(), "Specific URLs to analyze alongside web search.")),
	)
	s.AdditionalAllowed = &closed
	return s
}

// embeddedCodexSearchTool creates the agent.AgentTool for codex_search.
func (m *Manager) embeddedCodexSearchTool(owner embeddedOwner) agent.AgentTool {
	return agent.AgentTool{
		Name:        "codex_search",
		Label:       "Codex Search",
		Description: "Search the web using OpenAI Codex Responses API web search. Returns real-time web results with source citations and query grounding. Use this whenever current, real-time information or web research is needed.",
		Parameters:  embeddedCodexSearchSchema(),
		Execute: func(callCtx context.Context, id string, args map[string]any, update agent.ToolUpdateFunc) (agent.AgentToolResult, error) {
			if err := m.store.RenewEmbeddedOwner(callCtx, owner.binding, owner.identity, embeddedOwnerTTL); err != nil {
				return agent.AgentToolResult{}, err
			}
			role, err := m.store.EmbeddedSessionRole(callCtx, owner.binding)
			if err != nil {
				return agent.AgentToolResult{}, err
			}
			if role != "root" && role != "worker" {
				return agent.AgentToolResult{}, fmt.Errorf("codex_search requires embedded root or worker")
			}

			rawQuery, _ := args["query"].(string)
			query := strings.TrimSpace(rawQuery)
			if query == "" {
				return agent.AgentToolResult{
					Content: ai.ContentList{ai.TextContent{Text: "error: query argument must be a non-empty string"}},
					Details: map[string]any{"error": "empty query"},
				}, nil
			}

			var instruction string
			if inst, ok := args["instruction"].(string); ok {
				instruction = strings.TrimSpace(inst)
			}

			var urls []string
			if rawURLs, ok := args["urls"].([]any); ok {
				for _, u := range rawURLs {
					if s, ok := u.(string); ok && strings.TrimSpace(s) != "" {
						urls = append(urls, strings.TrimSpace(s))
					}
				}
			} else if strURLs, ok := args["urls"].([]string); ok {
				for _, s := range strURLs {
					if strings.TrimSpace(s) != "" {
						urls = append(urls, strings.TrimSpace(s))
					}
				}
			}

			if update != nil {
				update(agent.AgentToolResult{
					Details: map[string]any{"status": "searching", "query": query},
				})
			}

			resultText, searchErr := m.executeCodexSearch(callCtx, query, instruction, urls)
			if searchErr != nil {
				return agent.AgentToolResult{
					Content: ai.ContentList{ai.TextContent{Text: fmt.Sprintf("Error performing Codex search: %v", searchErr)}},
					Details: map[string]any{"error": searchErr.Error()},
				}, nil
			}

			return agent.AgentToolResult{
				Content: ai.ContentList{ai.TextContent{Text: resultText}},
				Details: map[string]any{
					"query": query,
				},
			}, nil
		},
	}
}

func (m *Manager) executeCodexSearch(ctx context.Context, query, instruction string, urls []string) (string, error) {
	token, accountID, err := m.resolveCodexSearchToken(ctx)
	if err != nil || token == "" {
		return "", errors.New("No OpenAI/Codex credentials found. Run /login openai-codex or authenticate with Codex CLI.")
	}

	apiURL := codexSearchAPIURL
	if custom := os.Getenv("OPENAI_CODEX_API_URL"); custom != "" {
		apiURL = custom
	}

	fullQuery := query
	if instruction != "" {
		fullQuery = fmt.Sprintf("%s\n\nInstruction: %s", fullQuery, instruction)
	}
	if len(urls) > 0 {
		fullQuery = fmt.Sprintf("%s\n\nReference URLs:\n%s", fullQuery, strings.Join(urls, "\n"))
	}

	payload := map[string]any{
		"model":   "gpt-5.5",
		"store":   false,
		"stream":  true,
		"tools":   []any{map[string]any{"type": "web_search"}},
		"include": []string{"web_search_call.results"},
		"input": []any{
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{
						"type": "text",
						"text": fullQuery,
					},
				},
			},
		},
	}

	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshalling request body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", fmt.Errorf("creating http request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+token)
	if accountID != "" {
		req.Header.Set("ChatGPT-Account-ID", accountID)
	}
	req.Header.Set("OpenAI-Beta", "responses=experimental")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("performing codex search request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return "", errors.New("Codex auth unauthorized (401)")
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return "", errors.New("Codex rate limit exceeded (429)")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("codex search api error (%d): %s", resp.StatusCode, string(b))
	}

	return parseCodexSearchResponse(resp.Body)
}


type codexSource struct {
	title string
	url   string
}

func parseCodexSearchResponse(body io.Reader) (string, error) {
	scanner := bufio.NewScanner(body)
	var textParts []string
	var sources []codexSource
	seenURLs := make(map[string]bool)
	var queries []string
	seenQueries := make(map[string]bool)

	for scanner.Scan() {
		line := scanner.Text()
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		dataStr := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if dataStr == "" || dataStr == "[DONE]" {
			continue
		}

		var event map[string]any
		if err := json.Unmarshal([]byte(dataStr), &event); err != nil {
			continue
		}

		eventType, _ := event["type"].(string)

		switch eventType {
		case "response.output_text.delta":
			if delta, ok := event["delta"].(string); ok && delta != "" {
				textParts = append(textParts, delta)
			}
		case "response.output_item.done":
			if item, ok := event["item"].(map[string]any); ok {
				extractItemData(item, &sources, seenURLs, &queries, seenQueries)
			}
		case "response.completed":
			if responseObj, ok := event["response"].(map[string]any); ok {
				if output, ok := responseObj["output"].([]any); ok {
					for _, outItem := range output {
						if m, ok := outItem.(map[string]any); ok {
							extractItemData(m, &sources, seenURLs, &queries, seenQueries)
						}
					}
				}
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("reading response sse stream: %w", err)
	}

	synthesis := strings.TrimSpace(strings.Join(textParts, ""))
	if synthesis == "" {
		synthesis = "No search results returned."
	}

	var sb strings.Builder
	sb.WriteString(synthesis)

	if len(sources) > 0 {
		sb.WriteString("\n\n### Sources\n")
		for _, s := range sources {
			sb.WriteString(fmt.Sprintf("- [%s](%s)\n", s.title, s.url))
		}
	}

	if len(queries) > 0 {
		sb.WriteString("\n*Search queries: ")
		sb.WriteString(strings.Join(queries, ", "))
		sb.WriteString("*\n")
	}

	return strings.TrimSpace(sb.String()), nil
}

func extractItemData(item map[string]any, sources *[]codexSource, seenURLs map[string]bool, queries *[]string, seenQueries map[string]bool) {
	itemType, _ := item["type"].(string)
	if itemType == "web_search_call" {
		if action, ok := item["action"].(map[string]any); ok {
			if q, ok := action["query"].(string); ok && strings.TrimSpace(q) != "" {
				trimmed := strings.TrimSpace(q)
				if !seenQueries[trimmed] {
					seenQueries[trimmed] = true
					*queries = append(*queries, trimmed)
				}
			}
			if sourcesList, ok := action["sources"].([]any); ok {
				for _, sRaw := range sourcesList {
					if sm, ok := sRaw.(map[string]any); ok {
						url, _ := sm["url"].(string)
						url = strings.TrimSpace(url)
						if url != "" && !seenURLs[url] {
							seenURLs[url] = true
							title, _ := sm["title"].(string)
							title = strings.TrimSpace(title)
							if title == "" {
								title = url
							}
							*sources = append(*sources, codexSource{title: title, url: url})
						}
					}
				}
			}
		}
	}
}
