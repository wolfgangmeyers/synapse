import { describe, expect, it } from "vitest";
import {
  buildAckFrame,
  buildBatchReceipts,
  buildGoalControlFrame,
  buildInlineToolControlFrame,
  buildInlineToolStateFrame,
  buildModelControlFrame,
  buildNackFrame,
  buildRegisterFrame,
  buildReminderLockControlFrame,
} from "../../src/protocol/frame-builders";
import { isClientFrame } from "../../src/protocol/guards";

const settings = {
  snooze_enabled: true,
  snooze_duration_ms: 60_000,
  snooze_turn_limit: null,
  snooze_budget_cap_ms: 120_000,
  snooze_refill_yield_ms: 1_000,
  snooze_refill_elapsed_ms: 2_000,
};

const item = {
  message_id: "message-1",
  message_incarnation_id: "123e4567-e89b-42d3-a456-426614174000",
  attempt_number: 2,
  delivery_expires_at: "2026-08-30T12:01:00.000Z",
};

const secondItem = {
  ...item,
  message_id: "message-2",
  message_incarnation_id: "123e4567-e89b-42d3-a456-426614174001",
};

const receiptIdentity = {
  agentId: "ui-agent",
  instanceId: "instance-1",
  leaseId: "lease-1",
};

describe("protocol frame builders", () => {
  it("builds a registration with explicit web identity and a request correlation", () => {
    const frame = buildRegisterFrame({
      ...receiptIdentity,
      frameId: "register-1",
    });
    expect(frame).toEqual({
      version: "v1",
      frame_id: "register-1",
      type: "register",
      agent_id: "ui-agent",
      instance_id: "instance-1",
      agent_role: "user_interaction",
      client_type: "web",
      ack_confirmation: "correlated_v1",
      conversation_sync: "v1",
      delivery_receipts: "delivery_items_v1",
      inline_tool_control: "v1",
    });
    expect(isClientFrame(frame)).toBe(true);
  });

  it("builds inline tool state and control frames", () => {
    expect(buildInlineToolStateFrame({ ...receiptIdentity, enabled: false })).toMatchObject({ type: "inline_tool_state", agent_id: "ui-agent", enabled: false });
    expect(buildInlineToolControlFrame({ ...receiptIdentity, targetAgentId: "worker-a", targetInstanceId: "worker-instance", controlId: "inline-control", enabled: true })).toEqual({ version: "v1", frame_id: expect.any(String), type: "inline_tool_control", agent_id: "ui-agent", instance_id: "instance-1", lease_id: "lease-1", target_agent_id: "worker-a", target_instance_id: "worker-instance", control_id: "inline-control", enabled: true });
  });

  it("builds controls with target identity and preserves goal set/off semantics", () => {
    expect(buildModelControlFrame({
      ...receiptIdentity,
      targetAgentId: "worker-a",
      targetInstanceId: "worker-instance",
      controlId: "control-model",
      model: { provider: "openai", id: "gpt-5" },
    })).toMatchObject({
      type: "model_control",
      action: "set_model",
      target_agent_id: "worker-a",
      target_instance_id: "worker-instance",
    });
    expect(buildGoalControlFrame({
      ...receiptIdentity,
      targetAgentId: "worker-a",
      targetInstanceId: "worker-instance",
      controlId: "control-goal",
      action: "set",
      goal: "Finish the task",
      settings,
    })).toMatchObject({ type: "goal_control", action: "set", goal: "Finish the task", settings });
    expect(buildGoalControlFrame({
      ...receiptIdentity,
      targetAgentId: "worker-a",
      targetInstanceId: "worker-instance",
      controlId: "control-off",
      action: "off",
      goal: "",
      settings,
    })).toMatchObject({ type: "goal_control", action: "off", goal: "" });
    expect(() => buildGoalControlFrame({
      ...receiptIdentity,
      targetAgentId: "worker-a",
      targetInstanceId: "worker-instance",
      controlId: "control-invalid",
      action: "off",
      goal: "still on",
      settings,
    })).toThrow();
  });

  it("emits one injection and one ack for a batch using the first item's identity", () => {
    const result = buildBatchReceipts({ ...receiptIdentity, deliveryItems: [item, secondItem] });
    expect(result.injectionRequested).toMatchObject({
      type: "injection_requested",
      message_id: "message-1",
      attempt_number: 2,
      delivery_items: [item, secondItem],
    });
    expect(result.ack).toMatchObject({
      type: "ack",
      message_id: "message-1",
      attempt_number: 2,
      delivery_items: [item, secondItem],
    });
    expect(result.nack).toBeUndefined();
  });

  it("builds a single-item ack and a batch nack with the same ordered receipt items", () => {
    expect(buildAckFrame({ ...receiptIdentity, messageId: item.message_id, attemptNumber: item.attempt_number, deliveryItems: [item] })).toMatchObject({
      type: "ack",
      message_id: "message-1",
      attempt_number: 2,
      delivery_items: [item],
    });
    const result = buildBatchReceipts({ ...receiptIdentity, deliveryItems: [item, secondItem], failureReason: "processing_failed" });
    expect(result.nack).toMatchObject({
      type: "nack",
      message_id: "message-1",
      attempt_number: 2,
      reason: "processing_failed",
      delivery_items: [item, secondItem],
    });
    expect(result.injectionRequested).toBeUndefined();
    expect(result.ack).toBeUndefined();
  });

  it("rejects invalid builder input and constructs lock controls", () => {
    expect(() => buildAckFrame({ ...receiptIdentity, messageId: "bad id", attemptNumber: 1 })).toThrow();
    expect(buildReminderLockControlFrame({
      ...receiptIdentity,
      targetAgentId: "worker-a",
      targetInstanceId: "worker-instance",
      controlId: "control-lock",
      reminderId: "reminder-1",
      action: "set",
    })).toMatchObject({ type: "reminder_lock_control", action: "set", reminder_id: "reminder-1" });
    expect(buildNackFrame({ ...receiptIdentity, messageId: item.message_id, attemptNumber: 1, reason: "timeout" })).toMatchObject({ type: "nack", reason: "timeout" });
  });
});
