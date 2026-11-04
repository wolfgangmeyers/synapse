package managed

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"
)

type claudePrivateMCPListener struct {
	url      string
	server   *http.Server
	listener net.Listener
	done     chan struct{}
}

// ListenPrivate starts one loopback-only MCP endpoint for the manager lifetime.
// A stopped or failed endpoint is never silently replaced or redirected.
func (s *ClaudeCodeMCPServer) ListenPrivate(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if s.closed {
		return "", errors.New("Claude MCP server closed")
	}
	if s.private != nil {
		select {
		case <-s.private.done:
			return "", errors.New("private Claude MCP listener stopped")
		default:
			return s.private.url, nil
		}
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	mux := http.NewServeMux()
	mux.Handle("/v1/mcp", s)
	p := &claudePrivateMCPListener{url: "http://" + listener.Addr().String() + "/v1/mcp", server: &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}, listener: listener, done: make(chan struct{})}
	s.private = p
	go func() {
		defer close(p.done)
		_ = p.server.Serve(listener)
		s.Close()
		_ = p.server.Close()
	}()
	context.AfterFunc(ctx, func() { _ = p.server.Close(); s.Close() })
	return p.url, nil
}

// closeClaudePrivateMCP joins listener shutdown without holding manager or server locks.
func (m *Manager) closeClaudePrivateMCP() {
	m.mu.Lock()
	s := m.claudeCodeMCP
	m.mu.Unlock()
	if s == nil {
		return
	}
	s.mu.Lock()
	p := s.private
	s.mu.Unlock()
	if p != nil {
		_ = p.server.Close()
		<-p.done
	}
	s.Close()
}

// ClaudeCodeMCPURL is the only supported Claude process MCP endpoint.
func (m *Manager) ClaudeCodeMCPURL() (string, error) {
	m.mu.Lock()
	ctx := m.runCtx
	closed := m.closed
	m.mu.Unlock()
	if closed || ctx == nil {
		return "", errors.New("Claude MCP manager is not running")
	}
	return m.ClaudeCodeMCP().ListenPrivate(ctx)
}
