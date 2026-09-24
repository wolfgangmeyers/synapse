package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReadManagedRecoveryCompletionFileRejectsInvalidFiles(t *testing.T) {
	valid := []byte(`{"event":"managed_recovery_complete","trial_id":"trial-1","token":"request-token","pi_session_id":"session-1"}`)
	tests := []struct {
		name    string
		prepare func(*testing.T, string)
	}{
		{
			name: "malformed JSON",
			prepare: func(t *testing.T, path string) {
				require.NoError(t, os.WriteFile(path, []byte(`{"event":`), 0600))
			},
		},
		{
			name: "unknown field",
			prepare: func(t *testing.T, path string) {
				require.NoError(t, os.WriteFile(path, []byte(`{"event":"managed_recovery_complete","trial_id":"trial-1","token":"request-token","pi_session_id":"session-1","unexpected":true}`), 0600))
			},
		},
		{
			name: "trailing JSON",
			prepare: func(t *testing.T, path string) {
				require.NoError(t, os.WriteFile(path, append(append([]byte{}, valid...), []byte(` {}`)...), 0600))
			},
		},
		{
			name: "symlink",
			prepare: func(t *testing.T, path string) {
				target := filepath.Join(filepath.Dir(path), "target.json")
				require.NoError(t, os.WriteFile(target, valid, 0600))
				require.NoError(t, os.Symlink(target, path))
			},
		},
		{
			name: "group permissions",
			prepare: func(t *testing.T, path string) {
				require.NoError(t, os.WriteFile(path, valid, 0600))
				require.NoError(t, os.Chmod(path, 0640))
			},
		},
		{
			name: "world permissions",
			prepare: func(t *testing.T, path string) {
				require.NoError(t, os.WriteFile(path, valid, 0600))
				require.NoError(t, os.Chmod(path, 0604))
			},
		},
		{
			name: "empty",
			prepare: func(t *testing.T, path string) {
				require.NoError(t, os.WriteFile(path, nil, 0600))
			},
		},
		{
			name: "oversize",
			prepare: func(t *testing.T, path string) {
				require.NoError(t, os.WriteFile(path, bytes.Repeat([]byte("x"), 16*1024+1), 0600))
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "completion.json")
			test.prepare(t, path)
			_, err := readManagedRecoveryCompletionFile(path)
			require.Error(t, err)
		})
	}
}

func TestReadManagedRecoveryCompletionFileAcceptsExactPayloadWith0600Permissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "completion.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"event":"managed_recovery_complete","trial_id":"trial-1","token":"request-token","pi_session_id":"session-1"}`), 0600))
	require.NoError(t, os.Chmod(path, 0600))

	request, err := readManagedRecoveryCompletionFile(path)
	require.NoError(t, err)
	require.Equal(t, managedRecoveryCompletionRequest{
		Event: "managed_recovery_complete", TrialID: "trial-1", Token: "request-token", PiSessionID: "session-1",
	}, request)
}

func TestManagedRecoveryBarrierCanBeReadImmediatelyAfterPersistence(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/managed-recovery/status", r.URL.Path)
		require.Equal(t, "workload-token", r.URL.Query().Get("token"))
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(managedRecoveryProviderState{
			Token: "workload-token", ProviderRequestSeq: 1, Active: true,
		}))
	}))
	defer provider.Close()

	database := filepath.Join(t.TempDir(), "router.db")
	db, err := sql.Open("sqlite", database)
	require.NoError(t, err)
	_, err = db.Exec(`
		CREATE TABLE managed_settings (singleton_id INTEGER PRIMARY KEY, projection_version INTEGER NOT NULL);
		INSERT INTO managed_settings(singleton_id, projection_version) VALUES(1, 7);
		CREATE TABLE managed_agents (
			agent_id TEXT PRIMARY KEY, pi_session_id TEXT NOT NULL, session_epoch INTEGER NOT NULL,
			runtime_generation TEXT NOT NULL, process_incarnation TEXT NOT NULL,
			process_pid INTEGER NOT NULL, process_start_marker TEXT NOT NULL,
			manifest_revision INTEGER NOT NULL, manifest_hash TEXT NOT NULL,
			lifecycle_state TEXT NOT NULL, activity_state TEXT NOT NULL,
			accepting_prompts INTEGER NOT NULL, updated_at TEXT NOT NULL
		);
		INSERT INTO managed_agents VALUES(
			'agent-1', 'session-1', 1, 'generation-1', 'incarnation-1', ?, 'start-1',
			1, 'manifest-1', 'running', 'active', 0, '2026-01-01T00:00:00Z'
		);`, os.Getpid())
	require.NoError(t, err)
	require.NoError(t, db.Close())

	authority, err := newManagedRecoveryAuthority(database, provider.URL, "trial-1", "workload-token", filepath.Join(t.TempDir(), "completion.json"), os.Getpid())
	require.NoError(t, err)
	defer authority.Close()

	body := []byte(`{"event":"managed_recovery_in_progress","trial_id":"trial-1","token":"workload-token","agent_id":"agent-1","observed_monotonic_ms":10}`)
	post := httptest.NewRequest(http.MethodPost, "/v1/acceptance/managed-recovery?kind=barrier", bytes.NewReader(body))
	postResponse := httptest.NewRecorder()
	authority.ServeHTTP(postResponse, post)
	require.Equal(t, http.StatusOK, postResponse.Code, postResponse.Body.String())

	get := httptest.NewRequest(http.MethodGet, "/v1/acceptance/managed-recovery?kind=barrier&trial_id=trial-1", nil)
	getResponse := httptest.NewRecorder()
	authority.ServeHTTP(getResponse, get)
	require.Equal(t, http.StatusOK, getResponse.Code, getResponse.Body.String())

	var posted, reread map[string]any
	require.NoError(t, json.Unmarshal(postResponse.Body.Bytes(), &posted))
	require.NoError(t, json.Unmarshal(getResponse.Body.Bytes(), &reread))
	require.Equal(t, posted, reread, fmt.Sprintf("POST=%s GET=%s", postResponse.Body.String(), getResponse.Body.String()))
}

func TestManagedRecoveryCompletionAcceptsActiveFenceAndUsesStoredBarrierToken(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/managed-recovery/status", r.URL.Path)
		require.Equal(t, "request-token", r.URL.Query().Get("token"))
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(managedRecoveryProviderState{
			Token: "request-token", ProviderRequestSeq: 4, Completed: true,
		}))
	}))
	defer provider.Close()

	database := filepath.Join(t.TempDir(), "router.db")
	db, err := sql.Open("sqlite", database)
	require.NoError(t, err)
	_, err = db.Exec(`
		CREATE TABLE managed_settings (singleton_id INTEGER PRIMARY KEY, projection_version INTEGER NOT NULL);
		INSERT INTO managed_settings(singleton_id, projection_version) VALUES(1, 9);
		CREATE TABLE managed_agents (
			agent_id TEXT PRIMARY KEY, pi_session_id TEXT NOT NULL, session_epoch INTEGER NOT NULL,
			runtime_generation TEXT NOT NULL, process_incarnation TEXT NOT NULL,
			process_pid INTEGER NOT NULL, process_start_marker TEXT NOT NULL,
			manifest_revision INTEGER NOT NULL, manifest_hash TEXT NOT NULL,
			lifecycle_state TEXT NOT NULL, activity_state TEXT NOT NULL,
			accepting_prompts INTEGER NOT NULL, updated_at TEXT NOT NULL
		);
		INSERT INTO managed_agents VALUES(
			'other-agent', 'other-session', 1, 'other-generation', 'other-incarnation', ?, 'other-start',
			1, 'other-manifest', 'running', 'idle', 1, '2026-01-01T00:00:00Z'
		);
		INSERT INTO managed_agents VALUES(
			'agent-1', 'session-1', 2, 'generation-1', 'incarnation-1', ?, 'start-1',
			3, 'manifest-1', 'running', 'active', 1, '2026-01-01T00:00:01Z'
		);`, os.Getpid(), os.Getpid())
	require.NoError(t, err)
	require.NoError(t, db.Close())

	completionFile := filepath.Join(t.TempDir(), "completion.json")
	authority, err := newManagedRecoveryAuthority(database, provider.URL, "trial-1", "startup-token", completionFile, os.Getpid())
	require.NoError(t, err)
	defer authority.Close()

	barrier := map[string]any{
		"agent_id": "agent-1", "token": "request-token", "observed_monotonic_ms": float64(10),
	}
	_, err = authority.insertBarrier(t.Context(), "trial-1", barrier, managedRecoveryFence{}, 4, monotonicNanoseconds())
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(completionFile, []byte(`{"event":"managed_recovery_complete","trial_id":"trial-1","token":"request-token","pi_session_id":"session-1"}`), 0600))

	get := httptest.NewRequest(http.MethodGet, "/v1/acceptance/managed-recovery?kind=completion&trial_id=trial-1", nil)
	getResponse := httptest.NewRecorder()
	authority.ServeHTTP(getResponse, get)
	require.Equal(t, http.StatusOK, getResponse.Code, getResponse.Body.String())

	var evidence managedRecoveryCompletionEvidence
	require.NoError(t, json.Unmarshal(getResponse.Body.Bytes(), &evidence))
	require.Equal(t, "request-token", evidence.Token)
	require.Equal(t, "agent-1", evidence.AgentID)
	require.Equal(t, int64(4), evidence.ProviderRequestSeq)
	requireManagedRecoveryCompletionReceiptCount(t, authority, 1)

	secondResponse := httptest.NewRecorder()
	authority.ServeHTTP(secondResponse, get)
	require.Equal(t, http.StatusOK, secondResponse.Code, secondResponse.Body.String())
	require.JSONEq(t, getResponse.Body.String(), secondResponse.Body.String())
	requireManagedRecoveryCompletionReceiptCount(t, authority, 1)

	post := httptest.NewRequest(http.MethodPost, "/v1/acceptance/managed-recovery?kind=completion", bytes.NewReader([]byte(`{}`)))
	postResponse := httptest.NewRecorder()
	authority.ServeHTTP(postResponse, post)
	require.Equal(t, http.StatusConflict, postResponse.Code, postResponse.Body.String())
}

type managedRecoveryCompletionTestFixture struct {
	authority      *managedRecoveryAuthority
	completionFile string
	providerState  *managedRecoveryProviderState
}

func newManagedRecoveryCompletionTestFixture(t *testing.T) *managedRecoveryCompletionTestFixture {
	t.Helper()
	providerState := &managedRecoveryProviderState{ProviderRequestSeq: 4, Completed: true}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		state := *providerState
		state.Token = r.URL.Query().Get("token")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(state)
	}))
	t.Cleanup(provider.Close)

	database := filepath.Join(t.TempDir(), "router.db")
	db, err := sql.Open("sqlite", database)
	require.NoError(t, err)
	_, err = db.Exec(`
		CREATE TABLE managed_settings (singleton_id INTEGER PRIMARY KEY, projection_version INTEGER NOT NULL);
		INSERT INTO managed_settings(singleton_id, projection_version) VALUES(1, 9);
		CREATE TABLE managed_agents (
			agent_id TEXT PRIMARY KEY, pi_session_id TEXT NOT NULL, session_epoch INTEGER NOT NULL,
			runtime_generation TEXT NOT NULL, process_incarnation TEXT NOT NULL,
			process_pid INTEGER NOT NULL, process_start_marker TEXT NOT NULL,
			manifest_revision INTEGER NOT NULL, manifest_hash TEXT NOT NULL,
			lifecycle_state TEXT NOT NULL, activity_state TEXT NOT NULL,
			accepting_prompts INTEGER NOT NULL, updated_at TEXT NOT NULL
		);
		INSERT INTO managed_agents VALUES(
			'agent-1', 'session-1', 2, 'generation-1', 'incarnation-1', ?, 'start-1',
			3, 'manifest-1', 'running', 'active', 1, '2026-01-01T00:00:01Z'
		);`, os.Getpid())
	require.NoError(t, err)
	require.NoError(t, db.Close())

	completionFile := filepath.Join(t.TempDir(), "completion.json")
	authority, err := newManagedRecoveryAuthority(database, provider.URL, "trial-1", "startup-token", completionFile, os.Getpid())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, authority.Close()) })
	barrier := map[string]any{
		"agent_id": "agent-1", "token": "request-token", "observed_monotonic_ms": float64(10),
		"provider_request_seq": float64(4),
	}
	_, err = authority.insertBarrier(t.Context(), "trial-1", barrier, managedRecoveryFence{}, 4, monotonicNanoseconds())
	require.NoError(t, err)
	return &managedRecoveryCompletionTestFixture{
		authority: authority, completionFile: completionFile, providerState: providerState,
	}
}

func (f *managedRecoveryCompletionTestFixture) writeCompletion(t *testing.T, request managedRecoveryCompletionRequest) {
	t.Helper()
	encoded, err := json.Marshal(request)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(f.completionFile, encoded, 0600))
	require.NoError(t, os.Chmod(f.completionFile, 0600))
}

func (f *managedRecoveryCompletionTestFixture) readCompletion() *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/v1/acceptance/managed-recovery?kind=completion&trial_id=trial-1", nil)
	f.authority.ServeHTTP(response, request)
	return response
}

func requireManagedRecoveryCompletionReceiptCount(t *testing.T, authority *managedRecoveryAuthority, expected int) {
	t.Helper()
	var count int
	require.NoError(t, authority.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM acceptance_managed_recovery_completion_receipts`).Scan(&count))
	require.Equal(t, expected, count)
}

func TestManagedRecoveryCompletionRejectsStaleSignalsWithoutPersistingReceipt(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *managedRecoveryCompletionTestFixture, *managedRecoveryCompletionRequest)
	}{
		{
			name: "wrong trial",
			mutate: func(_ *testing.T, _ *managedRecoveryCompletionTestFixture, request *managedRecoveryCompletionRequest) {
				request.TrialID = "trial-2"
			},
		},
		{
			name: "wrong token",
			mutate: func(_ *testing.T, _ *managedRecoveryCompletionTestFixture, request *managedRecoveryCompletionRequest) {
				request.Token = "wrong-token"
			},
		},
		{
			name: "wrong Pi session",
			mutate: func(_ *testing.T, _ *managedRecoveryCompletionTestFixture, request *managedRecoveryCompletionRequest) {
				request.PiSessionID = "wrong-session"
			},
		},
		{
			name: "stale Pi session binding",
			mutate: func(t *testing.T, fixture *managedRecoveryCompletionTestFixture, _ *managedRecoveryCompletionRequest) {
				_, err := fixture.authority.db.Exec(`UPDATE managed_agents SET pi_session_id='replacement-session' WHERE agent_id='agent-1'`)
				require.NoError(t, err)
			},
		},
		{
			name: "stale fence",
			mutate: func(t *testing.T, fixture *managedRecoveryCompletionTestFixture, _ *managedRecoveryCompletionRequest) {
				_, err := fixture.authority.db.Exec(`UPDATE managed_agents SET lifecycle_state='stopped' WHERE agent_id='agent-1'`)
				require.NoError(t, err)
			},
		},
		{
			name: "provider active",
			mutate: func(_ *testing.T, fixture *managedRecoveryCompletionTestFixture, _ *managedRecoveryCompletionRequest) {
				fixture.providerState.Active = true
			},
		},
		{
			name: "provider not completed",
			mutate: func(_ *testing.T, fixture *managedRecoveryCompletionTestFixture, _ *managedRecoveryCompletionRequest) {
				fixture.providerState.Completed = false
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newManagedRecoveryCompletionTestFixture(t)
			request := managedRecoveryCompletionRequest{
				Event: "managed_recovery_complete", TrialID: "trial-1", Token: "request-token", PiSessionID: "session-1",
			}
			test.mutate(t, fixture, &request)
			fixture.writeCompletion(t, request)

			response := fixture.readCompletion()
			require.Equal(t, http.StatusConflict, response.Code, response.Body.String())
			requireManagedRecoveryCompletionReceiptCount(t, fixture.authority, 0)
		})
	}
}

func TestManagedRecoveryCompletionPersistsOnceAndRereadsIdempotently(t *testing.T) {
	fixture := newManagedRecoveryCompletionTestFixture(t)
	fixture.writeCompletion(t, managedRecoveryCompletionRequest{
		Event: "managed_recovery_complete", TrialID: "trial-1", Token: "request-token", PiSessionID: "session-1",
	})

	first := fixture.readCompletion()
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	requireManagedRecoveryCompletionReceiptCount(t, fixture.authority, 1)

	second := fixture.readCompletion()
	require.Equal(t, http.StatusOK, second.Code, second.Body.String())
	require.JSONEq(t, first.Body.String(), second.Body.String())
	requireManagedRecoveryCompletionReceiptCount(t, fixture.authority, 1)
}

func TestManagedRecoveryCompletionRejectsStoppedFence(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(managedRecoveryProviderState{
			Token: "request-token", ProviderRequestSeq: 4, Completed: true,
		}))
	}))
	defer provider.Close()

	database := filepath.Join(t.TempDir(), "router.db")
	db, err := sql.Open("sqlite", database)
	require.NoError(t, err)
	_, err = db.Exec(`
		CREATE TABLE managed_settings (singleton_id INTEGER PRIMARY KEY, projection_version INTEGER NOT NULL);
		INSERT INTO managed_settings(singleton_id, projection_version) VALUES(1, 9);
		CREATE TABLE managed_agents (
			agent_id TEXT PRIMARY KEY, pi_session_id TEXT NOT NULL, session_epoch INTEGER NOT NULL,
			runtime_generation TEXT NOT NULL, process_incarnation TEXT NOT NULL,
			process_pid INTEGER NOT NULL, process_start_marker TEXT NOT NULL,
			manifest_revision INTEGER NOT NULL, manifest_hash TEXT NOT NULL,
			lifecycle_state TEXT NOT NULL, activity_state TEXT NOT NULL,
			accepting_prompts INTEGER NOT NULL, updated_at TEXT NOT NULL
		);
		INSERT INTO managed_agents VALUES(
			'agent-1', 'session-1', 2, 'generation-1', 'incarnation-1', ?, 'start-1',
			3, 'manifest-1', 'stopped', 'idle', 1, '2026-01-01T00:00:01Z'
		);`, os.Getpid())
	require.NoError(t, err)
	require.NoError(t, db.Close())

	completionFile := filepath.Join(t.TempDir(), "completion.json")
	authority, err := newManagedRecoveryAuthority(database, provider.URL, "trial-1", "startup-token", completionFile, os.Getpid())
	require.NoError(t, err)
	defer authority.Close()

	barrier := map[string]any{
		"agent_id": "agent-1", "token": "request-token", "observed_monotonic_ms": float64(10),
	}
	_, err = authority.insertBarrier(t.Context(), "trial-1", barrier, managedRecoveryFence{}, 4, monotonicNanoseconds())
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(completionFile, []byte(`{"event":"managed_recovery_complete","trial_id":"trial-1","token":"request-token","pi_session_id":"session-1"}`), 0600))

	get := httptest.NewRequest(http.MethodGet, "/v1/acceptance/managed-recovery?kind=completion&trial_id=trial-1", nil)
	getResponse := httptest.NewRecorder()
	authority.ServeHTTP(getResponse, get)
	require.Equal(t, http.StatusConflict, getResponse.Code, getResponse.Body.String())
	require.Contains(t, getResponse.Body.String(), "not running, accepting prompts, and active or idle")
}
