import assert from "node:assert/strict";
import { mkdir, readdir, readFile, rm, writeFile } from "node:fs/promises";
import { mkdtemp } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import test from "node:test";

import packageExtension, * as packageExports from "../pi-extensions/mecha-messaging/index.ts";

const PACKAGE_ROOT = resolve(dirname(fileURLToPath(import.meta.url)), "../pi-extensions/mecha-messaging");
const GOAL_PACKAGE_ROOT = resolve(dirname(fileURLToPath(import.meta.url)), "../pi-extensions/mecha-goal");
const EXPECTED_EXPORTS = [
  "DEFAULT_COMPACTION_POLICY",
  "INLINE_TOOL_ALLOWANCE_TOOL_NAME",
  "appendMemoryCaptureDiagnostic",
  "appendOrphanToolResultDiagnostic",
  "boundedConversationText",
  "captureNativeSentinelSpawner",
  "compactionModelKeys",
  "compactionNotificationMessage",
  "compactionPolicyFromEnvironment",
  "createExtensionTrace",
  "createInlineToolPolicy",
  "createMailboxClient",
  "createMechaMessagingExtension",
  "createNativeSubagentTrace",
  "createPiMailboxDeliveryCoordinator",
  "createRecallController",
  "createRecallPipeline",
  "createRecallState",
  "createSettledNotificationScheduler",
  "default",
  "effectiveCompactionPolicy",
  "inlineToolBlockReason",
  "mailboxSessionDeliveryState",
  "nextCompactionThreshold",
  "postCompactionNotificationMessage",
  "quarantineOrphanToolResults",
  "queryTmuxWindowName",
  "resolveAgentId",
  "resolveCompactionPolicy",
  "routeMailboxEnvelope",
  "runRecallEvaluator",
  "selectCompactionTier",
  "spawnNativeSentinel",
  "summarizeNativeSubagents",
  "transientChildAgentId",
  "validateRecallChoice",
].sort();

function extensionPi() {
  const handlers = new Map<string, unknown[]>();
  const tools = new Map<string, unknown>();
  const commands = new Map<string, unknown>();
  const toolRegistrations: string[] = [];
  const commandRegistrations: string[] = [];
  const events = {
    on(name: string, handler: unknown) {
      const current = eventsByName.get(name) ?? [];
      current.push(handler);
      eventsByName.set(name, current);
    },
    emit() {},
  };
  const eventsByName = new Map<string, unknown[]>();
  return {
    handlers,
    tools,
    commands,
    events,
    on(name: string, handler: unknown) {
      const current = handlers.get(name) ?? [];
      current.push(handler);
      handlers.set(name, current);
    },
    appendEntry() {},
    registerTool(tool: { name: string }) { toolRegistrations.push(tool.name); tools.set(tool.name, tool); },
    registerCommand(name: string, command: unknown) { commandRegistrations.push(name); commands.set(name, command); },
    sendMessage() {},
    exec: async () => ({ code: 0, stderr: "" }),
    toolRegistrations,
    commandRegistrations,
  };
}

test("the TypeScript package exposes the Mecha Messaging export surface", () => {
  assert.deepEqual(Object.keys(packageExports).sort(), EXPECTED_EXPORTS);
});

test("the Messaging package dependency graph stays within Messaging and extracted Goal packages", async () => {
  const files = (await readdir(PACKAGE_ROOT)).filter((file) => /\.(?:ts|js)$/.test(file)).sort();
  assert.deepEqual(files, [
    "index.ts",
    "mailbox-client.js",
    "mailbox-client.ts",
    "memory-capture.ts",
    "operational-tools.ts",
    "recall-controller.ts",
    "recall-evaluator.ts",
    "recall-pipeline.ts",
    "recall-state.ts",
    "reminder-lock.ts",
  ]);

  for (const file of files.filter((candidate) => candidate.endsWith(".ts"))) {
    const source = await readFile(join(PACKAGE_ROOT, file), "utf8");
    for (const specifier of source.matchAll(/from\s+["'](\.[^"']+)["']/g)) {
      const target = resolve(PACKAGE_ROOT, dirname(file), specifier[1]);
      assert.ok(
        target.startsWith(`${PACKAGE_ROOT}/`) || target.startsWith(`${GOAL_PACKAGE_ROOT}/`),
        `${file} escapes the package graph: ${specifier[1]}`,
      );
    }
  }

  const home = await mkdtemp(join(tmpdir(), "mecha-package-discovery-"));
  try {
    const settingsPath = join(home, ".pi", "agent", "settings.json");
    await mkdir(dirname(settingsPath), { recursive: true });
    await writeFile(settingsPath, JSON.stringify({ packages: [PACKAGE_ROOT] }));
    const settings = JSON.parse(await readFile(settingsPath, "utf8"));
    const entrypoint = join(settings.packages[0], "index.ts");
    assert.equal(await readFile(entrypoint, "utf8").then(() => true), true);
    assert.equal((await import(pathToFileURL(entrypoint).href)).default, packageExtension);
  } finally {
    await rm(home, { recursive: true, force: true });
  }
});

test("Pi discovers the Messaging package without registering Goal from temporary settings", async (t) => {
  const piEntry = process.env.PI_CODING_AGENT_ENTRY
    ?? "/opt/homebrew/lib/node_modules/@earendil-works/pi-coding-agent/dist/index.js";
  let piCore: {
    SettingsManager: { create: (cwd: string, agentDir: string) => { getPackages: () => string[] } };
    discoverAndLoadExtensions: (paths: string[], cwd: string, agentDir: string) => Promise<{
      extensions: Array<{ resolvedPath: string; commands: Map<string, unknown>; tools: Map<string, unknown> }>;
      errors: Array<{ path: string; error: string }>;
    }>;
  };
  try {
    piCore = await import(pathToFileURL(piEntry).href);
  } catch (error) {
    if (error && typeof error === "object" && "code" in error && error.code === "ERR_MODULE_NOT_FOUND") {
      t.skip("requires the Pi runtime");
      return;
    }
    throw error;
  }

  const home = await mkdtemp(join(tmpdir(), "mecha-pi-discovery-"));
  const agentDir = join(home, ".pi", "agent");
  const settingsPath = join(agentDir, "settings.json");
  const settingsText = JSON.stringify({ packages: [PACKAGE_ROOT] });
  try {
    await mkdir(agentDir, { recursive: true });
    await writeFile(settingsPath, settingsText);

    const settings = piCore.SettingsManager.create(PACKAGE_ROOT, agentDir);
    assert.deepEqual(settings.getPackages(), [PACKAGE_ROOT]);
    const loaded = await piCore.discoverAndLoadExtensions(settings.getPackages(), PACKAGE_ROOT, agentDir);
    assert.deepEqual(loaded.errors, []);
    assert.equal(loaded.extensions.length, 1);
    assert.equal(loaded.extensions[0].resolvedPath, join(PACKAGE_ROOT, "index.ts"));
    assert.deepEqual([...loaded.extensions[0].tools.keys()].filter((name) => name === "goal" || name.startsWith("goal_")), []);
    assert.deepEqual([...loaded.extensions[0].commands.keys()].filter((name) => name === "goal" || name.startsWith("goal-")), []);
    assert.equal(await readFile(settingsPath, "utf8"), settingsText);
  } finally {
    await rm(home, { recursive: true, force: true });
  }
});

test("Messaging package discovery does not register Goal commands or tools", () => {
  const pi = extensionPi();
  packageExtension(pi);
  const isGoalTool = (name: string) => name === "goal" || name.startsWith("goal_");
  const isGoalCommand = (name: string) => name === "goal" || name.startsWith("goal-");
  assert.deepEqual(pi.toolRegistrations.filter(isGoalTool), []);
  assert.deepEqual([...pi.tools.keys()].filter(isGoalTool), []);
  assert.deepEqual(pi.commandRegistrations.filter(isGoalCommand), []);
  assert.deepEqual([...pi.commands.keys()].filter(isGoalCommand), []);
  assert.equal(pi.handlers.get("session_start")?.length, 1);
});

test("history quarantine ignores null and malformed content blocks", () => {
  const assistant = {
    role: "assistant",
    content: [null, "malformed", { type: "toolCall", id: "call-valid|provider", name: "bash" }, { type: "toolCall", id: 42 }],
  };
  const orphan = { role: "toolResult", toolCallId: "call-orphan|provider", toolName: "bash", content: [] };

  assert.doesNotThrow(() => packageExports.quarantineOrphanToolResults([assistant, orphan]));
  const result = packageExports.quarantineOrphanToolResults([assistant, orphan]);
  assert.deepEqual(result.messages, [assistant]);
  assert.equal(result.orphaned[0]?.nearestAssistantToolCallIds[0], "call-valid");
});

test("memory diagnostic trigger fallback stays unknown", async () => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-memory-diagnostic-fallback-"));
  const path = join(directory, "diagnostic.jsonl");
  try {
    packageExports.appendMemoryCaptureDiagnostic({}, { path });
    const record = JSON.parse((await readFile(path, "utf8")).trim());
    assert.equal(record.trigger, "unknown");
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
});

test("model projection ignores null and malformed registry entries", async () => {
  const pi = extensionPi();
  let modelCatalog: (() => unknown) | undefined;
  let currentModel: (() => unknown) | undefined;
  const context = {
    sessionManager: {
      getBranch: () => [],
      getSessionId: () => "session",
      isPersisted: () => false,
      getSessionFile: () => undefined,
    },
    model: null,
    modelRegistry: {
      getAvailable: () => [null, "malformed", { provider: "provider" }, { provider: "provider", id: 42 }, { provider: "provider", id: "valid" }],
    },
    ui: { setStatus() {}, notify() {} },
  };
  const createClient = (options: Parameters<typeof packageExports.createMailboxClient>[0]) => {
    modelCatalog = options.getModelCatalog;
    currentModel = options.getCurrentModel;
    const client = packageExports.createMailboxClient(options);
    client.connect = () => Promise.resolve();
    return client;
  };

  packageExports.createMechaMessagingExtension(pi, { resolveCurrentAgentId: () => "worker", createClient });
  const sessionStart = pi.handlers.get("session_start")?.at(-1);
  assert.equal(typeof sessionStart, "function");
  await sessionStart?.({}, context);
  assert.deepEqual(modelCatalog?.(), [{ provider: "provider", id: "valid" }]);
  assert.equal(currentModel?.(), null);
});
