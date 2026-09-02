package transport

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInlineToolControlRejectsUnsupportedAndMissingTargets(t *testing.T) {
	durable, router := newTransportOrderingFixture(t)
	sourceErrors := make(chan map[string]any, 3)
	source := newTransportSession(t, router, durable, "source", "source-instance", "source-lease", "user_interaction", func(fields map[string]any) error {
		if fields["type"] == "error" {
			sourceErrors <- fields
		}
		return nil
	})
	target := newTransportSession(t, router, durable, "target", "target-instance", "target-lease", "agent", func(map[string]any) error { return nil })

	unsupported := inlineToolControlTestFrame("unsupported", "target", "target-instance")
	require.NoError(t, router.inlineToolControl(source, unsupported))
	require.Equal(t, "inline_tool_control_unsupported", (<-sourceErrors)["code"])

	missing := inlineToolControlTestFrame("missing", "missing-target", "target-instance")
	require.NoError(t, router.inlineToolControl(source, missing))
	require.Equal(t, "target_not_connected", (<-sourceErrors)["code"])

	staleInstance := inlineToolControlTestFrame("stale-instance", "target", "replacement-instance")
	require.NoError(t, router.inlineToolControl(source, staleInstance))
	require.Equal(t, "target_instance_changed", (<-sourceErrors)["code"])

	require.NotNil(t, target)
}

func TestInlineToolControlRejectsStaleAndDuplicateResults(t *testing.T) {
	durable, router := newTransportOrderingFixture(t)
	targetErrors := make(chan map[string]any, 3)
	sourceResults := make(chan map[string]any, 1)
	source := newTransportSession(t, router, durable, "source", "source-instance", "source-lease", "user_interaction", func(fields map[string]any) error {
		if fields["type"] == "inline_tool_control_result" {
			sourceResults <- fields
		}
		return nil
	})
	target := newTransportSession(t, router, durable, "target", "target-instance", "target-lease", "agent", func(fields map[string]any) error {
		if fields["type"] == "error" {
			targetErrors <- fields
		}
		return nil
	})
	target.inlineToolControl = true

	require.NoError(t, router.inlineToolControl(source, inlineToolControlTestFrame("request", "target", "target-instance")))

	stale := inlineToolControlResultTestFrame("stale-result", "source", "other-control", "target", "target-instance", "target-lease")
	require.NoError(t, router.inlineToolControlResult(target, stale))
	require.Equal(t, "inline_tool_control_result_not_pending", (<-targetErrors)["code"])

	valid := inlineToolControlResultTestFrame("result", "source", "request", "target", "target-instance", "target-lease")
	require.NoError(t, router.inlineToolControlResult(target, valid))
	require.Equal(t, "request", (<-sourceResults)["control_id"])

	require.NoError(t, router.inlineToolControlResult(target, valid))
	require.Equal(t, "inline_tool_control_result_not_pending", (<-targetErrors)["code"])
}

func TestInlineToolControlRejectsDuplicateAndCapacity(t *testing.T) {
	durable, router := newTransportOrderingFixture(t)
	router.maxControls = 1
	router.maxPerSrc = 1
	sourceErrors := make(chan map[string]any, 2)
	source := newTransportSession(t, router, durable, "source", "source-instance", "source-lease", "user_interaction", func(fields map[string]any) error {
		if fields["type"] == "error" {
			sourceErrors <- fields
		}
		return nil
	})
	targetSendCount := 0
	target := newTransportSession(t, router, durable, "target", "target-instance", "target-lease", "agent", func(map[string]any) error {
		targetSendCount++
		return nil
	})
	target.inlineToolControl = true

	first := inlineToolControlTestFrame("first", "target", "target-instance")
	require.NoError(t, router.inlineToolControl(source, first))
	require.NoError(t, router.inlineToolControl(source, first))
	require.Equal(t, "inline_tool_control_already_pending", (<-sourceErrors)["code"])

	second := inlineToolControlTestFrame("second", "target", "target-instance")
	require.NoError(t, router.inlineToolControl(source, second))
	require.Equal(t, "inline_tool_control_capacity_exceeded", (<-sourceErrors)["code"])
	require.Equal(t, 1, targetSendCount)
}

func TestInlineToolControlRemovesPendingAfterForwardingFailure(t *testing.T) {
	durable, router := newTransportOrderingFixture(t)
	sourceErrors := make(chan map[string]any, 1)
	source := newTransportSession(t, router, durable, "source", "source-instance", "source-lease", "user_interaction", func(fields map[string]any) error {
		if fields["type"] == "error" {
			sourceErrors <- fields
		}
		return nil
	})
	target := newTransportSession(t, router, durable, "target", "target-instance", "target-lease", "agent", func(map[string]any) error {
		return errors.New("forward failed")
	})
	target.inlineToolControl = true

	request := inlineToolControlTestFrame("forward-failure", "target", "target-instance")
	require.NoError(t, router.inlineToolControl(source, request))
	require.Equal(t, "target_send_failed", (<-sourceErrors)["code"])

	router.mu.RLock()
	_, pending := router.pendingInlineTool["source\x00forward-failure"]
	router.mu.RUnlock()
	require.False(t, pending)
}

func inlineToolControlTestFrame(frameID, targetAgent, targetInstance string) map[string]json.RawMessage {
	return map[string]json.RawMessage{
		"type": rawJSON("inline_tool_control"), "frame_id": rawJSON(frameID),
		"agent_id": rawJSON("source"), "instance_id": rawJSON("source-instance"), "lease_id": rawJSON("source-lease"),
		"target_agent_id": rawJSON(targetAgent), "target_instance_id": rawJSON(targetInstance),
		"control_id": rawJSON(frameID), "enabled": rawJSON(true),
	}
}

func inlineToolControlResultTestFrame(frameID, sourceAgent, controlID, targetAgent, targetInstance, targetLease string) map[string]json.RawMessage {
	return map[string]json.RawMessage{
		"type": rawJSON("inline_tool_control_result"), "frame_id": rawJSON(frameID),
		"agent_id": rawJSON(targetAgent), "instance_id": rawJSON(targetInstance), "lease_id": rawJSON(targetLease),
		"source_agent_id": rawJSON(sourceAgent), "control_id": rawJSON(controlID),
		"accepted": rawJSON(true), "enabled": rawJSON(true),
	}
}
