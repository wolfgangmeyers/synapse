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
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sky-valley/pi/ai"
)

const (
	googleClientID     = "1071006060591-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com"
	googleClientSecret = "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f"
)

var (
	googleTokenURL           = "https://oauth2.googleapis.com/token"
	googleGenerateContentURL = "https://daily-cloudcode-pa.googleapis.com/v1internal:streamGenerateContent?alt=sse"
	googleAuthMu             sync.Mutex
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

func (m *Manager) refreshGoogleAntigravityToken(ctx context.Context, authPath string, allAuth map[string]any, antiMap map[string]any) error {
	refreshToken, _ := antiMap["refresh"].(string)
	if refreshToken == "" {
		return errors.New("no refresh token available for google antigravity")
	}

	data := url.Values{}
	data.Set("client_id", googleClientID)
	data.Set("client_secret", googleClientSecret)
	data.Set("refresh_token", refreshToken)
	data.Set("grant_type", "refresh_token")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, googleTokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return fmt.Errorf("failed to create refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("google token refresh request failed: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read refresh response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("google token refresh failed with status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var tokenResp struct {
		AccessToken  string `json:"access_token"`
		ExpiresIn    int64  `json:"expires_in"`
		RefreshToken string `json:"refresh_token"`
		TokenType    string `json:"token_type"`
	}
	if err := json.Unmarshal(bodyBytes, &tokenResp); err != nil {
		return fmt.Errorf("failed to parse google token response: %w", err)
	}

	if tokenResp.AccessToken == "" {
		return errors.New("google token refresh returned empty access_token")
	}

	antiMap["access"] = tokenResp.AccessToken
	if tokenResp.RefreshToken != "" {
		antiMap["refresh"] = tokenResp.RefreshToken
	}

	now := time.Now().UnixMilli()
	expires := now + tokenResp.ExpiresIn*1000
	if tokenResp.ExpiresIn > 300 {
		expires -= 5 * 60 * 1000
	}
	antiMap["expires"] = expires
	allAuth["antigravity"] = antiMap

	out, err := json.MarshalIndent(allAuth, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal updated auth.json: %w", err)
	}
	out = append(out, '\n')

	dir := filepath.Dir(authPath)
	tmpFile, err := os.CreateTemp(dir, "auth.*.json.tmp")
	if err != nil {
		return os.WriteFile(authPath, out, 0600)
	}
	tmpName := tmpFile.Name()
	defer os.Remove(tmpName)

	if err := tmpFile.Chmod(0600); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("failed to chmod temp auth file: %w", err)
	}
	if _, err := tmpFile.Write(out); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("failed to write temp auth file: %w", err)
	}
	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("failed to sync temp auth file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("failed to close temp auth file: %w", err)
	}

	if err := os.Rename(tmpName, authPath); err != nil {
		return fmt.Errorf("failed to replace auth.json: %w", err)
	}
	return nil
}

func (m *Manager) embeddedGoogleAuth(ctx context.Context) (token, projectID string, err error) {
	return m.embeddedGoogleAuthWithRefresh(ctx, false)
}

func (m *Manager) embeddedGoogleAuthWithRefresh(ctx context.Context, forceRefresh bool) (token, projectID string, err error) {
	googleAuthMu.Lock()
	defer googleAuthMu.Unlock()

	userHome, _ := os.UserHomeDir()
	paths := []string{
		filepath.Join(m.config.PiAgentDir, "auth.json"),
		filepath.Join(m.config.ManagedHome, "auth.json"),
		filepath.Join(userHome, ".pi", "agent", "auth.json"),
		filepath.Join(os.Getenv("HOME"), ".pi", "agent", "auth.json"),
	}
	var data []byte
	var targetPath string
	for _, p := range paths {
		if p == "" {
			continue
		}
		data, err = os.ReadFile(p)
		if err == nil && len(data) > 0 {
			targetPath = p
			break
		}
	}
	if len(data) == 0 || targetPath == "" {
		return "", "", errors.New("google auth credentials not found in auth.json")
	}

	allAuth := make(map[string]any)
	if err := json.Unmarshal(data, &allAuth); err != nil {
		return "", "", fmt.Errorf("failed to unmarshal auth.json: %w", err)
	}

	antiRaw, ok := allAuth["antigravity"].(map[string]any)
	if !ok || antiRaw == nil {
		return "", "", errors.New("antigravity credentials not found in auth.json")
	}

	access, _ := antiRaw["access"].(string)
	refresh, _ := antiRaw["refresh"].(string)
	pID, _ := antiRaw["projectId"].(string)
	if pID == "" {
		pID = "aicode-consumers"
	}

	var expires int64
	switch v := antiRaw["expires"].(type) {
	case float64:
		expires = int64(v)
	case int64:
		expires = v
	case int:
		expires = int64(v)
	case json.Number:
		expires, _ = v.Int64()
	}

	needsRefresh := forceRefresh || access == "" || expires <= time.Now().UnixMilli()+60*1000
	if needsRefresh && refresh != "" {
		if err := m.refreshGoogleAntigravityToken(ctx, targetPath, allAuth, antiRaw); err != nil {
			if access == "" || forceRefresh || expires <= time.Now().UnixMilli() {
				return "", "", fmt.Errorf("failed to refresh google token: %w", err)
			}
		} else {
			access, _ = antiRaw["access"].(string)
			if updatedPID, _ := antiRaw["projectId"].(string); updatedPID != "" {
				pID = updatedPID
			}
		}
	}

	if access == "" {
		return "", "", errors.New("no google access token in auth.json")
	}

	return access, pID, nil
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
		var rawContents []map[string]any
		droppedToolCalls := make(map[string]string)

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
					} else if tc, ok := c.(*ai.TextContent); ok && tc != nil && tc.Text != "" {
						parts = append(parts, map[string]any{"text": tc.Text})
					}
				}
				if len(parts) > 0 {
					rawContents = append(rawContents, map[string]any{"role": "user", "parts": parts})
				}
			} else if tm, ok := asToolResultMsg(msg); ok {
				outText := ai.ContentText(tm.Content)
				toolName, isDropped := droppedToolCalls[tm.ToolCallID]
				if !isDropped && tm.ToolName != "" {
					toolName, isDropped = droppedToolCalls[tm.ToolName]
				}
				if isDropped {
					if toolName == "" {
						toolName = tm.ToolName
					}
					if toolName == "" {
						toolName = tm.ToolCallID
					}
					rawContents = append(rawContents, map[string]any{
						"role": "user",
						"parts": []any{
							map[string]any{
								"text": fmt.Sprintf("[Observation from %s:\n%s]", toolName, outText),
							},
						},
					})
				} else {
					rawContents = append(rawContents, map[string]any{
						"role": "user",
						"parts": []any{
							map[string]any{
								"functionResponse": map[string]any{
									"name": tm.ToolCallID,
									"response": map[string]any{
										"output": outText,
									},
								},
							},
						},
					})
				}
			} else if am, ok := asAssistantMsg(msg); ok {
				var parts []any
				for _, c := range am.Content {
					if tc, ok := c.(ai.TextContent); ok && tc.Text != "" {
						parts = append(parts, map[string]any{"text": tc.Text})
					} else if tc, ok := c.(*ai.TextContent); ok && tc != nil && tc.Text != "" {
						parts = append(parts, map[string]any{"text": tc.Text})
					} else if call, ok := c.(ai.ToolCall); ok {
						if call.ID != "" {
							droppedToolCalls[call.ID] = call.Name
						}
						if call.Name != "" {
							droppedToolCalls[call.Name] = call.Name
						}
						parts = append(parts, map[string]any{"text": fmt.Sprintf("[Invoked %s]", call.Name)})
					} else if call, ok := c.(*ai.ToolCall); ok && call != nil {
						if call.ID != "" {
							droppedToolCalls[call.ID] = call.Name
						}
						if call.Name != "" {
							droppedToolCalls[call.Name] = call.Name
						}
						parts = append(parts, map[string]any{"text": fmt.Sprintf("[Invoked %s]", call.Name)})
					}
				}
				if len(parts) > 0 {
					rawContents = append(rawContents, map[string]any{"role": "model", "parts": parts})
				}
			}
		}

		var contents []any
		for _, item := range rawContents {
			role, _ := item["role"].(string)
			parts, _ := item["parts"].([]any)
			if len(parts) == 0 {
				continue
			}
			if len(contents) > 0 {
				lastTurn, ok := contents[len(contents)-1].(map[string]any)
				if ok && lastTurn["role"] == role {
					if lastParts, ok := lastTurn["parts"].([]any); ok {
						lastTurn["parts"] = append(lastParts, parts...)
						continue
					}
				}
			}
			contents = append(contents, map[string]any{
				"role":  role,
				"parts": parts,
			})
		}

		if len(contents) > 0 {
			firstTurn, _ := contents[0].(map[string]any)
			if firstTurn != nil && firstTurn["role"] == "model" {
				contents = append([]any{
					map[string]any{
						"role":  "user",
						"parts": []any{map[string]any{"text": "Start"}},
					},
				}, contents...)
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

		httpReq, err := http.NewRequestWithContext(requestCtx, http.MethodPost, googleGenerateContentURL, bytes.NewReader(payloadBytes))
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

		if resp.StatusCode == http.StatusUnauthorized {
			_ = resp.Body.Close()
			newToken, newProjectID, refreshErr := m.embeddedGoogleAuthWithRefresh(requestCtx, true)
			if refreshErr == nil {
				token = newToken
				if newProjectID != "" {
					projectID = newProjectID
					envelope["project"] = projectID
					if pb, err := json.Marshal(envelope); err == nil {
						payloadBytes = pb
					}
				}
				httpReq, err = http.NewRequestWithContext(requestCtx, http.MethodPost, googleGenerateContentURL, bytes.NewReader(payloadBytes))
				if err != nil {
					stream.Push(ai.AssistantMessageEvent{Type: ai.EventError, Error: &ai.AssistantMessage{ErrorMessage: err.Error()}})
					return
				}
				httpReq.Header.Set("Authorization", "Bearer "+token)
				httpReq.Header.Set("Content-Type", "application/json")
				httpReq.Header.Set("User-Agent", "antigravity/cli/1.2.4 (aidev_client; os_type=linux; arch=amd64; cl=982146307; auth_method=consumer)")

				resp, err = client.Do(httpReq)
				if err != nil {
					stream.Push(ai.AssistantMessageEvent{Type: ai.EventError, Error: &ai.AssistantMessage{ErrorMessage: err.Error()}})
					return
				}
			}
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
