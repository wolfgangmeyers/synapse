import assert from "node:assert/strict";
import { existsSync, mkdirSync, statSync } from "node:fs";
import { readFile } from "node:fs/promises";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import { createJsonlLogger, DEFAULT_MAX_LOG_BYTES, rotateJsonlLog } from "../harness/jsonl_logger.js";

test("JSONL logger persists declared mailbox events and rejects body-bearing fields", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-jsonl-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const logPath = join(directory, "router.jsonl");
  const logger = createJsonlLogger({ logPath, clock: () => new Date("2030-01-01T00:00:00.000Z") });

  assert.equal(logger.log("message_enqueued", {
    message_id: "message", from: "sender", to: "receiver", agent_id: "receiver", state: "queued", created: true,
  }), true);
  assert.equal(logger.log("message_enqueued", {
    message_id: "duplicate", from: "sender", to: "receiver", agent_id: "receiver", state: "queued", created: false, deduped: true,
  }), true);
  assert.equal(logger.log("message_enqueued", {
    message_id: "bad", from: "sender", to: "receiver", agent_id: "receiver", state: "queued", created: true, body: "private",
  }), false);

  const records = (await readFile(logPath, "utf8")).trim().split("\n").map(JSON.parse);
  assert.deepEqual(records, [
    {
      timestamp: "2030-01-01T00:00:00.000Z", event: "message_enqueued", message_id: "message",
      from: "sender", to: "receiver", agent_id: "receiver", state: "queued", created: true,
    },
    {
      timestamp: "2030-01-01T00:00:00.000Z", event: "message_enqueued", message_id: "duplicate",
      from: "sender", to: "receiver", agent_id: "receiver", state: "queued", created: false, deduped: true,
    },
  ]);
});

test("JSONL logger persists correlated model-control events without payload content", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-jsonl-model-control-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const logPath = join(directory, "router.jsonl");
  const logger = createJsonlLogger({ logPath, clock: () => new Date("2030-01-01T00:00:00.000Z") });

  assert.equal(logger.log("model_control_forwarded", {
    control_id: "control-1",
    source_agent_id: "controller",
    source_instance_id: "controller-instance",
    target_agent_id: "worker",
    target_instance_id: "worker-instance",
    target_lease_id: "worker-lease",
    requested_provider: "openai-codex",
    requested_model_id: "gpt-5.6-terra",
  }), true);
  assert.equal(logger.log("model_control_result_received", {
    control_id: "control-1",
    source_agent_id: "controller",
    target_agent_id: "worker",
    target_instance_id: "worker-instance",
    target_lease_id: "worker-lease",
    accepted: true,
    current_provider: "openai-codex",
    current_model_id: "gpt-5.6-terra",
  }), true);
  assert.equal(logger.log("model_control_forwarded", {
    control_id: "control-private",
    source_agent_id: "controller",
    source_instance_id: "controller-instance",
    target_agent_id: "worker",
    target_instance_id: "worker-instance",
    target_lease_id: "worker-lease",
    requested_provider: "openai-codex",
    requested_model_id: "gpt-5.6-terra",
    body: "private",
  }), false);

  const records = (await readFile(logPath, "utf8")).trim().split("\n").map(JSON.parse);
  assert.deepEqual(records.map(({ event, control_id, accepted }) => ({
    event,
    control_id,
    ...(accepted === undefined ? {} : { accepted }),
  })), [
    { event: "model_control_forwarded", control_id: "control-1" },
    { event: "model_control_result_received", control_id: "control-1", accepted: true },
  ]);
});

test("JSONL logger rotates at the size limit and keeps on-disk usage bounded", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-jsonl-rotate-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const logPath = join(directory, "router.jsonl");
  const maxBytes = 2048;
  const logger = createJsonlLogger({
    logPath,
    clock: () => new Date("2030-01-01T00:00:00.000Z"),
    maxBytes,
  });

  const write = (index) => logger.log("agent_lease_renewed", {
    agent_id: `agent-${index}`, instance_id: `instance-${index}`, lease_id: `lease-${index}`,
  });

  // One record is ~130 bytes, so 500 of them would be ~65 KB unrotated.
  for (let index = 0; index < 500; index += 1) assert.equal(write(index), true);

  assert.ok(existsSync(`${logPath}.1`), "expected the live log to rotate to <log>.1");
  assert.equal(existsSync(`${logPath}.2`), false, "only one rotated generation is kept");
  const liveSize = statSync(logPath).size;
  const rotatedSize = statSync(`${logPath}.1`).size;
  // The size is checked before the append, so either file may exceed the limit by at
  // most one record. Anything beyond that means rotation is not actually happening.
  const recordSlack = 256;
  assert.ok(liveSize < maxBytes + recordSlack, `live log must stay at the limit, got ${liveSize}`);
  assert.ok(rotatedSize < maxBytes + recordSlack, `rotated log must stay at the limit, got ${rotatedSize}`);
  assert.ok(
    liveSize + rotatedSize < (2 * maxBytes) + (2 * recordSlack),
    `total on-disk usage must stay bounded, got ${liveSize + rotatedSize}`,
  );

  // Rotation must not lose the newest records: the last write is still readable.
  const live = (await readFile(logPath, "utf8")).trim().split("\n").map(JSON.parse);
  assert.equal(live.at(-1).agent_id, "agent-499");
});

test("a rotation that cannot complete still records the event", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-jsonl-blocked-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const logPath = join(directory, "router.jsonl");
  const maxBytes = 512;
  const logger = createJsonlLogger({
    logPath,
    clock: () => new Date("2030-01-01T00:00:00.000Z"),
    maxBytes,
  });

  const write = (index) => logger.log("agent_lease_renewed", {
    agent_id: `agent-${index}`, instance_id: `instance-${index}`, lease_id: `lease-${index}`,
  });
  assert.equal(write(0), true);

  // A non-empty directory where the rotated generation belongs: rmSync cannot remove
  // it, so every rotation attempt from here on throws.
  await rm(`${logPath}.1`, { recursive: true, force: true });
  mkdirSync(join(`${logPath}.1`, "blocker"), { recursive: true });

  // Losing retention must not lose the log. Silence would be indistinguishable from
  // an idle router now that idle sweeps are not logged.
  for (let index = 1; index < 40; index += 1) assert.equal(write(index), true);

  assert.ok(statSync(logPath).size > maxBytes, "the log grows rather than going silent");
  assert.throws(() => rotateJsonlLog(logPath, maxBytes), "rotation itself is still failing");
  const records = (await readFile(logPath, "utf8")).trim().split("\n").map(JSON.parse);
  assert.equal(records.at(-1).agent_id, "agent-39");

  // A log past its documented bound must say so in band, once, rather than leaving the
  // overrun visible only to whoever thinks to check the file size.
  const failures = records.filter((record) => record.event === "router_error");
  assert.deepEqual(failures.map(({ error_code }) => error_code), ["log_rotation_failed"]);
  assert.equal(records.length, 41, "39 events plus the first record and one failure marker");
});

test("JSONL log rotation is a no-op below the limit and for a missing log", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-jsonl-norotate-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const logPath = join(directory, "router.jsonl");

  assert.equal(rotateJsonlLog(logPath, 1024), false);
  assert.equal(existsSync(`${logPath}.1`), false);

  const logger = createJsonlLogger({ logPath, clock: () => new Date("2030-01-01T00:00:00.000Z") });
  assert.equal(logger.log("router_stopped", {}), true);
  assert.equal(rotateJsonlLog(logPath, 1024), false);
  assert.equal(existsSync(`${logPath}.1`), false);

  assert.equal(DEFAULT_MAX_LOG_BYTES, 16 * 1024 * 1024);
  assert.throws(() => createJsonlLogger({ logPath, maxBytes: 0 }), /maxBytes/);
  assert.throws(() => rotateJsonlLog(logPath, -1), /maxBytes/);
});
