package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/gitsync"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

type gitSyncTargetPayload struct {
	RepoURL string `json:"repo_url"`
}

type promptTemplatesSyncFile struct {
	Version   int                    `json:"version"`
	Templates []store.PromptTemplate `json:"templates"`
}

type promptTemplatesExportFile struct {
	Format     string                 `json:"format"`
	Version    int                    `json:"version"`
	ExportedAt string                 `json:"exported_at"`
	Templates  []store.PromptTemplate `json:"templates"`
}

func gitSyncRoute(path string) (target string, action string, ok bool) {
	if !strings.HasPrefix(path, "/v1/git-sync/") {
		return "", "", false
	}
	rest := strings.TrimPrefix(path, "/v1/git-sync/")
	if rest == "" {
		return "", "", false
	}
	parts := strings.Split(rest, "/")
	if len(parts) == 1 {
		return parts[0], "", true
	}
	if len(parts) == 2 {
		return parts[0], parts[1], true
	}
	return "", "", false
}

func (r *Router) getGitSyncer() gitsync.Syncer {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.gitSyncer != nil {
		return r.gitSyncer
	}
	// Fallback to default engine
	engine, err := gitsync.New()
	if err != nil {
		return nil
	}
	r.gitSyncer = engine
	return r.gitSyncer
}

// SetGitSyncer allows overriding the git syncer (useful for testing).
func (r *Router) SetGitSyncer(syncer gitsync.Syncer) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.gitSyncer = syncer
}

func (r *Router) handleGitSync(w http.ResponseWriter, req *http.Request, target, action string) {
	if r.store == nil {
		r.writeError(w, &httpError{status: http.StatusInternalServerError, code: "internal_error"})
		return
	}

	target = strings.TrimSpace(target)
	if target == "" {
		r.writeError(w, &httpError{status: http.StatusBadRequest, code: "invalid_request"})
		return
	}

	switch action {
	case "":
		switch req.Method {
		case http.MethodGet:
			t, err := r.store.GetGitSyncTarget(req.Context(), target)
			if err != nil {
				r.writeError(w, &httpError{status: http.StatusInternalServerError, code: "internal_error"})
				return
			}
			if t == nil {
				r.writeError(w, &httpError{status: http.StatusNotFound, code: "not_found"})
				return
			}
			r.writeJSON(w, http.StatusOK, t)
		case http.MethodPut:
			body, err := io.ReadAll(io.LimitReader(req.Body, 64*1024))
			if err != nil {
				r.writeError(w, &httpError{status: http.StatusBadRequest, code: "invalid_request"})
				return
			}
			var p gitSyncTargetPayload
			if err := json.Unmarshal(body, &p); err != nil {
				r.writeError(w, &httpError{status: http.StatusBadRequest, code: "invalid_request"})
				return
			}
			trimmedURL := strings.TrimSpace(p.RepoURL)
			if trimmedURL != "" {
				if err := gitsync.ValidateRepoURL(trimmedURL); err != nil {
					r.writeError(w, &httpError{status: http.StatusBadRequest, code: "invalid_request"})
					return
				}
			}
			saved, err := r.store.SetGitSyncTarget(req.Context(), target, trimmedURL)
			if err != nil {
				r.writeError(w, &httpError{status: http.StatusInternalServerError, code: "internal_error"})
				return
			}
			r.writeJSON(w, http.StatusOK, saved)
		case http.MethodDelete:
			if err := r.store.DeleteGitSyncTarget(req.Context(), target); err != nil {
				r.writeError(w, &httpError{status: http.StatusInternalServerError, code: "internal_error"})
				return
			}
			r.writeJSON(w, http.StatusOK, map[string]any{"ok": true, "target": target})
		default:
			r.writeError(w, &httpError{status: http.StatusMethodNotAllowed, code: "method_not_allowed"})
		}

	case "pull":
		if req.Method != http.MethodPost {
			r.writeError(w, &httpError{status: http.StatusMethodNotAllowed, code: "method_not_allowed"})
			return
		}
		r.handleGitSyncPull(w, req, target)

	case "push":
		if req.Method != http.MethodPost {
			r.writeError(w, &httpError{status: http.StatusMethodNotAllowed, code: "method_not_allowed"})
			return
		}
		r.handleGitSyncPush(w, req, target)

	default:
		r.writeError(w, &httpError{status: http.StatusNotFound, code: "not_found"})
	}
}

func (r *Router) handleGitSyncPull(w http.ResponseWriter, req *http.Request, target string) {
	ctx, cancel := context.WithTimeout(req.Context(), 60*time.Second)
	defer cancel()

	targetConfig, err := r.store.GetGitSyncTarget(ctx, target)
	if err != nil {
		r.writeError(w, &httpError{status: http.StatusInternalServerError, code: "internal_error"})
		return
	}
	if targetConfig == nil || strings.TrimSpace(targetConfig.RepoURL) == "" {
		r.writeError(w, &httpError{status: http.StatusBadRequest, code: "target_not_configured"})
		return
	}

	syncer := r.getGitSyncer()
	if syncer == nil {
		r.writeError(w, &httpError{status: http.StatusInternalServerError, code: "internal_error"})
		return
	}

	switch target {
	case "prompt_templates":
		filename := "prompt-templates.json"
		content, exists, err := syncer.Pull(ctx, targetConfig.RepoURL, filename)
		if err != nil {
			r.writeJSON(w, http.StatusOK, map[string]any{
				"success": false,
				"error":   fmt.Sprintf("Git pull failed: %v", err),
			})
			return
		}
		if !exists {
			r.writeJSON(w, http.StatusOK, map[string]any{
				"exists":  false,
				"message": "File not found",
			})
			return
		}

		var payload promptTemplatesSyncFile
		if err := json.Unmarshal(content, &payload); err != nil {
			r.writeJSON(w, http.StatusOK, map[string]any{
				"success": false,
				"error":   fmt.Sprintf("Invalid JSON in %s: %v", filename, err),
			})
			return
		}

		// Validate incoming prompt templates before replacing
		names := make(map[string]struct{}, len(payload.Templates))
		for _, tpl := range payload.Templates {
			name := strings.TrimSpace(tpl.Name)
			if name == "" {
				r.writeJSON(w, http.StatusBadRequest, map[string]any{
					"error":   "invalid_request",
					"message": "Prompt template name cannot be empty",
				})
				return
			}
			if len(tpl.Name) > 128 {
				r.writeJSON(w, http.StatusBadRequest, map[string]any{
					"error":   "invalid_request",
					"message": fmt.Sprintf("Prompt template name exceeds maximum length of 128: %s", tpl.Name),
				})
				return
			}
			if strings.ContainsRune(tpl.Name, 0) {
				r.writeJSON(w, http.StatusBadRequest, map[string]any{
					"error":   "invalid_request",
					"message": fmt.Sprintf("Prompt template name contains null bytes: %s", tpl.Name),
				})
				return
			}
			body := strings.TrimSpace(tpl.Body)
			if body == "" {
				r.writeJSON(w, http.StatusBadRequest, map[string]any{
					"error":   "invalid_request",
					"message": fmt.Sprintf("Prompt template body cannot be empty for template: %s", tpl.Name),
				})
				return
			}
			if len(tpl.Body) > 65536 {
				r.writeJSON(w, http.StatusBadRequest, map[string]any{
					"error":   "invalid_request",
					"message": fmt.Sprintf("Prompt template body exceeds maximum length of 65536 for template: %s", tpl.Name),
				})
				return
			}
			if strings.ContainsRune(tpl.Body, 0) {
				r.writeJSON(w, http.StatusBadRequest, map[string]any{
					"error":   "invalid_request",
					"message": fmt.Sprintf("Prompt template body contains null bytes for template: %s", tpl.Name),
				})
				return
			}
			if _, exists := names[tpl.Name]; exists {
				r.writeJSON(w, http.StatusBadRequest, map[string]any{
					"error":   "invalid_request",
					"message": fmt.Sprintf("Duplicate prompt template name: %s", tpl.Name),
				})
				return
			}
			names[tpl.Name] = struct{}{}
		}

		if err := r.store.ReplaceAllPromptTemplates(ctx, payload.Templates); err != nil {
			r.writeError(w, &httpError{status: http.StatusInternalServerError, code: "internal_error"})
			return
		}

		r.writeJSON(w, http.StatusOK, map[string]any{
			"exists":    true,
			"count":     len(payload.Templates),
			"templates": payload.Templates,
		})

	case "workflows":
		filename := "workflows.json"
		content, exists, err := syncer.Pull(ctx, targetConfig.RepoURL, filename)
		if err != nil {
			r.writeJSON(w, http.StatusOK, map[string]any{
				"success": false,
				"error":   fmt.Sprintf("Git pull failed: %v", err),
			})
			return
		}
		if !exists {
			r.writeJSON(w, http.StatusOK, map[string]any{
				"target":  "workflows",
				"exists":  false,
				"message": "No workflows.json found in repository",
			})
			return
		}

		var payload store.WorkflowsExportPayload
		if err := json.Unmarshal(content, &payload); err != nil {
			r.writeJSON(w, http.StatusOK, map[string]any{
				"success": false,
				"error":   fmt.Sprintf("Invalid JSON in %s: %v", filename, err),
			})
			return
		}

		if payload.Format != "" && payload.Format != "mecha-workflows" {
			r.writeJSON(w, http.StatusBadRequest, map[string]any{
				"error":   "invalid_request",
				"message": fmt.Sprintf("unsupported format %q, expected mecha-workflows", payload.Format),
			})
			return
		}

		if payload.Version < 1 {
			r.writeJSON(w, http.StatusBadRequest, map[string]any{
				"error":   "invalid_request",
				"message": fmt.Sprintf("unsupported version %d, expected version >= 1", payload.Version),
			})
			return
		}

		for _, bundle := range payload.Workflows {
			if err := r.store.ValidateExportBundle(bundle); err != nil {
				r.writeJSON(w, http.StatusBadRequest, map[string]any{
					"error":   "invalid_request",
					"message": fmt.Sprintf("Invalid workflow bundle %q: %v", bundle.Workflow.Name, err),
				})
				return
			}
		}

		if err := r.store.ReplaceAllWorkflows(ctx, payload.Workflows); err != nil {
			r.writeError(w, &httpError{status: http.StatusInternalServerError, code: "internal_error"})
			return
		}

		r.writeJSON(w, http.StatusOK, map[string]any{
			"target": "workflows",
			"exists": true,
			"count":  len(payload.Workflows),
		})

	default:
		r.writeError(w, &httpError{status: http.StatusBadRequest, code: "unsupported_target"})
	}
}

func (r *Router) handleGitSyncPush(w http.ResponseWriter, req *http.Request, target string) {
	ctx, cancel := context.WithTimeout(req.Context(), 60*time.Second)
	defer cancel()

	targetConfig, err := r.store.GetGitSyncTarget(ctx, target)
	if err != nil {
		r.writeError(w, &httpError{status: http.StatusInternalServerError, code: "internal_error"})
		return
	}
	if targetConfig == nil || strings.TrimSpace(targetConfig.RepoURL) == "" {
		r.writeError(w, &httpError{status: http.StatusBadRequest, code: "target_not_configured"})
		return
	}

	syncer := r.getGitSyncer()
	if syncer == nil {
		r.writeError(w, &httpError{status: http.StatusInternalServerError, code: "internal_error"})
		return
	}

	switch target {
	case "prompt_templates":
		filename := "prompt-templates.json"
		templates, err := r.store.PromptTemplates(ctx)
		if err != nil {
			r.writeError(w, &httpError{status: http.StatusInternalServerError, code: "internal_error"})
			return
		}

		exportPayload := promptTemplatesExportFile{
			Format:     "mecha-prompt-templates",
			Version:    1,
			ExportedAt: r.clock.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
			Templates:  templates,
		}

		contentBytes, err := json.MarshalIndent(exportPayload, "", "  ")
		if err != nil {
			r.writeError(w, &httpError{status: http.StatusInternalServerError, code: "internal_error"})
			return
		}
		contentBytes = append(contentBytes, '\n')

		commitMsg := "Update prompt-templates.json from app"
		if err := syncer.Push(ctx, targetConfig.RepoURL, filename, contentBytes, commitMsg); err != nil {
			r.writeJSON(w, http.StatusOK, map[string]any{
				"success": false,
				"error":   fmt.Sprintf("Git push failed: %v", err),
			})
			return
		}

		r.writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"count":   len(templates),
		})

	case "workflows":
		filename := "workflows.json"
		payload, err := r.store.ExportAllWorkflows(ctx)
		if err != nil {
			r.writeError(w, &httpError{status: http.StatusInternalServerError, code: "internal_error"})
			return
		}

		contentBytes, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			r.writeError(w, &httpError{status: http.StatusInternalServerError, code: "internal_error"})
			return
		}
		contentBytes = append(contentBytes, '\n')

		commitMsg := "Update workflows.json from app"
		if err := syncer.Push(ctx, targetConfig.RepoURL, filename, contentBytes, commitMsg); err != nil {
			r.writeJSON(w, http.StatusOK, map[string]any{
				"success": false,
				"error":   fmt.Sprintf("Git push failed: %v", err),
			})
			return
		}

		r.writeJSON(w, http.StatusOK, map[string]any{
			"target":  "workflows",
			"success": true,
			"count":   len(payload.Workflows),
		})

	default:
		r.writeError(w, &httpError{status: http.StatusBadRequest, code: "unsupported_target"})
	}
}
