import { createMailboxClient, createNanoId, assertAgentId, routerHttpUrl } from "./mailbox-client.js";
export { createMailboxClient } from "./mailbox-client.js";
import { execFile, execFileSync } from "node:child_process";
import { appendFileSync, mkdirSync, readFileSync, statSync, writeFileSync } from "node:fs";
import { createRecallState } from "./recall-state.js";
import { createRecallController } from "./recall-controller.js";
import { createRecallPipeline as createPipeline } from "./recall-pipeline.js";
import { runRecallEvaluator as evaluateRecall, validateRecallChoice as validateChoice } from "./recall-evaluator.js";
import { createMemoryCaptureLifecycle } from "./memory-capture.js";
export { createRecallState } from "./recall-state.js";
export { createRecallController } from "./recall-controller.js";
export { createRecallPipeline } from "./recall-pipeline.js";
export { runRecallEvaluator, validateRecallChoice } from "./recall-evaluator.js";

const DEFAULT_SESSION_CHECKPOINT_RETRY_MS = 250;
const DEFAULT_PENDING_REINJECTION_DELAY_MS = 5_000;
const MAILBOX_MESSAGE_TYPE = "mecha-agent-message";
const CONTEXT_THRESHOLD_MESSAGE_TYPE = "mecha-context-threshold";
const POST_COMPACTION_MESSAGE_TYPE = "mecha-post-compaction-handoff";
const COMPACTION_POLICY_ENV = "MECHA_PI_COMPACTION_POLICY";
const COMPACTION_MODEL_KEY_ENV = "MECHA_PI_MODEL_KEY";
const MAX_POST_COMPACTION_NOTE_CHARS = 4000;
const MEMORY_CAPTURE_DIAGNOSTIC_MAX_BYTES = 128 * 1024;
const MEMORY_CAPTURE_DIAGNOSTIC_KEEP_BYTES = 64 * 1024;
const ORPHAN_TOOL_RESULT_DIAGNOSTIC_MAX_BYTES = 256 * 1024;
const ORPHAN_TOOL_RESULT_DIAGNOSTIC_KEEP_BYTES = 128 * 1024;
const CHILD_SESSION_IDS = Symbol.for("mecha.mailbox-v2.child-session-ids");
const PI_SUBAGENTS_SERVICE = Symbol.for("@gotgenes/pi-subagents:service");

const CONTEXT_THRESHOLD_SUFFIX = "Use workers or subagents for bounded side work to avoid context bloat.";
export const INLINE_TOOL_ALLOWANCE_TOOL_NAME = "mecha_request_inline_tool_allowance";
const INLINE_ACTION_TOOL_NAMES = new Set(["bash", "edit", "write"]);
const INLINE_TOOL_BLOCK_REASON = "Top-level inline write-ish tool calls are blocked by default. Subagents are mandatory for this work. Only when genuinely necessary, use mecha_request_inline_tool_allowance to request a temporary allowance; that is an exceptional path, not the normal workaround.";

const SMALL_CONTEXT_LEVELS = Object.freeze([
  Object.freeze({ percent: 40, guidance: "consider compacting at a reasonable stopping point to keep future calls cheaper" }),
  Object.freeze({ percent: 50, guidance: "compact soon, ideally at the next clean boundary" }),
  Object.freeze({ percent: 60, guidance: "compact when practical; each call is getting meaningfully more expensive" }),
  Object.freeze({ percent: 70, guidance: "strongly consider compacting before more expensive work" }),
  Object.freeze({ percent: 80, guidance: "compact as soon as practical; the session is now very expensive" }),
  Object.freeze({ percent: 90, guidance: "mandatory stop and compact before continuing", mandatory: true }),
]);
// Opus 5 reuses the small-context guidance wording verbatim at lower percentages;
// mandatory compaction lands at 40% instead of 90%.
const OPUS_LEVEL_PERCENTS = Object.freeze([15, 20, 25, 30, 35, 40]);
const OPUS_CONTEXT_LEVELS = Object.freeze(
  SMALL_CONTEXT_LEVELS.map((level, index) => Object.freeze({ ...level, percent: OPUS_LEVEL_PERCENTS[index] })),
);
const LARGE_CONTEXT_LEVELS = Object.freeze([
  Object.freeze({ percent: 20, guidance: "large-context awareness only; no compaction needed yet" }),
  Object.freeze({ percent: 30, guidance: "compact soon, ideally at the next clean boundary" }),
  Object.freeze({ percent: 40, guidance: "large-context calls are expensive; compact as soon as practical" }),
  Object.freeze({ percent: 45, guidance: "mandatory stop and compact before the session reaches 500k tokens", mandatory: true }),
]);
const SOL_CONTEXT_LEVELS = Object.freeze([
  Object.freeze({ percent: 15, guidance: "Sol context awareness; keep the session lean" }),
  Object.freeze({ percent: 25, guidance: "Sol calls are expensive; plan to compact soon" }),
  Object.freeze({ percent: 35, guidance: "strongly consider compacting this Sol session" }),
  Object.freeze({ percent: 45, guidance: "mandatory stop and compact this Sol session", mandatory: true }),
]);

export const DEFAULT_COMPACTION_POLICY = Object.freeze({
  tiers: Object.freeze([
    Object.freeze({ maxContextTokens: 300_000, levels: SMALL_CONTEXT_LEVELS }),
    Object.freeze({ maxContextTokens: 1_000_000, levels: LARGE_CONTEXT_LEVELS }),
    Object.freeze({ maxContextTokens: null, levels: LARGE_CONTEXT_LEVELS }),
  ]),
  models: Object.freeze({
    "openai-codex/gpt-5.6-sol": Object.freeze({ levels: SOL_CONTEXT_LEVELS }),
    "gpt-5.6-sol": Object.freeze({ levels: SOL_CONTEXT_LEVELS }),
    "anthropic/claude-opus-5": Object.freeze({ levels: OPUS_CONTEXT_LEVELS }),
    "claude-opus-5": Object.freeze({ levels: OPUS_CONTEXT_LEVELS }),
  }),
});

function normalizePostCompactionNotification(value) {
  if (value === undefined || value === null) return undefined;
  if (typeof value !== "string") throw new TypeError("postCompactionNotification must be a string");
  const trimmed = value.trim();
  return trimmed.length === 0 ? undefined : trimmed.slice(0, MAX_POST_COMPACTION_NOTE_CHARS);
}

function normalizeCompactionLevel(level) {
  const source = typeof level === "number" ? { percent: level } : level;
  if (source === null || typeof source !== "object") {
    throw new TypeError("each compaction level must be a number or object");
  }
  const percent = Number(source.percent);
  if (!Number.isFinite(percent) || percent <= 0 || percent >= 100) {
    throw new TypeError("compaction level percent must be greater than 0 and less than 100");
  }
  const normalized = { percent: Math.round(percent * 100) / 100 };
  if (typeof source.guidance === "string" && source.guidance.trim().length > 0) {
    normalized.guidance = source.guidance.trim();
  }
  if (source.mandatory === true) normalized.mandatory = true;
  return normalized;
}

function normalizeCompactionLevels(rawLevels) {
  if (!Array.isArray(rawLevels) || rawLevels.length === 0) {
    throw new TypeError("compaction policy requires at least one percentage level");
  }
  const byPercent = new Map();
  for (const level of rawLevels.map(normalizeCompactionLevel)) {
    byPercent.set(level.percent, level);
  }
  return [...byPercent.values()].sort((left, right) => left.percent - right.percent);
}

function normalizeCompactionTier(tier) {
  if (tier === null || typeof tier !== "object" || Array.isArray(tier)) {
    throw new TypeError("each compaction tier must be an object");
  }
  const maxContextTokens = tier.maxContextTokens ?? null;
  if (maxContextTokens !== null && (!Number.isSafeInteger(maxContextTokens) || maxContextTokens <= 0)) {
    throw new TypeError("compaction tier maxContextTokens must be a positive safe integer or null");
  }
  return { maxContextTokens, levels: normalizeCompactionLevels(tier.levels) };
}

function normalizeModelKey(key) {
  if (typeof key !== "string" || key.trim().length === 0) throw new TypeError("model policy key must be a non-empty string");
  return key.trim().toLowerCase();
}

function normalizeCompactionModels(rawModels) {
  if (rawModels === undefined || rawModels === null) return undefined;
  const entries = Array.isArray(rawModels)
    ? rawModels.map((entry) => [entry?.key, entry?.policy ?? entry])
    : Object.entries(rawModels);
  const models = {};
  for (const [key, value] of entries) {
    models[normalizeModelKey(key)] = resolveCompactionPolicy(value, { allowModels: false });
  }
  return Object.keys(models).length === 0 ? undefined : models;
}

export function resolveCompactionPolicy(input = DEFAULT_COMPACTION_POLICY, { allowModels = true } = {}) {
  if (Array.isArray(input) || Array.isArray(input?.levels)) {
    const source = Array.isArray(input) ? {} : input;
    return {
      tiers: [{ maxContextTokens: null, levels: normalizeCompactionLevels(Array.isArray(input) ? input : input.levels) }],
      postCompactionNotification: normalizePostCompactionNotification(source.postCompactionNotification),
      models: allowModels ? normalizeCompactionModels(source.models) : undefined,
    };
  }
  const rawTiers = input?.tiers;
  if (!Array.isArray(rawTiers) || rawTiers.length === 0) {
    throw new TypeError("compaction policy requires default levels or at least one context-size tier");
  }
  const tiers = rawTiers.map(normalizeCompactionTier).sort((left, right) => {
    if (left.maxContextTokens === null) return 1;
    if (right.maxContextTokens === null) return -1;
    return left.maxContextTokens - right.maxContextTokens;
  });
  return {
    tiers,
    postCompactionNotification: normalizePostCompactionNotification(input.postCompactionNotification),
    models: allowModels ? normalizeCompactionModels(input.models) : undefined,
  };
}

export function compactionPolicyFromEnvironment(environment = process.env) {
  const configured = environment[COMPACTION_POLICY_ENV];
  if (typeof configured !== "string" || configured.trim().length === 0) {
    return resolveCompactionPolicy(DEFAULT_COMPACTION_POLICY);
  }
  const trimmed = configured.trim();
  try {
    return resolveCompactionPolicy(JSON.parse(trimmed));
  } catch (jsonError) {
    if (!/^\d+(?:\.\d+)?(?:\s*,\s*\d+(?:\.\d+)?)*$/.test(trimmed)) throw jsonError;
    return resolveCompactionPolicy(trimmed.split(",").map((part) => Number(part.trim())));
  }
}

export function compactionModelKeys(model, environment = process.env) {
  const keys = [];
  const add = (value) => {
    if (typeof value !== "string" || value.trim().length === 0) return;
    const key = value.trim().toLowerCase();
    if (!keys.includes(key)) keys.push(key);
  };
  if (model !== undefined && model !== null) {
    add(model.provider && model.id ? `${model.provider}/${model.id}` : undefined);
    add(model.provider && model.name ? `${model.provider}:${model.name}` : undefined);
    add(model.id);
    add(model.name);
  }
  add(environment[COMPACTION_MODEL_KEY_ENV]);
  return keys;
}

export function effectiveCompactionPolicy(policy, modelOrKeys, environment = process.env) {
  const basePolicy = resolveCompactionPolicy(policy);
  const modelKeys = Array.isArray(modelOrKeys) ? modelOrKeys.map(normalizeModelKey) : compactionModelKeys(modelOrKeys, environment);
  const matchedModelKey = modelKeys.find((key) => basePolicy.models?.[key] !== undefined);
  return {
    policy: matchedModelKey === undefined ? basePolicy : basePolicy.models[matchedModelKey],
    matchedModelKey,
    modelKeys,
  };
}

export function selectCompactionTier(policy, contextWindow, modelOrKeys, environment = process.env) {
  const effective = effectiveCompactionPolicy(policy, modelOrKeys, environment);
  const tiers = effective.policy.tiers;
  const tier = !Number.isFinite(contextWindow)
    ? tiers[0]
    : tiers.find((candidate) => candidate.maxContextTokens === null || contextWindow <= candidate.maxContextTokens) ?? tiers.at(-1);
  return { ...tier, matchedModelKey: effective.matchedModelKey, modelKeys: effective.modelKeys };
}

export function nextCompactionThreshold(policy, usageOrPercent, alreadyNotifiedPercent = 0, contextWindow, modelOrKeys, environment = process.env) {
  const percent = typeof usageOrPercent === "object" && usageOrPercent !== null ? usageOrPercent.percent : usageOrPercent;
  const window = typeof usageOrPercent === "object" && usageOrPercent !== null ? usageOrPercent.contextWindow : contextWindow;
  const model = typeof usageOrPercent === "object" && usageOrPercent !== null ? usageOrPercent.model : modelOrKeys;
  if (!Number.isFinite(percent)) return undefined;
  const tier = selectCompactionTier(policy, window, model, environment);
  const level = tier.levels
    .filter((candidate) => percent >= candidate.percent && candidate.percent > alreadyNotifiedPercent)
    .at(-1);
  return level === undefined ? undefined : { ...level, tier };
}

export function postCompactionNotificationMessage(note, result = {}) {
  const fields = [];
  if (Number.isFinite(result.tokensBefore)) fields.push(`tokens before: ${result.tokensBefore.toLocaleString()}`);
  if (Number.isFinite(result.estimatedTokensAfter)) fields.push(`estimated tokens after: ${result.estimatedTokensAfter.toLocaleString()}`);
  if (typeof result.firstKeptEntryId === "string" && result.firstKeptEntryId.length > 0) fields.push(`first kept entry: ${result.firstKeptEntryId}`);
  if (typeof result.summary === "string") fields.push(`summary chars: ${result.summary.length.toLocaleString()}`);
  const status = fields.length > 0 ? `\n\nCompaction status: ${fields.join("; ")}.` : "";
  return `Post-compaction handoff note:\n\n${note}${status}`;
}

export function compactionNotificationMessage({ level, usage }) {
  const roundedPercent = usage.percent === null ? level.percent : Math.round(usage.percent * 10) / 10;
  const tokenText = usage.tokens === null
    ? `${roundedPercent}% of ${usage.contextWindow.toLocaleString()} context tokens`
    : `${usage.tokens.toLocaleString()} / ${usage.contextWindow.toLocaleString()} context tokens (${roundedPercent}%)`;
  const guidance = level.guidance ?? (level.mandatory ? "mandatory threshold reached" : "threshold reached");
  return `Pi context threshold reached: ${tokenText}. Guidance at ${level.percent}%: ${guidance}. ${CONTEXT_THRESHOLD_SUFFIX}`;
}

function normalizeInlineToolName(toolName) {
  if (typeof toolName !== "string" || toolName.trim().length === 0) throw new TypeError("toolName must be a non-empty string");
  return toolName.trim();
}

function validateInlineAllowanceCount(usageCount) {
  if (!Number.isSafeInteger(usageCount) || usageCount < 1 || usageCount > 5) {
    throw new TypeError("usageCount must be an integer from 1 through 5");
  }
}

export function inlineToolBlockReason() {
  return INLINE_TOOL_BLOCK_REASON;
}

export function createInlineToolPolicy() {
  const credits = new Map();
  return {
    reset() {
      credits.clear();
    },
    grant(toolName, usageCount) {
      const normalizedToolName = normalizeInlineToolName(toolName);
      validateInlineAllowanceCount(usageCount);
      const remaining = (credits.get(normalizedToolName) ?? 0) + usageCount;
      credits.set(normalizedToolName, remaining);
      return { toolName: normalizedToolName, usageCount, remaining };
    },
    evaluate(toolName) {
      if (!INLINE_ACTION_TOOL_NAMES.has(toolName)) return undefined;
      // Credits are per-tool and temporary: matching top-level calls spend them, exhausted entries are removed, and agent_start resets the state.
      const remaining = credits.get(toolName) ?? 0;
      if (remaining > 0) {
        if (remaining === 1) credits.delete(toolName);
        else credits.set(toolName, remaining - 1);
        return undefined;
      }
      return { block: true, reason: inlineToolBlockReason() };
    },
  };
}

export function boundedConversationText(content, maximum = 4096) {
  const blocks = Array.isArray(content) ? content : [content];
  const text = blocks.map((block) => typeof block === "string" ? block : block?.type === "text" && typeof block.text === "string" ? block.text : "").filter(Boolean).join("\n");
  return text.slice(-maximum);
}

function memoryCaptureDiagnosticsPath(environment = process.env) {
  if (typeof environment.MECHA_MEMORY_CAPTURE_DIAGNOSTICS_PATH === "string" && environment.MECHA_MEMORY_CAPTURE_DIAGNOSTICS_PATH.trim().length > 0) return environment.MECHA_MEMORY_CAPTURE_DIAGNOSTICS_PATH.trim();
  const home = environment.HOME;
  return typeof home === "string" && home.length > 0 ? `${home}/.mecha-developer/logs/memory-capture.jsonl` : undefined;
}

function normalizeMemoryCaptureDiagnosticReason(reason) {
  return typeof reason === "string" && /^[a-z0-9-]{1,120}$/.test(reason) ? reason : "capture-failed";
}

function appendBoundedJsonl(record, { path, maxBytes, keepBytes }) {
  if (path === undefined) return;
  try {
    mkdirSync(path.replace(/\/[^/]*$/, ""), { recursive: true });
    appendFileSync(path, `${JSON.stringify(record)}\n`, { encoding: "utf8", mode: 0o600 });
    if (statSync(path).size <= maxBytes) return;
    const lines = readFileSync(path, "utf8").slice(-keepBytes).split("\n").slice(1).filter(Boolean);
    writeFileSync(path, `${lines.join("\n")}\n`, { encoding: "utf8", mode: 0o600 });
  } catch {
    // Diagnostics are best-effort and must never expose source text or break runtime flow.
  }
}

export function appendMemoryCaptureDiagnostic(event, { path = memoryCaptureDiagnosticsPath(), maxBytes = MEMORY_CAPTURE_DIAGNOSTIC_MAX_BYTES, keepBytes = MEMORY_CAPTURE_DIAGNOSTIC_KEEP_BYTES } = {}) {
  const record = {
    timestamp: typeof event?.timestamp === "string" ? event.timestamp : new Date().toISOString(),
    trigger: ["compaction", "shutdown"].includes(event?.trigger) ? event.trigger : "unknown",
    status: ["noop", "applied", "failed"].includes(event?.status) ? event.status : "failed",
    reason: normalizeMemoryCaptureDiagnosticReason(event?.reason),
    records: Number.isFinite(event?.records) ? event.records : undefined,
  };
  appendBoundedJsonl(record, { path, maxBytes, keepBytes });
}

function orphanToolResultDiagnosticsPath(environment = process.env) {
  if (typeof environment.MECHA_ORPHAN_TOOL_RESULT_DIAGNOSTICS_PATH === "string" && environment.MECHA_ORPHAN_TOOL_RESULT_DIAGNOSTICS_PATH.trim().length > 0) return environment.MECHA_ORPHAN_TOOL_RESULT_DIAGNOSTICS_PATH.trim();
  const home = environment.HOME;
  return typeof home === "string" && home.length > 0 ? `${home}/.mecha-developer/logs/orphan-tool-results.jsonl` : undefined;
}

function safeMessageRole(message) {
  return typeof message?.role === "string" ? message.role : undefined;
}

function safeStopReason(message) {
  return typeof message?.stopReason === "string" ? message.stopReason : undefined;
}

function messageToolCallIds(message) {
  if (!Array.isArray(message?.content)) return [];
  return message.content
    .map((block) => block?.type === "toolCall" ? providerToolCallId(block.id) : undefined)
    .filter((toolCallId) => toolCallId !== undefined);
}

export function appendOrphanToolResultDiagnostic(event, { path = orphanToolResultDiagnosticsPath(), maxBytes = ORPHAN_TOOL_RESULT_DIAGNOSTIC_MAX_BYTES, keepBytes = ORPHAN_TOOL_RESULT_DIAGNOSTIC_KEEP_BYTES } = {}) {
  const record = {
    timestamp: new Date().toISOString(),
    session_id: typeof event?.sessionId === "string" ? event.sessionId : undefined,
    session_file: typeof event?.sessionFile === "string" ? event.sessionFile : undefined,
    model: typeof event?.model === "string" ? event.model : undefined,
    agent_id: typeof event?.agentId === "string" ? event.agentId : undefined,
    total_messages: Number.isInteger(event?.totalMessages) ? event.totalMessages : undefined,
    quarantined_count: Number.isInteger(event?.quarantinedCount) ? event.quarantinedCount : undefined,
    orphans: Array.isArray(event?.orphans) ? event.orphans.map((orphan) => ({
      message_index: Number.isInteger(orphan?.messageIndex) ? orphan.messageIndex : undefined,
      tool_call_id: typeof orphan?.toolCallId === "string" ? orphan.toolCallId : undefined,
      provider_tool_call_id: typeof orphan?.providerToolCallId === "string" ? orphan.providerToolCallId : undefined,
      tool_name: typeof orphan?.toolName === "string" ? orphan.toolName : undefined,
      previous_role: typeof orphan?.previousRole === "string" ? orphan.previousRole : undefined,
      next_role: typeof orphan?.nextRole === "string" ? orphan.nextRole : undefined,
      nearest_assistant_index: Number.isInteger(orphan?.nearestAssistantIndex) ? orphan.nearestAssistantIndex : undefined,
      nearest_assistant_stop_reason: typeof orphan?.nearestAssistantStopReason === "string" ? orphan.nearestAssistantStopReason : undefined,
      nearest_assistant_tool_call_ids: Array.isArray(orphan?.nearestAssistantToolCallIds) ? orphan.nearestAssistantToolCallIds : undefined,
      matching_tool_call_later: orphan?.matchingToolCallLater === true,
    })) : [],
  };
  appendBoundedJsonl(record, { path, maxBytes, keepBytes });
}

function childSessionIds() {
  if (!(globalThis[CHILD_SESSION_IDS] instanceof Set)) {
    globalThis[CHILD_SESSION_IDS] = new Set();
  }
  return globalThis[CHILD_SESSION_IDS];
}

function childSessionId(event) {
  return typeof event?.sessionId === "string" && event.sessionId.length > 0
    ? event.sessionId
    : undefined;
}

const SETTLED_NOTIFICATION_COOLDOWN_MS = 120_000;
const ACTIVITY_HEARTBEAT_INTERVAL_MS = 5_000;
const ACTIVITY_REFRESH_MIN_MS = 10_000;
const EVENT_TRACKED_MISSING_LIMIT_MS = 60_000;
const MAX_ACTIVITY_SUBAGENTS = 999;
const MAX_TERMINAL_SUBAGENT_TOMBSTONES = 2048;

// Managed workers use background subagents, whose native started and terminal
// events share a run ID. Foreground runs lack a matching terminal ID, so they
// remain an upstream lifecycle limitation rather than a heuristic target.
export function createSettledNotificationScheduler({
  setTimeoutImpl = setTimeout,
  clearTimeoutImpl = clearTimeout,
} = {}) {
  if (typeof setTimeoutImpl !== "function" || typeof clearTimeoutImpl !== "function") {
    throw new TypeError("settled notification timers are required");
  }

  const children = new Set();
  const timers = new Set();
  let generation = 0;
  let parentSettled = false;

  function invalidate() {
    generation += 1;
    for (const timer of timers) clearTimeoutImpl(timer);
    timers.clear();
  }

  function schedule(delay, expectedGeneration, send) {
    let timer;
    timer = setTimeoutImpl(() => {
      timers.delete(timer);
      if (generation !== expectedGeneration || children.size > 0) return;
      send();
    }, delay);
    timers.add(timer);
    timer?.unref?.();
  }

  return {
    onParentActivity() {
      parentSettled = false;
      invalidate();
    },
    onParentSettled(send) {
      if (parentSettled || typeof send !== "function") return;
      parentSettled = true;
      if (children.size > 0) return;

      invalidate();
      const settledGeneration = generation;
      schedule(SETTLED_NOTIFICATION_COOLDOWN_MS, settledGeneration, send);
    },
    onChildStarted(sessionId) {
      if (typeof sessionId !== "string" || sessionId.length === 0 || children.has(sessionId)) return;
      children.add(sessionId);
      invalidate();
    },
    onChildDisposed(sessionId) {
      if (typeof sessionId !== "string" || sessionId.length === 0) return;
      children.delete(sessionId);
    },
    dispose() {
      invalidate();
      children.clear();
      parentSettled = false;
    },
    activeChildCount() {
      return children.size;
    },
  };
}

export function transientChildAgentId(sessionId, generateId = createNanoId) {
  if (typeof sessionId !== "string" || sessionId.length === 0 || typeof generateId !== "function") {
    throw new TypeError("a child session ID and ID generator are required");
  }
  return `subagent-${generateId()}`;
}

export function resolveAgentId(
  environment = process.env,
  { queryWindowName = queryTmuxWindowName } = {},
) {
  if (typeof environment.TMUX !== "string" || environment.TMUX.length === 0
    || typeof environment.TMUX_PANE !== "string" || !/^%\d+$/.test(environment.TMUX_PANE)) {
    return undefined;
  }
  const agentId = queryWindowName(environment);
  return assertAgentId(typeof agentId === "string" ? agentId.trim() : agentId);
}

export function queryTmuxWindowName(
  environment = process.env,
  { execFileSync: exec = execFileSync } = {},
) {
  const paneId = environment.TMUX_PANE;
  if (typeof paneId !== "string" || !/^%\d+$/.test(paneId)) {
    throw new Error("Mailbox v2 could not identify the current tmux pane");
  }
  try {
    const windowName = exec("tmux", ["display-message", "-p", "-t", paneId, "#{window_name}"], {
      encoding: "utf8",
      stdio: ["ignore", "pipe", "ignore"],
    });
    return windowName.trim();
  } catch {
    throw new Error("Mailbox v2 could not read the current tmux window");
  }
}

export function routeMailboxEnvelope(envelope, inject) {
  inject(envelope);
}

function sameDeliveryItem(left, right) {
  return left?.messageId === right?.messageId
    && left?.messageIncarnationId === right?.messageIncarnationId
    && left?.attemptNumber === right?.attemptNumber
    && left?.deliveryExpiresAt === right?.deliveryExpiresAt;
}

function isMailboxSessionEntry(entry, messageId, deliveryItems = undefined) {
  if (entry?.type !== "custom_message"
    || entry.customType !== MAILBOX_MESSAGE_TYPE
    || entry.details?.message_id !== messageId) return false;
  if (!Array.isArray(deliveryItems) || deliveryItems.length === 0) return true;
  const persistedItems = entry.details?.delivery_items;
  return Array.isArray(persistedItems)
    && persistedItems.length === deliveryItems.length
    && deliveryItems.every((item, index) => sameDeliveryItem(persistedItems[index], item));
}


function readPersistedSessionEntries(sessionFile, readSessionFile) {
  const source = readSessionFile(sessionFile, "utf8");
  return source.split("\n")
    .filter((line) => line.length > 0)
    .map((line) => JSON.parse(line));
}

export function mailboxSessionDeliveryState(sessionManager, messageId, {
  deliveryItems = undefined,
  readSessionFile = readFileSync,
  onPersistenceError = () => {},
} = {}) {
  const branch = sessionManager.getBranch();
  const hasBranchMessage = branch.some((entry) => isMailboxSessionEntry(entry, messageId));
  if (!hasBranchMessage) return "absent";
  if (!branch.some((entry) => isMailboxSessionEntry(entry, messageId, deliveryItems))) return "history_unverified";
  if (!sessionManager.isPersisted()) return "history_unverified";
  const sessionFile = sessionManager.getSessionFile();
  if (typeof sessionFile !== "string" || sessionFile.length === 0) return "history_unverified";
  try {
    return readPersistedSessionEntries(sessionFile, readSessionFile)
      .some((entry) => isMailboxSessionEntry(entry, messageId, deliveryItems))
      ? "durable"
      : "history_unverified";
  } catch (error) {
    if (error?.code !== "ENOENT") onPersistenceError(error);
    return "history_unverified";
  }
}

function mailboxCustomMessage(message, delivery = undefined) {
  const subject = message.subject === undefined ? "" : `\nSubject: ${message.subject}`;
  return {
    customType: MAILBOX_MESSAGE_TYPE,
    content: `Mailbox message from ${message.from}${subject}\n\n${message.body}`,
    display: true,
    details: {
      message_id: message.id,
      ...(Array.isArray(delivery?.items) && delivery.items.length > 0 ? { delivery_items: delivery.items } : {}),
      from: message.from,
      to: message.to,
      ...(message.subject === undefined ? {} : { subject: message.subject }),
    },
  };
}

function providerToolCallId(toolCallId) {
  return typeof toolCallId === "string" && toolCallId.length > 0 ? toolCallId.split("|", 1)[0] : undefined;
}

const VALID_TOOL_NAME_PATTERN = /^[a-zA-Z0-9_-]+$/;
const MAX_TOOL_NAME_LENGTH = 64;

function isValidToolName(name) {
  return typeof name === "string"
    && name.length > 0
    && name.length <= MAX_TOOL_NAME_LENGTH
    && VALID_TOOL_NAME_PATTERN.test(name);
}

function quarantineMalformedToolCalls(messages) {
  const sanitized = [];
  const malformed = [];

  for (const [index, message] of messages.entries()) {
    if (message?.role === "assistant" && Array.isArray(message.content)) {
      const sanitizedContent = [];
      for (const [blockIndex, block] of message.content.entries()) {
        if (block?.type === "toolCall" || block?.type === "tool_use") {
          const name = block.name;
          if (!isValidToolName(name)) {
            malformed.push({
              messageIndex: index,
              blockIndex,
              toolName: name,
              toolNameLength: typeof name === "string" ? name.length : undefined,
              toolCallId: block.id,
            });
            continue; // Skip this malformed tool call
          }
        }
        sanitizedContent.push(block);
      }
      sanitized.push({ ...message, content: sanitizedContent });
    } else {
      sanitized.push(message);
    }
  }

  return { messages: sanitized, malformed };
}

function assistantToolCallIds(message) {
  if (message?.role !== "assistant"
    || message.stopReason === "error"
    || message.stopReason === "aborted"
    || !Array.isArray(message.content)) return [];
  return message.content
    .map((block) => block?.type === "toolCall" ? providerToolCallId(block.id) : undefined)
    .filter((toolCallId) => toolCallId !== undefined);
}

export function quarantineOrphanToolResults(messages) {
  const seenToolCalls = new Set();
  const orphaned = [];
  const sanitized = [];
  let nearestAssistant;
  let nearestAssistantIndex;

  for (const [index, message] of messages.entries()) {
    if (message?.role === "toolResult") {
      const toolCallId = providerToolCallId(message.toolCallId);
      if (toolCallId !== undefined && !seenToolCalls.has(toolCallId)) {
        orphaned.push({
          messageIndex: index,
          toolCallId: message.toolCallId,
          providerToolCallId: toolCallId,
          toolName: message.toolName,
          previousRole: safeMessageRole(messages[index - 1]),
          nextRole: safeMessageRole(messages[index + 1]),
          nearestAssistantIndex,
          nearestAssistantStopReason: safeStopReason(nearestAssistant),
          nearestAssistantToolCallIds: messageToolCallIds(nearestAssistant),
          matchingToolCallLater: messages.slice(index + 1).some((candidate) => messageToolCallIds(candidate).includes(toolCallId)),
        });
        continue;
      }
    }

    sanitized.push(message);
    if (message?.role === "assistant") {
      nearestAssistant = message;
      nearestAssistantIndex = index;
    }
    for (const seenToolCallId of assistantToolCallIds(message)) seenToolCalls.add(seenToolCallId);
  }

  return orphaned.length === 0
    ? { messages, orphaned }
    : { messages: sanitized, orphaned };
}

export async function spawnNativeSentinel(message, service = globalThis[PI_SUBAGENTS_SERVICE]) {
  if (service === undefined || typeof service.spawn !== "function") {
    throw new Error("Pi native subagent service is unavailable");
  }
  return service.spawn("general-purpose", message.body, {
    description: "Scheduled Sentinel work",
    model: message.sentinel.model,
    inheritContext: true,
  });
}

export function captureNativeSentinelSpawner(service = globalThis[PI_SUBAGENTS_SERVICE]) {
  return (message) => spawnNativeSentinel(message, service);
}

export function createPiMailboxDeliveryCoordinator({
  pi,
  sessionContext,
  client,
  defer = setImmediate,
  setTimeoutImpl = setTimeout,
  clearTimeoutImpl = clearTimeout,
  checkpointRetryDelayMs = DEFAULT_SESSION_CHECKPOINT_RETRY_MS,
  pendingReinjectionDelayMs = DEFAULT_PENDING_REINJECTION_DELAY_MS,
  readSessionFile = readFileSync,
  spawnSubagent = spawnNativeSentinel,
  now = Date.now,
} = {}) {
  if (pi === undefined || typeof pi.sendMessage !== "function") {
    throw new TypeError("pi.sendMessage is required");
  }
  if (sessionContext?.sessionManager === undefined) {
    throw new TypeError("a Pi session context is required");
  }
  if (client === undefined || typeof client.sendReceipt !== "function") {
    throw new TypeError("a mailbox client with sendReceipt is required");
  }
  if (typeof defer !== "function") throw new TypeError("defer must be a function");
  if (typeof setTimeoutImpl !== "function" || typeof clearTimeoutImpl !== "function") {
    throw new TypeError("session checkpoint timers are required");
  }
  if (!Number.isSafeInteger(checkpointRetryDelayMs) || checkpointRetryDelayMs <= 0) {
    throw new TypeError("checkpointRetryDelayMs must be a positive safe integer");
  }
  if (!Number.isSafeInteger(pendingReinjectionDelayMs) || pendingReinjectionDelayMs <= 0) {
    throw new TypeError("pendingReinjectionDelayMs must be a positive safe integer");
  }
  if (typeof readSessionFile !== "function") throw new TypeError("readSessionFile must be a function");
  if (typeof now !== "function") throw new TypeError("now must be a function");
  if (typeof spawnSubagent !== "function") throw new TypeError("spawnSubagent must be a function");

  const sessionId = sessionContext.sessionManager.getSessionId();
  const pending = new Map();
  const announcedArrivals = new Set();
  const sentinelInFlight = new Map();
  const reportedPersistenceErrors = new Set();
  let active = true;

  function receipt(type, delivery, reason) {
    client.sendReceipt(type, delivery, reason);
  }

  function persistenceUnavailable(context) {
    context.ui?.setStatus("mecha-mailbox", "mailbox: waiting for durable session checkpoint");
  }

  function sessionDeliveryState(context, messageId, deliveryItems = undefined) {
    return mailboxSessionDeliveryState(context.sessionManager, messageId, {
      deliveryItems,
      readSessionFile,
      onPersistenceError: (error) => {
        if (reportedPersistenceErrors.has(messageId)) return;
        reportedPersistenceErrors.add(messageId);
        context.ui?.notify(`Mailbox v2 session checkpoint unreadable: ${error.message}`, "error");
      },
    });
  }

  function confirmExistingDelivery(delivery, context) {
    const state = sessionDeliveryState(context, delivery.messageId, delivery.items);
    if (state !== "durable") {
      persistenceUnavailable(context);
      return false;
    }
    try {
      receipt("injection_requested", delivery);
      receipt("ack", delivery);
      context.ui?.setStatus("mecha-mailbox", "mailbox: connected");
      return true;
    } catch {
      // The persisted session entry makes the next router replay safe to acknowledge.
      return false;
    }
  }

  function clearCheckpointTimer(pendingDelivery) {
    if (pendingDelivery.checkpointTimer !== undefined) {
      clearTimeoutImpl(pendingDelivery.checkpointTimer);
      pendingDelivery.checkpointTimer = undefined;
    }
  }

  function checkDurableCheckpoint(messageId, context) {
    if (!active || context.sessionManager.getSessionId() !== sessionId) return;
    const latest = pending.get(messageId);
    if (latest === undefined) return;
    const state = sessionDeliveryState(context, messageId, latest.delivery.items);
    if (state === "durable") {
      clearCheckpointTimer(latest);
      pending.delete(messageId);
      reportedPersistenceErrors.delete(messageId);
      try {
        receipt("ack", latest.delivery);
        context.ui?.setStatus("mecha-mailbox", "mailbox: connected");
      } catch {
        // The persisted session entry makes the next router replay safe to acknowledge.
      }
      return;
    }
    persistenceUnavailable(context);
    if (latest.checkpointTimer !== undefined) return;
    latest.checkpointTimer = setTimeoutImpl(() => {
      latest.checkpointTimer = undefined;
      checkDurableCheckpoint(messageId, context);
    }, checkpointRetryDelayMs);
  }

  function maybeSendMessageArrival(message) {
    if (announcedArrivals.has(message.id)) return;
    const sourceKind = message?.source?.kind;
    if (message.from === message.to || !["agent", "discord", "reminder"].includes(sourceKind)) return;
    if (typeof client.sendMessageArrival !== "function") return;
    client.sendMessageArrival(message);
    announcedArrivals.add(message.id);
  }

  function injectMailboxMessage(message, delivery) {
    routeMailboxEnvelope(message, (envelope) => {
      pi.sendMessage(mailboxCustomMessage(envelope, delivery), { deliverAs: "steer", triggerTurn: true });
    });
  }

  function shouldReinjectPending(pendingDelivery, delivery, state) {
    return state === "absent"
      && delivery.attemptNumber > pendingDelivery.lastInjectedAttemptNumber
      && now() - pendingDelivery.lastInjectedAt >= pendingReinjectionDelayMs;
  }

  function recordSentinelStatus(message, subagentId) {
    // ponytail: this status deliberately contains no scheduled prompt or derived text.
    pi.sendMessage({
      customType: "mecha-sentinel-status",
      content: "Sentinel work handed off to a native subagent.",
      display: false,
      details: { message_id: message.id, subagent_id: subagentId, state: "handed_off" },
    }, { deliverAs: "followUp", triggerTurn: false });
  }

  async function onSentinelEnvelope(message, delivery) {
    const existing = sentinelInFlight.get(message.id);
    if (existing !== undefined) return existing;
    const handoff = (async () => {
      let subagentId;
      try {
        subagentId = await spawnSubagent(message);
      } catch {
        try { receipt("nack", delivery, "injection_failed"); } catch { /* normal delivery timeout preserves replay */ }
        return;
      }
      if (typeof subagentId !== "string" || subagentId.length === 0) {
        try { receipt("nack", delivery, "injection_failed"); } catch { /* normal delivery timeout preserves replay */ }
        return;
      }
      try { recordSentinelStatus(message, subagentId); } catch { /* a local status must not duplicate a handed-off child */ }
      try {
        receipt("injection_requested", delivery);
        receipt("ack", delivery);
      } catch {
        // The native child already exists. The normal router delivery lifecycle decides any replay.
      }
    })();
    sentinelInFlight.set(message.id, handoff);
    return handoff;
  }

  function onEnvelope(message, delivery) {
    if (!active) return undefined;
    if (message.sentinel !== undefined) return onSentinelEnvelope(message, delivery);
    const currentContext = sessionContext;
    const state = sessionDeliveryState(currentContext, message.id, delivery.items);
    if (state === "durable") {
      confirmExistingDelivery(delivery, currentContext);
      return;
    }

    const existing = pending.get(message.id);
    if (existing !== undefined) {
      existing.delivery = delivery;
      if (shouldReinjectPending(existing, delivery, state)) {
        try {
          injectMailboxMessage(message, delivery);
          existing.lastInjectedAt = now();
          existing.lastInjectedAttemptNumber = delivery.attemptNumber;
        } catch {
          // Keep the original pending delivery; the router lease timeout preserves retry.
        }
      }
      try {
        receipt("injection_requested", delivery);
      } catch {
        // The original Pi injection remains pending; do not NACK it as a failed injection.
      }
      return;
    }
    if (state === "history_unverified") {
      persistenceUnavailable(currentContext);
      return;
    }

    pending.set(message.id, {
      delivery,
      checkpointTimer: undefined,
      lastInjectedAt: now(),
      lastInjectedAttemptNumber: delivery.attemptNumber,
    });
    try {
      injectMailboxMessage(message, delivery);
    } catch {
      pending.delete(message.id);
      try {
        receipt("nack", delivery, "injection_failed");
      } catch {
        // The router's lease timeout preserves retry when the socket is gone.
      }
      return;
    }
    try {
      maybeSendMessageArrival(message);
    } catch {
      // Arrival indicators are ephemeral UI hints; delivery receipts own replay safety.
    }
    // Keep this pending on a receipt-send failure: Pi may have queued the message already.
    try {
      receipt("injection_requested", delivery);
    } catch {
      // The router lease will time out or the reconnecting client will receive a replay.
    }
  }

  function onMessageEnd(event, context) {
    const messageId = event.message?.details?.message_id;
    if (event.message?.role !== "custom" || event.message.customType !== MAILBOX_MESSAGE_TYPE
      || typeof messageId !== "string") {
      return;
    }
    if (context.sessionManager.getSessionId() !== sessionId) return;
    const pendingDelivery = pending.get(messageId);
    if (pendingDelivery === undefined) return;

    defer(() => checkDurableCheckpoint(messageId, context));
  }

  return {
    onEnvelope,
    onMessageEnd,

    close() {
      active = false;
      for (const pendingDelivery of pending.values()) clearCheckpointTimer(pendingDelivery);
      pending.clear();
      announcedArrivals.clear();
      sentinelInFlight.clear();
      reportedPersistenceErrors.clear();
    },
  };
}

export function createMechaMessagingExtension(pi, {
  resolveCurrentAgentId = resolveAgentId,
  createClient = createMailboxClient,
  createSettledScheduler = createSettledNotificationScheduler,
  setTimeoutImpl = setTimeout,
  setIntervalImpl = setInterval,
  clearIntervalImpl = clearInterval,
  nowImpl = Date.now,
  subagentsService = globalThis[PI_SUBAGENTS_SERVICE],
} = {}) {
  let client;
  let coordinator;
  let mailboxPausedForCompaction = false;
  let startupError;
  let activeWork = ""; let recallAbort; let recallProcess; let policyAbort; let policyReady; let capture;
  const ensureCapture = async (signal) => { if (capture !== undefined) return capture; const response = await fetch(`${routerHttpUrl(process.env.MECHA_ROUTER_URL ?? DEFAULT_ROUTER_URL)}/v1/memory/policy`, { signal: signal ? AbortSignal.any([signal, AbortSignal.timeout(5_000)]) : AbortSignal.timeout(5_000) }); if (!response.ok) throw new Error("capture policy unavailable"); const { recall: policy } = await response.json(); capture = createMemoryCaptureLifecycle({ policy, context: async ({ signal } = {}) => { const lookup = await fetch(`${routerHttpUrl(process.env.MECHA_ROUTER_URL ?? DEFAULT_ROUTER_URL)}/v1/memory/lookup`, { method: "POST", headers: { "content-type": "application/json" }, signal: signal ? AbortSignal.any([signal, AbortSignal.timeout(policy.capture.router_timeout_ms)]) : AbortSignal.timeout(policy.capture.router_timeout_ms), body: JSON.stringify({ stores: ["default"] }) }); if (!lookup.ok) throw new Error("capture destination unavailable"); const { records } = await lookup.json(); return records; }, apply: async (body, signal) => { const applied = await fetch(`${routerHttpUrl(process.env.MECHA_ROUTER_URL ?? DEFAULT_ROUTER_URL)}/v1/memory/apply`, { method: "POST", headers: { "content-type": "application/json" }, signal: signal ? AbortSignal.any([signal, AbortSignal.timeout(policy.capture.router_timeout_ms)]) : AbortSignal.timeout(policy.capture.router_timeout_ms), body: JSON.stringify(body) }); if (!applied.ok) throw new Error("capture apply failed"); return applied.json(); }, diagnostics: appendMemoryCaptureDiagnostic }); return capture; };
  const ensureRecallPolicy = async () => { if (policyReady !== undefined) return policyReady; policyAbort = new AbortController(); const timeout = setTimeout(() => policyAbort?.abort(), 5_000); policyReady = fetch(`${routerHttpUrl(process.env.MECHA_ROUTER_URL ?? DEFAULT_ROUTER_URL)}/v1/memory/policy`, { signal: policyAbort.signal }).then((response) => { if (!response.ok) throw new Error("recall policy unavailable"); return response.json(); }).then(({ recall: policy }) => { recallController.configure(policy); return policy; }).finally(() => clearTimeout(timeout)).catch((error) => { policyReady = undefined; throw error; }); return policyReady; };
  let recall; let recallConfigurationError;
  try { recall = createRecallState(); } catch (error) { recallConfigurationError = error; recall = createRecallState({}); }
  let recallPipeline;
  const reportedOrphanToolResults = new Set();
  const reportedMalformedToolCalls = new Set();

  function appendMalformedToolCallDiagnostic({ sessionId, sessionFile, model, agentId, totalMessages, malformedCount, malformed }) {
    try {
      const ts = new Date().toISOString();
      const payload = {
        timestamp: ts,
        sessionId,
        sessionFile,
        model,
        agentId,
        totalMessages,
        malformedCount,
        malformed: malformed.slice(0, 20).map((m) => ({
          messageIndex: m.messageIndex,
          blockIndex: m.blockIndex,
          toolName: typeof m.toolName === "string" ? m.toolName.slice(0, 100) : String(m.toolName),
          toolNameLength: m.toolNameLength,
          toolCallId: m.toolCallId,
        })),
      };
      appendDiagnostic("malformed-tool-call", payload);
    } catch {}
  }

  const recallController = createRecallController({
    cancel: () => { policyAbort?.abort(); recallAbort?.abort(); recallProcess?.kill(); },
    evaluate: async (active) => {
      const status = recall.status();
      if (!status.enabled || !active()) return undefined;
      recallPipeline = createPipeline({ routerUrl: process.env.MECHA_ROUTER_URL ?? DEFAULT_ROUTER_URL, stores: status.stores, workSnapshot: activeWork, evaluate: evaluateRecall, validateChoice, onPolicy: (policy) => recallController.configure(policy), registerAbort: (controller) => { recallAbort = controller; }, registerProcess: (child) => { recallProcess = child; }, send: pi.sendMessage.bind(pi) });
      return recallPipeline.evaluate(active);
    },
    recall: (choice, active) => recallPipeline?.recall(choice, active),
  });
  let settledNotificationAgentId;
  let settledNotificationPending = false;
  let activityHeartbeatTimer;
  let activityHeartbeatActive = false;
  let parentActivityContext;
  let lastActivitySent;
  const runningSubagents = new Map();
  const terminalSubagentTombstones = new Map();
  let settledNotificationRecipient;
  let settledNotificationEnabled = false;
  let compactionPolicy;
  let compactionPolicyError;
  try { compactionPolicy = compactionPolicyFromEnvironment(); } catch (error) { compactionPolicyError = error; compactionPolicy = resolveCompactionPolicy(DEFAULT_COMPACTION_POLICY); }
  let notifiedCompactionPercent = 0;
  let pendingSelfCompaction;

  function normalizeSubagentMetadata(event, startedAtMs = nowImpl()) {
    const id = typeof event?.id === "string" ? event.id.trim().slice(0, 128) : "";
    if (id.length === 0) return undefined;
    const description = typeof event?.description === "string" ? event.description.trim().slice(0, 512) : "";
    const type = typeof event?.type === "string" ? event.type.trim().slice(0, 64) : "";
    return {
      id,
      ...(type.length > 0 ? { type } : {}),
      ...(description.length > 0 ? { description } : {}),
      startedAtMs,
      lastSeenMs: startedAtMs,
    };
  }

  function publicSubagentSummaries() {
    return [...runningSubagents.values()]
      .filter(({ description }) => typeof description === "string" && description.length > 0)
      .sort((left, right) => left.startedAtMs - right.startedAtMs || left.id.localeCompare(right.id))
      .slice(0, MAX_ACTIVITY_SUBAGENTS)
      .map(({ id, type, description }) => ({ id, ...(type !== undefined ? { type } : {}), description }));
  }

  function sameSubagentMetadata(left, right) {
    return left !== undefined && right !== undefined && left.id === right.id && left.type === right.type && left.description === right.description;
  }

  function nativeSubagentRecords(service = globalThis[PI_SUBAGENTS_SERVICE] ?? subagentsService) {
    try {
      const source = typeof service?.listAgents === "function"
        ? service.listAgents()
        : typeof service?.getRunning === "function"
          ? service.getRunning()
          : service?.running ?? service?.runs ?? service?.subagents;
      return Array.isArray(source) ? source : undefined;
    } catch {
      return undefined;
    }
  }

  function nativeSubagentStatus(record) {
    return typeof record?.status === "string" ? record.status.toLowerCase() : record?.status;
  }

  function isActiveNativeSubagentRecord(record) {
    return [undefined, "running", "queued", "active"].includes(nativeSubagentStatus(record));
  }

  function isTerminalNativeSubagentRecord(record) {
    return [
      "completed", "complete", "done", "finished", "failed", "failure", "error", "errored",
      "canceled", "cancelled", "aborted", "terminated", "stopped",
    ].includes(nativeSubagentStatus(record));
  }

  function nativeSubagentExplicitStartedAtMs(record) {
    if (Number.isFinite(record?.startedAt)) return record.startedAt;
    if (typeof record?.startedAt === "string") {
      const parsed = Date.parse(record.startedAt);
      if (Number.isFinite(parsed)) return parsed;
    }
    return undefined;
  }

  function nativeSubagentStartedAtMs(record, fallbackMs) {
    return nativeSubagentExplicitStartedAtMs(record) ?? fallbackMs;
  }

  function rememberTerminalSubagent(id, terminalAtMs = nowImpl()) {
    if (typeof id !== "string" || id.length === 0) return false;
    terminalSubagentTombstones.delete(id);
    terminalSubagentTombstones.set(id, terminalAtMs);
    while (terminalSubagentTombstones.size > MAX_TERMINAL_SUBAGENT_TOMBSTONES) {
      terminalSubagentTombstones.delete(terminalSubagentTombstones.keys().next().value);
    }
    return runningSubagents.delete(id);
  }

  function clearTerminalSubagentTombstone(id) {
    if (typeof id === "string" && id.length > 0) terminalSubagentTombstones.delete(id);
  }

  function isTerminalTombstonedNativeRecord(id, record) {
    const terminalAtMs = terminalSubagentTombstones.get(id);
    if (terminalAtMs === undefined) return false;
    const startedAtMs = nativeSubagentExplicitStartedAtMs(record);
    if (startedAtMs !== undefined && startedAtMs > terminalAtMs) {
      terminalSubagentTombstones.delete(id);
      return false;
    }
    return true;
  }

  function mergeNativeSubagentMetadata(existing, metadata, nowMs) {
    if (existing?.eventTracked === true) {
      return {
        id: existing.id,
        type: existing.type ?? metadata.type,
        description: existing.description ?? metadata.description,
        eventTracked: true,
        serviceTracked: true,
        startedAtMs: existing.startedAtMs ?? metadata.startedAtMs,
        lastSeenMs: nowMs,
      };
    }
    return {
      ...metadata,
      eventTracked: false,
      serviceTracked: true,
      startedAtMs: existing?.startedAtMs ?? metadata.startedAtMs,
      lastSeenMs: nowMs,
    };
  }

  function reconcileNativeSubagentService() {
    const source = nativeSubagentRecords();
    if (!Array.isArray(source)) return false;
    const nowMs = nowImpl();
    const serviceIds = new Set();
    let changed = false;
    // The service list can lag behind lifecycle events, so event-tracked children
    // stay visible until a terminal status or lifecycle event removes them.
    for (const record of source) {
      const metadata = normalizeSubagentMetadata(record, nativeSubagentStartedAtMs(record, nowMs));
      if (metadata === undefined) continue;
      serviceIds.add(metadata.id);
      if (isTerminalNativeSubagentRecord(record)) {
        if (rememberTerminalSubagent(metadata.id, nowMs)) changed = true;
        settledNotifications.onChildDisposed(metadata.id);
        continue;
      }
      if (!isActiveNativeSubagentRecord(record)) continue;
      if (isTerminalTombstonedNativeRecord(metadata.id, record)) {
        if (runningSubagents.delete(metadata.id)) changed = true;
        continue;
      }
      const existing = runningSubagents.get(metadata.id);
      const merged = mergeNativeSubagentMetadata(existing, metadata, nowMs);
      if (sameSubagentMetadata(existing, merged)) {
        existing.lastSeenMs = nowMs;
        existing.serviceTracked = true;
        continue;
      }
      runningSubagents.set(metadata.id, merged);
      changed = true;
    }
    for (const [id, metadata] of runningSubagents.entries()) {
      if (!serviceIds.has(id) && metadata?.serviceTracked === true && metadata?.eventTracked !== true) {
        runningSubagents.delete(id);
        changed = true;
      } else if (!serviceIds.has(id) && metadata?.eventTracked === true
        && nowMs - metadata.lastSeenMs >= EVENT_TRACKED_MISSING_LIMIT_MS) {
        // Lifecycle events can arrive before the native service publishes a record. Keep that
        // child through ordinary service-list lag, then age it out if the service still cannot
        // find it (for example, after Escape killed the child without a terminal event).
        runningSubagents.delete(id);
        settledNotifications.onChildDisposed(id);
        changed = true;
      }
    }
    return changed;
  }

  function rememberParentActivityContext(context) {
    if (context !== undefined) parentActivityContext = context;
  }

  function parentActivityStateFromPiContext() {
    // Precedence is deliberate: compaction masks ordinary work, Pi's live idle
    // bit wins when available, and the latch is only the older-context fallback.
    if (mailboxPausedForCompaction) return "compacting";
    if (typeof parentActivityContext?.isIdle === "function") {
      return parentActivityContext.isIdle() ? "settled" : "active";
    }
    return undefined;
  }

  function parentActivityStateFromContext() {
    return parentActivityStateFromPiContext() ?? (activityHeartbeatActive ? "active" : "settled");
  }

  function sendActivityState(state, { force = false, reconcile = true } = {}) {
    // Compaction owns the visible state until its lifecycle resumes or cancels;
    // ordinary settled/active frames must not clear that pause early.
    if (mailboxPausedForCompaction && state !== "compacting") return;
    const nowMs = nowImpl();
    const reconciled = reconcile ? reconcileNativeSubagentService() : false;
    const subagents = publicSubagentSummaries();
    const subagentCount = Math.min(runningSubagents.size, MAX_ACTIVITY_SUBAGENTS);
    if (lastActivitySent !== undefined
      && lastActivitySent.state === state
      && lastActivitySent.subagentCount === subagentCount
      && JSON.stringify(lastActivitySent.subagents) === JSON.stringify(subagents)
      && !force
      && !reconciled
      && nowMs - lastActivitySent.sentAtMs < ACTIVITY_REFRESH_MIN_MS) {
      return false;
    }
    try {
      client?.sendActivity?.(state, { subagentCount, subagents });
      lastActivitySent = { state, subagentCount, subagents, sentAtMs: nowMs };
      return true;
    } catch {
      // Activity is an ephemeral UI hint; mailbox delivery and reconnect behavior own real work.
      return false;
    }
  }

  function ensureActivityHeartbeatTimer() {
    if (activityHeartbeatTimer !== undefined) return;
    activityHeartbeatTimer = setIntervalImpl(() => {
      const state = parentActivityStateFromContext();
      sendActivityState(state);
      if (state === "settled" && runningSubagents.size === 0) stopActivityHeartbeat();
    }, ACTIVITY_HEARTBEAT_INTERVAL_MS);
    activityHeartbeatTimer?.unref?.();
  }

  function clearActivityHeartbeatTimer() {
    if (activityHeartbeatTimer !== undefined) {
      clearIntervalImpl(activityHeartbeatTimer);
      activityHeartbeatTimer = undefined;
    }
  }

  function stopActivityHeartbeat({ settle = false, context } = {}) {
    rememberParentActivityContext(context);
    if (settle && parentActivityStateFromPiContext() === "active") {
      // A settled event can race a queued retry or continuation. If Pi still
      // reports active, keep polling instead of publishing a stale stop.
      activityHeartbeatActive = true;
      ensureActivityHeartbeatTimer();
      return;
    }
    const wasActive = activityHeartbeatActive;
    activityHeartbeatActive = false;
    if (wasActive && settle && !mailboxPausedForCompaction) {
      sendActivityState(parentActivityStateFromContext(), { force: true });
    }
    if (runningSubagents.size > 0) return;
    clearActivityHeartbeatTimer();
  }

  function clearRunningSubagentsForBoundary() {
    runningSubagents.clear();
    activityHeartbeatActive = false;
    if (!mailboxPausedForCompaction) sendActivityState("settled", { force: true, reconcile: false });
    clearActivityHeartbeatTimer();
  }

  function startActivityHeartbeat(context) {
    rememberParentActivityContext(context);
    activityHeartbeatActive = true;
    sendActivityState(parentActivityStateFromContext(), { force: true });
    ensureActivityHeartbeatTimer();
  }

  function refreshActivityState(context) {
    rememberParentActivityContext(context);
    sendActivityState(parentActivityStateFromContext(), { force: true });
    if (runningSubagents.size > 0) ensureActivityHeartbeatTimer();
    else if (!activityHeartbeatActive) stopActivityHeartbeat();
  }

  function createMailboxCoordinator(context, mailboxClient) {
    return createPiMailboxDeliveryCoordinator({
      pi,
      sessionContext: context,
      client: mailboxClient,
      spawnSubagent: captureNativeSentinelSpawner(),
    });
  }

  function pauseMailboxForCompaction(context) {
    if (children.has(context.sessionManager.getSessionId()) || client === undefined || mailboxPausedForCompaction) return;
    rememberParentActivityContext(context);
    mailboxPausedForCompaction = true;
    clearRunningSubagentsForBoundary();
    sendActivityState("compacting", { force: true, reconcile: false });
    context.ui?.setStatus("mecha-mailbox", "mailbox: compacting");
  }

  function resumeMailboxAfterCompaction(context) {
    if (children.has(context.sessionManager.getSessionId()) || client === undefined || !mailboxPausedForCompaction) return;
    rememberParentActivityContext(context);
    mailboxPausedForCompaction = false;
    reconcileNativeSubagentService();
    refreshActivityState();
    context.ui?.setStatus("mecha-mailbox", "mailbox: connected");
  }

  function compactionStatus(context) {
    const usage = context?.getContextUsage?.();
    const effective = effectiveCompactionPolicy(compactionPolicy, context?.model);
    return {
      policy: compactionPolicy,
      effectivePolicy: effective.policy,
      modelKeys: effective.modelKeys,
      matchedModelKey: effective.matchedModelKey,
      selectedTier: usage === undefined ? undefined : selectCompactionTier(compactionPolicy, usage.contextWindow, context?.model),
      usage,
      notifiedThroughPercent: notifiedCompactionPercent,
      configurationError: compactionPolicyError?.message,
    };
  }

  function sendPostCompactionNotification(note, result) {
    const normalizedNote = normalizePostCompactionNotification(note);
    if (normalizedNote === undefined) return;
    const message = {
      customType: POST_COMPACTION_MESSAGE_TYPE,
      content: postCompactionNotificationMessage(normalizedNote, result),
      display: true,
      details: {
        tokens_before: Number.isFinite(result?.tokensBefore) ? result.tokensBefore : undefined,
        estimated_tokens_after: Number.isFinite(result?.estimatedTokensAfter) ? result.estimatedTokensAfter : undefined,
        first_kept_entry_id: typeof result?.firstKeptEntryId === "string" ? result.firstKeptEntryId : undefined,
        summary_chars: typeof result?.summary === "string" ? result.summary.length : undefined,
      },
    };
    setTimeoutImpl(() => {
      try {
        pi.sendMessage(message, { deliverAs: "followUp", triggerTurn: true });
      } catch {
        // A post-compaction handoff must not turn a successful compaction into a failed compact call.
      }
    }, 0);
  }

  function requestSelfCompaction(input, context) {
    if (typeof context?.compact !== "function") throw new Error("Pi compaction is unavailable in this context");
    const handoffNote = input.postCompactionNotification ?? effectiveCompactionPolicy(compactionPolicy, context?.model).policy.postCompactionNotification;
    pendingSelfCompaction = {
      customInstructions: input.customInstructions,
      postCompactionNotification: normalizePostCompactionNotification(handoffNote),
    };
    return pendingSelfCompaction;
  }

  function runPendingSelfCompaction(context) {
    if (pendingSelfCompaction === undefined || children.has(context.sessionManager.getSessionId())) return;
    if (typeof context?.compact !== "function") {
      pendingSelfCompaction = undefined;
      context.ui?.notify("Self-compaction is unavailable at the idle boundary; use /compact manually.", "error");
      return;
    }
    if (typeof context.isIdle === "function" && !context.isIdle()) return;
    const request = pendingSelfCompaction;
    pendingSelfCompaction = undefined;
    context.compact({
      customInstructions: request.customInstructions,
      onComplete: (result) => {
        resumeMailboxAfterCompaction(context);
        notifiedCompactionPercent = 0;
        sendPostCompactionNotification(request.postCompactionNotification, result);
      },
      onError: (error) => {
        resumeMailboxAfterCompaction(context);
        context.ui?.notify(`Self-compaction failed: ${error.message}`, "error");
      },
    });
  }

  function monitorContextThreshold(context) {
    if (children.has(context.sessionManager.getSessionId())) return;
    const usage = context.getContextUsage?.();
    if (usage?.percent === null || usage?.percent === undefined) return;
    const level = nextCompactionThreshold(compactionPolicy, { ...usage, model: context.model }, notifiedCompactionPercent);
    if (level === undefined) return;
    notifiedCompactionPercent = level.percent;
    pi.sendMessage({
      customType: CONTEXT_THRESHOLD_MESSAGE_TYPE,
      content: compactionNotificationMessage({ level, usage }),
      display: true,
      details: {
        percent: usage.percent,
        tokens: usage.tokens,
        context_window: usage.contextWindow,
        threshold_percent: level.percent,
        tier_max_context_tokens: level.tier.maxContextTokens,
        mandatory: level.mandatory === true,
      },
    }, { deliverAs: "followUp", triggerTurn: true });
  }
  try {
    settledNotificationRecipient = assertAgentId(process.env.MECHA_PI_NOTIFY_ON_SETTLED);
  } catch {
    // The launcher rejects invalid values; a manually configured Pi stays disabled.
  }
  const children = childSessionIds();
  const settledNotifications = createSettledScheduler();
  const childReplyClients = new Map();
  const inlineToolPolicy = createInlineToolPolicy();

  pi.registerTool({
    name: "mecha_parent_message",
    label: "Message parent",
    description: "Child-only: send an ordinary Mailbox message to this native subagent's target parent.",
    parameters: {
      type: "object",
      additionalProperties: false,
      required: ["message"],
      properties: { message: { type: "string", minLength: 1 } },
    },
    async execute(_toolCallId, { message }, _signal, _onUpdate, toolContext) {
      const childClient = childReplyClients.get(toolContext.sessionManager.getSessionId());
      const parentAgentId = resolveCurrentAgentId();
      if (childClient === undefined || parentAgentId === undefined || typeof message !== "string" || message.length === 0) {
        return { content: [{ type: "text", text: "Parent message is unavailable in this session." }], isError: true };
      }
      try {
        const sent = await childClient.sendReply({ to: parentAgentId, body: message, subject: "Pi subagent message" });
        return { content: [{ type: "text", text: "Parent message accepted." }], details: { message_id: sent.id } };
      } catch {
        return { content: [{ type: "text", text: "Parent message was not accepted." }], isError: true };
      }
    },
  });

  pi.registerTool({
    name: INLINE_TOOL_ALLOWANCE_TOOL_NAME,
    label: "Request inline tool allowance",
    description: "Request a temporary exception for genuinely necessary inline use of a named tool. This is an exception to the rule; subagents remain mandatory by default.",
    parameters: {
      type: "object",
      additionalProperties: false,
      required: ["toolName", "usageCount"],
      properties: {
        toolName: { type: "string", minLength: 1, maxLength: 64, description: "Exact tool name to allow temporarily." },
        usageCount: { type: "integer", minimum: 1, maximum: 5, description: "Number of temporary uses to grant, from 1 through 5." },
      },
    },
    async execute(_toolCallId, input, _signal, _onUpdate, toolContext) {
      try {
        // Children already bypass the guard; keeping their requests out of this state prevents a child from granting parent-session credits.
        if (children.has(toolContext?.sessionManager?.getSessionId?.())) {
          return { content: [{ type: "text", text: "Native subagents are exempt from the inline tool guard; no parent allowance was changed." }] };
        }
        const grant = inlineToolPolicy.grant(input?.toolName, input?.usageCount);
        const text = `Granted ${grant.usageCount} temporary inline use(s) for ${grant.toolName}. This allowance is an exception to the rule; subagents remain mandatory by default.`;
        return { content: [{ type: "text", text }], details: grant };
      } catch (error) {
        return { content: [{ type: "text", text: error.message }], details: {}, isError: true };
      }
    },
  });

  if (typeof pi.registerTool === "function") pi.registerTool({
    name: "mecha_recall",
    label: "Mecha Recall",
    description: "Enable, disable, or inspect process-local Markdown recall.",
    parameters: { type: "object", additionalProperties: false, required: ["action"], properties: {
      action: { type: "string", enum: ["enable", "disable", "status"] },
      stores: { type: "array", items: { type: "string" } },
    } },
    async execute(_toolCallId, input) {
      try {
        if (recallConfigurationError !== undefined) throw recallConfigurationError;
        if (input.action === "enable") { recallController.stop(); recall.enable(input.stores); }
        else if (input.action === "disable") { recall.disable(); recallController.stop(); }
        else if (input.action !== "status") throw new Error("unsupported mecha_recall action");
        const status = recall.status();
        return { content: [{ type: "text", text: JSON.stringify(status) }], details: status };
      } catch (error) {
        return { content: [{ type: "text", text: error.message }], details: {}, isError: true };
      }
    },
  });

  pi.events.on("subagents:started", (event) => {
    const metadata = normalizeSubagentMetadata(event);
    clearTerminalSubagentTombstone(metadata?.id);
    const existing = metadata === undefined ? undefined : runningSubagents.get(metadata.id);
    const tracked = metadata === undefined ? undefined : {
      ...metadata,
      eventTracked: true,
      serviceTracked: existing?.serviceTracked === true,
      startedAtMs: existing?.startedAtMs ?? metadata.startedAtMs,
    };
    const changed = tracked !== undefined && !sameSubagentMetadata(existing, tracked);
    if (changed) runningSubagents.set(metadata.id, tracked);
    else if (existing !== undefined) existing.eventTracked = true;
    settledNotifications.onChildStarted(metadata?.id ?? event?.id);
    if (changed) refreshActivityState();
  });
  pi.events.on("subagents:completed", (event) => {
    const id = event?.id;
    const changed = rememberTerminalSubagent(id);
    settledNotifications.onChildDisposed(id);
    if (changed) refreshActivityState();
  });
  pi.events.on("subagents:failed", (event) => {
    const id = event?.id;
    const changed = rememberTerminalSubagent(id);
    settledNotifications.onChildDisposed(id);
    if (changed) refreshActivityState();
  });

  function settledNotificationStatus() {
    return {
      configured: settledNotificationRecipient !== undefined,
      enabled: settledNotificationEnabled,
      recipient: settledNotificationRecipient,
      pending: settledNotificationPending,
      cooldownMs: SETTLED_NOTIFICATION_COOLDOWN_MS,
    };
  }

  if (typeof pi.registerTool === "function") pi.registerTool({
    name: "mecha_settled_notifications",
    label: "Mecha Settled Notifications",
    description: "Enable, disable, or inspect this Pi process's managed-worker settled mailbox notifications.",
    parameters: { type: "object", additionalProperties: false, required: ["action"], properties: {
      action: { type: "string", enum: ["enable", "disable", "status"] },
    } },
    async execute(_toolCallId, input) {
      try {
        if (input.action === "disable") {
          settledNotificationEnabled = false;
          settledNotificationPending = false;
          settledNotifications.dispose();
        } else if (input.action === "enable") {
          if (settledNotificationRecipient === undefined) throw new Error("Settled notifications are not configured for this process.");
          settledNotificationEnabled = true;
        } else if (input.action !== "status") {
          throw new Error("unsupported mecha_settled_notifications action");
        }
        const status = settledNotificationStatus();
        return { content: [{ type: "text", text: JSON.stringify(status) }], details: status };
      } catch (error) {
        return { content: [{ type: "text", text: error.message }], details: settledNotificationStatus(), isError: true };
      }
    },
  });

  if (typeof pi.registerTool === "function") pi.registerTool({
    name: "mecha_context",
    label: "Mecha Context",
    description: "Inspect or configure Pi context-threshold notifications, or trigger self-compaction. Use action=compact only after a threshold notification or direct user instruction. postCompactionNotification is optional: include it while you are in the middle of an active task, review, or decision loop; omit it only when clearly idle/done and no details are needed. A default handoff note can be configured in the compaction policy, and a per-compact call can override it.",
    parameters: { type: "object", additionalProperties: false, required: ["action"], properties: {
      action: { type: "string", enum: ["status", "set_policy", "compact"], description: "status inspects usage/policy, set_policy changes thresholds/default handoff note, compact requests self-compaction." },
      policy: { oneOf: [
        { type: "object", additionalProperties: false, required: ["levels"], properties: {
          postCompactionNotification: { type: "string", description: "Optional default handoff note injected after successful compaction. Include it while you are in the middle of an active task, review, or decision loop; omit it only when clearly idle/done and no details are needed." },
          models: { type: "object" },
          levels: { type: "array", minItems: 1, items: { oneOf: [
            { type: "number", exclusiveMinimum: 0, exclusiveMaximum: 100 },
            { type: "object", additionalProperties: false, required: ["percent"], properties: {
              percent: { type: "number", exclusiveMinimum: 0, exclusiveMaximum: 100 },
              guidance: { type: "string", minLength: 1 },
              mandatory: { type: "boolean" },
            } },
          ] } },
        } },
        { type: "object", additionalProperties: false, required: ["tiers"], properties: {
          postCompactionNotification: { type: "string", description: "Optional default handoff note injected after successful compaction. Include it while you are in the middle of an active task, review, or decision loop; omit it only when clearly idle/done and no details are needed." },
          models: { type: "object" },
          tiers: { type: "array", minItems: 1, items: {
            type: "object", additionalProperties: false, required: ["levels"], properties: {
              maxContextTokens: { oneOf: [{ type: "integer", minimum: 1 }, { type: "null" }] },
              levels: { type: "array", minItems: 1, items: { oneOf: [
                { type: "number", exclusiveMinimum: 0, exclusiveMaximum: 100 },
                { type: "object", additionalProperties: false, required: ["percent"], properties: {
                  percent: { type: "number", exclusiveMinimum: 0, exclusiveMaximum: 100 },
                  guidance: { type: "string", minLength: 1 },
                  mandatory: { type: "boolean" },
                } },
              ] } },
            },
          } },
        } },
        { type: "array", minItems: 1, items: { type: "number", exclusiveMinimum: 0, exclusiveMaximum: 100 } },
      ] },
      customInstructions: { type: "string", description: "Optional instructions for Pi's compaction summary." },
      postCompactionNotification: { type: "string", description: "Optional per-compact handoff note. Include it while you are in the middle of an active task, review, or decision loop; omit it only when clearly idle/done and no details are needed. Overrides any default handoff note configured in the compaction policy for this compact call." },
    } },
    async execute(_toolCallId, input, _signal, _onUpdate, toolContext) {
      try {
        if (input.action === "set_policy") {
          compactionPolicy = resolveCompactionPolicy(input.policy);
          compactionPolicyError = undefined;
          notifiedCompactionPercent = 0;
          const status = compactionStatus(toolContext);
          return { content: [{ type: "text", text: JSON.stringify(status) }], details: status };
        }
        if (input.action === "compact") {
          const request = requestSelfCompaction(input, toolContext);
          return { content: [{ type: "text", text: "Self-compaction deferred until Pi reaches the idle settled boundary." }], details: { ...compactionStatus(toolContext), compactState: "deferred_until_agent_settled", postCompactionNotification: request.postCompactionNotification } };
        }
        if (input.action !== "status") throw new Error("unsupported mecha_context action");
        const status = compactionStatus(toolContext);
        return { content: [{ type: "text", text: JSON.stringify(status) }], details: status };
      } catch (error) {
        return { content: [{ type: "text", text: error.message }], details: {}, isError: true };
      }
    },
  });

  pi.events.on("subagents:child:session-created", (event) => {
    const sessionId = childSessionId(event);
    if (sessionId !== undefined) children.add(sessionId);
  });
  pi.events.on("subagents:child:disposed", (event) => {
    const sessionId = childSessionId(event);
    if (sessionId !== undefined) children.delete(sessionId);
  });

  pi.on("tool_call", (_event, context) => {
    // Suppress only native subagent children recorded by lifecycle events. Do not use the generic
    // parentSession header here: ordinary /fork and /clone sessions carry it but are still top-level agents.
    if (children.has(context?.sessionManager?.getSessionId?.())) return undefined;
    return inlineToolPolicy.evaluate(_event?.toolName);
  });

  pi.on("session_before_compact", async (event, context) => {
    if (children.has(context.sessionManager.getSessionId())) return undefined;
    pauseMailboxForCompaction(context);
    const resumeIfCanceled = () => resumeMailboxAfterCompaction(context);
    event.signal?.addEventListener?.("abort", resumeIfCanceled, { once: true });
    try {
      await (await ensureCapture(event.signal)).compact([...(event.preparation?.messagesToSummarize ?? []), ...(event.preparation?.turnPrefixMessages ?? [])], { signal: event.signal });
    } catch {}
    if (event.signal?.aborted) resumeIfCanceled();
    return undefined;
  });
  pi.on("context", (event, context) => {
    // First quarantine malformed tool calls (invalid names)
    const { messages: validMessages, malformed } = quarantineMalformedToolCalls(event.messages);
    if (malformed.length > 0) {
      const sessionId = context.sessionManager.getSessionId();
      const fresh = malformed.filter(({ toolCallId }) => {
        const key = `${sessionId}:${toolCallId}`;
        if (reportedMalformedToolCalls.has(key)) return false;
        reportedMalformedToolCalls.add(key);
        return true;
      });
      if (fresh.length > 0) {
        let agentId;
        try { agentId = resolveCurrentAgentId(); } catch {}
        appendMalformedToolCallDiagnostic({
          sessionId,
          sessionFile: typeof context.sessionManager.getSessionFile === "function" ? context.sessionManager.getSessionFile() : undefined,
          model: context.model,
          agentId,
          totalMessages: Array.isArray(event.messages) ? event.messages.length : undefined,
          malformedCount: fresh.length,
          malformed: fresh,
        });
        const names = fresh.map(({ toolName }) => typeof toolName === "string" ? toolName.slice(0, 50) : String(toolName)).join(", ");
        context.ui?.notify(`Quarantined malformed tool call(s): ${names}`, "error");
      }
    }
    event.messages = validMessages;

    // Then quarantine orphan tool results
    const { messages, orphaned } = quarantineOrphanToolResults(event.messages);
    if (orphaned.length === 0) return undefined;
    const sessionId = context.sessionManager.getSessionId();
    const fresh = orphaned.filter(({ toolCallId }) => {
      const key = `${sessionId}:${toolCallId}`;
      if (reportedOrphanToolResults.has(key)) return false;
      reportedOrphanToolResults.add(key);
      return true;
    });
    if (fresh.length > 0) {
      let agentId;
      try { agentId = resolveCurrentAgentId(); } catch {}
      appendOrphanToolResultDiagnostic({
        sessionId,
        sessionFile: typeof context.sessionManager.getSessionFile === "function" ? context.sessionManager.getSessionFile() : undefined,
        model: context.model,
        agentId,
        totalMessages: Array.isArray(event.messages) ? event.messages.length : undefined,
        quarantinedCount: fresh.length,
        orphans: fresh,
      });
      const callIds = fresh.map(({ toolCallId }) => toolCallId).join(", ");
      context.ui?.notify(`Quarantined orphan tool result(s) before provider request: ${callIds}`, "error");
    }
    return { messages };
  });
  pi.on("message_end", (event, context) => { if (!children.has(context.sessionManager.getSessionId())) activeWork = boundedConversationText(event.message?.content, 4096); coordinator?.onMessageEnd(event, context); });
  pi.on("message_start", async (_event, context) => { if (children.has(context.sessionManager.getSessionId()) || !recall.status().enabled) return; recallController.activity(); try { await ensureRecallPolicy(); } catch { /* Fail closed: the scheduled evaluation will not receive policy. */ } });
  pi.on("agent_start", (_event, context) => {
    if (children.has(context.sessionManager.getSessionId())) return;
    inlineToolPolicy.reset();
    startActivityHeartbeat(context);
    if (settledNotificationEnabled && settledNotificationRecipient !== undefined) {
      settledNotificationPending = true;
      settledNotifications.onParentActivity();
    }
  });
  pi.on("agent_settled", async (_event, context) => { if (children.has(context.sessionManager.getSessionId()) || !recall.status().enabled) return; recallController.activity(); try { await ensureRecallPolicy(); } catch { /* Fail closed: the scheduled evaluation will not receive policy. */ } });
  pi.on("agent_settled", (_event, context) => { monitorContextThreshold(context); });
  pi.on("session_compact", (_event, context) => { if (!children.has(context.sessionManager.getSessionId())) { notifiedCompactionPercent = 0; resumeMailboxAfterCompaction(context); } });
  pi.on("agent_settled", (_event, context) => {
    if (!children.has(context.sessionManager.getSessionId())) stopActivityHeartbeat({ settle: true, context });
    if (pendingSelfCompaction !== undefined) settledNotificationPending = false;
    runPendingSelfCompaction(context);
    if (children.has(context.sessionManager.getSessionId()) || !settledNotificationEnabled || !settledNotificationPending) return;

    // Consume before sendReply starts so a later run can arm independently.
    settledNotificationPending = false;
    settledNotifications.onParentSettled(() => {
      const notificationClient = client;
      const worker = settledNotificationAgentId;
      if (notificationClient === undefined || worker === undefined) return;
      try {
        Promise.resolve(notificationClient.sendReply({
          to: settledNotificationRecipient,
          subject: "Pi worker settled",
          body: `Worker ${worker} has settled and is idle.`,
        })).catch((error) => {
          context.ui.notify(`Pi settled notification failed: ${error.message}`, "error");
        });
      } catch (error) {
        context.ui.notify(`Pi settled notification failed: ${error.message}`, "error");
      }
    });
  });
  pi.on("session_shutdown", async (_event, context) => { if (children.has(context.sessionManager.getSessionId())) return; recallController.stop(); const controller = new AbortController(); const deadlineAt = Date.now() + 10_000; const timer = setTimeout(() => controller.abort(), 10_000); try { const branch = context.sessionManager.getBranch(); const boundary = branch.map(({ type }) => type).lastIndexOf("compaction"); await (await ensureCapture(controller.signal)).shutdown(branch.slice(boundary + 1), { deadlineAt, signal: controller.signal }); } catch {} finally { controller.abort(); clearTimeout(timer); } });

  pi.on("session_start", (_event, context) => {
    const sessionId = context.sessionManager.getSessionId();
    if (children.has(sessionId)) {
      try {
        const parentAgentId = resolveCurrentAgentId();
        if (parentAgentId === undefined) return;
        childReplyClients.set(sessionId, createClient({
          agentId: transientChildAgentId(sessionId),
          onEnvelope: () => {},
        }));
      } catch {
        // A child without ordinary Mailbox send capability can still finish natively.
      }
      return;
    }
    try {
      const agentId = resolveCurrentAgentId();
      if (agentId === undefined) return;
      rememberParentActivityContext(context);
      settledNotificationPending = false;
      settledNotificationAgentId = agentId;
      pi.registerCommand("mecha-parent-message", {
        description: "Child-only ordinary Mailbox message to its target parent: /mecha-parent-message <message>",
        handler: async (argumentsText, commandContext) => {
          try {
            const childClient = childReplyClients.get(commandContext.sessionManager.getSessionId());
            const parentAgentId = resolveCurrentAgentId();
            const body = argumentsText.trim();
            if (childClient === undefined || parentAgentId === undefined || body.length === 0) {
              throw new Error("This command is available only to a child with a parent message");
            }
            const message = await childClient.sendReply({ to: parentAgentId, body, subject: "Pi subagent message" });
            commandContext.ui.notify(`Mailbox v2 child message accepted (${message.id})`, "info");
          } catch (error) {
            commandContext.ui.notify(error.message, "error");
          }
        },
      });
      pi.registerCommand("mecha-reply", {
        description: "Send a Mailbox v2 reply: /mecha-reply <recipient> <message>",
        handler: async (argumentsText, commandContext) => {
          try {
            if (startupError !== undefined) throw startupError;
            if (client === undefined) throw new Error("Mailbox v2 is not initialized");
            const { recipient, body } = parseReplyArguments(argumentsText);
            const message = await client.sendReply({ to: recipient, body });
            commandContext.ui.notify(`Mailbox v2 reply accepted (${message.id})`, "info");
          } catch (error) {
            commandContext.ui.notify(error.message, "error");
          }
        },
      });
      let mailboxClient;
      mailboxClient = createClient({
        agentId,
        onEnvelope: (message, delivery) => { if (!mailboxPausedForCompaction) coordinator?.onEnvelope(message, delivery); },
        kickstart: async () => {
          if (typeof process.getuid !== "function") return;
          const result = await pi.exec(
            "launchctl",
            ["kickstart", `gui/${process.getuid()}/${process.env.MECHA_ROUTER_LAUNCHD_LABEL ?? "life.meyers.mecha-router"}`],
            { timeout: 5_000 },
          );
          if (result.code !== 0) {
            throw new Error(result.stderr.trim() || `launchctl exited ${result.code}`);
          }
        },
        onKickstartFailure: (error) => {
          context.ui.setStatus("mecha-mailbox", "mailbox: router start failed; retrying");
          context.ui.notify(`Mailbox v2 router kickstart failed: ${error.message}`, "error");
        },
        onConnectionState: ({ state, error }) => {
          if (client !== mailboxClient) return;
          if (state === "connected") {
            context.ui.setStatus("mecha-mailbox", "mailbox: connected");
          } else if (state === "connecting" || state === "reconnecting") {
            context.ui.setStatus("mecha-mailbox", "mailbox: reconnecting");
          } else if (state === "disconnected") {
            context.ui.setStatus("mecha-mailbox", "mailbox: disconnected");
            if (error !== undefined) context.ui.notify(`Mailbox v2 reconnecting: ${error.message}`, "error");
          }
        },
        log: () => {},
      });
      client = mailboxClient;
      // Child sessions inherit extensions and can replace the package's global
      // service symbol. Bind scheduled work to this resident parent service.
      coordinator = createMailboxCoordinator(context, mailboxClient);
      void mailboxClient.connect().catch((error) => {
        if (client === mailboxClient) context.ui.notify(`Mailbox v2 unavailable: ${error.message}`, "error");
      });
    } catch (error) {
      startupError = error;
      context.ui.notify(`Mailbox v2 unavailable: ${error.message}`, "error");
    }
  });

  pi.on("session_shutdown", (_event, context) => {
    const sessionId = context.sessionManager.getSessionId();
    if (children.has(sessionId)) {
      childReplyClients.delete(sessionId);
      return;
    }
    rememberParentActivityContext(context);
    clearRunningSubagentsForBoundary();
    parentActivityContext = undefined;
    settledNotificationPending = false;
    settledNotificationAgentId = undefined;
    pendingSelfCompaction = undefined;
    mailboxPausedForCompaction = false;
    settledNotifications.dispose();
    coordinator?.close();
    coordinator = undefined;
    client?.close();
    client = undefined;
    context.ui.setStatus("mecha-mailbox", undefined);
  });
}

export default function mechaMessagingExtension(pi) {
  return createMechaMessagingExtension(pi);
}
