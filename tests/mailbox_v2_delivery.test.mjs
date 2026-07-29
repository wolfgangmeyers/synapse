import assert from "node:assert/strict";
import { EventEmitter } from "node:events";
import { readFileSync, writeFileSync } from "node:fs";
import { mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import WebSocket from "ws";

import {
  appendMemoryCaptureDiagnostic,
  compactionNotificationMessage,
  compactionPolicyFromEnvironment,
  createMailboxClient,
  createMechaMessagingExtension,
  createPiMailboxDeliveryCoordinator,
  createSettledNotificationScheduler,
  DEFAULT_COMPACTION_POLICY,
  effectiveCompactionPolicy,

  mailboxSessionDeliveryState,
  nextCompactionThreshold,
  postCompactionNotificationMessage,
  selectCompactionTier,
} from "../pi-extensions/mecha-messaging.js";
import { QueueLimitError, openMessageRepository } from "../harness/db.js";
import { createAgentRouter } from "../harness/router.js";

const nextTurn = () => new Promise((resolve) => setImmediate(resolve));
async function waitFor(assertion, attempts = 20) {
  for (let attempt = 0; attempt < attempts; attempt += 1) {
    if (assertion()) return;
    await nextTurn();
  }
  assert.ok(assertion());
}
function envelope(id, overrides = {}) {
  return {
    id,
    from: "sender",
    to: "worker",
    body: "durable body",
    created_at: "2026-07-13T00:00:00.000Z",
    ...overrides,
  };
}

function sessionEntry(messageId) {
  return {
    type: "custom_message",
    customType: "mecha-agent-message",
    content: "message",
    display: true,
    details: { message_id: messageId },
  };
}

async function durableSession(messageId) {
  const directory = await mkdtemp(join(tmpdir(), "mecha-mailbox-session-"));
  const sessionFile = join(directory, "session.jsonl");
  const entries = [];
  const manager = {
    getBranch: () => entries,
    isPersisted: () => true,
    getSessionFile: () => sessionFile,
    getSessionId: () => "session-1",
  };
  return {
    directory,
    sessionFile,
    entries,
    manager,
    async persist() {
      if (!entries.some((entry) => entry.details?.message_id === messageId)) {
        entries.push(sessionEntry(messageId));
      }
      await writeFile(sessionFile, `${entries.map((entry) => JSON.stringify(entry)).join("\n")}\n`);
    },
    async close() {
      await rm(directory, { recursive: true, force: true });
    },
  };
}

function nextRouterFrame(socket) {
  return new Promise((resolve, reject) => {
    const onMessage = (payload) => settle(resolve, JSON.parse(payload.toString("utf8")));
    const onError = (error) => settle(reject, error);
    const settle = (callback, value) => {
      socket.off("message", onMessage);
      socket.off("error", onError);
      callback(value);
    };
    socket.once("message", onMessage);
    socket.once("error", onError);
  });
}

function receiptClient() {
  const receipts = [];
  return {
    receipts,
    sendReceipt(type, delivery, reason) {
      receipts.push({ type, messageId: delivery.messageId, attemptNumber: delivery.attemptNumber, reason });
    },
  };
}


function fakeTimers() {
  let now = 0;
  const timers = [];
  return {
    timers,
    setTimeout(callback, delay) {
      const timer = { callback, dueAt: now + delay, cleared: false, fired: false };
      timers.push(timer);
      return timer;
    },
    clearTimeout(timer) { timer.cleared = true; },
    advanceTo(target) {
      for (;;) {
        const timer = timers
          .filter((candidate) => !candidate.cleared && !candidate.fired && candidate.dueAt <= target)
          .sort((left, right) => left.dueAt - right.dueAt)[0];
        if (timer === undefined) break;
        now = timer.dueAt;
        timer.fired = true;
        timer.callback();
      }
      now = target;
    },
  };
}

test("settled scheduler sends the no-child schedule at 0, 1, 5, and 10 minutes", () => {
  const clock = fakeTimers();
  const scheduler = createSettledNotificationScheduler({
    setTimeoutImpl: clock.setTimeout,
    clearTimeoutImpl: clock.clearTimeout,
  });
  const sent = [];

  scheduler.onParentActivity();
  scheduler.onParentSettled(() => sent.push("sent"));
  clock.advanceTo(59999);
  assert.deepEqual(sent, ["sent"]);
  clock.advanceTo(60_000);
  clock.advanceTo(300_000);
  clock.advanceTo(600_000);
  clock.advanceTo(600_001);

  assert.deepEqual(sent, ["sent", "sent", "sent", "sent"]);
});

test("settled scheduler rejects missing timer functions", () => {
  assert.throws(
    () => createSettledNotificationScheduler({ setTimeoutImpl: null, clearTimeoutImpl: () => {} }),
    /settled notification timers are required/,
  );
  assert.throws(
    () => createSettledNotificationScheduler({ setTimeoutImpl: () => {}, clearTimeoutImpl: null }),
    /settled notification timers are required/,
  );
});

test("settled scheduler suppresses a settled parent while a native child is active", () => {
  const clock = fakeTimers();
  const scheduler = createSettledNotificationScheduler({
    setTimeoutImpl: clock.setTimeout,
    clearTimeoutImpl: clock.clearTimeout,
  });
  const sent = [];

  scheduler.onChildStarted("child-session");
  scheduler.onParentActivity();
  scheduler.onParentSettled(() => sent.push("sent"));
  assert.equal(scheduler.activeChildCount(), 1);
  assert.deepEqual(sent, []);

  scheduler.onChildDisposed("child-session");
  scheduler.onParentSettled(() => sent.push("retroactive"));
  assert.deepEqual(sent, []);

  scheduler.onParentActivity();
  scheduler.onParentSettled(() => sent.push("next-settle"));
  assert.deepEqual(sent, ["next-settle"]);
});

test("settled scheduler disposal cancels pending follow-ups", () => {
  const clock = fakeTimers();
  const scheduler = createSettledNotificationScheduler({
    setTimeoutImpl: clock.setTimeout,
    clearTimeoutImpl: clock.clearTimeout,
  });
  const sent = [];

  scheduler.onParentActivity();
  scheduler.onParentSettled(() => sent.push("sent"));
  const staleTimers = [...clock.timers];
  scheduler.dispose();
  for (const timer of staleTimers) timer.callback();
  assert.deepEqual(sent, ["sent"]);
  assert.equal(scheduler.activeChildCount(), 0);
});

test("child starts and stale timers suppress pending settled follow-ups", () => {
  const clock = fakeTimers();
  const scheduler = createSettledNotificationScheduler({
    setTimeoutImpl: clock.setTimeout,
    clearTimeoutImpl: clock.clearTimeout,
  });
  const sent = [];

  scheduler.onParentActivity();
  scheduler.onParentSettled(() => sent.push("first"));
  const staleTimers = [...clock.timers];
  scheduler.onChildStarted("child-session");
  for (const timer of staleTimers) timer.callback();
  clock.advanceTo(600_000);
  assert.deepEqual(sent, ["first"]);

  scheduler.onChildDisposed("child-session");
  scheduler.onParentSettled(() => sent.push("retroactive"));
  assert.deepEqual(sent, ["first"]);
  scheduler.onParentActivity();
  scheduler.onParentSettled(() => sent.push("second"));
  assert.deepEqual(sent, ["first", "second"]);
});

test("parent activity before each follow-up cancels that idle generation", () => {
  for (const cutoff of [59_999, 299_999, 599_999]) {
    const clock = fakeTimers();
    const scheduler = createSettledNotificationScheduler({
      setTimeoutImpl: clock.setTimeout,
      clearTimeoutImpl: clock.clearTimeout,
    });
    const sent = [];
    scheduler.onParentActivity();
    scheduler.onParentSettled(() => sent.push("sent"));
    clock.advanceTo(cutoff);
    const staleTimers = [...clock.timers];
    scheduler.onParentActivity();
    for (const timer of staleTimers) timer.callback();
    clock.advanceTo(600_000);
    assert.equal(sent.length, cutoff < 60_000 ? 1 : cutoff < 300_000 ? 2 : 3);
  }
});

test("subject-bearing PR watch wakeups preserve their exact subject through durable Pi delivery", async () => {
  const message = envelope("message-1", { to: "pr-watch", subject: "PR watch wakeup" });

test("post-compaction handoff message includes bounded note and compact status without summary text", () => {
  const message = postCompactionNotificationMessage("Resume the validation checklist.", {
    tokensBefore: 123_456,
    estimatedTokensAfter: 12_345,
    firstKeptEntryId: "entry-1",
    summary: "secret summary text",
  });
  assert.match(message, /Resume the validation checklist/);
  assert.match(message, /tokens before: 123,456/);
  assert.match(message, /estimated tokens after: 12,345/);
  assert.match(message, /first kept entry: entry-1/);
  assert.match(message, /summary chars: 19/);
  assert.doesNotMatch(message, /secret summary text/);
});

  const session = await durableSession(message.id);
  const sent = [];
  const client = receiptClient();
  const statuses = [];
  const context = {
    sessionManager: session.manager,
    ui: { setStatus: (...args) => statuses.push(args) },
  };
  const checkpointTimers = [];
  let deferredCheckpoint;
  const coordinator = createPiMailboxDeliveryCoordinator({
    pi: { sendMessage: (customMessage, options) => sent.push({ customMessage, options }) },
    sessionContext: context,
    client,
    defer: (callback) => { deferredCheckpoint = callback; },
    setTimeoutImpl: (callback, delay) => {
      const timer = { callback, delay, cleared: false };
      checkpointTimers.push(timer);
      return timer;
    },
    clearTimeoutImpl: (timer) => { timer.cleared = true; },
  });

  coordinator.onEnvelope(message, { messageId: message.id, attemptNumber: 1 });

  assert.equal(sent.length, 1);
  assert.equal(
    sent[0].customMessage.content,
    "Mailbox message from sender\nSubject: PR watch wakeup\n\ndurable body",
  );
  assert.deepEqual(sent[0].customMessage.details, {
    message_id: "message-1",
    from: "sender",
    to: "pr-watch",
    subject: "PR watch wakeup",
  });
  assert.deepEqual(sent[0].options, { deliverAs: "steer", triggerTurn: true });
  assert.deepEqual(client.receipts, [
    { type: "injection_requested", messageId: message.id, attemptNumber: 1, reason: undefined },
  ]);

  coordinator.onMessageEnd({
    message: { role: "custom", customType: "mecha-agent-message", details: { message_id: message.id } },
  }, context);
  // Pi appends the custom entry after extension message_end handlers, before the deferred check.
  session.entries.push(sessionEntry(message.id));
  assert.equal(typeof deferredCheckpoint, "function");
  assert.equal(client.receipts.length, 1);
  deferredCheckpoint();
  assert.equal(client.receipts.length, 1);
  assert.deepEqual(checkpointTimers.map(({ delay, cleared }) => ({ delay, cleared })), [
    { delay: 250, cleared: false },
  ]);

  coordinator.onEnvelope(message, { messageId: message.id, attemptNumber: 2 });
  assert.equal(sent.length, 1);
  assert.deepEqual(client.receipts, [
    { type: "injection_requested", messageId: message.id, attemptNumber: 1, reason: undefined },
    { type: "injection_requested", messageId: message.id, attemptNumber: 2, reason: undefined },
  ]);

  await session.persist();
  checkpointTimers[0].callback();

  assert.equal(mailboxSessionDeliveryState(session.manager, message.id), "durable");
  assert.deepEqual(client.receipts, [
    { type: "injection_requested", messageId: message.id, attemptNumber: 1, reason: undefined },
    { type: "injection_requested", messageId: message.id, attemptNumber: 2, reason: undefined },
    { type: "ack", messageId: message.id, attemptNumber: 2, reason: undefined },
  ]);
  assert.deepEqual(statuses.slice(-1), [["mecha-mailbox", "mailbox: connected"]]);
  coordinator.close();
  await session.close();
});

test("default compaction policy uses context-size fallback and model-specific overrides", () => {
  assert.deepEqual(DEFAULT_COMPACTION_POLICY.tiers[0].levels.map((level) => level.percent), [40, 50, 60, 70, 80, 90]);
  assert.equal(selectCompactionTier(DEFAULT_COMPACTION_POLICY, 272_000).maxContextTokens, 300_000);
  assert.equal(nextCompactionThreshold(DEFAULT_COMPACTION_POLICY, { percent: 39, contextWindow: 272_000 }), undefined);
  assert.equal(nextCompactionThreshold(DEFAULT_COMPACTION_POLICY, { percent: 40, contextWindow: 272_000 }).guidance, "consider compacting at a reasonable stopping point to keep future calls cheaper");
  assert.equal(nextCompactionThreshold(DEFAULT_COMPACTION_POLICY, { percent: 55, contextWindow: 272_000 }, 40).guidance, "compact soon, ideally at the next clean boundary");
  assert.equal(nextCompactionThreshold(DEFAULT_COMPACTION_POLICY, { percent: 65, contextWindow: 272_000 }, 50).guidance, "compact when practical; each call is getting meaningfully more expensive");
  assert.equal(nextCompactionThreshold(DEFAULT_COMPACTION_POLICY, { percent: 75, contextWindow: 272_000 }, 60).guidance, "strongly consider compacting before more expensive work");
  assert.equal(nextCompactionThreshold(DEFAULT_COMPACTION_POLICY, { percent: 85, contextWindow: 272_000 }, 70).guidance, "compact as soon as practical; the session is now very expensive");
  assert.equal(nextCompactionThreshold(DEFAULT_COMPACTION_POLICY, { percent: 91, contextWindow: 272_000 }, 80).mandatory, true);

  const millionTokenTier = selectCompactionTier(DEFAULT_COMPACTION_POLICY, 1_000_000);
  assert.equal(millionTokenTier.maxContextTokens, 1_000_000);
  assert.equal(millionTokenTier.levels.find((level) => level.mandatory).percent, 45);
  assert.equal(nextCompactionThreshold(DEFAULT_COMPACTION_POLICY, { percent: 49, contextWindow: 1_000_000 }, 40).mandatory, true);
  const solTier = selectCompactionTier(DEFAULT_COMPACTION_POLICY, 272_000, { provider: "openai-codex", id: "gpt-5.6-sol", name: "Sol" });
  assert.equal(solTier.matchedModelKey, "openai-codex/gpt-5.6-sol");
  assert.deepEqual(solTier.levels.map((level) => level.percent), [15, 25, 35, 45]);
  assert.equal(nextCompactionThreshold(DEFAULT_COMPACTION_POLICY, { percent: 36, contextWindow: 272_000, model: { provider: "openai-codex", id: "gpt-5.6-sol" } }, 25).percent, 35);

  const configured = compactionPolicyFromEnvironment({ MECHA_PI_COMPACTION_POLICY: JSON.stringify({
    levels: [40, 90],
    models: { "openai-codex/custom-sol": { levels: [10, { percent: 30, mandatory: true }] } },
  }) });
  assert.deepEqual(configured.tiers[0].levels.map((level) => level.percent), [40, 90]);
  assert.equal(effectiveCompactionPolicy(configured, { provider: "openai-codex", id: "custom-sol" }).matchedModelKey, "openai-codex/custom-sol");
  assert.equal(effectiveCompactionPolicy(configured, undefined, { MECHA_PI_MODEL_KEY: "openai-codex/custom-sol" }).matchedModelKey, "openai-codex/custom-sol");
  assert.equal(nextCompactionThreshold(configured, { percent: 31, contextWindow: 272_000, model: { provider: "openai-codex", id: "custom-sol" } }).mandatory, true);
  assert.deepEqual(compactionPolicyFromEnvironment({ MECHA_PI_COMPACTION_POLICY: "45, 75" }).tiers[0].levels.map((level) => level.percent), [45, 75]);
});

test("claude opus 5 resolves the low-threshold ladder with small-context guidance wording", () => {
  const smallGuidance = DEFAULT_COMPACTION_POLICY.tiers[0].levels.map((level) => level.guidance);
  for (const modelKeyEnv of ["anthropic/claude-opus-5", "claude-opus-5"]) {
    const effective = effectiveCompactionPolicy(DEFAULT_COMPACTION_POLICY, undefined, { MECHA_PI_MODEL_KEY: modelKeyEnv });
    assert.equal(effective.matchedModelKey, modelKeyEnv);
    const levels = effective.policy.tiers[0].levels;
    assert.deepEqual(levels.map((level) => level.percent), [15, 20, 25, 30, 35, 40]);
    assert.deepEqual(levels.map((level) => level.guidance), smallGuidance);
    assert.deepEqual(levels.filter((level) => level.mandatory).map((level) => level.percent), [40]);
  }

  // Model object keys resolve the same ladder, even at a 1M context window
  // (which would otherwise select the large-context tier).
  const opusTier = selectCompactionTier(DEFAULT_COMPACTION_POLICY, 1_000_000, { provider: "anthropic", id: "claude-opus-5", name: "Claude Opus 5" });
  assert.equal(opusTier.matchedModelKey, "anthropic/claude-opus-5");
  assert.deepEqual(opusTier.levels.map((level) => level.percent), [15, 20, 25, 30, 35, 40]);
  assert.equal(nextCompactionThreshold(DEFAULT_COMPACTION_POLICY, { percent: 14, contextWindow: 1_000_000, model: { provider: "anthropic", id: "claude-opus-5" } }), undefined);
  assert.equal(nextCompactionThreshold(DEFAULT_COMPACTION_POLICY, { percent: 15, contextWindow: 1_000_000, model: { provider: "anthropic", id: "claude-opus-5" } }).guidance, "consider compacting at a reasonable stopping point to keep future calls cheaper");
  assert.equal(nextCompactionThreshold(DEFAULT_COMPACTION_POLICY, { percent: 41, contextWindow: 1_000_000, model: { provider: "anthropic", id: "claude-opus-5" } }, 35).mandatory, true);
});

test("configured compaction policy can add early gpt-5.5 threshold nudges", () => {
  const configured = compactionPolicyFromEnvironment({ MECHA_PI_COMPACTION_POLICY: JSON.stringify({
    levels: [99],
    models: {
      "openai-codex/gpt-5.5": {
        levels: [
          { percent: 20, guidance: "Context is around 20%. No compaction needed yet." },
          { percent: 30, guidance: "Context is around 30%. Keep the main thread lean." },
          { percent: 40, guidance: "Context is around 40%. Look for a clean boundary to compact soon." },
        ],
      },
    },
  }) });
  const effective = effectiveCompactionPolicy(configured, { provider: "openai-codex", id: "gpt-5.5" });
  assert.deepEqual(effective.policy.tiers[0].levels.map((level) => level.percent), [20, 30, 40]);
  assert.equal(nextCompactionThreshold(configured, { percent: 20, contextWindow: 272_000, model: { provider: "openai-codex", id: "gpt-5.5" } }).percent, 20);
  assert.equal(nextCompactionThreshold(configured, { percent: 30, contextWindow: 272_000, model: { provider: "openai-codex", id: "gpt-5.5" } }, 20).percent, 30);
});

test("compaction threshold message appends delegation suffix without hard-coded compact instructions", () => {
  const early = compactionNotificationMessage({
    level: { percent: 20, guidance: "Context is around 20%. No compaction needed yet." },
    usage: { tokens: 54_400, contextWindow: 272_000, percent: 20 },
  });
  assert.match(early, /Use workers or subagents .*avoid context bloat/);
  assert.doesNotMatch(early, /mecha_context action=compact/);

  const mandatory = compactionNotificationMessage({
    level: { percent: 90, guidance: "Context is around 90%. Stop immediately and compact before doing more work.", mandatory: true },
    usage: { tokens: 900_000, contextWindow: 1_000_000, percent: 90 },
  });
  assert.match(mandatory, /Stop immediately and compact before doing more work/);
  assert.match(mandatory, /Use workers or subagents .*avoid context bloat/);
  assert.doesNotMatch(mandatory, /mecha_context action=compact/);
});

function settledNotificationFixture(t, {
  recipient,
  sendReply = async () => ({ id: "reply" }),
  createSettledScheduler,
  sendMessage,
  setTimeoutImpl,
} = {}) {
  const previousRecipient = process.env.MECHA_PI_NOTIFY_ON_SETTLED;
  if (recipient === undefined) delete process.env.MECHA_PI_NOTIFY_ON_SETTLED;
  else process.env.MECHA_PI_NOTIFY_ON_SETTLED = recipient;
  t.after(() => {
    if (previousRecipient === undefined) delete process.env.MECHA_PI_NOTIFY_ON_SETTLED;
    else process.env.MECHA_PI_NOTIFY_ON_SETTLED = previousRecipient;
  });

  const handlers = new Map();
  const childEvents = new Map();
  const tools = new Map();
  const notifications = [];
  const sent = [];
  const context = {
    sessionManager: { getSessionId: () => "parent-session" },
    ui: { notify: (...args) => notifications.push(args), setStatus() {} },
  };
  const client = {
    connect: () => Promise.resolve(),
    close() {},
    sendReply,
    sendReceipt() {},
  };
  const pi = {
    events: { on(type, handler) { childEvents.set(type, handler); } },
    on(type, handler) { handlers.set(type, handler); },
    registerCommand() {},
    registerTool(tool) { tools.set(tool.name, tool); },
    sendMessage: sendMessage ?? ((...args) => sent.push(args)),
    exec: async () => ({ code: 0, stderr: "" }),
  };
  createMechaMessagingExtension(pi, {
    resolveCurrentAgentId: () => "worker",
    createClient: () => client,
    ...(createSettledScheduler === undefined ? {} : { createSettledScheduler }),
    setTimeoutImpl,
  });
  handlers.get("session_start")({}, context);
  return {
    childEvents,
    client,
    context,
    handlers,
    notifications,
    sent,
    tools,
  };
}

test("settled notification sends one exact reply when top-level work settles", async (t) => {
  const attempts = [];
  const fixture = settledNotificationFixture(t, {
    recipient: "supervisor",
    sendReply: async (message) => { attempts.push(message); return { id: "reply" }; },
  });

  assert.deepEqual(attempts, []);
  assert.equal(fixture.handlers.has("agent_end"), false);
  fixture.handlers.get("agent_start")({}, fixture.context);
  fixture.handlers.get("agent_start")({}, fixture.context);
  fixture.handlers.get("agent_settled")({}, fixture.context);
  assert.deepEqual(attempts, [{
    to: "supervisor",
    subject: "Pi worker settled",
    body: "Worker worker has settled and is idle.",
  }]);
  fixture.handlers.get("agent_settled")({}, fixture.context);
  assert.equal(attempts.length, 1);
});

test("settled notification stays disabled without a valid opt-in", async (t) => {
  const attempts = [];
  const fixture = settledNotificationFixture(t, {
    sendReply: async (message) => { attempts.push(message); return { id: "reply" }; },
  });

  fixture.handlers.get("agent_start")({}, fixture.context);
  fixture.handlers.get("agent_settled")({}, fixture.context);
  assert.equal(attempts.length, 0);
});

test("settled notification excludes child sessions", async (t) => {
  const attempts = [];
  const fixture = settledNotificationFixture(t, {
    recipient: "supervisor",
    sendReply: async (message) => { attempts.push(message); return { id: "reply" }; },
  });

  fixture.childEvents.get("subagents:child:session-created")({ sessionId: "child-session" });
  const childContext = {
    ...fixture.context,
    sessionManager: { getSessionId: () => "child-session" },
  };
  fixture.handlers.get("session_start")({}, childContext);
  fixture.handlers.get("agent_start")({}, childContext);
  fixture.handlers.get("agent_settled")({}, childContext);
  assert.equal(attempts.length, 0);
  fixture.childEvents.get("subagents:child:disposed")({ sessionId: "child-session" });
});

test("native child lifecycle suppresses parent settled notices until a new parent settle", async (t) => {
  const attempts = [];
  const fixture = settledNotificationFixture(t, {
    recipient: "supervisor",
    sendReply: async (message) => { attempts.push(message); return { id: "reply" }; },
  });

  fixture.handlers.get("agent_start")({}, fixture.context);
  fixture.childEvents.get("subagents:started")({ id: "child-run" });
  fixture.handlers.get("agent_settled")({}, fixture.context);
  assert.equal(attempts.length, 0);

  fixture.childEvents.get("subagents:completed")({ id: "child-run" });
  fixture.handlers.get("agent_settled")({}, fixture.context);
  assert.equal(attempts.length, 0);

  fixture.handlers.get("agent_start")({}, fixture.context);
  fixture.handlers.get("agent_settled")({}, fixture.context);
  assert.equal(attempts.length, 1);
});

test("native child failure releases parent suppression for the next parent settle", async (t) => {
  const attempts = [];
  const fixture = settledNotificationFixture(t, {
    recipient: "supervisor",
    sendReply: async (message) => { attempts.push(message); return { id: "reply" }; },
  });

  fixture.childEvents.get("subagents:started")({ id: "child-run" });
  fixture.handlers.get("agent_start")({}, fixture.context);
  fixture.handlers.get("agent_settled")({}, fixture.context);
  fixture.childEvents.get("subagents:failed")({ id: "child-run" });
  fixture.handlers.get("agent_start")({}, fixture.context);
  fixture.handlers.get("agent_settled")({}, fixture.context);

  assert.equal(attempts.length, 1);
});

test("parent session shutdown disposes armed settled follow-ups", async (t) => {
  const calls = [];
  const scheduler = {
    onParentActivity() { calls.push("activity"); },
    onParentSettled(send) { calls.push("settled"); send(); },
    onChildStarted() {},
    onChildDisposed() {},
    dispose() { calls.push("dispose"); },
  };
  const fixture = settledNotificationFixture(t, {
    recipient: "supervisor",
    createSettledScheduler: () => scheduler,
  });

  fixture.handlers.get("agent_start")({}, fixture.context);
  fixture.handlers.get("agent_settled")({}, fixture.context);
  fixture.handlers.get("session_shutdown")({}, fixture.context);

  assert.deepEqual(calls, ["activity", "settled", "dispose"]);
});

test("child sessions do not schedule recall work", async (t) => {
  const fixture = settledNotificationFixture(t, { recipient: "supervisor" });
  const previousFetch = globalThis.fetch;
  let fetches = 0;
  globalThis.fetch = async () => { fetches += 1; throw new Error("child recall must not fetch policy"); };
  t.after(() => { globalThis.fetch = previousFetch; });

  await fixture.tools.get("mecha_recall").execute("enable-recall", { action: "enable", stores: ["default"] });
  fixture.childEvents.get("subagents:child:session-created")({ sessionId: "child-session" });
  const childContext = { ...fixture.context, sessionManager: { getSessionId: () => "child-session" } };
  fixture.handlers.get("message_start")({}, childContext);
  fixture.handlers.get("agent_settled")({}, childContext);
  await nextTurn();

  assert.equal(fetches, 0);
  fixture.childEvents.get("subagents:child:disposed")({ sessionId: "child-session" });
});

test("an invalid settled recipient leaves notifications disabled", async (t) => {
  const attempts = [];
  const fixture = settledNotificationFixture(t, {
    recipient: "not a mailbox agent",
    sendReply: async (message) => { attempts.push(message); return { id: "reply" }; },
  });

  fixture.handlers.get("agent_start")({}, fixture.context);
  fixture.handlers.get("agent_settled")({}, fixture.context);
  assert.equal(attempts.length, 0);
});

test("a rejected settled notification reports once and does not block a later run", async (t) => {
  const attempts = [];
  const fixture = settledNotificationFixture(t, {
    recipient: "supervisor",
    sendReply(message) {
      attempts.push(message);
      return attempts.length === 1 ? Promise.reject(new Error("router rejected")) : Promise.resolve({ id: "reply" });
    },
  });

  fixture.handlers.get("agent_start")({}, fixture.context);
  fixture.handlers.get("agent_settled")({}, fixture.context);
  await nextTurn();
  assert.deepEqual(fixture.notifications, [["Pi settled notification failed: router rejected", "error"]]);
  assert.equal(attempts.length, 1);

  fixture.handlers.get("agent_start")({}, fixture.context);
  fixture.handlers.get("agent_settled")({}, fixture.context);
  await nextTurn();
  assert.equal(attempts.length, 2);
  assert.deepEqual(fixture.notifications, [["Pi settled notification failed: router rejected", "error"]]);
});

test("overlapping settled notification sends make independent attempts", async (t) => {
  const attempts = [];
  const deferred = [];
  const fixture = settledNotificationFixture(t, {
    recipient: "supervisor",
    sendReply(message) {
      attempts.push(message);
      return new Promise((resolve) => deferred.push(resolve));
    },
  });

  fixture.handlers.get("agent_start")({}, fixture.context);
  fixture.handlers.get("agent_settled")({}, fixture.context);
  fixture.handlers.get("agent_start")({}, fixture.context);
  fixture.handlers.get("agent_settled")({}, fixture.context);
  assert.equal(attempts.length, 2);

  deferred[0]({ id: "first" });
  await nextTurn();
  assert.equal(attempts.length, 2);
  deferred[1]({ id: "second" });
  await nextTurn();
  assert.equal(attempts.length, 2);
  assert.deepEqual(fixture.notifications, []);
});

test("context monitor injects direct threshold notifications and resets after compaction", async () => {
  const handlers = new Map();
  const sent = [];
  const tools = new Map();
  const context = {
    sessionManager: { getSessionId: () => "parent-session" },
    ui: { notify() {}, setStatus() {} },
    getContextUsage: () => ({ tokens: 510_000, contextWindow: 1_000_000, percent: 51 }),
  };
  const pi = {
    events: { on() {} },
    on(type, handler) {
      const list = handlers.get(type) ?? [];
      list.push(handler);
      handlers.set(type, list);
    },
    registerCommand() {},
    registerTool(tool) { tools.set(tool.name, tool); },
    sendMessage: (...args) => sent.push(args),
    exec: async () => ({ code: 0, stderr: "" }),
  };
  createMechaMessagingExtension(pi, {
    resolveCurrentAgentId: () => "worker",
    createClient: () => ({ connect: () => Promise.resolve(), close() {}, sendReply: async () => ({}), sendReceipt() {} }),
  });
  for (const handler of handlers.get("session_start")) handler({}, context);
  for (const handler of handlers.get("agent_settled")) await handler({}, context);
  assert.equal(sent.length, 1);
  assert.equal(sent[0][0].customType, "mecha-context-threshold");
  assert.equal(sent[0][0].details.threshold_percent, 45);
  assert.equal(sent[0][0].details.tier_max_context_tokens, 1_000_000);
  assert.deepEqual(sent[0][1], { deliverAs: "followUp", triggerTurn: true });

  for (const handler of handlers.get("agent_settled")) await handler({}, context);
  assert.equal(sent.length, 1);
  for (const handler of handlers.get("session_compact")) handler({}, context);
  for (const handler of handlers.get("agent_settled")) await handler({}, context);
  assert.equal(sent.length, 2);
});

test("mecha_context tool sets policy and defers self-compaction until agent_settled", async (t) => {
  const fixture = settledNotificationFixture(t);
  const contextTool = fixture.tools.get("mecha_context");
  assert.equal(typeof contextTool.execute, "function");
  assert.match(contextTool.description, /postCompactionNotification is optional/);
  assert.match(contextTool.description, /include it while you are in the middle of an active task, review, or decision loop/);
  assert.match(contextTool.parameters.properties.postCompactionNotification.description, /omit it only when clearly idle\/done and no details are needed/);

  const setResult = await contextTool.execute("set-policy", {
    action: "set_policy",
    policy: { tiers: [{ maxContextTokens: 300_000, levels: [{ percent: 45, guidance: "custom" }] }, { maxContextTokens: null, levels: [{ percent: 85, mandatory: true }] }] },
  }, undefined, undefined, { ...fixture.context, getContextUsage: () => ({ tokens: 45, contextWindow: 100, percent: 45 }) });
  assert.equal(setResult.details.policy.tiers[0].levels[0].percent, 45);
  assert.equal(setResult.details.selectedTier.maxContextTokens, 300_000);

  const compactCalls = [];
  const compactContext = {
    ...fixture.context,
    getContextUsage: () => ({ tokens: 45, contextWindow: 100, percent: 45 }),
    isIdle: () => true,
    compact: (options) => compactCalls.push(options),
  };
  const compactResult = await contextTool.execute("compact", {
    action: "compact",
    customInstructions: "preserve task state",
    postCompactionNotification: "Continue with the post-compact checklist.",
  }, undefined, undefined, compactContext);
  assert.equal(compactResult.content[0].text, "Self-compaction deferred until Pi reaches the idle settled boundary.");
  assert.equal(compactResult.details.compactState, "deferred_until_agent_settled");
  assert.equal(compactResult.details.postCompactionNotification, "Continue with the post-compact checklist.");
  assert.equal(compactCalls.length, 0);

  fixture.handlers.get("agent_settled")({}, compactContext);
  assert.equal(compactCalls.length, 1);
  assert.equal(compactCalls[0].customInstructions, "preserve task state");

  compactCalls[0].onComplete({ tokensBefore: 45, estimatedTokensAfter: 12, firstKeptEntryId: "entry-1", summary: "summary" });
  await new Promise((resolve) => setTimeout(resolve, 1));
  assert.equal(fixture.sent.length, 1);
  assert.equal(fixture.sent[0][0].customType, "mecha-post-compaction-handoff");
  assert.match(fixture.sent[0][0].content, /Continue with the post-compact checklist/);
  assert.equal(fixture.sent[0][0].details.tokens_before, 45);
  assert.deepEqual(fixture.sent[0][1], { deliverAs: "followUp", triggerTurn: true });
});

test("mecha_context does not compact at a non-idle settled boundary", async (t) => {
  const fixture = settledNotificationFixture(t);
  const compactCalls = [];
  const compactContext = {
    ...fixture.context,
    isIdle: () => false,
    compact: (options) => compactCalls.push(options),
  };

  const result = await fixture.tools.get("mecha_context").execute("compact", {
    action: "compact",
    customInstructions: "wait for safety",
  }, undefined, undefined, compactContext);

  assert.equal(result.details.compactState, "deferred_until_agent_settled");
  fixture.handlers.get("agent_settled")({}, compactContext);
  assert.equal(compactCalls.length, 0);
});

test("mecha_context uses configured default post-compaction handoff when command omits one", async (t) => {
  const fixture = settledNotificationFixture(t);
  const contextTool = fixture.tools.get("mecha_context");
  await contextTool.execute("set-policy", {
    action: "set_policy",
    policy: { postCompactionNotification: "Policy handoff note.", levels: [40, 90] },
  }, undefined, undefined, fixture.context);
  const compactCalls = [];
  const compactContext = {
    ...fixture.context,
    isIdle: () => true,
    compact: (options) => compactCalls.push(options),
  };
  await contextTool.execute("compact", { action: "compact" }, undefined, undefined, compactContext);
  assert.equal(compactCalls.length, 0);
  fixture.handlers.get("agent_settled")({}, compactContext);
  compactCalls[0].onComplete({ tokensBefore: 90, summary: "summary" });
  await new Promise((resolve) => setTimeout(resolve, 1));
  assert.equal(fixture.sent[0][0].customType, "mecha-post-compaction-handoff");
  assert.match(fixture.sent[0][0].content, /Policy handoff note/);
});

test("memory capture diagnostics are content-free and bounded", async () => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-memory-diagnostics-"));
  const path = join(directory, "memory-capture.jsonl");
  appendMemoryCaptureDiagnostic({ timestamp: "2026-07-27T00:00:00.000Z", trigger: "compaction", status: "applied", reason: "memory-apply", records: 1, source: "do not log" }, { path, maxBytes: 260, keepBytes: 130 });
  appendMemoryCaptureDiagnostic({ timestamp: "2026-07-27T00:00:01.000Z", trigger: "shutdown", status: "failed", reason: "capture apply failed: secret source body" }, { path, maxBytes: 260, keepBytes: 130 });
  assert.equal(JSON.parse(readFileSync(path, "utf8").trim().split("\n").at(-1)).reason, "capture-failed");
  for (let index = 0; index < 10; index += 1) appendMemoryCaptureDiagnostic({ timestamp: `2026-07-27T00:00:${String(index + 2).padStart(2, "0")}.000Z`, trigger: "compaction", status: "noop", reason: "extractor-noop" }, { path, maxBytes: 260, keepBytes: 130 });

  const text = readFileSync(path, "utf8");
  assert.ok(Buffer.byteLength(text) <= 260);
  assert.doesNotMatch(text, /do not log|source body/);
  const records = text.trim().split("\n").map((line) => JSON.parse(line));
  assert.ok(records.length > 0);
  assert.ok(records.every((record) => Object.keys(record).every((key) => ["timestamp", "trigger", "status", "reason", "records"].includes(key))));
  assert.deepEqual(Object.keys(records.at(-1)).sort(), ["reason", "status", "timestamp", "trigger"].sort());
  await rm(directory, { recursive: true, force: true });
});

test("post-compaction handoff is deferred so send failures do not fail compaction", async (t) => {
  const deferred = [];
  const fixture = settledNotificationFixture(t, {
    setTimeoutImpl: (callback, delay) => { deferred.push({ callback, delay }); return deferred.length; },
    sendMessage: () => { throw new Error("Cannot read properties of undefined (reading signal)"); },
  });
  const compactCalls = [];
  const compactContext = {
    ...fixture.context,
    isIdle: () => true,
    compact: (options) => compactCalls.push(options),
  };
  await fixture.tools.get("mecha_context").execute("compact", {
    action: "compact",
    postCompactionNotification: "Do not fail compact.",
  }, undefined, undefined, compactContext);
  assert.equal(compactCalls.length, 0);
  fixture.handlers.get("agent_settled")({}, compactContext);

  assert.doesNotThrow(() => compactCalls[0].onComplete({ tokensBefore: 100, summary: "summary" }));
  assert.equal(deferred.length, 1);
  assert.equal(deferred[0].delay, 0);
  assert.doesNotThrow(() => deferred[0].callback());
});

test("extension forwards Pi message_end into the durable mailbox checkpoint", async () => {
  const message = envelope("message-extension-wiring");
  const session = await durableSession(message.id);
  const handlers = new Map();
  const childEvents = new Map();
  const sent = [];
  const receipts = [];
  let clientOptions;
  let closeCount = 0;
  const client = {
    connect: () => Promise.resolve(),
    close() { closeCount += 1; },
    sendReply: async () => ({ id: "reply" }),
    sendReceipt(type, delivery) {
      receipts.push({ type, attemptNumber: delivery.attemptNumber });
    },
  };
  const pi = {
    events: { on(type, handler) { childEvents.set(type, handler); } },
    on(type, handler) { handlers.set(type, handler); },
    registerCommand() {},
    registerTool() {},
    sendMessage: (...args) => sent.push(args),
    exec: async () => ({ code: 0, stderr: "" }),
  };
  const context = {
    sessionManager: session.manager,
    ui: { notify() {}, setStatus() {} },
  };
  createMechaMessagingExtension(pi, {
    resolveCurrentAgentId: () => "worker",
    createClient: (options) => {
      clientOptions = options;
      return client;
    },
  });

  handlers.get("session_start")({}, context);
  clientOptions.onEnvelope(message, { messageId: message.id, attemptNumber: 1 });
  assert.equal(sent.length, 1);
  assert.equal(sent[0][0].content, "Mailbox message from sender\n\ndurable body");
  assert.deepEqual(sent[0][0].details, {
    message_id: message.id,
    from: "sender",
    to: "worker",
  });
  assert.deepEqual(sent[0][1], { deliverAs: "steer", triggerTurn: true });
  assert.deepEqual(receipts, [{ type: "injection_requested", attemptNumber: 1 }]);

  session.entries.push(sessionEntry(message.id));
  await session.persist();
  handlers.get("message_end")({
    message: { role: "custom", customType: "mecha-agent-message", details: { message_id: message.id } },
  }, context);
  await nextTurn();

  assert.deepEqual(receipts, [
    { type: "injection_requested", attemptNumber: 1 },
    { type: "ack", attemptNumber: 1 },
  ]);
  const childContext = {
    ...context,
    sessionManager: { ...session.manager, getSessionId: () => "child-session" },
  };
  childEvents.get("subagents:child:session-created")({ sessionId: "child-session" });
  handlers.get("session_shutdown")({}, childContext);
  assert.equal(closeCount, 0);
  childEvents.get("subagents:child:disposed")({ sessionId: "child-session" });
  handlers.get("session_shutdown")({}, context);
  assert.equal(closeCount, 1);
  await session.close();
});

test("unverified session history is never reinjected or acknowledged", async () => {
  const message = envelope("message-2");
  const session = await durableSession(message.id);
  session.entries.push(sessionEntry(message.id));
  const sent = [];
  const status = [];
  const client = receiptClient();
  const context = {
    sessionManager: session.manager,
    ui: { setStatus: (...args) => status.push(args) },
  };
  const coordinator = createPiMailboxDeliveryCoordinator({
    pi: { sendMessage: (...args) => sent.push(args) },
    sessionContext: context,
    client,
  });

  coordinator.onEnvelope(message, { messageId: message.id, attemptNumber: 1 });
  await nextTurn();

  assert.equal(mailboxSessionDeliveryState(session.manager, message.id), "history_unverified");
  assert.deepEqual(sent, []);
  assert.deepEqual(client.receipts, []);
  assert.deepEqual(status, [["mecha-mailbox", "mailbox: waiting for durable session checkpoint"]]);
  coordinator.close();
  await session.close();
});

test("unreadable persisted history is reported without reinjection or ACK", async () => {
  const message = envelope("message-corrupt-history");
  const session = await durableSession(message.id);
  session.entries.push(sessionEntry(message.id));
  await writeFile(session.sessionFile, "not-json\n");
  const sent = [];
  const notices = [];
  const client = receiptClient();
  const context = {
    sessionManager: session.manager,
    ui: {
      setStatus() {},
      notify: (...args) => notices.push(args),
    },
  };
  const coordinator = createPiMailboxDeliveryCoordinator({
    pi: { sendMessage: (...args) => sent.push(args) },
    sessionContext: context,
    client,
  });

  coordinator.onEnvelope(message, { messageId: message.id, attemptNumber: 1 });
  coordinator.onEnvelope(message, { messageId: message.id, attemptNumber: 2 });

  assert.deepEqual(sent, []);
  assert.deepEqual(client.receipts, []);
  assert.equal(notices.length, 1);
  assert.match(notices[0][0], /session checkpoint unreadable/);
  assert.equal(notices[0][1], "error");
  coordinator.close();
  await session.close();
});

test("replayed durable delivery ACKs without a second Pi message", async () => {
  const message = envelope("message-3");
  const session = await durableSession(message.id);
  await session.persist();
  const sent = [];
  const client = receiptClient();
  const context = { sessionManager: session.manager, ui: { setStatus() {} } };
  const coordinator = createPiMailboxDeliveryCoordinator({
    pi: { sendMessage: (...args) => sent.push(args) },
    sessionContext: context,
    client,
  });

  coordinator.onEnvelope(message, { messageId: message.id, attemptNumber: 2 });

  assert.deepEqual(sent, []);
  assert.deepEqual(client.receipts, [
    { type: "injection_requested", messageId: message.id, attemptNumber: 2, reason: undefined },
    { type: "ack", messageId: message.id, attemptNumber: 2, reason: undefined },
  ]);
  coordinator.close();
  await session.close();
});

test("post-injection receipt failure retains the pending message for replay", async () => {
  const message = envelope("message-receipt-failure");
  const session = await durableSession(message.id);
  const sent = [];
  const receipts = [];
  let failReceipt = true;
  const client = {
    sendReceipt(type, delivery) {
      if (type === "injection_requested" && failReceipt) {
        failReceipt = false;
        throw new Error("socket closed");
      }
      receipts.push({ type, attemptNumber: delivery.attemptNumber });
    },
  };
  const context = { sessionManager: session.manager, ui: { setStatus() {} } };
  const coordinator = createPiMailboxDeliveryCoordinator({
    pi: { sendMessage: (...args) => sent.push(args) },
    sessionContext: context,
    client,
  });

  coordinator.onEnvelope(message, { messageId: message.id, attemptNumber: 1 });
  coordinator.onEnvelope(message, { messageId: message.id, attemptNumber: 2 });

  assert.equal(sent.length, 1);
  assert.deepEqual(receipts, [{ type: "injection_requested", attemptNumber: 2 }]);
  coordinator.close();
  await session.close();
});

test("client does not NACK a Pi injection when its receipt send fails", async () => {
  const socket = new EventTarget();
  socket.readyState = 0;
  socket.sent = [];
  socket.send = (frame) => {
    const parsed = JSON.parse(frame);
    if (parsed.type === "injection_requested") throw new Error("socket write failed");
    socket.sent.push(parsed);
  };
  socket.close = () => { socket.readyState = 3; };
  const context = {
    sessionManager: {
      getBranch: () => [],
      isPersisted: () => false,
      getSessionFile: () => undefined,
      getSessionId: () => "session-1",
    },
    ui: { setStatus() {} },
  };
  const sent = [];
  let coordinator;
  const client = createMailboxClient({
    agentId: "worker",
    onEnvelope: (message, delivery) => coordinator.onEnvelope(message, delivery),
    webSocketFactory: () => socket,
    generateId: (() => { let index = 0; return () => `id-${++index}`; })(),
  });
  coordinator = createPiMailboxDeliveryCoordinator({
    pi: { sendMessage: (...args) => sent.push(args) },
    sessionContext: context,
    client,
  });

  const connected = client.connect();
  socket.readyState = 1;
  socket.dispatchEvent(new Event("open"));
  socket.dispatchEvent(new MessageEvent("message", {
    data: JSON.stringify({
      type: "registered", agent_id: "worker", instance_id: client.instanceId, lease_id: "lease-1",
    }),
  }));
  await connected;
  socket.dispatchEvent(new MessageEvent("message", {
    data: JSON.stringify({ type: "message", message: envelope("message-client-receipt"), attempt_number: 1 }),
  }));
  await nextTurn();

  assert.equal(sent.length, 1);
  assert.deepEqual(socket.sent.map(({ type }) => type), ["register"]);
  coordinator.close();
  client.close();
});

test("Pi injection failure NACKs without acknowledging the delivery", async () => {
  const message = envelope("message-injection-failure");
  const session = await durableSession(message.id);
  const client = receiptClient();
  const context = { sessionManager: session.manager, ui: { setStatus() {} } };
  const coordinator = createPiMailboxDeliveryCoordinator({
    pi: { sendMessage: () => { throw new Error("Pi rejected message"); } },
    sessionContext: context,
    client,
  });

  coordinator.onEnvelope(message, { messageId: message.id, attemptNumber: 1 });

  assert.deepEqual(client.receipts, [
    { type: "nack", messageId: message.id, attemptNumber: 1, reason: "injection_failed" },
  ]);
  coordinator.close();
  await session.close();
});

test("failed durable ACK is retried from persisted session history without reinjection", async () => {
  const message = envelope("message-ack-failure");
  const session = await durableSession(message.id);
  await session.persist();
  const sent = [];
  const receipts = [];
  let failAck = true;
  const client = {
    sendReceipt(type, delivery) {
      if (type === "ack" && failAck) {
        failAck = false;
        throw new Error("socket closed");
      }
      receipts.push({ type, attemptNumber: delivery.attemptNumber });
    },
  };
  const context = { sessionManager: session.manager, ui: { setStatus() {} } };
  const coordinator = createPiMailboxDeliveryCoordinator({
    pi: { sendMessage: (...args) => sent.push(args) },
    sessionContext: context,
    client,
  });

  coordinator.onEnvelope(message, { messageId: message.id, attemptNumber: 1 });
  coordinator.onEnvelope(message, { messageId: message.id, attemptNumber: 2 });

  assert.deepEqual(sent, []);
  assert.deepEqual(receipts, [
    { type: "injection_requested", attemptNumber: 1 },
    { type: "injection_requested", attemptNumber: 2 },
    { type: "ack", attemptNumber: 2 },
  ]);
  coordinator.close();
  await session.close();
});

test("replayed in-flight delivery does not create a second Pi injection", async () => {
  const message = envelope("message-4");
  const session = await durableSession(message.id);
  const sent = [];
  const client = receiptClient();
  const context = { sessionManager: session.manager, ui: { setStatus() {} } };
  const coordinator = createPiMailboxDeliveryCoordinator({
    pi: { sendMessage: (...args) => sent.push(args) },
    sessionContext: context,
    client,
  });

  coordinator.onEnvelope(message, { messageId: message.id, attemptNumber: 1 });
  coordinator.onEnvelope(message, { messageId: message.id, attemptNumber: 2 });
  await session.persist();
  coordinator.onMessageEnd({
    message: { role: "custom", customType: "mecha-agent-message", details: { message_id: message.id } },
  }, context);
  await nextTurn();

  assert.equal(sent.length, 1);
  assert.deepEqual(client.receipts, [
    { type: "injection_requested", messageId: message.id, attemptNumber: 1, reason: undefined },
    { type: "injection_requested", messageId: message.id, attemptNumber: 2, reason: undefined },
    { type: "ack", messageId: message.id, attemptNumber: 2, reason: undefined },
  ]);
  coordinator.close();
  await session.close();
});

test("mailbox client reconnects after close and stops retrying after shutdown", async () => {
  const sockets = [];
  const reconnectTimers = [];
  const kickstarts = [];
  class FakeSocket extends EventTarget {
    readyState = 0;
    sent = [];

    send(frame) {
      this.sent.push(JSON.parse(frame));
    }

    close() {
      this.readyState = 3;
      this.dispatchEvent(new Event("close"));
    }
  }
  const client = createMailboxClient({
    agentId: "worker",
    onEnvelope: () => {},
    kickstart: () => { kickstarts.push("kickstart"); },
    webSocketFactory: () => {
      const socket = new FakeSocket();
      sockets.push(socket);
      return socket;
    },
    generateId: (() => { let index = 0; return () => `id-${++index}`; })(),
    reconnectInitialDelayMs: 2,
    reconnectMaxDelayMs: 4,
    random: () => 1,
    setTimeoutImpl: (callback, delay) => {
      const timer = { callback, delay, cleared: false };
      reconnectTimers.push(timer);
      return timer;
    },
    clearTimeoutImpl: (timer) => { timer.cleared = true; },
  });

  const connected = client.connect();
  sockets[0].readyState = 1;
  sockets[0].dispatchEvent(new Event("open"));
  sockets[0].dispatchEvent(new MessageEvent("message", {
    data: JSON.stringify({
      type: "registered", agent_id: "worker", instance_id: client.instanceId, lease_id: "lease-1",
    }),
  }));
  await connected;

  sockets[0].close();
  assert.deepEqual(reconnectTimers.map(({ delay, cleared }) => ({ delay, cleared })), [
    { delay: 2, cleared: false },
  ]);
  assert.deepEqual(kickstarts, []);
  reconnectTimers[0].callback();
  assert.equal(sockets.length, 2);
  sockets[1].readyState = 1;
  sockets[1].dispatchEvent(new Event("open"));
  sockets[1].dispatchEvent(new MessageEvent("message", {
    data: JSON.stringify({
      type: "registered", agent_id: "worker", instance_id: client.instanceId, lease_id: "lease-2",
    }),
  }));
  sockets[1].close();
  assert.deepEqual(reconnectTimers.map(({ delay, cleared }) => ({ delay, cleared })), [
    { delay: 2, cleared: false },
    { delay: 2, cleared: false },
  ]);
  assert.deepEqual(kickstarts, []);
  client.close();
  assert.equal(reconnectTimers[1].cleared, true);
  reconnectTimers[1].callback();
  assert.equal(sockets.length, 2);
});

test("connection refusal requests launchd once per scheduled retry", () => {
  const sockets = [];
  const createSocket = () => {
    const socket = new EventTarget();
    socket.readyState = 0;
    socket.close = () => { socket.readyState = 3; };
    sockets.push(socket);
    return socket;
  };
  const kickstarts = [];
  const reconnectTimers = [];
  const client = createMailboxClient({
    agentId: "worker",
    onEnvelope: () => {},
    kickstart: () => { kickstarts.push("kickstart"); },
    webSocketFactory: createSocket,
    reconnectInitialDelayMs: 2,
    reconnectMaxDelayMs: 4,
    random: () => 1,
    setTimeoutImpl: (callback, delay) => {
      const timer = { callback, delay, cleared: false };
      reconnectTimers.push(timer);
      return timer;
    },
    clearTimeoutImpl: (timer) => { timer.cleared = true; },
  });

  void client.connect().catch(() => {});
  const refusal = new Event("error");
  Object.defineProperty(refusal, "error", {
    value: Object.assign(new Error("connection refused"), { code: "ECONNREFUSED" }),
  });
  sockets[0].dispatchEvent(refusal);

  assert.deepEqual(kickstarts, ["kickstart"]);
  assert.deepEqual(reconnectTimers.map(({ delay, cleared }) => ({ delay, cleared })), [
    { delay: 2, cleared: false },
  ]);
  reconnectTimers[0].callback();
  assert.equal(sockets.length, 2);
  const secondRefusal = new Event("error");
  Object.defineProperty(secondRefusal, "error", {
    value: Object.assign(new Error("connection refused"), { code: "ECONNREFUSED" }),
  });
  sockets[1].dispatchEvent(secondRefusal);
  assert.deepEqual(kickstarts, ["kickstart", "kickstart"]);
  assert.deepEqual(reconnectTimers.map(({ delay, cleared }) => ({ delay, cleared })), [
    { delay: 2, cleared: false },
    { delay: 4, cleared: false },
  ]);
  client.close();
  assert.equal(reconnectTimers[1].cleared, true);
});

test("same instance reconnect registration replaces the old live socket without duplicate_identity", async () => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-mailbox-reconnect-"));
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite") });
  const router = createAgentRouter({ repository, timeoutSweepIntervalMs: 60_000 });
  let firstSocket;
  let secondSocket;
  let frameId = 0;
  const send = (socket, frame) => socket.send(JSON.stringify({
    version: "v1",
    frame_id: `client-${++frameId}`,
    ...frame,
  }));
  try {
    const websocketUrl = await router.listen();
    firstSocket = new WebSocket(websocketUrl);
    await new Promise((resolve, reject) => {
      firstSocket.once("open", resolve);
      firstSocket.once("error", reject);
    });
    const firstRegistered = nextRouterFrame(firstSocket);
    send(firstSocket, { type: "register", agent_id: "worker", instance_id: "same-instance" });
    const firstRegistration = await firstRegistered;
    assert.equal(firstRegistration.type, "registered");

    const firstClosed = new Promise((resolve) => firstSocket.once("close", resolve));
    secondSocket = new WebSocket(websocketUrl);
    await new Promise((resolve, reject) => {
      secondSocket.once("open", resolve);
      secondSocket.once("error", reject);
    });
    const secondRegistered = nextRouterFrame(secondSocket);
    send(secondSocket, { type: "register", agent_id: "worker", instance_id: "same-instance" });
    const secondRegistration = await secondRegistered;

    assert.equal(secondRegistration.type, "registered");
    assert.notEqual(secondRegistration.lease_id, firstRegistration.lease_id);
    const activeLease = repository.leaseFor("worker");
    assert.equal(activeLease.agent_id, "worker");
    assert.equal(activeLease.instance_id, "same-instance");
    assert.equal(activeLease.lease_id, secondRegistration.lease_id);
    assert.equal(activeLease.expires_at, secondRegistration.expires_at);
    await firstClosed;
  } finally {
    firstSocket?.terminate();
    secondSocket?.terminate();
    await router.close();
    repository.close();
    await rm(directory, { recursive: true, force: true });
  }
});

test("router batches roughly ten mixed-sender idle notifications into one queued delivery", async () => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-mailbox-batch-"));
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite") });
  try {
    const router = createAgentRouter({ repository, timeoutSweepIntervalMs: 60_000 });
    for (let index = 1; index <= 10; index += 1) {
      const sender = `worker-${index}`;
      const result = router.enqueueMessage(envelope(`idle-${index}`, {
        from: sender,
        to: "supervisor",
        subject: index % 2 === 0 ? "Pi worker settled" : "Status update",
        body: `Worker ${sender} has settled and is idle.`,
      }));
      assert.equal(result.message.id, "idle-1");
      assert.equal(result.message.to, "supervisor");
      assert.equal(result.batched === true, index > 1);
    }

    const queued = repository.queuedFor("supervisor");
    assert.equal(queued.length, 1);
    assert.equal(queued[0].id, "idle-1");
    assert.match(queued[0].body, /^Mailbox message batch\n\n/);
    for (let index = 1; index <= 10; index += 1) {
      assert.match(queued[0].body, new RegExp(`From: worker-${index}`));
      assert.match(queued[0].body, new RegExp(`Worker worker-${index} has settled and is idle\\.`));
      assert.match(queued[0].body, new RegExp(`\\[Reply via mailbox to worker-${index}\\.\\]`));
    }
    assert.match(queued[0].body, /Subject: Status update/);
    assert.match(queued[0].body, /Subject: Pi worker settled/);
    assert.equal((queued[0].body.match(/\n\n---\n\n/g) ?? []).length, 9);
  } finally {
    repository.close();
    await rm(directory, { recursive: true, force: true });
  }
});

test("batched delivery carries per-item reply hints without one global sender footer", async () => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-mailbox-batch-delivery-"));
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite") });
  const router = createAgentRouter({ repository, timeoutSweepIntervalMs: 60_000 });
  let senderSocket;
  let receiverSocket;
  let frameId = 0;
  const send = (socket, frame) => socket.send(JSON.stringify({
    version: "v1",
    frame_id: `client-${++frameId}`,
    ...frame,
  }));
  try {
    const websocketUrl = await router.listen();
    senderSocket = new WebSocket(websocketUrl);
    await new Promise((resolve, reject) => {
      senderSocket.once("open", resolve);
      senderSocket.once("error", reject);
    });
    const senderRegistered = nextRouterFrame(senderSocket);
    send(senderSocket, { type: "register", agent_id: "worker-1", instance_id: "sender-instance" });
    assert.equal((await senderRegistered).type, "registered");

    router.enqueueMessage(envelope("batch-delivery-1", {
      from: "worker-1",
      to: "supervisor",
      subject: "Pi worker settled",
      body: "Worker worker-1 has settled and is idle.",
    }));
    router.enqueueMessage(envelope("batch-delivery-2", {
      from: "worker-2",
      to: "supervisor",
      subject: "Status update",
      body: "Worker worker-2 has settled and is idle.",
    }));

    receiverSocket = new WebSocket(websocketUrl);
    await new Promise((resolve, reject) => {
      receiverSocket.once("open", resolve);
      receiverSocket.once("error", reject);
    });
    const receiverFrames = [];
    receiverSocket.on("message", (payload) => receiverFrames.push(JSON.parse(payload.toString("utf8"))));
    send(receiverSocket, { type: "register", agent_id: "supervisor", instance_id: "receiver-instance" });
    await waitFor(() => receiverFrames.some((frame) => frame.type === "message"));
    assert.equal(receiverFrames[0].type, "registered");
    const delivery = receiverFrames.find((frame) => frame.type === "message");

    assert.equal(delivery.type, "message");
    assert.match(delivery.message.body, /^Mailbox message batch\n\n/);
    assert.equal((delivery.message.body.match(/\[Reply via mailbox to worker-1\.\]/g) ?? []).length, 1);
    assert.equal((delivery.message.body.match(/\[Reply via mailbox to worker-2\.\]/g) ?? []).length, 1);
    assert.doesNotMatch(delivery.message.body, /\[Reply via mailbox to worker-1\.\]$/);
  } finally {
    senderSocket?.terminate();
    receiverSocket?.terminate();
    await router.close();
    repository.close();
    await rm(directory, { recursive: true, force: true });
  }
});

test("router honors the configured maximum batched body character count", async () => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-mailbox-batch-limit-"));
  const batched = "Mailbox message batch\n\nFrom: worker-a\nSubject: Pi worker settled\n\nfirst\n\n[Reply via mailbox to worker-a.]\n\n---\n\nFrom: worker-b\nSubject: Another subject\n\nsecond\n\n[Reply via mailbox to worker-b.]";
  const repository = openMessageRepository({
    databasePath: join(directory, "router.sqlite"),
    maxBatchBodyChars: batched.length - 1,
  });
  try {
    const router = createAgentRouter({ repository, timeoutSweepIntervalMs: 60_000 });
    router.enqueueMessage(envelope("batch-limit-1", {
      from: "worker-a",
      to: "supervisor",
      subject: "Pi worker settled",
      body: "first",
    }));
    const result = router.enqueueMessage(envelope("batch-limit-2", {
      from: "worker-b",
      to: "supervisor",
      subject: "Another subject",
      body: "second",
    }));

    assert.equal(result.batched, undefined);
    assert.deepEqual(repository.queuedFor("supervisor").map(({ id }) => id), ["batch-limit-1", "batch-limit-2"]);
  } finally {
    repository.close();
    await rm(directory, { recursive: true, force: true });
  }
});

test("router rejects the sixteenth active unbatchable mailbox message by default", async () => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-mailbox-capacity-"));
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite"), maxBatchBodyChars: 0 });
  try {
    const router = createAgentRouter({ repository, timeoutSweepIntervalMs: 60_000 });
    for (let index = 1; index <= 15; index += 1) {
      router.enqueueMessage(envelope(`message-${index}`, {
        from: "worker-a",
        to: "supervisor",
        subject: `unique-${index}`,
        body: `message ${index}`,
      }));
    }

    assert.equal(repository.queueUsageFor("supervisor").message_count, 15);
    assert.throws(() => router.enqueueMessage(envelope("message-16", {
      from: "worker-a",
      to: "supervisor",
      subject: "unique-16",
      body: "message 16",
    })), QueueLimitError);
  } finally {
    repository.close();
    await rm(directory, { recursive: true, force: true });
  }
});

test("router and SQLite recover timed-out delivery attempts and complete one-shot reminders", async () => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-mailbox-router-"));
  const databasePath = join(directory, "router.sqlite");
  let now = Date.parse("2026-07-13T00:00:00.000Z");
  let repository;
  let router;
  const openRouter = () => {
    repository = openMessageRepository({
      databasePath,
      clock: () => new Date(now),
    });
    router = createAgentRouter({
      repository,
      clock: () => new Date(now),
      leaseDurationMs: 10_000,
      deliveryLeaseDurationMs: 100,
      timeoutSweepIntervalMs: 60_000,
    });
  };
  openRouter();
  let socket;
  let instanceId = "worker-instance";
  let frameId = 0;
  const send = (frame) => socket.send(JSON.stringify({
    version: "v1",
    frame_id: `client-${++frameId}`,
    ...frame,
  }));
  const receipt = (type, messageId, attemptNumber, leaseId) => send({
    type,
    agent_id: "worker",
    instance_id: instanceId,
    lease_id: leaseId,
    message_id: messageId,
    attempt_number: attemptNumber,
  });
  const drainAck = async () => {
    const pong = nextRouterFrame(socket);
    send({ type: "ping" });
    assert.equal((await pong).type, "pong");
  };
  try {
    const websocketUrl = await router.listen();
    assert.match(websocketUrl, /^ws:\/\/127\.0\.0\.1:/);
    socket = new WebSocket(websocketUrl);
    await new Promise((resolve, reject) => {
      socket.once("open", resolve);
      socket.once("error", reject);
    });

    const registered = nextRouterFrame(socket);
    send({ type: "register", agent_id: "worker", instance_id: instanceId });
    const registration = await registered;
    assert.equal(registration.type, "registered");
    assert.match(registration.frame_id, /^[A-Za-z0-9]{21}$/);
    const leaseId = registration.lease_id;
    assert.match(leaseId, /^[A-Za-z0-9]{21}$/);
    assert.notEqual(registration.frame_id, leaseId);

    const firstOffer = nextRouterFrame(socket);
    router.enqueueMessage(envelope("delivery-1"));
    const firstDelivery = await firstOffer;
    assert.deepEqual(
      { type: firstDelivery.type, messageId: firstDelivery.message.id, attempt: firstDelivery.attempt_number },
      { type: "message", messageId: "delivery-1", attempt: 1 },
    );
    receipt("injection_requested", "delivery-1", firstDelivery.attempt_number, leaseId);
    await drainAck();
    router.enqueueMessage(envelope("delivery-2"));

    repository.createReminder({
      id: "reminder-1",
      target: "worker",
      creator: "tester",
      body: "reminder body",
      created_at: new Date(now).toISOString(),
      next_due_at: new Date(now + 50).toISOString(),
      interval_ms: null,
    });
    socket.terminate();
    await router.close();
    repository.close();
    openRouter();
    const restartedUrl = await router.listen();
    socket = new WebSocket(restartedUrl);
    await new Promise((resolve, reject) => {
      socket.once("open", resolve);
      socket.once("error", reject);
    });
    instanceId = "worker-instance-restarted";
    const restartedRegistration = nextRouterFrame(socket);
    send({ type: "register", agent_id: "worker", instance_id: instanceId });
    const restartedLease = await restartedRegistration;
    assert.equal(restartedLease.type, "registered");
    const recoveredLeaseId = restartedLease.lease_id;

    now += 101;
    const replayOffer = nextRouterFrame(socket);
    assert.deepEqual(router.sweepDeliveryTimeouts().map((outcome) => outcome.state), ["queued"]);
    const replay = await replayOffer;
    assert.deepEqual(
      { messageId: replay.message.id, attempt: replay.attempt_number },
      { messageId: "delivery-1", attempt: 2 },
    );
    receipt("injection_requested", "delivery-1", replay.attempt_number, recoveredLeaseId);
    const secondOffer = nextRouterFrame(socket);
    receipt("ack", "delivery-1", replay.attempt_number, recoveredLeaseId);
    const secondDelivery = await secondOffer;
    await drainAck();
    assert.equal(repository.messageFor("delivery-1"), undefined);
    assert.deepEqual(
      { messageId: secondDelivery.message.id, attempt: secondDelivery.attempt_number },
      { messageId: "delivery-2", attempt: 1 },
    );
    receipt("injection_requested", "delivery-2", secondDelivery.attempt_number, recoveredLeaseId);
    receipt("ack", "delivery-2", secondDelivery.attempt_number, recoveredLeaseId);
    await drainAck();
    assert.equal(repository.messageFor("delivery-2"), undefined);

    const reminderOffer = nextRouterFrame(socket);
    const fired = router.sweepDueReminders();
    assert.equal(fired.length, 1);
    const reminderDelivery = await reminderOffer;
    assert.equal(reminderDelivery.message.id, fired[0].message.id);
    receipt("injection_requested", reminderDelivery.message.id, reminderDelivery.attempt_number, recoveredLeaseId);
    receipt("ack", reminderDelivery.message.id, reminderDelivery.attempt_number, recoveredLeaseId);
    await drainAck();
    assert.equal(repository.reminderFor("reminder-1"), undefined);
  } finally {
    socket?.terminate();
    await router.close();
    repository.close();
    await rm(directory, { recursive: true, force: true });
  }
});
