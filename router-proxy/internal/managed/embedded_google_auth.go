package managed

import (
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
	googleClientSecret = "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"
)

var (
	googleTokenURL                  = "https://oauth2.googleapis.com/token"
	googleGenerateContentURL        = "https://daily-cloudcode-pa.googleapis.com/v1internal:streamGenerateContent?alt=sse"
	googleGenerativeLanguageURLBase = "https://generativelanguage.googleapis.com"
	googleAuthMu                    sync.Mutex
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

// resolveGoogleAPIKey resolves the Google Gemini API key from environment variables
// (GEMINI_API_KEY, GOOGLE_API_KEY) or auth.json (allAuth["google"]["key"]).
func (m *Manager) resolveGoogleAPIKey(ctx context.Context) (string, error) {
	if key := strings.TrimSpace(os.Getenv("GEMINI_API_KEY")); key != "" {
		return key, nil
	}
	if key := strings.TrimSpace(os.Getenv("GOOGLE_API_KEY")); key != "" {
		return key, nil
	}

	userHome, _ := os.UserHomeDir()
	paths := []string{
		filepath.Join(m.config.PiAgentDir, "auth.json"),
		filepath.Join(m.config.ManagedHome, "auth.json"),
		filepath.Join(userHome, ".pi", "agent", "auth.json"),
		filepath.Join(os.Getenv("HOME"), ".pi", "agent", "auth.json"),
	}

	for _, p := range paths {
		if p == "" {
			continue
		}
		data, err := os.ReadFile(p)
		if err != nil || len(data) == 0 {
			continue
		}
		var allAuth map[string]any
		if err := json.Unmarshal(data, &allAuth); err != nil {
			continue
		}
		if gRaw, ok := allAuth["google"].(map[string]any); ok && gRaw != nil {
			if key, ok := gRaw["key"].(string); ok && strings.TrimSpace(key) != "" {
				return strings.TrimSpace(key), nil
			}
		}
	}

	return "", errors.New("no Google API key found in GEMINI_API_KEY, GOOGLE_API_KEY, or auth.json")
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

// stripSimulatedObservations filters or strips lines containing simulated tool observations
// (such as "Observation: subagent", lines starting with "Observation:", or fake background launch notices)
// from assistant message text so Gemini never sees simulated observations in its input context.
func stripSimulatedObservations(text string) string {
	if text == "" {
		return ""
	}
	lines := strings.Split(text, "\n")
	var filtered []string
	inObsCall := false
	inObsNotice := false
	parenDepth := 0

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		cleaned := strings.TrimLeft(trimmed, "[]")
		cleaned = strings.TrimSpace(cleaned)

		isObsStart := strings.HasPrefix(cleaned, "Observation:") ||
			strings.Contains(trimmed, "Observation: subagent") ||
			strings.Contains(trimmed, "Observation: `subagent`") ||
			strings.Contains(trimmed, "Observation: bash") ||
			strings.Contains(trimmed, "Observation: `bash`")

		if isObsStart {
			inObsCall = true
			inObsNotice = false
			parenDepth = strings.Count(line, "(") - strings.Count(line, ")")
			if parenDepth <= 0 {
				inObsCall = false
				inObsNotice = true
			}
			continue
		}

		if inObsCall {
			parenDepth += strings.Count(line, "(") - strings.Count(line, ")")
			if parenDepth <= 0 {
				inObsCall = false
				inObsNotice = true
			}
			continue
		}

		if inObsNotice {
			isNotice := strings.HasPrefix(cleaned, "Agent started in background") ||
				strings.HasPrefix(cleaned, "Agent ID: subagent-") ||
				strings.HasPrefix(cleaned, "Type: ") ||
				strings.HasPrefix(cleaned, "Description: ") ||
				strings.HasPrefix(cleaned, "You will be notified") ||
				strings.HasPrefix(cleaned, "Use get_subagent_result") ||
				strings.HasPrefix(cleaned, "Do not duplicate") ||
				strings.HasPrefix(cleaned, "exit=") ||
				cleaned == "]" ||
				cleaned == ""
			if isNotice {
				continue
			}
			inObsNotice = false
		}

		if strings.HasPrefix(cleaned, "Observation:") ||
			strings.Contains(trimmed, "Observation: subagent") ||
			strings.Contains(trimmed, "Observation: `subagent`") ||
			strings.HasPrefix(cleaned, "Agent started in background") ||
			strings.HasPrefix(cleaned, "Agent ID: subagent-") {
			continue
		}

		if strings.HasPrefix(line, "]") && !strings.Contains(line, "[") {
			line = strings.TrimPrefix(line, "]")
			line = strings.TrimLeft(line, " \t")
		}

		filtered = append(filtered, line)
	}

	return strings.TrimSpace(strings.Join(filtered, "\n"))
}

func isAssistantContentEmpty(am ai.AssistantMessage) bool {
	if len(am.Content) == 0 {
		return true
	}
	for _, c := range am.Content {
		switch part := c.(type) {
		case ai.ToolCall:
			return false
		case *ai.ToolCall:
			if part != nil {
				return false
			}
		case ai.TextContent:
			if strings.TrimSpace(part.Text) != "" {
				return false
			}
		case *ai.TextContent:
			if part != nil && strings.TrimSpace(part.Text) != "" {
				return false
			}
		case ai.ThinkingContent:
			if strings.TrimSpace(part.Thinking) != "" {
				return false
			}
		case *ai.ThinkingContent:
			if part != nil && strings.TrimSpace(part.Thinking) != "" {
				return false
			}
		}
	}
	return true
}

func estimateMessageTokens(m ai.Message) int {
	if um, ok := asUserMsg(m); ok {
		chars := 0
		for _, part := range um.Content {
			if tc, ok := part.(ai.TextContent); ok {
				chars += len(tc.Text)
			} else if tc, ok := part.(*ai.TextContent); ok && tc != nil {
				chars += len(tc.Text)
			} else if _, ok := part.(ai.ImageContent); ok {
				chars += 1000
			} else if img, ok := part.(*ai.ImageContent); ok && img != nil {
				chars += 1000
			}
		}
		return (chars + 3) / 4 + 4
	}
	if am, ok := asAssistantMsg(m); ok {
		chars := 0
		for _, part := range am.Content {
			if tc, ok := part.(ai.TextContent); ok {
				chars += len(tc.Text)
			} else if tc, ok := part.(*ai.TextContent); ok && tc != nil {
				chars += len(tc.Text)
			} else if th, ok := part.(ai.ThinkingContent); ok {
				chars += len(th.Thinking)
			} else if th, ok := part.(*ai.ThinkingContent); ok && th != nil {
				chars += len(th.Thinking)
			} else if call, ok := part.(ai.ToolCall); ok {
				argsBytes, _ := json.Marshal(call.Arguments)
				chars += len(call.Name) + len(argsBytes)
			} else if call, ok := part.(*ai.ToolCall); ok && call != nil {
				argsBytes, _ := json.Marshal(call.Arguments)
				chars += len(call.Name) + len(argsBytes)
			}
		}
		return (chars + 3) / 4 + 4
	}
	if tm, ok := asToolResultMsg(m); ok {
		chars := len(ai.ContentText(tm.Content))
		for _, part := range tm.Content {
			if _, ok := part.(ai.ImageContent); ok {
				chars += 1000
			} else if img, ok := part.(*ai.ImageContent); ok && img != nil {
				chars += 1000
			}
		}
		return (chars + 3) / 4 + 4
	}
	if sm, ok := asSystemMsg(m); ok {
		chars := len(ai.GetSystemMessageText(sm))
		return (chars + 3) / 4 + 4
	}
	return 4
}

func estimateTranscriptTokens(messages []ai.Message) int {
	total := 0
	for _, m := range messages {
		total += estimateMessageTokens(m)
	}
	return total
}

func estimateGoogleContentsTokens(contents []map[string]any) int {
	total := 0
	for _, item := range contents {
		parts, _ := item["parts"].([]any)
		for _, p := range parts {
			pMap, ok := p.(map[string]any)
			if !ok {
				continue
			}
			if text, ok := pMap["text"].(string); ok {
				total += (len(text) + 3) / 4
			}
			if fc, ok := pMap["functionCall"].(map[string]any); ok {
				name, _ := fc["name"].(string)
				args, _ := json.Marshal(fc["args"])
				total += (len(name) + len(args) + 3) / 4 + 4
			}
			if fr, ok := pMap["functionResponse"].(map[string]any); ok {
				name, _ := fr["name"].(string)
				resp, _ := json.Marshal(fr["response"])
				total += (len(name) + len(resp) + 3) / 4 + 4
			}
			if _, ok := pMap["inlineData"]; ok {
				total += 258
			}
		}
		total += 4
	}
	return total
}

func estimateTranscriptTokensPruned(messages []ai.Message, omitted map[int]bool) int {
	total := 0
	for i, m := range messages {
		if omitted[i] {
			continue
		}
		total += estimateMessageTokens(m)
	}
	return total
}

func collectPruned(messages []ai.Message, omitted map[int]bool) []ai.Message {
	var result []ai.Message
	for i, m := range messages {
		if omitted[i] {
			continue
		}
		result = append(result, m)
	}
	return result
}

func parseConvertOptions(args []any) (int, map[string]string) {
	contextWindow := 1048576 // default to 1M (Gemini standard)
	droppedToolCalls := make(map[string]string)

	for _, arg := range args {
		switch v := arg.(type) {
		case int:
			if v > 0 {
				contextWindow = v
			}
		case *ai.Model:
			if v != nil && v.ContextWindow > 0 {
				contextWindow = v.ContextWindow
			}
		case map[string]string:
			if v != nil {
				for k, val := range v {
					droppedToolCalls[k] = val
				}
			}
		}
	}
	return contextWindow, droppedToolCalls
}

func pruneTranscriptMessages(messages []ai.Message, targetTokens int) []ai.Message {
	if len(messages) <= 50 {
		return messages
	}

	suffixStart := len(messages) - 50
	if suffixStart < 1 {
		suffixStart = 1
	}

	pruned := make([]ai.Message, len(messages))
	copy(pruned, messages)

	// Stage 1: Prune intermediate ToolResultMessages, thinking content, and oversized tool call args
	for i := 1; i < suffixStart; i++ {
		if _, ok := asUserMsg(pruned[i]); ok {
			// ALWAYS KEEP EVERY user message!
			continue
		}
		if tm, ok := asToolResultMsg(pruned[i]); ok {
			prunedTM := tm
			prunedTM.Content = ai.ContentList{ai.TextContent{Text: "[Output omitted for context fitting]"}}
			pruned[i] = prunedTM
			continue
		}
		if am, ok := asAssistantMsg(pruned[i]); ok {
			var newContent ai.ContentList
			for _, part := range am.Content {
				if _, isThinking := part.(ai.ThinkingContent); isThinking {
					continue
				}
				if _, isThinkingPtr := part.(*ai.ThinkingContent); isThinkingPtr {
					continue
				}
				if call, isCall := part.(ai.ToolCall); isCall {
					argsBytes, _ := json.Marshal(call.Arguments)
					if len(argsBytes) > 500 {
						call.Arguments = map[string]any{"_omitted": "[Arguments omitted for context fitting]"}
					}
					newContent = append(newContent, call)
				} else if call, isCallPtr := part.(*ai.ToolCall); isCallPtr && call != nil {
					argsBytes, _ := json.Marshal(call.Arguments)
					cCopy := *call
					if len(argsBytes) > 500 {
						cCopy.Arguments = map[string]any{"_omitted": "[Arguments omitted for context fitting]"}
					}
					newContent = append(newContent, cCopy)
				} else {
					newContent = append(newContent, part)
				}
			}
			prunedAM := am
			prunedAM.Content = newContent
			pruned[i] = prunedAM
			continue
		}
	}

	if estimateTranscriptTokens(pruned) <= targetTokens {
		return pruned
	}

	// Stage 2: Omit intermediate tool call & response pairs from oldest to newest
	omitted := make(map[int]bool)

	callIDToResultIdx := make(map[string]int)
	callNameToResultIdx := make(map[string]int)
	for i := 1; i < suffixStart; i++ {
		if tm, ok := asToolResultMsg(pruned[i]); ok {
			if tm.ToolCallID != "" {
				callIDToResultIdx[tm.ToolCallID] = i
			}
			if tm.ToolName != "" {
				callNameToResultIdx[tm.ToolName] = i
			}
		}
	}

	for i := 1; i < suffixStart; i++ {
		if estimateTranscriptTokensPruned(pruned, omitted) <= targetTokens {
			break
		}
		if _, ok := asUserMsg(pruned[i]); ok {
			continue // NEVER omit user messages
		}
		if am, ok := asAssistantMsg(pruned[i]); ok {
			var remainingParts ai.ContentList
			hasToolCall := false
			for _, part := range am.Content {
				var callID, callName string
				if call, isCall := part.(ai.ToolCall); isCall {
					callID = call.ID
					callName = call.Name
				} else if call, isCallPtr := part.(*ai.ToolCall); isCallPtr && call != nil {
					callID = call.ID
					callName = call.Name
				}
				if callName != "" || callID != "" {
					hasToolCall = true
					if resIdx, found := callIDToResultIdx[callID]; found {
						omitted[resIdx] = true
					} else if resIdx, found := callNameToResultIdx[callName]; found {
						omitted[resIdx] = true
					}
					continue
				}
				remainingParts = append(remainingParts, part)
			}
			if hasToolCall {
				if isAssistantContentEmpty(ai.AssistantMessage{Content: remainingParts}) {
					omitted[i] = true
				} else {
					updatedAM := am
					updatedAM.Content = remainingParts
					pruned[i] = updatedAM
				}
			}
		}
	}

	if estimateTranscriptTokensPruned(pruned, omitted) <= targetTokens {
		return collectPruned(pruned, omitted)
	}

	// Stage 3: Omit older intermediate assistant text messages from oldest to newest
	for i := 1; i < suffixStart; i++ {
		if estimateTranscriptTokensPruned(pruned, omitted) <= targetTokens {
			break
		}
		if _, ok := asUserMsg(pruned[i]); ok {
			continue // NEVER touch user messages
		}
		if _, ok := asAssistantMsg(pruned[i]); ok {
			omitted[i] = true
		}
	}

	return collectPruned(pruned, omitted)
}

func convertMessagesToGoogleContents(messages []ai.Message, args ...any) []map[string]any {
	contextWindow, droppedToolCalls := parseConvertOptions(args)
	triggerTokens := contextWindow * 80 / 100
	targetTokens := contextWindow * 50 / 100

	initialTokens := estimateTranscriptTokens(messages)
	if initialTokens > triggerTokens {
		messages = pruneTranscriptMessages(messages, targetTokens)
	}
	toolCallNames := make(map[string]string)

	var rawContents []map[string]any

	for _, msg := range messages {
		if _, ok := asSystemMsg(msg); ok {
			continue
		} else if um, ok := asUserMsg(msg); ok {
			var parts []any
			for _, part := range um.Content {
				if tc, ok := part.(ai.TextContent); ok && tc.Text != "" {
					cleaned := unwrapEmbeddedMailText(tc.Text)
					parts = append(parts, map[string]any{"text": cleaned})
					continue
				} else if tc, ok := part.(*ai.TextContent); ok && tc != nil && tc.Text != "" {
					cleaned := unwrapEmbeddedMailText(tc.Text)
					parts = append(parts, map[string]any{"text": cleaned})
					continue
				}
				if img, ok := part.(ai.ImageContent); ok {
					parts = append(parts, map[string]any{
						"inlineData": map[string]any{
							"mimeType": img.MimeType,
							"data":     img.Data,
						},
					})
					continue
				} else if img, ok := part.(*ai.ImageContent); ok && img != nil {
					parts = append(parts, map[string]any{
						"inlineData": map[string]any{
							"mimeType": img.MimeType,
							"data":     img.Data,
						},
					})
					continue
				}
			}
			if len(parts) > 0 {
				rawContents = append(rawContents, map[string]any{"role": "user", "parts": parts})
			}
		} else if tm, ok := asToolResultMsg(msg); ok {
			outText := ai.ContentText(tm.Content)

			toolName := tm.ToolName
			if toolName == "" && tm.ToolCallID != "" {
				if name, exists := toolCallNames[tm.ToolCallID]; exists {
					toolName = name
				}
			}

			// Ensure that when tool results arrive, they match the corresponding functionCall in the preceding model message
			var precedingModelTurn map[string]any
			var modelTurnIndex = -1
			for i := len(rawContents) - 1; i >= 0; i-- {
				if rawContents[i]["role"] == "model" {
					precedingModelTurn = rawContents[i]
					modelTurnIndex = i
					break
				}
				if rawContents[i]["role"] != "user" {
					break
				}
			}

			if precedingModelTurn != nil {
				existingCalls := 0
				if modelParts, ok := precedingModelTurn["parts"].([]any); ok {
					for _, p := range modelParts {
						if pMap, ok := p.(map[string]any); ok {
							if fc, ok := pMap["functionCall"].(map[string]any); ok {
								if fc["name"] == toolName {
									existingCalls++
								}
							}
						}
					}
				}
				existingResponses := 0
				for i := modelTurnIndex + 1; i < len(rawContents); i++ {
					if userParts, ok := rawContents[i]["parts"].([]any); ok {
						for _, p := range userParts {
							if pMap, ok := p.(map[string]any); ok {
								if fr, ok := pMap["functionResponse"].(map[string]any); ok {
									if fr["name"] == toolName {
										existingResponses++
									}
								}
							}
						}
					}
				}
				if existingResponses+1 > existingCalls {
					precedingModelTurn["parts"] = append(precedingModelTurn["parts"].([]any), map[string]any{
						"functionCall": map[string]any{
							"name": toolName,
							"args": map[string]any{},
						},
						"thought_signature": "skip_thought_signature_validator",
						"thoughtSignature":  "skip_thought_signature_validator",
					})
				}
			} else if len(rawContents) > 0 {
				precedingModelTurn = map[string]any{
					"role": "model",
					"parts": []any{
						map[string]any{
							"functionCall": map[string]any{
								"name": toolName,
								"args": map[string]any{},
							},
							"thought_signature": "skip_thought_signature_validator",
							"thoughtSignature":  "skip_thought_signature_validator",
						},
					},
				}
				rawContents = append(rawContents, precedingModelTurn)
			}

			var parts []any
			parts = append(parts, map[string]any{
				"functionResponse": map[string]any{
					"name": toolName,
					"response": map[string]any{
						"output": outText,
					},
				},
			})
			for _, part := range tm.Content {
				if img, ok := part.(ai.ImageContent); ok {
					parts = append(parts, map[string]any{
						"inlineData": map[string]any{
							"mimeType": img.MimeType,
							"data":     img.Data,
						},
					})
					continue
				} else if img, ok := part.(*ai.ImageContent); ok && img != nil {
					parts = append(parts, map[string]any{
						"inlineData": map[string]any{
							"mimeType": img.MimeType,
							"data":     img.Data,
						},
					})
					continue
				}
			}
			if len(parts) > 0 {
				rawContents = append(rawContents, map[string]any{"role": "user", "parts": parts})
			}
		} else if am, ok := asAssistantMsg(msg); ok {
			toolCallCount := 0
			hasNonEmptyText := false
			for _, c := range am.Content {
				switch part := c.(type) {
				case ai.ToolCall:
					toolCallCount++
				case *ai.ToolCall:
					if part != nil {
						toolCallCount++
					}
				case ai.TextContent:
					t := strings.TrimSpace(part.Text)
					if t != "" && t != "(empty response)" {
						hasNonEmptyText = true
					}
				case *ai.TextContent:
					if part != nil {
						t := strings.TrimSpace(part.Text)
						if t != "" && t != "(empty response)" {
							hasNonEmptyText = true
						}
					}
				}
			}
			if toolCallCount == 0 && !hasNonEmptyText {
				continue
			}

			var parts []any
			for _, c := range am.Content {
				if tc, ok := c.(ai.TextContent); ok && tc.Text != "" {
					cleaned := stripSimulatedObservations(tc.Text)
					if cleaned != "" && cleaned != "(empty response)" {
						parts = append(parts, map[string]any{"text": cleaned})
					}
				} else if tc, ok := c.(*ai.TextContent); ok && tc != nil && tc.Text != "" {
					cleaned := stripSimulatedObservations(tc.Text)
					if cleaned != "" && cleaned != "(empty response)" {
						parts = append(parts, map[string]any{"text": cleaned})
					}
				} else if th, ok := c.(ai.ThinkingContent); ok && th.Thinking != "" {
					parts = append(parts, map[string]any{"thought": true, "text": th.Thinking})
				} else if th, ok := c.(*ai.ThinkingContent); ok && th != nil && th.Thinking != "" {
					parts = append(parts, map[string]any{"thought": true, "text": th.Thinking})
				} else if call, ok := c.(ai.ToolCall); ok {
					callID := call.ID
					if callID == "" {
						callID = call.Name
					}
					argsJSON, _ := json.Marshal(call.Arguments)
					label := fmt.Sprintf("`%s` (%s)", call.Name, string(argsJSON))
					if string(argsJSON) == "{}" || string(argsJSON) == "null" || len(call.Arguments) == 0 {
						label = fmt.Sprintf("`%s`", call.Name)
					}
					droppedToolCalls[callID] = label
					droppedToolCalls[call.Name] = label

					if call.ID != "" {
						toolCallNames[call.ID] = call.Name
					}

					args := call.Arguments
					if args == nil {
						args = map[string]any{}
					}
					parts = append(parts, map[string]any{
						"functionCall": map[string]any{
							"name": call.Name,
							"args": args,
						},
						"thought_signature": "skip_thought_signature_validator",
						"thoughtSignature":  "skip_thought_signature_validator",
					})
				} else if call, ok := c.(*ai.ToolCall); ok && call != nil {
					callID := call.ID
					if callID == "" {
						callID = call.Name
					}
					argsJSON, _ := json.Marshal(call.Arguments)
					label := fmt.Sprintf("`%s` (%s)", call.Name, string(argsJSON))
					if string(argsJSON) == "{}" || string(argsJSON) == "null" || len(call.Arguments) == 0 {
						label = fmt.Sprintf("`%s`", call.Name)
					}
					droppedToolCalls[callID] = label
					droppedToolCalls[call.Name] = label

					if call.ID != "" {
						toolCallNames[call.ID] = call.Name
					}

					args := call.Arguments
					if args == nil {
						args = map[string]any{}
					}
					parts = append(parts, map[string]any{
						"functionCall": map[string]any{
							"name": call.Name,
							"args": args,
						},
						"thought_signature": "skip_thought_signature_validator",
						"thoughtSignature":  "skip_thought_signature_validator",
					})
				}
			}
			if len(parts) > 0 {
				rawContents = append(rawContents, map[string]any{"role": "model", "parts": parts})
			}
		}
	}

	var contents []map[string]any
	for _, item := range rawContents {
		role, _ := item["role"].(string)
		parts, _ := item["parts"].([]any)
		if len(parts) == 0 {
			continue
		}
		if len(contents) > 0 {
			lastTurn := contents[len(contents)-1]
			if lastTurn["role"] == role {
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

	// Ensure that all functionCalls in non-final model turns have matching functionResponses in the next user turn
	for i := 0; i < len(contents)-1; i++ {
		if contents[i]["role"] == "model" && contents[i+1]["role"] == "user" {
			modelParts, _ := contents[i]["parts"].([]any)
			userParts, _ := contents[i+1]["parts"].([]any)

			callCounts := make(map[string]int)
			for _, p := range modelParts {
				if pMap, ok := p.(map[string]any); ok {
					if fc, ok := pMap["functionCall"].(map[string]any); ok {
						if name, ok := fc["name"].(string); ok && name != "" {
							callCounts[name]++
						}
					}
				}
			}

			responseCounts := make(map[string]int)
			for _, p := range userParts {
				if pMap, ok := p.(map[string]any); ok {
					if fr, ok := pMap["functionResponse"].(map[string]any); ok {
						if name, ok := fr["name"].(string); ok && name != "" {
							responseCounts[name]++
						}
					}
				}
			}

			for toolName, calls := range callCounts {
				resps := responseCounts[toolName]
				for r := resps; r < calls; r++ {
					userParts = append(userParts, map[string]any{
						"functionResponse": map[string]any{
							"name": toolName,
							"response": map[string]any{
								"output": "[Output omitted for context fitting]",
							},
						},
					})
				}
			}
			contents[i+1]["parts"] = userParts
		}
	}

	if len(contents) > 0 {
		firstTurn := contents[0]
		if firstTurn["role"] == "model" {
			contents = append([]map[string]any{
				{
					"role":  "user",
					"parts": []any{map[string]any{"text": "Start"}},
				},
			}, contents...)
		}
	}

	return contents
}

var geminiRejectedSchemaFields = map[string]bool{
	"additionalProperties": true,
	"$schema":              true,
	"$id":                  true,
	"$anchor":              true,
	"$dynamicAnchor":       true,
	"$vocabulary":          true,
	"$comment":             true,
	"$defs":                true,
	"definitions":          true,
}

func sanitizeGeminiParameters(v any) any {
	if v == nil {
		return nil
	}

	// If v is an *ai.Schema or other custom struct, marshal to JSON and unmarshal
	// to generic map/slice so we can traverse arbitrary schema structures cleanly.
	if _, ok := v.(map[string]any); !ok {
		if _, ok := v.([]any); !ok {
			raw, err := json.Marshal(v)
			if err != nil {
				return v
			}
			var generic any
			if err := json.Unmarshal(raw, &generic); err != nil {
				return v
			}
			v = generic
		}
	}

	switch val := v.(type) {
	case map[string]any:
		cleaned := make(map[string]any, len(val))
		for k, child := range val {
			if geminiRejectedSchemaFields[k] {
				continue
			}
			cleaned[k] = sanitizeGeminiParameters(child)
		}
		return cleaned
	case []any:
		cleaned := make([]any, len(val))
		for i, child := range val {
			cleaned[i] = sanitizeGeminiParameters(child)
		}
		return cleaned
	default:
		return v
	}
}

func (m *Manager) streamEmbeddedGoogle(requestCtx context.Context, requestModel *ai.Model, transcript ai.TranscriptContext, opts *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
	stream := ai.NewAssistantMessageEventStream()
	go func() {
		defer stream.End()

		var (
			apiKey     string
			token      string
			projectID  string
			targetURL  string
			isGoogle   = requestModel.Provider == "google"
			authHeader string
		)

		if isGoogle {
			key, err := m.resolveGoogleAPIKey(requestCtx)
			if err != nil {
				stream.Push(ai.AssistantMessageEvent{Type: ai.EventError, Error: &ai.AssistantMessage{ErrorMessage: err.Error()}})
				return
			}
			apiKey = key
			modelName := strings.TrimPrefix(requestModel.ID, "models/")
			targetURL = fmt.Sprintf("%s/v1beta/models/%s:streamGenerateContent?alt=sse&key=%s", googleGenerativeLanguageURLBase, modelName, apiKey)
		} else {
			tok, projID, err := m.embeddedGoogleAuth(requestCtx)
			if err != nil {
				stream.Push(ai.AssistantMessageEvent{Type: ai.EventError, Error: &ai.AssistantMessage{ErrorMessage: err.Error()}})
				return
			}
			token = tok
			projectID = projID
			authHeader = "Bearer " + token
			targetURL = googleGenerateContentURL
		}

		wireModel := antigravityWireModel(requestModel.ID)

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

		var payloadBytes []byte
		var err error
		var envelope map[string]any

		if isGoogle {
			payloadBytes, err = json.Marshal(reqBody)
		} else {
			envelope = map[string]any{
				"project": projectID,
				"model":   wireModel,
				"request": reqBody,
			}
			payloadBytes, err = json.Marshal(envelope)
		}
		if err != nil {
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventError, Error: &ai.AssistantMessage{ErrorMessage: err.Error()}})
			return
		}

		httpReq, err := http.NewRequestWithContext(requestCtx, http.MethodPost, targetURL, bytes.NewReader(payloadBytes))
		if err != nil {
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventError, Error: &ai.AssistantMessage{ErrorMessage: err.Error()}})
			return
		}

		if authHeader != "" {
			httpReq.Header.Set("Authorization", authHeader)
		}
		httpReq.Header.Set("Content-Type", "application/json")
		if !isGoogle {
			httpReq.Header.Set("User-Agent", "antigravity/cli/1.2.4 (aidev_client; os_type=linux; arch=amd64; cl=982146307; auth_method=consumer)")
		}

		client := &http.Client{Timeout: 120 * time.Second}
		resp, err := client.Do(httpReq)
		if err != nil {
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventError, Error: &ai.AssistantMessage{ErrorMessage: err.Error()}})
			return
		}

		if !isGoogle && resp.StatusCode == http.StatusUnauthorized {
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
		} else if !isGoogle && (resp.StatusCode == http.StatusServiceUnavailable || resp.StatusCode == http.StatusTooManyRequests) {
			_ = resp.Body.Close()
			time.Sleep(1500 * time.Millisecond)
			httpReq, err = http.NewRequestWithContext(requestCtx, http.MethodPost, googleGenerateContentURL, bytes.NewReader(payloadBytes))
			if err == nil {
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
			providerPrefix := "Antigravity"
			if isGoogle {
				providerPrefix = "Google Gemini"
			}
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventError, Error: &ai.AssistantMessage{ErrorMessage: fmt.Sprintf("%s API error %d: %s", providerPrefix, resp.StatusCode, errBody.String())}})
			return
		}

		stream.Push(ai.AssistantMessageEvent{Type: ai.EventStart})

		_, _ = scanGeminiSSEStream(resp.Body, m.nextToolCallID, stream)
	}()

	return stream
}
