package managed

import (
	"context"
	"errors"
	"fmt"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

var errManagedControlsUnavailable = errors.New("managed agent is not ready for controls")

// DirectRootControl executes a closed, manifest-backed root operation for the
// active managed process. Direct Web UI controls use the same host handler as
// a Pi tool request, while the HTTP request ID gives the host adapter its
// idempotency key.
func (m *Manager) DirectRootControl(ctx context.Context, agentID, requestID, operationID string, arguments map[string]any, receipt ...*store.ManagedOperation) (RootControlHandlerResult, error) {
	agent, err := m.store.ManagedAgent(ctx, agentID)
	if err != nil {
		return RootControlHandlerResult{}, err
	}
	if agent.Runtime == "embedded" {
		var operation *store.ManagedOperation
		if len(receipt) > 0 {
			operation = receipt[0]
		}
		return m.directEmbeddedBrowserGoal(ctx, agentID, operationID, arguments, operation)
	}
	binding, fence, err := m.currentDirectControlBinding(ctx, agentID)
	if err != nil {
		return RootControlHandlerResult{}, err
	}
	binding.rootControlSerial.Lock()
	defer binding.rootControlSerial.Unlock()
	if !m.rootBindingCurrent(ctx, binding, fence) {
		return RootControlHandlerResult{}, errManagedControlsUnavailable
	}
	tool, found := m.manifestToolForOperation(operationID)
	if !found || !validDirectRootArguments(tool, arguments) {
		return RootControlHandlerResult{}, errors.New("managed control request is invalid")
	}
	handler, available := m.rootControlHandlers.Handler(tool)
	if !available {
		return RootControlHandlerResult{}, errors.New("managed control handler is unavailable")
	}
	if requestID == "" {
		requestID, err = m.ids.Next()
		if err != nil {
			return RootControlHandlerResult{}, err
		}
	}
	resultID, err := m.ids.Next()
	if err != nil {
		return RootControlHandlerResult{}, err
	}
	toolCallID, err := m.ids.Next()
	if err != nil {
		return RootControlHandlerResult{}, err
	}
	return m.executeRootControlHandler(ctx, binding, handler, RootControlInvocation{
		RequestID: requestID, ResultID: resultID, ToolCallID: toolCallID, OperationID: operationID, Arguments: arguments, Fence: fence,
	}, false)
}

// ManagedInlineToolsEnabled reads the durable desired policy. The router owns
// this setting so a restart reapplies the same local Pi guard behavior.
func (m *Manager) ManagedInlineToolsEnabled(ctx context.Context, agentID string) (bool, error) {
	desired, err := m.store.LoadManagedDesiredPolicy(ctx, agentID)
	if err != nil {
		return false, err
	}
	policy, err := readinessPolicyValue(desired)
	if err != nil {
		return false, err
	}
	inline, ok := policy["inline"].(map[string]any)
	if !ok {
		return false, errors.New("managed inline policy is invalid")
	}
	enabled, ok := inline["enabled"].(bool)
	if !ok {
		return false, errors.New("managed inline policy is invalid")
	}
	return enabled, nil
}

// SetManagedInlineTools changes the router-owned policy and waits for the
// active managed Pi process to confirm the new guard projection before
// reporting success to the UI.
func (m *Manager) SetManagedInlineTools(ctx context.Context, agentID string, enabled bool, receipt ...*store.ManagedOperation) error {
	agent, err := m.store.ManagedAgent(ctx, agentID)
	if err != nil {
		return err
	}
	if agent.Runtime == "embedded" {
		var operation *store.ManagedOperation
		if len(receipt) > 0 {
			operation = receipt[0]
		}
		return m.setEmbeddedBrowserInline(ctx, agentID, enabled, operation)
	}
	binding, fence, err := m.currentDirectControlBinding(ctx, agentID)
	if err != nil {
		return err
	}
	binding.serial.Lock()
	defer binding.serial.Unlock()
	if !m.rootBindingCurrent(ctx, binding, fence) {
		return errManagedControlsUnavailable
	}
	desired, err := m.store.LoadManagedDesiredPolicy(ctx, agentID)
	if err != nil {
		return err
	}
	policy, err := readinessPolicyValue(desired)
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
	next := store.ManagedDesiredPolicy{Canonical: canonical, SHA256: hash}
	// Persist the intended guard before delivery. If the process exits during
	// the update, its next readiness gate applies this same desired policy.
	if err := m.store.UpdateManagedDesiredPolicy(ctx, fence, next); err != nil {
		return err
	}
	return m.applyCurrentManagedPolicy(ctx, binding, fence, next)
}

func (m *Manager) currentDirectControlBinding(ctx context.Context, agentID string) (*managedBinding, store.ManagedRuntimeFence, error) {
	if agentID == "" {
		return nil, store.ManagedRuntimeFence{}, errors.New("managed agent is invalid")
	}
	m.mu.Lock()
	binding := m.bindings[agentID]
	closed := m.closed
	m.mu.Unlock()
	if closed || binding == nil {
		return nil, store.ManagedRuntimeFence{}, errManagedControlsUnavailable
	}
	fence := bindingFence(agentID, binding)
	if directControlOwnershipLost(m.processOps.Probe(binding.identity).State) {
		m.reconcileLostDirectControlBinding(ctx, binding, fence)
		return nil, store.ManagedRuntimeFence{}, errManagedControlsUnavailable
	}
	if !m.rootBindingCurrent(ctx, binding, fence) {
		return nil, store.ManagedRuntimeFence{}, errManagedControlsUnavailable
	}
	return binding, fence, nil
}

func directControlOwnershipLost(state ProcessProbeState) bool {
	return state == ProcessAbsent || state == ProcessPresent
}

// reconcileLostDirectControlBinding treats an absent group or a dead leader
// with remaining descendants as a hard exit only while this exact ready fence
// still owns the durable row. The lane makes a racing Wait callback a no-op.
func (m *Manager) reconcileLostDirectControlBinding(ctx context.Context, binding *managedBinding, fence store.ManagedRuntimeFence) {
	if binding == nil || binding.lane == nil {
		return
	}
	binding.lane.serial.Lock()
	defer binding.lane.serial.Unlock()
	if !m.runningBindingCurrent(ctx, binding, fence) || !directControlOwnershipLost(m.processOps.Probe(binding.identity).State) {
		return
	}
	m.recordReadyBindingProcessExitInLane(binding, fence, "The managed process is no longer available.")
}

func validDirectRootArguments(tool ManifestTool, arguments map[string]any) bool {
	if tool.OperationID != "goal.control" {
		return ValidateManifestArguments(tool, arguments) == nil
	}
	// The shared Web UI goal editor also owns its evaluator settings. The Pi
	// tool schema intentionally carries only action/goal, so validate that
	// immutable subset separately while passing the already router-validated
	// settings to the durable host adapter.
	toolArguments := map[string]any{}
	for key, value := range arguments {
		if key != "settings" {
			toolArguments[key] = value
		}
	}
	if ValidateManifestArguments(tool, toolArguments) != nil {
		return false
	}
	settings, exists := arguments["settings"]
	return !exists || validManagedGoalSettings(settings)
}

func validManagedGoalSettings(value any) bool {
	settings, ok := value.(map[string]any)
	if !ok || len(settings) != 7 {
		return false
	}
	boolean := func(name string) bool {
		_, found := settings[name].(bool)
		return found
	}
	integer := func(name string, minimum int64) bool {
		current, found := settings[name].(int64)
		return found && current >= minimum
	}
	nullableInteger := func(name string) bool {
		current, found := settings[name]
		if !found || current == nil {
			return found
		}
		number, ok := current.(int64)
		return ok && number > 0
	}
	turnLimit := settings["snooze_turn_limit"]
	if turnLimit != nil {
		value, ok := turnLimit.(int64)
		if !ok || value < 1 || value > 20 {
			return false
		}
	}
	return boolean("goal_lock_enabled") && boolean("snooze_enabled") && integer("snooze_duration_ms", 1) && nullableInteger("snooze_budget_cap_ms") && integer("snooze_refill_yield_ms", 1) && integer("snooze_refill_elapsed_ms", 1) && nullableInteger("snooze_turn_limit")
}

func (m *Manager) manifestToolForOperation(operationID string) (ManifestTool, bool) {
	for _, tool := range m.manifest.Tools {
		if tool.OperationID == operationID {
			return tool, true
		}
	}
	return ManifestTool{}, false
}

func (m *Manager) applyCurrentManagedPolicy(ctx context.Context, binding *managedBinding, fence store.ManagedRuntimeFence, desired store.ManagedDesiredPolicy) error {
	sourceRPC, ok := binding.rpc.(readinessSourceRPC)
	if !ok {
		return errors.New("managed Pi does not support policy controls")
	}
	value, err := readinessPolicyValue(desired)
	if err != nil {
		return err
	}
	envelope := map[string]any{"protocol": "mecha.policy/v1", "policy": value, "runtime_generation": fence.RuntimeGeneration, "session_epoch": fence.SessionEpoch, "manifest_sha256": fence.ManifestHash}
	response, err := sourceRPC.ApplyPolicy(ctx, envelope)
	if err != nil {
		return err
	}
	if !privateCommandAcknowledgement(response) {
		responsePolicy, normalizeErr := normalizePolicyResponse(response)
		if normalizeErr != nil {
			return normalizeErr
		}
		responseCanonical, responseHash, hashErr := HashReadinessCanonicalJSON(responsePolicy)
		if hashErr != nil || string(responseCanonical) != string(desired.Canonical) || responseHash != desired.SHA256 {
			return errors.New("policy command response does not match desired policy")
		}
	}
	receipt, err := sourceRPC.WaitForPolicyApplied(ctx, fence.RuntimeGeneration, fence.SessionEpoch)
	if err != nil {
		return err
	}
	receiptPolicy, err := normalizePolicyReceiptEnvelope(receipt, fence)
	if err != nil {
		return err
	}
	receiptCanonical, receiptHash, err := HashReadinessCanonicalJSON(receiptPolicy)
	if err != nil || string(receiptCanonical) != string(desired.Canonical) || receiptHash != desired.SHA256 {
		return errors.New("policy receipt does not match desired policy")
	}
	if !m.rootBindingCurrent(ctx, binding, fence) {
		return errors.New("managed runtime binding changed before policy confirmation")
	}
	if err := m.store.ConfirmManagedRuntimePolicyApplied(context.Background(), fence, store.ManagedDesiredPolicy{Canonical: receiptCanonical, SHA256: receiptHash}); err != nil {
		return fmt.Errorf("managed policy confirmation failed: %w", err)
	}
	return nil
}
