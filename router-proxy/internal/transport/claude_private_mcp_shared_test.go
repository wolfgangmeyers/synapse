package transport

import (
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClaudeMCPUnavailableOnSharedRouterAndProxy(t *testing.T) {
	r := &Router{}
	shared := httptest.NewServer(http.HandlerFunc(r.handleHTTP))
	defer shared.Close()
	upstream, err := url.Parse(shared.URL)
	require.NoError(t, err)
	proxy := httptest.NewServer(httputil.NewSingleHostReverseProxy(upstream))
	defer proxy.Close()
	for _, base := range []string{shared.URL, proxy.URL} {
		for _, method := range []string{http.MethodPost, http.MethodOptions} {
			req, err := http.NewRequest(method, base+"/v1/mcp", nil)
			require.NoError(t, err)
			req.Header.Set("Origin", "https://arbitrary.example")
			req.Header.Set("X-Correlation-Id", "test")
			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			resp.Body.Close()
			require.Equal(t, http.StatusNotFound, resp.StatusCode)
		}
	}
}
