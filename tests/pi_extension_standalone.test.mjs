import assert from "node:assert/strict";
import test from "node:test";

import { copyFile, mkdir, mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { pathToFileURL } from "node:url";

import {
  createInlineToolPolicy,
  createMechaMessagingExtension,
  INLINE_TOOL_ALLOWANCE_TOOL_NAME,
} from "../pi-extensions/mecha-messaging.js";

function createMessagingFixture({ sendMessage = () => {}, sessionId = "parent-session", header = {}, contextUsage, nowImpl = Date.now } = {}) {
  const handlers = new Map();
  const childHandlers = new Map();
  const tools = new Map();
  const sent = [];
  const mailboxClients = [];
  const createContext = (currentSessionId = sessionId) => ({
    sessionManager: {
      getSessionId: () => currentSessionId,
      getHeader: () => header,
      getBranch: () => [],
      isPersisted: () => true,
      getSessionFile: () => `/tmp/${currentSessionId}.jsonl`,
    },
    ...(contextUsage === undefined ? {} : { getContextUsage: contextUsage }),
    ui: { notify() {}, setStatus() {} },
  });
  const context = createContext();
  const pi = {
    events: { on(type, handler) { childHandlers.set(type, handler); } },
    on(type, handler) {
      const bucket = handlers.get(type) ?? [];
      bucket.push(handler);
      handlers.set(type, bucket);
    },
    registerCommand() {},
    registerTool(tool) { tools.set(tool.name, tool); },
    sendMessage: (...args) => {
      sent.push(args);
      sendMessage(...args);
    },
    exec: async () => ({ code: 0, stderr: "" }),
  };
  createMechaMessagingExtension(pi, {
    resolveCurrentAgentId: () => "worker",
    createClient: (options) => {
      const client = {
        connect: async () => {},
        close() {},
        sendReceipt() {},
        sendActivity() {},
        onEnvelope: options.onEnvelope,
      };
      mailboxClients.push(client);
      return client;
    },
    nowImpl,
    createSettledScheduler: () => ({
      onParentActivity() {},
      onParentSettled() {},
      onChildStarted() {},
      onChildDisposed() {},
      dispose() {},
    }),
  });
  return { childHandlers, context, createContext, handlers, mailboxClients, sent, tools };
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
  assert.deepEqual(threshold[1], { deliverAs: "followUp", triggerTurn: true });
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

test("copied Pi extension loads without repository dependencies", async () => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-extension-"));
  try {
    const extensionDirectory = join(directory, "pi-extensions");
    await mkdir(extensionDirectory);
    const extension = join(extensionDirectory, "mecha-messaging.js");
    await copyFile(new URL("../pi-extensions/mecha-messaging.js", import.meta.url), extension);
    await copyFile(new URL("../pi-extensions/mailbox-client.js", import.meta.url), join(extensionDirectory, "mailbox-client.js"));
    for (const name of ["recall-state.js", "recall-controller.js", "recall-evaluator.js", "recall-pipeline.js", "memory-capture.js"]) {
      await copyFile(new URL(`../pi-extensions/${name}`, import.meta.url), join(extensionDirectory, name));
    }
    const module = await import(pathToFileURL(extension).href);
    const client = module.createMailboxClient({ agentId: "worker", onEnvelope: () => {} });
    assert.match(client.instanceId, /^[A-Za-z0-9]{21}$/);
    client.close();
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
});
