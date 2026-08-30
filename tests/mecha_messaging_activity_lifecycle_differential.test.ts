import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import { mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";
import { promisify } from "node:util";

const execFileAsync = promisify(execFile);
const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const TSX = resolve(ROOT, "pi-extensions/mecha-messaging/node_modules/tsx/dist/cli.mjs");
const BASELINE = "0cdcaf74f";

// The differential fixture injects fixed IDs, clocks, and contexts. Array indexes are
// observation ordering, so they are removed before comparing against these exact object paths.
const INJECTED_PROCESS_FIELDS = new Set(["timestamp", "lease_id", "producer_epoch", "instance_id", "temporary_path"]);
const APPROVED_INJECTED_OBSERVATION_PATHS = new Set([
  ".fields.timestamp",
  ".fields.lease_id",
  ".fields.producer_epoch",
  ".fields.instance_id",
  ".fields.temporary_path",
]);
function isApprovedInjectedObservationPath(path: string): boolean {
  return APPROVED_INJECTED_OBSERVATION_PATHS.has(path.replace(/^\[\d+\]/, ""));
}

const INTENTIONAL_DIFFERENCE_ALLOWLIST = Object.freeze([
  {
    name: "injected process values",
    matches: (_scenario: string, path: string) => isApprovedInjectedObservationPath(path),
  },
  {
    name: "documented malformed TypeScript boundary",
    matches: (scenario: string, path: string) => scenario.startsWith("malformed-input:") && path === ".malformed",
  },
]);

const RUNNER_SOURCE = String.raw`
import path from "node:path";
import { pathToFileURL } from "node:url";

const side = process.argv[2];
const scenario = process.argv[3];
const sourceRoot = process.argv[4];
const baselineRoot = process.argv[5];
const modulePath = side === "baseline"
  ? path.join(baselineRoot, "mecha-messaging", "index.ts")
  : path.join(sourceRoot, "pi-extensions", "mecha-messaging", "index.ts");

const implementation = await import(pathToFileURL(modulePath).href);
const createExtension = implementation.createMechaMessagingExtension;
const createScheduler = implementation.createSettledNotificationScheduler;
const observations = [];

function clone(value) {
  if (value === undefined) return undefined;
  return JSON.parse(JSON.stringify(value));
}

function observe(value) {
  observations.push(clone(value));
}

function operation(name, callback) {
  observe({ kind: "operation", name });
  return callback();
}

function createClock() {
  let now = 0;
  let nextTimerId = 1;
  const timeouts = [];
  const intervals = [];

  function setTimeoutImpl(callback, delay) {
    const timer = { id: nextTimerId++, callback, delay, dueAt: now + delay, cleared: false };
    timeouts.push(timer);
    observe({ kind: "timer", action: "timeout-set", delay });
    return timer;
  }

  function clearTimeoutImpl(timer) {
    if (timer !== undefined) timer.cleared = true;
    observe({ kind: "timer", action: "timeout-clear" });
  }

  function setIntervalImpl(callback, delay) {
    const timer = { id: nextTimerId++, callback, delay, cleared: false, unref() {} };
    intervals.push(timer);
    observe({ kind: "timer", action: "interval-set", delay });
    return timer;
  }

  function clearIntervalImpl(timer) {
    if (timer !== undefined) timer.cleared = true;
    observe({ kind: "timer", action: "interval-clear" });
  }

  function advance(milliseconds) {
    now += milliseconds;
    for (const timer of timeouts.filter((candidate) => !candidate.cleared && candidate.dueAt <= now).sort((left, right) => left.dueAt - right.dueAt || left.id - right.id)) {
      timer.cleared = true;
      observe({ kind: "timer", action: "timeout-fire", delay: timer.delay });
      timer.callback();
    }
  }

  function heartbeat() {
    for (const timer of [...intervals]) {
      if (!timer.cleared) timer.callback();
    }
  }

  return { get now() { return now; }, setTimeoutImpl, clearTimeoutImpl, setIntervalImpl, clearIntervalImpl, advance, heartbeat, intervals };
}

function createSchedulerFactory(clock) {
  return () => {
    const scheduler = createScheduler({
      setTimeoutImpl: clock.setTimeoutImpl,
      clearTimeoutImpl: clock.clearTimeoutImpl,
    });
    return {
      onParentActivity() { observe({ kind: "scheduler", action: "parent-activity" }); scheduler.onParentActivity(); },
      onParentSettled(send) {
        observe({ kind: "scheduler", action: "parent-settled" });
        scheduler.onParentSettled(() => { observe({ kind: "scheduler", action: "send" }); send(); });
      },
      onChildStarted(id) { observe({ kind: "scheduler", action: "child-started", id: typeof id === "string" ? id : "" }); scheduler.onChildStarted(id); },
      onChildDisposed(id) { observe({ kind: "scheduler", action: "child-disposed", id: typeof id === "string" ? id : "" }); scheduler.onChildDisposed(id); },
      dispose() { observe({ kind: "scheduler", action: "dispose" }); scheduler.dispose(); },
      activeChildCount() { return scheduler.activeChildCount(); },
    };
  };
}

function createPi() {
  const handlers = new Map();
  const eventHandlers = new Map();
  const clients = [];
  let clientNumber = 0;
  let lifecycleNumber = 0;

  const add = (map, name, handler) => {
    const bucket = map.get(name) ?? [];
    bucket.push(handler);
    map.set(name, bucket);
  };

  const pi = {
    handlers,
    eventHandlers,
    clients,
    events: {
      on(name, handler) { add(eventHandlers, name, handler); },
      emit(name, event) {
        for (const handler of eventHandlers.get(name) ?? []) handler(event);
      },
    },
    on(name, handler) { add(handlers, name, handler); },
    appendEntry() {},
    registerTool() {},
    registerCommand() {},
    sendMessage() {},
    exec: async () => ({ code: 0, stderr: "" }),
    makeClient(options) {
      const number = ++clientNumber;
      observe({ kind: "client", action: "created", number });
      const client = {
        instanceId: "fixed-instance",
        currentLeaseId: () => "fixed-lease",
        connect() { observe({ kind: "client", action: "connect", number }); return Promise.resolve(); },
        close() { observe({ kind: "client", action: "close", number }); },
        sendGoalState() { observe({ kind: "client", action: "goal-state", number }); },
        sendActivity(state, payload) { observe({ kind: "activity", state, payload }); },
        sendLifecycleSnapshot(subagents) {
          const eventId = "fixed-snapshot-" + (++lifecycleNumber);
          observe({ kind: "lifecycle-snapshot", eventId, subagents });
          return eventId;
        },
        sendLifecycleEvent(event) {
          observe({ kind: "lifecycle-event", event });
          return "fixed-lifecycle-event";
        },
        sendReply: async () => ({ id: "fixed-reply" }),
        sendReceipt() {},
      };
      clients.push({ client, options });
      return client;
    },
  };
  return pi;
}

function createContext(sessionId, state) {
  return {
    sessionManager: {
      getSessionId: () => sessionId,
      getBranch: () => [],
      isPersisted: () => false,
      getSessionFile: () => undefined,
    },
    model: { provider: "fake-provider", id: "fake-model", name: "Fake Model", contextWindow: 4096, maxTokens: 128 },
    ui: {
      notify(message, level) { observe({ kind: "ui", action: "notify", message, level }); },
      setStatus(key, value) { observe({ kind: "ui", action: "status", key, value }); },
    },
    isIdle: () => state.idle,
  };
}

function serviceWithShape(shape, records, state) {
  if (shape === "listAgents") return { listAgents: () => { if (state.failReads) throw new Error("read failed"); return records; } };
  if (shape === "getRunning") return { getRunning: () => records };
  return { [shape]: records };
}

function createHarness(serviceByOwner) {
  const clock = createClock();
  const pi = createPi();
  const state = { idle: false };
  const contexts = {
    parent: createContext("parent-session", state),
    child: createContext("child-session", state),
  };
  let scheduler;
  const extension = createExtension(pi, {
    resolveCurrentAgentId: () => "fixed-worker",
    createClient: (options) => pi.makeClient(options),
    createSettledScheduler: () => {
      scheduler = createSchedulerFactory(clock)();
      return scheduler;
    },
    setTimeoutImpl: clock.setTimeoutImpl,
    clearTimeoutImpl: clock.clearTimeoutImpl,
    setIntervalImpl: clock.setIntervalImpl,
    clearIntervalImpl: clock.clearIntervalImpl,
    nowImpl: () => clock.now,
    getSubagentsService: (ownerSessionId) => serviceByOwner[ownerSessionId],
    nativeSubagentTrace: {
      record(event, fields) {
        if (event.startsWith("extension_service_") || event.startsWith("extension_snapshot_")
          || event.startsWith("extension_native_event_") || event === "extension_lifecycle_heartbeat") {
          observe({ kind: "trace", event, fields });
        }
        return true;
      },
    },
  });
  void extension;

  async function root(name, event, context = contexts.parent) {
    for (const handler of pi.handlers.get(name) ?? []) await handler(event, context);
  }

  async function native(name, event) {
    for (const handler of pi.eventHandlers.get(name) ?? []) await handler(event);
  }

  return { clock, contexts, native, pi, root, scheduler: () => scheduler, state };
}

async function settledScheduler() {
  const clock = createClock();
  const scheduler = createSchedulerFactory(clock)();
  await operation("parent-settled", () => scheduler.onParentSettled(() => {}));
  await operation("cooldown-before-cancel", () => clock.advance(119_999));
  await operation("parent-activity-cancels", () => scheduler.onParentActivity());
  await operation("cooldown-cancellation-holds", () => clock.advance(1));
  await operation("parent-settled-sends", () => scheduler.onParentSettled(() => {}));
  await operation("cooldown-elapses", () => clock.advance(120_000));
  await operation("child-starts", () => scheduler.onChildStarted("fixed-child"));
  await operation("settled-with-child", () => scheduler.onParentSettled(() => {}));
  await operation("child-disposes", () => scheduler.onChildDisposed("fixed-child"));
  await operation("dispose-does-not-retroactively-send", () => scheduler.dispose());
  await operation("cooldown-after-dispose", () => clock.advance(120_000));
}

async function typedTerminal() {
  const records = [{ id: "typed-child", type: "worker", status: "running", description: "typed label", prompt: "private prompt", output: "private output", error: "private error", startedAt: 100 }];
  const harness = createHarness({ "parent-session": { getLifecycleSnapshots: () => records } });
  await operation("session-start", () => harness.root("session_start", {}));
  await operation("goal-projection", () => harness.pi.events.emit("goal:state", { enabled: true, goal: "ship lifecycle parity" }));
  await operation("agent-start", () => harness.root("agent_start", {}));
  await operation("heartbeat-before-terminal", () => harness.clock.heartbeat());
  await operation("native-start", () => harness.native("subagents:started", { id: "typed-child", type: "worker", status: "running", description: "typed label" }));
  await operation("native-terminal", () => harness.native("subagents:completed", { id: "typed-child", status: "completed", output: "private output" }));
  await operation("pre-heartbeat-terminal-observation", () => {});
  records.length = 0;
  await operation("heartbeat-omits-terminal", () => harness.clock.heartbeat());
  harness.state.idle = true;
  await operation("agent-settled", () => harness.root("agent_settled", {}));
  await operation("post-settle-heartbeat", () => harness.clock.heartbeat());
}

async function legacyReconciliation() {
  const records = [];
  const state = { failReads: false };
  const harness = createHarness({ "parent-session": serviceWithShape("listAgents", records, state) });
  await operation("session-start", () => harness.root("session_start", {}));
  await operation("agent-start", () => harness.root("agent_start", {}));
  await operation("event-only-start", () => harness.native("subagents:started", { id: "event-child", type: "worker", description: "event label" }));
  await operation("heartbeat-event-only", () => harness.clock.heartbeat());
  harness.clock.advance(59_999);
  await operation("heartbeat-before-missing-expiry", () => harness.clock.heartbeat());
  harness.clock.advance(1);
  await operation("heartbeat-at-missing-expiry", () => harness.clock.heartbeat());

  records.push({ id: "service-child", status: "running", description: "service label", startedAt: 70_000 });
  await operation("service-only-appears", () => harness.clock.heartbeat());
  records.length = 0;
  await operation("service-only-omitted", () => harness.clock.heartbeat());

  harness.clock.advance(20_000);
  records.push({ id: "tomb-child", status: "running", description: "tomb label", startedAt: 80_000 });
  await operation("tomb-child-appears", () => harness.clock.heartbeat());
  await operation("legacy-terminal-removes-now", () => harness.native("subagents:failed", { id: "tomb-child", status: "failed" }));
  records[0] = { id: "tomb-child", status: "running", description: "stale resurrection", startedAt: 80_000 };
  await operation("stale-row-is-fenced", () => harness.clock.heartbeat());
  harness.clock.advance(1);
  records[0] = { id: "tomb-child", status: "running", description: "reused child", startedAt: 80_001 };
  await operation("new-start-time-reuses-id", () => harness.clock.heartbeat());

  records.length = 0;
  records.push({ id: "unnamed", status: "running" });
  await operation("unnamed-counts", () => harness.clock.heartbeat());
  records.length = 0;
  await operation("unnamed-omitted", () => harness.clock.heartbeat());

  records.push({ id: "alias-active", status: "active", description: "alias label" });
  records.push({ id: "alias-failed", status: "failed", description: "failed label" });
  await operation("legacy-status-aliases", () => harness.clock.heartbeat());
}

async function legacyFallbacks() {
  for (const shape of ["getRunning", "running", "runs", "subagents"]) {
    const records = [{ id: "fallback-" + shape, status: "running", description: shape + " label" }];
    const harness = createHarness({ "parent-session": serviceWithShape(shape, records, { failReads: false }) });
    await operation("fallback-" + shape + "-session-start", () => harness.root("session_start", {}));
    await operation("fallback-" + shape + "-heartbeat", () => harness.clock.heartbeat());
  }
}

async function readFailure() {
  const records = [];
  const state = { failReads: true };
  const harness = createHarness({ "parent-session": serviceWithShape("listAgents", records, state) });
  await operation("session-start", () => harness.root("session_start", {}));
  await operation("read-failure-1", () => harness.clock.heartbeat());
  await operation("read-failure-2", () => harness.clock.heartbeat());
  harness.clock.advance(59_999);
  await operation("read-failure-before-rate-limit", () => harness.clock.heartbeat());
  harness.clock.advance(1);
  await operation("read-failure-after-rate-limit", () => harness.clock.heartbeat());
  state.failReads = false;
  records.push({ id: "recovered", status: "running", description: "recovered label" });
  await operation("read-recovery", () => harness.clock.heartbeat());
}

async function ownerScope() {
  const parentRecords = [{ id: "parent-child", status: "running", description: "parent label" }];
  const childRecords = [{ id: "wrong-child", status: "running", description: "wrong owner" }];
  const harness = createHarness({
    "parent-session": { getLifecycleSnapshots: () => parentRecords },
    "child-session": { getLifecycleSnapshots: () => childRecords },
  });
  await operation("parent-session-start", () => harness.root("session_start", {}, harness.contexts.parent));
  await operation("parent-heartbeat", () => harness.clock.heartbeat());
  await operation("child-session-created", () => harness.native("subagents:child:session-created", { sessionId: "child-session" }));
  await operation("child-session-start", () => harness.root("session_start", {}, harness.contexts.child));
  await operation("child-native-start-event", () => harness.native("subagents:started", { id: "wrong-child", status: "running", description: "wrong owner" }));
  await operation("parent-heartbeat-after-child", () => harness.clock.heartbeat());
}

async function compactionBoundary() {
  const records = [{ id: "compact-child", type: "worker", status: "running", description: "compact label" }];
  const harness = createHarness({ "parent-session": { getLifecycleSnapshots: () => records } });
  await operation("session-start", () => harness.root("session_start", {}));
  await operation("agent-start", () => harness.root("agent_start", {}));
  await operation("heartbeat-before-compaction", () => harness.clock.heartbeat());
  await operation("scheduler-before-compaction", () => observe({ kind: "scheduler-state", children: harness.scheduler()?.activeChildCount() }));
  const abortedSignal = { aborted: true, addEventListener() {} };
  await operation("compaction-start", () => harness.root("session_before_compact", { signal: abortedSignal, preparation: {} }));
  await operation("scheduler-after-compaction", () => observe({ kind: "scheduler-state", children: harness.scheduler()?.activeChildCount() }));
  await operation("compaction-resume", () => harness.root("session_compact", {}));
  await operation("heartbeat-after-resume", () => harness.clock.heartbeat());
  await operation("session-shutdown", () => harness.root("session_shutdown", {}));
  await operation("scheduler-after-shutdown", () => observe({ kind: "scheduler-state", children: harness.scheduler()?.activeChildCount() }));
}

async function startupShutdown() {
  const harness = createHarness({ "parent-session": undefined });
  await operation("parent-startup", () => harness.root("session_start", {}));
  await operation("agent-start", () => harness.root("agent_start", {}));
  await operation("parent-shutdown", () => harness.root("session_shutdown", {}));
}

async function reconnectCapabilityBoundary() {
  const typedRecords = [{ id: "reconnect-child", status: "running", description: "reconnect label" }];
  const typed = createHarness({ "parent-session": { getLifecycleSnapshots: () => typedRecords } });
  await operation("typed-session-start", () => typed.root("session_start", {}));
  await operation("typed-heartbeat-before-reconnect", () => typed.clock.heartbeat());
  const typedClient = typed.pi.clients[0];
  await operation("typed-reconnect", () => typedClient.options.onConnectionState?.({ state: "reconnecting" }));
  await operation("typed-reconnected", () => typedClient.options.onConnectionState?.({ state: "connected" }));
  await operation("typed-heartbeat-after-reconnect", () => typed.clock.heartbeat());

  const legacyRecords = [{ id: "legacy-capability", status: "running", description: "legacy label" }];
  const legacy = createHarness({ "parent-session": { listAgents: () => legacyRecords } });
  await operation("legacy-session-start", () => legacy.root("session_start", {}));
  await operation("legacy-heartbeat", () => legacy.clock.heartbeat());
}

const scenarios = { scheduler: settledScheduler, "typed-terminal": typedTerminal, "legacy-reconciliation": legacyReconciliation, "legacy-fallbacks": legacyFallbacks, "read-failure": readFailure, "owner-scope": ownerScope, "compaction-boundary": compactionBoundary, "startup-shutdown": startupShutdown, "reconnect-capability-boundary": reconnectCapabilityBoundary };
if (typeof scenarios[scenario] !== "function") throw new Error("unknown scenario: " + scenario);
await scenarios[scenario]();
process.stdout.write(JSON.stringify(observations) + "\n");
`;

type Observation = unknown;

type Difference = { path: string; left: unknown; right: unknown };

function collectDifferences(left: unknown, right: unknown, path = "", differences: Difference[] = []): Difference[] {
  if (Object.is(left, right)) return differences;
  if (Array.isArray(left) || Array.isArray(right)) {
    if (!Array.isArray(left) || !Array.isArray(right)) {
      differences.push({ path, left, right });
      return differences;
    }
    if (left.length !== right.length) differences.push({ path: `${path}.length`, left: left.length, right: right.length });
    const length = Math.min(left.length, right.length);
    for (let index = 0; index < length; index += 1) collectDifferences(left[index], right[index], `${path}[${index}]`, differences);
    return differences;
  }
  if (left !== null && right !== null && typeof left === "object" && typeof right === "object") {
    const leftRecord = left as Record<string, unknown>;
    const rightRecord = right as Record<string, unknown>;
    const keys = [...new Set([...Object.keys(leftRecord), ...Object.keys(rightRecord)])].sort();
    for (const key of keys) collectDifferences(leftRecord[key], rightRecord[key], `${path}.${key}`, differences);
    return differences;
  }
  differences.push({ path, left, right });
  return differences;
}

function normalize(value: unknown, path = ""): unknown {
  if (Array.isArray(value)) return value.map((item, index) => normalize(item, `${path}[${index}]`));
  if (value !== null && typeof value === "object") {
    return Object.fromEntries(Object.entries(value).map(([entryKey, entryValue]) => {
      const entryPath = `${path}.${entryKey}`;
      const normalized = typeof entryValue === "string"
        && isApprovedInjectedObservationPath(entryPath)
        ? `<${entryKey}>`
        : normalize(entryValue, entryPath);
      return [entryKey, normalized];
    }));
  }
  return value;
}

function compareScenario(scenario: string, baselineObservations: Observation, currentObservations: Observation): void {
  const differences = collectDifferences(normalize(baselineObservations), normalize(currentObservations));
  const difference = differences.find((candidate) => !INTENTIONAL_DIFFERENCE_ALLOWLIST.some((entry) => entry.matches(scenario, candidate.path)));
  if (difference === undefined) return;
  assert.fail(`unlisted lifecycle mismatch in ${scenario} at ${difference.path}: ${JSON.stringify(difference.left)} != ${JSON.stringify(difference.right)}`);
}

function observationRecords(value: Observation): Array<Record<string, unknown>> {
  assert.ok(Array.isArray(value), "child process did not return an observation sequence");
  return value.map((item) => {
    assert.ok(item !== null && typeof item === "object" && !Array.isArray(item), "observation is not an object");
    return item as Record<string, unknown>;
  });
}

function operationSegment(rows: Array<Record<string, unknown>>, name: string): Array<Record<string, unknown>> {
  const start = rows.findIndex((row) => row.kind === "operation" && row.name === name);
  assert.ok(start >= 0, `missing operation ${name}`);
  const end = rows.findIndex((row, index) => index > start && row.kind === "operation");
  return rows.slice(start + 1, end < 0 ? rows.length : end);
}

function activityRows(rows: Array<Record<string, unknown>>): Array<Record<string, unknown>> {
  return rows.filter((row) => row.kind === "activity");
}

function assertScenarioCoverage(scenario: string, value: Observation): void {
  const rows = observationRecords(value);
  const serialized = JSON.stringify(rows);
  if (scenario === "scheduler") {
    assert.equal(rows.filter((row) => row.kind === "scheduler" && row.action === "send").length, 1);
    assert.ok(rows.some((row) => row.kind === "timer" && row.action === "timeout-clear"));
    return;
  }
  if (scenario === "typed-terminal") {
    assert.equal(rows.filter((row) => row.kind === "lifecycle-event").length, 0);
    const beforeTerminal = operationSegment(rows, "heartbeat-before-terminal");
    assert.ok(beforeTerminal.some((row) => row.kind === "activity" && JSON.stringify(row.payload).includes("typed-child")));
    const preHeartbeat = operationSegment(rows, "pre-heartbeat-terminal-observation");
    assert.equal(preHeartbeat.filter((row) => row.kind === "activity" || row.kind === "lifecycle-snapshot").length, 0);
    const omission = operationSegment(rows, "heartbeat-omits-terminal");
    const omissionSnapshot = omission.find((row) => row.kind === "lifecycle-snapshot");
    assert.ok(omissionSnapshot !== undefined);
    assert.deepEqual(omissionSnapshot.subagents, []);
    assert.ok(omission.some((row) => row.kind === "activity" && !JSON.stringify(row.payload).includes("typed-child")));
    assert.ok(serialized.includes("typed-child"));
    assert.equal(serialized.includes("private prompt"), false);
    assert.equal(serialized.includes("private output"), false);
    assert.equal(serialized.includes("private error"), false);
    return;
  }
  if (scenario === "legacy-reconciliation") {
    assert.equal(rows.filter((row) => row.kind === "lifecycle-snapshot").length, 0);
    assert.ok(JSON.stringify(activityRows(operationSegment(rows, "event-only-start"))).includes("event-child"));
    const immediateRemovalRows = activityRows(operationSegment(rows, "legacy-terminal-removes-now"));
    assert.ok(immediateRemovalRows.length > 0);
    assert.equal(JSON.stringify(immediateRemovalRows).includes("tomb-child"), false);
    const staleRows = JSON.stringify(activityRows(operationSegment(rows, "stale-row-is-fenced")));
    assert.equal(staleRows.includes("stale resurrection"), false);
    assert.equal(staleRows.includes("tomb-child"), false);
    const reusedRows = JSON.stringify(activityRows(operationSegment(rows, "new-start-time-reuses-id")));
    assert.ok(reusedRows.includes("tomb-child"));
    assert.ok(reusedRows.includes("reused child"));
    assert.ok(activityRows(operationSegment(rows, "unnamed-counts")).some((row) => {
      const payload = row.payload;
      return payload !== null && typeof payload === "object" && !Array.isArray(payload)
        && (payload as Record<string, unknown>).subagentCount === 1
        && JSON.stringify((payload as Record<string, unknown>).subagents) === "[]";
    }));
    return;
  }
  if (scenario === "legacy-fallbacks") {
    for (const shape of ["getRunning", "running", "runs", "subagents"]) {
      assert.ok(JSON.stringify(operationSegment(rows, `fallback-${shape}-heartbeat`)).includes(`fallback-${shape}`));
    }
    return;
  }
  if (scenario === "read-failure") {
    assert.equal(rows.filter((row) => row.kind === "ui" && row.action === "notify").length, 2);
    assert.ok(JSON.stringify(operationSegment(rows, "read-recovery")).includes("recovered"));
    return;
  }
  if (scenario === "owner-scope") {
    assert.ok(serialized.includes("parent-child"));
    assert.equal(serialized.includes("wrong owner"), false);
    return;
  }
  if (scenario === "compaction-boundary") {
    const compactionStart = activityRows(operationSegment(rows, "compaction-start"));
    assert.equal(compactionStart[0]?.state, "compacting");
    const schedulerStates = rows.filter((row) => row.kind === "scheduler-state");
    assert.ok((schedulerStates[0]?.children as number) > 0);
    assert.equal(schedulerStates[0]?.children, schedulerStates[1]?.children);
    assert.equal(schedulerStates.at(-1)?.children, 0);
    assert.equal(rows.filter((row) => row.kind === "timer" && row.action === "interval-set").length, 2);
    assert.equal(rows.filter((row) => row.kind === "timer" && row.action === "interval-clear").length, 2);
    return;
  }
  if (scenario === "startup-shutdown") {
    const startup = rows.findIndex((row) => row.kind === "operation" && row.name === "parent-startup");
    const goal = rows.findIndex((row, index) => index > startup && row.kind === "client" && row.action === "goal-state");
    const interval = rows.findIndex((row, index) => index > goal && row.kind === "timer" && row.action === "interval-set");
    const connect = rows.findIndex((row, index) => index > interval && row.kind === "client" && row.action === "connect");
    assert.ok(startup >= 0 && goal > startup && interval > goal && connect > interval);
    const shutdown = rows.findIndex((row) => row.kind === "operation" && row.name === "parent-shutdown");
    const settled = rows.findIndex((row, index) => index > shutdown && row.kind === "activity" && row.state === "settled");
    const clear = rows.findIndex((row, index) => index > settled && row.kind === "timer" && row.action === "interval-clear");
    const dispose = rows.findIndex((row, index) => index > clear && row.kind === "scheduler" && row.action === "dispose");
    const close = rows.findIndex((row, index) => index > dispose && row.kind === "client" && row.action === "close");
    assert.ok(shutdown >= 0 && settled > shutdown && clear > settled && dispose > clear && close > dispose);
    return;
  }
  if (scenario === "reconnect-capability-boundary") {
    assert.equal(rows.filter((row) => row.kind === "lifecycle-snapshot").length, 2);
    assert.equal(rows.filter((row) => row.kind === "lifecycle-event").length, 0);
    return;
  }
  assert.fail(`no coverage assertions for scenario ${scenario}`);
}

async function materializeBaseline(directory: string): Promise<string> {
  const archive = join(directory, "baseline.tar");
  await execFileAsync("git", ["archive", "--format=tar", "--output", archive, BASELINE, "pi-extensions"], { cwd: ROOT });
  await execFileAsync("tar", ["-xf", archive, "-C", directory], { cwd: ROOT });
  return join(directory, "pi-extensions");
}

async function runChild(runner: string, side: "baseline" | "new", scenario: string, baselineRoot: string): Promise<Observation> {
  const result = await execFileAsync(process.execPath, [TSX, runner, side, scenario, ROOT, baselineRoot], {
    cwd: ROOT,
    env: { HOME: dirname(baselineRoot), MECHA_PI_NOTIFY_ON_SETTLED: "", MECHA_ROUTER_URL: "http://127.0.0.1:1" },
    maxBuffer: 8 * 1024 * 1024,
  });
  const lines = result.stdout.trim().split("\n");
  return JSON.parse(lines.at(-1) ?? "null") as Observation;
}

const SCENARIOS = [
  "scheduler",
  "typed-terminal",
  "legacy-reconciliation",
  "legacy-fallbacks",
  "read-failure",
  "owner-scope",
  "compaction-boundary",
  "startup-shutdown",
  "reconnect-capability-boundary",
] as const;

test("pre-extraction TypeScript and current TypeScript lifecycle observations stay in parity", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-lifecycle-differential-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const baselineRoot = await materializeBaseline(directory);
  const runner = join(directory, "runner.mjs");
  await writeFile(runner, RUNNER_SOURCE, "utf8");

  for (const scenario of SCENARIOS) {
    const [baselineObservations, currentObservations] = await Promise.all([
      runChild(runner, "baseline", scenario, baselineRoot),
      runChild(runner, "new", scenario, baselineRoot),
    ]);
    compareScenario(scenario, baselineObservations, currentObservations);
    assertScenarioCoverage(scenario, baselineObservations);
    assertScenarioCoverage(scenario, currentObservations);
  }
});

test("the differential allowlist accepts only exact injected observation paths", () => {
  for (const field of INJECTED_PROCESS_FIELDS) {
    compareScenario("scheduler", [{ fields: { [field]: "left" } }], [{ fields: { [field]: "right" } }]);
  }
});

test("the differential allowlist rejects unauthorized identity-field mutations", () => {
  for (const field of INJECTED_PROCESS_FIELDS) {
    assert.throws(
      () => compareScenario("scheduler", [{ [field]: "left" }], [{ [field]: "right" }]),
      new RegExp(`unlisted lifecycle mismatch.*\\.${field}`),
    );
    assert.throws(
      () => compareScenario("scheduler", [{ [`foo_${field}`]: "left" }], [{ [`foo_${field}`]: "right" }]),
      /unlisted lifecycle mismatch/,
    );
    assert.throws(
      () => compareScenario("scheduler", [{ fields: [{ [field]: "left" }] }], [{ fields: [{ [field]: "right" }] }]),
      /unlisted lifecycle mismatch/,
    );
  }
});

test("the differential comparator rejects an unlisted lifecycle publication", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-lifecycle-differential-mutation-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const baselineRoot = await materializeBaseline(directory);
  const runner = join(directory, "runner.mjs");
  await writeFile(runner, RUNNER_SOURCE, "utf8");
  const [baselineObservations, currentObservations] = await Promise.all([
    runChild(runner, "baseline", "typed-terminal", baselineRoot),
    runChild(runner, "new", "typed-terminal", baselineRoot),
  ]);
  const mutated = [...(currentObservations as unknown[])];
  const terminalIndex = mutated.findIndex((observation) => (
    observation !== null && typeof observation === "object" && !Array.isArray(observation)
      && (observation as Record<string, unknown>).kind === "operation"
      && (observation as Record<string, unknown>).name === "native-terminal"
  ));
  assert.ok(terminalIndex >= 0);
  mutated.splice(terminalIndex + 1, 0, { kind: "lifecycle-snapshot", eventId: "unapproved-terminal-event", subagents: [] });
  assert.throws(() => compareScenario("typed-terminal", baselineObservations, mutated), /unlisted lifecycle mismatch/);
});

test("lifecycle coverage assertions fail under representative mutations", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-lifecycle-coverage-mutation-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const baselineRoot = await materializeBaseline(directory);
  const runner = join(directory, "runner.mjs");
  await writeFile(runner, RUNNER_SOURCE, "utf8");

  const runCurrent = (scenario: string) => runChild(runner, "new", scenario, baselineRoot);
  const typed = observationRecords(JSON.parse(JSON.stringify(await runCurrent("typed-terminal"))) as Observation);
  const typedBefore = operationSegment(typed, "heartbeat-before-terminal").find((row) => row.kind === "activity");
  assert.ok(typedBefore !== undefined);
  typedBefore.payload = { subagentCount: 0, subagents: [] };
  assert.throws(() => assertScenarioCoverage("typed-terminal", typed));

  const omission = observationRecords(JSON.parse(JSON.stringify(await runCurrent("typed-terminal"))) as Observation);
  const omissionSnapshot = operationSegment(omission, "heartbeat-omits-terminal").find((row) => row.kind === "lifecycle-snapshot");
  assert.ok(omissionSnapshot !== undefined);
  omissionSnapshot.subagents = [{ id: "typed-child", status: "running" }];
  assert.throws(() => assertScenarioCoverage("typed-terminal", omission));

  const compaction = observationRecords(JSON.parse(JSON.stringify(await runCurrent("compaction-boundary"))) as Observation);
  const compactionActivity = activityRows(operationSegment(compaction, "compaction-start"))[0];
  assert.ok(compactionActivity !== undefined);
  compactionActivity.state = "active";
  assert.throws(() => assertScenarioCoverage("compaction-boundary", compaction));

  const tombstone = observationRecords(JSON.parse(JSON.stringify(await runCurrent("legacy-reconciliation"))) as Observation);
  const immediateRemoval = activityRows(operationSegment(tombstone, "legacy-terminal-removes-now"))[0];
  assert.ok(immediateRemoval !== undefined);
  immediateRemoval.payload = { subagentCount: 1, subagents: [{ id: "tomb-child", description: "stale resurrection" }] };
  assert.throws(() => assertScenarioCoverage("legacy-reconciliation", tombstone));

  const reused = activityRows(operationSegment(tombstone, "new-start-time-reuses-id"))[0];
  assert.ok(reused !== undefined);
  reused.payload = { subagentCount: 0, subagents: [] };
  assert.throws(() => assertScenarioCoverage("legacy-reconciliation", tombstone));

  const ordering = observationRecords(JSON.parse(JSON.stringify(await runCurrent("startup-shutdown"))) as Observation);
  const startup = ordering.findIndex((row) => row.kind === "operation" && row.name === "parent-startup");
  const goal = ordering.findIndex((row, index) => index > startup && row.kind === "client" && row.action === "goal-state");
  const interval = ordering.findIndex((row, index) => index > goal && row.kind === "timer" && row.action === "interval-set");
  assert.ok(startup >= 0 && goal > startup && interval > goal);
  [ordering[goal], ordering[interval]] = [ordering[interval], ordering[goal]];
  assert.throws(() => assertScenarioCoverage("startup-shutdown", ordering));

  const shutdownOrdering = observationRecords(JSON.parse(JSON.stringify(await runCurrent("startup-shutdown"))) as Observation);
  const shutdown = shutdownOrdering.findIndex((row) => row.kind === "operation" && row.name === "parent-shutdown");
  const settled = shutdownOrdering.findIndex((row, index) => index > shutdown && row.kind === "activity" && row.state === "settled");
  const clear = shutdownOrdering.findIndex((row, index) => index > settled && row.kind === "timer" && row.action === "interval-clear");
  assert.ok(shutdown >= 0 && settled > shutdown && clear > settled);
  [shutdownOrdering[settled], shutdownOrdering[clear]] = [shutdownOrdering[clear], shutdownOrdering[settled]];
  assert.throws(() => assertScenarioCoverage("startup-shutdown", shutdownOrdering));
});

// Keep the baseline materialization assertion tied to the exact requested source identity. This
// also prevents a future test edit from silently switching back to a history-only JavaScript file.
test("the differential harness names the exact pre-extraction TypeScript baseline", async () => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-lifecycle-baseline-"));
  try {
    const baselineRoot = await materializeBaseline(directory);
    const source = await readFile(join(baselineRoot, "mecha-messaging", "index.ts"), "utf8");
    assert.match(source, /export function createMechaMessagingExtension/);
    assert.match(source, /function publishTerminalLifecycleEvent/);
    assert.doesNotMatch(source, /createActivityLifecycle/);
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
});
