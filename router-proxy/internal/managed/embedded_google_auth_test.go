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

	"github.com/sky-valley/pi/ai"
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
	// Turn 2: model "[Invoked read]"
	// Turn 3: user "[Observation from read:\nworld content]" merged with user "Now tell me what it said"
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
	if len(parts2) != 1 {
		t.Fatalf("turn 2 expected 1 part, got %d", len(parts2))
	}
	part2Map := parts2[0].(map[string]any)
	if part2Map["text"] != "[Invoked read]" {
		t.Errorf("turn 2 expected text [Invoked read], got %v", part2Map["text"])
	}
	if _, hasFC := part2Map["functionCall"]; hasFC {
		t.Errorf("turn 2 should NOT contain functionCall, got %+v", part2Map)
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
	if !strings.Contains(part3_1["text"].(string), "[Observation from read:\nworld content]") {
		t.Errorf("turn 3 part 1 expected observation text, got %v", part3_1["text"])
	}
	if _, hasFR := part3_1["functionResponse"]; hasFR {
		t.Errorf("turn 3 part 1 should NOT contain functionResponse, got %+v", part3_1)
	}
	part3_2 := parts3[1].(map[string]any)
	if part3_2["text"] != "Now tell me what it said" {
		t.Errorf("turn 3 part 2 expected text, got %v", part3_2["text"])
	}
}
