import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import { DatabaseSync } from "node:sqlite";

import { openMessageRepository } from "../harness/db.js";

const legacyMigrations = [
  "001_initial",
  "002_delivery_attempt_deadline",
  "003_reminders",
  "004_server_jobs",
  "005_message_incarnation",
  "006_sentinel",
  "007_sentinel_occurrences",
  "008_sentinel_deadline",
];

test("009 preserves an observed launched child while completing legacy uncertain launches", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-sentinel-launched-migration-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const databasePath = join(directory, "router.sqlite");
  const legacy = new DatabaseSync(databasePath);
  legacy.exec("CREATE TABLE schema_migrations (id TEXT PRIMARY KEY, applied_at TEXT NOT NULL)");
  for (const id of legacyMigrations) {
    legacy.exec(readFileSync(new URL(`../harness/migrations/${id}.sql`, import.meta.url), "utf8"));
    legacy.prepare("INSERT INTO schema_migrations (id, applied_at) VALUES (?, ?)")
      .run(id, "2030-01-01T00:00:00.000Z");
  }
  legacy.prepare("INSERT INTO agents (agent_id, first_seen_at) VALUES (?, ?)")
    .run("receiver", "2030-01-01T00:00:00.000Z");
  legacy.prepare(`
    INSERT INTO messages (id, sender, recipient, subject, body, created_at, expires_at, sentinel_json, state)
    VALUES (?, ?, ?, NULL, ?, ?, ?, ?, 'leased')
  `).run(
    "launched-message", "supervisor", "receiver", "opaque",
    "2030-01-01T00:00:00.000Z", "2030-01-01T01:00:00.000Z",
    JSON.stringify({ receiver_policy: "managed", model: "gpt-5.6-terra" }),
  );
  legacy.prepare(`
    INSERT INTO sentinel_occurrences (
      occurrence_id, message_id, state, attempt_id, child_identity, launch_deadline_at, created_at, updated_at
    ) VALUES (?, ?, 'launched', ?, ?, ?, ?, ?)
  `).run(
    "launched-occurrence", "launched-message", "attempt", "child",
    "2000-01-01T00:00:00.000Z", "2030-01-01T00:00:00.000Z", "2030-01-01T00:00:00.000Z",
  );
  legacy.close();

  const repository = openMessageRepository({ databasePath, clock: () => new Date("2030-01-01T00:00:00.000Z") });
  t.after(() => repository.close());
  assert.equal(repository.sentinelState("launched-occurrence").state, "launched");
  assert.equal(repository.messageFor("launched-message").body, "opaque");
  assert.equal(repository.recoverSentinelDeadlines(), 0);
});
