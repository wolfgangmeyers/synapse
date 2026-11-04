package managed

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"os"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"

	"github.com/google/uuid"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

const backgroundMaxAge = 24 * time.Hour

func backgroundExpired(agent *store.ManagedAgentRecord, now time.Time) bool {
	if agent == nil || !agent.IsBackground {
		return false
	}
	created, err := time.Parse(time.RFC3339Nano, agent.CreatedAt)
	return err == nil && !created.After(now.Add(-backgroundMaxAge))
}

// TerminateExpiredBackgroundWorkers uses the same lifecycle path as a worker DELETE.
// The router calls this on startup and once per minute thereafter.
func (m *Manager) TerminateExpiredBackgroundWorkers(ctx context.Context) {
	agents, err := m.store.ManagedAgents(ctx)
	if err != nil {
		log.Printf("background termination: list workers: %v", err)
		return
	}
	now := m.now()
	for _, agent := range agents {
		if ctx.Err() != nil {
			return
		}
		if !backgroundExpired(agent, now) {
			continue
		}
		m.dispatchBackgroundExpiry(ctx, agent.AgentID, m.terminateExpiredBackgroundWorker)
	}
}

// Deduplicate each worker while allowing independent terminations to progress.
func (m *Manager) dispatchBackgroundExpiry(ctx context.Context, agentID string, terminate func(context.Context, string)) {
	m.mu.Lock()
	if m.closed || ctx.Err() != nil {
		m.mu.Unlock()
		return
	}
	if _, exists := m.backgroundExpiryInFlight[agentID]; exists {
		m.mu.Unlock()
		return
	}
	if m.backgroundExpiryInFlight == nil {
		m.backgroundExpiryInFlight = make(map[string]struct{})
	}
	m.backgroundExpiryInFlight[agentID] = struct{}{}
	// Expose dispatch before the lifecycle lane can block, only for the
	// explicitly accelerated test-clock maintenance loop.
	if _, ok := m.clock.(*deterministic.FixedClock); ok && os.Getenv("MECHA_TEST_BACKGROUND_EXPIRY_INTERVAL") != "" {
		log.Printf("test background expiry dispatch: %s", agentID)
	}
	m.startWG.Add(1)
	m.mu.Unlock()
	go func() {
		defer m.startWG.Done()
		defer func() {
			m.mu.Lock()
			delete(m.backgroundExpiryInFlight, agentID)
			m.mu.Unlock()
		}()
		if ctx.Err() == nil {
			terminate(ctx, agentID)
		}
	}()
}

func (m *Manager) terminateExpiredBackgroundWorker(ctx context.Context, agentID string) {
	// An active action belongs to its original caller. Its cleanup policy and
	// stop result are not persisted here, so expiry must never resume it.
	action, err := m.store.ActiveManagedLifecycleAction(ctx, agentID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		log.Printf("background termination: %s: inspect lifecycle: %v", agentID, err)
		return
	}
	if action != nil {
		return
	}
	_, err = m.Terminate(ctx, TerminateRequest{
		AgentID:           agentID,
		Method:            "DELETE",
		CanonicalPath:     "/v1/managed-agents/" + agentID,
		RequestID:         uuid.NewString(),
		CanonicalBodyHash: "44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a",
		CleanupWorktree:   true,
	})
	if err != nil {
		log.Printf("background termination: %s: %v", agentID, err)
	}
}

// StartBackgroundExpiry starts one router-owned maintenance loop after recovery.
func (m *Manager) StartBackgroundExpiry() {
	m.startManagedTask(func() {
		m.TerminateExpiredBackgroundWorkers(m.runCtx)
		interval := time.Minute
		// Only the explicitly enabled test clock may shorten this maintenance loop.
		if _, ok := m.clock.(*deterministic.FixedClock); ok {
			if duration, err := time.ParseDuration(os.Getenv("MECHA_TEST_BACKGROUND_EXPIRY_INTERVAL")); err == nil && duration > 0 && duration < time.Minute {
				interval = duration
			}
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-m.runCtx.Done():
				return
			case <-ticker.C:
				m.TerminateExpiredBackgroundWorkers(m.runCtx)
			}
		}
	})
}
