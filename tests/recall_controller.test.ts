import assert from "node:assert/strict";
import test from "node:test";
import { createRecallController, createRecallState } from "../pi-extensions/mecha-messaging/index.ts";

test("launcher selection is consumed once and reload clears selected stores", () => {
  const environment = { MECHA_RECALL_STORES: "work" }; const recall = createRecallState(environment);
  assert.deepEqual(recall.status(), { enabled: false, stores: ["work"] }); assert.equal(environment.MECHA_RECALL_STORES, undefined);
  assert.deepEqual(createRecallState(environment).status(), { enabled: false, stores: [] });
  assert.deepEqual(recall.enable(["work"]), ["work"]); recall.disable(); assert.deepEqual(recall.status(), { enabled: false, stores: [] });
});

test("controller configures initial policy seconds before its first window", () => {
  let delay; const controller = createRecallController({ evaluate: async () => undefined, recall: async () => {}, setTimer: (_fn, value) => { delay = value; return 1; }, clearTimer: () => {}, now: () => 0 });
  controller.configure({ inactivity_seconds: 7, maximum_window_seconds: 9 }); controller.activity(); assert.equal(delay, 7_000);
});

test("recall controller debounces activity and coalesces evaluation", async () => {
  const timers = []; let cleared = 0; let evaluated = 0; let recalled = 0;
  const controller = createRecallController({
    evaluate: async () => { evaluated += 1; return { ids: ["one"] }; },
    recall: async () => { recalled += 1; },
    setTimer: (callback, delay) => { timers.push({ callback, delay }); return timers.length; },
    clearTimer: () => { cleared += 1; }, now: () => 0,
  });
  controller.activity(); controller.activity();
  assert.equal(cleared, 2); assert.equal(timers.at(-1).delay, 30_000);
  await timers.at(-1).callback();
  assert.equal(evaluated, 1); assert.equal(recalled, 1); assert.deepEqual(controller.status(), { pending: false, cooling: false });
});

test("activity during cooling schedules one follow-up from its first activity deadline", async () => {
  const timers = []; let resolve; let current = 0; const controller = createRecallController({ evaluate: () => new Promise((done) => { resolve = done; }), recall: async () => {}, setTimer: (fn, delay) => { timers.push({ fn, delay }); return timers.length; }, clearTimer: () => {}, now: () => current });
  controller.activity(); const running = timers[1].fn(); current = 100; controller.activity(); current = 125_000; controller.activity(); resolve({ ids: [] }); await running;
  assert.equal(timers.length, 4); assert.equal(timers.at(-1).delay, 0);
});

test("shutdown before timer callback prevents evaluation", async () => {
  let callback; let evaluated = 0; const controller = createRecallController({ evaluate: async () => { evaluated += 1; }, recall: async () => {}, setTimer: (fn) => { callback = fn; return 1; }, clearTimer: () => {}, now: () => 0 });
  controller.activity(); controller.stop(); await callback(); assert.equal(evaluated, 0);
});

test("recall controller passes its live cancellation guard into recall", async () => {
  let callback; let active; const controller = createRecallController({ evaluate: async () => ({ ids: ["one"] }), recall: async (_selection, guard) => { active = guard; assert.equal(active(), true); }, setTimer: (fn) => { callback = fn; return 1; }, clearTimer: () => {}, now: () => 0 });
  controller.activity(); await callback(); assert.equal(typeof active, "function"); assert.equal(active(), false); controller.stop(); assert.equal(active(), false);
});

test("recall controller cancels in-flight recall after shutdown", async () => {
  let callback; let resolve; let recalled = 0; const controller = createRecallController({ evaluate: () => new Promise((done) => { resolve = done; }), recall: async () => { recalled += 1; }, setTimer: (fn) => { callback = fn; return 1; }, clearTimer: () => {}, now: () => 0 });
  const pending = controller.activity(); const running = callback(); controller.stop(); resolve({ ids: ["one"] }); await running; assert.equal(recalled, 0);
});

test("stop kills pending evaluator work exactly once", () => {
  let killed = 0; const child = { kill: () => { killed += 1; } }; const controller = createRecallController({ evaluate: async () => new Promise(() => {}), recall: async () => assert.fail("must not recall"), cancel: () => child.kill(), setTimer: () => 1, clearTimer: () => {}, now: () => 0 });
  controller.activity(); controller.stop(); controller.stop(); assert.equal(killed, 1);
});

test("recall deadline prevents a late evaluator result from delivery", async () => {
  const timers = []; let current = 0; let resolve; let recalled = 0; let canceled = 0;
  const controller = createRecallController({ evaluate: () => new Promise((done) => { resolve = done; }), recall: async () => { recalled += 1; }, cancel: () => { canceled += 1; }, setTimer: (fn, delay) => { timers.push({ fn, delay }); return timers.length; }, clearTimer: () => {}, now: () => current });
  controller.activity(); const evaluation = timers.find(({ delay }) => delay === 30_000).fn(); current = 120_000; timers.find(({ delay }) => delay === 120_000).fn(); resolve({ ids: ["one"] }); await evaluation;
  assert.equal(canceled, 1); assert.equal(recalled, 0);
});

test("recall controller contains evaluator failures", async () => {
  let callback; const controller = createRecallController({ evaluate: async () => { throw new Error("router down"); }, recall: async () => assert.fail("must not recall"), setTimer: (fn) => { callback = fn; return 1; }, clearTimer: () => {}, now: () => 0 });
  controller.activity(); await callback(); assert.deepEqual(controller.status(), { pending: false, cooling: false });
});
