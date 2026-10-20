package managed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
)

var (
	antigravityGroundingURL    = "https://daily-cloudcode-pa.googleapis.com/v1internal:generateContent"
	vertexGroundingURLTemplate = "https://%s/v1beta1/projects/%s/locations/%s/publishers/google/models/%s:generateContent"
	geminiGroundingURLTemplate = "https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent?key=%s"
)

func embeddedGoogleSearchSchema() *ai.Schema {
	closed := false
	s := ai.Object(
		ai.Prop("query", ai.String("The search query to ground against Google Search.")),
		ai.Opt("instruction", ai.String("Specific directive or guidance for search synthesis and focus.")),
		ai.Opt("urls", ai.ArrayOf(ai.String(), "Specific URLs to analyze alongside web search.")),
	)
	s.AdditionalAllowed = &closed
	return s
}

// hasGoogleSearchAuth reports true if Antigravity credentials, Gemini API key, or Vertex AI ADC credentials
// are available on this system.
func (m *Manager) hasGoogleSearchAuth(ctx context.Context) bool {
	if m == nil {
		return false
	}
	token, _, err := m.embeddedGoogleAuth(ctx)
	if err == nil && token != "" {
		return true
	}
	if apiKey, aErr := m.resolveGoogleAPIKey(ctx); aErr == nil && apiKey != "" {
		return true
	}
	vToken, _, _, vErr := m.embeddedVertexAuth(ctx, false, "gemini-3.8-flash")
	return vErr == nil && vToken != ""
}

// embeddedGoogleSearchTool creates the agent.AgentTool for google_search.
func (m *Manager) embeddedGoogleSearchTool(owner embeddedOwner) agent.AgentTool {
	return agent.AgentTool{
		Name:        "google_search",
		Label:       "Google Search",
		Description: "Search the web using Google Search via Google Search Grounding. Returns real-time web results with multi-angle deep search and source citations. Use this whenever current, real-time information or web research is needed.",
		Parameters:  embeddedGoogleSearchSchema(),
		Execute: func(callCtx context.Context, id string, args map[string]any, update agent.ToolUpdateFunc) (agent.AgentToolResult, error) {
			if err := m.store.RenewEmbeddedOwner(callCtx, owner.binding, owner.identity, embeddedOwnerTTL); err != nil {
				return agent.AgentToolResult{}, err
			}
			role, err := m.store.EmbeddedSessionRole(callCtx, owner.binding)
			if err != nil {
				return agent.AgentToolResult{}, err
			}
			if role != "root" && role != "worker" {
				return agent.AgentToolResult{}, fmt.Errorf("google_search requires embedded root or worker")
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

			resultText, searchErr := m.executeGoogleSearch(callCtx, query, instruction, urls)
			if searchErr != nil {
				return agent.AgentToolResult{
					Content: ai.ContentList{ai.TextContent{Text: fmt.Sprintf("Error performing Google search: %v", searchErr)}},
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

// executeGoogleSearch attempts to execute search grounding via Antigravity first,
// falling back to Gemini API key grounding, and then to Vertex AI if prior methods fail or are uncredentialed.
func (m *Manager) executeGoogleSearch(ctx context.Context, query string, instruction string, urls []string) (string, error) {
	var antigravityErr error
	token, projectID, err := m.embeddedGoogleAuth(ctx)
	if err == nil && token != "" {
		res, aErr := m.executeAntigravityGrounding(ctx, token, projectID, query, instruction, urls)
		if aErr == nil {
			return res, nil
		}
		antigravityErr = aErr
	} else if err != nil {
		antigravityErr = err
	}

	var geminiAPIErr error
	apiKey, keyErr := m.resolveGoogleAPIKey(ctx)
	if keyErr == nil && apiKey != "" {
		res, gErr := m.executeGeminiAPIKeyGrounding(ctx, apiKey, query, instruction, urls)
		if gErr == nil {
			return res, nil
		}
		geminiAPIErr = gErr
	}

	// Fallback to Vertex AI
	vToken, vProject, vLoc, vErr := m.embeddedVertexAuth(ctx, false, "gemini-3.8-flash")
	if vErr == nil && vToken != "" {
		res, vExecErr := m.executeVertexGrounding(ctx, vToken, vProject, vLoc, query, instruction, urls)
		if vExecErr == nil {
			return res, nil
		}
		if antigravityErr != nil || geminiAPIErr != nil {
			return "", fmt.Errorf("google search failed (antigravity: %v; gemini: %v; vertex: %v)", antigravityErr, geminiAPIErr, vExecErr)
		}
		return "", fmt.Errorf("vertex google search failed: %w", vExecErr)
	}

	if geminiAPIErr != nil {
		return "", fmt.Errorf("gemini api google search failed: %w", geminiAPIErr)
	}
	if antigravityErr != nil {
		return "", fmt.Errorf("antigravity google search failed: %w", antigravityErr)
	}
	return "", errors.New("no Google credentials available for search (neither Antigravity, Gemini API key, nor Vertex AI)")
}

type groundingChunkWeb struct {
	URI   string `json:"uri"`
	Title string `json:"title"`
}

type groundingChunk struct {
	Web *groundingChunkWeb `json:"web"`
}

type groundingMetadata struct {
	WebSearchQueries []string         `json:"webSearchQueries"`
	GroundingChunks  []groundingChunk `json:"groundingChunks"`
}

type candidatePart struct {
	Text    string `json:"text"`
	Thought bool   `json:"thought,omitempty"`
}

type candidateContent struct {
	Parts []candidatePart `json:"parts"`
	Role  string          `json:"role"`
}

type searchCandidate struct {
	Content           *candidateContent  `json:"content"`
	GroundingMetadata *groundingMetadata `json:"groundingMetadata"`
	FinishReason      string             `json:"finishReason"`
}

type generateContentResponse struct {
	Candidates []searchCandidate `json:"candidates"`
	Response   *struct {
		Candidates []searchCandidate `json:"candidates"`
	} `json:"response"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error"`
}

func formatGroundingResponse(resp *generateContentResponse) (string, error) {
	if resp == nil {
		return "", errors.New("empty response from Google Search Grounding")
	}
	if resp.Error != nil && resp.Error.Message != "" {
		return "", fmt.Errorf("api error (%d %s): %s", resp.Error.Code, resp.Error.Status, resp.Error.Message)
	}

	candidates := resp.Candidates
	if len(candidates) == 0 && resp.Response != nil {
		candidates = resp.Response.Candidates
	}
	if len(candidates) == 0 {
		return "No search results returned.", nil
	}

	cand := candidates[0]
	var textParts []string
	if cand.Content != nil {
		for _, part := range cand.Content.Parts {
			if !part.Thought && strings.TrimSpace(part.Text) != "" {
				textParts = append(textParts, part.Text)
			}
		}
	}

	var sb strings.Builder
	if len(textParts) > 0 {
		sb.WriteString(strings.Join(textParts, "\n\n"))
	} else {
		sb.WriteString("No summary text generated.")
	}

	meta := cand.GroundingMetadata
	if meta != nil {
		if len(meta.GroundingChunks) > 0 {
			sb.WriteString("\n\n### Sources\n")
			seen := make(map[string]bool)
			for _, chunk := range meta.GroundingChunks {
				if chunk.Web != nil && chunk.Web.URI != "" {
					uri := strings.TrimSpace(chunk.Web.URI)
					if seen[uri] {
						continue
					}
					seen[uri] = true
					title := strings.TrimSpace(chunk.Web.Title)
					if title == "" {
						title = uri
					}
					sb.WriteString(fmt.Sprintf("- [%s](%s)\n", title, uri))
				}
			}
		}
		if len(meta.WebSearchQueries) > 0 {
			sb.WriteString("\n*Search queries: ")
			sb.WriteString(strings.Join(meta.WebSearchQueries, ", "))
			sb.WriteString("*\n")
		}
	}

	return strings.TrimSpace(sb.String()), nil
}

func (m *Manager) executeAntigravityGrounding(ctx context.Context, token, projectID, query, instruction string, urls []string) (string, error) {
	if projectID == "" {
		projectID = "antigravity"
	}
	reqID := fmt.Sprintf("req-%d", time.Now().UnixNano())

	tools := []any{
		map[string]any{"googleSearch": map[string]any{}},
	}
	if len(urls) > 0 {
		tools = append(tools, map[string]any{"urlContext": map[string]any{}})
	}

	var contents []any
	contents = append(contents, map[string]any{
		"role":  "user",
		"parts": []any{map[string]any{"text": query}},
	})

	innerReq := map[string]any{
		"contents": contents,
		"tools":    tools,
	}
	if instruction != "" {
		innerReq["systemInstruction"] = map[string]any{
			"role":  "user",
			"parts": []any{map[string]any{"text": instruction}},
		}
	}

	envelope := map[string]any{
		"project":     projectID,
		"model":       "gemini-3.8-flash-low",
		"request":     innerReq,
		"requestType": "agent",
		"userAgent":   "antigravity",
		"requestId":   reqID,
	}

	bodyBytes, err := json.Marshal(envelope)
	if err != nil {
		return "", err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, antigravityGroundingURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("User-Agent", "antigravity/cli/1.2.4 (aidev_client; os_type=linux; arch=amd64; cl=982146307; auth_method=consumer)")

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("antigravity grounding request failed with status %d: %s", resp.StatusCode, string(respBody))
	}

	var parsed generateContentResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", fmt.Errorf("parsing grounding response: %w", err)
	}

	return formatGroundingResponse(&parsed)
}

func (m *Manager) executeVertexGrounding(ctx context.Context, token, projectID, location, query, instruction string, urls []string) (string, error) {
	if location == "" {
		location = "us-central1"
	}
	model := "gemini-3.8-flash"
	url := fmt.Sprintf(vertexGroundingURLTemplate, vertexHost(location), projectID, location, model)

	tools := []any{
		map[string]any{"googleSearch": map[string]any{}},
	}
	if len(urls) > 0 {
		tools = append(tools, map[string]any{"urlContext": map[string]any{}})
	}

	payload := map[string]any{
		"contents": []any{
			map[string]any{
				"role":  "user",
				"parts": []any{map[string]any{"text": query}},
			},
		},
		"tools": tools,
	}
	if instruction != "" {
		payload["systemInstruction"] = map[string]any{
			"role":  "user",
			"parts": []any{map[string]any{"text": instruction}},
		}
	}

	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("vertex grounding request failed with status %d: %s", resp.StatusCode, string(respBody))
	}

	var parsed generateContentResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", fmt.Errorf("parsing vertex grounding response: %w", err)
	}

	return formatGroundingResponse(&parsed)
}

func (m *Manager) executeGeminiAPIKeyGrounding(ctx context.Context, apiKey, query, instruction string, urls []string) (string, error) {
	model := "gemini-3.8-flash"
	url := fmt.Sprintf(geminiGroundingURLTemplate, model, apiKey)

	tools := []any{
		map[string]any{"googleSearch": map[string]any{}},
	}
	if len(urls) > 0 {
		tools = append(tools, map[string]any{"urlContext": map[string]any{}})
	}

	payload := map[string]any{
		"contents": []any{
			map[string]any{
				"role":  "user",
				"parts": []any{map[string]any{"text": query}},
			},
		},
		"tools": tools,
	}
	if instruction != "" {
		payload["systemInstruction"] = map[string]any{
			"role":  "user",
			"parts": []any{map[string]any{"text": instruction}},
		}
	}

	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("gemini api grounding request failed with status %d: %s", resp.StatusCode, string(respBody))
	}

	var parsed generateContentResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", fmt.Errorf("parsing gemini api grounding response: %w", err)
	}

	return formatGroundingResponse(&parsed)
}
