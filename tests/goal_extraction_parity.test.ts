import assert from "node:assert/strict";
import { access, mkdir, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import test, { type TestContext } from "node:test";

const REPO_ROOT = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const GOAL_PACKAGE_ROOT = join(REPO_ROOT, "pi-extensions", "mecha-goal");
const MESSAGING_PACKAGE_ROOT = join(REPO_ROOT, "pi-extensions", "mecha-messaging");
const GOAL_TOOLS = ["goal", "goal_snooze"];
const GOAL_COMMANDS = [
  "goal",
  "goal-lock",
  "goal-snooze",
  "goal-snooze-turn-limit",
  "goal-snooze-budget",
  "goal-snooze-budget-refill",
];

async function packageEntry(root: string): Promise<string | undefined> {
  for (const name of ["index.ts", "index.js"]) {
    try {
      await access(join(root, name));
      return join(root, name);
    } catch {
      // Try the generated JavaScript entrypoint next.
    }
  }
  return undefined;
}

async function loadPackage(t: TestContext, root: string, label: string): Promise<Record<string, unknown> | undefined> {
  const entry = await packageEntry(root);
  if (entry === undefined) {
    t.skip(`${label} package source is not ready at ${root}`);
    return undefined;
  }
  return import(pathToFileURL(entry).href);
}

function registrationPi() {
  const handlers = new Map<string, unknown[]>();
  const eventsByName = new Map<string, unknown[]>();
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
  return {
    handlers,
    events,
    tools,
    commands,
    toolRegistrations,
    commandRegistrations,
    getSessionId: () => "parity-session",
    on(name: string, handler: unknown) {
      const current = handlers.get(name) ?? [];
      current.push(handler);
      handlers.set(name, current);
    },
    appendEntry() {},
    registerTool(tool: { name: string }) {
      toolRegistrations.push(tool.name);
      tools.set(tool.name, tool);
    },
    registerCommand(name: string, command: unknown) {
      commandRegistrations.push(name);
      commands.set(name, command);
    },
    sendMessage() {},
    exec: async () => ({ code: 0, stderr: "" }),
  };
}

function isGoalTool(name: string): boolean {
  return name === "goal" || name.startsWith("goal_");
}

function isGoalCommand(name: string): boolean {
  return name === "goal" || name.startsWith("goal-");
}

test("Goal-only package registers the complete Goal tool and command surface", async (t) => {
  const module = await loadPackage(t, GOAL_PACKAGE_ROOT, "Goal");
  if (module === undefined) return;

  const pi = registrationPi();
  const extension = module.default ?? module.createGoalExtension;
  assert.equal(typeof extension, "function");
  if (typeof extension !== "function") return;
  extension(pi);

  assert.deepEqual([...pi.tools.keys()], GOAL_TOOLS);
  assert.deepEqual([...pi.commands.keys()], GOAL_COMMANDS);
  assert.deepEqual(pi.toolRegistrations.filter(isGoalTool), GOAL_TOOLS);
  assert.deepEqual(pi.commandRegistrations.filter(isGoalCommand), GOAL_COMMANDS);
});

test("Messaging-only package does not register Goal tools or commands", async (t) => {
  const module = await loadPackage(t, MESSAGING_PACKAGE_ROOT, "Messaging");
  if (module === undefined) return;

  const pi = registrationPi();
  assert.equal(typeof module.default, "function");
  module.default(pi);

  assert.deepEqual(pi.toolRegistrations.filter(isGoalTool), []);
  assert.deepEqual(pi.commandRegistrations.filter(isGoalCommand), []);
  assert.deepEqual([...pi.tools.keys()].filter(isGoalTool), []);
  assert.deepEqual([...pi.commands.keys()].filter(isGoalCommand), []);
});

test("Pi temporary settings discover Goal first and Messaging second without duplicate tools or commands", async (t) => {
  const goalEntry = await packageEntry(GOAL_PACKAGE_ROOT);
  const messagingEntry = await packageEntry(MESSAGING_PACKAGE_ROOT);
  if (goalEntry === undefined || messagingEntry === undefined) {
    t.skip("extracted Goal and Messaging package sources are not both ready");
    return;
  }

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

  const home = await mkdtemp(join(tmpdir(), "mecha-goal-extraction-"));
  const agentDir = join(home, ".pi", "agent");
  const settingsPath = join(agentDir, "settings.json");
  const packagePaths = [GOAL_PACKAGE_ROOT, MESSAGING_PACKAGE_ROOT];
  const settingsText = JSON.stringify({ packages: packagePaths });
  try {
    await mkdir(agentDir, { recursive: true });
    await writeFile(settingsPath, settingsText);

    const settings = piCore.SettingsManager.create(REPO_ROOT, agentDir);
    assert.deepEqual(settings.getPackages(), packagePaths);
    const loaded = await piCore.discoverAndLoadExtensions(settings.getPackages(), REPO_ROOT, agentDir);
    assert.deepEqual(loaded.errors, []);
    assert.equal(loaded.extensions.length, 2);
    assert.equal(loaded.extensions[0].resolvedPath, goalEntry);
    assert.equal(loaded.extensions[1].resolvedPath, messagingEntry);

    const goalExtension = loaded.extensions[0];
    const messagingExtension = loaded.extensions[1];
    assert.deepEqual([...goalExtension.tools.keys()], GOAL_TOOLS);
    assert.deepEqual([...goalExtension.commands.keys()], GOAL_COMMANDS);
    assert.deepEqual([...messagingExtension.tools.keys()].filter(isGoalTool), []);
    assert.deepEqual([...messagingExtension.commands.keys()].filter(isGoalCommand), []);

    const allTools = loaded.extensions.flatMap((extension) => [...extension.tools.keys()]);
    const allCommands = loaded.extensions.flatMap((extension) => [...extension.commands.keys()]);
    assert.equal(new Set(allTools).size, allTools.length);
    assert.equal(new Set(allCommands).size, allCommands.length);
    assert.equal(allTools.filter(isGoalTool).length, GOAL_TOOLS.length);
    assert.equal(allCommands.filter(isGoalCommand).length, GOAL_COMMANDS.length);
    assert.equal(await readFile(settingsPath, "utf8"), settingsText);
  } finally {
    await rm(home, { recursive: true, force: true });
  }
});
