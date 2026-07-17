import assert from "node:assert/strict";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { DatabaseSync } from "node:sqlite";
import test from "node:test";

import { openMessageRepository } from "../harness/db.js";

test("forward cleanup removes the retired Sentinel relay lifecycle tables", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-sentinel-cleanup-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const databasePath = join(directory, "router.sqlite");
  const repository = openMessageRepository({ databasePath });
  assert.ok(repository.migrationIds().includes("012_sentinel_native_cleanup"));
  repository.close();

  const database = new DatabaseSync(databasePath, { readOnly: true });
  const retired = database.prepare("SELECT name FROM sqlite_master WHERE type = 'table' AND name IN ('sentinel_occurrences', 'sentinel_relay_capabilities')").all();
  database.close();
  assert.deepEqual(retired, []);
});

test("forward cleanup retains only Sentinel model metadata on existing records", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-sentinel-model-cleanup-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const databasePath = join(directory, "router.sqlite");
  const repository = openMessageRepository({ databasePath });
  repository.close();

  const legacy = new DatabaseSync(databasePath);
  legacy.exec("DELETE FROM schema_migrations WHERE id = '012_sentinel_native_cleanup'");
  legacy.prepare("INSERT INTO agents (agent_id, first_seen_at) VALUES (?, ?)").run("target", "2026-01-01T00:00:00.000Z");
  const sentinel = JSON.stringify({ receiver_policy: "retired", origin_sender: "sender", model: "gpt-5.6-terra" });
  legacy.prepare("INSERT INTO messages (id, sender, recipient, body, created_at, expires_at, state, sentinel_json) VALUES (?, ?, ?, ?, ?, ?, ?, ?)")
    .run("legacy-message", "sender", "target", "opaque", "2026-01-01T00:00:00.000Z", "2026-01-02T00:00:00.000Z", "queued", sentinel);
  legacy.prepare("INSERT INTO reminders (id, target, creator, body, created_at, next_due_at, interval_ms, state, occurrence_number, sentinel_json) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)")
    .run("legacy-reminder", "target", "sender", "opaque", "2026-01-01T00:00:00.000Z", "2026-01-01T01:00:00.000Z", null, "scheduled", 0, sentinel);
  legacy.close();

  const migrated = openMessageRepository({ databasePath });
  migrated.close();
  const database = new DatabaseSync(databasePath, { readOnly: true });
  const records = database.prepare("SELECT sentinel_json FROM messages UNION ALL SELECT sentinel_json FROM reminders").all();
  database.close();
  assert.deepEqual(records.map((row) => JSON.parse(row.sentinel_json)), [
    { model: "gpt-5.6-terra" },
    { model: "gpt-5.6-terra" },
  ]);
});
