import assert from "node:assert/strict";
import test from "node:test";

import { mergeMessages, messageTime, pruneHistoryMessages, reconcileHistoryMessages } from "../web/router-ui/history.js";
import {
  canSendDraft,
  draftScope,
  historyRequestMatches,
  matchesActiveScope,
  pruneDrafts,
  replacePendingMessage,
  settlePendingMessages,
} from "../web/router-ui/state.js";

test("history reconciliation removes router-pruned rows without losing newer live events", () => {
  const stale = { sequence: 1, id: "stale", message_incarnation_id: "inc-stale", recorded_at: "2026-01-01T00:00:00.000Z" };
  const retained = { sequence: 2, id: "retained", message_incarnation_id: "inc-retained", recorded_at: "2026-02-01T00:00:00.000Z" };
  const liveAfterSnapshot = { sequence: 5, id: "live", message_incarnation_id: "inc-live", recorded_at: "2026-03-01T00:00:00.000Z" };
  const localPending = { sequence: 0, id: "pending", recorded_at: "2026-03-01T00:00:01.000Z" };

  const reconciled = reconcileHistoryMessages(
    [stale, liveAfterSnapshot, localPending],
    [retained],
    4,
  );

  assert.deepEqual(reconciled.map(({ id }) => id), ["pending", "retained", "live"]);
  assert.equal(reconciled.some(({ id }) => id === "stale"), false);
});

test("history reconciliation keys canonical replacements by message incarnation", () => {
  const live = { id: "reused", message_incarnation_id: "first", sequence: 2, body: "live" };
  const canonical = { ...live, body: "canonical", attachments: [{ id: "canonical.webp" }] };
  assert.deepEqual(reconcileHistoryMessages([live], [canonical], 2), [canonical]);

  const secondIncarnation = { ...canonical, message_incarnation_id: "second", sequence: 3, body: "new incarnation" };
  assert.deepEqual(
    mergeMessages([canonical], [secondIncarnation]).map(({ body }) => body),
    ["canonical", "new incarnation"],
  );

  const legacy = { id: "legacy", sequence: 1, body: "old legacy copy" };
  const legacyCanonical = { ...legacy, body: "canonical legacy copy" };
  assert.deepEqual(mergeMessages([legacy], [legacyCanonical]), [legacyCanonical]);
});

test("history ordering and local retention use trusted router sequence and time", () => {
  assert.equal(messageTime({
    created_at: "1999-01-01T00:00:00.000Z",
    recorded_at: "2026-08-03T00:00:00.000Z",
  }), Date.parse("2026-08-03T00:00:00.000Z"));

  const messages = mergeMessages([], [
    { id: "second", sequence: 2, recorded_at: "2026-08-03T00:00:00.000Z" },
    { id: "first", sequence: 1, recorded_at: "2026-08-03T00:00:01.000Z" },
  ]);
  assert.deepEqual(messages.map(({ id }) => id), ["first", "second"]);

  const now = Date.parse("2026-08-03T00:10:00.000Z");
  const retained = pruneHistoryMessages(Array.from({ length: 205 }, (_, index) => ({
    id: `message-${index}`,
    sequence: index + 1,
    recorded_at: new Date(now - (205 - index) * 1_000).toISOString(),
  })), now);
  assert.equal(retained.length, 200);
  assert.equal(retained[0].id, "message-5");
  assert.equal(retained.at(-1).id, "message-204");
});

test("web state helpers reject stale scope and request IDs and keep drafts owner-scoped", () => {
  const active = { routerUrl: "http://router-one.test", agentId: "web-user" };
  assert.equal(matchesActiveScope({ type: "agents", routerUrl: active.routerUrl, agentId: active.agentId }, active), true);
  assert.equal(matchesActiveScope({ type: "agents", routerUrl: "http://router-two.test", agentId: active.agentId }, active), false);
  assert.equal(matchesActiveScope({ type: "agents", routerUrl: active.routerUrl, agentId: "other-user" }, active), false);
  assert.equal(historyRequestMatches({ peer: "peer", requestId: "fresh" }, "peer", "fresh"), true);
  assert.equal(historyRequestMatches({ peer: "peer", requestId: "stale" }, "peer", "fresh"), false);
  assert.equal(historyRequestMatches({ peer: "other-peer", requestId: "fresh" }, "peer", "fresh"), false);

  const firstScope = draftScope(active.routerUrl, active.agentId, "peer");
  const otherRouterScope = draftScope("http://router-two.test", active.agentId, "peer");
  const otherOwnerScope = draftScope(active.routerUrl, "other-user", "peer");
  const otherPeerScope = draftScope(active.routerUrl, active.agentId, "other-peer");
  assert.equal(new Set([firstScope, otherRouterScope, otherOwnerScope, otherPeerScope]).size, 4);
  const now = Date.parse("2026-08-03T00:00:00.000Z");
  assert.deepEqual(pruneDrafts({
    [firstScope]: { body: "first draft", updatedAtMs: now },
    [otherOwnerScope]: { body: "second draft", updatedAtMs: now },
    expired: { body: "old", updatedAtMs: 0 },
  }, now), {
    [firstScope]: { body: "first draft", updatedAtMs: now },
    [otherOwnerScope]: { body: "second draft", updatedAtMs: now },
  });
});

test("web send-state helpers preserve failed drafts and replace success with canonical attachment metadata", () => {
  assert.equal(canSendDraft("", "hello", 0), false);
  assert.equal(canSendDraft("peer", "   ", 0), false);
  assert.equal(canSendDraft("peer", "", 1), true);
  assert.equal(canSendDraft("peer", "hello", 0), true);

  const pending = {
    id: "local-id",
    from: "web-user",
    to: "peer",
    body: "pending",
    pending: true,
    attachments: [{ url: "data:image/webp;base64,pending", name: "pending.webp" }],
  };
  const failed = settlePendingMessages([pending], "local-id", false);
  assert.equal(failed[0].pending, false);
  assert.equal(failed[0].failed, true);
  assert.deepEqual(failed[0].attachments, pending.attachments);

  const canonical = {
    id: "router-id",
    message_incarnation_id: "router-incarnation",
    sequence: 7,
    from: "web-user",
    to: "peer",
    body: "pending",
    attachments: [{ id: "stored.webp", url: "http://router.test/v1/attachments/stored.webp", mime_type: "image/webp" }],
  };
  const settled = replacePendingMessage(failed, "local-id", canonical);
  assert.equal(settled.length, 1);
  assert.equal(settled[0].id, "router-id");
  assert.equal(settled[0].message_incarnation_id, "router-incarnation");
  assert.equal(settled[0].sequence, 7);
  assert.equal(settled[0].direction, "outgoing");
  assert.deepEqual(settled[0].attachments, canonical.attachments);
});
