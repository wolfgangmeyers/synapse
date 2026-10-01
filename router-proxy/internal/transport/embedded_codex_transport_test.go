package transport

import (
	"errors"
	"net/http"
)

type fixtureCodexDoer interface {
	Do(*http.Request) (*http.Response, error)
}
type fixtureCodexTransport struct{ doer fixtureCodexDoer }

func (t fixtureCodexTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.String() != "https://chatgpt.com/backend-api/codex/responses" {
		return nil, errors.New("Codex fixture destination denied")
	}
	return t.doer.Do(r)
}
