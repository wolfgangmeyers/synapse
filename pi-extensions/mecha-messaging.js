import { createMailboxClient, createNanoId, assertAgentId, routerHttpUrl } from "./mailbox-client.js";
export { createMailboxClient } from "./mailbox-client.js";
import { execFile, execFileSync } from "node:child_process";
import { appendFileSync, mkdirSync, readFileSync, renameSync, rmSync, statSync, writeFileSync } from "node:fs";
import { dirname } from "node:path";
import { createRecallState } from "./recall-state.js";
import { createRecallController } from "./recall-controller.js";
import { createRecallPipeline as createPipeline } from "./recall-pipeline.js";
import { runRecallEvaluator as evaluateRecall, validateRecallChoice as validateChoice } from "./recall-evaluator.js";
import { createMemoryCaptureLifecycle } from "./memory-capture.js";
import { registerOperationalTools } from "./operational-tools.js";
import {
  GOAL_CONTROL_REQUEST_EVENT,
  GOAL_CONTROL_STATE_EVENT,
  GOAL_STATE_EVENT,
  GOAL_STATE_REQUEST_EVENT,
} from "./goal.js";
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
const NATIVE_SUBAGENT_DIAGNOSTIC_RATE_LIMIT_MS = 60_000;
const MAILBOX_SEND_RATE_LIMIT_COUNT = 6;
const MAILBOX_SEND_RATE_LIMIT_WINDOW_MS = 60_000;
const CHILD_SESSION_IDS = Symbol.for("mecha.mailbox-v2.child-session-ids");
const MAX_GOAL_CHARS = 4_000;
const GOAL_CONTROL_RESPONSE_TIMEOUT_MS = 1_000;
const GOAL_OUTCOME_KEYS = ["BLOCKED", "COMPLETED", "NOT_DONE", "WAITING_FOR_USER_AUTHORIZATION"];

const PI_SUBAGENTS_SERVICE_REGISTRY = Symbol.for("@gotgenes/pi-subagents:service-registry");

function normalizeGoalProjection(value) {
  if (value === null || typeof value !== "object" || Array.isArray(value)
    || typeof value.enabled !== "boolean" || typeof value.goal !== "string"
    || value.goal.length > MAX_GOAL_CHARS) return undefined;
  if (!value.enabled && value.goal !== "") return undefined;
  if (value.enabled && value.goal.length === 0) return undefined;
  return { enabled: value.enabled, goal: value.enabled ? value.goal : "" };
}

function defaultGoalControlState() {
  return {
    enabled: false,
    goal: "",
    blocking: true,
    goalLockEnabled: false,
    snoozeEnabled: true,
    snoozeDurationMs: 60_000,
    snoozeTurnLimit: null,
    snoozeBudgetCapMs: null,
    snoozeRefillYieldMs: 30_000,
    snoozeRefillElapsedMs: 60_000,
    outcomeCounts: Object.fromEntries(GOAL_OUTCOME_KEYS.map((key) => [key, 0])),
  };
}

function normalizeGoalControlState(value) {
  if (value === null || typeof value !== "object" || Array.isArray(value)) return undefined;
  const projection = normalizeGoalProjection(value);
  if (projection === undefined
    || (Object.hasOwn(value, "blocking") && typeof value.blocking !== "boolean")
    || typeof value.goalLockEnabled !== "boolean"
    || typeof value.snoozeEnabled !== "boolean"
    || !Number.isSafeInteger(value.snoozeDurationMs) || value.snoozeDurationMs <= 0
    || (value.snoozeTurnLimit !== null && (!Number.isSafeInteger(value.snoozeTurnLimit) || value.snoozeTurnLimit < 1))
    || (value.snoozeBudgetCapMs !== null && (!Number.isSafeInteger(value.snoozeBudgetCapMs) || value.snoozeBudgetCapMs < 1))
    || !Number.isSafeInteger(value.snoozeRefillYieldMs) || value.snoozeRefillYieldMs <= 0
    || !Number.isSafeInteger(value.snoozeRefillElapsedMs) || value.snoozeRefillElapsedMs <= 0
    || value.outcomeCounts === null || typeof value.outcomeCounts !== "object" || Array.isArray(value.outcomeCounts)) {
    return undefined;
  }
  const outcomeCounts = {};
  for (const key of GOAL_OUTCOME_KEYS) {
    if (!Number.isSafeInteger(value.outcomeCounts[key]) || value.outcomeCounts[key] < 0) return undefined;
    outcomeCounts[key] = value.outcomeCounts[key];
  }
  return {
    ...projection,
    blocking: value.blocking !== false,
    goalLockEnabled: value.goalLockEnabled,
    snoozeEnabled: value.snoozeEnabled,
    snoozeDurationMs: value.snoozeDurationMs,
    snoozeTurnLimit: value.snoozeTurnLimit,
    snoozeBudgetCapMs: value.snoozeBudgetCapMs,
    snoozeRefillYieldMs: value.snoozeRefillYieldMs,
    snoozeRefillElapsedMs: value.snoozeRefillElapsedMs,
    outcomeCounts,
  };
}

function cloneGoalControlState(state) {
  return {
    ...state,
    outcomeCounts: { ...state.outcomeCounts },
  };
}

function boundedModelString(value, maximum) {
  if (typeof value !== "string") return undefined;
  const normalized = value.trim();
  return normalized.length > 0 && [...normalized].length <= maximum ? normalized : undefined;
}

function projectModelMetadata(model) {
  if (model === null || typeof model !== "object" || Array.isArray(model)) return undefined;
  const provider = boundedModelString(model.provider, MAX_MODEL_PROVIDER_CHARS);
  const id = boundedModelString(model.id, MAX_MODEL_ID_CHARS);
  const name = boundedModelString(model.name, MAX_MODEL_NAME_CHARS);
  const contextWindow = Number.isSafeInteger(model.contextWindow) && model.contextWindow >= 0
    ? model.contextWindow
    : undefined;
  const maxTokens = Number.isSafeInteger(model.maxTokens) && model.maxTokens >= 0
    ? model.maxTokens
    : undefined;
  if (provider === undefined || id === undefined) return undefined;
  return {
    provider,
    id,
    ...(name === undefined ? {} : { name }),
    ...(contextWindow === undefined ? {} : { contextWindow }),
    ...(maxTokens === undefined ? {} : { maxTokens }),
  };
}

function availableModelsForContext(context) {
  try {
    const available = context?.modelRegistry?.getAvailable?.();
    return Array.isArray(available) ? available : undefined;
  } catch {
    return undefined;
  }
}

class ModelControlError extends Error {
  constructor(code, message, options) {
    super(message, options);
    this.name = "ModelControlError";
    this.code = code;
  }
}

function readAvailableModelsForContext(context) {
  const getAvailable = context?.modelRegistry?.getAvailable;
  if (typeof getAvailable !== "function") {
    throw new ModelControlError("model_registry_unavailable", "Pi model registry is unavailable");
  }
  let available;
  try {
    available = getAvailable.call(context.modelRegistry);
  } catch (error) {
    throw new ModelControlError("model_registry_failed", error?.message ?? "Pi model registry failed", { cause: error });
  }
  if (!Array.isArray(available)) {
    throw new ModelControlError("model_registry_unavailable", "Pi model registry is unavailable");
  }
  return available;
}

function projectAvailableModels(context) {
  return readAvailableModelsForContext(context)
    .slice(0, 1_000_000)
    .map(projectModelMetadata)
    .filter(Boolean);
}

function modelProjection(context) {
  const availableModels = availableModelsForContext(context);
  return {
    current: projectModelMetadata(context?.model) ?? null,
    available: (availableModels ?? [])
      .slice(0, MAX_MODEL_METADATA_ITEMS)
      .map(projectModelMetadata)
      .filter(Boolean),
  };
}

function getOwnerSubagentsService(ownerSessionId) {
  const services = globalThis[PI_SUBAGENTS_SERVICE_REGISTRY]?.services;
  return typeof ownerSessionId === "string" && typeof services?.get === "function"
    ? services.get(ownerSessionId)
    : undefined;
}

const CONTEXT_THRESHOLD_SUFFIX = "Use workers or subagents for bounded side work to avoid context bloat.";
export const INLINE_TOOL_ALLOWANCE_TOOL_NAME = "mecha_request_inline_tool_allowance";
const MECHA_MESSAGING_CURSOR_TOOL_METADATA = Object.freeze({
  mecha_parent_message: {
    promptSnippet: "Send a message from a native subagent to its parent",
    promptGuidelines: [
      "Use mecha_parent_message only in native subagent sessions to message the parent agent.",
      "Call pi__mecha_parent_message through the Cursor pi bridge.",
    ],
  },
  mecha_mailbox_send: {
    promptSnippet: "Send a durable Mailbox V2 message to another local agent",
    promptGuidelines: [
      "Use mecha_mailbox_send for supervisor, worker, and service replies with recipient, body, and optional subject.",
      "Call pi__mecha_mailbox_send through the Cursor pi bridge.",
    ],
  },
  [INLINE_TOOL_ALLOWANCE_TOOL_NAME]: {
    promptSnippet: "Request a temporary exception to the inline tool guard",
    promptGuidelines: [
      "Use mecha_request_inline_tool_allowance only for genuinely necessary top-level bash/edit/write work.",
      "Call pi__mecha_request_inline_tool_allowance through the Cursor pi bridge.",
    ],
  },
  mecha_recall: {
    promptSnippet: "Enable, disable, or inspect process-local Markdown recall",
    promptGuidelines: [
      "Use mecha_recall with action enable, disable, or status for recall stores.",
      "Call pi__mecha_recall through the Cursor pi bridge.",
    ],
  },
  mecha_settled_notifications: {
    promptSnippet: "Enable, disable, or inspect settled mailbox notifications",
    promptGuidelines: [
      "Use mecha_settled_notifications with action enable, disable, or status for worker settled notifications.",
      "Call pi__mecha_settled_notifications through the Cursor pi bridge.",
    ],
  },
  mecha_context: {
    promptSnippet: "Inspect context-threshold policy or request Pi self-compaction",
    promptGuidelines: [
      "Use mecha_context with action status, set_policy, or compact for context-threshold and compaction control.",
      "Call pi__mecha_context through the Cursor pi bridge.",
    ],
  },
});
const INLINE_ACTION_TOOL_NAMES = new Set(["bash", "edit", "write"]);
const INLINE_TOOL_POOL_CAPACITY = 5;
const INLINE_TOOL_REPLENISHMENT_INTERVAL_MS = 60_000;
const INLINE_TOOL_BLOCK_REASON = "Top-level inline write-ish tool calls are blocked by default. Subagents are mandatory for this work. Only when genuinely necessary, use mecha_request_inline_tool_allowance to request a temporary allowance; that is an exceptional path, not the normal workaround.";
// Keep the catalog bounded while allowing the configured Pi registry to reach
// the UI instead of silently dropping the models after the first page.
const MAX_MODEL_METADATA_ITEMS = 1_000;
const MAX_MODEL_PROVIDER_CHARS = 128;
const MAX_MODEL_ID_CHARS = 256;
const MAX_MODEL_NAME_CHARS = 256;

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
export const DEFAULT_COMPACTION_POLICY = Object.freeze({
  tiers: Object.freeze([
    Object.freeze({ maxContextTokens: 300_000, levels: SMALL_CONTEXT_LEVELS }),
    Object.freeze({ maxContextTokens: 1_000_000, levels: LARGE_CONTEXT_LEVELS }),
    Object.freeze({ maxContextTokens: null, levels: LARGE_CONTEXT_LEVELS }),
  ]),
  models: Object.freeze({
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
  const trimmedToolName = toolName.trim();
  const normalizedToolName = trimmedToolName.startsWith("functions.")
    ? trimmedToolName.slice("functions.".length)
    : trimmedToolName;
  if (!INLINE_ACTION_TOOL_NAMES.has(normalizedToolName)) {
    throw new TypeError(`unsupported inline tool name: ${trimmedToolName}; expected bash, edit, or write`);
  }
  return normalizedToolName;
}

function validateInlineAllowanceCount(usageCount) {
  if (!Number.isSafeInteger(usageCount) || usageCount < 1 || usageCount > 5) {
    throw new TypeError("usageCount must be an integer from 1 through 5");
  }
}

export function inlineToolBlockReason() {
  return INLINE_TOOL_BLOCK_REASON;
}

export function createInlineToolPolicy({ now = Date.now } = {}) {
  if (typeof now !== "function") throw new TypeError("now must be a function");

  const credits = new Map();
  let poolRemainingMs = INLINE_TOOL_POOL_CAPACITY * INLINE_TOOL_REPLENISHMENT_INTERVAL_MS;
  let poolUpdatedAt = now();

  function replenishPool() {
    const currentTime = now();
    const elapsedMs = Math.max(0, currentTime - poolUpdatedAt);
    poolRemainingMs = Math.min(
      INLINE_TOOL_POOL_CAPACITY * INLINE_TOOL_REPLENISHMENT_INTERVAL_MS,
      poolRemainingMs + elapsedMs,
    );
    poolUpdatedAt = currentTime;
  }

  function allowanceDetails({ toolName, usageCount, grantedCount, remaining, availableWholeCredits, remainingPool }) {
    return {
      toolName,
      usageCount,
      grantedCount,
      remaining,
      availableWholeCredits,
      remainingPool,
    };
  }

  return {
    reset() {
      credits.clear();
      poolRemainingMs = INLINE_TOOL_POOL_CAPACITY * INLINE_TOOL_REPLENISHMENT_INTERVAL_MS;
      poolUpdatedAt = now();
    },
    grant(toolName, usageCount) {
      const normalizedToolName = normalizeInlineToolName(toolName);
      validateInlineAllowanceCount(usageCount);
      replenishPool();
      const availableWholeCredits = Math.floor(poolRemainingMs / INLINE_TOOL_REPLENISHMENT_INTERVAL_MS);
      const remainingPool = poolRemainingMs / INLINE_TOOL_REPLENISHMENT_INTERVAL_MS;
      if (availableWholeCredits < usageCount) {
        const exhaustedText = availableWholeCredits === 0 && poolRemainingMs === 0 ? " The pool is exhausted." : "";
        const error = new Error(`Insufficient inline tool credits: requested ${usageCount} credit(s), but only ${availableWholeCredits} whole credit(s) are available (pool currently holds ${remainingPool} credit(s)).${exhaustedText} No credits were granted. Credits replenish at 1 credit per minute up to a maximum of 5 credits.`);
        error.details = allowanceDetails({
          toolName: normalizedToolName,
          usageCount,
          grantedCount: 0,
          remaining: credits.get(normalizedToolName) ?? 0,
          availableWholeCredits,
          remainingPool,
        });
        throw error;
      }

      const grantedCount = usageCount;
      poolRemainingMs -= grantedCount * INLINE_TOOL_REPLENISHMENT_INTERVAL_MS;
      const remaining = (credits.get(normalizedToolName) ?? 0) + grantedCount;
      credits.set(normalizedToolName, remaining);
      return allowanceDetails({
        toolName: normalizedToolName,
        usageCount,
        grantedCount,
        remaining,
        availableWholeCredits,
        remainingPool: poolRemainingMs / INLINE_TOOL_REPLENISHMENT_INTERVAL_MS,
      });
    },
    evaluate(toolName) {
      let normalizedToolName;
      try {
        normalizedToolName = normalizeInlineToolName(toolName);
      } catch {
        // Tools outside the inline write allowance are handled by their own policies.
        return undefined;
      }
      // Credits are per-tool and temporary: matching top-level calls spend them, and exhausted entries are removed. The parent session resets this state at session_start.
      const remaining = credits.get(normalizedToolName) ?? 0;
      if (remaining > 0) {
        if (remaining === 1) credits.delete(normalizedToolName);
        else credits.set(normalizedToolName, remaining - 1);
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

const EXTENSION_TRACE_PATH_ENV = "MECHA_PI_EXTENSION_TRACE_PATH";
const LEGACY_NATIVE_SUBAGENT_TRACE_PATH_ENV = "MECHA_PI_NATIVE_SUBAGENT_TRACE_PATH";
const EXTENSION_TRACE_MAX_BYTES = 512 * 1024;
const NATIVE_SUBAGENT_TRACE_STRING_FIELDS = new Set([
  "agent_id", "instance_id", "parent_session_id", "session_id", "subagent_id", "event_id",
  "status", "reason", "error_code", "error_name", "source", "frame_type", "disposition", "state", "phase",
  "control_id", "source_agent_id", "target_agent_id", "lease_id", "action",
  "requested_provider", "requested_model_id", "current_provider", "current_model_id",
]);
const NATIVE_SUBAGENT_TRACE_NUMBER_FIELDS = new Set([
  "client_sequence", "router_version", "subagent_count", "snapshot_count", "pending", "bytes", "description_length",
  "recipient_count", "attempt", "heartbeat_interval_ms", "available_count", "model_count", "source_model_count",
  "frame_bytes",
]);
const NATIVE_SUBAGENT_TRACE_BOOLEAN_FIELDS = new Set([
  "available", "registry_available", "lifecycle_service", "connected", "ready", "reconnect", "published",
  "accepted", "switched", "truncated",
]);

export function summarizeNativeSubagents(subagents) {
  if (!Array.isArray(subagents)) return [];
  return subagents.slice(0, 999).flatMap((subagent) => {
    const id = typeof subagent?.id === "string" ? subagent.id.trim().slice(0, 128) : "";
    if (id.length === 0) return [];
    const type = typeof subagent.type === "string" ? subagent.type.trim().slice(0, 64) : "";
    const status = typeof subagent.status === "string" ? subagent.status.trim().slice(0, 64) : "";
    const descriptionLength = typeof subagent.description === "string" ? subagent.description.length : 0;
    return [{ id, ...(type.length > 0 ? { type } : {}), ...(status.length > 0 ? { status } : {}), description_length: descriptionLength }];
  });
}

export function createExtensionTrace({
  environment = process.env,
  path = typeof environment?.[EXTENSION_TRACE_PATH_ENV] === "string" && environment[EXTENSION_TRACE_PATH_ENV].trim().length > 0
    ? environment[EXTENSION_TRACE_PATH_ENV].trim()
    : typeof environment?.[LEGACY_NATIVE_SUBAGENT_TRACE_PATH_ENV] === "string"
      && environment[LEGACY_NATIVE_SUBAGENT_TRACE_PATH_ENV].trim().length > 0
      ? environment[LEGACY_NATIVE_SUBAGENT_TRACE_PATH_ENV].trim()
    : undefined,
  maxBytes = EXTENSION_TRACE_MAX_BYTES,
  now = () => new Date(),
} = {}) {
  if (path === undefined) return { enabled: false, record: () => false };
  if (typeof path !== "string" || path.length === 0) throw new TypeError("extension trace path is invalid");
  if (!Number.isSafeInteger(maxBytes) || maxBytes <= 0) throw new TypeError("extension trace maxBytes must be a positive safe integer");
  if (typeof now !== "function") throw new TypeError("extension trace clock must be a function");
  return {
    enabled: true,
    record(event, fields = {}) {
      try {
        if (typeof event !== "string" || !/^[a-z][a-z0-9_.-]{0,80}$/.test(event)) throw new TypeError("extension trace event is invalid");
        if (fields === null || typeof fields !== "object" || Array.isArray(fields)) throw new TypeError("extension trace fields are invalid");
        const timestamp = now();
        if (!(timestamp instanceof Date) || Number.isNaN(timestamp.getTime())) throw new TypeError("extension trace clock is invalid");
        const record = { timestamp: timestamp.toISOString(), event };
        for (const [key, value] of Object.entries(fields)) {
          if (NATIVE_SUBAGENT_TRACE_STRING_FIELDS.has(key) && typeof value === "string") record[key] = value.slice(0, 128);
          else if (NATIVE_SUBAGENT_TRACE_NUMBER_FIELDS.has(key) && Number.isSafeInteger(value) && value >= 0) record[key] = value;
          else if (NATIVE_SUBAGENT_TRACE_BOOLEAN_FIELDS.has(key) && typeof value === "boolean") record[key] = value;
          else if (key === "subagents") record.subagents = summarizeNativeSubagents(value);
        }
        mkdirSync(dirname(path), { recursive: true, mode: 0o700 });
        try {
          if (statSync(path).size >= maxBytes) {
            rmSync(`${path}.1`, { force: true });
            renameSync(path, `${path}.1`);
          }
        } catch (error) {
          if (error?.code !== "ENOENT") throw error;
        }
        appendFileSync(path, `${JSON.stringify(record)}\n`, { encoding: "utf8", mode: 0o600 });
        return true;
      } catch {
        // Diagnostics cannot change extension behavior or reveal failure details.
        return false;
      }
    },
  };
}

export const createNativeSubagentTrace = createExtensionTrace;

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
// Native lifecycle visibility is intentionally polling-only. This cadence bounds how
// long the web UI can show a stale child without relying on Pi event delivery.
const ACTIVITY_HEARTBEAT_INTERVAL_MS = 2_000;
const ACTIVITY_REFRESH_MIN_MS = 10_000;
const EVENT_TRACKED_MISSING_LIMIT_MS = 60_000;
// pi-subagents intentionally exposes at most 100 live records. Keep the Mailbox
// projection at the same bound so an extension cannot turn activity into an
// unbounded cross-extension data path.
const MAX_ACTIVITY_SUBAGENTS = 100;
const MAX_TERMINAL_SUBAGENT_TOMBSTONES = 2048;
const LIVE_SUBAGENT_STATUSES = new Set(["queued", "running"]);
const TERMINAL_SUBAGENT_STATUSES = new Set(["completed", "steered", "aborted", "stopped", "error"]);

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

export async function spawnNativeSentinel(message, resolveService) {
  const service = await resolveService?.();
  if (service === undefined || typeof service.spawn !== "function") {
    throw new Error("Pi native subagent service is unavailable");
  }
  return service.spawn("general-purpose", message.body, {
    description: "Scheduled Sentinel work",
    model: message.sentinel.model,
    inheritContext: true,
  });
}

export function captureNativeSentinelSpawner(resolveService) {
  return (message) => spawnNativeSentinel(message, resolveService);
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
  clearTimeoutImpl = clearTimeout,
  setIntervalImpl = setInterval,
  clearIntervalImpl = clearInterval,
  nowImpl = Date.now,
  getSubagentsService = getOwnerSubagentsService,
  nativeSubagentTrace = createNativeSubagentTrace(),
  operationalTools = {},
} = {}) {
  registerOperationalTools(pi, operationalTools);
  if (nativeSubagentTrace === undefined || typeof nativeSubagentTrace.record !== "function") {
    throw new TypeError("nativeSubagentTrace must provide record");
  }
  const traceNativeSubagent = (event, fields = {}) => nativeSubagentTrace.record(event, fields);
  traceNativeSubagent("extension_initialized", {
    registry_available: typeof globalThis[PI_SUBAGENTS_SERVICE_REGISTRY]?.services?.get === "function",
  });
  const currentSubagentsServiceAccessor = getSubagentsService;
  let client;
  let coordinator;
  let goalProjection;
  let goalProjectionReceived = false;
  let goalControlState = defaultGoalControlState();
  const receiveGoalProjection = (value) => {
    const normalized = normalizeGoalProjection(value);
    if (normalized === undefined) return;
    goalProjection = normalized;
    goalProjectionReceived = true;
  };
  const receiveGoalControlState = (value) => {
    const normalized = normalizeGoalControlState(value);
    if (normalized === undefined) return;
    goalControlState = normalized;
    publishGoalState();
  };
  const publishGoalState = (mailboxClient = client) => {
    if (mailboxClient === undefined || typeof mailboxClient.sendGoalState !== "function") return false;
    try {
      mailboxClient.sendGoalState(cloneGoalControlState(goalControlState));
      return true;
    } catch {
      return false;
    }
  };
  const safeGoalControlRejection = (errorCode = "unavailable") => ({
    accepted: false,
    errorCode,
    goalState: cloneGoalControlState(goalControlState),
  });
  const normalizeGoalControlResult = (result) => {
    if (result === null || typeof result !== "object" || Array.isArray(result) || typeof result.accepted !== "boolean") {
      return safeGoalControlRejection("malformed_response");
    }
    if (result.accepted === true && result.goalState === undefined) {
      return safeGoalControlRejection("malformed_response");
    }
    const responseState = result.goalState === undefined
      ? cloneGoalControlState(goalControlState)
      : normalizeGoalControlState(result.goalState);
    if (responseState === undefined) return safeGoalControlRejection("malformed_response");
    goalControlState = responseState;
    publishGoalState();
    return {
      accepted: result.accepted,
      ...(typeof result.errorCode === "string" && /^[a-z][a-z0-9_]{0,127}$/.test(result.errorCode)
        ? { errorCode: result.errorCode }
        : {}),
      goalState: cloneGoalControlState(responseState),
    };
  };
  const handleGoalControl = async (control) => {
    const request = control !== null && typeof control === "object" && !Array.isArray(control)
      ? {
        correlationId: control.correlationId ?? control.controlId,
        controlId: control.controlId,
        sourceAgentId: control.sourceAgentId,
        targetInstanceId: control.targetInstanceId ?? control.instanceId,
        revision: control.revision,
        action: control.action,
        goal: control.goal,
        settings: control.settings,
        context: parentActivityContext,
      }
      : undefined;
    if (request?.controlId === undefined || request?.targetInstanceId === undefined) {
      return safeGoalControlRejection("invalid_control");
    }
    let timer;
    let response;
    let responded = false;
    let resolveResponse;
    const responsePromise = new Promise((resolve) => { resolveResponse = resolve; });
    const respond = (value) => {
      if (responded) return;
      responded = true;
      resolveResponse(value);
    };
    try {
      pi.events?.emit?.(GOAL_CONTROL_REQUEST_EVENT, { ...request, respond });
    } catch {
      return safeGoalControlRejection("unavailable");
    }
    if (!responded) {
      timer = setTimeoutImpl(() => respond(undefined), GOAL_CONTROL_RESPONSE_TIMEOUT_MS);
      timer?.unref?.();
      response = await responsePromise;
      clearTimeoutImpl(timer);
    } else {
      response = await responsePromise;
    }
    if (!responded || response === undefined) return safeGoalControlRejection("unavailable");
    return normalizeGoalControlResult(response);
  };
  pi.events?.on?.(GOAL_STATE_EVENT, receiveGoalProjection);
  pi.events?.on?.(GOAL_CONTROL_STATE_EVENT, receiveGoalControlState);
  try { pi.events?.emit?.(GOAL_STATE_REQUEST_EVENT); } catch { /* optional goal bridge is best-effort */ }
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
  // Extension event contexts expose the model as a read-only snapshot. Keep a
  // short-lived per-session override so a successful remote switch is
  // published immediately even if the next event has not supplied a fresh
  // context snapshot yet.
  let modelSwitchOverride;
  let lastActivitySent;
  let lastNativeSubagentReadDiagnosticMs;
  function nativeSubagentService(ownerSessionId = parentActivityContext?.sessionManager?.getSessionId?.()) {
    const available = typeof ownerSessionId === "string";
    let service;
    try {
      service = available ? currentSubagentsServiceAccessor?.(ownerSessionId) : undefined;
      traceNativeSubagent("extension_service_lookup", {
        ...(available ? { parent_session_id: ownerSessionId } : {}),
        available: service !== undefined,
        registry_available: typeof globalThis[PI_SUBAGENTS_SERVICE_REGISTRY]?.services?.get === "function",
        lifecycle_service: typeof service?.getLifecycleSnapshots === "function",
      });
      return service;
    } catch (error) {
      traceNativeSubagent("extension_service_lookup_error", {
        ...(available ? { parent_session_id: ownerSessionId } : {}),
        error_name: error?.name ?? "Error",
      });
      return undefined;
    }
  }

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
    const status = typeof event?.status === "string" ? event.status.toLowerCase() : undefined;
    return {
      id,
      ...(type.length > 0 ? { type } : {}),
      ...(description.length > 0 ? { description } : {}),
      ...(status === undefined ? {} : { status }),
      startedAtMs,
      lastSeenMs: startedAtMs,
    };
  }

  function publicSubagentSummaries() {
    return [...runningSubagents.values()]
      .filter(({ description }) => typeof description === "string" && description.length > 0)
      .sort((left, right) => left.startedAtMs - right.startedAtMs || left.id.localeCompare(right.id))
      .slice(0, MAX_ACTIVITY_SUBAGENTS)
      .map(({ id, type, description, status }) => ({ id, ...(type !== undefined ? { type } : {}), description, ...(status !== undefined ? { status } : {}) }));
  }

  function lifecycleSubagentSummary(metadata) {
    const id = typeof metadata?.id === "string" ? metadata.id.trim().slice(0, 128) : "";
    if (id.length === 0) return undefined;
    const type = typeof metadata?.type === "string" ? metadata.type.trim().slice(0, 64) : "";
    const description = typeof metadata?.description === "string" ? metadata.description.trim().slice(0, 512) : "";
    const status = ["queued", "running", "completed", "steered", "aborted", "stopped", "error"].includes(metadata?.status)
      ? metadata.status
      : undefined;
    return {
      id,
      ...(type.length > 0 ? { type } : {}),
      // Native callers supply this short label specifically for observers. Keep a
      // stable fallback only for older event shapes that did not carry one.
      description: description.length > 0 ? description : "Native subagent",
      ...(status === undefined ? {} : { status }),
    };
  }

  function lifecycleSubagentSummaries() {
    return [...runningSubagents.values()]
      .sort((left, right) => left.startedAtMs - right.startedAtMs || left.id.localeCompare(right.id))
      .slice(0, MAX_ACTIVITY_SUBAGENTS)
      .map(lifecycleSubagentSummary)
      .filter(Boolean);
  }

  function publishLifecycleSnapshot(subagents) {
    const summary = summarizeNativeSubagents(subagents);
    try {
      const eventId = client?.sendLifecycleSnapshot?.(subagents);
      traceNativeSubagent("extension_snapshot_publish", {
        published: typeof eventId === "string",
        ...(typeof eventId === "string" ? { event_id: eventId } : {}),
        snapshot_count: summary.length,
        subagents: summary,
      });
    } catch (error) {
      traceNativeSubagent("extension_snapshot_publish_error", {
        snapshot_count: summary.length,
        subagents: summary,
        error_name: error?.name ?? "Error",
      });
      // lifecycle polling retries on the next tick
    }
  }

  function sameSubagentMetadata(left, right) {
    return left !== undefined && right !== undefined && left.id === right.id && left.type === right.type && left.description === right.description && left.status === right.status;
  }

  function lifecycleSnapshotSubagentService(service = nativeSubagentService()) {
    return typeof service?.getLifecycleSnapshots === "function" ? service : undefined;
  }

  function reportNativeSubagentReadFailure(error) {
    traceNativeSubagent("extension_snapshot_read_error", { error_name: error?.name ?? "Error" });
    const nowMs = nowImpl();
    if (lastNativeSubagentReadDiagnosticMs !== undefined && nowMs - lastNativeSubagentReadDiagnosticMs < NATIVE_SUBAGENT_DIAGNOSTIC_RATE_LIMIT_MS) return;
    lastNativeSubagentReadDiagnosticMs = nowMs;
    try {
      parentActivityContext?.ui?.notify?.("Native subagent service read failed; polling will retry.", "error");
    } catch {
      // A UI notification is only an operational signal; a broken UI must not stop polling retries.
    }
  }

  function nativeSubagentRecords(service = nativeSubagentService()) {
    try {
      // The lifecycle projection is the producer's public, redacted source of truth. listAgents()
      // retains terminal result records, so it is only the compatibility fallback for older Pi.
      const source = typeof service?.getLifecycleSnapshots === "function"
        ? service.getLifecycleSnapshots()
        : typeof service?.listAgents === "function"
          ? service.listAgents()
          : typeof service?.getRunning === "function"
            ? service.getRunning()
            : service?.running ?? service?.runs ?? service?.subagents;
      const records = Array.isArray(source) ? source : undefined;
      traceNativeSubagent("extension_snapshot_read", {
        source: typeof service?.getLifecycleSnapshots === "function" ? "lifecycle_snapshot" : typeof service?.listAgents === "function" ? "list_agents" : typeof service?.getRunning === "function" ? "get_running" : "property",
        available: records !== undefined,
        snapshot_count: records?.length ?? 0,
        subagents: summarizeNativeSubagents(records),
      });
      return records;
    } catch (error) {
      reportNativeSubagentReadFailure(error);
      return undefined;
    }
  }

  function nativeSubagentStatus(record) {
    return typeof record?.status === "string" ? record.status.toLowerCase() : record?.status;
  }

  function isActiveNativeSubagentRecord(record, service = nativeSubagentService()) {
    const status = nativeSubagentStatus(record);
    return lifecycleSnapshotSubagentService(service) !== undefined
      ? LIVE_SUBAGENT_STATUSES.has(status)
      : [undefined, "running", "queued", "active"].includes(status);
  }

  function isTerminalNativeSubagentRecord(record, service = nativeSubagentService()) {
    const status = nativeSubagentStatus(record);
    return lifecycleSnapshotSubagentService(service) !== undefined
      ? TERMINAL_SUBAGENT_STATUSES.has(status)
      : [
        "completed", "complete", "done", "finished", "failed", "failure", "error", "errored",
        "canceled", "cancelled", "aborted", "terminated", "stopped", "steered",
      ].includes(status);
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
        status: metadata.status ?? existing.status,
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

  function lifecycleSnapshotSummary(record) {
    const id = typeof record?.id === "string" ? record.id.trim().slice(0, 128) : "";
    if (id.length === 0) return undefined;
    const type = typeof record?.type === "string" ? record.type.trim().slice(0, 64) : "";
    const description = typeof record?.description === "string" ? record.description.trim().slice(0, 512) : "";
    const status = nativeSubagentStatus(record);
    if (!LIVE_SUBAGENT_STATUSES.has(status)) return undefined;
    // Construct this object instead of spreading native records. Snapshot payloads must
    // never expose prompts, output, result, error details, or internal timestamps.
    return {
      id,
      type: type.length > 0 ? type : "general-purpose",
      description: description.length > 0 ? description : "Native subagent",
      status,
    };
  }

  function replaceNativeLifecycleSnapshot(snapshot, nowMs) {
    const next = new Map();
    for (const record of snapshot) {
      const existing = runningSubagents.get(record.id);
      next.set(record.id, {
        ...record,
        eventTracked: false,
        serviceTracked: true,
        startedAtMs: existing?.startedAtMs ?? nowMs,
        lastSeenMs: nowMs,
      });
    }
    let changed = next.size !== runningSubagents.size;
    for (const [id, metadata] of next) {
      if (!sameSubagentMetadata(runningSubagents.get(id), metadata)) changed = true;
    }
    for (const id of runningSubagents.keys()) {
      if (!next.has(id)) settledNotifications.onChildDisposed(id);
    }
    for (const id of next.keys()) {
      if (!runningSubagents.has(id)) settledNotifications.onChildStarted(id);
    }
    runningSubagents.clear();
    for (const [id, metadata] of next) runningSubagents.set(id, metadata);
    return changed;
  }

  function reconcileNativeSubagentService() {
    const service = nativeSubagentService();
    const lifecycleSnapshotService = lifecycleSnapshotSubagentService(service);
    const source = nativeSubagentRecords(service);
    if (!Array.isArray(source)) return { changed: false };
    const nowMs = nowImpl();
    if (lifecycleSnapshotService !== undefined) {
      const snapshot = source
        .map(lifecycleSnapshotSummary)
        .filter(Boolean)
        .slice(0, MAX_ACTIVITY_SUBAGENTS);
      return { changed: replaceNativeLifecycleSnapshot(snapshot, nowMs), snapshot };
    }

    const serviceIds = new Set();
    let changed = false;
    for (const record of source) {
      const metadata = normalizeSubagentMetadata(record, nativeSubagentStartedAtMs(record, nowMs));
      if (metadata === undefined) continue;
      // Older service shapes did not expose a typed lifecycle contract.
      if (lifecycleSnapshotService === undefined) delete metadata.status;
      serviceIds.add(metadata.id);
      if (isTerminalNativeSubagentRecord(record, service)) {
        if (rememberTerminalSubagent(metadata.id, nowMs)) changed = true;
        settledNotifications.onChildDisposed(metadata.id);
        continue;
      }
      if (!isActiveNativeSubagentRecord(record, service)) continue;
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
        runningSubagents.delete(id);
        settledNotifications.onChildDisposed(id);
        changed = true;
      }
    }
    return { changed };
  }

  function pollNativeLifecycleSnapshot() {
    if (lifecycleSnapshotSubagentService() === undefined) {
      traceNativeSubagent("extension_snapshot_poll_skipped", { reason: "lifecycle_service_unavailable" });
      return false;
    }
    const { changed, snapshot } = reconcileNativeSubagentService();
    if (snapshot === undefined) {
      traceNativeSubagent("extension_snapshot_poll_failed", { reason: "snapshot_unavailable" });
      return false;
    }
    traceNativeSubagent("extension_snapshot_reconciled", {
      state: changed ? "changed" : "unchanged", snapshot_count: snapshot.length, subagents: summarizeNativeSubagents(snapshot),
    });
    publishLifecycleSnapshot(snapshot);
    return changed;
  }

  function rememberParentActivityContext(context) {
    if (context !== undefined) parentActivityContext = context;
  }

  function contextSessionId(context) {
    return context?.sessionManager?.getSessionId?.();
  }

  function effectiveModelProjection(context) {
    const projection = modelProjection(context);
    const sessionId = contextSessionId(context);
    if (modelSwitchOverride?.sessionId !== undefined
      && modelSwitchOverride.sessionId === sessionId
      && modelSwitchOverride.model !== undefined) {
      const contextChanged = context !== modelSwitchOverride.sourceContext;
      const reflected = sameModelIdentity(projection.current, modelSwitchOverride.model);
      const locallyChanged = !sameModelIdentity(projection.current, modelSwitchOverride.sourceModel);
      if (contextChanged || reflected || locallyChanged) {
        modelSwitchOverride = undefined;
        return projection;
      }
      return { ...projection, current: modelSwitchOverride.model };
    }
    return projection;
  }

  function sameModelIdentity(left, right) {
    return left?.provider === right?.provider && left?.id === right?.id;
  }

  function rememberSuccessfulModelSwitch(context, model) {
    const projected = projectModelMetadata(model);
    const sessionId = contextSessionId(context);
    if (projected !== undefined && sessionId !== undefined) {
      modelSwitchOverride = {
        sessionId,
        model: projected,
        sourceContext: context,
        sourceModel: modelProjection(context).current,
      };
    }
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
    // Native lifecycle state changes only in the timer-driven snapshot poll. Activity
    // events must not produce lifecycle frames or read the native lifecycle projection.
    const reconciliation = reconcile && lifecycleSnapshotSubagentService() === undefined
      ? reconcileNativeSubagentService()
      : undefined;
    const reconciled = reconciliation?.changed === true;
    const subagents = publicSubagentSummaries();
    const subagentCount = Math.min(runningSubagents.size, MAX_ACTIVITY_SUBAGENTS);
    const model = effectiveModelProjection(parentActivityContext);
    const currentModel = model.current ?? undefined;
    const unchanged = lastActivitySent !== undefined
      && lastActivitySent.state === state
      && lastActivitySent.subagentCount === subagentCount
      && JSON.stringify(lastActivitySent.subagents) === JSON.stringify(subagents)
      && JSON.stringify(lastActivitySent.goal) === JSON.stringify(goalProjectionReceived ? goalProjection : undefined)
      && JSON.stringify(lastActivitySent.currentModel) === JSON.stringify(currentModel);
    // Settled with no subagents is terminal until reconciliation changes it; unlike
    // active work and settled subagents, it does not need a TTL refresh frame.
    if (unchanged && state === "settled" && subagentCount === 0 && !force && !reconciled) return false;
    if (unchanged && !force && !reconciled && nowMs - lastActivitySent.sentAtMs < ACTIVITY_REFRESH_MIN_MS) {
      return false;
    }
    try {
      client?.sendActivity?.(state, {
        subagentCount,
        subagents,
        ...(goalProjectionReceived ? { goal: goalProjection } : {}),
        currentModel,
      });
      lastActivitySent = {
        state,
        subagentCount,
        subagents,
        goal: goalProjectionReceived ? goalProjection : undefined,
        currentModel,
        sentAtMs: nowMs,
      };
      return true;
    } catch {
      // Activity is an ephemeral UI hint; mailbox delivery and reconnect behavior own real work.
      return false;
    }
  }

  function ensureActivityHeartbeatTimer() {
    if (activityHeartbeatTimer !== undefined) return;
    // Keep polling after the parent settles so lifecycle snapshots repair missed changes.
    activityHeartbeatTimer = setIntervalImpl(() => {
      // This is the only path that reads and publishes the native lifecycle projection.
      const changed = pollNativeLifecycleSnapshot();
      const state = parentActivityStateFromContext();
      const activitySent = sendActivityState(state);
      traceNativeSubagent("extension_lifecycle_heartbeat", {
        state, subagent_count: runningSubagents.size, published: activitySent, available: changed,
        heartbeat_interval_ms: ACTIVITY_HEARTBEAT_INTERVAL_MS,
      });
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
    // Keep polling while the parent is settled or waiting for native subagent results; the
    // native service remains the source of truth when lifecycle events are missed.
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
      spawnSubagent: captureNativeSentinelSpawner(() => nativeSubagentService(context.sessionManager.getSessionId())),
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
    refreshActivityState();
    ensureActivityHeartbeatTimer();
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
  const childReplyClientAgentIds = new Map();
  let parentAgentId;
  const inlineToolPolicy = createInlineToolPolicy({ now: nowImpl });
  const mailboxSendRateLimits = new Map();

  function reserveMailboxSend(sender, recipient) {
    const now = nowImpl();
    const key = `${sender ?? "unknown"}\u0000${recipient}`;
    const recent = (mailboxSendRateLimits.get(key) ?? []).filter(
      (reservation) => now - reservation.timestamp < MAILBOX_SEND_RATE_LIMIT_WINDOW_MS,
    );
    if (recent.length === 0) mailboxSendRateLimits.delete(key);
    else mailboxSendRateLimits.set(key, recent);
    if (recent.length >= MAILBOX_SEND_RATE_LIMIT_COUNT) {
      throw new Error("Temporary rate limit reached for this recipient. Stop sending and retry after 60 seconds, after the reset.");
    }
    const reservation = { timestamp: now };
    recent.push(reservation);
    mailboxSendRateLimits.set(key, recent);
    return { key, reservation };
  }

  function rollbackMailboxSend({ key, reservation }) {
    const reservations = mailboxSendRateLimits.get(key);
    if (reservations === undefined) return;
    const index = reservations.indexOf(reservation);
    if (index >= 0) reservations.splice(index, 1);
    if (reservations.length === 0) mailboxSendRateLimits.delete(key);
  }

  function mailboxSenderForSession(context) {
    const sessionId = context?.sessionManager?.getSessionId?.();
    return childReplyClientAgentIds.get(sessionId) ?? parentAgentId;
  }

  function mailboxClientForSession(context) {
    const sessionId = context?.sessionManager?.getSessionId?.();
    // Keep a disposed child identifiable until its shutdown cleanup; otherwise a late child send would use the parent client.
    return children.has(sessionId) || childReplyClients.has(sessionId)
      ? childReplyClients.get(sessionId)
      : client;
  }

  function modelSelector(input) {
    if (input === null || typeof input !== "object" || Array.isArray(input)
      || Object.keys(input).some((key) => !["provider", "id", "name"].includes(key))) {
      throw new ModelControlError("invalid_model", "model provider and id are required");
    }
    const provider = typeof input.provider === "string" ? input.provider.trim() : "";
    const id = typeof input.id === "string" ? input.id.trim() : "";
    if (provider.length === 0 || [...provider].length > MAX_MODEL_PROVIDER_CHARS
      || id.length === 0 || [...id].length > MAX_MODEL_ID_CHARS) {
      throw new ModelControlError(
        "invalid_model",
        `model provider and id must be non-empty strings of at most ${MAX_MODEL_PROVIDER_CHARS} and ${MAX_MODEL_ID_CHARS} characters`,
      );
    }
    return { provider, id };
  }

  async function switchPiModel(input, context) {
    const requested = modelSelector(input);
    const before = effectiveModelProjection(context);
    const nativeBefore = modelProjection(context);
    traceNativeSubagent("extension_model_switch_started", {
      requested_provider: requested.provider,
      requested_model_id: requested.id,
      ...(before.current?.provider === undefined ? {} : { current_provider: before.current.provider }),
      ...(before.current?.id === undefined ? {} : { current_model_id: before.current.id }),
    });
    if (typeof pi.setModel !== "function") {
      throw new ModelControlError("model_switch_unavailable", "Pi model switching is unavailable");
    }
    const availableModels = readAvailableModelsForContext(context);
    traceNativeSubagent("extension_model_registry_read", {
      requested_provider: requested.provider,
      requested_model_id: requested.id,
      available_count: availableModels.length,
    });
    const selectedModel = availableModels.find((candidate) => {
      const projected = projectModelMetadata(candidate);
      return projected?.provider === requested.provider && projected?.id === requested.id;
    });
    if (selectedModel === undefined) {
      throw new ModelControlError("model_unavailable", `Model ${requested.provider}/${requested.id} is not available`);
    }
    let switched;
    try {
      switched = await pi.setModel(selectedModel);
    } catch (error) {
      const after = modelProjection(context);
      if (!sameModelIdentity(nativeBefore.current, projectModelMetadata(selectedModel))
        && sameModelIdentity(after.current, projectModelMetadata(selectedModel))) {
        traceNativeSubagent("extension_model_set_result", {
          requested_provider: requested.provider,
          requested_model_id: requested.id,
          switched: true,
          mutation_before_throw: true,
        });
        return selectedModel;
      }
      throw error;
    }
    traceNativeSubagent("extension_model_set_result", {
      requested_provider: requested.provider,
      requested_model_id: requested.id,
      switched: switched === true,
    });
    if (switched !== true) {
      throw new ModelControlError("model_switch_rejected", `Pi rejected model ${requested.provider}/${requested.id}`);
    }
    return selectedModel;
  }

  function modelControlErrorCode(error) {
    return [
      "invalid_model",
      "unsupported_model_control",
      "model_registry_unavailable",
      "model_registry_failed",
      "model_unavailable",
      "model_switch_unavailable",
      "model_switch_rejected",
    ].includes(error?.code) ? error.code : "model_switch_failed";
  }

  async function performModelControl({ controlId, sourceAgentId, action, model }) {
    traceNativeSubagent("extension_model_control_entered", {
      control_id: controlId,
      source_agent_id: sourceAgentId,
      action,
      ...(model?.provider === undefined ? {} : { requested_provider: model.provider }),
      ...(model?.id === undefined ? {} : { requested_model_id: model.id }),
      available: parentActivityContext !== undefined,
    });
    try {
      if (action !== "set_model") {
        throw new ModelControlError("unsupported_model_control", "unsupported model control action");
      }
      const switchedModel = await switchPiModel(model, parentActivityContext);
      rememberSuccessfulModelSwitch(parentActivityContext, switchedModel);
      refreshActivityState(parentActivityContext);
      const projection = effectiveModelProjection(parentActivityContext);
      traceNativeSubagent("extension_model_control_completed", {
        control_id: controlId,
        source_agent_id: sourceAgentId,
        accepted: true,
        ...(projection.current?.provider === undefined ? {} : { current_provider: projection.current.provider }),
        ...(projection.current?.id === undefined ? {} : { current_model_id: projection.current.id }),
      });
      return {
        accepted: true,
        currentModel: projection.current,
      };
    } catch (error) {
      const projection = effectiveModelProjection(parentActivityContext);
      traceNativeSubagent("extension_model_control_completed", {
        control_id: controlId,
        source_agent_id: sourceAgentId,
        accepted: false,
        error_code: modelControlErrorCode(error),
        error_name: error?.name ?? "Error",
        ...(projection.current?.provider === undefined ? {} : { current_provider: projection.current.provider }),
        ...(projection.current?.id === undefined ? {} : { current_model_id: projection.current.id }),
      });
      return {
        accepted: false,
        errorCode: modelControlErrorCode(error),
        currentModel: projection.current,
      };
    }
  }

  function handleModelControl(control) {
    return performModelControl(control);
  }

  pi.registerTool({
    name: "mecha_parent_message",
    label: "Message parent",
    description: "Child-only: send an ordinary Mailbox message to this native subagent's target parent.",
    ...MECHA_MESSAGING_CURSOR_TOOL_METADATA.mecha_parent_message,
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
    name: "mecha_mailbox_send",
    label: "Send Mailbox message",
    description: "Send an ordinary Mailbox V2 message to another local agent.",
    ...MECHA_MESSAGING_CURSOR_TOOL_METADATA.mecha_mailbox_send,
    parameters: {
      type: "object",
      additionalProperties: false,
      required: ["recipient", "body"],
      properties: {
        recipient: { type: "string", minLength: 1 },
        body: { type: "string", minLength: 1 },
        subject: { type: "string" },
      },
    },
    async execute(_toolCallId, input, _signal, _onUpdate, toolContext) {
      try {
        const recipient = input?.recipient;
        const body = input?.body;
        const subject = input?.subject;
        if (typeof recipient !== "string" || recipient.length === 0) throw new Error("a mailbox recipient is required");
        assertAgentId(recipient);
        if (typeof body !== "string" || body.length === 0) throw new Error("a message body is required");
        if (subject !== undefined && typeof subject !== "string") throw new Error("a mailbox subject must be a string");

        const sessionId = toolContext?.sessionManager?.getSessionId?.();
        const mailboxClient = mailboxClientForSession(toolContext);
        // Native children must use only their initialized client; falling back to the parent would send under the wrong identity.
        if (mailboxClient === undefined) {
          if (children.has(sessionId) || childReplyClients.has(sessionId)) throw new Error("Mailbox v2 is not initialized for this native child session");
          throw startupError ?? new Error("Mailbox v2 is not initialized");
        }
        const reservation = reserveMailboxSend(mailboxSenderForSession(toolContext), recipient);
        let sent;
        try {
          sent = await mailboxClient.sendReply({ to: recipient, body, ...(subject === undefined ? {} : { subject }) });
        } catch (error) {
          rollbackMailboxSend(reservation);
          throw error;
        }
        return {
          content: [{ type: "text", text: `Mailbox message accepted (${sent.id}, ${sent.state}).` }],
          details: { message_id: sent.id, state: sent.state },
        };
      } catch (error) {
        return { content: [{ type: "text", text: error.message }], isError: true };
      }
    },
  });

  pi.registerTool({
    name: INLINE_TOOL_ALLOWANCE_TOOL_NAME,
    label: "Request inline tool allowance",
    description: "Request a temporary exception for genuinely necessary inline use of a named tool. This is an exception to the rule; subagents remain mandatory by default.",
    ...MECHA_MESSAGING_CURSOR_TOOL_METADATA[INLINE_TOOL_ALLOWANCE_TOOL_NAME],
    parameters: {
      type: "object",
      additionalProperties: false,
      required: ["toolName", "usageCount"],
      properties: {
        toolName: { type: "string", minLength: 1, maxLength: 64, description: "Canonical tool name (bash, edit, or write), or its functions.* alias." },
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
        const text = `Granted ${grant.grantedCount} temporary inline use(s) for ${grant.toolName}. This allowance is an exception to the rule; subagents remain mandatory by default.`;
        return { content: [{ type: "text", text }], details: grant };
      } catch (error) {
        return { content: [{ type: "text", text: error.message }], details: error.details ?? {}, isError: true };
      }
    },
  });

  if (typeof pi.registerTool === "function") pi.registerTool({
    name: "mecha_recall",
    label: "Mecha Recall",
    description: "Enable, disable, or inspect process-local Markdown recall.",
    ...MECHA_MESSAGING_CURSOR_TOOL_METADATA.mecha_recall,
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
    // Native lifecycle visibility is timer-driven. Keep this hook only for settled
    // notifications, which do not publish router lifecycle state.
    if (lifecycleSnapshotSubagentService() !== undefined) {
      traceNativeSubagent("extension_native_event_started", { subagent_id: event?.id, source: "event_snapshot_mode" });
      settledNotifications.onChildStarted(event?.id);
      return;
    }
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
  function publishTerminalLifecycleEvent(event) {
    if (lifecycleSnapshotSubagentService() !== undefined) {
      traceNativeSubagent("extension_native_event_terminal", { subagent_id: event?.id, status: event?.status, source: "event_snapshot_mode" });
      settledNotifications.onChildDisposed(event?.id);
      return;
    }
    const id = event?.id;
    const changed = rememberTerminalSubagent(id);
    settledNotifications.onChildDisposed(id);
    if (changed) refreshActivityState();
  }
  // Pi versions expose different terminal event names. Treat each as terminal so a queued
  // child cannot remain visible after a cancellation, steering action, or native shutdown.
  for (const eventName of [
    "subagents:completed", "subagents:failed", "subagents:canceled", "subagents:cancelled",
    "subagents:aborted", "subagents:terminated", "subagents:stopped", "subagents:steered",
  ]) {
    pi.events.on(eventName, (event) => publishTerminalLifecycleEvent(event));
  }

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
    ...MECHA_MESSAGING_CURSOR_TOOL_METADATA.mecha_settled_notifications,
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
    ...MECHA_MESSAGING_CURSOR_TOOL_METADATA.mecha_context,
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
    if (sessionId !== undefined) {
      children.add(sessionId);
      traceNativeSubagent("extension_child_session_created", { session_id: sessionId });
    }
  });
  pi.events.on("subagents:child:disposed", (event) => {
    const sessionId = childSessionId(event);
    if (sessionId !== undefined) {
      children.delete(sessionId);
      traceNativeSubagent("extension_child_session_disposed", { session_id: sessionId });
      childReplyClients.delete(sessionId);
      childReplyClientAgentIds.delete(sessionId);
      // The tombstone keeps late child tool calls from falling back to the parent client.
      childReplyClients.set(sessionId, undefined);
    }
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
  pi.on("session_shutdown", async (_event, context) => {
    // A disposed child stays in childReplyClients as a tombstone until this cleanup; do not run parent capture cleanup with its context.
    if (children.has(context.sessionManager.getSessionId()) || childReplyClients.has(context.sessionManager.getSessionId())) return;
    mailboxSendRateLimits.clear();
    recallController.stop(); const controller = new AbortController(); const deadlineAt = Date.now() + 10_000; const timer = setTimeout(() => controller.abort(), 10_000); try { const branch = context.sessionManager.getBranch(); const boundary = branch.map(({ type }) => type).lastIndexOf("compaction"); await (await ensureCapture(controller.signal)).shutdown(branch.slice(boundary + 1), { deadlineAt, signal: controller.signal }); } catch {} finally { controller.abort(); clearTimeout(timer); }
  });

  pi.on("session_start", (_event, context) => {
    const sessionId = context.sessionManager.getSessionId();
    if (children.has(sessionId)) {
      try {
        const parentAgentId = resolveCurrentAgentId();
        if (parentAgentId === undefined) return;
        const childAgentId = transientChildAgentId(sessionId);
        childReplyClients.set(sessionId, createClient({
          agentId: childAgentId,
          onEnvelope: () => {},
        }));
        childReplyClientAgentIds.set(sessionId, childAgentId);
      } catch {
        // A child without ordinary Mailbox send capability can still finish natively.
      }
      return;
    }
    mailboxSendRateLimits.clear();
    modelSwitchOverride = undefined;
    // Keep allowance state across agent runs; a parent session owns one pool for its lifetime.
    inlineToolPolicy.reset();
    mailboxSendRateLimits.clear();
    try {
      const agentId = resolveCurrentAgentId();
      if (agentId === undefined) return;
      parentAgentId = agentId;
      pi.sendMessage({
        customType: "mecha-worker-identity",
        content: `Your managed worker identity is ${agentId}.`,
        display: false,
        details: { agent_id: agentId },
      }, { deliverAs: "followUp", triggerTurn: false });
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
        parentSessionId: sessionId,
        onEnvelope: (message, delivery) => { if (!mailboxPausedForCompaction) coordinator?.onEnvelope(message, delivery); },
        onModelControl: handleModelControl,
        onModelControlTrace: ({ event, ...fields }) => traceNativeSubagent(event, fields),
        onGoalControl: handleGoalControl,
        onGoalControlTrace: ({ event, ...fields }) => traceNativeSubagent(event, fields),
        getGoalState: () => cloneGoalControlState(goalControlState),
        getModelCatalog: () => {
          try {
            return projectAvailableModels(parentActivityContext);
          } catch (error) {
            traceNativeSubagent("extension_model_catalog_read_failed", {
              error_code: modelControlErrorCode(error),
              error_name: error?.name ?? "Error",
            });
            throw error;
          }
        },
        getCurrentModel: () => effectiveModelProjection(parentActivityContext).current,
        onLifecycleReady: ({ routerVersion, reconnect }) => traceNativeSubagent("extension_client_lifecycle_ready", {
          router_version: routerVersion, reconnect,
        }),
        onLifecycleTrace: ({ event, ...fields }) => traceNativeSubagent(event, fields),
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
            publishGoalState(mailboxClient);
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
      publishGoalState(mailboxClient);
      coordinator = createMailboxCoordinator(context, mailboxClient);
      ensureActivityHeartbeatTimer();
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
    if (children.has(sessionId) || childReplyClients.has(sessionId)) {
      childReplyClients.delete(sessionId);
      childReplyClientAgentIds.delete(sessionId);
      return;
    }
    mailboxSendRateLimits.clear();
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
