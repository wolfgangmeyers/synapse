package transport

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/sdui"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupTestSDUIRouter(t *testing.T) (*Router, *store.Store, string) {
	dbPath := filepath.Join(t.TempDir(), "sdui_test.db")
	st, err := store.Open(context.Background(), dbPath, store.Options{})
	require.NoError(t, err)

	router := &Router{
		store: st,
		ids:   randomIDs{},
	}
	return router, st, dbPath
}

func TestSDUI_BootstrapEndpoint(t *testing.T) {
	router, st, _ := setupTestSDUIRouter(t)
	defer st.Close()

	t.Run("GET /v1/ui/bootstrap returns 200 with root surface and remote slots", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/ui/bootstrap", nil)
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)

		var surface sdui.Surface
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &surface))
		assert.Equal(t, "root", surface.SurfaceID)
		assert.Equal(t, "Mecha Workspace", surface.Title)

		slotFound := false
		for _, b := range surface.Blocks {
			if b.Type == sdui.BlockTypeRemoteSlot && b.SlotID == "sidebar_slot" {
				slotFound = true
				assert.Equal(t, "/v1/ui/views/sidebar", b.ViewURL)
			}
		}
		assert.True(t, slotFound)
	})

	t.Run("GET /v1/ui/bootstrap respects base_url query param", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/ui/bootstrap?base_url=http://192.168.0.167:8765", nil)
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)

		var surface sdui.Surface
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &surface))

		for _, b := range surface.Blocks {
			if b.Type == sdui.BlockTypeRemoteSlot {
				assert.Contains(t, b.ViewURL, "http://192.168.0.167:8765/v1/ui/views/")
			}
		}
	})

	t.Run("POST /v1/ui/bootstrap returns 405 Method Not Allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v1/ui/bootstrap", nil)
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
	})
}

func TestSDUI_SidebarEndpoint(t *testing.T) {
	router, st, _ := setupTestSDUIRouter(t)
	defer st.Close()

	ctx := context.Background()

	agent1 := store.ManagedAgentRecord{
		AgentID:          "agent-sdui-1",
		Name:             "SDUI Agent 1",
		SlotHeld:         true,
		Cwd:              "/tmp",
		SelectedModel:    store.ManagedModel{Provider: "google-vertex", ID: "gemini-3.8-flash"},
		DesiredState:     "running",
		LifecycleState:   "running",
		ActivityState:    "idle",
		ManifestRevision: 1,
		ManifestHash:     strings.Repeat("a", 64),
	}
	op1 := store.ManagedOperation{
		OperationID:       "op-1",
		Method:            http.MethodPost,
		CanonicalPath:     "/v1/managed-agents",
		RequestID:         "req-1",
		CanonicalBodyHash: strings.Repeat("b", 64),
		ResponseStatus:    http.StatusAccepted,
		ResponseHeaders:   map[string]string{},
		ResponseBody:      []byte(`{}`),
	}
	require.NoError(t, st.ReserveManagedLaunch(ctx, agent1, op1))

	agent2 := store.ManagedAgentRecord{
		AgentID:          "agent-sdui-2",
		Name:             "SDUI Agent 2",
		SlotHeld:         false,
		Cwd:              "/tmp",
		SelectedModel:    store.ManagedModel{Provider: "openai-codex", ID: "gpt-5"},
		DesiredState:     "terminated",
		LifecycleState:   "terminated",
		ActivityState:    "stopped",
		ManifestRevision: 1,
		ManifestHash:     strings.Repeat("c", 64),
	}
	op2 := store.ManagedOperation{
		OperationID:       "op-2",
		Method:            http.MethodPost,
		CanonicalPath:     "/v1/managed-agents",
		RequestID:         "req-2",
		CanonicalBodyHash: strings.Repeat("d", 64),
		ResponseStatus:    http.StatusAccepted,
		ResponseHeaders:   map[string]string{},
		ResponseBody:      []byte(`{}`),
	}
	require.NoError(t, st.ReserveManagedLaunch(ctx, agent2, op2))

	t.Run("GET /v1/ui/views/sidebar returns 200 with peer cards", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/ui/views/sidebar", nil)
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)

		var surface sdui.Surface
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &surface))
		assert.Equal(t, "sidebar", surface.SurfaceID)

		peerCardCount := 0
		for _, b := range surface.Blocks {
			if b.Type == sdui.BlockTypeCard {
				peerCardCount++
			}
		}
		assert.Equal(t, 2, peerCardCount)
	})

	t.Run("DELETE /v1/ui/views/sidebar returns 405 Method Not Allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodDelete, "/v1/ui/views/sidebar", nil)
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
	})
}

func TestSDUI_ConversationEndpoint(t *testing.T) {
	router, st, _ := setupTestSDUIRouter(t)
	defer st.Close()

	ctx := context.Background()
	roleUser := "user"
	content1 := "Hello Agent"
	_, err := st.RecordConversationTimelineTx(ctx, "evt-1", "agent-conv", "message", &roleUser, &content1, nil, 1000)
	require.NoError(t, err)

	content2 := "Invoking tool"
	_, err = st.RecordConversationTimelineTx(ctx, "evt-2", "agent-conv", "tool", nil, &content2, nil, 2000)
	require.NoError(t, err)

	roleAsst := "assistant"
	content3 := "Here is the response"
	_, err = st.RecordConversationTimelineTx(ctx, "evt-3", "agent-conv", "message", &roleAsst, &content3, nil, 3000)
	require.NoError(t, err)

	t.Run("GET conversation returns 200 with header, messages, and composer", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/ui/views/agents/agent-conv/conversation", nil)
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)

		var surface sdui.Surface
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &surface))
		assert.Equal(t, "conversation_agent-conv", surface.SurfaceID)

		hasInput := false
		for _, b := range surface.Blocks {
			if b.Type == sdui.BlockTypeInput && b.BlockID == "composer_input" {
				hasInput = true
			}
		}
		assert.True(t, hasInput)
	})

	t.Run("GET conversation with invalid agent ID returns 400", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/ui/views/agents//conversation", nil)
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("POST conversation returns 405 Method Not Allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v1/ui/views/agents/agent-conv/conversation", nil)
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
	})
}

func TestSDUI_ConfigEndpoint(t *testing.T) {
	router, st, dbPath := setupTestSDUIRouter(t)
	defer st.Close()

	ctx := context.Background()
	rawDB, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	defer rawDB.Close()

	_, err = rawDB.ExecContext(ctx, `
		INSERT INTO managed_agents(agent_id,name,cwd,selected_model_provider,selected_model_id,desired_state,lifecycle_state,activity_state,runtime,pi_session_id,session_epoch,runtime_generation,manifest_revision,manifest_hash,created_at,updated_at)
		VALUES('agent-cfg','Configured Name','/tmp','google-vertex','gemini-3.8-flash','running','running','idle','embedded','session-1',1,'gen-1',1,'hash','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')
	`)
	require.NoError(t, err)

	t.Run("GET config view returns 200 with agent settings form", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/ui/views/agents/agent-cfg/config", nil)
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)

		var surface sdui.Surface
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &surface))
		assert.Equal(t, "config_agent-cfg", surface.SurfaceID)

		inputIDs := make(map[string]bool)
		for _, b := range surface.Blocks {
			if b.Type == sdui.BlockTypeInput {
				inputIDs[b.BlockID] = true
			}
		}

		assert.True(t, inputIDs["cfg_name"])
		assert.True(t, inputIDs["cfg_provider"])
		assert.True(t, inputIDs["cfg_model_id"])
		assert.True(t, inputIDs["cfg_temperature"])
		assert.True(t, inputIDs["cfg_allow_inline_tools"])
		assert.True(t, inputIDs["cfg_auto_restart"])
	})

	t.Run("PUT config returns 405 Method Not Allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPut, "/v1/ui/views/agents/agent-cfg/config", nil)
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
	})
}

func TestSDUI_ActionEndpoint(t *testing.T) {
	router, st, _ := setupTestSDUIRouter(t)
	defer st.Close()

	t.Run("POST /v1/ui/action with select_peer returns update_slot", func(t *testing.T) {
		payload := sdui.ActionPayload{
			ActionID: "select_peer",
			Value:    "target-peer",
		}
		body, _ := json.Marshal(payload)
		req := httptest.NewRequest(http.MethodPost, "/v1/ui/action", bytes.NewReader(body))
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)

		var resp sdui.ActionResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Equal(t, sdui.ResponseActionUpdateSlot, resp.ResponseAction)
		assert.Equal(t, "main_slot", resp.SlotID)
		assert.Equal(t, "/v1/ui/views/agents/target-peer/conversation", resp.ViewURL)
	})

	t.Run("POST /v1/ui/action with send_message returns clear_inputs", func(t *testing.T) {
		payload := sdui.ActionPayload{
			ActionID: "send_message",
			AgentID:  "target-peer",
		}
		body, _ := json.Marshal(payload)
		req := httptest.NewRequest(http.MethodPost, "/v1/ui/action", bytes.NewReader(body))
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)

		var resp sdui.ActionResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Equal(t, sdui.ResponseActionClearInputs, resp.ResponseAction)
		assert.Contains(t, resp.ClearInputs, "composer_input")
	})

	t.Run("POST /v1/ui/action with open_config returns push", func(t *testing.T) {
		payload := sdui.ActionPayload{
			ActionID: "open_config",
			AgentID:  "target-peer",
		}
		body, _ := json.Marshal(payload)
		req := httptest.NewRequest(http.MethodPost, "/v1/ui/action", bytes.NewReader(body))
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)

		var resp sdui.ActionResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Equal(t, sdui.ResponseActionPush, resp.ResponseAction)
		require.NotNil(t, resp.Surface)
		assert.Equal(t, "config_target-peer", resp.Surface.SurfaceID)
	})

	t.Run("POST /v1/ui/action with save_config validation failure returns errors", func(t *testing.T) {
		payload := sdui.ActionPayload{
			ActionID: "save_config",
			AgentID:  "target-peer",
			State: sdui.ActionState{
				Values: map[string]map[string]any{
					"cfg_name": {
						"name_input": "",
					},
					"cfg_temperature": {
						"temperature_input": "invalid-temp",
					},
				},
			},
		}
		body, _ := json.Marshal(payload)
		req := httptest.NewRequest(http.MethodPost, "/v1/ui/action", bytes.NewReader(body))
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)

		var resp sdui.ActionResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Equal(t, sdui.ResponseActionErrors, resp.ResponseAction)
		assert.Equal(t, "Agent name cannot be empty", resp.Errors["cfg_name"])
		assert.Equal(t, "Temperature must be between 0.0 and 1.0", resp.Errors["cfg_temperature"])
	})

	t.Run("POST /v1/ui/action with invalid JSON returns 400", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v1/ui/action", bytes.NewReader([]byte("{invalid-json")))
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("GET /v1/ui/action returns 405 Method Not Allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/ui/action", nil)
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
	})
}
