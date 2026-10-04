package managed

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sky-valley/pi/ai"
)

type googleAuthJSON struct {
	Antigravity struct {
		Type      string `json:"type"`
		Access    string `json:"access"`
		Refresh   string `json:"refresh"`
		Expires   int64  `json:"expires"`
		ProjectID string `json:"projectId"`
		Email     string `json:"email"`
	} `json:"antigravity"`
}

func (m *Manager) embeddedGoogleAuth(ctx context.Context) (token, projectID string, err error) {
	userHome, _ := os.UserHomeDir()
	paths := []string{
		filepath.Join(m.config.PiAgentDir, "auth.json"),
		filepath.Join(m.config.ManagedHome, "auth.json"),
		filepath.Join(userHome, ".pi", "agent", "auth.json"),
		filepath.Join(os.Getenv("HOME"), ".pi", "agent", "auth.json"),
	}
	var data []byte
	for _, p := range paths {
		if p == "" {
			continue
		}
		data, err = os.ReadFile(p)
		if err == nil && len(data) > 0 {
			break
		}
	}
	if len(data) == 0 {
		return "", "", errors.New("google auth credentials not found in auth.json")
	}
	var authFile googleAuthJSON
	if err := json.Unmarshal(data, &authFile); err != nil {
		return "", "", err
	}
	if authFile.Antigravity.Access == "" {
		return "", "", errors.New("no google access token in auth.json")
	}
	pID := authFile.Antigravity.ProjectID
	if pID == "" {
		pID = "aicode-consumers"
	}
	return authFile.Antigravity.Access, pID, nil
}

func antigravityWireModel(id string) string {
	switch id {
	case "gemini-3.8-flash":
		return "gemini-3.8-flash-low"
	case "gemini-3.7-flash":
		return "gemini-3.7-flash-low"
	case "gemini-3.6-flash":
		return "gemini-3.6-flash-low"
	case "gemini-3.5-flash":
		return "gemini-3.5-flash-extra-low"
	case "gemini-3.1-pro":
		return "gemini-3.1-pro-low"
	default:
		return id
	}
}

func asUserMsg(m ai.Message) (ai.UserMessage, bool) {
	switch v := m.(type) {
	case ai.UserMessage:
		return v, true
	case *ai.UserMessage:
		return *v, true
	}
	return ai.UserMessage{}, false
}

func asAssistantMsg(m ai.Message) (ai.AssistantMessage, bool) {
	switch v := m.(type) {
	case ai.AssistantMessage:
		return v, true
	case *ai.AssistantMessage:
		return *v, true
	}
	return ai.AssistantMessage{}, false
}

func asSystemMsg(m ai.Message) (ai.SystemMessage, bool) {
	switch v := m.(type) {
	case ai.SystemMessage:
		return v, true
	case *ai.SystemMessage:
		return *v, true
	}
	return ai.SystemMessage{}, false
}

func asToolResultMsg(m ai.Message) (ai.ToolResultMessage, bool) {
	switch v := m.(type) {
	case ai.ToolResultMessage:
		return v, true
	case *ai.ToolResultMessage:
		return *v, true
	}
	return ai.ToolResultMessage{}, false
}

func (m *Manager) streamEmbeddedGoogle(requestCtx context.Context, requestModel *ai.Model, transcript ai.TranscriptContext, opts *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
	stream := ai.NewAssistantMessageEventStream()
	go func() {
		defer stream.End()

		token, projectID, err := m.embeddedGoogleAuth(requestCtx)
		if err != nil {
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventError, Error: &ai.AssistantMessage{ErrorMessage: err.Error()}})
			return
		}

		wireModel := antigravityWireModel(requestModel.ID)

		var systemParts []any
		var contents []any

		for _, msg := range transcript.Messages {
			if sm, ok := asSystemMsg(msg); ok {
				if text := strings.TrimSpace(ai.GetSystemMessageText(sm)); text != "" {
					systemParts = append(systemParts, map[string]any{"text": text})
				}
			} else if um, ok := asUserMsg(msg); ok {
				var parts []any
				for _, c := range um.Content {
					if tc, ok := c.(ai.TextContent); ok && tc.Text != "" {
						parts = append(parts, map[string]any{"text": tc.Text})
					}
				}
				if len(parts) > 0 {
					contents = append(contents, map[string]any{"role": "user", "parts": parts})
				}
			} else if tm, ok := asToolResultMsg(msg); ok {
				contents = append(contents, map[string]any{
					"role": "user",
					"parts": []any{
						map[string]any{
							"functionResponse": map[string]any{
								"name": tm.ToolCallID,
								"response": map[string]any{
									"output": ai.ContentText(tm.Content),
								},
							},
						},
					},
				})
			} else if am, ok := asAssistantMsg(msg); ok {
				var parts []any
				for _, c := range am.Content {
					if tc, ok := c.(ai.TextContent); ok && tc.Text != "" {
						parts = append(parts, map[string]any{"text": tc.Text})
					} else if call, ok := c.(ai.ToolCall); ok {
						parts = append(parts, map[string]any{
							"functionCall": map[string]any{
								"name": call.Name,
								"args": call.Arguments,
							},
						})
					}
				}
				if len(parts) > 0 {
					contents = append(contents, map[string]any{"role": "model", "parts": parts})
				}
			}
		}

		currentTools := ai.GetCurrentTools(transcript.Messages)
		var tools []any
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

		reqBody := map[string]any{
			"contents": contents,
			"generationConfig": map[string]any{
				"maxOutputTokens": 8192,
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

		envelope := map[string]any{
			"project": projectID,
			"model":   wireModel,
			"request": reqBody,
		}

		payloadBytes, err := json.Marshal(envelope)
		if err != nil {
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventError, Error: &ai.AssistantMessage{ErrorMessage: err.Error()}})
			return
		}

		httpReq, err := http.NewRequestWithContext(requestCtx, http.MethodPost, "https://daily-cloudcode-pa.googleapis.com/v1internal:streamGenerateContent?alt=sse", bytes.NewReader(payloadBytes))
		if err != nil {
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventError, Error: &ai.AssistantMessage{ErrorMessage: err.Error()}})
			return
		}

		httpReq.Header.Set("Authorization", "Bearer "+token)
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("User-Agent", "antigravity/cli/1.2.4 (aidev_client; os_type=linux; arch=amd64; cl=982146307; auth_method=consumer)")

		client := &http.Client{Timeout: 120 * time.Second}
		resp, err := client.Do(httpReq)
		if err != nil {
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventError, Error: &ai.AssistantMessage{ErrorMessage: err.Error()}})
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			var errBody bytes.Buffer
			_, _ = errBody.ReadFrom(resp.Body)
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventError, Error: &ai.AssistantMessage{ErrorMessage: fmt.Sprintf("Antigravity API error %d: %s", resp.StatusCode, errBody.String())}})
			return
		}

		stream.Push(ai.AssistantMessageEvent{Type: ai.EventStart})

		scanner := bufio.NewScanner(resp.Body)
		buf := make([]byte, 1024*1024)
		scanner.Buffer(buf, 1024*1024)

		var accumulatedText strings.Builder
		var accumulatedToolCalls []ai.ToolCall
		textStarted := false

		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := strings.TrimPrefix(line, "data: ")
			if strings.TrimSpace(data) == "" {
				continue
			}

			var sse struct {
				Response struct {
					Candidates []struct {
						Content struct {
							Role  string `json:"role"`
							Parts []struct {
								Text         string `json:"text"`
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

			for _, candidate := range sse.Response.Candidates {
				for _, part := range candidate.Content.Parts {
					if part.Text != "" {
						if !textStarted {
							stream.Push(ai.AssistantMessageEvent{Type: ai.EventTextStart, ContentIndex: 0})
							textStarted = true
						}
						accumulatedText.WriteString(part.Text)
						stream.Push(ai.AssistantMessageEvent{
							Type:         ai.EventTextDelta,
							ContentIndex: 0,
							Delta:        part.Text,
						})
					}
					if part.FunctionCall != nil {
						tc := ai.ToolCall{
							ID:        part.FunctionCall.Name,
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

		if textStarted {
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventTextEnd, ContentIndex: 0})
		}

		finalMsg := &ai.AssistantMessage{
			Content: ai.ContentList{},
		}
		if accumulatedText.Len() > 0 {
			finalMsg.Content = append(finalMsg.Content, ai.TextContent{Text: accumulatedText.String()})
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
