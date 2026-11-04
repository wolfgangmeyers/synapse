//go:build embedded_restart_test_only

package main

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"os"
	"sync"
)

// Only the explicitly opted-in test launcher calls this. Configuration lives
// beside its disposable SQLite store; no environment passthrough or public route.
// One selected agent/phase, one request per process; restart needs no authority.
func wireClaudeEffectBarrier(database string, out *managed.ManagerOptions) {
	var mu sync.Mutex
	consumed := false
	out.EmbeddedEffectObserver = func(ctx context.Context, event managed.EmbeddedEffectObservation) error {
		path := database + ".effect-barrier.json"
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 4096 {
			return errors.New("invalid effect barrier config")
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var config struct {
			Enabled  string `json:"enabled"`
			Endpoint string `json:"endpoint"`
			Token    string `json:"token"`
			AgentID  string `json:"agent_id"`
			Phase    string `json:"phase"`
		}
		if err := json.Unmarshal(raw, &config); err != nil {
			return err
		}
		if config.AgentID == "" || (config.Phase != "intent" && config.Phase != "domain" && config.Phase != "checked") {
			return errors.New("invalid effect barrier scope")
		}
		if event.Binding.AgentID != config.AgentID || event.Phase != config.Phase {
			return nil
		}
		mu.Lock()
		if consumed {
			mu.Unlock()
			return nil
		}
		consumed = true
		mu.Unlock()
		barrier, err := newClaudeEffectBarrier(config.Enabled, config.Endpoint, config.Token)
		if err != nil {
			return err
		}
		return barrier.wait(ctx, claudeEffectBarrierReceipt{Phase: event.Phase, AgentID: event.Binding.AgentID, SessionID: event.Binding.SessionID, Epoch: event.Binding.Epoch, Generation: event.Binding.Generation, OwnerToken: event.Binding.OwnerToken, EffectID: event.CanonicalEffectID})
	}
}
