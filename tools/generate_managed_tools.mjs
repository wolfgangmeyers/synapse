#!/usr/bin/env node
import { createHash } from "node:crypto";
import { readFileSync, writeFileSync } from "node:fs";
import { resolve } from "node:path";

const root = resolve(import.meta.dirname, "..");
const output = resolve(root, "router/contract/managed-tools-v1.json");
const sources = {
  goal: resolve(root, "pi-extensions/mecha-goal/index.ts"),
  messaging: resolve(root, "pi-extensions/mecha-messaging/index.ts"),
  operational: resolve(root, "pi-extensions/mecha-messaging/operational-tools.ts"),
  rootControl: resolve(root, ".claude/skills/supervisor/tools/managed_root_control.py"),
};

function canonicalize(value) {
  if (value === null || typeof value === "boolean" || typeof value === "string") return JSON.stringify(value);
  if (typeof value === "number") return Object.is(value, -0) ? "0" : JSON.stringify(value);
  if (Array.isArray(value)) return `[${value.map(canonicalize).join(",")}]`;
  const keys = Object.keys(value).filter((key) => value[key] !== undefined).sort((left, right) => Buffer.compare(Buffer.from(left), Buffer.from(right)));
  return `{${keys.map((key) => `${JSON.stringify(key)}:${canonicalize(value[key])}`).join(",")}}`;
}
function hash(value) { return createHash("sha256").update(canonicalize(value), "utf8").digest("hex"); }
function closed(properties = {}, required = []) { return { type: "object", additionalProperties: false, required, properties }; }
function string(maxLength = 1024, minLength) { return { type: "string", ...(minLength === undefined ? {} : { minLength }), maxLength }; }
function action(values, properties = {}, required = ["action"]) {
  const actionField = values.length === 1
    ? { type: "string", minLength: values[0].length, maxLength: values[0].length }
    : { type: "string", maxLength: 64, enum: values };
  return closed({ action: actionField, ...properties }, required);
}
function definition(capabilityId, label, description, parameters, operationId, metadata = {}, immutableName = false) {
  const base = { capability_id: capabilityId, label, description, parameters, operation_id: operationId, ...metadata };
  const definition_sha256 = hash(base);
  return { ...base, name: immutableName ? `${capabilityId}__${definition_sha256}` : capabilityId, definition_id: `def_${definition_sha256}`, definition_sha256 };
}

function assertSourceContract() {
  const goal = readFileSync(sources.goal, "utf8");
  const messaging = readFileSync(sources.messaging, "utf8");
  const operational = readFileSync(sources.operational, "utf8");
  const rootControl = readFileSync(sources.rootControl, "utf8");
  for (const [text, marker, file] of [
    [goal, 'description: "Set, inspect, or disable the active goal evaluator."', sources.goal],
    [goal, 'description: "Deliberately wait before resuming an active goal when snooze is enabled."', sources.goal],
    [messaging, 'description: "Child-only: send an ordinary Mailbox message to this native subagent\'s target parent."', sources.messaging],
    [messaging, 'description: "Send an ordinary Mailbox V2 message to another local agent."', sources.messaging],
    [messaging, 'description: "Enable, disable, or inspect process-local Markdown recall."', sources.messaging],
    [messaging, 'description: "Enable, disable, or inspect this Pi process\'s managed-worker settled mailbox notifications."', sources.messaging],
    [messaging, 'name: "mecha_context"', sources.messaging],
    [operational, "createOperationalToolDefinitions", sources.operational],
    [operational, 'descriptor("mecha_state", "Supervisor state"', sources.operational],
    [operational, 'descriptor("mecha_priority", "Supervisor priority"', sources.operational],
    [rootControl, 'if operation == "web.search":', sources.rootControl],
  ]) if (!text.includes(marker)) throw new Error(`Reviewed source no longer exposes ${marker}: ${file}`);
}

function definitions() {
  assertSourceContract();
  const text = string(4000, 1);
  const task = string(120, 1);
  const optionalSubject = string(256);
  const tool = string(64, 1);
  const count = { type: "integer", minimum: 1, maximum: 5 };
  const metadata = {
    goal: { prompt_snippet: "Set, inspect, or disable the active goal evaluator.", prompt_guidelines: ["Use goal with action set, status, or off for standing goal evaluation.", "Call pi__goal through the Cursor pi bridge."] },
    mecha_parent_message: { prompt_snippet: "Send a message from a native subagent to its parent", prompt_guidelines: ["Use mecha_parent_message only in native subagent sessions to message the parent agent.", "Call pi__mecha_parent_message through the Cursor pi bridge."] },
    mecha_mailbox_send: { prompt_snippet: "Send a durable Mailbox V2 message to another local agent", prompt_guidelines: ["Use mecha_mailbox_send for supervisor, worker, and service replies with recipient, body, and optional subject.", "Call pi__mecha_mailbox_send through the Cursor pi bridge."] },
    mecha_request_inline_tool_allowance: { prompt_snippet: "Request a temporary exception to the inline tool guard", prompt_guidelines: ["Use mecha_request_inline_tool_allowance only for genuinely necessary top-level bash/edit/write work.", "Call pi__mecha_request_inline_tool_allowance through the Cursor pi bridge."] },
    mecha_recall: { prompt_snippet: "Enable, disable, or inspect process-local Markdown recall", prompt_guidelines: ["Use mecha_recall with action enable, disable, or status for recall stores.", "Call pi__mecha_recall through the Cursor pi bridge."] },
    mecha_settled_notifications: { prompt_snippet: "Enable, disable, or inspect settled mailbox notifications", prompt_guidelines: ["Use mecha_settled_notifications with action enable, disable, or status for worker settled notifications.", "Call pi__mecha_settled_notifications through the Cursor pi bridge."] },
    mecha_context: { prompt_snippet: "Inspect context-threshold policy or request Pi self-compaction", prompt_guidelines: ["Use mecha_context with action status, set_policy, or compact for context-threshold and compaction control.", "Call pi__mecha_context through the Cursor pi bridge."] },
    mecha_workflow_transition: {
      prompt_snippet: "Transition the current managed workflow from its latest state entry",
      prompt_guidelines: [
        "Use only the exact entry ID and a listed destination name or complete from the latest workflow entry message; a concise note is optional; never invent a target.",
        "Call pi__mecha_workflow_transition through the Cursor pi bridge.",
      ],
    },
    web_search: { prompt_snippet: "Search the web and return linked source results", prompt_guidelines: ["Use web_search when current or external source-backed information is needed.", "Choose live for time-sensitive facts, cached for stable information, and indexed when freshness is not required."] },
  };
  return [
    definition("goal", "Goal", "Set, inspect, or disable the active goal evaluator.", action(["set", "status", "off"], { goal: string(4000, 1) }, ["action"]), "goal.control", metadata.goal),
    definition("goal_snooze", "Goal Snooze", "Deliberately wait before resuming an active goal when snooze is enabled.", closed(), "goal.snooze"),
    definition("mecha_parent_message", "Message parent", "Child-only: send an ordinary Mailbox message to this native subagent's target parent.", closed({ message: string(8192, 1) }, ["message"]), "mail.parent", metadata.mecha_parent_message),
    definition("mecha_mailbox_send", "Send Mailbox message", "Send an ordinary Mailbox V2 message to another local agent.", closed({ recipient: string(120, 1), body: string(8192, 1), subject: optionalSubject }, ["recipient", "body"]), "mail.send", metadata.mecha_mailbox_send),
    definition("mecha_request_inline_tool_allowance", "Request inline tool allowance", "Request a temporary exception for genuinely necessary inline use of a named tool. This is an exception to the rule; subagents remain mandatory by default.", closed({ toolName: tool, usageCount: count }, ["toolName", "usageCount"]), "policy.inline_allowance", metadata.mecha_request_inline_tool_allowance),
    definition("mecha_recall", "Mecha Recall", "Enable, disable, or inspect process-local Markdown recall.", action(["enable", "disable", "status"], { stores: { type: "array", items: string(8192), maxItems: 64 } }), "recall.control", metadata.mecha_recall),
    definition("mecha_settled_notifications", "Mecha Settled Notifications", "Enable, disable, or inspect this Pi process's managed-worker settled mailbox notifications.", action(["enable", "disable", "status"]), "notifications.settled", metadata.mecha_settled_notifications),
    definition("mecha_context", "Mecha Context", "Inspect or configure Pi context-threshold notifications, or trigger self-compaction.", action(["status", "set_policy", "compact"], {
      policy: closed({ levels: { type: "array", items: closed({ percent: { type: "number", minimum: 1, maximum: 99 }, guidance: string(2000), mandatory: { type: "boolean" } }, ["percent"]), maxItems: 64 } }),
      customInstructions: string(2000), postCompactionNotification: string(2000),
    }), "context.control", metadata.mecha_context),
    definition("mecha_state", "Supervisor state", "Manage supervisor task state.", action(["query", "create", "update"], { task, state: { type: "string", maxLength: 16, enum: ["active", "completed", "abandoned"] }, alias: task, type: task, project: task, description: string(4000, 1), purpose: string(4000, 1), window: task, branch: task, pr: string(4000, 1), linear: task, field: { type: "string", maxLength: 32, enum: ["summary", "branch", "window", "pr", "linear", "dev_environment", "supervisor_reminder_id", "worker_self_reminder_ids"] }, value: string(4000, 1) }), "ops.state"),
    definition("mecha_priority", "Supervisor priority", "Manage supervisor priority records.", action(["create", "update", "status"], { priorityId: task, name: string(4000, 1), status: { type: "string", maxLength: 16, enum: ["active", "blocked", "completed", "deferred", "cancelled"] }, taskIds: { type: "array", minItems: 1, items: { type: "integer", minimum: 1, maximum: Number.MAX_SAFE_INTEGER }, maxItems: 50 }, evidence: string(4000, 1) }), "ops.priority"),
    definition("mecha_verification_handoff", "Verification handoff", "Record a verification handoff.", action(["record"], { task, reminderId: task, successor: string(4000, 1), remoteVerified: { type: "string", maxLength: 16, enum: ["pending", "true", "false"] } }, ["action", "task", "reminderId", "successor"]), "ops.verification_handoff"),
    definition("mecha_reminder", "Mecha reminder", "Manage reminders through fixed operations.", action(["list", "create", "update", "delete"], { reminderId: task, message: text, subject: optionalSubject, delayMinutes: { type: "integer", minimum: 1, maximum: 10080 }, scheduledAt: string(64), intervalMinutes: { type: "integer", minimum: 1, maximum: 10080 } }), "reminder.control"),
    definition("mecha_worker", "Managed worker", "Manage existing tmux workers through fixed operations.", action(["list", "launch", "cleanup", "send"], { alias: string(120, 1), worker: task, message: text, subject: string(200) }), "ops.worker"),
    definition("mecha_workflow_transition", "Managed workflow transition", "Transition the current managed workflow using its exact state-entry identity and a listed exit.", closed({ entryId: string(36, 36), exit: string(256, 1), note: string(4096) }, ["entryId", "exit"]), "workflow.transition", metadata.mecha_workflow_transition),
    definition("mecha_pr", "Mecha pull request", "Read pull request status.", action(["status"], { pullRequest: { type: "integer", minimum: 1, maximum: 1000000000 } }, ["action", "pullRequest"]), "ops.pr_status"),
    definition("mecha_git", "Mecha git", "Read task git status.", action(["status"], { task: task }, ["action", "task"]), "ops.git_status"),
    definition("mecha_router", "Mecha router", "Read or control fixed router operations.", action(["health", "retry", "dismiss"], { messageId: task }), "router.control"),
    definition("web_search", "Web search", "Search the web and return source links from the managed search provider.", closed({ query: string(4000, 1), freshness: { type: "string", maxLength: 16, enum: ["live", "cached", "indexed"] }, maxResults: { type: "integer", minimum: 1, maximum: 10 } }, ["query"]), "web.search", metadata.web_search, true),
  ].sort((left, right) => Buffer.compare(Buffer.from(left.capability_id), Buffer.from(right.capability_id)));
}

const desired = { protocol: "mecha.tools/v1", revision: 3, tools: definitions() };
const rendered = `${JSON.stringify(desired, null, 2)}\n`;
if (process.argv.includes("--check")) {
  const existing = readFileSync(output, "utf8");
  if (existing !== rendered) throw new Error("managed-tools-v1.json is out of date; run tools/generate_managed_tools.mjs");
} else {
  writeFileSync(output, rendered, "utf8");
}
