//go:build embedded_restart_test_only

package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai"
)

// This disposable, build-tagged seam cannot use the production provider or issuer.
func configureEmbeddedRestartFake(options config, out *managed.ManagerOptions) error {
	if os.Getenv("EMBEDDED_RESTART_FAKE_ENABLE") != "1" {
		return errors.New("embedded restart fake requires explicit opt-in")
	}
	endpoint := os.Getenv("EMBEDDED_RESTART_FAKE_ENDPOINT")
	target, err := url.Parse(endpoint)
	if err != nil || target == nil || target.Scheme != "http" || target.Opaque != "" || target.User != nil || target.RawQuery != "" || target.ForceQuery || target.Fragment != "" || target.RawFragment != "" || target.Path != "" || target.RawPath != "" {
		return errors.New("embedded restart fake endpoint must be a bare literal loopback HTTP URL")
	}
	host := target.Hostname()
	port := target.Port()
	n, e := strconv.Atoi(port)
	if e != nil || n < 1 || n > 65535 || (host != "127.0.0.1" && host != "::1") || endpoint != "http://"+net.JoinHostPort(host, port) {
		return errors.New("embedded restart fake endpoint must use a literal loopback host and port")
	}
	if options.host != "127.0.0.1" && options.host != "::1" {
		return errors.New("embedded restart fake requires a loopback router listener")
	}
	if !options.managed.Enabled {
		return errors.New("embedded restart fake requires managed mode")
	}
	if out == nil {
		return errors.New("embedded restart fake manager options missing")
	}
	out.EmbeddedModelResolver = func(row store.ManagedAgentRecord) *ai.Model {
		if row.SelectedModel.Provider != "openai-codex" || (row.SelectedModel.ID != "gpt-6-sol" && row.SelectedModel.ID != "gpt-6.1-sol") {
			return nil
		}
		original := ai.GetModel("openai-codex", row.SelectedModel.ID)
		if original == nil {
			return nil
		}
		copy := *original
		return &copy
	}
	address := net.JoinHostPort(host, port)
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		if network != "tcp" || addr != address {
			return nil, errors.New("embedded restart fake dial denied")
		}
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", address)
	}}
	out.CodexHTTPTransport = embeddedRestartTransport{endpoint: endpoint, transport: transport}
	out.CodexRefresh = func(context.Context, store.CodexCredential) (store.CodexCredential, error) {
		return store.CodexCredential{}, errors.New("embedded restart fake: credential refresh disabled")
	}
	return nil
}

type embeddedRestartTransport struct {
	endpoint  string
	transport http.RoundTripper
}

func (d embeddedRestartTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req == nil || req.URL == nil || req.Method != http.MethodPost || req.URL.String() != "https://chatgpt.com/backend-api/codex/responses" || req.URL.User != nil || req.URL.RawQuery != "" || req.URL.Fragment != "" || req.Host != "" && req.Host != req.URL.Host {
		return nil, fmt.Errorf("embedded restart fake request denied")
	}
	copy := req.Clone(req.Context())
	copy.URL, _ = url.Parse(d.endpoint + "/codex/responses")
	copy.Host = copy.URL.Host
	return d.transport.RoundTrip(copy)
}
