import assert from "node:assert/strict";
import test from "node:test";

import {
  DEFAULT_COMPACTION_POLICY,
  compactionModelKeys,
  compactionNotificationMessage,
  compactionPolicyFromEnvironment,
  effectiveCompactionPolicy,
  nextCompactionThreshold,
  postCompactionNotificationMessage,
  resolveCompactionPolicy,
  selectCompactionTier,
} from "../pi-extensions/mecha-messaging/index.ts";
import { hardCompactionThreshold, normalizePostCompactionNotification } from "../pi-extensions/mecha-messaging/compaction-policy.ts";

test("default and legacy compaction policy inputs normalize to stable tiers", () => {
  const defaults = resolveCompactionPolicy();
  assert.deepEqual(defaults.tiers[0].levels.map((level) => level.percent), [40, 50, 60, 70, 80, 90]);
  assert.equal(defaults.tiers[0].maxContextTokens, 300_000);
  assert.equal(selectCompactionTier(DEFAULT_COMPACTION_POLICY, undefined, undefined, {}).maxContextTokens, 300_000);
  assert.deepEqual(compactionPolicyFromEnvironment({}).tiers, defaults.tiers);
  assert.deepEqual(resolveCompactionPolicy([60, 40, 60]).tiers[0].levels.map((level) => level.percent), [40, 60]);
  assert.deepEqual(compactionPolicyFromEnvironment({ MECHA_PI_COMPACTION_POLICY: "45, 75" }).tiers[0].levels.map((level) => level.percent), [45, 75]);
  assert.deepEqual(resolveCompactionPolicy({ levels: [90], models: [] }).models, undefined);
  assert.deepEqual(DEFAULT_COMPACTION_POLICY.tiers.map((tier) => tier.maxContextTokens), [300_000, 1_000_000, null]);
});

test("malformed compaction policy inputs fail with the existing validation errors", () => {
  assert.throws(() => resolveCompactionPolicy([]), {
    name: "TypeError",
    message: "compaction policy requires at least one percentage level",
  });
  assert.throws(() => compactionPolicyFromEnvironment({ MECHA_PI_COMPACTION_POLICY: JSON.stringify({}) }), {
    name: "TypeError",
    message: "compaction policy requires default levels or at least one context-size tier",
  });
  assert.throws(() => compactionPolicyFromEnvironment({ MECHA_PI_COMPACTION_POLICY: JSON.stringify({ levels: [] }) }), {
    name: "TypeError",
    message: "compaction policy requires at least one percentage level",
  });
  assert.throws(() => compactionPolicyFromEnvironment({ MECHA_PI_COMPACTION_POLICY: JSON.stringify({ levels: [0] }) }), {
    name: "TypeError",
    message: "compaction level percent must be greater than 0 and less than 100",
  });
  assert.throws(() => compactionPolicyFromEnvironment({ MECHA_PI_COMPACTION_POLICY: JSON.stringify({ levels: [null] }) }), {
    name: "TypeError",
    message: "each compaction level must be a number or object",
  });
  assert.throws(() => compactionPolicyFromEnvironment({ MECHA_PI_COMPACTION_POLICY: JSON.stringify({ tiers: [] }) }), {
    name: "TypeError",
    message: "compaction policy requires default levels or at least one context-size tier",
  });
  assert.throws(() => compactionPolicyFromEnvironment({ MECHA_PI_COMPACTION_POLICY: JSON.stringify({ tiers: [null] }) }), {
    name: "TypeError",
    message: "each compaction tier must be an object",
  });
  assert.throws(() => compactionPolicyFromEnvironment({ MECHA_PI_COMPACTION_POLICY: JSON.stringify({ tiers: [{ maxContextTokens: 0, levels: [40] }] }) }), {
    name: "TypeError",
    message: "compaction tier maxContextTokens must be a positive safe integer or null",
  });
  assert.throws(() => compactionPolicyFromEnvironment({ MECHA_PI_COMPACTION_POLICY: "not-json" }), SyntaxError);
});

test("model policy keys take precedence over the environment fallback in stable order", () => {
  const model = { provider: "OpenAI-Codex", id: "custom-model", name: "Custom Model" };
  const environment = { MECHA_PI_MODEL_KEY: "environment-model" };
  assert.deepEqual(compactionModelKeys(model, environment), [
    "openai-codex/custom-model",
    "openai-codex:custom model",
    "custom-model",
    "custom model",
    "environment-model",
  ]);

  const policy = resolveCompactionPolicy({
    levels: [90],
    models: {
      "openai-codex/custom-model": { levels: [20] },
      "environment-model": { levels: [30] },
    },
  });
  const fromModel = effectiveCompactionPolicy(policy, model, environment);
  assert.equal(fromModel.matchedModelKey, "openai-codex/custom-model");
  assert.deepEqual(fromModel.policy.tiers[0].levels.map((level) => level.percent), [20]);

  const fromEnvironment = effectiveCompactionPolicy(policy, undefined, environment);
  assert.equal(fromEnvironment.matchedModelKey, "environment-model");
  assert.deepEqual(fromEnvironment.policy.tiers[0].levels.map((level) => level.percent), [30]);

  const fromExplicitKeys = effectiveCompactionPolicy(policy, [" ENVIRONMENT-MODEL "], {});
  assert.equal(fromExplicitKeys.matchedModelKey, "environment-model");
});

test("next compaction threshold selects the highest crossed level not already notified", () => {
  const policy = { levels: [40, 50, 60, 70] };
  const usage = { percent: 65, tokens: 65_000, contextWindow: 100_000 };
  assert.equal(nextCompactionThreshold(policy, usage, 40).percent, 60);
  assert.equal(nextCompactionThreshold(policy, usage, 60), undefined);
  assert.equal(nextCompactionThreshold(policy, { ...usage, percent: null }), undefined);
  assert.equal(hardCompactionThreshold({ levels: [{ percent: 60, mandatory: true }] }, usage)?.percent, 60);
  assert.equal(hardCompactionThreshold(policy, usage), undefined);
  assert.equal(hardCompactionThreshold(policy, undefined), undefined);
});

test("post-compaction handoff notes are trimmed, bounded, and omit summary text", () => {
  const normalized = normalizePostCompactionNotification(`  ${"resume ".repeat(1_000)}  `);
  assert.equal(normalized?.length, 4_000);
  assert.equal(normalized?.startsWith("resume resume"), true);
  assert.equal(normalized?.endsWith(" "), false);
  assert.equal(normalizePostCompactionNotification("   "), undefined);
  assert.equal(normalizePostCompactionNotification(null), undefined);

  const message = postCompactionNotificationMessage(normalized ?? "", {
    summary: "private summary text",
    tokensBefore: 123,
    estimatedTokensAfter: 45,
    firstKeptEntryId: "entry-1",
  });
  assert.match(message, /Post-compaction handoff note:/);
  assert.match(message, /tokens before: 123/);
  assert.match(message, /estimated tokens after: 45/);
  assert.match(message, /first kept entry: entry-1/);
  assert.match(message, /summary chars: 20/);
  assert.doesNotMatch(message, /private summary text/);

  assert.match(compactionNotificationMessage({
    level: { percent: 40, mandatory: true },
    usage: { percent: null, tokens: null, contextWindow: 100_000 },
  }), /40% of 100,000 context tokens/);
  assert.match(compactionNotificationMessage({
    level: { percent: 40 },
    usage: { percent: 40.55, tokens: 40_550, contextWindow: 100_000 },
  }), /Guidance at 40%: threshold reached/);
});
