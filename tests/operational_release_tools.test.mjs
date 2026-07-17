import assert from "node:assert/strict";
import { mkdtemp, rm } from "node:fs/promises";
import { spawnSync } from "node:child_process";
import { DatabaseSync } from "node:sqlite";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

const python = process.env.PYTHON ?? "python3";

test("SQLite snapshot preflight backs up WAL-visible data and verifies a restore copy", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-release-tools-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const databasePath = join(directory, "router.sqlite");
  const database = new DatabaseSync(databasePath);
  database.exec("PRAGMA journal_mode=WAL; CREATE TABLE schema_migrations (id TEXT PRIMARY KEY); INSERT INTO schema_migrations VALUES ('012_sentinel_native_cleanup');");
  const snapshot = join(directory, "snapshot.sqlite");
  const restored = join(directory, "restore-check.sqlite");
  const result = spawnSync(python, ["tools/sqlite_snapshot_preflight.py", "--database", databasePath, "--snapshot", snapshot, "--restore-check", restored], { encoding: "utf8" });
  database.close();
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /snapshot_integrity=ok migrations=1 restore_integrity=ok/);
  const check = new DatabaseSync(restored, { readOnly: true });
  assert.equal(check.prepare("SELECT id FROM schema_migrations").get().id, "012_sentinel_native_cleanup");
  check.close();
});
