package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
)

const (
	// MaxOutputBytes limits tool output to 512 KiB per call to prevent worker memory exhaustion.
	MaxOutputBytes = 512 * 1024
	JSONRPCVersion = "2.0"
)

var (
	ErrUnauthorized = errors.New("mcp: unauthorized (401)")
)

// Client implements Streamable HTTP transport for remote MCP servers.
type Client struct {
	mcpURL         string
	getAccessToken func(ctx context.Context) (string, error)
	httpClient     *http.Client
	requestCounter uint64
}

// NewClient returns a new MCP Client configured with the given endpoint and token getter.
func NewClient(mcpURL string, getAccessToken func(ctx context.Context) (string, error), httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{
		mcpURL:         mcpURL,
		getAccessToken: getAccessToken,
		httpClient:     httpClient,
	}
}

func (c *Client) nextID() uint64 {
	return atomic.AddUint64(&c.requestCounter, 1)
}

func (c *Client) postJSONRPC(ctx context.Context, method string, params any) (*JSONRPCResponse, error) {
	reqID := c.nextID()
	rpcReq := JSONRPCRequest{
		JSONRPC: JSONRPCVersion,
		ID:      reqID,
		Method:  method,
		Params:  params,
	}

	bodyBytes, err := json.Marshal(rpcReq)
	if err != nil {
		return nil, fmt.Errorf("marshal json-rpc request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.mcpURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create http request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json, text/event-stream")

	if c.getAccessToken != nil {
		token, err := c.getAccessToken(ctx)
		if err != nil {
			return nil, fmt.Errorf("get access token: %w", err)
		}
		if token != "" {
			httpReq.Header.Set("Authorization", "Bearer "+token)
		}
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("dispatch http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, ErrUnauthorized
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("http error %d: %s", resp.StatusCode, string(errBody))
	}

	// Limit response reading to 4 MiB to protect against unbounded network streams,
	// while parsing complete JSON before truncating structured output to 512 KiB.
	lr := io.LimitReader(resp.Body, 4*1024*1024)
	respBytes, err := io.ReadAll(lr)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}

	var rpcResp JSONRPCResponse
	contentType := resp.Header.Get("Content-Type")
	trimmed := bytes.TrimSpace(respBytes)

	if strings.Contains(contentType, "text/event-stream") || bytes.HasPrefix(trimmed, []byte("event:")) || bytes.HasPrefix(trimmed, []byte("data:")) {
		parsed, parseErr := parseSSEJSONRPCResponse(respBytes, reqID)
		if parseErr != nil {
			return nil, fmt.Errorf("decode sse json-rpc response: %w", parseErr)
		}
		rpcResp = *parsed
	} else {
		if err := json.Unmarshal(respBytes, &rpcResp); err != nil {
			return nil, fmt.Errorf("decode json-rpc response: %w", err)
		}
	}

	if rpcResp.Error != nil {
		return nil, rpcResp.Error
	}

	return &rpcResp, nil
}

// Initialize performs the protocol negotiation handshake and sends notifications/initialized.
func (c *Client) Initialize(ctx context.Context) error {
	initParams := InitializeParams{
		ProtocolVersion: "2024-11-05",
		Capabilities: map[string]any{
			"roots":    map[string]any{"listChanged": false},
			"sampling": map[string]any{},
		},
		ClientInfo: ClientInfo{
			Name:    "mecha-router",
			Version: "1.0.0",
		},
	}

	resp, err := c.postJSONRPC(ctx, "initialize", initParams)
	if err != nil {
		return fmt.Errorf("initialize handshake: %w", err)
	}

	var initResult InitializeResult
	if len(resp.Result) > 0 {
		if err := json.Unmarshal(resp.Result, &initResult); err != nil {
			return fmt.Errorf("parse initialize result: %w", err)
		}
	}

	// Complete handshake by sending notifications/initialized (notification has no ID)
	notif := JSONRPCRequest{
		JSONRPC: JSONRPCVersion,
		Method:  "notifications/initialized",
		Params:  map[string]any{},
	}
	notifBytes, _ := json.Marshal(notif)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.mcpURL, bytes.NewReader(notifBytes))
	if err == nil {
		httpReq.Header.Set("Content-Type", "application/json")
		if c.getAccessToken != nil {
			if token, _ := c.getAccessToken(ctx); token != "" {
				httpReq.Header.Set("Authorization", "Bearer "+token)
			}
		}
		// Send notification, ignore error or response
		if resp, err := c.httpClient.Do(httpReq); err == nil {
			_ = resp.Body.Close()
		}
	}

	return nil
}

// ListTools retrieves all available tools, handling pagination if nextCursor is present.
func (c *Client) ListTools(ctx context.Context) ([]MCPTool, error) {
	var allTools []MCPTool
	cursor := ""

	for {
		params := ListToolsParams{
			Cursor: cursor,
		}
		resp, err := c.postJSONRPC(ctx, "tools/list", params)
		if err != nil {
			return nil, fmt.Errorf("tools/list rpc: %w", err)
		}

		var listResult ListToolsResult
		if err := json.Unmarshal(resp.Result, &listResult); err != nil {
			return nil, fmt.Errorf("parse tools/list result: %w", err)
		}

		allTools = append(allTools, listResult.Tools...)

		if listResult.NextCursor == "" || listResult.NextCursor == cursor {
			break
		}
		cursor = listResult.NextCursor
	}

	return allTools, nil
}

// CallTool invokes a tool by name with arguments, enforcing 512 KiB truncation on outputs.
func (c *Client) CallTool(ctx context.Context, name string, args map[string]any) (*ToolCallResult, error) {
	callParams := CallToolParams{
		Name:      name,
		Arguments: args,
	}

	resp, err := c.postJSONRPC(ctx, "tools/call", callParams)
	if err != nil {
		return nil, err
	}

	var callResult ToolCallResult
	if err := json.Unmarshal(resp.Result, &callResult); err != nil {
		return nil, fmt.Errorf("parse tools/call result: %w", err)
	}

	// Enforce 512 KiB truncation limit on outputs
	totalBytes := 0
	for i := range callResult.Content {
		item := &callResult.Content[i]
		itemLen := len(item.Text) + len(item.Data)
		if totalBytes+itemLen > MaxOutputBytes {
			remaining := MaxOutputBytes - totalBytes
			if remaining <= 0 {
				item.Text = "[truncated: tool output exceeded 512 KiB limit]"
				item.Data = ""
			} else {
				if len(item.Text) > remaining {
					item.Text = item.Text[:remaining] + "\n...[truncated: output exceeded 512 KiB limit]"
				}
				if len(item.Data) > remaining {
					item.Data = ""
				}
			}
			// Truncate remaining items
			callResult.Content = callResult.Content[:i+1]
			break
		}
		totalBytes += itemLen
	}

	return &callResult, nil
}

// parseSSEJSONRPCResponse extracts and parses a JSON-RPC response from an SSE stream.
func parseSSEJSONRPCResponse(body []byte, reqID uint64) (*JSONRPCResponse, error) {
	lines := strings.Split(string(body), "\n")
	var (
		dataLines    []string
		matchingResp *JSONRPCResponse
		fallbackResp *JSONRPCResponse
	)

	processEvent := func() {
		if len(dataLines) == 0 {
			return
		}
		data := strings.Join(dataLines, "\n")
		dataLines = nil

		var candidate JSONRPCResponse
		if err := json.Unmarshal([]byte(data), &candidate); err == nil && candidate.JSONRPC != "" {
			switch id := candidate.ID.(type) {
			case float64:
				if uint64(id) == reqID {
					matchingResp = &candidate
				}
			case string:
				if fmt.Sprintf("%d", reqID) == id {
					matchingResp = &candidate
				}
			case json.Number:
				if n, err := id.Int64(); err == nil && n >= 0 && uint64(n) == reqID {
					matchingResp = &candidate
				}
			default:
				if candidate.ID == reqID {
					matchingResp = &candidate
				}
			}

			if matchingResp == nil && fallbackResp == nil && (len(candidate.Result) > 0 || candidate.Error != nil) {
				fallbackResp = &candidate
			}
		}
	}

	for _, line := range lines {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			processEvent()
			if matchingResp != nil {
				return matchingResp, nil
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			// SSE comment
			continue
		}
		if strings.HasPrefix(line, "data:") {
			val := strings.TrimPrefix(line, "data:")
			val = strings.TrimPrefix(val, " ")
			dataLines = append(dataLines, val)
		}
	}
	processEvent()

	if matchingResp != nil {
		return matchingResp, nil
	}
	if fallbackResp != nil {
		return fallbackResp, nil
	}

	return nil, fmt.Errorf("no valid json-rpc response found in sse stream")
}

