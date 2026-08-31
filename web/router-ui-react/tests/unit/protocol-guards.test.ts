import { describe, expect, it } from "vitest";
import {
  isEnvelope,
  isGoalControlFrame,
  isModelControlResultFrame,
  isServerFrame,
  parseServerFrame,
} from "../../src/protocol/guards";
import type { Envelope } from "../../src/protocol/types";

const validEnvelope: Envelope = {
  id: "message-1",
  from: "worker-a",
  to: "ui-agent",
  body: "hello",
  created_at: "2026-08-30T12:00:00.000Z",
};

const identity = {
  version: "v1" as const,
  frame_id: "frame-1",
  agent_id: "ui-agent",
  instance_id: "instance-1",
  lease_id: "lease-1",
  target_agent_id: "worker-a",
  target_instance_id: "worker-instance",
  control_id: "control-1",
};

const goalSettings = {
  goal_lock_enabled: true,
  snooze_enabled: true,
  snooze_duration_ms: 1_000,
  snooze_turn_limit: 3,
  snooze_budget_cap_ms: null,
  snooze_refill_yield_ms: 2_000,
  snooze_refill_elapsed_ms: 3_000,
};

const goalState = {
  enabled: true,
  goal: "Finish the task",
  ...goalSettings,
  outcome_counts: { COMPLETED: 1, NOT_DONE: 2 },
};

describe("protocol guards", () => {
  it("accepts a valid envelope and rejects unknown fields", () => {
    expect(isEnvelope(validEnvelope)).toBe(true);
    expect(isEnvelope({ ...validEnvelope, unexpected: true })).toBe(false);
  });

  it("accepts a valid reminders_changed server frame", () => {
    const frame = {
      version: "v1",
      frame_id: "reminders-changed-1",
      type: "reminders_changed",
    };

    expect(isServerFrame(frame)).toBe(true);
    expect(parseServerFrame(frame).ok).toBe(true);
  });

  it("rejects malformed IDs, timestamps, nulls, and unpaired surrogates", () => {
    expect(isEnvelope({ ...validEnvelope, id: "bad id" })).toBe(false);
    expect(isEnvelope({ ...validEnvelope, created_at: "2026-08-30T12:00:00Z" })).toBe(false);
    expect(isEnvelope({ ...validEnvelope, body: "contains\u0000null" })).toBe(false);
    expect(isEnvelope({ ...validEnvelope, body: "lone\ud800" })).toBe(false);
  });

  it("validates goal conditional fields and settings bounds", () => {
    const frame = {
      type: "goal_control",
      ...identity,
      action: "set",
      goal: "Finish the task",
      settings: goalSettings,
    };
    expect(isGoalControlFrame(frame)).toBe(true);
    expect(isGoalControlFrame({ ...frame, action: "off", goal: "still enabled" })).toBe(false);
    expect(isGoalControlFrame({ ...frame, settings: { ...goalSettings, snooze_turn_limit: 21 } })).toBe(false);
    expect(isServerFrame({
      version: "v1",
      frame_id: "goal-result",
      type: "goal_control_result",
      agent_id: "worker-a",
      instance_id: "worker-instance",
      lease_id: "lease-1",
      source_agent_id: "ui-agent",
      control_id: "control-1",
      revision: 4,
      accepted: false,
      error_code: "unavailable",
      goal_state: { ...goalState, enabled: false, goal: "" },
    })).toBe(true);
  });

  it("requires the schema's accepted/rejected result fields", () => {
    const base = {
      version: "v1",
      frame_id: "result-1",
      type: "model_control_result",
      agent_id: "worker-a",
      instance_id: "worker-instance",
      lease_id: "lease-1",
      source_agent_id: "ui-agent",
      control_id: "control-1",
    };
    expect(isModelControlResultFrame({
      ...base,
      accepted: true,
      current_model: { provider: "openai", id: "gpt-5" },
    })).toBe(true);
    expect(isModelControlResultFrame({ ...base, accepted: true })).toBe(false);
    expect(isModelControlResultFrame({
      ...base,
      accepted: false,
      current_model: { provider: "openai", id: "gpt-5" },
    })).toBe(false);
    expect(isModelControlResultFrame({
      ...base,
      accepted: false,
      error_code: "unsupported_model",
      current_model: { provider: "openai", id: "gpt-5" },
    })).toBe(true);
  });

  it("validates a batched message and rejects unpaired delivery arrays", () => {
    const message = {
      id: "message-1",
      from: "worker-a",
      to: "ui-agent",
      body: "hello",
      created_at: "2026-08-30T12:00:00.000Z",
    };
    const item = {
      message_id: "message-1",
      message_incarnation_id: "123e4567-e89b-42d3-a456-426614174000",
      attempt_number: 1,
      delivery_expires_at: "2026-08-30T12:01:00.000Z",
    };
    const batch = {
      version: "v1",
      frame_id: "delivery-1",
      type: "message",
      message,
      message_incarnation_id: item.message_incarnation_id,
      attempt_number: 1,
      delivery_expires_at: item.delivery_expires_at,
      delivery_items: [item, { ...item, message_id: "message-2" }],
      delivery_messages: [
        message,
        { ...message, id: "message-2" },
      ],
    };
    expect(parseServerFrame(batch).ok).toBe(true);
    const invalidBatch = parseServerFrame({ ...batch, delivery_messages: undefined });
    expect(invalidBatch.ok).toBe(false);
    if (!invalidBatch.ok) expect(invalidBatch.error.code).toBe("invalid_protocol_value");
  });
});
