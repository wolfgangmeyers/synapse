package managed

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

func (m *Manager) currentEmbeddedBrowserOwner(ctx context.Context, agentID string) (embeddedOwner, error) {
	m.mu.Lock()
	owner, ok := m.embeddedOwners[agentID]
	closed := m.closed
	m.mu.Unlock()
	if !ok || closed || m.runCtx.Err() != nil {
		return embeddedOwner{}, errManagedControlsUnavailable
	}
	// Renew checks the actual saved owner identity/token and binding. The Store
	// mutation rechecks that fence in its own immediate transaction.
	if err := m.store.RenewEmbeddedOwner(ctx, owner.binding, owner.identity, embeddedOwnerTTL); err != nil {
		return embeddedOwner{}, errManagedControlsUnavailable
	}
	return owner, nil
}

func embeddedBrowserGoalState(policy *store.ManagedGoalPolicy) map[string]any {
	state := managedGoalStatusDetails(policy)
	delete(state, "desired_revision")
	delete(state, "applied_revision")
	delete(state, "projection_state")
	state["outcome_counts"] = map[string]any{"COMPLETED": 0, "NOT_DONE": 0}
	return state
}

func (m *Manager) directEmbeddedBrowserGoal(ctx context.Context, agentID, operationID string, arguments map[string]any, operation *store.ManagedOperation) (RootControlHandlerResult, error) {
	if operationID != "goal.control" {
		return RootControlHandlerResult{}, errors.New("unsupported embedded browser control")
	}
	tool, found := m.manifestToolForOperation(operationID)
	if !found || !validDirectRootArguments(tool, arguments) {
		return RootControlHandlerResult{}, errors.New("invalid embedded browser goal")
	}
	owner, err := m.currentEmbeddedBrowserOwner(ctx, agentID)
	if err != nil {
		return RootControlHandlerResult{}, err
	}
	action, _ := arguments["action"].(string)
	policy, err := m.store.ManagedGoalPolicyFor(ctx, agentID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return RootControlHandlerResult{}, err
	}
	if action != "status" {
		config := store.ManagedGoalConfig{Settings: store.DefaultManagedGoalSettings()}
		var revision int64
		if policy != nil {
			config.Settings = policy.Config.Settings
			revision = policy.DesiredRevision
		}
		if action == "set" {
			config.Enabled = true
			config.Goal, _ = arguments["goal"].(string)
		}
		if settings, ok := arguments["settings"]; ok {
			var valid bool
			config.Settings, valid = managedGoalSettingsFromArguments(settings)
			if !valid {
				return RootControlHandlerResult{}, errors.New("invalid embedded goal settings")
			}
		}
		policy, err = m.store.UpdateEmbeddedBrowserGoal(ctx, owner.binding, config, operation, func(p *store.ManagedGoalPolicy) ([]byte, error) {
			return json.Marshal(map[string]any{"goal_state": embeddedBrowserGoalState(p)})
		}, revision)
	} else if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	if err != nil {
		return RootControlHandlerResult{}, err
	}
	return RootControlHandlerResult{Status: "ok", Details: map[string]any{"goal_state": embeddedBrowserGoalState(policy)}}, nil
}

func (m *Manager) setEmbeddedBrowserInline(ctx context.Context, agentID string, enabled bool, operation *store.ManagedOperation) error {
	owner, err := m.currentEmbeddedBrowserOwner(ctx, agentID)
	if err != nil {
		return err
	}
	previous, err := m.store.LoadManagedDesiredPolicy(ctx, agentID)
	if err != nil {
		return err
	}
	policy, err := readinessPolicyValue(previous)
	if err != nil {
		return err
	}
	inline, ok := policy["inline"].(map[string]any)
	if !ok {
		return errors.New("managed inline policy is invalid")
	}
	inline["enabled"] = enabled
	canonical, hash, err := HashReadinessCanonicalJSON(policy)
	if err != nil {
		return err
	}
	return m.store.UpdateEmbeddedBrowserPolicy(ctx, owner.binding, previous, store.ManagedDesiredPolicy{Canonical: canonical, SHA256: hash}, operation)
}
