import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import { createJsonlLogger } from "../harness/jsonl_logger.js";

test("JSONL logger persists declared mailbox events and rejects body-bearing fields", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-jsonl-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const logPath = join(directory, "router.jsonl");
  const logger = createJsonlLogger({ logPath, clock: () => new Date("2030-01-01T00:00:00.000Z") });

  assert.equal(logger.log("message_enqueued", {
    message_id: "message", from: "sender", to: "receiver", agent_id: "receiver", state: "queued", created: true,
  }), true);
  assert.equal(logger.log("message_enqueued", {
    message_id: "bad", from: "sender", to: "receiver", agent_id: "receiver", state: "queued", created: true, body: "private",
  }), false);

  const records = (await readFile(logPath, "utf8")).trim().split("\n").map(JSON.parse);
  assert.deepEqual(records, [{
    timestamp: "2030-01-01T00:00:00.000Z", event: "message_enqueued", message_id: "message",
    from: "sender", to: "receiver", agent_id: "receiver", state: "queued", created: true,
  }]);
});
