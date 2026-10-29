import { Buffer } from "node:buffer";
import { createHash, randomBytes, randomUUID } from "node:crypto";
import { execFile } from "node:child_process";
import { createServer } from "node:http";
import { readFileSync } from "node:fs";
import { open, readFile, readdir, readlink, realpath, rm, stat, writeFile, mkdir } from "node:fs/promises";
import { dirname, isAbsolute, join, relative, resolve } from "node:path";
import { performance } from "node:perf_hooks";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";
import {
  allocateLoopbackPort,
  createTaskRoot,
  isLoopbackPortAvailable,
  spawnRouterProcess,
  stopRouterProcess,
} from "../support/isolated-go-router.mjs";
import { startIsolatedBcbBrowser } from "./bcb-runner.mjs";
import {
  bcbCommand,
  buildRouterUI,
  reactInputScript,
  sqliteRows,
  startManagedRouter,
} from "./bcb-managed-rpc-release-runner.mjs";
import {
  DEADLINE_MS,
  assertArtifactRedacted,
  assertCleanupComplete,
  assertSharedResourcesUnchanged,
  runDeadlineBoundOperation,
  runManagedRecoverySequence,
  serializeCleanup,
  signalRouterAtBoundary,
  validateBarrier,
  validateCompletion,
  validateFreshness,
} from "./managed-recovery-harness.mjs";

const execFileAsync = promisify(execFile);
const HERE = dirname(fileURLToPath(import.meta.url));
const PROJECT = resolve(HERE, "../..");
const REPOSITORY = resolve(PROJECT, "../..");
const PROVIDER = "openai-codex";
const MODEL = "managed-rpc-release";
const TOKEN_PREFIX = "MECHA_MANAGED_RECOVERY_WORKLOAD:";
const COMPLETE_PREFIX = "MECHA_MANAGED_RECOVERY_COMPLETE:";
const STARTUP_TIMEOUT_MS = 90_000;
const BCB_POLL_TIMEOUT_MS = 1_000;
const BCB_SUBPROCESS_WATCHDOG_GRACE_MS = 1_000;

function failure(message) { return new Error(`Managed recovery acceptance: ${message}`); }
function delay(ms) { return new Promise((resolve) => setTimeout(resolve, ms)); }
function remaining(deadline) { return Math.max(1, Math.floor(deadline - performance.now())); }
async function exists(path) { return stat(path).then(() => true).catch(() => false); }

export async function persistRouterRecoveryEvidence(routerURL, kind, body, deadline) {
  const endpoint = `${routerURL}/v1/acceptance/managed-recovery?kind=${encodeURIComponent(kind)}`;
  const response = await fetch(endpoint, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify(body),
    signal: globalThis.AbortSignal.timeout(remaining(deadline)),
  });
  const persisted = await response.json().catch(() => ({}));
  if (!response.ok) throw failure(`router recovery ${kind} persistence returned ${response.status}: ${persisted.error ?? "invalid response"}`);
  const reread = await fetchJSON(`${endpoint}&trial_id=${encodeURIComponent(body.trial_id)}`, deadline);
  if (JSON.stringify(reread) !== JSON.stringify(persisted)) throw failure(`router recovery ${kind} re-read did not match persisted evidence`);
  return reread;
}

async function requireRecoveryFile(path, label, executable = false) {
  if (typeof path !== "string" || !isAbsolute(path)) throw failure(`${label} must be an absolute path`);
  let info;
  try { info = await stat(path); } catch { throw failure(`${label} is unavailable: ${path}`); }
  if (!info.isFile()) throw failure(`${label} is not a file: ${path}`);
  if (executable && (info.mode & 0o111) === 0) throw failure(`${label} is not executable: ${path}`);
  return realpath(path);
}

export function assertExplicitSubagentsRouterArgument(args, subagents) {
  const positions = args.flatMap((value, index) => value === "--managed-pi-subagents" ? [index] : []);
  if (positions.length !== 1 || args[positions[0] + 1] !== subagents) {
    throw failure("router launch must pass the locked pi-subagents realpath exactly once");
  }
  return { router_argument: "--managed-pi-subagents", resolved_path: subagents, command_spec_argument: "-e" };
}

export async function recoveryPreflight(environment = process.env) {
  const nodeBin = await requireRecoveryFile(process.execPath, "Node runtime", true);
  const piBin = await requireRecoveryFile(environment.MECHA_PI_BIN, "MECHA_PI_BIN", true);
  const configuredSubagents = environment.MECHA_PI_SUBAGENTS_EXTENSION;
  const subagents = await requireRecoveryFile(configuredSubagents, "MECHA_PI_SUBAGENTS_EXTENSION");
  const subagentsRepository = resolve(dirname(subagents), "../../..");
  const subagentsGitSHA = await execFileAsync("git", ["-C", subagentsRepository, "rev-parse", "HEAD"])
    .then(({ stdout }) => stdout.trim() || null)
    .catch(() => null);
  const subagentsVersion = await execFileAsync("git", ["-C", subagentsRepository, "describe", "--always", "--dirty"])
    .then(({ stdout }) => stdout.trim() || null)
    .catch(() => null);
  const bcbSource = environment.MECHA_BCB_ROOT ?? resolve(process.env.HOME || "", "code/browser-console-bridge");
  const python = await requireRecoveryFile(environment.MECHA_BCB_PYTHON ?? join(bcbSource, ".venv", "bin", "python3"), "BCB Python", true);
  await requireRecoveryFile(join(bcbSource, "server", "bridge_server.py"), "BCB server");
  await requireRecoveryFile(join(bcbSource, "extension", "background.js"), "BCB extension");
  await requireRecoveryFile(join(REPOSITORY, "pi-extensions", "mecha-rpc", "index.ts"), "mecha-rpc extension");
  await requireRecoveryFile(join(REPOSITORY, "pi-extensions", "mecha-rpc", "tests", "fixtures", "managed-rpc-release-probe.mjs"), "managed recovery probe");
  return { nodeBin, piBin, subagents, subagentsGitSHA, subagentsVersion, python };
}

export async function execBrowser(bcb, tab, code, timeoutMs = 45_000, command = bcbCommand) {
  const effectiveTimeoutMs = Math.max(1, timeoutMs);
  const args = ["cli.bcb_exec", "--timeout", String(Math.ceil(effectiveTimeoutMs / 1000))];
  if (tab !== undefined) args.push("--tab", String(tab));
  args.push(code);
  return command(bcb, args, effectiveTimeoutMs + BCB_SUBPROCESS_WATCHDOG_GRACE_MS);
}

async function waitForStartup(probe, label) {
  const deadline = performance.now() + STARTUP_TIMEOUT_MS;
  let lastError;
  while (performance.now() < deadline) {
    try {
      const value = await probe();
      if (value !== undefined && value !== false) return value;
    } catch (error) { lastError = error; }
    await delay(50);
  }
  throw failure(`${label} timed out${lastError instanceof Error ? `: ${lastError.message}` : ""}`);
}

async function waitUntilDeadline(deadline, probe, label) {
  let lastError;
  while (performance.now() < deadline) {
    try {
      const value = await probe();
      if (value !== undefined && value !== false) return value;
    } catch (error) { lastError = error; }
    await delay(Math.min(50, remaining(deadline)));
  }
  throw failure(`${label} exceeded the absolute deadline${lastError instanceof Error ? `: ${lastError.message}` : ""}`);
}

async function fetchJSON(url, deadline) {
  const response = await fetch(url, { signal: globalThis.AbortSignal.timeout(remaining(deadline)), cache: "no-store" });
  const body = await response.json().catch(() => ({}));
  if (!response.ok) throw failure(`${url} returned ${response.status}: ${body.error ?? "invalid response"}`);
  return body;
}

function sha256(value) {
  return createHash("sha256").update(value).digest("hex");
}

function portFromURL(value, label) {
  let parsed;
  try { parsed = new URL(value); } catch { throw failure(`${label} must be an absolute URL`); }
  const port = parsed.port === "" ? (parsed.protocol === "https:" ? 443 : 80) : Number(parsed.port);
  if (!Number.isSafeInteger(port) || port < 1 || port > 65_535) throw failure(`${label} has an invalid port`);
  return port;
}

function listenerPIDs(stdout, ports) {
  const byPort = new Map(ports.map((port) => [port, new Set()]));
  for (const line of String(stdout).split("\n")) {
    const local = line.match(/\s(?:\[[^\]]+\]|\S+):(\d+)\s/u);
    if (local === null) continue;
    const port = Number(local[1]);
    const pids = byPort.get(port);
    if (pids === undefined) continue;
    for (const match of line.matchAll(/pid=(\d+)/gu)) pids.add(Number(match[1]));
  }
  return byPort;
}

async function capturedProcessIdentity(pid, dependencies) {
  if (!Number.isSafeInteger(pid) || pid < 2) throw failure("shared listener returned an invalid process PID");
  const [statText, executable, command] = await Promise.all([
    dependencies.readFileImpl(`/proc/${pid}/stat`, "utf8"),
    dependencies.readlinkImpl(`/proc/${pid}/exe`),
    dependencies.readFileImpl(`/proc/${pid}/cmdline`),
  ]).catch((error) => { throw failure(`shared process ${pid} identity is unavailable: ${error instanceof Error ? error.message : String(error)}`); });
  const close = statText.lastIndexOf(")");
  const fields = close < 0 ? [] : statText.slice(close + 2).trim().split(/\s+/u);
  if (typeof fields[19] !== "string" || fields[19].length === 0) throw failure(`shared process ${pid} start marker is unavailable`);
  return {
    identity: { pid, start_marker: fields[19], executable },
    command_sha256: sha256(command),
  };
}

export async function captureSharedResourceSnapshot(options = {}, injected = {}) {
  const routerURL = options.routerURL ?? process.env.MECHA_SHARED_ROUTER_URL ?? "http://127.0.0.1:8765";
  const webUIURL = options.webUIURL ?? process.env.MECHA_SHARED_WEB_UI_URL ?? "http://127.0.0.1:8787";
  const bcbHTTPPort = options.bcbHTTPPort ?? Number(process.env.MECHA_SHARED_BCB_HTTP_PORT ?? 18_080);
  const bcbWSPort = options.bcbWSPort ?? Number(process.env.MECHA_SHARED_BCB_WS_PORT ?? 18_081);
  const configurationPaths = options.configurationPaths ?? [join(REPOSITORY, "tools", "run_router.sh"), join(REPOSITORY, "tools", "run_webui.sh")];
  if (!Array.isArray(configurationPaths) || configurationPaths.length === 0) throw failure("shared configuration identity paths are unavailable");
  const dependencies = {
    fetchImpl: injected.fetchImpl ?? fetch,
    readFileImpl: injected.readFileImpl ?? readFile,
    readlinkImpl: injected.readlinkImpl ?? readlink,
    runCommand: injected.runCommand ?? ((command, args) => execFileAsync(command, args)),
    listUserTabs: injected.listUserTabs ?? (() => {
      if (typeof options.bcbPython !== "string" || options.bcbPython.length === 0) throw failure("user browser tab identity capture is unavailable");
      return bcbCommand({ python: options.bcbPython, host: "127.0.0.1", httpPort: bcbHTTPPort, wsPort: bcbWSPort }, ["cli.bcb_tabs", "--json"], 10_000);
    }),
  };
  for (const [port, label] of [[bcbHTTPPort, "BCB HTTP"], [bcbWSPort, "BCB WebSocket"]]) {
    if (!Number.isSafeInteger(port) || port < 1 || port > 65_535) throw failure(`${label} port is invalid`);
  }

  const routerPort = portFromURL(routerURL, "shared router URL");
  const webUIPort = portFromURL(webUIURL, "shared web UI URL");
  const { stdout } = await dependencies.runCommand("ss", ["-H", "-ltnp"])
    .catch((error) => { throw failure(`shared listener identity capture failed: ${error instanceof Error ? error.message : String(error)}`); });
  const listeners = listenerPIDs(stdout, [routerPort, webUIPort, bcbHTTPPort, bcbWSPort]);
  const processPIDs = [...new Set([...listeners.values()].flatMap((pids) => [...pids]))].sort((left, right) => left - right);
  const capturedProcesses = await Promise.all(processPIDs.map((pid) => capturedProcessIdentity(pid, dependencies)));
  const processByPID = new Map(capturedProcesses.map((captured) => [captured.identity.pid, captured.identity]));
  const identitiesForPort = (port) => [...(listeners.get(port) ?? [])]
    .sort((left, right) => left - right)
    .map((pid) => processByPID.get(pid));

  const [routerResponse, webUIResponse, files, tabs] = await Promise.all([
    dependencies.fetchImpl(`${routerURL}/v1/health`, { signal: globalThis.AbortSignal.timeout(2_000), cache: "no-store" })
      .catch((error) => { throw failure(`shared router identity capture failed: ${error instanceof Error ? error.message : String(error)}`); }),
    dependencies.fetchImpl(`${webUIURL}/`, { signal: globalThis.AbortSignal.timeout(2_000), cache: "no-store" })
      .catch((error) => { throw failure(`shared web UI identity capture failed: ${error instanceof Error ? error.message : String(error)}`); }),
    Promise.all(configurationPaths.map(async (path) => ({ path, sha256: sha256(await dependencies.readFileImpl(path)) })))
      .catch((error) => { throw failure(`shared configuration identity capture failed: ${error instanceof Error ? error.message : String(error)}`); }),
    Promise.resolve().then(() => dependencies.listUserTabs()),
  ]);
  if (!routerResponse.ok) throw failure(`shared router identity capture returned ${routerResponse.status}`);
  if (!webUIResponse.ok) throw failure(`shared web UI identity capture returned ${webUIResponse.status}`);
  const routerHealth = await routerResponse.json().catch(() => { throw failure("shared router identity response is not JSON"); });
  const document = Buffer.from(await webUIResponse.arrayBuffer());
  if (!Array.isArray(tabs)) throw failure("user browser tab identity capture is unavailable");
  const userTabs = tabs.map((tab) => {
    if ((typeof tab?.id !== "string" && typeof tab?.id !== "number") || typeof tab?.url !== "string" || typeof tab?.title !== "string") {
      throw failure("user browser tab identity capture is malformed");
    }
    return { id: String(tab.id), title: tab.title, url: tab.url };
  }).sort((left, right) => left.id.localeCompare(right.id));
  const processCommands = capturedProcesses.map((captured) => ({ pid: captured.identity.pid, sha256: captured.command_sha256 }));

  return {
    configuration: { files: files.sort((left, right) => left.path.localeCompare(right.path)), process_commands: processCommands },
    processes: capturedProcesses.map((captured) => captured.identity),
    router: {
      endpoint: routerURL,
      health: {
        readiness: routerHealth?.readiness ?? null,
        runtime: routerHealth?.runtime ?? null,
        status: routerHealth?.status ?? null,
        version: routerHealth?.version ?? null,
      },
      listeners: identitiesForPort(routerPort),
    },
    user_tabs: userTabs,
    web_ui: { document_sha256: sha256(document), endpoint: webUIURL, listeners: identitiesForPort(webUIPort) },
  };
}

function providerCompletion(response, content) {
  response.writeHead(200, { "content-type": "text/event-stream", "cache-control": "no-cache" });
  response.write(`data: ${JSON.stringify({ id: "managed-recovery", model: MODEL, choices: [{ index: 0, delta: { role: "assistant", content }, finish_reason: null }] })}\n\n`);
  response.write(`data: ${JSON.stringify({ id: "managed-recovery", model: MODEL, choices: [{ index: 0, delta: {}, finish_reason: "stop" }] })}\n\n`);
  response.end("data: [DONE]\n\n");
}

function requestText(payload) {
  return JSON.stringify(Array.isArray(payload?.messages) ? payload.messages : []);
}

async function startRecoveryProvider(token) {
  const requests = [];
  const sockets = new Set();
  let sequence = 0;
  const server = createServer(async (request, response) => {
    const requestURL = new URL(request.url ?? "/", "http://127.0.0.1");
    if (request.method === "GET" && requestURL.pathname === "/managed-recovery/status") {
      const requestedToken = requestURL.searchParams.get("token");
      const latest = [...requests].reverse().find((candidate) => candidate.token_bound);
      if (requestedToken !== token || latest === undefined) { response.writeHead(404).end(); return; }
      response.writeHead(200, { "content-type": "application/json" });
      response.end(JSON.stringify({ token, provider_request_seq: latest.sequence, active: latest.active, completed: latest.completed }));
      return;
    }
    if (request.method !== "POST" || requestURL.pathname !== "/v1/chat/completions") { response.writeHead(404).end(); return; }
    const chunks = [];
    for await (const chunk of request) chunks.push(Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk));
    let payload;
    try { payload = JSON.parse(Buffer.concat(chunks).toString("utf8")); } catch { response.writeHead(400).end(); return; }
    const current = { sequence: ++sequence, token_bound: requestText(payload).includes(`${TOKEN_PREFIX}${token}`), active: true, completed: false, aborted: false };
    requests.push(current);
    const finish = () => { current.active = false; };
    const complete = () => { current.active = false; current.completed = true; };
    request.once("aborted", () => { current.aborted = true; finish(); });
    response.once("close", finish);
    if (!current.token_bound) { complete(); providerCompletion(response, "Managed recovery fixture ignored an unbound request."); return; }
    const tokenRequests = requests.filter((candidate) => candidate.token_bound);
    if (tokenRequests.length === 1) return;
    complete();
    providerCompletion(response, `${COMPLETE_PREFIX}${token}`);
  });
  server.on("connection", (socket) => { sockets.add(socket); socket.once("close", () => sockets.delete(socket)); });
  await new Promise((resolveListen, rejectListen) => { server.once("error", rejectListen); server.listen(0, "127.0.0.1", resolveListen); });
  const address = server.address();
  if (address === null || typeof address === "string") throw failure("provider did not bind a loopback port");
  return {
    baseUrl: `http://127.0.0.1:${address.port}/v1`,
    port: address.port,
    activeBarrier() { return requests.find((candidate) => candidate.token_bound && candidate.active); },
    completion() { return requests.find((candidate) => candidate.token_bound && candidate.completed); },
    evidence() { return requests.map((request) => ({ ...request })); },
    async close() {
      for (const socket of sockets) socket.destroy();
      if (!server.listening) return;
      await new Promise((resolveClose, rejectClose) => server.close((error) => error ? rejectClose(error) : resolveClose()));
    },
  };
}

function processIdentity(pid, row) {
  let process_group;
  let kernel_start_marker;
  try {
    const text = readFileSync(`/proc/${pid}/stat`, "utf8");
    const fields = text.slice(text.lastIndexOf(")") + 2).trim().split(/\s+/u);
    process_group = Number(fields[2]);
    kernel_start_marker = fields[19];
  } catch { /* absence is reported by callers */ }
  return {
    pid,
    process_start_marker: row.process_start_marker,
    process_incarnation: row.process_incarnation,
    process_group,
    kernel_start_marker,
  };
}

async function configuredSubagentsEvidence(database, agentID, prerequisites, timeoutMs = 45_000) {
  const rows = await sqliteRows(database, "SELECT pi_subagents_extension FROM managed_launch_recipes WHERE agent_id=?", [agentID], timeoutMs);
  if (rows.length !== 1 || rows[0].pi_subagents_extension !== prerequisites.subagents) {
    throw failure("managed CommandSpec did not retain the explicit locked pi-subagents path");
  }
  return {
    resolved_path: prerequisites.subagents,
    git_sha: prerequisites.subagentsGitSHA,
    router_argument: "--managed-pi-subagents",
    command_spec_argument: "-e",
    recipe_path: rows[0].pi_subagents_extension,
  };
}

async function databaseFence(database, agentID, timeoutMs = 45_000) {
  const rows = await sqliteRows(database, "SELECT pi_session_id,session_epoch,runtime_generation,process_incarnation,process_pid,process_start_marker,manifest_revision,manifest_hash,lifecycle_state,activity_state,accepting_prompts,updated_at FROM managed_agents WHERE agent_id=?", [agentID], timeoutMs);
  const versions = await sqliteRows(database, "SELECT projection_version FROM managed_settings WHERE singleton_id=1", [], timeoutMs);
  if (rows.length !== 1 || versions.length !== 1) throw failure("isolated DB fence is unavailable");
  const row = rows[0];
  if (!Number.isSafeInteger(row.process_pid) || row.process_pid < 2) throw failure("isolated DB fence has no managed process PID");
  return { ...row, projection_version: versions[0].projection_version, process_group: processIdentity(row.process_pid, row).process_group };
}

async function sessionContains(root, text) {
  const entries = await readdir(root, { withFileTypes: true }).catch(() => []);
  for (const entry of entries) {
    const path = join(root, entry.name);
    if (entry.isDirectory()) {
      if (await sessionContains(path, text)) return true;
    } else if (entry.isFile() && (await readFile(path, "utf8").catch(() => "")).includes(text)) return true;
  }
  return false;
}

const SESSION_EVIDENCE_FILE_LIMIT = 32;
const SESSION_HEADER_BYTE_LIMIT = 16 * 1024;

async function safeSessionFileEvidence(sessionRoot, path) {
  const info = await stat(path);
  const relative_filename = relative(sessionRoot, path);
  const result = {
    relative_filename,
    size_bytes: info.size,
    modified_ms: info.mtimeMs,
    header: null,
  };
  if (!info.isFile()) return result;
  const handle = await open(path, "r");
  try {
    const buffer = Buffer.alloc(SESSION_HEADER_BYTE_LIMIT);
    const { bytesRead } = await handle.read(buffer, 0, buffer.length, 0);
    const firstLine = buffer.subarray(0, bytesRead).toString("utf8").split("\n", 1)[0];
    try {
      const parsed = JSON.parse(firstLine);
      if (parsed !== null && typeof parsed === "object" && !Array.isArray(parsed)) {
        result.header = Object.fromEntries(["id", "type", "version"]
          .filter((key) => typeof parsed[key] === "string" || typeof parsed[key] === "number")
          .map((key) => [key, parsed[key]]));
      }
    } catch {
      result.header = { parse_error: firstLine.length === bytesRead && bytesRead === SESSION_HEADER_BYTE_LIMIT ? "header_exceeds_limit" : "invalid_json" };
    }
  } finally {
    await handle.close();
  }
  return result;
}

export async function captureSessionHeaderEvidence(sessionRoot, database, agentID, durableSessionID) {
  const rows = await sqliteRows(database, "SELECT state,pi_session_id,root_relative_path FROM managed_stock_pi_session_reservations WHERE agent_id=? ORDER BY created_at DESC LIMIT 1", [agentID], 2_000).catch(() => []);
  const reservation = rows[0];
  const boundRelativePath = typeof reservation?.root_relative_path === "string" ? reservation.root_relative_path : null;
  const boundPath = boundRelativePath === null ? null : resolve(sessionRoot, boundRelativePath);
  const rootPrefix = `${resolve(sessionRoot)}/`;
  const boundPathSafe = boundPath !== null && boundPath.startsWith(rootPrefix);
  const entries = await readdir(sessionRoot, { withFileTypes: true }).catch(() => []);
  const files = [];
  for (const entry of entries.slice(0, SESSION_EVIDENCE_FILE_LIMIT)) {
    if (!entry.isFile()) continue;
    files.push(await safeSessionFileEvidence(sessionRoot, join(sessionRoot, entry.name)).catch((error) => ({
      relative_filename: entry.name,
      error: error instanceof Error ? error.code ?? error.name : "unknown_error",
    })));
  }
  return {
    durable_session_id: durableSessionID,
    reservation: reservation === undefined ? null : {
      state: reservation.state,
      pi_session_id: reservation.pi_session_id,
      root_relative_path: boundRelativePath,
    },
    bound_path_exists: boundPathSafe && await exists(boundPath),
    files_truncated: entries.length > SESSION_EVIDENCE_FILE_LIMIT,
    files,
  };
}

async function browserRow(bcb, tab, agentID, timeoutMs) {
  const result = await execBrowser(bcb, tab, `(() => {
    const row = document.querySelector(${JSON.stringify(`[data-agent-id="${agentID}"]`)});
    return {
      connected: document.querySelector('[aria-label="Connection status"]')?.textContent?.includes('Connected') === true,
      found: row instanceof HTMLElement,
      lifecycle: row?.getAttribute('data-managed-lifecycle') ?? null,
      activity: row?.getAttribute('data-managed-activity') ?? null,
      projection_version: row?.getAttribute('data-managed-projection-version') ?? null,
      text: row?.innerText ?? '',
    };
  })()`, timeoutMs);
  return { ...result, observed_monotonic_ms: performance.now() };
}

async function connectPrivateUI(bcb, tab, routerURL) {
  await waitForStartup(async () => (await execBrowser(bcb, tab, "Boolean(document.querySelector('input[placeholder=\"http://127.0.0.1:4000\"]'))")) === true ? true : undefined, "private UI configuration");
  await execBrowser(bcb, tab, reactInputScript("input[placeholder='http://127.0.0.1:4000']", routerURL));
  await execBrowser(bcb, tab, "(() => { const button = [...document.querySelectorAll('button')].find((item) => item.textContent?.trim() === 'Connect'); if (!(button instanceof HTMLButtonElement)) throw new Error('Connect button missing'); button.click(); return true; })()");
  await waitForStartup(async () => (await execBrowser(bcb, tab, "document.querySelector('[aria-label=\"Connection status\"]')?.textContent?.includes('Connected')")) === true ? true : undefined, "private UI connection");
}

async function launchAgent(bcb, tab, router, trial) {
  const payload = { name: trial.agent_id, cwd: router.cwd, model: { provider: PROVIDER, id: MODEL }, initial_instruction: `Keep this unique workload running until the router recovers. ${TOKEN_PREFIX}${trial.workload_token}` };
  const result = await execBrowser(bcb, tab, `(async () => { const response = await fetch('/v1/managed-agents', { method: 'POST', headers: { 'content-type': 'application/json', 'X-Request-ID': ${JSON.stringify(randomUUID())} }, body: ${JSON.stringify(JSON.stringify(payload))} }); return { status: response.status, body: await response.json().catch(() => ({})) }; })()`);
  if (result?.status !== 202) throw failure(`managed launch returned ${result?.status ?? "no status"}`);
  return result.body;
}

async function childClosed(child, deadline) {
  if (child.exitCode !== null || child.signalCode !== null) return { code: child.exitCode, signal: child.signalCode };
  return await new Promise((resolveClose, rejectClose) => {
    const timer = setTimeout(() => rejectClose(failure("isolated router did not exit before the deadline")), remaining(deadline));
    child.once("close", (code, signal) => { clearTimeout(timer); resolveClose({ code, signal }); });
  });
}

async function processAbsent(pid) {
  if (!Number.isSafeInteger(pid) || pid < 2) return true;
  try { process.kill(pid, 0); return false; } catch (error) { return error?.code === "ESRCH"; }
}

function blankArtifact(trial) {
  return {
    trial_id: trial.trial_id,
    agent_id: trial.agent_id,
    logical_agent: trial.logical_agent,
    pi_session_id: null,
    workload_token: trial.workload_token,
    router_pid: null,
    process_identity: null,
    restart_boundary: null,
    runtime_generation_before: null,
    runtime_generation_after: null,
    process_incarnation_before: null,
    process_incarnation_after: null,
    lifecycle_before: null,
    lifecycle_after: null,
    router_projection_version: null,
    replacement_identity: null,
    completion: null,
    persisted_receipt: null,
    bcb_dom: null,
    bcb_console: null,
    elapsed_recovery_ms: null,
    deadline_ms: DEADLINE_MS,
    freshness_token: null,
    bcb_observation_timestamp: null,
    barrier: null,
    private_db_fence: { before: null, after: null },
    launch_evidence: null,
    final_timeout_evidence: null,
    resources: null,
    cleanup: null,
    result: "incomplete",
  };
}

async function runTrial(number, prerequisites, sequenceOutput) {
  const suffix = randomBytes(8).toString("hex");
  const trial = { trial_id: `issue31-trial-${number}-${suffix}`, logical_agent: `issue31-recovery-${number}`, agent_id: `issue31-recovery-${number}-${suffix.slice(0, 8)}`, workload_token: randomBytes(20).toString("hex") };
  const output = join(sequenceOutput, `trial-${number}`);
  await mkdir(output, { recursive: true, mode: 0o700 });
  const artifact = blankArtifact(trial);
  const writeArtifact = async () => writeFile(join(output, "managed-recovery.json"), `${JSON.stringify(artifact, null, 2)}\n`, { mode: 0o600 });
  let root;
  let provider;
  let router;
  let controlledBrowser;
  let bcb;
  let tab;
  const ownedPids = new Set();
  const ports = new Set();

  const cleanup = serializeCleanup(async () => {
    const errors = [];
    if (controlledBrowser) {
      try { await controlledBrowser.close(); } catch (error) { errors.push(`browser: ${String(error)}`); }
    }
    if (router) {
      try { await stopRouterProcess(router, { timeoutMs: 10_000 }); } catch (error) { errors.push(`router: ${String(error)}`); }
    }
    if (provider) {
      try { await provider.close(); } catch (error) { errors.push(`provider: ${String(error)}`); }
    }
    const processStates = await Promise.all([...ownedPids].map(async (pid) => ({ pid, absent: await processAbsent(pid) })));
    if (root) await rm(root, { recursive: true, force: true });
    const portStates = await Promise.all([...ports].map(async (port) => ({ port, available: await isLoopbackPortAvailable(port) })));
    const result = {
      task_root_absent: root === undefined || !(await exists(root)),
      profile_absent: root === undefined || !(await exists(join(root, "cft-profile"))),
      processes_absent: processStates.every(({ absent }) => absent),
      ports_available: portStates.every(({ available }) => available),
      private_tabs_remaining: 0,
      process_states: processStates,
      port_states: portStates,
      errors,
    };
    artifact.cleanup = result;
    assertCleanupComplete(result);
    if (errors.length > 0) throw failure(`isolated cleanup failed: ${errors.join("; ")}`);
    return result;
  });

  const timeoutEvidence = async ({ markTimedOut = true } = {}) => {
    const privateFenceAfterTimeout = router === undefined ? null : await databaseFence(router.database, trial.agent_id, 2_000).catch((error) => ({ error: error instanceof Error ? error.message : String(error) }));
    const evidence = {
      captured_monotonic_ms: performance.now(),
      router: router === undefined ? null : await fetch(`${router.httpUrl}/v1/managed-agents/${encodeURIComponent(trial.agent_id)}`, { signal: globalThis.AbortSignal.timeout(2_000), cache: "no-store" }).then(async (response) => ({ status: response.status, body: await response.json().catch(() => ({})) })).catch((error) => ({ error: String(error) })),
      dom: bcb === undefined || tab === undefined ? null : await browserRow(bcb, tab, trial.agent_id, 2_000).catch((error) => ({ error: String(error) })),
      console: bcb === undefined || tab === undefined ? null : await bcbCommand(bcb, ["cli.bcb_console", "--json", "--tab", String(tab), "--limit", "500"], 2_000).catch((error) => ({ error: String(error) })),
      log: router === undefined ? "" : await readFile(router.log, "utf8").catch(() => ""),
      pi_stderr: router === undefined ? "" : await readFile(router.piStderr, "utf8").catch(() => ""),
      session_header_evidence: router === undefined ? null : await captureSessionHeaderEvidence(router.sessions, router.database, trial.agent_id, artifact.pi_session_id).catch((error) => ({ error: error instanceof Error ? error.message : String(error) })),
      completion_marker_persisted: router !== undefined && await sessionContains(router.sessions, `${COMPLETE_PREFIX}${trial.workload_token}`).catch(() => false),
      private_db_fence_after_timeout: privateFenceAfterTimeout,
      processes: await Promise.all([...ownedPids].map(async (pid) => ({ pid, absent: await processAbsent(pid) }))),
      provider: provider?.evidence() ?? [],
    };
    artifact.final_timeout_evidence = evidence;
    if (markTimedOut) artifact.result = "timed_out";
    await writeArtifact();
    return evidence;
  };

  try {
    root = await createTaskRoot(`managed-recovery-${number}`);
    await mkdir(join(root, "tmp"), { recursive: true, mode: 0o700 });
    provider = await startRecoveryProvider(trial.workload_token);
    ports.add(provider.port);
    const staticUIRoot = await buildRouterUI(root);
    router = await startManagedRouter(root, prerequisites, provider, staticUIRoot, "", { TMPDIR: join(root, "tmp"), MECHA_ACCEPTANCE_TRIAL_ID: trial.trial_id, MECHA_MANAGED_RECOVERY_TOKEN: trial.workload_token, MECHA_PI_SUBAGENTS_EXTENSION: prerequisites.subagents });
    artifact.launch_evidence = assertExplicitSubagentsRouterArgument(router.args, prerequisites.subagents);
    ports.add(router.port);
    ownedPids.add(router.pid);
    artifact.router_pid = router.pid;
    await writeFile(join(root, "router.pid"), `${router.pid}\n`, { mode: 0o600 });
    const taskURL = `${router.httpUrl}/?managed-recovery=${encodeURIComponent(trial.trial_id)}`;
    controlledBrowser = await startIsolatedBcbBrowser(root, taskURL, { width: 1480, height: 980 });
    bcb = controlledBrowser.bcb;
    tab = controlledBrowser.tab.id;
    for (const port of [bcb.httpPort, bcb.wsPort, controlledBrowser.chrome.cdpPort]) ports.add(port);
    ownedPids.add(bcb.server.pid);
    ownedPids.add(controlledBrowser.chrome.chrome.pid);
    artifact.resources = {
      bcb_bridge: `${bcb.server.pid}:${bcb.httpPort}:${bcb.wsPort}`,
      browser_profile: controlledBrowser.chrome.profile,
      browser_tab: String(tab),
      database: router.database,
      router: `${router.pid}:${router.port}`,
      workload_token: trial.workload_token,
    };
    await connectPrivateUI(bcb, tab, router.httpUrl);
    await launchAgent(bcb, tab, router, trial);
    artifact.launch_evidence = await configuredSubagentsEvidence(router.database, trial.agent_id, prerequisites);

    const before = await waitForStartup(async () => {
      const pending = provider.activeBarrier();
      if (pending === undefined) return undefined;
      const current = await databaseFence(router.database, trial.agent_id);
      const projection = await fetch(`${router.httpUrl}/v1/managed-agents/${encodeURIComponent(trial.agent_id)}`, { cache: "no-store" }).then((response) => response.ok ? response.json() : undefined);
      if (projection?.projection_version !== current.projection_version || projection?.agent?.updated_at !== current.updated_at) return undefined;
      const dom = await browserRow(bcb, tab, trial.agent_id, 10_000);
      const domVersion = Number(dom.projection_version);
      if (!dom.connected || !Number.isSafeInteger(domVersion) || domVersion !== projection.projection_version) return undefined;
      return { current, projection, dom, pending };
    }, "tokenized in-progress barrier");

    trial.router_pid = router.pid;
    artifact.pi_session_id = before.current.pi_session_id;
    trial.pi_session_id = before.current.pi_session_id;
    artifact.process_identity = processIdentity(before.current.process_pid, before.current);
    artifact.runtime_generation_before = before.current.runtime_generation;
    artifact.process_incarnation_before = before.current.process_incarnation;
    artifact.lifecycle_before = { lifecycle_state: before.current.lifecycle_state, activity_state: before.current.activity_state, accepting: before.current.accepting_prompts === 1 };
    artifact.private_db_fence.before = before.current;
    const barrier = await persistRouterRecoveryEvidence(router.httpUrl, "barrier", {
      event: "managed_recovery_in_progress",
      trial_id: trial.trial_id,
      token: trial.workload_token,
      agent_id: trial.agent_id,
      observed_monotonic_ms: performance.now(),
    }, performance.now() + 10_000);
    artifact.barrier = barrier;

    // The router POST and immediate GET re-read already validate this exact
    // persisted fence and provider-active acknowledgement atomically enough for
    // the restart boundary. A later runner-side DB read can legitimately race a
    // projection update and is not authority for the persisted barrier.
    validateBarrier(barrier, trial, barrier.fence);

    const boundary = signalRouterAtBoundary(router.child);
    artifact.restart_boundary = boundary.restart_boundary;
    const oldRouter = router;
    const operation = (async () => {
      await childClosed(oldRouter.child, boundary.deadline);
      const replacement = await spawnRouterProcess({ command: oldRouter.command, args: oldRouter.args, cwd: oldRouter.cwd, root, env: oldRouter.launchEnv, expectedPort: oldRouter.port, timeoutMs: remaining(boundary.deadline) });
      ownedPids.add(replacement.pid);
      router = { ...replacement, database: oldRouter.database, log: oldRouter.log, home: oldRouter.home, sessions: oldRouter.sessions, cwd: oldRouter.cwd, piStderr: oldRouter.piStderr, staticUIRoot: oldRouter.staticUIRoot, launchEnv: oldRouter.launchEnv };

      const observed = await waitUntilDeadline(boundary.deadline, async () => {
        const completedRequest = provider.completion();
        const transcriptCompletionPersisted = await sessionContains(router.sessions, `${COMPLETE_PREFIX}${trial.workload_token}`);
        if (completedRequest === undefined || !transcriptCompletionPersisted) return undefined;
        const [current, projection] = await Promise.all([
          databaseFence(router.database, trial.agent_id, remaining(boundary.deadline)),
          fetchJSON(`${router.httpUrl}/v1/managed-agents/${encodeURIComponent(trial.agent_id)}`, boundary.deadline),
        ]);
        if (projection.projection_version !== current.projection_version || projection.agent?.updated_at !== current.updated_at) return undefined;
        const dom = await browserRow(bcb, tab, trial.agent_id, Math.min(1_000, remaining(boundary.deadline)));
        const noStuckState = projection.agent?.lifecycle_state === "running"
          && projection.agent?.activity_state === "idle"
          && projection.agent?.accepting_prompts === true;
        if (!noStuckState || !dom.connected || dom.lifecycle !== "running" || dom.activity !== "idle") return undefined;
        const completionURL = `${router.httpUrl}/v1/acceptance/managed-recovery?kind=completion&trial_id=${encodeURIComponent(trial.trial_id)}`;
        const completionResponse = await fetch(completionURL, { signal: globalThis.AbortSignal.timeout(remaining(boundary.deadline)), cache: "no-store" });
        const receipt = await completionResponse.json().catch(() => ({}));
        if (!completionResponse.ok) throw failure(`${completionURL} returned ${completionResponse.status}: ${typeof receipt?.error === "string" ? receipt.error : "invalid response"}`);
        const receiptMatches = receipt?.trial_id === trial.trial_id
          && receipt.token === trial.workload_token
          && receipt.agent_id === trial.agent_id
          && completedRequest.completed === true
          && completedRequest.active === false
          && receipt.provider_request_seq === completedRequest.sequence
          && receipt.runtime_generation === current.runtime_generation
          && receipt.process_incarnation === current.process_incarnation;
        if (!receiptMatches) throw failure("router completion receipt does not match the observed trial, provider completion, or current runtime fence");
        return { completedRequest, current, projection, dom, receipt, transcriptCompletionPersisted };
      }, "automatic managed recovery completion");

      artifact.runtime_generation_after = observed.current.runtime_generation;
      artifact.process_incarnation_after = observed.current.process_incarnation;
      artifact.lifecycle_after = { lifecycle_state: observed.projection.agent.lifecycle_state, activity_state: observed.projection.agent.activity_state, accepting: observed.projection.agent.accepting_prompts };
      artifact.router_projection_version = observed.projection.projection_version;
      artifact.replacement_identity = processIdentity(observed.current.process_pid, observed.current);
      artifact.private_db_fence.after = observed.current;
      if (artifact.runtime_generation_after === artifact.runtime_generation_before || artifact.process_incarnation_after === artifact.process_incarnation_before) throw failure("replacement runtime identity did not change");
      artifact.persisted_receipt = observed.receipt;
      validateCompletion({
        event: observed.receipt,
        trial,
        restart_boundary: boundary.restart_boundary,
        deadline: boundary.deadline,
        current_fence: observed.current,
        lifecycle: artifact.lifecycle_after,
        persisted_receipt: observed.receipt,
        provider_completion: observed.completedRequest,
        transcript_completion: observed.transcriptCompletionPersisted,
      });
      const domVersion = Number(observed.dom.projection_version);
      validateFreshness({ pre_token: before.projection.projection_version, router_token: observed.projection.projection_version, dom_token: domVersion, restart_boundary: boundary.restart_boundary, observed_monotonic_ms: observed.dom.observed_monotonic_ms });
      artifact.completion = observed.receipt;
      artifact.bcb_dom = observed.dom;
      artifact.bcb_observation_timestamp = observed.dom.observed_monotonic_ms;
      artifact.freshness_token = domVersion;
      artifact.bcb_console = await bcbCommand(bcb, ["cli.bcb_console", "--json", "--tab", String(tab), "--limit", "500"], remaining(boundary.deadline));
      artifact.elapsed_recovery_ms = performance.now() - boundary.restart_boundary;
      const secrets = [
        before.current.pi_session_id, before.current.runtime_generation, before.current.process_incarnation, before.current.process_start_marker, before.current.process_pid,
        observed.current.pi_session_id, observed.current.runtime_generation, observed.current.process_incarnation, observed.current.process_start_marker, observed.current.process_pid,
      ];
      assertArtifactRedacted({ bcb_dom: artifact.bcb_dom, bcb_console: artifact.bcb_console, router_public: observed.projection }, secrets);
      return observed;
    })();

    await runDeadlineBoundOperation({ operation, deadline: boundary.deadline, captureTimeoutEvidence: timeoutEvidence, cleanup });
    artifact.result = "passed";
  } catch (error) {
    artifact.error = error instanceof Error ? error.message : String(error);
    if (artifact.result === "incomplete") artifact.result = "failed";
    if (artifact.final_timeout_evidence === null) {
      try { await timeoutEvidence({ markTimedOut: false }); } catch { /* preserve the original failure */ }
    }
    throw error;
  } finally {
    try { await cleanup(); } catch (error) {
      artifact.cleanup_error = error instanceof Error ? error.message : String(error);
      artifact.result = "cleanup_failed";
    }
    await writeArtifact();
  }
  return { trial_id: trial.trial_id, result: artifact.result, resources: artifact.resources, artifact: join(output, "managed-recovery.json") };
}

export async function runManagedRecovery({ trials = 5 } = {}) {
  if (trials !== 5) throw failure("exactly five trials are required");
  const prerequisites = await recoveryPreflight();
  const output = process.env.MECHA_BCB_ARTIFACT_DIR ?? join("/tmp", `mecha-managed-recovery-${process.pid}-${Date.now()}`);
  if (!output.startsWith("/tmp/")) throw failure("MECHA_BCB_ARTIFACT_DIR must be under /tmp");
  await mkdir(output, { recursive: true, mode: 0o700 });
  const report = { mode: "managed-recovery", result: "incomplete", trials_required: 5, trials: [], started_at: new Date().toISOString(), shared_resources: null };
  const sharedOptions = { bcbPython: prerequisites.python };
  let sharedBefore;
  let runError;
  try {
    sharedBefore = await captureSharedResourceSnapshot(sharedOptions);
    report.trials = await runManagedRecoverySequence({ trials, runTrial: (number) => runTrial(number, prerequisites, output) });
  } catch (error) {
    runError = error;
  }
  if (sharedBefore !== undefined) {
    try {
      const sharedAfter = await captureSharedResourceSnapshot(sharedOptions);
      report.shared_resources = assertSharedResourcesUnchanged({ before: sharedBefore, after: sharedAfter });
    } catch (error) {
      report.shared_resource_error = error instanceof Error ? error.message : String(error);
      runError ??= error;
    }
  }
  if (runError === undefined && report.shared_resources !== null) {
    report.result = "passed";
  } else {
    report.result = "failed";
    report.error = runError instanceof Error ? runError.message : String(runError ?? "shared resource evidence is unavailable");
  }
  report.finished_at = new Date().toISOString();
  await writeFile(join(output, "REPORT.json"), `${JSON.stringify(report, null, 2)}\n`, { mode: 0o600 });
  if (runError !== undefined) throw runError;
  return report;
}
