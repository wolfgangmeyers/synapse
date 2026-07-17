import assert from "node:assert/strict";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { spawnSync } from "node:child_process";
import { DatabaseSync } from "node:sqlite";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

const python = process.env.PYTHON ?? "python3";

function run(script, args) {
  return spawnSync(python, [script, ...args], { encoding: "utf8" });
}

test("body-free Sentinel smoke verifier accepts one fixed pre-ack relay audit", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-release-tools-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const log = join(directory, "router.jsonl");
  await writeFile(log, [
    JSON.stringify({ event: "message_enqueued", message_id: "child-result", from: "watchdog", to: "supervisor", created: true, sentinel_child_occurrence_id: "smoke" }),
    JSON.stringify({ event: "delivery_offered", message_id: "child-result", agent_id: "supervisor" }),
  ].join("\n") + "\n");
  const result = run("tools/sentinel_smoke_verify.py", ["--router-log", log, "--occurrence-id", "smoke", "--from", "watchdog", "--to", "supervisor", "--timeout-seconds", "1"]);
  assert.equal(result.status, 0, result.stderr);
  assert.deepEqual(JSON.parse(result.stdout), { occurrence_id: "smoke", fixed_recipient: true, relay_count: 1, delivery_offered: true, body_absent: true });
});

test("SQLite snapshot preflight backs up WAL-visible data and verifies a restore copy", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-release-tools-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const databasePath = join(directory, "router.sqlite");
  const database = new DatabaseSync(databasePath);
  database.exec("PRAGMA journal_mode=WAL; CREATE TABLE schema_migrations (id TEXT PRIMARY KEY); INSERT INTO schema_migrations VALUES ('011_sentinel_relay_capabilities');");
  const snapshot = join(directory, "snapshot.sqlite");
  const restored = join(directory, "restore-check.sqlite");
  const result = run("tools/sqlite_snapshot_preflight.py", ["--database", databasePath, "--snapshot", snapshot, "--restore-check", restored]);
  database.close();
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /snapshot_integrity=ok migrations=1 restore_integrity=ok/);
  const check = new DatabaseSync(restored, { readOnly: true });
  assert.equal(check.prepare("SELECT id FROM schema_migrations").get().id, "011_sentinel_relay_capabilities");
  check.close();
});
