//go:build embedded_restart_test_only

package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClaudeEffectBarrierConfigurationFence(t *testing.T) {
	token := strings.Repeat("a", 64)
	for _, endpoint := range []string{"http://localhost:1234", "http://example.com:1234", "https://127.0.0.1:1234", "http://127.0.0.1:1234/path", "http://127.0.0.1:1234?", "http://u@127.0.0.1:1234"} {
		if _, err := newClaudeEffectBarrier("1", endpoint, token); err == nil {
			t.Fatalf("accepted %s", endpoint)
		}
	}
	if _, err := newClaudeEffectBarrier("0", "http://127.0.0.1:1234", token); err == nil {
		t.Fatal("missing opt-in accepted")
	}
}

func TestClaudeEffectBarrierReceiptAndCancellation(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/barrier" || r.Header.Get("Authorization") != "Bearer "+strings.Repeat("a", 64) {
			t.Error("wrong request")
		}
		close(entered)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer func() { close(release); server.Close() }()
	barrier, err := newClaudeEffectBarrier("1", server.URL, strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- barrier.wait(ctx, claudeEffectBarrierReceipt{Phase: "intent", AgentID: "a", SessionID: "s", Epoch: 1, Generation: "g", OwnerToken: 1, EffectID: "canonical"})
	}()
	<-entered
	cancel()
	if err := <-done; err == nil {
		t.Fatal("cancellation released execution")
	}
}
