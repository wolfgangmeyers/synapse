import assert from "node:assert/strict";
import test from "node:test";

import { copyFile, mkdir, mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { pathToFileURL } from "node:url";

import {
  createInlineToolNudgeTracker,
  createMechaMessagingExtension,
  INLINE_TOOL_NUDGE_MESSAGE,
  INLINE_TOOL_NUDGE_MESSAGE_TYPE,
} from "../pi-extensions/mecha-messaging.js";

function createMessagingFixture({ sendMessage = () => {}, sessionId = "parent-session", header = {} } = {}) {
  const handlers = new Map();
  const childHandlers = new Map();
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
    registerTool() {},
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
  return { childHandlers, context, handlers };
}

async function emitToolCalls(fixture, toolNames, context = fixture.context) {
  for (const toolName of toolNames) {
    for (const handler of fixture.handlers.get("tool_call") ?? []) {
      await handler({ toolName }, context);
    }
  }
}

const nextTurn = () => new Promise((resolve) => setImmediate(resolve));

test("inline tool nudge tracker rejects invalid configuration", () => {
  assert.throws(() => createInlineToolNudgeTracker({ actionLimit: -1 }), /actionLimit/);
  assert.throws(() => createInlineToolNudgeTracker({ actionLimit: 1.5 }), /actionLimit/);
  assert.throws(() => createInlineToolNudgeTracker({ actionLimit: Number.MAX_SAFE_INTEGER + 1 }), /actionLimit/);
  assert.throws(() => createInlineToolNudgeTracker({ readThreshold: 0 }), /readThreshold/);
  assert.throws(() => createInlineToolNudgeTracker({ readThreshold: 1.5 }), /readThreshold/);
  assert.throws(() => createInlineToolNudgeTracker({ readThreshold: Number.MAX_SAFE_INTEGER + 1 }), /readThreshold/);
  assert.throws(() => createInlineToolNudgeTracker({ notify: "nope" }), /notify/);
});

test("inline tool nudge tracker supports custom threshold boundaries", () => {
  const notices = [];
  const tracker = createInlineToolNudgeTracker({
    actionLimit: 0,
    readThreshold: 2,
    notify: (notice) => notices.push(notice),
  });

  tracker.record("bash");
  assert.deepEqual(notices.map(({ kind, count, threshold }) => ({ kind, count, threshold })), [
    { kind: "action", count: 1, threshold: 0 },
  ]);

  tracker.record("read");
  assert.equal(notices.length, 1);
  tracker.record("read");
  assert.deepEqual(notices.map(({ kind, count, threshold }) => ({ kind, count, threshold })), [
    { kind: "action", count: 1, threshold: 0 },
    { kind: "read", count: 2, threshold: 2 },
  ]);
});

test("inline action tools nudge on the fifth mixed shell/edit/write call and reset", () => {
  const notices = [];
  const tracker = createInlineToolNudgeTracker({ notify: (notice) => notices.push(notice) });

  for (const toolName of ["bash", "edit", "write", "bash"]) assert.equal(tracker.record(toolName), undefined);
  assert.equal(notices.length, 0);

  const notice = tracker.record("edit");
  assert.equal(notices.length, 1);
  assert.deepEqual(notice, notices[0]);
  assert.deepEqual(notices[0], {
    kind: "action",
    count: 5,
    threshold: 4,
    toolName: "edit",
    message: INLINE_TOOL_NUDGE_MESSAGE,
  });

  for (const toolName of ["write", "bash", "edit", "write"]) tracker.record(toolName);
  assert.equal(notices.length, 1);
  tracker.record("bash");
  assert.equal(notices.length, 2);
});

test("inline reads use the separate eighth-read threshold and reset", () => {
  const notices = [];
  const tracker = createInlineToolNudgeTracker({ notify: (notice) => notices.push(notice) });

  for (let index = 0; index < 7; index += 1) assert.equal(tracker.record("read"), undefined);
  assert.equal(notices.length, 0);

  const notice = tracker.record("read");
  assert.equal(notices.length, 1);
  assert.deepEqual(notice, notices[0]);
  assert.deepEqual(notices[0], {
    kind: "read",
    count: 8,
    threshold: 8,
    toolName: "read",
    message: INLINE_TOOL_NUDGE_MESSAGE,
  });

  for (let index = 0; index < 7; index += 1) tracker.record("read");
  assert.equal(notices.length, 1);
  tracker.record("read");
  assert.equal(notices.length, 2);
});

test("inline read and action counters reset each other", () => {
  const notices = [];
  const tracker = createInlineToolNudgeTracker({ notify: (notice) => notices.push(notice) });

  for (const toolName of ["bash", "edit", "write", "bash", "read", "edit"]) tracker.record(toolName);
  assert.equal(notices.length, 0);

  for (let index = 0; index < 7; index += 1) tracker.record("read");
  tracker.record("bash");
  for (let index = 0; index < 7; index += 1) tracker.record("read");
  assert.equal(notices.length, 0);

  tracker.record("read");
  assert.equal(notices.length, 1);
  assert.equal(notices[0].kind, "read");
});

test("agent start resets inline counters", () => {
  const notices = [];
  const tracker = createInlineToolNudgeTracker({ notify: (notice) => notices.push(notice) });

  for (const toolName of ["bash", "edit", "write", "bash"]) tracker.record(toolName);
  tracker.reset();
  tracker.record("edit");

  assert.equal(notices.length, 0);
});

test("control-plane and unknown tools clear inline counters without nudging", () => {
  for (const controlTool of ["mecha_context", "mecha_parent_message", "mecha_recall", "mecha_settled_notifications", "unknown_tool"]) {
    const notices = [];
    const tracker = createInlineToolNudgeTracker({ notify: (notice) => notices.push(notice) });
    for (const toolName of ["bash", "edit", "write", "bash", controlTool, "edit"]) tracker.record(toolName);
    assert.equal(notices.length, 0, controlTool);
  }
});

test("inline tool nudge uses the canned binding message", () => {
  assert.equal(
    INLINE_TOOL_NUDGE_MESSAGE,
    "System noticed this worker is doing work inline. Reminder: using subagents when possible is mandatory to avoid tying up the main thread and bloating context.",
  );
  assert.match(INLINE_TOOL_NUDGE_MESSAGE, /noticed this worker is doing work inline/);
  assert.match(INLINE_TOOL_NUDGE_MESSAGE, /using subagents when possible is mandatory/);
  assert.match(INLINE_TOOL_NUDGE_MESSAGE, /tying up the main thread/);
  assert.match(INLINE_TOOL_NUDGE_MESSAGE, /bloating context/);
});

test("agent_start resets integrated inline tool counters", async () => {
  const sent = [];
  const fixture = createMessagingFixture({ sendMessage: (...args) => sent.push(args) });

  await emitToolCalls(fixture, ["bash", "edit", "write", "bash"]);
  for (const handler of fixture.handlers.get("agent_start") ?? []) await handler({}, fixture.context);
  await emitToolCalls(fixture, ["edit"]);

  assert.equal(sent.length, 0);
});

test("top-level tool calls send inline work nudge as a steer", async () => {
  const sent = [];
  const fixture = createMessagingFixture({ sendMessage: (...args) => sent.push(args) });
  for (const handler of fixture.handlers.get("session_start") ?? []) await handler({}, fixture.context);

  await emitToolCalls(fixture, ["bash", "edit", "write", "bash", "edit"]);

  assert.equal(sent.length, 1);
  assert.equal(sent[0][0].customType, INLINE_TOOL_NUDGE_MESSAGE_TYPE);
  assert.equal(sent[0][0].content, INLINE_TOOL_NUDGE_MESSAGE);
  assert.equal(sent[0][0].display, true);
  assert.deepEqual(sent[0][0].details, {
    kind: "action",
    count: 5,
    threshold: 4,
    tool_name: "edit",
  });
  assert.deepEqual(sent[0][1], { deliverAs: "steer", triggerTurn: true });
});

test("top-level read calls send inline work nudge as a steer", async () => {
  const sent = [];
  const fixture = createMessagingFixture({ sendMessage: (...args) => sent.push(args) });

  await emitToolCalls(fixture, Array.from({ length: 8 }, () => "read"));

  assert.equal(sent.length, 1);
  assert.equal(sent[0][0].customType, INLINE_TOOL_NUDGE_MESSAGE_TYPE);
  assert.equal(sent[0][0].content, INLINE_TOOL_NUDGE_MESSAGE);
  assert.deepEqual(sent[0][0].details, {
    kind: "read",
    count: 8,
    threshold: 8,
    tool_name: "read",
  });
  assert.deepEqual(sent[0][1], { deliverAs: "steer", triggerTurn: true });
});

test("inline nudge send failures do not block tool execution and still reset", async () => {
  let attempts = 0;
  const fixture = createMessagingFixture({ sendMessage: () => { attempts += 1; throw new Error("send failed"); } });

  await assert.doesNotReject(() => emitToolCalls(fixture, ["bash", "bash", "bash", "bash", "bash"]));
  assert.equal(attempts, 1);

  await assert.doesNotReject(() => emitToolCalls(fixture, ["edit", "edit", "edit", "edit"]));
  assert.equal(attempts, 1);
  await assert.doesNotReject(() => emitToolCalls(fixture, ["edit"]));
  assert.equal(attempts, 2);
});

test("inline nudge async send failures do not block tool execution", async () => {
  let attempts = 0;
  const fixture = createMessagingFixture({ sendMessage: () => { attempts += 1; return Promise.reject(new Error("send failed")); } });

  await assert.doesNotReject(() => emitToolCalls(fixture, ["bash", "bash", "bash", "bash", "bash"]));
  await nextTurn();
  assert.equal(attempts, 1);

  await assert.doesNotReject(() => emitToolCalls(fixture, ["edit", "edit", "edit", "edit", "edit"]));
  await nextTurn();
  assert.equal(attempts, 2);
});

test("forked sessions with parentSession headers still receive inline nudges", async () => {
  const sent = [];
  const fixture = createMessagingFixture({
    sendMessage: (...args) => sent.push(args),
    header: { parentSession: "/tmp/parent-session.jsonl" },
  });

  await emitToolCalls(fixture, ["bash", "edit", "write", "bash", "edit"]);

  assert.equal(sent.length, 1);
  assert.equal(sent[0][0].details.kind, "action");
});

test("child agent_start does not reset parent inline counters", async () => {
  const sent = [];
  const fixture = createMessagingFixture({ sendMessage: (...args) => sent.push(args) });
  fixture.childHandlers.get("subagents:child:session-created")({ sessionId: "child-session" });
  try {
    const childContext = {
      sessionManager: { getSessionId: () => "child-session", getHeader: () => ({}) },
      ui: { notify() {}, setStatus() {} },
    };

    await emitToolCalls(fixture, ["bash", "edit", "write", "bash"]);
    for (const handler of fixture.handlers.get("agent_start") ?? []) await handler({}, childContext);
    await emitToolCalls(fixture, ["edit"]);

    assert.equal(sent.length, 1);
  } finally {
    fixture.childHandlers.get("subagents:child:disposed")({ sessionId: "child-session" });
  }
});

test("child sessions do not receive inline tool nudges", async () => {
  const sent = [];
  const fixture = createMessagingFixture({ sendMessage: (...args) => sent.push(args) });
  fixture.childHandlers.get("subagents:child:session-created")({ sessionId: "child-session" });
  try {
    const childContext = {
      sessionManager: { getSessionId: () => "child-session", getHeader: () => ({}) },
      ui: { notify() {}, setStatus() {} },
    };
    await emitToolCalls(fixture, ["bash", "edit", "write", "bash", "edit"], childContext);
    assert.equal(sent.length, 0);
  } finally {
    fixture.childHandlers.get("subagents:child:disposed")({ sessionId: "child-session" });
  }
});

test("child tool calls do not advance parent counters", async () => {
  const sent = [];
  const fixture = createMessagingFixture({ sendMessage: (...args) => sent.push(args) });
  fixture.childHandlers.get("subagents:child:session-created")({ sessionId: "child-session" });
  try {
    const childContext = {
      sessionManager: { getSessionId: () => "child-session", getHeader: () => ({}) },
      ui: { notify() {}, setStatus() {} },
    };

    await emitToolCalls(fixture, ["bash", "edit", "write", "bash"], childContext);
    await emitToolCalls(fixture, ["edit"], fixture.context);

    assert.equal(sent.length, 0);
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
