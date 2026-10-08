package managed

import (
	"bufio"
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

		token, projectID, location, err := m.embeddedVertexAuth(requestCtx, false)
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
						"parameters":  t.Parameters,
					})
				}
				tools = append(tools, map[string]any{"functionDeclarations": decls})
			}
		}

		maxOutputTokens := 8192
		if opts != nil && opts.MaxTokens != nil && *opts.MaxTokens > 0 {
			maxOutputTokens = *opts.MaxTokens
		}

		reqBody := map[string]any{
			"contents": contents,
			"generationConfig": map[string]any{
				"maxOutputTokens": maxOutputTokens,
				"thinkingConfig":  map[string]any{"includeThoughts": true},
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

		targetURL := fmt.Sprintf(vertexGenerateContentURLTemplate, location, projectID, location, requestModel.ID)

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
			newToken, newProjectID, newLocation, refreshErr := m.embeddedVertexAuth(requestCtx, true)
			if refreshErr == nil {
				token = newToken
				if newProjectID != "" {
					projectID = newProjectID
				}
				if newLocation != "" {
					location = newLocation
				}
				targetURL = fmt.Sprintf(vertexGenerateContentURLTemplate, location, projectID, location, requestModel.ID)
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

		scanner := bufio.NewScanner(resp.Body)
		buf := make([]byte, 1024*1024)
		scanner.Buffer(buf, 1024*1024)

		type contentBlock struct {
			kind string
			buf  strings.Builder
		}
		var blocks []*contentBlock
		var currentBlock *contentBlock
		currentBlockIndex := -1
		var accumulatedToolCalls []ai.ToolCall

		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
			if data == "" {
				continue
			}

			var sse struct {
				Candidates []struct {
					Content struct {
						Role  string `json:"role"`
						Parts []struct {
							Text         string `json:"text"`
							Thought      bool   `json:"thought"`
							FunctionCall *struct {
								Name string         `json:"name"`
								Args map[string]any `json:"args"`
							} `json:"functionCall"`
						} `json:"parts"`
					} `json:"content"`
					FinishReason string `json:"finishReason"`
				} `json:"candidates"`
				Response *struct {
					Candidates []struct {
						Content struct {
							Role  string `json:"role"`
							Parts []struct {
								Text         string `json:"text"`
								Thought      bool   `json:"thought"`
								FunctionCall *struct {
									Name string         `json:"name"`
									Args map[string]any `json:"args"`
								} `json:"functionCall"`
							} `json:"parts"`
						} `json:"content"`
						FinishReason string `json:"finishReason"`
					} `json:"candidates"`
				} `json:"response"`
				Error *struct {
					Code    int    `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}

			if err := json.Unmarshal([]byte(data), &sse); err != nil {
				continue
			}

			if sse.Error != nil {
				stream.Push(ai.AssistantMessageEvent{Type: ai.EventError, Error: &ai.AssistantMessage{ErrorMessage: sse.Error.Message}})
				return
			}

			candidates := sse.Candidates
			if len(candidates) == 0 && sse.Response != nil {
				candidates = sse.Response.Candidates
			}

			for _, candidate := range candidates {
				for _, part := range candidate.Content.Parts {
					if part.Text != "" {
						wantKind := "text"
						if part.Thought {
							wantKind = "thinking"
						}
						if currentBlock == nil || currentBlock.kind != wantKind {
							if currentBlock != nil {
								if currentBlock.kind == "thinking" {
									stream.Push(ai.AssistantMessageEvent{
										Type:         ai.EventThinkingEnd,
										ContentIndex: currentBlockIndex,
										Content:      currentBlock.buf.String(),
									})
								} else {
									stream.Push(ai.AssistantMessageEvent{
										Type:         ai.EventTextEnd,
										ContentIndex: currentBlockIndex,
										Content:      currentBlock.buf.String(),
									})
								}
							}
							currentBlock = &contentBlock{kind: wantKind}
							blocks = append(blocks, currentBlock)
							currentBlockIndex = len(blocks) - 1
							if wantKind == "thinking" {
								stream.Push(ai.AssistantMessageEvent{
									Type:         ai.EventThinkingStart,
									ContentIndex: currentBlockIndex,
								})
							} else {
								stream.Push(ai.AssistantMessageEvent{
									Type:         ai.EventTextStart,
									ContentIndex: currentBlockIndex,
								})
							}
						}
						currentBlock.buf.WriteString(part.Text)
						if wantKind == "thinking" {
							stream.Push(ai.AssistantMessageEvent{
								Type:         ai.EventThinkingDelta,
								ContentIndex: currentBlockIndex,
								Delta:        part.Text,
							})
						} else {
							stream.Push(ai.AssistantMessageEvent{
								Type:         ai.EventTextDelta,
								ContentIndex: currentBlockIndex,
								Delta:        part.Text,
							})
						}
					}
					if part.FunctionCall != nil {
						if currentBlock != nil {
							if currentBlock.kind == "thinking" {
								stream.Push(ai.AssistantMessageEvent{
									Type:         ai.EventThinkingEnd,
									ContentIndex: currentBlockIndex,
									Content:      currentBlock.buf.String(),
								})
							} else {
								stream.Push(ai.AssistantMessageEvent{
									Type:         ai.EventTextEnd,
									ContentIndex: currentBlockIndex,
									Content:      currentBlock.buf.String(),
								})
							}
							currentBlock = nil
						}
						callID := ""
						if m != nil && m.ids != nil {
							callID, _ = m.ids.Next()
						}
						if callID == "" {
							callID = fmt.Sprintf("call_%s_%d", part.FunctionCall.Name, time.Now().UnixNano())
						} else {
							callID = "call_" + callID
						}
						tc := ai.ToolCall{
							ID:        callID,
							Name:      part.FunctionCall.Name,
							Arguments: part.FunctionCall.Args,
						}
						accumulatedToolCalls = append(accumulatedToolCalls, tc)
						stream.Push(ai.AssistantMessageEvent{
							Type:     ai.EventToolCallStart,
							ToolCall: &tc,
						})
						stream.Push(ai.AssistantMessageEvent{
							Type:     ai.EventToolCallEnd,
							ToolCall: &tc,
						})
					}
				}
			}
		}

		if currentBlock != nil {
			if currentBlock.kind == "thinking" {
				stream.Push(ai.AssistantMessageEvent{
					Type:         ai.EventThinkingEnd,
					ContentIndex: currentBlockIndex,
					Content:      currentBlock.buf.String(),
				})
			} else {
				stream.Push(ai.AssistantMessageEvent{
					Type:         ai.EventTextEnd,
					ContentIndex: currentBlockIndex,
					Content:      currentBlock.buf.String(),
				})
			}
			currentBlock = nil
		}

		finalMsg := &ai.AssistantMessage{
			Content: ai.ContentList{},
		}
		for _, b := range blocks {
			if b.kind == "thinking" && b.buf.Len() > 0 {
				finalMsg.Content = append(finalMsg.Content, ai.ThinkingContent{Thinking: b.buf.String()})
			} else if b.kind == "text" && b.buf.Len() > 0 {
				finalMsg.Content = append(finalMsg.Content, ai.TextContent{Text: b.buf.String()})
			}
		}
		for _, tc := range accumulatedToolCalls {
			finalMsg.Content = append(finalMsg.Content, tc)
		}
		if len(accumulatedToolCalls) > 0 {
			finalMsg.StopReason = ai.StopToolUse
		} else {
			finalMsg.StopReason = ai.StopStop
		}

		stream.Push(ai.AssistantMessageEvent{
			Type:    ai.EventDone,
			Message: finalMsg,
		})
	}()

	return stream
}
