package transport

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

func agentProgressRoute(path string) (agentID, action string, ok bool) {
	prefix := ""
	if strings.HasPrefix(path, "/v1/agents/") {
		prefix = "/v1/agents/"
	} else if strings.HasPrefix(path, "/v1/managed-agents/") {
		prefix = "/v1/managed-agents/"
	} else {
		return "", "", false
	}

	rest := strings.TrimPrefix(path, prefix)
	parts := strings.Split(rest, "/")
	if len(parts) != 3 || parts[1] != "progress" {
		return "", "", false
	}

	var err error
	agentID, err = url.PathUnescape(parts[0])
	if err != nil || agentID == "" {
		return "", "", false
	}

	switch parts[2] {
	case "history":
		return agentID, "history", true
	case "chart.png":
		return agentID, "chart.png", true
	default:
		return "", "", false
	}
}

func (r *Router) handleAgentProgressHTTP(w http.ResponseWriter, req *http.Request, agentID, action string) {
	if req.Method != http.MethodGet {
		r.writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	if r.store == nil {
		r.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "store unavailable"})
		return
	}

	_, err := r.store.ManagedAgent(req.Context(), agentID)
	if err != nil {
		var notFound *store.ManagedAgentNotFoundError
		if errors.As(err, &notFound) || errors.Is(err, sql.ErrNoRows) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "agent not found"})
			return
		}
		r.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load agent"})
		return
	}

	if action == "history" {
		entries, err := r.store.GetAgentProgressHistory(req.Context(), agentID)
		if err != nil {
			r.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to get progress history"})
			return
		}
		type historyItem struct {
			Percent   int    `json:"percent"`
			Note      string `json:"note"`
			Timestamp string `json:"timestamp"`
		}
		out := make([]historyItem, 0, len(entries))
		for _, e := range entries {
			out = append(out, historyItem{Percent: e.Percent, Note: e.Note, Timestamp: e.Timestamp})
		}
		r.writeJSON(w, http.StatusOK, out)
		return
	}

	if action == "chart.png" {
		entries, err := r.store.GetAgentProgressHistory(req.Context(), agentID)
		if err != nil {
			r.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to get progress history"})
			return
		}
		width := 800
		height := 400
		if wStr := req.URL.Query().Get("width"); wStr != "" {
			if val, err := strconv.Atoi(wStr); err == nil && val > 0 {
				width = val
			}
		}
		if hStr := req.URL.Query().Get("height"); hStr != "" {
			if val, err := strconv.Atoi(hStr); err == nil && val > 0 {
				height = val
			}
		}
		pngBytes, err := managed.RenderProgressChartPNG(entries, width, height)
		if err != nil {
			r.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to render chart"})
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Content-Length", strconv.Itoa(len(pngBytes)))
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(pngBytes)
		return
	}

	r.writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
}
