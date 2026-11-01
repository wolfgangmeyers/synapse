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
	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

func TestEmbeddedCodexSearch_ExecuteSuccess(t *testing.T) {
	var capturedAuth string
	var capturedAccountID string
	var capturedBeta string
	var capturedBody map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")
		capturedAccountID = r.Header.Get("ChatGPT-Account-ID")
		capturedBeta = r.Header.Get("OpenAI-Beta")

		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		_ = json.Unmarshal(bodyBytes, &capturedBody)

		w.Header().Set("Content-Type", "text/event-stream")
		sseResponse := `data: {"type":"response.output_text.delta","delta":"Go 1.25 is expected in August 2025."}

data: {"type":"response.output_item.done","item":{"type":"web_search_call","action":{"query":"Go 1.25 release date","sources":[{"title":"Go 1.25 Release Milestones","url":"https://go.dev/doc/go1.25"}]}}}

data: {"type":"response.completed","response":{"id":"resp_123","status":"completed"}}

data: [DONE]

`
		_, _ = w.Write([]byte(sseResponse))
	}))
	defer server.Close()

	origURL := codexSearchAPIURL
	codexSearchAPIURL = server.URL
	defer func() { codexSearchAPIURL = origURL }()

	tmpDir := t.TempDir()
	authPath := filepath.Join(tmpDir, "auth.json")
	authData := map[string]any{
		"openai-codex": map[string]any{
			"access":    "test-codex-access-token",
			"accountId": "test-account-123",
			"expires":   time.Now().Add(time.Hour).UnixMilli(),
		},
	}
	authBytes, err := json.Marshal(authData)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(authPath, authBytes, 0600))

	mgr := &Manager{
		config: Config{PiAgentDir: tmpDir},
	}

	res, err := mgr.executeCodexSearch(context.Background(), "Go 1.25 release date", "focus on milestones", []string{"https://go.dev/doc"})
	require.NoError(t, err)

	require.Equal(t, "Bearer test-codex-access-token", capturedAuth)
	require.Equal(t, "test-account-123", capturedAccountID)
	require.Equal(t, "responses=experimental", capturedBeta)

	require.NotNil(t, capturedBody)
	require.Equal(t, "gpt-5.5", capturedBody["model"])
	require.Equal(t, false, capturedBody["store"])
	require.Equal(t, true, capturedBody["stream"])

	// Check result formatting
	require.Contains(t, res, "Go 1.25 is expected in August 2025.")
	require.Contains(t, res, "### Sources")
	require.Contains(t, res, "[Go 1.25 Release Milestones](https://go.dev/doc/go1.25)")
	require.Contains(t, res, "*Search queries: Go 1.25 release date*")
}

func TestEmbeddedCodexSearch_ToolExecute_Success(t *testing.T) {
	var capturedAuth string
	var capturedAccountID string
	var capturedBeta string
	var capturedBody map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")
		capturedAccountID = r.Header.Get("ChatGPT-Account-ID")
		capturedBeta = r.Header.Get("OpenAI-Beta")

		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		_ = json.Unmarshal(bodyBytes, &capturedBody)

		w.Header().Set("Content-Type", "text/event-stream")
		sseResponse := `data: {"type":"response.output_text.delta","delta":"Go 1.25 is expected in August 2025."}

data: {"type":"response.output_item.done","item":{"type":"web_search_call","action":{"query":"Go 1.25 release date","sources":[{"title":"Go 1.25 Release Milestones","url":"https://go.dev/doc/go1.25"}]}}}

data: {"type":"response.completed","response":{"id":"resp_123","status":"completed"}}

data: [DONE]

`
		_, _ = w.Write([]byte(sseResponse))
	}))
	defer server.Close()

	origURL := codexSearchAPIURL
	codexSearchAPIURL = server.URL
	defer func() { codexSearchAPIURL = origURL }()

	tmpDir := t.TempDir()
	authPath := filepath.Join(tmpDir, "auth.json")
	authData := map[string]any{
		"openai-codex": map[string]any{
			"access":    "test-codex-access-token",
			"accountId": "test-account-123",
			"expires":   time.Now().Add(time.Hour).UnixMilli(),
		},
	}
	authBytes, err := json.Marshal(authData)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(authPath, authBytes, 0600))

	ctx := context.Background()
	dbPath := filepath.Join(tmpDir, "test.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	agentID := "worker-search-agent"
	binding := subagentTestParent(t, db, agentID)
	owner := embeddedOwner{binding: binding, identity: "fixture:" + agentID}

	mgr := &Manager{
		store:  db,
		config: Config{PiAgentDir: tmpDir},
	}

	tool := mgr.embeddedCodexSearchTool(owner)

	// Test with []any urls
	var updateCalls []agent.AgentToolResult
	updateFunc := func(u agent.AgentToolResult) {
		updateCalls = append(updateCalls, u)
	}

	args := map[string]any{
		"query":       "Go 1.25 release date",
		"instruction": "focus on milestones",
		"urls":        []any{"https://go.dev/doc", "https://tip.golang.org"},
	}

	res, err := tool.Execute(ctx, "call-search-1", args, updateFunc)
	require.NoError(t, err)
	require.NotEmpty(t, updateCalls)
	updateDetails, ok := updateCalls[0].Details.(map[string]any)
	require.True(t, ok)
	require.Equal(t, "searching", updateDetails["status"])
	require.Equal(t, "Go 1.25 release date", updateDetails["query"])

	require.Equal(t, "Bearer test-codex-access-token", capturedAuth)
	require.Equal(t, "test-account-123", capturedAccountID)
	require.Equal(t, "responses=experimental", capturedBeta)

	require.NotNil(t, capturedBody)
	require.Equal(t, []any{map[string]any{"type": "web_search"}}, capturedBody["tools"])
	require.Equal(t, []any{"web_search_call.results"}, capturedBody["include"])

	inputList, ok := capturedBody["input"].([]any)
	require.True(t, ok)
	require.NotEmpty(t, inputList)
	firstInput := inputList[0].(map[string]any)
	contentList := firstInput["content"].([]any)
	textContent := contentList[0].(map[string]any)["text"].(string)
	require.Contains(t, textContent, "focus on milestones")
	require.Contains(t, textContent, "https://go.dev/doc")
	require.Contains(t, textContent, "https://tip.golang.org")

	require.NotEmpty(t, res.Content)
	text := res.Content[0].(ai.TextContent).Text
	require.Contains(t, text, "Go 1.25 is expected in August 2025.")
	require.Contains(t, text, "### Sources")
	require.Contains(t, text, "[Go 1.25 Release Milestones](https://go.dev/doc/go1.25)")

	// Test with []string urls
	argsStringURLs := map[string]any{
		"query":       "Go 1.25 release date",
		"instruction": "focus on milestones",
		"urls":        []string{"https://go.dev/doc"},
	}
	res2, err := tool.Execute(ctx, "call-search-2", argsStringURLs, nil)
	require.NoError(t, err)
	require.NotEmpty(t, res2.Content)
}

func TestEmbeddedCodexSearch_ToolExecute_EmptyQuery(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	agentID := "worker-search-empty"
	binding := subagentTestParent(t, db, agentID)
	owner := embeddedOwner{binding: binding, identity: "fixture:" + agentID}

	mgr := &Manager{store: db}
	tool := mgr.embeddedCodexSearchTool(owner)

	for _, emptyQuery := range []string{"", "   ", "\t\n"} {
		res, err := tool.Execute(ctx, "call-empty", map[string]any{"query": emptyQuery}, nil)
		require.NoError(t, err)
		require.NotEmpty(t, res.Content)
		text := res.Content[0].(ai.TextContent).Text
		require.Equal(t, "error: query argument must be a non-empty string", text)
		details, ok := res.Details.(map[string]any)
		require.True(t, ok)
		require.Equal(t, "empty query", details["error"])
	}
}

func TestEmbeddedCodexSearch_ToolExecute_MissingCredentials(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	t.Setenv("HOME", tmpDir)
	t.Setenv("PI_AUTH_FILE", filepath.Join(tmpDir, "nonexistent.json"))

	agentID := "worker-search-nocreds"
	binding := subagentTestParent(t, db, agentID)
	owner := embeddedOwner{binding: binding, identity: "fixture:" + agentID}

	mgr := &Manager{
		store:  db,
		config: Config{PiAgentDir: tmpDir},
	}
	tool := mgr.embeddedCodexSearchTool(owner)

	res, err := tool.Execute(ctx, "call-nocreds", map[string]any{"query": "valid query"}, nil)
	require.NoError(t, err)
	require.NotEmpty(t, res.Content)
	text := res.Content[0].(ai.TextContent).Text
	require.Contains(t, text, "No OpenAI/Codex credentials found. Run /login openai-codex or authenticate with Codex CLI.")
}

func TestEmbeddedCodexSearch_ToolExecute_RoleCheckAndTTL(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	agentID := "agent-role-test"
	binding := subagentTestParent(t, db, agentID)
	owner := embeddedOwner{binding: binding, identity: "fixture:" + agentID}

	mgr := &Manager{store: db}
	tool := mgr.embeddedCodexSearchTool(owner)

	// Subagent role rejection
	rawDB, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	defer rawDB.Close()

	_, err = rawDB.ExecContext(ctx, `UPDATE embedded_sessions SET role='subagent' WHERE agent_id=?`, agentID)
	require.NoError(t, err)

	_, err = tool.Execute(ctx, "call-subagent", map[string]any{"query": "test query"}, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "codex_search requires embedded root or worker")

	// Custom non-root non-worker role (e.g. "guest" or "evaluator" with ignore_check_constraints)
	_, _ = rawDB.ExecContext(ctx, "PRAGMA ignore_check_constraints = ON;")
	for _, invalidRole := range []string{"evaluator", "guest"} {
		_, err = rawDB.ExecContext(ctx, fmt.Sprintf("UPDATE embedded_sessions SET role='%s' WHERE agent_id=?", invalidRole), agentID)
		require.NoError(t, err)

		_, err = tool.Execute(ctx, "call-"+invalidRole, map[string]any{"query": "test query"}, nil)
		require.Error(t, err)
	}

	// RenewEmbeddedOwner failure (e.g. stale owner or closed store)
	staleOwner := embeddedOwner{
		binding:  binding,
		identity: "stale-owner-identity",
	}
	staleTool := mgr.embeddedCodexSearchTool(staleOwner)
	_, err = staleTool.Execute(ctx, "call-stale", map[string]any{"query": "test query"}, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "stale embedded owner")
}

func TestEmbeddedCodexSearch_SessionToolAvailability(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	// 1. Without credentials: hasCodexSearchAuth is false
	t.Setenv("HOME", tmpDir)
	t.Setenv("PI_AUTH_FILE", filepath.Join(tmpDir, "nonexistent.json"))
	mgrWithoutAuth := &Manager{
		config: Config{PiAgentDir: tmpDir},
	}
	require.False(t, mgrWithoutAuth.hasCodexSearchAuth(ctx))

	// 2. With valid credentials: hasCodexSearchAuth is true
	authPath := filepath.Join(tmpDir, "auth.json")
	validExp := time.Now().Add(1 * time.Hour)
	jwt := makeTestJWT("test-acc", validExp)
	authData := map[string]any{
		"openai-codex": map[string]any{
			"access":    jwt,
			"accountId": "test-acc",
			"expires":   validExp.UnixMilli(),
		},
	}
	authBytes, err := json.Marshal(authData)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(authPath, authBytes, 0600))

	mgrWithAuth := &Manager{
		config: Config{PiAgentDir: tmpDir},
	}
	require.True(t, mgrWithAuth.hasCodexSearchAuth(ctx))
}

func TestEmbeddedCodexSearch_ParseCodexSearchResponse_EdgeCases(t *testing.T) {
	// Empty body
	res, err := parseCodexSearchResponse(strings.NewReader(""))
	require.NoError(t, err)
	require.Equal(t, "No search results returned.", res)

	// Completed response with output items
	sse := `data: {"type":"response.output_text.delta","delta":"Synthesized answer."}
data: {"type":"response.completed","response":{"output":[{"type":"web_search_call","action":{"query":"first query","sources":[{"title":"","url":"https://example.com/one"},{"title":"Second","url":"https://example.com/two"}]}},{"type":"web_search_call","action":{"query":"first query","sources":[{"title":"Duplicate URL","url":"https://example.com/one"}]}}]}}
`
	res, err = parseCodexSearchResponse(strings.NewReader(sse))
	require.NoError(t, err)
	require.Contains(t, res, "Synthesized answer.")
	require.Contains(t, res, "### Sources")
	require.Contains(t, res, "- [https://example.com/one](https://example.com/one)")
	require.Contains(t, res, "- [Second](https://example.com/two)")
	// Duplicate URL in markdown format [https://example.com/one](https://example.com/one) appears once as a line
	require.Equal(t, 1, strings.Count(res, "- [https://example.com/one]"))
	// Query deduplicated
	require.Contains(t, res, "*Search queries: first query*")
}

func TestEmbeddedCodexSearch_HTTP401And429(t *testing.T) {
	// 401 test
	server401 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server401.Close()

	origURL := codexSearchAPIURL
	codexSearchAPIURL = server401.URL
	defer func() { codexSearchAPIURL = origURL }()

	tmpDir := t.TempDir()
	authPath := filepath.Join(tmpDir, "auth.json")
	authData := map[string]any{
		"openai": map[string]any{
			"access":  "some-token",
			"expires": time.Now().Add(time.Hour).UnixMilli(),
		},
	}
	b, _ := json.Marshal(authData)
	_ = os.WriteFile(authPath, b, 0600)

	mgr := &Manager{config: Config{PiAgentDir: tmpDir}}

	_, err := mgr.executeCodexSearch(context.Background(), "query", "", nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "Codex auth unauthorized (401)")

	// 429 test
	server429 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server429.Close()
	codexSearchAPIURL = server429.URL

	_, err = mgr.executeCodexSearch(context.Background(), "query", "", nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "Codex rate limit exceeded (429)")
}
