package transport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/systemone"
)

// InvokeSystemOne invokes the router-wide configured System 1 server.
// Returns systemone.ErrNotConfigured or systemone.ErrDisabled when unconfigured.
func (r *Router) InvokeSystemOne(ctx context.Context, payload any) (*systemone.Response, error) {
	if r.store == nil {
		return nil, systemone.ErrNotConfigured
	}
	cfg, err := r.store.GetSystemOneConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("read system one config: %w", err)
	}
	sysCfg := systemone.Config{
		Host:    cfg.Host,
		Port:    cfg.Port,
		Enabled: cfg.Enabled,
	}
	if r.systemOneClient == nil {
		r.systemOneClient = systemone.NewClient(nil)
	}
	return r.systemOneClient.Invoke(ctx, sysCfg, payload)
}

func (r *Router) handleSystemOneConfigGET(w http.ResponseWriter, req *http.Request) {
	if r.store == nil {
		r.writeError(w, &httpError{status: http.StatusInternalServerError, code: "internal_error"})
		return
	}
	cfg, err := r.store.GetSystemOneConfig(req.Context())
	if err != nil {
		r.writeError(w, &httpError{status: http.StatusInternalServerError, code: "internal_error"})
		return
	}
	r.writeJSON(w, http.StatusOK, cfg)
}

func (r *Router) handleSystemOneConfigPUT(w http.ResponseWriter, req *http.Request) {
	if r.store == nil {
		r.writeError(w, &httpError{status: http.StatusInternalServerError, code: "internal_error"})
		return
	}
	body, err := io.ReadAll(io.LimitReader(req.Body, 64*1024))
	if err != nil {
		r.writeError(w, &httpError{status: http.StatusBadRequest, code: "invalid_request"})
		return
	}
	var cfg store.SystemOneConfig
	if err := json.Unmarshal(body, &cfg); err != nil {
		r.writeError(w, &httpError{status: http.StatusBadRequest, code: "invalid_request"})
		return
	}
	if cfg.Port < 0 || cfg.Port > 65535 {
		r.writeError(w, &httpError{status: http.StatusBadRequest, code: "invalid_request"})
		return
	}
	if err := r.store.SetSystemOneConfig(req.Context(), cfg); err != nil {
		r.writeError(w, &httpError{status: http.StatusInternalServerError, code: "internal_error"})
		return
	}
	r.writeJSON(w, http.StatusOK, cfg)
}

func (r *Router) handleSystemOneTestPOST(w http.ResponseWriter, req *http.Request) {
	body, err := io.ReadAll(io.LimitReader(req.Body, 1024*1024))
	if err != nil {
		r.writeError(w, &httpError{status: http.StatusBadRequest, code: "invalid_request"})
		return
	}

	var payload any
	if len(body) > 0 && string(body) != "{}" {
		var raw map[string]any
		if err := json.Unmarshal(body, &raw); err != nil {
			r.writeError(w, &httpError{status: http.StatusBadRequest, code: "invalid_request"})
			return
		}
		if _, ok := raw["questions"]; ok {
			payload = raw
		}
	}

	// Default pre-populated example if none provided
	if payload == nil {
		payload = systemone.Request{
			Model: "clef",
			State: "POST /api/v1/auth/login HTTP/1.1\nHost: production-service.internal\nUser-Agent: sqlmap/1.8.3#stable\nContent-Type: application/json\n\n{\"username\": \"admin' OR '1'='1\", \"password\": \"test\"}",
			Questions: map[string]any{
				"is_sql_injection": map[string]any{
					"type":         "noul",
					"instructions": "Determine if this payload contains an SQL injection attack attempt.",
					"criteria": map[string]string{
						"true":  "Input contains SQL injection syntax or exploitation attempts.",
						"false": "Input is benign standard authentication traffic.",
					},
				},
			},
		}
	}

	resp, err := r.InvokeSystemOne(req.Context(), payload)
	if err != nil {
		r.writeJSON(w, http.StatusOK, map[string]any{
			"status": "error",
			"error":  err.Error(),
		})
		return
	}

	r.writeJSON(w, http.StatusOK, map[string]any{
		"status":   "ok",
		"response": resp,
	})
}

func (r *Router) handleSystemOneGET(w http.ResponseWriter, req *http.Request) {
	if r.store == nil {
		r.writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error":     "system one is not configured or disabled",
			"available": false,
		})
		return
	}
	cfg, err := r.store.GetSystemOneConfig(req.Context())
	if err != nil || !cfg.Enabled || strings.TrimSpace(cfg.Host) == "" || cfg.Port <= 0 {
		r.writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error":     "system one is not configured or disabled",
			"available": false,
		})
		return
	}
	r.writeJSON(w, http.StatusOK, map[string]any{
		"available": true,
		"host":      cfg.Host,
		"port":      cfg.Port,
		"model":     "clef",
	})
}

func (r *Router) handleSystemOnePOST(w http.ResponseWriter, req *http.Request) {
	body, err := io.ReadAll(io.LimitReader(req.Body, 10*1024*1024))
	if err != nil {
		r.writeJSON(w, http.StatusBadRequest, map[string]any{
			"error":   "invalid_request",
			"message": "failed to read request body",
		})
		return
	}
	if len(body) == 0 {
		r.writeJSON(w, http.StatusBadRequest, map[string]any{
			"error":   "invalid_request",
			"message": "request body must not be empty",
		})
		return
	}

	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		r.writeJSON(w, http.StatusBadRequest, map[string]any{
			"error":   "invalid_request",
			"message": "request body must be valid JSON",
		})
		return
	}

	if _, ok := raw["state"]; !ok {
		r.writeJSON(w, http.StatusBadRequest, map[string]any{
			"error":   "invalid_request",
			"message": "missing required field: state",
		})
		return
	}
	if _, ok := raw["questions"]; !ok {
		r.writeJSON(w, http.StatusBadRequest, map[string]any{
			"error":   "invalid_request",
			"message": "missing required field: questions",
		})
		return
	}

	resp, err := r.InvokeSystemOne(req.Context(), body)
	if err != nil {
		if errors.Is(err, systemone.ErrDisabled) || errors.Is(err, systemone.ErrNotConfigured) {
			r.writeJSON(w, http.StatusServiceUnavailable, map[string]any{
				"error":     "system one is not configured or disabled",
				"available": false,
			})
			return
		}
		if errors.Is(err, systemone.ErrTimeout) || errors.Is(err, context.DeadlineExceeded) {
			r.writeJSON(w, http.StatusGatewayTimeout, map[string]any{
				"error":     "system one request timed out",
				"available": false,
			})
			return
		}
		r.writeJSON(w, http.StatusBadGateway, map[string]any{
			"error":     fmt.Sprintf("system one backend call failed: %v", err),
			"available": false,
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if len(resp.Raw) > 0 {
		_, _ = w.Write(resp.Raw)
	} else {
		_ = json.NewEncoder(w).Encode(resp)
	}
}
