import assert from "node:assert/strict";
import test from "node:test";

import { messageTime, reconcileHistoryMessages } from "../web/router-ui/history.js";

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

test("history ordering and local retention use trusted router time", () => {
  assert.equal(messageTime({
    created_at: "1999-01-01T00:00:00.000Z",
    recorded_at: "2026-08-03T00:00:00.000Z",
  }), Date.parse("2026-08-03T00:00:00.000Z"));
});
