import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { once } from "node:events";
import { existsSync, readFileSync, realpathSync } from "node:fs";
import { mkdir, mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import test from "node:test";
import WebSocket from "ws";

import { openMessageRepository } from "../harness/db.js";
import { createAgentRouter } from "../harness/router.js";
import { createMailboxClient } from "../pi-extensions/mecha-messaging/mailbox-client.ts";
import { createMechaMessagingExtension } from "../pi-extensions/mecha-messaging/index.ts";
import { createGoalExtension } from "../pi-extensions/mecha-goal/index.ts";

type RecordValue = Record<string, unknown>;
type InstalledPackage = { root: string; name: string; version: string };
type Router = ReturnType<typeof createAgentRouter>;
type Repository = ReturnType<typeof openMessageRepository>;
type ClientOptions = NonNullable<Parameters<typeof createMailboxClient>[0]>;
type Controller = { socket: WebSocket; leaseId: string; frames: RecordValue[] };

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const EXPECTED_PI_VERSION = "0.84.3";
const AGENT_ID = "model-control-installed-e2e";
const MODEL_PROVIDER = "installed-e2e-provider";
const GOAL = "verify the installed Pi model control path";

function isRecord(value: unknown): value is RecordValue {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function packageManifest(root: string, expectedName: string): InstalledPackage {
  const manifestPath = join(root, "package.json");
  if (!existsSync(manifestPath)) throw new Error(`installed package manifest is missing: ${manifestPath}`);
  const manifest: unknown = JSON.parse(readFileSync(manifestPath, "utf8"));
  if (!isRecord(manifest) || manifest.name !== expectedName || typeof manifest.version !== "string") {
    throw new Error(`installed package identity mismatch at ${root}`);
  }
  return { root, name: expectedName, version: manifest.version };
}

function isTrackedCheckout(root: string): boolean {
  try {
    execFileSync("git", ["-C", root, "ls-files", "--error-unmatch", "package.json"], { stdio: "ignore", timeout: 5_000 });
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

function installedPi(): { coding: InstalledPackage; ai: InstalledPackage } {
  const configuredRaw = process.env.MECHA_PI_PACKAGE_ROOT;
  if (configuredRaw !== undefined && configuredRaw.trim().length === 0) throw new Error("MECHA_PI_PACKAGE_ROOT is configured but empty");
  const configured = configuredRaw?.trim();
  const codingRoot = configured === undefined
    ? join(globalNpmRoot(), "@earendil-works", "pi-coding-agent")
    : resolve(configured);
  if (!existsSync(codingRoot)) throw new Error(`installed Pi runtime is missing: ${codingRoot}`);
  const coding = packageManifest(realpathSync(codingRoot), "@earendil-works/pi-coding-agent");
  if (coding.version !== EXPECTED_PI_VERSION) throw new Error(`installed Pi version is ${coding.version}; expected ${EXPECTED_PI_VERSION}`);
  if (isTrackedCheckout(coding.root)) throw new Error(`installed Pi resolves to a source checkout: ${coding.root}`);
  const aiCandidates = [
    join(coding.root, "node_modules", "@earendil-works", "pi-ai"),
    join(dirname(dirname(coding.root)), "@earendil-works", "pi-ai"),
    join(globalNpmRoot(), "@earendil-works", "pi-ai"),
  ];
  const aiRoot = aiCandidates.find((candidate) => existsSync(join(candidate, "package.json")));
  if (aiRoot === undefined) throw new Error(`installed Pi AI peer is missing beside ${coding.root}`);
  const ai = packageManifest(realpathSync(aiRoot), "@earendil-works/pi-ai");
  if (ai.version !== EXPECTED_PI_VERSION) throw new Error(`installed Pi AI version is ${ai.version}; expected ${EXPECTED_PI_VERSION}`);
  if (isTrackedCheckout(ai.root)) throw new Error(`installed Pi AI resolves to a source checkout: ${ai.root}`);
  if (!existsSync(join(coding.root, "dist", "index.js")) || !existsSync(join(ai.root, "dist", "index.js"))) {
    throw new Error("installed Pi runtime entrypoints are incomplete");
  }
  return { coding, ai };
}

function routerHttpUrl(websocketUrl: string): string {
  const url = new URL(websocketUrl);
  url.protocol = "http:";
  url.pathname = "";
  return url.toString().replace(/\/$/, "");
}

async function waitFor<T>(read: () => T | undefined | Promise<T | undefined>, description: string, timeoutMs = 10_000): Promise<T> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const value = await read();
    if (value !== undefined) return value;
    await new Promise((resolvePromise) => setTimeout(resolvePromise, 25));
  }
  throw new Error(`timed out waiting for ${description}`);
}

async function agentSnapshot(httpUrl: string): Promise<RecordValue | undefined> {
  const response = await fetch(`${httpUrl}/v1/agents?models=include`);
  if (!response.ok) throw new Error(`isolated router agent query failed: ${response.status}`);
  const body: unknown = await response.json();
  if (!isRecord(body) || !Array.isArray(body.agents)) throw new Error("isolated router returned malformed agent snapshot");
  return body.agents.filter(isRecord).find((agent) => agent.agent_id === AGENT_ID);
}

function assertUnder(root: string, path: string, name: string): void {
  assert.equal(realpathSync(dirname(path)).startsWith(realpathSync(root)), true, `${name} escaped the isolated root`);
}

async function openController(websocketUrl: string): Promise<Controller> {
  const socket = new WebSocket(websocketUrl);
  const frames: RecordValue[] = [];
  socket.on("message", (payload) => {
    try {
      const frame: unknown = JSON.parse(payload.toString("utf8"));
      if (isRecord(frame)) frames.push(frame);
    } catch {
      // The awaited protocol frame will fail the test if the router sends malformed data.
    }
  });
  await Promise.race([
    once(socket, "open").then(() => undefined),
    once(socket, "error").then(([error]) => { throw error; }),
  ]);
  socket.send(JSON.stringify({ version: "v1", frame_id: "controller-register", type: "register", agent_id: "model-control-controller", instance_id: "controller-instance", agent_role: "user_interaction" }));
  const registered = await waitFor(() => frames.find((frame) => frame.type === "registered"), "controller registration");
  if (typeof registered.lease_id !== "string") throw new Error("controller registration did not return a lease");
  return { socket, leaseId: registered.lease_id, frames };
}

async function sendControl(controller: Controller, websocketUrl: string, frame: RecordValue, resultType: string, controlId: string): Promise<RecordValue> {
  controller.socket.send(JSON.stringify({ version: "v1", frame_id: `${controlId}-frame`, agent_id: "model-control-controller", instance_id: "controller-instance", lease_id: controller.leaseId, ...frame }));
  return waitFor(() => controller.frames.find((candidate) => candidate.type === resultType && candidate.control_id === controlId), `${resultType} ${controlId}`);
}

test("installed Pi exercises the isolated model and Goal control projection", { timeout: 90_000 }, async () => {
  const installed = installedPi();
  const originalCwd = process.cwd();
  const originalEnvironment = { ...process.env };
  const runtimeRoot = await mkdtemp(join(tmpdir(), "mecha-model-control-native-pi-e2e-"));
  const home = join(runtimeRoot, "home");
  const agentDir = join(runtimeRoot, "agent");
  const projectDir = join(runtimeRoot, "project");
  const sessionDir = join(runtimeRoot, "sessions");
  const dataDir = join(runtimeRoot, "data");
  const routerDir = join(runtimeRoot, "router");
  const databasePath = join(routerDir, "mailbox.sqlite");
  const modelDefinitions = Array.from({ length: 1_005 }, (_, index) => ({
    id: `model-${String(index).padStart(4, "0")}`,
    name: `Installed E2E model ${index}`,
    reasoning: false,
    input: ["text"],
    cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
    contextWindow: 128_000,
    maxTokens: 256,
  }));
  const targetModel = modelDefinitions[1];
  const throwModel = modelDefinitions[2];
  const rejectModel = modelDefinitions[3];
  if (targetModel === undefined || throwModel === undefined || rejectModel === undefined) throw new Error("model fixture is incomplete");
  let repository: Repository | undefined;
  let router: Router | undefined;
  let session: { dispose: () => void; bindExtensions: (options: { mode: "json" }) => Promise<void>; prompt: (prompt: string) => Promise<void>; isIdle: boolean; extensionRunner: { hasHandlers: (name: string) => boolean }; sessionManager: { getSessionFile: () => string | undefined } } | undefined;
  let client: ReturnType<typeof createMailboxClient> | undefined;
  let clientOptions: ClientOptions | undefined;
  const modelTraces: RecordValue[] = [];
  const goalTraces: RecordValue[] = [];
  const connectionStates: RecordValue[] = [];
  const setModelCalls: RecordValue[] = [];
  let fakeMode: "accept" | "throw-after-mutation" | "reject" = "accept";
  try {
    await Promise.all([mkdir(home, { recursive: true }), mkdir(agentDir, { recursive: true }), mkdir(projectDir, { recursive: true }), mkdir(sessionDir, { recursive: true }), mkdir(dataDir, { recursive: true }), mkdir(routerDir, { recursive: true })]);
    process.chdir(projectDir);
    for (const key of Object.keys(process.env)) delete process.env[key];
    Object.assign(process.env, {
      HOME: home,
      PATH: originalEnvironment.PATH ?? "",
      PI_CODING_AGENT_DIR: agentDir,
      TMPDIR: dataDir,
      MECHA_PI_SESSION_ROOT: sessionDir,
      MECHA_PI_DATA_ROOT: dataDir,
      MECHA_PI_ROUTER_ROOT: routerDir,
    });
    repository = openMessageRepository({ databasePath });
    router = createAgentRouter({ repository, host: "127.0.0.1", port: 0, startupProvision: false, timeoutSweepIntervalMs: 60_000, deliveryOfferCoalesceMs: 0 });
    const websocketUrl = await router.listen();
    const httpUrl = routerHttpUrl(websocketUrl);
    const routerPort = Number(new URL(websocketUrl).port);
    assert.ok(routerPort > 0 && routerPort !== 8765, `isolated router used forbidden port ${routerPort}`);
    process.env.MECHA_ROUTER_URL = httpUrl;
    assert.notEqual(httpUrl, "http://127.0.0.1:8765");
    for (const [path, name] of [[home, "HOME"], [sessionDir, "session"], [dataDir, "data"], [routerDir, "router"], [databasePath, "database"]] as const) assertUnder(runtimeRoot, path, name);

    const coding = await import(pathToFileURL(join(installed.coding.root, "dist", "index.js")).href);
    const ai = await import(pathToFileURL(join(installed.ai.root, "dist", "index.js")).href);
    assert.equal(coding.VERSION, EXPECTED_PI_VERSION);
    console.log(`installed-pi-model-control-e2e identity=${JSON.stringify({ pi: installed.coding, ai: installed.ai, runtime: "installed-dist", extension: "checkout-inline-factory", router: httpUrl })}`);

    const faux = ai.fauxProvider({ provider: MODEL_PROVIDER, models: modelDefinitions, tokenSize: { min: 1, max: 1 }, tokensPerSecond: 1_000 });
    faux.setResponses([async () => ai.fauxAssistantMessage("installed model-control E2E response")]);
    const modelRuntime = await coding.ModelRuntime.create({ modelsPath: null, refreshOnCreate: false });
    modelRuntime.registerNativeProvider(faux.provider);
    const settingsManager = coding.SettingsManager.inMemory({ compaction: { enabled: false, reserveTokens: 1, keepRecentTokens: 1 }, retry: { enabled: false } });
    const sessionManager = coding.SessionManager.create(projectDir, sessionDir);
    const resourceLoader = new coding.DefaultResourceLoader({
      cwd: projectDir,
      agentDir,
      settingsManager,
      noSkills: true,
      noPromptTemplates: true,
      noThemes: true,
      noContextFiles: true,
      extensionFactories: [
        (piApi) => createGoalExtension(piApi),
        (piApi) => {
          const nativeSetModel = piApi.setModel;
          Object.defineProperty(piApi, "setModel", {
            configurable: true,
            value: async (model: unknown) => {
              const selected = isRecord(model) ? model : {};
              setModelCalls.push({ provider: selected.provider, id: selected.id });
              if (fakeMode === "reject") return false;
              if (nativeSetModel === undefined) throw new Error("installed Pi setModel boundary is unavailable");
              await nativeSetModel(model);
              if (fakeMode === "throw-after-mutation") throw new Error("deterministic mutation-after-throw boundary");
              return true;
            },
          });
          createMechaMessagingExtension(piApi, {
            resolveCurrentAgentId: () => AGENT_ID,
            createClient: (options: ClientOptions) => {
              const wrapped: ClientOptions = {
                ...options,
                webSocketFactory: (url) => new WebSocket(url),
                onConnectionState: (state) => { connectionStates.push(state); options.onConnectionState?.(state); },
                onModelControlTrace: (value) => { modelTraces.push(value); options.onModelControlTrace?.(value); },
                onGoalControlTrace: (value) => { goalTraces.push(value); options.onGoalControlTrace?.(value); },
              };
              clientOptions = wrapped;
              client = createMailboxClient(wrapped);
              return client;
            },
            createCoordinator: () => ({ onEnvelope() {}, onMessageEnd() {}, close() {} }),
            createSettledScheduler: () => ({ onParentActivity() {}, onParentSettled() {}, onChildStarted() {}, onChildDisposed() {}, dispose() {}, activeChildCount: () => 0 }),
            nativeSubagentTrace: {
              enabled: true,
              record: (event: string, fields: RecordValue = {}) => { modelTraces.push({ event, ...fields }); return true; },
            },
          });
        },
      ],
    });
    await resourceLoader.reload();
    const loaded = resourceLoader.getExtensions();
    assert.equal(loaded.errors.length, 0, JSON.stringify(loaded.errors));
    assert.equal(loaded.extensions.filter(({ path }: { path: string }) => path.startsWith("<inline:" )).length, 2);
    const sessionResult = await coding.createAgentSession({ cwd: projectDir, agentDir, modelRuntime, model: faux.getModel(), settingsManager, sessionManager, resourceLoader, noTools: "all" });
    session = sessionResult.session;
    assert.equal(sessionResult.extensionsResult.errors.length, 0, JSON.stringify(sessionResult.extensionsResult.errors));
    await session.bindExtensions({ mode: "json" });
    assert.equal(session.extensionRunner.hasHandlers("session_start"), true);
    assert.equal(session.sessionManager.getSessionFile()?.startsWith(sessionDir), true);
    await waitFor(() => connectionStates.find((state) => state.state === "connected"), "installed Messaging client connection");
    assert.ok(client);

    const initialAgent = await waitFor(async () => {
      const agent = await agentSnapshot(httpUrl);
      return agent?.model_catalog_total_count === modelDefinitions.length ? agent : undefined;
    }, "isolated router agent registration and model catalog");
    assert.equal(initialAgent?.agent_id, AGENT_ID);
    const initialCatalog = initialAgent?.available_models;
    assert.ok(Array.isArray(initialCatalog));
    assert.ok(initialCatalog.length > 0 && initialCatalog.length <= 1_000, "router catalog must be bounded to 1,000 models");
    assert.equal(initialAgent?.model_catalog_total_count, 1_005);
    assert.equal(initialAgent?.model_catalog_truncated, true);
    assert.deepEqual(initialAgent?.current_model, { provider: MODEL_PROVIDER, id: modelDefinitions[0]?.id, name: modelDefinitions[0]?.name });

    assert.ok(clientOptions);
    // These callbacks belong to the real installed-Pi extension's real Mailbox client. The
    // fixture drives the external control boundary with deterministic model inputs.
    await session.prompt("exercise installed Pi model control");
    assert.equal(session.isIdle, true);
    assert.equal(faux.state.callCount, 1);

    const successful = await clientOptions.onModelControl?.({
      controlId: "control-success", sourceAgentId: "control-source", action: "set_model",
      model: { provider: MODEL_PROVIDER, id: targetModel.id },
    });
    assert.equal(successful?.accepted, true);
    assert.deepEqual(successful?.currentModel, { provider: MODEL_PROVIDER, id: targetModel.id, name: targetModel.name, contextWindow: targetModel.contextWindow, maxTokens: targetModel.maxTokens });
    const successEntered = modelTraces.findIndex((trace) => trace.event === "extension_model_control_entered" && trace.control_id === "control-success");
    const successRegistry = modelTraces.findIndex((trace, index) => index > successEntered && trace.event === "extension_model_registry_read" && trace.requested_model_id === targetModel.id);
    const successSet = modelTraces.findIndex((trace, index) => index > successRegistry && trace.event === "extension_model_set_result" && trace.requested_model_id === targetModel.id);
    const successCompleted = modelTraces.findIndex((trace, index) => index > successSet && trace.event === "extension_model_control_completed" && trace.control_id === "control-success");
    assert.ok(successEntered >= 0 && successEntered < successRegistry && successRegistry < successSet && successSet < successCompleted, JSON.stringify({ successful, traces: modelTraces.slice(successEntered, successCompleted + 1) }));

    fakeMode = "throw-after-mutation";
    const mutationThrow = await clientOptions.onModelControl?.({
      controlId: "control-throw", sourceAgentId: "control-source", action: "set_model",
      model: { provider: MODEL_PROVIDER, id: throwModel.id },
    });
    assert.equal(mutationThrow?.accepted, true);
    assert.deepEqual(mutationThrow?.currentModel, { provider: MODEL_PROVIDER, id: throwModel.id, name: throwModel.name, contextWindow: throwModel.contextWindow, maxTokens: throwModel.maxTokens });

    fakeMode = "reject";
    const rejected = await clientOptions.onModelControl?.({
      controlId: "control-rejected", sourceAgentId: "control-source", action: "set_model",
      model: { provider: MODEL_PROVIDER, id: rejectModel.id },
    });
    assert.equal(rejected?.accepted, false);
    assert.equal(rejected?.errorCode, "model_switch_rejected");
    assert.deepEqual(rejected?.currentModel, { provider: MODEL_PROVIDER, id: throwModel.id, name: throwModel.name, contextWindow: throwModel.contextWindow, maxTokens: throwModel.maxTokens });
    assert.deepEqual(setModelCalls.map(({ id }) => id), [targetModel.id, throwModel.id, rejectModel.id]);

    fakeMode = "accept";
    const goalResult = await clientOptions.onGoalControl?.({
      controlId: "goal-control", sourceAgentId: "goal-controller", targetAgentId: AGENT_ID,
      targetInstanceId: "goal-instance", revision: 1, action: "set", goal: GOAL,
      settings: {
        goalLockEnabled: false, snoozeEnabled: true, snoozeDurationMs: 60_000,
        snoozeTurnLimit: null, snoozeBudgetCapMs: null, snoozeRefillYieldMs: 30_000,
        snoozeRefillElapsedMs: 60_000,
      },
    });
    assert.equal(goalResult?.accepted, true);
    assert.equal(isRecord(goalResult?.goalState) && goalResult.goalState.goal, GOAL);
    assert.equal(isRecord(goalResult?.goalState) && goalResult.goalState.enabled, true);
    // Setting a goal schedules a real Pi self-wake. Turn it off in the same macrotask so the
    // isolated test does not leave an asynchronous wake racing session disposal.
    const goalOff = await clientOptions.onGoalControl?.({
      controlId: "goal-control-off", sourceAgentId: "goal-controller", targetAgentId: AGENT_ID,
      targetInstanceId: "goal-instance", revision: 2, action: "off", goal: "",
      settings: {
        goalLockEnabled: false, snoozeEnabled: true, snoozeDurationMs: 60_000,
        snoozeTurnLimit: null, snoozeBudgetCapMs: null, snoozeRefillYieldMs: 30_000,
        snoozeRefillElapsedMs: 60_000,
      },
    });
    assert.equal(goalOff?.accepted, true);
    assert.equal(isRecord(goalOff?.goalState) && goalOff.goalState.enabled, false);
    assert.equal(connectionStates.some((state) => state.state === "connected"), true);
  } finally {
    try { session?.dispose(); } catch { /* Pi cleanup is best effort after assertion failures. */ }
    try { client?.close(); } catch { /* Mailbox cleanup is best effort after assertion failures. */ }
    try { await router?.close(); } catch { /* Router cleanup is best effort after assertion failures. */ }
    try { repository?.close(); } catch { /* SQLite cleanup is best effort after assertion failures. */ }
    process.chdir(originalCwd);
    for (const key of Object.keys(process.env)) delete process.env[key];
    Object.assign(process.env, originalEnvironment);
    await rm(runtimeRoot, { recursive: true, force: true });
  }
});
