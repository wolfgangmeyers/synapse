package transport

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func TestBackgroundBashRESTSnapshotFailure(t *testing.T) {
	r, db, _ := newUILiveTestRouter(t)
	reserveUILiveAgent(t, db, "alpha")
	type contextKey struct{}
	ctx := context.WithValue(context.Background(), contextKey{}, "request")
	for _, tc := range []struct {
		name, path string
		handle     func(http.ResponseWriter, *http.Request, func(context.Context, *store.ManagedAgentRecord) ([]map[string]string, error))
	}{
		{"list", "/v1/managed-agents", r.handleManagedAgentsGETWithSnapshot},
		{"get", "/v1/managed-agents/alpha", r.handleManagedAgentGETWithSnapshot},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			req := httptest.NewRequest(http.MethodGet, tc.path, nil).WithContext(ctx)
			res := httptest.NewRecorder()
			tc.handle(res, req, func(got context.Context, record *store.ManagedAgentRecord) ([]map[string]string, error) {
				called = true
				require.Equal(t, "request", got.Value(contextKey{}))
				require.Equal(t, "alpha", record.AgentID)
				return nil, errors.New("owner/store failure")
			})
			require.True(t, called)
			require.Equal(t, http.StatusInternalServerError, res.Code)
			require.Contains(t, res.Body.String(), "INTERNAL")
			require.NotContains(t, res.Body.String(), "background_bash_jobs")
			require.NotContains(t, res.Body.String(), "owner/store failure")
		})
	}
}

// Exercise registered routes as well as the injected snapshot failure above.
func TestBackgroundBashRESTRegisteredRoutes(t *testing.T) {
	r, db, _ := newUILiveTestRouter(t)
	reserveUILiveAgent(t, db, "alpha")
	for _, tc := range []struct {
		name, path, field string
	}{
		{"list", "/v1/managed-agents", "agents"},
		{"detail", "/v1/managed-agents/alpha", "agent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := httptest.NewRecorder()
			r.ServeHTTP(res, httptest.NewRequest(http.MethodGet, tc.path, nil))
			require.Equal(t, http.StatusOK, res.Code, res.Body.String())
			var body map[string]any
			require.NoError(t, json.Unmarshal(res.Body.Bytes(), &body))
			var agent map[string]any
			if tc.field == "agents" {
				agents := body[tc.field].([]any)
				require.Len(t, agents, 1)
				agent = agents[0].(map[string]any)
			} else {
				agent = body[tc.field].(map[string]any)
			}
			require.Equal(t, "alpha", agent["agent_id"])
			require.Equal(t, []any{}, agent["background_bash_jobs"])

			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			res = httptest.NewRecorder()
			r.ServeHTTP(res, httptest.NewRequest(http.MethodGet, tc.path, nil).WithContext(ctx))
			require.Equal(t, http.StatusInternalServerError, res.Code, res.Body.String())
			require.Contains(t, res.Body.String(), "INTERNAL")
			require.NotContains(t, res.Body.String(), "background_bash_jobs")
		})
	}
}
