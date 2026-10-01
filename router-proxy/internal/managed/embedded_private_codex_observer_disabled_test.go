//go:build !embedded_private_codex_observer

package managed

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestOrdinaryCodexClientHasNoPrivateObserver(t *testing.T) {
	t.Setenv("MECHA_EMBEDDED_PRIVATE_CODEX_OBSERVER_DIR", "/missing/private/observer")
	if err := PreparePrivateCodexObserver(); err != nil {
		t.Fatal("ordinary build read observer configuration")
	}
	body := `{"error":{"code":"sensitive"}}`
	client := newEmbeddedCodexClient(codexTestRoundTrip(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 401, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	}))
	req, _ := http.NewRequest(http.MethodPost, embeddedCodexResponsesURL, nil)
	resp, err := (embeddedCodexTerminalDoer{client}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || string(got) != body {
		t.Fatal("ordinary response changed")
	}
}
