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

	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
)

// The disposable router pauses only after process reaping and before artifact
// deletion. The receipt is the actual durable lifecycle action ID.
func wireClaudeCleanupBarrier(database string, out *managed.ManagerOptions) {
	out.BeforeManagedTerminationArtifactCleanup = func(ctx context.Context, actionID string) error {
		path := database + ".cleanup-barrier.json"
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 4096 {
			return errors.New("invalid cleanup barrier config")
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var cfg struct {
			Enabled  string `json:"enabled"`
			Endpoint string `json:"endpoint"`
			Token    string `json:"token"`
		}
		if err = json.Unmarshal(raw, &cfg); err != nil {
			return err
		}
		barrier, err := newClaudeEffectBarrier(cfg.Enabled, cfg.Endpoint, cfg.Token)
		if err != nil {
			return err
		}
		if actionID == "" {
			return errors.New("missing cleanup action")
		}
		body, err := json.Marshal(map[string]string{"action_id": actionID})
		if err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, barrier.endpoint+"/cleanup", bytes.NewReader(body))
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
			return fmt.Errorf("cleanup barrier not released: HTTP %d", res.StatusCode)
		}
		return nil
	}
}
