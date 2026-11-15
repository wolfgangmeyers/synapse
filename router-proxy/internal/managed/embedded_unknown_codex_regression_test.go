package managed

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sky-valley/pi/ai"
)

// An undeclared provider tool should reach dispatch as a call so it can receive
// a correlated, safe unavailable-tool result rather than an opaque stream error.
func TestCodexUnknownToolCanReachDispatch(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, e := range []string{
			`{"type":"response.created","response":{"id":"response_1"}}`,
			`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_response_1_0","call_id":"call_response_1_0","name":"nonexistent_issue45_tool","arguments":""}}`,
			`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"fc_response_1_0","call_id":"call_response_1_0","name":"nonexistent_issue45_tool","arguments":"{}"}}`,
			`{"type":"response.completed","response":{"id":"response_1","status":"completed"}}`,
		} {
			fmt.Fprintf(w, "data: %s\n\n", e)
		}
	}))
	defer server.Close()
	model := *ai.GetModel("openai-codex", "gpt-6-sol")
	model.BaseURL = server.URL
	opts := &ai.SimpleStreamOptions{StreamOptions: ai.StreamOptions{ProviderRequestOptions: ai.ProviderRequestOptions{CodexAuth: func(context.Context) (ai.ModelAuth, error) {
		return ai.ModelAuth{APIKey: "fixture", Headers: ai.ProviderHeaders{"ChatGPT-Account-ID": ai.HeaderValue("fixture")}}, nil
	}}}}
	opts.HTTPClient = server.Client()
	stream := ai.StreamSimple(context.Background(), &model, ai.Context{Messages: []ai.Message{ai.NewUserText("test", 1)}, Tools: []ai.Tool{{Name: "read", Parameters: &ai.Schema{Type: "object"}}}}, opts)
	for range stream.Events() {
	}
	result := stream.Result()
	if result.StopReason != ai.StopToolUse || len(result.Content) != 1 {
		t.Fatalf("expected dispatchable unknown call, got stop=%s error=%q content=%#v", result.StopReason, result.ErrorMessage, result.Content)
	}
}
