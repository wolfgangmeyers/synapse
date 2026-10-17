package transport

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/crypto"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/mcp"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

// MCPManager returns the router's active mcp.Manager, lazily initializing it if needed.
func (r *Router) MCPManager() *mcp.Manager {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.mcpManager != nil {
		return r.mcpManager
	}

	key, err := crypto.DeriveKey("")
	if err != nil || len(key) != crypto.KeySize {
		key = make([]byte, crypto.KeySize)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	r.mcpManager = mcp.NewManager(r.store, key, client)
	return r.mcpManager
}

func (r *Router) broadcastMCPServerUpdated(s *store.MCPServer) {
	if s == nil {
		return
	}
	var lastSyncStr *string
	if s.LastSyncAt != nil && !s.LastSyncAt.IsZero() {
		str := s.LastSyncAt.UTC().Format(time.RFC3339)
		lastSyncStr = &str
	}
	payload := map[string]any{
		"type": "mcp_server_updated",
		"server": map[string]any{
			"id":           s.ID,
			"name":         s.Name,
			"status":       s.Status,
			"tools_count":  s.ToolsCount,
			"last_sync_at": lastSyncStr,
		},
	}
	r.broadcast(payload)
}

func (r *Router) handleMCPServersCollection(w http.ResponseWriter, req *http.Request) {
	mgr := r.MCPManager()
	switch req.Method {
	case http.MethodGet:
		servers, err := r.store.ListMCPServers(req.Context())
		if err != nil {
			r.writeError(w, &httpError{status: http.StatusInternalServerError, code: "internal_error"})
			return
		}
		if servers == nil {
			servers = []*store.MCPServer{}
		}
		r.writeJSON(w, http.StatusOK, map[string]any{"servers": servers})

	case http.MethodPost:
		payload, err := r.readJSON(req)
		if err != nil {
			r.writeError(w, err)
			return
		}

		serverReq, err := parseMCPServerCreateRequest(payload)
		if err != nil {
			r.writeError(w, err)
			return
		}

		// Check URL uniqueness
		existing, err := r.store.GetMCPServerByURL(req.Context(), serverReq.URL)
		if err == nil && existing != nil {
			r.writeError(w, &httpError{status: http.StatusConflict, code: "conflict"})
			return
		}

		created, err := mgr.AddServer(req.Context(), serverReq)
		if err != nil {
			r.writeError(w, &httpError{status: http.StatusInternalServerError, code: "internal_error"})
			return
		}

		// If auth_type is none, attempt initial handshake & tool sync
		if created.AuthType == "none" {
			_ = mgr.SyncTools(req.Context(), created.ID)
			// reload after sync
			if reloaded, err := r.store.GetMCPServer(req.Context(), created.ID); err == nil {
				created = reloaded
			}
		}

		r.broadcastMCPServerUpdated(created)

		resp := map[string]any{"server": created}
		if created.AuthType == "oauth" {
			redirectURI := r.oauthCallbackURL(req)
			if authURL, err := mgr.StartAuth(req.Context(), created.ID, redirectURI); err == nil {
				resp["auth_url"] = authURL
			}
		}

		r.writeJSON(w, http.StatusCreated, resp)

	default:
		r.writeError(w, &httpError{status: http.StatusMethodNotAllowed, code: "method_not_allowed"})
	}
}

func (r *Router) handleMCPServerItem(w http.ResponseWriter, req *http.Request, id, subAction, subSub string) {
	if id == "" {
		r.writeError(w, &httpError{status: http.StatusBadRequest, code: "invalid_request"})
		return
	}

	mgr := r.MCPManager()

	switch {
	case subAction == "" && subSub == "":
		switch req.Method {
		case http.MethodGet:
			server, err := r.store.GetMCPServer(req.Context(), id)
			if err != nil {
				if errors.Is(err, store.ErrMCPServerNotFound) {
					r.writeError(w, &httpError{status: http.StatusNotFound, code: "not_found"})
					return
				}
				r.writeError(w, &httpError{status: http.StatusInternalServerError, code: "internal_error"})
				return
			}

			tools, err := r.store.ListMCPTools(req.Context(), id)
			if err != nil {
				r.writeError(w, &httpError{status: http.StatusInternalServerError, code: "internal_error"})
				return
			}
			if tools == nil {
				tools = []store.MCPToolRecord{}
			}

			// Token status without exposing secret
			var tokenInfo map[string]any
			key := mgr.SecretKey()
			if len(key) == crypto.KeySize {
				tok, err := r.store.GetMCPToken(req.Context(), id, key)
				if err == nil && tok != nil {
					hasRefresh := tok.RefreshToken != nil && *tok.RefreshToken != ""
					tokenInfo = map[string]any{
						"token_type":        tok.TokenType,
						"expires_at":        tok.ExpiresAt,
						"has_refresh_token": hasRefresh,
						"scopes":            tok.Scopes,
						"updated_at":        tok.UpdatedAt,
					}
				}
			}

			resp := map[string]any{
				"server": server,
				"tools":  tools,
			}
			if tokenInfo != nil {
				resp["token"] = tokenInfo
			}
			r.writeJSON(w, http.StatusOK, resp)

		case http.MethodPut:
			existing, err := r.store.GetMCPServer(req.Context(), id)
			if err != nil {
				if errors.Is(err, store.ErrMCPServerNotFound) {
					r.writeError(w, &httpError{status: http.StatusNotFound, code: "not_found"})
					return
				}
				r.writeError(w, &httpError{status: http.StatusInternalServerError, code: "internal_error"})
				return
			}

			payload, err := r.readJSON(req)
			if err != nil {
				r.writeError(w, err)
				return
			}

			if err := applyMCPServerUpdate(existing, payload); err != nil {
				r.writeError(w, err)
				return
			}

			if err := r.store.UpdateMCPServer(req.Context(), existing); err != nil {
				r.writeError(w, &httpError{status: http.StatusInternalServerError, code: "internal_error"})
				return
			}

			r.broadcastMCPServerUpdated(existing)
			r.writeJSON(w, http.StatusOK, map[string]any{"server": existing})

		case http.MethodDelete:
			err := r.store.DeleteMCPServer(req.Context(), id)
			if err != nil {
				if errors.Is(err, store.ErrMCPServerNotFound) {
					r.writeError(w, &httpError{status: http.StatusNotFound, code: "not_found"})
					return
				}
				r.writeError(w, &httpError{status: http.StatusInternalServerError, code: "internal_error"})
				return
			}

			r.broadcastMCPServerUpdated(&store.MCPServer{
				ID:         id,
				Status:     "deleted",
				ToolsCount: 0,
			})
			r.writeNoContent(w)

		default:
			r.writeError(w, &httpError{status: http.StatusMethodNotAllowed, code: "method_not_allowed"})
		}

	case (subAction == "sync" || subAction == "refresh-tools") && subSub == "":
		if req.Method != http.MethodPost {
			r.writeError(w, &httpError{status: http.StatusMethodNotAllowed, code: "method_not_allowed"})
			return
		}

		server, err := r.store.GetMCPServer(req.Context(), id)
		if err != nil {
			if errors.Is(err, store.ErrMCPServerNotFound) {
				r.writeError(w, &httpError{status: http.StatusNotFound, code: "not_found"})
				return
			}
			r.writeError(w, &httpError{status: http.StatusInternalServerError, code: "internal_error"})
			return
		}

		syncErr := mgr.SyncTools(req.Context(), id)
		updated, _ := r.store.GetMCPServer(req.Context(), id)
		if updated == nil {
			updated = server
		}
		r.broadcastMCPServerUpdated(updated)

		if syncErr != nil {
			status := http.StatusBadGateway
			if errors.Is(syncErr, mcp.ErrAuthRequired) || errors.Is(syncErr, mcp.ErrUnauthorized) {
				status = http.StatusUnauthorized
			}
			r.writeJSON(w, status, map[string]any{
				"error": map[string]any{
					"code":    "sync_failed",
					"message": syncErr.Error(),
				},
				"server": updated,
			})
			return
		}

		tools, _ := r.store.ListMCPTools(req.Context(), id)
		if tools == nil {
			tools = []store.MCPToolRecord{}
		}

		r.writeJSON(w, http.StatusOK, map[string]any{
			"server": updated,
			"tools":  tools,
		})

	case subAction == "auth" && subSub == "start":
		if req.Method != http.MethodPost {
			r.writeError(w, &httpError{status: http.StatusMethodNotAllowed, code: "method_not_allowed"})
			return
		}

		redirectURI := r.oauthCallbackURL(req)
		authURL, err := mgr.StartAuth(req.Context(), id, redirectURI)
		if err != nil {
			if errors.Is(err, store.ErrMCPServerNotFound) {
				r.writeError(w, &httpError{status: http.StatusNotFound, code: "not_found"})
				return
			}
			r.writeError(w, &httpError{status: http.StatusBadRequest, code: "invalid_request"})
			return
		}

		r.writeJSON(w, http.StatusOK, map[string]any{
			"authorization_url": authURL,
		})

	case subAction == "auth" && subSub == "revoke":
		if req.Method != http.MethodPost {
			r.writeError(w, &httpError{status: http.StatusMethodNotAllowed, code: "method_not_allowed"})
			return
		}

		err := mgr.RevokeAuth(req.Context(), id)
		if err != nil {
			if errors.Is(err, store.ErrMCPServerNotFound) {
				r.writeError(w, &httpError{status: http.StatusNotFound, code: "not_found"})
				return
			}
			r.writeError(w, &httpError{status: http.StatusInternalServerError, code: "internal_error"})
			return
		}

		updated, _ := r.store.GetMCPServer(req.Context(), id)
		if updated != nil {
			r.broadcastMCPServerUpdated(updated)
		}

		r.writeJSON(w, http.StatusOK, map[string]any{
			"server": updated,
		})

	default:
		r.writeError(w, &httpError{status: http.StatusNotFound, code: "not_found"})
	}
}

func (r *Router) handleMCPOAuthCallback(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		r.writeError(w, &httpError{status: http.StatusMethodNotAllowed, code: "method_not_allowed"})
		return
	}

	state := req.URL.Query().Get("state")
	code := req.URL.Query().Get("code")
	errParam := req.URL.Query().Get("error")

	if errParam != "" {
		errDesc := req.URL.Query().Get("error_description")
		renderOAuthCallbackHTML(w, http.StatusBadRequest, false, "", fmt.Sprintf("%s: %s", errParam, errDesc))
		return
	}

	if state == "" || code == "" {
		renderOAuthCallbackHTML(w, http.StatusBadRequest, false, "", "Missing authorization code or state parameter.")
		return
	}

	mgr := r.MCPManager()
	server, err := mgr.CompleteAuth(req.Context(), state, code)
	if err != nil {
		renderOAuthCallbackHTML(w, http.StatusBadRequest, false, "", err.Error())
		return
	}

	r.broadcastMCPServerUpdated(server)
	renderOAuthCallbackHTML(w, http.StatusOK, true, server.ID, "Authorization successful. You may close this window.")
}

func (r *Router) oauthCallbackURL(req *http.Request) string {
	proto := "http"
	if req.TLS != nil || req.Header.Get("X-Forwarded-Proto") == "https" {
		proto = "https"
	}
	host := req.Host
	if host == "" {
		host = fmt.Sprintf("%s:%d", r.host, r.port)
	}
	return fmt.Sprintf("%s://%s/v1/mcp/oauth/callback", proto, host)
}

func parseMCPServerCreateRequest(payload map[string]json.RawMessage) (*store.MCPServer, error) {
	urlStr, err := rawString(payload, "url", true)
	if err != nil {
		return nil, &httpError{status: http.StatusBadRequest, code: "invalid_request"}
	}
	urlStr = strings.TrimSpace(urlStr)
	if urlStr == "" {
		return nil, &httpError{status: http.StatusBadRequest, code: "invalid_request"}
	}

	parsedURL, err := url.Parse(urlStr)
	if err != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") || parsedURL.Host == "" {
		return nil, &httpError{status: http.StatusBadRequest, code: "invalid_request"}
	}

	name, _ := rawString(payload, "name", false)
	name = strings.TrimSpace(name)
	if name == "" {
		name = urlStr
	}

	authType, _ := rawString(payload, "auth_type", false)
	if authType == "" {
		// support both auth_mode and auth_type
		authType, _ = rawString(payload, "auth_mode", false)
	}
	authType = strings.TrimSpace(authType)
	if authType == "" {
		authType = "none"
	}
	if authType != "none" && authType != "bearer" && authType != "oauth" {
		return nil, &httpError{status: http.StatusBadRequest, code: "invalid_request"}
	}

	s := &store.MCPServer{
		Name:     name,
		URL:      urlStr,
		AuthType: authType,
		Status:   "unconfigured",
	}

	if authType == "oauth" {
		s.Status = "needs_auth"
		if clientID, _ := rawString(payload, "client_id", false); clientID != "" {
			s.ClientID = &clientID
		}
		if clientSecret, _ := rawString(payload, "client_secret", false); clientSecret != "" {
			s.ClientSecret = &clientSecret
		}
		if tokenEnd, _ := rawString(payload, "token_endpoint", false); tokenEnd != "" {
			s.TokenEndpoint = &tokenEnd
		}
		if authEnd, _ := rawString(payload, "auth_endpoint", false); authEnd != "" {
			s.AuthEndpoint = &authEnd
		}
		if regEnd, _ := rawString(payload, "registration_endpoint", false); regEnd != "" {
			s.RegistrationEndpoint = &regEnd
		}
		if revEnd, _ := rawString(payload, "revocation_endpoint", false); revEnd != "" {
			s.RevocationEndpoint = &revEnd
		}
		if scopes, _ := rawString(payload, "scopes", false); scopes != "" {
			s.Scopes = &scopes
		}
	}

	return s, nil
}

func applyMCPServerUpdate(server *store.MCPServer, payload map[string]json.RawMessage) error {
	if raw, ok := payload["name"]; ok && len(raw) > 0 && string(raw) != "null" {
		var name string
		if err := json.Unmarshal(raw, &name); err != nil {
			return &httpError{status: http.StatusBadRequest, code: "invalid_request"}
		}
		name = strings.TrimSpace(name)
		if name != "" {
			server.Name = name
		}
	}

	if raw, ok := payload["status"]; ok && len(raw) > 0 && string(raw) != "null" {
		var status string
		if err := json.Unmarshal(raw, &status); err != nil {
			return &httpError{status: http.StatusBadRequest, code: "invalid_request"}
		}
		server.Status = status
	}

	if raw, ok := payload["scopes"]; ok && len(raw) > 0 {
		var scopes string
		if string(raw) == "null" {
			server.Scopes = nil
		} else if err := json.Unmarshal(raw, &scopes); err == nil {
			server.Scopes = &scopes
		}
	}

	return nil
}

func renderOAuthCallbackHTML(w http.ResponseWriter, statusCode int, success bool, serverID, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(statusCode)

	escapedMsg := strings.ReplaceAll(message, "<", "&lt;")
	escapedMsg = strings.ReplaceAll(escapedMsg, ">", "&gt;")

	safeServerID := strings.ReplaceAll(serverID, `\`, `\\`)
	safeServerID = strings.ReplaceAll(safeServerID, `"`, `\"`)

	statusClass := "success"
	statusHeading := "Authorization Complete"
	if !success {
		statusClass = "error"
		statusHeading = "Authorization Failed"
	}

	html := fmt.Sprintf(`<!DOCTYPE html>
<html>
<head>
  <meta charset="utf-8">
  <title>MCP Authorization</title>
  <style>
    body {
      font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif;
      display: flex;
      align-items: center;
      justify-content: center;
      min-height: 100vh;
      margin: 0;
      background-color: #0f172a;
      color: #f8fafc;
    }
    .card {
      background-color: #1e293b;
      padding: 2.5rem;
      border-radius: 12px;
      box-shadow: 0 10px 25px rgba(0, 0, 0, 0.4);
      max-width: 480px;
      text-align: center;
      border: 1px solid #334155;
    }
    h1 {
      margin-top: 0;
      font-size: 1.5rem;
    }
    h1.success { color: #10b981; }
    h1.error { color: #ef4444; }
    p {
      color: #94a3b8;
      line-height: 1.6;
      margin: 1rem 0;
    }
    button {
      background: #3b82f6;
      color: white;
      border: none;
      padding: 0.6rem 1.2rem;
      font-size: 0.95rem;
      border-radius: 6px;
      cursor: pointer;
      margin-top: 1rem;
    }
    button:hover {
      background: #2563eb;
    }
  </style>
</head>
<body>
  <div class="card">
    <h1 class="%s">%s</h1>
    <p>%s</p>
    <button onclick="window.close()">Close Window</button>
  </div>
  <script>
    try {
      if (window.opener) {
        window.opener.postMessage({
          type: 'mcp_oauth_complete',
          success: %t,
          serverId: "%s",
          message: "%s"
        }, '*');
      }
    } catch (e) {
      console.error('Failed to notify opener window:', e);
    }
    setTimeout(function() {
      window.close();
    }, 2500);
  </script>
</body>
</html>`, statusClass, statusHeading, escapedMsg, success, safeServerID, escapedMsg)

	_, _ = w.Write([]byte(html))
}
