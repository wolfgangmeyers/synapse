package managed

// Claude's HTTP boundary owns grants and wire validation only. Execution,
// deduplication, producer rendezvous and persisted replay belong to the shared
// turn authority callback. There is intentionally no effect/result cache here.
import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

const claudeMCPMaxBody = 6 << 20
const claudeMCPMaxGrants = 1024

type ClaudeCodeMCPTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// ClaudeCodeMCPCall is scoped to one process, never merely an agent ID. Name
// is canonical (without mcp__mecha__). Arguments retain the original JSON.
type ClaudeCodeMCPCall struct {
	Binding                             store.EmbeddedBinding
	Owner, Incarnation, ToolUseID, Name string
	Arguments                           json.RawMessage
}

// ExecuteChecked MUST serialize with the actual turn's persistence, compare
// original canonical arguments before admission, wait <=5s for an actual
// checked main-thread producer, and use shared admission/witness/Execute and
// checked result append. Only checked persisted results may be replayed.
// UNKNOWN must return an error without Execute. Idle read-only status still
// needs current ownership; idle effects need a real admitted background turn.
// context_reset/goal_snooze/mecha_worker_terminate require a foreground turn.
// The callback owns schema/workflow/turn admission; a grant is not a turn.
type ClaudeCodeMCPGrant struct {
	Binding            store.EmbeddedBinding
	Owner, Incarnation string
	Tools              []ClaudeCodeMCPTool
	ExecuteChecked     func(context.Context, ClaudeCodeMCPCall) (json.RawMessage, error)
}
type claudeMCPGrant struct {
	spec   ClaudeCodeMCPGrant
	ctx    context.Context
	cancel context.CancelFunc
	serial chan struct{}
}

// ClaudeCodeMCPServer retains at most 1024 active process grants, no history.
// Issue replaces an earlier grant for the same agent. Revoke on process exit,
// reset or stop; manager context cancellation clears remaining grants.
type ClaudeCodeMCPServer struct {
	db      *store.Store
	mu      sync.Mutex
	grants  map[string]*claudeMCPGrant
	private *claudePrivateMCPListener
	closed  bool
}

func (m *Manager) ClaudeCodeMCP() *ClaudeCodeMCPServer {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.claudeCodeMCP == nil {
		m.claudeCodeMCP = &ClaudeCodeMCPServer{db: m.store, grants: make(map[string]*claudeMCPGrant)}
		if m.runCtx != nil {
			context.AfterFunc(m.runCtx, m.claudeCodeMCP.Close)
		}
	}
	return m.claudeCodeMCP
}
func (s *ClaudeCodeMCPServer) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	for token, g := range s.grants {
		g.cancel()
		delete(s.grants, token)
	}
}
func (s *ClaudeCodeMCPServer) Issue(ctx context.Context, spec ClaudeCodeMCPGrant) (string, func(), error) {
	if spec.ExecuteChecked == nil || spec.Owner == "" || spec.Incarnation == "" || len(spec.Incarnation) > 256 || len(spec.Tools) > 128 {
		return "", nil, errors.New("invalid Claude MCP grant")
	}
	if err := s.db.RenewEmbeddedOwner(ctx, spec.Binding, spec.Owner, embeddedOwnerTTL); err != nil {
		return "", nil, err
	}
	seen := map[string]bool{}
	copied := make([]ClaudeCodeMCPTool, len(spec.Tools))
	for i, tool := range spec.Tools {
		if !claudeMCPName(tool.Name) || seen[tool.Name] || !json.Valid(tool.InputSchema) {
			return "", nil, errors.New("invalid Claude MCP tool catalog")
		}
		seen[tool.Name] = true
		copied[i] = tool
		copied[i].InputSchema = append(json.RawMessage(nil), tool.InputSchema...)
	}
	spec.Tools = copied
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", nil, err
	}
	token := hex.EncodeToString(secret)
	grantCtx, cancel := context.WithCancel(ctx)
	g := &claudeMCPGrant{spec: spec, ctx: grantCtx, cancel: cancel, serial: make(chan struct{}, 1)}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		cancel()
		return "", nil, errors.New("Claude MCP server closed")
	}
	for old, grant := range s.grants {
		if grant.ctx.Err() != nil || grant.spec.Binding.AgentID == spec.Binding.AgentID {
			grant.cancel()
			delete(s.grants, old)
		}
	}
	if len(s.grants) >= claudeMCPMaxGrants {
		s.mu.Unlock()
		cancel()
		return "", nil, errors.New("Claude MCP grant capacity")
	}
	s.grants[token] = g
	s.mu.Unlock()
	revoke := func() {
		s.mu.Lock()
		if s.grants[token] == g {
			delete(s.grants, token)
			g.cancel()
		}
		s.mu.Unlock()
	}
	context.AfterFunc(grantCtx, revoke)
	return token, revoke, nil
}
func claudeMCPName(name string) bool {
	if name == "" || len(name) > 128 || strings.HasPrefix(name, "mcp__") {
		return false
	}
	for _, c := range name {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_') {
			return false
		}
	}
	return true
}
func claudeMCPReply(w http.ResponseWriter, status int, id json.RawMessage, result any, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	out := map[string]any{"jsonrpc": "2.0", "id": id}
	if code != 0 {
		out["error"] = map[string]any{"code": code, "message": message}
	} else {
		out["result"] = result
	}
	_ = json.NewEncoder(w).Encode(out)
}

// The endpoint advertises the stateless 2025-03-26 protocol subset.
func claudeMCPObject(raw json.RawMessage) map[string]json.RawMessage {
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return nil
	}
	return obj
}
func claudeMCPString(raw json.RawMessage) bool {
	var value string
	return len(raw) > 0 && string(raw) != "null" && json.Unmarshal(raw, &value) == nil
}
func claudeMCPInitializeValid(raw json.RawMessage) bool {
	p := claudeMCPObject(raw)
	info := claudeMCPObject(p["clientInfo"])
	if p == nil || !claudeMCPString(p["protocolVersion"]) || info == nil || !claudeMCPString(info["name"]) || !claudeMCPString(info["version"]) {
		return false
	}
	caps := claudeMCPObject(p["capabilities"])
	if caps == nil {
		return false
	}
	for _, key := range []string{"experimental", "roots", "sampling"} {
		raw, exists := caps[key]
		if !exists {
			continue
		}
		obj := claudeMCPObject(raw)
		if obj == nil {
			return false
		}
		if key == "experimental" {
			for _, value := range obj {
				if claudeMCPObject(value) == nil {
					return false
				}
			}
		}
		if key == "roots" {
			if value, exists := obj["listChanged"]; exists {
				var b bool
				if string(value) == "null" || json.Unmarshal(value, &b) != nil {
					return false
				}
			}
		}
	}
	return true
}
func claudeMCPPingValid(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return true
	}
	p := claudeMCPObject(raw)
	if p == nil {
		return false
	}
	if meta, exists := p["_meta"]; exists {
		m := claudeMCPObject(meta)
		if m == nil {
			return false
		}
		if progress, exists := m["progressToken"]; exists {
			var v any
			if json.Unmarshal(progress, &v) != nil {
				return false
			}
			switch v.(type) {
			case string, float64:
			default:
				return false
			}
		}
	}
	return true
}
func (s *ClaudeCodeMCPServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		claudeMCPReply(w, 405, nil, nil, -32600, "POST required")
		return
	}
	auth := r.Header.Get("Authorization")
	token, ok := strings.CutPrefix(auth, "Bearer ")
	s.mu.Lock()
	g := s.grants[token]
	s.mu.Unlock()
	if !ok || g == nil || g.ctx.Err() != nil {
		claudeMCPReply(w, 401, nil, nil, -32001, "invalid process grant")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	stop := context.AfterFunc(g.ctx, cancel)
	defer stop()
	// All requests, including catalog reads, confirm exact persisted ownership.
	if err := s.db.RenewEmbeddedOwner(ctx, g.spec.Binding, g.spec.Owner, embeddedOwnerTTL); err != nil {
		s.mu.Lock()
		if s.grants[token] == g {
			delete(s.grants, token)
			g.cancel()
		}
		s.mu.Unlock()
		claudeMCPReply(w, 401, nil, nil, -32001, "stale process grant")
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, claudeMCPMaxBody))
	if err != nil {
		claudeMCPReply(w, 413, nil, nil, -32600, "request exceeds limit")
		return
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		var entries []json.RawMessage
		if json.Unmarshal(trimmed, &entries) != nil || len(entries) == 0 {
			claudeMCPReply(w, 400, nil, nil, -32600, "invalid JSON-RPC batch")
			return
		}
		responses := make([]json.RawMessage, 0, len(entries))
		for _, entry := range entries {
			capture := &claudeMCPBatchWriter{header: make(http.Header)}
			s.handleMCPMessage(capture, ctx, g, entry)
			if capture.body.Len() > 0 {
				responses = append(responses, json.RawMessage(bytes.TrimSpace(capture.body.Bytes())))
			}
		}
		if len(responses) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(responses)
		return
	}
	s.handleMCPMessage(w, ctx, g, raw)
}

type claudeMCPBatchWriter struct {
	header http.Header
	body   bytes.Buffer
}

func (w *claudeMCPBatchWriter) Header() http.Header         { return w.header }
func (w *claudeMCPBatchWriter) WriteHeader(int)             {}
func (w *claudeMCPBatchWriter) Write(p []byte) (int, error) { return w.body.Write(p) }

func (s *ClaudeCodeMCPServer) handleMCPMessage(w http.ResponseWriter, ctx context.Context, g *claudeMCPGrant, raw json.RawMessage) {
	// Client responses do not receive a response and cannot trigger effects.
	obj := claudeMCPObject(raw)
	if obj != nil && string(obj["jsonrpc"]) == `"2.0"` && len(obj["id"]) > 0 && len(obj["method"]) == 0 && (len(obj["result"]) > 0) != (len(obj["error"]) > 0) {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	var req struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}
	if json.Unmarshal(raw, &req) != nil || req.JSONRPC != "2.0" || req.Method == "" {
		claudeMCPReply(w, 400, nil, nil, -32600, "invalid JSON-RPC request")
		return
	}
	// Notifications have no JSON-RPC response and cannot invoke checked effects.
	if len(req.ID) == 0 {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	var identifier any
	if len(req.ID) == 0 || json.Unmarshal(req.ID, &identifier) != nil || identifier == nil {
		claudeMCPReply(w, 400, nil, nil, -32600, "request ID required")
		return
	}
	switch identifier.(type) {
	case string, float64:
	default:
		claudeMCPReply(w, 400, nil, nil, -32600, "invalid request ID")
		return
	}
	reply := func(result any) { claudeMCPReply(w, 200, req.ID, result, 0, "") }
	reject := func(code int, message string) { claudeMCPReply(w, 200, req.ID, nil, code, message) }
	switch req.Method {
	case "initialize":
		if !claudeMCPInitializeValid(req.Params) {
			reject(-32602, "invalid initialize parameters")
			return
		}
		reply(map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "mecha", "version": "1"}})
	case "ping":
		if !claudeMCPPingValid(req.Params) {
			reject(-32602, "invalid ping parameters")
			return
		}
		reply(map[string]any{})
	case "server/discover":
		reply(map[string]any{"serverInfo": map[string]string{"name": "mecha", "version": "1"}})
	case "tools/list":
		reply(map[string]any{"tools": g.spec.Tools})
	case "tools/call":
		var params struct {
			Name      string                     `json:"name"`
			Arguments json.RawMessage            `json:"arguments"`
			Meta      map[string]json.RawMessage `json:"_meta"`
		}
		if json.Unmarshal(req.Params, &params) != nil {
			reject(-32602, "invalid tool parameters")
			return
		}
		var toolID string
		if json.Unmarshal(params.Meta["claudecode/toolUseId"], &toolID) != nil || strings.TrimSpace(toolID) == "" || len(toolID) > 256 {
			reject(-32602, "claudecode/toolUseId required")
			return
		}
		name := strings.TrimPrefix(params.Name, "mcp__mecha__")
		found := false
		for _, tool := range g.spec.Tools {
			if tool.Name == name {
				found = true
				break
			}
		}
		var arguments map[string]json.RawMessage
		if !found || !claudeMCPName(name) || json.Unmarshal(params.Arguments, &arguments) != nil || arguments == nil {
			reject(-32602, "invalid tool or arguments")
			return
		}
		select {
		case g.serial <- struct{}{}:
			defer func() { <-g.serial }()
		case <-ctx.Done():
			reject(-32000, "tool request cancelled")
			return
		}
		// Recheck after serialization: a queued request cannot inherit an old owner.
		if g.ctx.Err() != nil || s.db.RenewEmbeddedOwner(ctx, g.spec.Binding, g.spec.Owner, embeddedOwnerTTL) != nil {
			reject(-32001, "stale process grant")
			return
		}
		result, err := g.spec.ExecuteChecked(ctx, ClaudeCodeMCPCall{Binding: g.spec.Binding, Owner: g.spec.Owner, Incarnation: g.spec.Incarnation, ToolUseID: toolID, Name: name, Arguments: append(json.RawMessage(nil), params.Arguments...)})
		if err != nil {
			reject(-32000, "checked tool execution rejected")
			return
		}
		if ctx.Err() != nil || g.ctx.Err() != nil || s.db.RenewEmbeddedOwner(ctx, g.spec.Binding, g.spec.Owner, embeddedOwnerTTL) != nil {
			reject(-32001, "stale process grant")
			return
		}
		var checked struct {
			Content []json.RawMessage `json:"content"`
		}
		if len(result) > claudeMCPMaxBody || json.Unmarshal(result, &checked) != nil || checked.Content == nil {
			reject(-32603, "invalid checked tool result")
			return
		}
		reply(result)
	default:
		reject(-32601, "method not found")
	}
}
