import assert from "node:assert/strict";
import test from "node:test";

import {
  createCompactionOrchestration,
  type CompactionContext,
  type CompactionOrchestrationOptions,
} from "../pi-extensions/mecha-messaging/compaction-orchestration.ts";
import { resolveCompactionPolicy } from "../pi-extensions/mecha-messaging/compaction-policy.ts";

type CompactCall = Parameters<NonNullable<CompactionContext["compact"]>>[0];

type Fixture = {
  orchestration: ReturnType<typeof createCompactionOrchestration>;
  context: CompactionContext;
  childContext: CompactionContext;
  compactCalls: CompactCall[];
  notifications: Array<[string, string | undefined]>;
  statuses: Array<[string, string | undefined]>;
  activity: string[];
  sent: Array<[object, object | undefined]>;
  scheduled: Array<() => void>;
  captures: Array<{ kind: string; messages: unknown; signal?: AbortSignal }>;
  aborts: number[];
  order: string[];
  delays: number[];
};

const model = { provider: "fixture", id: "model", name: "Fixture model", contextWindow: 100_000 };
const environment = { MECHA_PI_MODEL_KEY: "fixture/model" };
const initialPolicy = resolveCompactionPolicy({
  levels: [{ percent: 40, guidance: "advisory" }, { percent: 80, mandatory: true }],
  postCompactionNotification: "Default handoff",
  models: { "fixture/model": { levels: [{ percent: 20, guidance: "model advisory" }, { percent: 60, mandatory: true }], postCompactionNotification: "Model handoff" } },
});

function fixture(overrides: Partial<{
  client: object | undefined;
  initialPolicyError: Error;
  sendMessageThrows: boolean;
  captureLoaderThrows: boolean;
}> = {}): Fixture {
  const compactCalls: CompactCall[] = [];
  const notifications: Array<[string, string | undefined]> = [];
  const statuses: Array<[string, string | undefined]> = [];
  const activity: string[] = [];
  const sent: Array<[object, object | undefined]> = [];
  const scheduled: Array<() => void> = [];
  const captures: Array<{ kind: string; messages: unknown; signal?: AbortSignal }> = [];
  const order: string[] = [];
  const delays: number[] = [];
  let idle = true;
  let usage = { percent: 35, tokens: 35_000, contextWindow: 100_000 };
  const aborts: number[] = [];
  const makeContext = (sessionId: string): CompactionContext => ({
    sessionManager: { getSessionId: () => sessionId },
    model,
    ui: {
      notify: (message, level) => notifications.push([message, level]),
      setStatus: (key, value) => statuses.push([key, value]),
    },
    isIdle: () => idle,
    getContextUsage: () => usage,
    compact: (options) => compactCalls.push(options),
    abort: () => { aborts.push(1); },
  });
  const context = makeContext("parent");
  const childContext = makeContext("child");
  const client = Object.hasOwn(overrides, "client") ? overrides.client : { sendMessage: () => {} };
  function fakeSetTimeout(callback: string | Function, delay?: number): number;
  function fakeSetTimeout<TArgs extends unknown[]>(callback: (...args: TArgs) => void, delay?: number, ...args: TArgs): NodeJS.Timeout;
  function fakeSetTimeout(callback: string | Function, delay?: number): number | NodeJS.Timeout {
    if (typeof callback === "function") scheduled.push(() => { Reflect.apply(callback, undefined, []); });
    delays.push(delay ?? 0);
    return 1;
  }
  const setTimeoutImpl = Object.assign(fakeSetTimeout, { __promisify__: setTimeout.__promisify__ });
  const options: CompactionOrchestrationOptions = {
    getClient: () => client,
    isChildSession: (sessionId) => sessionId === "child",
    rememberParentContext: () => { order.push("remember"); },
    activity: {
      onCompactionStarted: () => { activity.push("started"); order.push("activity-started"); },
      onCompactionResumed: () => { activity.push("resumed"); order.push("activity-resumed"); },
    },
    setMailboxStatus: (_context, value) => { statuses.push(["mecha-mailbox", `mailbox: ${value}`]); order.push(`status-${value}`); },
    sendMessage: (message, messageOptions) => {
      if (overrides.sendMessageThrows) throw new Error("handoff failed");
      sent.push([message, messageOptions]);
    },
    ensureCapture: async () => {
      if (overrides.captureLoaderThrows) throw new Error("capture loader failed");
      return {
        compact: async (messages, captureOptions) => {
          captures.push({ kind: "compact", messages, signal: captureOptions?.signal });
          return {};
        },
        shutdown: async (messages, shutdownOptions) => {
          captures.push({ kind: "shutdown", messages, signal: shutdownOptions?.signal });
          return {};
        },
      };
    },
    getEnvironment: () => environment,
    initialPolicy,
    initialPolicyError: overrides.initialPolicyError,
    errorMessage: (error) => error instanceof Error ? error.message : String(error),
    setTimeoutImpl,
  };
  const orchestration = createCompactionOrchestration(options);
  const setIdle = (value: boolean) => { idle = value; };
  const setUsage = (value: typeof usage) => { usage = value; };
  Object.assign(context, { setIdle, setUsage });
  Object.assign(childContext, { setIdle, setUsage });
  return { orchestration, context, childContext, compactCalls, notifications, statuses, activity, sent, scheduled, captures, aborts, order, delays };
}

function complete(call: CompactCall, result = {}): void {
  assert.equal(typeof call.onComplete, "function");
  call.onComplete?.(result);
}

function fail(call: CompactCall, error: Error): void {
  assert.equal(typeof call.onError, "function");
  call.onError?.(error);
}

test("initial policy and model overrides flow through status, self notes, and threshold monitoring", () => {
  const f = fixture({ initialPolicyError: new Error("invalid environment policy") });
  const status = f.orchestration.status(f.context);
  const noUsage = { ...f.context, getContextUsage: undefined };
  assert.equal(f.orchestration.status(noUsage).selectedTier, undefined);
  assert.equal(status.matchedModelKey, "fixture/model");
  assert.deepEqual(status.modelKeys, ["fixture/model", "fixture:fixture model", "model", "fixture model"]);
  assert.equal(status.selectedTier?.levels[0]?.percent, 20);
  assert.equal(status.configurationError, "invalid environment policy");

  const request = f.orchestration.requestSelfCompaction({ customInstructions: "keep this" }, f.context);
  assert.equal(request.postCompactionNotification, "Model handoff");
  const custom = f.orchestration.requestSelfCompaction({ postCompactionNotification: "Command note" }, f.context);
  assert.equal(custom.postCompactionNotification, "Command note");

  f.orchestration.monitorContextThreshold(f.context);
  assert.equal((f.sent[0]?.[0] as { details?: { threshold_percent?: number } }).details?.threshold_percent, 20);
  f.orchestration.setPolicy({ levels: [{ percent: 30, mandatory: true }], postCompactionNotification: "New note" }, f.context);
  assert.equal(f.orchestration.status(f.context).notifiedThroughPercent, 0);
  f.orchestration.monitorContextThreshold(f.context);
  assert.equal(f.sent.length, 2);
  assert.equal((f.sent[1]?.[0] as { details?: { threshold_percent?: number } }).details?.threshold_percent, 30);
});

test("initial policy fallback remains observable and policy replacement resets the watermark", () => {
  const f = fixture({ initialPolicyError: new Error("bad policy") });
  const status = f.orchestration.status(f.context);
  assert.equal(status.configurationError, "bad policy");
  assert.equal(status.policy.tiers.length > 0, true);
  f.orchestration.monitorContextThreshold(f.context);
  assert.equal(f.orchestration.status(f.context).notifiedThroughPercent, 20);
  f.orchestration.setPolicy([50], f.context);
  assert.equal(f.orchestration.status(f.context).notifiedThroughPercent, 0);
});

test("self-compaction is last-write-wins, child-armed, idle-gated, and consumes one pending slot", () => {
  const f = fixture();
  f.orchestration.requestSelfCompaction({ customInstructions: "first" }, f.context);
  f.orchestration.requestSelfCompaction({ customInstructions: "second" }, f.context);
  assert.equal(f.orchestration.hasPendingSelfCompaction(), true);
  f.orchestration.runPendingSelfCompaction(f.childContext);
  assert.equal(f.compactCalls.length, 0);
  assert.equal(f.orchestration.hasPendingSelfCompaction(), true);
  Object.assign(f.context, { isIdle: () => false });
  f.orchestration.runPendingSelfCompaction(f.context);
  assert.equal(f.compactCalls.length, 0);
  Object.assign(f.context, { isIdle: () => true });
  f.orchestration.runPendingSelfCompaction(f.context);
  assert.equal(f.compactCalls.length, 1);
  assert.equal(f.compactCalls[0]?.customInstructions, "second");
  assert.equal(f.orchestration.hasPendingSelfCompaction(), false);
  complete(f.compactCalls[0] ?? {});
  assert.equal(f.orchestration.isMailboxPaused(), false);
});

test("an older self callback keeps its baseline side effects after a newer request", () => {
  const f = fixture();
  f.orchestration.requestSelfCompaction({ postCompactionNotification: "old" }, f.context);
  f.orchestration.runPendingSelfCompaction(f.context);
  const first = f.compactCalls[0];
  f.orchestration.requestSelfCompaction({ postCompactionNotification: "new" }, f.context);
  complete(first ?? {});
  assert.equal(f.scheduled.length, 1);
  f.scheduled[0]?.();
  assert.match(String((f.sent[0]?.[0] as { content?: string }).content), /old/);
  f.orchestration.runPendingSelfCompaction(f.context);
  assert.equal(f.compactCalls.length, 2);
});

test("self completion resets the watermark and handoff is deferred; self errors resume and notify", () => {
  const f = fixture();
  f.orchestration.monitorContextThreshold(f.context);
  assert.equal(f.orchestration.status(f.context).notifiedThroughPercent, 20);
  f.sent.length = 0;
  f.orchestration.requestSelfCompaction({ postCompactionNotification: "handoff" }, f.context);
  f.orchestration.runPendingSelfCompaction(f.context);
  complete(f.compactCalls[0] ?? {}, { tokensBefore: 10, estimatedTokensAfter: 5, firstKeptEntryId: "entry", summary: "secret" });
  assert.equal(f.orchestration.status(f.context).notifiedThroughPercent, 0);
  assert.equal(f.sent.length, 0);
  f.scheduled[0]?.();
  assert.deepEqual((f.sent[0]?.[0] as { details?: object }).details, {
    tokens_before: 10,
    estimated_tokens_after: 5,
    first_kept_entry_id: "entry",
    summary_chars: 6,
  });

  f.orchestration.requestSelfCompaction({}, f.context);
  f.orchestration.runPendingSelfCompaction(f.context);
  fail(f.compactCalls[1] ?? {}, new Error("self failed"));
  assert.deepEqual(f.notifications.at(-1), ["Self-compaction failed: self failed", "error"]);

  const failingHandoff = fixture({ sendMessageThrows: true });
  failingHandoff.orchestration.requestSelfCompaction({ postCompactionNotification: "handoff" }, failingHandoff.context);
  failingHandoff.orchestration.runPendingSelfCompaction(failingHandoff.context);
  complete(failingHandoff.compactCalls[0] ?? {});
  assert.doesNotThrow(() => failingHandoff.scheduled[0]?.());

  f.orchestration.setPolicy([50], f.context);
  f.orchestration.requestSelfCompaction({}, f.context);
  f.orchestration.runPendingSelfCompaction(f.context);
  complete(f.compactCalls[2] ?? {});
  assert.equal(f.scheduled.length, 1);
});

test("missing self compaction is reported at request and execution boundaries", () => {
  const f = fixture();
  const withoutCompact: CompactionContext = { ...f.context, compact: undefined };
  assert.throws(() => f.orchestration.requestSelfCompaction({}, withoutCompact), /Pi compaction is unavailable in this context/);
  f.orchestration.requestSelfCompaction({}, f.context);
  f.orchestration.runPendingSelfCompaction(withoutCompact);
  assert.equal(f.orchestration.hasPendingSelfCompaction(), false);
  assert.deepEqual(f.notifications.at(-1), ["Self-compaction is unavailable at the idle boundary; use /compact manually.", "error"]);
});

test("hard compaction guards missing and advisory usage, re-entry, identity, success, and failures", () => {
  const f = fixture();
  Object.assign(f.context, { getContextUsage: () => undefined });
  assert.equal(f.orchestration.enforceHardCompaction(f.context), false);
  Object.assign(f.context, { getContextUsage: () => ({ percent: 30, tokens: 30_000, contextWindow: 100_000 }) });
  assert.equal(f.orchestration.enforceHardCompaction(f.context), false);
  Object.assign(f.context, { getContextUsage: () => ({ percent: 70, tokens: 70_000, contextWindow: 100_000 }) });
  assert.equal(f.orchestration.enforceHardCompaction(f.context), true);
  assert.equal(f.orchestration.hasPendingHardCompaction(), true);
  assert.equal(f.orchestration.enforceHardCompaction(f.context), true);
  assert.equal(f.aborts.length, 2);
  assert.equal(f.compactCalls.length, 1);
  const first = f.compactCalls[0];
  complete(first ?? {});
  assert.equal(f.orchestration.hasPendingHardCompaction(), false);
  assert.equal(f.orchestration.enforceHardCompaction(f.context), true);
  assert.equal(f.compactCalls.length, 2);
  complete(first ?? {});
  fail(first ?? {}, new Error("stale"));
  assert.equal(f.orchestration.hasPendingHardCompaction(), true);
  fail(f.compactCalls[1] ?? {}, new Error("hard failed"));
  assert.equal(f.orchestration.hasPendingHardCompaction(), false);
  assert.deepEqual(f.notifications.at(-1), ["Hard context compaction failed: hard failed", "error"]);

  const unavailable = { ...f.context, compact: undefined };
  assert.equal(f.orchestration.enforceHardCompaction(unavailable), true);
  assert.deepEqual(f.notifications.at(-1), ["Pi context limit reached, but compaction is unavailable.", "error"]);
});

test("hard synchronous compact errors are contained and empty completion clears the block", () => {
  const f = fixture();
  Object.assign(f.context, {
    getContextUsage: () => ({ percent: 90, tokens: 90_000, contextWindow: 100_000 }),
    compact: () => { throw new Error("sync hard failure"); },
  });
  assert.equal(f.orchestration.enforceHardCompaction(f.context), true);
  assert.equal(f.orchestration.hasPendingHardCompaction(), false);
  assert.deepEqual(f.notifications.at(-1), ["Hard context compaction failed: sync hard failure", "error"]);
});

test("threshold monitoring sends advisory and mandatory options, tracks watermark, and suppresses children", () => {
  const f = fixture();
  Object.assign(f.context, { getContextUsage: () => ({ percent: 25, tokens: 25_000, contextWindow: 100_000 }) });
  f.orchestration.monitorContextThreshold(f.context);
  assert.deepEqual(f.sent[0]?.[1], { deliverAs: "followUp", triggerTurn: true });
  Object.assign(f.context, { getContextUsage: () => ({ percent: 70, tokens: 70_000, contextWindow: 100_000 }) });
  f.orchestration.monitorContextThreshold(f.context);
  assert.deepEqual(f.sent[1]?.[1], { triggerTurn: false });
  const sent = f.sent.length;
  f.orchestration.monitorContextThreshold(f.childContext);
  assert.equal(f.sent.length, sent);
  Object.assign(f.context, { getContextUsage: () => undefined });
  f.orchestration.monitorContextThreshold(f.context);
  Object.assign(f.context, { getContextUsage: () => ({ percent: 25, tokens: 25_000, contextWindow: 100_000 }) });
  f.orchestration.monitorContextThreshold(f.context);
  assert.equal(f.sent.length, sent);
});

test("compaction start pauses in order, captures concatenated preparation, and abort resumes", async () => {
  const f = fixture();
  const controller = new AbortController();
  await f.orchestration.onSessionBeforeCompact({
    signal: controller.signal,
    preparation: { messagesToSummarize: ["summary"], turnPrefixMessages: ["prefix"] },
  }, f.context);
  assert.deepEqual(f.order, ["remember", "activity-started", "status-compacting"]);
  assert.deepEqual(f.captures[0]?.messages, ["summary", "prefix"]);
  assert.equal(f.orchestration.isMailboxPaused(), true);
  controller.abort();
  assert.deepEqual(f.order, ["remember", "activity-started", "status-compacting", "remember", "activity-resumed", "status-connected"]);
  assert.equal(f.orchestration.isMailboxPaused(), false);

  const noClient = fixture({ client: undefined });
  const noClientController = new AbortController();
  await noClient.orchestration.onSessionBeforeCompact({ signal: noClientController.signal, preparation: {} }, noClient.context);
  assert.equal(noClient.orchestration.isMailboxPaused(), false);
  assert.equal(noClient.captures.length, 1);
  noClientController.abort();

  const aborted = fixture();
  const abortedController = new AbortController();
  abortedController.abort();
  await aborted.orchestration.onSessionBeforeCompact({ signal: abortedController.signal, preparation: {} }, aborted.context);
  assert.equal(aborted.orchestration.isMailboxPaused(), false);

  const child = fixture();
  child.orchestration.onSessionCompact(child.childContext);
  assert.equal(child.orchestration.status(child.context).notifiedThroughPercent, 0);
});

test("already-paused compaction suppresses pause side effects but still captures and listens for abort", async () => {
  const f = fixture();
  await f.orchestration.onSessionBeforeCompact({ preparation: { messagesToSummarize: ["first"] } }, f.context);
  const pauseOrder = [...f.order];
  const controller = new AbortController();

  await f.orchestration.onSessionBeforeCompact({
    signal: controller.signal,
    preparation: { messagesToSummarize: ["summary"], turnPrefixMessages: ["prefix"] },
  }, f.context);

  assert.deepEqual(f.order, pauseOrder);
  assert.equal(f.orchestration.isMailboxPaused(), true);
  assert.deepEqual(f.captures[1]?.messages, ["summary", "prefix"]);
  assert.equal(f.captures[1]?.signal, controller.signal);

  controller.abort();
  assert.deepEqual(f.order, [...pauseOrder, "remember", "activity-resumed", "status-connected"]);
  assert.equal(f.orchestration.isMailboxPaused(), false);
});

test("capture loader rejection stays contained and does not unconditionally resume", async () => {
  const f = fixture({ captureLoaderThrows: true });
  await assert.doesNotReject(() => f.orchestration.onSessionBeforeCompact({ preparation: {} }, f.context));
  assert.equal(f.orchestration.isMailboxPaused(), true);
  assert.deepEqual(f.order, ["remember", "activity-started", "status-compacting"]);
  assert.equal(f.captures.length, 0);
});

test("capture failures stay contained and do not unconditionally resume", async () => {
  const f = fixture();
  const errorCapture: CompactionOrchestrationOptions["ensureCapture"] = async () => ({
    compact: async () => { throw new Error("capture failed"); },
    shutdown: async () => ({}),
  });
  const orchestration = createCompactionOrchestration({
    getClient: () => ({ sendMessage: () => {} }),
    isChildSession: () => false,
    rememberParentContext: () => {},
    activity: { onCompactionStarted: () => {}, onCompactionResumed: () => {} },
    setMailboxStatus: () => {},
    sendMessage: () => {},
    ensureCapture: errorCapture,
    getEnvironment: () => ({}),
    initialPolicy,
    errorMessage: String,
    setTimeoutImpl: setTimeout,
  });
  await orchestration.onSessionBeforeCompact({ preparation: {} }, f.context);
  assert.equal(orchestration.isMailboxPaused(), true);
});

test("empty self completion unblocks, resumes, resets thresholds, and defers an exact handoff payload", async () => {
  const f = fixture();
  f.orchestration.monitorContextThreshold(f.context);
  assert.equal(f.orchestration.status(f.context).notifiedThroughPercent, 20);
  f.sent.length = 0;

  await f.orchestration.onSessionBeforeCompact({ preparation: {} }, f.context);
  f.orchestration.requestSelfCompaction({ postCompactionNotification: "handoff" }, f.context);
  f.orchestration.runPendingSelfCompaction(f.context);
  assert.equal(f.orchestration.hasPendingSelfCompaction(), false);
  assert.equal(f.orchestration.isMailboxPaused(), true);

  complete(f.compactCalls[0] ?? {}, {});
  assert.equal(f.orchestration.isMailboxPaused(), false);
  assert.equal(f.orchestration.status(f.context).notifiedThroughPercent, 0);
  assert.equal(f.sent.length, 0);
  assert.equal(f.scheduled.length, 1);
  assert.deepEqual(f.delays, [0]);

  f.scheduled[0]?.();
  assert.deepEqual(f.sent, [[
    {
      customType: "mecha-post-compaction-handoff",
      content: "Post-compaction handoff note:\n\nhandoff",
      display: true,
      details: {
        tokens_before: undefined,
        estimated_tokens_after: undefined,
        first_kept_entry_id: undefined,
        summary_chars: undefined,
      },
    },
    { deliverAs: "followUp", triggerTurn: true },
  ]]);
});

test("session compaction resets watermark and resumes; shutdown clears only self and pause", async () => {
  const f = fixture();
  f.orchestration.requestSelfCompaction({}, f.context);
  f.orchestration.onSessionCompact(f.context);
  assert.equal(f.orchestration.status(f.context).notifiedThroughPercent, 0);
  f.orchestration.requestSelfCompaction({}, f.context);
  Object.assign(f.context, { getContextUsage: () => ({ percent: 90, tokens: 90_000, contextWindow: 100_000 }) });
  f.orchestration.enforceHardCompaction(f.context);
  assert.equal(f.orchestration.hasPendingHardCompaction(), true);
  await f.orchestration.onSessionBeforeCompact({ preparation: {} }, f.context);
  assert.equal(f.orchestration.isMailboxPaused(), true);
  f.orchestration.onSessionShutdown();
  assert.equal(f.orchestration.hasPendingSelfCompaction(), false);
  assert.equal(f.orchestration.hasPendingHardCompaction(), true);
  assert.equal(f.orchestration.isMailboxPaused(), false);
  assert.equal(f.orchestration.status(f.context).policy, initialPolicy);
});
