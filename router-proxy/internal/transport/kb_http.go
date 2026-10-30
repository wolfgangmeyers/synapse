package transport

import (
	"net/http"
	"os"
	"strings"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
)

// handleKBFile serves raw markdown documents from the KB directory (GET /v1/kb/file).
// Defaults to INDEX.md if path is omitted, and uses managed.SanitizeKBPath to prevent
// directory traversal attacks.
func (r *Router) handleKBFile(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		r.writeError(w, &httpError{status: http.StatusMethodNotAllowed, code: "method_not_allowed"})
		return
	}

	rawPath := req.URL.Query().Get("path")
	if strings.TrimSpace(rawPath) == "" {
		rawPath = "INDEX.md"
	}

	kbRoot := managed.ResolveKBRoot()
	filePath, err := managed.SanitizeKBPath(kbRoot, rawPath)
	if err != nil {
		r.writeError(w, &httpError{status: http.StatusBadRequest, code: "invalid_request"})
		return
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			r.writeError(w, &httpError{status: http.StatusNotFound, code: "not_found"})
			return
		}
		r.writeError(w, &httpError{status: http.StatusInternalServerError, code: "internal_error"})
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}
