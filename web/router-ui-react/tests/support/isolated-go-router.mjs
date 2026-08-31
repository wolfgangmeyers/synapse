import { createHash } from "node:crypto";
import { execFile } from "node:child_process";
import { chmod, copyFile, mkdir, mkdtemp, readFile, readdir, rm, stat } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, dirname, relative, resolve, sep } from "node:path";
import { fileURLToPath } from "node:url";
import { spawn } from "node:child_process";
import { createServer } from "node:net";
import { promisify } from "node:util";

const execFileAsync = promisify(execFile);
export const REPOSITORY_ROOT = resolve(dirname(fileURLToPath(import.meta.url)), "../../../../");
export const EXPECTED_GOAL_MIGRATIONS = Object.freeze([
  "019_goal_control_desired_state",
  "021_goal_control_revision_history",
]);
export const EXPECTED_CAPABILITIES = Object.freeze(["goal_control_v1", "subagent_lifecycle_v2"]);
export const FIXTURE_TIME = "2030-01-01T00:00:00.000Z";
export const PROMPT_EXPORT_TEST_ID = "prompt-export";
export const CLIPBOARD_PASTE_TEST_ID = "clipboard-paste-target";

export const LIVE_ROUTER_FINGERPRINT_DESIGN = Object.freeze({
  timing: "capture immediately before and after the task",
  inputs: Object.freeze(["router database files", "router JSONL event log", "attachment directory contents", "connected-agent snapshot"]),
  mode: "read-only; no startup, mutation, or socket connection",
  attribution: "compare task process IDs, working directories, arguments, and listeners before assigning a change",
  missingRouter: "record unavailable as an environment condition",
});

export async function createTaskRoot(prefix = "router-test") {
  const root = await mkdtemp(join(tmpdir(), `mecha-${prefix}-`));
  await chmod(root, 0o700);
  return root;
}

export async function allocateLoopbackPort() {
  const server = createServer();
  await new Promise((resolveListen, rejectListen) => {
    server.once("error", rejectListen);
    server.listen(0, "127.0.0.1", resolveListen);
  });
  const address = server.address();
  if (address === null || typeof address === "string") {
    await closeServer(server);
    throw new Error("loopback port allocation returned no TCP address");
  }
  const port = address.port;
  await closeServer(server);
  if (port === 8765) return allocateLoopbackPort();
  return port;
}

export async function isLoopbackPortAvailable(port) {
  const server = createServer();
  try {
    await new Promise((resolveListen, rejectListen) => {
      server.once("error", rejectListen);
      server.listen(port, "127.0.0.1", resolveListen);
    });
    return true;
  } catch {
    return false;
  } finally {
    if (server.listening) await closeServer(server);
  }
}

async function closeServer(server) {
  await new Promise((resolveClose, rejectClose) => server.close((error) => error ? rejectClose(error) : resolveClose()));
}

function pathInside(root, candidate) {
  const rootPath = resolve(root);
  const candidatePath = resolve(candidate);
  const relativePath = relative(rootPath, candidatePath);
  return relativePath !== "" && relativePath !== ".." && !relativePath.startsWith(`..${sep}`);
}

export function assertTaskPath(root, candidate, label) {
  if (!pathInside(root, candidate)) throw new Error(`${label} must be inside the task temporary root`);
  return resolve(candidate);
}

export async function assertPrivatePath(path, label) {
  const info = await stat(path);
  if ((info.mode & 0o077) !== 0) throw new Error(`${label} has permissions accessible outside the task owner`);
}

function parseReadyLine(line) {
  const match = /^MECHA_V2_ROUTER_READY http=(\S+) ws=(\S+) database=(\S+) log=(\S+)$/.exec(line);
  if (match === null) return undefined;
  return { httpUrl: match[1], websocketUrl: match[2], databasePath: match[3], logPath: match[4] };
}

async function stopFailedChild(child) {
  if (child.exitCode !== null || child.signalCode !== null) return;
  const closed = new Promise((resolveClose) => child.once("close", resolveClose));
  child.kill("SIGTERM");
  const stopped = await Promise.race([closed, new Promise((resolveTimeout) => setTimeout(resolveTimeout, 2_000))]);
  if (stopped === undefined) {
    child.kill("SIGKILL");
    await closed;
  }
}

export async function spawnRouterProcess({ command, args, cwd = REPOSITORY_ROOT, root, env = process.env, expectedPort, timeoutMs = 30_000 }) {
  const child = spawn(command, args, { cwd, env, stdio: ["ignore", "pipe", "pipe"], windowsHide: true });
  const output = [];
  let lineBuffer = "";
  let settled = false;
  let rejectReady;
  let resolveReady;
  const ready = new Promise((resolvePromise, rejectPromise) => {
    resolveReady = resolvePromise;
    rejectReady = rejectPromise;
  });
  const consume = (chunk) => {
    const text = String(chunk);
    output.push(text);
    lineBuffer += text;
    const lines = lineBuffer.split(/\r?\n/u);
    lineBuffer = lines.pop() ?? "";
    for (const line of lines) {
      const parsed = parseReadyLine(line);
      if (parsed !== undefined && !settled) {
        settled = true;
        resolveReady(parsed);
      }
    }
  };
  child.stdout.on("data", consume);
  child.stderr.on("data", consume);
  const startupError = new Promise((_, reject) => child.once("error", reject));
  const timer = setTimeout(() => rejectReady(new Error(`timed out waiting for router readiness\n${output.join("")}`)), timeoutMs);
  let readyInfo;
  try {
    readyInfo = await Promise.race([ready, startupError]);
  } catch (error) {
    clearTimeout(timer);
    await stopFailedChild(child);
    throw error;
  }
  clearTimeout(timer);
  if (readyInfo === undefined) throw new Error("router readiness did not include URLs or paths");
  const rootPath = resolve(root);
  const httpUrl = new URL(readyInfo.httpUrl);
  const websocketUrl = new URL(readyInfo.websocketUrl);
  if (httpUrl.protocol !== "http:" || httpUrl.hostname !== "127.0.0.1") throw new Error("isolated router must publish an HTTP loopback URL");
  if (websocketUrl.protocol !== "ws:" || websocketUrl.hostname !== "127.0.0.1" || websocketUrl.pathname !== "/v1/agents") throw new Error("isolated router must publish a loopback agent WebSocket URL");
  const databasePath = assertTaskPath(rootPath, readyInfo.databasePath, "database");
  const logPath = assertTaskPath(rootPath, readyInfo.logPath, "log");
  if (Number(httpUrl.port) !== Number(websocketUrl.port)) throw new Error("router readiness URLs use different ports");
  if (expectedPort !== undefined && Number(httpUrl.port) !== expectedPort) throw new Error("router readiness used a port other than the allocated task port");
  return {
    child,
    pid: child.pid,
    command,
    args: [...args],
    cwd: resolve(cwd),
    output,
    httpUrl: httpUrl.toString().replace(/\/$/u, ""),
    websocketUrl: websocketUrl.toString(),
    databasePath,
    logPath,
    attachmentsPath: `${databasePath}.attachments`,
    port: Number(httpUrl.port),
  };
}

export async function stopRouterProcess(processInfo, { timeoutMs = 15_000 } = {}) {
  const child = processInfo.child;
  if (child.exitCode === null && child.signalCode === null) child.kill("SIGTERM");
  const closed = new Promise((resolveClose) => child.once("close", (code, signal) => resolveClose({ code, signal })));
  const result = child.exitCode !== null || child.signalCode !== null
    ? { code: child.exitCode, signal: child.signalCode }
    : await Promise.race([
      closed,
      new Promise((resolveTimeout) => setTimeout(() => resolveTimeout(undefined), timeoutMs)),
    ]);
  if (result === undefined) {
    child.kill("SIGKILL");
    await closed;
    throw new Error("isolated router did not stop after SIGTERM");
  }
  return result;
}

async function jsonRequest(url, init) {
  const response = await fetch(url, init);
  const body = await response.json();
  return { response, body };
}

export async function verifyRouterReadiness(httpUrl) {
  const health = await jsonRequest(new URL("/v1/health", httpUrl));
  if (!health.response.ok || health.body?.status !== "ok") throw new Error("isolated router health check failed");
  const migrations = health.body?.schema_migrations;
  if (!Array.isArray(migrations) || EXPECTED_GOAL_MIGRATIONS.some((migration) => !migrations.includes(migration))) {
    throw new Error(`isolated router is missing required goal migrations: ${EXPECTED_GOAL_MIGRATIONS.join(", ")}`);
  }
  const capabilities = await jsonRequest(new URL("/v1/capabilities", httpUrl));
  if (!capabilities.response.ok || EXPECTED_CAPABILITIES.some((capability) => capabilities.body?.[capability] !== true)) {
    throw new Error("isolated router does not advertise the required capabilities");
  }
  return { health: health.body, capabilities: capabilities.body };
}

/** @returns {Promise<{rootPath: string, httpUrl: string, websocketUrl: string, databasePath: string, logPath: string, attachmentsPath: string, port: number, pid: number, cwd: string, health: Record<string, unknown>, capabilities: Record<string, unknown>, close: () => Promise<{rootRemoved?: boolean, logText?: string}>}>} */
export async function startIsolatedGoRouter({ repositoryRoot = REPOSITORY_ROOT, timeoutMs = 30_000 } = {}) {
  const root = await createTaskRoot("go-router");
  const databasePath = join(root, "go-router.db");
  const logPath = join(root, "go-router.jsonl");
  const binaryPath = join(root, "mailbox-router");
  const port = await allocateLoopbackPort();
  let processInfo;
  try {
    const configuredBinary = process.env.MECHA_GO_ROUTER_BINARY;
    if (configuredBinary === undefined) {
      await execFileAsync("go", ["build", "-o", binaryPath, "./cmd/mailbox-router"], { cwd: join(repositoryRoot, "router-proxy"), timeout: timeoutMs });
    } else {
      const source = resolve(configuredBinary);
      if (!(await stat(source)).isFile()) throw new Error("MECHA_GO_ROUTER_BINARY is not a file");
      await mkdir(dirname(binaryPath), { recursive: true, mode: 0o700 });
      await copyFile(source, binaryPath);
    }
    processInfo = await spawnRouterProcess({
      command: binaryPath,
      args: ["--database", databasePath, "--log", logPath, "--host", "127.0.0.1", "--port", String(port)],
      cwd: repositoryRoot,
      root,
      expectedPort: port,
      timeoutMs,
    });
    if (processInfo.databasePath !== databasePath || processInfo.logPath !== logPath) throw new Error("Go router readiness reported paths different from its explicit task paths");
    const verified = await verifyRouterReadiness(processInfo.httpUrl);
    if (!(await stat(processInfo.databasePath)).isFile()) throw new Error("isolated router database was not created");
    if (!(await stat(processInfo.logPath)).isFile()) throw new Error("isolated router log was not created");
    if (!(await stat(processInfo.attachmentsPath)).isDirectory()) throw new Error("isolated router attachments directory was not created");
    await assertPrivatePath(root, "task temporary root");
    await assertPrivatePath(processInfo.databasePath, "isolated router database");
    await assertPrivatePath(processInfo.logPath, "isolated router log");
    await assertPrivatePath(processInfo.attachmentsPath, "isolated router attachments");
    let closed = false;
    return {
      ...processInfo,
      rootPath: root,
      health: verified.health,
      capabilities: verified.capabilities,
      async close() {
        if (closed) return { alreadyClosed: true };
        closed = true;
        const stop = await stopRouterProcess(processInfo);
        let logText = "";
        let shutdownError;
        try {
          logText = await readFile(processInfo.logPath, "utf8");
          if (!logText.includes('"event":"router_stopped"')) shutdownError = new Error("isolated Go router log lacks router_stopped");
        } catch (error) {
          shutdownError = error;
        } finally {
          await rm(root, { recursive: true, force: true });
        }
        if (shutdownError !== undefined) throw shutdownError;
        return { stop, logText, rootRemoved: true };
      },
    };
  } catch (error) {
    if (processInfo !== undefined) {
      try { await stopRouterProcess(processInfo); } catch { /* cleanup below owns only this task root */ }
    }
    await rm(root, { recursive: true, force: true });
    throw error;
  }
}

async function hashFile(path) {
  const bytes = await readFile(path);
  return createHash("sha256").update(bytes).digest("hex");
}

async function listTree(path, root = path) {
  const result = [];
  let entries;
  try { entries = await readdir(path, { withFileTypes: true }); } catch { return result; }
  for (const entry of entries.sort((left, right) => left.name.localeCompare(right.name))) {
    const fullPath = join(path, entry.name);
    const relativePath = relative(root, fullPath);
    if (entry.isDirectory()) result.push(...await listTree(fullPath, root));
    else result.push({ path: relativePath, sha256: await hashFile(fullPath) });
  }
  return result;
}

/** @returns {Promise<{rootPath: string, attachmentsDirectory: {exists: boolean, mode?: number}, files: Array<{path: string, sha256?: string, missing?: boolean}>}>} */
export async function fingerprintTaskResources(router) {
  const files = [];
  let attachmentsDirectory = { exists: false };
  try {
    const info = await stat(router.attachmentsPath);
    attachmentsDirectory = { exists: info.isDirectory(), mode: info.mode };
  } catch { /* the task root may already be cleaned up */ }
  const databaseName = router.databasePath.split(sep).pop();
  let rootEntries = [];
  try { rootEntries = await readdir(router.rootPath, { withFileTypes: true }); } catch { /* the task root may already be cleaned up */ }
  for (const entry of rootEntries) {
    if (entry.name !== databaseName && !entry.name.startsWith(`${databaseName}-`)) continue;
    const path = join(router.rootPath, entry.name);
    if (!entry.isFile()) continue;
    files.push({ path: relative(router.rootPath, path), sha256: await hashFile(path) });
  }
  for (const path of [router.logPath]) {
    try { files.push({ path: relative(router.rootPath, path), sha256: await hashFile(path) }); } catch { files.push({ path: relative(router.rootPath, path), missing: true }); }
  }
  files.push(...await listTree(router.attachmentsPath, router.rootPath));
  return { rootPath: router.rootPath, attachmentsDirectory, files };
}

export function compareLiveFingerprints(before, after) {
  const beforeValue = JSON.stringify(before);
  const afterValue = JSON.stringify(after);
  return { unchanged: beforeValue === afterValue, changed: beforeValue !== afterValue };
}

export function fixturePeerAgent(overrides = {}) {
  return {
    agent_id: overrides.agent_id ?? "fixture-peer",
    instance_id: overrides.instance_id ?? "fixture-peer-instance",
    lease_id: overrides.lease_id ?? "fixture-peer-lease",
    role: overrides.role ?? "user_interaction",
  };
}

export function fixtureMessage(id = "fixture-message", to = "fixture-peer", overrides = {}) {
  return {
    id,
    from: overrides.from ?? "fixture-sender",
    to,
    body: overrides.body ?? `fixture body ${id}`,
    created_at: overrides.created_at ?? FIXTURE_TIME,
    ...(overrides.source === undefined ? {} : { source: overrides.source }),
    ...(overrides.attachments === undefined ? {} : { attachments: overrides.attachments }),
  };
}

export function fixtureGoalSettings() {
  return {
    goal_lock_enabled: false,
    snooze_enabled: true,
    snooze_duration_ms: 1_000,
    snooze_turn_limit: 2,
    snooze_budget_cap_ms: 120_000,
    snooze_refill_yield_ms: 2_000,
    snooze_refill_elapsed_ms: 3_000,
  };
}

export function fixtureModelCatalog() {
  return { current_model: { provider: "fixture-provider", id: "fixture-model" }, model_summaries: [{ provider: "fixture-provider", id: "fixture-model", label: "Fixture model" }], total_count: 1, truncated: false };
}

export function fixtureActivity() {
  return { activity_state: "working", subagent_count: 1, subagents: [{ id: "fixture-subagent", description: "Fixture worker", status: "running" }], generated_at: FIXTURE_TIME };
}

export function fixtureLifecycle() {
  return { router_version: 1, agents: [{ id: "fixture-subagent", type: "worker", status: "running", description: "Fixture worker" }] };
}

export function fixtureReminder() {
  return { id: "fixture-reminder", agent_id: "fixture-peer", body: "Fixture reminder", due_at: "2030-01-01T00:05:00.000Z", recurrence: null, status: "scheduled" };
}

export function fixtureDeliveryItem(id = "fixture-message", attempt_number = 1) {
  return { message_id: id, message_incarnation_id: `00000000-0000-4000-8000-${String(attempt_number).padStart(12, "0")}`, attempt_number, delivery_expires_at: "2030-01-01T00:01:00.000Z" };
}

export function fixtureArrival() {
  return { agent_id: "fixture-peer", from: "fixture-sender", subject: "Fixture arrival", preview: "Fixture preview", body: "Fixture arrival body", created_at: FIXTURE_TIME, message_id: "fixture-arrival" };
}

export function fixtureImage() {
  return { type: "image", name: "fixture.png", data_url: "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=" };
}

export function fixturePromptTemplate(overrides = {}) {
  return { id: "fixture-prompt", name: "Fixture prompt", body: "Review ${file}", ...overrides };
}

export function fixtureClipboardImage() {
  return { name: "clipboard.png", mimeType: "image/png", dataUrl: "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=" };
}

export function createFixtureData(overrides = {}) {
  return {
    peer: fixturePeerAgent(),
    catalog: fixtureModelCatalog(),
    activity: fixtureActivity(),
    lifecycle: fixtureLifecycle(),
    goal: { enabled: true, goal: "Fixture goal", ...fixtureGoalSettings(), outcome_counts: { COMPLETED: 1, NOT_DONE: 0 } },
    reminder: fixtureReminder(),
    conversation: [fixtureMessage("fixture-message", "fixture-peer")],
    delivery: fixtureDeliveryItem(),
    arrival: fixtureArrival(),
    image: fixtureImage(),
    promptTemplates: [fixturePromptTemplate()],
    clipboardImage: fixtureClipboardImage(),
    ...overrides,
  };
}
