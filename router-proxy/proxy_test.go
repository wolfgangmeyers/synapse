package main

import (
	"bytes"
	"errors"
	"flag"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func TestParseConfigRequiredFlags(t *testing.T) {
	t.Parallel()

	_, err := parseConfig([]string{"--upstream-host", "127.0.0.1", "--upstream-port", "8765"})
	if err == nil || !strings.Contains(err.Error(), "--listen-port") {
		t.Fatalf("expected listen-port required error, got %v", err)
	}

	_, err = parseConfig([]string{"--upstream-port", "8765", "--listen-port", "9876"})
	if err == nil || !strings.Contains(err.Error(), "--upstream-host") {
		t.Fatalf("expected upstream-host required error, got %v", err)
	}
}

func TestParseConfigDefaults(t *testing.T) {
	t.Parallel()

	cfg, err := parseConfig([]string{
		"--upstream-host", "127.0.0.1",
		"--upstream-port", "8765",
		"--listen-port", "9876",
	})
	if err != nil {
		t.Fatalf("parseConfig() error = %v", err)
	}
	if cfg.listenHost != defaultListenHost {
		t.Fatalf("listenHost = %q, want %q", cfg.listenHost, defaultListenHost)
	}
	if cfg.upstreamHost != "127.0.0.1" || cfg.upstreamPort != 8765 || cfg.listenPort != 9876 {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestParseConfigCustomListenHost(t *testing.T) {
	t.Parallel()

	cfg, err := parseConfig([]string{
		"--upstream-host", "10.0.0.5",
		"--upstream-port", "8765",
		"--listen-port", "9876",
		"--listen-host", "127.0.0.1",
	})
	if err != nil {
		t.Fatalf("parseConfig() error = %v", err)
	}
	if cfg.listenHost != "127.0.0.1" {
		t.Fatalf("listenHost = %q, want 127.0.0.1", cfg.listenHost)
	}
}

func TestParseConfigRejectsUnknownFlag(t *testing.T) {
	t.Parallel()

	_, err := parseConfig([]string{
		"--upstream-host", "127.0.0.1",
		"--upstream-port", "8765",
		"--listen-port", "9876",
		"--scheme", "https",
	})
	if err == nil || !strings.Contains(err.Error(), "flag provided but not defined") {
		t.Fatalf("expected unknown flag error, got %v", err)
	}
}

func TestProxyForwardsHTTPRequest(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/health" {
			t.Fatalf("unexpected upstream request: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte(`{"status":"ok","version":"v1"}`))
	}))
	defer upstream.Close()

	host, port := upstreamHostPort(t, upstream.URL)
	proxy, err := newRouterProxy(host, port)
	if err != nil {
		t.Fatalf("newRouterProxy() error = %v", err)
	}

	proxyServer := httptest.NewServer(proxy)
	defer proxyServer.Close()

	resp, err := http.Get(proxyServer.URL + "/v1/health")
	if err != nil {
		t.Fatalf("GET proxy /v1/health: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200, body = %q", resp.StatusCode, body)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !strings.Contains(string(body), `"status":"ok"`) {
		t.Fatalf("unexpected body: %s", body)
	}
}

func TestProxyRejectsOversizedBody(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("upstream should not receive oversized request")
	}))
	defer upstream.Close()

	host, port := upstreamHostPort(t, upstream.URL)
	proxy, err := newRouterProxy(host, port)
	if err != nil {
		t.Fatalf("newRouterProxy() error = %v", err)
	}

	proxyServer := httptest.NewServer(proxy)
	defer proxyServer.Close()

	body := bytes.Repeat([]byte("a"), maxRequestBytes+1)
	resp, err := http.Post(proxyServer.URL+"/v1/messages", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST proxy /v1/messages: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 413, body = %q", resp.StatusCode, respBody)
	}
}

func TestProxyDoesNotInjectOrigin(t *testing.T) {
	t.Parallel()

	var seenOrigin string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenOrigin = r.Header.Get("Origin")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	host, port := upstreamHostPort(t, upstream.URL)
	proxy, err := newRouterProxy(host, port)
	if err != nil {
		t.Fatalf("newRouterProxy() error = %v", err)
	}

	proxyServer := httptest.NewServer(proxy)
	defer proxyServer.Close()

	resp, err := http.Get(proxyServer.URL + "/v1/agents")
	if err != nil {
		t.Fatalf("GET proxy /v1/agents: %v", err)
	}
	resp.Body.Close()

	if seenOrigin != "" {
		t.Fatalf("proxy injected Origin %q", seenOrigin)
	}
}

func TestDirectorRewritesToUpstream(t *testing.T) {
	t.Parallel()

	var seenHost string
	var seenPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenHost = r.Host
		seenPath = r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	host, port := upstreamHostPort(t, upstream.URL)
	proxy, err := newRouterProxy(host, port)
	if err != nil {
		t.Fatalf("newRouterProxy() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, "http://proxy.example/v1/reminders/rem-1", nil)
	proxy.proxy.Director(req)

	if seenPath != "" {
		t.Fatalf("Director should not call upstream handler synchronously")
	}
	if req.URL.Host != net.JoinHostPort(host, strconv.Itoa(port)) {
		t.Fatalf("URL.Host = %q, want %q", req.URL.Host, net.JoinHostPort(host, strconv.Itoa(port)))
	}
	if req.URL.Path != "/v1/reminders/rem-1" {
		t.Fatalf("URL.Path = %q, want /v1/reminders/rem-1", req.URL.Path)
	}
	if req.Host != net.JoinHostPort(host, strconv.Itoa(port)) {
		t.Fatalf("Host = %q, want %q", req.Host, net.JoinHostPort(host, strconv.Itoa(port)))
	}
	_ = seenHost
}

func TestHelpFlag(t *testing.T) {
	t.Parallel()

	fs := flag.NewFlagSet("router-proxy", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if err := fs.Parse([]string{"--help"}); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("expected flag.ErrHelp, got %v", err)
	}
}

func upstreamHostPort(t *testing.T, rawURL string) (string, int) {
	t.Helper()

	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse upstream URL: %v", err)
	}
	host, portString, err := net.SplitHostPort(parsed.Host)
	if err != nil {
		t.Fatalf("split upstream host/port: %v", err)
	}
	port, err := strconv.Atoi(portString)
	if err != nil {
		t.Fatalf("parse upstream port: %v", err)
	}
	return host, port
}
