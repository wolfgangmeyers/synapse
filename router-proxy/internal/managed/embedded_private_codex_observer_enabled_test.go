//go:build embedded_private_codex_observer

package managed

import (
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func privateCodexTestSetup(t *testing.T) string {
	t.Helper()
	privateCodexState.Lock()
	if privateCodexState.prepared {
		unix.Close(privateCodexState.dirFD)
	}
	privateCodexState.prepared = false
	privateCodexState.attempts = 0
	privateCodexState.Unlock()
	t.Cleanup(func() {
		privateCodexState.Lock()
		if privateCodexState.prepared {
			unix.Close(privateCodexState.dirFD)
		}
		privateCodexState.prepared = false
		privateCodexState.attempts = 0
		privateCodexState.Unlock()
	})
	dir := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(privateCodexObserverDir, dir)
	if err := PreparePrivateCodexObserver(); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestPrivateCodexObserverRejectsUnsafeSetup(t *testing.T) {
	for _, path := range []string{"", "relative", "/", "/tmp/../tmp/x"} {
		if fd, err := privateCodexOpenDir(path); err == nil {
			unix.Close(fd)
			t.Fatal("unsafe path accepted")
		}
	}
	base := t.TempDir()
	leaf := filepath.Join(base, "leaf")
	if err := os.Mkdir(leaf, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(leaf, link); err != nil {
		t.Fatal(err)
	}
	if fd, err := privateCodexOpenDir(link); err == nil {
		unix.Close(fd)
		t.Fatal("symlink accepted")
	}
	if err := os.Chmod(leaf, 0755); err != nil {
		t.Fatal(err)
	}
	if fd, err := privateCodexOpenDir(leaf); err == nil {
		unix.Close(fd)
		t.Fatal("unsafe permissions accepted")
	}
}

func TestPrivateCodexObserverFixedRecordsAndBodyReplay(t *testing.T) {
	dir := privateCodexTestSetup(t)
	secret := "secret-token-email@host.invalid-https://host.invalid/path"
	body := `{"error":{"code":"invalid_api_key","message":"` + secret + `"}}`
	calls := 0
	client := newEmbeddedCodexClient(codexTestRoundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.String() != embeddedCodexResponsesURL || r.Header.Get("Authorization") != "Bearer "+secret {
			t.Fatal("request fixture changed")
		}
		return &http.Response{StatusCode: 401, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	}))
	req, err := http.NewRequest(http.MethodPost, embeddedCodexResponsesURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	resp, err := (embeddedCodexTerminalDoer{client}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || string(got) != body {
		t.Fatal("SDK body changed")
	}
	// Even if the SDK classifies this as a typed pre-output 401, its
	// attempted retry cannot cross the terminal Do boundary.
	if _, err := (embeddedCodexTerminalDoer{client}).Do(req); err == nil {
		t.Fatal("401 retry accepted")
	}
	if calls != 1 {
		t.Fatal("second request reached transport")
	}
	for name, want := range map[string]string{
		"start.json":             "\"category\":\"no_http_do\"",
		"attempt-1-entered.json": "\"category\":\"http_do_entered\"",
		"attempt-1-result.json":  "\"code_category\":\"invalid_api_key\"",
	} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || !strings.Contains(string(data), want) || strings.Contains(string(data), secret) || strings.Contains(string(data), embeddedCodexResponsesURL) {
			t.Fatalf("invalid fixed output %s", name)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "attempt-2-entered.json")); !os.IsNotExist(err) {
		t.Fatal("rejected retry wrote a second record")
	}
}

func TestPrivateCodexObserverRedirectAndTransportError(t *testing.T) {
	dir := privateCodexTestSetup(t)
	secret := "secret-redirect-target"
	client := newEmbeddedCodexClient(codexTestRoundTrip(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 307, Header: http.Header{"Location": []string{"https://" + secret}, "Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(secret)), Request: r}, nil
	}))
	req, _ := http.NewRequest(http.MethodPost, embeddedCodexResponsesURL, nil)
	if _, err := (embeddedCodexTerminalDoer{client}).Do(req); err == nil {
		t.Fatal("redirect accepted")
	}
	secondCalls := 0
	client = newEmbeddedCodexClient(codexTestRoundTrip(func(*http.Request) (*http.Response, error) {
		secondCalls++
		return nil, errors.New(secret)
	}))
	otherTurn, _ := http.NewRequest(http.MethodPost, embeddedCodexResponsesURL, nil)
	if _, err := (embeddedCodexTerminalDoer{client}).Do(otherTurn); err == nil || secondCalls != 0 {
		t.Fatal("independent second turn reached transport")
	}
	data, err := os.ReadFile(filepath.Join(dir, "attempt-1-result.json"))
	if err != nil || strings.Contains(string(data), secret) {
		t.Fatal("private material written")
	}
}

func TestPrivateCodexObserverPartialSSEUntouched(t *testing.T) {
	dir := privateCodexTestSetup(t)
	secret := "secret-token-email@host.invalid-https://host.invalid/path"
	body := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"" + secret + "\"}\n\ndata: {\"type\":\"response.failed\""
	calls := 0
	client := newEmbeddedCodexClient(codexTestRoundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	}))
	req, _ := http.NewRequest(http.MethodPost, embeddedCodexResponsesURL, nil)
	resp, err := (embeddedCodexTerminalDoer{client}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || string(got) != body || calls != 1 {
		t.Fatal("partial SSE body changed")
	}
	for _, name := range []string{"start.json", "attempt-1-entered.json", "attempt-1-result.json"} {
		out, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || strings.Contains(string(out), secret) || strings.Contains(string(out), embeddedCodexResponsesURL) || strings.Contains(string(out), "response.failed") {
			t.Fatalf("partial SSE leaked into %s", name)
		}
		if name == "attempt-1-result.json" && !strings.Contains(string(out), `"category":"http_2xx","code_category":"no_code"`) {
			t.Fatal("partial SSE classified from body")
		}
	}
}

func TestPrivateCodexObserverOversizeBody(t *testing.T) {
	dir := privateCodexTestSetup(t)
	secret := "secret-oversize-raw-body"
	large := `{"error":{"code":"invalid_api_key","message":"` + strings.Repeat(secret, 300) + `"}}`
	calls := 0
	client := newEmbeddedCodexClient(codexTestRoundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 429, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(large)), Request: r}, nil
	}))
	req, _ := http.NewRequest(http.MethodPost, embeddedCodexResponsesURL, nil)
	resp, err := (embeddedCodexTerminalDoer{client}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || string(got) != large || calls != 1 {
		t.Fatal("response replay changed")
	}
	out, err := os.ReadFile(filepath.Join(dir, "attempt-1-result.json"))
	if err != nil || strings.Contains(string(out), secret) || !strings.Contains(string(out), `"code_category":"no_code"`) {
		t.Fatal("unsafe bounded classification")
	}
}

func TestPrivateCodexObserverBoundedPrefixReplay(t *testing.T) {
	dir := privateCodexTestSetup(t)
	secret := "secret-token-email@host.invalid-https://host.invalid/path"
	prefix := strings.Repeat(" ", 4096)
	tail := secret + `{"error":{"code":"invalid_api_key"}}`
	client := newEmbeddedCodexClient(codexTestRoundTrip(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 429, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(prefix + tail)), Request: r}, nil
	}))
	req, _ := http.NewRequest(http.MethodPost, embeddedCodexResponsesURL, nil)
	resp, err := (embeddedCodexTerminalDoer{client}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	first := make([]byte, 4096)
	_, firstErr := io.ReadFull(resp.Body, first)
	rest, restErr := io.ReadAll(resp.Body)
	resp.Body.Close()
	if firstErr != nil || restErr != nil || string(first) != prefix || string(rest) != tail {
		t.Fatal("4096-byte prefix or remaining body changed")
	}
	out, err := os.ReadFile(filepath.Join(dir, "attempt-1-result.json"))
	if err != nil || !strings.Contains(string(out), `"code_category":"no_code"`) || strings.Contains(string(out), secret) || strings.Contains(string(out), tail) {
		t.Fatal("bounded prefix leaked or misclassified")
	}
}

func TestPrivateCodexCodeExoticShapes(t *testing.T) {
	secret := "secret-token-email@host.invalid-https://host.invalid/path"
	for _, tc := range []struct{ name, body, want string }{
		{"top-level-array", `[{"error":{"code":"invalid_api_key"}}]`, "unknown"},
		{"code-object", `{"error":{"code":{"secret":"` + secret + `"}}}`, "no_code"},
		{"code-array", `{"error":{"code":["invalid_api_key","` + secret + `"]}}`, "no_code"},
		{"excessive-nesting", `{"error":{"code":"invalid_api_key","nested":{"a":{"b":{"c":"` + secret + `"}}}}}`, "unknown"},
		{"duplicate-code-first-unrecognized", `{"error":{"code":"` + secret + `","code":"invalid_api_key"}}`, "unknown"},
		{"duplicate-code-first-recognized", `{"error":{"code":"invalid_api_key","code":"` + secret + `"}}`, "unknown"},
		{"malformed-json", `{"error":{"code":"invalid_api_key"`, "unknown"},
		{"invalid-type", `{"error":{"code":false}}`, "no_code"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := privateCodexTestSetup(t)
			client := newEmbeddedCodexClient(codexTestRoundTrip(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 400, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(tc.body)), Request: r}, nil
			}))
			req, _ := http.NewRequest(http.MethodPost, embeddedCodexResponsesURL, nil)
			resp, err := (embeddedCodexTerminalDoer{client}).Do(req)
			if err != nil {
				t.Fatal(err)
			}
			got, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil || string(got) != tc.body {
				t.Fatal("exotic response body changed")
			}
			for _, name := range []string{"start.json", "attempt-1-entered.json", "attempt-1-result.json"} {
				out, err := os.ReadFile(filepath.Join(dir, name))
				if err != nil || strings.Contains(string(out), secret) || strings.Contains(string(out), embeddedCodexResponsesURL) || strings.Contains(string(out), tc.body) {
					t.Fatalf("exotic response leaked into %s", name)
				}
				if name == "attempt-1-result.json" && !strings.Contains(string(out), `"code_category":"`+tc.want+`"`) {
					t.Fatalf("wrong fixed category for %s", tc.name)
				}
			}
		})
	}
}

func TestPrivateCodexCodeHostileShapes(t *testing.T) {
	for _, body := range []string{
		`{"error":{"code":"secret@email.invalid"}}`, `{"error":{"code":5}}`,
		`{"error":{"code":"invalid_api_key"`,
		`{"error":{"code":"invalid_api_key","nested":{"a":{"b":{"c":{"d":1}}}}}}`,
		`{"error":{"code":"invalid_api_key"},"extra":1}`,
		`{"error":{"code":"invalid_api_key"},` + strings.Repeat(" ", 4097),
	} {
		if got := privateCodexCode([]byte(body)); got == "invalid_api_key" {
			t.Fatal("hostile shape accepted")
		}
	}
}
