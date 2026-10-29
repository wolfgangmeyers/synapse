package transport

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

func TestAgentProgressRoute(t *testing.T) {
	tests := []struct {
		path       string
		wantAgent  string
		wantAction string
		wantOK     bool
	}{
		{"/v1/agents/agent-1/progress/history", "agent-1", "history", true},
		{"/v1/agents/agent-1/progress/chart.png", "", "", false},
		{"/v1/managed-agents/agent-2/progress/history", "agent-2", "history", true},
		{"/v1/managed-agents/agent-2/progress/chart.png", "", "", false},
		{"/v1/agents/agent%201/progress/history", "agent 1", "history", true},
		{"/v1/agents/agent-1/progress/unknown", "", "", false},
		{"/v1/agents/agent-1/progress", "", "", false},
		{"/v1/other/agent-1/progress/history", "", "", false},
	}

	for _, tt := range tests {
		gotAgent, gotAction, gotOK := agentProgressRoute(tt.path)
		if gotAgent != tt.wantAgent || gotAction != tt.wantAction || gotOK != tt.wantOK {
			t.Errorf("agentProgressRoute(%q) = (%q, %q, %v), want (%q, %q, %v)",
				tt.path, gotAgent, gotAction, gotOK, tt.wantAgent, tt.wantAction, tt.wantOK)
		}
	}
}

func TestAgentProgressHTTP_MethodNotAllowed(t *testing.T) {
	r := &Router{}
	req := httptest.NewRequest(http.MethodPost, "/v1/agents/test/progress/history", nil)
	w := httptest.NewRecorder()

	r.handleAgentProgressHTTP(w, req, "test", "history")

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", w.Code)
	}
	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if resp["error"] != "method not allowed" {
		t.Fatalf("unexpected error response: %v", resp)
	}
}

func TestAgentProgressHTTP_Integration(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "router.db")
	st, err := store.Open(context.Background(), dbPath, store.Options{})
	if err != nil {
		t.Fatalf("failed to init store: %v", err)
	}
	defer st.Close()

	rawDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("failed to open raw sqlite: %v", err)
	}
	defer rawDB.Close()

	ctx := context.Background()
	_, err = rawDB.ExecContext(ctx, `INSERT INTO managed_agents(agent_id,name,cwd,selected_model_provider,selected_model_id,desired_state,lifecycle_state,activity_state,runtime,pi_session_id,session_epoch,runtime_generation,manifest_revision,manifest_hash,created_at,updated_at) VALUES(?,?,'/tmp','openai-codex','gpt-5','running','running','idle','embedded','session-1',1,'gen-1',1,'hash','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`, "agent-progress-test", "progress-test-agent")
	if err != nil {
		t.Fatalf("failed to insert test agent: %v", err)
	}

	if err := st.SetAgentProgress(ctx, "agent-progress-test", 42, "halfway done"); err != nil {
		t.Fatalf("failed to set progress: %v", err)
	}

	router := &Router{store: st, ids: randomIDs{}}

	// 1. Not found agent
	{
		req := httptest.NewRequest(http.MethodGet, "/v1/agents/non-existent/progress/history", nil)
		w := httptest.NewRecorder()
		router.handleHTTP(w, req)
		if w.Code != http.StatusNotFound {
			t.Fatalf("expected 404 for missing agent, got %d", w.Code)
		}
	}

	// 2. History endpoint
	{
		req := httptest.NewRequest(http.MethodGet, "/v1/agents/agent-progress-test/progress/history", nil)
		w := httptest.NewRecorder()
		router.handleHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d body: %s", w.Code, w.Body.String())
		}
		var history []struct {
			Percent   int    `json:"percent"`
			Note      string `json:"note"`
			Timestamp string `json:"timestamp"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &history); err != nil {
			t.Fatalf("failed to unmarshal history: %v", err)
		}
		if len(history) != 1 || history[0].Percent != 42 || history[0].Note != "halfway done" {
			t.Fatalf("unexpected history: %+v", history)
		}
	}
}
