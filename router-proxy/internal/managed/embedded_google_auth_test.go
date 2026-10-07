package managed

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

func TestGoogleAuth_ExpiredTokenAutoRefresh(t *testing.T) {
	origURL := googleTokenURL
	defer func() { googleTokenURL = origURL }()

	var refreshCount int64
	mockOAuth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&refreshCount, 1)
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if err := r.ParseForm(); err != nil {
			t.Errorf("failed to parse form: %v", err)
		}
		if r.FormValue("client_id") != googleClientID {
			t.Errorf("unexpected client_id: %s", r.FormValue("client_id"))
		}
		if r.FormValue("client_secret") != googleClientSecret {
			t.Errorf("unexpected client_secret: %s", r.FormValue("client_secret"))
		}
		if r.FormValue("refresh_token") != "test-refresh-1" {
			t.Errorf("unexpected refresh_token: %s", r.FormValue("refresh_token"))
		}
		if r.FormValue("grant_type") != "refresh_token" {
			t.Errorf("unexpected grant_type: %s", r.FormValue("grant_type"))
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "fresh-access-token-123",
			"expires_in":    3600,
			"token_type":    "Bearer",
			"refresh_token": "next-refresh-token-456",
		})
	}))
	defer mockOAuth.Close()

	googleTokenURL = mockOAuth.URL

	tempDir := t.TempDir()
	authPath := filepath.Join(tempDir, "auth.json")
	initialAuth := map[string]any{
		"antigravity": map[string]any{
			"access":    "expired-access-token",
			"refresh":   "test-refresh-1",
			"expires":   int64(1000), // long expired
			"projectId": "test-project-alpha",
			"type":      "oauth",
		},
		"openai": map[string]any{
			"access":      "openai-secret-token",
			"customKey":   "customValue",
			"preservedOk": true,
		},
	}
	initialBytes, err := json.Marshal(initialAuth)
	if err != nil {
		t.Fatalf("failed to marshal initial auth: %v", err)
	}
	if err := os.WriteFile(authPath, initialBytes, 0600); err != nil {
		t.Fatalf("failed to write initial auth: %v", err)
	}

	m := &Manager{config: Config{PiAgentDir: tempDir}}
	token, projectID, err := m.embeddedGoogleAuth(context.Background())
	if err != nil {
		t.Fatalf("embeddedGoogleAuth failed: %v", err)
	}

	if token != "fresh-access-token-123" {
		t.Errorf("expected fresh-access-token-123, got %s", token)
	}
	if projectID != "test-project-alpha" {
		t.Errorf("expected test-project-alpha, got %s", projectID)
	}
	if atomic.LoadInt64(&refreshCount) != 1 {
		t.Errorf("expected 1 refresh call, got %d", refreshCount)
	}

	// Verify auth.json content and permissions
	info, err := os.Stat(authPath)
	if err != nil {
		t.Fatalf("failed to stat auth.json: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("expected file mode 0600, got %o", perm)
	}

	updatedData, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatalf("failed to read updated auth.json: %v", err)
	}
	var updatedAuth map[string]any
	if err := json.Unmarshal(updatedData, &updatedAuth); err != nil {
		t.Fatalf("failed to unmarshal updated auth.json: %v", err)
	}

	anti := updatedAuth["antigravity"].(map[string]any)
	if anti["access"] != "fresh-access-token-123" {
		t.Errorf("expected updated access token, got %v", anti["access"])
	}
	if anti["refresh"] != "next-refresh-token-456" {
		t.Errorf("expected updated refresh token, got %v", anti["refresh"])
	}
	expVal, _ := anti["expires"].(float64)
	if int64(expVal) <= time.Now().UnixMilli() {
		t.Errorf("expected future expiry, got %v", expVal)
	}

	// Verify non-antigravity keys are completely preserved
	openai, ok := updatedAuth["openai"].(map[string]any)
	if !ok {
		t.Fatalf("openai section missing or not a map: %v", updatedAuth["openai"])
	}
	if openai["access"] != "openai-secret-token" {
		t.Errorf("openai.access modified: %v", openai["access"])
	}
	if openai["customKey"] != "customValue" {
		t.Errorf("openai.customKey modified: %v", openai["customKey"])
	}
	if openai["preservedOk"] != true {
		t.Errorf("openai.preservedOk modified: %v", openai["preservedOk"])
	}
}

func TestGoogleAuth_WithinSixtySecondsRefresh(t *testing.T) {
	origURL := googleTokenURL
	defer func() { googleTokenURL = origURL }()

	var refreshCount int64
	mockOAuth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&refreshCount, 1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "token-renewed-within-window",
			"expires_in":   3600,
		})
	}))
	defer mockOAuth.Close()

	googleTokenURL = mockOAuth.URL

	tempDir := t.TempDir()
	authPath := filepath.Join(tempDir, "auth.json")
	initialAuth := map[string]any{
		"antigravity": map[string]any{
			"access":    "expiring-soon-token",
			"refresh":   "refresh-token-soon",
			"expires":   time.Now().UnixMilli() + 30*1000, // Expires in 30s (<= 60s)
			"projectId": "test-project-soon",
		},
	}
	data, _ := json.Marshal(initialAuth)
	_ = os.WriteFile(authPath, data, 0600)

	m := &Manager{config: Config{PiAgentDir: tempDir}}
	token, _, err := m.embeddedGoogleAuth(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if token != "token-renewed-within-window" {
		t.Errorf("expected token-renewed-within-window, got %s", token)
	}
	if atomic.LoadInt64(&refreshCount) != 1 {
		t.Errorf("expected 1 refresh call, got %d", refreshCount)
	}
}

func TestGoogleAuth_ValidTokenNoRefresh(t *testing.T) {
	origURL := googleTokenURL
	defer func() { googleTokenURL = origURL }()

	var refreshCount int64
	mockOAuth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&refreshCount, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer mockOAuth.Close()

	googleTokenURL = mockOAuth.URL

	tempDir := t.TempDir()
	authPath := filepath.Join(tempDir, "auth.json")
	initialAuth := map[string]any{
		"antigravity": map[string]any{
			"access":    "valid-long-lived-token",
			"refresh":   "refresh-token",
			"expires":   time.Now().UnixMilli() + 3600*1000, // Expires in 1h
			"projectId": "test-project-valid",
		},
	}
	data, _ := json.Marshal(initialAuth)
	_ = os.WriteFile(authPath, data, 0600)

	m := &Manager{config: Config{PiAgentDir: tempDir}}
	token, projectID, err := m.embeddedGoogleAuth(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if token != "valid-long-lived-token" {
		t.Errorf("expected valid-long-lived-token, got %s", token)
	}
	if projectID != "test-project-valid" {
		t.Errorf("expected test-project-valid, got %s", projectID)
	}
	if atomic.LoadInt64(&refreshCount) != 0 {
		t.Errorf("expected 0 refresh calls, got %d", refreshCount)
	}
}

func TestGoogleAuth_StreamRetry401(t *testing.T) {
	origTokenURL := googleTokenURL
	origContentURL := googleGenerateContentURL
	defer func() {
		googleTokenURL = origTokenURL
		googleGenerateContentURL = origContentURL
	}()

	var refreshCount int64
	mockOAuth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&refreshCount, 1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "token-after-401-refresh",
			"expires_in":   3600,
		})
	}))
	defer mockOAuth.Close()
	googleTokenURL = mockOAuth.URL

	var apiAttempts int64
	mockAntigravityAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt := atomic.AddInt64(&apiAttempts, 1)
		authHeader := r.Header.Get("Authorization")
		if attempt == 1 {
			if authHeader != "Bearer initial-stale-token" {
				t.Errorf("first attempt expected 'Bearer initial-stale-token', got %s", authHeader)
			}
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error": {"code": 401, "message": "Request had invalid authentication credentials."}}`))
			return
		}

		if authHeader != "Bearer token-after-401-refresh" {
			t.Errorf("second attempt expected 'Bearer token-after-401-refresh', got %s", authHeader)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"Hello from Antigravity after retry!\"}],\"role\":\"model\"}}]}}\n\n"))
	}))
	defer mockAntigravityAPI.Close()
	googleGenerateContentURL = mockAntigravityAPI.URL

	tempDir := t.TempDir()
	authPath := filepath.Join(tempDir, "auth.json")
	initialAuth := map[string]any{
		"antigravity": map[string]any{
			"access":    "initial-stale-token",
			"refresh":   "test-refresh-token",
			"expires":   time.Now().UnixMilli() + 3600*1000,
			"projectId": "test-project-401",
		},
	}
	data, _ := json.Marshal(initialAuth)
	_ = os.WriteFile(authPath, data, 0600)

	m := &Manager{config: Config{PiAgentDir: tempDir}}
	transcript := ai.TranscriptContext{
		Messages: []ai.Message{
			ai.UserMessage{
				Content: []ai.Content{
					ai.TextContent{Text: "Test message"},
				},
			},
		},
	}

	stream := m.streamEmbeddedGoogle(context.Background(), &ai.Model{ID: "gemini-3.8-flash", Provider: "antigravity"}, transcript, nil)

	var receivedText strings.Builder
	var streamError error
	for event := range stream.Events() {
		if event.Type == ai.EventTextDelta {
			receivedText.WriteString(event.Delta)
		} else if event.Type == ai.EventError {
			if event.Error != nil {
				streamError = fmt.Errorf("%s", event.Error.ErrorMessage)
			} else {
				streamError = fmt.Errorf("unknown stream error")
			}
		}
	}

	if streamError != nil {
		t.Fatalf("stream encountered error: %v", streamError)
	}

	if atomic.LoadInt64(&apiAttempts) != 2 {
		t.Errorf("expected 2 API attempts, got %d", apiAttempts)
	}
	if atomic.LoadInt64(&refreshCount) != 1 {
		t.Errorf("expected 1 OAuth refresh call, got %d", refreshCount)
	}
	if !strings.Contains(receivedText.String(), "Hello from Antigravity after retry!") {
		t.Errorf("expected response text, got %q", receivedText.String())
	}
}

func TestGoogleAuth_StreamDroppedToolCallsConversion(t *testing.T) {
	origTokenURL := googleTokenURL
	origContentURL := googleGenerateContentURL
	defer func() {
		googleTokenURL = origTokenURL
		googleGenerateContentURL = origContentURL
	}()

	mockOAuth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "token-test",
			"expires_in":   3600,
		})
	}))
	defer mockOAuth.Close()
	googleTokenURL = mockOAuth.URL

	var capturedReqBody map[string]any
	mockAntigravityAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		var envelope map[string]any
		_ = json.Unmarshal(bodyBytes, &envelope)
		if req, ok := envelope["request"].(map[string]any); ok {
			capturedReqBody = req
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"All done\"}],\"role\":\"model\"}}]}}\n\n"))
	}))
	defer mockAntigravityAPI.Close()
	googleGenerateContentURL = mockAntigravityAPI.URL

	tempDir := t.TempDir()
	authPath := filepath.Join(tempDir, "auth.json")
	initialAuth := map[string]any{
		"antigravity": map[string]any{
			"access":    "test-access-token",
			"refresh":   "test-refresh-token",
			"expires":   time.Now().UnixMilli() + 3600*1000,
			"projectId": "test-project-123",
		},
	}
	data, _ := json.Marshal(initialAuth)
	_ = os.WriteFile(authPath, data, 0600)

	m := &Manager{config: Config{PiAgentDir: tempDir}}
	transcript := ai.TranscriptContext{
		Messages: []ai.Message{
			ai.UserMessage{
				Content: []ai.Content{
					ai.TextContent{Text: "Please read hello.txt"},
				},
			},
			ai.AssistantMessage{
				Content: []ai.Content{
					ai.TextContent{Text: "I will check the file."},
					ai.ToolCall{
						ID:   "call-read-1",
						Name: "read",
						Arguments: map[string]any{
							"path": "hello.txt",
						},
					},
				},
			},
			ai.ToolResultMessage{
				ToolCallID: "call-read-1",
				ToolName:   "read",
				Content: []ai.Content{
					ai.TextContent{Text: "world content"},
				},
			},
			ai.UserMessage{
				Content: []ai.Content{
					ai.TextContent{Text: "Now tell me what it said"},
				},
			},
		},
	}

	stream := m.streamEmbeddedGoogle(context.Background(), &ai.Model{ID: "gemini-3.8-flash", Provider: "antigravity"}, transcript, nil)

	for event := range stream.Events() {
		if event.Type == ai.EventError {
			t.Fatalf("stream encountered error: %v", event.Error)
		}
	}

	if capturedReqBody == nil {
		t.Fatal("expected capturedReqBody, got nil")
	}

	contents, ok := capturedReqBody["contents"].([]any)
	if !ok {
		t.Fatalf("expected contents slice, got %T", capturedReqBody["contents"])
	}

	// Because:
	// Turn 1: user "Please read hello.txt"
	// Turn 2: model "I will check the file." (no [Invoked read]!)
	// Turn 3: user tool response with functionResponse merged with user "Now tell me what it said"
	// There should be exactly 3 turns alternating user -> model -> user!
	if len(contents) != 3 {
		t.Fatalf("expected 3 turns after merging consecutive user turns, got %d: %+v", len(contents), contents)
	}

	turn1 := contents[0].(map[string]any)
	if turn1["role"] != "user" {
		t.Errorf("turn 1 role expected user, got %v", turn1["role"])
	}

	turn2 := contents[1].(map[string]any)
	if turn2["role"] != "model" {
		t.Errorf("turn 2 role expected model, got %v", turn2["role"])
	}
	parts2 := turn2["parts"].([]any)
	if len(parts2) != 2 {
		t.Fatalf("turn 2 expected 2 parts (text + functionCall), got %d", len(parts2))
	}
	part2Map := parts2[0].(map[string]any)
	if part2Map["text"] != "I will check the file." {
		t.Errorf("turn 2 expected text 'I will check the file.', got %v", part2Map["text"])
	}
	if strings.Contains(part2Map["text"].(string), "[Invoked") {
		t.Errorf("turn 2 must NOT contain [Invoked], got %v", part2Map["text"])
	}
	part2FCMap := parts2[1].(map[string]any)
	fc, hasFC := part2FCMap["functionCall"].(map[string]any)
	if !hasFC {
		t.Errorf("turn 2 part 2 must contain functionCall, got %+v", part2FCMap)
	} else if fc["name"] != "read" {
		t.Errorf("turn 2 functionCall expected name 'read', got %v", fc["name"])
	}

	turn3 := contents[2].(map[string]any)
	if turn3["role"] != "user" {
		t.Errorf("turn 3 role expected user, got %v", turn3["role"])
	}
	parts3 := turn3["parts"].([]any)
	if len(parts3) != 2 {
		t.Fatalf("turn 3 expected 2 merged parts, got %d: %+v", len(parts3), parts3)
	}
	part3_1 := parts3[0].(map[string]any)
	fr, ok := part3_1["functionResponse"].(map[string]any)
	if !ok {
		t.Fatalf("turn 3 part 1 expected functionResponse, got %+v", part3_1)
	}
	if fr["name"] != "read" {
		t.Errorf("expected functionResponse name 'read', got %v", fr["name"])
	}
	if resp, ok := fr["response"].(map[string]any); !ok || resp["output"] != "world content" {
		t.Errorf("expected output 'world content', got %v", fr["response"])
	}
	if strings.Contains(fmt.Sprintf("%v", part3_1), "[Observation from") {
		t.Errorf("turn 3 part 1 must NOT contain [Observation from], got %v", part3_1)
	}
	if strings.Contains(fmt.Sprintf("%v", part3_1), "[Invoked") {
		t.Errorf("turn 3 part 1 must NOT contain [Invoked], got %v", part3_1)
	}
	part3_2 := parts3[1].(map[string]any)
	if part3_2["text"] != "Now tell me what it said" {
		t.Errorf("turn 3 part 2 expected text, got %v", part3_2["text"])
	}

	// Verify model parts never contain [Invoked ...] across any turn
	for i, turnAny := range contents {
		turnMap := turnAny.(map[string]any)
		if turnMap["role"] == "model" {
			for j, pAny := range turnMap["parts"].([]any) {
				pMap := pAny.(map[string]any)
				if txt, ok := pMap["text"].(string); ok && strings.Contains(txt, "[Invoked") {
					t.Errorf("model turn %d part %d must NOT contain [Invoked], got: %s", i, j, txt)
				}
			}
		}
	}
}

func TestGoogleAuth_StreamDroppedToolCallsOnlyToolCallNoText(t *testing.T) {
	origTokenURL := googleTokenURL
	origContentURL := googleGenerateContentURL
	defer func() {
		googleTokenURL = origTokenURL
		googleGenerateContentURL = origContentURL
	}()

	mockOAuth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "token-test",
			"expires_in":   3600,
		})
	}))
	defer mockOAuth.Close()
	googleTokenURL = mockOAuth.URL

	var capturedReqBody map[string]any
	mockAntigravityAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		var envelope map[string]any
		_ = json.Unmarshal(bodyBytes, &envelope)
		if req, ok := envelope["request"].(map[string]any); ok {
			capturedReqBody = req
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"Final answer\"}],\"role\":\"model\"}}]}}\n\n"))
	}))
	defer mockAntigravityAPI.Close()
	googleGenerateContentURL = mockAntigravityAPI.URL

	tempDir := t.TempDir()
	authPath := filepath.Join(tempDir, "auth.json")
	initialAuth := map[string]any{
		"antigravity": map[string]any{
			"access":    "test-access-token",
			"refresh":   "test-refresh-token",
			"expires":   time.Now().UnixMilli() + 3600*1000,
			"projectId": "test-project-123",
		},
	}
	data, _ := json.Marshal(initialAuth)
	_ = os.WriteFile(authPath, data, 0600)

	m := &Manager{config: Config{PiAgentDir: tempDir}}
	// Assistant message has ONLY a ToolCall, NO TextContent at all
	transcript := ai.TranscriptContext{
		Messages: []ai.Message{
			ai.UserMessage{
				Content: []ai.Content{
					ai.TextContent{Text: "Please read hello.txt"},
				},
			},
			ai.AssistantMessage{
				Content: []ai.Content{
					ai.ToolCall{
						ID:   "call-read-1",
						Name: "read",
						Arguments: map[string]any{
							"path": "hello.txt",
						},
					},
				},
			},
			ai.ToolResultMessage{
				ToolCallID: "call-read-1",
				ToolName:   "read",
				Content: []ai.Content{
					ai.TextContent{Text: "world content"},
				},
			},
			ai.UserMessage{
				Content: []ai.Content{
					ai.TextContent{Text: "Now tell me what it said"},
				},
			},
		},
	}

	stream := m.streamEmbeddedGoogle(context.Background(), &ai.Model{ID: "gemini-3.8-flash", Provider: "antigravity"}, transcript, nil)

	for event := range stream.Events() {
		if event.Type == ai.EventError {
			t.Fatalf("stream encountered error: %v", event.Error)
		}
	}

	if capturedReqBody == nil {
		t.Fatal("expected capturedReqBody, got nil")
	}

	contents, ok := capturedReqBody["contents"].([]any)
	if !ok {
		t.Fatalf("expected contents slice, got %T", capturedReqBody["contents"])
	}

	// Assistant turn had NO text, so no model turn is produced.
	// Consecutive user turns (original user message + observation + follow-up user message) merge into 1 user turn.
	for i, turnAny := range contents {
		turnMap := turnAny.(map[string]any)
		if turnMap["role"] == "model" {
			for j, pAny := range turnMap["parts"].([]any) {
				pMap := pAny.(map[string]any)
				if txt, ok := pMap["text"].(string); ok && strings.Contains(txt, "[Invoked") {
					t.Errorf("model turn %d part %d must NOT contain [Invoked], got: %s", i, j, txt)
				}
			}
		}
	}

	if len(contents) != 3 {
		t.Fatalf("expected 3 alternating turns, got %d: %+v", len(contents), contents)
	}
	userTurn1 := contents[0].(map[string]any)
	if userTurn1["role"] != "user" {
		t.Fatalf("expected turn 1 role 'user', got %v", userTurn1["role"])
	}
	modelTurn := contents[1].(map[string]any)
	if modelTurn["role"] != "model" {
		t.Fatalf("expected turn 2 role 'model', got %v", modelTurn["role"])
	}
	modelParts := modelTurn["parts"].([]any)
	if len(modelParts) != 1 {
		t.Fatalf("expected 1 part in model turn, got %d", len(modelParts))
	}
	fc, ok := modelParts[0].(map[string]any)["functionCall"].(map[string]any)
	if !ok || fc["name"] != "read" {
		t.Fatalf("expected functionCall for 'read', got %+v", modelParts[0])
	}
	userTurn2 := contents[2].(map[string]any)
	if userTurn2["role"] != "user" {
		t.Fatalf("expected turn 3 role 'user', got %v", userTurn2["role"])
	}
}

func TestGoogleAuth_StreamUniqueToolCallIDsForDuplicateToolNames(t *testing.T) {
	origURL := googleTokenURL
	defer func() { googleTokenURL = origURL }()
	origGenURL := googleGenerateContentURL
	defer func() { googleGenerateContentURL = origGenURL }()

	mockOAuth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "test-token",
			"expires_in":   3600,
			"token_type":   "Bearer",
		})
	}))
	defer mockOAuth.Close()
	googleTokenURL = mockOAuth.URL

	mockAntigravityAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		// SSE stream emitting multiple FunctionCall parts with the same tool name "read"
		sseChunk := "data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"functionCall\":{\"name\":\"read\",\"args\":{\"path\":\"file1.txt\"}}},{\"functionCall\":{\"name\":\"read\",\"args\":{\"path\":\"file2.txt\"}}}],\"role\":\"model\"}}]}}\n\n"
		_, _ = w.Write([]byte(sseChunk))
	}))
	defer mockAntigravityAPI.Close()
	googleGenerateContentURL = mockAntigravityAPI.URL

	tempDir := t.TempDir()
	authPath := filepath.Join(tempDir, "auth.json")
	initialAuth := map[string]any{
		"antigravity": map[string]any{
			"access":    "test-access-token",
			"refresh":   "test-refresh-token",
			"expires":   time.Now().UnixMilli() + 3600*1000,
			"projectId": "test-project-123",
		},
	}
	data, _ := json.Marshal(initialAuth)
	_ = os.WriteFile(authPath, data, 0600)

	m := &Manager{
		config: Config{PiAgentDir: tempDir},
		ids:    managerIDs{},
	}
	transcript := ai.TranscriptContext{
		Messages: []ai.Message{
			ai.UserMessage{
				Content: []ai.Content{
					ai.TextContent{Text: "Read both files"},
				},
			},
		},
	}

	stream := m.streamEmbeddedGoogle(context.Background(), &ai.Model{ID: "gemini-3.8-flash", Provider: "antigravity"}, transcript, nil)

	var toolCalls []ai.ToolCall
	for event := range stream.Events() {
		if event.Type == ai.EventError {
			t.Fatalf("stream encountered error: %v", event.Error)
		}
		if event.Type == ai.EventDone && event.Message != nil {
			for _, c := range event.Message.Content {
				if tc, ok := c.(ai.ToolCall); ok {
					toolCalls = append(toolCalls, tc)
				}
			}
		}
	}

	if len(toolCalls) != 2 {
		t.Fatalf("expected 2 tool calls, got %d: %+v", len(toolCalls), toolCalls)
	}

	for i, tc := range toolCalls {
		if tc.Name != "read" {
			t.Errorf("tool call %d name expected 'read', got %q", i, tc.Name)
		}
		if tc.ID == "read" {
			t.Errorf("tool call %d ID must not be equal to 'read', got %q", i, tc.ID)
		}
		if !strings.HasPrefix(tc.ID, "call_") {
			t.Errorf("tool call %d ID expected prefix 'call_', got %q", i, tc.ID)
		}
	}

	if toolCalls[0].ID == toolCalls[1].ID {
		t.Fatalf("tool call IDs must be unique, both were %q", toolCalls[0].ID)
	}
}

func TestGoogleAuth_MultiTurnRepeatedToolEffects(t *testing.T) {
	origURL := googleTokenURL
	defer func() { googleTokenURL = origURL }()
	origGenURL := googleGenerateContentURL
	defer func() { googleGenerateContentURL = origGenURL }()

	mockOAuth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "test-token",
			"expires_in":   3600,
			"token_type":   "Bearer",
		})
	}))
	defer mockOAuth.Close()
	googleTokenURL = mockOAuth.URL

	turn := 0
	mockAntigravityAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		turn++
		// Turn 1 calls read for a.txt, Turn 2 calls read for b.txt
		path := "a.txt"
		if turn > 1 {
			path = "b.txt"
		}
		sseChunk := fmt.Sprintf("data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"functionCall\":{\"name\":\"read\",\"args\":{\"path\":%q}}}],\"role\":\"model\"}}]}}\n\n", path)
		_, _ = w.Write([]byte(sseChunk))
	}))
	defer mockAntigravityAPI.Close()
	googleGenerateContentURL = mockAntigravityAPI.URL

	tempDir := t.TempDir()
	authPath := filepath.Join(tempDir, "auth.json")
	initialAuth := map[string]any{
		"antigravity": map[string]any{
			"access":    "test-access-token",
			"refresh":   "test-refresh-token",
			"expires":   time.Now().UnixMilli() + 3600*1000,
			"projectId": "test-project-123",
		},
	}
	data, _ := json.Marshal(initialAuth)
	_ = os.WriteFile(authPath, data, 0600)

	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(tempDir, "router.db"), store.Options{})
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	defer db.Close()

	agent := store.ManagedAgentRecord{
		AgentID:           "test-agent",
		Name:              "test-agent",
		Cwd:               tempDir,
		SelectedModel:     store.ManagedModel{Provider: "antigravity", ID: "gemini-3.8-flash"},
		Runtime:           "embedded",
		PiSessionID:       "test-session",
		SessionEpoch:      1,
		RuntimeGeneration: "test-gen",
		ManifestRevision:  1,
		ManifestHash:      strings.Repeat("a", 64),
	}
	op := store.ManagedOperation{
		OperationID:       "test-launch",
		Method:            "POST",
		CanonicalPath:     "/v1/managed-agents",
		RequestID:         "test-launch",
		CanonicalBodyHash: "hash",
		ResponseStatus:    202,
		ResponseBody:      []byte(`{}`),
	}
	if err := db.ReserveManagedLaunch(ctx, agent, op); err != nil {
		t.Fatalf("failed to reserve managed launch: %v", err)
	}

	binding := store.EmbeddedBinding{
		AgentID:    "test-agent",
		SessionID:  "test-session",
		Epoch:      1,
		Generation: "test-gen",
	}
	if err := db.BindEmbeddedSession(ctx, binding, "worker"); err != nil {
		t.Fatalf("failed to bind embedded session: %v", err)
	}
	token, err := db.AcquireEmbeddedOwner(ctx, binding, "test-owner", time.Minute)
	if err != nil {
		t.Fatalf("failed to acquire embedded owner: %v", err)
	}
	binding.OwnerToken = token

	m := &Manager{
		config: Config{PiAgentDir: tempDir},
		ids:    managerIDs{},
		store:  db,
	}

	// First turn stream
	stream1 := m.streamEmbeddedGoogle(ctx, &ai.Model{ID: "gemini-3.8-flash", Provider: "antigravity"}, ai.TranscriptContext{}, nil)
	var call1 ai.ToolCall
	for event := range stream1.Events() {
		if event.Type == ai.EventDone && event.Message != nil {
			for _, c := range event.Message.Content {
				if tc, ok := c.(ai.ToolCall); ok {
					call1 = tc
				}
			}
		}
	}
	if call1.ID == "" || call1.ID == "read" {
		t.Fatalf("invalid tool call 1 ID: %q", call1.ID)
	}

	// Second turn stream
	stream2 := m.streamEmbeddedGoogle(ctx, &ai.Model{ID: "gemini-3.8-flash", Provider: "antigravity"}, ai.TranscriptContext{}, nil)
	var call2 ai.ToolCall
	for event := range stream2.Events() {
		if event.Type == ai.EventDone && event.Message != nil {
			for _, c := range event.Message.Content {
				if tc, ok := c.(ai.ToolCall); ok {
					call2 = tc
				}
			}
		}
	}
	if call2.ID == "" || call2.ID == "read" {
		t.Fatalf("invalid tool call 2 ID: %q", call2.ID)
	}

	if call1.ID == call2.ID {
		t.Fatalf("sequential tool calls must have different IDs, got %q for both", call1.ID)
	}

	// Test recording effect intents sequentially in embedded_effects table
	intent1, err := json.Marshal(call1)
	if err != nil {
		t.Fatalf("failed to marshal call 1: %v", err)
	}
	if err := db.RecordEmbeddedEffectIntent(ctx, binding, call1.ID, intent1); err != nil {
		t.Fatalf("failed to record effect 1 intent: %v", err)
	}

	intent2, err := json.Marshal(call2)
	if err != nil {
		t.Fatalf("failed to marshal call 2: %v", err)
	}
	// This would fail with "UNIQUE constraint failed: embedded_effects.effect_id" if both call IDs were "read"
	if err := db.RecordEmbeddedEffectIntent(ctx, binding, call2.ID, intent2); err != nil {
		t.Fatalf("failed to record effect 2 intent (UNIQUE constraint violation?): %v", err)
	}
}

func TestGoogleAuth_ConvertMessagesToGoogleContents_UserImage(t *testing.T) {
	fakeData := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="
	messages := []ai.Message{
		ai.UserMessage{
			Content: []ai.Content{
				ai.TextContent{Text: "Look at this image:"},
				ai.ImageContent{
					MimeType: "image/png",
					Data:     fakeData,
				},
			},
		},
	}

	contents := convertMessagesToGoogleContents(messages)
	require.Len(t, contents, 1)
	turn := contents[0]
	require.Equal(t, "user", turn["role"])

	parts, ok := turn["parts"].([]any)
	require.True(t, ok)
	require.Len(t, parts, 2)

	textPart, ok := parts[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "Look at this image:", textPart["text"])

	imgPart, ok := parts[1].(map[string]any)
	require.True(t, ok)
	inlineData, ok := imgPart["inlineData"].(map[string]any)
	require.True(t, ok, "expected inlineData in image part")
	require.Equal(t, "image/png", inlineData["mimeType"])
	require.Equal(t, fakeData, inlineData["data"])
}

func TestGoogleAuth_ConvertMessagesToGoogleContents_ToolResultImage(t *testing.T) {
	fakeData := "/9j/4AAQSkZJRgABAQEASABIAAD/2wBD..."
	messages := []ai.Message{
		ai.UserMessage{
			Content: []ai.Content{
				ai.TextContent{Text: "Please capture screenshot"},
			},
		},
		ai.AssistantMessage{
			Content: []ai.Content{
				ai.ToolCall{
					ID:        "call-screen-1",
					Name:      "screenshot",
					Arguments: map[string]any{},
				},
			},
		},
		ai.ToolResultMessage{
			ToolCallID: "call-screen-1",
			ToolName:   "screenshot",
			Content: []ai.Content{
				ai.ImageContent{
					MimeType: "image/jpeg",
					Data:     fakeData,
				},
			},
		},
	}

	contents := convertMessagesToGoogleContents(messages)
	// Expecting 2 turns: user prompt -> user tool observation (since assistant only had toolCall, droppedToolCalls makes it observation)
	// Or alternating turns
	require.NotEmpty(t, contents)

	// Verify that the tool result part contains inlineData with the image
	foundImage := false
	for _, turn := range contents {
		if parts, ok := turn["parts"].([]any); ok {
			for _, part := range parts {
				if partMap, ok := part.(map[string]any); ok {
					if inlineData, ok := partMap["inlineData"].(map[string]any); ok {
						if inlineData["mimeType"] == "image/jpeg" && inlineData["data"] == fakeData {
							foundImage = true
						}
					}
				}
			}
		}
	}
	require.True(t, foundImage, "expected to find inlineData part with image/jpeg in contents")
}

func TestGoogleAuth_ConvertMessagesToGoogleContents_StandardFunctionResponseWithImage(t *testing.T) {
	fakeData := "R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7"
	messages := []ai.Message{
		ai.ToolResultMessage{
			ToolCallID: "call-read-img",
			ToolName:   "read",
			Content: []ai.Content{
				ai.ImageContent{
					MimeType: "image/gif",
					Data:     fakeData,
				},
			},
		},
	}

	// Not in droppedToolCalls, so it produces functionResponse + inlineData
	contents := convertMessagesToGoogleContents(messages)
	require.Len(t, contents, 1)
	turn := contents[0]
	require.Equal(t, "user", turn["role"])

	parts := turn["parts"].([]any)
	require.Len(t, parts, 2)

	frPart := parts[0].(map[string]any)
	fr, ok := frPart["functionResponse"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "read", fr["name"])

	imgPart := parts[1].(map[string]any)
	inlineData, ok := imgPart["inlineData"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "image/gif", inlineData["mimeType"])
	require.Equal(t, fakeData, inlineData["data"])
}

func TestGoogleAuth_ConvertMessagesToGoogleContents_FilterSimulatedObservations(t *testing.T) {
	simulatedSubagentText := "Observation: `subagent` ({\n\"description\": \"Design workflow states\",\n\"prompt\": \"Write files\"\n}):\nAgent started in background.\nAgent ID: subagent-workflows-123\nType: general-purpose\nDescription: Design workflow states\nYou will be notified when this agent completes.\nUse get_subagent_result to retrieve full results, or steer_subagent to send it messages.\nDo not duplicate this agent's work."

	messages := []ai.Message{
		ai.UserMessage{
			Content: []ai.Content{
				ai.TextContent{Text: "Please start the task."},
			},
		},
		ai.AssistantMessage{
			Content: []ai.Content{
				ai.TextContent{Text: simulatedSubagentText},
			},
		},
		ai.UserMessage{
			Content: []ai.Content{
				ai.TextContent{Text: "How is it going?"},
			},
		},
		ai.AssistantMessage{
			Content: []ai.Content{
				ai.TextContent{Text: "Observation: subagent (launching workflow states agent)\nI am actively working on the task now."},
			},
		},
	}

	contents := convertMessagesToGoogleContents(messages)

	// The first assistant message was entirely simulated subagent observation,
	// so its text is stripped completely and the assistant turn is removed.
	// The two user messages are merged into a single user turn.
	// The second assistant message has the observation lines stripped, leaving only the real response.
	require.Len(t, contents, 2)

	require.Equal(t, "user", contents[0]["role"])
	require.Equal(t, "model", contents[1]["role"])

	modelParts := contents[1]["parts"].([]any)
	require.Len(t, modelParts, 1)
	textPart := modelParts[0].(map[string]any)
	require.Equal(t, "I am actively working on the task now.", textPart["text"])
	require.NotContains(t, textPart["text"], "Observation:")
	require.NotContains(t, textPart["text"], "exit=0")
}

func TestGoogleAuth_ConvertMessagesToGoogleContents_EmptyAssistantPreservesAlternation(t *testing.T) {
	// Case 1: user -> empty assistant -> user continuation prompt
	// Empty assistant message must be omitted so the model never sees empty responses,
	// and consecutive user messages are merged into a single valid user turn.
	messages1 := []ai.Message{
		ai.UserMessage{
			Content: []ai.Content{
				ai.TextContent{Text: "Please start the task."},
			},
		},
		ai.AssistantMessage{
			Content: []ai.Content{},
		},
		ai.UserMessage{
			Content: []ai.Content{
				ai.TextContent{Text: "Continue working on the current goal."},
			},
		},
	}

	contents1 := convertMessagesToGoogleContents(messages1)
	// Must omit the empty assistant message and merge the user turns
	require.Len(t, contents1, 1)
	require.Equal(t, "user", contents1[0]["role"])
	parts1 := contents1[0]["parts"].([]any)
	require.Len(t, parts1, 2)
	require.Equal(t, "Please start the task.", parts1[0].(map[string]any)["text"])
	require.Equal(t, "Continue working on the current goal.", parts1[1].(map[string]any)["text"])

	// Case 2: tool response -> empty assistant -> user continuation prompt
	// Tool result (role user) and user prompt (role user) merge into a single user turn.
	messages2 := []ai.Message{
		ai.UserMessage{
			Content: []ai.Content{
				ai.TextContent{Text: "Initial instruction"},
			},
		},
		ai.AssistantMessage{
			Content: []ai.Content{
				ai.TextContent{Text: "Calling tool"},
			},
		},
		ai.ToolResultMessage{
			ToolCallID: "call-write-1",
			ToolName:   "write",
			Content: []ai.Content{
				ai.TextContent{Text: "Successfully wrote file"},
			},
		},
		ai.AssistantMessage{
			Content: []ai.Content{},
		},
		ai.UserMessage{
			Content: []ai.Content{
				ai.TextContent{Text: "Continue working on the current goal."},
			},
		},
	}

	contents2 := convertMessagesToGoogleContents(messages2)
	// Must produce 3 turns: user -> model ("Calling tool") -> user (tool result + prompt)
	require.Len(t, contents2, 3)
	require.Equal(t, "user", contents2[0]["role"])
	require.Equal(t, "model", contents2[1]["role"])
	require.Equal(t, "user", contents2[2]["role"])

	// Case 3: assistant with "(empty response)" text or whitespace-only is also omitted
	messages3 := []ai.Message{
		ai.UserMessage{
			Content: []ai.Content{
				ai.TextContent{Text: "Prompt 1"},
			},
		},
		ai.AssistantMessage{
			Content: []ai.Content{
				ai.TextContent{Text: "(empty response)"},
			},
		},
		ai.AssistantMessage{
			Content: []ai.Content{
				ai.TextContent{Text: "   \n\t  "},
			},
		},
		ai.UserMessage{
			Content: []ai.Content{
				ai.TextContent{Text: "Prompt 2"},
			},
		},
	}
	contents3 := convertMessagesToGoogleContents(messages3)
	require.Len(t, contents3, 1)
	require.Equal(t, "user", contents3[0]["role"])
}


func TestOpenAI_ImageConversion(t *testing.T) {
	fakePNG := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="
	img := ai.ImageContent{
		MimeType: "image/png",
		Data:     fakePNG,
	}

	block := ConvertImageContentToOpenAI(img)
	require.Equal(t, "image_url", block["type"])
	imgURL, ok := block["image_url"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "data:image/png;base64,"+fakePNG, imgURL["url"])

	messages := []ai.Message{
		ai.UserMessage{
			Content: []ai.Content{
				ai.TextContent{Text: "Inspect this:"},
				img,
			},
		},
		ai.ToolResultMessage{
			ToolCallID: "call-1",
			ToolName:   "read",
			Content: []ai.Content{
				img,
			},
		},
	}

	openaiContents := ConvertMessagesToOpenAIContents(messages)
	require.Len(t, openaiContents, 2)

	// User message content parts
	userContent := openaiContents[0]["content"].([]any)
	require.Len(t, userContent, 2)
	require.Equal(t, "text", userContent[0].(map[string]any)["type"])
	require.Equal(t, "image_url", userContent[1].(map[string]any)["type"])
	require.Equal(t, "data:image/png;base64,"+fakePNG, userContent[1].(map[string]any)["image_url"].(map[string]any)["url"])

	// Tool message content parts
	toolContent := openaiContents[1]["content"].([]any)
	require.Len(t, toolContent, 1)
	require.Equal(t, "image_url", toolContent[0].(map[string]any)["type"])
	require.Equal(t, "data:image/png;base64,"+fakePNG, toolContent[0].(map[string]any)["image_url"].(map[string]any)["url"])
}

func TestGoogleAuth_StreamThinkingTrace(t *testing.T) {
	origURL := googleGenerateContentURL
	defer func() { googleGenerateContentURL = origURL }()

	mockAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"Let me think about this.\",\"thought\":true}],\"role\":\"model\"}}]}}\n\n"))
		_, _ = w.Write([]byte("data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"Here is the answer.\"}],\"role\":\"model\"}}]}}\n\n"))
	}))
	defer mockAPI.Close()
	googleGenerateContentURL = mockAPI.URL

	tempDir := t.TempDir()
	authPath := filepath.Join(tempDir, "auth.json")
	initialAuth := map[string]any{
		"antigravity": map[string]any{
			"access":    "test-token",
			"refresh":   "test-refresh-token",
			"expires":   time.Now().UnixMilli() + 3600*1000,
			"projectId": "test-project",
		},
	}
	data, _ := json.Marshal(initialAuth)
	_ = os.WriteFile(authPath, data, 0600)

	m := &Manager{config: Config{PiAgentDir: tempDir}}
	transcript := ai.TranscriptContext{
		Messages: []ai.Message{
			ai.UserMessage{
				Content: []ai.Content{
					ai.TextContent{Text: "Question"},
				},
			},
		},
	}

	stream := m.streamEmbeddedGoogle(context.Background(), &ai.Model{ID: "gemini-3.8-flash", Provider: "antigravity"}, transcript, nil)

	var thinkingEvents []ai.AssistantMessageEvent
	var textEvents []ai.AssistantMessageEvent
	var finalDone *ai.AssistantMessage

	for event := range stream.Events() {
		switch event.Type {
		case ai.EventThinkingStart, ai.EventThinkingDelta, ai.EventThinkingEnd:
			thinkingEvents = append(thinkingEvents, event)
		case ai.EventTextStart, ai.EventTextDelta, ai.EventTextEnd:
			textEvents = append(textEvents, event)
		case ai.EventDone:
			finalDone = event.Message
		}
	}

	require.NotEmpty(t, thinkingEvents)
	var thinkingDelta strings.Builder
	for _, ev := range thinkingEvents {
		if ev.Type == ai.EventThinkingDelta {
			thinkingDelta.WriteString(ev.Delta)
		}
	}
	require.Equal(t, "Let me think about this.", thinkingDelta.String())

	require.NotEmpty(t, textEvents)
	var textDelta strings.Builder
	for _, ev := range textEvents {
		if ev.Type == ai.EventTextDelta {
			textDelta.WriteString(ev.Delta)
		}
	}
	require.Equal(t, "Here is the answer.", textDelta.String())

	require.NotNil(t, finalDone)
	require.Len(t, finalDone.Content, 2)
	th, isTh := finalDone.Content[0].(ai.ThinkingContent)
	require.True(t, isTh)
	require.Equal(t, "Let me think about this.", th.Thinking)
	txt, isTxt := finalDone.Content[1].(ai.TextContent)
	require.True(t, isTxt)
	require.Equal(t, "Here is the answer.", txt.Text)
}

func TestGoogleAuth_ConvertMessagesToGoogleContents_EmitsFunctionCall(t *testing.T) {
	messages := []ai.Message{
		ai.UserMessage{
			Content: []ai.Content{
				ai.TextContent{Text: "List directory contents"},
			},
		},
		ai.AssistantMessage{
			Content: []ai.Content{
				ai.TextContent{Text: "I will list the files in the directory."},
				ai.ToolCall{
					ID:   "call-bash-123",
					Name: "bash",
					Arguments: map[string]any{
						"command": "ls -la",
					},
				},
			},
		},
		ai.ToolResultMessage{
			ToolCallID: "call-bash-123",
			ToolName:   "bash",
			Content: []ai.Content{
				ai.TextContent{Text: "total 0\n-rw-r--r-- 1 test test 0 file.txt"},
			},
		},
	}

	contents := convertMessagesToGoogleContents(messages)
	require.Len(t, contents, 3)

	// Turn 0: user
	require.Equal(t, "user", contents[0]["role"])
	parts0 := contents[0]["parts"].([]any)
	require.Len(t, parts0, 1)
	require.Equal(t, "List directory contents", parts0[0].(map[string]any)["text"])

	// Turn 1: model with text and functionCall
	require.Equal(t, "model", contents[1]["role"])
	parts1 := contents[1]["parts"].([]any)
	require.Len(t, parts1, 2)
	require.Equal(t, "I will list the files in the directory.", parts1[0].(map[string]any)["text"])
	fcMap, ok := parts1[1].(map[string]any)["functionCall"].(map[string]any)
	require.True(t, ok, "expected functionCall in model parts")
	require.Equal(t, "bash", fcMap["name"])
	require.Equal(t, map[string]any{"command": "ls -la"}, fcMap["args"])

	// Turn 2: user with functionResponse matching the functionCall
	require.Equal(t, "user", contents[2]["role"])
	parts2 := contents[2]["parts"].([]any)
	require.Len(t, parts2, 1)
	frMap, ok := parts2[0].(map[string]any)["functionResponse"].(map[string]any)
	require.True(t, ok, "expected functionResponse in user parts")
	require.Equal(t, "bash", frMap["name"])
	respMap, ok := frMap["response"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "total 0\n-rw-r--r-- 1 test test 0 file.txt", respMap["output"])
}

func TestGoogleAuth_ConvertMessagesToGoogleContents_EmitsFunctionCall_ToolCallOnly(t *testing.T) {
	messages := []ai.Message{
		ai.UserMessage{
			Content: []ai.Content{
				ai.TextContent{Text: "Read config"},
			},
		},
		ai.AssistantMessage{
			Content: []ai.Content{
				&ai.ToolCall{
					ID:   "call-read-456",
					Name: "read",
					Arguments: map[string]any{
						"path": "config.json",
					},
				},
			},
		},
		ai.ToolResultMessage{
			ToolCallID: "call-read-456",
			ToolName:   "read",
			Content: []ai.Content{
				ai.TextContent{Text: "{\"debug\": true}"},
			},
		},
	}

	contents := convertMessagesToGoogleContents(messages)
	require.Len(t, contents, 3)

	require.Equal(t, "user", contents[0]["role"])
	require.Equal(t, "model", contents[1]["role"])
	require.Equal(t, "user", contents[2]["role"])

	modelParts := contents[1]["parts"].([]any)
	require.Len(t, modelParts, 1)
	fcMap, ok := modelParts[0].(map[string]any)["functionCall"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "read", fcMap["name"])
	require.Equal(t, map[string]any{"path": "config.json"}, fcMap["args"])

	userParts := contents[2]["parts"].([]any)
	require.Len(t, userParts, 1)
	frMap, ok := userParts[0].(map[string]any)["functionResponse"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "read", frMap["name"])
}

