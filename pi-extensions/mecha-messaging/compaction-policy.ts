/**
 * Resolves compaction thresholds without owning the session lifecycle. The
 * messaging extension uses the result to warn before context becomes costly and
 * to identify the explicit thresholds that must stop provider work.
 *
 * Policy inputs may come from tool calls or process environment, so this module
 * validates and normalizes them before they can affect a live session.
 */
import os from "node:os";
import path from "node:path";
import fs from "node:fs";

type Environment = NodeJS.ProcessEnv;
type ModelInput = { provider?: string; id?: string; name?: string; contextWindow?: number; maxTokens?: number };
type CompactResult = { tokensBefore?: number; estimatedTokensAfter?: number; firstKeptEntryId?: string; summary?: string };

export type CompactionLevelInput = number | { percent: number; guidance?: string; mandatory?: boolean };
export type CompactionLevel = { percent: number; guidance?: string; mandatory?: boolean };
type CompactionTierInput = { maxContextTokens?: number | null; levels: readonly CompactionLevelInput[] };
type CompactionModelEntry = CompactionPolicyInput & { key?: string; policy?: CompactionPolicyInput };
export type CompactionPolicyInput = { levels?: readonly CompactionLevelInput[]; tiers?: readonly CompactionTierInput[]; postCompactionNotification?: string | null; models?: Record<string, CompactionPolicyInput> | readonly CompactionModelEntry[] };
export type CompactionTier = { maxContextTokens: number | null; levels: CompactionLevel[] };
export type CompactionPolicy = { tiers: CompactionTier[]; postCompactionNotification?: string; models?: Record<string, CompactionPolicy> };
export type CompactionUsage = { percent: number | null; tokens: number | null; contextWindow: number; model?: ModelInput };
type CompactionUsageOrPercent = number | CompactionUsage;

const CONTEXT_THRESHOLD_SUFFIX = "Use workers or subagents for bounded side work to avoid context bloat.";
export const COMPACTION_POLICY_ENV = "MECHA_PI_COMPACTION_POLICY";
export const COMPACTION_POLICY_FILE_ENV = "MECHA_PI_COMPACTION_POLICY_FILE";
export const DEFAULT_COMPACTION_POLICY_PATH = path.join(os.homedir(), ".mecha-developer", "compaction-policy.json");
const COMPACTION_MODEL_KEY_ENV = "MECHA_PI_MODEL_KEY";
// Handoff notes are operator guidance, not a second transcript. Keep the
// injected message bounded even when policy comes from an external process.
const MAX_POST_COMPACTION_NOTE_CHARS = 4000;

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
export const GEMINI_FLASH_CONTEXT_LEVELS = Object.freeze([
  Object.freeze({ percent: 15, guidance: "consider compacting at a reasonable stopping point to keep future calls cheaper" }),
  Object.freeze({ percent: 20, guidance: "compact soon, ideally at the next clean boundary" }),
  Object.freeze({ percent: 25, guidance: "compact when practical; each call is getting meaningfully more expensive" }),
  Object.freeze({ percent: 30, guidance: "strongly consider compacting before more expensive work" }),
  Object.freeze({ percent: 35, guidance: "mandatory stop and compact before continuing", mandatory: true }),
]);
const OPUS_CONTEXT_LEVELS = Object.freeze(
  SMALL_CONTEXT_LEVELS.map((level, index) => Object.freeze({ ...level, percent: OPUS_LEVEL_PERCENTS[index] })),
);
// Tiers are ordered by context-window size below; null is the unbounded
// fallback. The upper bound is inclusive, so a window exactly at a boundary
// uses that tier's ladder.
const LARGE_CONTEXT_LEVELS = Object.freeze([
  Object.freeze({ percent: 20, guidance: "large-context awareness only; no compaction needed yet" }),
  Object.freeze({ percent: 30, guidance: "compact soon, ideally at the next clean boundary" }),
  Object.freeze({ percent: 40, guidance: "large-context calls are expensive; compact as soon as practical" }),
  Object.freeze({ percent: 45, guidance: "mandatory stop and compact before the session reaches 500k tokens", mandatory: true }),
]);
export const DEFAULT_COMPACTION_POLICY: CompactionPolicyInput = Object.freeze({
  tiers: Object.freeze([
    Object.freeze({ maxContextTokens: 300_000, levels: SMALL_CONTEXT_LEVELS }),
    Object.freeze({ maxContextTokens: 1_000_000, levels: LARGE_CONTEXT_LEVELS }),
    Object.freeze({ maxContextTokens: null, levels: LARGE_CONTEXT_LEVELS }),
  ]),
  models: Object.freeze({
    "anthropic/claude-opus-5": Object.freeze({ levels: OPUS_CONTEXT_LEVELS }),
    "claude-opus-5": Object.freeze({ levels: OPUS_CONTEXT_LEVELS }),
    "antigravity/gemini-3.8-flash": Object.freeze({ levels: GEMINI_FLASH_CONTEXT_LEVELS }),
    "google/gemini-3.8-flash": Object.freeze({ levels: GEMINI_FLASH_CONTEXT_LEVELS }),
    "gemini-3.8-flash": Object.freeze({ levels: GEMINI_FLASH_CONTEXT_LEVELS }),
  }),
});

/**
 * Trims optional handoff guidance and enforces the message-size limit. Empty
 * notes disappear, and runtime values from untyped configuration are rejected.
 */
export function normalizePostCompactionNotification(value: string | null | undefined): string | undefined {
  if (value === undefined || value === null) return undefined;
  if (typeof value !== "string") throw new TypeError("postCompactionNotification must be a string");
  const trimmed = value.trim();
  return trimmed.length === 0 ? undefined : trimmed.slice(0, MAX_POST_COMPACTION_NOTE_CHARS);
}

function normalizeCompactionLevel(level: CompactionLevelInput): CompactionLevel {
  const source = typeof level === "number" ? { percent: level } : level;
  if (source === null || typeof source !== "object") {
    throw new TypeError("each compaction level must be a number or object");
  }
  const percent = Number(source.percent);
  if (!Number.isFinite(percent) || percent <= 0 || percent >= 100) {
    throw new TypeError("compaction level percent must be greater than 0 and less than 100");
  }
  const normalized: CompactionLevel = { percent: Math.round(percent * 100) / 100 };
  if (typeof source.guidance === "string" && source.guidance.trim().length > 0) {
    normalized.guidance = source.guidance.trim();
  }
  if (source.mandatory === true) normalized.mandatory = true;
  return normalized;
}

function normalizeCompactionLevels(rawLevels: readonly CompactionLevelInput[]): CompactionLevel[] {
  // Duplicate percentages are collapsed after validation; the last entry wins,
  // then the levels are sorted so threshold selection is deterministic.
  if (!Array.isArray(rawLevels) || rawLevels.length === 0) {
    throw new TypeError("compaction policy requires at least one percentage level");
  }
  const byPercent = new Map();
  for (const level of rawLevels.map(normalizeCompactionLevel)) {
    byPercent.set(level.percent, level);
  }
  return [...byPercent.values()].sort((left, right) => left.percent - right.percent);
}

function normalizeCompactionTier(tier: CompactionTierInput): CompactionTier {
  // A null limit denotes the final fallback tier. Finite limits must be safe
  // positive integers because they define context-window routing boundaries.
  if (tier === null || typeof tier !== "object" || Array.isArray(tier)) {
    throw new TypeError("each compaction tier must be an object");
  }
  const maxContextTokens = tier.maxContextTokens ?? null;
  if (maxContextTokens !== null && (!Number.isSafeInteger(maxContextTokens) || maxContextTokens <= 0)) {
    throw new TypeError("compaction tier maxContextTokens must be a positive safe integer or null");
  }
  return { maxContextTokens, levels: normalizeCompactionLevels(tier.levels) };
}

// Model keys are compared case-insensitively and without surrounding spaces;
// this keeps environment values and model metadata on the same lookup path.
function normalizeModelKey(key: string | undefined): string {
  if (typeof key !== "string" || key.trim().length === 0) throw new TypeError("model policy key must be a non-empty string");
  return key.trim().toLowerCase();
}

function normalizeCompactionModels(rawModels: CompactionPolicyInput["models"]): Record<string, CompactionPolicy> | undefined {
  // Model overrides are recursively normalized, but nested model maps are not
  // processed, so one lookup has one unambiguous policy to apply.
  if (rawModels === undefined || rawModels === null) return undefined;
  const entries = Array.isArray(rawModels)
    ? rawModels.map((entry): [string | undefined, CompactionPolicyInput] => [entry.key, entry.policy ?? entry])
    : Object.entries(rawModels);
  const models: Record<string, CompactionPolicy> = {};
  for (const [key, value] of entries) {
    models[normalizeModelKey(key)] = resolveCompactionPolicy(value, { allowModels: false });
  }
  return Object.keys(models).length === 0 ? undefined : models;
}

function isLevelInputArray(input: CompactionPolicyInput | readonly CompactionLevelInput[]): input is readonly CompactionLevelInput[] {
  return Array.isArray(input);
}

function isLevelsPolicy(input: CompactionPolicyInput | readonly CompactionLevelInput[]): input is CompactionPolicyInput & { levels: readonly CompactionLevelInput[] } {
  return !isLevelInputArray(input) && input.levels !== undefined;
}

function isTierPolicy(input: CompactionPolicyInput | readonly CompactionLevelInput[]): input is CompactionPolicyInput & { tiers: readonly CompactionTierInput[] } {
  return !isLevelInputArray(input) && input.levels === undefined && input.tiers !== undefined;
}

function isStringArray(value: ModelInput | readonly string[] | undefined): value is readonly string[] {
  return Array.isArray(value) && value.every((item) => typeof item === "string");
}

/**
 * Accepts legacy percentage arrays and structured level/tier policies. Every
 * form is converted to sorted tiers and validated levels; model overrides are
 * resolved separately without processing another model map.
 */
export function resolveCompactionPolicy(input: CompactionPolicyInput | readonly CompactionLevelInput[] = DEFAULT_COMPACTION_POLICY, { allowModels = true }: { allowModels?: boolean } = {}): CompactionPolicy {
  if (isLevelInputArray(input)) {
    return { tiers: [{ maxContextTokens: null, levels: normalizeCompactionLevels(input) }] };
  }
  if (isLevelsPolicy(input)) {
    return {
      tiers: [{ maxContextTokens: null, levels: normalizeCompactionLevels(input.levels) }],
      postCompactionNotification: normalizePostCompactionNotification(input.postCompactionNotification),
      models: allowModels ? normalizeCompactionModels(input.models) : undefined,
    };
  }
  if (!isTierPolicy(input)) throw new TypeError("compaction policy requires default levels or at least one context-size tier");
  const rawTiers = input.tiers;
  if (rawTiers.length === 0) {
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

/**
 * Reads the optional environment policy as JSON, with the legacy comma-separated
 * percentage form as a fallback. Invalid external configuration throws instead
 * of silently changing the active thresholds; the caller can then report the
 * error and choose its known-safe default.
 */
export function resolveCompactionPolicyFile(environment: Environment = process.env): string | undefined {
  const configured = environment[COMPACTION_POLICY_FILE_ENV];
  const filePath = typeof configured === "string" && configured.trim().length > 0 ? configured.trim() : DEFAULT_COMPACTION_POLICY_PATH;
  try { fs.accessSync(filePath, fs.constants.R_OK); return filePath; } catch { return undefined; }
}

export function loadCompactionPolicyFromFile(filePath?: string): CompactionPolicy | undefined {
  if (filePath === undefined) return undefined;
  return resolveCompactionPolicy(JSON.parse(fs.readFileSync(filePath, "utf8")));
}

export function compactionPolicyFromEnvironment(environment: Environment = process.env): CompactionPolicy {
  const configured = environment[COMPACTION_POLICY_ENV];
  if (typeof configured !== "string" || configured.trim().length === 0) {
    const filePath = resolveCompactionPolicyFile(environment);
    return loadCompactionPolicyFromFile(filePath) ?? resolveCompactionPolicy(DEFAULT_COMPACTION_POLICY);
  }
  const trimmed = configured.trim();
  try {
    return resolveCompactionPolicy(JSON.parse(trimmed));
  } catch (jsonError) {
    if (!/^\d+(?:\.\d+)?(?:\s*,\s*\d+(?:\.\d+)?)*$/.test(trimmed)) throw jsonError;
    return resolveCompactionPolicy(trimmed.split(",").map((part) => Number(part.trim())));
  }
}

/**
 * Produces deterministic model lookup keys from most specific to least specific,
 * then appends the explicit environment fallback. The first configured match
 * therefore wins without depending on object key order.
 */
export function compactionModelKeys(model: ModelInput | undefined, environment: Environment = process.env): string[] {
  const keys: string[] = [];
  const add = (value: string | undefined) => {
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

export function effectiveCompactionPolicy(policy: CompactionPolicyInput | readonly CompactionLevelInput[], modelOrKeys: ModelInput | readonly string[] | undefined, environment: Environment = process.env) {
  const basePolicy = resolveCompactionPolicy(policy);
  const modelKeys = isStringArray(modelOrKeys) ? modelOrKeys.map((key) => normalizeModelKey(key)) : compactionModelKeys(modelOrKeys, environment);
  const modelPolicies = basePolicy.models ?? {};
  const matchedModelKey = modelKeys.find((key) => modelPolicies?.[key] !== undefined);
  return {
    policy: matchedModelKey === undefined ? basePolicy : modelPolicies[matchedModelKey],
    matchedModelKey,
    modelKeys,
  };
}

/**
 * Selects the first tier whose inclusive limit contains the context window.
 * Unknown windows use the smallest tier; oversized finite windows fall through
 * to the final tier, which is normally the unbounded fallback.
 */
export function selectCompactionTier(policy: CompactionPolicyInput | readonly CompactionLevelInput[], contextWindow: number | undefined, modelOrKeys: ModelInput | readonly string[] | undefined, environment: Environment = process.env) {
  const effective = effectiveCompactionPolicy(policy, modelOrKeys, environment);
  const tiers = effective.policy.tiers;
  const tier = !Number.isFinite(contextWindow)
    ? tiers[0]
    : tiers.find((candidate) => candidate.maxContextTokens === null || (contextWindow !== undefined && contextWindow <= candidate.maxContextTokens)) ?? tiers[tiers.length - 1];
  if (tier === undefined) throw new Error("compaction policy has no tiers");
  return { ...tier, matchedModelKey: effective.matchedModelKey, modelKeys: effective.modelKeys };
}

/**
 * Finds the highest crossed level above the notification watermark. This is the
 * advisory path: it reports guidance and does not itself force compaction.
 */
export function nextCompactionThreshold(policy: CompactionPolicyInput | readonly CompactionLevelInput[], usageOrPercent: CompactionUsageOrPercent, alreadyNotifiedPercent = 0, contextWindow: number | undefined = undefined, modelOrKeys: ModelInput | readonly string[] | undefined = undefined, environment: Environment = process.env) {
  const percent = typeof usageOrPercent === "object" && usageOrPercent !== null ? usageOrPercent.percent : usageOrPercent;
  const window = typeof usageOrPercent === "object" && usageOrPercent !== null ? usageOrPercent.contextWindow : contextWindow;
  const model = typeof usageOrPercent === "object" && usageOrPercent !== null && !Array.isArray(usageOrPercent) ? usageOrPercent.model : modelOrKeys;
  if (!Number.isFinite(percent)) return undefined;
  const tier = selectCompactionTier(policy, window, model, environment);
  const level = tier.levels
    .filter((candidate) => typeof percent === "number" && percent >= candidate.percent && candidate.percent > alreadyNotifiedPercent)
    .at(-1);
  return level === undefined ? undefined : { ...level, tier };
}

/**
 * Finds the first crossed level explicitly marked mandatory. The lifecycle guard
 * uses this separate result to stop provider work; advisory levels never become
 * mandatory merely because usage is high.
 */
export function hardCompactionThreshold(policy: CompactionPolicyInput | readonly CompactionLevelInput[], usage: CompactionUsage | undefined, modelOrKeys: ModelInput | readonly string[] | undefined = undefined, environment: Environment = process.env) {
  if (usage === undefined || !Number.isFinite(usage.percent) || usage.percent === null) return undefined;
  const tier = selectCompactionTier(policy, usage.contextWindow, usage.model ?? modelOrKeys, environment);
  const level = tier.levels.find((candidate) => candidate.mandatory === true && usage.percent !== null && usage.percent >= candidate.percent);
  return level === undefined ? undefined : { ...level, tier };
}

/**
 * Formats normalized handoff guidance with compact status metadata. It reports
 * summary length but omits summary text, keeping private or large summaries out
 * of the follow-up message.
 */
export function postCompactionNotificationMessage(note: string, result: CompactResult = {}) {
  const fields = [];
  if (typeof result.tokensBefore === "number" && Number.isFinite(result.tokensBefore)) fields.push(`tokens before: ${result.tokensBefore.toLocaleString()}`);
  if (typeof result.estimatedTokensAfter === "number" && Number.isFinite(result.estimatedTokensAfter)) fields.push(`estimated tokens after: ${result.estimatedTokensAfter.toLocaleString()}`);
  if (typeof result.firstKeptEntryId === "string" && result.firstKeptEntryId.length > 0) fields.push(`first kept entry: ${result.firstKeptEntryId}`);
  if (typeof result.summary === "string") fields.push(`summary chars: ${result.summary.length.toLocaleString()}`);
  const status = fields.length > 0 ? `\n\nCompaction status: ${fields.join("; ")}.` : "";
  return `Post-compaction handoff note:\n\n${note}${status}`;
}

export function compactionNotificationMessage({ level, usage }: { level: CompactionLevel; usage: CompactionUsage }) {
  const roundedPercent = usage.percent === null ? level.percent : Math.round(usage.percent * 10) / 10;
  const tokenText = usage.tokens === null
    ? `${roundedPercent}% of ${usage.contextWindow.toLocaleString()} context tokens`
    : `${usage.tokens.toLocaleString()} / ${usage.contextWindow.toLocaleString()} context tokens (${roundedPercent}%)`;
  const guidance = level.guidance ?? (level.mandatory ? "mandatory threshold reached" : "threshold reached");
  return `Pi context threshold reached: ${tokenText}. Guidance at ${level.percent}%: ${guidance}. ${CONTEXT_THRESHOLD_SUFFIX}`;
}
