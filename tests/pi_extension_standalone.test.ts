import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import { once } from "node:events";
import { chmod, mkdir, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { existsSync } from "node:fs";
import { createServer } from "node:http";
import test from "node:test";

import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { promisify } from "node:util";

const execFileAsync = promisify(execFile);

import {
  createInlineToolPolicy,
  createMechaMessagingExtension,
  createNativeSubagentTrace,
  INLINE_TOOL_ALLOWANCE_TOOL_NAME,
} from "../pi-extensions/mecha-messaging/index.ts";
import goalExtension, { GOAL_ACTIVITY_EVENT, GOAL_STATE_REQUEST_EVENT } from "../pi-extensions/mecha-goal/index.ts";
import { createMailboxClient } from "../pi-extensions/mecha-messaging/mailbox-client.js";

const MAX_NOT_DONE_WAKEUPS = 1000;

function createMessagingFixture({
  sendMessage = () => {},
  sessionId = "parent-session",
  header = {},
  contextUsage,
  nowImpl = Date.now,
  client: configuredClient,
  createClient,
  resolveCurrentAgentId = () => "worker",
  model = { provider: "openai-codex", id: "gpt-5.6-terra", name: "GPT-5.6 Terra" },
  availableModels = [model],
  setModel = () => true,
  updateContextModel = true,
  complete = async () => ({ content: [{ type: "text", text: "VERDICT: NOT_DONE\nREASON: Work remains." }] }),
} = {}) {
  const handlers = new Map();
  const childHandlers = new Map();
  const tools = new Map();
  const commands = new Map();
  const branch = [];
  const emittedEvents = [];
  const sent = [];
  const modelSwitches = [];
  const mailboxClients = [];
  const createContext = (currentSessionId = sessionId) => ({
    sessionManager: {
      getSessionId: () => currentSessionId,
      getHeader: () => header,
      getBranch: () => branch,
      isPersisted: () => true,
      getSessionFile: () => `/tmp/${currentSessionId}.jsonl`,
    },
    ...(contextUsage === undefined ? {} : { getContextUsage: contextUsage }),
    model,
    modelRegistry: { getAvailable: () => availableModels, complete },
    ui: { notify() {}, setStatus() {} },
  });
  const context = createContext();
  const pi = {
    getSessionId: () => sessionId,
    events: {
      on(type, handler) { childHandlers.set(type, handler); },
      emit(type, data) {
        emittedEvents.push({ type, data });
        childHandlers.get(type)?.(data);
      },
    },
    on(type, handler) {
      const bucket = handlers.get(type) ?? [];
      bucket.push(handler);
      handlers.set(type, bucket);
    },
    registerCommand(name, command) { commands.set(name, command); },
    registerTool(tool) { tools.set(tool.name, tool); },
    appendEntry(customType, data) {
      const entry = { type: "custom", customType, data };
      branch.push(entry);
    },
    setModel(nextModel) {
      modelSwitches.push(nextModel);
      const switched = setModel(nextModel, context);
      if (switched && updateContextModel) context.model = nextModel;
      return switched;
    },
    sendMessage: (...args) => {
      sent.push(args);
      sendMessage(...args);
    },
    exec: async () => ({ code: 0, stderr: "" }),
  };
  createMechaMessagingExtension(pi, {
    resolveCurrentAgentId,
    createClient: createClient ?? ((options) => {
      const client = configuredClient ?? {
        connect: async () => {},
        close() {},
        sendReceipt() {},
        sendActivity() {},
        sendReply: async ({ to, body, subject }) => ({ id: "message-1", state: "queued", to, body, subject }),
        onEnvelope: options.onEnvelope,
      };
      mailboxClients.push(client);
      return client;
    }),
    nowImpl,
    createSettledScheduler: () => ({
      onParentActivity() {},
      onParentSettled() {},
      onChildStarted() {},
      onChildDisposed() {},
      dispose() {},
    }),
  });
  return { childHandlers, commands, context, createContext, emittedEvents, handlers, mailboxClients, modelSwitches, pi, sent, tools };
}

async function emitToolCalls(fixture, toolNames, context = fixture.context) {
  const results = [];
  for (const toolName of toolNames) {
    for (const handler of fixture.handlers.get("tool_call") ?? []) {
      results.push(await handler({ toolName }, context));
    }
  }
  return results;
}

function assertBlocked(result) {
  assert.equal(result?.block, true);
  assert.equal(typeof result?.reason, "string");
}

test("inline tool policy validates temporary allowance requests", () => {
  const policy = createInlineToolPolicy();
  assert.throws(() => policy.grant("", 1), /toolName/);
  assert.throws(() => policy.grant("edit", 0), /usageCount/);
  assert.throws(() => policy.grant("edit", 1.5), /usageCount/);
  assert.throws(() => policy.grant("edit", 6), /usageCount/);
});

test("function tool aliases grant usable credits and invalid names grant none", async () => {
  for (const [alias, canonical] of [
    ["functions.bash", "bash"],
    ["functions.edit", "edit"],
    ["functions.write", "write"],
  ]) {
    const fixture = createMessagingFixture();
    const allowanceTool = fixture.tools.get(INLINE_TOOL_ALLOWANCE_TOOL_NAME);

    const aliasGrant = await allowanceTool.execute("call-1", { toolName: alias, usageCount: 2 });
    assert.equal(aliasGrant.details.toolName, canonical);
    assert.deepEqual(await emitToolCalls(fixture, [alias, canonical]), [undefined, undefined]);
    assertBlocked((await emitToolCalls(fixture, [canonical]))[0]);
    assertBlocked((await emitToolCalls(fixture, [alias]))[0]);
  }

  const fixture = createMessagingFixture();
  const allowanceTool = fixture.tools.get(INLINE_TOOL_ALLOWANCE_TOOL_NAME);
  const invalidGrant = await allowanceTool.execute("call-2", { toolName: "functions.read", usageCount: 1 });
  assert.equal(invalidGrant.isError, true);
  assert.match(invalidGrant.content[0].text, /unsupported.*tool/i);
  const validGrant = await allowanceTool.execute("call-3", { toolName: "edit", usageCount: 5 });
  assert.equal(validGrant.details.grantedCount, 5);
  assertBlocked((await emitToolCalls(fixture, ["bash"]))[0]);
});

test("top-level reads are always allowed without nudges or state changes", async () => {
  const sent = [];
  const fixture = createMessagingFixture({ sendMessage: (...args) => sent.push(args) });
  const results = await emitToolCalls(fixture, Array.from({ length: 20 }, () => "read"));

  assert.equal(sent.length, 0);
  assert.deepEqual(results, Array.from({ length: 20 }, () => undefined));

  const allowanceTool = fixture.tools.get(INLINE_TOOL_ALLOWANCE_TOOL_NAME);
  await allowanceTool.execute("call-1", { toolName: "edit", usageCount: 1 });
  await emitToolCalls(fixture, Array.from({ length: 20 }, () => "read"));
  assert.deepEqual(await emitToolCalls(fixture, ["edit"]), [undefined]);
  const [blocked] = await emitToolCalls(fixture, ["edit"]);
  assertBlocked(blocked);
});

test("write-ish top-level tools are blocked by default with mandatory-subagent guidance", async () => {
  const fixture = createMessagingFixture();
  const results = await emitToolCalls(fixture, ["bash", "edit", "write"]);

  assert.equal(results.length, 3);
  for (const result of results) {
    assert.equal(result.block, true);
    assert.match(result.reason, /subagents are mandatory/i);
    assert.match(result.reason, new RegExp(INLINE_TOOL_ALLOWANCE_TOOL_NAME));
    assert.match(result.reason, /genuinely necessary/);
    assert.match(result.reason, /exceptional path/);
    assert.match(result.reason, /not the normal workaround/);
  }
});

test("allowance tool is registered with a five-usage maximum and exceptional wording", async () => {
  const fixture = createMessagingFixture();
  const allowanceTool = fixture.tools.get(INLINE_TOOL_ALLOWANCE_TOOL_NAME);

  assert.ok(allowanceTool);
  assert.equal(allowanceTool.parameters.properties.toolName.type, "string");
  assert.equal(allowanceTool.parameters.properties.usageCount.maximum, 5);
  assert.match(allowanceTool.description, /exception/i);
  assert.match(allowanceTool.description, /subagents remain mandatory|mandatory by default/i);

  const result = await allowanceTool.execute("call-1", { toolName: "edit", usageCount: 5 });
  assert.equal(result.details.usageCount, 5);
  assert.match(result.content[0].text, /exception/i);
  assert.match(result.content[0].text, /subagents remain mandatory|mandatory by default/i);

  assert.deepEqual(
    await emitToolCalls(fixture, Array.from({ length: 5 }, () => "edit")),
    Array.from({ length: 5 }, () => undefined),
  );
  const [blocked] = await emitToolCalls(fixture, ["edit"]);
  assertBlocked(blocked);

  const rejected = await allowanceTool.execute("call-2", { toolName: "edit", usageCount: 6 });
  assert.equal(rejected.isError, true);
  assert.match(rejected.content[0].text, /usageCount/);
});

test("allowance requests start with five credits and report the pool", async () => {
  const fixture = createMessagingFixture({ nowImpl: () => 0 });
  const allowanceTool = fixture.tools.get(INLINE_TOOL_ALLOWANCE_TOOL_NAME);

  const result = await allowanceTool.execute("call-1", { toolName: "edit", usageCount: 5 });
  assert.equal(result.details.grantedCount, 5);
  assert.equal(result.details.remainingPool, 0);
  assert.equal(result.details.remaining, 5);
});

test("allowance pool replenishment is capped at five credits", async () => {
  let now = 0;
  const fixture = createMessagingFixture({ nowImpl: () => now });
  const allowanceTool = fixture.tools.get(INLINE_TOOL_ALLOWANCE_TOOL_NAME);

  const first = await allowanceTool.execute("call-1", { toolName: "edit", usageCount: 3 });
  assert.equal(first.details.grantedCount, 3);
  now = 10 * 60_000;
  const second = await allowanceTool.execute("call-2", { toolName: "bash", usageCount: 5 });
  assert.equal(second.details.grantedCount, 5);
  assert.equal(second.details.remainingPool, 0);
});

test("exhausted allowance pool returns a clear error without granting credits", async () => {
  const fixture = createMessagingFixture({ nowImpl: () => 0 });
  const allowanceTool = fixture.tools.get(INLINE_TOOL_ALLOWANCE_TOOL_NAME);

  await allowanceTool.execute("call-1", { toolName: "edit", usageCount: 5 });
  const denied = await allowanceTool.execute("call-2", { toolName: "bash", usageCount: 1 });
  assert.equal(denied.isError, true);
  assert.match(denied.content[0].text, /requested 1 credit/i);
  assert.match(denied.content[0].text, /0 whole credit.*available/i);
  assert.match(denied.content[0].text, /pool is exhausted/i);
  assert.match(denied.content[0].text, /1 credit per minute.*maximum of 5 credits/i);
  assert.equal(denied.details.grantedCount, 0);
  assert.equal(denied.details.availableWholeCredits, 0);
  assert.equal(denied.details.remainingPool, 0);
  assertBlocked((await emitToolCalls(fixture, ["bash"]))[0]);
});

test("allowance pool virtually replenishes one credit after one minute", async () => {
  let now = 0;
  const fixture = createMessagingFixture({ nowImpl: () => now });
  const allowanceTool = fixture.tools.get(INLINE_TOOL_ALLOWANCE_TOOL_NAME);

  await allowanceTool.execute("call-1", { toolName: "edit", usageCount: 5 });
  now = 60_000;
  const replenished = await allowanceTool.execute("call-2", { toolName: "bash", usageCount: 1 });
  assert.equal(replenished.details.grantedCount, 1);
  assert.equal(replenished.details.remainingPool, 0);
});

test("allowance replenishment keeps exact minute boundaries after repeated updates", async () => {
  let now = 0;
  const fixture = createMessagingFixture({ nowImpl: () => now });
  const allowanceTool = fixture.tools.get(INLINE_TOOL_ALLOWANCE_TOOL_NAME);

  await allowanceTool.execute("call-1", { toolName: "edit", usageCount: 5 });
  for (let minute = 1; minute <= 5; minute += 1) {
    now = minute * 60_000;
    const replenished = await allowanceTool.execute(`call-${minute + 1}`, { toolName: "bash", usageCount: 1 });
    assert.equal(replenished.details.grantedCount, 1);
    assert.deepEqual(await emitToolCalls(fixture, ["bash"]), [undefined]);
  }
  assertBlocked((await emitToolCalls(fixture, ["bash"]))[0]);
});

test("insufficient partial pool denies the whole request and preserves fractional credits", async () => {
  let now = 0;
  const fixture = createMessagingFixture({ nowImpl: () => now });
  const allowanceTool = fixture.tools.get(INLINE_TOOL_ALLOWANCE_TOOL_NAME);

  await allowanceTool.execute("call-1", { toolName: "edit", usageCount: 5 });
  now = 90_000;
  const denied = await allowanceTool.execute("call-2", { toolName: "bash", usageCount: 2 });
  assert.equal(denied.isError, true);
  assert.equal(denied.details.grantedCount, 0);
  assert.equal(denied.details.availableWholeCredits, 1);
  assert.equal(denied.details.remainingPool, 1.5);
  assert.match(denied.content[0].text, /requested 2 credit/i);
  assert.match(denied.content[0].text, /only 1 whole credit.*available/i);
  assert.match(denied.content[0].text, /1 credit per minute.*maximum of 5 credits/i);
  assertBlocked((await emitToolCalls(fixture, ["bash"]))[0]);

  const completed = await allowanceTool.execute("call-3", { toolName: "write", usageCount: 1 });
  assert.equal(completed.details.grantedCount, 1);
  assert.equal(completed.details.remainingPool, 0.5);
});

test("request larger than available whole credits is denied without partial allowance", async () => {
  let now = 0;
  const fixture = createMessagingFixture({ nowImpl: () => now });
  const allowanceTool = fixture.tools.get(INLINE_TOOL_ALLOWANCE_TOOL_NAME);

  await allowanceTool.execute("call-1", { toolName: "edit", usageCount: 5 });
  now = 2 * 60_000;
  const denied = await allowanceTool.execute("call-2", { toolName: "bash", usageCount: 5 });
  assert.equal(denied.isError, true);
  assert.equal(denied.details.grantedCount, 0);
  assert.equal(denied.details.availableWholeCredits, 2);
  assert.equal(denied.details.remainingPool, 2);
  assertBlocked((await emitToolCalls(fixture, ["bash"]))[0]);

  const completed = await allowanceTool.execute("call-3", { toolName: "bash", usageCount: 2 });
  assert.equal(completed.details.grantedCount, 2);
  assert.equal(completed.details.remainingPool, 0);
});

test("allowance credits spend down only on matching top-level calls", async () => {
  const fixture = createMessagingFixture();
  const allowanceTool = fixture.tools.get(INLINE_TOOL_ALLOWANCE_TOOL_NAME);
  await allowanceTool.execute("call-1", { toolName: "edit", usageCount: 2 });

  for (const blockedTool of await emitToolCalls(fixture, ["bash", "write"])) assertBlocked(blockedTool);
  assert.deepEqual(await emitToolCalls(fixture, ["edit", "edit"]), [undefined, undefined]);
  const [blockedEdit] = await emitToolCalls(fixture, ["edit"]);
  assertBlocked(blockedEdit);
});

test("allowance credits persist across agent runs within a session", async () => {
  const fixture = createMessagingFixture();
  const allowanceTool = fixture.tools.get(INLINE_TOOL_ALLOWANCE_TOOL_NAME);
  await allowanceTool.execute("call-1", { toolName: "edit", usageCount: 1 });
  for (const handler of fixture.handlers.get("agent_start") ?? []) await handler({}, fixture.context);

  assert.deepEqual(await emitToolCalls(fixture, ["edit"]), [undefined]);
  assertBlocked((await emitToolCalls(fixture, ["edit"]))[0]);
});

test("allowance pool persists across agent runs and resets for a new session", async () => {
  const fixture = createMessagingFixture({ nowImpl: () => 0 });
  const allowanceTool = fixture.tools.get(INLINE_TOOL_ALLOWANCE_TOOL_NAME);
  await allowanceTool.execute("call-1", { toolName: "edit", usageCount: 5 });

  for (const handler of fixture.handlers.get("agent_start") ?? []) await handler({}, fixture.context);
  const deniedDuringSameSession = await allowanceTool.execute("call-2", { toolName: "bash", usageCount: 1 });
  assert.equal(deniedDuringSameSession.isError, true);
  assert.equal(deniedDuringSameSession.details.remainingPool, 0);

  const newSessionContext = fixture.createContext("new-session");
  for (const handler of fixture.handlers.get("session_start") ?? []) await handler({}, newSessionContext);
  const grantedInNewSession = await allowanceTool.execute("call-3", { toolName: "bash", usageCount: 5 });
  assert.equal(grantedInNewSession.details.grantedCount, 5);
  assert.equal(grantedInNewSession.details.remainingPool, 0);
});

test("session-scoped allowance reset preserves context-threshold notifications", async () => {
  const fixture = createMessagingFixture({
    contextUsage: () => ({ tokens: 510_000, contextWindow: 1_000_000, percent: 51 }),
  });

  for (const handler of fixture.handlers.get("session_start") ?? []) await handler({}, fixture.context);
  for (const handler of fixture.handlers.get("agent_start") ?? []) await handler({}, fixture.context);
  for (const handler of fixture.handlers.get("agent_settled") ?? []) await handler({}, fixture.context);

  const threshold = fixture.sent.find(([message]) => message?.customType === "mecha-context-threshold");
  assert.ok(threshold);
  assert.equal(threshold[0].details.threshold_percent, 45);
  assert.deepEqual(threshold[1], { triggerTurn: false });
});

test("session-scoped allowance reset preserves mailbox delivery after agent start", async () => {
  const fixture = createMessagingFixture();
  for (const handler of fixture.handlers.get("session_start") ?? []) await handler({}, fixture.context);
  for (const handler of fixture.handlers.get("agent_start") ?? []) await handler({}, fixture.context);

  assert.equal(fixture.mailboxClients.length, 1);
  await fixture.mailboxClients[0].onEnvelope(
    { id: "message-1", from: "sender", to: "worker", body: "preserve this", source: { kind: "agent" } },
    { messageId: "message-1", attemptNumber: 1 },
  );

  const mailboxMessage = fixture.sent.find(([message]) => message?.customType === "mecha-agent-message");
  assert.ok(mailboxMessage);
  assert.match(mailboxMessage[0].content, /preserve this/);
  assert.deepEqual(mailboxMessage[1], { deliverAs: "steer", triggerTurn: true });
});

async function sessionStart(fixture, context = fixture.context) {
  for (const handler of fixture.handlers.get("session_start") ?? []) await handler({}, context);
}

async function runHandlers(fixture, eventName, event = {}, context = fixture.context) {
  for (const handler of fixture.handlers.get(eventName) ?? []) await handler(event, context);
}

test("managed workers receive their tmux identity on startup and resume", async () => {
  for (const [reason, agentId] of [["startup", "worker-start"], ["resume", "worker-resumed"]]) {
    const fixture = createMessagingFixture({ resolveCurrentAgentId: () => agentId });
    for (const handler of fixture.handlers.get("session_start") ?? []) {
      await handler({ reason }, fixture.context);
    }

    assert.deepEqual(fixture.sent, [[{
      customType: "mecha-worker-identity",
      content: `Your managed worker identity is ${agentId}.`,
      display: false,
      details: { agent_id: agentId },
    }, {
      deliverAs: "followUp",
      triggerTurn: false,
    }]], reason);
  }
});

test("goal progress bridge counts only successful concrete actions with provenance", async () => {
  const fixture = createMessagingFixture({ sessionId: "parent-session" });
  goalExtension(fixture.pi);
  // The goal extension is installed after messaging in this fixture, so replay the optional
  // state request once to establish the bridge before the goal is set.
  fixture.pi.events.emit(GOAL_STATE_REQUEST_EVENT);
  await sessionStart(fixture);
  const goal = fixture.tools.get("goal");
  const set = await goal.execute("goal-set", { action: "set", goal: "ship the bridge" }, undefined, undefined, fixture.context);
  assert.equal(set.isError, undefined);
  await runHandlers(fixture, "tool_result", { toolName: "read", toolCallId: "read-1", isError: false });
  const first = fixture.emittedEvents.filter(({ type }) => type === GOAL_ACTIVITY_EVENT);
  assert.equal(first.length, 1);
  assert.match(first[0].data.producerEpoch, /^[0-9a-f-]{36}$/);
  const firstData = { ...first[0].data };
  delete firstData.producerEpoch;
  assert.deepEqual(firstData, {
    activityId: "tool-completed:read-1",
    kind: "tool-completed",
    revision: 1,
    sessionId: "parent-session",
    workerId: "worker",
    goal: { enabled: true, goal: "ship the bridge" },
  });

  // A replayed provider result is not a new worker action.
  await runHandlers(fixture, "tool_result", { toolName: "read", toolCallId: "read-1", isError: false });
  // Failed work and control-plane tools are never progress events.
  await runHandlers(fixture, "tool_result", { toolName: "edit", toolCallId: "edit-1", isError: true });
  await runHandlers(fixture, "tool_result", { toolName: "read", toolCallId: "ambiguous-1" });
  await runHandlers(fixture, "tool_execution_end", { toolName: "read", toolCallId: "failed-end", isError: true });
  await runHandlers(fixture, "tool_execution_end", { toolName: "read", toolCallId: "successful-end", isError: false });
  for (const toolName of ["goal", "goal_snooze", "mecha_mailbox_send", "mecha_reminder", "mecha_worker"]) {
    await runHandlers(fixture, "tool_result", { toolName, toolCallId: `${toolName}-1`, isError: false });
  }
  assert.equal(fixture.emittedEvents.filter(({ type }) => type === GOAL_ACTIVITY_EVENT).length, 2);

  await fixture.childHandlers.get("subagents:started")({ id: "child-1", type: "general-purpose", description: "Concrete child" });
  const activity = fixture.emittedEvents.filter(({ type }) => type === GOAL_ACTIVITY_EVENT);
  assert.equal(activity.length, 3);
  assert.equal(activity[1].data.kind, "tool-completed");
  assert.equal(activity[1].data.activityId, "tool-completed:successful-end");
  assert.equal(activity[1].data.revision, 2);
  assert.equal(activity[2].data.kind, "native-subagent-launch");
  assert.equal(activity[2].data.activityId, "native-subagent-launch:child-1");
  assert.equal(activity[2].data.revision, 3);

  // The goal consumer rejects malformed/stale/foreign provenance even if another local caller
  // tries to impersonate a meaningful event. This is not a transport or liveness signal.
  const consume = fixture.childHandlers.get(GOAL_ACTIVITY_EVENT);
  consume({ activityId: "foreign-worker", kind: "tool-completed", revision: 3, sessionId: "parent-session", workerId: "other-worker", goal: { enabled: true, goal: "ship the bridge" } });
  consume({ activityId: "foreign-goal", kind: "tool-completed", revision: 4, sessionId: "parent-session", workerId: "worker", goal: { enabled: true, goal: "other goal" } });
  consume({ activityId: "stale-check", kind: "goal-check", revision: 5, sessionId: "parent-session", workerId: "worker", goal: { enabled: true, goal: "ship the bridge" } });
  assert.equal(fixture.emittedEvents.filter(({ type }) => type === GOAL_ACTIVITY_EVENT).length, 3, "foreign/stale consumer injections do not create bridge emissions");

  const goalResults = () => fixture.sent.filter(([message]) => message?.customType === "goal-result");
  const settleAndFlush = async () => {
    await runHandlers(fixture, "agent_settled");
    await new Promise((resolve) => setTimeout(resolve, 0));
    await new Promise((resolve) => setTimeout(resolve, 0));
  };
  for (let attempt = 0; attempt < MAX_NOT_DONE_WAKEUPS; attempt += 1) await settleAndFlush();
  assert.equal(goalResults().length, MAX_NOT_DONE_WAKEUPS, "the first retry-limit NOT_DONE attempts are delivered");
  for (let action = 1; action <= 2; action += 1) {
    await runHandlers(fixture, "tool_result", { toolName: "read", toolCallId: `rearm-${action}`, isError: false });
    await settleAndFlush();
    assert.equal(goalResults().length, MAX_NOT_DONE_WAKEUPS, `${action} concrete action(s) do not rearm early`);
  }
  await runHandlers(fixture, "tool_result", { toolName: "read", toolCallId: "rearm-3", isError: false });
  await settleAndFlush();
  assert.equal(goalResults().length, MAX_NOT_DONE_WAKEUPS + 1, "the third producer action re-arms the next delivery");
  for (let attempt = 0; attempt < MAX_NOT_DONE_WAKEUPS - 1; attempt += 1) await settleAndFlush();
  assert.equal(goalResults().length, MAX_NOT_DONE_WAKEUPS * 2, "the re-armed cycle delivers the remaining attempts");
  await settleAndFlush();
  assert.equal(goalResults().length, MAX_NOT_DONE_WAKEUPS * 2, "the second cycle is capped at the retry limit");
});

test("activity broadcasts current model metadata without recurring catalog payloads", async () => {
  const activity = [];
  const current = {
    provider: "openai-codex",
    id: "gpt-5.6-terra",
    name: "GPT-5.6 Terra",
    contextWindow: 1_000_000,
    unsupportedObject: { secret: true },
  };
  const available = [
    current,
    { provider: "anthropic", id: "claude-opus-5", name: "Claude Opus 5", maxTokens: 64_000 },
    ...Array.from({ length: 110 }, (_, index) => ({ provider: "provider", id: `model-${index}` })),
  ];
  const fixture = createMessagingFixture({
    model: current,
    availableModels: available,
    client: {
      connect: async () => {},
      close() {},
      sendReceipt() {},
      sendActivity: (...args) => activity.push(args),
    },
  });
  await sessionStart(fixture);
  for (const handler of fixture.handlers.get("agent_start") ?? []) await handler({}, fixture.context);

  assert.equal(activity.length, 1);
  assert.equal(activity[0][0], "active");
  assert.deepEqual(activity[0][1].subagentCount, 0);
  assert.deepEqual(activity[0][1].subagents, []);
  assert.deepEqual(activity[0][1].currentModel, {
    provider: "openai-codex",
    id: "gpt-5.6-terra",
    name: "GPT-5.6 Terra",
    contextWindow: 1_000_000,
  });
  assert.equal("availableModels" in activity[0][1], false);
  assert.doesNotThrow(() => JSON.stringify(activity[0][1]));
});

test("parent model control invokes the distinct mailbox callback", async () => {
  let modelControl;
  let getModelCatalog;
  const current = { provider: "openai-codex", id: "gpt-5.6-terra" };
  const target = { provider: "anthropic", id: "claude-opus-5" };
  const fixture = createMessagingFixture({
    model: current,
    availableModels: [current, target],
    createClient: (options) => {
      modelControl = options.onModelControl;
      getModelCatalog = options.getModelCatalog;
      return { connect: async () => {}, close() {}, sendReceipt() {}, sendActivity() {} };
    },
  });
  await sessionStart(fixture);
  assert.equal(typeof modelControl, "function");
  assert.equal(typeof getModelCatalog, "function");
  assert.deepEqual(getModelCatalog(), [current, target]);
  const result = await modelControl({
    action: "set_model",
    model: target,
    controlId: "control-1",
    sourceAgentId: "controller",
  });

  assert.deepEqual(result, {
    accepted: true,
    currentModel: target,
  });
});

test("parent model control publishes the selected model before a fresh context snapshot", async () => {
  let modelControl;
  const activity = [];
  const current = { provider: "openai-codex", id: "gpt-5.6-terra" };
  const target = { provider: "anthropic", id: "claude-opus-5" };
  const fixture = createMessagingFixture({
    model: current,
    availableModels: [current, target],
    updateContextModel: false,
    createClient: (options) => {
      modelControl = options.onModelControl;
      return {
        connect: async () => {},
        close() {},
        sendReceipt() {},
        sendActivity: (...args) => activity.push(args),
      };
    },
  });
  await sessionStart(fixture);

  const result = await modelControl({ action: "set_model", model: target });

  assert.equal(result.accepted, true);
  assert.deepEqual(result.currentModel, target);
  assert.deepEqual(activity.at(-1)[1].currentModel, target);
  assert.deepEqual(fixture.context.model, current);
});

test("real mailbox model-control delivery switches the Pi model", async () => {
  const current = { provider: "openai-codex", id: "gpt-5.6-terra" };
  const target = { provider: "anthropic", id: "claude-opus-5" };
  const socket = new EventTarget();
  socket.readyState = 0;
  socket.sent = [];
  socket.send = (payload) => socket.sent.push(JSON.parse(payload));
  socket.close = () => { socket.readyState = 3; };
  let client;
  const fixture = createMessagingFixture({
    model: current,
    availableModels: [current, target],
    createClient: (options) => {
      client = createMailboxClient({
        ...options,
        routerUrl: "http://127.0.0.1:8765",
        webSocketFactory: () => socket,
        fetchImpl: async () => ({ ok: false, json: async () => ({}) }),
        generateId: (() => {
          let id = 0;
          return () => `model-control-${++id}`;
        })(),
      });
      return client;
    },
  });

  await sessionStart(fixture);
  socket.readyState = 1;
  socket.dispatchEvent(new Event("open"));
  const register = socket.sent.find((frame) => frame.type === "register");
  socket.dispatchEvent(new MessageEvent("message", {
    data: JSON.stringify({
      version: "v1",
      frame_id: "registered-model-control",
      type: "registered",
      request_id: register.frame_id,
      agent_id: "worker",
      instance_id: client.instanceId,
      lease_id: "lease-model-control",
      expires_at: "2030-01-01T00:00:00.000Z",
      recovered: false,
      agent_role: "agent",
    }),
  }));
  await new Promise((resolve) => setImmediate(resolve));

  socket.dispatchEvent(new MessageEvent("message", {
    data: JSON.stringify({
      version: "v1",
      frame_id: "control-model",
      type: "model_control",
      agent_id: "worker",
      instance_id: client.instanceId,
      lease_id: "lease-model-control",
      source_agent_id: "controller",
      control_id: "control-1",
      action: "set_model",
      model: target,
    }),
  }));
  await new Promise((resolve) => setImmediate(resolve));

  assert.deepEqual(fixture.modelSwitches, [target]);
  assert.deepEqual(fixture.context.model, target);
  const modelControlResult = socket.sent.find((frame) => frame.type === "model_control_result");
  assert.deepEqual({ ...modelControlResult, frame_id: "ignored" }, {
    version: "v1",
    frame_id: "ignored",
    type: "model_control_result",
    agent_id: "worker",
    instance_id: client.instanceId,
    lease_id: "lease-model-control",
    source_agent_id: "controller",
    control_id: "control-1",
    accepted: true,
    current_model: target,
  });
  socket.dispatchEvent(new MessageEvent("message", {
    data: JSON.stringify({
      version: "v1",
      frame_id: "control-missing-model",
      type: "model_control",
      agent_id: "worker",
      instance_id: client.instanceId,
      lease_id: "lease-model-control",
      source_agent_id: "controller",
      control_id: "control-missing",
      action: "set_model",
      model: { provider: "anthropic", id: "missing-model" },
      expires_at: "2030-01-01T00:00:00.000Z",
    }),
  }));
  await new Promise((resolve) => setImmediate(resolve));
  const rejectedResult = socket.sent.find((frame) => frame.type === "model_control_result"
    && frame.control_id === "control-missing");
  assert.equal(rejectedResult.accepted, false);
  assert.equal(rejectedResult.error_code, "model_unavailable");
  assert.deepEqual(rejectedResult.current_model, target);
  assert.deepEqual(fixture.modelSwitches, [target]);
  client.close();
});

test("parent model control rejects invalid requests without changing the Pi model", async () => {
  let modelControl;
  const current = { provider: "openai-codex", id: "gpt-5.6-terra" };
  const fixture = createMessagingFixture({
    model: current,
    availableModels: [current],
    createClient: (options) => {
      modelControl = options.onModelControl;
      return { connect: async () => {}, close() {}, sendReceipt() {}, sendActivity() {} };
    },
  });
  await sessionStart(fixture);

  const result = await modelControl({
    action: "set_model",
    model: { provider: "anthropic", id: "missing-model" },
    controlId: "control-1",
    sourceAgentId: "controller",
  });

  assert.deepEqual(result, {
    accepted: false,
    errorCode: "model_unavailable",
    currentModel: current,
  });
  assert.deepEqual(fixture.modelSwitches, []);
  assert.deepEqual(fixture.context.model, current);
});

test("mailbox client capability-gates and byte-bounds changed model catalogs", async () => {
  const socket = new EventTarget();
  socket.readyState = 0;
  socket.sent = [];
  socket.send = (payload) => socket.sent.push(JSON.parse(payload));
  socket.close = () => { socket.readyState = 3; };
  const intervals = [];
  const traces = [];
  const provider = "p".repeat(128);
  const first = { provider, id: "i".repeat(256), name: "n".repeat(256) };
  let catalog = [first, ...Array.from({ length: 100 }, (_, index) => ({
    provider: "provider",
    id: `model-${index}-${"x".repeat(200)}`,
    name: `Model ${index} ${"y".repeat(200)}`,
  }))];
  let catalogReads = 0;
  const client = createMailboxClient({
    agentId: "worker",
    onEnvelope: () => {},
    getModelCatalog: () => {
      catalogReads += 1;
      return catalog;
    },
    onModelControlTrace: (event) => traces.push(event),
    webSocketFactory: () => socket,
    fetchImpl: async () => ({
      ok: true,
      json: async () => ({
        subagent_lifecycle_v2: false,
        model_catalog_v1: true,
        model_catalog_limits: { frame_bytes: 1_024 },
      }),
    }),
    generateId: (() => { let index = 0; return () => `id-${++index}`; })(),
    renewIntervalMs: 11_000,
    setIntervalImpl: (callback, delay) => {
      intervals.push({ callback, delay });
      return intervals.length;
    },
    clearIntervalImpl: () => {},
  });

  const connected = client.connect();
  socket.readyState = 1;
  socket.dispatchEvent(new Event("open"));
  const register = socket.sent.find((frame) => frame.type === "register");
  socket.dispatchEvent(new MessageEvent("message", { data: JSON.stringify({
    type: "registered",
    agent_id: "worker",
    instance_id: client.instanceId,
    lease_id: "lease-1",
    request_id: register.frame_id,
  }) }));
  await connected;
  await new Promise((resolve) => setImmediate(resolve));

  const published = socket.sent.filter((frame) => frame.type === "model_catalog");
  assert.equal(published.length, 1);
  assert.ok(Buffer.byteLength(JSON.stringify(published[0]), "utf8") <= 1_024);
  assert.deepEqual(published[0].model_summaries[0], first);
  assert.ok(published[0].model_summaries.length < catalog.length);
  assert.equal(published[0].truncated, true);
  assert.equal(published[0].total_count, catalog.length);

  intervals.find(({ delay }) => delay === 10_000).callback();
  assert.equal(catalogReads, 2);
  assert.equal(socket.sent.filter((frame) => frame.type === "model_catalog").length, 1);

  catalog = [...catalog, { provider: "provider", id: "omitted-tail-change" }];
  intervals.find(({ delay }) => delay === 10_000).callback();
  assert.equal(socket.sent.filter((frame) => frame.type === "model_catalog").length, 2);
  assert.equal(socket.sent.filter((frame) => frame.type === "model_catalog").at(-1).total_count, catalog.length);

  catalog = Array.from({ length: 1_001 }, (_, index) => ({
    provider: "provider",
    id: `compact-${index}`,
  }));
  intervals.find(({ delay }) => delay === 10_000).callback();
  assert.equal(socket.sent.filter((frame) => frame.type === "model_catalog").length, 3);
  assert.equal(socket.sent.filter((frame) => frame.type === "model_catalog").at(-1).total_count, 1_001);
  assert.equal(socket.sent.filter((frame) => frame.type === "model_catalog").at(-1).truncated, true);

  catalog = [{ provider: "provider", id: "replacement" }];
  intervals.find(({ delay }) => delay === 10_000).callback();
  assert.equal(socket.sent.filter((frame) => frame.type === "model_catalog").length, 4);
  assert.ok(traces.some(({ event }) => event === "client_model_catalog_published"));
  client.close();
});

test("native trace retains model catalog size and truncation diagnostics", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "model-catalog-trace-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const path = join(directory, "trace.jsonl");
  const trace = createNativeSubagentTrace({ path, now: () => new Date("2030-01-01T00:00:00.000Z") });
  assert.equal(trace.record("client_model_catalog_published", {
    model_count: 90,
    source_model_count: 100,
    frame_bytes: 61_000,
    truncated: true,
  }), true);
  const record = JSON.parse((await readFile(path, "utf8")).trim());
  assert.deepEqual(record, {
    timestamp: "2030-01-01T00:00:00.000Z",
    event: "client_model_catalog_published",
    model_count: 90,
    source_model_count: 100,
    frame_bytes: 61_000,
    truncated: true,
  });
});

test("mailbox client keeps fallback catalog frames within 64 KiB", async () => {
  const socket = new EventTarget();
  socket.readyState = 0;
  socket.sent = [];
  socket.send = (payload) => socket.sent.push(JSON.parse(payload));
  socket.close = () => { socket.readyState = 3; };
  const catalog = Array.from({ length: 1_000 }, (_, index) => ({
    provider: "p".repeat(128),
    id: `${String(index).padStart(4, "0")}${"i".repeat(252)}`,
    name: "n".repeat(256),
  }));
  const client = createMailboxClient({
    agentId: "worker",
    onEnvelope: () => {},
    getModelCatalog: () => catalog,
    webSocketFactory: () => socket,
    fetchImpl: async () => ({
      ok: true,
      json: async () => ({ model_catalog_v1: true, subagent_lifecycle_v2: false }),
    }),
  });
  const connected = client.connect();
  socket.readyState = 1;
  socket.dispatchEvent(new Event("open"));
  const register = socket.sent.find((frame) => frame.type === "register");
  socket.dispatchEvent(new MessageEvent("message", { data: JSON.stringify({
    type: "registered",
    agent_id: "worker",
    instance_id: client.instanceId,
    lease_id: "lease-1",
    request_id: register.frame_id,
  }) }));
  await connected;
  await new Promise((resolve) => setImmediate(resolve));

  const published = socket.sent.find((frame) => frame.type === "model_catalog");
  assert.ok(Buffer.byteLength(JSON.stringify(published), "utf8") <= 65_536);
  assert.equal(published.truncated, true);
  assert.equal(published.total_count, 1_000);
  const nextFrame = {
    ...published,
    model_summaries: catalog.slice(0, published.model_summaries.length + 1),
  };
  assert.ok(Buffer.byteLength(JSON.stringify(nextFrame), "utf8") > 65_536);
  client.close();
});

test("mailbox client retries catalog publication after frame-budget and send failures", async () => {
  async function run({ frameBytes, failFirstCatalogSend = false }) {
    const socket = new EventTarget();
    socket.readyState = 0;
    socket.sent = [];
    let catalogSendFailures = failFirstCatalogSend ? 1 : 0;
    socket.send = (payload) => {
      const frame = JSON.parse(payload);
      if (frame.type === "model_catalog" && catalogSendFailures > 0) {
        catalogSendFailures -= 1;
        throw new Error("catalog send failed");
      }
      socket.sent.push(frame);
    };
    socket.close = () => { socket.readyState = 3; };
    const intervals = [];
    const traces = [];
    const client = createMailboxClient({
      agentId: "worker",
      onEnvelope: () => {},
      getModelCatalog: () => [{ provider: "provider", id: "model" }],
      onModelControlTrace: (event) => traces.push(event),
      webSocketFactory: () => socket,
      fetchImpl: async () => ({
        ok: true,
        json: async () => ({
          subagent_lifecycle_v2: false,
          model_catalog_v1: true,
          model_catalog_limits: { frame_bytes: frameBytes },
        }),
      }),
      renewIntervalMs: 11_000,
      setIntervalImpl: (callback, delay) => {
        intervals.push({ callback, delay });
        return intervals.length;
      },
      clearIntervalImpl: () => {},
    });
    const connected = client.connect();
    socket.readyState = 1;
    socket.dispatchEvent(new Event("open"));
    const register = socket.sent.find((frame) => frame.type === "register");
    socket.dispatchEvent(new MessageEvent("message", { data: JSON.stringify({
      type: "registered",
      agent_id: "worker",
      instance_id: client.instanceId,
      lease_id: "lease-1",
      request_id: register.frame_id,
    }) }));
    await connected;
    await new Promise((resolve) => setImmediate(resolve));
    return { client, intervals, socket, traces };
  }

  const tooSmall = await run({ frameBytes: 1 });
  assert.equal(tooSmall.socket.sent.some((frame) => frame.type === "model_catalog"), false);
  assert.ok(tooSmall.traces.some(({ event, reason }) => event === "client_model_catalog_publish_failed"
    && reason === "frame_budget_too_small"));
  tooSmall.client.close();

  const transientFailure = await run({ frameBytes: 1_024, failFirstCatalogSend: true });
  assert.equal(transientFailure.socket.sent.some((frame) => frame.type === "model_catalog"), false);
  assert.ok(transientFailure.traces.some(({ event, reason }) => event === "client_model_catalog_publish_failed"
    && reason === "socket_send_failed"));
  transientFailure.intervals.find(({ delay }) => delay === 10_000).callback();
  assert.equal(transientFailure.socket.sent.filter((frame) => frame.type === "model_catalog").length, 1);
  transientFailure.client.close();
});

test("mailbox client publishes no catalog without capability or after registry failure", async () => {
  async function run(capabilities, getModelCatalog, { responseOk = true } = {}) {
    const socket = new EventTarget();
    socket.readyState = 0;
    socket.sent = [];
    socket.send = (payload) => socket.sent.push(JSON.parse(payload));
    socket.close = () => { socket.readyState = 3; };
    const traces = [];
    const intervals = [];
    const client = createMailboxClient({
      agentId: "worker",
      onEnvelope: () => {},
      getModelCatalog,
      onModelControlTrace: (event) => traces.push(event),
      webSocketFactory: () => socket,
      fetchImpl: async () => ({ ok: responseOk, status: responseOk ? 200 : 404, json: async () => capabilities }),
      renewIntervalMs: 11_000,
      setIntervalImpl: (callback, delay) => {
        intervals.push({ callback, delay });
        return intervals.length;
      },
      clearIntervalImpl: () => {},
    });
    const connected = client.connect();
    socket.readyState = 1;
    socket.dispatchEvent(new Event("open"));
    socket.dispatchEvent(new MessageEvent("message", { data: JSON.stringify({
      type: "registered", agent_id: "worker", instance_id: client.instanceId, lease_id: "lease-1",
    }) }));
    await connected;
    await new Promise((resolve) => setImmediate(resolve));
    return { client, intervals, socket, traces };
  }

  const unsupported = await run({ subagent_lifecycle_v2: false }, () => [{ provider: "provider", id: "model" }]);
  assert.equal(unsupported.socket.sent.some((frame) => frame.type === "model_catalog"), false);
  unsupported.client.sendActivity("active", { currentModel: { provider: "provider", id: "model" } });
  assert.equal("current_model" in unsupported.socket.sent.at(-1), false);
  unsupported.client.close();

  const missingCapabilities = await run({}, () => [{ provider: "provider", id: "model" }], { responseOk: false });
  assert.equal(missingCapabilities.socket.sent.some((frame) => frame.type === "model_catalog"), false);
  assert.equal(missingCapabilities.traces.some(({ event }) => event === "client_model_catalog_capability_retry_scheduled"), false);
  missingCapabilities.client.close();

  let registryReads = 0;
  const failed = await run(
    { subagent_lifecycle_v2: false, model_catalog_v1: true },
    () => {
      registryReads += 1;
      if (registryReads === 1) throw new Error("registry exploded");
      return [{ provider: "provider", id: "recovered" }];
    },
  );
  assert.equal(failed.socket.sent.some((frame) => frame.type === "model_catalog"), false);
  assert.ok(failed.traces.some(({ event, error_name }) => event === "client_model_catalog_read_failed" && error_name === "Error"));
  failed.intervals.find(({ delay }) => delay === 10_000).callback();
  assert.equal(failed.socket.sent.some((frame) => frame.type === "model_catalog"), true);
  failed.client.close();
});

test("mailbox client retries transient capability discovery failures", async () => {
  const socket = new EventTarget();
  socket.readyState = 0;
  socket.sent = [];
  socket.send = (payload) => socket.sent.push(JSON.parse(payload));
  socket.close = () => { socket.readyState = 3; };
  const timers = [];
  const traces = [];
  let capabilityCalls = 0;
  const client = createMailboxClient({
    agentId: "worker",
    onEnvelope: () => {},
    getModelCatalog: () => [{ provider: "provider", id: "model" }],
    onModelControlTrace: (event) => traces.push(event),
    webSocketFactory: () => socket,
    fetchImpl: async () => {
      capabilityCalls += 1;
      return capabilityCalls === 1
        ? { ok: false, status: 503, json: async () => ({}) }
        : { ok: true, status: 200, json: async () => ({ model_catalog_v1: true }) };
    },
    setTimeoutImpl: (callback, delay) => {
      timers.push({ callback, delay });
      return timers.length;
    },
    clearTimeoutImpl: () => {},
  });
  const connected = client.connect();
  socket.readyState = 1;
  socket.dispatchEvent(new Event("open"));
  socket.dispatchEvent(new MessageEvent("message", { data: JSON.stringify({
    type: "registered", agent_id: "worker", instance_id: client.instanceId, lease_id: "lease-1",
  }) }));
  await connected;
  await new Promise((resolve) => setImmediate(resolve));

  assert.equal(socket.sent.some((frame) => frame.type === "model_catalog"), false);
  assert.equal(timers[0].delay, 1_000);
  assert.ok(traces.some(({ event }) => event === "client_model_catalog_capability_retry_scheduled"));
  timers[0].callback();
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(capabilityCalls, 2);
  assert.equal(socket.sent.filter((frame) => frame.type === "model_catalog").length, 1);
  client.close();
});

test("mailbox client serializes model controls in arrival order", async () => {
  const socket = new EventTarget();
  socket.readyState = 0;
  socket.sent = [];
  socket.send = (payload) => socket.sent.push(JSON.parse(payload));
  socket.close = () => { socket.readyState = 3; };
  const started = [];
  const resolvers = [];
  const traces = [];
  const client = createMailboxClient({
    agentId: "worker",
    onEnvelope: () => {},
    onModelControl: ({ controlId, model }) => new Promise((resolve) => {
      started.push(controlId);
      resolvers.push(() => resolve({ accepted: true, currentModel: model, availableModels: [model] }));
    }),
    onModelControlTrace: (event) => traces.push(event),
    webSocketFactory: () => socket,
    fetchImpl: async () => ({ ok: true, json: async () => ({}) }),
  });
  const connected = client.connect();
  socket.readyState = 1;
  socket.dispatchEvent(new Event("open"));
  socket.dispatchEvent(new MessageEvent("message", { data: JSON.stringify({
    type: "registered", agent_id: "worker", instance_id: client.instanceId, lease_id: "lease-1",
  }) }));
  await connected;
  for (const controlId of ["control-1", "control-2"]) {
    socket.dispatchEvent(new MessageEvent("message", { data: JSON.stringify({
      type: "model_control",
      source_agent_id: "controller",
      control_id: controlId,
      action: "set_model",
      model: { provider: "provider", id: controlId },
    }) }));
  }
  await new Promise((resolve) => setImmediate(resolve));
  assert.deepEqual(started, ["control-1"]);
  resolvers.shift()();
  await new Promise((resolve) => setImmediate(resolve));
  assert.deepEqual(started, ["control-1", "control-2"]);
  resolvers.shift()();
  await new Promise((resolve) => setImmediate(resolve));
  assert.deepEqual(
    socket.sent.filter((frame) => frame.type === "model_control_result").map((frame) => frame.control_id),
    ["control-1", "control-2"],
  );
  assert.ok(traces.filter(({ event }) => event.startsWith("client_model_control_"))
    .every(({ instance_id, lease_id }) => instance_id === client.instanceId && lease_id === "lease-1"));
  client.close();
});

test("mailbox client re-checks queued model-control expiry before execution", async () => {
  const socket = new EventTarget();
  socket.readyState = 0;
  socket.sent = [];
  socket.send = (payload) => socket.sent.push(JSON.parse(payload));
  socket.close = () => { socket.readyState = 3; };
  let now = 0;
  const started = [];
  const traces = [];
  let finishFirst;
  const client = createMailboxClient({
    agentId: "worker",
    onEnvelope: () => {},
    onModelControl: ({ controlId, model }) => {
      started.push(controlId);
      if (controlId === "control-1") {
        return new Promise((resolve) => { finishFirst = () => resolve({ accepted: true, currentModel: model }); });
      }
      return Promise.resolve({ accepted: true, currentModel: model });
    },
    onModelControlTrace: (event) => traces.push(event),
    webSocketFactory: () => socket,
    fetchImpl: async () => ({ ok: true, json: async () => ({}) }),
    now: () => now,
  });
  const connected = client.connect();
  socket.readyState = 1;
  socket.dispatchEvent(new Event("open"));
  socket.dispatchEvent(new MessageEvent("message", { data: JSON.stringify({
    type: "registered", agent_id: "worker", instance_id: client.instanceId, lease_id: "lease-1",
  }) }));
  await connected;
  for (const [controlId, expiresAt] of [["control-1", 100], ["control-2", 10]]) {
    socket.dispatchEvent(new MessageEvent("message", { data: JSON.stringify({
      type: "model_control",
      source_agent_id: "controller",
      control_id: controlId,
      action: "set_model",
      model: { provider: "provider", id: controlId },
      expires_at: new Date(expiresAt).toISOString(),
    }) }));
  }
  await new Promise((resolve) => setImmediate(resolve));
  assert.deepEqual(started, ["control-1"]);

  now = 20;
  finishFirst();
  await new Promise((resolve) => setImmediate(resolve));
  assert.deepEqual(started, ["control-1"]);
  assert.equal(socket.sent.some((frame) => frame.type === "model_control_result" && frame.control_id === "control-2"), false);
  assert.ok(traces.some(({ event, control_id, reason }) => event === "client_model_control_dropped"
    && control_id === "control-2" && reason === "expired"));
  client.close();
});

test("mailbox client contains async model-control result failures after disconnect", async () => {
  const socket = new EventTarget();
  socket.readyState = 0;
  socket.sent = [];
  socket.send = (payload) => socket.sent.push(JSON.parse(payload));
  socket.close = () => { socket.readyState = 3; };
  const traces = [];
  let finish;
  const client = createMailboxClient({
    agentId: "worker",
    onEnvelope: () => {},
    onModelControl: () => new Promise((resolve) => { finish = resolve; }),
    onModelControlTrace: (event) => traces.push(event),
    webSocketFactory: () => socket,
    fetchImpl: async () => ({ ok: true, json: async () => ({}) }),
    setTimeoutImpl: () => 1,
    clearTimeoutImpl: () => {},
  });
  const connected = client.connect();
  socket.readyState = 1;
  socket.dispatchEvent(new Event("open"));
  socket.dispatchEvent(new MessageEvent("message", { data: JSON.stringify({
    type: "registered", agent_id: "worker", instance_id: client.instanceId, lease_id: "lease-1",
  }) }));
  await connected;
  socket.dispatchEvent(new MessageEvent("message", { data: JSON.stringify({
    type: "model_control",
    source_agent_id: "controller",
    control_id: "control-disconnect",
    action: "set_model",
    model: { provider: "provider", id: "target" },
  }) }));
  await new Promise((resolve) => setImmediate(resolve));
  socket.readyState = 3;
  socket.dispatchEvent(new Event("close"));
  finish({ accepted: true, currentModel: { provider: "provider", id: "target" } });
  await new Promise((resolve) => setImmediate(resolve));

  assert.equal(socket.sent.some((frame) => frame.type === "model_control_result"), false);
  assert.ok(traces.some(({ event, reason }) => event === "client_model_control_result_failed" && reason === "stale_connection"));
  client.close();
});

test("mailbox client drops queued model controls when their connection closes", async () => {
  const socket = new EventTarget();
  socket.readyState = 0;
  socket.sent = [];
  socket.send = (payload) => socket.sent.push(JSON.parse(payload));
  socket.close = () => { socket.readyState = 3; };
  const started = [];
  const traces = [];
  let finishFirst;
  const client = createMailboxClient({
    agentId: "worker",
    onEnvelope: () => {},
    onModelControl: ({ controlId, model }) => {
      started.push(controlId);
      if (controlId === "control-1") {
        return new Promise((resolve) => { finishFirst = () => resolve({ accepted: true, currentModel: model }); });
      }
      return Promise.resolve({ accepted: true, currentModel: model });
    },
    onModelControlTrace: (event) => traces.push(event),
    webSocketFactory: () => socket,
    fetchImpl: async () => ({ ok: true, json: async () => ({}) }),
    setTimeoutImpl: () => 1,
    clearTimeoutImpl: () => {},
  });
  const connected = client.connect();
  socket.readyState = 1;
  socket.dispatchEvent(new Event("open"));
  socket.dispatchEvent(new MessageEvent("message", { data: JSON.stringify({
    type: "registered", agent_id: "worker", instance_id: client.instanceId, lease_id: "lease-1",
  }) }));
  await connected;
  for (const controlId of ["control-1", "control-2"]) {
    socket.dispatchEvent(new MessageEvent("message", { data: JSON.stringify({
      type: "model_control",
      source_agent_id: "controller",
      control_id: controlId,
      action: "set_model",
      model: { provider: "provider", id: controlId },
    }) }));
  }
  await new Promise((resolve) => setImmediate(resolve));
  assert.deepEqual(started, ["control-1"]);
  socket.readyState = 3;
  socket.dispatchEvent(new Event("close"));
  finishFirst();
  await new Promise((resolve) => setImmediate(resolve));
  assert.deepEqual(started, ["control-1"]);
  assert.ok(traces.some(({ event, control_id, reason }) => event === "client_model_control_dropped"
    && control_id === "control-2" && reason === "disconnected"));
  client.close();
});

test("model control preserves protocol limits and returns exact failure codes", async () => {
  let modelControl;
  let getModelCatalog;
  const provider = "p".repeat(128);
  const current = { provider, id: "c".repeat(256), name: "C".repeat(256) };
  const target = { provider, id: "t".repeat(256), name: "T".repeat(256) };
  const fixture = createMessagingFixture({
    model: current,
    availableModels: [current, target],
    createClient: (options) => {
      modelControl = options.onModelControl;
      getModelCatalog = options.getModelCatalog;
      return { connect: async () => {}, close() {}, sendReceipt() {}, sendActivity() {} };
    },
  });
  await sessionStart(fixture);

  assert.deepEqual(getModelCatalog(), [current, target]);
  assert.equal((await modelControl({ action: "set_model", model: target })).accepted, true);
  assert.equal((await modelControl({ action: "unknown", model: target })).errorCode, "unsupported_model_control");
  assert.equal((await modelControl({ action: "set_model", model: { provider, id: "x".repeat(257) } })).errorCode, "invalid_model");
  assert.equal((await modelControl({ action: "set_model", model: { provider, id: "missing" } })).errorCode, "model_unavailable");

  fixture.context.modelRegistry = undefined;
  assert.equal((await modelControl({ action: "set_model", model: target })).errorCode, "model_registry_unavailable");
  fixture.context.modelRegistry = { getAvailable() { throw new Error("registry failed"); } };
  assert.equal((await modelControl({ action: "set_model", model: target })).errorCode, "model_registry_failed");
  assert.throws(() => getModelCatalog(), /registry failed/);
  fixture.context.modelRegistry = { getAvailable: () => ({ malformed: true }) };
  assert.equal((await modelControl({ action: "set_model", model: target })).errorCode, "model_registry_unavailable");

  fixture.context.modelRegistry = { getAvailable: () => [current, target] };
  fixture.pi.setModel = () => false;
  assert.equal((await modelControl({ action: "set_model", model: target })).errorCode, "model_switch_rejected");
  fixture.pi.setModel = () => { throw new Error("switch failed"); };
  assert.equal((await modelControl({ action: "set_model", model: target })).errorCode, "model_switch_failed");
  delete fixture.pi.setModel;
  assert.equal((await modelControl({ action: "set_model", model: target })).errorCode, "model_switch_unavailable");
});

test("model control reconciles a Pi model mutation that throws afterward", async () => {
  let modelControl;
  const current = { provider: "provider", id: "current" };
  const target = { provider: "provider", id: "target" };
  const fixture = createMessagingFixture({
    model: current,
    availableModels: [current, target],
    setModel: (_model, context) => {
      context.model = target;
      throw new Error("switch reported failure after mutation");
    },
    createClient: (options) => {
      modelControl = options.onModelControl;
      return { connect: async () => {}, close() {}, sendReceipt() {}, sendActivity() {} };
    },
  });
  await sessionStart(fixture);

  assert.deepEqual(await modelControl({ action: "set_model", model: target }), {
    accepted: true,
    currentModel: target,
  });
});

test("model switch override yields to reflected and later local model context", async () => {
  let modelControl;
  const activity = [];
  const current = { provider: "provider", id: "current" };
  const target = { provider: "provider", id: "target" };
  const local = { provider: "provider", id: "local" };
  const fixture = createMessagingFixture({
    model: current,
    availableModels: [current, target, local],
    updateContextModel: false,
    createClient: (options) => {
      modelControl = options.onModelControl;
      return {
        connect: async () => {},
        close() {},
        sendReceipt() {},
        sendActivity: (...args) => activity.push(args),
      };
    },
  });
  await sessionStart(fixture);
  await modelControl({ action: "set_model", model: target });
  assert.deepEqual(activity.at(-1)[1].currentModel, target);

  fixture.context.model = target;
  for (const handler of fixture.handlers.get("agent_start") ?? []) await handler({}, fixture.context);
  fixture.context.model = local;
  for (const handler of fixture.handlers.get("agent_start") ?? []) await handler({}, fixture.context);
  assert.deepEqual(activity.at(-1)[1].currentModel, local);

  await modelControl({ action: "set_model", model: target });
  const freshContext = fixture.createContext();
  freshContext.model = local;
  for (const handler of fixture.handlers.get("agent_start") ?? []) await handler({}, freshContext);
  assert.deepEqual(activity.at(-1)[1].currentModel, local);
});

test("mecha_mailbox_send is registered with the recipient, body, and subject schema", () => {
  const fixture = createMessagingFixture();
  const tool = fixture.tools.get("mecha_mailbox_send");

  assert.ok(tool);
  assert.equal(tool.parameters.type, "object");
  assert.equal(tool.parameters.additionalProperties, false);
  assert.deepEqual(tool.parameters.required, ["recipient", "body"]);
  assert.equal(tool.parameters.properties.recipient.type, "string");
  assert.equal(tool.parameters.properties.body.type, "string");
  assert.equal(tool.parameters.properties.subject.type, "string");
  assert.equal(Object.hasOwn(tool.parameters.properties, "from"), false);
});

test("mecha-reply invokes the registered slash-command entrypoint", async () => {
  const calls = [];
  const fixture = createMessagingFixture({
    client: {
      connect: async () => {}, close() {}, sendReceipt() {}, sendActivity() {},
      sendReply: async (message) => { calls.push(message); return { id: "message-1", state: "queued" }; },
    },
  });
  await sessionStart(fixture);

  await fixture.commands.get("mecha-reply").handler("recipient hello from the slash command", fixture.context);

  assert.deepEqual(calls, [{ to: "recipient", body: "hello from the slash command" }]);
});

test("mecha_mailbox_send rejects a caller-supplied sender before using the Mailbox client", async () => {
  const calls = [];
  const fixture = createMessagingFixture({
    client: {
      connect: async () => {}, close() {}, sendReceipt() {}, sendActivity() {},
      sendReply: async (message) => { calls.push(message); return { id: "message-1", state: "queued" }; },
    },
  });
  await sessionStart(fixture);

  const result = await fixture.tools.get("mecha_mailbox_send").execute(
    "call-1",
    { recipient: "worker", body: "hello", from: "spoofed-sender" },
    undefined,
    undefined,
    fixture.context,
  );

  assert.equal(result.isError, true);
  assert.match(result.content[0].text, /sender is derived from the current tmux window/i);
  assert.deepEqual(calls, []);
});

test("mecha_mailbox_send rejects an inherited caller-supplied sender before using the Mailbox client", async () => {
  const calls = [];
  const fixture = createMessagingFixture({
    client: {
      connect: async () => {}, close() {}, sendReceipt() {}, sendActivity() {},
      sendReply: async (message) => { calls.push(message); return { id: "message-1", state: "queued" }; },
    },
  });
  await sessionStart(fixture);
  const input = Object.create({ from: "prototype-spoofed-sender" });
  input.recipient = "worker";
  input.body = "hello";

  const result = await fixture.tools.get("mecha_mailbox_send").execute(
    "call-1",
    input,
    undefined,
    undefined,
    fixture.context,
  );

  assert.equal(result.isError, true);
  assert.match(result.content[0].text, /sender is derived from the current tmux window/i);
  assert.deepEqual(calls, []);
});

test("mecha_mailbox_send validates input before using the Mailbox client", async () => {
  const calls = [];
  const fixture = createMessagingFixture({
    client: {
      connect: async () => {}, close() {}, sendReceipt() {}, sendActivity() {},
      sendReply: async (message) => { calls.push(message); return { id: "message-1", state: "queued" }; },
    },
  });
  await sessionStart(fixture);
  const tool = fixture.tools.get("mecha_mailbox_send");

  for (const [input, expected] of [
    [{ recipient: "", body: "hello" }, /recipient is required/],
    [{ recipient: "worker@example.com", body: "hello" }, /identity is invalid/],
    [{ recipient: "worker", body: "" }, /message body is required/],
    [{ recipient: "worker", body: 42 }, /message body is required/],
    [{ recipient: "worker", body: "hello", subject: 42 }, /subject must be a string/],
  ]) {
    const result = await tool.execute("call-1", input, undefined, undefined, fixture.context);
    assert.equal(result.isError, true);
    assert.match(result.content[0].text, expected);
  }
  assert.deepEqual(calls, []);
});

test("mecha_mailbox_send sends through the active client and returns acceptance evidence", async () => {
  const calls = [];
  const fixture = createMessagingFixture({
    client: {
      connect: async () => {}, close() {}, sendReceipt() {}, sendActivity() {},
      sendReply: async (message) => { calls.push(message); return { id: "message-1", state: "acknowledged" }; },
    },
  });
  await sessionStart(fixture);
  const result = await fixture.tools.get("mecha_mailbox_send").execute(
    "call-1",
    { recipient: "worker", body: "hello", subject: "Subject" },
    undefined,
    undefined,
    fixture.context,
  );

  assert.match(result.content[0].text, /Mailbox message accepted \(message-1, acknowledged\)/);
  assert.deepEqual(result.details, { message_id: "message-1", state: "acknowledged" });
  assert.deepEqual(calls, [{ to: "worker", body: "hello", subject: "Subject" }]);
});

test("mecha_mailbox_send limits the initialized parent sender-target pair to six sends per rolling minute", async () => {
  let now = 0;
  let sender = "worker";
  const calls = [];
  const fixture = createMessagingFixture({
    nowImpl: () => now,
    resolveCurrentAgentId: () => sender,
    client: {
      connect: async () => {}, close() {}, sendReceipt() {}, sendActivity() {},
      sendReply: async (message) => {
        calls.push(message);
        return { id: `message-${calls.length}`, state: "acknowledged" };
      },
    },
  });
  await sessionStart(fixture);
  const tool = fixture.tools.get("mecha_mailbox_send");
  for (let index = 0; index < 6; index += 1) {
    const result = await tool.execute(`call-${index}`, { recipient: "worker", body: `hello-${index}` }, undefined, undefined, fixture.context);
    assert.equal(result.isError, undefined);
  }
  const denied = await tool.execute("call-6", { recipient: "worker", body: "blocked" }, undefined, undefined, fixture.context);
  assert.equal(denied.isError, true);
  assert.match(denied.content[0].text, /temporary.*rate limit/i);
  assert.match(denied.content[0].text, /60 seconds/i);
  assert.equal(calls.length, 6);

  now = 59_999;
  const stillWithinWindow = await tool.execute("call-7", { recipient: "worker", body: "still blocked" }, undefined, undefined, fixture.context);
  assert.equal(stillWithinWindow.isError, true);

  sender = "other-worker";
  const stillDenied = await tool.execute("call-8", { recipient: "worker", body: "stable sender" }, undefined, undefined, fixture.context);
  assert.equal(stillDenied.isError, true);
  const otherTarget = await tool.execute("call-9", { recipient: "other-worker", body: "isolated target" }, undefined, undefined, fixture.context);
  assert.equal(otherTarget.isError, undefined);
  now = 60_000;
  const afterWindow = await tool.execute("call-10", { recipient: "worker", body: "window reset" }, undefined, undefined, fixture.context);
  assert.equal(afterWindow.isError, undefined);
});

test("mecha_mailbox_send rolls back the exact reservation when identical timestamps collide", async () => {
  let firstReject;
  let callCount = 0;
  const fixture = createMessagingFixture({
    nowImpl: () => 0,
    client: {
      connect: async () => {}, close() {}, sendReceipt() {}, sendActivity() {},
      sendReply: async () => {
        callCount += 1;
        if (callCount === 1) return new Promise((resolve, reject) => { firstReject = reject; });
        if (callCount === 2) throw new Error("second send failed");
        return { id: `message-${callCount}`, state: "acknowledged" };
      },
    },
  });
  await sessionStart(fixture);
  const tool = fixture.tools.get("mecha_mailbox_send");

  const first = tool.execute("call-first", { recipient: "worker", body: "first" }, undefined, undefined, fixture.context);
  await Promise.resolve();
  const second = await tool.execute("call-second", { recipient: "worker", body: "second" }, undefined, undefined, fixture.context);
  assert.equal(second.isError, true);
  assert.match(second.content[0].text, /second send failed/);

  firstReject(new Error("first send failed"));
  const firstResult = await first;
  assert.equal(firstResult.isError, true);
  assert.match(firstResult.content[0].text, /first send failed/);

  for (let index = 0; index < 6; index += 1) {
    const result = await tool.execute(`call-retry-${index}`, { recipient: "worker", body: `retry-${index}` }, undefined, undefined, fixture.context);
    assert.equal(result.isError, undefined);
  }
  const denied = await tool.execute("call-denied", { recipient: "worker", body: "blocked" }, undefined, undefined, fixture.context);
  assert.equal(denied.isError, true);
  assert.match(denied.content[0].text, /temporary.*rate limit/i);
});

test("mecha_mailbox_send releases a reservation when sendReply fails", async () => {
  let fail = true;
  const fixture = createMessagingFixture({
    client: {
      connect: async () => {}, close() {}, sendReceipt() {}, sendActivity() {},
      sendReply: async () => {
        if (fail) {
          fail = false;
          throw new Error("router unavailable");
        }
        return { id: "message-1", state: "acknowledged" };
      },
    },
  });
  await sessionStart(fixture);
  const tool = fixture.tools.get("mecha_mailbox_send");

  const failed = await tool.execute("call-failed", { recipient: "worker", body: "retry me" }, undefined, undefined, fixture.context);
  assert.equal(failed.isError, true);
  assert.match(failed.content[0].text, /router unavailable/);

  for (let index = 0; index < 6; index += 1) {
    const result = await tool.execute(`call-${index}`, { recipient: "worker", body: `hello-${index}` }, undefined, undefined, fixture.context);
    assert.equal(result.isError, undefined);
  }
  const denied = await tool.execute("call-denied", { recipient: "worker", body: "blocked" }, undefined, undefined, fixture.context);
  assert.equal(denied.isError, true);
  assert.match(denied.content[0].text, /temporary.*rate limit/i);
});

test("dev-only deterministic proof: mecha_mailbox_send rate limit uses the stable parent sender", async () => {
  const extensionPath = fileURLToPath(new URL("../pi-extensions/mecha-messaging/index.ts", import.meta.url));
  assert.equal(extensionPath.endsWith("/pi-extensions/mecha-messaging/index.ts"), true);

  let now = 0;
  let sender = "sender-a";
  let capturedAgentId;
  const calls = [];
  const fixture = createMessagingFixture({
    nowImpl: () => now,
    resolveCurrentAgentId: () => sender,
    createClient: (options) => {
      capturedAgentId = options.agentId;
      return {
        connect: async () => {}, close() {}, sendReceipt() {}, sendActivity() {},
        sendReply: async ({ to, body }) => {
          calls.push({ from: options.agentId, to, body });
          return { id: `message-${calls.length}`, state: "acknowledged" };
        },
      };
    },
  });
  await sessionStart(fixture);
  const tool = fixture.tools.get("mecha_mailbox_send");

  for (let index = 0; index < 6; index += 1) {
    const result = await tool.execute(`call-${index}`, { recipient: "target-b", body: `hello-${index}` }, undefined, undefined, fixture.context);
    assert.equal(result.isError, undefined);
  }
  const denied = await tool.execute("call-6", { recipient: "target-b", body: "blocked" }, undefined, undefined, fixture.context);
  assert.equal(denied.isError, true);
  assert.match(denied.content[0].text, /temporary.*rate limit/i);

  const otherTarget = await tool.execute("call-7", { recipient: "target-c", body: "isolated target" }, undefined, undefined, fixture.context);
  assert.equal(otherTarget.isError, undefined);
  sender = "sender-d";
  const stillDenied = await tool.execute("call-8", { recipient: "target-b", body: "stable sender" }, undefined, undefined, fixture.context);
  assert.equal(stillDenied.isError, true);

  now = 60_000;
  const afterWindow = await tool.execute("call-9", { recipient: "target-b", body: "window reset" }, undefined, undefined, fixture.context);
  assert.equal(afterWindow.isError, undefined);
  assert.equal(calls.length, 8);
  assert.equal(capturedAgentId, "sender-a");
  assert.ok(calls.every((envelope) => envelope.from === "sender-a"));
});

test("mecha_mailbox_send quota resets for a new parent session", async () => {
  let now = 0;
  const calls = [];
  const fixture = createMessagingFixture({
    nowImpl: () => now,
    client: {
      connect: async () => {}, close() {}, sendReceipt() {}, sendActivity() {},
      sendReply: async (message) => {
        calls.push(message);
        return { id: `message-${calls.length}`, state: "acknowledged" };
      },
    },
  });
  await sessionStart(fixture);
  const tool = fixture.tools.get("mecha_mailbox_send");
  for (let index = 0; index < 6; index += 1) {
    await tool.execute(`call-${index}`, { recipient: "worker", body: `hello-${index}` }, undefined, undefined, fixture.context);
  }
  const denied = await tool.execute("call-6", { recipient: "worker", body: "blocked" }, undefined, undefined, fixture.context);
  assert.equal(denied.isError, true);

  await sessionStart(fixture, fixture.createContext("new-parent-session"));
  const granted = await tool.execute("call-7", { recipient: "worker", body: "new session" }, undefined, undefined, fixture.createContext("new-parent-session"));
  assert.equal(granted.isError, undefined);
  assert.equal(calls.length, 7);
});

test("mecha_mailbox_send reaches the real Mailbox V2 HTTP client path with the stable parent sender", async () => {
  const requests = [];
  let sender = "stable-parent";
  let client;
  const socket = {
    readyState: 1,
    addEventListener(type, handler) { if (type === "open") queueMicrotask(handler); },
    send() {},
    close() {},
  };
  const fixture = createMessagingFixture({
    resolveCurrentAgentId: () => sender,
    createClient: (options) => {
      client = createMailboxClient({
        ...options,
        routerUrl: "http://127.0.0.1:8765",
        webSocketFactory: () => socket,
        generateId: () => "request-id",
        fetchImpl: async (url, request) => {
          requests.push({ url, request });
          return {
            ok: true,
            status: 201,
            json: async () => ({ message: { id: "router-message", state: "leased" } }),
          };
        },
      });
      return client;
    },
  });

  await sessionStart(fixture);
  sender = "changed-after-session-start";
  const result = await fixture.tools.get("mecha_mailbox_send").execute("call-1", { recipient: "worker", body: "hello" }, undefined, undefined, fixture.context);
  assert.deepEqual(result.details, { message_id: "router-message", state: "leased" });
  assert.equal(requests.length, 1);
  assert.equal(requests[0].url, "http://127.0.0.1:8765/v1/messages");
  const envelope = JSON.parse(requests[0].request.body);
  assert.deepEqual({ ...envelope, created_at: undefined }, {
    id: "request-id",
    from: "stable-parent",
    to: "worker",
    subject: "Pi mailbox reply",
    body: "hello",
    created_at: undefined,
    source: { kind: "agent" },
  });
  client.close();
});

test("mecha_mailbox_send uses an initialized child client and never falls back to the parent", async () => {
  const calls = [];
  const parentClient = {
    connect: async () => {}, close() {}, sendReceipt() {}, sendActivity() {},
    sendReply: async () => { calls.push("parent"); return { id: "parent", state: "queued" }; },
  };
  const childClient = {
    connect: async () => {}, close() {}, sendReceipt() {}, sendActivity() {},
    sendReply: async () => { calls.push("child"); return { id: "child", state: "queued" }; },
  };
  let createCount = 0;
  const fixture = createMessagingFixture({ createClient: () => createCount++ === 0 ? parentClient : childClient });
  await sessionStart(fixture);
  fixture.childHandlers.get("subagents:child:session-created")({ sessionId: "child-session" });
  const childContext = fixture.createContext("child-session");
  await sessionStart(fixture, childContext);

  const result = await fixture.tools.get("mecha_mailbox_send").execute("call-1", { recipient: "worker", body: "from child" }, undefined, undefined, childContext);
  assert.equal(result.details.message_id, "child");
  assert.deepEqual(calls, ["child"]);
});

test("mecha_mailbox_send rejects a disposed native child without touching either client", async () => {
  const calls = [];
  const parentClient = {
    connect: async () => {}, close() {}, sendReceipt() {}, sendActivity() {},
    sendReply: async () => { calls.push("parent"); return { id: "parent", state: "queued" }; },
  };
  const childClient = {
    connect: async () => {}, close() {}, sendReceipt() {}, sendActivity() {},
    sendReply: async () => { calls.push("child"); return { id: "child", state: "queued" }; },
  };
  let createCount = 0;
  const fixture = createMessagingFixture({ createClient: () => createCount++ === 0 ? parentClient : childClient });
  await sessionStart(fixture);
  fixture.childHandlers.get("subagents:child:session-created")({ sessionId: "child-session" });
  const childContext = fixture.createContext("child-session");
  await sessionStart(fixture, childContext);
  fixture.childHandlers.get("subagents:child:disposed")({ sessionId: "child-session" });

  const result = await fixture.tools.get("mecha_mailbox_send").execute("call-1", { recipient: "worker", body: "from disposed child" }, undefined, undefined, childContext);
  assert.equal(result.isError, true);
  assert.match(result.content[0].text, /native child/i);
  assert.deepEqual(calls, []);
});

test("disposed child shutdown skips parent capture cleanup", async () => {
  let capturePolicyFetches = 0;
  const originalFetch = globalThis.fetch;
  globalThis.fetch = async (url) => {
    if (String(url).endsWith("/v1/memory/policy")) capturePolicyFetches += 1;
    return {
      ok: true,
      json: async () => ({ recall: { capture: { router_timeout_ms: 5_000 } } }),
    };
  };
  try {
    const parentClient = {
      connect: async () => {}, close() {}, sendReceipt() {}, sendActivity() {},
      sendReply: async () => ({ id: "parent", state: "queued" }),
    };
    const childClient = {
      connect: async () => {}, close() {}, sendReceipt() {}, sendActivity() {},
      sendReply: async () => ({ id: "child", state: "queued" }),
    };
    let createCount = 0;
    const fixture = createMessagingFixture({ createClient: () => createCount++ === 0 ? parentClient : childClient });
    await sessionStart(fixture);
    fixture.childHandlers.get("subagents:child:session-created")({ sessionId: "child-session" });
    const childContext = fixture.createContext("child-session");
    await sessionStart(fixture, childContext);
    fixture.childHandlers.get("subagents:child:disposed")({ sessionId: "child-session" });

    for (const handler of fixture.handlers.get("session_shutdown") ?? []) await handler({}, childContext);

    assert.equal(capturePolicyFetches, 0);
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test("mecha_parent_message remains child-only", async () => {
  const fixture = createMessagingFixture();
  await sessionStart(fixture);
  const result = await fixture.tools.get("mecha_parent_message").execute("call-1", { message: "from parent" }, undefined, undefined, fixture.context);
  assert.equal(result.isError, true);
  assert.match(result.content[0].text, /unavailable/i);
});

test("control-plane and unknown tools remain allowed", async () => {
  const fixture = createMessagingFixture();
  const results = await emitToolCalls(fixture, [
    "mecha_context",
    "mecha_parent_message",
    "mecha_recall",
    "mecha_settled_notifications",
    INLINE_TOOL_ALLOWANCE_TOOL_NAME,
    "unknown_tool",
  ]);

  assert.deepEqual(results, Array.from({ length: 6 }, () => undefined));
});

test("forked sessions with parentSession headers are still guarded as top-level sessions", async () => {
  const fixture = createMessagingFixture({ header: { parentSession: "/tmp/parent-session.jsonl" } });
  const [result] = await emitToolCalls(fixture, ["bash"]);

  assert.equal(result.block, true);
});

test("native child sessions bypass the inline tool guard", async () => {
  const fixture = createMessagingFixture();
  fixture.childHandlers.get("subagents:child:session-created")({ sessionId: "child-session" });
  try {
    const childContext = {
      sessionManager: { getSessionId: () => "child-session", getHeader: () => ({}) },
      ui: { notify() {}, setStatus() {} },
    };
    assert.deepEqual(await emitToolCalls(fixture, ["bash", "edit", "write"], childContext), [undefined, undefined, undefined]);
  } finally {
    fixture.childHandlers.get("subagents:child:disposed")({ sessionId: "child-session" });
  }
});

test("native child calls do not spend parent allowance credits", async () => {
  const fixture = createMessagingFixture();
  const allowanceTool = fixture.tools.get(INLINE_TOOL_ALLOWANCE_TOOL_NAME);
  await allowanceTool.execute("call-1", { toolName: "edit", usageCount: 1 });
  fixture.childHandlers.get("subagents:child:session-created")({ sessionId: "child-session" });
  try {
    const childContext = {
      sessionManager: { getSessionId: () => "child-session", getHeader: () => ({}) },
      ui: { notify() {}, setStatus() {} },
    };
    await emitToolCalls(fixture, ["edit"], childContext);
    assert.deepEqual(await emitToolCalls(fixture, ["edit"]), [undefined]);
    const [blocked] = await emitToolCalls(fixture, ["edit"]);
    assertBlocked(blocked);
  } finally {
    fixture.childHandlers.get("subagents:child:disposed")({ sessionId: "child-session" });
  }
});

test("native child allowance requests do not change parent allowance state", async () => {
  const fixture = createMessagingFixture();
  const allowanceTool = fixture.tools.get(INLINE_TOOL_ALLOWANCE_TOOL_NAME);
  fixture.childHandlers.get("subagents:child:session-created")({ sessionId: "child-session" });
  try {
    const childContext = {
      sessionManager: { getSessionId: () => "child-session", getHeader: () => ({}) },
      ui: { notify() {}, setStatus() {} },
    };
    const result = await allowanceTool.execute("call-1", { toolName: "edit", usageCount: 1 }, undefined, undefined, childContext);
    assert.match(result.content[0].text, /exempt/i);
    const [blocked] = await emitToolCalls(fixture, ["edit"]);
    assertBlocked(blocked);
  } finally {
    fixture.childHandlers.get("subagents:child:disposed")({ sessionId: "child-session" });
  }
});

test("checkout-source Pi extension executes operational actions without installed artifact copies", async () => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-extension-"));
  const requests = [];
  const server = createServer((request, response) => {
    requests.push([request.method, request.url]);
    response.setHeader("content-type", "application/json");
    if (request.method === "POST" && request.url === "/v1/messages") {
      response.statusCode = 201;
      response.end(JSON.stringify({ message: { id: "standalone-message", state: "queued" } }));
      return;
    }
    if (request.method === "GET" && request.url === "/v1/tmux/windows") {
      response.end(JSON.stringify({ windows: [] }));
      return;
    }
    if (request.method === "GET" && request.url === "/v1/reminders") {
      response.end(JSON.stringify({ reminders: [] }));
      return;
    }
    if (request.method === "POST" && request.url === "/v1/reminders") {
      response.statusCode = 201;
      response.end(JSON.stringify({ reminder: { id: "standalone-reminder", state: "active" } }));
      return;
    }
    if (request.method === "GET" && request.url === "/v1/health") {
      response.end(JSON.stringify({ healthy: true }));
      return;
    }
    if (request.method === "POST" && request.url === "/v1/messages/standalone-message/retry") {
      response.end(JSON.stringify({ message: { id: "standalone-message", state: "queued" } }));
      return;
    }
    if (request.method === "DELETE" && request.url === "/v1/messages/standalone-message") {
      response.statusCode = 204;
      response.end();
      return;
    }
    response.statusCode = 404;
    response.end(JSON.stringify({ error: { message: "not found" } }));
  });
  server.listen(0, "127.0.0.1");
  await once(server, "listening");
  try {
    const extension = fileURLToPath(new URL("../pi-extensions/mecha-messaging/index.ts", import.meta.url));
    const checkoutSupervisorTools = fileURLToPath(new URL("../.claude/skills/supervisor/tools", import.meta.url));
    await import(pathToFileURL(extension).href);
    await execFileAsync("python3", [
      "-c",
      [
        "import _cluster, _sidecar, _state, _tmux",
        "import git_task_status, mailbox_send, pr_status, state_add, state_priority",
        "import state_query, state_update_field, state_update_status, state_verification_handoff",
        "import worker_list",
      ].join("; "),
    ], {
      cwd: checkoutSupervisorTools,
      env: { ...process.env, HOME: directory, PYTHONDONTWRITEBYTECODE: "1" },
    });

    const fakeBin = join(directory, "bin");
    await mkdir(fakeBin);
    const tmux = join(fakeBin, "tmux");
    await writeFile(tmux, "#!/bin/sh\nif [ \"$1\" = \"display-message\" ]; then printf 'standalone-worker\\n'; exit 0; fi\nexit 1\n");
    await chmod(tmux, 0o755);
    const gh = join(fakeBin, "gh");
    await writeFile(gh, "#!/bin/sh\nif [ \"$1 $2\" = \"pr view\" ]; then printf '%s\\n' '{\"number\":42,\"url\":\"https://example.test/pull/42\",\"title\":\"Standalone\",\"state\":\"OPEN\",\"isDraft\":false,\"headRefName\":\"feature\",\"headRefOid\":\"abcdef123456\",\"baseRefName\":\"main\",\"mergeStateStatus\":\"CLEAN\",\"reviewDecision\":\"\"}'; else printf '%s\\n' '[{\"name\":\"tests\",\"state\":\"SUCCESS\"}]'; fi\n");
    await chmod(gh, 0o755);

    const repository = join(directory, "repository");
    await mkdir(repository);
    await execFileAsync("git", ["-C", repository, "init", "--quiet"]);
    const statePath = join(directory, "supervisor-state.json");
    await writeFile(statePath, JSON.stringify({
      next_task_id: 2,
      standing_orders: [],
      tasks: [{ id: 1, alias: "git-task", state: "active", type: "worker", project: "standalone", worktree: repository, description: "Git fixture" }],
    }));
    const address = server.address();
    const environment = {
      ...process.env,
      PATH: `${fakeBin}:${process.env.PATH}`,
      MECHA_AGENT_ID: "standalone-supervisor",
      MECHA_ROUTER_URL: `http://127.0.0.1:${address.port}`,
      MECHA_STATE_FILE: statePath,
      TMUX: "standalone",
      TMUX_PANE: "%1",
    };

    assert.equal(existsSync(join(directory, ".mecha-developer", "extensions")), false);
    const module = await import(pathToFileURL(extension).href);
    const tools = new Map();
    const pi = {
      events: { on() {} },
      on() {},
      registerCommand() {},
      registerTool(tool) { tools.set(tool.name, tool); },
      sendMessage() {},
    };
    module.createMechaMessagingExtension(pi, {
      resolveCurrentAgentId: () => "worker",
      createClient: () => ({ connect: async () => {}, close() {}, sendReceipt() {}, sendActivity() {} }),
      createSettledScheduler: () => ({ onParentActivity() {}, onParentSettled() {}, onChildStarted() {}, onChildDisposed() {}, dispose() {} }),
      operationalTools: { environment },
    });
    const execute = (name, input) => tools.get(name).execute("standalone-call", input);
    const executedToolNames = new Set();
    for (const [name, input] of [
      ["mecha_state", { action: "create", alias: "state-task", type: "worker", project: "standalone", description: "State fixture" }],
      ["mecha_state", { action: "query", task: "state-task" }],
      ["mecha_priority", { action: "create", priorityId: "standalone-priority", name: "Standalone priority", taskIds: [1] }],
      ["mecha_verification_handoff", { action: "record", task: "git-task", reminderId: "standalone-reminder", successor: "standalone-worker" }],
      ["mecha_worker", { action: "list" }],
      ["mecha_worker", { action: "send", worker: "recipient", message: "Standalone mailbox fixture" }],
      ["mecha_pr", { action: "status", pullRequest: 42 }],
      ["mecha_git", { action: "status", task: "git-task" }],
      ["mecha_reminder", { action: "list" }],
      ["mecha_reminder", { action: "create", target: "supervisor", message: "Standalone reminder fixture", delayMinutes: 5 }],
      ["mecha_router", { action: "health" }],
      ["mecha_router", { action: "retry", messageId: "standalone-message" }],
      ["mecha_router", { action: "dismiss", messageId: "standalone-message" }],
    ]) {
      const result = await execute(name, input);
      executedToolNames.add(name);
      assert.equal(result.isError, undefined, `${name}: ${result.content[0].text}`);
      assert.deepEqual(Object.keys(result).sort(), ["content", "details"]);
      assert.ok(Array.isArray(result.content));
      assert.equal(result.content[0].type, "text");
      assert.equal(typeof result.content[0].text, "string");
      assert.equal(typeof result.details, "object");
      assert.notEqual(result.details, null);
      assert.equal(Array.isArray(result.details), false);
    }
    assert.deepEqual([...executedToolNames], [
      "mecha_state",
      "mecha_priority",
      "mecha_verification_handoff",
      "mecha_worker",
      "mecha_pr",
      "mecha_git",
      "mecha_reminder",
      "mecha_router",
    ]);
    assert.deepEqual(requests, [
      ["GET", "/v1/tmux/windows"],
      ["POST", "/v1/messages"],
      ["GET", "/v1/reminders"],
      ["POST", "/v1/reminders"],
      ["GET", "/v1/health"],
      ["POST", "/v1/messages/standalone-message/retry"],
      ["DELETE", "/v1/messages/standalone-message"],
    ]);
    const client = module.createMailboxClient({ agentId: "worker", onEnvelope: () => {} });
    assert.match(client.instanceId, /^[A-Za-z0-9]{21}$/);
    client.close();
  } finally {
    server.close();
    await once(server, "close");
    await rm(directory, { recursive: true, force: true });
  }
});
