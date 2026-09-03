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
  ActivityLifecycleOptions,
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

function activityFixture(initialService: NativeSubagentService = {}, overrides: Partial<ActivityLifecycleOptions> = {}): ActivityFixture {
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
    ...overrides,
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

function validLifecycleOptions(): ActivityLifecycleOptions {
  return {
    getClient: () => undefined,
    getParentContext: () => undefined,
    isCompacting: () => false,
    getGoalProjection: () => undefined,
    getCurrentModel: () => undefined,
    getNativeService: () => undefined,
    notify: () => {},
    trace: () => undefined,
    createSettledScheduler: () => createSettledNotificationScheduler({
      setTimeoutImpl: () => ({}) as ReturnType<typeof setTimeout>,
      clearTimeoutImpl: () => {},
    }),
    setIntervalImpl: () => ({}) as ReturnType<typeof setInterval>,
    clearIntervalImpl: () => {},
    nowImpl: () => 0,
  };
}

test("lifecycle callback validation and optional boundaries stay explicit", () => {
  const callbackKeys: Array<keyof ActivityLifecycleOptions> = [
    "getClient", "getParentContext", "isCompacting", "getGoalProjection", "getCurrentModel",
    "getNativeService", "notify", "trace", "createSettledScheduler",
  ];
  for (const key of callbackKeys) {
    const options = validLifecycleOptions();
    Reflect.set(options, key, undefined);
    assert.throws(() => createActivityLifecycle(options), TypeError, key);
  }
  for (const key of ["setIntervalImpl", "clearIntervalImpl", "nowImpl"] as const) {
    const options = validLifecycleOptions();
    Reflect.set(options, key, null);
    assert.throws(() => createActivityLifecycle(options), TypeError, key);
  }
});

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

test("malformed summaries and scheduler boundaries stay safe", () => {
  assert.deepEqual(summarizeNativeSubagents(undefined), []);
  assert.deepEqual(summarizeNativeSubagents([null as unknown as NativeSubagent, { id: "   " }, { id: "child" }]), [
    { id: "child", description_length: 0 },
  ]);

  const timers: Array<{ callback: () => void }> = [];
  const scheduler = createSettledNotificationScheduler({
    setTimeoutImpl: (callback) => { const timer = { callback: () => callback() }; timers.push(timer); return timer; },
    clearTimeoutImpl: () => {},
  });
  scheduler.onParentSettled(undefined as unknown as () => void);
  scheduler.onParentSettled(() => {});
  scheduler.onParentSettled(() => {});
  scheduler.onChildStarted("");
  scheduler.onChildStarted("child");
  scheduler.onChildStarted("child");
  scheduler.onChildDisposed("");
  scheduler.onChildDisposed("missing");
  timers[0]?.callback();
  assert.equal(scheduler.activeChildCount(), 1);

  assert.throws(() => createSettledNotificationScheduler({ setTimeoutImpl: null as unknown as typeof setTimeout, clearTimeoutImpl: () => {} }), TypeError);
  assert.throws(() => createSettledNotificationScheduler({ setTimeoutImpl: () => ({}) as ReturnType<typeof setTimeout>, clearTimeoutImpl: null as unknown as typeof clearTimeout }), TypeError);
});

test("remaining lifecycle boundary branches stay covered without changing ownership", () => {
  const schedulerTimers: Array<() => void> = [];
  const scheduler = createSettledNotificationScheduler({
    setTimeoutImpl: (callback) => {
      schedulerTimers.push(callback);
      return undefined as unknown as ReturnType<typeof setTimeout>;
    },
    clearTimeoutImpl: () => {},
  });
  let sent = 0;
  scheduler.onParentSettled(() => { sent += 1; });
  schedulerTimers[0]?.();
  assert.equal(sent, 1);

  const registrySymbol = Symbol.for("@gotgenes/pi-subagents:service-registry");
  const previousRegistry = Object.getOwnPropertyDescriptor(globalThis, registrySymbol);
  Object.defineProperty(globalThis, registrySymbol, { value: { services: new Map() }, configurable: true });
  try {
    const registryFixture = activityFixture({}, { getParentContext: () => undefined });
    assert.equal(registryFixture.lifecycle.getNativeService(), undefined);
    assert.equal(registryFixture.traces.at(-1)?.fields && JSON.stringify(registryFixture.traces.at(-1)?.fields).includes("registry_available"), true);
  } finally {
    if (previousRegistry === undefined) delete (globalThis as Record<PropertyKey, unknown>)[registrySymbol];
    else Object.defineProperty(globalThis, registrySymbol, previousRegistry);
  }

  const lookupFixture = activityFixture({}, {
    getNativeService: () => { throw { name: "structured lookup failure" }; },
  });
  assert.equal(lookupFixture.lifecycle.getNativeService("parent-session"), undefined);
  const unnamedLookupFixture = activityFixture({}, {
    getNativeService: () => { throw { name: 42 }; },
  });
  assert.equal(unnamedLookupFixture.lifecycle.getNativeService("parent-session"), undefined);

  let remembered = 0;
  const parentWithoutIdle: ActivityContext = { sessionManager: { getSessionId: () => "parent-session" } };
  const rememberFixture = activityFixture({ getRunning: () => [] }, {
    getParentContext: () => parentWithoutIdle,
    rememberParentContext: () => { remembered += 1; },
  });
  rememberFixture.lifecycle.onAgentStart(parentWithoutIdle);
  rememberFixture.intervals[0]?.callback();
  assert.equal(remembered > 0, true);

  const propertyFixture = activityFixture({ running: [{ id: "running-child", description: "label" }] });
  propertyFixture.lifecycle.ensureHeartbeat();
  propertyFixture.intervals[0]?.callback();
  const fallbackFixture = activityFixture({ runs: [{ id: "runs-child", description: "label" }] });
  fallbackFixture.lifecycle.ensureHeartbeat();
  fallbackFixture.intervals[0]?.callback();
  const subagentsFixture = activityFixture({ subagents: [{ id: "subagents-child", description: "label" }] });
  subagentsFixture.lifecycle.ensureHeartbeat();
  subagentsFixture.intervals[0]?.callback();
  const malformedSourceFixture = activityFixture({ getRunning: () => ({}) as unknown as NativeSubagent[] });
  malformedSourceFixture.lifecycle.ensureHeartbeat();
  malformedSourceFixture.intervals[0]?.callback();
  const malformedTypedSummaryFixture = activityFixture(typedService([{}]));
  malformedTypedSummaryFixture.lifecycle.ensureHeartbeat();
  malformedTypedSummaryFixture.intervals[0]?.callback();
  const malformedResumeFixture = activityFixture({ getLifecycleSnapshots: () => ({}) as unknown as NativeSubagent[] });
  malformedResumeFixture.lifecycle.onCompactionResumed(malformedResumeFixture.context);

  const malformedEventFixture = activityFixture(legacyService([]));
  malformedEventFixture.lifecycle.onNativeStarted({ id: 123 } as unknown as NativeSubagent);
  malformedEventFixture.lifecycle.onNativeStarted({ id: "bare-event", description: "label" });
  malformedEventFixture.lifecycle.onNativeTerminal({ id: "" });
  malformedEventFixture.lifecycle.refresh();
  const malformedRecordFixture = activityFixture(legacyService([{ id: 123 } as unknown as NativeSubagent]));
  malformedRecordFixture.lifecycle.ensureHeartbeat();
  malformedRecordFixture.intervals[0]?.callback();

  const mergeRecords: NativeSubagent[] = [{ id: "merge-child", status: "running", description: "service label" }];
  const mergeFixture = activityFixture(legacyService(mergeRecords));
  mergeFixture.lifecycle.onNativeStarted({ id: "merge-child" });
  mergeFixture.lifecycle.refresh();
  mergeFixture.lifecycle.refresh();
  mergeRecords.length = 0;
  mergeFixture.lifecycle.refresh();
  const serviceOnlyRecords: NativeSubagent[] = [{ id: "service-only", status: "running", description: "service label" }];
  const serviceOnlyFixture = activityFixture(legacyService(serviceOnlyRecords));
  serviceOnlyFixture.lifecycle.refresh();
  serviceOnlyRecords.length = 0;
  serviceOnlyFixture.lifecycle.refresh();

  const terminalFixture = activityFixture(legacyService([{ id: "terminal-child", status: "completed", description: "done" }]));
  terminalFixture.lifecycle.refresh();
  const serviceTerminalRecords: NativeSubagent[] = [{ id: "service-terminal", status: "running", description: "label" }];
  const serviceTerminalFixture = activityFixture(legacyService(serviceTerminalRecords));
  serviceTerminalFixture.lifecycle.refresh();
  serviceTerminalRecords[0] = { id: "service-terminal", status: "completed", description: "label" };
  serviceTerminalFixture.lifecycle.refresh();
  const inactiveFixture = activityFixture(legacyService([{ id: "inactive-child", status: "waiting", description: "waiting" }]));
  inactiveFixture.lifecycle.refresh();

  const snapshotUndefinedFixture = activityFixture(typedService([{ id: "snapshot-child", status: "running", description: "label" }]), {
    getClient: () => ({ sendLifecycleSnapshot: () => undefined }),
  });
  snapshotUndefinedFixture.lifecycle.ensureHeartbeat();
  snapshotUndefinedFixture.intervals[0]?.callback();
  snapshotUndefinedFixture.lifecycle.onNativeStarted({ id: 123 } as unknown as NativeSubagent);
  snapshotUndefinedFixture.lifecycle.onNativeTerminal({ id: 123 } as unknown as NativeSubagent);
  snapshotUndefinedFixture.lifecycle.onNativeTerminal({ id: "unknown-child", type: "worker", description: "unknown" });

  let compacting = false;
  const compactionFixture = activityFixture({}, {
    isCompacting: () => compacting,
    getParentContext: () => parentWithoutIdle,
    getGoalProjection: () => ({ enabled: true, goal: "cover lifecycle" }),
    getCurrentModel: () => ({ provider: "fixture", id: "model" }),
  });
  compactionFixture.lifecycle.onAgentStart(parentWithoutIdle);
  compacting = true;
  compactionFixture.lifecycle.refresh();
  compacting = false;
  compactionFixture.lifecycle.onAgentSettled({ ...parentWithoutIdle, isIdle: () => true });
  compactionFixture.lifecycle.ensureHeartbeat();
  compactionFixture.intervals.at(-1)?.callback();
  const idleFixture = activityFixture({}, { getParentContext: () => ({ ...parentWithoutIdle, isIdle: () => true }) });
  idleFixture.lifecycle.onAgentStart(idleFixture.context);
  idleFixture.intervals[0]?.callback();

  const stateChurnRecords: NativeSubagent[] = [];
  const stateChurnFixture = activityFixture(typedService(stateChurnRecords));
  stateChurnFixture.lifecycle.onAgentStart(stateChurnFixture.context);
  for (let cycle = 0; cycle < 21; cycle += 1) {
    stateChurnRecords.splice(0, stateChurnRecords.length, ...Array.from({ length: 100 }, (_, index) => ({
      id: `state-churn-${cycle}-${index}`, type: "worker", description: "label", status: "running",
    })));
    stateChurnFixture.intervals[0]?.callback();
  }

  const fencedRecords: NativeSubagent[] = [{ id: "fenced-child", type: "worker", description: "old", status: "running" }];
  const fencedFixture = activityFixture(typedService(fencedRecords));
  fencedFixture.lifecycle.onAgentStart(fencedFixture.context);
  fencedFixture.intervals[0]?.callback();
  fencedFixture.lifecycle.onNativeTerminal({ id: "fenced-child", type: "worker", description: "old", status: "completed" });
  fencedFixture.lifecycle.onNativeStarted({ id: "fenced-child", type: "worker", description: "new", status: "running" });
  fencedRecords[0] = { id: "fenced-child", type: "worker", description: "old", status: "running" };
  fencedFixture.lifecycle.onCompactionResumed(fencedFixture.context);

  const boundaryFixture = activityFixture(typedService([]));
  boundaryFixture.lifecycle.onNativeStarted({ id: "boundary-child", type: "worker", description: "label" });
  boundaryFixture.lifecycle.onCompactionResumed({ sessionManager: { getSessionId: () => "parent-session" } });
  const noContextResume = activityFixture(typedService([]), { getParentContext: () => undefined });
  noContextResume.lifecycle.onCompactionResumed(noContextResume.context);
  boundaryFixture.lifecycle.onSessionBoundary();
  boundaryFixture.lifecycle.onNativeStarted({ id: "blocked-child", type: "worker", description: "late" });
  boundaryFixture.lifecycle.onNativeTerminal({ id: "blocked-child" });
});

test("typed terminal events retain the local record until the next heartbeat snapshot omits it", () => {
  const records: NativeSubagent[] = [{ id: "typed-child", type: "worker", status: "running", description: "safe label" }];
  const fixture = activityFixture(typedService(records));
  fixture.lifecycle.onAgentStart(fixture.context);
  fixture.intervals[0]?.callback();
  assert.equal(fixture.activities.at(-1)?.payload && JSON.stringify(fixture.activities.at(-1)?.payload).includes("typed-child"), true);
  fixture.lifecycle.onNativeStarted({ id: "typed-child", type: "worker", description: "safe label" });
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

test("a native resume republishing the same identity survives a terminal event before heartbeat", () => {
  let record: NativeSubagent = { id: "resumed-child", type: "worker", status: "running", description: "resumed label" };
  const fixture = activityFixture({ getLifecycleSnapshots: () => [record] });
  fixture.lifecycle.onAgentStart(fixture.context);
  fixture.intervals[0]?.callback();
  fixture.lifecycle.onNativeTerminal({ ...record, status: "completed" });

  // pi-subagents' onResumeStarted publishes a new running snapshot for the same
  // ID, type, and description without emitting subagents:started.
  record = { ...record };
  fixture.intervals[0]?.callback();

  const activity = fixture.activities.at(-1);
  assert.equal(activity?.state, "active");
  assert.deepEqual((activity?.payload as { subagents?: unknown[] }).subagents, [{
    id: "resumed-child", type: "worker", description: "resumed label", status: "running",
  }]);
  assert.deepEqual(fixture.snapshots.at(-1), [{
    id: "resumed-child", type: "worker", description: "resumed label", status: "running",
  }]);
  assert.equal(fixture.scheduler.activeChildCount(), 1);
});

test("typed steered status is terminal during heartbeat reconciliation", () => {
  const records: NativeSubagent[] = [{ id: "steered-child", type: "worker", status: "running", description: "safe label" }];
  const fixture = activityFixture(typedService(records));
  fixture.lifecycle.onAgentStart(fixture.context);
  fixture.intervals[0]?.callback();
  assert.equal(JSON.stringify(fixture.activities.at(-1)?.payload).includes("steered-child"), true);

  records[0] = { id: "steered-child", type: "worker", status: "steered", description: "safe label" };
  fixture.intervals[0]?.callback();
  assert.equal(JSON.stringify(fixture.activities.at(-1)?.payload).includes("steered-child"), false);
});

test("event-tracked legacy child is removed when its service record reaches terminal steered status", () => {
  const records: NativeSubagent[] = [];
  const fixture = activityFixture(legacyService(records));
  fixture.lifecycle.onNativeStarted({ id: "legacy-steered-child", status: "running", description: "safe label" });
  assert.equal(fixture.scheduler.activeChildCount(), 1);

  records.push({ id: "legacy-steered-child", status: "steered", description: "safe label" });
  fixture.lifecycle.ensureHeartbeat();
  fixture.intervals.at(-1)?.callback();

  assert.equal(fixture.scheduler.activeChildCount(), 0);
  assert.equal(JSON.stringify(fixture.activities.at(-1)?.payload).includes("legacy-steered-child"), false);
});

test("metadata-less reused-ID starts survive delayed omissions and default snapshots", () => {
  const records: NativeSubagent[] = [{ id: "reused-child", type: "worker", status: "running", description: "old" }];
  const fixture = activityFixture(typedService(records));
  fixture.lifecycle.onAgentStart(fixture.context);
  fixture.intervals[0]?.callback();

  fixture.lifecycle.onNativeTerminal({ id: "reused-child" });
  fixture.lifecycle.onNativeStarted({ id: "reused-child" });
  fixture.lifecycle.onNativeStarted({ id: "reused-child", type: "worker", description: "old" });
  records.length = 0;
  fixture.intervals[0]?.callback();
  assert.equal(fixture.scheduler.activeChildCount(), 1);
  assert.equal(JSON.stringify(fixture.activities.at(-1)?.payload).includes('"description":"old"'), false);

  records.push({ id: "reused-child", status: "running" });
  fixture.intervals[0]?.callback();
  assert.equal(fixture.scheduler.activeChildCount(), 1);

  records.length = 0;
  fixture.intervals[0]?.callback();
  assert.equal(fixture.scheduler.activeChildCount(), 0);
});

test("metadata-less terminal stays fenced after reused-ID metadata eviction", () => {
  const records: NativeSubagent[] = [{ id: "reused-child", type: "worker", status: "running", description: "old" }];
  const fixture = activityFixture(typedService(records));
  fixture.lifecycle.onAgentStart(fixture.context);
  fixture.intervals[0]?.callback();
  fixture.lifecycle.onNativeTerminal({ id: "reused-child", type: "worker", description: "old", status: "completed" });

  for (let index = 0; index < 4_096; index += 1) {
    fixture.lifecycle.onNativeStarted({ id: `evicting-child-${index}`, type: "worker", description: "safe label" });
  }
  fixture.lifecycle.onNativeTerminal({ id: "reused-child", type: "worker", description: "old", status: "completed" });
  fixture.lifecycle.onNativeStarted({ id: "reused-child" });
  fixture.lifecycle.onNativeTerminal({ id: "reused-child" });
  records[0] = { id: "reused-child", type: "worker", status: "running", description: "old" };
  fixture.intervals[0]?.callback();
  fixture.lifecycle.onCompactionResumed(fixture.context);
  records.length = 0;
  fixture.lifecycle.onCompactionStarted(fixture.context);
  fixture.lifecycle.onCompactionResumed(fixture.context);
  fixture.intervals.at(-1)?.callback();


  assert.equal(JSON.stringify(fixture.activities.at(-1)?.payload).includes('"description":"old"'), false);
  assert.equal(fixture.scheduler.activeChildCount() > 0, true);
  assert.equal(fixture.schedulerCalls.includes("child-disposed:reused-child"), false);

  records.push({ id: "reused-child", type: "worker", status: "running", description: "new" });
  fixture.intervals[0]?.callback();
  records.length = 0;
  fixture.intervals[0]?.callback();
  assert.equal(fixture.schedulerCalls.includes("child-disposed:reused-child"), true);
});

test("legacy terminal events for unknown IDs still release no scheduler ownership", () => {
  const fixture = activityFixture(legacyService([]));
  fixture.lifecycle.onNativeTerminal({ id: "unknown-child" });
  assert.equal(fixture.schedulerCalls.at(-1), "child-disposed:unknown-child");
  assert.equal(fixture.scheduler.activeChildCount(), 0);
});

test("evicted terminal identity fences do not synthesize metadata for a different reused run", () => {
  const records: NativeSubagent[] = [{ id: "reused-child", type: "worker", status: "running", description: "old" }];
  const fixture = activityFixture(typedService(records));
  fixture.lifecycle.onAgentStart(fixture.context);
  fixture.intervals[0]?.callback();
  fixture.lifecycle.onNativeTerminal({ id: "reused-child", type: "worker", description: "old", status: "completed" });

  for (let index = 0; index < 4_096; index += 1) {
    fixture.lifecycle.onNativeStarted({ id: `evicting-child-${index}`, type: "worker", description: "safe label" });
  }
  fixture.activities.length = 0;
  fixture.lifecycle.onNativeStarted({ id: "reused-child", type: "worker", description: "new" });

  assert.deepEqual(fixture.activities, []);
});

test("partial typed snapshots retain an omitted scheduler child after compaction", () => {
  const records: NativeSubagent[] = [{ id: "retained-child", type: "worker", status: "running", description: "retained" }];
  for (let index = 0; index < 99; index += 1) {
    records.push({ id: `initial-child-${index}`, type: "worker", status: "running", description: "initial" });
  }
  const fixture = activityFixture(typedService(records));
  fixture.lifecycle.onAgentStart(fixture.context);
  fixture.intervals[0]?.callback();
  assert.equal(fixture.scheduler.activeChildCount(), 100);

  fixture.lifecycle.onCompactionStarted(fixture.context);
  records.splice(0, records.length, ...Array.from({ length: 100 }, (_, index) => ({
    id: `partial-child-${index}`, type: "worker", status: "running", description: "partial",
  })));
  fixture.lifecycle.onCompactionResumed(fixture.context);
  fixture.intervals.at(-1)?.callback();

  assert.equal(fixture.scheduler.activeChildCount(), 200);
});

test("partial typed omission without a prior activity record keeps scheduler ownership", () => {
  const records: NativeSubagent[] = [{ id: "retained-child", type: "worker", status: "running", description: "retained" }];
  const fixture = activityFixture(typedService(records));
  fixture.lifecycle.onAgentStart(fixture.context);
  fixture.intervals[0]?.callback();
  fixture.lifecycle.onCompactionStarted(fixture.context);
  records.splice(0, records.length, ...Array.from({ length: 100 }, (_, index) => ({
    id: `partial-child-${index}`, type: "worker", status: "running", description: "partial",
  })));
  fixture.lifecycle.onCompactionResumed(fixture.context);
  fixture.intervals.at(-1)?.callback();

  assert.equal(fixture.scheduler.activeChildCount(), 101);
  assert.equal(JSON.stringify(fixture.activities.at(-1)?.payload).includes("retained-child"), false);
});

test("typed late terminal events cannot dispose a newer reused child", () => {
  const records: NativeSubagent[] = [{ id: "reused-child", type: "worker", status: "running", description: "old" }];
  const fixture = activityFixture(typedService(records));
  fixture.lifecycle.onAgentStart(fixture.context);
  fixture.intervals[0]?.callback();
  fixture.lifecycle.onNativeTerminal({ id: "reused-child", type: "worker", description: "old", status: "completed", result: "private result" });
  assert.equal(fixture.scheduler.activeChildCount(), 1);
  fixture.lifecycle.onNativeStarted({ id: "reused-child" });

  records[0] = { id: "reused-child", type: "worker", status: "running", description: "new" };
  fixture.lifecycle.onNativeStarted({ id: "reused-child", type: "worker", description: "new" });
  assert.equal(fixture.scheduler.activeChildCount(), 1);
  fixture.lifecycle.onNativeTerminal({ id: "reused-child", type: "worker", description: "old", status: "completed", result: "private result" });
  assert.equal(fixture.scheduler.activeChildCount(), 1);
});

test("typed stale omission cannot dispose a child started after the prior snapshot", () => {
  const snapshots: NativeSubagent[][] = [
    [{ id: "raced-child", type: "worker", status: "running", description: "old" }],
    [{ id: "raced-child", type: "worker", status: "running", description: "old" }],
    [],
    [],
    [{ id: "raced-child", type: "worker", status: "running", description: "new" }],
    [],
  ];
  const fixture = activityFixture({ getLifecycleSnapshots: () => snapshots.shift() ?? [] });
  fixture.lifecycle.onAgentStart(fixture.context);
  fixture.intervals[0]?.callback();
  fixture.lifecycle.onNativeTerminal({ id: "raced-child", type: "worker", description: "old", status: "completed" });
  fixture.lifecycle.onNativeStarted({ id: "raced-child", type: "worker", description: "new" });

  fixture.intervals[0]?.callback();
  assert.equal(fixture.scheduler.activeChildCount(), 1);
  fixture.intervals[0]?.callback();
  assert.equal(fixture.scheduler.activeChildCount(), 1);
  fixture.intervals[0]?.callback();
  assert.equal(fixture.scheduler.activeChildCount(), 1);
  fixture.intervals[0]?.callback();
  assert.equal(fixture.scheduler.activeChildCount(), 1);
  fixture.intervals[0]?.callback();
  assert.equal(fixture.scheduler.activeChildCount(), 0);
});

test("late typed events after a session boundary cannot repopulate settled ownership", () => {
  const records: NativeSubagent[] = [{ id: "reused-child", type: "worker", status: "running", description: "old session" }];
  const fixture = activityFixture(typedService(records));
  fixture.lifecycle.onAgentStart(fixture.context);
  fixture.intervals[0]?.callback();
  fixture.lifecycle.onNativeTerminal({ id: "reused-child", type: "worker", status: "completed", description: "old session" });
  assert.equal(fixture.scheduler.activeChildCount(), 1);

  fixture.lifecycle.onSessionBoundary();
  assert.equal(fixture.scheduler.activeChildCount(), 1);
  const callsBeforeLateEvents = fixture.schedulerCalls.length;
  fixture.lifecycle.onNativeStarted({ id: "reused-child", type: "worker", status: "running", description: "old session" });
  fixture.lifecycle.onNativeTerminal({ id: "reused-child", type: "worker", status: "completed", description: "old session" });
  records[0] = { id: "reused-child", type: "worker", status: "running", description: "old session" };
  fixture.lifecycle.ensureHeartbeat();
  fixture.intervals.at(-1)?.callback();

  assert.equal(fixture.scheduler.activeChildCount(), 1);
  assert.equal(fixture.schedulerCalls.length, callsBeforeLateEvents);
  assert.equal(JSON.stringify(fixture.activities.at(-1)?.payload).includes('"description":"old session"'), false);

  fixture.scheduler.dispose();
  assert.equal(fixture.scheduler.activeChildCount(), 0);
});

test("session boundaries fence queued heartbeats and late compaction reconciliation", () => {
  const records: NativeSubagent[] = [{ id: "late-child", type: "worker", status: "running", description: "late child" }];
  const fixture = activityFixture(typedService(records));
  fixture.lifecycle.onAgentStart(fixture.context);
  const queuedHeartbeat = fixture.intervals[0]?.callback;

  fixture.lifecycle.onSessionBoundary();
  const callsAfterBoundary = [...fixture.schedulerCalls];
  fixture.lifecycle.onCompactionResumed(fixture.context);
  queuedHeartbeat?.();
  fixture.lifecycle.ensureHeartbeat();
  fixture.lifecycle.onAgentStart(fixture.context);

  assert.equal(fixture.scheduler.activeChildCount(), 0);
  assert.deepEqual(fixture.schedulerCalls, callsAfterBoundary);
  assert.equal(fixture.intervals.length, 1);
  assert.equal(JSON.stringify(fixture.activities.at(-1)?.payload).includes("late-child"), false);
});

test("session start reactivates polling while fencing an old queued heartbeat", () => {
  const records: NativeSubagent[] = [{ id: "resumed-child", type: "worker", status: "running", description: "resumed child" }];
  const fixture = activityFixture(typedService(records));
  fixture.lifecycle.onAgentStart(fixture.context);
  const oldHeartbeat = fixture.intervals[0]?.callback;
  oldHeartbeat?.();
  const snapshotsBeforeBoundary = fixture.snapshots.length;
  const activitiesBeforeBoundary = fixture.activities.length;

  fixture.lifecycle.onSessionBoundary();
  fixture.lifecycle.onSessionStart();
  fixture.lifecycle.ensureHeartbeat();
  const newHeartbeat = fixture.intervals.at(-1)?.callback;

  oldHeartbeat?.();
  assert.equal(fixture.snapshots.length, snapshotsBeforeBoundary);
  assert.equal(fixture.activities.length, activitiesBeforeBoundary + 1);

  newHeartbeat?.();
  assert.equal(fixture.snapshots.length, snapshotsBeforeBoundary + 1);
  assert.equal(JSON.stringify(fixture.activities.at(-1)?.payload).includes("resumed-child"), true);
});

test("typed capped snapshots do not dispose a retained child omitted by the partial read", () => {
  const records: NativeSubagent[] = [{ id: "aaa-retained-child", type: "worker", status: "running", description: "retained label" }];
  const fixture = activityFixture(typedService(records));
  fixture.lifecycle.onAgentStart(fixture.context);
  fixture.intervals[0]?.callback();
  assert.equal(fixture.scheduler.activeChildCount(), 1);

  records.splice(0, records.length, ...Array.from({ length: 101 }, (_, index) => ({
    id: `other-child-${index}`,
    type: "worker",
    status: "running",
    description: "other label",
  })));
  fixture.intervals[0]?.callback();

  assert.equal(fixture.scheduler.activeChildCount(), 101);
  assert.equal(fixture.snapshots.length, 1);
  assert.equal((fixture.activities.at(-1)?.payload as { subagentCount?: number }).subagentCount, 100);
});

test("typed capped snapshot churn evicts old activity records without disposing partial children", () => {
  const recordsFor = (prefix: string): NativeSubagent[] => Array.from({ length: 100 }, (_, index) => ({
    id: `${prefix}-${index}`,
    type: "worker",
    status: "running",
    description: "safe label",
  }));
  const records = recordsFor("initial-child");
  const fixture = activityFixture(typedService(records));
  fixture.lifecycle.onAgentStart(fixture.context);
  fixture.intervals[0]?.callback();

  for (let cycle = 1; cycle <= 3; cycle += 1) {
    records.splice(0, records.length, ...recordsFor(`cycle-${cycle}-child`));
    fixture.intervals[0]?.callback();
    const payload = fixture.activities.at(-1)?.payload as { subagentCount?: number; subagents?: Array<{ id: string }> };
    assert.equal(payload.subagentCount, 100);
    assert.equal(payload.subagents?.every(({ id }) => id.startsWith(`cycle-${cycle}-child-`)), true);
    assert.equal(fixture.scheduler.activeChildCount(), (cycle + 1) * 100);
  }

  const heartbeatCounts = fixture.traces
    .filter(({ event }) => event === "extension_lifecycle_heartbeat")
    .map(({ fields }) => (fields as { subagent_count?: number } | undefined)?.subagent_count);
  assert.deepEqual(heartbeatCounts, [100, 100, 100, 100]);

  records.length = 0;
  fixture.intervals[0]?.callback();
  assert.equal(fixture.scheduler.activeChildCount(), 0);
});

test("bounded metadata eviction preserves live suppression and duplicate ownership", () => {
  const fixture = activityFixture(typedService([]));
  for (let index = 0; index < 2_049; index += 1) {
    fixture.lifecycle.onNativeStarted({ id: `unique-child-${index}`, type: "worker", description: "safe label" });
  }
  assert.equal(fixture.scheduler.activeChildCount(), 2_048);

  fixture.lifecycle.onNativeStarted({ id: "unique-child-0", type: "worker", description: "safe label" });
  assert.equal(fixture.scheduler.activeChildCount(), 2_048);

  fixture.lifecycle.onSessionBoundary();
  assert.equal(fixture.scheduler.activeChildCount(), 2_048);
  fixture.scheduler.dispose();
  assert.equal(fixture.scheduler.activeChildCount(), 0);
});

test("identity fence overflow quarantines old snapshots after all entries are fenced", () => {
  const records: NativeSubagent[] = [];
  const fixture = activityFixture(typedService(records));
  for (let index = 0; index <= 4_096; index += 1) {
    const description = `child ${index}`;
    fixture.lifecycle.onNativeStarted({ id: `fenced-child-${index}`, type: "worker", description });
    fixture.lifecycle.onNativeTerminal({ id: `fenced-child-${index}`, type: "worker", description, status: "completed" });
  }
  assert.equal(fixture.scheduler.activeChildCount(), 2_048);

  const callsBeforeStaleSnapshot = fixture.schedulerCalls.length;
  records.push({ id: "fenced-child-2048", type: "worker", status: "running", description: "child 2048" });
  fixture.lifecycle.onCompactionResumed(fixture.context);
  fixture.intervals.at(-1)?.callback();

  assert.equal(JSON.stringify(fixture.activities.at(-1)?.payload).includes("fenced-child-2048"), false);
  assert.equal(fixture.scheduler.activeChildCount(), 2_048);
  assert.equal(fixture.schedulerCalls.length, callsBeforeStaleSnapshot);

  records[0] = { id: "fenced-child-2048", type: "worker", status: "running", description: "new run" };
  fixture.lifecycle.onNativeStarted({ id: "fenced-child-2048", type: "worker", description: "new run" });
  fixture.intervals.at(-1)?.callback();
  assert.equal(JSON.stringify(fixture.activities.at(-1)?.payload).includes('"description":"new run"'), true);
});

test("metadata-less typed starts stay quarantined until an identity arrives", () => {
  const records: NativeSubagent[] = [{ id: "awaited-child", type: "worker", status: "running", description: "stale row" }];
  const fixture = activityFixture(typedService(records));
  fixture.lifecycle.onNativeStarted({ id: "awaited-child" });
  assert.equal(fixture.scheduler.activeChildCount(), 1);
  fixture.lifecycle.onCompactionResumed(fixture.context);
  fixture.intervals.at(-1)?.callback();

  assert.equal(fixture.scheduler.activeChildCount(), 1);
  assert.equal(JSON.stringify(fixture.activities.at(-1)?.payload).includes("awaited-child"), false);

  const matchingIdentityRecords: NativeSubagent[] = [{ id: "matching-awaited-child", type: "worker", status: "running", description: "matching row" }];
  const matchingIdentityFixture = activityFixture(typedService(matchingIdentityRecords));
  matchingIdentityFixture.lifecycle.onNativeStarted({ id: "matching-awaited-child" });
  matchingIdentityFixture.lifecycle.onNativeStarted({ id: "matching-awaited-child", type: "worker", description: "matching row" });
  matchingIdentityFixture.lifecycle.onCompactionResumed(matchingIdentityFixture.context);
  assert.equal(matchingIdentityFixture.scheduler.activeChildCount(), 1);

  const evictedAwaitingRecords: NativeSubagent[] = [];
  const evictedAwaitingFixture = activityFixture(typedService(evictedAwaitingRecords));
  evictedAwaitingFixture.lifecycle.onNativeStarted({ id: "evicted-awaiting-child" });
  for (let index = 0; index < 2_048; index += 1) {
    evictedAwaitingFixture.lifecycle.onNativeStarted({ id: `evicting-awaiting-child-${index}`, type: "worker", description: "label" });
  }
  evictedAwaitingRecords.push({ id: "evicted-awaiting-child", type: "worker", status: "running", description: "stale row" });
  evictedAwaitingFixture.lifecycle.onCompactionResumed(evictedAwaitingFixture.context);
  assert.equal(evictedAwaitingFixture.scheduler.activeChildCount(), 2_048);

  const boundaryRecords: NativeSubagent[] = [{ id: "boundary-awaited-child", type: "worker", status: "running", description: "old row" }];
  const boundaryFixture = activityFixture(typedService(boundaryRecords));
  boundaryFixture.lifecycle.onNativeStarted({ id: "boundary-awaited-child" });
  boundaryFixture.lifecycle.onSessionBoundary();
  boundaryFixture.lifecycle.onCompactionResumed(boundaryFixture.context);
  assert.equal(boundaryFixture.scheduler.activeChildCount(), 1);
  assert.equal(JSON.stringify(boundaryFixture.activities.at(-1)?.payload).includes("boundary-awaited-child"), false);

  records[0] = { id: "awaited-child", type: "worker", status: "running", description: "new run" };
  fixture.lifecycle.onNativeStarted({ id: "awaited-child", type: "worker", description: "new run" });
  fixture.intervals.at(-1)?.callback();
  assert.equal(JSON.stringify(fixture.activities.at(-1)?.payload).includes('"description":"new run"'), true);
});

test("fenced children stay owned when the service falls back during terminal delivery", () => {
  const fixture = activityFixture(typedService([]));
  fixture.lifecycle.onNativeStarted({ id: "fenced-child", type: "worker", description: "fenced" });
  fixture.lifecycle.onNativeStarted({ id: "awaiting-fenced-child" });
  fixture.setService(legacyService([]));
  const callsBeforeTerminal = fixture.schedulerCalls.length;

  fixture.lifecycle.onNativeTerminal({ id: "fenced-child", type: "worker", description: "fenced", status: "completed" });
  fixture.lifecycle.onNativeTerminal({ id: "awaiting-fenced-child", status: "completed" });

  assert.equal(fixture.scheduler.activeChildCount(), 2);
  assert.equal(fixture.schedulerCalls.length, callsBeforeTerminal);
});

test("typed projection keeps the direct 100-child bound", () => {
  const records: NativeSubagent[] = Array.from({ length: 101 }, (_, index) => ({
    id: `child-${index}`,
    type: "worker",
    status: "running",
    description: `child ${index}`,
  }));
  const fixture = activityFixture(typedService(records));
  fixture.lifecycle.onAgentStart(fixture.context);
  fixture.intervals[0]?.callback();

  const payload = fixture.activities.at(-1)?.payload as { subagentCount?: number; subagents?: Array<{ id: string }> };
  // Native getLifecycleSnapshots() is capped. A capped read is partial and
  // must not publish a destructive omission snapshot.
  assert.equal(fixture.snapshots.length, 0);
  assert.equal(payload.subagentCount, 100);
  assert.equal(payload.subagents?.length, 100);
  assert.equal(payload.subagents?.at(-1)?.id, "child-99");
  assert.equal(payload.subagents?.some(({ id }) => id === "child-100"), false);
});

test("legacy onNativeStarted keeps the tracked record map at 100", () => {
  const fixture = activityFixture(legacyService([]));
  for (let index = 0; index < 101; index += 1) {
    fixture.lifecycle.onNativeStarted({ id: `event-child-${index}`, status: "running", description: "event label" });
  }
  fixture.lifecycle.refresh();
  const payload = fixture.activities.at(-1)?.payload as { subagentCount?: number; subagents?: Array<{ id: string }> };
  assert.equal(payload.subagentCount, 100);
  assert.equal(payload.subagents?.length, 100);
  assert.equal(payload.subagents?.some(({ id }) => id === "event-child-0"), false);
  assert.equal(fixture.scheduler.activeChildCount(), 101);
});

test("legacy activity records evict oldest entries at the 100-child bound", () => {
  const records: NativeSubagent[] = Array.from({ length: 101 }, (_, index) => ({
    id: `legacy-child-${index}`, status: "running", description: "legacy label",
  }));
  const fixture = activityFixture(legacyService(records));
  fixture.lifecycle.onAgentStart(fixture.context);
  fixture.intervals[0]?.callback();
  const payload = fixture.activities.at(-1)?.payload as { subagentCount?: number; subagents?: Array<{ id: string }> };
  assert.equal(payload.subagentCount, 100);
  assert.equal(payload.subagents?.length, 100);
  // Service reconciliation owns the activity projection. Event callbacks own
  // settled-notification suppression for legacy services.
  assert.equal(fixture.scheduler.activeChildCount(), 0);
});

test("legacy tombstones retain exactly the newest 2048 IDs", () => {
  const records: NativeSubagent[] = [{ id: "terminal-0", status: "running", description: "reappeared" }];
  const fixture = activityFixture(legacyService(records));
  for (let index = 0; index < 2_048; index += 1) {
    fixture.lifecycle.onNativeTerminal({ id: `terminal-${index}` });
  }

  fixture.lifecycle.refresh();
  let payload = fixture.activities.at(-1)?.payload as { subagents?: Array<{ id: string }> };
  assert.deepEqual(payload.subagents, []);

  fixture.lifecycle.onNativeTerminal({ id: "terminal-2048" });
  fixture.lifecycle.refresh();
  payload = fixture.activities.at(-1)?.payload as { subagents?: Array<{ id: string }> };
  assert.deepEqual(payload.subagents, [{ id: "terminal-0", description: "reappeared" }]);
});

test("legacy terminal tombstones survive compaction and shutdown boundaries", () => {
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
  assert.equal(JSON.stringify(fixture.activities.at(-1)?.payload).includes("legacy-child"), false);
});

test("compaction instrumentation records cleared and reconciled activity counts", () => {
  const child = { id: "child", type: "worker", description: "label", status: "running" };
  const diagnostics: Array<{ event: string; fields?: object }> = [];
  const f = activityFixture(legacyService([child]), { diagnostic: (event, fields) => diagnostics.push({ event, fields }) });
  f.lifecycle.onNativeStarted(child);
  f.traces.length = 0;

  f.lifecycle.onCompactionStarted(f.context);
  const cleared = diagnostics.find(({ event }) => event === "compaction_activity_cleared");
  assert.deepEqual(cleared?.fields, { cleared_count: 1, remaining_count: 0 });

  f.setService(typedService([child]));
  f.lifecycle.onCompactionResumed(f.context);
  const reconciled = diagnostics.find(({ event }) => event === "compaction_activity_reconciled");
  assert.deepEqual(reconciled?.fields, {
    cleared_count: 1,
    source_count: 1,
    started_count: 0,
    disposed_count: 0,
    reconciled_count: 1,
    remaining_count: 0,
  });
});

test("activity client failures and active settle races stay recoverable", () => {
  const failingClient = activityFixture({}, { getClient: () => ({ sendActivity: () => { throw new Error("client unavailable"); } }) });
  failingClient.lifecycle.onAgentStart(failingClient.context);
  assert.deepEqual(failingClient.activities, []);

  const activeContext: ActivityContext = {
    sessionManager: { getSessionId: () => "parent-session" },
    isIdle: () => false,
  };
  const racing = activityFixture({}, { getParentContext: () => activeContext });
  racing.lifecycle.onAgentSettled(activeContext);
  assert.equal(racing.intervals.length, 1);
  racing.lifecycle.onAgentSettled(activeContext);
  assert.equal(racing.intervals.length, 1);
});

test("lifecycle failure paths remain best effort and retryable", () => {
  const lookupFailure = activityFixture({}, { getNativeService: () => { throw new Error("lookup failed"); } });
  lookupFailure.lifecycle.ensureHeartbeat();
  lookupFailure.intervals[0]?.callback();
  assert.equal(lookupFailure.traces.some(({ event }) => event === "extension_service_lookup_error"), true);

  const publicationFailure = activityFixture(typedService([{ id: "child", status: "running", description: "label" }]), {
    getClient: () => ({ sendLifecycleSnapshot: () => { throw new Error("publish failed"); } }),
  });
  publicationFailure.lifecycle.ensureHeartbeat();
  publicationFailure.intervals[0]?.callback();
  assert.equal(publicationFailure.traces.some(({ event }) => event === "extension_snapshot_publish_error"), true);

  const resumeLookupFailure = activityFixture(typedService([]), {
    getNativeService: () => { throw new Error("resume lookup failed"); },
  });
  resumeLookupFailure.lifecycle.onCompactionResumed(resumeLookupFailure.context);
  const resumeReadFailure = activityFixture({ getLifecycleSnapshots: () => { throw new Error("resume read failed"); } });
  resumeReadFailure.lifecycle.onCompactionResumed(resumeReadFailure.context);

  const notificationFailure = activityFixture({ listAgents: () => { throw new Error("read failed"); } }, {
    notify: () => { throw new Error("notify failed"); },
    diagnostic: () => { throw new Error("diagnostic sink failed"); },
  });
  notificationFailure.lifecycle.ensureHeartbeat();
  assert.doesNotThrow(() => notificationFailure.intervals[0]?.callback());
  assert.doesNotThrow(() => notificationFailure.intervals[0]?.callback());
});

test("legacy started-at parsing handles valid and invalid strings", () => {
  const fixture = activityFixture(legacyService([
    { id: "dated-child", status: "running", description: "dated", startedAt: "2026-01-01T00:00:00Z" },
    { id: "numeric-child", status: "running", description: "numeric", startedAt: 1_000 },
    { id: "undated-child", status: "running", description: "undated", startedAt: "invalid" },
  ]));
  fixture.lifecycle.onAgentStart(fixture.context);
  fixture.intervals[0]?.callback();
  assert.equal((fixture.activities.at(-1)?.payload as { subagentCount?: number }).subagentCount, 3);

  const tombstoneRecords: NativeSubagent[] = [{ id: "reused-child", status: "running", description: "old", startedAt: 100 }];
  const tombstoneFixture = activityFixture(legacyService(tombstoneRecords));
  tombstoneFixture.setNow(100);
  tombstoneFixture.lifecycle.onNativeStarted(tombstoneRecords[0]);
  tombstoneFixture.lifecycle.onNativeTerminal({ id: "reused-child", status: "completed" });
  tombstoneRecords[0] = { id: "reused-child", status: "running", description: "new", startedAt: 200 };
  tombstoneFixture.setNow(200);
  tombstoneFixture.lifecycle.refresh();
  assert.equal(JSON.stringify(tombstoneFixture.activities.at(-1)?.payload).includes('"description":"new"'), true);
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

test("legacy duplicate starts mark an existing event record without publishing twice", () => {
  const fixture = activityFixture(legacyService([]));
  fixture.lifecycle.onNativeStarted({ id: "event-child", description: "event label" });
  fixture.lifecycle.onNativeStarted({ id: "event-child", description: "event label" });
  assert.equal(fixture.scheduler.activeChildCount(), 1);
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

test("compaction resume removes omitted typed children from settled scheduling immediately", () => {
  const records: NativeSubagent[] = [{ id: "omitted-child", type: "worker", status: "running", description: "label" }];
  const fixture = activityFixture(typedService(records));
  fixture.lifecycle.onAgentStart(fixture.context);
  fixture.intervals[0]?.callback();
  assert.equal(fixture.scheduler.activeChildCount(), 1);

  fixture.lifecycle.onCompactionStarted(fixture.context);
  records.length = 0;
  fixture.lifecycle.onCompactionResumed(fixture.context);

  assert.equal(fixture.scheduler.activeChildCount(), 0);
  assert.equal(fixture.schedulerCalls.at(-1), "child-disposed:omitted-child");
});

test("compaction resume keeps a typed terminal fence through a delayed old snapshot", () => {
  const oldRecord: NativeSubagent = { id: "delayed-old-child", type: "worker", status: "running", description: "old run" };
  const snapshots: NativeSubagent[][] = [[oldRecord], [oldRecord], [oldRecord]];
  const fixture = activityFixture({ getLifecycleSnapshots: () => snapshots.shift() ?? [] });
  fixture.lifecycle.onAgentStart(fixture.context);
  fixture.intervals[0]?.callback();
  fixture.lifecycle.onNativeTerminal({ ...oldRecord, status: "completed" });
  assert.equal(fixture.scheduler.activeChildCount(), 1);

  fixture.lifecycle.onCompactionStarted(fixture.context);
  fixture.lifecycle.onCompactionResumed(fixture.context);
  fixture.intervals.at(-1)?.callback();

  assert.deepEqual(fixture.snapshots.at(-1), []);
  assert.equal(JSON.stringify(fixture.activities.at(-1)?.payload).includes("delayed-old-child"), false);
  assert.equal(fixture.scheduler.activeChildCount(), 0);
});

test("settled scheduler bounds ownership without releasing overflow suppression", () => {
  const timers: Array<{ callback: () => void }> = [];
  const scheduler = createSettledNotificationScheduler({
    setTimeoutImpl: (callback) => { const timer = { callback: () => callback() }; timers.push(timer); return timer; },
    clearTimeoutImpl: () => {},
  });
  let sent = 0;
  for (let index = 0; index < 2_049; index += 1) scheduler.onChildStarted(`child-${index}`);
  assert.equal(scheduler.activeChildCount(), 2_048);
  scheduler.onParentSettled(() => { sent += 1; });
  timers[0]?.callback();
  assert.equal(sent, 0);
  scheduler.dispose();
  assert.equal(scheduler.activeChildCount(), 0);
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

test("root compaction failure resumes heartbeat and preserves the watermark", async () => {
  const handlers = new Map<string, Array<(event: object, context: object) => unknown>>();
  const eventHandlers = new Map<string, Array<(event: object, context: object) => unknown>>();
  const tools = new Map();
  const activities: Array<{ state: string; payload: unknown }> = [];
  const snapshots: unknown[] = [];
  const delivered: Array<{ message: unknown; delivery: unknown }> = [];
  let rootEnvelope: Parameters<typeof createMailboxClient>[0]["onEnvelope"];
  const intervals: Array<() => void> = [];
  const intervalHandles: ReturnType<typeof setInterval>[] = [];
  const records: NativeSubagent[] = [{ id: "visible-child", type: "worker", status: "running", description: "visible label" }];
  const context = {
    sessionManager: {
      getBranch: () => [],
      getSessionId: () => "root-session",
      isPersisted: () => false,
      getSessionFile: () => undefined,
    },
    model: { provider: "fixture", id: "model", name: "Fixture", contextWindow: 100_000 },
    getContextUsage: () => ({ percent: 25, tokens: 25_000, contextWindow: 100_000 }),
    compact: () => {},
    isIdle: () => false,
    ui: { setStatus() {}, notify() {} },
  };
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
    registerTool(tool: { name: string }) { tools.set(tool.name, tool); },
    registerCommand() {},
    sendMessage() {},
    exec: async () => ({ code: 0, stderr: "" }),
  };
  const client = {
    instanceId: "instance",
    currentLeaseId: () => "lease",
    connect: () => Promise.resolve(),
    close() {},
    sendReceipt() {},
    sendGoalState() {},
    sendActivity(state: string, payload: unknown) { activities.push({ state, payload }); },
    sendLifecycleSnapshot(subagents: unknown) { snapshots.push(subagents); return "snapshot"; },
    sendReply: async () => ({ id: "reply", state: "queued" }),
  };
  const scheduler: SettledNotificationScheduler = {
    onParentActivity() {}, onParentSettled() {}, onChildStarted() {}, onChildDisposed() {},
    dispose() {}, activeChildCount: () => 0,
  };
  createMechaMessagingExtension(pi, {
    resolveCurrentAgentId: () => "worker",
    createClient: (options) => {
      rootEnvelope = options.onEnvelope;
      return client;
    },
    createCoordinator: () => ({
      onEnvelope(message, delivery) { delivered.push({ message, delivery }); },
      onMessageEnd() {},
      close() {},
    }),
    createSettledScheduler: () => scheduler,
    setIntervalImpl: (callback) => {
      intervals.push(callback);
      const handle = setInterval(() => {}, 60_000);
      intervalHandles.push(handle);
      return handle;
    },
    clearIntervalImpl: (handle) => clearInterval(handle),
    getSubagentsService: () => typedService(records),
  });

  const previousFetch = globalThis.fetch;
  globalThis.fetch = async (input) => new Response(JSON.stringify(
    String(input).includes("/v1/memory/policy") ? { recall: {} } : { records: [] },
  ), { status: 200, headers: { "content-type": "application/json" } });
  try {
    await handlers.get("session_start")?.at(-1)?.({}, context);
    await handlers.get("agent_settled")?.[1]?.({}, context);
    const statusTool = tools.get("mecha_context");
    const before = await statusTool.execute("status-before", { action: "status" }, undefined, undefined, context);
    await handlers.get("session_before_compact")?.at(-1)?.({ preparation: {} }, context);
    const intervalsBeforeFailure = intervals.length;

    await handlers.get("session_compact_failed")?.at(-1)?.({ reason: "threshold" }, context);
    const after = await statusTool.execute("status-after", { action: "status" }, undefined, undefined, context);
    intervals.at(-1)?.();
    const message = { id: "mail-after-compaction-failure", from: "sender", to: "worker", body: "deliver after recovery" };
    const delivery = { messageId: message.id, attemptNumber: 1, items: [] };
    assert.equal(typeof rootEnvelope, "function");
    rootEnvelope?.(message, delivery);

    assert.equal(after.details.notifiedThroughPercent, before.details.notifiedThroughPercent);
    assert.equal(intervals.length > intervalsBeforeFailure, true);
    assert.equal(snapshots.length > 0, true);
    assert.deepEqual(delivered, [{ message, delivery }]);
    const activity = activities.at(-1);
    assert.equal(activity?.state, "active");
    assert.deepEqual((activity?.payload as { subagents?: unknown[] }).subagents, [{ id: "visible-child", type: "worker", description: "visible label", status: "running" }]);
  } finally {
    globalThis.fetch = previousFetch;
    for (const handle of intervalHandles) clearInterval(handle);
  }
});

test("root steered events leave a running legacy child active", async () => {
  const handlers = new Map<string, Array<(event: object, context: object) => unknown>>();
  const eventHandlers = new Map<string, Array<(event: object, context: object) => unknown>>();
  const activities: Array<{ state: string; payload: unknown }> = [];
  const snapshots: unknown[] = [];
  const intervals: Array<() => void> = [];
  let heartbeatRuns = 0;
  const intervalHandles: ReturnType<typeof setInterval>[] = [];
  const records: NativeSubagent[] = [];
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
  const pi = {
    handlers,
    events: {
      on(name: string, handler: (event: object, context: object) => unknown) {
        eventHandlers.set(name, [...(eventHandlers.get(name) ?? []), handler]);
      },
      emit(name: string, event: object, eventContext: object = context) {
        for (const handler of eventHandlers.get(name) ?? []) void handler(event, eventContext);
      },
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
  const client = {
    instanceId: "instance",
    currentLeaseId: () => "lease",
    connect: () => Promise.resolve(),
    close() {},
    sendReceipt() {},
    sendGoalState() {},
    sendActivity(state: string, payload: unknown) { activities.push({ state, payload }); },
    sendLifecycleSnapshot(subagents: unknown) { snapshots.push(subagents); return `snapshot-${snapshots.length}`; },
    sendReply: async () => ({ id: "reply", state: "queued" }),
  };
  const scheduler = createSettledNotificationScheduler();
  createMechaMessagingExtension(pi, {
    resolveCurrentAgentId: () => "worker",
    createClient: () => client,
    createCoordinator: () => ({ onEnvelope() {}, onMessageEnd() {}, close() {} }),
    createSettledScheduler: () => scheduler,
    setIntervalImpl: (callback) => {
      intervals.push(() => { heartbeatRuns += 1; callback(); });
      const handle = setInterval(() => {}, 60_000);
      intervalHandles.push(handle);
      return handle;
    },
    clearIntervalImpl: (handle) => clearInterval(handle),
    getSubagentsService: () => legacyService(records),
  });

  try {
    await handlers.get("session_start")?.at(-1)?.({}, context);
    await handlers.get("agent_start")?.at(-1)?.({}, context);
    intervals.at(-1)?.();
    const snapshotsBeforeSteered = snapshots.length;
    const heartbeatsBeforeSteered = heartbeatRuns;

    assert.equal(eventHandlers.has("subagents:steered"), false);
    pi.events.emit("subagents:started", { id: "steered-child", type: "worker", description: "visible label", status: "running" });
    assert.deepEqual((activities.at(-1)?.payload as { subagents?: unknown[] }).subagents, [{ id: "steered-child", type: "worker", description: "visible label", status: "running" }]);
    assert.equal(scheduler.activeChildCount(), 1);

    records.push({ id: "steered-child", type: "worker", status: "running", description: "visible label" });
    pi.events.emit("subagents:steered", { id: "steered-child", message: "redirect" });
    intervals.at(-1)?.();

    assert.equal(snapshots.length, snapshotsBeforeSteered);
    assert.equal(heartbeatRuns, heartbeatsBeforeSteered + 1);
    assert.deepEqual((activities.at(-1)?.payload as { subagents?: unknown[] }).subagents, [{ id: "steered-child", type: "worker", description: "visible label", status: "running" }]);
    assert.equal(scheduler.activeChildCount(), 1);
    let prematurelySettled = false;
    scheduler.onParentSettled(() => { prematurelySettled = true; });
    assert.equal(prematurelySettled, false);

    records[0] = { id: "steered-child", type: "worker", status: "completed", description: "visible label" };
    pi.events.emit("subagents:completed", { id: "steered-child", status: "completed" });
    intervals.at(-1)?.();
    assert.equal(scheduler.activeChildCount(), 0);
    assert.deepEqual((activities.at(-1)?.payload as { subagents?: unknown[] }).subagents, []);
  } finally {
    for (const handle of intervalHandles) clearInterval(handle);
  }
});

test("root shutdown remembers the context immediately before lifecycle cleanup", async () => {
  const source = await readFile(fileURLToPath(new URL("../pi-extensions/mecha-messaging/index.ts", import.meta.url)), "utf8");
  const shutdownStarts = [...source.matchAll(/  pi\.on\("session_shutdown"/g)].map(({ index }) => index ?? -1);
  assert.equal(shutdownStarts.length, 2);
  const initialEnd = source.indexOf("\n  });", shutdownStarts[0]);
  assert.ok(initialEnd > shutdownStarts[0]);
  const initialHandler = source.slice(shutdownStarts[0], initialEnd);
  assert.doesNotMatch(initialHandler, /rememberParentActivityContext|onSessionBoundary/);
  assert.match(source.slice(shutdownStarts[1]), /mailboxSendRateLimits\.clear\(\);\s+diagnosticSessionClosed = true;\s+rememberParentActivityContext\(context\);\s+activityLifecycle\.onSessionBoundary\(\);/);
  const sessionStart = source.indexOf('  pi.on("session_start"');
  const heartbeat = source.indexOf("activityLifecycle.ensureHeartbeat();", sessionStart);
  const reactivation = source.indexOf("activityLifecycle.onSessionStart();", sessionStart);
  assert.ok(sessionStart >= 0 && reactivation >= sessionStart && reactivation < heartbeat);
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
  assert.equal(eventHandlers.has("subagents:completed"), true);
  assert.equal(eventHandlers.has("subagents:steered"), false);
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

  const oldHeartbeat = heartbeat;
  await sessionStart?.({}, context);
  const orderAfterRestart = [...order];
  oldHeartbeat?.();
  assert.deepEqual(order, orderAfterRestart);
  heartbeat?.();
  assert.equal(order.at(-1), "activity:active");
  await sessionShutdown?.({}, shutdownContext);
});
