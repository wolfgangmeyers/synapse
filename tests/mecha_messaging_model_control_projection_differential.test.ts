import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { execFile, spawn } from "node:child_process";
import { cp, mkdir, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";
import { promisify } from "node:util";

const execFileAsync = promisify(execFile);
const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const TSX = resolve(ROOT, "pi-extensions/mecha-messaging/node_modules/.bin/tsx");
const BASELINE_COMMIT = "94c99f05e957698860c59704e3acabb9647e08a5";
const BASELINE_INDEX_SHA256 = "202d3ab4d13bfc7642748e8ae606db9e9668bc891caac602e4277b7fc669372f";
const BASELINE_INDEX_BYTES = 74_034;

// The fixture fixes all model, session, data, router, clock, and response values, so no
// generated value is allowlisted. Goal producer epochs no longer exist in the Messaging package.
const DIFFERENTIAL_ALLOWLIST = Object.freeze([] as const);

const RUNNER_SOURCE = String.raw`
import { mkdir, writeFile } from "node:fs/promises";
import { rmSync } from "node:fs";
import { join } from "node:path";
import { pathToFileURL } from "node:url";

const side = process.argv[2];
const sourceRoot = process.argv[3];
const baselineRoot = process.argv[4];
if (side !== "baseline" && side !== "current") throw new Error("unknown differential side");
if (typeof sourceRoot !== "string" || typeof baselineRoot !== "string") throw new Error("roots are required");

const runtimeRoot = await (await import("node:fs/promises")).mkdtemp(join((await import("node:os")).tmpdir(), "mecha-model-control-differential-child-"));
process.on("exit", () => rmSync(runtimeRoot, { recursive: true, force: true }));
const home = join(runtimeRoot, "home");
const sessionRoot = join(runtimeRoot, "session");
const dataRoot = join(runtimeRoot, "data");
const routerRoot = join(runtimeRoot, "router");
await Promise.all([mkdir(home, { recursive: true }), mkdir(sessionRoot, { recursive: true }), mkdir(dataRoot, { recursive: true }), mkdir(routerRoot, { recursive: true })]);
await writeFile(join(sessionRoot, "parent-session.jsonl"), "fixture session\n", { mode: 0o600 });
process.env.HOME = home;
process.env.MECHA_ROUTER_URL = "http://127.0.0.1:43199";
process.env.MECHA_PI_SESSION_ROOT = sessionRoot;
process.env.MECHA_PI_DATA_ROOT = dataRoot;
process.env.MECHA_PI_ROUTER_ROOT = routerRoot;
process.chdir(dataRoot);

const modulePath = side === "baseline"
  ? join(baselineRoot, "pi-extensions", "mecha-messaging", "index.ts")
  : join(sourceRoot, "pi-extensions", "mecha-messaging", "index.ts");
const implementation = await import(pathToFileURL(modulePath).href);
if (typeof implementation.createMechaMessagingExtension !== "function") throw new Error("messaging extension factory is unavailable");

const observations = [];
const modelCurrent = { provider: "fixture-provider", id: "current", name: "Current" };
const modelTarget = { provider: "fixture-provider", id: "target", name: "Target", contextWindow: 4096, maxTokens: 256 };
const modelOtherProvider = { provider: "other-provider", id: "target", name: "Other target" };
const modelThrow = { provider: "fixture-provider", id: "throw-target", name: "Throw target" };
const modelReject = { provider: "fixture-provider", id: "reject-target", name: "Reject target" };
const catalog = [modelCurrent, null, "malformed", modelOtherProvider, modelTarget, modelThrow, modelReject];
let context;
let clientOptions;
let currentClient;
const handlers = new Map();
const eventHandlers = new Map();
const activityCalls = [];
const setModelCalls = [];
const traces = [];

function clone(value) {
  if (value === undefined) return undefined;
  return JSON.parse(JSON.stringify(value));
}
function observe(kind, value) { observations.push({ kind, value: clone(value) }); }
function add(map, name, handler) {
  const bucket = map.get(name) ?? [];
  bucket.push(handler);
  map.set(name, bucket);
}
function createContext() {
  return {
    sessionManager: {
      getSessionId: () => "parent-session",
      getBranch: () => [],
      isPersisted: () => false,
      getSessionFile: () => join(sessionRoot, "parent-session.jsonl"),
    },
    model: modelCurrent,
    modelRegistry: { getAvailable: () => catalog },
    ui: { notify() {}, setStatus() {} },
  };
}
function createPi() {
  return {
    events: {
      on(name, handler) { add(eventHandlers, name, handler); },
      emit(name, value) {
        observe("event", { name, request: value });
        for (const handler of eventHandlers.get(name) ?? []) handler(value);
      },
    },
    on(name, handler) { add(handlers, name, handler); },
    registerTool() {},
    registerCommand() {},
    appendEntry() {},
    sendMessage() {},
    exec: async () => ({ code: 0, stderr: "" }),
    setModel(model) {
      setModelCalls.push(clone(model));
      context.model = model;
      if (model.id === "throw-target") throw new Error("mutation then throw");
      return model.id !== "reject-target";
    },
  };
}
const pi = createPi();
context = createContext();

function createClient(options) {
  clientOptions = options;
  currentClient = {
    connect: async () => {},
    close() {},
    sendReceipt() {},
    sendReply: async () => ({ id: "fixture-message", state: "queued" }),
    sendActivity(state, payload) { activityCalls.push({ state, payload: clone(payload) }); },
  };
  return currentClient;
}

implementation.createMechaMessagingExtension(pi, {
  resolveCurrentAgentId: () => "fixture-agent",
  createClient,
  createCoordinator: () => ({ onEnvelope() {}, onMessageEnd() {}, close() {} }),
  createSettledScheduler: () => ({ onParentActivity() {}, onParentSettled() {}, onChildStarted() {}, onChildDisposed() {}, dispose() {}, activeChildCount: () => 0 }),
  setTimeoutImpl: () => ({ unref() {} }),
  clearTimeoutImpl: () => {},
  setIntervalImpl: () => ({ unref() {} }),
  clearIntervalImpl: () => {},
  nowImpl: () => 1_700_000_000_000,
  nativeSubagentTrace: { enabled: false, record(event, fields) { traces.push({ event, fields }); return false; } },
});

const sessionStart = handlers.get("session_start")?.[0];
if (typeof sessionStart !== "function") throw new Error("session_start handler is unavailable");
await sessionStart({}, context);
if (clientOptions === undefined || currentClient === undefined) throw new Error("client was not initialized");

observe("catalog", { models: clientOptions.getModelCatalog(), current: clientOptions.getCurrentModel() });
context.model = modelCurrent;
observe("control", await clientOptions.onModelControl({ controlId: "success", sourceAgentId: "controller", action: "set_model", model: modelTarget }));
observe("refresh", activityCalls);
context.model = modelCurrent;
observe("control", await clientOptions.onModelControl({ controlId: "mutation-throw", sourceAgentId: "controller", action: "set_model", model: { provider: "fixture-provider", id: "throw-target" } }));
observe("refresh", activityCalls);
context.model = modelCurrent;
observe("control", await clientOptions.onModelControl({ controlId: "rejected", sourceAgentId: "controller", action: "set_model", model: { provider: "fixture-provider", id: "reject-target" } }));
observe("refresh", activityCalls);
observe("final", { setModelCalls, activityCalls, traces });
process.stdout.write(JSON.stringify(observations) + "\n");
`;

type Observation = { kind: string; value: unknown };

function compareExact(left: unknown, right: unknown, path = ""): void {
  if (DIFFERENTIAL_ALLOWLIST.some(({ pathSuffix }) => path.endsWith(pathSuffix))) {
    assert.equal(typeof left, "string", `allowlisted value is not a string at ${path}`);
    assert.equal(typeof right, "string", `allowlisted value is not a string at ${path}`);
    return;
  }
  if (Object.is(left, right)) return;
  if (Array.isArray(left) || Array.isArray(right)) {
    assert.equal(Array.isArray(left), true, `array mismatch at ${path}`);
    assert.equal(Array.isArray(right), true, `array mismatch at ${path}`);
    assert.equal((left as unknown[]).length, (right as unknown[]).length, `array length mismatch at ${path}`);
    for (let index = 0; index < (left as unknown[]).length; index += 1) compareExact((left as unknown[])[index], (right as unknown[])[index], `${path}[${index}]`);
    return;
  }
  if (left !== null && right !== null && typeof left === "object" && typeof right === "object") {
    const leftRecord = left as Record<string, unknown>;
    const rightRecord = right as Record<string, unknown>;
    assert.deepEqual(Object.keys(leftRecord).sort(), Object.keys(rightRecord).sort(), `object keys mismatch at ${path}`);
    for (const key of Object.keys(leftRecord).sort()) compareExact(leftRecord[key], rightRecord[key], `${path}.${key}`);
    return;
  }
  assert.fail(`differential mismatch at ${path}: ${JSON.stringify(left)} != ${JSON.stringify(right)}`);
}

async function archiveBaseline(destination: string): Promise<void> {
  await new Promise<void>((resolveArchive, reject) => {
    const child = spawn("git", ["archive", "--format=tar", BASELINE_COMMIT, "pi-extensions/mecha-messaging"], { cwd: ROOT, stdio: ["ignore", "pipe", "pipe"] });
    const chunks: Buffer[] = [];
    const errors: Buffer[] = [];
    child.stdout.on("data", (chunk: Buffer) => chunks.push(Buffer.from(chunk)));
    child.stderr.on("data", (chunk: Buffer) => errors.push(Buffer.from(chunk)));
    child.on("error", reject);
    child.on("close", (code) => code === 0
      ? writeFile(destination, Buffer.concat(chunks)).then(resolveArchive, reject)
      : reject(new Error(`git archive failed (${code}): ${Buffer.concat(errors).toString("utf8")}`)));
  });
}

async function copyPackageTree(source: string, destination: string): Promise<void> {
  await mkdir(destination, { recursive: true });
  await cp(source, destination, { recursive: true, filter: (sourcePath) => !sourcePath.includes("/node_modules/") });
}

async function materialize(runDirectory: string): Promise<{ baselineRoot: string; currentRoot: string }> {
  const baselineRoot = join(runDirectory, "baseline-root");
  const currentRoot = join(runDirectory, "current-root");
  const archivePath = join(runDirectory, "baseline.tar");
  await mkdir(baselineRoot, { recursive: true });
  await archiveBaseline(archivePath);
  await execFileAsync("tar", ["-xf", archivePath, "-C", baselineRoot]);
  await copyPackageTree(join(ROOT, "pi-extensions", "mecha-messaging"), join(currentRoot, "pi-extensions", "mecha-messaging"));
  const baselineIndex = await readFile(join(baselineRoot, "pi-extensions", "mecha-messaging", "index.ts"));
  assert.equal(createHash("sha256").update(baselineIndex).digest("hex"), BASELINE_INDEX_SHA256);
  assert.equal(baselineIndex.byteLength, BASELINE_INDEX_BYTES);
  return { baselineRoot, currentRoot };
}

async function runChild(runnerPath: string, side: "baseline" | "current", roots: { baselineRoot: string; currentRoot: string }): Promise<Observation[]> {
  const { stdout } = await execFileAsync(TSX, [runnerPath, side, roots.currentRoot, roots.baselineRoot], { cwd: ROOT, maxBuffer: 10 * 1024 * 1024 });
  const line = stdout.trim().split("\n").at(-1);
  assert.ok(line, `${side} child returned no fixture output`);
  const observations = JSON.parse(line) as unknown;
  assert.ok(Array.isArray(observations), `${side} child output is not an observation array`);
  return observations as Observation[];
}

test("model projections match the exact extraction parent", { timeout: 60_000 }, async () => {
  assert.deepEqual(DIFFERENTIAL_ALLOWLIST.map(({ name, pathSuffix }) => ({ name, pathSuffix })), []);
  const runDirectory = await mkdtemp(join(tmpdir(), "mecha-model-control-differential-"));
  const runnerPath = join(runDirectory, "runner.mjs");
  try {
    await writeFile(runnerPath, RUNNER_SOURCE, { mode: 0o600 });
    const roots = await materialize(runDirectory);
    const baseline = await runChild(runnerPath, "baseline", roots);
    const current = await runChild(runnerPath, "current", roots);
    compareExact(baseline, current);
    assert.equal(baseline.some(({ kind }) => kind === "catalog"), true);
    assert.equal(baseline.filter(({ kind }) => kind === "control").length, 3);
  } finally {
    await rm(runDirectory, { recursive: true, force: true });
  }
});
