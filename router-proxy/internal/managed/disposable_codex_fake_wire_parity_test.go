package managed

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
)

// Disposable, token-free local characterization. Never retain or print a raw request.
type fakeWireShape struct {
	HeaderNames                                           []string
	AuthOK, AccountOK, AcceptOK, ContentTypeOK            bool
	OriginatorOK, UserAgentOK, BetaOK                     bool
	Keys                                                  []string
	Types                                                 map[string]string
	ModelOK, InstructionsOK, StreamOK, StoreOK, IncludeOK bool
	InstructionsCategory                                  string
	InputCount, IncludeCount                              int
	InputTypes                                            []string
	ToolCount                                             int
	ToolTypes                                             []string
	ToolNameOK                                            bool
	StrictPresent                                         bool
	StrictType                                            string
	StrictNull                                            bool
	ChoiceType, ChoiceAuto, ParallelOK                    bool
	StoreValue, StreamValue, ParallelValue                bool
	TextType, TextLow, ReasoningType, ReasoningNone       bool
	ReasoningEffortType                                   string
	ReasoningKeys, TextKeys                               []string
	SchemaType, PropertyType                              string
	RequiredValueOK                                       bool
}

type fakeWireDoer struct {
	calls int
	shape fakeWireShape
	mode  string
}

func fakeWireType(v any) string {
	switch v.(type) {
	case string:
		return "string"
	case bool:
		return "boolean"
	case float64:
		return "number"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	case nil:
		return "null"
	default:
		return "unknown"
	}
}

func (d *fakeWireDoer) Do(r *http.Request) (*http.Response, error) {
	d.calls++
	// This doer has no network fallback. A mismatch yields a fixed error, not the URL.
	if r.Method != http.MethodPost || r.URL.String() != embeddedCodexResponsesURL || r.URL.RawQuery != "" {
		return nil, &fakeWireError{}
	}
	s := fakeWireShape{Types: map[string]string{}}
	for k := range r.Header {
		s.HeaderNames = append(s.HeaderNames, k)
	}
	slices.Sort(s.HeaderNames)
	s.AuthOK = r.Header.Get("Authorization") == "Bearer synthetic-bearer"
	s.AccountOK = r.Header.Get("ChatGPT-Account-ID") == "synthetic-account"
	s.AcceptOK = r.Header.Get("Accept") == "text/event-stream"
	s.ContentTypeOK = r.Header.Get("Content-Type") == "application/json"
	s.OriginatorOK = r.Header.Get("Originator") == "pi"
	s.UserAgentOK = strings.HasPrefix(r.Header.Get("User-Agent"), "pi (") && strings.HasSuffix(r.Header.Get("User-Agent"), ")")
	s.BetaOK = r.Header.Get("OpenAI-Beta") == "responses=experimental"
	var body map[string]any
	if err := json.NewDecoder(io.LimitReader(r.Body, 65536)).Decode(&body); err != nil {
		return nil, &fakeWireError{}
	}
	for k, v := range body {
		s.Keys = append(s.Keys, k)
		s.Types[k] = fakeWireType(v)
	}
	slices.Sort(s.Keys)
	s.ModelOK = body["model"] == "gpt-6-sol"
	if body["instructions"] == "Synthetic system." {
		s.InstructionsOK = true
		s.InstructionsCategory = "nonempty_synthetic"
	}
	if body["instructions"] == "You are a helpful assistant." {
		s.InstructionsCategory = "empty_fallback"
	}
	s.StreamOK = body["stream"] == true
	s.StoreOK = body["store"] == false
	s.StreamValue, _ = body["stream"].(bool)
	s.StoreValue, _ = body["store"].(bool)
	s.IncludeOK = reflect.DeepEqual(body["include"], []any{"reasoning.encrypted_content"})
	if include, ok := body["include"].([]any); ok {
		s.IncludeCount = len(include)
	}
	s.ChoiceType = fakeWireType(body["tool_choice"]) == "string"
	s.ChoiceAuto = body["tool_choice"] == "auto"
	s.ParallelOK = body["parallel_tool_calls"] == true
	s.ParallelValue, _ = body["parallel_tool_calls"].(bool)
	if text, ok := body["text"].(map[string]any); ok {
		s.TextType = true
		for key := range text {
			s.TextKeys = append(s.TextKeys, key)
		}
		slices.Sort(s.TextKeys)
		s.TextLow = reflect.DeepEqual(text, map[string]any{"verbosity": "low"})
	}
	if reasoning, ok := body["reasoning"].(map[string]any); ok {
		s.ReasoningType = true
		for key := range reasoning {
			s.ReasoningKeys = append(s.ReasoningKeys, key)
		}
		slices.Sort(s.ReasoningKeys)
		s.ReasoningEffortType = fakeWireType(reasoning["effort"])
		s.ReasoningNone = reflect.DeepEqual(reasoning, map[string]any{"effort": "none"})
	}
	if input, ok := body["input"].([]any); ok {
		s.InputCount = len(input)
		for _, item := range input {
			kind := fakeWireType(item)
			if m, ok := item.(map[string]any); ok {
				if m["role"] == "user" {
					if content, ok := m["content"].([]any); ok && len(content) == 1 {
						if part, ok := content[0].(map[string]any); ok && part["type"] == "input_text" && part["text"] == "Synthetic user." {
							kind = "user/input_text/synthetic"
						}
					}
				}
			}
			s.InputTypes = append(s.InputTypes, kind)
		}
	}
	if tools, ok := body["tools"].([]any); ok {
		s.ToolCount = len(tools)
		for _, entry := range tools {
			if tool, ok := entry.(map[string]any); ok {
				// Store only the fixed allowlisted type, never an arbitrary tool value.
				kind := "other"
				if tool["type"] == "function" {
					kind = "function"
				}
				s.ToolTypes = append(s.ToolTypes, kind)
				s.ToolNameOK = tool["name"] == "synthetic_tool"
				if strict, exists := tool["strict"]; exists {
					s.StrictPresent = true
					s.StrictType = fakeWireType(strict)
					s.StrictNull = strict == nil
				}
				if schema, ok := tool["parameters"].(map[string]any); ok {
					if schema["type"] == "object" {
						s.SchemaType = "object"
					}
					if props, ok := schema["properties"].(map[string]any); ok {
						if value, ok := props["value"].(map[string]any); ok && value["type"] == "string" {
							s.PropertyType = "string"
						}
					}
					if required, ok := schema["required"].([]any); ok {
						s.RequiredValueOK = reflect.DeepEqual(required, []any{"value"})
					}
				}
			} else {
				s.ToolTypes = append(s.ToolTypes, fakeWireType(entry))
			}
		}
	}
	d.shape = s
	status := 200
	payload := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"synthetic_response\"}}\n\ndata: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"message\",\"id\":\"synthetic_item\"}}\n\ndata: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":\"Synthetic done.\"}\n\ndata: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"message\",\"id\":\"synthetic_item\",\"content\":[{\"type\":\"output_text\",\"text\":\"Synthetic done.\"}]}}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"synthetic_response\",\"status\":\"completed\"}}\n\n"
	if d.mode == "401" {
		status = 401
		payload = ""
	}
	if d.mode == "partial" {
		payload = "data: {\"type\":\"response.created\",\"response\":{\"id\":\"synthetic_response\"}}\n\ndata: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"message\",\"id\":\"synthetic_item\"}}\n\ndata: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":\"Synthetic partial.\"}\n\ndata: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"code\":\"invalid_token\"}}}\n\n"
	}
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(payload)), Request: r}, nil
}

type fakeWireError struct{}

func (*fakeWireError) Error() string { return "offline fake-wire request rejected" }

func TestDisposableCodexFakeWireParity(t *testing.T) {
	model := ai.GetModel("openai-codex", "gpt-6-sol")
	if model == nil || model.Api != ai.APIOpenAICodexResponses {
		t.Fatal("exact registered model unavailable")
	}
	local := *model
	providers.RegisterOpenAICodexResponses()
	passed := true
	cases := make([]fakeWireReceiptCase, 0, 5)
	for _, tc := range []struct {
		name, label string
		tool        bool
		mode        string
		empty       bool
	}{
		{name: "no-tool", label: "nonempty_no_tool"}, {name: "ordinary-function", label: "nonempty_one_tool", tool: true}, {name: "empty-no-tool", label: "empty_no_tool", empty: true}, {name: "pre-start-401", label: "pre_start_401", mode: "401"}, {name: "partial-sse-no-replay", label: "partial_sse_no_replay", mode: "partial"},
	} {
		if !t.Run(tc.name, func(t *testing.T) {
			d := &fakeWireDoer{mode: tc.mode}
			input := ai.Context{SystemPrompt: "Synthetic system.", Messages: []ai.Message{ai.NewUserText("Synthetic user.", 1)}}
			if tc.empty {
				input.SystemPrompt = ""
			}
			if tc.tool {
				input.Tools = []ai.Tool{{Name: "synthetic_tool", Description: "Synthetic function.", Parameters: ai.Object(ai.Prop("value", ai.String()))}}
			}
			opts := &ai.SimpleStreamOptions{StreamOptions: ai.StreamOptions{ProviderRequestOptions: ai.ProviderRequestOptions{
				CodexAuth: func(context.Context) (ai.ModelAuth, error) {
					return ai.ModelAuth{APIKey: "synthetic-bearer", Headers: ai.ProviderHeaders{"ChatGPT-Account-ID": ai.HeaderValue("synthetic-account")}}, nil
				},
				HTTPClient: d, MaxRetries: 4,
			}}}
			stream := ai.StreamSimple(context.Background(), &local, input, opts)
			var events []ai.EventType
			for event := range stream.Events() {
				events = append(events, event.Type)
			}
			final := stream.Result()
			if d.calls != 1 {
				t.Fatal("fake request count mismatch")
			}
			s := d.shape
			if !reflect.DeepEqual(s.HeaderNames, []string{"Accept", "Authorization", "Chatgpt-Account-Id", "Content-Type", "Openai-Beta", "Originator", "User-Agent"}) || !s.AuthOK || !s.AccountOK || !s.AcceptOK || !s.ContentTypeOK || !s.OriginatorOK || !s.UserAgentOK || !s.BetaOK {
				t.Fatal("fixed header-name or allowlisted header checks mismatch")
			}
			expectedKeys := []string{"include", "input", "instructions", "model", "parallel_tool_calls", "reasoning", "store", "stream", "text", "tool_choice"}
			expectedTypes := map[string]string{"include": "array", "input": "array", "instructions": "string", "model": "string", "parallel_tool_calls": "boolean", "reasoning": "object", "store": "boolean", "stream": "boolean", "text": "object", "tool_choice": "string"}
			if tc.tool {
				expectedKeys = append(expectedKeys, "tools")
				expectedTypes["tools"] = "array"
			}
			if !reflect.DeepEqual(s.Keys, expectedKeys) || !reflect.DeepEqual(s.Types, expectedTypes) {
				t.Fatal("body key/type shape mismatch")
			}
			if !s.ModelOK || !s.StreamOK || !s.StoreOK || !s.IncludeOK || !s.ParallelOK || !s.ChoiceType || !s.ChoiceAuto || !s.TextType || !s.TextLow || !reflect.DeepEqual(s.TextKeys, []string{"verbosity"}) || !s.ReasoningType || !s.ReasoningNone || !reflect.DeepEqual(s.ReasoningKeys, []string{"effort"}) || s.InputCount != 1 || !reflect.DeepEqual(s.InputTypes, []string{"user/input_text/synthetic"}) {
				t.Fatal("fixed model/input/include/boolean checks mismatch")
			}
			if tc.empty {
				// Both adapters serialize the nonempty fallback without a system prompt.
				if s.InstructionsCategory != "empty_fallback" {
					t.Fatal("empty Go instruction category mismatch")
				}
			} else if !s.InstructionsOK || s.InstructionsCategory != "nonempty_synthetic" {
				t.Fatal("nonempty instruction category mismatch")
			}
			if tc.tool {
				if s.Types["tools"] != "array" || s.ToolCount != 1 || !reflect.DeepEqual(s.ToolTypes, []string{"function"}) || !s.ToolNameOK || s.SchemaType != "object" || s.PropertyType != "string" || !s.RequiredValueOK || !s.StrictPresent || s.StrictType != "null" || !s.StrictNull {
					t.Fatal("ordinary tool shape mismatch")
				}
			} else if s.ToolCount != 0 || s.StrictPresent {
				t.Fatal("unexpected tool or strict field")
			}
			if final == nil {
				t.Fatal("missing terminal result")
			}
			if tc.mode == "" {
				if final.StopReason != ai.StopStop || len(final.Content) != 1 || !reflect.DeepEqual(events, []ai.EventType{ai.EventStart, ai.EventTextStart, ai.EventTextDelta, ai.EventTextEnd, ai.EventDone}) {
					t.Fatal("completed SSE terminal mismatch")
				}
				if text, ok := final.Content[0].(ai.TextContent); !ok || text.Text != "Synthetic done." {
					t.Fatal("terminal output mismatch")
				}
			} else if tc.mode == "401" {
				if final.StopReason != ai.StopError || !reflect.DeepEqual(events, []ai.EventType{ai.EventError}) {
					t.Fatal("pre-start 401 terminal/replay mismatch")
				}
			} else {
				if final.StopReason != ai.StopError || !slices.Contains(events, ai.EventTextDelta) || events[len(events)-1] != ai.EventError || slices.Contains(events, ai.EventDone) {
					t.Fatal("partial stream terminal/replay mismatch")
				}
			}
			terminal := "done:stop"
			if tc.mode == "401" {
				terminal = "error:401:no_replay"
			}
			if tc.mode == "partial" {
				terminal = "error:partial:no_replay"
			}
			cases = append(cases, newFakeWireReceiptCase(tc.label, s, terminal, d.calls))
		}) {
			passed = false
			break
		}
	}
	if !passed {
		return
	}
	if !t.Run("effective-router-selection", checkDisposableCodexEffectiveRouterSelection) {
		return
	}
	writeDisposableCodexReceipt(t, cases)
}

func checkDisposableCodexEffectiveRouterSelection(t *testing.T) {
	catalog := Catalog{Models: []Model{{Provider: "openai-codex", ID: "gpt-6-sol", Available: true}}}
	implicit, err := SelectModel(nil, nil, catalog)
	if err != nil || implicit.Provider != "openai-codex" || implicit.ID != "gpt-5.6-sol" {
		t.Fatal("omitted router model selection mismatch")
	}
	explicit, err := SelectModel(&Model{Provider: "openai-codex", ID: "gpt-6-sol"}, nil, catalog)
	if err != nil || explicit.Provider != "openai-codex" || explicit.ID != "gpt-6-sol" {
		t.Fatal("explicit router model selection mismatch")
	}
}

// Only sanitized, validated observations reach the receipt; no raw header,
// instruction, credential, request body, or provider identifier is retained.
type fakeWireReceiptCase struct {
	Label                      string            `json:"label"`
	HeaderNames                []string          `json:"headerNames"`
	AllowedHeaders             map[string]string `json:"allowedHeaders"`
	CredentialMatchesSynthetic bool              `json:"credentialMatchesSynthetic"`
	BodyKeys                   []string          `json:"bodyKeys"`
	BodyTypes                  map[string]string `json:"bodyTypes"`
	ModelMatchesApproved       bool              `json:"modelMatchesApproved"`
	InstructionCategory        string            `json:"instructionCategory"`
	InputCount                 int               `json:"inputCount"`
	InputTypes                 []string          `json:"inputTypes"`
	Flags                      map[string]bool   `json:"flags"`
	Tool                       any               `json:"tool"`
	Optional                   map[string]any    `json:"optional"`
	Terminal                   string            `json:"terminal"`
	FetchCount                 int               `json:"fetchCount"`
}

func newFakeWireReceiptCase(label string, s fakeWireShape, terminal string, calls int) fakeWireReceiptCase {
	var tool any
	if s.ToolCount == 1 {
		tool = map[string]any{"type": s.ToolTypes[0], "strictPresent": s.StrictPresent, "strictType": s.StrictType, "strictValue": nil, "parametersType": s.SchemaType}
	}
	return fakeWireReceiptCase{
		Label: label, HeaderNames: s.HeaderNames,
		AllowedHeaders:             map[string]string{"accept": "text/event-stream", "contentType": "application/json", "openaiBeta": "responses=experimental", "originator": "pi"},
		CredentialMatchesSynthetic: s.AuthOK && s.AccountOK,
		BodyKeys:                   s.Keys, BodyTypes: s.Types, ModelMatchesApproved: s.ModelOK,
		InstructionCategory: s.InstructionsCategory, InputCount: s.InputCount, InputTypes: s.InputTypes,
		Flags: map[string]bool{"store": s.StoreValue, "stream": s.StreamValue, "parallelToolCalls": s.ParallelValue}, Tool: tool,
		Optional: map[string]any{
			"reasoning":             map[string]any{"keys": s.ReasoningKeys, "effortType": s.ReasoningEffortType, "effortIsNone": s.ReasoningNone},
			"promptCacheKeyPresent": slices.Contains(s.Keys, "prompt_cache_key"),
			"textKeys":              s.TextKeys, "includeCount": s.IncludeCount, "toolChoiceType": s.Types["tool_choice"],
		},
		Terminal: terminal, FetchCount: calls,
	}
}

// Written only after every synthetic subtest and router selection assertion passes.
func writeDisposableCodexReceipt(t *testing.T, cases []fakeWireReceiptCase) {
	dir := os.Getenv("GO_WIRE_RECEIPT_DIR")
	info, err := os.Lstat(dir)
	if dir == "" || err != nil || !info.IsDir() || info.Mode().Perm() != 0700 || info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("private receipt directory invalid")
	}
	receipt := struct {
		Status              string                `json:"status"`
		SelectedModel       string                `json:"selectedModel"`
		OmittedRouterModel  string                `json:"omittedRouterModel"`
		ExplicitRouterModel string                `json:"explicitRouterModel"`
		Cases               []fakeWireReceiptCase `json:"cases"`
	}{"passed", "gpt-6-sol", "gpt-5.6-sol", "gpt-6-sol", cases}
	data, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		t.Fatal("receipt encoding failed")
	}
	for _, sentinel := range []string{"synthetic-bearer", "synthetic-account", "Synthetic system.", "Synthetic user.", "Synthetic function.", "Synthetic done."} {
		if strings.Contains(string(data), sentinel) {
			t.Fatal("receipt sentinel exclusion failed")
		}
	}
	file, err := os.OpenFile(filepath.Join(dir, "result.json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal("exclusive receipt creation failed")
	}
	if _, err = file.Write(data); err != nil {
		file.Close()
		t.Fatal("receipt write failed")
	}
	if err = file.Close(); err != nil {
		t.Fatal("receipt close failed")
	}
}
