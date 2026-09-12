package managed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

// executeManagedInlineAllowance makes an allowance useful before its root
// result can become visible. The normal desired policy deliberately remains
// default-deny: these short-lived credits exist only in the current, fenced Pi
// process and disappear on replacement rather than silently becoming a broad
// persistent permission.
func (m *Manager) executeManagedInlineAllowance(ctx context.Context, binding *managedBinding, invocation RootControlInvocation) (RootControlHandlerResult, error) {
	arguments, ok := invocation.Arguments.(map[string]any)
	if !ok {
		return RootControlHandlerResult{}, errors.New("managed inline allowance arguments are invalid")
	}
	toolName, toolOK := arguments["toolName"].(string)
	usageCount, countOK := managedInlineAllowanceCount(arguments["usageCount"])
	if !toolOK || !countOK || (toolName != "bash" && toolName != "edit" && toolName != "write") {
		return RootControlHandlerResult{}, errors.New("managed inline allowance is invalid")
	}
	if !m.rootBindingCurrent(ctx, binding, invocation.Fence) {
		return RootControlHandlerResult{}, errors.New("managed runtime binding changed before inline allowance")
	}
	desired, err := m.store.LoadManagedDesiredPolicy(ctx, invocation.Fence.AgentID)
	if err != nil {
		return RootControlHandlerResult{}, err
	}
	policy, err := readinessPolicyValue(desired)
	if err != nil {
		return RootControlHandlerResult{}, err
	}
	inline, ok := policy["inline"].(map[string]any)
	if !ok || inline["enabled"] != true {
		return RootControlHandlerResult{}, errors.New("managed inline tools are disabled")
	}
	credits, ok := inline["credits"].(map[string]any)
	if !ok {
		return RootControlHandlerResult{}, errors.New("managed inline policy is invalid")
	}
	// The target extension consumes a credit exactly once before the builtin
	// runs. Do not accumulate caller-controlled values or retain them in the
	// desired policy; a later normal projection and every replacement return to
	// default deny.
	credits = cloneInlineCredits(credits)
	credits[toolName] = usageCount
	inline["credits"] = credits
	canonical, _, err := HashReadinessCanonicalJSON(policy)
	if err != nil {
		return RootControlHandlerResult{}, err
	}
	if err := m.projectManagedEphemeralPolicy(ctx, binding, invocation.Fence, policy, canonical); err != nil {
		return RootControlHandlerResult{}, err
	}
	return RootControlHandlerResult{Status: "ok", Content: fmt.Sprintf("Granted %d inline %s use(s).", usageCount, toolName), Details: map[string]any{"tool_name": toolName, "usage_count": usageCount}}, nil
}

func managedInlineAllowanceCount(value any) (int64, bool) {
	switch count := value.(type) {
	case int64:
		return count, count >= 1 && count <= 5
	case json.Number:
		parsed, err := strconv.ParseInt(string(count), 10, 64)
		return parsed, err == nil && parsed >= 1 && parsed <= 5
	default:
		return 0, false
	}
}

func cloneInlineCredits(value map[string]any) map[string]any {
	result := make(map[string]any, len(value)+1)
	for toolName, count := range value {
		result[toolName] = count
	}
	return result
}

func (m *Manager) projectManagedEphemeralPolicy(ctx context.Context, binding *managedBinding, fence store.ManagedRuntimeFence, policy map[string]any, canonical []byte) error {
	rpc, ok := binding.rpc.(readinessSourceRPC)
	if !ok {
		return errors.New("managed Pi does not support inline allowances")
	}
	envelope := map[string]any{"protocol": "mecha.policy/v1", "policy": policy, "runtime_generation": fence.RuntimeGeneration, "session_epoch": fence.SessionEpoch, "manifest_sha256": fence.ManifestHash}
	if _, err := rpc.ApplyPolicy(ctx, envelope); err != nil {
		return err
	}
	receipt, err := rpc.WaitForPolicyApplied(ctx, fence.RuntimeGeneration, fence.SessionEpoch)
	if err != nil {
		return err
	}
	applied, err := normalizePolicyReceiptEnvelope(receipt, fence)
	if err != nil {
		return err
	}
	appliedCanonical, _, err := HashReadinessCanonicalJSON(applied)
	if err != nil || string(appliedCanonical) != string(canonical) {
		return errors.New("inline allowance receipt does not match the requested bounded credit")
	}
	if !m.rootBindingCurrent(ctx, binding, fence) {
		return errors.New("managed runtime binding changed before inline allowance confirmation")
	}
	return nil
}
