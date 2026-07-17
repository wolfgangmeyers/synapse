import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import { DatabaseSync } from "node:sqlite";

import { openMessageRepository } from "../harness/db.js";

const migrationIds = [
  "001_initial",
  "002_delivery_attempt_deadline",
  "003_reminders",
  "004_server_jobs",
  "005_message_incarnation",
  "006_sentinel",
  "007_sentinel_occurrences",
  "008_sentinel_deadline",
];

function applyLegacyMigrations(database) {
  database.exec("CREATE TABLE schema_migrations (id TEXT PRIMARY KEY, applied_at TEXT NOT NULL)");
  for (const id of migrationIds) {
    database.exec(readFileSync(new URL(`../harness/migrations/${id}.sql`, import.meta.url), "utf8"));
    database.prepare("INSERT INTO schema_migrations (id, applied_at) VALUES (?, ?)")
      .run(id, "2030-01-01T00:00:00.000Z");
  }
}

test("006 applies Sentinel schema after an existing Phase5 message-incarnation migration", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-incarnation-compat-migration-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const databasePath = join(directory, "router.sqlite");
  const legacy = new DatabaseSync(databasePath);
  legacy.exec("CREATE TABLE schema_migrations (id TEXT PRIMARY KEY, applied_at TEXT NOT NULL)");
  for (const id of migrationIds.slice(0, 5)) {
    legacy.exec(readFileSync(new URL(`../harness/migrations/${id}.sql`, import.meta.url), "utf8"));
    legacy.prepare("INSERT INTO schema_migrations (id, applied_at) VALUES (?, ?)")
      .run(id, "2030-01-01T00:00:00.000Z");
  }
  legacy.prepare("INSERT INTO agents (agent_id, first_seen_at) VALUES ('receiver', '2030-01-01T00:00:00.000Z')").run();
  for (const [id, incarnation] of [["legacy-incarnation-1", "550e8400-e29b-41d4-a716-446655440000"], ["legacy-incarnation-2", "650e8400-e29b-41d4-a716-446655440000"]]) {
    legacy.prepare(`
      INSERT INTO messages (id, sender, recipient, subject, body, created_at, expires_at, state, message_incarnation_id)
      VALUES (?, 'sender', 'receiver', NULL, 'opaque', '2030-01-01T00:00:00.000Z', '2030-01-01T01:00:00.000Z', 'queued', ?)
    `).run(id, incarnation);
  }
  legacy.close();

  const repository = openMessageRepository({ databasePath });
  t.after(() => repository.close());
  assert.ok(repository.migrationIds().includes("005_message_incarnation"));
  assert.ok(repository.migrationIds().includes("006_sentinel"));
});

test("009 forwards a 005/006/007/008 database and preserves Sentinel reminder eligibility", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-sentinel-migration-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const databasePath = join(directory, "router.sqlite");
  const legacy = new DatabaseSync(databasePath);
  applyLegacyMigrations(legacy);
  legacy.prepare("INSERT INTO agents (agent_id, first_seen_at) VALUES (?, ?)")
    .run("receiver", "2030-01-01T00:00:00.000Z");
  legacy.prepare("INSERT INTO agents (agent_id, first_seen_at) VALUES (?, ?)")
    .run("receiver-2", "2030-01-01T00:00:00.000Z");
  legacy.prepare("INSERT INTO agents (agent_id, first_seen_at) VALUES (?, ?)")
    .run("receiver-3", "2030-01-01T00:00:00.000Z");
  legacy.prepare("INSERT INTO agents (agent_id, first_seen_at) VALUES (?, ?)")
    .run("receiver-4", "2030-01-01T00:00:00.000Z");
  legacy.prepare(`
    INSERT INTO messages (
      id, sender, recipient, subject, body, created_at, expires_at, sentinel_json, state
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'injection_requested')
  `).run(
    "legacy-message", "supervisor", "receiver", null, "secret",
    "2030-01-01T00:00:00.000Z", "2030-01-01T01:00:00.000Z",
    JSON.stringify({ receiver_policy: "managed", model: "gpt-5.6-terra" }),
  );
  legacy.prepare(`
    INSERT INTO delivery_attempts (
      message_id, attempt_number, lease_id, owner_lease_id, state, created_at, updated_at, delivery_expires_at
    ) VALUES (?, 1, NULL, 'legacy-owner', 'injection_requested', ?, ?, ?)
  `).run(
    "legacy-message", "2030-01-01T00:00:00.000Z", "2030-01-01T00:00:00.000Z", "2030-01-01T00:01:00.000Z",
  );
  legacy.prepare(`
    INSERT INTO messages (
      id, sender, recipient, subject, body, created_at, expires_at, sentinel_json, state
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'leased')
  `).run(
    "legacy-deadline-less-message", "supervisor", "receiver-2", null, "secret",
    "2030-01-01T00:00:00.000Z", "2030-01-01T01:00:00.000Z",
    JSON.stringify({ receiver_policy: "managed", model: "gpt-5.6-terra" }),
  );
  legacy.prepare(`
    INSERT INTO messages (
      id, sender, recipient, subject, body, created_at, expires_at, sentinel_json, state
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'leased')
  `).run(
    "legacy-one-shot-message", "supervisor", "receiver-4", null, "secret",
    "2030-01-01T00:00:00.000Z", "2030-01-01T01:00:00.000Z",
    JSON.stringify({ receiver_policy: "managed", model: "gpt-5.6-terra" }),
  );
  legacy.prepare(`
    INSERT INTO delivery_attempts (
      message_id, attempt_number, lease_id, owner_lease_id, state, created_at, updated_at, delivery_expires_at
    ) VALUES (?, 1, NULL, 'legacy-owner', 'leased', ?, ?, ?)
  `).run(
    "legacy-one-shot-message", "2030-01-01T00:00:00.000Z", "2030-01-01T00:00:00.000Z", "2030-01-01T00:01:00.000Z",
  );
  legacy.prepare(`
    INSERT INTO reminders (
      id, target, creator, subject, body, created_at, next_due_at,
      interval_ms, state, linked_message_id, occurrence_number, attempt_count, last_attempt_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, NULL, 'message_queued', 'legacy-one-shot-message', 1, 0, NULL)
  `).run(
    "legacy-one-shot-reminder", "receiver-4", "supervisor", null, "legacy-once",
    "2030-01-01T00:00:00.000Z", "2030-01-02T00:00:00.000Z",
  );
  legacy.prepare(`
    INSERT INTO messages (
      id, sender, recipient, subject, body, created_at, expires_at, sentinel_json, state
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'leased')
  `).run(
    "legacy-deadline-less-launching-message", "supervisor", "receiver-3", null, "secret",
    "2030-01-01T00:00:00.000Z", "2030-01-01T01:00:00.000Z",
    JSON.stringify({ receiver_policy: "managed", model: "gpt-5.6-terra" }),
  );
  legacy.prepare(`
    INSERT INTO reminders (
      id, target, creator, subject, body, created_at, next_due_at,
      interval_ms, state, linked_message_id, occurrence_number, attempt_count, last_attempt_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, 60000, 'scheduled', 'legacy-message', 1, 0, NULL)
  `).run(
    "legacy-reminder", "receiver", "supervisor", null, "legacy",
    "2030-01-01T00:00:00.000Z", "2030-01-02T00:00:00.000Z",
  );
  legacy.prepare(`
    INSERT INTO sentinel_occurrences (
      occurrence_id, message_id, state, attempt_id, launch_deadline_at, created_at, updated_at
    ) VALUES (?, ?, 'launching', ?, ?, ?, ?)
  `).run(
    "legacy-occurrence", "legacy-message", "legacy-attempt",
    "2030-01-01T00:01:00.000Z", "2030-01-01T00:00:00.000Z", "2030-01-01T00:00:00.000Z",
  );
  legacy.prepare(`
    INSERT INTO sentinel_occurrences (
      occurrence_id, message_id, state, attempt_id, launch_deadline_at, created_at, updated_at
    ) VALUES (?, ?, 'launching', ?, ?, ?, ?)
  `).run(
    "legacy-one-shot-occurrence", "legacy-one-shot-message", "legacy-one-shot-attempt",
    "2030-01-01T00:01:00.000Z", "2030-01-01T00:00:00.000Z", "2030-01-01T00:00:00.000Z",
  );
  legacy.prepare(`
    INSERT INTO sentinel_occurrences (
      occurrence_id, message_id, state, attempt_id, launch_deadline_at, created_at, updated_at
    ) VALUES (?, ?, 'failed', ?, NULL, ?, ?)
  `).run(
    "legacy-failed-occurrence", "legacy-message", "legacy-failed-attempt",
    "2000-01-01T00:00:00.000Z", "2000-01-01T00:00:00.000Z",
  );
  legacy.prepare(`
    INSERT INTO sentinel_occurrences (
      occurrence_id, message_id, state, attempt_id, launch_deadline_at, created_at, updated_at
    ) VALUES (?, ?, 'claimed', ?, NULL, ?, ?)
  `).run(
    "legacy-deadline-less-occurrence", "legacy-deadline-less-message", "legacy-deadline-less-attempt",
    "2030-01-01T00:00:00.000Z", "2030-01-01T00:00:00.000Z",
  );
  legacy.prepare(`
    INSERT INTO sentinel_occurrences (
      occurrence_id, message_id, state, attempt_id, launch_deadline_at, created_at, updated_at
    ) VALUES (?, ?, 'launching', ?, NULL, ?, ?)
  `).run(
    "legacy-deadline-less-launching-occurrence", "legacy-deadline-less-launching-message", "legacy-deadline-less-launching-attempt",
    "2030-01-01T00:00:00.000Z", "2030-01-01T00:00:00.000Z",
  );
  legacy.close();

  const repository = openMessageRepository({
    databasePath,
    clock: () => new Date("2030-01-01T00:00:00.000Z"),
  });
  t.after(() => repository.close());

  assert.deepEqual(repository.migrationIds().slice(-7), [
    "005_message_incarnation",
    "006_sentinel",
    "007_sentinel_occurrences",
    "008_sentinel_deadline",
    "009_sentinel_tombstones",
    "010_sentinel_exit_outcome",
    "011_sentinel_relay_capabilities",
  ]);
  const migratedDatabase = new DatabaseSync(databasePath);
  const occurrenceColumns = migratedDatabase.prepare("PRAGMA table_info(sentinel_occurrences)").all().map(({ name }) => name);
  migratedDatabase.close();
  assert.deepEqual(occurrenceColumns, [
    "occurrence_id",
    "message_id",
    "state",
    "attempt_id",
    "transition_sequence",
    "child_identity",
    "created_at",
    "updated_at",
    "launch_deadline_at",
    "terminal_at",
    "recipient",
    "claim_lease_id",
    "exit_code",
  ]);
  const migratedReminder = repository.reminderFor("legacy-reminder");
  assert.equal(migratedReminder.sentinel, undefined);
  assert.equal(migratedReminder.body, "legacy");
  assert.equal(migratedReminder.next_due_at, "2030-01-02T00:00:00.000Z");
  assert.equal(migratedReminder.state, "scheduled");
  assert.equal(migratedReminder.linked_message_id, null);
  const migratedOneShotReminder = repository.reminderFor("legacy-one-shot-reminder");
  assert.equal(migratedOneShotReminder.body, "legacy-once");
  assert.equal(migratedOneShotReminder.state, "failed");
  assert.equal(migratedOneShotReminder.linked_message_id, null);
  assert.equal(repository.deliveryAttemptsFor("legacy-one-shot-message")[0].state, "timed_out");
  assert.equal(repository.messageFor("legacy-one-shot-message").state, "dead_letter");
  assert.equal(repository.messageFor("legacy-one-shot-message").body, "");
  assert.equal(repository.sentinelState("legacy-one-shot-occurrence").state, "failed");
  assert.equal(repository.sentinelState("legacy-occurrence").state, "failed");
  assert.equal(repository.messageFor("legacy-message").state, "dead_letter");
  assert.equal(repository.messageFor("legacy-message").body, "");
  assert.equal(repository.deliveryAttemptsFor("legacy-message")[0].state, "timed_out");
  assert.equal(repository.sentinelState("legacy-failed-occurrence").terminal_at, "2000-01-01T00:00:00.000Z");
  assert.equal(repository.recoverSentinelDeadlines(), 0);
  assert.equal(repository.sentinelState("legacy-deadline-less-occurrence").state, "failed");
  assert.equal(repository.messageFor("legacy-deadline-less-message").state, "dead_letter");
  assert.equal(repository.sentinelState("legacy-deadline-less-launching-occurrence").state, "failed");
  assert.equal(repository.messageFor("legacy-deadline-less-launching-message").state, "dead_letter");
  assert.equal(repository.cleanupSentinelTombstones("2030-01-02T00:00:00.000Z"), 5);
  assert.equal(repository.sentinelState("legacy-failed-occurrence"), undefined);

  repository.createReminder({
    id: "sentinel-reminder",
    target: "receiver",
    creator: "supervisor",
    body: "secret",
    created_at: "2030-01-01T00:00:00.000Z",
    next_due_at: "2030-01-01T00:00:00.000Z",
    interval_ms: null,
    sentinel: { receiver_policy: "managed", model: "gpt-5.6-terra" },
  });
  const fired = repository.fireDueReminders().find(({ reminder }) => reminder.id === "sentinel-reminder");
  assert.deepEqual(fired.message.sentinel, { receiver_policy: "managed", model: "gpt-5.6-terra", origin_sender: "supervisor" });
});
