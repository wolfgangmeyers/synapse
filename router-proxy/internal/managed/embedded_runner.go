package managed

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

const embeddedOwnerTTL = 30 * time.Second

type embeddedOwnerContextKey struct{}
type embeddedOwner struct {
	binding  store.EmbeddedBinding
	identity string
}

// Each dispatch gets a fresh identity; neither a reused Manager nor a recovered
// process may reuse a previous lease's authority.
func newEmbeddedOwnerIdentity() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(id[:]), nil
}

// startEmbeddedRoot is a post-reservation handoff for a saved root or worker.
// It never creates a stock
// session file, process, or RPC connection.
// SetEmbeddedStartupGeneration is called only after the Router binds and mints
// its process incarnation. It does not authorize a provider call on its own.
func (m *Manager) SetEmbeddedStartupGeneration(g int64) error {
	if g <= 0 {
		return errors.New("missing embedded startup generation")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return errors.New("manager is closed")
	}
	if m.embeddedStartupGeneration != 0 && m.embeddedStartupGeneration != g {
		return errors.New("embedded startup generation changed")
	}
	m.embeddedStartupGeneration = g
	return nil
}

func (m *Manager) startEmbeddedRoot(agent store.ManagedAgentRecord, operationID string, recovered bool) {
	lane := m.laneFor(agent.AgentID)
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	binding := store.EmbeddedBinding{AgentID: agent.AgentID, SessionID: agent.PiSessionID, Epoch: agent.SessionEpoch, Generation: agent.RuntimeGeneration}
	if recovered {
		if previous, ok := m.embeddedRecovery[agent.AgentID]; ok && previous == binding {
			m.mu.Unlock()
			return
		}
		if m.embeddedRecovery == nil {
			m.embeddedRecovery = make(map[string]store.EmbeddedBinding)
		}
		m.embeddedRecovery[agent.AgentID] = binding
	}
	m.startWG.Add(1)
	m.mu.Unlock()
	go func() {
		defer m.startWG.Done()
		completed := false
		defer func() {
			if !recovered || completed {
				return
			}
			m.mu.Lock()
			if m.embeddedRecovery[agent.AgentID] == binding {
				delete(m.embeddedRecovery, agent.AgentID)
			}
			m.mu.Unlock()
		}()
		lane.serial.Lock()
		defer lane.serial.Unlock()
		// Revalidate the exact saved incarnation after every lease wait. A
		// pending launch alone does not grant an obsolete session authority.
		valid := func() bool {
			current, err := m.store.ManagedAgent(m.runCtx, agent.AgentID)
			return err == nil && current.Runtime == "embedded" && current.DesiredState == "running" && (current.LifecycleState == "starting" || current.LifecycleState == "running") && current.LaunchOperationID == operationID && current.PiSessionID == agent.PiSessionID && current.SessionEpoch == agent.SessionEpoch && current.RuntimeGeneration == agent.RuntimeGeneration
		}
		if !valid() {
			return
		}
		// The committed role is authoritative. Missing or stale reservations
		// fail closed; a runner must never invent a root on recovery.
		if _, err := m.store.EmbeddedSessionRole(m.runCtx, binding); err != nil {
			return
		}
		aliasTrace("startup", binding.AgentID, binding.SessionID, operationID, "")
		ownerID, err := newEmbeddedOwnerIdentity()
		if err != nil {
			return
		}
		var token int64
		for {
			if m.runCtx.Err() != nil || !valid() {
				return
			}
			token, err = m.store.AcquireEmbeddedOwner(m.runCtx, binding, ownerID, embeddedOwnerTTL)
			if err == nil {
				break
			}
			var held *store.EmbeddedOwnerLeaseHeld
			if !recovered || !errors.As(err, &held) {
				return
			}
			// One cancellable timer per saved agent. The persisted deadline is
			// millisecond precision, so leave a small margin before competing.
			wait := held.RetryAfter + 10*time.Millisecond
			if wait < 10*time.Millisecond {
				wait = 10 * time.Millisecond
			}
			if wait > embeddedOwnerTTL+time.Second {
				wait = embeddedOwnerTTL + time.Second
			}
			lane.serial.Unlock()
			timer := time.NewTimer(wait)
			select {
			case <-m.runCtx.Done():
				timer.Stop()
				lane.serial.Lock()
				return
			case <-timer.C:
			}
			lane.serial.Lock()
		}
		binding.OwnerToken = token
		completed = true
		runCtx, cancel := context.WithCancel(m.runCtx)
		owner := embeddedOwner{binding: binding, identity: ownerID}
		runCtx = context.WithValue(runCtx, embeddedOwnerContextKey{}, owner)
		done := make(chan struct{})
		// Admission shares Close's mutex; retained heartbeats are joined by
		// startWG after parent cancellation, without holding the serial lane.
		if !m.startManagedTask(func() {
			defer close(done)
			var ticks <-chan time.Time
			var stop func()
			if m.embeddedOwnerTicker != nil {
				ticks, stop = m.embeddedOwnerTicker()
			} else {
				ticker := time.NewTicker(embeddedOwnerTTL / 3)
				ticks, stop = ticker.C, ticker.Stop
			}
			defer stop()
			for {
				select {
				case <-runCtx.Done():
					return
				case <-ticks:
					if m.store.RenewEmbeddedOwner(runCtx, binding, ownerID, embeddedOwnerTTL) != nil {
						cancel()
						return
					}
				}
			}
		}) {
			cancel()
			return
		}
		keepOwner := false
		defer func() {
			if !keepOwner {
				cancel()
				<-done
			}
		}()
		runner := m.embeddedRunnerFactory
		if runner == nil {
			runner = m.runEmbeddedSession
		}
		var runErr error
		if recovered && m.embeddedRunnerFactory == nil {
			m.mu.Lock()
			generation := m.embeddedStartupGeneration
			m.mu.Unlock()
			if generation <= 0 {
				return
			}
			runErr = m.dispatchEmbeddedStartupWake(runCtx, agent, binding, generation)
		} else {
			runErr = runner(runCtx, agent)
		}
		if err := runErr; err != nil {
			if runCtx.Err() == nil {
				m.failEmbeddedOwner(binding, operationID, "EMBEDDED_START_FAILED", err)
			}
			return
		}
		if runCtx.Err() != nil {
			return
		}
		if err := m.store.ReadyEmbeddedRoot(runCtx, binding, operationID); err != nil {
			if runCtx.Err() == nil {
				m.failEmbeddedOwner(binding, operationID, "EMBEDDED_READY_FAILED", err)
			}
			return
		}
		_ = m.store.MarkManagedOperationTerminal(runCtx, operationID)
		if m.embeddedRunnerFactory == nil && runCtx.Err() == nil {
			m.mu.Lock()
			if !m.closed {
				if m.embeddedOwners == nil {
					m.embeddedOwners = make(map[string]embeddedOwner)
				}
				m.embeddedOwners[agent.AgentID] = owner
				keepOwner = true // heartbeat lives until Manager.Close cancels runCtx
			}
			m.mu.Unlock()
			if keepOwner {
				m.OfferDirect(agent.AgentID) // queued during launch or cold recovery
				m.recoverReservedEmbeddedGoalSnoozes(binding)
			}
		}
	}()
}

func (m *Manager) failEmbeddedOwner(binding store.EmbeddedBinding, operationID, code string, cause error) {
	// The ownership check and failure transition must share a Store transaction.
	// Never mark a newer owner's lifecycle failed after a stale callback.
	_ = m.store.MarkEmbeddedOwnerFailed(context.Background(), binding, operationID, code, sanitizeError(cause))
}

// dispatchEmbeddedStartupWake runs on the same serial lane as ordinary mail.
// Classification alone never calls a provider; every SDK invocation needs a
// checked, one-shot wake claim under the current owner and process generation.
func (m *Manager) dispatchEmbeddedStartupWake(ctx context.Context, saved store.ManagedAgentRecord, b store.EmbeddedBinding, generation int64) error {
	decision, err := m.store.ClassifyEmbeddedRestart(ctx, b, generation)
	if err != nil {
		return err
	}
	if decision.Suppressed {
		return nil
	}
	if decision.Kind == "pending-original" {
		prompt, id, err := m.store.ClaimEmbeddedPendingOriginal(ctx, b, generation)
		if err != nil {
			return err
		}
		// Use the durably claimed prompt, not the stale managed-row copy.
		// Ordinary SDK persistence still fences owner, intent and provider entry.
		if err := m.runEmbeddedSessionPrompt(ctx, saved, prompt); err != nil {
			return err // the pending claim remains UNKNOWN, never replayable
		}
		return m.store.CompleteEmbeddedPrompt(ctx, b, id)
	}
	if decision.Kind == "unentered-original" {
		if err := m.runEmbeddedSessionOriginalRestart(ctx, saved, generation); err != nil {
			return err
		}
		// Continue persists the original checked final before this second dispatch.
		decision, err = m.store.ClassifyEmbeddedRestart(ctx, b, generation)
		if err != nil {
			return err
		}
	}
	if decision.Kind == "neutral" && !decision.Suppressed && decision.Source.Sequence != 0 {
		// Classification supplies only an exact settled nonmail source. The
		// Store rechecks source, owner, process and final head before planning.
		if _, err := m.store.ScheduleEmbeddedSettledOriginalWake(ctx, b, generation, decision.Source.Sequence, decision.Source.Digest); err != nil {
			return err
		}
		decision, err = m.store.ClassifyEmbeddedRestart(ctx, b, generation)
		if err != nil {
			return err
		}
	}
	if decision.Kind != "root-idle" && decision.Kind != "worker-idle" && decision.Kind != "interrupted" {
		return nil
	}
	if err := m.store.ClaimEmbeddedRestartWake(ctx, b, generation, decision.Wake.ID); err != nil {
		return err
	}
	scope := embeddedClaimedWakeScope{binding: b, processGeneration: generation, id: decision.Wake.ID, kind: decision.Kind}
	var prompts []agent.AgentMessage
	if decision.Kind != "interrupted" {
		prompts = []agent.AgentMessage{ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "Continue working from your saved context."}}}}
	}
	return m.runEmbeddedClaimedWake(ctx, saved, scope, prompts)
}

// RecoverEmbeddedRoots re-dispatches only saved embedded launch reservations.
// A real runner must validate and reopen its own ordered SQLite session.
func (m *Manager) RecoverEmbeddedRoots(ctx context.Context) error {
	agents, err := m.store.ManagedAgents(ctx)
	if err != nil {
		return err
	}
	for _, agent := range agents {
		if agent.Runtime != "embedded" || agent.DesiredState != "running" || (agent.LifecycleState != "starting" && agent.LifecycleState != "running") {
			continue
		}

		if agent.LaunchOperationID == "" {
			continue
		}
		m.startEmbeddedRoot(*agent, agent.LaunchOperationID, true)
	}
	return nil
}
