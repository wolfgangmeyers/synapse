#!/usr/bin/env node
import process from "node:process";
import { Buffer } from "node:buffer";
import { setImmediate } from "node:timers";
import { existsSync, readFileSync, watch, appendFileSync } from "node:fs";
import { createHash } from "node:crypto";
import { dirname, join } from "node:path";
import { createInterface } from "node:readline";

const args = process.argv.slice(2);
let sessionId = "functional-session";
const sessionIndex = args.indexOf("--session-id");
if (sessionIndex >= 0) {
  sessionId = args[sessionIndex + 1];
} else {
  const sessionFlagIndex = args.indexOf("--session");
  if (sessionFlagIndex >= 0 && existsSync(args[sessionFlagIndex + 1])) {
    try {
      const firstLine = readFileSync(args[sessionFlagIndex + 1], "utf8").split("\n")[0];
      const header = JSON.parse(firstLine);
      if (header.id) sessionId = header.id;
    } catch {}
  }
}
const sessionRoot = process.env.MECHA_MANAGED_SESSION_ROOT ?? process.cwd();
const fixtureMarker = join(sessionRoot, "root-control-fixture");
if (existsSync(fixtureMarker)) {
  process.env.MECHA_TEST_ROOT_CONTROL = dirname(dirname(sessionRoot));
  process.env.MECHA_TEST_ROOT_MANIFEST = join(readFileSync(fixtureMarker, "utf8"), "router/contract/managed-tools-v1.json");
}
const hiddenCommands = [
  "mecha-bootstrap-v1", "mecha-install-tools-v1", "mecha-control-result-v1",
  "mecha-mailbox-deliver-v1", "mecha-policy-apply-v1", "mecha-child-snapshot-v1",
  "mecha-post-compaction-notification-v1",
];
if (process.env.MECHA_TEST_ROOT_CONTROL) hiddenCommands.push("mecha-control-result-reconcile-v1");
const commands = [...hiddenCommands, "goal", "goal-lock", "goal-snooze", "goal-snooze-turn-limit", "goal-snooze-budget", "goal-snooze-budget-refill", "mecha-parent-message", "inline-tools", "mecha-reply"];
let activeTools = [
  "read", "bash", "edit", "write", "subagent", "get_subagent_result", "steer_subagent", "list_subagents", "stop_subagent",
  "goal", "goal_snooze", "mecha_context", "mecha_git", "mecha_mailbox_send", "mecha_parent_message",
  "mecha_pr", "mecha_priority", "mecha_recall", "mecha_reminder", "mecha_request_inline_tool_allowance",
  "mecha_router", "mecha_settled_notifications", "mecha_state", "mecha_verification_handoff", "mecha_worker",
  "web_search__4f4ca2a3ae8ea237531b8c5264d53d15474c0848fc8d7eeada0fef5b85b139ac",
].sort();

if (process.env.MECHA_TEST_ROOT_CONTROL) {
  activeTools = [...["read", "bash", "edit", "write", "subagent", "get_subagent_result", "steer_subagent", "list_subagents", "stop_subagent"], ...JSON.parse(readFileSync(process.env.MECHA_TEST_ROOT_MANIFEST, "utf8")).tools.map(tool => tool.name)].sort();
}

function emit(value) { process.stdout.write(`${JSON.stringify(value)}\n`); }
function decodeMessage(message) {
  const encoded = message.slice(message.indexOf(" ") + 1);
  const json = Buffer.from(encoded, "base64url").toString("utf8");
  return JSON.parse(json);
}
function response(request, data = {}) { emit({ id: request.id, type: "response", command: request.type, success: true, data }); }
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
  emit({ type: "compaction_start", reason: "manual" });
  emit({
    type: "compaction_end",
    reason: "manual",
    aborted: false,
    willRetry: false,
    result: {
      summary: "deterministic parent compaction",
      firstKeptEntryId: "entry-1",
      tokensBefore: 100,
      estimatedTokensAfter: 50,
    },
  });
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
    const timer = setInterval(() => {
      if (existsSync(path)) {
        clearInterval(timer);
        resolve();
      }
    }, 50);
  });
}
async function emitDirectLifecycle(envelope) {
  const messageId = envelope.message_id;
  const appendId = `functional-append-${messageId}`;
  privateReceipt("mecha.mailbox.appended.v1", { protocol: "mecha.mailbox/v1", message_id: messageId, runtime_generation: envelope.runtime_generation, session_epoch: envelope.session_epoch }, appendId);
  emit({ type: "tool_execution_start", toolCallId: `functional-tool-${messageId}`, toolName: "bash", args: { command: "echo functional" } });
  await waitForMarker(join(sessionRoot, "allow-tool-end"));
  emit({ type: "tool_execution_end", toolCallId: `functional-tool-${messageId}`, toolName: "bash", result: "functional", isError: false });
  emit({ type: "agent_start" });
  emit({ type: "message_end", message: { id: `functional-reply-${messageId}`, role: "assistant", content: [{ type: "text", text: "Deterministic managed reply" }] } });
  emit({ type: "agent_settled" });
}

// Opt-in RPC boundary fixture: never enabled by ordinary functional tests.
let rootBootstrap;
const rootRequests = new Map();
const rootResults = new Map();
const canonical = value => value === null || typeof value !== "object" ? JSON.stringify(value) : Array.isArray(value) ? `[${value.map(canonical).join(",")}]` : `{${Object.keys(value).sort().map(key => `${JSON.stringify(key)}:${canonical(value[key])}`).join(",")}}`;
const hash = value => createHash("sha256").update(canonical(value)).digest("hex");
function rootLog(value) { appendFileSync(join(process.env.MECHA_TEST_ROOT_CONTROL, "root-fixture.jsonl"), JSON.stringify(value) + "\n"); }
function rootRequest(content) {
  const data = JSON.parse(content.slice("ROOT_CONTROL ".length).split("\n")[0]);
  const tool = JSON.parse(readFileSync(process.env.MECHA_TEST_ROOT_MANIFEST, "utf8")).tools.find(item => item.operation_id === data.operation);
  if (!tool || !rootBootstrap) throw new Error("fixture missing tool or bootstrap");
  const details = { protocol: "mecha.control/v1", request_id: data.id, tool_call_id: `call-${data.id}`, capability_id: tool.capability_id, tool_name: tool.name, definition_id: tool.definition_id, definition_sha256: tool.definition_sha256, operation_id: tool.operation_id, manifest_sha256: rootBootstrap.manifest_sha256, runtime_generation: rootBootstrap.runtime_generation, session_epoch: rootBootstrap.session_epoch, context_ref: "root", arguments: data.arguments };
  rootRequests.set(data.id, details);
  rootLog({ kind: "request", details });
  emit({ type: "custom_message", customType: "mecha.control.request.v1", content: "Router control request queued.", display: false, details });
}
if (process.env.MECHA_TEST_ROOT_CONTROL) {
  const replayPath = join(process.env.MECHA_TEST_ROOT_CONTROL, "replay-request");
  const replayed = new Set();
  setInterval(() => {
    if (!existsSync(replayPath)) return;
    const replay = JSON.parse(readFileSync(replayPath, "utf8"));
    const id = replay.id;
    const details = rootRequests.get(id);
    if (!details || replayed.has(id)) return;
    replayed.add(id);
    rootLog({ kind: "request", details });
    emit({ type: "custom_message", customType: "mecha.control.request.v1", content: "Router control request queued.", display: false, details });
    rootRequest(`ROOT_CONTROL ${JSON.stringify(replay.barrier)}`);
  }, 50).unref();
}
const input = createInterface({ input: process.stdin });
input.on("line", (line) => {
  let request;
  try { request = JSON.parse(line); } catch { return; }
  if (request.type === "get_state") { response(request, { sessionId, isCompacting: false }); return; }
  if (request.type === "get_commands") { response(request, { commands }); return; }
  if (request.type === "get_active_tools") { response(request, { active_tools: activeTools }); return; }
  if (request.type === "get_entries") {
    emit({ id: request.id, type: "response", success: false, error: "Unsupported command: get_entries" });
    return;
  }
  if (request.type === "abort") {
    const success = !existsSync(join(sessionRoot, "fail-abort"));
    response(request, { command: "abort", success });
    if (success) setImmediate(() => { input.close(); process.exit(0); });
    return;
  }
  if (request.type !== "prompt") { response(request); return; }

  const message = typeof request.message === "string" ? request.message : "";
  if (!message.startsWith("/")) { response(request, { command: "prompt", success: true }); return; }
  const command = message.slice(1).split(" ", 1)[0];
  let envelope;
  try { envelope = decodeMessage(message); } catch { response(request); return; }
  if (process.env.MECHA_TEST_ROOT_CONTROL && command === "mecha-control-result-v1") {
    rootResults.set(envelope.request_id, envelope);
    rootLog({ kind: "result", payload: envelope });
    response(request, { kind: "accepted", result_id: envelope.result_id });
    return;
  }
  if (process.env.MECHA_TEST_ROOT_CONTROL && command === "mecha-control-result-reconcile-v1") {
    const result = rootResults.get(envelope.request_id);
    if (!result || result.result_id !== envelope.result_id || hash(result) !== envelope.payload_hash || result.runtime_generation !== envelope.runtime_generation || result.target_session_epoch !== envelope.session_epoch || result.manifest_sha256 !== envelope.manifest_sha256) {
      emit({ id: request.id, type: "response", command: request.type, success: false, error: "fixture reconciliation mismatch" }); return;
    }
    rootLog({ kind: "reconcile", envelope });
    response(request, { protocol: "mecha.control.result.persisted.v1", receipt_id: `mcrr1_${hash(envelope)}`, request_id: envelope.request_id, result_id: envelope.result_id, payload_hash: envelope.payload_hash, runtime_generation: envelope.runtime_generation, session_epoch: envelope.session_epoch, manifest_sha256: envelope.manifest_sha256 });
    return;
  }
  response(request, { command: "prompt", success: true });
  if (command === "mecha-bootstrap-v1") {
    if (process.env.MECHA_TEST_ROOT_CONTROL) rootBootstrap = envelope;
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
    if (process.env.MECHA_TEST_ROOT_CONTROL && envelope.content?.startsWith("ROOT_CONTROL ")) {
      privateReceipt("mecha.mailbox.appended.v1", { protocol: "mecha.mailbox/v1", message_id: envelope.message_id, runtime_generation: envelope.runtime_generation, session_epoch: envelope.session_epoch }, `functional-append-${envelope.message_id}`);
      rootRequest(envelope.content);
      emit({ type: "agent_settled" });
      return;
    }
    const childCommand = ["Start structured child", "Start child compaction", "Finish child compaction", "Finish structured child"].find((c) => envelope.content?.startsWith(c));
    if (childCommand) {
      // Each managed prompt commands exactly one source transition. The direct
      // receipt and settlement follow the command on this same Pi stdout.
      privateReceipt("mecha.mailbox.appended.v1", { protocol: "mecha.mailbox/v1", message_id: envelope.message_id, runtime_generation: envelope.runtime_generation, session_epoch: envelope.session_epoch }, `functional-append-${envelope.message_id}`);
      void emitChildTransitionCommand(childCommand).then(() => emit({ type: "agent_settled" }));
    } else if (envelope.content?.startsWith("Run parent compaction")) {
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
