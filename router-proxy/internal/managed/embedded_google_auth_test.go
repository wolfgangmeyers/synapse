package managed

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

func TestGoogleAuth_StreamDroppedToolCallsConversion(t *testing.T) {
	origContentURL := googleGenerativeLanguageURLBase
	defer func() {
		googleGenerativeLanguageURLBase = origContentURL
	}()

	t.Setenv("GEMINI_API_KEY", "test-api-key")

	var capturedReqBody map[string]any
	mockGeminiAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		var envelope map[string]any
		_ = json.Unmarshal(bodyBytes, &envelope)
		require.NotContains(t, envelope, "request")
		require.Equal(t, "test-api-key", r.URL.Query().Get("key"))
		capturedReqBody = envelope
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"All done\"}],\"role\":\"model\"}}]}}\n\n"))
	}))
	defer mockGeminiAPI.Close()
	googleGenerativeLanguageURLBase = mockGeminiAPI.URL

	tempDir := t.TempDir()
	authPath := filepath.Join(tempDir, "auth.json")
	initialAuth := map[string]any{"google": map[string]any{"key": "test-api-key"}}
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

	stream := m.streamEmbeddedGoogle(context.Background(), &ai.Model{ID: "gemini-3.8-flash", Provider: "google"}, transcript, nil)

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
	// Turn 3: user tool response with functionResponse
	// Turn 4: user text "Now tell me what it said" (distinct from turn 3 because Gemini rejects functionResponse + text in same user turn)
	// There should be exactly 4 turns: user -> model -> user (tool result) -> user (text)!
	if len(contents) != 4 {
		t.Fatalf("expected 4 turns after preserving separate functionResponse and text user turns, got %d: %+v", len(contents), contents)
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
	if len(parts3) != 1 {
		t.Fatalf("turn 3 expected 1 part (functionResponse), got %d: %+v", len(parts3), parts3)
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

	turn4 := contents[3].(map[string]any)
	if turn4["role"] != "user" {
		t.Errorf("turn 4 role expected user, got %v", turn4["role"])
	}
	parts4 := turn4["parts"].([]any)
	if len(parts4) != 1 {
		t.Fatalf("turn 4 expected 1 part (text), got %d: %+v", len(parts4), parts4)
	}
	part4_1 := parts4[0].(map[string]any)
	if part4_1["text"] != "Now tell me what it said" {
		t.Errorf("turn 4 part 1 expected text 'Now tell me what it said', got %v", part4_1["text"])
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
	origContentURL := googleGenerativeLanguageURLBase
	defer func() {
		googleGenerativeLanguageURLBase = origContentURL
	}()

	t.Setenv("GEMINI_API_KEY", "test-api-key")

	var capturedReqBody map[string]any
	mockGeminiAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		var envelope map[string]any
		_ = json.Unmarshal(bodyBytes, &envelope)
		require.NotContains(t, envelope, "request")
		require.Equal(t, "test-api-key", r.URL.Query().Get("key"))
		capturedReqBody = envelope
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"Final answer\"}],\"role\":\"model\"}}]}}\n\n"))
	}))
	defer mockGeminiAPI.Close()
	googleGenerativeLanguageURLBase = mockGeminiAPI.URL

	tempDir := t.TempDir()
	authPath := filepath.Join(tempDir, "auth.json")
	initialAuth := map[string]any{"google": map[string]any{"key": "test-api-key"}}
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

	stream := m.streamEmbeddedGoogle(context.Background(), &ai.Model{ID: "gemini-3.8-flash", Provider: "google"}, transcript, nil)

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

	// Assistant turn had NO text, but had a toolCall, so a model turn with functionCall is produced.
	// Tool response (functionResponse) and follow-up user text message are NOT merged into 1 user turn,
	// because Gemini requires functionResponse and user text to remain in separate turns.
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

	if len(contents) != 4 {
		t.Fatalf("expected 4 turns, got %d: %+v", len(contents), contents)
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
	userTurn3 := contents[3].(map[string]any)
	if userTurn3["role"] != "user" {
		t.Fatalf("expected turn 4 role 'user', got %v", userTurn3["role"])
	}
}

func TestGoogleAuth_StreamUniqueToolCallIDsForDuplicateToolNames(t *testing.T) {
	origGenURL := googleGenerativeLanguageURLBase
	defer func() { googleGenerativeLanguageURLBase = origGenURL }()

	t.Setenv("GEMINI_API_KEY", "test-api-key")

	mockGeminiAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		// SSE stream emitting multiple FunctionCall parts with the same tool name "read"
		sseChunk := "data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"functionCall\":{\"name\":\"read\",\"args\":{\"path\":\"file1.txt\"}}},{\"functionCall\":{\"name\":\"read\",\"args\":{\"path\":\"file2.txt\"}}}],\"role\":\"model\"}}]}}\n\n"
		_, _ = w.Write([]byte(sseChunk))
	}))
	defer mockGeminiAPI.Close()
	googleGenerativeLanguageURLBase = mockGeminiAPI.URL

	tempDir := t.TempDir()
	authPath := filepath.Join(tempDir, "auth.json")
	initialAuth := map[string]any{"google": map[string]any{"key": "test-api-key"}}
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

	stream := m.streamEmbeddedGoogle(context.Background(), &ai.Model{ID: "gemini-3.8-flash", Provider: "google"}, transcript, nil)

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

func TestGoogleAuth_StreamReasoningOff_ThinkingDisabled(t *testing.T) {
	origContentURL := googleGenerativeLanguageURLBase
	defer func() {
		googleGenerativeLanguageURLBase = origContentURL
	}()

	t.Setenv("GEMINI_API_KEY", "test-api-key")

	var capturedReqBody map[string]any
	mockGeminiAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		var envelope map[string]any
		_ = json.Unmarshal(bodyBytes, &envelope)
		require.NotContains(t, envelope, "request")
		require.Equal(t, "test-api-key", r.URL.Query().Get("key"))
		capturedReqBody = envelope
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"Response\"}],\"role\":\"model\"}}]}}\n\n"))
	}))
	defer mockGeminiAPI.Close()
	googleGenerativeLanguageURLBase = mockGeminiAPI.URL

	tempDir := t.TempDir()
	authPath := filepath.Join(tempDir, "auth.json")
	initialAuth := map[string]any{"google": map[string]any{"key": "test-api-key"}}
	data, _ := json.Marshal(initialAuth)
	_ = os.WriteFile(authPath, data, 0600)

	m := &Manager{config: Config{PiAgentDir: tempDir}}
	transcript := ai.TranscriptContext{
		Messages: []ai.Message{
			ai.UserMessage{
				Content: []ai.Content{
					ai.TextContent{Text: "Evaluate goal"},
				},
			},
		},
	}

	opts := &ai.SimpleStreamOptions{
		Reasoning: "off",
	}

	// 1. Test gemini-3.8-flash: thinkingLevel: "MINIMAL" and no includeThoughts
	capturedReqBody = nil
	stream38 := m.streamEmbeddedGoogle(context.Background(), &ai.Model{ID: "gemini-3.8-flash", Provider: "google"}, transcript, opts)
	for event := range stream38.Events() {
		if event.Type == ai.EventError {
			t.Fatalf("stream encountered error: %v", event.Error)
		}
	}
	require.NotNil(t, capturedReqBody)
	genConfig38, ok := capturedReqBody["generationConfig"].(map[string]any)
	require.True(t, ok)
	thinkingConfig38, ok := genConfig38["thinkingConfig"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "MINIMAL", thinkingConfig38["thinkingLevel"])
	_, hasInclude38 := thinkingConfig38["includeThoughts"]
	require.False(t, hasInclude38, "gemini-3.8-flash with reasoning off must not include includeThoughts")

	// 2. Test gemini-2.5-flash: thinkingBudget: 0 and no includeThoughts
	capturedReqBody = nil
	stream25 := m.streamEmbeddedGoogle(context.Background(), &ai.Model{ID: "gemini-2.5-flash", Provider: "google"}, transcript, opts)
	for event := range stream25.Events() {
		if event.Type == ai.EventError {
			t.Fatalf("stream encountered error: %v", event.Error)
		}
	}
	require.NotNil(t, capturedReqBody)
	genConfig25, ok := capturedReqBody["generationConfig"].(map[string]any)
	require.True(t, ok)
	thinkingConfig25, ok := genConfig25["thinkingConfig"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, float64(0), thinkingConfig25["thinkingBudget"])
	_, hasInclude25 := thinkingConfig25["includeThoughts"]
	require.False(t, hasInclude25, "gemini-2.5-flash with reasoning off must not include includeThoughts")

	// 3. Test gemini-4 models: thinkingLevel: "LOW" and no includeThoughts
	for _, gemini4ID := range []string{"gemini-4-argon", "gemini-4-carbon", "gemini-4-hydrogen"} {
		capturedReqBody = nil
		s := m.streamEmbeddedGoogle(context.Background(), &ai.Model{ID: gemini4ID, Provider: "google"}, transcript, opts)
		for event := range s.Events() {
			if event.Type == ai.EventError {
				t.Fatalf("stream encountered error: %v", event.Error)
			}
		}
		require.NotNil(t, capturedReqBody)
		genCfg, ok := capturedReqBody["generationConfig"].(map[string]any)
		require.True(t, ok)
		thkCfg, ok := genCfg["thinkingConfig"].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "LOW", thkCfg["thinkingLevel"], "expected LOW for %s", gemini4ID)
		_, hasInc := thkCfg["includeThoughts"]
		require.False(t, hasInc, "%s with reasoning off must not include includeThoughts", gemini4ID)
	}
}

func TestGoogleAuth_MultiTurnRepeatedToolEffects(t *testing.T) {
	origGenURL := googleGenerativeLanguageURLBase
	defer func() { googleGenerativeLanguageURLBase = origGenURL }()

	t.Setenv("GEMINI_API_KEY", "test-api-key")

	turn := 0
	mockGeminiAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	defer mockGeminiAPI.Close()
	googleGenerativeLanguageURLBase = mockGeminiAPI.URL

	tempDir := t.TempDir()
	authPath := filepath.Join(tempDir, "auth.json")
	initialAuth := map[string]any{"google": map[string]any{"key": "test-api-key"}}
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
		SelectedModel:     store.ManagedModel{Provider: "google", ID: "gemini-3.8-flash"},
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
	stream1 := m.streamEmbeddedGoogle(ctx, &ai.Model{ID: "gemini-3.8-flash", Provider: "google"}, ai.TranscriptContext{}, nil)
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
	stream2 := m.streamEmbeddedGoogle(ctx, &ai.Model{ID: "gemini-3.8-flash", Provider: "google"}, ai.TranscriptContext{}, nil)
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

	// Tool result with image produces functionResponse in turn 1, and image in turn 2 (separated to satisfy Vertex AI requirements)
	contents := convertMessagesToGoogleContents(messages)
	require.Len(t, contents, 2)

	turn1 := contents[0]
	require.Equal(t, "user", turn1["role"])
	parts1 := turn1["parts"].([]any)
	require.Len(t, parts1, 1)

	frPart := parts1[0].(map[string]any)
	fr, ok := frPart["functionResponse"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "read", fr["name"])

	turn2 := contents[1]
	require.Equal(t, "user", turn2["role"])
	parts2 := turn2["parts"].([]any)
	require.Len(t, parts2, 2)

	textPart := parts2[0].(map[string]any)
	require.Equal(t, "Tool result image:", textPart["text"])

	imgPart := parts2[1].(map[string]any)
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

	// Test explanation:
	// In managed agent executions, background workers or subagents might output simulated
	// tool harness observations in the model text channel (e.g. "[Observation: subagent ...]").
	// The router strips these out via stripSimulatedObservations. When an assistant turn
	// contains ONLY simulated observations, stripping leaves 0 text parts.
	// Rather than dropping the assistant turn (which would merge adjacent user messages),
	// the converter normalizes it to a valid model turn with "(empty response)".
	// The first assistant message below was entirely simulated subagent observation,
	// so its text is stripped completely, normalizing to "(empty response)" to preserve turn alternation.
	// The second assistant message has the observation lines stripped, leaving only the real response.
	require.Len(t, contents, 4)

	require.Equal(t, "user", contents[0]["role"])
	require.Equal(t, "model", contents[1]["role"])
	parts1 := contents[1]["parts"].([]any)
	require.Len(t, parts1, 1)
	require.Equal(t, "(empty response)", parts1[0].(map[string]any)["text"])

	require.Equal(t, "user", contents[2]["role"])
	require.Equal(t, "model", contents[3]["role"])

	modelParts := contents[3]["parts"].([]any)
	require.Len(t, modelParts, 1)
	textPart := modelParts[0].(map[string]any)
	require.Equal(t, "I am actively working on the task now.", textPart["text"])
	require.NotContains(t, textPart["text"], "Observation:")
	require.NotContains(t, textPart["text"], "exit=0")
}

func TestGoogleAuth_ConvertMessagesToGoogleContents_EmptyAssistantPreservesAlternation(t *testing.T) {
	// Test suite covering SPEC-TRANSCRIPT-EMPTY-TURN-001 (Empty Assistant Turn Transcript Retention).
	// Gemini requires strict turn role alternation (user -> model -> user) and rejects empty part arrays.
	// If assistant turns are omitted when empty, surrounding user turns (e.g. subagent completions
	// and goal wake-ups) accidentally concatenate into a single compound user prompt, confusing context.

	// SCN-001: Empty assistant turn between user prompt and goal reminder retains model turn and prevents user message merger.
	// Verifies that when an empty assistant turn occurs between two user messages,
	// it is converted to {"role": "model", "parts": [{"text": "(empty response)"}]}
	// instead of dropping the turn, preserving 3 separate turns.
	messages1 := []ai.Message{
		ai.UserMessage{
			Content: []ai.Content{
				ai.TextContent{Text: "Task completion notification"},
			},
		},
		ai.AssistantMessage{
			Content: []ai.Content{},
		},
		ai.UserMessage{
			Content: []ai.Content{
				ai.TextContent{Text: "NOT_DONE: Please finish the remaining subgoals"},
			},
		},
	}

	contents1 := convertMessagesToGoogleContents(messages1)
	// Must retain the model turn as "(empty response)" and preserve 3 distinct turns: user -> model -> user
	require.Len(t, contents1, 3)
	require.Equal(t, "user", contents1[0]["role"])
	parts1 := contents1[0]["parts"].([]any)
	require.Len(t, parts1, 1)
	require.Equal(t, "Task completion notification", parts1[0].(map[string]any)["text"])

	require.Equal(t, "model", contents1[1]["role"])
	parts2 := contents1[1]["parts"].([]any)
	require.Len(t, parts2, 1)
	require.Equal(t, "(empty response)", parts2[0].(map[string]any)["text"])

	require.Equal(t, "user", contents1[2]["role"])
	parts3 := contents1[2]["parts"].([]any)
	require.Len(t, parts3, 1)
	require.Equal(t, "NOT_DONE: Please finish the remaining subgoals", parts3[0].(map[string]any)["text"])

	// SCN-002: Tool response -> empty assistant -> user continuation prompt
	// Tool results are mapped to role "user" with functionResponse parts. When followed by an
	// empty assistant turn and then a user continuation prompt, retaining the "(empty response)"
	// model turn ensures Gemini's protocol requirement: functionResponse parts are not mixed with
	// regular user text parts in the same turn, and the tool response and user prompt remain separate.
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
	// Must produce 5 turns: user -> model ("Calling tool") -> user (tool result) -> model ("(empty response)") -> user (prompt)
	require.Len(t, contents2, 5)
	require.Equal(t, "user", contents2[0]["role"])
	require.Equal(t, "model", contents2[1]["role"])
	require.Equal(t, "user", contents2[2]["role"])
	require.Equal(t, "model", contents2[3]["role"])
	modelEmpty := contents2[3]["parts"].([]any)
	require.Len(t, modelEmpty, 1)
	require.Equal(t, "(empty response)", modelEmpty[0].(map[string]any)["text"])
	require.Equal(t, "user", contents2[4]["role"])
	require.Equal(t, "Continue working on the current goal.", contents2[4]["parts"].([]any)[0].(map[string]any)["text"])

	// SCN-003: Assistant turn with only whitespace or "(empty response)" is normalized to model turn with "(empty response)"
	// Assistant turns may contain whitespace-only text or already-emitted "(empty response)" strings.
	// When multiple empty assistant turns occur in succession without user intervention, they are
	// merged and deduplicated so that consecutive identical "(empty response)" model turns do not multiply.
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
	// Consecutive empty assistant turns merge into a single model turn with "(empty response)"
	require.Len(t, contents3, 3)
	require.Equal(t, "user", contents3[0]["role"])
	require.Equal(t, "model", contents3[1]["role"])
	mParts3 := contents3[1]["parts"].([]any)
	require.Len(t, mParts3, 1)
	require.Equal(t, "(empty response)", mParts3[0].(map[string]any)["text"])
	require.Equal(t, "user", contents3[2]["role"])

	// SCN-004: Consecutive user messages without intervening assistant turn continue to merge
	// Validates the invariant that normal user message concatenation is preserved when there is NO
	// assistant turn between them (standard multi-part user turn behavior).
	messages4 := []ai.Message{
		ai.UserMessage{
			Content: []ai.Content{
				ai.TextContent{Text: "User message 1"},
			},
		},
		ai.UserMessage{
			Content: []ai.Content{
				ai.TextContent{Text: "User message 2"},
			},
		},
	}
	contents4 := convertMessagesToGoogleContents(messages4)
	require.Len(t, contents4, 1)
	require.Equal(t, "user", contents4[0]["role"])
	parts4 := contents4[0]["parts"].([]any)
	require.Len(t, parts4, 2)
	require.Equal(t, "User message 1", parts4[0].(map[string]any)["text"])
	require.Equal(t, "User message 2", parts4[1].(map[string]any)["text"])
}

func TestGoogleAuth_ConvertMessagesToGoogleContents_AssistantThinkingOnlyPreserved(t *testing.T) {
	// When an assistant message contains ONLY thinking tokens (e.g. Gemini 3.8 Flash internal reasoning),
	// it must NOT be dropped, preserving valid turn alternation in the Google contents array.
	messages := []ai.Message{
		ai.UserMessage{
			Content: []ai.Content{
				ai.TextContent{Text: "Please think about this problem."},
			},
		},
		ai.AssistantMessage{
			Content: []ai.Content{
				ai.ThinkingContent{Thinking: "Analyzing requirements deeply before responding..."},
			},
		},
		ai.UserMessage{
			Content: []ai.Content{
				ai.TextContent{Text: "Here is additional context."},
			},
		},
	}

	contents := convertMessagesToGoogleContents(messages)
	require.Len(t, contents, 3, "thinking-only assistant message must not be dropped")
	require.Equal(t, "user", contents[0]["role"])
	require.Equal(t, "model", contents[1]["role"])
	require.Equal(t, "user", contents[2]["role"])

	modelParts := contents[1]["parts"].([]any)
	require.Len(t, modelParts, 1)
	partMap := modelParts[0].(map[string]any)
	require.Equal(t, true, partMap["thought"])
	require.Equal(t, "Analyzing requirements deeply before responding...", partMap["text"])
}

func TestGoogleAuth_ConvertMessagesToGoogleContents_ConsecutiveTurns(t *testing.T) {
	// 1. Consecutive text user messages (should merge)
	textMsgs := []ai.Message{
		ai.UserMessage{
			Content: []ai.Content{
				ai.TextContent{Text: "First line"},
			},
		},
		ai.UserMessage{
			Content: []ai.Content{
				ai.TextContent{Text: "Second line"},
			},
		},
	}
	contentsText := convertMessagesToGoogleContents(textMsgs)
	require.Len(t, contentsText, 1)
	require.Equal(t, "user", contentsText[0]["role"])
	textParts := contentsText[0]["parts"].([]any)
	require.Len(t, textParts, 2)
	require.Equal(t, "First line", textParts[0].(map[string]any)["text"])
	require.Equal(t, "Second line", textParts[1].(map[string]any)["text"])

	// 2. Consecutive tool responses (should merge into same tool response turn if multiple calls)
	toolMsgs := []ai.Message{
		ai.UserMessage{
			Content: []ai.Content{
				ai.TextContent{Text: "Run tools"},
			},
		},
		ai.AssistantMessage{
			Content: []ai.Content{
				ai.ToolCall{ID: "call-1", Name: "bash", Arguments: map[string]any{"command": "ls"}},
				ai.ToolCall{ID: "call-2", Name: "read", Arguments: map[string]any{"path": "foo.txt"}},
			},
		},
		ai.ToolResultMessage{
			ToolCallID: "call-1",
			ToolName:   "bash",
			Content:    []ai.Content{ai.TextContent{Text: "file1"}},
		},
		ai.ToolResultMessage{
			ToolCallID: "call-2",
			ToolName:   "read",
			Content:    []ai.Content{ai.TextContent{Text: "file content"}},
		},
	}
	contentsTools := convertMessagesToGoogleContents(toolMsgs)
	require.Len(t, contentsTools, 3)
	require.Equal(t, "user", contentsTools[0]["role"])
	require.Equal(t, "model", contentsTools[1]["role"])
	require.Equal(t, "user", contentsTools[2]["role"])
	toolParts := contentsTools[2]["parts"].([]any)
	require.Len(t, toolParts, 2)
	fr1 := toolParts[0].(map[string]any)["functionResponse"].(map[string]any)
	fr2 := toolParts[1].(map[string]any)["functionResponse"].(map[string]any)
	require.Equal(t, "bash", fr1["name"])
	require.Equal(t, "read", fr2["name"])

	// 3. Tool response followed by user text message (must NOT merge into the functionResponse turn)
	mixedMsgs := []ai.Message{
		ai.UserMessage{
			Content: []ai.Content{
				ai.TextContent{Text: "Run tool"},
			},
		},
		ai.AssistantMessage{
			Content: []ai.Content{
				ai.ToolCall{ID: "call-1", Name: "bash", Arguments: map[string]any{"command": "ls"}},
			},
		},
		ai.ToolResultMessage{
			ToolCallID: "call-1",
			ToolName:   "bash",
			Content:    []ai.Content{ai.TextContent{Text: "file1"}},
		},
		ai.UserMessage{
			Content: []ai.Content{
				ai.TextContent{Text: "Now do something else"},
			},
		},
	}
	contentsMixed := convertMessagesToGoogleContents(mixedMsgs)
	require.Len(t, contentsMixed, 4)
	require.Equal(t, "user", contentsMixed[0]["role"])
	require.Equal(t, "model", contentsMixed[1]["role"])
	require.Equal(t, "user", contentsMixed[2]["role"])
	require.Equal(t, "user", contentsMixed[3]["role"])

	frParts := contentsMixed[2]["parts"].([]any)
	require.Len(t, frParts, 1)
	require.Contains(t, frParts[0].(map[string]any), "functionResponse")

	txtParts := contentsMixed[3]["parts"].([]any)
	require.Len(t, txtParts, 1)
	require.Equal(t, "Now do something else", txtParts[0].(map[string]any)["text"])
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
	t.Setenv("GEMINI_API_KEY", "test-api-key")
	origURL := googleGenerativeLanguageURLBase
	defer func() { googleGenerativeLanguageURLBase = origURL }()

	mockAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"Let me think about this.\",\"thought\":true}],\"role\":\"model\"}}]}}\n\n"))
		_, _ = w.Write([]byte("data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"Here is the answer.\"}],\"role\":\"model\"}}]}}\n\n"))
	}))
	defer mockAPI.Close()
	googleGenerativeLanguageURLBase = mockAPI.URL

	tempDir := t.TempDir()
	authPath := filepath.Join(tempDir, "auth.json")
	initialAuth := map[string]any{"google": map[string]any{"key": "test-api-key"}}
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

	stream := m.streamEmbeddedGoogle(context.Background(), &ai.Model{ID: "gemini-3.8-flash", Provider: "google"}, transcript, nil)

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

func TestGoogleAuth_ConvertMessagesToGoogleContents_RetainsLongTranscript(t *testing.T) {
	// Create a transcript with >60 messages (e.g., 80 messages: 40 user, 40 assistant).
	// Verify that the very first user message and full conversation history are preserved.
	var messages []ai.Message
	messages = append(messages, ai.UserMessage{
		Content: []ai.Content{
			ai.TextContent{Text: "Initial user message that must not be truncated"},
		},
	})
	messages = append(messages, ai.AssistantMessage{
		Content: []ai.Content{
			ai.TextContent{Text: "Initial assistant response"},
		},
	})

	for i := 2; i < 80; i++ {
		if i%2 == 0 {
			messages = append(messages, ai.UserMessage{
				Content: []ai.Content{
					ai.TextContent{Text: fmt.Sprintf("User follow-up message %d", i)},
				},
			})
		} else {
			messages = append(messages, ai.AssistantMessage{
				Content: []ai.Content{
					ai.TextContent{Text: fmt.Sprintf("Assistant follow-up response %d", i)},
				},
			})
		}
	}
	require.Len(t, messages, 80)

	contents := convertMessagesToGoogleContents(messages)
	require.Len(t, contents, 80)

	// Initial user message must be retained at contents[0]
	require.Equal(t, "user", contents[0]["role"])
	parts0, ok := contents[0]["parts"].([]any)
	require.True(t, ok)
	require.NotEmpty(t, parts0)
	require.Equal(t, "Initial user message that must not be truncated", parts0[0].(map[string]any)["text"])

	// Check final turn as well
	require.Equal(t, "model", contents[79]["role"])
	parts79, ok := contents[79]["parts"].([]any)
	require.True(t, ok)
	require.NotEmpty(t, parts79)
	require.Equal(t, "Assistant follow-up response 79", parts79[0].(map[string]any)["text"])
}

func TestGoogleAuth_TranscriptOver100MessagesNotPrunedIfUnderThreshold(t *testing.T) {
	// Construct a transcript with 150 messages whose token usage is well below 80% of context window
	var messages []ai.Message
	messages = append(messages, ai.UserMessage{
		Content: []ai.Content{
			ai.TextContent{Text: "Initial instruction for 150-message test"},
		},
	})
	for i := 1; i < 150; i++ {
		if i%2 == 1 {
			messages = append(messages, ai.AssistantMessage{
				Content: []ai.Content{
					ai.ToolCall{
						ID:        fmt.Sprintf("call_%d", i),
						Name:      "bash",
						Arguments: map[string]any{"command": fmt.Sprintf("echo %d", i)},
					},
				},
			})
		} else {
			messages = append(messages, ai.ToolResultMessage{
				ToolCallID: fmt.Sprintf("call_%d", i-1),
				ToolName:   "bash",
				Content: []ai.Content{
					ai.TextContent{Text: fmt.Sprintf("output_%d", i)},
				},
			})
		}
	}
	require.Len(t, messages, 150)

	initialTokens := estimateTranscriptTokens(messages)
	require.Less(t, initialTokens, 50000, "150 small messages must be under 50k tokens")

	// Convert with standard 1M context window
	contents := convertMessagesToGoogleContents(messages, 1048576)
	require.NotEmpty(t, contents)

	// Verify no tool results were pruned with the omission placeholder
	for _, turn := range contents {
		if parts, ok := turn["parts"].([]any); ok {
			for _, p := range parts {
				if pMap, ok := p.(map[string]any); ok {
					if fr, ok := pMap["functionResponse"].(map[string]any); ok {
						if resp, ok := fr["response"].(map[string]any); ok {
							if out, ok := resp["output"].(string); ok {
								require.NotContains(t, out, "[Output omitted for context fitting]", "turn output should not be pruned under 80% threshold")
							}
						}
					}
				}
			}
		}
	}
}

func TestGoogleAuth_LargeTranscriptPrunedRetainsAllUserMessages(t *testing.T) {
	// Construct a transcript with 2,000 messages and > 1.5M tokens
	var messages []ai.Message
	var expectedUserTexts []string

	// Message 0: Genesis prompt / initial task
	genesisPrompt := "Genesis prompt: build the core system and coordinate all workflows."
	messages = append(messages, ai.UserMessage{
		Content: []ai.Content{
			ai.TextContent{Text: genesisPrompt},
		},
	})
	expectedUserTexts = append(expectedUserTexts, genesisPrompt)

	// Middle messages: 1 to 1949
	// Interspersed with user messages, assistant tool calls, and oversized tool results
	largeOutput := strings.Repeat("Oversized bash output line for testing token limits in context.\n", 130) // ~8KB per result
	for i := 1; i < 1950; i++ {
		if i%10 == 0 {
			// User conversation message
			userTxt := fmt.Sprintf("Wolfgang chat directive message %d: status update please", i)
			messages = append(messages, ai.UserMessage{
				Content: []ai.Content{
					ai.TextContent{Text: userTxt},
				},
			})
			expectedUserTexts = append(expectedUserTexts, userTxt)
		} else if i%2 == 1 {
			// Assistant tool call
			messages = append(messages, ai.AssistantMessage{
				Content: []ai.Content{
					ai.ToolCall{
						ID:        fmt.Sprintf("call_%d", i),
						Name:      "bash",
						Arguments: map[string]any{"command": fmt.Sprintf("run_check_%d", i)},
					},
				},
			})
		} else {
			// Large tool result
			messages = append(messages, ai.ToolResultMessage{
				ToolCallID: fmt.Sprintf("call_%d", i-1),
				ToolName:   "bash",
				Content: []ai.Content{
					ai.TextContent{Text: fmt.Sprintf("Result for %d:\n%s", i, largeOutput)},
				},
			})
		}
	}

	// Recent 50 messages: 1950 to 1999 (active turn context)
	for i := 1950; i < 2000; i++ {
		if i%2 == 0 {
			userTxt := fmt.Sprintf("Recent user message %d", i)
			messages = append(messages, ai.UserMessage{
				Content: []ai.Content{
					ai.TextContent{Text: userTxt},
				},
			})
			expectedUserTexts = append(expectedUserTexts, userTxt)
		} else {
			messages = append(messages, ai.AssistantMessage{
				Content: []ai.Content{
					ai.TextContent{Text: fmt.Sprintf("Recent assistant response %d", i)},
				},
			})
		}
	}
	require.Len(t, messages, 2000)

	initialTokens := estimateTranscriptTokens(messages)
	t.Logf("Initial transcript tokens: %d (messages: %d)", initialTokens, len(messages))
	require.Greater(t, initialTokens, 1500000, "transcript must exceed 1.5M tokens initially")

	// Convert with default 1M context window (trigger 80% = 800k, target 50% = 500k)
	contents := convertMessagesToGoogleContents(messages)
	require.NotEmpty(t, contents)

	prunedTokens := estimateGoogleContentsTokens(contents)
	t.Logf("Pruned payload tokens: %d (converted turns: %d)", prunedTokens, len(contents))
	require.Less(t, prunedTokens, 600000, "pruned contents must be cleanly under 600,000 tokens")

	// 1. Genesis prompt must be retained at contents[0]
	require.Equal(t, "user", contents[0]["role"])
	parts0, ok := contents[0]["parts"].([]any)
	require.True(t, ok)
	require.NotEmpty(t, parts0)
	require.Contains(t, parts0[0].(map[string]any)["text"], genesisPrompt)

	// 2. Every single user message must be preserved
	var allUserContent strings.Builder
	for _, turn := range contents {
		if turn["role"] == "user" {
			for _, p := range turn["parts"].([]any) {
				if pMap, ok := p.(map[string]any); ok {
					if txt, ok := pMap["text"].(string); ok {
						allUserContent.WriteString(txt)
						allUserContent.WriteString("\n")
					}
				}
			}
		}
	}
	allUserStr := allUserContent.String()
	for _, want := range expectedUserTexts {
		require.Contains(t, allUserStr, want, "every user message must be preserved")
	}

	// 3. Verify Gemini role alternation
	require.Equal(t, "user", contents[0]["role"])
	for i := 1; i < len(contents); i++ {
		prevRole := contents[i-1]["role"].(string)
		currRole := contents[i]["role"].(string)
		require.NotEqual(t, prevRole, currRole, "consecutive turns must alternate role")
	}

	// 4. Verify thought_signature on any functionCall
	for _, turn := range contents {
		if turn["role"] == "model" {
			for _, p := range turn["parts"].([]any) {
				if pMap, ok := p.(map[string]any); ok {
					if _, ok := pMap["functionCall"]; ok {
						require.Equal(t, "skip_thought_signature_validator", pMap["thought_signature"])
					}
				}
			}
		}
	}
}

func TestGoogleAuth_WorkflowsLiveEntriesPruning(t *testing.T) {
	dbPath := "/home/wolfgang/.mecha-developer/router.sqlite"
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Skip("router.sqlite not found")
	}

	db, err := sql.Open("sqlite", dbPath+"?mode=ro")
	require.NoError(t, err)
	defer db.Close()

	rows, err := db.Query(`SELECT body FROM embedded_entries WHERE agent_id='workflows' ORDER BY seq`)
	require.NoError(t, err)
	defer rows.Close()

	var messages []ai.Message
	var wolfgangChats []string
	for rows.Next() {
		var raw []byte
		require.NoError(t, rows.Scan(&raw))
		msg, err := ai.UnmarshalMessage(raw)
		require.NoError(t, err)
		messages = append(messages, msg)

		if um, ok := asUserMsg(msg); ok {
			for _, part := range um.Content {
				if tc, ok := part.(ai.TextContent); ok && tc.Text != "" {
					cleaned := unwrapEmbeddedMailText(tc.Text)
					wolfgangChats = append(wolfgangChats, cleaned)
				} else if tc, ok := part.(*ai.TextContent); ok && tc != nil && tc.Text != "" {
					cleaned := unwrapEmbeddedMailText(tc.Text)
					wolfgangChats = append(wolfgangChats, cleaned)
				}
			}
		}
	}
	if len(messages) < 1800 {
		t.Skipf("skipping test: expected at least 1800 entries for workflows in live db, found %d", len(messages))
	}
	require.NotEmpty(t, wolfgangChats, "expected non-empty Wolfgang chat messages")

	// Convert with Gemini context window (1,048,576)
	contents := convertMessagesToGoogleContents(messages, 1048576)
	require.NotEmpty(t, contents)

	// Verify token count is under 500,000 tokens
	tokens := estimateGoogleContentsTokens(contents)
	t.Logf("Workflows entries: %d original messages, %d converted turns, estimated %d tokens", len(messages), len(contents), tokens)
	require.Less(t, tokens, 500000, "converted payload must be under 500,000 tokens")

	// Verify all of Wolfgang's chat messages are preserved
	var allUserContent strings.Builder
	for _, turn := range contents {
		if turn["role"] == "user" {
			for _, p := range turn["parts"].([]any) {
				if pMap, ok := p.(map[string]any); ok {
					if txt, ok := pMap["text"].(string); ok {
						allUserContent.WriteString(txt)
						allUserContent.WriteString("\n")
					}
				}
			}
		}
	}
	allUserStr := allUserContent.String()
	for _, chat := range wolfgangChats {
		require.Contains(t, allUserStr, chat, "Wolfgang's chat message must be retained")
	}

	// Verify Gemini role alternation
	require.Equal(t, "user", contents[0]["role"])
	for i := 1; i < len(contents); i++ {
		prevRole := contents[i-1]["role"].(string)
		currRole := contents[i]["role"].(string)
		require.NotEqual(t, prevRole, currRole, "consecutive turns must alternate role")
	}

	// Verify all functionCalls have thought_signature
	for _, turn := range contents {
		if turn["role"] == "model" {
			for _, p := range turn["parts"].([]any) {
				if pMap, ok := p.(map[string]any); ok {
					if _, ok := pMap["functionCall"]; ok {
						require.Equal(t, "skip_thought_signature_validator", pMap["thought_signature"])
					}
				}
			}
		}
	}
}

func TestGoogleAuth_StreamGoogleProviderAPIKey(t *testing.T) {
	var capturedPath string
	var capturedQuery string
	var capturedBody map[string]any

	mockAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		capturedQuery = r.URL.RawQuery
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		capturedBody = body

		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			return
		}

		sse1 := `data: {"candidates":[{"content":{"parts":[{"text":"Thinking chunk","thought":true}],"role":"model"}}]}

`
		_, _ = w.Write([]byte(sse1))
		flusher.Flush()

		sse2 := `data: {"candidates":[{"content":{"parts":[{"text":"Final answer"}],"role":"model"}}]}

`
		_, _ = w.Write([]byte(sse2))
		flusher.Flush()
	}))
	defer mockAPI.Close()

	origBase := googleGenerativeLanguageURLBase
	googleGenerativeLanguageURLBase = mockAPI.URL
	defer func() { googleGenerativeLanguageURLBase = origBase }()

	tempDir := t.TempDir()
	authPath := filepath.Join(tempDir, "auth.json")
	initialAuth := map[string]any{
		"google": map[string]any{
			"key": "mock-gemini-key",
		},
	}
	data, _ := json.Marshal(initialAuth)
	require.NoError(t, os.WriteFile(authPath, data, 0600))

	m := &Manager{config: Config{PiAgentDir: tempDir}}

	key, err := m.resolveGoogleAPIKey(context.Background())
	require.NoError(t, err)
	require.Equal(t, "mock-gemini-key", key)

	transcript := ai.TranscriptContext{
		Messages: []ai.Message{
			ai.UserMessage{
				Content: []ai.Content{
					ai.TextContent{Text: "Hello Gemini"},
				},
			},
		},
	}

	stream := m.streamEmbeddedGoogle(context.Background(), &ai.Model{ID: "models/gemini-2.5-flash", Provider: "google"}, transcript, nil)

	var thinkingEvents []ai.AssistantMessageEvent
	var textEvents []ai.AssistantMessageEvent
	var finalDone *ai.AssistantMessage

	for event := range stream.Events() {
		t.Logf("Event: type=%v, err=%+v", event.Type, event.Error)
		switch event.Type {
		case ai.EventThinkingStart, ai.EventThinkingDelta, ai.EventThinkingEnd:
			thinkingEvents = append(thinkingEvents, event)
		case ai.EventTextStart, ai.EventTextDelta, ai.EventTextEnd:
			textEvents = append(textEvents, event)
		case ai.EventDone:
			finalDone = event.Message
		}
	}

	require.Equal(t, "/v1beta/models/gemini-2.5-flash:streamGenerateContent", capturedPath)
	require.Contains(t, capturedQuery, "alt=sse")
	require.Contains(t, capturedQuery, "key=mock-gemini-key")
	require.NotNil(t, capturedBody)
	// Direct request body: contents must be at root of body (no Antigravity project/envelope)
	require.NotContains(t, capturedBody, "project")
	require.Contains(t, capturedBody, "contents")

	require.NotEmpty(t, thinkingEvents)
	require.NotEmpty(t, textEvents)
	require.NotNil(t, finalDone)
	require.Len(t, finalDone.Content, 2)
	th, isTh := finalDone.Content[0].(ai.ThinkingContent)
	require.True(t, isTh)
	require.Equal(t, "Thinking chunk", th.Thinking)
	txt, isTxt := finalDone.Content[1].(ai.TextContent)
	require.True(t, isTxt)
	require.Equal(t, "Final answer", txt.Text)

	// Test missing key error handling
	emptyDir := t.TempDir()
	emptyMgr := &Manager{config: Config{PiAgentDir: emptyDir, ManagedHome: emptyDir}}
	t.Setenv("HOME", emptyDir)
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")
	errStream := emptyMgr.streamEmbeddedGoogle(context.Background(), &ai.Model{ID: "gemini-2.5-flash", Provider: "google"}, ai.TranscriptContext{}, nil)
	var errEvents []ai.AssistantMessageEvent
	for ev := range errStream.Events() {
		if ev.Type == ai.EventError {
			errEvents = append(errEvents, ev)
		}
	}
	require.NotEmpty(t, errEvents)
	require.Contains(t, errEvents[0].Error.ErrorMessage, "no Google API key found")
}

func TestGoogleAuth_ResolveGoogleAPIKey_Precedence(t *testing.T) {
	tempDir := t.TempDir()
	authPath := filepath.Join(tempDir, "auth.json")
	authData := map[string]any{
		"google": map[string]any{
			"key": "file-key",
		},
	}
	raw, _ := json.Marshal(authData)
	require.NoError(t, os.WriteFile(authPath, raw, 0600))

	m := &Manager{config: Config{PiAgentDir: tempDir}}

	// Env var GEMINI_API_KEY takes precedence
	t.Setenv("GEMINI_API_KEY", "env-gemini-key")
	t.Setenv("GOOGLE_API_KEY", "env-google-key")
	k, err := m.resolveGoogleAPIKey(context.Background())
	require.NoError(t, err)
	require.Equal(t, "env-gemini-key", k)

	// Next GOOGLE_API_KEY takes precedence over file
	t.Setenv("GEMINI_API_KEY", "")
	k, err = m.resolveGoogleAPIKey(context.Background())
	require.NoError(t, err)
	require.Equal(t, "env-google-key", k)

	// Finally auth.json
	t.Setenv("GOOGLE_API_KEY", "")
	k, err = m.resolveGoogleAPIKey(context.Background())
	require.NoError(t, err)
	require.Equal(t, "file-key", k)
}

func TestSanitizeGeminiParameters(t *testing.T) {
	// Case 1: nil input returns nil
	require.Nil(t, sanitizeGeminiParameters(nil))

	// Case 2: raw map with additionalProperties, $schema, etc.
	rawMap := map[string]any{
		"$schema":              "http://json-schema.org/draft-07/schema#",
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"command": map[string]any{
				"type":                 "string",
				"description":          "command to run",
				"additionalProperties": false,
				"$schema":              "http://example.com/schema",
				"$comment":             "internal note",
			},
			"items": []any{
				map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"$defs": map[string]any{
						"def1": "val1",
					},
					"properties": map[string]any{
						"nested": map[string]any{
							"type": "string",
						},
					},
				},
				"scalar-item",
			},
		},
		"required": []any{"command"},
	}

	sanitized := sanitizeGeminiParameters(rawMap)
	sanitizedMap, ok := sanitized.(map[string]any)
	require.True(t, ok)

	// Top level checks
	require.NotContains(t, sanitizedMap, "$schema")
	require.NotContains(t, sanitizedMap, "additionalProperties")
	require.Equal(t, "object", sanitizedMap["type"])
	require.Equal(t, []any{"command"}, sanitizedMap["required"])

	// Nested property checks
	props, ok := sanitizedMap["properties"].(map[string]any)
	require.True(t, ok)
	cmdProp, ok := props["command"].(map[string]any)
	require.True(t, ok)
	require.NotContains(t, cmdProp, "additionalProperties")
	require.NotContains(t, cmdProp, "$schema")
	require.NotContains(t, cmdProp, "$comment")
	require.Equal(t, "string", cmdProp["type"])
	require.Equal(t, "command to run", cmdProp["description"])

	// Slice traversal checks
	items, ok := props["items"].([]any)
	require.True(t, ok)
	require.Len(t, items, 2)
	item0, ok := items[0].(map[string]any)
	require.True(t, ok)
	require.NotContains(t, item0, "additionalProperties")
	require.NotContains(t, item0, "$defs")
	require.Equal(t, "object", item0["type"])
	require.Equal(t, "scalar-item", items[1])

	// Case 3: *ai.Schema struct input (as produced by ai.Object)
	additionalFalse := false
	schema := &ai.Schema{
		Type:              "object",
		AdditionalAllowed: &additionalFalse,
		Properties: map[string]*ai.Schema{
			"param1": {
				Type:              "string",
				Description:       "first param",
				AdditionalAllowed: &additionalFalse,
			},
		},
		Required: []string{"param1"},
	}

	sanitizedSchema := sanitizeGeminiParameters(schema)
	schemaMap, ok := sanitizedSchema.(map[string]any)
	require.True(t, ok)
	require.NotContains(t, schemaMap, "additionalProperties")
	require.NotContains(t, schemaMap, "$schema")
	schemaProps, ok := schemaMap["properties"].(map[string]any)
	require.True(t, ok)
	p1, ok := schemaProps["param1"].(map[string]any)
	require.True(t, ok)
	require.NotContains(t, p1, "additionalProperties")
	require.Equal(t, "string", p1["type"])
}

func TestAntigravityRemovalRejectsModel(t *testing.T) {
	_, err := ResolveModel(store.ManagedModel{Provider: "antigravity", ID: "gemini-3.8-flash"})
	require.Error(t, err)
	require.False(t, validProviderAuthProvider("antigravity"))
}
