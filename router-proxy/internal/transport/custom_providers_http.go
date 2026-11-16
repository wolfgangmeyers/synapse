package transport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/crypto"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

// CustomProviderModelResponse represents a model returned in HTTP responses.
type CustomProviderModelResponse struct {
	ID            string `json:"id"`
	DisplayName   string `json:"display_name"`
	ContextWindow int    `json:"context_window"`
	MaxTokens     int    `json:"max_tokens"`
	SupportsTools bool   `json:"supports_tools"`
}

// CustomProviderResponse represents a provider formatted for JSON responses.
type CustomProviderResponse struct {
	ID            string                        `json:"id"`
	Label         string                        `json:"label"`
	BaseURL       string                        `json:"base_url"`
	Protocol      string                        `json:"protocol"`
	Enabled       bool                          `json:"enabled"`
	HasAPIKey     bool                          `json:"has_api_key"`
	APIKeyMasked  string                        `json:"api_key_masked,omitempty"`
	Models        []CustomProviderModelResponse `json:"models"`
	CreatedAt     string                        `json:"created_at"`
	UpdatedAt     string                        `json:"updated_at"`
}

type customProviderCreateOrUpdateRequest struct {
	ID       string                        `json:"id"`
	Label    string                        `json:"label"`
	BaseURL  string                        `json:"base_url"`
	Protocol string                        `json:"protocol"`
	APIKey   *string                       `json:"api_key,omitempty"`
	Enabled  *bool                         `json:"enabled,omitempty"`
	Models   []CustomProviderModelResponse `json:"models"`
}

type customProviderDiscoverRequest struct {
	BaseURL string `json:"base_url"`
	APIKey  string `json:"api_key"`
}

func customProviderRoute(path string) (id string, ok bool) {
	prefix := "/v1/custom-providers/"
	if !strings.HasPrefix(path, prefix) {
		return "", false
	}
	raw := strings.TrimPrefix(path, prefix)
	if raw == "" || strings.Contains(raw, "/") {
		return "", false
	}
	unescaped, err := url.PathUnescape(raw)
	if err != nil || unescaped == "" {
		return "", false
	}
	return unescaped, true
}

func formatCustomProviderResponse(p *store.CustomProviderWithModels) CustomProviderResponse {
	hasAPIKey := p.Provider.APIKeyCiphertext != nil && *p.Provider.APIKeyCiphertext != ""
	var masked string
	if hasAPIKey {
		masked = "sk-••••••••"
	}

	models := make([]CustomProviderModelResponse, 0, len(p.Models))
	for _, m := range p.Models {
		models = append(models, CustomProviderModelResponse{
			ID:            m.ID,
			DisplayName:   m.DisplayName,
			ContextWindow: m.ContextWindow,
			MaxTokens:     m.MaxTokens,
			SupportsTools: m.SupportsTools,
		})
	}

	createdStr := ""
	if !p.Provider.CreatedAt.IsZero() {
		createdStr = p.Provider.CreatedAt.UTC().Format(time.RFC3339Nano)
	}
	updatedStr := ""
	if !p.Provider.UpdatedAt.IsZero() {
		updatedStr = p.Provider.UpdatedAt.UTC().Format(time.RFC3339Nano)
	}

	return CustomProviderResponse{
		ID:           p.Provider.ID,
		Label:        p.Provider.Label,
		BaseURL:      p.Provider.BaseURL,
		Protocol:     p.Provider.Protocol,
		Enabled:      p.Provider.Enabled,
		HasAPIKey:    hasAPIKey,
		APIKeyMasked: masked,
		Models:       models,
		CreatedAt:    createdStr,
		UpdatedAt:    updatedStr,
	}
}

func (r *Router) handleCustomProvidersCollection(w http.ResponseWriter, req *http.Request) {
	switch req.Method {
	case http.MethodGet:
		providers, err := r.store.ListCustomProviders(req.Context())
		if err != nil {
			r.writeError(w, &httpError{status: http.StatusInternalServerError, code: "internal_error"})
			return
		}
		respList := make([]CustomProviderResponse, 0, len(providers))
		for _, p := range providers {
			respList = append(respList, formatCustomProviderResponse(p))
		}
		r.writeJSON(w, http.StatusOK, map[string]any{"custom_providers": respList})

	case http.MethodPost:
		if !isJSON(req.Header.Get("Content-Type")) {
			r.writeError(w, &httpError{status: http.StatusBadRequest, code: "invalid_request"})
			return
		}
		body, err := io.ReadAll(io.LimitReader(req.Body, int64(r.maxRequest)+1))
		if err != nil || len(body) > r.maxRequest {
			r.writeError(w, &httpError{status: http.StatusBadRequest, code: "invalid_request"})
			return
		}
		var parsed customProviderCreateOrUpdateRequest
		if err := json.Unmarshal(body, &parsed); err != nil {
			r.writeError(w, &httpError{status: http.StatusBadRequest, code: "invalid_request"})
			return
		}

		parsed.ID = strings.TrimSpace(parsed.ID)
		parsed.Label = strings.TrimSpace(parsed.Label)
		parsed.BaseURL = strings.TrimSpace(parsed.BaseURL)
		if parsed.ID == "" || parsed.Label == "" || parsed.BaseURL == "" {
			r.writeError(w, &httpError{status: http.StatusBadRequest, code: "invalid_request"})
			return
		}

		// Encrypt API key if provided
		var ciphertext *string
		if parsed.APIKey != nil && strings.TrimSpace(*parsed.APIKey) != "" {
			key, err := crypto.DeriveKey("")
			if err != nil || len(key) != crypto.KeySize {
				r.writeError(w, &httpError{status: http.StatusInternalServerError, code: "internal_error"})
				return
			}
			enc, err := crypto.Encrypt([]byte(strings.TrimSpace(*parsed.APIKey)), key)
			if err != nil {
				r.writeError(w, &httpError{status: http.StatusInternalServerError, code: "internal_error"})
				return
			}
			ciphertext = &enc
		}

		enabled := true
		if parsed.Enabled != nil {
			enabled = *parsed.Enabled
		}

		protocol := strings.TrimSpace(parsed.Protocol)
		if protocol == "" {
			protocol = "openai-compatible"
		}

		providerRec := store.CustomProviderRecord{
			ID:               parsed.ID,
			Label:            parsed.Label,
			BaseURL:          parsed.BaseURL,
			Protocol:         protocol,
			APIKeyCiphertext: ciphertext,
			Enabled:          enabled,
		}

		modelsRec := make([]store.CustomProviderModelRecord, 0, len(parsed.Models))
		for _, m := range parsed.Models {
			mID := strings.TrimSpace(m.ID)
			if mID == "" {
				continue
			}
			dispName := strings.TrimSpace(m.DisplayName)
			if dispName == "" {
				dispName = mID
			}
			ctxWin := m.ContextWindow
			if ctxWin <= 0 {
				ctxWin = 131072
			}
			maxTok := m.MaxTokens
			if maxTok <= 0 {
				maxTok = 8192
			}
			modelsRec = append(modelsRec, store.CustomProviderModelRecord{
				ID:            mID,
				ProviderID:    parsed.ID,
				DisplayName:   dispName,
				ContextWindow: ctxWin,
				MaxTokens:     maxTok,
				SupportsTools: m.SupportsTools,
			})
		}

		if err := r.store.CreateCustomProvider(req.Context(), providerRec, modelsRec); err != nil {
			// Check if duplicate
			if strings.Contains(err.Error(), "UNIQUE constraint failed") {
				r.writeError(w, &httpError{status: http.StatusConflict, code: "conflict"})
				return
			}
			r.writeError(w, &httpError{status: http.StatusBadRequest, code: "invalid_request"})
			return
		}

		created, err := r.store.GetCustomProvider(req.Context(), parsed.ID)
		if err != nil {
			r.writeError(w, &httpError{status: http.StatusInternalServerError, code: "internal_error"})
			return
		}

		_ = managed.SyncCustomModelsToAI(req.Context(), r.store)

		r.writeJSON(w, http.StatusCreated, map[string]any{"custom_provider": formatCustomProviderResponse(created)})

	default:
		r.writeError(w, &httpError{status: http.StatusMethodNotAllowed, code: "method_not_allowed"})
	}
}

func (r *Router) handleCustomProviderItem(w http.ResponseWriter, req *http.Request, id string) {
	if id == "" {
		r.writeError(w, &httpError{status: http.StatusBadRequest, code: "invalid_request"})
		return
	}

	switch req.Method {
	case http.MethodGet:
		p, err := r.store.GetCustomProvider(req.Context(), id)
		if err != nil {
			if errors.Is(err, store.ErrCustomProviderNotFound) {
				r.writeError(w, &httpError{status: http.StatusNotFound, code: "not_found"})
				return
			}
			r.writeError(w, &httpError{status: http.StatusInternalServerError, code: "internal_error"})
			return
		}
		r.writeJSON(w, http.StatusOK, map[string]any{"custom_provider": formatCustomProviderResponse(p)})

	case http.MethodPut:
		existing, err := r.store.GetCustomProvider(req.Context(), id)
		if err != nil {
			if errors.Is(err, store.ErrCustomProviderNotFound) {
				r.writeError(w, &httpError{status: http.StatusNotFound, code: "not_found"})
				return
			}
			r.writeError(w, &httpError{status: http.StatusInternalServerError, code: "internal_error"})
			return
		}

		if !isJSON(req.Header.Get("Content-Type")) {
			r.writeError(w, &httpError{status: http.StatusBadRequest, code: "invalid_request"})
			return
		}
		body, err := io.ReadAll(io.LimitReader(req.Body, int64(r.maxRequest)+1))
		if err != nil || len(body) > r.maxRequest {
			r.writeError(w, &httpError{status: http.StatusBadRequest, code: "invalid_request"})
			return
		}
		var parsed customProviderCreateOrUpdateRequest
		if err := json.Unmarshal(body, &parsed); err != nil {
			r.writeError(w, &httpError{status: http.StatusBadRequest, code: "invalid_request"})
			return
		}

		label := strings.TrimSpace(parsed.Label)
		if label == "" {
			label = existing.Provider.Label
		}
		baseURL := strings.TrimSpace(parsed.BaseURL)
		if baseURL == "" {
			baseURL = existing.Provider.BaseURL
		}
		protocol := strings.TrimSpace(parsed.Protocol)
		if protocol == "" {
			protocol = existing.Provider.Protocol
		}
		enabled := existing.Provider.Enabled
		if parsed.Enabled != nil {
			enabled = *parsed.Enabled
		}

		ciphertext := existing.Provider.APIKeyCiphertext
		if parsed.APIKey != nil {
			trimmedKey := strings.TrimSpace(*parsed.APIKey)
			if trimmedKey == "" {
				ciphertext = nil
			} else {
				key, err := crypto.DeriveKey("")
				if err != nil || len(key) != crypto.KeySize {
					r.writeError(w, &httpError{status: http.StatusInternalServerError, code: "internal_error"})
					return
				}
				enc, err := crypto.Encrypt([]byte(trimmedKey), key)
				if err != nil {
					r.writeError(w, &httpError{status: http.StatusInternalServerError, code: "internal_error"})
					return
				}
				ciphertext = &enc
			}
		}

		providerRec := store.CustomProviderRecord{
			ID:               id,
			Label:            label,
			BaseURL:          baseURL,
			Protocol:         protocol,
			APIKeyCiphertext: ciphertext,
			Enabled:          enabled,
		}

		modelsRec := make([]store.CustomProviderModelRecord, 0, len(parsed.Models))
		for _, m := range parsed.Models {
			mID := strings.TrimSpace(m.ID)
			if mID == "" {
				continue
			}
			dispName := strings.TrimSpace(m.DisplayName)
			if dispName == "" {
				dispName = mID
			}
			ctxWin := m.ContextWindow
			if ctxWin <= 0 {
				ctxWin = 131072
			}
			maxTok := m.MaxTokens
			if maxTok <= 0 {
				maxTok = 8192
			}
			modelsRec = append(modelsRec, store.CustomProviderModelRecord{
				ID:            mID,
				ProviderID:    id,
				DisplayName:   dispName,
				ContextWindow: ctxWin,
				MaxTokens:     maxTok,
				SupportsTools: m.SupportsTools,
			})
		}

		if err := r.store.UpdateCustomProvider(req.Context(), providerRec, modelsRec); err != nil {
			r.writeError(w, &httpError{status: http.StatusBadRequest, code: "invalid_request"})
			return
		}

		updated, err := r.store.GetCustomProvider(req.Context(), id)
		if err != nil {
			r.writeError(w, &httpError{status: http.StatusInternalServerError, code: "internal_error"})
			return
		}

		_ = managed.SyncCustomModelsToAI(req.Context(), r.store)

		r.writeJSON(w, http.StatusOK, map[string]any{"custom_provider": formatCustomProviderResponse(updated)})

	case http.MethodDelete:
		if err := r.store.DeleteCustomProvider(req.Context(), id); err != nil {
			if errors.Is(err, store.ErrCustomProviderNotFound) {
				r.writeError(w, &httpError{status: http.StatusNotFound, code: "not_found"})
				return
			}
			r.writeError(w, &httpError{status: http.StatusInternalServerError, code: "internal_error"})
			return
		}
		_ = managed.SyncCustomModelsToAI(req.Context(), r.store)

		r.writeJSON(w, http.StatusOK, map[string]any{"ok": true})

	default:
		r.writeError(w, &httpError{status: http.StatusMethodNotAllowed, code: "method_not_allowed"})
	}
}

type openAIModelListResponse struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}

func (r *Router) handleCustomProvidersDiscover(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		r.writeError(w, &httpError{status: http.StatusMethodNotAllowed, code: "method_not_allowed"})
		return
	}

	if !isJSON(req.Header.Get("Content-Type")) {
		r.writeError(w, &httpError{status: http.StatusBadRequest, code: "invalid_request"})
		return
	}
	body, err := io.ReadAll(io.LimitReader(req.Body, int64(r.maxRequest)+1))
	if err != nil || len(body) > r.maxRequest {
		r.writeError(w, &httpError{status: http.StatusBadRequest, code: "invalid_request"})
		return
	}

	var parsed customProviderDiscoverRequest
	if err := json.Unmarshal(body, &parsed); err != nil {
		r.writeError(w, &httpError{status: http.StatusBadRequest, code: "invalid_request"})
		return
	}

	baseURL := strings.TrimRight(strings.TrimSpace(parsed.BaseURL), "/")
	if baseURL == "" {
		r.writeError(w, &httpError{status: http.StatusBadRequest, code: "invalid_request"})
		return
	}

	parsedURL, err := url.Parse(baseURL)
	if err != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") {
		r.writeError(w, &httpError{status: http.StatusBadRequest, code: "invalid_request"})
		return
	}

	client := &http.Client{Timeout: 5 * time.Second}

	// Try discovery endpoints:
	// If baseURL already ends with /v1, try baseURL + "/models"
	// Otherwise try baseURL + "/v1/models", and fallback to baseURL + "/models"
	var candidateURLs []string
	if strings.HasSuffix(baseURL, "/v1") {
		candidateURLs = []string{baseURL + "/models"}
	} else {
		candidateURLs = []string{baseURL + "/v1/models", baseURL + "/models"}
	}

	var resp *http.Response
	var fetchErr error
	for _, targetURL := range candidateURLs {
		reqDiscovery, err := http.NewRequestWithContext(req.Context(), http.MethodGet, targetURL, nil)
		if err != nil {
			continue
		}
		if apiKey := strings.TrimSpace(parsed.APIKey); apiKey != "" {
			reqDiscovery.Header.Set("Authorization", "Bearer "+apiKey)
		}
		resp, fetchErr = client.Do(reqDiscovery)
		if fetchErr == nil && resp.StatusCode == http.StatusOK {
			break
		}
		if resp != nil {
			resp.Body.Close()
			resp = nil
		}
	}

	if resp == nil {
		status := http.StatusBadGateway
		if fetchErr != nil && errors.Is(fetchErr, context.DeadlineExceeded) {
			status = http.StatusGatewayTimeout
		}
		r.writeJSON(w, status, map[string]any{
			"error":   "Failed to discover models from endpoint",
			"code":    "discovery_failed",
			"details": fmt.Sprintf("%v", fetchErr),
		})
		return
	}
	defer resp.Body.Close()

	// Read up to 2 MiB with io.LimitReader
	limitReader := io.LimitReader(resp.Body, 2*1024*1024+1)
	respBody, err := io.ReadAll(limitReader)
	if err != nil || len(respBody) > 2*1024*1024 {
		r.writeJSON(w, http.StatusBadGateway, map[string]any{
			"error": "Model discovery response too large",
			"code":  "discovery_failed",
		})
		return
	}

	var openAIResp openAIModelListResponse
	if err := json.Unmarshal(respBody, &openAIResp); err != nil {
		r.writeJSON(w, http.StatusBadGateway, map[string]any{
			"error": "Failed to parse models response from endpoint",
			"code":  "discovery_failed",
		})
		return
	}

	models := make([]CustomProviderModelResponse, 0, len(openAIResp.Data))
	for _, item := range openAIResp.Data {
		mID := strings.TrimSpace(item.ID)
		if mID == "" {
			continue
		}
		models = append(models, CustomProviderModelResponse{
			ID:            mID,
			DisplayName:   mID,
			ContextWindow: 131072,
			MaxTokens:     8192,
			SupportsTools: true,
		})
	}

	r.writeJSON(w, http.StatusOK, map[string]any{"models": models})
}
