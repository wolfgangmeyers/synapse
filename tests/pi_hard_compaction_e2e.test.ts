import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { existsSync, readFileSync } from "node:fs";
import { join } from "node:path";
import test from "node:test";
import { pathToFileURL } from "node:url";

import { createMechaMessagingExtension } from "../pi-extensions/mecha-messaging/index.ts";
import { createMailboxClient } from "../pi-extensions/mecha-messaging/mailbox-client.js";

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

async function waitFor(check, description, attempts = 100) {
  for (let attempt = 0; attempt < attempts; attempt += 1) {
    if (check()) return;
    await sleep(10);
  }
  assert.ok(check(), description);
}

function packageRootFromEnvironment(): { root: string | undefined; configured: boolean } {
  const configuredRoot = process.env.MECHA_PI_PACKAGE_ROOT?.trim();
  if (configuredRoot !== undefined && configuredRoot.length > 0) {
    return { root: configuredRoot, configured: true };
  }
  try {
    const npmRoot = execFileSync("npm", ["root", "-g"], { encoding: "utf8", timeout: 5_000 }).trim();
    if (npmRoot.length === 0) return { root: undefined, configured: false };
    const root = join(npmRoot, "@earendil-works", "pi-coding-agent");
    return { root: existsSync(root) ? root : undefined, configured: false };
  } catch {
    return { root: undefined, configured: false };
  }
}

function readPackageMetadata(root: string, expectedName: string): { name?: string; version?: string } {
  const manifestPath = join(root, "package.json");
  if (!existsSync(manifestPath)) throw new Error(`installed Pi package manifest is missing: ${manifestPath}`);
  let manifest: unknown;
  try {
    manifest = JSON.parse(readFileSync(manifestPath, "utf8")) as unknown;
  } catch (error) {
    throw new Error(`installed Pi package manifest is unreadable: ${manifestPath}`, { cause: error });
  }
  if (manifest === null || typeof manifest !== "object" || Array.isArray(manifest)) {
    throw new Error(`installed Pi package manifest is invalid: ${manifestPath}`);
  }
  const metadata = manifest as { name?: unknown; version?: unknown };
  if (metadata.name !== expectedName || typeof metadata.version !== "string" || metadata.version.length === 0) {
    throw new Error(`installed Pi package identity mismatch at ${root}: expected ${expectedName}`);
  }
  return { name: metadata.name, version: metadata.version };
}

async function installedPiRuntime(t) {
  const { root, configured } = packageRootFromEnvironment();
  if (root === undefined) {
    t.skip("requires an installed Pi runtime; set MECHA_PI_PACKAGE_ROOT to run this integration test");
    return undefined;
  }
  const codingPath = join(root, "dist", "index.js");
  const aiRoot = join(root, "node_modules", "@earendil-works", "pi-ai");
  const aiPath = join(aiRoot, "dist", "index.js");
  if (!existsSync(codingPath) || !existsSync(aiPath)) {
    throw new Error(`${configured ? "configured" : "discovered"} Pi package root is incomplete: ${root}`);
  }
  const codingPackage = readPackageMetadata(root, "@earendil-works/pi-coding-agent");
  const aiPackage = readPackageMetadata(aiRoot, "@earendil-works/pi-ai");
  if (codingPackage.version !== aiPackage.version) {
    throw new Error(`installed Pi package versions do not match: ${codingPackage.version} and ${aiPackage.version}`);
  }
  return {
    coding: await import(pathToFileURL(codingPath).href),
    ai: await import(pathToFileURL(aiPath).href),
  };
}

function restoreCompactionPolicy(t, policy) {
  const previous = process.env.MECHA_PI_COMPACTION_POLICY;
  process.env.MECHA_PI_COMPACTION_POLICY = JSON.stringify(policy);
  t.after(() => {
    if (previous === undefined) delete process.env.MECHA_PI_COMPACTION_POLICY;
    else process.env.MECHA_PI_COMPACTION_POLICY = previous;
  });
}

function mailboxExtension(pi) {
  createMechaMessagingExtension(pi, {
    resolveCurrentAgentId: () => "hard-compaction-e2e",
    createClient: () => ({
      connect: async () => {},
      close() {},
      sendActivity() {},
      sendReceipt() {},
      sendReply: async () => ({ id: "message", state: "queued" }),
    }),
  });
}

function usage(input) {
  return {
    input,
    output: 1,
    cacheRead: 0,
    cacheWrite: 0,
    totalTokens: input + 1,
    cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 },
  };
}

test("unreachable router startup closes bounded resources without invoking launchctl", async () => {
  const handlers = new Map();
  const reconnectTimers = [];
  const intervals = [];
  const execCalls = [];
  const states = [];
  const kickstarts = [];
  let client;
  const pi = {
    events: { on() {} },
    on(type, handler) { handlers.set(type, handler); },
    registerCommand() {},
    registerTool() {},
    sendMessage() {},
    exec: async (...args) => { execCalls.push(args); throw new Error("launchctl must not run in this test"); },
  };
  const context = {
    sessionManager: {
      getSessionId: () => "unreachable-router-session",
      getBranch: () => [],
      isPersisted: () => false,
      getSessionFile: () => undefined,
    },
    ui: { notify() {}, setStatus() {} },
  };
  createMechaMessagingExtension(pi, {
    resolveCurrentAgentId: () => "unreachable-router",
    setIntervalImpl: (callback, delay) => {
      const interval = { callback, delay, cleared: false };
      intervals.push(interval);
      return interval;
    },
    clearIntervalImpl: (interval) => { if (interval !== undefined) interval.cleared = true; },
    createClient: (options) => {
      client = createMailboxClient({
        ...options,
        routerUrl: "http://127.0.0.1:1",
        kickstart: () => { kickstarts.push("attempted"); },
        onConnectionState: (state) => { states.push(state); },
        reconnectInitialDelayMs: 1,
        reconnectMaxDelayMs: 1,
        setTimeoutImpl: (callback, delay) => {
          const timer = { callback, delay, cleared: false };
          reconnectTimers.push(timer);
          return timer;
        },
        clearTimeoutImpl: (timer) => { timer.cleared = true; },
      });
      return client;
    },
  });

  try {
    handlers.get("session_start")({}, context);
    await waitFor(() => states.some(({ state }) => state === "reconnecting"), "unreachable router should enter bounded reconnect");
  } finally {
    handlers.get("session_shutdown")({}, context);
  }

  assert.ok(client);
  assert.ok(kickstarts.length <= 1);

  assert.deepEqual(execCalls, []);
  assert.ok(reconnectTimers.length >= 1);
  assert.ok(reconnectTimers.every(({ cleared }) => cleared));
  assert.ok(intervals.length >= 1);
  assert.ok(intervals.every(({ cleared }) => cleared));
});

async function createRuntimeSession(t, runtime, {
  responses,
  extensionFactories = [],
  withMailbox = false,
  contextWindow = 1_000,
  tokensPerSecond = 1_000,
  seed,
} = {}) {
  const { coding, ai } = runtime;
  const faux = ai.fauxProvider({
    provider: "hard-compaction-test",
    models: [{
      id: "hard-compaction-model",
      name: "Hard Compaction Model",
      reasoning: false,
      input: ["text"],
      cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
      contextWindow,
      maxTokens: 256,
    }],
    tokenSize: { min: 1, max: 1 },
    tokensPerSecond,
  });
  faux.setResponses(responses);

  const modelRuntime = await coding.ModelRuntime.create({ modelsPath: null, refreshOnCreate: false });
  modelRuntime.registerNativeProvider(faux.provider);
  const settingsManager = coding.SettingsManager.inMemory({
    compaction: { enabled: false, reserveTokens: 1, keepRecentTokens: 1 },
    retry: { enabled: false },
  });
  const sessionManager = coding.SessionManager.inMemory(process.cwd());
  seed?.(sessionManager, faux.getModel());
  const resourceLoader = new coding.DefaultResourceLoader({
    cwd: process.cwd(),
    agentDir: process.cwd(),
    noExtensions: true,
    noSkills: true,
    noPromptTemplates: true,
    noThemes: true,
    noContextFiles: true,
    extensionFactories: [...(withMailbox ? [mailboxExtension] : []), ...extensionFactories],
  });
  await resourceLoader.reload();
  const { session } = await coding.createAgentSession({
    cwd: process.cwd(),
    modelRuntime,
    model: faux.getModel(),
    settingsManager,
    sessionManager,
    resourceLoader,
    noTools: "builtin",
  });
  const events = [];
  session.subscribe((event) => events.push(event));
  t.after(() => session.dispose());
  return { ai, events, faux, session };
}

test("installed Pi aborts an active stream before starting compaction", async (t) => {
  const runtime = await installedPiRuntime(t);
  if (runtime === undefined) return;

  let requested = false;
  const compactDuringStream = (pi) => {
    pi.on("message_update", (_event, context) => {
      if (requested) return;
      requested = true;
      context.compact();
    });
  };
  const { events, session } = await createRuntimeSession(t, runtime, {
    extensionFactories: [compactDuringStream],
    responses: [
      runtime.ai.fauxAssistantMessage("stream ".repeat(600)),
      runtime.ai.fauxAssistantMessage("compaction summary"),
    ],
  });

  await session.prompt("start the stream");
  await waitFor(() => events.some((event) => event.type === "compaction_end"), "compaction should finish after the stream aborts");

  assert.equal(requested, true);
  const agentEnd = events.findIndex((event) => event.type === "agent_end");
  const compactionStart = events.findIndex((event) => event.type === "compaction_start");
  assert.ok(agentEnd >= 0);
  assert.ok(compactionStart > agentEnd);
  assert.equal(events.some((event) => event.type === "message_end" && event.message?.stopReason === "aborted"), true);
  assert.equal(session.isIdle, true);
});

test("installed Pi hard guard aborts the next run and compacts it", async (t) => {
  const runtime = await installedPiRuntime(t);
  if (runtime === undefined) return;
  restoreCompactionPolicy(t, { levels: [40, { percent: 50, mandatory: true }] });

  const { events, session } = await createRuntimeSession(t, runtime, {
    withMailbox: true,
    seed(sessionManager, model) {
      sessionManager.appendMessage({ role: "user", content: "prior request ".repeat(1_000), timestamp: Date.now() - 2 });
      sessionManager.appendMessage({
        role: "assistant",
        content: [{ type: "text", text: "prior response" }],
        api: model.api,
        provider: model.provider,
        model: model.id,
        usage: usage(600),
        stopReason: "stop",
        timestamp: Date.now() - 1,
      });
    },
    responses: [
      runtime.ai.fauxAssistantMessage("normal response that must be aborted"),
      runtime.ai.fauxAssistantMessage("compaction summary"),
    ],
  });

  await session.prompt("continue the task");
  await waitFor(() => events.some((event) => event.type === "compaction_end"), "hard guard compaction should finish");
  const agentEnd = events.findIndex((event) => event.type === "agent_end");
  const compactionStart = events.findIndex((event) => event.type === "compaction_start");
  assert.ok(agentEnd >= 0);
  assert.ok(compactionStart > agentEnd);
  assert.equal(session.messages.some((message) => message.role === "assistant" && message.content.some((part) => part.type === "text" && part.text.includes("normal response that must be aborted"))), false);

  assert.equal(session.isIdle, true);
});

test("installed Pi waits for a tool that ignores its abort signal", async (t) => {
  const runtime = await installedPiRuntime(t);
  if (runtime === undefined) return;

  let releaseTool;
  let markToolStarted;
  const toolStarted = new Promise((resolve) => { markToolStarted = resolve; });
  const blockingTool = (pi) => {
    pi.registerTool({
      name: "hold_until_released",
      label: "Hold until released",
      description: "Test-only tool that deliberately ignores cancellation.",
      parameters: runtime.ai.Type.Object({}),
      async execute(_id, _params, _signal, _onUpdate, context) {
        markToolStarted();
        context.compact();
        return new Promise((resolve) => {
          releaseTool = () => resolve({ content: [{ type: "text", text: "released" }], details: {} });
        });
      },
    });
  };
  const { events, session } = await createRuntimeSession(t, runtime, {
    extensionFactories: [blockingTool],
    responses: [
      runtime.ai.fauxAssistantMessage(runtime.ai.fauxToolCall("hold_until_released", {})),
      runtime.ai.fauxAssistantMessage("compaction summary"),
    ],
  });

  const prompt = session.prompt("run the holding tool");
  await toolStarted;
  assert.equal(events.some((event) => event.type === "compaction_start"), false);

  releaseTool();
  await prompt;
  await waitFor(() => events.some((event) => event.type === "compaction_end"), "compaction should finish after the tool releases");
  const agentEnd = events.findIndex((event) => event.type === "agent_end");
  const compactionStart = events.findIndex((event) => event.type === "compaction_start");
  assert.ok(agentEnd >= 0);
  assert.ok(compactionStart > agentEnd);
  assert.equal(session.isIdle, true);
});
