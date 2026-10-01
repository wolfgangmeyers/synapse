package managed

import (
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sky-valley/pi/ai"
)

type codexTestDoer interface {
	Do(*http.Request) (*http.Response, error)
}
type codexTestTransport struct{ doer codexTestDoer }

func (t codexTestTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.String() != embeddedCodexResponsesURL {
		return nil, errCodexTestDestination
	}
	return t.doer.Do(r)
}

var errCodexTestDestination = &codexTestError{}

type codexTestError struct{}

func (*codexTestError) Error() string { return "Codex fixture destination denied" }

func TestEmbeddedCodexModelEndpointPinned(t *testing.T) {
	base := ai.GetModel("openai-codex", "gpt-6-sol")
	if base == nil || validateEmbeddedCodexModel(base) != nil {
		t.Fatal("catalog Codex model rejected")
	}
	for _, raw := range []string{
		"http://chatgpt.com/backend-api", "https://other.invalid/backend-api",
		"https://chatgpt.com:443/backend-api", "https://user@chatgpt.com/backend-api",
		"https://chatgpt.com/backend-api/", "https://chatgpt.com/backend-api?x=1",
		"https://chatgpt.com/backend-api#fragment", "http://127.0.0.1:1234",
	} {
		model := *base
		model.BaseURL = raw
		if validateEmbeddedCodexModel(&model) == nil {
			t.Fatal("alternate endpoint accepted")
		}
	}
	model := *base
	model.Headers = ai.ProviderHeaders{"Authorization": ai.HeaderValue("override")}
	if validateEmbeddedCodexModel(&model) == nil {
		t.Fatal("auth override accepted")
	}
}

func TestEmbeddedCodexClientRejectsAllRedirects(t *testing.T) {
	for _, tc := range []struct {
		name, target string
		status       int
	}{
		{"cross-host 307", "https://other.invalid/codex/responses", 307},
		{"same-origin 307", embeddedCodexResponsesURL, 307},
		{"same-origin 308", embeddedCodexResponsesURL, 308},
		{"downgrade 307", "http://chatgpt.com/backend-api/codex/responses", 307},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			client := newEmbeddedCodexClient(codexTestRoundTrip(func(r *http.Request) (*http.Response, error) {
				calls.Add(1)
				if r.URL.String() != embeddedCodexResponsesURL {
					t.Fatal("destination reached")
				}
				return &http.Response{StatusCode: tc.status, Header: http.Header{"Location": []string{tc.target}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
			}))
			req, err := http.NewRequest(http.MethodPost, embeddedCodexResponsesURL, nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != tc.status || calls.Load() != 1 {
				t.Fatal("redirect was followed")
			}
			calls.Store(0)
			terminal := embeddedCodexTerminalDoer{client}
			_, err = terminal.Do(req)
			if err == nil || err.Error() != "embedded Codex redirect denied" || calls.Load() != 1 {
				t.Fatal("redirect must fail with a fixed terminal error")
			}
		})
	}
}

type codexTestRoundTrip func(*http.Request) (*http.Response, error)

func (f codexTestRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
