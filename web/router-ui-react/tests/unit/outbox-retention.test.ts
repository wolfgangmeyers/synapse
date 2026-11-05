import { describe, expect, it } from "vitest";
import { createInitialState } from "../../src/state/app-state";
import { reducer } from "../../src/state/reducer";
import { canBridgeAccepted, visibleAccepted } from "../../src/state/selectors";
import type { HistoryMessage } from "../../src/domain/history";

const scope = { routerUrl: "http://localhost:8765", agentId: "web" };
describe("transient outbox", () => {
  it("bounds pending sends globally and reports eviction and expiry", () => {
    let state = createInitialState(scope);
    const now = Date.now();
    for (let i = 0; i < 151; i++) state = reducer(state, { type: "message/append", scope, peer: i % 2 ? "a" : "b", message: { id: String(i), pending: true, created_at: new Date(now + i).toISOString() } });
    expect(Object.values(state.messagesByPeer).flat()).toHaveLength(150);
    expect(state.outboxNotice).toMatch(/unconfirmed messages/);
    state = reducer(state, { type: "outbox/prune", scope, now: now + 24 * 60 * 60 * 1000 + 151 });
    expect(Object.values(state.messagesByPeer).flat()).toHaveLength(0);
    expect(state.outboxNotice).toBeTruthy();
  });
  it("reconciles prefixed history and surfaces failure", () => {
    let state = createInitialState(scope);
    state = reducer(state, { type: "message/append", scope, peer: "a", message: { id: "abc", pending: true, created_at: new Date().toISOString() } });
    state = reducer(state, { type: "history/reconcile", scope, peer: "a", snapshot: [{ id: "msg:a:abc" }], highWaterSequence: 1 });
    expect(state.messagesByPeer).toEqual({});
    expect(visibleAccepted([{ id: "abc" }], [{ id: "hist:complete:abc" }])).toEqual([]);
    state = reducer(state, { type: "message/append", scope, peer: "a", message: { id: "other", body: "recover me", attachments: [{ url: "data:image/png;base64,abc" }], pending: true, created_at: new Date().toISOString() } });
    state = reducer(state, { type: "send/settle", scope, peer: "a", localId: "other", ok: false });
    expect(state.messagesByPeer.a?.[0]).toMatchObject({ body: "recover me", failed: true, pending: false, attachments: [{ url: "data:image/png;base64,abc" }] });
    expect(state.outboxNotice).toMatch(/could not be confirmed/);
  });
  it("does not reconcile another peer or router scope", () => {
    let state = createInitialState(scope);
    state = reducer(state, { type: "message/append", scope, peer: "a", message: { id: "id", pending: true, created_at: new Date().toISOString() } });
    state = reducer(state, { type: "history/reconcile", scope, peer: "b", snapshot: [{ id: "hist:complete:id" }], highWaterSequence: 1 });
    state = reducer(state, { type: "send/settle", scope: { ...scope, routerUrl: "http://other" }, peer: "a", localId: "id", ok: false });
    expect(state.messagesByPeer.a?.[0]).toMatchObject({ id: "id", pending: true });
  });

});

describe("image memory budget", () => {
  it("counts retained and in-flight data even after eviction until request settles", async () => {
    const { InFlightImageBudget, OUTBOX_IMAGE_BYTES } = await import("../../src/state/outbox-budget");
    const budget = new InFlightImageBudget();
    const image = { id: "first", attachments: [{ url: `data:image/png;base64,${"a".repeat(Math.floor(OUTBOX_IMAGE_BYTES / 4))}` }] };
    expect(budget.reserve("first", image, {})).toBe(true);
    expect(budget.reserve("second", { ...image, id: "second" }, {})).toBe(false);
    budget.release("first");
    expect(budget.reserve("second", { ...image, id: "second" }, { peer: [image] })).toBe(false);
    expect(budget.reserve("second", { ...image, id: "second" }, {})).toBe(true);
  });
  it("retains text and explicitly reports rejected images", () => {
    const message = { id: "x", body: "recover text", attachments: [{ url: "data:image/png;base64,abc" }], created_at: new Date().toISOString() };
    const state = reducer(createInitialState(scope), { type: "outbox/reject-images", scope, peer: "a", message });
    expect(state.messagesByPeer.a?.[0]).toMatchObject({ body: "recover text", failed: true, attachments: [] });
    expect(state.outboxNotice).toMatch(/not sent.*Reattach/);
  });
});

describe("accepted view bridge", () => {
  it("rejects acceptance from an A-B-A visit and already-confirmed sends", () => {
    expect(canBridgeAccepted(1, 3, false)).toBe(false);
    expect(canBridgeAccepted(1, 1, true)).toBe(false);
    expect(canBridgeAccepted(1, 1, false)).toBe(true);
  });
  it("permanently removes confirmed bridge entries before a later empty refresh", () => {
    let bridge: HistoryMessage[] = [{ id: "id", body: "accepted" }];
    bridge = visibleAccepted(bridge, [{ id: "hist:complete:id" }]);
    expect(visibleAccepted(bridge, [])).toEqual([]);
  });
});

it("releases a stale-scope in-flight reservation on independent commit", async () => {
  const { DeferredImageReleases, InFlightImageBudget, OUTBOX_IMAGE_BYTES } = await import("../../src/state/outbox-budget");
  const budget = new InFlightImageBudget();
  const releases = new DeferredImageReleases();
  const image = { id: "old", attachments: [{ url: `data:image/png;base64,${"a".repeat(Math.floor(OUTBOX_IMAGE_BYTES / 4))}` }] };
  expect(budget.reserve("old", image, {})).toBe(true);
  const newScope = { ...scope, routerUrl: "http://new-router" };
  const state = createInitialState(newScope);
  expect(reducer(state, { type: "send/settle", scope, peer: "a", localId: "old", ok: false })).toBe(state);
  releases.defer("old");
  expect(budget.reserve("new", { ...image, id: "new" }, {})).toBe(false);
  releases.commit(budget);
  expect(budget.reserve("new", { ...image, id: "new" }, {})).toBe(true);
});

it("does not report failure after a snapshot confirms the pending send", () => {
  let state = createInitialState(scope);
  state = reducer(state, { type: "message/append", scope, peer: "a", message: { id: "sent", pending: true, created_at: new Date().toISOString() } });
  state = reducer(state, { type: "history/reconcile", scope, peer: "a", snapshot: [{ id: "msg:a:sent" }], highWaterSequence: 1 });
  state = reducer(state, { type: "send/settle", scope, peer: "a", localId: "sent", ok: false });
  expect(state.outboxNotice).toBeUndefined();
  expect(state.messagesByPeer).toEqual({});
});

it("admission counts newly committed failed attachments after reservation release", async () => {
  const { DeferredImageReleases, InFlightImageBudget, OUTBOX_IMAGE_BYTES } = await import("../../src/state/outbox-budget");
  const budget = new InFlightImageBudget();
  const releases = new DeferredImageReleases();
  const image = { id: "failed", attachments: [{ url: `data:image/png;base64,${"a".repeat(Math.floor(OUTBOX_IMAGE_BYTES / 4))}` }] };
  expect(budget.reserve("failed", image, {})).toBe(true);
  const committed = { current: {} as Record<string, typeof image[]> };
  committed.current = { peer: [image] };
  releases.defer("failed"); releases.commit(budget);
  expect(budget.reserve("next", { ...image, id: "next" }, committed.current)).toBe(false);
});

it("notifies a late failure after eviction and dismissal", () => {
  let state = createInitialState(scope);
  state = reducer(state, { type: "message/append", scope, peer: "a", message: { id: "old", pending: true, created_at: new Date().toISOString() } });
  state = reducer(state, { type: "outbox/prune", scope, now: Date.now() + 25 * 60 * 60 * 1000 });
  state = reducer(state, { type: "outbox/dismiss", scope });
  state = reducer(state, { type: "send/settle", scope, peer: "a", localId: "old", ok: false });
  expect(state.outboxNotice).toMatch(/could not be confirmed/);
});
