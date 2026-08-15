import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import { once } from "node:events";
import { chmod, mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
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
  INLINE_TOOL_ALLOWANCE_TOOL_NAME,
} from "../pi-extensions/mecha-messaging.js";
import { createMailboxClient } from "../pi-extensions/mailbox-client.js";

function createMessagingFixture({ sendMessage = () => {}, sessionId = "parent-session", header = {}, contextUsage, nowImpl = Date.now, client: configuredClient, createClient } = {}) {
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

async function sessionStart(fixture, context = fixture.context) {
  for (const handler of fixture.handlers.get("session_start") ?? []) await handler({}, context);
}

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

test("mecha_mailbox_send reaches the real Mailbox V2 HTTP client path", async () => {
  const requests = [];
  let client;
  const socket = {
    readyState: 1,
    addEventListener(type, handler) { if (type === "open") queueMicrotask(handler); },
    send() {},
    close() {},
  };
  const fixture = createMessagingFixture({
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
  const result = await fixture.tools.get("mecha_mailbox_send").execute("call-1", { recipient: "worker", body: "hello" }, undefined, undefined, fixture.context);
  assert.deepEqual(result.details, { message_id: "router-message", state: "leased" });
  assert.equal(requests.length, 1);
  assert.equal(requests[0].url, "http://127.0.0.1:8765/v1/messages");
  const envelope = JSON.parse(requests[0].request.body);
  assert.deepEqual({ ...envelope, created_at: undefined }, {
    id: "request-id",
    from: "worker",
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
    const extension = fileURLToPath(new URL("../pi-extensions/mecha-messaging.js", import.meta.url));
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
