import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import { createJsonlLogger } from "../harness/jsonl_logger.js";

test("JSONL logger persists declared body-free Sentinel audit events and rejects unknown or body-bearing fields", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-jsonl-sentinel-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const logPath = join(directory, "router.jsonl");
  const logger = createJsonlLogger({ logPath, clock: () => new Date("2030-01-01T00:00:00.000Z") });

  assert.equal(logger.log("sentinel_state_recorded", {
    message_id: "message", occurrence_id: "occurrence", attempt_id: "attempt", state: "launched",
  }), true);
  assert.equal(logger.log("message_enqueued", {
    message_id: "result", from: "watchdog", to: "supervisor", agent_id: "supervisor", state: "queued", created: true,
    sentinel_child_occurrence_id: "occurrence",
  }), true);
  assert.equal(logger.log("message_enqueued", {
    message_id: "bad", from: "watchdog", to: "supervisor", agent_id: "supervisor", state: "queued", created: true,
    sentinel_child_occurrence_id: "occurrence", body: "private",
  }), false);
  assert.equal(logger.log("sentinel_state_recorded", {
    occurrence_id: "occurrence", attempt_id: "attempt", state: "launched", unknown: "field",
  }), false);

  const records = (await readFile(logPath, "utf8")).trim().split("\n").map(JSON.parse);
  assert.deepEqual(records, [
    { timestamp: "2030-01-01T00:00:00.000Z", event: "sentinel_state_recorded", message_id: "message", occurrence_id: "occurrence", attempt_id: "attempt", state: "launched" },
    { timestamp: "2030-01-01T00:00:00.000Z", event: "message_enqueued", message_id: "result", from: "watchdog", to: "supervisor", agent_id: "supervisor", state: "queued", created: true, sentinel_child_occurrence_id: "occurrence" },
  ]);
});
