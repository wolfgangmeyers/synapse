import assert from "node:assert/strict";
import { createServer } from "node:net";
import { execFileSync, spawn } from "node:child_process";
import { once } from "node:events";
import { existsSync, readFileSync, realpathSync } from "node:fs";
import { cp, mkdtemp, readFile, readdir, rm, symlink, mkdir, writeFile } from "node:fs/promises";
import { dirname, join, relative } from "node:path";
import { tmpdir } from "node:os";
import { fileURLToPath, pathToFileURL } from "node:url";
import test from "node:test";
import WebSocket from "ws";
import {
  readJsonLines,
  restoreEnvironment,
  snapshotEnvironment,
  trackTimeoutResources,
} from "./support/native_pi_lifecycle_test_support.ts";

const CHILD_FLAG = "--mecha-messaging-native-pi-lifecycle-child";
const PI_VERSION = "0.84.3";
const NATIVE_VERSION = "19.2.2";
const NATIVE_SOURCE_ROOT = "/Users/wolfgang/code/pi-packages";
const NATIVE_SOURCE_COMMIT = "c058820f9437807c626392b53e56081a6477deac";
// The current source checkout intentionally carries the approved diagnostics instrumentation as
// uncommitted changes. Keep this digest tied to that exact source, so stale packages still fail.
const NATIVE_SOURCE_DIGEST = "f7611c2c33d65f584ebd45c88549bb9bd98abe1885bf2df61ba223e957c5d36c";
const AGENT_ID = "native-lifecycle-e2e";
const PRIVATE_PROMPT = "PRIVATE_NATIVE_PROMPT_7f13";
const PRIVATE_MODEL = "PRIVATE_NATIVE_MODEL_4c92";
const PRIVATE_RESULT = "PRIVATE_NATIVE_RESULT_31b7";
const PRIVATE_ERROR = "PRIVATE_NATIVE_ERROR_d1a8";
const PRIVATE_TIMESTAMP = "PRIVATE_NATIVE_TIMESTAMP_2030";
const CHILD_DESCRIPTION = "Native child label";
const COMPACTION_MARKER = "NATIVE_COMPACTION_SCENARIO";
const WAIT_MS = 12_000;

type RecordValue = Record<string, unknown>;
type PackageIdentity = { root: string; name: string; version: string; peerDependencies: Record<string, string>; extensionEntry: string };
type PiSession = {
  bindExtensions: (options: { mode: string }) => Promise<void>;
  dispose: () => void;
  extensionRunner: { emit: (event: RecordValue) => Promise<unknown> };
  sessionManager: { getSessionFile: () => string | undefined; getSessionId: () => string };
  prompt: (prompt: string) => Promise<void>;
  compact: (customInstructions?: string) => Promise<unknown>;
  abortCompaction: () => void;
  isIdle: boolean;
};
type NativeService = {
  spawn: (type: string, prompt: string, options: RecordValue) => string;
  getLifecycleSnapshots?: () => RecordValue[];
};
type Reader = { frames: RecordValue[]; close: () => Promise<void> };
type LifecycleStatus = { supported: boolean; ready: boolean; routerVersion: number; pending: number; bytes: number };
type TestClient = {
  close: () => void;
  lifecycleStatus: () => LifecycleStatus;
  sendLifecycleSnapshot?: (snapshot: unknown) => string | undefined;
  sendLifecycleEvent?: (event: RecordValue) => string | undefined;
  pause?: () => void;
  resume?: () => Promise<unknown> | void;
  sendActivity: (...args: never[]) => unknown;
};

function isRecord(value: unknown): value is RecordValue {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}
function stringValue(value: unknown): string | undefined { return typeof value === "string" ? value : undefined; }
function packageValue(value: unknown, key: string): unknown { return isRecord(value) ? value[key] : undefined; }
function wait(ms: number): Promise<void> { return new Promise((resolve) => setTimeout(resolve, ms)); }

async function waitFor(check: () => boolean | Promise<boolean>, description: string, timeoutMs = WAIT_MS): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (await check()) return;
    await wait(25);
  }
  assert.fail(`Timed out waiting for ${description}`);
}
async function withTimeout<T>(promise: Promise<T>, description: string, timeoutMs = WAIT_MS): Promise<T> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  try {
    return await Promise.race([promise, new Promise<T>((_, reject) => { timer = setTimeout(() => reject(new Error(`Timed out waiting for ${description}`)), timeoutMs); })]);
  } finally {
    if (timer !== undefined) clearTimeout(timer);
  }
}

function readPackageIdentity(root: string, expectedName: string): PackageIdentity {
  const manifestPath = join(root, "package.json");
  const parsed: unknown = JSON.parse(readFileSync(manifestPath, "utf8"));
  const name = stringValue(packageValue(parsed, "name"));
  const version = stringValue(packageValue(parsed, "version"));
  if (name !== expectedName || version === undefined || version.length === 0) throw new Error(`package identity mismatch at ${root}`);
  const peers: Record<string, string> = {};
  const peerValue = packageValue(parsed, "peerDependencies");
  if (isRecord(peerValue)) for (const [key, value] of Object.entries(peerValue)) if (typeof value === "string") peers[key] = value;
  const extensions = packageValue(packageValue(parsed, "pi"), "extensions");
  const extensionEntry = Array.isArray(extensions) && typeof extensions[0] === "string" ? extensions[0] : "src/index.ts";
  return { root, name, version, peerDependencies: peers, extensionEntry };
}
function isTrackedSourceCheckout(root: string): boolean {
  try {
    execFileSync("git", ["-C", root, "ls-files", "--error-unmatch", "package.json"], {
      stdio: ["ignore", "ignore", "ignore"],
      timeout: 5_000,
    });
    return true;
  } catch {
    return false;
  }
}
function globalNpmRoot(): string {
  return execFileSync("npm", ["root", "-g"], {
    encoding: "utf8",
    timeout: 5_000,
    env: { PATH: process.env.PATH ?? "", HOME: process.env.HOME ?? "" },
  }).trim();
}
function resolveInstalledPi(): PackageIdentity {
  const configured = process.env.MECHA_PI_PACKAGE_ROOT;
  const root = configured !== undefined && configured.trim().length > 0
    ? configured.trim()
    : join(globalNpmRoot(), "@earendil-works", "pi-coding-agent");
  const resolved = realpathSync(root);
  if (isTrackedSourceCheckout(resolved)) throw new Error(`installed Pi resolves to a source checkout: ${resolved}`);
  return readPackageIdentity(resolved, "@earendil-works/pi-coding-agent");
}
async function recursiveFiles(root: string): Promise<string[]> {
  if (!existsSync(root)) return [];
  const entries = await readdir(root, { withFileTypes: true });
  const result: string[] = [];
  for (const entry of entries) {
    const path = join(root, entry.name);
    if (entry.isDirectory()) result.push(...await recursiveFiles(path)); else result.push(path);
  }
  return result;
}
function assertVersionAtLeast(actual: string, minimum: string, packageName: string): void {
  const actualParts = actual.split(".").map(Number);
  const minimumParts = minimum.split(".").map(Number);
  assert.equal(actualParts.length, 3, `${packageName} version must be major.minor.patch`);
  assert.equal(minimumParts.length, 3, `${packageName} peer minimum must be major.minor.patch`);
  for (let index = 0; index < 3; index += 1) {
    if (actualParts[index] !== minimumParts[index]) {
      assert.equal(actualParts[index] > minimumParts[index], true, `${packageName}@${actual} does not satisfy >=${minimum}`);
      return;
    }
  }
}

async function packageNative(): Promise<{ identity: PackageIdentity; cleanup: () => Promise<void> }> {
  const packageRoot = join(NATIVE_SOURCE_ROOT, "packages", "pi-subagents");
  if (!existsSync(packageRoot)) throw new Error(`approved native source is missing: ${packageRoot}`);
  const sourceCommit = execFileSync("git", ["-C", NATIVE_SOURCE_ROOT, "rev-parse", "HEAD"], { encoding: "utf8" }).trim();
  assert.equal(sourceCommit, NATIVE_SOURCE_COMMIT);
  const files = (await recursiveFiles(packageRoot)).filter((path) => {
    const name = relative(packageRoot, path);
    return name === "package.json" || name === "rollup.dts.config.mjs" || name.startsWith("src/");
  }).sort();
  const { createHash } = await import("node:crypto");
  const hash = createHash("sha256");
  for (const path of files) { hash.update(relative(packageRoot, path)); hash.update("\0"); hash.update(await readFile(path)); hash.update("\0"); }
  assert.equal(hash.digest("hex"), NATIVE_SOURCE_DIGEST);
  const stage = await mkdtemp(join(tmpdir(), "mecha-native-pi-package-"));
  const runtime = join(stage, "runtime");
  try {
    execFileSync("pnpm", ["--filter", "@gotgenes/pi-subagents", "build:types"], { cwd: NATIVE_SOURCE_ROOT, stdio: "ignore", timeout: 30_000 });
    execFileSync("pnpm", ["--filter", "@gotgenes/pi-subagents", "pack", "--pack-destination", stage], { cwd: NATIVE_SOURCE_ROOT, stdio: "ignore", timeout: 30_000 });
    const artifact = (await readdir(stage)).find((entry) => entry.endsWith(".tgz"));
    if (artifact === undefined) throw new Error("native package tarball was not created");
    execFileSync("npm", ["install", "--prefix", runtime, "--no-save", "--package-lock=false", "--ignore-scripts", "--omit=peer", join(stage, artifact)], { stdio: "ignore", timeout: 30_000 });
    const root = join(runtime, "node_modules", "@gotgenes", "pi-subagents");
    const identity = readPackageIdentity(root, "@gotgenes/pi-subagents");
    assert.equal(identity.version, NATIVE_VERSION);
    assert.deepEqual(identity.peerDependencies, {
      "@earendil-works/pi-ai": ">=0.75.0",
      "@earendil-works/pi-coding-agent": ">=0.80.5",
      "@earendil-works/pi-tui": ">=0.75.0",
    });
    assert.equal(identity.extensionEntry, "./src/index.ts");
    const manifest = JSON.parse(readFileSync(join(root, "package.json"), "utf8")) as Record<string, unknown>;
    const extensions = packageValue(packageValue(manifest, "pi"), "extensions");
    assert.deepEqual(extensions, ["./src/index.ts"]);
    assert.equal(existsSync(join(root, ".git")), false);
    console.log(`native-pi-e2e package=${JSON.stringify({ sourceCommit, sourceDigest: NATIVE_SOURCE_DIGEST, artifact, root: realpathSync(root) })}`);
    return { identity: { ...identity, root: realpathSync(root) }, cleanup: () => rm(stage, { recursive: true, force: true }) };
  } catch (error) {
    await rm(stage, { recursive: true, force: true });
    throw error;
  }
}
async function linkPackage(target: string, source: string): Promise<void> { await mkdir(dirname(target), { recursive: true }); await symlink(source, target, "dir"); }
async function prepareModuleGraph(root: string, pi: PackageIdentity, native: PackageIdentity): Promise<{ root: string; piPath: string; nativePath: string }> {
  const moduleRoot = join(root, "modules");
  const isolatedPi = join(moduleRoot, "installed", "@earendil-works", "pi-coding-agent");
  await mkdir(dirname(isolatedPi), { recursive: true });
  await cp(pi.root, isolatedPi, { recursive: true });
  for (const name of ["@earendil-works/pi-coding-agent", "@earendil-works/pi-ai", "@earendil-works/pi-tui"]) {
    const source = name === "@earendil-works/pi-coding-agent" ? isolatedPi : join(isolatedPi, "node_modules", ...name.split("/"));
    if (!existsSync(join(source, "package.json"))) throw new Error(`copied Pi peer is missing: ${name}`);
    await linkPackage(join(moduleRoot, "node_modules", ...name.split("/")), source);
  }
  const typeboxSource = [
    join(native.root, "node_modules", "@sinclair", "typebox"),
    join(NATIVE_SOURCE_ROOT, ".pi/npm/node_modules/@sinclair/typebox"),
    join(execFileSync("npm", ["root", "-g"], { encoding: "utf8" }).trim(), "@sinclair/typebox"),
  ].find((candidate) => existsSync(join(candidate, "package.json")));
  if (typeboxSource === undefined) throw new Error("native package typebox dependency is missing");
  const typeboxTarget = join(moduleRoot, "node_modules", "@sinclair", "typebox");
  await mkdir(dirname(typeboxTarget), { recursive: true });
  await cp(realpathSync(typeboxSource), typeboxTarget, { recursive: true });
  const nativeTarget = join(moduleRoot, "node_modules", "@gotgenes", "pi-subagents");
  await mkdir(dirname(nativeTarget), { recursive: true });
  await cp(native.root, nativeTarget, { recursive: true });
  const nativePackages = (await recursiveFiles(moduleRoot)).filter((path) => path.endsWith("@gotgenes/pi-subagents/package.json"));
  assert.equal(nativePackages.length, 1, "isolated module graph must contain exactly one native extension package");
  assert.equal(realpathSync(nativePackages[0]), realpathSync(join(nativeTarget, "package.json")));
  const installedPeerVersions = [
    readPackageIdentity(isolatedPi, "@earendil-works/pi-coding-agent"),
    readPackageIdentity(join(isolatedPi, "node_modules", "@earendil-works", "pi-ai"), "@earendil-works/pi-ai"),
    readPackageIdentity(join(isolatedPi, "node_modules", "@earendil-works", "pi-tui"), "@earendil-works/pi-tui"),
  ];
  assertVersionAtLeast(installedPeerVersions[0].version, "0.80.5", "@earendil-works/pi-coding-agent");
  assertVersionAtLeast(installedPeerVersions[1].version, "0.75.0", "@earendil-works/pi-ai");
  assertVersionAtLeast(installedPeerVersions[2].version, "0.75.0", "@earendil-works/pi-tui");
  const piPath = join(moduleRoot, "node_modules", "@earendil-works", "pi-coding-agent", "dist", "index.js");
  const nativePath = join(nativeTarget, native.extensionEntry);
  if (!existsSync(piPath) || !existsSync(nativePath)) throw new Error("isolated installed module graph is incomplete");
  assert.equal(realpathSync(dirname(dirname(piPath))), realpathSync(isolatedPi));
  assert.equal(native.extensionEntry, "./src/index.ts");
  assert.equal(realpathSync(nativePath), realpathSync(join(nativeTarget, "src/index.ts")));
  return { root: moduleRoot, piPath, nativePath };
}

async function ephemeralPort(): Promise<number> {
  const server = createServer();
  await new Promise<void>((resolve, reject) => { server.once("error", reject); server.listen(0, "127.0.0.1", () => resolve()); });
  const address = server.address();
  if (address === null || typeof address === "string") throw new Error("ephemeral port allocation failed");
  const port = address.port;
  await new Promise<void>((resolve) => server.close(() => resolve()));
  return port;
}
type RouterProcess = { process: ReturnType<typeof spawn>; http: string; ws: string; output: string };
type ResourceTracker = ReturnType<typeof trackTimeoutResources>;
function processExited(child: ReturnType<typeof spawn>): boolean {
  return child.exitCode !== null || child.signalCode !== null;
}
async function waitForProcessExit(child: ReturnType<typeof spawn>, description: string, timeoutMs: number): Promise<boolean> {
  if (processExited(child)) return true;
  try {
    await waitFor(() => processExited(child), description, timeoutMs);
    return true;
  } catch {
    return false;
  }
}
async function terminateProcess(child: ReturnType<typeof spawn>, description: string): Promise<boolean> {
  if (!processExited(child)) {
    try { child.kill("SIGTERM"); } catch { /* the process may have exited between the check and kill */ }
    await waitForProcessExit(child, description, 10_000);
  }
  if (!processExited(child)) {
    try { child.kill("SIGKILL"); } catch { /* the process may have exited between the check and kill */ }
    await waitForProcessExit(child, `${description} after SIGKILL`, 2_000);
  }
  return processExited(child);
}
async function startRouter(binary: string, database: string, logPath: string, port: number, home: string, resources?: ResourceTracker): Promise<RouterProcess> {
  const child = spawn(binary, ["--database", database, "--log", logPath, "--host", "127.0.0.1", "--port", String(port)], { env: { ...process.env, HOME: home }, stdio: ["ignore", "pipe", "pipe"] });
  resources?.trackChild(child, "isolated-router");
  let output = "";
  child.stdout.on("data", (chunk) => { output += chunk.toString("utf8"); });
  child.stderr.on("data", (chunk) => { output += chunk.toString("utf8"); });
  try {
    await waitFor(() => /MECHA_V2_ROUTER_READY http=\S+ ws=\S+/.test(output), "isolated Go router startup", 15_000);
    const match = /MECHA_V2_ROUTER_READY http=(\S+) ws=(\S+)/.exec(output);
    if (match === null) throw new Error(`router startup output did not include URLs: ${output}`);
    return { process: child, http: match[1], ws: match[2], output };
  } catch (error) {
    await terminateProcess(child, "failed isolated Go router");
    throw error;
  }
}
async function stopRouter(router: RouterProcess | undefined): Promise<boolean> {
  if (router === undefined) return true;
  return terminateProcess(router.process, "isolated Go router shutdown");
}
async function createReader(wsUrl: string, id: string, resources?: ResourceTracker): Promise<Reader> {
  const socket = new WebSocket(wsUrl);
  resources?.trackSocket(socket, `reader:${id}`);
  const frames: RecordValue[] = [];
  socket.on("message", (payload) => { try { const value: unknown = JSON.parse(payload.toString("utf8")); if (isRecord(value)) frames.push(value); } catch { /* waiters handle malformed output */ } });
  await withTimeout(once(socket, "open").then(() => undefined), "lifecycle reader connection", 5_000);
  const send = (frame: RecordValue) => socket.send(JSON.stringify(frame));
  send({ version: "v1", frame_id: `${id}-register`, type: "register", agent_id: id, instance_id: `${id}-instance`, conversation_sync: "v1" });
  await waitFor(() => frames.some((frame) => frame.type === "registered"), "lifecycle reader registration");
  const registered = frames.find((frame) => frame.type === "registered");
  const leaseId = stringValue(registered?.lease_id);
  if (leaseId === undefined) throw new Error("lifecycle reader lease missing");
  send({ version: "v1", frame_id: `${id}-sync`, type: "subagent_sync_enable", agent_id: id, instance_id: `${id}-instance`, lease_id: leaseId });
  await waitFor(() => frames.some((frame) => frame.type === "subagent_state_snapshot"), "lifecycle reader initial snapshot");
  return { frames, async close() { if (socket.readyState === WebSocket.CLOSED) return; socket.close(); await wait(100); if (socket.readyState !== WebSocket.CLOSED) socket.terminate(); } };
}
function frameChildren(frame: RecordValue): RecordValue[] {
  const agents = frame.type === "subagent_state_snapshot" ? (Array.isArray(frame.agents) ? frame.agents : []) : [frame.agent];
  return agents.filter(isRecord).flatMap((agent) => Array.isArray(agent.subagents) ? agent.subagents.filter(isRecord) : []);
}
function hasChild(frame: RecordValue, id: string): boolean { return frameChildren(frame).some((child) => child.id === id && ["queued", "running"].includes(String(child.status))); }
async function currentAgent(http: string): Promise<RecordValue | undefined> {
  const response = await fetch(`${http}/v1/agents?models=omit`);
  if (!response.ok) throw new Error(`router agents failed: ${response.status}`);
  const body: unknown = await response.json();
  const agents = isRecord(body) && Array.isArray(body.agents) ? body.agents : [];
  return agents.filter(isRecord).find((agent) => agent.agent_id === AGENT_ID);
}

async function runChild(binary: string, pi: PackageIdentity, native: PackageIdentity): Promise<void> {
  const root = await mkdtemp(join(tmpdir(), "mecha-native-pi-e2e-"));
  const home = join(root, "home");
  const project = join(root, "project");
  const agentDir = join(root, "pi-agent");
  const sessions = join(root, "sessions");
  const temp = join(root, "tmp");
  const database = join(root, "router.sqlite");
  const logPath = join(root, "router.jsonl");
  const moduleGraph = await prepareModuleGraph(root, pi, native);
  const routerPort = await ephemeralPort();
  let router: RouterProcess | undefined;
  let session: PiSession | undefined;
  let shutdownDone = false;
  const readers: Reader[] = [];
  const lifecycleTraces: RecordValue[] = [];
  const lifecycleWireFrames: Array<{ direction: "outbound" | "inbound"; connection: number; frame: RecordValue }> = [];
  let connectionNumber = 0;
  let holdLifecycleFrames = false;
  let parentRawSend: ((data: Parameters<WebSocket["send"]>[0]) => void) | undefined;
  let dropLifecycleAckEventId: string | undefined;
  const shutdownTrace: string[] = [];
  const postShutdownActivity: unknown[] = [];
  let compactionMode: "success" | "failure" | undefined;
  const compactionRuns: Array<{ mode: string; before: boolean; resumed: boolean; failed: boolean }> = [];
  let releaseChild = () => {};
  let releaseReplayChild = () => {};
  let releaseShutdownChild = () => {};
  let shutdownNativeExecutionActive = false;
  let resolveShutdownNativeExecutionStarted: (() => void) | undefined;
  const shutdownNativeExecutionStarted = new Promise<void>((resolve) => { resolveShutdownNativeExecutionStarted = resolve; });
  let captureHang = false;
  let captureRequestStarted = false;
  let captureRequestAborted = false;
  let testError: unknown;
  const originalEnv = snapshotEnvironment(); const originalCwd = process.cwd();
  const originalFetch = globalThis.fetch;
  const timeoutResources = trackTimeoutResources();
  const setTimeoutImpl: typeof setTimeout = (...args) => {
    const handle = setTimeout(...args);
    timeoutResources.trackTimer(handle, "timeout", "lifecycle-timeout");
    return handle;
  };
  const clearTimeoutImpl: typeof clearTimeout = (handle) => {
    timeoutResources.clearTimer(handle);
    clearTimeout(handle);
  };
  const setIntervalImpl: typeof setInterval = (...args) => {
    const handle = setInterval(...args);
    timeoutResources.trackTimer(handle, "interval", "lifecycle-interval");
    return handle;
  };
  const clearIntervalImpl: typeof clearInterval = (handle) => {
    timeoutResources.clearTimer(handle);
    clearInterval(handle);
  };
  try {
    await mkdir(project, { recursive: true }); await mkdir(home, { recursive: true }); await mkdir(temp, { recursive: true }); await mkdir(sessions, { recursive: true });
    process.chdir(project);
    for (const key of Object.keys(process.env)) delete process.env[key];
    Object.assign(process.env, { HOME: home, PATH: originalEnv.PATH ?? "", TMPDIR: temp, PI_CODING_AGENT_DIR: agentDir });
    router = await startRouter(binary, database, logPath, routerPort, home, timeoutResources);
    process.env.MECHA_ROUTER_URL = router.http;
    console.log(`native-pi-e2e isolation=${JSON.stringify({ root, home, project, database, router: router.http, routerPort, moduleGraph: moduleGraph.root })}`);
    Object.defineProperty(globalThis, "WebSocket", { value: WebSocket, configurable: true, writable: true });
    const coding = await import(pathToFileURL(moduleGraph.piPath).href);
    const ai = await import(pathToFileURL(join(moduleGraph.root, "node_modules/@earendil-works/pi-ai/dist/index.js")).href);
    const nativeModule = await import(pathToFileURL(moduleGraph.nativePath).href);
    const messagingModule = await import(pathToFileURL(join(originalCwd, "pi-extensions/mecha-messaging/index.ts")).href);
    const faux = ai.fauxProvider({ provider: "native-lifecycle-e2e", models: [{ id: "native-lifecycle-model", name: "Native lifecycle model", reasoning: false, input: ["text"], cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 }, contextWindow: 128_000, maxTokens: 256 }], tokenSize: { min: 1, max: 1 }, tokensPerSecond: 1_000 });
    let release: (() => void) | undefined; const childGate = new Promise<void>((resolve) => { release = resolve; }); releaseChild = () => release?.();
    let releaseReplay: (() => void) | undefined; const replayChildGate = new Promise<void>((resolve) => { releaseReplay = resolve; }); releaseReplayChild = () => releaseReplay?.();
    let releaseShutdown: (() => void) | undefined; const shutdownChildGate = new Promise<void>((resolve) => { releaseShutdown = resolve; }); releaseShutdownChild = () => releaseShutdown?.();
    const response = async (context: unknown, options: { signal?: AbortSignal } | undefined) => {
      faux.appendResponses([response]);
      if (isRecord(context) && Array.isArray(context.messages) && context.messages.some((message) => JSON.stringify(message).includes(PRIVATE_PROMPT))) { await childGate; return ai.fauxAssistantMessage(PRIVATE_RESULT); }
      if (isRecord(context) && Array.isArray(context.messages) && context.messages.some((message) => JSON.stringify(message).includes("REPLAY_NATIVE_PROMPT"))) { await replayChildGate; return ai.fauxAssistantMessage(PRIVATE_RESULT); }
      if (isRecord(context) && Array.isArray(context.messages) && context.messages.some((message) => JSON.stringify(message).includes("SHUTDOWN_NATIVE_PROMPT"))) {
        shutdownNativeExecutionActive = true;
        resolveShutdownNativeExecutionStarted?.();
        try { await shutdownChildGate; } finally { shutdownNativeExecutionActive = false; }
        return ai.fauxAssistantMessage(PRIVATE_RESULT);
      }
      if (compactionMode === "failure") {
        await new Promise<void>((_, reject) => { const abort = () => reject(new Error(`${PRIVATE_ERROR} model=${PRIVATE_MODEL}`)); if (options?.signal?.aborted) abort(); else options?.signal?.addEventListener("abort", abort, { once: true }); });
      }
      return ai.fauxAssistantMessage(`${COMPACTION_MARKER} ${PRIVATE_RESULT} ${PRIVATE_TIMESTAMP}`);
    };
    faux.setResponses([response]);
    const parentModel = faux.getModel();
    const factoryCalls = { native: 0, messaging: 0 };
    const originalModelRuntimeCreate = coding.ModelRuntime.create;
    coding.ModelRuntime.create = async function patchedModelRuntimeCreate(options: unknown) {
      const runtime = await originalModelRuntimeCreate.call(this, options);
      runtime.registerNativeProvider(faux.provider);
      return runtime;
    };
    const modelRuntime = await coding.ModelRuntime.create({ modelsPath: null, refreshOnCreate: false });
    const factory = (piApi: unknown) => { factoryCalls.native += 1; if (typeof nativeModule.default !== "function") throw new Error("native package has no extension factory"); nativeModule.default(piApi); };
    const compactionProbe = (piApi: unknown) => {
      if (!isRecord(piApi) || typeof piApi.on !== "function") return;
      piApi.on("session_before_compact", (event: unknown) => {
        if (compactionMode === undefined) return;
        const preparation = isRecord(event) && isRecord(event.preparation) ? event.preparation : undefined;
        if (typeof preparation?.firstKeptEntryId !== "string" || typeof preparation.tokensBefore !== "number") throw new Error("compaction preparation missing");
        compactionRuns.push({ mode: compactionMode, before: true, resumed: false, failed: false });
      });
      piApi.on("session_compact", () => { const run = compactionRuns.at(-1); if (run?.mode === "success") run.resumed = true; });
      piApi.on("session_compact_failed", () => { const run = compactionRuns.at(-1); if (run?.mode === "failure") run.failed = true; });
    };
    const shutdownProbe = (piApi: unknown) => { if (!isRecord(piApi) || typeof piApi.on !== "function") return; piApi.on("session_shutdown", async () => { shutdownTrace.push("shutdown-start"); if (shutdownNativeExecutionActive) shutdownTrace.push("shutdown-during-native-execution"); await Promise.resolve(); shutdownTrace.push("shutdown-end"); }); };
    const clients: TestClient[] = [];
    const messagingFactory = (piApi: unknown) => {
      factoryCalls.messaging += 1;
      messagingModule.createMechaMessagingExtension(piApi, { resolveCurrentAgentId: () => AGENT_ID, nativeSubagentTrace: { record: (event: string, fields: object = {}) => lifecycleTraces.push({ event, ...fields }) }, setTimeoutImpl, clearTimeoutImpl, setIntervalImpl, clearIntervalImpl, createClient: (options: RecordValue) => {
        const webSocketFactory = (url: string) => {
          const connection = ++connectionNumber;
          const socket = new WebSocket(url);
          timeoutResources.trackSocket(socket, `mailbox:${connection}`);
          const emit = socket.emit.bind(socket);
          Object.defineProperty(socket, "emit", {
            configurable: true,
            value: (event: string | symbol, ...args: unknown[]) => {
              if (event === "message" && dropLifecycleAckEventId !== undefined) {
                const payload = args[0];
                try {
                  const frame: unknown = JSON.parse(typeof payload === "string" ? payload : String(payload));
                  if (isRecord(frame) && frame.type === "subagent_lifecycle_acknowledged"
                    && (dropLifecycleAckEventId === "__next__" || frame.event_id === dropLifecycleAckEventId)) {
                    dropLifecycleAckEventId = undefined;
                    return true;
                  }
                } catch { /* non-JSON frames belong to the normal client path */ }
              }
              return emit(event, ...args);
            },
          });
          const send = socket.send.bind(socket);
          if (connection === 1) parentRawSend = send;
          socket.send = (data: Parameters<WebSocket["send"]>[0]) => {
            try {
              const frame: unknown = JSON.parse(typeof data === "string" ? data : data.toString());
              if (isRecord(frame)) {
                lifecycleWireFrames.push({ direction: "outbound", connection, frame: structuredClone(frame) });
                if (holdLifecycleFrames && typeof frame.type === "string" && frame.type.startsWith("subagent_lifecycle_")) return;
              }
            } catch { /* malformed frames are handled by the router */ }
            send(data);
          };
          socket.on("message", (data) => { try { const frame: unknown = JSON.parse(data.toString()); if (isRecord(frame)) lifecycleWireFrames.push({ direction: "inbound", connection, frame: structuredClone(frame) }); } catch { /* waiters handle malformed output */ } });
          return socket;
        };
        const client = messagingModule.createMailboxClient({ ...options, webSocketFactory, onLifecycleTrace: (value: RecordValue) => lifecycleTraces.push(value) });
        const sendActivity = client.sendActivity;
        client.sendActivity = (...args: Parameters<typeof client.sendActivity>) => { if (shutdownDone) postShutdownActivity.push(args); return sendActivity(...args); };
        const close = client.close; client.close = () => { shutdownTrace.push("client-close"); close(); }; clients.push(client); return client;
      } });
    };
    const settings = coding.SettingsManager.inMemory({ compaction: { enabled: false, reserveTokens: 1, keepRecentTokens: 1 }, retry: { enabled: false } });
    const manager = coding.SessionManager.create(project, sessions);
    const resourceLoader = new coding.DefaultResourceLoader({ cwd: project, agentDir, noSkills: true, noPromptTemplates: true, noThemes: true, noContextFiles: true, extensionFactories: [shutdownProbe, compactionProbe, factory, messagingFactory] });
    await resourceLoader.reload(); assert.equal(resourceLoader.getExtensions().errors.length, 0);
    const result = await coding.createAgentSession({ cwd: project, agentDir, modelRuntime, model: parentModel, settingsManager: settings, sessionManager: manager, resourceLoader, noTools: "builtin" });
    session = result.session; await session.bindExtensions({ mode: "json" });
    assert.deepEqual(factoryCalls, { native: 1, messaging: 1 }, "the canonical native and messaging extensions must each load exactly once");
    await withTimeout(session.prompt("initialize parent"), "parent initialization");
    await waitFor(() => clients.length === 1, "parent mailbox client startup");
    const reader = await createReader(router.ws, "native-lifecycle-reader", timeoutResources); readers.push(reader);
    const registry = Object.getOwnPropertyDescriptor(globalThis, Symbol.for("@gotgenes/pi-subagents:service-registry"))?.value;
    const services = isRecord(registry) && registry.services instanceof Map ? registry.services : undefined;
    const service = services?.get(manager.getSessionId());
    if (!isRecord(service) || typeof service.spawn !== "function") throw new Error("native service was not published");
    const parentService = service as NativeService;
    const childId = parentService.spawn("general-purpose", PRIVATE_PROMPT, { description: CHILD_DESCRIPTION, maxTurns: 1, inheritContext: false });
    await waitFor(() => reader.frames.some((frame) => hasChild(frame, childId)), "redacted running child");
    await withTimeout(session.prompt("settle parent"), "parent prompt");
    await waitFor(async () => (await currentAgent(router?.http ?? ""))?.subagent_count === 1, "durable child projection");
    assert.equal(clients.length, 1);
    const retryClient = clients[0];
    if (retryClient === undefined) throw new Error("parent mailbox client was not created");
    let retryEvent: string | undefined;
    let replacementEvent: string | undefined;
    let snapshotCalls = 0;
    let captureSnapshots = false;
    const originalSnapshot = retryClient.sendLifecycleSnapshot;
    if (typeof originalSnapshot !== "function") throw new Error("lifecycle snapshot publisher was not installed");
    retryClient.sendLifecycleSnapshot = (snapshot: unknown) => {
      const eventId = originalSnapshot(snapshot);
      if (!captureSnapshots) return eventId;
      snapshotCalls += 1;
      if (snapshotCalls === 1 && typeof eventId === "string") retryEvent = eventId;
      if (snapshotCalls === 2 && typeof eventId === "string") { replacementEvent = eventId; retryClient.pause?.(); }
      return eventId;
    };
    await waitFor(() => retryClient.lifecycleStatus?.().pending === 0, "initial lifecycle journal settlement");
    dropLifecycleAckEventId = "__next__";
    const replayChildId = parentService.spawn("general-purpose", "REPLAY_NATIVE_PROMPT", { description: "Replay child label", maxTurns: 1, inheritContext: false });
    await waitFor(() => reader.frames.some((frame) => hasChild(frame, replayChildId)), "running replay child");
    const lifecycleSnapshot = parentService.getLifecycleSnapshots?.() ?? [];
    holdLifecycleFrames = true;
    const lifecycleEvent = retryClient.sendLifecycleEvent?.({
      lifecycleEvent: "started",
      subagent: { id: replayChildId, type: "general-purpose", description: "Replay child label", status: "running" },
      snapshot: lifecycleSnapshot,
    });
    if (typeof lifecycleEvent !== "string") throw new Error("lifecycle event publisher was not installed");
    const lifecycleEventFrame = lifecycleWireFrames.find(({ direction, frame }) => direction === "outbound" && frame.event_id === lifecycleEvent)?.frame;
    if (lifecycleEventFrame === undefined || parentRawSend === undefined) throw new Error("lifecycle replay seed event was not captured");
    parentRawSend(JSON.stringify(lifecycleEventFrame));
    await waitFor(() => reader.frames.some((frame) => frame.type === "subagent_state_update" && hasChild(frame, replayChildId)), "durable lifecycle replay seed");
    captureSnapshots = true;
    holdLifecycleFrames = true;
    retryClient.sendLifecycleSnapshot(lifecycleSnapshot);
    retryClient.sendLifecycleSnapshot(lifecycleSnapshot);
    await waitFor(() => (retryClient.lifecycleStatus?.().pending ?? 0) >= 2, "unacknowledged lifecycle journal items");
    const oldRouter = router; await stopRouter(oldRouter); router = undefined;
    holdLifecycleFrames = false;
    router = await startRouter(binary, database, logPath, routerPort, home, timeoutResources);
    await Promise.resolve(retryClient.resume?.());
    const restartReader = await createReader(router.ws, "native-lifecycle-reader-restart", timeoutResources); readers.push(restartReader);
    await waitFor(() => lifecycleTraces.filter((trace) => trace.event === "client_lifecycle_registered").length >= 2, "client lifecycle reconnect");
    await waitFor(() => lifecycleTraces.some((trace) => trace.event === "client_lifecycle_router_snapshot" && trace.frame_type === "subagent_state_replay"), "router replay before journal retry");
    const replayIndex = lifecycleTraces.findIndex((trace) => trace.event === "client_lifecycle_router_snapshot" && trace.frame_type === "subagent_state_replay");
    const retryIndex = lifecycleTraces.findIndex((trace, index) => index > replayIndex && trace.event === "client_lifecycle_publish_attempt" && trace.event_id === retryEvent);
    assert.equal(typeof retryEvent, "string"); assert.equal(typeof replacementEvent, "string"); assert.equal(retryIndex > replayIndex, true);
    const replay = lifecycleWireFrames.find(({ direction, connection, frame }) => direction === "inbound" && connection >= 2 && frame.type === "subagent_state_replay")?.frame;
    if (replay === undefined || !Array.isArray(replay.updates)) throw new Error("router replay payload was not received");
    assert.deepEqual(Object.keys(replay).sort(), ["frame_id", "router_version", "type", "updates", "version"]);
    assert.equal(replay.version, "v1");
    assert.equal(typeof replay.frame_id, "string");
    assert.equal(typeof replay.router_version, "number");
    assert.equal(replay.updates.length > 0, true, "router replay must contain at least one update");
    const sortReplayChildren = (value: unknown): unknown[] => Array.isArray(value) ? [...value].sort((left, right) => {
      const leftKey = isRecord(left) && typeof left.id === "string" ? left.id : "";
      const rightKey = isRecord(right) && typeof right.id === "string" ? right.id : "";
      return leftKey < rightKey ? -1 : leftKey > rightKey ? 1 : 0;
    }) : [];
    const normalizedReplayUpdates = replay.updates.map((update) => isRecord(update) ? { ...update, subagents: sortReplayChildren(update.subagents) } : update);
    assert.deepEqual(normalizedReplayUpdates, [{
      agent_id: AGENT_ID,
      subagent_count: 2,
      subagent_version: (replay.updates[0] as RecordValue).subagent_version,
      subagents: sortReplayChildren([
        { id: replayChildId, type: "general-purpose", description: "Replay child label", status: "running" },
        { id: childId, type: "general-purpose", description: CHILD_DESCRIPTION, status: "running" },
      ]),
    }], "router replay updates must exactly describe the running native children");
    assert.equal(Number.isSafeInteger((replay.updates[0] as RecordValue).subagent_version), true);
    const reconnectLifecycle = lifecycleWireFrames.filter(({ direction, connection, frame }) => direction === "outbound" && connection >= 2 && typeof frame.type === "string" && frame.type.startsWith("subagent_lifecycle_"));
    assert.equal(reconnectLifecycle.length >= 2, true, "reconnect did not flush the lifecycle journal");
    for (const { frame } of reconnectLifecycle) {
      assert.equal(frame.version, "v1");
      for (const field of ["frame_id", "agent_id", "instance_id", "parent_session_id", "lease_id", "producer_epoch", "event_id"]) assert.equal(typeof frame[field], "string", `lifecycle frame is missing ${field}`);
      assert.equal(typeof frame.client_sequence, "number");
    }
    const reconnectPending = reconnectLifecycle.filter(({ frame }) => frame.event_id === retryEvent || frame.event_id === replacementEvent);
    assert.deepEqual(reconnectPending.slice(0, 4).map(({ frame }) => frame.event_id), [retryEvent, replacementEvent, retryEvent, replacementEvent], "reconnect must preserve the journal order while retrying the duplicate and replacement");
    assert.equal(reconnectPending.some(({ frame }) => frame.event_id === retryEvent), true);
    const firstRetry = lifecycleWireFrames.find(({ direction, frame }) => direction === "outbound" && frame.event_id === retryEvent);
    const retried = [...lifecycleWireFrames].reverse().find(({ direction, frame }) => direction === "outbound" && frame.event_id === retryEvent);
    const replacement = lifecycleWireFrames.find(({ direction, frame }) => direction === "outbound" && frame.event_id === replacementEvent);
    if (firstRetry === undefined || retried === undefined || replacement === undefined) throw new Error("lifecycle duplicate or replacement frame was not captured");
    assert.equal(retried.frame.producer_epoch, firstRetry.frame.producer_epoch);
    assert.equal(retried.frame.client_sequence, firstRetry.frame.client_sequence);
    assert.notEqual(retried.frame.frame_id, firstRetry.frame.frame_id);
    assert.equal(typeof retried.frame.lease_id, "string");
    const retryTraces = lifecycleTraces.filter((trace) => trace.event === "client_lifecycle_publish_attempt" && trace.event_id === retryEvent);
    assert.equal(retryTraces.length >= 2, true, "lifecycle retry identity trace was not recorded");
    assert.equal(retryTraces[0]?.frame_id, firstRetry.frame.frame_id);
    assert.equal(retryTraces.at(-1)?.frame_id, retried.frame.frame_id);
    assert.equal(retryTraces.at(-1)?.producer_epoch, retried.frame.producer_epoch);
    assert.equal(retryTraces.at(-1)?.lease_id, retried.frame.lease_id);
    assert.equal((retryTraces.at(-1)?.connection_generation ?? 0) > (retryTraces[0]?.connection_generation ?? 0), true);
    const retryAckTrace = lifecycleTraces.find((trace) => trace.event === "client_lifecycle_acknowledged" && trace.event_id === retryEvent && trace.request_id === retried.frame.frame_id);
    assert.equal(retryAckTrace?.ack_frame_id !== undefined, true, "lifecycle ACK identity trace was not recorded");
    assert.equal(Number(replacement.frame.client_sequence) > Number(firstRetry.frame.client_sequence), true);
    const duplicateReceipts = lifecycleWireFrames.filter(({ direction, frame }) => direction === "inbound" && frame.type === "subagent_lifecycle_acknowledged" && frame.disposition === "duplicate");
    if (duplicateReceipts.length === 0) throw new Error("router did not receipt any duplicate lifecycle frame");
    for (const { frame } of duplicateReceipts) {
      assert.equal(typeof frame.event_id, "string");
      assert.equal(typeof frame.producer_epoch, "string");
      assert.equal(typeof frame.client_sequence, "number");
      assert.equal(typeof frame.router_version, "number");
    }
    await waitFor(() => retryClient.lifecycleStatus?.().pending === 0, "lifecycle journal settlement after reconnect");
    assert.equal(retryClient.lifecycleStatus?.().pending, 0, "reconnected lifecycle journal must be empty");
    releaseReplayChild(); releaseChild(); await waitFor(() => (parentService.getLifecycleSnapshots?.() ?? []).length === 0, "native child completion");
    await waitFor(async () => (await currentAgent(router?.http ?? ""))?.subagent_count === 0, "durable child removal");
    const childSessionDir = join(temp, `pi-subagents-${process.getuid?.() ?? 0}`, project.replace(/[\\/]/g, "-").replace(/^-+/, ""), "tasks");
    const childTranscripts = (await recursiveFiles(childSessionDir)).filter((path) => path.endsWith(".jsonl"));
    assert.equal(childTranscripts.length > 0, true, "native child transcript was not persisted");
    const transcript = (await Promise.all(childTranscripts.map((path) => readFile(path, "utf8")))).join("\\n");
    assert.equal(transcript.includes(PRIVATE_PROMPT), true); assert.equal(transcript.includes(PRIVATE_RESULT), true);
    const providerCallsBeforeCompaction = faux.state.callCount;
    compactionMode = "success"; const successful = session.compact("successful lifecycle compaction"); await waitFor(async () => (await currentAgent(router?.http ?? ""))?.activity_state === "compacting", "successful compaction activity"); await withTimeout(successful, "successful compaction"); compactionMode = undefined; assert.equal(faux.state.callCount > providerCallsBeforeCompaction, true, "successful compaction did not call the installed Pi provider"); const compactedSessionFile = session.sessionManager.getSessionFile(); if (typeof compactedSessionFile !== "string") throw new Error("installed Pi did not expose its session file"); assert.equal(readFileSync(compactedSessionFile, "utf8").includes(COMPACTION_MARKER), true, "successful compaction did not persist the provider result"); await waitFor(() => compactionRuns.at(-1)?.resumed === true, "successful compaction resume");
    await withTimeout(session.prompt("post-compaction turn"), "post-compaction prompt"); await waitFor(() => session?.isIdle === true, "post-compaction idle");
    compactionMode = "failure"; const failed = session.compact("failed lifecycle compaction"); await waitFor(async () => (await currentAgent(router?.http ?? ""))?.activity_state === "compacting", "failed compaction activity"); session.abortCompaction(); await assert.rejects(failed); compactionMode = undefined; await waitFor(() => compactionRuns.at(-1)?.failed === true, "failed compaction event");
    assert.deepEqual(compactionRuns.map(({ mode, before, resumed, failed: didFail }) => ({ mode, before, resumed, failed: didFail })), [{ mode: "success", before: true, resumed: true, failed: false }, { mode: "failure", before: true, resumed: false, failed: true }]);
    const allowed = new Set(["id", "type", "description", "status"]);
    for (const child of readers.flatMap((item) => item.frames.flatMap(frameChildren))) for (const key of Object.keys(child)) assert.equal(allowed.has(key), true);
    const routerLog = readJsonLines(logPath);
    assert.equal(routerLog.length > 0, true, "isolated router JSONL log was empty");
    const receipts = lifecycleWireFrames.filter(({ direction, frame }) => direction === "inbound" && frame.type === "subagent_lifecycle_acknowledged").map(({ frame }) => frame);
    assert.equal(receipts.length > 0, true, "router sent no lifecycle receipts");
    const serialized = JSON.stringify({ lifecycleTraces, status: retryClient.lifecycleStatus?.(), receipts, frames: readers.flatMap((item) => item.frames), routerLog });
    for (const secret of [PRIVATE_PROMPT, PRIVATE_MODEL, PRIVATE_RESULT, PRIVATE_ERROR, PRIVATE_TIMESTAMP]) assert.equal(serialized.includes(secret), false, `lifecycle telemetry leaked ${secret}`);

    const shutdownChildId = parentService.spawn("general-purpose", "SHUTDOWN_NATIVE_PROMPT", { description: "Shutdown execution child", maxTurns: 1, inheritContext: false });
    await waitFor(() => (parentService.getLifecycleSnapshots?.() ?? []).some((child) => child.id === shutdownChildId && ["queued", "running"].includes(String(child.status))), "running child during shutdown");
    assert.equal((parentService.getLifecycleSnapshots?.() ?? []).some((child) => child.id === shutdownChildId), true);
    await waitFor(async () => (await currentAgent(router?.http ?? ""))?.subagent_count === 1, "durable child execution during shutdown");
    await withTimeout(shutdownNativeExecutionStarted, "native child model execution during shutdown");
    assert.equal(shutdownNativeExecutionActive, true, "native child execution resolved before shutdown started");
    const hangingCaptureFetch: typeof fetch = async (input, init) => {
      if (!captureHang || !String(input).includes("/v1/memory/lookup")) return originalFetch(input, init);
      captureRequestStarted = true;
      return await new Promise<Response>((_, reject) => {
        const abort = () => { captureRequestAborted = true; reject(new Error("forced capture timeout")); };
        if (init?.signal?.aborted) abort(); else init?.signal?.addEventListener("abort", abort, { once: true });
      });
    };
    captureHang = true;
    globalThis.fetch = hangingCaptureFetch;
    try {
      await session.extensionRunner.emit({ type: "session_shutdown", reason: "test" });
      shutdownDone = true;
    } finally {
      captureHang = false;
      globalThis.fetch = originalFetch;
    }
    assert.equal(captureRequestStarted, true, "shutdown did not start memory capture");
    assert.equal(captureRequestAborted, true, "hanging shutdown capture did not reach its timeout");
    assert.equal(shutdownNativeExecutionActive, true, "shutdown resolved after native execution completed");
    const shutdownDuringExecutionIndex = shutdownTrace.indexOf("shutdown-during-native-execution");
    const shutdownEndIndex = shutdownTrace.indexOf("shutdown-end");
    assert.equal(shutdownDuringExecutionIndex >= 0, true, "shutdown was not observed during native execution");
    assert.equal(shutdownEndIndex > shutdownDuringExecutionIndex, true, "shutdown observation did not precede shutdown resolution");
    assert.equal(shutdownTrace.indexOf("client-close") > shutdownEndIndex, true);
    releaseShutdownChild();
    await waitFor(() => (parentService.getLifecycleSnapshots?.() ?? []).length === 0, "shutdown child completion");
    await wait(250);
    assert.deepEqual(postShutdownActivity, [], "scheduler emitted activity after shutdown");
    const traceAfterShutdown = lifecycleTraces.length;
    await wait(100);
    assert.equal(lifecycleTraces.length, traceAfterShutdown, "coordinator emitted lifecycle work after shutdown");
    session.dispose(); session = undefined;
    // The tracker includes the isolated router and readers, so close the E2E harness before checking it.
    for (const reader of readers) await reader.close();
    await stopRouter(router);
    await wait(100);
    assert.deepEqual(timeoutResources.activeResources(), { timers: [], intervals: [], sockets: [], childProcesses: [] }, "lifecycle left owned resources running");
    console.log(`native-pi-e2e contract=${JSON.stringify({ compaction: compactionRuns, replayBeforeRetry: true, replayUpdatesExact: true, lifecycleJournalPending: 0, duplicateAndReplacementOrdering: true, durableRemoval: true, redacted: true, shutdownDuringExecution: true, shutdownCaptureTimedOut: true, shutdownOrder: shutdownTrace })}`);
  } catch (error) {
    testError = error;
    throw error;
  } finally {
    const cleanupErrors: string[] = [];
    const attemptCleanup = async (label: string, action: () => Promise<void> | void) => {
      try { await action(); } catch (error) { cleanupErrors.push(`${label}: ${error instanceof Error ? error.message : String(error)}`); }
    };
    releaseReplayChild();
    releaseChild();
    releaseShutdownChild();
    globalThis.fetch = originalFetch;
    await attemptCleanup("session shutdown", async () => {
      if (session !== undefined && !shutdownDone) await withTimeout(session.extensionRunner.emit({ type: "session_shutdown", reason: "cleanup" }), "cleanup shutdown", 2_000);
    });
    await attemptCleanup("session disposal", () => { session?.dispose(); session = undefined; });
    for (const reader of readers) await attemptCleanup("reader close", () => reader.close());
    const routerStopped = await stopRouter(router).catch((error) => {
      cleanupErrors.push(`router shutdown: ${error instanceof Error ? error.message : String(error)}`);
      return false;
    });
    await attemptCleanup("environment restore", () => restoreEnvironment(originalEnv));
    await attemptCleanup("cwd restore", () => process.chdir(originalCwd));
    await attemptCleanup("temporary root removal", () => rm(root, { recursive: true, force: true }));
    const resources = timeoutResources.activeResources();
    timeoutResources.disable();
    const rootRemoved = !existsSync(root);
    const environmentRestored = JSON.stringify(snapshotEnvironment()) === JSON.stringify(originalEnv);
    const cwdRestored = process.cwd() === originalCwd;
    if (testError === undefined) {
      assert.deepEqual(resources, { timers: [], intervals: [], sockets: [], childProcesses: [] }, "lifecycle cleanup left owned resources");
      assert.equal(rootRemoved, true, `temporary root remained: ${root}`);
      assert.equal(routerStopped, true, "isolated router was not terminated");
      assert.equal(environmentRestored, true, "test environment was not restored");
      assert.equal(cwdRestored, true, "test cwd was not restored");
      assert.deepEqual(cleanupErrors, []);
    }
    console.log(`native-pi-e2e teardown=${JSON.stringify({ rootRemoved, routerStopped, environmentRestored, cwdRestored, resources, cleanupErrors })}`);
  }
}

if (process.argv.includes(CHILD_FLAG)) {
  let exitCode = 0;
  try {
    const pi = resolveInstalledPi();
    const nativeRoot = process.env.MECHA_PI_SUBAGENTS_PACKAGE_ROOT;
    if (nativeRoot === undefined) throw new Error("native package root is required");
    await runChild(process.env.MECHA_ROUTER_BINARY ?? "", pi, readPackageIdentity(nativeRoot, "@gotgenes/pi-subagents"));
  } catch (error) { console.error(error instanceof Error ? error.stack ?? error.message : String(error)); exitCode = 1; }
  process.exit(exitCode);
} else {
  test("runs the installed Pi lifecycle against an isolated current Go router", { timeout: 120_000 }, async () => {
    const pi = resolveInstalledPi(); assert.equal(pi.version, PI_VERSION);
    const binaryRoot = await mkdtemp(join(tmpdir(), "mecha-native-router-"));
    const binary = join(binaryRoot, "mailbox-router");
    const native = await packageNative();
    try {
      execFileSync("go", ["build", "-o", binary, "./cmd/mailbox-router"], { cwd: join(process.cwd(), "router-proxy"), timeout: 60_000, stdio: "ignore" });
      const loader = process.execArgv.find((argument) => argument.includes("tsx/dist/loader.mjs"));
      const preflight = process.execArgv.find((argument) => argument.includes("tsx/dist/preflight.cjs"));
      const args = [...(preflight === undefined ? [] : [preflight]), ...(loader === undefined ? [] : [loader]), fileURLToPath(import.meta.url), CHILD_FLAG];
      const child = spawn(process.execPath, args, { cwd: process.cwd(), env: { PATH: process.env.PATH ?? "", MECHA_PI_PACKAGE_ROOT: pi.root, MECHA_PI_SUBAGENTS_PACKAGE_ROOT: native.identity.root, MECHA_ROUTER_BINARY: binary }, stdio: ["ignore", "pipe", "pipe"] });
      let stdout = ""; let stderr = ""; child.stdout.on("data", (chunk) => { stdout += chunk.toString("utf8"); }); child.stderr.on("data", (chunk) => { stderr += chunk.toString("utf8"); });
      const childExited = await waitForProcessExit(child, "native lifecycle E2E child", 100_000);
      if (!childExited) await terminateProcess(child, "native lifecycle E2E child timeout");
      const code = child.exitCode;
      const signal = child.signalCode;
      const port = stdout.match(/"router":"http:\/\/127\.0\.0\.1:(\d+)"/)?.[1];
      const listener = port === undefined ? "missing-port" : (() => { try { return execFileSync("lsof", ["-nP", `-iTCP:${port}`, "-sTCP:LISTEN"], { encoding: "utf8" }).trim(); } catch { return ""; } })();
      assert.equal(code, 0, `isolated lifecycle child failed (${String(signal)}):\n${stdout}\n${stderr}`); assert.equal(listener, "", `isolated router listener remained: ${listener}`); assert.match(stdout, /native-pi-e2e contract=/); assert.match(stdout, /"rootRemoved":true/); process.stdout.write(stdout);
    } finally { try { await native.cleanup(); } finally { await rm(binaryRoot, { recursive: true, force: true }); } }
  });
}
