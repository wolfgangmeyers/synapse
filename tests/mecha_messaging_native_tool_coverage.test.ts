import assert from "node:assert/strict";
import test from "node:test";

import {
  createInlineToolPolicy,
  createMechaMessagingExtension,
  INLINE_TOOL_ALLOWANCE_TOOL_NAME,
} from "../pi-extensions/mecha-messaging/index.ts";
import { createMailboxClient } from "../pi-extensions/mecha-messaging/mailbox-client.ts";

type JsonPrimitive = string | number | boolean | null;
type JsonValue = JsonPrimitive | JsonObject | JsonValue[] | undefined;
type JsonObject = { [key: string]: JsonValue };

type Context = {
  sessionManager: {
    getSessionId: () => string;
    getBranch: () => JsonObject[];
    isPersisted: () => boolean;
    getSessionFile: () => string | undefined;
  };
  ui: {
    notify: (message: string, level?: string) => void;
    setStatus: (key: string, value: string | undefined) => void;
  };
  isIdle?: () => boolean;
  compact?: (options: {
    customInstructions?: string;
    onComplete?: (result: { tokensBefore?: number }) => void;
    onError?: (error: Error) => void;
  }) => void;
};

type Tool = {
  name: string;
  execute: (
    toolCallId: string,
    input: unknown,
    signal: AbortSignal | undefined,
    onUpdate: unknown,
    context: Context,
  ) => Promise<unknown>;
};

type ToolResult = {
  content: Array<{ type: string; text: string }>;
  details?: Record<string, unknown>;
  isError?: boolean;
};

type Fixture = {
  tools: Map<string, Tool>;
  handlers: Map<string, Array<(event: JsonObject, context: Context) => unknown>>;
  eventHandlers: Map<string, Array<(event: JsonObject, context: Context) => unknown>>;
  context: Context;
  createContext: (sessionId: string, overrides?: Partial<Context>) => Context;
};

type MailboxClient = ReturnType<typeof createMailboxClient>;
type MailboxReply = Awaited<ReturnType<MailboxClient["sendReply"]>>;
type MailboxClientStub = Omit<MailboxClient, "sendReply"> & {
  sendReply: (message: { to: string; body: string; subject?: string }) => Promise<MailboxReply>;
};

function isToolResult(value: unknown): value is ToolResult {
  if (value === null || typeof value !== "object" || !("content" in value) || !Array.isArray(value.content)) return false;
  return value.content.every((entry) => entry !== null && typeof entry === "object"
    && "type" in entry && typeof entry.type === "string"
    && "text" in entry && typeof entry.text === "string");
}

function isTool(value: object): value is Tool {
  return "name" in value && typeof value.name === "string"
    && "execute" in value && typeof value.execute === "function";
}

function resultOf(value: unknown): ToolResult {
  if (!isToolResult(value)) throw new Error("tool returned an invalid result");
  return value;
}

function createClientStub(sendReply: MailboxClientStub["sendReply"]): MailboxClientStub {
  return {
    instanceId: "test-instance",
    parentSessionId: "test-parent-session",
    producerEpoch: "00000000-0000-4000-8000-000000000000",
    connect: async () => {},
    sendReceipt: () => {},
    sendActivity: () => {},
    sendGoalState: () => false,
    sendInlineToolState: () => false,
    sendLifecycleSnapshot: () => undefined,
    sendLifecycleEvent: () => undefined,
    requestLifecycleResync: () => false,
    lifecycleStatus: () => ({ supported: false, ready: false, routerVersion: 0, pending: 0, bytes: 0 }),
    currentLeaseId: () => undefined,
    sendMessageArrival: () => {},
    pause: () => {},
    resume: () => Promise.resolve(),
    sendReply,
    close: () => {},
  };
}

function createFixture({
  sendReply = async () => ({ id: "message-1", state: "queued" }),
  clientFactory,
  now = () => 0,
}: {
  sendReply?: (message: { to: string; body: string; subject?: string }) => Promise<MailboxReply>;
  clientFactory?: () => MailboxClientStub;
  now?: () => number;
} = {}): Fixture {
  const tools = new Map<string, Tool>();
  const handlers = new Map<string, Array<(event: JsonObject, context: Context) => unknown>>();
  const eventHandlers = new Map<string, Array<(event: JsonObject, context: Context) => unknown>>();
  const branch: JsonObject[] = [];
  const createContext = (sessionId: string, overrides: Partial<Context> = {}): Context => {
    const context: Context = {
      sessionManager: {
        getSessionId: () => sessionId,
        getBranch: () => branch,
        isPersisted: () => true,
        getSessionFile: () => `/tmp/${sessionId}.jsonl`,
      },
      ui: { notify() {}, setStatus() {} },
      ...overrides,
    };
    return context;
  };
  const context = createContext("parent-session");
  const defaultClient = createClientStub(sendReply);
  const pi = {
    events: {
      on(name: string, handler: (event: JsonObject, context: Context) => unknown) {
        const current = eventHandlers.get(name) ?? [];
        current.push(handler);
        eventHandlers.set(name, current);
      },
      emit() {},
    },
    on(name: string, handler: (event: JsonObject, context: Context) => unknown) {
      const current = handlers.get(name) ?? [];
      current.push(handler);
      handlers.set(name, current);
    },
    registerTool(tool: object) {
      if (isTool(tool)) tools.set(tool.name, tool);
    },
    registerCommand() {},
    sendMessage() {},
    appendEntry() {},
    exec: async () => ({ code: 0, stderr: "" }),
  };
  createMechaMessagingExtension(pi, {
    resolveCurrentAgentId: () => "parent-agent",
    createClient: () => clientFactory?.() ?? defaultClient,
    nowImpl: now,
    createCoordinator: () => ({ onEnvelope() {}, onMessageEnd() {}, close() {} }),
    createSettledScheduler: () => ({
      activeChildCount: () => 0,
      onParentActivity() {}, onParentSettled() {}, onChildStarted() {}, onChildDisposed() {}, dispose() {},
    }),
    operationalTools: { environment: { MECHA_ROUTER_URL: "http://127.0.0.1:8765" } },
  });
  return { tools, handlers, eventHandlers, context, createContext };
}

async function sessionStart(fixture: Fixture, context = fixture.context): Promise<void> {
  for (const handler of fixture.handlers.get("session_start") ?? []) await handler({}, context);
}

async function execute(fixture: Fixture, name: string, input: unknown, context = fixture.context): Promise<ToolResult> {
  const tool = fixture.tools.get(name);
  assert.ok(tool, `missing ${name}`);
  return resultOf(await tool.execute("test-call", input, undefined, undefined, context));
}

test("native mailbox send preserves the real accepted message ID and state", async () => {
  const calls: Array<{ to: string; body: string; subject?: string }> = [];
  const fixture = createFixture({
    sendReply: async (message) => {
      calls.push(message);
      return { id: "actual-message-id", state: "queued" };
    },
  });
  await sessionStart(fixture);

  const result = await execute(fixture, "mecha_mailbox_send", {
    recipient: "recipient",
    body: "message body",
    subject: "message subject",
  });

  assert.deepEqual(calls, [{ to: "recipient", body: "message body", subject: "message subject" }]);
  assert.deepEqual(result.details, { message_id: "actual-message-id", state: "queued" });
  assert.match(result.content[0].text, /actual-message-id, queued/);
  assert.equal(result.isError, undefined);
});

test("native mailbox send accepts a delivered router result", async () => {
  const fixture = createFixture({ sendReply: async () => ({ id: "delivered-message-id", state: "delivered" }) });
  await sessionStart(fixture);

  const result = await execute(fixture, "mecha_mailbox_send", { recipient: "recipient", body: "message body" });

  assert.deepEqual(result.details, { message_id: "delivered-message-id", state: "delivered" });
  assert.match(result.content[0].text, /delivered-message-id, delivered/);
  assert.equal(result.isError, undefined);
});

test("native mailbox send rejects failed, rejected, or empty-ID results instead of claiming acceptance", async () => {
  for (const sent of [
    { id: "message-id", state: "failed" },
    { id: "message-id", state: "rejected" },
    { id: "", state: "queued" },
  ]) {
    const fixture = createFixture({ sendReply: async () => sent });
    await sessionStart(fixture);

    const result = await execute(fixture, "mecha_mailbox_send", { recipient: "recipient", body: "message body" });

    assert.equal(result.isError, true);
    assert.doesNotMatch(result.content[0].text, /Mailbox message accepted/);
  }
});

test("native mailbox send rolls back the rate reservation for an invalid result", async () => {
  let attempts = 0;
  const fixture = createFixture({
    sendReply: async () => {
      attempts += 1;
      return attempts === 1 ? { id: "message-id", state: "failed" } : { id: `message-${attempts}`, state: "queued" };
    },
  });
  await sessionStart(fixture);

  const rejected = await execute(fixture, "mecha_mailbox_send", { recipient: "recipient", body: "message body" });
  assert.equal(rejected.isError, true);
  for (let index = 0; index < 6; index += 1) {
    const result = await execute(fixture, "mecha_mailbox_send", { recipient: "recipient", body: "message body" });
    assert.equal(result.isError, undefined);
  }
  assert.equal(attempts, 7);
});

test("native mailbox send rejects malformed results and rejected promises", async () => {
  for (const malformed of [null, { id: "message-id" }, { id: 42, state: "queued" }]) {
    const fixture = createFixture({ sendReply: async () => malformed });
    await sessionStart(fixture);

    const result = await execute(fixture, "mecha_mailbox_send", { recipient: "recipient", body: "message body" });

    assert.equal(result.isError, true);
    assert.match(result.content[0].text, /invalid mailbox reply result/i);
    assert.doesNotMatch(result.content[0].text, /Mailbox message accepted/);
  }

  const fixture = createFixture({ sendReply: async () => { throw new Error("router unavailable"); } });
  await sessionStart(fixture);
  const result = await execute(fixture, "mecha_mailbox_send", { recipient: "recipient", body: "message body" });
  assert.equal(result.isError, true);
  assert.equal(result.content[0].text, "router unavailable");
});

test("native mailbox send validates sender and message inputs before invoking the client", async () => {
  let calls = 0;
  const fixture = createFixture({ sendReply: async () => { calls += 1; return { id: "message-id", state: "queued" }; } });
  await sessionStart(fixture);
  const inputs: unknown[] = [
    { recipient: "recipient", body: "body", from: "spoofed" },
    { recipient: "", body: "body" },
    { recipient: "recipient@example.com", body: "body" },
    { recipient: "recipient", body: "" },
    { recipient: "recipient", body: "body", subject: 42 },
  ];

  for (const input of inputs) {
    const result = await execute(fixture, "mecha_mailbox_send", input);
    assert.equal(result.isError, true);
  }
  assert.equal(calls, 0);
});

test("native child mailbox sends use the child client and do not fall back to the parent", async () => {
  const calls: string[] = [];
  let clientNumber = 0;
  const parentClient = createClientStub(async () => { calls.push("parent"); return { id: "parent-id", state: "queued" }; });
  const childClient = createClientStub(async () => { calls.push("child"); return { id: "child-id", state: "queued" }; });
  const fixture = createFixture({ clientFactory: () => clientNumber++ === 0 ? parentClient : childClient });
  await sessionStart(fixture);
  const childCreated = fixture.eventHandlers.get("subagents:child:session-created");
  assert.ok(childCreated);
  childCreated?.forEach((handler) => handler({ sessionId: "child-session" }, fixture.context));
  const childContext = fixture.createContext("child-session");
  await sessionStart(fixture, childContext);

  const result = await execute(fixture, "mecha_mailbox_send", { recipient: "recipient", body: "from child" }, childContext);

  assert.equal(result.details?.message_id, "child-id");
  assert.deepEqual(calls, ["child"]);
});

test("native child mailbox send fails without an initialized child client", async () => {
  const calls: string[] = [];
  const fixture = createFixture({ sendReply: async () => { calls.push("parent"); return { id: "parent-id", state: "queued" }; } });
  await sessionStart(fixture);
  const childCreated = fixture.eventHandlers.get("subagents:child:session-created");
  assert.ok(childCreated);
  childCreated?.forEach((handler) => handler({ sessionId: "child-without-client" }, fixture.context));

  const result = await execute(fixture, "mecha_mailbox_send", { recipient: "recipient", body: "from child" }, fixture.createContext("child-without-client"));

  assert.equal(result.isError, true);
  assert.match(result.content[0].text, /native child/i);
  assert.deepEqual(calls, []);
  fixture.eventHandlers.get("subagents:child:disposed")?.forEach((handler) => handler({ sessionId: "child-without-client" }, fixture.context));
});

test("inline allowance normalizes functions aliases and leaves the pool unchanged on denial", async () => {
  const policy = createInlineToolPolicy({ now: () => 0 });
  const fixture = createFixture();
  const allowance = fixture.tools.get(INLINE_TOOL_ALLOWANCE_TOOL_NAME);
  assert.ok(allowance);

  const granted = await execute(fixture, INLINE_TOOL_ALLOWANCE_TOOL_NAME, { toolName: "functions.bash", usageCount: 4 });
  assert.equal(granted.details?.toolName, "bash");
  assert.equal(granted.details?.remainingPool, 1);
  const denied = await execute(fixture, INLINE_TOOL_ALLOWANCE_TOOL_NAME, { toolName: "bash", usageCount: 2 });
  assert.equal(denied.isError, true);
  assert.equal(denied.details?.remainingPool, 1);
  assert.equal(policy.grant("edit", 1).remainingPool, 4);
});

test("mecha_context reports context reset and scratchpad status, and rejects compact action", async () => {
  const fixture = createFixture();
  await sessionStart(fixture);

  const status = await execute(fixture, "mecha_context", {
    action: "status",
  });
  assert.equal(status.isError, undefined);
  assert.equal(status.details?.system, "context_reset+scratchpad");
  assert.equal(status.details?.warningThresholdPercent, 70);
  assert.equal(status.details?.resetThresholdPercent, 80);
  assert.ok(status.details?.guidance);

  const compactAttempt = await execute(fixture, "mecha_context", {
    action: "compact",
  });
  assert.equal(compactAttempt.isError, true);
  assert.match(compactAttempt.content[0].text, /compaction has been replaced/);
});
