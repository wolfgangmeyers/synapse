package managed

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

const embeddedOwnerTTL = 30 * time.Second

type embeddedOwnerContextKey struct{}
type embeddedOwner struct {
	binding          store.EmbeddedBinding
	identity         string
	backgroundBash   *backgroundBashOwnerCapability
	claudeOrphanRoot string
}

// ensureEmbeddedOwner guarantees that the Manager holds an active, valid owner claim
// for the local embedded agent session. For local embedded sessions, the router process
// itself owns the lane, and m.laneFor(agentID).serial.Lock() is the single source of
// truth for in-process turn serialization.
func (m *Manager) ensureEmbeddedOwner(ctx context.Context, agentID string, saved store.ManagedAgentRecord) (embeddedOwner, error) {
	m.mu.Lock()
	owner, ok := m.embeddedOwners[agentID]
	closed := m.closed
	m.mu.Unlock()
	if closed {
		return embeddedOwner{}, errors.New("manager closed")
	}
	binding := store.EmbeddedBinding{
		AgentID:    agentID,
		SessionID:  saved.PiSessionID,
		Epoch:      saved.SessionEpoch,
		Generation: saved.RuntimeGeneration,
	}
	if ok {
		if owner.binding.AgentID != agentID || owner.binding.SessionID != saved.PiSessionID || owner.binding.Epoch != saved.SessionEpoch || owner.binding.Generation != saved.RuntimeGeneration {
			return embeddedOwner{}, errors.New("cached embedded owner binding mismatch")
		}
		if err := m.store.RenewEmbeddedOwner(ctx, owner.binding, owner.identity, embeddedOwnerTTL); err != nil {
			return embeddedOwner{}, fmt.Errorf("validate cached embedded owner: %w", err)
		}
		return owner, nil
	}
	ownerID, err := newEmbeddedOwnerIdentity()
	if err != nil {
		return embeddedOwner{}, fmt.Errorf("create embedded owner identity: %w", err)
	}
	token, err := m.store.AcquireEmbeddedOwner(ctx, binding, ownerID, embeddedOwnerTTL)
	if err != nil {
		return embeddedOwner{}, fmt.Errorf("acquire embedded owner for agent %s: %w", agentID, err)
	}
	binding.OwnerToken = token
	owner = embeddedOwner{binding: binding, identity: ownerID, backgroundBash: &backgroundBashOwnerCapability{}}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return embeddedOwner{}, errors.New("manager closed")
	}
	if m.embeddedOwners == nil {
		m.embeddedOwners = make(map[string]embeddedOwner)
	}
	m.embeddedOwners[agentID] = owner
	m.mu.Unlock()
	return owner, nil
}

// refreshEmbeddedOwner refreshes the lease for an in-flight owner claim.
// Under Stage 2 lease elimination, this is a no-op.
func (m *Manager) refreshEmbeddedOwner(ctx context.Context, owner *embeddedOwner) error {
	return nil
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
	if m.closed {
		m.mu.Unlock()
		return errors.New("manager is closed")
	}
	if m.embeddedStartupGeneration != 0 && m.embeddedStartupGeneration != g {
		m.mu.Unlock()
		return errors.New("embedded startup generation changed")
	}
	m.embeddedStartupGeneration = g
	m.mu.Unlock()

	if m.store != nil {
		ctx := m.runCtx
		if ctx == nil {
			ctx = context.Background()
		}
		if err := m.ResetActiveEmbeddedAgents(ctx); err != nil {
			return err
		}
	}
	return nil
}

// ResetActiveEmbeddedAgents resets active embedded agents to idle and reconciles
// dangling tool history entries from interrupted turns.
func (m *Manager) ResetActiveEmbeddedAgents(ctx context.Context) error {
	if m.store == nil {
		return nil
	}
	return m.store.ResetActiveEmbeddedAgents(ctx)
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
		if m.runCtx.Err() != nil || !valid() {
			return
		}
		token, err := m.store.AcquireEmbeddedOwner(m.runCtx, binding, ownerID, embeddedOwnerTTL)
		if err != nil {
			return
		}
		binding.OwnerToken = token
		completed = true
		runCtx, cancel := context.WithCancel(m.runCtx)
		defer cancel()
		owner := embeddedOwner{binding: binding, identity: ownerID, backgroundBash: &backgroundBashOwnerCapability{}}
		// Retire the previous incarnation before exposing its replacement.
		m.mu.Lock()
		previous, hadPrevious := m.embeddedOwners[agent.AgentID]
		m.mu.Unlock()
		if hadPrevious && backgroundBashOwnerKey(previous) != backgroundBashOwnerKey(owner) {
			if err := m.retireBackgroundBashOwner(previous); err != nil {
				m.failEmbeddedOwner(binding, operationID, "EMBEDDED_START_FAILED", fmt.Errorf("cleanup previous background bash owner: %w", err))
				return
			}
		}
		// Publish the acquired identity before any fallible recovery work. Even
		// failed startup must not leave an obsolete cached identity behind.
		m.mu.Lock()
		if m.embeddedOwners == nil {
			m.embeddedOwners = make(map[string]embeddedOwner)
		}
		if m.closed {
			m.mu.Unlock()
			return
		}
		m.embeddedOwners[agent.AgentID] = owner
		m.mu.Unlock()
		runCtx = context.WithValue(runCtx, embeddedOwnerContextKey{}, owner)
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
			if runCtx.Err() == nil && !errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), "context canceled") {
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
		lane.goalOwnerInvalid = false
		_ = m.store.MarkManagedOperationTerminal(runCtx, operationID)
		if m.embeddedRunnerFactory == nil && runCtx.Err() == nil {
			published := false
			m.mu.Lock()
			if !m.closed {
				if m.embeddedOwners == nil {
					m.embeddedOwners = make(map[string]embeddedOwner)
				}
				m.embeddedOwners[agent.AgentID] = owner
				published = true
			}
			m.mu.Unlock()
			if published {
				m.offerLaunchDirect(agent.AgentID) // queued during launch or cold recovery
				m.recoverReservedEmbeddedGoalSnoozes(binding)
				if recovered {
					if policy, err := m.store.ManagedGoalPolicyFor(runCtx, agent.AgentID); err == nil && policy != nil && policy.Config.Enabled {
						curAgent, err := m.store.ManagedAgent(runCtx, agent.AgentID)
						if err == nil && curAgent != nil && curAgent.ActivityState == "idle" && curAgent.LifecycleState == "running" && !m.hasActiveSubagents(agent.AgentID) {
							go m.kickoffIdleGoalContinuation(binding)
						}
					}
				}
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
		prompts = []agent.AgentMessage{ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "The mailbox router was restarted. Your session was restored from saved context. Continue working from your saved context."}}}}
	}
	return m.runEmbeddedClaimedWake(ctx, saved, scope, prompts)
}

// RecoverEmbeddedRoots re-dispatches only saved embedded launch reservations.
// A real runner must validate and reopen its own ordered SQLite session.
func (m *Manager) RecoverEmbeddedRoots(ctx context.Context) error {
	if m.store != nil {
		if err := m.ResetActiveEmbeddedAgents(ctx); err != nil {
			return err
		}
	}
	_ = m.ReconcileInterruptedSubagents(ctx)
	agents, err := m.store.ManagedAgents(ctx)
	if err != nil {
		return err
	}
	for _, agent := range agents {
		if agent.ParentAgentID != "" {
			continue
		}
		if agent.Runtime != "embedded" || agent.DesiredState != "running" {
			continue
		}

		if agent.LifecycleState != "starting" && agent.LifecycleState != "running" {
			continue
		}

		opID := agent.LaunchOperationID
		if opID == "" {
			if id, err := m.ids.Next(); err == nil && id != "" {
				opID = id
			} else {
				opID = agent.AgentID + "-launch"
			}
			agent.LaunchOperationID = opID
		}
		m.startEmbeddedRoot(*agent, opID, true)
	}
	return nil
}

// EnsureEmbeddedRootRunning ensures an embedded root is running if it has no active owner.
func (m *Manager) EnsureEmbeddedRootRunning(agentID string) {
	m.mu.Lock()
	_, hasOwner := m.embeddedOwners[agentID]
	m.mu.Unlock()
	if !hasOwner {
		agent, err := m.store.ManagedAgent(m.runCtx, agentID)
		if err == nil && agent.Runtime == "embedded" && agent.DesiredState == "running" && agent.LifecycleState == "running" {
			m.startEmbeddedRoot(*agent, agent.LaunchOperationID, true)
		}
	}
}
