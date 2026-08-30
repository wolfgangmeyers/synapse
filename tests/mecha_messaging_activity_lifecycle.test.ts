import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import test from "node:test";

import {
  createActivityLifecycle,
  createSettledNotificationScheduler,
  summarizeNativeSubagents,
} from "../pi-extensions/mecha-messaging/activity-lifecycle.ts";
import { createMailboxClient } from "../pi-extensions/mecha-messaging/mailbox-client.ts";
import { createMechaMessagingExtension } from "../pi-extensions/mecha-messaging/index.ts";
import type {
  ActivityContext,
  NativeSubagent,
  NativeSubagentService,
  SettledNotificationScheduler,
} from "../pi-extensions/mecha-messaging/activity-lifecycle.ts";

type FakeTimer = { callback: () => void };

type ActivityFixture = {
  lifecycle: ReturnType<typeof createActivityLifecycle>;
  context: ActivityContext;
  service: NativeSubagentService;
  setService: (service: NativeSubagentService) => void;
  setNow: (value: number) => void;
  records: NativeSubagent[];
  activities: Array<{ state: string; payload: unknown }>;
  snapshots: unknown[];
  traces: Array<{ event: string; fields?: object }>;
  notifications: Array<{ message: string; level?: string }>;
  intervals: FakeTimer[];
  clearedIntervals: number;
  scheduler: SettledNotificationScheduler;
  schedulerCalls: string[];
};

function activityFixture(initialService: NativeSubagentService = {}): ActivityFixture {
  let now = 0;
  let service = initialService;
  const records: NativeSubagent[] = [];
  const activities: Array<{ state: string; payload: unknown }> = [];
  const snapshots: unknown[] = [];
  const traces: Array<{ event: string; fields?: object }> = [];
  const notifications: Array<{ message: string; level?: string }> = [];
  const intervals: FakeTimer[] = [];
  let clearedIntervals = 0;
  const context: ActivityContext = {
    sessionManager: { getSessionId: () => "parent-session" },
    isIdle: () => false,
    ui: { notify: (message, level) => notifications.push({ message, level }) },
  };
  const schedulerCalls: string[] = [];
  const scheduler = createSettledNotificationScheduler({
    setTimeoutImpl: (_callback, _delay) => setTimeout(() => {}, 60_000),
    clearTimeoutImpl: (handle) => { clearTimeout(handle); },
  });
  const schedulerSpy: SettledNotificationScheduler = {
    onParentActivity() { schedulerCalls.push("parent-activity"); scheduler.onParentActivity(); },
    onParentSettled(send) { schedulerCalls.push("parent-settled"); scheduler.onParentSettled(send); },
    onChildStarted(sessionId) { schedulerCalls.push(`child-started:${sessionId}`); scheduler.onChildStarted(sessionId); },
    onChildDisposed(sessionId) { schedulerCalls.push(`child-disposed:${sessionId}`); scheduler.onChildDisposed(sessionId); },
    dispose() { schedulerCalls.push("dispose"); scheduler.dispose(); },
    activeChildCount: () => scheduler.activeChildCount(),
  };
  const lifecycle = createActivityLifecycle({
    getClient: () => ({
      sendActivity: (state, payload) => { activities.push({ state, payload }); },
      sendLifecycleSnapshot: (snapshot) => { snapshots.push(snapshot); return `snapshot-${snapshots.length}`; },
    }),
    getParentContext: () => context,
    isCompacting: () => false,
    getGoalProjection: () => undefined,
    getCurrentModel: () => undefined,
    getNativeService: (ownerSessionId) => ownerSessionId === "parent-session" ? service : undefined,
    notify: (message, level) => notifications.push({ message, level }),
    trace: (event, fields) => traces.push({ event, fields }),
    createSettledScheduler: () => schedulerSpy,
    setIntervalImpl: (callback, _delay) => {
      const timer: FakeTimer = { callback: () => callback() };
      intervals.push(timer);
      return setInterval(() => {}, 60_000);
    },
    clearIntervalImpl: (timer) => { clearInterval(timer); clearedIntervals += 1; },
    nowImpl: () => now,
  });
  return {
    lifecycle,
    context,
    service,
    setService: (next) => { service = next; },
    setNow: (value) => { now = value; },
    records,
    activities,
    snapshots,
    traces,
    notifications,
    intervals,
    get clearedIntervals() { return clearedIntervals; },
    scheduler: schedulerSpy,
    schedulerCalls,
  };
}

function typedService(records: NativeSubagent[]): NativeSubagentService {
  return { getLifecycleSnapshots: () => records };
}

function legacyService(records: NativeSubagent[]): NativeSubagentService {
  return { listAgents: () => records };
}

test("the independent trace summary remains bounded and does not expose native payload text", () => {
  const privateDescription = "private output".repeat(100);
  const records = Array.from({ length: 1_001 }, (_, index) => ({
    id: ` child-${index} `,
    type: ` type-${index} `,
    status: ` custom-${index} `,
    description: privateDescription,
    output: privateDescription,
    error: privateDescription,
  }));
  const summary = summarizeNativeSubagents(records);
  assert.equal(summary.length, 999);
  assert.deepEqual(summary[0], {
    id: "child-0",
    type: "type-0",
    status: "custom-0",
    description_length: privateDescription.length,
  });
  assert.equal(JSON.stringify(summary).includes(privateDescription), false);
});

test("typed terminal events retain the local record until the next heartbeat snapshot omits it", () => {
  const records: NativeSubagent[] = [{ id: "typed-child", type: "worker", status: "running", description: "safe label" }];
  const fixture = activityFixture(typedService(records));
  fixture.lifecycle.onAgentStart(fixture.context);
  fixture.intervals[0]?.callback();
  assert.equal(fixture.activities.at(-1)?.payload && JSON.stringify(fixture.activities.at(-1)?.payload).includes("typed-child"), true);
  const activitiesBeforeTerminal = fixture.activities.length;
  const snapshotsBeforeTerminal = fixture.snapshots.length;

  fixture.lifecycle.onNativeTerminal({ id: "typed-child", status: "completed" });
  assert.equal(fixture.activities.length, activitiesBeforeTerminal);
  assert.equal(fixture.snapshots.length, snapshotsBeforeTerminal);
  assert.equal(JSON.stringify(fixture.activities.at(-1)?.payload).includes("typed-child"), true);

  records.length = 0;
  fixture.intervals[0]?.callback();
  assert.equal(fixture.snapshots.at(-1) instanceof Array, true);
  assert.equal(JSON.stringify(fixture.activities.at(-1)?.payload).includes("typed-child"), false);
});

test("legacy terminal tombstones survive compaction and shutdown boundaries, with newer ID reuse", () => {
  const records: NativeSubagent[] = [{ id: "legacy-child", status: "running", description: "legacy label", startedAt: 100 }];
  const fixture = activityFixture(legacyService(records));
  fixture.setNow(100);
  fixture.lifecycle.onNativeStarted({ id: "legacy-child", status: "running", description: "legacy label", startedAt: 100 });
  fixture.lifecycle.onAgentStart(fixture.context);
  fixture.intervals[0]?.callback();
  assert.equal(JSON.stringify(fixture.activities.at(-1)?.payload).includes("legacy-child"), true);
  assert.deepEqual(fixture.schedulerCalls, ["child-started:legacy-child"]);
  assert.equal(fixture.scheduler.activeChildCount(), 1);

  fixture.lifecycle.onNativeTerminal({ id: "legacy-child", status: "failed" });
  assert.equal(JSON.stringify(fixture.activities.at(-1)?.payload).includes("legacy-child"), false);
  assert.equal(fixture.scheduler.activeChildCount(), 0);
  assert.deepEqual(fixture.schedulerCalls.filter((call) => call.includes("legacy-child")), [
    "child-started:legacy-child",
    "child-disposed:legacy-child",
  ]);

  fixture.lifecycle.onCompactionStarted(fixture.context);
  assert.equal(fixture.schedulerCalls.includes("dispose"), false);
  records[0] = { id: "legacy-child", status: "running", description: "stale resurrection", startedAt: 100 };
  fixture.lifecycle.onCompactionResumed(fixture.context);
  assert.equal(JSON.stringify(fixture.activities.at(-1)?.payload).includes("legacy-child"), false);
  assert.equal(fixture.scheduler.activeChildCount(), 0);

  fixture.lifecycle.onSessionBoundary();
  fixture.lifecycle.refresh();
  assert.equal(fixture.schedulerCalls.includes("dispose"), false);
  assert.equal(JSON.stringify(fixture.activities.at(-1)?.payload).includes("legacy-child"), false);

  fixture.setNow(2_000);
  records[0] = { id: "legacy-child", status: "running", description: "reused", startedAt: 2_000 };
  fixture.lifecycle.refresh();
  assert.equal(JSON.stringify(fixture.activities.at(-1)?.payload).includes("legacy-child"), true);
});

test("structured native read failures trace as Error after normalization", () => {
  const fixture = activityFixture({
    listAgents: () => { throw { name: "StructuredFailure", message: "read failed" }; },
  });
  fixture.lifecycle.ensureHeartbeat();
  fixture.intervals[0]?.callback();
  const failure = fixture.traces.find(({ event }) => event === "extension_snapshot_read_error");
  assert.equal((failure?.fields as { error_name?: unknown } | undefined)?.error_name, "Error");
  assert.equal(fixture.notifications.length, 1);
});

test("native lookup is scoped to the parent owner and heartbeat polling stays periodic", () => {
  const records: NativeSubagent[] = [{ id: "child", status: "running", description: "label" }];
  const fixture = activityFixture(typedService(records));
  assert.equal(fixture.lifecycle.getNativeService("parent-session")?.getLifecycleSnapshots !== undefined, true);
  assert.equal(fixture.lifecycle.getNativeService("child-session"), undefined);

  fixture.lifecycle.ensureHeartbeat();
  fixture.lifecycle.ensureHeartbeat();
  assert.equal(fixture.intervals.length, 1);
  fixture.intervals[0]?.callback();
  fixture.intervals[0]?.callback();
  assert.equal(fixture.snapshots.length, 2);
  assert.equal(fixture.traces.filter(({ event }) => event === "extension_lifecycle_heartbeat").length, 2);
});

test("legacy service fallbacks retain event-tracked children through list lag and age them out", () => {
  const records: NativeSubagent[] = [];
  const fixture = activityFixture(legacyService(records));
  fixture.lifecycle.onNativeStarted({ id: "event-child", description: "event label" });
  fixture.lifecycle.ensureHeartbeat();
  fixture.intervals[0]?.callback();
  assert.equal(JSON.stringify(fixture.activities.at(-1)?.payload).includes("event-child"), true);

  fixture.setNow(59_999);
  fixture.intervals[0]?.callback();
  assert.equal(JSON.stringify(fixture.activities.at(-1)?.payload).includes("event-child"), true);
  fixture.setNow(60_000);
  fixture.intervals[0]?.callback();
  assert.equal(JSON.stringify(fixture.activities.at(-1)?.payload).includes("event-child"), false);
});

test("compaction clears transient projection without disposing the actual scheduler", () => {
  const records: NativeSubagent[] = [{ id: "child", status: "running", description: "label" }];
  const fixture = activityFixture(typedService(records));
  fixture.lifecycle.onAgentStart(fixture.context);
  fixture.intervals[0]?.callback();
  const intervalCount = fixture.intervals.length;
  assert.equal(fixture.scheduler.activeChildCount(), 1);

  fixture.lifecycle.onCompactionStarted(fixture.context);
  assert.equal(fixture.activities.at(-1)?.state, "compacting");
  assert.equal(fixture.clearedIntervals, 1);
  assert.equal(fixture.scheduler.activeChildCount(), 1);
  assert.equal(fixture.schedulerCalls.includes("dispose"), false);
  assert.equal(intervalCount, 1);

  fixture.lifecycle.onCompactionResumed(fixture.context);
  assert.equal(fixture.intervals.length, 2);
  fixture.lifecycle.onSessionBoundary();
  assert.equal(fixture.clearedIntervals, 2);
  assert.equal(fixture.scheduler.activeChildCount(), 1);
  assert.equal(fixture.schedulerCalls.includes("dispose"), false);
});

test("settled scheduler retains cooldown generations, child suppression, and disposal semantics", () => {
  const timers: Array<{ callback: () => void; delay: number }> = [];
  const cleared: object[] = [];
  const scheduler = createSettledNotificationScheduler({
    setTimeoutImpl: (callback, delay) => {
      const timer = { callback: () => callback(), delay };
      timers.push(timer);
      return setTimeout(() => {}, 60_000);
    },
    clearTimeoutImpl: (timer) => { clearTimeout(timer); cleared.push(timer); },
  });
  let sent = 0;
  scheduler.onParentSettled(() => { sent += 1; });
  assert.equal(timers[0]?.delay, 120_000);
  scheduler.onParentActivity();
  timers[0]?.callback();
  assert.equal(sent, 0);
  scheduler.onChildStarted("child");
  scheduler.onParentSettled(() => { sent += 1; });
  assert.equal(timers.length, 1);
  scheduler.onChildDisposed("child");
  scheduler.onParentActivity();
  scheduler.dispose();
  assert.equal(scheduler.activeChildCount(), 0);
  assert.equal(cleared.length >= 1, true);
});

test("root shutdown remembers the context immediately before lifecycle cleanup", async () => {
  const source = await readFile(fileURLToPath(new URL("../pi-extensions/mecha-messaging/index.ts", import.meta.url)), "utf8");
  const shutdownStarts = [...source.matchAll(/  pi\.on\("session_shutdown"/g)].map(({ index }) => index ?? -1);
  assert.equal(shutdownStarts.length, 2);
  const initialEnd = source.indexOf("\n  });", shutdownStarts[0]);
  assert.ok(initialEnd > shutdownStarts[0]);
  const initialHandler = source.slice(shutdownStarts[0], initialEnd);
  assert.doesNotMatch(initialHandler, /rememberParentActivityContext|onSessionBoundary/);
  assert.match(source.slice(shutdownStarts[1]), /mailboxSendRateLimits\.clear\(\);\s+rememberParentActivityContext\(context\);\s+activityLifecycle\.onSessionBoundary\(\);/);
});

test("root session startup and shutdown keep client and lifecycle ordering", async () => {
  const handlers = new Map<string, Array<(event: object, context: object) => unknown>>();
  const eventHandlers = new Map<string, Array<(event: object, context: object) => unknown>>();
  const order: string[] = [];
  const pi = {
    handlers,
    events: {
      on(name: string, handler: (event: object, context: object) => unknown) {
        eventHandlers.set(name, [...(eventHandlers.get(name) ?? []), handler]);
      },
      emit() {},
    },
    on(name: string, handler: (event: object, context: object) => unknown) {
      handlers.set(name, [...(handlers.get(name) ?? []), handler]);
    },
    appendEntry() {},
    registerTool() {},
    registerCommand() {},
    sendMessage() {},
    exec: async () => ({ code: 0, stderr: "" }),
  };
  const context = {
    sessionManager: {
      getBranch: () => [],
      getSessionId: () => "root-session",
      isPersisted: () => false,
      getSessionFile: () => undefined,
    },
    ui: { setStatus() {}, notify() {} },
    isIdle: () => false,
  };
  const scheduler: SettledNotificationScheduler = {
    onParentActivity() {}, onParentSettled() {}, onChildStarted() {}, onChildDisposed() {},
    dispose: () => order.push("scheduler-dispose"), activeChildCount: () => 0,
  };
  let createdClient: ReturnType<typeof createMailboxClient> | undefined;
  let heartbeat: (() => void) | undefined;
  const createClient = (options: Parameters<typeof createMailboxClient>[0]) => {
    const mailboxClient = createMailboxClient(options);
    order.push("client-created");
    createdClient = mailboxClient;
    mailboxClient.connect = () => Promise.resolve();
    mailboxClient.sendGoalState = () => { order.push("goal-state"); return undefined; };
    mailboxClient.sendActivity = (state) => { order.push(`activity:${state}`); };
    mailboxClient.close = () => { order.push("client-close"); };
    return mailboxClient;
  };
  createMechaMessagingExtension(pi, {
    resolveCurrentAgentId: () => "worker",
    createClient,
    createCoordinator: () => {
      order.push("coordinator-created");
      return {
        onEnvelope() {},
        onMessageEnd() {},
        close: () => order.push("coordinator-close"),
      };
    },
    createSettledScheduler: () => scheduler,
    setIntervalImpl: (callback) => {
      heartbeat = callback;
      return setInterval(() => {}, 60_000);
    },
    clearIntervalImpl: (timer) => { clearInterval(timer); },
    getSubagentsService: () => undefined,
  });
  const sessionStart = handlers.get("session_start")?.at(-1);
  await sessionStart?.({}, context);
  assert.equal(createdClient !== undefined, true);
  assert.deepEqual(order.slice(0, 3), ["client-created", "goal-state", "coordinator-created"]);

  const shutdownHandlers = handlers.get("session_shutdown") ?? [];
  assert.equal(shutdownHandlers.length, 2);
  const shutdownContext = { ...context, isIdle: () => true };
  await shutdownHandlers[0]?.({}, shutdownContext);
  heartbeat?.();
  assert.deepEqual(order, ["client-created", "goal-state", "coordinator-created", "activity:active"]);

  const sessionShutdown = shutdownHandlers.at(-1);
  await sessionShutdown?.({}, shutdownContext);
  assert.deepEqual(order.slice(-4), ["activity:settled", "scheduler-dispose", "coordinator-close", "client-close"]);
});
