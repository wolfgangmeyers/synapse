package transport

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

func (r *Router) handleContextThresholdConfigGET(w http.ResponseWriter, req *http.Request) {
	if r.store == nil {
		r.writeError(w, &httpError{status: http.StatusInternalServerError, code: "internal_error"})
		return
	}
	cfg, err := r.store.GetContextThresholdConfig(req.Context())
	if err != nil {
		r.writeError(w, &httpError{status: http.StatusInternalServerError, code: "internal_error"})
		return
	}
	if cfg.ModelThresholds == nil {
		cfg.ModelThresholds = make(map[string]store.ModelContextThreshold)
	}
	r.writeJSON(w, http.StatusOK, cfg)
}

func (r *Router) handleContextThresholdConfigPUT(w http.ResponseWriter, req *http.Request) {
	if r.store == nil {
		r.writeError(w, &httpError{status: http.StatusInternalServerError, code: "internal_error"})
		return
	}
	body, err := io.ReadAll(io.LimitReader(req.Body, 64*1024))
	if err != nil {
		r.writeError(w, &httpError{status: http.StatusBadRequest, code: "invalid_request"})
		return
	}

	var cfg store.ContextThresholdConfig
	if err := json.Unmarshal(body, &cfg); err != nil {
		r.writeError(w, &httpError{status: http.StatusBadRequest, code: "invalid_request"})
		return
	}

	if cfg.WarningPercent < 1 || cfg.ResetPercent <= cfg.WarningPercent || cfg.ResetPercent > 100 {
		r.writeError(w, &httpError{
			status: http.StatusBadRequest,
			code:   "invalid_request",
		})
		return
	}

	for modelKey, override := range cfg.ModelThresholds {
		if modelKey == "" {
			r.writeError(w, &httpError{
				status: http.StatusBadRequest,
				code:   "invalid_request",
			})
			return
		}
		if override.WarningPercent < 1 || override.ResetPercent <= override.WarningPercent || override.ResetPercent > 100 {
			r.writeError(w, &httpError{
				status: http.StatusBadRequest,
				code:   "invalid_request",
			})
			return
		}
	}

	if err := r.store.SetContextThresholdConfig(req.Context(), cfg); err != nil {
		r.writeError(w, &httpError{status: http.StatusInternalServerError, code: "internal_error"})
		return
	}

	saved, err := r.store.GetContextThresholdConfig(req.Context())
	if err != nil {
		r.writeError(w, &httpError{status: http.StatusInternalServerError, code: "internal_error"})
		return
	}
	if saved.ModelThresholds == nil {
		saved.ModelThresholds = make(map[string]store.ModelContextThreshold)
	}
	r.writeJSON(w, http.StatusOK, saved)
}
