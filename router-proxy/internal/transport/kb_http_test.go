package transport_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/testsupport/storetest"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/transport"
	"github.com/stretchr/testify/require"
)

func TestKBFileHTTP(t *testing.T) {
	s, _ := storetest.OpenTestStore(t)
	r, err := transport.New(transport.Options{
		Store: s,
	})
	require.NoError(t, err)

	tempKB := t.TempDir()
	t.Setenv("MECHA_KB_DIR", tempKB)

	indexContent := "# Knowledge Base Index\n\n## Ungrouped\n- [Test](test.md)\n"
	require.NoError(t, os.WriteFile(filepath.Join(tempKB, "INDEX.md"), []byte(indexContent), 0644))

	testContent := "# Test Topic\n\nSome test details."
	require.NoError(t, os.WriteFile(filepath.Join(tempKB, "test.md"), []byte(testContent), 0644))

	// 1. GET /v1/kb/file without query param -> defaults to INDEX.md
	req := httptest.NewRequest(http.MethodGet, "/v1/kb/file", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "text/plain; charset=utf-8", w.Header().Get("Content-Type"))
	require.Equal(t, indexContent, w.Body.String())

	// 2. GET /v1/kb/file?path=test.md
	req = httptest.NewRequest(http.MethodGet, "/v1/kb/file?path=test.md", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "text/plain; charset=utf-8", w.Header().Get("Content-Type"))
	require.Equal(t, testContent, w.Body.String())

	// 3. GET /v1/kb/file?path=nonexistent.md -> 404
	req = httptest.NewRequest(http.MethodGet, "/v1/kb/file?path=nonexistent.md", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusNotFound, w.Code)

	// 4. GET /v1/kb/file?path=../traversal.md -> 400
	req = httptest.NewRequest(http.MethodGet, "/v1/kb/file?path=../traversal.md", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)

	// 5. POST /v1/kb/file -> 405 Method Not Allowed
	req = httptest.NewRequest(http.MethodPost, "/v1/kb/file", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusMethodNotAllowed, w.Code)
}
