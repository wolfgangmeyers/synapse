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

function createMessagingFixture({ sendMessage = () => {}, sessionId = "parent-session", header = {} } = {}) {
  const handlers = new Map();
  const childHandlers = new Map();
  const tools = new Map();
  const context = {
    sessionManager: { getSessionId: () => sessionId, getHeader: () => header },
    ui: { notify() {}, setStatus() {} },
  };
  const client = { connect: async () => {}, close() {}, sendReceipt() {}, sendActivity() {} };
  const pi = {
    events: { on(type, handler) { childHandlers.set(type, handler); } },
    on(type, handler) {
      const bucket = handlers.get(type) ?? [];
      bucket.push(handler);
      handlers.set(type, bucket);
    },
    registerCommand() {},
    registerTool(tool) { tools.set(tool.name, tool); },
    sendMessage,
    exec: async () => ({ code: 0, stderr: "" }),
  };
  createMechaMessagingExtension(pi, {
    resolveCurrentAgentId: () => "worker",
    createClient: () => client,
    createSettledScheduler: () => ({
      onParentActivity() {},
      onParentSettled() {},
      onChildStarted() {},
      onChildDisposed() {},
      dispose() {},
    }),
  });
  return { childHandlers, context, handlers, tools };
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

test("allowance credits spend down only on matching top-level calls", async () => {
  const fixture = createMessagingFixture();
  const allowanceTool = fixture.tools.get(INLINE_TOOL_ALLOWANCE_TOOL_NAME);
  await allowanceTool.execute("call-1", { toolName: "edit", usageCount: 2 });

  for (const blockedTool of await emitToolCalls(fixture, ["bash", "write"])) assertBlocked(blockedTool);
  assert.deepEqual(await emitToolCalls(fixture, ["edit", "edit"]), [undefined, undefined]);
  const [blockedEdit] = await emitToolCalls(fixture, ["edit"]);
  assertBlocked(blockedEdit);
});

test("allowance credits reset at agent start", async () => {
  const fixture = createMessagingFixture();
  const allowanceTool = fixture.tools.get(INLINE_TOOL_ALLOWANCE_TOOL_NAME);
  await allowanceTool.execute("call-1", { toolName: "edit", usageCount: 1 });
  for (const handler of fixture.handlers.get("agent_start") ?? []) await handler({}, fixture.context);

  const [result] = await emitToolCalls(fixture, ["edit"]);
  assert.equal(result.block, true);
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
