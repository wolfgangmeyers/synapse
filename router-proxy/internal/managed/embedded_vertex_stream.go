package managed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/sky-valley/pi/ai"
)

// streamEmbeddedVertex streams directly against the Google Cloud Vertex AI streamGenerateContent endpoint.
func (m *Manager) streamEmbeddedVertex(requestCtx context.Context, requestModel *ai.Model, transcript ai.TranscriptContext, opts *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
	stream := ai.NewAssistantMessageEventStream()
	go func() {
		defer stream.End()

		token, projectID, location, err := m.embeddedVertexAuth(requestCtx, false, requestModel.ID)
		if err != nil {
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventError, Error: &ai.AssistantMessage{ErrorMessage: err.Error()}})
			return
		}

		var systemParts []any
		hasToolInstruction := false
		for _, msg := range transcript.Messages {
			if sm, ok := asSystemMsg(msg); ok {
				if text := strings.TrimSpace(ai.GetSystemMessageText(sm)); text != "" {
					if strings.Contains(text, "Never simulate, roleplay, or print bracketed tool observations") {
						hasToolInstruction = true
					}
					systemParts = append(systemParts, map[string]any{"text": text})
				}
			}
		}
		if !hasToolInstruction {
			systemParts = append(systemParts, map[string]any{"text": EmbeddedSystemInstruction})
		}

		contents := convertMessagesToGoogleContents(transcript.Messages, requestModel)

		currentTools := ai.GetCurrentTools(transcript.Messages)
		var tools []any
		if opts == nil || opts.ToolChoice != ai.ToolChoiceNone {
			if len(currentTools) > 0 {
				var decls []any
				for _, t := range currentTools {
					decls = append(decls, map[string]any{
						"name":        t.Name,
						"description": t.Description,
						"parameters":  sanitizeGeminiParameters(t.Parameters),
					})
				}
				tools = append(tools, map[string]any{"functionDeclarations": decls})
			}
		}

		maxOutputTokens := 8192
		if opts != nil && opts.MaxTokens != nil && *opts.MaxTokens > 0 {
			maxOutputTokens = *opts.MaxTokens
		}

		thinkingCfg := map[string]any{"includeThoughts": true}
		if opts != nil && (opts.Reasoning == "off" || opts.Reasoning == ai.ThinkingLevel("off")) {
			if strings.Contains(requestModel.ID, "gemini-4") {
				thinkingCfg = map[string]any{"thinkingLevel": "LOW"}
			} else if strings.Contains(requestModel.ID, "3.8") || strings.Contains(requestModel.ID, "3.7") || strings.Contains(requestModel.ID, "gemma") {
				thinkingCfg = map[string]any{"thinkingLevel": "MINIMAL"}
			} else {
				thinkingCfg = map[string]any{"thinkingBudget": 0}
			}
		}

		reqBody := map[string]any{
			"contents": contents,
			"generationConfig": map[string]any{
				"maxOutputTokens": maxOutputTokens,
				"thinkingConfig":  thinkingCfg,
			},
		}
		if len(systemParts) > 0 {
			reqBody["systemInstruction"] = map[string]any{
				"role":  "user",
				"parts": systemParts,
			}
		}
		if len(tools) > 0 {
			reqBody["tools"] = tools
		}

		payloadBytes, err := json.Marshal(reqBody)
		if err != nil {
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventError, Error: &ai.AssistantMessage{ErrorMessage: err.Error()}})
			return
		}

		targetURL := fmt.Sprintf(vertexGenerateContentURLTemplate, vertexHost(location), projectID, location, requestModel.ID)

		httpReq, err := http.NewRequestWithContext(requestCtx, http.MethodPost, targetURL, bytes.NewReader(payloadBytes))
		if err != nil {
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventError, Error: &ai.AssistantMessage{ErrorMessage: err.Error()}})
			return
		}

		httpReq.Header.Set("Authorization", "Bearer "+token)
		httpReq.Header.Set("Content-Type", "application/json")

		client := &http.Client{Timeout: 120 * time.Second}
		resp, err := client.Do(httpReq)
		if err != nil {
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventError, Error: &ai.AssistantMessage{ErrorMessage: err.Error()}})
			return
		}

		if resp.StatusCode == http.StatusUnauthorized {
			_ = resp.Body.Close()
			newToken, newProjectID, newLocation, refreshErr := m.embeddedVertexAuth(requestCtx, true, requestModel.ID)
			if refreshErr == nil {
				token = newToken
				if newProjectID != "" {
					projectID = newProjectID
				}
				if newLocation != "" {
					location = newLocation
				}
				targetURL = fmt.Sprintf(vertexGenerateContentURLTemplate, vertexHost(location), projectID, location, requestModel.ID)
				httpReq, err = http.NewRequestWithContext(requestCtx, http.MethodPost, targetURL, bytes.NewReader(payloadBytes))
				if err != nil {
					stream.Push(ai.AssistantMessageEvent{Type: ai.EventError, Error: &ai.AssistantMessage{ErrorMessage: err.Error()}})
					return
				}
				httpReq.Header.Set("Authorization", "Bearer "+token)
				httpReq.Header.Set("Content-Type", "application/json")

				resp, err = client.Do(httpReq)
				if err != nil {
					stream.Push(ai.AssistantMessageEvent{Type: ai.EventError, Error: &ai.AssistantMessage{ErrorMessage: err.Error()}})
					return
				}
			}
		} else if resp.StatusCode == http.StatusServiceUnavailable || resp.StatusCode == http.StatusTooManyRequests {
			_ = resp.Body.Close()
			time.Sleep(1500 * time.Millisecond)
			httpReq, err = http.NewRequestWithContext(requestCtx, http.MethodPost, targetURL, bytes.NewReader(payloadBytes))
			if err == nil {
				httpReq.Header.Set("Authorization", "Bearer "+token)
				httpReq.Header.Set("Content-Type", "application/json")
				resp, err = client.Do(httpReq)
				if err != nil {
					stream.Push(ai.AssistantMessageEvent{Type: ai.EventError, Error: &ai.AssistantMessage{ErrorMessage: err.Error()}})
					return
				}
			}
		}

		if resp.StatusCode != http.StatusOK {
			defer resp.Body.Close()
			var errBuf bytes.Buffer
			_, _ = errBuf.ReadFrom(resp.Body)
			stream.Push(ai.AssistantMessageEvent{
				Type:  ai.EventError,
				Error: &ai.AssistantMessage{ErrorMessage: fmt.Sprintf("vertex api error (status %d): %s", resp.StatusCode, errBuf.String())},
			})
			return
		}
		defer resp.Body.Close()

		stream.Push(ai.AssistantMessageEvent{Type: ai.EventStart})

		_, _ = scanGeminiSSEStream(resp.Body, m.nextToolCallID, stream)
	}()

	return stream
}
