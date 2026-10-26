import { describe, expect, it } from "vitest";
import {
  isConversationMessage,
  isEnvelope,
  isGoalControlFrame,
  isInlineToolControlFrame,
  isInlineToolControlResultFrame,
  isInlineToolStateFrame,
  isManagedAgent,
  isManagedChildrenProjection,
  isManagedCompleteHistoryEntry,
  isManagedHistoryEntry,
  isUILiveFrame,
  isRouterUtcTimestamp,
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
    expect(isEnvelope({ ...validEnvelope, created_at: "2026-08-30T12:00:00.123456Z" })).toBe(false);
    expect(isEnvelope({ ...validEnvelope, body: "contains\u0000null" })).toBe(false);
    expect(isEnvelope({ ...validEnvelope, body: "lone\ud800" })).toBe(false);
  });

  it("accepts canonical router nanosecond timestamps only on the managed surface", () => {
    const managedAgent = {
      agent_id: "managed-alpha", source: "router-managed", cwd: "/tmp/project", model: { provider: "openai-codex", id: "gpt-5.6-terra" },
      desired_state: "running", lifecycle_state: "running", activity_state: "idle", accepting_prompts: true,
      manifest: { revision: 1, sha256: "a".repeat(64), installed_tool_count: 16 },
      created_at: "2026-09-09T09:48:28.414204Z", started_at: "2026-09-09T09:48:29.414204003Z", updated_at: "2026-09-09T09:48:30Z", last_error: null,
    };
    expect(isRouterUtcTimestamp("2026-09-09T09:48:28.414204003Z")).toBe(true);
    expect(isRouterUtcTimestamp("2026-09-09T09:48:28Z")).toBe(true);
    expect(isRouterUtcTimestamp("2026-02-29T09:48:28.414204003Z")).toBe(false);
    expect(isRouterUtcTimestamp("2026-09-09T09:48:28.414204003+00:00")).toBe(false);
    expect(isRouterUtcTimestamp("2026-09-09T09:48:28.4142040030Z")).toBe(false);
    expect(isManagedAgent(managedAgent)).toBe(true);
    expect(isManagedAgent({ ...managedAgent, updated_at: "2026-09-09T09:48:28.4142040030Z" })).toBe(false);
    expect(isManagedAgent({ ...managedAgent, active_workflow_state_id: "" })).toBe(true);
    expect(isManagedAgent({ ...managedAgent, active_workflow_state_id: "state-uuid-123" })).toBe(true);
    expect(isManagedAgent({ ...managedAgent, active_workflow_state_id: null })).toBe(true);
    expect(isManagedAgent({ ...managedAgent, active_workflow_state_id: 123 })).toBe(false);
    expect(isManagedAgent({ ...managedAgent, active_workflow_state_id: "a".repeat(129) })).toBe(false);
    expect(isManagedAgent({ ...managedAgent, worktree: true, worktree_base_path: "/base/repo" })).toBe(true);
    expect(isManagedAgent({ ...managedAgent, worktree_base_path: 123 })).toBe(false);
    expect(isManagedAgent({ ...managedAgent, runtime: "embedded", active_workflow_state_id: "" })).toBe(true);
    expect(isManagedAgent({ ...managedAgent, runtime: "embedded", context_usage: { tokens: null, context_window: 1048576, percent: null } })).toBe(true);
    expect(isManagedAgent({ ...managedAgent, runtime: "embedded", context_usage: { tokens: 50000, context_window: 1048576, percent: 4.8 } })).toBe(true);
    expect(isManagedAgent({ ...managedAgent, runtime: "embedded", context_usage: { tokens: "invalid", context_window: 1048576, percent: null } })).toBe(false);
  });

  it("validates managed agent with open-schema, accepting active_workflow_id, workflow_variables, and unknown forward-compatible fields", () => {
    const managedAgent = {
      agent_id: "managed-alpha", source: "router-managed", cwd: "/tmp/project", model: { provider: "openai-codex", id: "gpt-5.6-terra" },
      desired_state: "running", lifecycle_state: "running", activity_state: "idle", accepting_prompts: true,
      manifest: { revision: 1, sha256: "a".repeat(64), installed_tool_count: 16 },
      created_at: "2026-09-09T09:48:28.414204Z", started_at: "2026-09-09T09:48:29.414204003Z", updated_at: "2026-09-09T09:48:30Z", last_error: null,
    };

    // active_workflow_id validation (string | null | undefined)
    expect(isManagedAgent({ ...managedAgent, active_workflow_id: "wf-dev-loop" })).toBe(true);
    expect(isManagedAgent({ ...managedAgent, active_workflow_id: "" })).toBe(true);
    expect(isManagedAgent({ ...managedAgent, active_workflow_id: null })).toBe(true);
    expect(isManagedAgent({ ...managedAgent, active_workflow_id: undefined })).toBe(true);
    expect(isManagedAgent({ ...managedAgent, active_workflow_id: 123 })).toBe(false);
    expect(isManagedAgent({ ...managedAgent, active_workflow_id: "a".repeat(129) })).toBe(false);

    // workflow_variables validation (record of string to string | null | undefined)
    expect(isManagedAgent({ ...managedAgent, workflow_variables: { TASK: "fix-bug", ENV: "staging" } })).toBe(true);
    expect(isManagedAgent({ ...managedAgent, workflow_variables: {} })).toBe(true);
    expect(isManagedAgent({ ...managedAgent, workflow_variables: null })).toBe(true);
    expect(isManagedAgent({ ...managedAgent, workflow_variables: undefined })).toBe(true);
    expect(isManagedAgent({ ...managedAgent, workflow_variables: "not-a-record" })).toBe(false);
    expect(isManagedAgent({ ...managedAgent, workflow_variables: { INVALID: 123 } })).toBe(false);

    // workflow_name validation (safe string, null/undefined acceptance, rejection of invalid types/length > 256)
    expect(isManagedAgent({ ...managedAgent, workflow_name: "Feature Development" })).toBe(true);
    expect(isManagedAgent({ ...managedAgent, workflow_name: "" })).toBe(true);
    expect(isManagedAgent({ ...managedAgent, workflow_name: null })).toBe(true);
    expect(isManagedAgent({ ...managedAgent, workflow_name: undefined })).toBe(true);
    expect(isManagedAgent({ ...managedAgent, workflow_name: 123 })).toBe(false);
    expect(isManagedAgent({ ...managedAgent, workflow_name: "a".repeat(257) })).toBe(false);

    // workflow_state validation (safe string, null/undefined acceptance, rejection of invalid types/length > 256)
    expect(isManagedAgent({ ...managedAgent, workflow_state: "Implementation" })).toBe(true);
    expect(isManagedAgent({ ...managedAgent, workflow_state: "" })).toBe(true);
    expect(isManagedAgent({ ...managedAgent, workflow_state: null })).toBe(true);
    expect(isManagedAgent({ ...managedAgent, workflow_state: undefined })).toBe(true);
    expect(isManagedAgent({ ...managedAgent, workflow_state: 123 })).toBe(false);
    expect(isManagedAgent({ ...managedAgent, workflow_state: "a".repeat(257) })).toBe(false);

    // both workflow fields together
    expect(isManagedAgent({
      ...managedAgent,
      active_workflow_id: "wf-123",
      active_workflow_state_id: "state-456",
      workflow_variables: { FOO: "bar" },
    })).toBe(true);

    // open-schema forward compatibility with unknown/new fields
    expect(isManagedAgent({ ...managedAgent, future_backend_feature: true })).toBe(true);
    expect(isManagedAgent({ ...managedAgent, custom_tags: ["alpha", "beta"], extra_config: { nested: 1 } })).toBe(true);

    // privateRuntimeFields are strictly rejected
    for (const privateField of ["pid", "process_pid", "session_id", "pi_session_id", "process_incarnation", "runtime_generation", "generation", "managedProjectionVersion"]) {
      expect(isManagedAgent({ ...managedAgent, [privateField]: "secret" })).toBe(false);
    }

    // archived records are rejected
    expect(isManagedAgent({ ...managedAgent, record_kind: "archived" })).toBe(false);

    // missing required fields are rejected
    const { agent_id, ...missingAgentId } = managedAgent;
    expect(isManagedAgent(missingAgentId)).toBe(false);
    const { manifest, ...missingManifest } = managedAgent;
    expect(isManagedAgent(missingManifest)).toBe(false);
  });

  it("accepts bounded Complete history details while keeping presentation metadata closed", () => {
    const entry = { id: "complete:1", agent_id: "managed-alpha", at: "2026-09-09T09:48:28Z", level: "info", message: "Ran bash", presentation: { kind: "tool", tool_name: "functions.bash", outcome: "success", summary: "Completed successfully" } };
    expect(isManagedHistoryEntry(entry)).toBe(true);
    for (const outcome of ["active", "success", "warning", "error", "unknown", "compacting", "completed", "failed", "interrupted", "aborted", "reconciled_unknown"]) {
      expect(isManagedHistoryEntry({ ...entry, presentation: { ...entry.presentation, outcome } })).toBe(true);
    }
    expect(isManagedHistoryEntry({ ...entry, presentation: { ...entry.presentation, outcome: "pending" } })).toBe(false);
    expect(isManagedHistoryEntry({ ...entry, presentation: { ...entry.presentation, summary: "stdout: private output" } })).toBe(false);
    expect(isManagedHistoryEntry({ ...entry, presentation: { ...entry.presentation, tool_name: "bash --private-argument" } })).toBe(false);
    expect(isManagedCompleteHistoryEntry({ ...entry, message: "token=private" })).toBe(false);
    expect(isManagedHistoryEntry({ ...entry, message: "token=ordinary-message-content" })).toBe(true);

    expect(isManagedCompleteHistoryEntry({
      ...entry,
      message: "Message received",
      message_content: "token=the exact inbound body is allowed here",
      sender_identity: "discord:operations-user",
      tool_input: { path: "/workspace/report.md", offset: 2, nested: [true, null, "[REDACTED]"] },
    })).toBe(true);
    expect(isManagedHistoryEntry({ ...entry, sender_identity: "x".repeat(129) })).toBe(false);
    expect(isManagedHistoryEntry({ ...entry, tool_input: { invalid: Number.POSITIVE_INFINITY } })).toBe(false);
    expect(isManagedHistoryEntry({ ...entry, tool_input: { invalid: () => undefined } })).toBe(false);
    expect(isManagedHistoryEntry({ ...entry, message_content: "body", unexpected_detail: true })).toBe(false);
  });

  it("accepts the new lifecycle frames and the bounded legacy record key", () => {
    const managedAgent = {
      agent_id: "managed-alpha", source: "router-managed", cwd: "/tmp/project", model: { provider: "openai-codex", id: "gpt-5.6-terra" },
      desired_state: "running", lifecycle_state: "running", activity_state: "idle", accepting_prompts: true,
      manifest: { revision: 1, sha256: "a".repeat(64), installed_tool_count: 16 },
      created_at: "2026-09-09T09:48:28Z", started_at: null, updated_at: "2026-09-09T09:48:30Z", last_error: null,
    };
    const delta = { type: "managed_agent_delta", protocol: "mecha.web/v1", projection_version: 2, base_version: 1, agent_id: "managed-alpha", changes: managedAgent };
    const removed = { type: "managed_agent_removed", protocol: "mecha.web/v1", projection_version: 2, base_version: 1, agent_id: "managed-alpha" };
    expect(isUILiveFrame(delta)).toBe(true);
    expect(isUILiveFrame(removed)).toBe(true);
    expect(isUILiveFrame({ ...delta, record_key: "live:managed-alpha" })).toBe(true);
    expect(isUILiveFrame({ ...removed, record_key: "live:managed-alpha" })).toBe(true);
    expect(isUILiveFrame({ ...delta, record_key: "" })).toBe(false);
    expect(isUILiveFrame({ ...removed, record_key: 42 })).toBe(false);
    expect(isUILiveFrame({ ...delta, undocumented: true })).toBe(false);
    expect(isUILiveFrame({ ...removed, undocumented: true })).toBe(false);
    expect(isUILiveFrame({ ...delta, changes: { ...managedAgent, record_kind: "archived" } })).toBe(false);
    for (const privateField of ["pid", "process_pid", "session_id", "pi_session_id", "process_incarnation", "runtime_generation", "generation", "managedProjectionVersion"]) {
      expect(isUILiveFrame({ ...delta, changes: { ...managedAgent, [privateField]: "private-runtime-identity" } })).toBe(false);
    }
    expect(isUILiveFrame({ ...delta, projection_version: -1 })).toBe(false);
    expect(isUILiveFrame({ ...delta, agent_id: "bad id" })).toBe(false);
  });

  it("accepts only bounded provider-auth live frames", () => {
    const prompt = { type: "provider_auth_prompt", protocol: "mecha.web/v1", projection_version: 1, operation_id: "auth-1", provider: "google", prompt: { id: "prompt-1", kind: "secret", message: "Enter the requested sign-in value.", url: "https://accounts.google.com/device" } };
    expect(isUILiveFrame(prompt)).toBe(true);
    expect(isUILiveFrame({ ...prompt, prompt: { ...prompt.prompt, url: "http://accounts.google.com/device" } })).toBe(false);
    expect(isUILiveFrame({ ...prompt, prompt: { ...prompt.prompt, kind: "oauth_callback" } })).toBe(false);
    const state = { type: "provider_auth_state", protocol: "mecha.web/v1", projection_version: 1, operation_id: "auth-1", provider: "google", status: "pending" };
    expect(isUILiveFrame(state)).toBe(true);
    expect(isUILiveFrame({ ...state, status: "provider-sdk-detail" })).toBe(false);
  });

  it("accepts provider_auth frames for google-vertex and antigravity and rejects unsupported providers", () => {
    const vertexPrompt = {
      type: "provider_auth_prompt",
      protocol: "mecha.web/v1",
      projection_version: 1,
      operation_id: "op-vertex-1",
      provider: "google-vertex",
      prompt: { id: "project-1", kind: "text", message: "Google Cloud Project ID", placeholder: "my-project" },
    };
    expect(isUILiveFrame(vertexPrompt)).toBe(true);

    const antigravityPrompt = {
      type: "provider_auth_prompt",
      protocol: "mecha.web/v1",
      projection_version: 1,
      operation_id: "op-ag-1",
      provider: "antigravity",
      prompt: { id: "ag-code-1", kind: "text", message: "Paste callback URL", url: "https://accounts.google.com/o/oauth2/v2/auth?client_id=test" },
    };
    expect(isUILiveFrame(antigravityPrompt)).toBe(true);

    const vertexState = {
      type: "provider_auth_state",
      protocol: "mecha.web/v1",
      projection_version: 1,
      operation_id: "op-vertex-1",
      provider: "google-vertex",
      status: "succeeded",
    };
    expect(isUILiveFrame(vertexState)).toBe(true);

    const antigravityState = {
      type: "provider_auth_state",
      protocol: "mecha.web/v1",
      projection_version: 1,
      operation_id: "op-ag-1",
      provider: "antigravity",
      status: "pending",
    };
    expect(isUILiveFrame(antigravityState)).toBe(true);

    const unsupportedPrompt = {
      ...vertexPrompt,
      provider: "aws-bedrock",
    };
    expect(isUILiveFrame(unsupportedPrompt)).toBe(false);

    const unsupportedState = {
      ...vertexState,
      provider: "aws-bedrock",
    };
    expect(isUILiveFrame(unsupportedState)).toBe(false);
  });

  it("requires a closed per-child recovery object", () => {
    const child = {
      id: "child-1", parent_entry_id: "entry-1", task_id: "task-1", run_id: "run-1", sequence: 1,
      recovery: { state: "interrupted_unknown", cause: "snapshot_omitted", observed_at: "2026-09-09T09:48:28Z" }, status: "running",
      compaction_state: "unknown", compaction: { state: "idle", count: 0, last_outcome: "reconciled_unknown" },
    };
    const frame = { type: "managed_children_snapshot", protocol: "mecha.web/v1", lifecycle_version: 1, generated_at: "2026-09-09T09:48:28Z", children: [{ agent_id: "managed-alpha", source: "router-managed", lifecycle_version: 1, recovery_state: "current", children: [child] }] };
    expect(isUILiveFrame(frame)).toBe(true);
    expect(isUILiveFrame({ ...frame, children: [{ ...frame.children[0], children: [{ ...child, recovery: { state: "confirmed", cause: "session_changed", observed_at: "2026-09-09T09:48:28Z" } }] }] })).toBe(false);
    expect(isUILiveFrame({ ...frame, children: [{ ...frame.children[0], children: [{ ...child, recovery: { state: "interrupted_unknown", cause: "old_session", observed_at: "2026-09-09T09:48:28Z" } }] }] })).toBe(false);
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

  it("validates inline tool state and control results", () => {
    const state = { version: "v1", frame_id: "inline-state", type: "inline_tool_state", agent_id: "worker-a", instance_id: "worker-instance", lease_id: "lease-1", enabled: false };
    expect(isInlineToolStateFrame(state)).toBe(true);
    expect(isInlineToolControlFrame({ ...identity, type: "inline_tool_control", enabled: true })).toBe(true);
    expect(isInlineToolControlResultFrame({ version: "v1", frame_id: "inline-result", type: "inline_tool_control_result", agent_id: "worker-a", instance_id: "worker-instance", lease_id: "lease-1", source_agent_id: "ui-agent", control_id: "control-1", accepted: true, enabled: true })).toBe(true);
    expect(isInlineToolControlResultFrame({ version: "v1", frame_id: "inline-result", type: "inline_tool_control_result", agent_id: "worker-a", instance_id: "worker-instance", lease_id: "lease-1", source_agent_id: "ui-agent", control_id: "control-1", accepted: false, enabled: false })).toBe(false);
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

  describe("isConversationMessage", () => {
    it("accepts a standard conversation message", () => {
      const msg = {
        sequence: 1,
        id: "msg-1",
        message_incarnation_id: "7d1c2651-d156-428e-970b-6fdda07862ea",
        from: "wolfgang",
        to: "workflows",
        body: "Hello world",
        created_at: "2026-10-07T13:28:29.155Z",
        recorded_at: "2026-10-07T13:28:29.165Z",
        attachments: [],
        source: { kind: "web_ui" },
      };
      expect(isConversationMessage(msg)).toBe(true);
    });

    it("accepts messages with Postel resilience: extra fields, unpadded timestamps, sender/recipient alias", () => {
      // Unpadded RFC3339 timestamp without millis (as returned by Go router when nanos == 0)
      const unpaddedTimestampMsg = {
        sequence: 13140,
        id: "embedded-reply-7f119118",
        message_incarnation_id: "6204312b-77ef-4763-adf7-a7d9b3c69dfa",
        from: "workflows",
        to: "wolfgang",
        body: "3 * 3 is 9.",
        created_at: "2026-10-08T16:34:00Z",
        recorded_at: "2026-10-08T16:34:00Z",
        attachments: [],
        source: { kind: "agent" },
        unexpected_extra_field: "tolerated",
      };
      expect(isConversationMessage(unpaddedTimestampMsg)).toBe(true);

      // Sender and recipient field aliases with minimal fields
      const aliasMsg = {
        id: "msg-2",
        sender: "workflows",
        recipient: "wolfgang",
        body: "test body",
        extra_key: 123,
      };
      expect(isConversationMessage(aliasMsg)).toBe(true);
    });

    it("accepts managed children projections with Postel resilience (extra fields on projection, child, recovery, compaction, model)", () => {
      const projectionWithExtraFields = {
        agent_id: "kb-helper",
        source: "router-managed",
        lifecycle_version: 1,
        recovery_state: "current",
        children: [
          {
            id: "subagent-1",
            parent_entry_id: "entry-1",
            task_id: "task-1",
            run_id: "run-1",
            sequence: 0,
            recovery: {
              state: "confirmed",
              cause: null,
              observed_at: "2026-08-30T12:00:00.000Z",
              extra_recovery_field: "allowed",
            },
            description: "Research kb items",
            model: {
              provider: "openai-codex",
              id: "gpt-5.6-terra",
              extra_model_field: "allowed",
            },
            status: "running",
            compaction: {
              state: "idle",
              count: 0,
              extra_compaction_field: true,
            },
            compaction_state: "none",
            extra_child_field: "allowed",
          },
        ],
        extra_projection_field: "allowed",
      };
      expect(isManagedChildrenProjection(projectionWithExtraFields)).toBe(true);
    });

    it("rejects messages missing required fields (id, sender, recipient, body)", () => {
      expect(isConversationMessage(null)).toBe(false);
      expect(isConversationMessage({})).toBe(false);
      expect(isConversationMessage({ id: "", from: "a", to: "b", body: "c" })).toBe(false);
      expect(isConversationMessage({ id: "1", from: "", to: "b", body: "c" })).toBe(false);
      expect(isConversationMessage({ id: "1", from: "a", to: "", body: "c" })).toBe(false);
      expect(isConversationMessage({ id: "1", from: "a", to: "b" })).toBe(false); // missing body
      expect(isConversationMessage({ id: "1", from: "a", to: "b", body: 123 })).toBe(false); // non-string body
      expect(isConversationMessage({ id: "1", body: "c" })).toBe(false); // missing sender & recipient
    });
  });
});
