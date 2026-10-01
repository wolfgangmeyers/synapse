package managed

import (
	"errors"
	"net/http"
	"time"

	"github.com/sky-valley/pi/ai"
)

const embeddedCodexBaseURL = "https://chatgpt.com/backend-api"
const embeddedCodexResponsesURL = embeddedCodexBaseURL + "/codex/responses"

// Bound a connected peer's header wait, not a productive response body.
const embeddedCodexResponseHeaderTimeout = 30 * time.Second

// The Codex request owns its client. In particular, neither an SDK-supplied
// doer nor http.DefaultClient may decide where a redirect sends the bearer.
func newEmbeddedCodexClient(transport http.RoundTripper) *http.Client {
	base, ownedTransport := transport.(*http.Transport)
	if transport == nil {
		base, ownedTransport = http.DefaultTransport.(*http.Transport)
		if !ownedTransport {
			// A replaced global doer must not remove the owned header policy.
			base = &http.Transport{}
		}
		ownedTransport = true
	}
	if ownedTransport {
		owned := base.Clone()
		if owned.ResponseHeaderTimeout <= 0 || owned.ResponseHeaderTimeout > embeddedCodexResponseHeaderTimeout {
			owned.ResponseHeaderTimeout = embeddedCodexResponseHeaderTimeout
		}
		transport = owned
	}
	// Custom RoundTrippers are fixture/host boundaries and must themselves
	// honor bounded header waits and request cancellation. Never set a total
	// client timeout: SSE progress can legitimately outlive the header budget.
	return &http.Client{
		Transport:     transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

type embeddedCodexTerminalDoer struct{ client *http.Client }

func (d embeddedCodexTerminalDoer) Do(req *http.Request) (*http.Response, error) {
	attempt, err := privateCodexObserverEnter()
	if err != nil {
		return nil, errors.New("private Codex observer unavailable")
	}
	resp, err := d.client.Do(req)
	resp, observeErr := privateCodexObserverResult(attempt, resp, err)
	if observeErr != nil {
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}
		return nil, errors.New("private Codex observer unavailable")
	}
	if resp != nil && resp.StatusCode >= 300 && resp.StatusCode < 400 {
		resp.Body.Close()
		return nil, errors.New("embedded Codex redirect denied")
	}
	return resp, err
}

func validateEmbeddedCodexModel(model *ai.Model) error {
	if model == nil || model.Provider != "openai-codex" || model.Api != ai.APIOpenAICodexResponses ||
		model.BaseURL != embeddedCodexBaseURL || len(model.Headers) != 0 {
		return errors.New("embedded Codex endpoint denied")
	}
	return nil
}
