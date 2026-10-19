package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClientInitialize(t *testing.T) {
	var initCalled bool
	var notifCalled bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-access-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		var req JSONRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		if req.Method == "initialize" {
			initCalled = true
			res := InitializeResult{
				ProtocolVersion: "2024-11-05",
				Capabilities:    map[string]any{"tools": map[string]any{}},
				ServerInfo:      ServerInfo{Name: "test-server", Version: "1.0"},
			}
			rawRes, _ := json.Marshal(res)
			_ = json.NewEncoder(w).Encode(JSONRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result:  rawRes,
			})
			return
		}

		if req.Method == "notifications/initialized" {
			notifCalled = true
			w.WriteHeader(http.StatusOK)
			return
		}

		http.NotFound(w, r)
	}))
	defer server.Close()

	ctx := context.Background()
	client := NewClient(server.URL, func(ctx context.Context) (string, error) {
		return "test-access-token", nil
	}, http.DefaultClient)

	err := client.Initialize(ctx)
	if err != nil {
		t.Fatalf("Initialize failed: %v", err)
	}

	if !initCalled {
		t.Errorf("expected initialize to be called")
	}
	if !notifCalled {
		t.Errorf("expected notifications/initialized to be called")
	}
}

func TestClientListToolsWithPagination(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req JSONRPCRequest
		_ = json.NewDecoder(r.Body).Decode(&req)

		if req.Method == "tools/list" {
			var params ListToolsParams
			if req.Params != nil {
				b, _ := json.Marshal(req.Params)
				_ = json.Unmarshal(b, &params)
			}

			w.Header().Set("Content-Type", "application/json")
			if params.Cursor == "" {
				res := ListToolsResult{
					Tools: []MCPTool{
						{Name: "tool1", Description: "first tool", InputSchema: json.RawMessage(`{"type":"object"}`)},
					},
					NextCursor: "cursor-page-2",
				}
				raw, _ := json.Marshal(res)
				_ = json.NewEncoder(w).Encode(JSONRPCResponse{
					JSONRPC: "2.0",
					ID:      req.ID,
					Result:  raw,
				})
				return
			} else if params.Cursor == "cursor-page-2" {
				res := ListToolsResult{
					Tools: []MCPTool{
						{Name: "tool2", Description: "second tool", InputSchema: json.RawMessage(`{"type":"object"}`)},
					},
					NextCursor: "",
				}
				raw, _ := json.Marshal(res)
				_ = json.NewEncoder(w).Encode(JSONRPCResponse{
					JSONRPC: "2.0",
					ID:      req.ID,
					Result:  raw,
				})
				return
			}
		}

		http.NotFound(w, r)
	}))
	defer server.Close()

	ctx := context.Background()
	client := NewClient(server.URL, nil, http.DefaultClient)

	tools, err := client.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools failed: %v", err)
	}

	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools))
	}
	if tools[0].Name != "tool1" || tools[1].Name != "tool2" {
		t.Errorf("unexpected tools: %+v", tools)
	}
}

func TestClientCallTool(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req JSONRPCRequest
		_ = json.NewDecoder(r.Body).Decode(&req)

		if req.Method == "tools/call" {
			var params CallToolParams
			b, _ := json.Marshal(req.Params)
			_ = json.Unmarshal(b, &params)

			w.Header().Set("Content-Type", "application/json")
			if params.Name == "echo" {
				res := ToolCallResult{
					Content: []ToolCallContent{
						{Type: "text", Text: fmt.Sprintf("hello %v", params.Arguments["msg"])},
					},
				}
				raw, _ := json.Marshal(res)
				_ = json.NewEncoder(w).Encode(JSONRPCResponse{
					JSONRPC: "2.0",
					ID:      req.ID,
					Result:  raw,
				})
				return
			}
		}

		http.NotFound(w, r)
	}))
	defer server.Close()

	ctx := context.Background()
	client := NewClient(server.URL, nil, http.DefaultClient)

	res, err := client.CallTool(ctx, "echo", map[string]any{"msg": "world"})
	if err != nil {
		t.Fatalf("CallTool failed: %v", err)
	}

	if len(res.Content) != 1 || res.Content[0].Text != "hello world" {
		t.Errorf("unexpected tool result: %+v", res)
	}
}

func TestClientCallToolTruncation512KiB(t *testing.T) {
	// Generate output exceeding 512 KiB (e.g. 600 KiB)
	largeOutput := strings.Repeat("a", 600*1024)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req JSONRPCRequest
		_ = json.NewDecoder(r.Body).Decode(&req)

		if req.Method == "tools/call" {
			res := ToolCallResult{
				Content: []ToolCallContent{
					{Type: "text", Text: largeOutput},
				},
			}
			raw, _ := json.Marshal(res)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(JSONRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result:  raw,
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	ctx := context.Background()
	client := NewClient(server.URL, nil, http.DefaultClient)

	res, err := client.CallTool(ctx, "huge_tool", nil)
	if err != nil {
		t.Fatalf("CallTool failed: %v", err)
	}

	if len(res.Content) == 0 {
		t.Fatalf("expected content, got none")
	}

	// Verify truncation
	totalLen := len(res.Content[0].Text)
	if totalLen > MaxOutputBytes+100 { // MaxOutputBytes + marker length
		t.Errorf("output not truncated! length: %d, expected <= %d", totalLen, MaxOutputBytes+100)
	}
	if !strings.Contains(res.Content[0].Text, "truncated") {
		t.Errorf("expected truncated notice in output")
	}
}

func TestClientUnauthorizedError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	ctx := context.Background()
	client := NewClient(server.URL, nil, http.DefaultClient)

	_, err := client.CallTool(ctx, "any_tool", nil)
	if !errors.Is(err, ErrUnauthorized) {
		t.Errorf("expected ErrUnauthorized, got %v", err)
	}
}

func TestClientJSONRPCError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req JSONRPCRequest
		_ = json.NewDecoder(r.Body).Decode(&req)

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: &JSONRPCError{
				Code:    -32601,
				Message: "Method not found",
			},
		})
	}))
	defer server.Close()

	ctx := context.Background()
	client := NewClient(server.URL, nil, http.DefaultClient)

	_, err := client.CallTool(ctx, "nonexistent", nil)
	if err == nil || !strings.Contains(err.Error(), "Method not found") {
		t.Errorf("expected Method not found error, got %v", err)
	}
}

func TestClientSSEResponseDecoding(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req JSONRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		if req.Method == "tools/call" {
			res := ToolCallResult{
				Content: []ToolCallContent{
					{Type: "text", Text: "sse streamed tool result"},
				},
			}
			rawRes, _ := json.Marshal(res)
			rpcResp := JSONRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result:  rawRes,
			}
			data, _ := json.Marshal(rpcResp)

			w.Header().Set("Content-Type", "text/event-stream")
			// Send SSE format with comments and multiple events
			fmt.Fprintf(w, ": heartbeat\n\n")
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", string(data))
			return
		}

		http.NotFound(w, r)
	}))
	defer server.Close()

	ctx := context.Background()
	client := NewClient(server.URL, nil, http.DefaultClient)

	res, err := client.CallTool(ctx, "stream_tool", map[string]any{"key": "val"})
	if err != nil {
		t.Fatalf("CallTool with SSE response failed: %v", err)
	}

	if len(res.Content) != 1 || res.Content[0].Text != "sse streamed tool result" {
		t.Errorf("unexpected SSE tool call result: %+v", res)
	}
}

