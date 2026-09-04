import assert from "node:assert/strict";
import { mkdtempSync, mkdirSync, rmSync, writeFileSync } from "node:fs";
import { basename, join } from "node:path";
import { tmpdir } from "node:os";
import { fileURLToPath } from "node:url";
import test from "node:test";

import {
  createOperationalToolDefinitions,
  MAX_OPERATIONAL_OUTPUT,
  OPERATIONAL_TRUNCATION_MARKER,
  registerOperationalTools,
  resolveSupervisorTools,
} from "../pi-extensions/mecha-messaging/operational-tools.ts";
import { createMechaMessagingExtension } from "../pi-extensions/mecha-messaging/index.ts";
import { createReminderLockState, createRouterReminderReader } from "../pi-extensions/mecha-messaging/reminder-lock.ts";

function fakeRunner(result = { stdout: "ok" }) {
  const calls = [];
  return {
    calls,
    async runFile(file, args, options) {
      calls.push({ file, args, options });
      return typeof result === "function" ? result(file, args, options) : result;
    },
  };
}

function registered(options = {}) {
  const tools = new Map();
  registerOperationalTools({ registerTool(tool) { tools.set(tool.name, tool); } }, options);
  return tools;
}

async function execute(tool, input) {
  return tool.execute("call", input);
}

function actionVariant(tool, action) {
  assert.equal(tool.parameters.type, "object");
  assert.ok(tool.parameters.properties.action.enum.includes(action), `${tool.name}:${action}`);
  return { properties: tool.parameters.properties, required: ["action"] };
}

function assertCursorMetadata(tool) {
  assert.ok(tool.promptSnippet, tool.name);
  assert.ok(Array.isArray(tool.promptGuidelines) && tool.promptGuidelines.length > 0, tool.name);
  for (const guideline of tool.promptGuidelines) assert.match(guideline, new RegExp(tool.name));
}

function assertClosedActionSchemas(tools) {
  for (const tool of tools.values()) {
    assert.equal(tool.parameters.type, "object", tool.name);
    assert.equal(tool.parameters.additionalProperties, false, tool.name);
    assert.deepEqual(tool.parameters.required, ["action"], tool.name);
    assert.ok(Array.isArray(tool.parameters.properties.action.enum), tool.name);
    assertCursorMetadata(tool);
  }
}

function messagingPi() {
  const tools = [];
  return {
    tools,
    events: { on() {} },
    on() {},
    registerCommand() {},
    registerTool(tool) { tools.push(tool); },
    sendMessage() {},
  };
}

function messagingOptions(overrides = {}) {
  return {
    resolveCurrentAgentId: () => "worker",
    createClient: () => ({ connect: async () => {}, close() {}, sendReceipt() {}, sendActivity() {} }),
    createSettledScheduler: () => ({
      onParentActivity() {}, onParentSettled() {}, onChildStarted() {}, onChildDisposed() {}, dispose() {},
    }),
    ...overrides,
  };
}

test("registers exactly the eight approved tools once from the messaging extension", () => {
  const pi = messagingPi();
  createMechaMessagingExtension(pi, messagingOptions({ runFile: async () => ({ stdout: "ok" }) }));
  const names = pi.tools.map((tool) => tool.name).filter((name) => name.startsWith("mecha_") && [
    "mecha_state", "mecha_priority", "mecha_verification_handoff", "mecha_reminder",
    "mecha_worker", "mecha_pr", "mecha_git", "mecha_router",
  ].includes(name));
  assert.deepEqual(names, [
    "mecha_state", "mecha_priority", "mecha_verification_handoff", "mecha_reminder",
    "mecha_worker", "mecha_pr", "mecha_git", "mecha_router",
  ]);
  assert.equal(new Set(names).size, names.length);
});

test("registers Cursor metadata for every messaging control tool", () => {
  const pi = messagingPi();
  createMechaMessagingExtension(pi, messagingOptions({ runFile: async () => ({ stdout: "ok" }) }));
  const tools = new Map(pi.tools.map((tool) => [tool.name, tool]));
  for (const name of [
    "mecha_mailbox_send", "mecha_parent_message", "mecha_request_inline_tool_allowance",
    "mecha_recall", "mecha_settled_notifications", "mecha_context",
  ]) {
    const tool = tools.get(name);
    assert.ok(tool, name);
    assertCursorMetadata(tool);
  }
});

test("uses action-specific closed schemas for every approved action", () => {
  const tools = registered({ runFile: async () => ({ stdout: "ok" }) });
  assertClosedActionSchemas(tools);
  assert.ok(tools.get("mecha_state").parameters.properties.alias);
  assert.ok(tools.get("mecha_worker").parameters.properties.alias);
  assert.deepEqual(tools.get("mecha_worker").parameters.properties.alias.pattern, "^[A-Za-z0-9][A-Za-z0-9_-]{0,119}$");
  assert.deepEqual(tools.get("mecha_pr").parameters.properties.pullRequest, { type: "integer", minimum: 1 });
  assert.ok(tools.get("mecha_git").parameters.properties.task);
  assert.equal(tools.get("mecha_reminder").parameters.properties.target, undefined);
});

test("native reminder create derives its target from the current identity and fails closed without it", async () => {
  const requests = [];
  const fetchImpl = async (url, request = {}) => {
    requests.push({ url, request });
    return { ok: true, status: 201, json: async () => ({ reminder: { id: "reminder-1" } }) };
  };
  const tools = registered({
    resolveCurrentAgentId: () => "current-window",
    fetchImpl,
    environment: { MECHA_ROUTER_URL: "http://127.0.0.1:8765" },
    now: () => new Date("2030-01-01T00:00:00.000Z"),
    generateId: () => "reminder-1",
  });
  const created = await execute(tools.get("mecha_reminder"), { action: "create", message: "hello", delayMinutes: 1 });
  assert.equal(created.isError, undefined);
  assert.equal(JSON.parse(requests[0].request.body).target, "current-window");

  const failedRequests = [];
  const failedTools = registered({
    resolveCurrentAgentId: () => undefined,
    fetchImpl: async (url, request = {}) => {
      failedRequests.push({ url, request });
      return { ok: true, status: 201, json: async () => ({}) };
    },
  });
  const failed = await execute(failedTools.get("mecha_reminder"), { action: "create", message: "hello", delayMinutes: 1 });
  assert.equal(failed.isError, true);
  assert.equal(failed.content[0].text, "Reminder target is unavailable: current tmux window identity could not be resolved.");
  assert.deepEqual(failedRequests, []);
});

test("supports recurring and absolute reminder schedules while preserving delayMinutes", async () => {
  const requests = [];
  const tools = registered({
    resolveCurrentAgentId: () => "current-window",
    fetchImpl: async (url, request = {}) => {
      requests.push({ url, request });
      if ((request.method ?? "GET") === "GET") {
        return {
          ok: true,
          status: 200,
          json: async () => ({ reminder: {
            id: "reminder-1",
            target: "current-window",
            creator: "pi",
            created_at: "2030-01-01T00:00:00.000Z",
          } }),
          headers: { get: () => null },
        };
      }
      return { ok: true, status: request.method === "POST" ? 201 : 200, json: async () => ({ reminder: { id: "reminder-1" } }) };
    },
    environment: { MECHA_ROUTER_URL: "http://127.0.0.1:8765" },
    now: () => new Date("2030-01-01T00:00:00.000Z"),
    generateId: () => "reminder-1",
  });
  const reminder = tools.get("mecha_reminder");
  const created = await execute(reminder, { action: "create", message: "recurring", intervalMinutes: 15 });
  assert.equal(created.isError, undefined);
  const createPayload = JSON.parse(requests[0].request.body);
  assert.equal(createPayload.interval_ms, 900_000);
  assert.equal(createPayload.next_due_at, "2030-01-01T00:15:00.000Z");

  const updated = await execute(reminder, {
    action: "update", reminderId: "reminder-1", scheduledAt: "2030-01-02T01:02:03-05:00",
  });
  assert.equal(updated.isError, undefined);
  assert.deepEqual(JSON.parse(requests.at(-1).request.body), {
    next_due_at: "2030-01-02T06:02:03.000Z",
  });

  const absolute = await execute(reminder, { action: "create", message: "absolute", scheduledAt: "2030-01-03T04:05:06+02:00" });
  assert.equal(absolute.isError, undefined);
  const absolutePayload = JSON.parse(requests.at(-1).request.body);
  assert.equal(absolutePayload.next_due_at, "2030-01-03T02:05:06.000Z");
  assert.equal(absolutePayload.interval_ms, null);
});

test("rejects scheduled times without timezones or valid ISO values before router calls", async () => {
  const requests = [];
  const tools = registered({
    resolveCurrentAgentId: () => "current-window",
    fetchImpl: async (url, request = {}) => {
      requests.push({ url, request });
      return { ok: true, status: 201, json: async () => ({}) };
    },
    environment: { MECHA_ROUTER_URL: "http://127.0.0.1:8765" },
  });
  const reminder = tools.get("mecha_reminder");

  for (const scheduledAt of ["2030-01-01T00:00:00", "not-an-ISO-datetime"]) {
    const result = await execute(reminder, { action: "create", message: "hello", scheduledAt });
    assert.equal(result.isError, true);
    assert.match(result.content[0].text, /scheduledAt must/);
  }
  assert.deepEqual(requests, []);
});

test("updates recurring reminders with the next due time and interval", async () => {
  const requests = [];
  const tools = registered({
    resolveCurrentAgentId: () => "current-window",
    fetchImpl: async (url, request = {}) => {
      requests.push({ url, request });
      if ((request.method ?? "GET") === "GET") {
        return {
          ok: true,
          status: 200,
          json: async () => ({ reminder: { id: "reminder-1", target: "current-window" } }),
          headers: { get: () => null },
        };
      }
      return { ok: true, status: 200, json: async () => ({ reminder: { id: "reminder-1" } }) };
    },
    environment: { MECHA_ROUTER_URL: "http://127.0.0.1:8765" },
    now: () => new Date("2030-01-01T00:00:00.000Z"),
  });

  const result = await execute(tools.get("mecha_reminder"), {
    action: "update", reminderId: "reminder-1", intervalMinutes: 15,
  });
  assert.equal(result.isError, undefined);
  assert.deepEqual(JSON.parse(requests.at(-1).request.body), {
    next_due_at: "2030-01-01T00:15:00.000Z",
    interval_ms: 900_000,
  });
});

test("rejects malformed reminder list responses", async () => {
  const tools = registered({
    resolveCurrentAgentId: () => "current-window",
    fetchImpl: async () => ({
      ok: true,
      status: 200,
      json: async () => ({ reminders: {} }),
      headers: { get: () => null },
    }),
    environment: { MECHA_ROUTER_URL: "http://127.0.0.1:8765" },
  });

  const result = await execute(tools.get("mecha_reminder"), { action: "list" });
  assert.equal(result.isError, true);
  assert.equal(result.content[0].text, "router returned an invalid reminder list");
});

test("lists only current-target reminders and denies foreign updates and deletes", async () => {
  const requests = [];
  const tools = registered({
    resolveCurrentAgentId: () => "current-window",
    fetchImpl: async (url, request = {}) => {
      requests.push({ url, request });
      return {
        ok: true,
        status: 200,
        json: async () => (request.method === "GET"
          ? { reminders: [
            { id: "own", target: "current-window" },
            { id: "foreign", target: "other-window" },
          ] }
          : { reminder: { id: "foreign", target: "other-window", creator: "pi", created_at: "2030-01-01T00:00:00.000Z" } }),
        headers: { get: () => null },
      };
    },
    environment: { MECHA_ROUTER_URL: "http://127.0.0.1:8765" },
  });
  const reminder = tools.get("mecha_reminder");
  const listed = await execute(reminder, { action: "list" });
  assert.deepEqual(listed.details.reminders, [{ id: "own", target: "current-window" }]);
  assert.doesNotMatch(listed.content[0].text, /foreign/);

  const updated = await execute(reminder, { action: "update", reminderId: "foreign", message: "changed" });
  const deleted = await execute(reminder, { action: "delete", reminderId: "foreign" });
  assert.equal(updated.isError, true);
  assert.equal(deleted.isError, true);
  assert.deepEqual(requests.slice(1).map(({ request }) => request.method ?? "GET"), ["GET", "GET"]);
});

test("treats a null reminder list as an empty owned list", async () => {
  const tools = registered({
    resolveCurrentAgentId: () => "current-window",
    fetchImpl: async () => ({
      ok: true,
      status: 200,
      json: async () => ({ reminders: null }),
      headers: { get: () => null },
    }),
    environment: { MECHA_ROUTER_URL: "http://127.0.0.1:8765" },
  });

  const listed = await execute(tools.get("mecha_reminder"), { action: "list" });
  assert.deepEqual(listed.details.reminders, []);
  assert.equal(listed.content[0].text, JSON.stringify({ reminders: [] }));
});

test("enforces reminder scheduling mutual exclusion", async () => {
  const requests = [];
  const tools = registered({
    resolveCurrentAgentId: () => "current-window",
    fetchImpl: async (url, request = {}) => {
      requests.push({ url, request });
      return {
        ok: true,
        status: 200,
        json: async () => ({ reminder: { id: "reminder-1", target: "current-window" } }),
        headers: { get: () => null },
      };
    },
    environment: { MECHA_ROUTER_URL: "http://127.0.0.1:8765" },
  });
  const reminder = tools.get("mecha_reminder");
  const invalidInputs = [
    { action: "create", message: "missing schedule" },
    { action: "create", message: "multiple schedules", delayMinutes: 1, intervalMinutes: 2 },
    { action: "update", reminderId: "reminder-1", message: "multiple schedules", scheduledAt: "2030-01-02T01:02:03Z", intervalMinutes: 2 },
  ];

  for (const input of invalidInputs) {
    const result = await execute(reminder, input);
    assert.equal(result.isError, true);
    assert.match(result.content[0].text, /exactly one|at most one/);
  }
  assert.deepEqual(requests, []);
});

test("matches the approved actions exactly", () => {
  const tools = registered({ runFile: async () => ({ stdout: "ok" }) });
  const actions = Object.fromEntries([...tools].map(([name, tool]) => [name, tool.parameters.properties.action.enum]));
  assert.deepEqual(actions, {
    mecha_state: ["query", "create", "update"],
    mecha_priority: ["create", "update", "status"],
    mecha_verification_handoff: ["record"],
    mecha_reminder: ["list", "create", "update", "delete"],
    mecha_worker: ["list", "launch", "cleanup", "send"],
    mecha_pr: ["status"],
    mecha_git: ["status"],
    mecha_router: ["health", "retry", "dismiss"],
  });
});

test("guards native reminder updates before PATCH and fails open after clear or stale rows", async () => {
  let current = {
    id: "reminder-1",
    target: "worker",
    creator: "supervisor",
    created_at: "2030-01-01T00:00:00.000Z",
  };
  let target = { agentId: "worker", instanceId: "instance-1", leaseId: "lease-1" };
  const requests = [];
  const fetchImpl = async (url, request = {}) => {
    requests.push({ url, request });
    if ((request.method ?? "GET") === "GET") {
      return {
        ok: true,
        status: 200,
        json: async () => ({ reminder: { ...current } }),
        headers: { get: (name) => name === "x-reminder-incarnation" ? "incarnation-a" : null },
      };
    }
    return { ok: true, status: 200, json: async () => ({ id: "reminder-1" }) };
  };
  const readReminder = createRouterReminderReader({ fetchImpl, routerUrl: "http://127.0.0.1:8765" });
  const lock = createReminderLockState({
    getCurrentReminder: readReminder,
    getCurrentTarget: () => target,
    now: () => Date.parse("2030-01-01T00:00:00.000Z"),
  });
  const tools = registered({
    reminderLock: lock,
    fetchImpl,
    environment: { MECHA_ROUTER_URL: "http://127.0.0.1:8765" },
    resolveCurrentAgentId: () => "worker",
    now: () => new Date("2030-01-01T00:00:00.000Z"),
  });
  const update = (message) => execute(tools.get("mecha_reminder"), {
    action: "update", reminderId: "reminder-1", message,
  });

  const unlocked = await update("unlocked");
  assert.equal(unlocked.isError, undefined);
  assert.equal(requests.at(-1).request.method, "PATCH");

  const control = (action) => lock.control({
    targetAgentId: "worker",
    targetInstanceId: "instance-1",
    leaseId: "lease-1",
    reminderId: "reminder-1",
    action,
    expiresAt: "2030-01-01T00:01:00.000Z",
    isCurrent: () => true,
  });
  await control("set");
  const requestCountBeforeLocked = requests.length;
  const locked = await update("locked");
  assert.equal(locked.isError, true);
  assert.equal(locked.content[0].text, "reminder_locked");
  assert.equal(locked.details.error_code, "reminder_locked");
  assert.equal(requests.length, requestCountBeforeLocked + 1);
  assert.equal(requests.at(-1).request.method ?? "GET", "GET");

  const requestCountBeforeLockedDelete = requests.length;
  const lockedDelete = await execute(tools.get("mecha_reminder"), { action: "delete", reminderId: "reminder-1" });
  assert.equal(lockedDelete.isError, true);
  assert.equal(lockedDelete.content[0].text, "reminder_locked");
  assert.equal(lockedDelete.details.error_code, "reminder_locked");
  assert.deepEqual(requests.slice(requestCountBeforeLockedDelete).map(({ request }) => request.method ?? "GET"), ["GET"]);

  await control("clear");
  const cleared = await update("cleared");
  assert.equal(cleared.isError, undefined);
  assert.equal(requests.at(-1).request.method, "PATCH");

  await control("set");
  current = { ...current, created_at: "2030-01-01T00:02:00.000Z" };
  const stale = await update("stale");
  assert.equal(stale.isError, undefined);
  assert.equal(requests.at(-1).request.method, "PATCH");
});

test("fails closed when reminder ownership cannot be read", async () => {
  const requests = [];
  const tools = registered({
    fetchImpl: async (url, request = {}) => {
      requests.push({ url, request });
      throw new Error("router read failed");
    },
    resolveCurrentAgentId: () => "worker",
    environment: { MECHA_ROUTER_URL: "http://127.0.0.1:8765" },
  });
  const reminder = tools.get("mecha_reminder");
  const updated = await execute(reminder, { action: "update", reminderId: "reminder-1", message: "updated" });
  const deleted = await execute(reminder, { action: "delete", reminderId: "reminder-1" });
  assert.equal(updated.isError, true);
  assert.equal(deleted.isError, true);
  assert.deepEqual(requests.map(({ request }) => request.method ?? "GET"), ["GET", "GET"]);
});

test("rejects unknown actions, missing fields, wrong types, and extras before adapters", async () => {
  const runner = fakeRunner();
  const requests = [];
  const tools = registered({
    runFile: runner.runFile,
    fetchImpl: async (...args) => { requests.push(args); return { ok: true, status: 200, json: async () => ({}) }; },
  });
  for (const [name, input, expected] of [
    ["mecha_worker", { action: "launch", task: "x" }, /unsupported field/],
    ["mecha_worker", { action: "launch" }, /alias is required/],
    ["mecha_worker", { action: "launch", alias: "bad.worker" }, /alias contains unsupported characters/],
    ["mecha_worker", { action: "send", worker: "x" }, /message is required/],
    ["mecha_worker", { action: "shutdown", worker: "x", verified: true }, /unsupported action/],
    ["mecha_pr", { action: "status", pullRequest: "12" }, /pullRequest must be an integer/],
    ["mecha_state", { action: "query", state: "paused" }, /state must be one of/],
    ["mecha_priority", { action: "status", priorityId: "p1", status: "unknown" }, /status must be one of/],
    ["mecha_verification_handoff", { action: "record", task: "x", reminderId: "r1", successor: "w1", remoteVerified: "yes" }, /remoteVerified must be one of/],
    ["mecha_git", { action: "status", task: "x", repo: "/tmp" }, /unsupported field/],
    ["mecha_router", { action: "retry", messageId: "x", command: "curl" }, /unsupported field/],
    ["mecha_reminder", { action: "create", target: "other", message: "x", delayMinutes: 1 }, /unsupported field/],
    ["mecha_reminder", { action: "update", reminderId: "r1", target: "other", message: "x" }, /unsupported field/],
  ]) {
    const result = await execute(tools.get(name), input);
    assert.equal(result.isError, true, name);
    assert.match(result.content[0].text, expected, name);
  }
  assert.equal(runner.calls.length, 0);
  assert.equal(requests.length, 0);
});

test("prefers adjacent supervisor tools when both layouts exist", (t) => {
  const root = mkdtempSync(join(tmpdir(), "operational-tools-"));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const extensionDirectory = join(root, "pi-extensions", "mecha-messaging");
  const adjacentTools = join(extensionDirectory, ".claude", "skills", "supervisor", "tools");
  mkdirSync(adjacentTools, { recursive: true });
  mkdirSync(join(root, ".claude", "skills", "supervisor", "tools"), { recursive: true });

  assert.equal(resolveSupervisorTools(extensionDirectory), adjacentTools);
});

test("falls back from the package directory to checkout supervisor tools", (t) => {
  const root = mkdtempSync(join(tmpdir(), "operational-tools-"));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const extensionDirectory = join(root, "pi-extensions", "mecha-messaging");
  const checkoutTools = join(root, ".claude", "skills", "supervisor", "tools");
  mkdirSync(checkoutTools, { recursive: true });

  assert.equal(resolveSupervisorTools(extensionDirectory), checkoutTools);
});

test("falls back when the package adjacent supervisor tools path is a regular file", (t) => {
  const root = mkdtempSync(join(tmpdir(), "operational-tools-"));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const extensionDirectory = join(root, "pi-extensions", "mecha-messaging");
  const adjacentTools = join(extensionDirectory, ".claude", "skills", "supervisor", "tools");
  mkdirSync(join(extensionDirectory, ".claude", "skills", "supervisor"), { recursive: true });
  writeFileSync(adjacentTools, "not a directory\n");
  const checkoutTools = join(root, ".claude", "skills", "supervisor", "tools");
  mkdirSync(checkoutTools, { recursive: true });

  assert.equal(resolveSupervisorTools(extensionDirectory), checkoutTools);
});

test("selects exact fixed script paths and passes safe argument arrays", async () => {
  const runner = fakeRunner((file, args) => {
    const script = basename(args[0]);
    if (script === "pr_status.py" || script === "git_task_status.py") return { stdout: "[]" };
    return { stdout: "ok" };
  });
  const tools = registered({ runFile: runner.runFile });
  const calls = [
    ["mecha_state", { action: "query", task: "task-1" }],
    ["mecha_state", { action: "create", alias: "task-2", type: "worker", project: "mecha", description: "Repair tools" }],
    ["mecha_state", { action: "update", task: "task-2", field: "summary", value: "Ready" }],
    ["mecha_priority", { action: "create", priorityId: "p1", name: "Ship", taskIds: [3] }],
    ["mecha_verification_handoff", { action: "record", task: "task-1", reminderId: "r1", successor: "worker-1" }],
    ["mecha_worker", { action: "list" }],
    ["mecha_worker", { action: "launch", alias: "worker-1" }],
    ["mecha_worker", { action: "cleanup", alias: "worker-1" }],
    ["mecha_worker", { action: "send", worker: "worker-1", message: "Check this" }],
    ["mecha_pr", { action: "status", pullRequest: 42 }],
    ["mecha_git", { action: "status", task: "task-1" }],
  ];
  for (const [name, input] of calls) {
    const result = await execute(tools.get(name), input);
    assert.equal(result.isError, undefined, `${name}: ${result.content[0].text}`);
  }
  const expectedDirectory = fileURLToPath(new URL("../.claude/skills/supervisor/tools", import.meta.url));
  assert.deepEqual(runner.calls.map(({ args }) => args[0]), [
    "state_query.py", "state_add.py", "state_update_status.py", "state_priority.py",
    "state_verification_handoff.py", "worker_list.py", "worker_lifecycle.py",
    "worker_lifecycle.py", "mailbox_send.py", "pr_status.py", "git_task_status.py",
  ].map((name) => join(expectedDirectory, name)));
  assert.equal(runner.calls[0].file, "python3");
  assert.deepEqual(runner.calls[0].args, [join(expectedDirectory, "state_query.py"), "--alias", "task-1"]);
  assert.equal(runner.calls[0].options.env, process.env);
  assert.deepEqual(runner.calls[6].args.slice(1), ["launch", "--alias", "worker-1"]);
  assert.deepEqual(runner.calls[7].args.slice(1), ["cleanup", "--alias", "worker-1"]);
  assert.deepEqual(runner.calls[8].args.slice(1), ["--to", "worker-1", "--body", "Check this"]);
  assert.deepEqual(runner.calls[9].args.slice(1), ["42", "--json"]);
  assert.deepEqual(runner.calls[10].args.slice(1), ["--task", "task-1", "--json"]);
});

test("injects runFile and fetchImpl through createMechaMessagingExtension", async () => {
  const runner = fakeRunner();
  const requests = [];
  const pi = messagingPi();
  createMechaMessagingExtension(pi, messagingOptions({
    operationalTools: {
      runFile: runner.runFile,
      fetchImpl: async (url, request) => {
        requests.push({ url, request });
        return { ok: true, status: 200, json: async () => ({ healthy: true }) };
      },
      environment: { MECHA_ROUTER_URL: "http://127.0.0.1:8765" },
    },
  }));
  const tools = new Map(pi.tools.map((tool) => [tool.name, tool]));
  await execute(tools.get("mecha_worker"), { action: "list" });
  await execute(tools.get("mecha_router"), { action: "health" });
  assert.equal(runner.calls.length, 1);
  assert.equal(requests.length, 1);
  assert.equal(requests[0].url, "http://127.0.0.1:8765/v1/health");
});

test("router actions are isolated to the injected HTTP adapter", async () => {
  const runner = fakeRunner();
  const requests = [];
  const tools = registered({
    runFile: runner.runFile,
    fetchImpl: async (url, request) => {
      requests.push({ url, request });
      return { ok: true, status: request.method === "DELETE" ? 204 : 200, json: async () => ({ state: "queued" }) };
    },
    environment: { MECHA_ROUTER_URL: "http://localhost:8765" },
  });
  await execute(tools.get("mecha_router"), { action: "retry", messageId: "message-1" });
  await execute(tools.get("mecha_router"), { action: "dismiss", messageId: "message-2" });
  assert.equal(runner.calls.length, 0);
  assert.deepEqual(requests.map(({ url, request }) => [url, request.method]), [
    ["http://localhost:8765/v1/messages/message-1/retry", "POST"],
    ["http://localhost:8765/v1/messages/message-2", "DELETE"],
  ]);
});

test("wraps top-level array router details and preserves top-level object router details", async () => {
  const arrayPayload = [{ id: "message-1" }, { id: "message-2" }];
  const arrayTools = registered({
    fetchImpl: async () => ({ ok: true, status: 200, json: async () => arrayPayload }),
  });
  const arrayResult = await execute(arrayTools.get("mecha_router"), { action: "health" });
  assert.deepEqual(arrayResult.details, { value: arrayPayload });

  const objectPayload = { healthy: true, attempts: 2 };
  const objectTools = registered({
    fetchImpl: async () => ({ ok: true, status: 200, json: async () => objectPayload }),
  });
  const objectResult = await execute(objectTools.get("mecha_router"), { action: "health" });
  assert.deepEqual(objectResult.details, objectPayload);
});

test("normalizes typed output and bounds successful and expected error responses", async () => {
  const long = "x".repeat(MAX_OPERATIONAL_OUTPUT + 100);
  const successTools = registered({ runFile: async () => ({ stdout: long }) });
  const successful = await execute(successTools.get("mecha_worker"), { action: "list" });
  assert.equal(successful.isError, undefined);
  assert.equal(successful.content[0].text.length, MAX_OPERATIONAL_OUTPUT + OPERATIONAL_TRUNCATION_MARKER.length);
  assert.ok(successful.content[0].text.endsWith(OPERATIONAL_TRUNCATION_MARKER));

  const expectedErrorTools = registered({
    fetchImpl: async () => ({
      ok: false,
      status: 500,
      json: async () => ({ error: { message: long } }),
    }),
  });
  const expectedFailure = await execute(expectedErrorTools.get("mecha_router"), { action: "health" });
  assert.equal(expectedFailure.isError, true);
  assert.equal(expectedFailure.content[0].text.length, MAX_OPERATIONAL_OUTPUT + OPERATIONAL_TRUNCATION_MARKER.length);
  assert.ok(expectedFailure.content[0].text.endsWith(OPERATIONAL_TRUNCATION_MARKER));

  const typedTools = registered({
    fetchImpl: async () => ({ ok: true, status: 200, json: async () => ({ healthy: true, attempts: 2 }) }),
  });
  const typed = await execute(typedTools.get("mecha_router"), { action: "health" });
  assert.equal(typed.content[0].text, '{"healthy":true,"attempts":2}');
  assert.deepEqual(typed.details, { healthy: true, attempts: 2 });
});

test("redacts complete credential values across JSON, CLI, and error fixtures", async () => {
  const fixtures = [
    {
      input: "Authorization: Bearer bearer-secret-value",
      expected: "Authorization: [redacted credential]",
      secrets: ["bearer-secret-value"],
    },
    {
      input: "api_key=api-secret-value",
      expected: "api_key=[redacted credential]",
      secrets: ["api-secret-value"],
    },
    {
      input: '{"password":"value with spaces"}',
      expected: '{"password":"[redacted credential]"}',
      secrets: ["value with spaces"],
    },
    {
      input: '{"client_secret":"escaped \\\"quoted\\\" value\\nsecond line"}',
      expected: '{"client_secret":"[redacted credential]"}',
      secrets: ["escaped", "quoted", "second line"],
    },
    {
      input: "password='literal multiline\nsecret value'",
      expected: "password='[redacted credential]'",
      secrets: ["literal multiline", "secret value"],
    },
    {
      input: 'command failed: --auth-token="CLI value with spaces and \\\"quotes\\\""',
      expected: 'command failed: --auth-token="[redacted credential]"',
      secrets: ["CLI value", "quotes"],
    },
    {
      input: 'error={"token":"error value with spaces"}',
      expected: 'error={"token":"[redacted credential]"}',
      secrets: ["error value"],
    },
    {
      input: "AWS_SECRET_ACCESS_KEY=environment-secret-value",
      expected: "AWS_SECRET_ACCESS_KEY=[redacted credential]",
      secrets: ["environment-secret-value"],
    },
    {
      input: "https://user:password-secret-value@example.test/path",
      expected: "https://[redacted credential]@example.test/path",
      secrets: ["password-secret-value"],
    },
    {
      input: "-----BEGIN PRIVATE KEY-----\nprivate-key-secret-value\n-----END PRIVATE KEY-----",
      expected: "[redacted private key]",
      secrets: ["private-key-secret-value"],
    },
  ];
  for (const { input, expected, secrets } of fixtures) {
    const tools = registered({ runFile: async () => ({ stdout: input }) });
    const result = await execute(tools.get("mecha_worker"), { action: "list" });
    assert.equal(result.content[0].text, expected, input);
    for (const secret of secrets) assert.equal(result.content[0].text.includes(secret), false, secret);
  }

  const detailTools = registered({
    fetchImpl: async () => ({
      ok: true,
      status: 200,
      json: async () => ({ healthy: true, token: "detail-secret-value", note: "password=note-secret-value" }),
    }),
  });
  const detailed = await execute(detailTools.get("mecha_router"), { action: "health" });
  assert.doesNotMatch(JSON.stringify(detailed), /detail-secret-value|note-secret-value/);
  assert.equal(detailed.details.token, "[redacted credential]");

  const expectedErrorTools = registered({
    fetchImpl: async () => ({
      ok: false,
      status: 500,
      json: async () => ({ error: { message: "token=expected-secret-value" } }),
    }),
  });
  const expectedError = await execute(expectedErrorTools.get("mecha_router"), { action: "health" });
  assert.equal(expectedError.isError, true);
  assert.match(expectedError.content[0].text, /\[redacted credential\]/);
  assert.doesNotMatch(JSON.stringify(expectedError), /expected-secret-value/);

  const unexpectedTools = registered({ runFile: async () => { throw new Error("token=unexpected-secret-value"); } });
  const unexpected = await execute(unexpectedTools.get("mecha_worker"), { action: "list" });
  assert.equal(unexpected.isError, true);
  assert.equal(unexpected.content[0].text, "Operational action failed.");
  assert.doesNotMatch(JSON.stringify(unexpected), /unexpected-secret-value/);
});

test("redacts credentials written to stderr by a successful script", async () => {
  const tools = registered({
    runFile: async () => ({
      stdout: "operation complete",
      stderr: "Authorization: Bearer successful-stderr-secret",
    }),
  });
  const result = await execute(tools.get("mecha_worker"), { action: "list" });
  assert.equal(result.isError, undefined);
  assert.match(result.content[0].text, /^operation complete\nAuthorization: \[redacted credential\]$/);
  assert.doesNotMatch(JSON.stringify(result), /successful-stderr-secret/);
});

test("preserves ordinary quoted and unquoted identifier fields while redacting credential fields", async () => {
  const ordinary = [
    "key=task-42",
    "MONKEY=capuchin",
    "token_count=3",
    "api_key_id=credential-record-7",
    "public_key_id=signing-record-9",
    '"key":"task 42"',
    '"token_count":"three tokens"',
    '"note":"password reset requested"',
    '"identifier":"api_key_id"',
    "--api-key-id=credential-record-8",
  ].join("\n");
  const textTools = registered({ runFile: async () => ({ stdout: ordinary }) });
  const textResult = await execute(textTools.get("mecha_worker"), { action: "list" });
  assert.equal(textResult.content[0].text, ordinary);

  const detailTools = registered({
    fetchImpl: async () => ({
      ok: true,
      status: 200,
      json: async () => ({
        key: "task-42",
        monkey: "capuchin",
        token_count: 3,
        api_key_id: "credential-record-7",
        api_key: "actual-secret-value",
      }),
    }),
  });
  const detailResult = await execute(detailTools.get("mecha_router"), { action: "health" });
  assert.deepEqual(detailResult.details, {
    key: "task-42",
    monkey: "capuchin",
    token_count: 3,
    api_key_id: "credential-record-7",
    api_key: "[redacted credential]",
  });
  assert.doesNotMatch(JSON.stringify(detailResult), /actual-secret-value/);
});

test("fails closed when ownership reads are non-ok or return the wrong reminder", async () => {
  for (const ownershipResponse of [
    { ok: false, status: 404, json: async () => ({}) },
    { ok: true, status: 200, json: async () => ({}) },
    { ok: true, status: 200, json: async () => ({ reminder: { id: "another-reminder", target: "worker" } }) },
  ]) {
    for (const action of ["update", "delete"]) {
      const requests = [];
      const tools = registered({
        resolveCurrentAgentId: () => "worker",
        fetchImpl: async (url, request = {}) => {
          requests.push({ url, request });
          return ownershipResponse;
        },
        environment: { MECHA_ROUTER_URL: "http://127.0.0.1:8765" },
      });
      const input = action === "update"
        ? { action, reminderId: "reminder-1", message: "updated" }
        : { action, reminderId: "reminder-1" };
      const result = await execute(tools.get("mecha_reminder"), input);
      assert.equal(result.isError, true, `${action}:${ownershipResponse.status}`);
      assert.equal(result.content[0].text, "Reminder ownership could not be verified; mutation denied.");
      assert.deepEqual(requests.map(({ request }) => request.method ?? "GET"), ["GET"]);
    }
  }
});

test("rejects arbitrary paths, repositories, working directories, and commands", async () => {
  const runner = fakeRunner();
  const tools = registered({ runFile: runner.runFile });
  for (const [name, input] of [
    ["mecha_worker", { action: "send", worker: "x", message: "hello", directory: "/tmp" }],
    ["mecha_worker", { action: "launch", alias: "x", directory: "/tmp" }],
    ["mecha_worker", { action: "cleanup", alias: "x", command: "tmux kill-server" }],
    ["mecha_pr", { action: "status", pullRequest: 3, repo: "evil/repo" }],
    ["mecha_git", { action: "status", task: "x", cwd: "/tmp" }],
    ["mecha_router", { action: "health", script: "curl" }],
  ]) {
    const result = await execute(tools.get(name), input);
    assert.equal(result.isError, true, name);
  }
  assert.equal(runner.calls.length, 0);
});

test("default definitions expose no generic execution surface", () => {
  const names = createOperationalToolDefinitions({ runFile: async () => ({ stdout: "ok" }) }).map((tool) => tool.name);
  assert.equal(names.includes("mecha_supervisor_runtime"), false);
  assert.equal(names.includes("shell"), false);
  assert.equal(names.includes("mecha_task"), false);
});
