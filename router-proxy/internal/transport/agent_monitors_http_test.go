package transport_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/transport"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestAgentMonitorsHTTPCRUDAndEvaluate(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "router.sqlite")

	durable, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer durable.Close()

	manifest, _, err := managed.LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)

	manager, err := managed.NewManager(managed.ManagerOptions{
		Store:    durable,
		Config:   managed.Config{Enabled: false},
		Manifest: manifest,
	})
	require.NoError(t, err)

	router, err := transport.New(transport.Options{
		Store:   durable,
		Managed: manager,
	})
	require.NoError(t, err)

	server := httptest.NewServer(router)
	defer server.Close()

	client := server.Client()

	// Seed managed agent in DB
	rawDB, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	defer rawDB.Close()

	agentID := "agent-" + uuid.NewString()
	_, err = rawDB.ExecContext(ctx, `INSERT INTO managed_agents(agent_id,name,cwd,selected_model_provider,selected_model_id,desired_state,lifecycle_state,activity_state,runtime,pi_session_id,session_epoch,runtime_generation,manifest_revision,manifest_hash,created_at,updated_at) VALUES(?,?,'/tmp','openai-codex','gpt-5','running','running','idle','embedded','session-1',1,'gen-1',1,'hash','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`, agentID, "monitor-test-agent")
	require.NoError(t, err)

	baseURL := server.URL + "/v1/agents/" + agentID + "/monitors"

	// 1. GET /v1/agents/{id}/monitors (initially empty)
	res, err := client.Get(baseURL)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.StatusCode)

	var emptyListRes map[string]any
	require.NoError(t, json.NewDecoder(res.Body).Decode(&emptyListRes))
	res.Body.Close()
	require.Empty(t, emptyListRes["monitors"])

	// 2. POST /v1/agents/{id}/monitors (creates monitor with defaults)
	createPayload := map[string]any{
		"name":                  "Check Stuck",
		"description":           "Checks if agent is looping",
		"question_instructions": "Is the agent repeating the same failed command?",
		"nudge_template":        "You appear stuck repeating commands. Try a different strategy.",
	}
	createBytes, err := json.Marshal(createPayload)
	require.NoError(t, err)

	res, err = client.Post(baseURL, "application/json", bytes.NewReader(createBytes))
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, res.StatusCode)

	var createRes map[string]any
	require.NoError(t, json.NewDecoder(res.Body).Decode(&createRes))
	res.Body.Close()

	createdMonitor, ok := createRes["monitor"].(map[string]any)
	require.True(t, ok)
	monitorID, ok := createdMonitor["id"].(string)
	require.True(t, ok)
	require.NotEmpty(t, monitorID)
	require.Equal(t, "Check Stuck", createdMonitor["name"])
	require.Equal(t, "Checks if agent is looping", createdMonitor["description"])
	require.Equal(t, agentID, createdMonitor["agent_id"])
	require.Equal(t, true, createdMonitor["enabled"])
	// Verify defaults
	require.EqualValues(t, 30, createdMonitor["interval_seconds"])
	require.EqualValues(t, 120, createdMonitor["cooldown_seconds"])
	require.EqualValues(t, 6, createdMonitor["trailing_messages_count"])
	require.Equal(t, "noul", createdMonitor["question_type"])
	require.Equal(t, "gte", createdMonitor["condition_operator"])
	require.Equal(t, "0.8", createdMonitor["condition_threshold"])
	require.Equal(t, "skip", createdMonitor["fallback_mode"])

	// 3. GET /v1/agents/{id}/monitors (with items)
	res, err = client.Get(baseURL)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.StatusCode)

	var listWithItemsRes map[string]any
	require.NoError(t, json.NewDecoder(res.Body).Decode(&listWithItemsRes))
	res.Body.Close()

	monitorsList, ok := listWithItemsRes["monitors"].([]any)
	require.True(t, ok)
	require.Len(t, monitorsList, 1)
	itemMap := monitorsList[0].(map[string]any)
	require.Equal(t, monitorID, itemMap["id"])

	// 4. GET /v1/agents/{id}/monitors/{monitorId}
	itemURL := baseURL + "/" + monitorID
	res, err = client.Get(itemURL)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.StatusCode)

	var getItemRes map[string]any
	require.NoError(t, json.NewDecoder(res.Body).Decode(&getItemRes))
	res.Body.Close()

	gotMonitor, ok := getItemRes["monitor"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, monitorID, gotMonitor["id"])
	require.Equal(t, "Check Stuck", gotMonitor["name"])

	// 5. PUT /v1/agents/{id}/monitors/{monitorId} (updates fields)
	updatePayload := map[string]any{
		"name":                    "Updated Loop Check",
		"description":             "Updated description",
		"enabled":                 false,
		"interval_seconds":        45,
		"cooldown_seconds":        180,
		"max_alerts":              3,
		"trailing_messages_count": 10,
		"question_type":           "score",
		"question_instructions":   "Score how repetitive the output is from 0 to 1.",
		"condition_operator":      "gt",
		"condition_threshold":     "0.75",
		"nudge_template":          "Updated nudge template.",
		"fallback_mode":           "alert",
	}
	updateBytes, err := json.Marshal(updatePayload)
	require.NoError(t, err)

	putReq, err := http.NewRequest(http.MethodPut, itemURL, bytes.NewReader(updateBytes))
	require.NoError(t, err)
	putReq.Header.Set("Content-Type", "application/json")

	res, err = client.Do(putReq)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.StatusCode)

	var putRes map[string]any
	require.NoError(t, json.NewDecoder(res.Body).Decode(&putRes))
	res.Body.Close()

	updatedMonitor, ok := putRes["monitor"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, monitorID, updatedMonitor["id"])
	require.Equal(t, "Updated Loop Check", updatedMonitor["name"])
	require.Equal(t, "Updated description", updatedMonitor["description"])
	require.Equal(t, false, updatedMonitor["enabled"])
	require.EqualValues(t, 45, updatedMonitor["interval_seconds"])
	require.EqualValues(t, 180, updatedMonitor["cooldown_seconds"])
	require.EqualValues(t, 3, updatedMonitor["max_alerts"])
	require.EqualValues(t, 10, updatedMonitor["trailing_messages_count"])
	require.Equal(t, "score", updatedMonitor["question_type"])
	require.Equal(t, "Score how repetitive the output is from 0 to 1.", updatedMonitor["question_instructions"])
	require.Equal(t, "gt", updatedMonitor["condition_operator"])
	require.Equal(t, "0.75", updatedMonitor["condition_threshold"])
	require.Equal(t, "Updated nudge template.", updatedMonitor["nudge_template"])
	require.Equal(t, "alert", updatedMonitor["fallback_mode"])

	// 6. POST /v1/agents/{id}/monitors/{monitorId}/evaluate (dry-run evaluation)
	evalURL := itemURL + "/evaluate"
	res, err = client.Post(evalURL, "application/json", nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.StatusCode)

	var evalRes map[string]any
	require.NoError(t, json.NewDecoder(res.Body).Decode(&evalRes))
	res.Body.Close()

	require.Equal(t, true, evalRes["evaluated"])
	// System 1 is not configured/enabled, fallback_mode is "alert" so fired should be true
	require.Equal(t, true, evalRes["fired"])
	evalMonitor, ok := evalRes["monitor"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, monitorID, evalMonitor["id"])
	require.NotEmpty(t, evalMonitor["last_fired_at"])

	// 7. DELETE /v1/agents/{id}/monitors/{monitorId}
	delReq, err := http.NewRequest(http.MethodDelete, itemURL, nil)
	require.NoError(t, err)

	res, err = client.Do(delReq)
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, res.StatusCode)
	res.Body.Close()

	// Verify 404 after deletion on GET
	res, err = client.Get(itemURL)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, res.StatusCode)
	res.Body.Close()

	// Verify DELETE on already deleted monitor returns 404
	delReq2, err := http.NewRequest(http.MethodDelete, itemURL, nil)
	require.NoError(t, err)
	res, err = client.Do(delReq2)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, res.StatusCode)
	res.Body.Close()
}
