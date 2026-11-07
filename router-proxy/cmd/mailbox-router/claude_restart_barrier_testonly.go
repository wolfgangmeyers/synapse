//go:build embedded_restart_test_only

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sync"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
)

// Hold one restart and report at most two completed gated dispatches for the selected agent.
func wireClaudeRestartBarrier(database string) {
	var mu sync.Mutex
	consumed := map[string]int{}
	send := func(ctx context.Context, agentID, phase, messageID, site string) error {
		if phase != "restart" && phase != "restart-ready" && phase != "reset" && phase != "gated" && phase != "launch" {
			return errors.New("invalid restart barrier phase")
		}
		path := database + ".restart-mail-barrier.json"
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 4096 {
			return errors.New("invalid restart mail barrier config")
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var cfg struct {
			Enabled   string `json:"enabled"`
			Endpoint  string `json:"endpoint"`
			Token     string `json:"token"`
			AgentID   string `json:"agent_id"`
			MessageID string `json:"message_id"`
		}
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return err
		}
		if cfg.AgentID == "" {
			return errors.New("missing restart mail barrier agent")
		}
		if cfg.AgentID != agentID || (phase == "gated" && (cfg.MessageID == "" || cfg.MessageID != messageID)) {
			return nil
		}
		mu.Lock()
		limit := 1
		if phase == "gated" {
			limit = 2
		}
		if consumed[phase] >= limit {
			mu.Unlock()
			return nil
		}
		consumed[phase]++
		mu.Unlock()
		barrier, err := newClaudeEffectBarrier(cfg.Enabled, cfg.Endpoint, cfg.Token)
		if err != nil {
			return err
		}
		body, err := json.Marshal(map[string]string{"agent_id": agentID, "phase": phase, "message_id": messageID, "site": site})
		if err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, barrier.endpoint+"/"+phase, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+barrier.token)
		res, err := barrier.client.Do(req)
		if err != nil {
			return err
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusNoContent {
			return fmt.Errorf("restart mail barrier not released: HTTP %d", res.StatusCode)
		}
		return nil
	}
	managed.SetRestartMailTestBarrier = func(ctx context.Context, agentID, phase string) error { return send(ctx, agentID, phase, "", "") }
	managed.SetRestartMailGatedReceipt = func(ctx context.Context, agentID, messageID, site string) error {
		return send(ctx, agentID, "gated", messageID, site)
	}
}
