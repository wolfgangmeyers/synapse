import assert from "node:assert/strict";
import { execFileSync, spawn } from "node:child_process";
import { once } from "node:events";
import { existsSync, readFileSync, realpathSync } from "node:fs";
import { cp, mkdir, mkdtemp, readFile, readdir, rm, symlink, writeFile } from "node:fs/promises";
import { dirname, join } from "node:path";
import { tmpdir } from "node:os";
import { fileURLToPath, pathToFileURL } from "node:url";
import test from "node:test";
import WebSocket from "ws";

import { openMessageRepository } from "../harness/db.js";
import { createNativeSubagentTrace as createRouterTrace } from "../harness/native_subagent_trace.js";
import { createAgentRouter } from "../harness/router.js";
import { createActivityLifecycle, createSettledNotificationScheduler } from "../pi-extensions/mecha-messaging/activity-lifecycle.ts";
import { createMailboxClient } from "../pi-extensions/mecha-messaging/mailbox-client.ts";
import type { ActivityContext, NativeSubagent } from "../pi-extensions/mecha-messaging/activity-lifecycle.ts";

const CHILD_FLAG = "--mecha-messaging-native-pi-lifecycle-child";
const AGENT_ID = "native-lifecycle-e2e";
const READER_ID = "native-lifecycle-reader";
const PRIVATE_PROMPT = "PRIVATE_NATIVE_PROMPT_7f13";
const PRIVATE_OUTPUT = "PRIVATE_NATIVE_OUTPUT_9c42";
const PRIVATE_ERROR = "PRIVATE_NATIVE_ERROR_d1a8";
const PRIVATE_TIMESTAMP = "PRIVATE_NATIVE_TIMESTAMP_2030";
const CHILD_DESCRIPTION = "Native child label";
const FAILURE_MARKER = "NATIVE_FAILURE_SCENARIO";
const STEER_MARKER = "NATIVE_STEER_SCENARIO";
const ABORT_MARKER = "NATIVE_ABORT_SCENARIO";
const STOP_HOLDER_MARKER = "NATIVE_STOP_HOLDER_SCENARIO";
const STOP_QUEUED_MARKER = "NATIVE_STOP_QUEUED_SCENARIO";
const REPLACEMENT_MARKER = "NATIVE_REPLACEMENT_SCENARIO";
const MISSED_EVENT_MARKER = "NATIVE_MISSED_EVENT_SCENARIO";
const MISSED_EVENT_DESCRIPTION = "Missed event child";
const COMPACTION_MARKER = "NATIVE_COMPACTION_SCENARIO";
const ABORTED_MARKER = "NATIVE_ABORTED_SCENARIO";
const MAILBOX_MARKER = "NATIVE_MAILBOX_SCENARIO";
const SENTINEL_MARKER = "NATIVE_SENTINEL_SCENARIO";
const WAIT_MS = 12_000;

type WireRecord = Record<string, unknown>;
type Repository = ReturnType<typeof openMessageRepository>;
type Router = ReturnType<typeof createAgentRouter>;
type PiSession = {
  bindExtensions: (options: { mode: string }) => Promise<void>;
  dispose: () => void;
  extensionRunner: { hasHandlers: (name: string) => boolean; emit: (event: WireRecord) => Promise<unknown> };
  sessionManager: { getSessionFile: () => string | undefined };
  prompt: (prompt: string) => Promise<void>;
  compact: (customInstructions?: string) => Promise<unknown>;
  isIdle: boolean;
};
type NativeService = {
  spawn: (type: string, prompt: string, options: WireRecord) => string;
  getRecord?: (id: string) => WireRecord | undefined;
  listAgents?: () => WireRecord[];
  abort?: (id: string) => boolean;
  steer?: (id: string, message: string) => Promise<boolean>;
  registerWorkspaceProvider?: (provider: { prepare: (context: unknown) => Promise<unknown> }) => () => void;
};
type LifecycleReader = {
  frames: WireRecord[];
  waitFor: (predicate: (frame: WireRecord) => boolean, description: string, timeoutMs?: number) => Promise<WireRecord>;
  close: () => Promise<void>;
};
type PackageIdentity = {
  root: string;
  name: string;
  version: string;
  peerDependencies: Record<string, string>;
  extensionEntry: string;
};
type DeferredGate = { promise: Promise<void>; release: () => void; entered: number };

function isRecord(value: unknown): value is WireRecord {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function stringValue(value: unknown): string | undefined {
  return typeof value === "string" ? value : undefined;
}

function isNativeService(value: unknown): value is NativeService {
  return isRecord(value) && typeof value.spawn === "function";
}

function nativeRecord(service: NativeService, id: string): WireRecord | undefined {
  if (typeof service.getRecord === "function") return service.getRecord(id);
  return service.listAgents?.().find((record) => record.id === id);
}

function createDeferredGate(): DeferredGate {
  let release = () => {};
  const gate = new Promise<void>((resolvePromise) => { release = resolvePromise; });
  return { promise: gate, release, entered: 0 };
}

function contextContains(context: unknown, marker: string): boolean {
  return isRecord(context) && Array.isArray(context.messages)
    && context.messages.some((message) => JSON.stringify(message).includes(marker));
}

async function runMissedEventRepairHybrid(): Promise<void> {
  // The packaged manager always emits its public started event. Its public API has no
  // switch for dropping one event, so only the event-less mutation is hybrid here.
  // The real native service poll and router path are exercised by the main scenario.
  const records: NativeSubagent[] = [];
  const snapshots: unknown[] = [];
  let heartbeat: (() => void) | undefined;
  let timer: ReturnType<typeof setInterval> | undefined;
  const context: ActivityContext = { sessionManager: { getSessionId: () => "hybrid-parent" }, isIdle: () => false, ui: { notify() {} } };
  const lifecycle = createActivityLifecycle({
    getClient: () => ({ sendActivity() {}, sendLifecycleSnapshot: (value) => { snapshots.push(value); return "hybrid-snapshot"; } }),
    getParentContext: () => context,
    isCompacting: () => false,
    getGoalProjection: () => undefined,
    getCurrentModel: () => undefined,
    getNativeService: () => ({ getLifecycleSnapshots: () => records }),
    notify: () => {},
    trace: () => {},
    createSettledScheduler: (options) => createSettledNotificationScheduler(options),
    setIntervalImpl: (callback, delay) => {
      heartbeat = callback;
      timer = setInterval(() => {}, delay);
      return timer;
    },
    clearIntervalImpl: (value) => { clearInterval(value); },
  });
  lifecycle.onAgentStart(context);
  records.push({ id: "hybrid-missed-child", type: "worker", status: "running", description: MISSED_EVENT_DESCRIPTION });
  heartbeat?.();
  assert.equal(JSON.stringify(snapshots.at(-1)).includes("hybrid-missed-child"), true);
  records.length = 0;
  heartbeat?.();
  assert.equal(JSON.stringify(snapshots.at(-1)).includes("hybrid-missed-child"), false);
  lifecycle.onSessionBoundary();
  if (timer !== undefined) clearInterval(timer);
}

async function runCompactionBoundaryHybrid(): Promise<void> {
  // A positive manual compaction requires the provider's summary API. The deterministic
  // faux provider cannot supply that summary without entering Pi's unrelated stale-context
  // failure path, so the pure lifecycle boundary is checked with the same injected timer and
  // client seams. The real Pi canceled-compaction hook is exercised by the caller.
  const records: NativeSubagent[] = [{ id: "hybrid-compaction-child", type: "worker", status: "running", description: "hybrid compaction child" }];
  const activities: Array<{ state: string; payload?: unknown }> = [];
  const snapshots: unknown[] = [];
  let compacting = false;
  let heartbeat: (() => void) | undefined;
  let timer: ReturnType<typeof setInterval> | undefined;
  const context: ActivityContext = { sessionManager: { getSessionId: () => "hybrid-compaction-parent" }, isIdle: () => false, ui: { notify() {} } };
  const lifecycle = createActivityLifecycle({
    getClient: () => ({ sendActivity: (state, payload) => { activities.push({ state, payload }); }, sendLifecycleSnapshot: (value) => { snapshots.push(value); return "hybrid-compaction-snapshot"; } }),
    getParentContext: () => context,
    isCompacting: () => compacting,
    getGoalProjection: () => undefined,
    getCurrentModel: () => undefined,
    getNativeService: () => ({ getLifecycleSnapshots: () => records }),
    notify: () => {},
    trace: () => {},
    createSettledScheduler: (options) => createSettledNotificationScheduler(options),
    setIntervalImpl: (callback, delay) => {
      heartbeat = callback;
      timer = setInterval(() => {}, delay);
      return timer;
    },
    clearIntervalImpl: (value) => { clearInterval(value); },
  });
  lifecycle.onAgentStart(context);
  heartbeat?.();
  compacting = true;
  lifecycle.onCompactionStarted(context);
  assert.equal(activities.at(-1)?.state, "compacting");
  compacting = false;
  lifecycle.onCompactionResumed(context);
  heartbeat?.();
  assert.equal(activities.at(-1)?.state, "active");
  assert.equal(JSON.stringify(snapshots.at(-1)).includes("hybrid-compaction-child"), true);
  lifecycle.onSessionBoundary();
  if (timer !== undefined) clearInterval(timer);
}

function packageValue(value: unknown, key: string): unknown {
  return isRecord(value) ? value[key] : undefined;
}

function readPackageIdentity(root: string, expectedName: string): PackageIdentity {
  const manifestPath = join(root, "package.json");
  if (!existsSync(manifestPath)) throw new Error(`Installed package manifest is missing: ${manifestPath}`);
  let parsed: unknown;
  try {
    parsed = JSON.parse(readFileSync(manifestPath, "utf8"));
  } catch (error) {
    throw new Error(`Installed package manifest is unreadable: ${manifestPath}: ${String(error)}`);
  }
  const name = stringValue(packageValue(parsed, "name"));
  const version = stringValue(packageValue(parsed, "version"));
  if (name !== expectedName || version === undefined || version.length === 0) {
    throw new Error(`Installed package identity mismatch at ${root}: expected ${expectedName}, found ${name ?? "unknown"}@${version ?? "unknown"}`);
  }
  const peers: Record<string, string> = {};
  const peerValue = packageValue(parsed, "peerDependencies");
  if (isRecord(peerValue)) {
    for (const [key, value] of Object.entries(peerValue)) {
      if (typeof value === "string") peers[key] = value;
    }
  }
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

function globalNpmRoot(): string | undefined {
  try {
    const root = execFileSync("npm", ["root", "-g"], {
      encoding: "utf8",
      timeout: 5_000,
      env: { PATH: process.env.PATH ?? "", HOME: process.env.HOME ?? "" },
    }).trim();
    return root.length > 0 ? root : undefined;
  } catch {
    return undefined;
  }
}

function resolveInstalledPackage(
  name: string,
  environmentName: string,
  configuredRoot: string | undefined,
  options: { requireConfiguredRoot?: boolean } = {},
): PackageIdentity {
  if (options.requireConfiguredRoot === true && configuredRoot === undefined) {
    throw new Error(`Required package root is not explicitly configured: ${name}. Set ${environmentName} to the installed package root.`);
  }
  const root = configuredRoot ?? (() => {
    const npmRoot = globalNpmRoot();
    return npmRoot === undefined ? undefined : join(npmRoot, ...name.split("/"));
  })();
  if (root === undefined || !existsSync(root)) {
    throw new Error(`Required installed package is missing: ${name}. Set ${environmentName} to an installed package root.`);
  }
  const resolvedRoot = realpathSync(root);
  if (isTrackedSourceCheckout(resolvedRoot)) {
    throw new Error(`The E2E cannot load ${name} from a source checkout: ${resolvedRoot}`);
  }
  return { ...readPackageIdentity(resolvedRoot, name), root: resolvedRoot };
}

function versionParts(value: string): [number, number, number] | undefined {
  const match = /^(\d+)\.(\d+)\.(\d+)(?:[-+].*)?$/.exec(value);
  return match === null ? undefined : [Number(match[1]), Number(match[2]), Number(match[3])];
}

function compareVersions(left: string, right: string): number {
  const leftParts = versionParts(left);
  const rightParts = versionParts(right);
  if (leftParts === undefined || rightParts === undefined) throw new Error(`Unsupported package version: ${left} or ${right}`);
  for (let index = 0; index < leftParts.length; index += 1) {
    if (leftParts[index] !== rightParts[index]) return leftParts[index] > rightParts[index] ? 1 : -1;
  }
  return 0;
}

function satisfiesPeer(version: string, range: string): boolean {
  const minimum = /^>=\s*(\d+\.\d+\.\d+)/.exec(range);
  if (minimum !== null) return compareVersions(version, minimum[1]) >= 0;
  return /^\d+\.\d+\.\d+(?:[-+].*)?$/.test(range) && compareVersions(version, range) === 0;
}

function installedPeerRoot(piRoot: string, packageName: string): string | undefined {
  const npmRoot = globalNpmRoot();
  const candidates = [
    join(piRoot, "node_modules", ...packageName.split("/")),
    join(dirname(dirname(piRoot)), ...packageName.split("/")),
    npmRoot === undefined ? "" : join(npmRoot, ...packageName.split("/")),
  ];
  return candidates.find((candidate) => candidate.length > 0 && existsSync(join(candidate, "package.json")));
}

function verifyPackagePair(pi: PackageIdentity, native: PackageIdentity): PackageIdentity[] {
  const peers = ["@earendil-works/pi-coding-agent", "@earendil-works/pi-ai", "@earendil-works/pi-tui"];
  const identities: PackageIdentity[] = [pi];
  for (const peerName of peers) {
    const range = native.peerDependencies[peerName];
    if (range === undefined) throw new Error(`${native.name}@${native.version} declares no ${peerName} peer dependency`);
    const root = peerName === "@earendil-works/pi-coding-agent" ? pi.root : installedPeerRoot(pi.root, peerName);
    if (root === undefined) throw new Error(`Required installed peer is missing: ${peerName}`);
    const identity = readPackageIdentity(realpathSync(root), peerName);
    if (!satisfiesPeer(identity.version, range)) {
      throw new Error(`Incompatible native-Pi peer: ${native.name}@${native.version} requires ${peerName} ${range}, found ${identity.version}`);
    }
    identities.push({ ...identity, root: realpathSync(root) });
  }
  return identities;
}

async function linkPackage(target: string, source: string): Promise<void> {
  await mkdir(dirname(target), { recursive: true });
  await symlink(source, target, "dir");
}

async function prepareModuleGraph(root: string, pi: PackageIdentity, native: PackageIdentity): Promise<string> {
  const moduleRoot = join(root, "module-resolution");
  const isolatedPiRoot = join(moduleRoot, "installed", "@earendil-works", "pi-coding-agent");
  await mkdir(dirname(isolatedPiRoot), { recursive: true });
  await cp(pi.root, isolatedPiRoot, { recursive: true });
  for (const peerName of ["@earendil-works/pi-coding-agent", "@earendil-works/pi-ai", "@earendil-works/pi-tui"]) {
    const source = peerName === "@earendil-works/pi-coding-agent"
      ? isolatedPiRoot
      : join(isolatedPiRoot, "node_modules", ...peerName.split("/"));
    if (!existsSync(join(source, "package.json"))) throw new Error(`Cannot resolve copied installed peer ${peerName}`);
    await linkPackage(join(moduleRoot, "node_modules", ...peerName.split("/")), source);
  }
  const npmRoot = globalNpmRoot();
  const typebox = [
    join(native.root, "node_modules", "@sinclair", "typebox"),
    join(dirname(dirname(native.root)), "@sinclair", "typebox"),
    npmRoot === undefined ? "" : join(npmRoot, "@sinclair", "typebox"),
  ].find((candidate) => candidate.length > 0 && existsSync(join(candidate, "package.json")));
  if (typebox === undefined) throw new Error("Required installed dependency is missing: @sinclair/typebox");
  const isolatedTypebox = join(moduleRoot, "node_modules", "@sinclair", "typebox");
  await mkdir(dirname(isolatedTypebox), { recursive: true });
  await cp(realpathSync(typebox), isolatedTypebox, { recursive: true });
  const isolatedNative = join(moduleRoot, "node_modules", "@gotgenes", "pi-subagents");
  await mkdir(dirname(isolatedNative), { recursive: true });
  await cp(native.root, isolatedNative, { recursive: true });
  return moduleRoot;
}

async function waitForFrame(
  frames: WireRecord[],
  predicate: (frame: WireRecord) => boolean,
  description: string,
  timeoutMs = WAIT_MS,
): Promise<WireRecord> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const frame = frames.find(predicate);
    if (frame !== undefined) return frame;
    await new Promise((resolvePromise) => setTimeout(resolvePromise, 25));
  }
  throw new Error(`Timed out waiting for ${description}`);
}

async function createLifecycleReader(websocketUrl: string, readerId: string): Promise<LifecycleReader> {
  const socket = new WebSocket(websocketUrl);
  const frames: WireRecord[] = [];
  socket.on("message", (payload) => {
    try {
      const frame: unknown = JSON.parse(payload.toString("utf8"));
      if (isRecord(frame)) frames.push(frame);
    } catch {
      // Ignore malformed observer input; the awaited protocol frame will time out.
    }
  });
  await withTimeout(once(socket, "open").then(() => undefined), "lifecycle reader connection", 5_000);
  const send = (frame: WireRecord) => socket.send(JSON.stringify(frame));
  send({ version: "v1", frame_id: `${readerId}-register`, type: "register", agent_id: readerId, instance_id: `${readerId}-instance`, conversation_sync: "v1" });
  const registered = await waitForFrame(frames, (frame) => frame.type === "registered", "lifecycle reader registration");
  const leaseId = stringValue(registered.lease_id);
  if (leaseId === undefined) throw new Error("Lifecycle reader registration did not return a lease");
  send({
    version: "v1", frame_id: `${readerId}-sync`, type: "subagent_sync_enable",
    agent_id: readerId, instance_id: `${readerId}-instance`, lease_id: leaseId,
  });
  await waitForFrame(frames, (frame) => frame.type === "subagent_state_snapshot", "lifecycle reader initial snapshot");
  return {
    frames,
    waitFor: (predicate, description, timeoutMs = WAIT_MS) => waitForFrame(frames, predicate, description, timeoutMs),
    async close() {
      if (socket.readyState === WebSocket.CLOSED) return;
      const closed = once(socket, "close");
      socket.close();
      await Promise.race([closed, new Promise((resolvePromise) => setTimeout(resolvePromise, 1_000))]);
      if (socket.readyState !== WebSocket.CLOSED) socket.terminate();
    },
  };
}

function routerHttpUrl(websocketUrl: string): string {
  const url = new URL(websocketUrl);
  url.protocol = "http:";
  url.pathname = "";
  return url.toString().replace(/\/$/, "");
}

async function currentAgent(httpUrl: string): Promise<WireRecord | undefined> {
  const response = await fetch(`${httpUrl}/v1/agents?models=omit`);
  if (!response.ok) throw new Error(`Router agent snapshot failed: ${response.status}`);
  const body: unknown = await response.json();
  const agents = isRecord(body) && Array.isArray(body.agents) ? body.agents : [];
  return agents.map((agent) => isRecord(agent) ? agent : undefined).find((agent) => agent?.agent_id === AGENT_ID);
}

async function waitForAsync(check: () => boolean | Promise<boolean>, description: string, timeoutMs = WAIT_MS): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (await check()) return;
    await new Promise((resolvePromise) => setTimeout(resolvePromise, 25));
  }
  assert.fail(`Timed out waiting for ${description}`);
}

async function withTimeout<T>(promise: Promise<T>, description: string, timeoutMs = WAIT_MS): Promise<T> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  try {
    return await Promise.race([
      promise,
      new Promise<T>((_resolve, reject) => {
        timer = setTimeout(() => reject(new Error(`Timed out waiting for ${description}`)), timeoutMs);
      }),
    ]);
  } finally {
    if (timer !== undefined) clearTimeout(timer);
  }
}

function frameAgents(frame: WireRecord): WireRecord[] {
  const candidates: unknown[] = frame.type === "subagent_state_snapshot" ? (Array.isArray(frame.agents) ? frame.agents : []) : [frame.agent];
  return candidates.filter((candidate): candidate is WireRecord => isRecord(candidate));
}

function frameChildren(frame: WireRecord): WireRecord[] {
  return frameAgents(frame).flatMap((agent) => Array.isArray(agent.subagents)
    ? agent.subagents.filter((child): child is WireRecord => isRecord(child))
    : []);
}

function frameHasChild(frame: WireRecord, childId: string, statuses?: string[]): boolean {
  return frameChildren(frame).some((child) => child.id === childId && (statuses === undefined || statuses.includes(String(child.status))));
}

function lifecycleFrames(reader: LifecycleReader): WireRecord[] {
  return reader.frames.filter((frame) => frame.type === "subagent_state_snapshot" || frame.type === "subagent_state_update");
}

function readJsonLines(path: string): WireRecord[] {
  if (!existsSync(path)) return [];
  const content = readFileSync(path, "utf8").trim();
  if (content.length === 0) return [];
  return content.split("\n").flatMap((line) => {
    try {
      const value: unknown = JSON.parse(line);
      return isRecord(value) ? [value] : [];
    } catch {
      return [];
    }
  });
}

async function recursiveFiles(root: string): Promise<string[]> {
  if (!existsSync(root)) return [];
  const entries = await readdir(root, { withFileTypes: true });
  const files: string[] = [];
  for (const entry of entries) {
    const path = join(root, entry.name);
    if (entry.isDirectory()) files.push(...await recursiveFiles(path));
    else files.push(path);
  }
  return files;
}

function assertRedaction(frames: WireRecord[], traces: WireRecord[]): void {
  const allowedFrameChildKeys = new Set(["id", "type", "description", "status"]);
  for (const child of frames.flatMap(frameChildren)) {
    for (const key of Object.keys(child)) assert.equal(allowedFrameChildKeys.has(key), true, `unexpected lifecycle child field: ${key}`);
  }
  for (const trace of traces) {
    if (!Array.isArray(trace.subagents)) continue;
    for (const child of trace.subagents.filter((value): value is WireRecord => isRecord(value))) {
      for (const key of Object.keys(child)) assert.equal(new Set(["id", "type", "status", "description_length"]).has(key), true, `unexpected trace child field: ${key}`);
      assert.equal(Object.hasOwn(child, "description"), false);
    }
  }
  const serialized = JSON.stringify({ frames, traces });
  assert.equal(serialized.includes(PRIVATE_PROMPT), false);
  assert.equal(serialized.includes(PRIVATE_OUTPUT), false);
  assert.equal(serialized.includes(PRIVATE_ERROR), false);
  assert.equal(serialized.includes(PRIVATE_TIMESTAMP), false);
}

async function packageNativeForIsolatedChild(): Promise<{ root: string; cleanup: () => Promise<void> }> {
  const forkRoot = "/Users/wolfgang/code/pi-packages";
  const packageRoot = join(forkRoot, "packages", "pi-subagents");
  if (!existsSync(packageRoot)) throw new Error(`approved native package source is missing: ${packageRoot}`);
  const sourceCommit = execFileSync("git", ["-C", forkRoot, "rev-parse", "HEAD"], { encoding: "utf8", timeout: 5_000 }).trim();
  if (!sourceCommit.startsWith("3be57a223")) throw new Error(`native package source commit is ${sourceCommit}; expected the approved 3be57a223 fork commit`);
  const stage = await mkdtemp(join(tmpdir(), "mecha-messaging-native-pi-package-"));
  const runtime = join(stage, "runtime");
  try {
    execFileSync("pnpm", ["--filter", "@gotgenes/pi-subagents", "build:types"], { cwd: forkRoot, stdio: "ignore", timeout: 20_000 });
    execFileSync("pnpm", ["--filter", "@gotgenes/pi-subagents", "pack", "--pack-destination", stage], { cwd: forkRoot, stdio: "ignore", timeout: 20_000 });
    const artifacts = (await readdir(stage)).filter((entry) => entry.endsWith(".tgz"));
    if (artifacts.length !== 1) throw new Error(`expected one native package artifact in ${stage}, found ${artifacts.length}`);
    await mkdir(runtime, { recursive: true });
    execFileSync("npm", ["install", "--prefix", runtime, "--no-save", "--package-lock=false", "--ignore-scripts", "--omit=peer", join(stage, artifacts[0])], { stdio: "ignore", timeout: 20_000 });
    const root = join(runtime, "node_modules", "@gotgenes", "pi-subagents");
    if (!existsSync(join(root, "package.json"))) throw new Error(`isolated native package install is missing its manifest: ${root}`);
    if (readPackageIdentity(root, "@gotgenes/pi-subagents").version !== "19.2.2") throw new Error(`isolated native package version is not 19.2.2: ${root}`);
    if (isTrackedSourceCheckout(root)) throw new Error(`isolated native package unexpectedly resolves to a source checkout: ${root}`);
    if (existsSync(join(root, "node_modules", "@earendil-works", "pi-coding-agent"))) throw new Error("isolated native package has an unintended nested Pi copy");
    console.log(`native-pi-e2e package-source=${JSON.stringify({ forkRoot, sourceCommit, stage, runtime, root: realpathSync(root), artifact: artifacts[0] })}`);
    return { root: realpathSync(root), cleanup: () => rm(stage, { recursive: true, force: true }) };
  } catch (error) {
    await rm(stage, { recursive: true, force: true });
    throw new Error(`could not create the approved isolated native package install: ${String(error)}`);
  }
}

async function runNativePiLifecycleE2E(): Promise<void> {
  const pi = resolveInstalledPackage("@earendil-works/pi-coding-agent", "MECHA_PI_PACKAGE_ROOT", process.env.MECHA_PI_PACKAGE_ROOT, { requireConfiguredRoot: true });
  const native = resolveInstalledPackage("@gotgenes/pi-subagents", "MECHA_PI_SUBAGENTS_PACKAGE_ROOT", process.env.MECHA_PI_SUBAGENTS_PACKAGE_ROOT, { requireConfiguredRoot: true });
  if (pi.version !== "0.84.3") throw new Error(`installed Pi version is ${pi.version}; expected 0.84.3`);
  if (native.version !== "19.2.2") throw new Error(`installed native package version is ${native.version}; expected 19.2.2`);
  const peers = verifyPackagePair(pi, native);
  console.log(`native-pi-e2e identity=${JSON.stringify({ pi: { name: pi.name, version: pi.version, root: pi.root }, native: { name: native.name, version: native.version, root: native.root }, peers: peers.map(({ name, version, root }) => ({ name, version, root })), peerResult: "compatible" })}`);

  const originalCwd = process.cwd();
  const originalEnvironment = { ...process.env };
  const root = await mkdtemp(join(tmpdir(), "mecha-messaging-native-pi-e2e-"));
  const home = join(root, "home");
  const agentDir = join(root, "pi-agent");
  const projectDir = join(root, "project");
  const tempDir = join(root, "tmp");
  const parentSessionDir = join(root, "parent-sessions");
  const childSessionDir = join(tempDir, `pi-subagents-${process.getuid?.() ?? 0}`, projectDir.replace(/[\\/]/g, "-").replace(/^[A-Za-z]:-/, "").replace(/^-+/, ""), "tasks");
  const databasePath = join(root, "router.sqlite");
  const extensionTracePath = join(root, "extension-trace.jsonl");
  const routerTracePath = join(root, "router-trace.jsonl");
  const moduleRoot = await prepareModuleGraph(root, pi, native);
  const isolatedPiRoot = join(moduleRoot, "installed", "@earendil-works", "pi-coding-agent");
  const piPath = join(moduleRoot, "node_modules", "@earendil-works", "pi-coding-agent", "dist", "index.js");
  const aiPath = join(moduleRoot, "node_modules", "@earendil-works", "pi-ai", "dist", "index.js");
  const tuiPath = join(moduleRoot, "node_modules", "@earendil-works", "pi-tui", "dist", "index.js");
  const nativePath = join(moduleRoot, "node_modules", "@gotgenes", "pi-subagents", native.extensionEntry);
  if (!existsSync(piPath) || !existsSync(aiPath) || !existsSync(tuiPath) || !existsSync(nativePath)) throw new Error("The isolated installed Pi module graph is incomplete");
  assert.equal(realpathSync(dirname(dirname(piPath))), realpathSync(isolatedPiRoot));
  assert.equal(realpathSync(dirname(dirname(aiPath))), realpathSync(join(isolatedPiRoot, "node_modules", "@earendil-works", "pi-ai")));
  assert.equal(realpathSync(dirname(dirname(tuiPath))), realpathSync(join(isolatedPiRoot, "node_modules", "@earendil-works", "pi-tui")));
  assert.equal(realpathSync(nativePath).startsWith(realpathSync(moduleRoot)), true);
  const graphFiles = await recursiveFiles(moduleRoot);
  assert.equal(graphFiles.filter((path) => path.endsWith("/installed/@earendil-works/pi-coding-agent/package.json")).length, 1);
  assert.equal(graphFiles.filter((path) => path.endsWith("/node_modules/@earendil-works/pi-coding-agent/package.json")).length, 0);
  assert.equal(graphFiles.filter((path) => path.endsWith("/node_modules/@earendil-works/pi-ai/package.json")).length, 1);
  assert.equal(graphFiles.filter((path) => path.endsWith("/node_modules/@earendil-works/pi-tui/package.json")).length, 1);
  console.log(`native-pi-e2e module-graph=${JSON.stringify({ forcedPiRoot: process.env.MECHA_PI_PACKAGE_ROOT, forcedNativeRoot: process.env.MECHA_PI_SUBAGENTS_PACKAGE_ROOT, moduleRoot, piRoot: realpathSync(isolatedPiRoot), aiRoot: realpathSync(dirname(dirname(aiPath))), tuiRoot: realpathSync(dirname(dirname(tuiPath))), nativeRoot: realpathSync(dirname(nativePath)) })}`);

  let repository: Repository | undefined;
  let router: Router | undefined;
  let session: PiSession | undefined;
  let secondarySession: PiSession | undefined;
  const readers: LifecycleReader[] = [];
  let modelRuntimeClass;
  let originalModelRuntimeCreate;
  let releaseChild = () => {};
  let releaseScenarioGates = () => {};
  let shutdownEmitted = false;
  try {
    await mkdir(home, { recursive: true });
    await mkdir(projectDir, { recursive: true });
    await mkdir(join(projectDir, ".pi"), { recursive: true });
    await writeFile(join(projectDir, ".pi", "subagents.json"), JSON.stringify({ maxConcurrent: 1, graceTurns: 5 }));
    await mkdir(tempDir, { recursive: true });
    await mkdir(parentSessionDir, { recursive: true });
    process.chdir(projectDir);
    for (const key of Object.keys(process.env)) delete process.env[key];
    Object.assign(process.env, {
      HOME: home,
      PATH: originalEnvironment.PATH ?? "",
      PI_CODING_AGENT_DIR: agentDir,
      TMPDIR: tempDir,
      MECHA_PI_EXTENSION_TRACE_PATH: extensionTracePath,
    });

    repository = openMessageRepository({ databasePath });
    router = createAgentRouter({
      repository,
      host: "127.0.0.1",
      port: 0,
      startupProvision: false,
      timeoutSweepIntervalMs: 60_000,
      maxDeliveryAttempts: 1,
      deliveryOfferCoalesceMs: 0,
      nativeSubagentTrace: createRouterTrace({ path: routerTracePath }),
    });
    const websocketUrl = await router.listen();
    const httpUrl = routerHttpUrl(websocketUrl);
    process.env.MECHA_ROUTER_URL = httpUrl;
    console.log(`native-pi-e2e isolation=${JSON.stringify({ home, agentDir, projectDir, parentSessionDir, databasePath, moduleRoot, router: httpUrl })}`);

    const coding = await import(pathToFileURL(piPath).href);
    const ai = await import(pathToFileURL(aiPath).href);
    const nativeModule = await import(pathToFileURL(nativePath).href);
    const messagingModule = await import(pathToFileURL(join(originalCwd, "pi-extensions/mecha-messaging/index.ts")).href);
    if (typeof nativeModule.default !== "function") throw new Error("Installed native package has no extension factory");
    if (typeof messagingModule.createMechaMessagingExtension !== "function") throw new Error("Messaging package has no extension factory");
    const loadedNativeArtifact = realpathSync(nativePath);
    const loadedNativeIdentity = readPackageIdentity(native.root, "@gotgenes/pi-subagents");
    assert.equal(loadedNativeIdentity.version, "19.2.2");
    assert.equal(loadedNativeArtifact.startsWith(realpathSync(moduleRoot)), true);
    console.log(`native-pi-e2e loaded-artifact=${JSON.stringify({ packageRoot: loadedNativeIdentity.root, entry: loadedNativeArtifact, version: loadedNativeIdentity.version, factory: "@gotgenes/pi-subagents" })}`);

    const faux = ai.fauxProvider({
      provider: "native-lifecycle-faux",
      models: [{ id: "native-lifecycle-model", name: "Native Lifecycle Faux", reasoning: false, input: ["text"], cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 }, contextWindow: 128_000, maxTokens: 256 }],
      tokenSize: { min: 1, max: 1 },
      tokensPerSecond: 1_000,
    });
    const parentModel = faux.getModel();
    const parentModelRuntime = await coding.ModelRuntime.create({ modelsPath: null, refreshOnCreate: false });
    parentModelRuntime.registerNativeProvider(faux.provider);
    let releasePromise: (() => void) | undefined;
    const childGate = new Promise<void>((resolvePromise) => { releasePromise = resolvePromise; });
    releaseChild = () => releasePromise?.();
    const markerGates = new Map<string, DeferredGate>();
    releaseScenarioGates = () => { for (const gate of markerGates.values()) gate.release(); };
    const markerGate = (marker: string): DeferredGate => {
      const gate = createDeferredGate();
      markerGates.set(marker, gate);
      return gate;
    };
    let parentSessionId = "";
    const parentSessionIds = new Set<string>();
    let abortedToolCalls = 0;
    const responseStep = async (context: unknown, options: { sessionId?: string } | undefined) => {
      faux.appendResponses([responseStep]);
      if (options?.sessionId !== undefined && parentSessionIds.has(options.sessionId)) {
        return ai.fauxAssistantMessage(`parent settled ${PRIVATE_TIMESTAMP}`);
      }
      if (contextContains(context, PRIVATE_PROMPT)) {
        await childGate;
        return ai.fauxAssistantMessage(PRIVATE_OUTPUT);
      }
      if (contextContains(context, ABORTED_MARKER)) {
        abortedToolCalls += 1;
        return ai.fauxToolCall("bash", { command: "true" });
      }
      for (const [marker, gate] of markerGates) {
        if (!contextContains(context, marker)) continue;
        gate.entered += 1;
        await gate.promise;
        if (marker === FAILURE_MARKER) return ai.fauxAssistantMessage("", { stopReason: "error", errorMessage: PRIVATE_ERROR });
        return ai.fauxAssistantMessage(marker === COMPACTION_MARKER ? "compaction child complete" : PRIVATE_OUTPUT);
      }
      return ai.fauxAssistantMessage(PRIVATE_OUTPUT);
    };
    faux.setResponses([responseStep]);

    modelRuntimeClass = coding.ModelRuntime;
    originalModelRuntimeCreate = modelRuntimeClass.create;
    modelRuntimeClass.create = async function patchedModelRuntimeCreate(options) {
      const runtime = await originalModelRuntimeCreate.call(this, options);
      runtime.registerNativeProvider(faux.provider);
      return runtime;
    };

    const factoryCalls = { native: 0, messaging: 0 };
    const runtimeClients: Array<ReturnType<typeof createMailboxClient>> = [];
    const createCapturedClient = (options: Parameters<typeof createMailboxClient>[0]) => {
      const client = createMailboxClient(options);
      runtimeClients.push(client);
      return client;
    };
    const nativeChildSessionIds: string[] = [];
    const nativeChildCreationServiceStates: boolean[] = [];
    const suppressedMissedEvents: string[] = [];
    const nativeFactory = (piApi: unknown) => {
      factoryCalls.native += 1;
      nativeModule.default(piApi);
      // These are test-only event observations. Production lifecycle publication remains
      // timer-driven; this listener only creates the approved missed-event fixture.
      const events = isRecord(piApi) && isRecord(piApi.events) ? piApi.events : undefined;
      if (events !== undefined && typeof events.on === "function") {
        events.on("subagents:child:session-created", (event: unknown) => {
          if (isRecord(event) && typeof event.sessionId === "string") {
            nativeChildSessionIds.push(event.sessionId);
            const registry = Object.getOwnPropertyDescriptor(globalThis, Symbol.for("@gotgenes/pi-subagents:service-registry"))?.value;
            const services = isRecord(registry) && registry.services instanceof Map ? registry.services : undefined;
            nativeChildCreationServiceStates.push(services?.has(event.sessionId) === true);
          }
        });
        events.on("subagents:started", (event: unknown) => {
          if (isRecord(event) && event.description === MISSED_EVENT_DESCRIPTION) {
            event.id = undefined;
            suppressedMissedEvents.push(MISSED_EVENT_MARKER);
          }
        });
      }
    };
    const messagingFactory = (piApi: unknown) => {
      factoryCalls.messaging += 1;
      messagingModule.createMechaMessagingExtension(piApi, { resolveCurrentAgentId: () => AGENT_ID, createClient: createCapturedClient });
    };
    const settingsManager = coding.SettingsManager.inMemory({ compaction: { enabled: false, reserveTokens: 1, keepRecentTokens: 1 }, retry: { enabled: false } });
    const sessionManager = coding.SessionManager.create(projectDir, parentSessionDir);
    parentSessionId = sessionManager.getSessionId();
    parentSessionIds.add(parentSessionId);
    const resourceLoader = new coding.DefaultResourceLoader({ cwd: projectDir, agentDir, noSkills: true, noPromptTemplates: true, noThemes: true, noContextFiles: true, extensionFactories: [nativeFactory, messagingFactory] });
    await resourceLoader.reload();
    const loaded = resourceLoader.getExtensions();
    assert.equal(loaded.errors.length, 0, JSON.stringify(loaded.errors));
    assert.equal(loaded.extensions.filter(({ path }) => path.startsWith("<inline:")).length, 2);
    assert.equal(loaded.extensions.some(({ tools }) => tools.has("subagent")), true);
    assert.equal(loaded.extensions.some(({ tools }) => tools.has("mecha_context")), true);
    assert.equal(factoryCalls.native, 1);
    assert.equal(factoryCalls.messaging, 1);

    const sessionResult = await coding.createAgentSession({ cwd: projectDir, agentDir, modelRuntime: parentModelRuntime, model: parentModel, settingsManager, sessionManager, resourceLoader, noTools: "builtin" });
    session = sessionResult.session;
    assert.equal(sessionResult.extensionsResult.errors.length, 0, JSON.stringify(sessionResult.extensionsResult.errors));
    await session.bindExtensions({ mode: "json" });
    assert.equal(session.extensionRunner.hasHandlers("session_start"), true);
    assert.equal(session.sessionManager.getSessionFile() !== undefined, true);

    const reader = await createLifecycleReader(websocketUrl, READER_ID);
    readers.push(reader);
    const registry = Object.getOwnPropertyDescriptor(globalThis, Symbol.for("@gotgenes/pi-subagents:service-registry"))?.value;
    const services = isRecord(registry) && registry.services instanceof Map ? registry.services : undefined;
    const parentServiceValue = services?.get(parentSessionId);
    if (!isNativeService(parentServiceValue)) throw new Error(`Native service was not published for parent session ${parentSessionId}`);
    const parentService = parentServiceValue;
    const childId = parentService.spawn("general-purpose", `${PRIVATE_PROMPT} ${PRIVATE_ERROR}`, { description: CHILD_DESCRIPTION, maxTurns: 1, inheritContext: false });
    assert.equal(typeof childId, "string");
    await reader.waitFor((frame) => frameHasChild(frame, childId, ["queued", "running"]), "a timer-driven redacted running lifecycle frame");
    await waitForAsync(() => nativeChildSessionIds.length >= 1, "child-session creation before child binding");
    assert.equal(nativeChildCreationServiceStates[0], false, "child session is announced before its own service is published");
    await withTimeout(session.prompt("parent settle"), "parent prompt settlement", 8_000);
    await waitForAsync(async () => {
      const agent = await currentAgent(httpUrl);
      return agent?.activity_state === "idle" && agent.subagent_count === 1;
    }, "parent settled activity with one child");
    const settledLifecycleFrames = readJsonLines(routerTracePath).filter((trace) => trace.event === "router_lifecycle_received" && trace.snapshot_count === 1).length;
    await waitForAsync(() => readJsonLines(routerTracePath).filter((trace) => trace.event === "router_lifecycle_received" && trace.snapshot_count === 1).length >= settledLifecycleFrames + 2, "timer polling to continue after parent settlement", 7_000);

    const firstRouterVersion = repository.subagentLifecycleSnapshot().router_version;
    const firstPort = Number(new URL(websocketUrl).port);
    await reader.close();
    await router.close();
    router = undefined;
    repository.close();
    repository = openMessageRepository({ databasePath });
    router = createAgentRouter({ repository, host: "127.0.0.1", port: firstPort, startupProvision: false, timeoutSweepIntervalMs: 60_000, maxDeliveryAttempts: 1, deliveryOfferCoalesceMs: 0, nativeSubagentTrace: createRouterTrace({ path: routerTracePath }) });
    const restartedWebsocketUrl = await router.listen();
    assert.equal(Number(new URL(restartedWebsocketUrl).port), firstPort);
    assert.equal(routerHttpUrl(restartedWebsocketUrl), httpUrl);
    const restartedReader = await createLifecycleReader(restartedWebsocketUrl, `${READER_ID}-restart`);
    readers.push(restartedReader);
    await restartedReader.waitFor((frame) => frameHasChild(frame, childId, ["queued", "running"]), "durable child state after isolated router restart");
    await waitForAsync(() => readJsonLines(extensionTracePath).filter((trace) => trace.event === "client_lifecycle_registered").length >= 2, "the real Messaging client to reconnect", 8_000);
    await waitForAsync(() => repository?.subagentLifecycleSnapshot().router_version >= firstRouterVersion, "the restarted router to retain its durable lifecycle version", 8_000);

    releaseChild();
    const retainedFrame = lifecycleFrames(restartedReader).at(-1);
    assert.equal(frameHasChild(retainedFrame ?? {}, childId, ["queued", "running"]), true, "completion must not publish an immediate lifecycle removal");
    await restartedReader.waitFor((frame) => frameAgents(frame).some((agent) => agent.agent_id === AGENT_ID && Array.isArray(agent.subagents) && agent.subagents.length === 0), "timer-driven lifecycle convergence to zero");
    await waitForAsync(async () => (await currentAgent(httpUrl))?.subagent_count === 0, "router activity convergence to zero");
    await waitForAsync(() => {
      const childSessionId = nativeChildSessionIds[0];
      return childSessionId !== undefined && services?.has(childSessionId) === false;
    }, "child service cleanup after terminal completion");

    const transcriptFiles = (await recursiveFiles(childSessionDir)).filter((path) => path.endsWith(".jsonl"));
    assert.equal(transcriptFiles.length >= 1, true, "the child must write a real transcript");
    const transcript = (await Promise.all(transcriptFiles.map((path) => readFile(path, "utf8")))).join("\n");
    assert.equal(transcript.includes(PRIVATE_PROMPT), true);
    assert.equal(transcript.includes(PRIVATE_OUTPUT), true);

    await runMissedEventRepairHybrid();
    const missedGate = markerGate(MISSED_EVENT_MARKER);
    const missedId = parentService.spawn("general-purpose", MISSED_EVENT_MARKER, { description: MISSED_EVENT_DESCRIPTION, maxTurns: 1, inheritContext: false });
    await waitForAsync(() => missedGate.entered > 0, "real missed-event child to enter its provider gate");
    await waitForAsync(() => suppressedMissedEvents.length > 0, "test-only missed-event suppression");
    await restartedReader.waitFor((frame) => frameHasChild(frame, missedId, ["queued", "running"]), "polling to repair the missed started event");
    missedGate.release();
    await restartedReader.waitFor((frame) => !frameHasChild(frame, missedId), "polling to remove the missed-event child");

    const terminalStatuses = async (id: string, expectedStatus: string, description: string, service: NativeService = parentService): Promise<void> => {
      try {
        await waitForAsync(() => nativeRecord(service, id)?.status === expectedStatus, `${description} to reach ${expectedStatus}`);
      } catch (error) {
        throw new Error(`${description} status was ${String(nativeRecord(service, id)?.status)}: ${String(error)}`);
      }
      const beforeCount = lifecycleFrames(restartedReader).length;
      const before = lifecycleFrames(restartedReader).at(-1);
      assert.equal(frameHasChild(before ?? {}, id, ["queued", "running"]), true, `${description} remains visible before its next typed snapshot; recent=${JSON.stringify(lifecycleFrames(restartedReader).slice(-3))}`);
      await new Promise((resolvePromise) => setTimeout(resolvePromise, 100));
      assert.equal(lifecycleFrames(restartedReader).length, beforeCount, `${description} must not publish a terminal event`);
      await restartedReader.waitFor((frame) => !frameHasChild(frame, id), `${description} removal from the next typed snapshot`);
    };

    assert.equal(typeof parentService.registerWorkspaceProvider, "function");
    const failureWorkspaceGate = createDeferredGate();
    let failureWorkspaceEntered = false;
    const releaseFailureWorkspace = parentService.registerWorkspaceProvider?.({
      prepare: async () => {
        failureWorkspaceEntered = true;
        await failureWorkspaceGate.promise;
        throw new Error(PRIVATE_ERROR);
      },
    });
    const failureId = parentService.spawn("general-purpose", FAILURE_MARKER, { description: "Failure child", inheritContext: false });
    await waitForAsync(() => nativeRecord(parentService, failureId)?.status === "running", "failed child to enter the native run");
    await waitForAsync(() => failureWorkspaceEntered, "failed child to enter the workspace failure seam");
    await restartedReader.waitFor((frame) => frameHasChild(frame, failureId, ["queued", "running"]), "failed child running snapshot");
    failureWorkspaceGate.release();
    await terminalStatuses(failureId, "error", "failed child");
    assert.equal(JSON.stringify(nativeRecord(parentService, failureId)).includes(PRIVATE_ERROR), true, "native failure record must retain its error");
    releaseFailureWorkspace?.();

    const steerGate = markerGate(STEER_MARKER);
    const steerId = parentService.spawn("general-purpose", STEER_MARKER, { description: "Steered child", maxTurns: 1, inheritContext: false });
    await waitForAsync(() => steerGate.entered > 0, "steered child to enter its provider gate");
    await restartedReader.waitFor((frame) => frameHasChild(frame, steerId, ["queued", "running"]), "steered child running snapshot");
    assert.equal(typeof parentService.steer, "function");
    assert.equal(await parentService.steer?.(steerId, "Please wrap up this child."), true);
    steerGate.release();
    await terminalStatuses(steerId, "steered", "steered child");

    const abortGate = markerGate(ABORT_MARKER);
    const abortId = parentService.spawn("general-purpose", ABORT_MARKER, { description: "Aborted child", maxTurns: 1, inheritContext: false });
    await waitForAsync(() => abortGate.entered > 0, "aborted child to enter its provider gate");
    await restartedReader.waitFor((frame) => frameHasChild(frame, abortId, ["queued", "running"]), "aborted child running snapshot");
    assert.equal(typeof parentService.abort, "function");
    assert.equal(parentService.abort?.(abortId), true);
    abortGate.release();
    await terminalStatuses(abortId, "stopped", "running child aborted through public service control");

    const stopHolderGate = markerGate(STOP_HOLDER_MARKER);
    const stopHolderId = parentService.spawn("general-purpose", STOP_HOLDER_MARKER, { description: "Stop holder", inheritContext: false });
    await waitForAsync(() => stopHolderGate.entered > 0, "stop holder to enter its provider gate");
    const stoppedId = parentService.spawn("general-purpose", STOP_QUEUED_MARKER, { description: "Stopped queued child", maxTurns: 1, inheritContext: false });
    await restartedReader.waitFor((frame) => frameHasChild(frame, stoppedId, ["queued"]), "queued child before stop");
    assert.equal(parentService.abort?.(stoppedId), true);
    await terminalStatuses(stoppedId, "stopped", "queued child stopped through public service control");
    stopHolderGate.release();
    await waitForAsync(() => nativeRecord(parentService, stopHolderId)?.status === "completed", "stop holder completion");
    await restartedReader.waitFor((frame) => !frameHasChild(frame, stopHolderId), "stop holder removal");

    await waitForAsync(() => session.isIdle, "parent idle after native terminal variants");
    await withTimeout(session.prompt("parent remains able to settle after native terminal variants"), "parent settlement after terminal variants", 8_000);
    await waitForAsync(async () => (await currentAgent(httpUrl))?.activity_state === "idle", "parent settle after terminal variants");

    const compactionChildGate = markerGate(COMPACTION_MARKER);
    const compactionChildId = parentService.spawn("general-purpose", COMPACTION_MARKER, { description: "Compaction child", inheritContext: false });
    await waitForAsync(() => compactionChildGate.entered > 0, "compaction child to enter its provider gate");
    await restartedReader.waitFor((frame) => frameHasChild(frame, compactionChildId, ["queued", "running"]), "compaction child running snapshot");
    // A non-aborted Pi compaction also invokes the unrelated memory-capture provider and
    // cannot be made deterministic with this faux model. Exercise the real canceled hook,
    // then use the minimal lifecycle-only boundary above for the positive mask/resume pair.
    await withTimeout(session.extensionRunner.emit({
      type: "session_before_compact",
      signal: AbortSignal.abort(),
      preparation: { messagesToSummarize: [], turnPrefixMessages: [] },
    }), "real Pi canceled-compaction hook", 8_000);
    await waitForAsync(async () => (await currentAgent(httpUrl))?.activity_state !== "compacting", "canceled compaction to resume activity");
    await runCompactionBoundaryHybrid();
    await waitForAsync(() => nativeRecord(parentService, compactionChildId)?.status === "running", "compaction child remains active after resume");
    compactionChildGate.release();
    await waitForAsync(() => nativeRecord(parentService, compactionChildId)?.status === "completed", "compaction child completion");
    await restartedReader.waitFor((frame) => !frameHasChild(frame, compactionChildId), "compaction child terminal removal");

    const mailboxClient = runtimeClients[0];
    const originalSendActivity = mailboxClient.sendActivity;
    const originalSendLifecycleSnapshot = mailboxClient.sendLifecycleSnapshot;
    mailboxClient.sendActivity = () => { throw new Error("activity publication intentionally failed"); };
    mailboxClient.sendLifecycleSnapshot = () => { throw new Error("lifecycle publication intentionally failed"); };
    try {
      await waitForAsync(async () => {
        const agent = await currentAgent(httpUrl);
        return agent?.instance_id === mailboxClient.instanceId && agent.activity_state !== "compacting";
      }, "parent mailbox client reconnect before ordinary delivery");
      const mailboxId = "native-mailbox-durable-e2e";
      router.enqueueMessage({ id: mailboxId, from: "native-mailbox-sender", to: AGENT_ID, body: MAILBOX_MARKER, created_at: new Date().toISOString() });
      await waitForAsync(() => readJsonLines(extensionTracePath).some((trace) => trace.event === "extension_snapshot_publish_error"), "lifecycle publication failure evidence");
      const mailboxTranscriptPath = session.sessionManager.getSessionFile();
      try {
        await waitForAsync(async () => {
          if (mailboxTranscriptPath === undefined) return false;
          return (await readFile(mailboxTranscriptPath, "utf8")).includes(MAILBOX_MARKER);
        }, "ordinary mailbox message persisted in the parent session");
      } catch (error) {
        throw new Error(`ordinary mailbox state=${JSON.stringify(repository?.messageFor("native-mailbox-durable-e2e"))}: ${String(error)}`);
      }
      await waitForAsync(() => repository?.messageFor(mailboxId) === undefined, "ordinary mailbox acknowledgement after durable persistence");
    } finally {
      mailboxClient.sendActivity = originalSendActivity;
      mailboxClient.sendLifecycleSnapshot = originalSendLifecycleSnapshot;
    }

    const beforeSentinelChildren = parentService.listAgents?.().length ?? 0;
    const sentinelId = "native-sentinel-success-e2e";
    router.enqueueMessage({ id: sentinelId, from: "native-sentinel-sender", to: AGENT_ID, body: SENTINEL_MARKER, sentinel: { model: "native-lifecycle-model" }, created_at: new Date().toISOString() });
    await waitForAsync(() => repository?.messageFor(sentinelId) === undefined, "successful Sentinel acknowledgement");
    await waitForAsync(() => (parentService.listAgents?.().length ?? 0) === beforeSentinelChildren + 1, "one native child for one Sentinel message");
    const sentinelChild = parentService.listAgents?.().find((record) => record.id !== undefined && ![childId, missedId, failureId, steerId, abortId, stoppedId, stopHolderId, compactionChildId].includes(String(record.id)));
    assert.equal(typeof sentinelChild?.id, "string");
    const failedSentinelId = "native-sentinel-failure-e2e";
    router.enqueueMessage({ id: failedSentinelId, from: "native-sentinel-sender", to: AGENT_ID, body: SENTINEL_MARKER, sentinel: { model: "missing-native-lifecycle-model" }, created_at: new Date().toISOString() });
    await waitForAsync(() => repository?.messageFor(failedSentinelId)?.state === "dead_letter", "failed Sentinel handoff NACK");
    assert.equal((parentService.listAgents?.() ?? []).filter((record) => record.id === failedSentinelId).length, 0, "failed Sentinel handoff must not create a child");

    const replacementGate = markerGate(REPLACEMENT_MARKER);
    const replacementChildId = parentService.spawn("general-purpose", REPLACEMENT_MARKER, { description: "Replacement stale child", inheritContext: false, bypassQueue: true });
    await waitForAsync(() => ["queued", "running"].includes(String(nativeRecord(parentService, replacementChildId)?.status)), "replacement child to start through the public service");
    const replacementRecord = nativeRecord(parentService, replacementChildId);
    assert.deepEqual({ type: replacementRecord?.type, description: replacementRecord?.description }, { type: "general-purpose", description: "Replacement stale child" });
    await waitForAsync(() => replacementGate.entered > 0, "replacement child to enter its provider gate");
    await restartedReader.waitFor((frame) => frameHasChild(frame, replacementChildId, ["queued", "running"]), "stale child before new Pi instance");

    await writeFile(join(projectDir, ".pi", "subagents.json"), JSON.stringify({ maxConcurrent: 1, graceTurns: 1 }));
    assert.equal(typeof runtimeClients[0]?.pause, "function");
    runtimeClients[0]?.pause();
    const secondSettingsManager = coding.SettingsManager.inMemory({ compaction: { enabled: false, reserveTokens: 1, keepRecentTokens: 1 }, retry: { enabled: false } });
    const secondSessionManager = coding.SessionManager.create(projectDir, parentSessionDir);
    const secondParentSessionId = secondSessionManager.getSessionId();
    parentSessionIds.add(secondParentSessionId);
    const secondResourceLoader = new coding.DefaultResourceLoader({ cwd: projectDir, agentDir, noSkills: true, noPromptTemplates: true, noThemes: true, noContextFiles: true, extensionFactories: [nativeFactory, messagingFactory] });
    await secondResourceLoader.reload();
    const secondLoaded = secondResourceLoader.getExtensions();
    assert.equal(secondLoaded.errors.length, 0, JSON.stringify(secondLoaded.errors));
    assert.equal(secondLoaded.extensions.filter(({ path }) => path.startsWith("<inline:")).length, 2);
    const secondSessionResult = await coding.createAgentSession({ cwd: projectDir, agentDir, modelRuntime: await coding.ModelRuntime.create({ modelsPath: null, refreshOnCreate: false }), model: parentModel, settingsManager: secondSettingsManager, sessionManager: secondSessionManager, resourceLoader: secondResourceLoader, noTools: "builtin" });
    secondarySession = secondSessionResult.session;
    assert.equal(secondSessionResult.extensionsResult.errors.length, 0, JSON.stringify(secondSessionResult.extensionsResult.errors));
    await secondarySession.bindExtensions({ mode: "json" });
    assert.equal(runtimeClients.length >= 2, true, "new Pi instance must create a distinct mailbox client");
    const secondaryClient = runtimeClients.at(-1);
    assert.notEqual(secondaryClient?.instanceId, runtimeClients[0]?.instanceId);
    const secondaryServiceValue = services?.get(secondParentSessionId);
    if (!isNativeService(secondaryServiceValue)) throw new Error("new Pi instance did not publish its owner-scoped native service");
    const secondaryService = secondaryServiceValue;
    try {
      await waitForAsync(async () => {
        const agent = await currentAgent(httpUrl);
        return agent?.instance_id === secondaryClient?.instanceId && agent.subagent_count === 0;
      }, "new Pi instance empty replacement snapshot");
    } catch (error) {
      throw new Error(`new instance state=${JSON.stringify(await currentAgent(httpUrl))} client=${secondaryClient?.instanceId ?? "missing"} lifecycle=${JSON.stringify(readJsonLines(routerTracePath).filter((trace) => trace.event.startsWith("router_lifecycle_")).slice(-8))}: ${String(error)}`);
    }
    const replacementProducer = repository.subagentLifecycleProducerFor(AGENT_ID);
    assert.equal(replacementProducer?.parent_session_id, secondParentSessionId);
    assert.notEqual(replacementProducer?.producer_epoch, runtimeClients[0]?.producerEpoch);
    replacementGate.release();

    const abortedId = secondaryService.spawn("general-purpose", ABORTED_MARKER, { description: "Max-turn aborted child", maxTurns: 1, inheritContext: false });
    await waitForAsync(() => nativeRecord(secondaryService, abortedId)?.status === "aborted", "max-turn aborted child");
    assert.equal(abortedToolCalls > 0, true);
    const secondReader = await createLifecycleReader(restartedWebsocketUrl, `${READER_ID}-second`);
    readers.push(secondReader);
    await secondReader.waitFor((frame) => !frameHasChild(frame, replacementChildId), "replacement reader sees stale child removed");
    await waitForAsync(() => services?.has(nativeChildSessionIds.at(-1) ?? "") === false, "replacement child service cleanup");

    const extensionTraces = readJsonLines(extensionTracePath);
    const routerTraces = readJsonLines(routerTracePath);
    assert.equal(factoryCalls.native, 2);
    assert.equal(factoryCalls.messaging, 2);
    assert.equal(suppressedMissedEvents.includes(MISSED_EVENT_MARKER), true);
    assertRedaction([...reader.frames, ...restartedReader.frames, ...secondReader.frames], [...extensionTraces, ...routerTraces]);
    assert.equal(routerTraces.some((trace) => trace.event === "router_lifecycle_received" && trace.frame_type === "subagent_lifecycle_event"), false, "native start and terminal events must remain polling-only");
    assert.equal(extensionTraces.some((trace) => trace.event === "extension_snapshot_publish"), true);
    assert.equal(routerTraces.some((trace) => trace.event === "router_lifecycle_persisted"), true);

    await withTimeout(session.extensionRunner.emit({ type: "session_shutdown", reason: "quit" }), "session shutdown", 8_000);
    shutdownEmitted = true;
    session.dispose();
    await withTimeout(secondarySession.extensionRunner.emit({ type: "session_shutdown", reason: "quit" }), "secondary session shutdown", 8_000);
    secondarySession.dispose();
    secondarySession = undefined;
  } finally {
    releaseChild();
    releaseScenarioGates();
    if (secondarySession !== undefined) {
      try { await withTimeout(secondarySession.extensionRunner.emit({ type: "session_shutdown", reason: "quit" }), "secondary session shutdown cleanup", 8_000); } catch { /* Preserve the original failure. */ }
      secondarySession.dispose();
    }
    if (session !== undefined && !shutdownEmitted) {
      try { await withTimeout(session.extensionRunner.emit({ type: "session_shutdown", reason: "quit" }), "session shutdown cleanup", 8_000); } catch { /* Preserve the original failure. */ }
      session.dispose();
    }
    for (const reader of readers) {
      try { await reader.close(); } catch { /* Cleanup must continue. */ }
    }
    if (router !== undefined) {
      try { await router.close(); } catch { /* Cleanup must continue. */ }
    }
    repository?.close();
    if (modelRuntimeClass !== undefined && originalModelRuntimeCreate !== undefined) modelRuntimeClass.create = originalModelRuntimeCreate;
    process.chdir(originalCwd);
    for (const key of Object.keys(process.env)) delete process.env[key];
    Object.assign(process.env, originalEnvironment);
    await rm(root, { recursive: true, force: true });
  }
}

if (process.argv.includes(CHILD_FLAG)) {
  let exitCode = 0;
  try {
    await runNativePiLifecycleE2E();
  } catch (error) {
    console.error(error instanceof Error ? error.stack ?? error.message : String(error));
    exitCode = 1;
  }
  process.exitCode = exitCode;
  process.exit(exitCode);
} else {
  test("covers approved native Pi lifecycle gaps through an isolated router", { timeout: 90_000 }, async () => {
    const pi = resolveInstalledPackage("@earendil-works/pi-coding-agent", "MECHA_PI_PACKAGE_ROOT", process.env.MECHA_PI_PACKAGE_ROOT);
    if (pi.version !== "0.84.3") throw new Error(`installed Pi version is ${pi.version}; expected 0.84.3`);
    const nativePackage = await packageNativeForIsolatedChild();
    try {
      const loader = process.execArgv.find((argument) => argument.includes("tsx/dist/loader.mjs"));
      const preflight = process.execArgv.find((argument) => argument.includes("tsx/dist/preflight.cjs"));
      const args = [
        ...(preflight === undefined ? [] : [preflight]),
        ...(loader === undefined ? [] : [loader]),
        fileURLToPath(import.meta.url),
        CHILD_FLAG,
      ];
      const childEnvironment: NodeJS.ProcessEnv = {
        PATH: process.env.PATH ?? "",
        MECHA_PI_PACKAGE_ROOT: pi.root,
        MECHA_PI_SUBAGENTS_PACKAGE_ROOT: nativePackage.root,
      };
      const child = spawn(process.execPath, args, { cwd: process.cwd(), env: childEnvironment, stdio: ["ignore", "pipe", "pipe"] });
      let stdout = "";
      let stderr = "";
      child.stdout.on("data", (chunk) => { stdout += chunk.toString("utf8"); });
      child.stderr.on("data", (chunk) => { stderr += chunk.toString("utf8"); });
      const timer = setTimeout(() => child.kill("SIGTERM"), 55_000);
      const [code, signal] = await once(child, "exit");
      clearTimeout(timer);
      assert.equal(code, 0, `native-Pi E2E child failed (${String(signal)}):\n${stdout}\n${stderr}`);
      assert.match(stdout, /native-pi-e2e identity=/);
      process.stdout.write(stdout);
    } finally {
      await nativePackage.cleanup();
    }
  });
}
