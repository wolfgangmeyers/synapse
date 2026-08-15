import assert from "node:assert/strict";
import { execFileSync, spawn } from "node:child_process";
import { randomUUID } from "node:crypto";
import { once } from "node:events";
import { copyFile, mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import { existsSync, readFileSync } from "node:fs";
import { createServer } from "node:http";
import { DatabaseSync } from "node:sqlite";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { pathToFileURL } from "node:url";
import test from "node:test";

import WebSocket from "ws";

import { openMessageRepository } from "../harness/db.js";
import { createAgentRouter } from "../harness/router.js";

const PRE_017_MIGRATIONS = [
  "001_initial.sql", "002_delivery_attempt_deadline.sql", "003_reminders.sql", "004_server_jobs.sql",
  "005_message_incarnation.sql", "006_sentinel.sql", "007_sentinel_occurrences.sql", "008_sentinel_deadline.sql",
  "009_sentinel_tombstones.sql", "010_sentinel_exit_outcome.sql", "011_sentinel_relay_capabilities.sql",
  "012_sentinel_native_cleanup.sql", "013_conversation_history.sql", "014_reminder_batch_links.sql",
  "015_conversation_source.sql", "016_delivery_batches.sql",
].map((file) => ({ id: file.slice(0, -4), sql: readFileSync(new URL(`../harness/migrations/${file}`, import.meta.url), "utf8") }));

const PRE_018_MIGRATIONS = [
  ...PRE_017_MIGRATIONS,
  {
    id: "017_subagent_lifecycle",
    sql: readFileSync(new URL("../harness/migrations/017_subagent_lifecycle.sql", import.meta.url), "utf8"),
  },
];

async function temporaryDatabase(t) {
  const directory = await mkdtemp(join(tmpdir(), "mecha-lifecycle-rollout-"));
  const databasePath = join(directory, "router.sqlite");
  t.after(() => rm(directory, { recursive: true, force: true }));
  return { directory, databasePath };
}

async function nextFrame(socket, type) {
  const [payload] = await once(socket, "message");
  const frame = JSON.parse(payload.toString("utf8"));
  if (frame.type === type) return frame;
  return nextFrame(socket, type);
}

async function connectLegacyClient(t, routerUrl) {
  const socket = new WebSocket(routerUrl);
  t.after(() => socket.close());
  await once(socket, "open");
  const registered = nextFrame(socket, "registered");
  socket.send(JSON.stringify({
    version: "v1", frame_id: "legacy-register", type: "register", agent_id: "legacy-parent", instance_id: "legacy-instance",
  }));
  return { socket, registration: await registered };
}

async function availablePort() {
  const server = createServer();
  server.listen(0, "127.0.0.1");
  await once(server, "listening");
  const { port } = server.address();
  await new Promise((resolve, reject) => server.close((error) => error === undefined ? resolve() : reject(error)));
  return port;
}

async function stopProcess(child) {
  if (child.exitCode !== null || child.signalCode !== null) return;
  const exited = once(child, "exit");
  child.kill("SIGTERM");
  await exited;
}

async function startRouterProcess(t, command, args, { cwd, env }) {
  const child = spawn(command, args, { cwd, env, stdio: ["ignore", "pipe", "pipe"] });
  let output = "";
  const ready = new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error(`router process did not become ready: ${output}`)), 5_000);
    const observe = (chunk) => {
      output += chunk.toString("utf8");
      const match = output.match(/(?:MECHA_V2_ROUTER_READY|LEGACY_ROUTER_READY) http=(\S+)/);
      if (match !== null) {
        clearTimeout(timer);
        resolve(match[1]);
      }
    };
    child.stdout.on("data", observe);
    child.stderr.on("data", observe);
    child.once("error", (error) => { clearTimeout(timer); reject(error); });
    child.once("exit", (code, signal) => {
      if (!/MECHA_V2_ROUTER_READY|LEGACY_ROUTER_READY/.test(output)) {
        clearTimeout(timer);
        reject(new Error(`router process exited before ready (${code ?? signal}): ${output}`));
      }
    });
  });
  t.after(() => stopProcess(child));
  return { child, httpUrl: await ready };
}

async function loadOldRouter(t) {
  // The durable lifecycle schema shipped in cf67888. Load its parent router
  // source against the migrated database to keep the old-client check stable
  // after later commits advance HEAD.
  const temporaryRouter = join(process.cwd(), "harness", `.old-router-${randomUUID()}.mjs`);
  await writeFile(temporaryRouter, execFileSync("git", ["show", "cf67888^:harness/router.js"], { encoding: "utf8" }));
  t.after(() => rm(temporaryRouter, { force: true }));
  return import(`${pathToFileURL(temporaryRouter).href}?test=${randomUUID()}`);
}

test("preserves legacy activity for an old lifecycle client", async (t) => {
  const { databasePath } = await temporaryDatabase(t);
  const repository = openMessageRepository({ databasePath });
  const router = createAgentRouter({ repository, timeoutSweepIntervalMs: 60_000 });
  t.after(async () => { await router.close(); repository.close(); });
  const routerUrl = await router.listen();
  const { socket, registration } = await connectLegacyClient(t, routerUrl);
  const pong = nextFrame(socket, "pong");
  socket.send(JSON.stringify({
    version: "v1", frame_id: "legacy-activity", type: "activity", agent_id: "legacy-parent", instance_id: "legacy-instance",
    lease_id: registration.lease_id, activity_state: "active", subagent_count: 1,
    subagents: [{ id: "legacy-child", description: "Legacy label" }],
  }));
  socket.send(JSON.stringify({ version: "v1", frame_id: "legacy-activity-barrier", type: "ping" }));
  await pong;
  const response = await fetch(routerUrl.replace("ws://", "http://"));
  const body = await response.json();
  assert.equal(body.agents[0].subagent_count, 1);
  assert.equal(body.agents[0].subagents[0].id, "legacy-child");
});

test("keeps old-router legacy behavior on a database at migration 017", async (t) => {
  const { databasePath } = await temporaryDatabase(t);
  const repository = openMessageRepository({ databasePath });
  assert.ok(repository.migrationIds().includes("017_subagent_lifecycle"));
  const { createAgentRouter: createOldRouter } = await loadOldRouter(t);
  const router = createOldRouter({ repository, timeoutSweepIntervalMs: 60_000 });
  t.after(async () => { await router.close(); repository.close(); });
  const routerUrl = await router.listen();
  const capabilityResponse = await fetch(new URL("/v1/capabilities", routerUrl.replace("ws://", "http://")));
  assert.equal(capabilityResponse.status, 404);
  const { socket, registration } = await connectLegacyClient(t, routerUrl);
  const pong = nextFrame(socket, "pong");
  socket.send(JSON.stringify({
    version: "v1", frame_id: "legacy-017", type: "activity", agent_id: "legacy-parent", instance_id: "legacy-instance",
    lease_id: registration.lease_id, activity_state: "settled", subagent_count: 0,
  }));
  socket.send(JSON.stringify({ version: "v1", frame_id: "legacy-017-barrier", type: "ping" }));
  await pong;
  const agents = await (await fetch(routerUrl.replace("ws://", "http://"))).json();
  assert.equal(agents.agents[0].activity_state, "idle");
});

test("fails v2 closed when migration 017 is missing", async (t) => {
  const { databasePath } = await temporaryDatabase(t);
  const repository = openMessageRepository({ databasePath, migrations: PRE_017_MIGRATIONS });
  assert.equal(repository.migrationIds().includes("017_subagent_lifecycle"), false);
  const router = createAgentRouter({ repository, timeoutSweepIntervalMs: 60_000 });
  t.after(async () => { await router.close(); repository.close(); });
  const routerUrl = await router.listen();
  const capabilities = await (await fetch(new URL("/v1/capabilities", routerUrl.replace("ws://", "http://")))).json();
  assert.deepEqual(capabilities, { subagent_lifecycle_v2: false, lifecycle_error: "migration_017_required" });
  const { socket, registration } = await connectLegacyClient(t, routerUrl);
  const error = nextFrame(socket, "error");
  socket.send(JSON.stringify({
    version: "v1", frame_id: "missing-017-enable", type: "subagent_sync_enable",
    agent_id: "legacy-parent", instance_id: "legacy-instance", lease_id: registration.lease_id,
  }));
  assert.equal((await error).code, "migration_017_required");
});

test("forward migration backfills lifecycle status without changing existing labels", async (t) => {
  const { databasePath } = await temporaryDatabase(t);
  const oldRepository = openMessageRepository({ databasePath, migrations: PRE_018_MIGRATIONS });
  oldRepository.close();

  const oldDatabase = new DatabaseSync(databasePath);
  const timestamp = "2030-01-01T00:00:00.000Z";
  const description = "Soak sleeper 1 2m";
  oldDatabase.prepare(`
    INSERT INTO subagent_lifecycle_state (
      parent_agent_id, subagent_id, instance_id, parent_session_id, producer_epoch,
      type, description, state, started_at, updated_at, terminal_at, terminal_reason, router_version
    ) VALUES (?, ?, ?, ?, ?, ?, ?, 'running', ?, ?, NULL, NULL, 1)
  `).run("existing-parent", "soak-sleeper-1", "existing-instance", "existing-session", "11111111-1111-4111-8111-111111111111", "general-purpose", description, timestamp, timestamp);
  oldDatabase.prepare(`
    INSERT INTO subagent_lifecycle_events (
      parent_agent_id, instance_id, parent_session_id, producer_epoch, subagent_id,
      event_id, client_sequence, event_kind, state, type, description, terminal_reason, payload_hash, received_at
    ) VALUES (?, ?, ?, ?, ?, ?, 1000, 'snapshot', 'running', ?, ?, NULL, ?, ?)
  `).run("existing-parent", "existing-instance", "existing-session", "11111111-1111-4111-8111-111111111111", "soak-sleeper-1", "existing-event", "general-purpose", description, "existing-hash", timestamp);
  oldDatabase.close();

  const repository = openMessageRepository({ databasePath });
  assert.ok(repository.migrationIds().includes("018_subagent_lifecycle_status"));
  const replay = repository.replaySubagentLifecycle({ afterVersion: 0 });
  const snapshot = repository.subagentLifecycleSnapshot({ parentAgentId: "existing-parent" });
  assert.deepEqual(replay.updates, [{
    router_version: 1,
    parent_agent_id: "existing-parent",
    subagent_id: "soak-sleeper-1",
    state: "running",
    type: "general-purpose",
    description,
    status: "running",
  }]);
  assert.deepEqual(snapshot.agents[0].subagents, [{
    id: "soak-sleeper-1", type: "general-purpose", description, status: "running",
  }]);
  repository.close();

  const migrated = new DatabaseSync(databasePath);
  assert.equal(migrated.prepare(`
    SELECT status FROM subagent_lifecycle_state WHERE parent_agent_id = 'existing-parent'
  `).get().status, "running");
  assert.equal(migrated.prepare(`
    SELECT status FROM subagent_lifecycle_events WHERE parent_agent_id = 'existing-parent'
  `).get().status, "running");
  assert.throws(() => migrated.prepare(`
    UPDATE subagent_lifecycle_state SET status = 'invalid' WHERE parent_agent_id = 'existing-parent'
  `).run(), /CHECK constraint failed/);
  migrated.close();
});

async function writePre017RouterEntrypoint(directory, port, databasePath) {
  const entrypoint = join(directory, "pre017-router.mjs");
  const repositoryRoot = process.cwd();
  const migrationFiles = PRE_017_MIGRATIONS.map(({ id }) => `${id}.sql`);
  await writeFile(entrypoint, `
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { openMessageRepository } from ${JSON.stringify(pathToFileURL(join(repositoryRoot, "harness", "db.js")).href)};
import { createAgentRouter } from ${JSON.stringify(pathToFileURL(join(repositoryRoot, "harness", "router.js")).href)};
const root = ${JSON.stringify(repositoryRoot)};
const migrations = ${JSON.stringify(migrationFiles)}.map((file) => ({
  id: file.slice(0, -4),
  sql: readFileSync(join(root, "harness", "migrations", file), "utf8"),
}));
const repository = openMessageRepository({ databasePath: ${JSON.stringify(databasePath)}, migrations });
const router = createAgentRouter({ repository, host: "127.0.0.1", port: ${port}, timeoutSweepIntervalMs: 60_000 });
const websocketUrl = await router.listen();
console.log(\`LEGACY_ROUTER_READY http=\${websocketUrl.replace(/^ws:/, "http:").replace(/\\/v1\\/agents$/, "")}\`);
let closing = false;
async function close() {
  if (closing) return;
  closing = true;
  await router.close();
  repository.close();
}
process.once("SIGTERM", () => { void close(); });
process.once("SIGINT", () => { void close(); });
`);
  return entrypoint;
}

test("uses the captured router command and verified backup for atomic lifecycle rollout", async (t) => {
  const { databasePath, directory } = await temporaryDatabase(t);
  const backupPath = join(directory, "router-before-017.sqlite");
  const pre017 = openMessageRepository({ databasePath, migrations: PRE_017_MIGRATIONS });
  pre017.enqueue({ id: "pre-rollout-message", from: "sender", to: "receiver", body: "kept on rollback", created_at: "2030-01-01T00:00:00.000Z" });
  pre017.close();

  // VACUUM INTO is SQLite's consistent-file backup path, unlike copying a WAL
  // database while it is live. The fixture verifies the backup before switchover.
  const source = new DatabaseSync(databasePath);
  source.exec(`VACUUM INTO '${backupPath.replaceAll("'", "''")}'`);
  source.close();
  const backup = new DatabaseSync(backupPath, { readOnly: true });
  assert.equal(backup.prepare("SELECT COUNT(*) AS count FROM messages").get().count, 1);
  assert.equal(backup.prepare("SELECT COUNT(*) AS count FROM schema_migrations WHERE id = '017_subagent_lifecycle'").get().count, 0);
  backup.close();

  const capturedStart = { command: process.execPath, args: ["tools/run_router.mjs"], cwd: process.cwd() };
  const updated = openMessageRepository({ databasePath });
  assert.ok(updated.migrationIds().includes("017_subagent_lifecycle"));
  updated.close();

  // A failed replacement restores the verified backup, then uses the original
  // recorded command. Clear only the test database's stale WAL sidecars before
  // replacing its main file; otherwise SQLite can replay the failed migration.
  await Promise.all([rm(`${databasePath}-wal`, { force: true }), rm(`${databasePath}-shm`, { force: true })]);
  await copyFile(backupPath, databasePath);
  const rolledBack = openMessageRepository({ databasePath, migrations: PRE_017_MIGRATIONS });
  assert.equal(rolledBack.migrationIds().includes("017_subagent_lifecycle"), false);
  assert.equal(rolledBack.queuedFor("receiver").length, 1);
  rolledBack.close();
  const restarted = openMessageRepository({ databasePath });
  const migrations = restarted.migrationIds();
  assert.equal(migrations.filter((id) => id === "017_subagent_lifecycle").length, 1);
  assert.deepEqual(capturedStart, { command: process.execPath, args: ["tools/run_router.mjs"], cwd: process.cwd() });
  restarted.close();
});

test("validates router and checkout-source client rollout in an isolated home", async (t) => {
  const { databasePath, directory } = await temporaryDatabase(t);
  const home = join(directory, "home");
  const backupPath = join(directory, "router-before-017.sqlite");
  await mkdir(home, { recursive: true });
  const pre017 = openMessageRepository({ databasePath, migrations: PRE_017_MIGRATIONS });
  pre017.enqueue({ id: "rollback-message", from: "sender", to: "receiver", body: "retained", created_at: "2030-01-01T00:00:00.000Z" });
  pre017.close();

  const source = new DatabaseSync(databasePath);
  source.exec(`VACUUM INTO '${backupPath.replaceAll("'", "''")}'`);
  source.close();
  const backup = new DatabaseSync(backupPath, { readOnly: true });
  assert.equal(backup.prepare("SELECT COUNT(*) AS count FROM messages").get().count, 1);
  assert.equal(backup.prepare("SELECT COUNT(*) AS count FROM schema_migrations WHERE id = '017_subagent_lifecycle'").get().count, 0);
  backup.close();

  const legacyPort = await availablePort();
  const legacyEntrypoint = await writePre017RouterEntrypoint(directory, legacyPort, databasePath);
  const legacy = await startRouterProcess(t, process.execPath, [legacyEntrypoint], {
    cwd: process.cwd(), env: { ...process.env, HOME: home },
  });
  assert.deepEqual(await (await fetch(new URL("/v1/capabilities", legacy.httpUrl))).json(), {
    subagent_lifecycle_v2: false,
    lifecycle_error: "migration_017_required",
  });
  await stopProcess(legacy.child);

  const routerPort = await availablePort();
  const capturedStart = {
    command: process.execPath,
    args: ["tools/run_router.mjs", "--database", databasePath, "--port", String(routerPort)],
    cwd: process.cwd(),
  };
  const updated = await startRouterProcess(t, capturedStart.command, capturedStart.args, {
    cwd: capturedStart.cwd,
    env: { ...process.env, HOME: home, MECHA_KB_ROOT: join(home, ".mecha-developer", "kb") },
  });
  const updatedCapabilities = await (await fetch(new URL("/v1/capabilities", updated.httpUrl))).json();
  assert.equal(updatedCapabilities.subagent_lifecycle_v2, true);

  const registeredExtension = join(process.cwd(), "pi-extensions", "mecha-messaging.js");
  const registeredClient = join(process.cwd(), "pi-extensions", "mailbox-client.js");
  await mkdir(join(home, ".pi", "agent"), { recursive: true });
  await writeFile(join(home, ".pi", "agent", "settings.json"), JSON.stringify({ packages: [registeredExtension] }));
  const settings = JSON.parse(readFileSync(join(home, ".pi", "agent", "settings.json"), "utf8"));
  assert.deepEqual(settings.packages, [registeredExtension]);
  assert.ok(readFileSync(registeredExtension).length > 0);
  assert.ok(readFileSync(registeredClient).length > 0);
  assert.equal(existsSync(join(home, ".mecha-developer", "extensions")), false);
  assert.deepEqual(capturedStart, {
    command: process.execPath,
    args: ["tools/run_router.mjs", "--database", databasePath, "--port", String(routerPort)],
    cwd: process.cwd(),
  });
  await stopProcess(updated.child);

  await Promise.all([rm(`${databasePath}-wal`, { force: true }), rm(`${databasePath}-shm`, { force: true })]);
  await copyFile(backupPath, databasePath);
  const rolledBack = openMessageRepository({ databasePath, migrations: PRE_017_MIGRATIONS });
  assert.equal(rolledBack.migrationIds().includes("017_subagent_lifecycle"), false);
  assert.equal(rolledBack.queuedFor("receiver").length, 1);
  rolledBack.close();

  const restoredPort = await availablePort();
  const restoredEntrypoint = await writePre017RouterEntrypoint(directory, restoredPort, databasePath);
  const restored = await startRouterProcess(t, process.execPath, [restoredEntrypoint], {
    cwd: process.cwd(), env: { ...process.env, HOME: home },
  });
  assert.deepEqual(await (await fetch(new URL("/v1/capabilities", restored.httpUrl))).json(), {
    subagent_lifecycle_v2: false,
    lifecycle_error: "migration_017_required",
  });
  await stopProcess(restored.child);
});
