package managed

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestClaudePrivateMCPListenerLifecycle(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s := &ClaudeCodeMCPServer{grants: make(map[string]*claudeMCPGrant)}
	url, err := s.ListenPrivate(ctx)
	require.NoError(t, err)
	require.Regexp(t, `^http://127\.0\.0\.1:[0-9]+/v1/mcp$`, url)
	status, _ := mcpRequest(t, url, "invalid", "tools/list", nil)
	require.Equal(t, http.StatusUnauthorized, status)
	same, err := s.ListenPrivate(ctx)
	require.NoError(t, err)
	require.Equal(t, url, same)
	cancel()
	require.Eventually(t, func() bool {
		client := &http.Client{Timeout: 100 * time.Millisecond, Transport: &http.Transport{DisableKeepAlives: true}}
		resp, err := client.Get(url)
		if resp != nil {
			resp.Body.Close()
		}
		return err != nil
	}, time.Second, 10*time.Millisecond)
	_, err = s.ListenPrivate(ctx)
	require.Error(t, err)
}

func TestClaudePrivateMCPListenerCanceledContextFailsClosed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := &ClaudeCodeMCPServer{grants: make(map[string]*claudeMCPGrant)}
	url, err := s.ListenPrivate(ctx)
	require.Error(t, err)
	require.Empty(t, url)
}

func TestClaudePrivateMCPManagerCloseJoinsListenerAndRevokes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{runCtx: ctx, cancel: cancel}
	url, err := m.ClaudeCodeMCPURL()
	require.NoError(t, err)
	s := m.ClaudeCodeMCP()
	grantCtx, revoke := context.WithCancel(context.Background())
	s.mu.Lock()
	s.grants["test"] = &claudeMCPGrant{ctx: grantCtx, cancel: revoke}
	s.mu.Unlock()
	require.Greater(t, s.private.server.ReadHeaderTimeout, time.Duration(0))
	require.LessOrEqual(t, s.private.server.ReadHeaderTimeout, 10*time.Second)
	require.NoError(t, m.Close(context.Background()))
	select {
	case <-s.private.done:
	default:
		t.Fatal("listener not joined")
	}
	require.Error(t, grantCtx.Err())
	client := &http.Client{Timeout: time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	resp, err := client.Get(url)
	if resp != nil {
		resp.Body.Close()
	}
	require.Error(t, err, "listener still accepts connections after Close")
	_, err = m.ClaudeCodeMCPURL()
	require.Error(t, err)
	require.NoError(t, m.Close(context.Background()))
}

func TestClaudePrivateMCPManagerCloseWaitsForRevocation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{runCtx: ctx, cancel: cancel}
	_, err := m.ClaudeCodeMCPURL()
	require.NoError(t, err)
	s := m.ClaudeCodeMCP()
	grantCtx, revoke := context.WithCancel(context.Background())
	s.mu.Lock()
	s.grants["test"] = &claudeMCPGrant{ctx: grantCtx, cancel: revoke}
	done := make(chan error, 1)
	go func() { done <- m.Close(context.Background()) }()
	select {
	case err := <-done:
		s.mu.Unlock()
		t.Fatalf("Close returned before revocation could proceed: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	s.mu.Unlock()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Close failed to join cleanup")
	}
	require.Error(t, grantCtx.Err())
}

func TestClaudePrivateMCPListenerFailureClosesActiveConnections(t *testing.T) {
	s := &ClaudeCodeMCPServer{grants: make(map[string]*claudeMCPGrant)}
	url, err := s.ListenPrivate(context.Background())
	require.NoError(t, err)
	conn, err := net.Dial("tcp", strings.TrimSuffix(strings.TrimPrefix(url, "http://"), "/v1/mcp"))
	require.NoError(t, err)
	defer conn.Close()
	_, err = io.WriteString(conn, "GET /v1/mcp HTTP/1.1\r\nHost: localhost\r\n\r\n")
	require.NoError(t, err)
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, nil)
	require.NoError(t, err)
	_, err = io.Copy(io.Discard, response.Body)
	require.NoError(t, err)
	response.Body.Close()
	require.NoError(t, s.private.listener.Close())
	select {
	case <-s.private.done:
	case <-time.After(time.Second):
		t.Fatal("listener failure did not finish cleanup")
	}
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(time.Second)))
	_, err = reader.ReadByte()
	require.ErrorIs(t, err, io.EOF)
	_, err = s.ListenPrivate(context.Background())
	require.Error(t, err)
}

func TestClaudeMCPClosedServerRejectsIssue(t *testing.T) {
	db, binding, _, _ := wakePersistenceFixtureWithClaim(t, false)
	s := (&Manager{store: db}).ClaudeCodeMCP()
	spec := ClaudeCodeMCPGrant{Binding: binding, Owner: "wake-fixture", Incarnation: "test", ExecuteChecked: func(context.Context, ClaudeCodeMCPCall) (json.RawMessage, error) { return nil, nil }}
	token, revoke, err := s.Issue(context.Background(), spec)
	require.NoError(t, err)
	require.NotEmpty(t, token)
	s.Close()
	revoke()
	token, revoke, err = s.Issue(context.Background(), spec)
	require.Error(t, err)
	require.Empty(t, token)
	require.Nil(t, revoke)
	require.Empty(t, s.grants)
}

func TestClaudeMCPIssueConcurrentCloseLeavesNoGrants(t *testing.T) {
	db, binding, _, _ := wakePersistenceFixtureWithClaim(t, false)
	for i := 0; i < 20; i++ {
		s := (&Manager{store: db}).ClaudeCodeMCP()
		spec := ClaudeCodeMCPGrant{Binding: binding, Owner: "wake-fixture", Incarnation: "test", ExecuteChecked: func(context.Context, ClaudeCodeMCPCall) (json.RawMessage, error) { return nil, nil }}
		start := make(chan struct{})
		done := make(chan struct{})
		go func() { defer close(done); <-start; _, _, _ = s.Issue(context.Background(), spec) }()
		close(start)
		s.Close()
		<-done
		s.mu.Lock()
		require.Empty(t, s.grants)
		s.mu.Unlock()
	}
}
