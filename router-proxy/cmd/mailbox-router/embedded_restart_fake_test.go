//go:build embedded_restart_test_only

package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai"
)

func TestEmbeddedRestartStartupRejectsBeforeDatabase(t *testing.T) {
	if os.Getenv("EMBEDDED_RESTART_FAKE_TEST_CHILD") == "1" {
		os.Args = []string{"mailbox-router", "--database", os.Getenv("EMBEDDED_RESTART_FAKE_DB"), "--host", os.Getenv("EMBEDDED_RESTART_FAKE_HOST")}
		main()
		return
	}
	for _, tc := range []struct{ name, endpoint, host string }{
		{"missing", "", "127.0.0.1"},
		{"no opt-in", "http://127.0.0.1:9999", "127.0.0.1"}, {"remote", "http://example.com:9999", "127.0.0.1"},
		{"wildcard listener", "http://127.0.0.1:9999", "0.0.0.0"},
		{"missing port", "http://127.0.0.1", "127.0.0.1"},
		{"userinfo", "http://u@127.0.0.1:9999", "127.0.0.1"},
		{"path", "http://127.0.0.1:9999/v1", "127.0.0.1"},
		{"query", "http://127.0.0.1:9999?x=1", "127.0.0.1"},
		{"fragment", "http://127.0.0.1:9999#x", "127.0.0.1"},
		{"dns alias", "http://localhost:9999", "127.0.0.1"},
		{"wrong scheme", "https://127.0.0.1:9999", "127.0.0.1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			db := filepath.Join(root, "router.sqlite")
			command := exec.Command(os.Args[0], "-test.run=^TestEmbeddedRestartStartupRejectsBeforeDatabase$", "--", "--database", db, "--host", tc.host)
			enable := "1"
			if tc.name == "no opt-in" {
				enable = "0"
			}
			command.Env = append(os.Environ(), "EMBEDDED_RESTART_FAKE_TEST_CHILD=1", "EMBEDDED_RESTART_FAKE_ENABLE="+enable, "EMBEDDED_RESTART_FAKE_ENDPOINT="+tc.endpoint, "EMBEDDED_RESTART_FAKE_DB="+db, "EMBEDDED_RESTART_FAKE_HOST="+tc.host)
			output, err := command.CombinedOutput()
			if err == nil || !strings.Contains(string(output), "embedded restart fake") {
				t.Fatalf("expected startup denial: %v %s", err, output)
			}
			if _, err := os.Stat(db); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("database created: %v", err)
			}
			if _, err := os.Stat(db + ".attachments"); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("attachments created: %v", err)
			}
		})
	}
}

func TestEmbeddedRestartFakeIPv6LiteralAccepted(t *testing.T) {
	t.Setenv("EMBEDDED_RESTART_FAKE_ENABLE", "1")
	t.Setenv("EMBEDDED_RESTART_FAKE_ENDPOINT", "http://[::1]:9191")
	opts := managed.ManagerOptions{}
	if err := configureEmbeddedRestartFake(config{host: "::1", managed: managed.Config{Enabled: true}}, &opts); err != nil {
		t.Fatal(err)
	}
	if opts.CodexHTTPTransport == nil {
		t.Fatal("IPv6 fake not installed")
	}
}

func TestEmbeddedRestartFakeDoerFence(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path == "/codex/responses" {
			w.WriteHeader(http.StatusUnauthorized)
		} else {
			http.Redirect(w, r, "http://example.com", http.StatusFound)
		}
	}))
	defer server.Close()
	t.Setenv("EMBEDDED_RESTART_FAKE_ENABLE", "1")
	t.Setenv("EMBEDDED_RESTART_FAKE_ENDPOINT", server.URL)
	opts := managed.ManagerOptions{}
	if err := configureEmbeddedRestartFake(config{host: "127.0.0.1", managed: managed.Config{Enabled: true}}, &opts); err != nil {
		t.Fatal(err)
	}
	real := ai.GetModel("openai-codex", "gpt-6-sol")
	fake := opts.EmbeddedModelResolver(store.ManagedAgentRecord{SelectedModel: store.ManagedModel{Provider: "openai-codex", ID: "gpt-6-sol"}})
	if real == nil || fake == nil || fake == real || fake.BaseURL != real.BaseURL || real.BaseURL == server.URL || fake.Api != real.Api || fake.ID != real.ID || fake.Provider != real.Provider {
		t.Fatal("model must retain the pinned HTTPS endpoint")
	}
	if !reflect.DeepEqual(*fake, *real) {
		t.Fatal("fake model differs beyond BaseURL")
	}
	if opts.EmbeddedModelResolver(store.ManagedAgentRecord{SelectedModel: store.ManagedModel{Provider: "openai-codex", ID: "unknown"}}) != nil || opts.EmbeddedModelResolver(store.ManagedAgentRecord{SelectedModel: store.ManagedModel{Provider: "openai", ID: "gpt-6-sol"}}) != nil {
		t.Fatal("unexpected model accepted")
	}
	if opts.CodexHTTPTransport == nil || opts.CodexRefresh == nil {
		t.Fatal("fake wiring incomplete")
	}
	allowed := "https://chatgpt.com/backend-api/codex/responses"
	client := &http.Client{Transport: opts.CodexHTTPTransport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, _ := http.NewRequest(http.MethodPost, allowed, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 || hits.Load() != 1 {
		t.Fatalf("allowed request: %d, hits %d", resp.StatusCode, hits.Load())
	}
	for _, raw := range []string{server.URL + "/other", allowed + "?x=1", strings.Replace(allowed, "chatgpt.com", "localhost", 1), strings.Replace(allowed, "https://", "http://", 1), "http://127.0.0.1:1/codex/responses", "https://example.com/codex/responses"} {
		req, e := http.NewRequest(http.MethodPost, raw, nil)
		if e != nil {
			t.Fatal(e)
		}
		if response, e := client.Do(req); e == nil {
			response.Body.Close()
			t.Errorf("accepted %s", raw)
		}
	}
	if hits.Load() != 1 {
		t.Fatalf("rejected requests reached fake: %d", hits.Load())
	}
	_, err = opts.CodexRefresh(context.Background(), store.CodexCredential{ExpiresAt: time.Now().Add(-time.Hour)})
	if err == nil {
		t.Fatal("refresh must fail without issuer")
	}
	// Redirects must not follow, even to the same fake server or a nonlocal destination.
	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Redirect(w, r, "http://example.com/other", http.StatusFound)
	})
	req, _ = http.NewRequest(http.MethodPost, allowed, nil)
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound || hits.Load() != 2 {
		t.Fatalf("redirect followed: status %d hits %d", resp.StatusCode, hits.Load())
	}
}

// The seam resolves the real startup registry, never an injected Terra model.
func TestEmbeddedRestartTerraProductionRegistry(t *testing.T) {
	t.Setenv("EMBEDDED_RESTART_FAKE_ENABLE", "1")
	t.Setenv("EMBEDDED_RESTART_FAKE_ENDPOINT", "http://127.0.0.1:9191")
	if err := managed.LoadCodexModelsStore(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	opts := managed.ManagerOptions{}
	if err := configureEmbeddedRestartFake(config{host: "127.0.0.1", managed: managed.Config{Enabled: true}}, &opts); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"gpt-6-sol", "gpt-6.1-terra"} {
		original := ai.GetModel("openai-codex", id)
		resolved := opts.EmbeddedModelResolver(store.ManagedAgentRecord{SelectedModel: store.ManagedModel{Provider: "openai-codex", ID: id}})
		if original == nil || resolved == nil || original == resolved || !reflect.DeepEqual(*original, *resolved) {
			t.Fatalf("%s must resolve an unchanged copy of the production registry model", id)
		}
		if id == "gpt-6.1-terra" && (resolved.Api != ai.APIOpenAICodexResponses || resolved.BaseURL != "https://chatgpt.com/backend-api" || resolved.ContextWindow != 1050000 || resolved.MaxTokens != 128000 || !reflect.DeepEqual(resolved.Input, []string{"text", "image"}) || !reflect.DeepEqual(resolved.Cost, ai.ModelCost{Input: 1, CacheRead: 0.05, Output: 5}) || len(resolved.Headers) != 0 || !ai.IsModelType(resolved, ai.ModelTypeChat)) {
			t.Fatalf("Terra metadata mismatch: %#v", resolved)
		}
	}
	if opts.EmbeddedModelResolver(store.ManagedAgentRecord{SelectedModel: store.ManagedModel{Provider: "openai", ID: "gpt-6.1-terra"}}) != nil {
		t.Fatal("API-key provider must not resolve through Codex seam")
	}
}
