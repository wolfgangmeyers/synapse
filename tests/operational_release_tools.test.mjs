import assert from "node:assert/strict";
import { existsSync, statSync } from "node:fs";
import { mkdir, mkdtemp, open, readdir, readFile, rm, symlink, writeFile } from "node:fs/promises";
import { spawnSync } from "node:child_process";
import { DatabaseSync } from "node:sqlite";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

const python = process.env.PYTHON ?? "python3";
const tool = "tools/sqlite_snapshot_preflight.py";

// Every run goes through a shell that fixes the umask first. The tool's job is
// to create files 0600 no matter what the environment would have given it, and
// under a umask of 077 the 0600 assertions below pass even with the fix removed.
function runTool(args) {
  return spawnSync("/bin/sh", ["-c", 'umask 022; exec "$0" "$@"', python, tool, ...args], { encoding: "utf8" });
}

// Loads the tool as a module and runs `body` against it. Several branches need
// a failure injected at a point the command line cannot reach: nothing an
// operator can pass in makes a freshly written snapshot refuse to leave WAL, or
// makes an identical copy of a healthy snapshot fail its own verification.
async function runDriver(directory, body) {
  const script = join(directory, "driver.py");
  await writeFile(script, [
    "import importlib.util, os, sqlite3, sys, time",
    `spec = importlib.util.spec_from_file_location("preflight", ${JSON.stringify(tool)})`,
    "preflight = importlib.util.module_from_spec(spec)",
    "spec.loader.exec_module(preflight)",
    "os.umask(0o022)",
    // Work inside the directory the test already cleans up, so a driver never
    // leaves a copy of its fixture database behind in the system temp dir.
    "workspace = os.path.join(sys.argv[1], 'workspace')",
    "os.mkdir(workspace)",
    "database = os.path.join(workspace, 'router.sqlite')",
    "snapshot = os.path.join(workspace, 'snapshot.sqlite')",
    "restore_check = os.path.join(workspace, 'restore-check.sqlite')",
    body,
  ].join("\n"));
  return spawnSync(python, [script, directory], { encoding: "utf8" });
}

function permissions(path) {
  return (statSync(path).mode & 0o777).toString(8);
}

// A migrations database the tool should be able to snapshot and verify.
const ROUTER_FIXTURE = `
connection = sqlite3.connect(database)
connection.execute("CREATE TABLE schema_migrations (id TEXT PRIMARY KEY)")
connection.execute("INSERT INTO schema_migrations VALUES ('012_sentinel_native_cleanup')")
connection.commit()
connection.close()
sys.argv = ["preflight", "--database", database, "--snapshot", snapshot, "--restore-check", restore_check]
`;

test("SQLite snapshot preflight backs up WAL-visible data and verifies a restore copy", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-release-tools-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const databasePath = join(directory, "router.sqlite");
  const database = new DatabaseSync(databasePath);
  database.exec("PRAGMA journal_mode=WAL; CREATE TABLE schema_migrations (id TEXT PRIMARY KEY); INSERT INTO schema_migrations VALUES ('012_sentinel_native_cleanup');");
  const snapshot = join(directory, "snapshot.sqlite");
  const restored = join(directory, "restore-check.sqlite");
  const result = runTool(["--database", databasePath, "--snapshot", snapshot, "--restore-check", restored]);
  database.close();
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /snapshot_integrity=ok migrations=1 restore_integrity=ok/);
  // The snapshot must itself be readable read-only, which a WAL-headed file
  // with no sidecars is not, and it must not leave sidecars behind.
  const snapshotCheck = new DatabaseSync(snapshot, { readOnly: true });
  assert.equal(snapshotCheck.prepare("SELECT id FROM schema_migrations").get().id, "012_sentinel_native_cleanup");
  snapshotCheck.close();
  assert.equal(existsSync(`${snapshot}-wal`), false);
  assert.equal(existsSync(`${snapshot}-shm`), false);
  // The snapshot is a copy of the whole router database, so it may not be group-
  // or world-readable at any point, including while it is being written.
  assert.equal(permissions(snapshot), "600");
  // A successful run leaves the snapshot and nothing else. The restore copy is
  // a second full copy of the router database that has already proved what it
  // exists to prove, and the staging file has been published, so keeping either
  // would accumulate a copy per release and block the next run on the
  // refuse-to-overwrite guard.
  assert.equal(existsSync(restored), false, "the verified restore copy must not survive a successful run");
  const leftovers = (await readdir(directory)).filter((name) => name.includes(".partial-"));
  assert.deepEqual(leftovers, [], "a successful run must not leave its staging file behind");
  // Nothing the tool wrote survives except the snapshot itself. Scoped to the
  // tool's own outputs, since the source database's sidecars come and go with
  // the connection the test holds open.
  const written = (await readdir(directory)).filter((name) => !name.startsWith("router.sqlite"));
  assert.deepEqual(written.sort(), ["snapshot.sqlite"]);
});

test("SQLite snapshot preflight keeps the -wal/-shm sidecars whenever the mode switch is unconfirmed", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-release-tools-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const result = await runDriver(directory, `
path = snapshot
connection = sqlite3.connect(path)
connection.execute("PRAGMA journal_mode=WAL")
connection.execute("CREATE TABLE t (id TEXT)")
connection.execute("INSERT INTO t VALUES ('committed-but-not-checkpointed')")
connection.commit()

def sidecars():
    return [suffix for suffix in ("-wal", "-shm") if os.path.exists(path + suffix)]

print("start sidecars:", sidecars())

# The PRAGMA answers with a mode other than 'delete'. The stub records every
# statement it is asked to run, so a future preliminary statement — a
# wal_checkpoint, say — cannot be answered 'wal' and slip through unnoticed.
statements = []

class StubConnection:
    def execute(self, statement):
        statements.append(statement)
        return self

    def fetchone(self):
        return ("wal",)

    def close(self):
        pass

real_connect = preflight.sqlite3.connect
preflight.sqlite3.connect = lambda *args, **kwargs: StubConnection()
try:
    preflight.rewrite_out_of_wal(path)
    print("unconfirmed: NO FAILURE RAISED")
except SystemExit as error:
    print("unconfirmed:", error)
finally:
    preflight.sqlite3.connect = real_connect
print("unconfirmed statements:", statements)
print("unconfirmed sidecars:", sidecars())

# The PRAGMA cannot run at all because another connection holds a read lock.
connection.execute("BEGIN")
connection.execute("SELECT * FROM t").fetchall()
try:
    preflight.rewrite_out_of_wal(path)
    print("locked: NO FAILURE RAISED")
except BaseException as error:
    print("locked: raised", type(error).__module__ + "." + type(error).__name__)
print("locked sidecars:", sidecars())
connection.close()
`);
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /start sidecars: \['-wal', '-shm'\]/);
  assert.match(result.stdout, /unconfirmed: could not take snapshot out of WAL mode, journal_mode=\('wal',\)/);
  // Exactly the one statement, so the stub cannot be answering something else.
  assert.match(result.stdout, /unconfirmed statements: \['PRAGMA journal_mode=DELETE'\]/);
  // Which exception SQLite raises under contention is its business, not the
  // tool's, so the class is not pinned — but it must come from sqlite3, or an
  // unrelated refactor bug would satisfy this just as well.
  assert.match(result.stdout, /locked: raised sqlite3\./);
  // This is the whole point of the guard. A -wal holds committed transactions
  // that are not in the main file yet, so unlinking it beside a database that
  // is still in WAL mode destroys them.
  assert.match(result.stdout, /unconfirmed sidecars: \['-wal', '-shm'\]/);
  assert.match(result.stdout, /locked sidecars: \['-wal', '-shm'\]/);
});

test("SQLite snapshot preflight retains the snapshot when the WAL rewrite fails mid-run", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-release-tools-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  // rewrite_out_of_wal is the one step after the backup that rewrites the
  // snapshot rather than reading it, which makes it the statement most likely
  // to be moved back into the discard-on-failure half of main(). If it ever is,
  // its failure deletes a complete backup.
  const result = await runDriver(directory, `${ROUTER_FIXTURE}
def rewrite_out_of_wal(path):
    # The real guard fails only after the connection that put the snapshot into
    # WAL has left -wal/-shm beside it, so reproduce that state, not just the
    # exception. Hold the connection open so the sidecars are still there when
    # the failure message is built.
    leftover = sqlite3.connect(path)
    leftover.execute("PRAGMA journal_mode=WAL")
    leftover.execute("CREATE TABLE committed_but_not_checkpointed (id TEXT)")
    leftover.commit()
    raise SystemExit("could not take snapshot out of WAL mode, journal_mode=('wal',)")

preflight.rewrite_out_of_wal = rewrite_out_of_wal
try:
    preflight.main()
    print("NO FAILURE RAISED")
except SystemExit as error:
    print(error)
print("snapshot exists:", os.path.exists(snapshot))
print("sidecars:", [s for s in ("-wal", "-shm") if os.path.exists(snapshot + s)])
`);
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /could not take snapshot out of WAL mode/);
  assert.match(result.stdout, /retained snapshot for inspection: .*snapshot\.sqlite/);
  assert.match(result.stdout, /snapshot exists: True/);
  // On this branch the snapshot is knowingly still WAL-headed, and its -wal
  // holds committed transactions the main file does not. Telling the operator
  // to move "the retained path" without naming the sidecars invites them to
  // destroy by hand exactly what the guard refused to unlink.
  assert.match(result.stdout, /sidecars: \['-wal', '-shm'\]/);
  assert.match(result.stdout, /retained snapshot sidecar, move or delete it together with the snapshot: .*snapshot\.sqlite-wal/);
  assert.match(result.stdout, /retained snapshot sidecar, move or delete it together with the snapshot: .*snapshot\.sqlite-shm/);
});

test("SQLite snapshot preflight still names the retained paths when verification is interrupted", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-release-tools-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  // Copying a multi-gigabyte snapshot is a realistic place for an operator to
  // lose patience. A bare KeyboardInterrupt traceback would leave them with a
  // complete snapshot on disk that they never hear about, blocking the rerun.
  const result = await runDriver(directory, `${ROUTER_FIXTURE}
def copy_private(source, destination):
    raise KeyboardInterrupt()

preflight.copy_private = copy_private
try:
    preflight.main()
    print("NO FAILURE RAISED")
except SystemExit as error:
    print(error)
except KeyboardInterrupt:
    print("UNANNOTATED KeyboardInterrupt")
print("snapshot exists:", os.path.exists(snapshot))
`);
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /snapshot preflight failed after backup/);
  assert.match(result.stdout, /retained snapshot for inspection: .*snapshot\.sqlite/);
  assert.match(result.stdout, /snapshot exists: True/);
});

test("SQLite snapshot preflight refuses to snapshot beside a leftover -wal", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-release-tools-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const databasePath = join(directory, "router.sqlite");
  const database = new DatabaseSync(databasePath);
  database.exec("PRAGMA journal_mode=WAL; CREATE TABLE schema_migrations (id TEXT PRIMARY KEY); INSERT INTO schema_migrations VALUES ('012_sentinel_native_cleanup'); CREATE TABLE messages (body TEXT); INSERT INTO messages VALUES ('todays-router-data');");
  const snapshot = join(directory, "snapshot.sqlite");
  const restored = join(directory, "restore-check.sqlite");
  const first = runTool(["--database", databasePath, "--snapshot", snapshot, "--restore-check", restored]);
  assert.equal(first.status, 0, first.stderr);

  // Reproduce what an operator does with a retained snapshot: open it to look
  // inside. That puts the WAL-headed file back into WAL and leaves valid frames
  // in a -wal beside it. They then archive the file the message named, and rerun.
  const inspected = new DatabaseSync(snapshot);
  inspected.exec("PRAGMA journal_mode=WAL; UPDATE messages SET body='stale-inspection-edit';");
  assert.equal(existsSync(`${snapshot}-wal`), true, "inspecting a snapshot must leave the -wal this test is about");
  await rm(snapshot);

  const second = runTool(["--database", databasePath, "--snapshot", snapshot, "--restore-check", restored]);
  inspected.close();
  database.close();
  // Without the guard SQLite recovers those stale frames into the new backup,
  // checkpoints them in, deletes the -wal, and every check still passes: the
  // run exits 0 reporting integrity ok while the snapshot holds the old rows
  // instead of today's. Refusing is the only safe answer.
  assert.notEqual(second.status, 0, second.stdout);
  assert.ok(second.stderr.includes(`refusing to overwrite existing path: ${snapshot}-wal`), second.stderr);
  assert.equal(existsSync(snapshot), false, "no snapshot may be produced from a replayed stale -wal");
});

test("SQLite snapshot preflight refuses an existing restore-check path before touching the database", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-release-tools-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const databasePath = join(directory, "router.sqlite");
  const database = new DatabaseSync(databasePath);
  database.exec("PRAGMA journal_mode=WAL; CREATE TABLE schema_migrations (id TEXT PRIMARY KEY); INSERT INTO schema_migrations VALUES ('012_sentinel_native_cleanup');");
  const snapshot = join(directory, "snapshot.sqlite");
  const restored = join(directory, "restore-check.sqlite");
  // A leftover restore copy is now an expected state after a failed run, so the
  // operator must get the plain refusal up front rather than a late error after
  // a whole snapshot has been written.
  await writeFile(restored, "left over from an earlier failed run");
  const result = runTool(["--database", databasePath, "--snapshot", snapshot, "--restore-check", restored]);
  database.close();
  assert.notEqual(result.status, 0);
  assert.ok(result.stderr.includes(`refusing to overwrite existing path: ${restored}`), result.stderr);
  assert.equal(existsSync(snapshot), false, "nothing should be written before the paths are checked");
  assert.equal(await readFile(restored, "utf8"), "left over from an earlier failed run");
});

test("SQLite snapshot preflight keeps the live database read-only when its path contains URI syntax", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-release-tools-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  // The two halves of the URI hazard need separate names: in "a?b%2Fc" the '?'
  // ends the path first, so the %2F lands in the query section and is never
  // decoded. Whichever character comes first is the only one under test.
  const snapshot = join(directory, "snapshot.sqlite");
  const restored = join(directory, "restore-check.sqlite");

  // '?' first: raw interpolation ends the path early, so SQLite opens
  // "<directory>/router", creates it, and drops the mode=ro that is the only
  // thing keeping the live router database from being opened read-write.
  const queryPath = join(directory, "router?live.sqlite");
  const queryDatabase = new DatabaseSync(queryPath);
  queryDatabase.exec("PRAGMA journal_mode=WAL; CREATE TABLE schema_migrations (id TEXT PRIMARY KEY); INSERT INTO schema_migrations VALUES ('012_sentinel_native_cleanup');");
  const queryResult = runTool(["--database", queryPath, "--snapshot", snapshot, "--restore-check", restored]);
  queryDatabase.close();
  assert.equal(queryResult.status, 0, queryResult.stderr);
  assert.match(queryResult.stdout, /snapshot_integrity=ok migrations=1 restore_integrity=ok/);
  assert.equal(existsSync(join(directory, "router")), false, "mode=ro must not be pushed into the query section");

  // '%2F' first, in a subdirectory so the decoded form names a real-looking
  // path: raw interpolation has SQLite decode it to a separator and open
  // "<directory>/sub/live/path.sqlite" instead of the file that was asked for.
  const nested = join(directory, "sub");
  const escapePath = join(nested, "live%2Fpath.sqlite");
  await mkdir(nested);
  const escapeDatabase = new DatabaseSync(escapePath);
  escapeDatabase.exec("PRAGMA journal_mode=WAL; CREATE TABLE schema_migrations (id TEXT PRIMARY KEY); INSERT INTO schema_migrations VALUES ('012_sentinel_native_cleanup');");
  const escapeSnapshot = join(directory, "escape-snapshot.sqlite");
  const escapeRestored = join(directory, "escape-restore-check.sqlite");
  const escapeResult = runTool(["--database", escapePath, "--snapshot", escapeSnapshot, "--restore-check", escapeRestored]);
  escapeDatabase.close();
  assert.equal(escapeResult.status, 0, escapeResult.stderr);
  assert.match(escapeResult.stdout, /snapshot_integrity=ok migrations=1 restore_integrity=ok/);
  assert.equal(existsSync(join(nested, "live")), false, "%XX must not be decoded into a path separator");
});

test("SQLite snapshot preflight reports the corruption when the snapshot fails its integrity check", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-release-tools-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const databasePath = join(directory, "router.sqlite");
  const database = new DatabaseSync(databasePath);
  // No schema_migrations table: on a corrupt file the migrations read fails as
  // well, and the tool must not let that failure mask the corruption detail.
  database.exec("PRAGMA journal_mode=WAL; CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT);");
  const insert = database.prepare("INSERT INTO t (id, v) VALUES (?, ?)");
  for (let index = 1; index <= 300; index += 1) {
    insert.run(index, `value-${String(index).padStart(5, "0")}`);
  }
  database.exec("CREATE INDEX t_v ON t(v);");
  const pageSize = database.prepare("PRAGMA page_size").get().page_size;
  database.close();
  // Zero part of a table leaf page. This desynchronises the table from its
  // index, which integrity_check reports as many rows rather than one.
  const handle = await open(databasePath, "r+");
  await handle.write(Buffer.alloc(64), 0, 64, pageSize * 2 + pageSize / 2);
  await handle.close();
  const corrupt = new DatabaseSync(databasePath);
  const rows = corrupt.prepare("PRAGMA integrity_check").all();
  const nodeSqlite = corrupt.prepare("SELECT sqlite_version() AS version").get().version;
  const pythonSqlite = spawnSync(python, ["-c", "import sqlite3; print(sqlite3.sqlite_version)"], { encoding: "utf8" }).stdout.trim();
  // A page layout change would stop this fixture corrupting anything. Fail here
  // rather than letting the assertions below pass against a healthy file, and
  // name both engines because the fixture is built by one and read by the other.
  assert.ok(rows.length > 1, `fixture is not corrupt (node sqlite ${nodeSqlite}, python sqlite ${pythonSqlite}): ${JSON.stringify(rows)}`);
  const snapshot = join(directory, "snapshot.sqlite");
  const restored = join(directory, "restore-check.sqlite");
  const result = runTool(["--database", databasePath, "--snapshot", snapshot, "--restore-check", restored]);
  corrupt.close();
  assert.notEqual(result.status, 0);
  // The message has to name which of the two files failed: the same words from
  // the snapshot and from the restore copy mean opposite things.
  assert.ok(result.stderr.includes(`could not verify ${snapshot}`), result.stderr);
  assert.ok(result.stderr.includes("integrity check failed"), result.stderr);
  assert.ok(result.stderr.includes("missing from index t_v"), result.stderr);
  // Every integrity_check row reaches the operator, not just the first one.
  assert.ok(result.stderr.split("missing from index t_v").length > 2, result.stderr);
  // The migrations read would fail with "no such table" on this file. Seeing
  // that instead of the corruption is the failure mode this pins.
  assert.ok(!result.stderr.includes("no such table"), result.stderr);
  assert.equal(existsSync(snapshot), true, "failed verification must not delete the operator backup");
});

test("SQLite snapshot preflight retains the snapshot when verification fails", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-release-tools-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const databasePath = join(directory, "router.sqlite");
  const database = new DatabaseSync(databasePath);
  // No schema_migrations table, so verification cannot succeed.
  database.exec("PRAGMA journal_mode=WAL; CREATE TABLE unrelated (id TEXT PRIMARY KEY); INSERT INTO unrelated VALUES ('x');");
  const snapshot = join(directory, "snapshot.sqlite");
  const restored = join(directory, "restore-check.sqlite");
  // The tool refuses to overwrite an existing target, so prove both output
  // paths are absent first; otherwise a leftover file could make the retention
  // assertion below pass for the wrong reason.
  assert.equal(existsSync(snapshot), false);
  assert.equal(existsSync(restored), false);
  const result = runTool(["--database", databasePath, "--snapshot", snapshot, "--restore-check", restored]);
  database.close();
  assert.notEqual(result.status, 0);
  // The run must reach verification, not die earlier for an unrelated reason —
  // otherwise the retention assertion below proves nothing.
  assert.ok(result.stderr.includes(`could not verify ${snapshot}: no such table: schema_migrations`), result.stderr);
  assert.ok(result.stderr.includes(`retained snapshot for inspection: ${snapshot}`), result.stderr);
  assert.ok(result.stderr.includes("move or delete the retained path(s) before rerunning"), result.stderr);
  assert.equal(existsSync(snapshot), true, "failed verification must not delete the operator backup");
  const retained = new DatabaseSync(snapshot, { readOnly: true });
  assert.equal(retained.prepare("SELECT id FROM unrelated").get().id, "x");
  retained.close();
  assert.equal(permissions(snapshot), "600");
  // This failure happens before the restore copy is made, so the message must
  // not send the operator looking for a file that was never created.
  assert.equal(existsSync(restored), false);
  assert.ok(!result.stderr.includes("retained restore copy"), result.stderr);
});

test("SQLite snapshot preflight refuses to overwrite the snapshot a failed run retained", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-release-tools-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const databasePath = join(directory, "router.sqlite");
  const database = new DatabaseSync(databasePath);
  database.exec("PRAGMA journal_mode=WAL; CREATE TABLE unrelated (id TEXT PRIMARY KEY); INSERT INTO unrelated VALUES ('x');");
  const snapshot = join(directory, "snapshot.sqlite");
  const restored = join(directory, "restore-check.sqlite");
  const first = runTool(["--database", databasePath, "--snapshot", snapshot, "--restore-check", restored]);
  assert.notEqual(first.status, 0);
  assert.equal(existsSync(snapshot), true);
  const retainedBytes = await readFile(snapshot);
  // Retaining the snapshot is only safe if a rerun cannot then clobber it. It
  // is the operator's sole backup at this point.
  const second = runTool(["--database", databasePath, "--snapshot", snapshot, "--restore-check", restored]);
  database.close();
  assert.notEqual(second.status, 0);
  assert.ok(second.stderr.includes(`refusing to overwrite existing path: ${snapshot}`), second.stderr);
  assert.deepEqual(await readFile(snapshot), retainedBytes, "the rerun must leave the retained backup byte-for-byte intact");
});

test("SQLite snapshot preflight leaves nothing at the snapshot path when the backup is interrupted", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-release-tools-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const result = await runDriver(directory, `${ROUTER_FIXTURE}
# Enough pages that the backup can be caught genuinely part way through.
bulk = sqlite3.connect(database)
bulk.execute("CREATE TABLE payload (id INTEGER PRIMARY KEY, v BLOB)")
bulk.executemany("INSERT INTO payload (v) VALUES (?)", [(os.urandom(2000),) for _ in range(200)])
bulk.commit()
bulk.close()

def partials():
    return sorted(name for name in os.listdir(workspace) if ".partial-" in name)

def staging_file():
    # The staging file now lives inside a private staging directory, together
    # with whatever -journal SQLite puts beside it.
    directory = os.path.join(workspace, partials()[0])
    inner = [name for name in os.listdir(directory) if not name.endswith("-journal")]
    return os.path.join(directory, inner[0])

real_connect = preflight.sqlite3.connect

class InterruptingSource:
    """Interrupts once pages have actually been copied, not before."""

    def __init__(self, inner):
        self.inner = inner

    def backup(self, target, **kwargs):
        def progress(status, remaining, total):
            if remaining == total:
                return
            print("staging directories mid-backup:", len(partials()))
            print("pages copied mid-backup:", total - remaining > 0)
            print("staging directory mode:", oct(os.stat(os.path.join(workspace, partials()[0])).st_mode & 0o777))
            print("mode mid-backup:", oct(os.stat(staging_file()).st_mode & 0o777))
            raise KeyboardInterrupt()

        self.inner.backup(target, pages=1, progress=progress)

    def close(self):
        self.inner.close()

def connect(*args, **kwargs):
    inner = real_connect(*args, **kwargs)
    return InterruptingSource(inner) if kwargs.get("uri") else inner

preflight.sqlite3.connect = connect
try:
    preflight.main()
    print("NO INTERRUPT RAISED")
except KeyboardInterrupt:
    print("interrupted")
print("snapshot exists:", os.path.exists(snapshot))
print("partials left:", partials())
`);
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /interrupted/);
  // The partial copy is written to a staging name, so an interrupt — or a
  // signal that runs no handler at all — can never leave a part-written file at
  // the operator's snapshot path, where it would read as a finished backup.
  assert.match(result.stdout, /staging directories mid-backup: 1/);
  // The staging path is handed to sqlite3.connect() and os.link(), both of
  // which follow symlinks. A directory no other user can traverse is what stops
  // someone swapping that name for a symlink mid-run.
  assert.match(result.stdout, /staging directory mode: 0o700/);
  // Proves the interrupt landed after real pages were copied, not before the
  // backup started, which would make the assertions below prove much less.
  assert.match(result.stdout, /pages copied mid-backup: True/);
  assert.match(result.stdout, /snapshot exists: False/);
  // The snapshot is a full copy of the router database, so it must be private
  // from the moment it is created rather than from a chmod after the write.
  assert.match(result.stdout, /mode mid-backup: 0o600/);
  assert.match(result.stdout, /partials left: \[\]/);
});

test("SQLite snapshot preflight prunes staging debris older than the retention limit", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-release-tools-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  // A run killed by a signal leaves a near-complete copy of the router database
  // in the snapshot directory. Nothing else would ever remove it, so the tool
  // has to bound it itself rather than leave it as a manual chore.
  const result = await runDriver(directory, `${ROUTER_FIXTURE}
abandoned = os.path.join(workspace, "snapshot.sqlite.partial-abandoned")
in_flight = os.path.join(workspace, "snapshot.sqlite.partial-inflight")
for path in (abandoned, in_flight):
    os.mkdir(path, 0o700)
    open(os.path.join(path, "snapshot.sqlite"), "wb").close()
expired = time.time() - preflight.STAGING_RETENTION_SECONDS - 60
os.utime(abandoned, (expired, expired))

preflight.main()
print("retention limit hours:", preflight.STAGING_RETENTION_SECONDS / 3600)
print("expired staging pruned:", not os.path.exists(abandoned))
print("recent staging kept:", os.path.exists(in_flight))
`);
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /snapshot_integrity=ok/);
  assert.match(result.stdout, /retention limit hours: 24\.0/);
  assert.match(result.stdout, /expired staging pruned: True/);
  // A staging directory younger than the limit may belong to a run still in
  // progress, so pruning must never touch it.
  assert.match(result.stdout, /recent staging kept: True/);
});

test("SQLite snapshot preflight fails when the restore copy carries a different migration count", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-release-tools-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  // The migration-count comparison is what the printed restore_integrity=ok
  // actually claims, so it needs a case where the two counts genuinely differ.
  const result = await runDriver(directory, `${ROUTER_FIXTURE}
real_copy_private = preflight.copy_private

def copy_private(source, destination):
    real_copy_private(source, destination)
    divergent = sqlite3.connect(destination)
    divergent.execute("INSERT INTO schema_migrations VALUES ('999_only_in_the_restore_copy')")
    divergent.commit()
    divergent.close()

preflight.copy_private = copy_private
try:
    preflight.main()
    print("NO FAILURE RAISED")
except SystemExit as error:
    print(error)
print("snapshot exists:", os.path.exists(snapshot))
`);
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /restored SQLite migration count 2 differs from snapshot 1/);
  assert.match(result.stdout, /retained snapshot for inspection: .*snapshot\.sqlite/);
  assert.match(result.stdout, /retained restore copy for inspection: .*restore-check\.sqlite/);
  assert.match(result.stdout, /snapshot exists: True/);
});

test("SQLite snapshot preflight names the restore copy too when it exists at failure time", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-release-tools-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const result = await runDriver(directory, `${ROUTER_FIXTURE}
# Fail the restore copy's own verification, keyed on the path rather than on
# call order, so adding a third verification cannot silently retarget this.
real_verify = preflight.verify_and_count_migrations

def verify_and_count_migrations(path):
    if str(path) == restore_check:
        raise SystemExit("restore copy is unreadable")
    return real_verify(path)

preflight.verify_and_count_migrations = verify_and_count_migrations
try:
    preflight.main()
    print("NO FAILURE RAISED")
except SystemExit as error:
    print(error)
print("snapshot exists:", os.path.exists(snapshot))
print("restore copy exists:", os.path.exists(restore_check))
print("restore copy mode:", oct(os.stat(restore_check).st_mode & 0o777))
`);
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /snapshot preflight failed after backup: restore copy is unreadable/);
  assert.match(result.stdout, /retained snapshot for inspection: .*snapshot\.sqlite/);
  assert.match(result.stdout, /retained restore copy for inspection: .*restore-check\.sqlite/);
  assert.match(result.stdout, /move or delete the retained path\(s\) before rerunning/);
  assert.match(result.stdout, /snapshot exists: True/);
  assert.match(result.stdout, /restore copy exists: True/);
  assert.match(result.stdout, /restore copy mode: 0o600/);
});

test("SQLite snapshot preflight opens the live database, the snapshot and the restore copy read-only", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-release-tools-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  // Read-only access to the live router database is the tool's single most
  // important safety property, and "no stray file was created" only proves the
  // URI was escaped. This proves the handle itself refuses writes.
  const result = await runDriver(directory, `${ROUTER_FIXTURE}
real_connect = preflight.sqlite3.connect
real_read_only_uri = preflight.read_only_uri
opened_read_only = []

def read_only_uri(path):
    opened_read_only.append(os.path.basename(str(path)))
    return real_read_only_uri(path)

def connect(*args, **kwargs):
    inner = real_connect(*args, **kwargs)
    if kwargs.get("uri"):
        try:
            inner.execute("CREATE TABLE proof_this_handle_can_write (x)")
            print("live handle:", "WRITABLE")
        except sqlite3.OperationalError as error:
            print("live handle:", error)
    return inner

preflight.read_only_uri = read_only_uri
preflight.sqlite3.connect = connect
preflight.main()
print("opened read-only:", opened_read_only)
`);
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /live handle: attempt to write a readonly database/);
  assert.doesNotMatch(result.stdout, /live handle: WRITABLE/);
  // Both verification opens must go through the same read-only URI builder, or
  // verification could quietly mutate the artifact it is certifying.
  assert.match(result.stdout, /opened read-only: \['router\.sqlite', 'snapshot\.sqlite', 'restore-check\.sqlite'\]/);
});

test("SQLite snapshot preflight will not write the restore copy through a symlink planted mid-run", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-release-tools-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  // The restore-check path is checked early and written later. Anything that
  // reopens it by name in between — shutil.copyfile, or a reservation whose fd
  // is dropped — writes a full copy of the router database wherever the symlink
  // points.
  const result = await runDriver(directory, `${ROUTER_FIXTURE}
victim = os.path.join(workspace, "victim.txt")
with open(victim, "w") as handle:
    handle.write("not a restore copy")

real_verify = preflight.verify_and_count_migrations

def verify_and_count_migrations(path):
    count = real_verify(path)
    if str(path) == snapshot and not os.path.lexists(restore_check):
        os.symlink(victim, restore_check)
    return count

preflight.verify_and_count_migrations = verify_and_count_migrations
try:
    preflight.main()
    print("NO FAILURE RAISED")
except SystemExit as error:
    print(str(error).splitlines()[0])
with open(victim) as handle:
    print("victim contents:", handle.read())
`);
  assert.equal(result.status, 0, result.stderr);
  assert.equal(result.stdout.includes("NO FAILURE RAISED"), false, result.stdout);
  assert.match(result.stdout, /victim contents: not a restore copy/);
});

test("SQLite snapshot preflight refuses to publish over a snapshot that appeared after the path check", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-release-tools-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  // The exists() check happens before the backup; a second run can take the
  // path during it. Only the atomic link refuses that race, and it must refuse
  // rather than clobber — the file already there is someone's only backup.
  const result = await runDriver(directory, `${ROUTER_FIXTURE}
real_stage_snapshot = preflight.stage_snapshot

def stage_snapshot(destination):
    staged = real_stage_snapshot(destination)
    with open(destination, "wb") as competitor:
        competitor.write(b"another run's complete snapshot")
    return staged

preflight.stage_snapshot = stage_snapshot
try:
    preflight.main()
    print("NO FAILURE RAISED")
except SystemExit as error:
    print(error)
with open(snapshot, "rb") as handle:
    print("winner intact:", handle.read() == b"another run's complete snapshot")
`);
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /refusing to overwrite existing path: .*snapshot\.sqlite/);
  assert.match(result.stdout, /winner intact: True/);
});

test("SQLite snapshot preflight explains a backup that never started", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-release-tools-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const result = await runDriver(directory, `${ROUTER_FIXTURE}
real_connect = preflight.sqlite3.connect

def connect(*args, **kwargs):
    if kwargs.get("uri"):
        raise sqlite3.OperationalError("unable to open database file")
    return real_connect(*args, **kwargs)

preflight.sqlite3.connect = connect
try:
    preflight.main()
    print("NO FAILURE RAISED")
except SystemExit as error:
    print(error)
print("snapshot exists:", os.path.exists(snapshot))
print("staging left:", [n for n in os.listdir(workspace) if ".partial-" in n])
`);
  assert.equal(result.status, 0, result.stderr);
  // This is the message an operator gets when the source cannot be opened at
  // all, which is the known WAL-without-sidecars limitation among other causes.
  assert.match(result.stdout, /snapshot backup failed: unable to open database file/);
  assert.match(result.stdout, /no snapshot was written, so rerunning is safe/);
  assert.match(result.stdout, /snapshot exists: False/);
  assert.match(result.stdout, /staging left: \[\]/);
});

test("SQLite snapshot preflight builds read-only URIs for paths SQLite would misread", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-release-tools-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  // read_only_uri is a pure function, and two of its branches are unreachable
  // from any fixture a test can put on disk here.
  const result = await runDriver(directory, `
print("plain:", preflight.read_only_uri("/srv/mecha/router.sqlite"))
# A leading '//' is a URI authority, not a path: SQLite would reject "file://srv".
print("double slash:", preflight.read_only_uri("//srv/router.sqlite"))
# A filename that is not valid UTF-8 reaches python as surrogate escapes, which
# quote() refuses to encode from a str.
print("non utf8:", preflight.read_only_uri(os.fsdecode(b"/srv/router-\\xff.sqlite")))
`);
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /plain: file:\/srv\/mecha\/router\.sqlite\?mode=ro/);
  assert.match(result.stdout, /double slash: file:\/\/\/srv\/router\.sqlite\?mode=ro/);
  assert.match(result.stdout, /non utf8: file:\/srv\/router-%FF\.sqlite\?mode=ro/);
});

test("SQLite snapshot preflight refuses a symlinked database and a symlinked snapshot path", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-release-tools-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const databasePath = join(directory, "router.sqlite");
  const database = new DatabaseSync(databasePath);
  database.exec("PRAGMA journal_mode=WAL; CREATE TABLE schema_migrations (id TEXT PRIMARY KEY); INSERT INTO schema_migrations VALUES ('012_sentinel_native_cleanup');");
  const snapshot = join(directory, "snapshot.sqlite");
  const restored = join(directory, "restore-check.sqlite");

  // A symlinked --database is how you get the tool to copy a file the operator
  // did not name.
  const linkedDatabase = join(directory, "linked-router.sqlite");
  await symlink(databasePath, linkedDatabase);
  const viaLink = runTool(["--database", linkedDatabase, "--snapshot", snapshot, "--restore-check", restored]);
  assert.notEqual(viaLink.status, 0);
  assert.ok(viaLink.stderr.includes("--database must name a regular SQLite file"), viaLink.stderr);
  assert.equal(existsSync(snapshot), false);

  // A missing --database must be refused by the same guard.
  const missing = runTool(["--database", join(directory, "absent.sqlite"), "--snapshot", snapshot, "--restore-check", restored]);
  assert.notEqual(missing.status, 0);
  assert.ok(missing.stderr.includes("--database must name a regular SQLite file"), missing.stderr);

  // A dangling symlink at the snapshot path exists() cannot see, and writing
  // through it would put the router database at a path nobody asked for.
  const dangling = join(directory, "dangling-snapshot.sqlite");
  const target = join(directory, "symlink-target.sqlite");
  await symlink(target, dangling);
  const viaDangling = runTool(["--database", databasePath, "--snapshot", dangling, "--restore-check", restored]);
  assert.notEqual(viaDangling.status, 0);
  assert.ok(viaDangling.stderr.includes(`refusing to overwrite existing path: ${dangling}`), viaDangling.stderr);
  assert.equal(existsSync(target), false, "the tool must not write through a dangling symlink");

  // Same for the restore-check path, and it has to be refused up front: exists()
  // is blind to a dangling symlink, so without the symlink check the run gets
  // all the way past a full database copy before failing on a raw errno.
  const danglingRestore = join(directory, "dangling-restore.sqlite");
  await symlink(join(directory, "restore-target.sqlite"), danglingRestore);
  const viaDanglingRestore = runTool(["--database", databasePath, "--snapshot", snapshot, "--restore-check", danglingRestore]);
  assert.notEqual(viaDanglingRestore.status, 0);
  assert.ok(viaDanglingRestore.stderr.includes(`refusing to overwrite existing path: ${danglingRestore}`), viaDanglingRestore.stderr);
  assert.equal(existsSync(snapshot), false, "nothing should be copied before the paths are refused");
  database.close();
});
