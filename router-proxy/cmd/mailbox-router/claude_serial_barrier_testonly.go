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

// A one-shot, agent-scoped hold while prompt admission owns the serial lane.
func wireClaudeSerialBarrier(database string) {
	var mu sync.Mutex
	consumed := false
	managed.SetSerialLaneTestBarrier = func(ctx context.Context, agentID string) error {
		path := database + ".serial-barrier.json"
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 4096 {
			return errors.New("invalid serial barrier config")
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var cfg struct {
			Enabled  string `json:"enabled"`
			Endpoint string `json:"endpoint"`
			Token    string `json:"token"`
			AgentID  string `json:"agent_id"`
		}
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return err
		}
		if cfg.AgentID == "" {
			return errors.New("missing serial barrier agent")
		}
		if cfg.AgentID != agentID {
			return nil
		}
		mu.Lock()
		if consumed {
			mu.Unlock()
			return nil
		}
		consumed = true
		mu.Unlock()
		barrier, err := newClaudeEffectBarrier(cfg.Enabled, cfg.Endpoint, cfg.Token)
		if err != nil {
			return err
		}
		body, err := json.Marshal(map[string]string{"agent_id": agentID})
		if err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, barrier.endpoint+"/serial", bytes.NewReader(body))
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
			return fmt.Errorf("serial barrier not released: HTTP %d", res.StatusCode)
		}
		return nil
	}
}
