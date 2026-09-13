#!/usr/bin/env node
import process from "node:process";
import { Buffer } from "node:buffer";
import { setImmediate } from "node:timers";
import { existsSync, watch } from "node:fs";
import { dirname, join } from "node:path";
import { createInterface } from "node:readline";

const args = process.argv.slice(2);
const sessionIndex = args.indexOf("--session-id");
const sessionId = sessionIndex >= 0 ? args[sessionIndex + 1] : "functional-session";
const sessionRoot = process.env.MECHA_MANAGED_SESSION_ROOT ?? process.cwd();
const hiddenCommands = [
  "mecha-bootstrap-v1", "mecha-install-tools-v1", "mecha-control-result-v1",
  "mecha-mailbox-deliver-v1", "mecha-policy-apply-v1", "mecha-child-snapshot-v1",
  "mecha-post-compaction-notification-v1",
];
const commands = [...hiddenCommands, "goal", "goal-lock", "goal-snooze", "goal-snooze-turn-limit", "goal-snooze-budget", "goal-snooze-budget-refill", "mecha-parent-message", "inline-tools", "mecha-reply"];
const activeTools = [
  "read", "bash", "edit", "write", "subagent", "get_subagent_result", "steer_subagent",
  "goal", "goal_snooze", "mecha_context", "mecha_git", "mecha_mailbox_send", "mecha_parent_message",
  "mecha_pr", "mecha_priority", "mecha_recall", "mecha_reminder", "mecha_request_inline_tool_allowance",
  "mecha_router", "mecha_settled_notifications", "mecha_state", "mecha_verification_handoff", "mecha_worker",
  "web_search__4f4ca2a3ae8ea237531b8c5264d53d15474c0848fc8d7eeada0fef5b85b139ac",
].sort();

function emit(value) { process.stdout.write(`${JSON.stringify(value)}\n`); }
function decodeMessage(message) {
  const encoded = message.slice(message.indexOf(" ") + 1);
  const json = Buffer.from(encoded, "base64url").toString("utf8");
  return JSON.parse(json);
}
function response(request, data = {}) { emit({ id: request.id, type: "response", data }); }
function privateReceipt(customType, details, id = undefined) {
  emit({ type: "message_end", ...(id === undefined ? {} : { message: { id, customType, details } }), ...(id === undefined ? { customType, details } : {}) });
}
function policyApplied(envelope) {
  const policy = envelope.policy ?? {};
  return {
    protocol: "mecha.policy/v1",
    runtime_generation: envelope.runtime_generation,
    session_epoch: envelope.session_epoch,
    applied: {
      inline_enabled: policy.inline?.enabled === true,
      inline_credits: policy.inline?.credits ?? {},
      recall_content: policy.recall?.content ?? "",
      settled_enabled: policy.settled?.enabled === true,
      goal_lock_enabled: policy.goal_lock?.enabled === true,
      compaction_deferred: policy.compaction?.deferred === true,
    },
  };
}
let childSequence = 0;
let childRun = {
  task_id: "functional-child-task", run_id: "functional-child-run", parent_entry_id: "functional-parent-entry",
  description: "Functional child", model: { provider: "openai-codex", id: "gpt-5.6-terra", name: "" },
  lifecycle_state: "queued", started_at: "2030-01-01T00:00:00.000Z", finished_at: null, duration_ms: null,
  compaction: { state: "idle", count: 0, started_at: null, last_outcome: null }, sequence: 0,
};
let omitChild = false;

function childSnapshot() {
  const runs = omitChild || childRun === undefined ? [] : [childRun];
  return {
    protocol: "mecha.children/v1",
    snapshot_id: `functional-snapshot-${sessionId}-${childSequence}`,
    owner_session_id: sessionId,
    sequence: omitChild ? childSequence + 2 : childSequence,
    runs,
  };
}

async function childDelta(sequence, changes) {
  childSequence = sequence;
  childRun = {
    task_id: "functional-child-task",
    run_id: "functional-child-run",
    parent_entry_id: "functional-parent-entry",
    description: "Functional child",
    model: { provider: "openai-codex", id: "gpt-5.6-terra", name: "" },
    lifecycle_state: "running",
    started_at: "2030-01-01T00:00:00.000Z",
    finished_at: null,
    duration_ms: null,
    compaction: { state: "idle", count: 0, started_at: null, last_outcome: null },
    sequence,
    ...changes,
  };
  const details = {
    protocol: "mecha.children/v1",
    owner_session_id: sessionId,
    sequence,
    task_id: "functional-child-task",
    run_id: "functional-child-run",
    parent_entry_id: "functional-parent-entry",
    changes,
  };
  emitChildDeltaReceipt("mecha.child.delta.v1", "Managed child lifecycle updated.", `functional-child-delta-${sequence}`, details);
}

function emitParentCompactionLifecycle() {
  emit({ type: "compaction_start" });
  emit({ type: "compaction_end", result: { summary: "deterministic parent compaction" } });
}

function emitOmittedChildGap() {
  childSequence = 6;
  emitChildDeltaReceipt("mecha.child.delta.v1", "Managed child lifecycle updated.", "functional-child-delta-gap", {
    protocol: "mecha.children/v1",
    owner_session_id: "stale-session",
    sequence: 6,
    task_id: "functional-child-task",
    run_id: "functional-child-run",
    parent_entry_id: "functional-parent-entry",
    changes: { lifecycle_state: "completed" },
  });
}

function emitChildTransitionCommand(content) {
  switch (content) {
    case "Start structured child":
      return childDelta(1, {
        description: "Functional child",
        model: { provider: "openai-codex", id: "gpt-5.6-terra", name: "" },
        lifecycle_state: "running",
        started_at: "2030-01-01T00:00:00.000Z",
        finished_at: null,
        duration_ms: null,
        compaction: { state: "idle", count: 0, started_at: null, last_outcome: null },
      });
    case "Start child compaction":
      return childDelta(2, { compaction: { state: "compacting", count: 1, started_at: "2030-01-01T00:00:01.000Z", last_outcome: null } });
    case "Finish child compaction":
      return childDelta(3, { compaction: { state: "idle", count: 1, started_at: null, last_outcome: "completed" } });
    case "Finish structured child":
      return childDelta(4, { lifecycle_state: "completed", finished_at: "2030-01-01T00:00:04.000Z", duration_ms: 4000 });
    default:
      return undefined;
  }
}

function emitChildDeltaReceipt(customType, content, id, details) {
  emit({ type: "message_end", message: { id, customType, display: false, content, details } });
}
function waitForMarker(path) {
  if (existsSync(path)) return Promise.resolve();
  return new Promise((resolve) => {
    const watcher = watch(dirname(path), (_event, changed) => {
      if (changed === path.split("/").pop() && existsSync(path)) {
        watcher.close();
        resolve();
      }
    });
  });
}
async function emitDirectLifecycle(envelope) {
  const messageId = envelope.message_id;
  const appendId = `functional-append-${messageId}`;
  privateReceipt("mecha.mailbox.appended.v1", { protocol: "mecha.mailbox/v1", message_id: messageId, runtime_generation: envelope.runtime_generation, session_epoch: envelope.session_epoch }, appendId);
  emit({ type: "tool_execution_start", toolCallId: `functional-tool-${messageId}`, toolName: "bash" });
  await waitForMarker(join(sessionRoot, "allow-tool-end"));
  emit({ type: "tool_execution_end", toolCallId: `functional-tool-${messageId}`, toolName: "bash", isError: false });
  emit({ type: "agent_start" });
  emit({ type: "message_end", message: { id: `functional-reply-${messageId}`, role: "assistant", content: [{ type: "text", text: "Deterministic managed reply" }] } });
  emit({ type: "agent_settled" });
}

const input = createInterface({ input: process.stdin });
input.on("line", (line) => {
  let request;
  try { request = JSON.parse(line); } catch { return; }
  if (request.type === "get_state") { response(request, { sessionId, isCompacting: false }); return; }
  if (request.type === "get_commands") { response(request, { commands }); return; }
  if (request.type === "get_active_tools") { response(request, { active_tools: activeTools }); return; }
  if (request.type === "get_entries") { response(request, { entries: [], leafId: `functional-cursor-${sessionId}` }); return; }
  if (request.type === "abort") {
    const success = !existsSync(join(sessionRoot, "fail-abort"));
    response(request, { command: "abort", success });
    if (success) setImmediate(() => { input.close(); process.exit(0); });
    return;
  }
  if (request.type !== "prompt") { response(request, { command: request.type, success: true }); return; }

  const message = typeof request.message === "string" ? request.message : "";
  response(request, { command: "prompt", success: true });
  if (!message.startsWith("/")) return;
  const command = message.slice(1).split(" ", 1)[0];
  let envelope;
  try { envelope = decodeMessage(message); } catch { return; }
  if (command === "mecha-bootstrap-v1") {
    if (existsSync(join(sessionRoot, "fail-ready"))) {
      privateReceipt("mecha.tools.accepted.v1", { runtime_generation: "wrong-generation", session_epoch: envelope.session_epoch, manifest_revision: 2, manifest_sha256: envelope.manifest_sha256, active_tools: activeTools });
      return;
    }
    privateReceipt("mecha.tools.accepted.v1", { runtime_generation: envelope.runtime_generation, session_epoch: envelope.session_epoch, manifest_revision: 2, manifest_sha256: envelope.manifest_sha256, active_tools: activeTools });
  } else if (command === "mecha-policy-apply-v1") {
    privateReceipt("mecha.policy.applied.v1", policyApplied(envelope));
  } else if (command === "mecha-child-snapshot-v1") {
    if (existsSync(join(sessionRoot, "omit-child"))) omitChild = true;
    privateReceipt("mecha.child.snapshot.v1", childSnapshot());
  } else if (command === "mecha-mailbox-deliver-v1") {
    const childCommand = ["Start structured child", "Start child compaction", "Finish child compaction", "Finish structured child"].includes(envelope.content);
    if (childCommand) {
      // Each managed prompt commands exactly one source transition. The direct
      // receipt and settlement follow the command on this same Pi stdout.
      privateReceipt("mecha.mailbox.appended.v1", { protocol: "mecha.mailbox/v1", message_id: envelope.message_id, runtime_generation: envelope.runtime_generation, session_epoch: envelope.session_epoch }, `functional-append-${envelope.message_id}`);
      void emitChildTransitionCommand(envelope.content).then(() => emit({ type: "agent_settled" }));
    } else if (envelope.content === "Run parent compaction") {
      privateReceipt("mecha.mailbox.appended.v1", { protocol: "mecha.mailbox/v1", message_id: envelope.message_id, runtime_generation: envelope.runtime_generation, session_epoch: envelope.session_epoch }, `functional-append-${envelope.message_id}`);
      emitParentCompactionLifecycle();
      emit({ type: "agent_settled" });
    } else if (envelope.content === "Reconcile omitted child") {
      emitOmittedChildGap();
      emitDirectLifecycle(envelope);
    } else {
      emitDirectLifecycle(envelope);
    }
  }
});
process.on("SIGTERM", () => {
  if (existsSync(join(sessionRoot, "fail-abort"))) return;
  input.close();
  process.exit(0);
});
process.on("SIGINT", () => {
  if (existsSync(join(sessionRoot, "fail-abort"))) return;
  input.close();
  process.exit(0);
});
