import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { existsSync, readFileSync, realpathSync, statSync } from "node:fs";
import { mkdir, mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { pathToFileURL } from "node:url";
import test from "node:test";

import {
  createExtensionTrace,
  createMechaMessagingExtension,
} from "../pi-extensions/mecha-messaging/index.ts";

const EXPECTED_PI_VERSION = "0.85.0";
const FAKE_ROUTER_URL = "http://127.0.0.1:65535";
const FIXED_NOW = 1_735_689_600_000;
const FIXED_TRACE_DATE = new Date("2025-01-01T00:00:00.000Z");
const AGENT_ID = "diagnostics-history-e2e";
const PRIVATE_PROMPT = "PRIVATE_PROMPT_DIAGNOSTICS_HISTORY";
const CURRENT_PROMPT = `CURRENT_PROMPT_DIAGNOSTICS_HISTORY ${PRIVATE_PROMPT}`;
const ORDINARY_ASSISTANT = "ORDINARY_ASSISTANT_DIAGNOSTICS_HISTORY";
const VALID_RESULT = "VALID_RESULT_DIAGNOSTICS_HISTORY";
const PRIVATE_TOOL_OUTPUT = "PRIVATE_TOOL_OUTPUT_DIAGNOSTICS_HISTORY";
const PRIVATE_ERROR = "PRIVATE_ERROR_DIAGNOSTICS_HISTORY";
const PRIVATE_SECRET = "PRIVATE_SECRET_DIAGNOSTICS_HISTORY";
const INVALID_CALL_ID = "invalid-call";
const ALIAS_CALL_ID = "alias-call";
const LATER_CALL_ID = "later-call";
const ERROR_CALL_ID = "error-call";
const ABORTED_CALL_ID = "aborted-call";

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function readPackageMetadata(root: string, expectedName: string): { name: string; version: string } {
  const manifestPath = join(root, "package.json");
  if (!existsSync(manifestPath)) throw new Error(`installed package manifest is missing: ${manifestPath}`);
  let manifest: unknown;
  try {
    manifest = JSON.parse(readFileSync(manifestPath, "utf8"));
  } catch (error) {
    throw new Error(`installed package manifest is unreadable: ${manifestPath}: ${String(error)}`);
  }
  if (!isRecord(manifest) || typeof manifest.name !== "string" || typeof manifest.version !== "string") {
    throw new Error(`installed package manifest is malformed: ${manifestPath}`);
  }
  if (manifest.name !== expectedName || manifest.version.length === 0) {
    throw new Error(`installed package identity mismatch at ${root}: expected ${expectedName}`);
  }
  return { name: manifest.name, version: manifest.version };
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

type InstalledPiRuntime = { piRoot: string; aiRoot: string };

function resolveInstalledPiRoot(testContext: { skip: (message: string) => void }): InstalledPiRuntime | undefined {
  const configured = process.env.MECHA_PI_PACKAGE_ROOT;
  let root: string | undefined;
  if (configured !== undefined) {
    if (configured.trim().length === 0) throw new Error("MECHA_PI_PACKAGE_ROOT is configured but empty");
    root = resolve(configured.trim());
    if (!existsSync(root)) throw new Error(`configured MECHA_PI_PACKAGE_ROOT is missing: ${root}`);
  } else {
    const npmRoot = globalNpmRoot();
    root = npmRoot === undefined ? undefined : join(npmRoot, "@earendil-works", "pi-coding-agent");
    if (root === undefined || !existsSync(root)) {
      testContext.skip("not-run: installed Pi runtime is unavailable; set MECHA_PI_PACKAGE_ROOT for acceptance");
      return undefined;
    }
  }

  const resolvedRoot = realpathSync(root);
  if (isTrackedSourceCheckout(resolvedRoot)) {
    throw new Error(`the installed-Pi E2E rejects a source checkout: ${resolvedRoot}`);
  }
  const metadata = readPackageMetadata(resolvedRoot, "@earendil-works/pi-coding-agent");
  if (metadata.version !== EXPECTED_PI_VERSION) {
    throw new Error(`installed Pi version is ${metadata.version}; expected ${EXPECTED_PI_VERSION}`);
  }
  const codingEntry = join(resolvedRoot, "dist", "index.js");
  if (!existsSync(codingEntry)) throw new Error(`installed Pi entrypoint is missing: ${codingEntry}`);

  const npmRoot = globalNpmRoot();
  const aiCandidates = [
    join(resolvedRoot, "node_modules", "@earendil-works", "pi-ai"),
    join(dirname(dirname(resolvedRoot)), "@earendil-works", "pi-ai"),
    ...(npmRoot === undefined ? [] : [join(npmRoot, "@earendil-works", "pi-ai")]),
  ];
  const aiRoot = aiCandidates.find((candidate) => existsSync(join(candidate, "package.json")));
  if (aiRoot === undefined) throw new Error(`matching installed Pi AI package is missing beside ${resolvedRoot}`);
  const resolvedAiRoot = realpathSync(aiRoot);
  if (isTrackedSourceCheckout(resolvedAiRoot)) {
    throw new Error(`the installed-Pi E2E rejects a Pi AI source checkout: ${resolvedAiRoot}`);
  }
  const aiMetadata = readPackageMetadata(resolvedAiRoot, "@earendil-works/pi-ai");
  if (aiMetadata.version !== metadata.version) {
    throw new Error(`installed Pi package versions do not match: ${metadata.version} and ${aiMetadata.version}`);
  }
  const aiEntry = join(resolvedAiRoot, "dist", "index.js");
  if (!existsSync(aiEntry)) throw new Error(`installed Pi AI entrypoint is missing: ${aiEntry}`);

  return { piRoot: resolvedRoot, aiRoot: resolvedAiRoot };
}

function readJsonLines(path: string): Array<Record<string, unknown>> {
  if (!existsSync(path)) return [];
  const content = readFileSync(path, "utf8").trim();
  if (content.length === 0) return [];
  return content.split("\n").map((line) => JSON.parse(line)).filter(isRecord);
}

function mode(path: string): number {
  return statSync(path).mode & 0o777;
}

function clone<T>(value: T): T {
  return structuredClone(value);
}

function providerMessages(context: unknown): Array<Record<string, unknown>> {
  if (!isRecord(context) || !Array.isArray(context.messages)) throw new Error("faux provider did not receive context messages");
  return context.messages.filter(isRecord);
}

function messageHasText(message: Record<string, unknown>, text: string): boolean {
  if (message.role === "user" && message.content === text) return true;
  return Array.isArray(message.content) && message.content.some((block) => isRecord(block) && block.type === "text" && block.text === text);
}

function assistantWithText(messages: Array<Record<string, unknown>>, text: string): Record<string, unknown> {
  const message = messages.find((candidate) => candidate.role === "assistant" && messageHasText(candidate, text));
  if (message === undefined) throw new Error(`provider context is missing assistant text ${text}`);
  return message;
}

function toolResultWithId(messages: Array<Record<string, unknown>>, id: string): Record<string, unknown> {
  const message = messages.find((candidate) => candidate.role === "toolResult" && candidate.toolCallId === id);
  if (message === undefined) throw new Error(`provider context is missing tool result ${id}`);
  return message;
}

function contentBlock(message: Record<string, unknown>, predicate: (block: Record<string, unknown>) => boolean): Record<string, unknown> {
  if (!Array.isArray(message.content)) throw new Error("assistant content is not an array");
  const block = message.content.find((candidate) => isRecord(candidate) && predicate(candidate));
  if (!isRecord(block)) throw new Error("assistant content is missing the expected block");
  return block;
}

function assertSanitizedProviderContext(messages: Array<Record<string, unknown>>): void {
  const assistant = assistantWithText(messages, ORDINARY_ASSISTANT);
  assert.deepEqual(contentBlock(assistant, (block) => block.type === "toolCall" && block.id === "valid-call|provider"), {
    type: "toolCall",
    id: "valid-call|provider",
    name: "valid_tool",
    arguments: { value: "valid" },
  });
  assert.deepEqual(contentBlock(assistant, (block) => block.type === "tool_use" && block.id === ALIAS_CALL_ID), {
    type: "tool_use",
    id: ALIAS_CALL_ID,
    name: "alias_tool",
    arguments: { value: "alias" },
  });
  assert.deepEqual(contentBlock(assistant, (block) => block.type === "toolCall" && block.id === "|call"), {
    type: "toolCall",
    id: "|call",
    name: "empty_provider_tool",
    arguments: { value: "empty" },
  });
  assert.equal(Array.isArray(assistant.content) && assistant.content.some((block) => isRecord(block) && block.id === INVALID_CALL_ID), false);

  const validResult = toolResultWithId(messages, "valid-call|result");
  assert.deepEqual(validResult, {
    role: "toolResult",
    toolCallId: "valid-call|result",
    toolName: "valid_tool",
    content: [{ type: "text", text: VALID_RESULT }],
    isError: false,
  });
  assert.deepEqual(toolResultWithId(messages, "|suffix"), {
    role: "toolResult",
    toolCallId: "|suffix",
    toolName: "empty_provider_tool",
    content: [{ type: "text", text: "EMPTY_PROVIDER_RESULT" }],
    isError: false,
  });
  assert.deepEqual(toolResultWithId(messages, ""), {
    role: "toolResult",
    toolCallId: "",
    toolName: "missing_id_tool",
    content: [{ type: "text", text: "MISSING_ID_RESULT" }],
    isError: false,
  });

  const laterAssistant = messages.find((candidate) => candidate.role === "assistant" && Array.isArray(candidate.content)
    && candidate.content.some((block) => isRecord(block) && block.type === "toolCall" && block.id === `${LATER_CALL_ID}|provider`));
  assert.ok(laterAssistant);
  const errorAssistant = messages.find((candidate) => candidate.role === "assistant" && candidate.stopReason === "error");
  const abortedAssistant = messages.find((candidate) => candidate.role === "assistant" && candidate.stopReason === "aborted");
  assert.ok(errorAssistant);
  assert.ok(abortedAssistant);

  for (const id of [INVALID_CALL_ID, ALIAS_CALL_ID, `${LATER_CALL_ID}|orphan`, ERROR_CALL_ID, ABORTED_CALL_ID]) {
    assert.equal(messages.some((message) => message.role === "toolResult" && message.toolCallId === id), false, `orphan result ${id} reached provider`);
  }
  assert.equal(messages.some((message) => JSON.stringify(message).includes(INVALID_CALL_ID)), false, "invalid call data reached provider");

  const ordinaryIndex = messages.indexOf(assistant);
  const validResultIndex = messages.indexOf(validResult);
  const emptyResultIndex = messages.findIndex((message) => message.role === "toolResult" && message.toolCallId === "|suffix");
  const laterIndex = messages.indexOf(laterAssistant);
  const errorIndex = messages.indexOf(errorAssistant);
  const abortedIndex = messages.indexOf(abortedAssistant);
  const promptIndex = messages.findIndex((message) => message.role === "user" && messageHasText(message, CURRENT_PROMPT));
  assert.ok(ordinaryIndex >= 0 && ordinaryIndex < validResultIndex);
  assert.ok(validResultIndex < laterIndex && laterIndex < emptyResultIndex);
  assert.ok(emptyResultIndex < errorIndex && errorIndex < abortedIndex && abortedIndex < promptIndex, JSON.stringify(messages.map((message) => ({ role: message.role, stopReason: message.stopReason, toolCallId: message.toolCallId, content: message.content }))));
}

type ScenarioOptions = {
  coding: Record<string, unknown>;
  ai: Record<string, unknown>;
  root: string;
  dirty: boolean;
  unusableDiagnostics: boolean;
};

async function runScenario({ coding, ai, root, dirty, unusableDiagnostics }: ScenarioOptions): Promise<void> {
  const originalCwd = process.cwd();
  const originalEnvironment = { ...process.env };
  const originalFetch = globalThis.fetch;
  const home = join(root, "home");
  const agentDir = join(root, "pi-agent");
  const projectDir = join(root, "project");
  const sessionDir = join(root, "sessions");
  const tempDir = join(root, "tmp");
  const tracePath = join(root, "trace", "extension.jsonl");
  const orphanPath = join(root, "diagnostics", "orphan.jsonl");
  const memoryPath = join(root, "diagnostics", "memory.jsonl");
  const unexpectedCalls: string[] = [];
  const fetchCalls: string[] = [];
  const clientCalls: Array<{ method: string; args: unknown[] }> = [];
  const coordinatorCalls: Array<{ method: string; args: unknown[] }> = [];
  const timerCalls: Array<{ action: string; id: number; delay?: number }> = [];
  const traceCalls: Array<{ event: string; fields: Record<string, unknown> }> = [];
  const sideEffects: string[] = [];
  const uiCalls: Array<{ message: string; type?: string }> = [];
  const intervals: Array<{ id: number; cleared: boolean }> = [];
  const timeouts: Array<{ id: number; cleared: boolean }> = [];
  let nextTimerId = 1;
  let createdClient: Record<string, unknown> | undefined;
  let createdCoordinator: Record<string, unknown> | undefined;
  let clientClosed = false;
  let coordinatorClosed = false;
  let providerSawOrphanFile = false;
  let orphanFileSeenAtNotice = false;
  let session: Record<string, unknown> | undefined;

  const reportUnexpected = (boundary: string): never => {
    unexpectedCalls.push(boundary);
    throw new Error(`unexpected installed-Pi E2E boundary call: ${boundary}`);
  };
  const rejectAllFetch = async (input: unknown): Promise<never> => {
    const url = String(input);
    fetchCalls.push(url);
    return reportUnexpected(`fetch:${url}`);
  };
  const setIntervalImpl = (callback: () => void, delay: number) => {
    if (delay !== 2_000) return reportUnexpected(`setInterval:${delay}`);
    const handle = { id: nextTimerId++, cleared: false };
    intervals.push(handle);
    timerCalls.push({ action: "set-interval", id: handle.id, delay });
    void callback;
    return { ...handle, unref() {} };
  };
  const clearIntervalImpl = (handle: { id?: number; cleared?: boolean } | undefined) => {
    if (handle === undefined || typeof handle.id !== "number") return reportUnexpected("clearInterval:invalid-handle");
    const owned = intervals.find((candidate) => candidate.id === handle.id);
    if (owned === undefined) return reportUnexpected(`clearInterval:${handle.id}`);
    owned.cleared = true;
    timerCalls.push({ action: "clear-interval", id: handle.id });
  };
  const setTimeoutImpl = (callback: () => void, delay: number) => {
    const handle = { id: nextTimerId++, cleared: false };
    timeouts.push(handle);
    timerCalls.push({ action: "set-timeout", id: handle.id, delay });
    void callback;
    return reportUnexpected(`setTimeout:${delay}`);
  };
  const clearTimeoutImpl = (handle: unknown) => {
    if (!isRecord(handle) || typeof handle.id !== "number") return reportUnexpected("clearTimeout:invalid-handle");
    const owned = timeouts.find((candidate) => candidate.id === handle.id);
    if (owned === undefined) return reportUnexpected(`clearTimeout:${handle.id}`);
    owned.cleared = true;
    timerCalls.push({ action: "clear-timeout", id: handle.id });
  };

  function createStrictClient(): Record<string, unknown> {
    const target: Record<string, unknown> = {
      instanceId: "fixed-instance-diagnostics-history",
      parentSessionId: "fixed-parent-session",
      producerEpoch: "00000000-0000-4000-8000-000000000001",
      currentLeaseId: () => {
        clientCalls.push({ method: "currentLeaseId", args: [] });
        return "fixed-lease-diagnostics-history";
      },
      connect: async () => {
        clientCalls.push({ method: "connect", args: [] });
      },
      close: () => {
        clientClosed = true;
        clientCalls.push({ method: "close", args: [] });
      },
      sendGoalState: (value: unknown) => {
        clientCalls.push({ method: "sendGoalState", args: [value] });
        return true;
      },
      sendActivity: (...args: unknown[]) => {
        clientCalls.push({ method: "sendActivity", args });
        return true;
      },
      sendLifecycleSnapshot: (...args: unknown[]) => {
        clientCalls.push({ method: "sendLifecycleSnapshot", args });
        return "fixed-snapshot-diagnostics-history";
      },
      sendLifecycleEvent: (...args: unknown[]) => {
        clientCalls.push({ method: "sendLifecycleEvent", args });
        return true;
      },
      sendMessageArrival: (...args: unknown[]) => {
        clientCalls.push({ method: "sendMessageArrival", args });
        return true;
      },
      sendReceipt: async (...args: unknown[]) => {
        clientCalls.push({ method: "sendReceipt", args });
        return { id: "fixed-receipt" };
      },
      sendReply: async (...args: unknown[]) => {
        clientCalls.push({ method: "sendReply", args });
        return { id: "fixed-reply", state: "queued" };
      },
      requestLifecycleResync: (...args: unknown[]) => {
        clientCalls.push({ method: "requestLifecycleResync", args });
        return false;
      },
      lifecycleStatus: (...args: unknown[]) => {
        clientCalls.push({ method: "lifecycleStatus", args });
        return { supported: false, ready: false, routerVersion: 0, pending: 0, bytes: 0 };
      },
      pause: (...args: unknown[]) => {
        clientCalls.push({ method: "pause", args });
      },
      resume: async (...args: unknown[]) => {
        clientCalls.push({ method: "resume", args });
      },
    };
    return new Proxy(target, {
      get(object, property, receiver) {
        if (typeof property === "symbol" || property in object) return Reflect.get(object, property, receiver);
        return reportUnexpected(`client.${String(property)}`);
      },
    });
  }

  function createStrictCoordinator(): Record<string, unknown> {
    const target: Record<string, unknown> = {
      onEnvelope: (...args: unknown[]) => {
        coordinatorCalls.push({ method: "onEnvelope", args });
      },
      onMessageEnd: (...args: unknown[]) => {
        coordinatorCalls.push({ method: "onMessageEnd", args });
      },
      close: () => {
        coordinatorClosed = true;
        coordinatorCalls.push({ method: "close", args: [] });
      },
    };
    const coordinator = new Proxy(target, {
      get(object, property, receiver) {
        if (typeof property === "symbol" || property in object) return Reflect.get(object, property, receiver);
        return reportUnexpected(`coordinator.${String(property)}`);
      },
    });
    createdCoordinator = coordinator;
    return coordinator;
  }

  try {
    await mkdir(home, { recursive: true });
    await mkdir(agentDir, { recursive: true });
    await mkdir(projectDir, { recursive: true });
    await mkdir(sessionDir, { recursive: true });
    await mkdir(tempDir, { recursive: true });
    if (unusableDiagnostics) {
      await mkdir(tracePath, { recursive: true });
      await mkdir(orphanPath, { recursive: true });
    }
    process.chdir(projectDir);
    for (const key of Object.keys(process.env)) delete process.env[key];
    Object.assign(process.env, {
      HOME: home,
      PATH: originalEnvironment.PATH ?? "",
      PI_CODING_AGENT_DIR: agentDir,
      TMPDIR: tempDir,
      MECHA_ROUTER_URL: FAKE_ROUTER_URL,
      MECHA_MEMORY_CAPTURE_DIAGNOSTICS_PATH: memoryPath,
      MECHA_ORPHAN_TOOL_RESULT_DIAGNOSTICS_PATH: orphanPath,
      MECHA_PI_EXTENSION_TRACE_PATH: tracePath,
      MECHA_PI_COMPACTION_POLICY: JSON.stringify({ levels: [{ percent: 99, mandatory: true }] }),
    });
    globalThis.fetch = rejectAllFetch;

    const fileTrace = createExtensionTrace({ path: tracePath, now: () => new Date(FIXED_TRACE_DATE) });
    const nativeSubagentTrace = {
      enabled: fileTrace.enabled,
      record(event: string, fields: Record<string, unknown> = {}) {
        traceCalls.push({ event, fields: clone(fields) });
        sideEffects.push(`trace:${event}`);
        return fileTrace.record(event, fields);
      },
    };
    const traceMalformed = () => traceCalls.some(({ event }) => event === "malformed-tool-call");
    const uiContext = {
      notify(message: string, type?: string) {
        uiCalls.push({ message, type });
        sideEffects.push(`ui:${message}`);
        if (message.startsWith("Quarantined orphan tool result(s):") || message.startsWith("Quarantined orphan tool result(s) before provider request:")) {
          orphanFileSeenAtNotice = existsSync(orphanPath) && !statSync(orphanPath).isDirectory();
        }
      },
      setStatus(_key: string, _value: string | undefined) {},
    };
    const factoryCalls = { messaging: 0 };
    const settingsManager = coding.SettingsManager.inMemory({
      compaction: { enabled: false, reserveTokens: 1, keepRecentTokens: 1 },
      retry: { enabled: false },
    });
    assert.equal(settingsManager.getCompactionEnabled(), false);
    const faux = ai.fauxProvider({
      provider: "diagnostics-history-faux",
      models: [{
        id: "diagnostics-history-model",
        name: "Diagnostics History Faux",
        reasoning: false,
        input: ["text"],
        cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
        contextWindow: 128_000,
        maxTokens: 256,
      }],
      tokenSize: { min: 1, max: 1 },
      tokensPerSecond: 1_000,
    });
    const providerContexts: Array<Array<Record<string, unknown>>> = [];
    faux.setResponses([async (context: unknown) => {
      providerSawOrphanFile = existsSync(orphanPath) && !statSync(orphanPath).isDirectory();
      if (dirty) {
        const messages = providerMessages(context);
        providerContexts.push(clone(messages));
        assertSanitizedProviderContext(messages);
      } else {
        providerContexts.push(clone(providerMessages(context)));
      }
      sideEffects.push("provider");
      return ai.fauxAssistantMessage("DETERMINISTIC_PROVIDER_RESPONSE", { timestamp: FIXED_NOW });
    }]);
    const modelRuntime = await coding.ModelRuntime.create({ modelsPath: null, refreshOnCreate: false });
    modelRuntime.registerNativeProvider(faux.provider);
    const sessionManager = coding.SessionManager.create(projectDir, sessionDir);
    if (dirty) {
      const validCall = ai.fauxToolCall("valid_tool", { value: "valid" }, { id: "valid-call|provider" });
      const invalidCall = ai.fauxToolCall("bad name", { value: "invalid" }, { id: INVALID_CALL_ID });
      const assistant = ai.fauxAssistantMessage([
        ai.fauxText(ORDINARY_ASSISTANT),
        validCall,
        invalidCall,
        { type: "tool_use", id: ALIAS_CALL_ID, name: "alias_tool", arguments: { value: "alias" } },
        ai.fauxToolCall("empty_provider_tool", { value: "empty" }, { id: "|call" }),
      ], { timestamp: FIXED_NOW - 5_000 });
      sessionManager.appendMessage({ role: "user", content: "SEED_USER_DIAGNOSTICS_HISTORY", timestamp: FIXED_NOW - 6_000 });
      sessionManager.appendMessage(assistant);
      sessionManager.appendMessage({ role: "toolResult", toolCallId: INVALID_CALL_ID, toolName: "bad name", content: [{ type: "text", text: PRIVATE_TOOL_OUTPUT }], isError: false });
      sessionManager.appendMessage({ role: "toolResult", toolCallId: ALIAS_CALL_ID, toolName: "alias_tool", content: [{ type: "text", text: PRIVATE_TOOL_OUTPUT }], isError: false });
      sessionManager.appendMessage({ role: "toolResult", toolCallId: "valid-call|result", toolName: "valid_tool", content: [{ type: "text", text: VALID_RESULT }], isError: false });
      sessionManager.appendMessage({ role: "toolResult", toolCallId: `${LATER_CALL_ID}|orphan`, toolName: "later_tool", content: [{ type: "text", text: PRIVATE_TOOL_OUTPUT }], isError: false });
      sessionManager.appendMessage(ai.fauxAssistantMessage([ai.fauxToolCall("later_tool", {}, { id: `${LATER_CALL_ID}|provider` })], { timestamp: FIXED_NOW - 4_000 }));
      sessionManager.appendMessage({ role: "toolResult", toolCallId: "|suffix", toolName: "empty_provider_tool", content: [{ type: "text", text: "EMPTY_PROVIDER_RESULT" }], isError: false });
      sessionManager.appendMessage({ role: "toolResult", toolCallId: "", toolName: "missing_id_tool", content: [{ type: "text", text: "MISSING_ID_RESULT" }], isError: false });
      sessionManager.appendMessage(ai.fauxAssistantMessage([ai.fauxToolCall("error_tool", { value: PRIVATE_SECRET }, { id: ERROR_CALL_ID })], { stopReason: "error", errorMessage: PRIVATE_ERROR, timestamp: FIXED_NOW - 3_000 }));
      sessionManager.appendMessage({ role: "toolResult", toolCallId: ERROR_CALL_ID, toolName: "error_tool", content: [{ type: "text", text: PRIVATE_TOOL_OUTPUT }], isError: false });
      sessionManager.appendMessage(ai.fauxAssistantMessage([ai.fauxToolCall("aborted_tool", { value: PRIVATE_SECRET }, { id: ABORTED_CALL_ID })], { stopReason: "aborted", timestamp: FIXED_NOW - 2_000 }));
      sessionManager.appendMessage({ role: "toolResult", toolCallId: ABORTED_CALL_ID, toolName: "aborted_tool", content: [{ type: "text", text: PRIVATE_TOOL_OUTPUT }], isError: false });
    } else {
      sessionManager.appendMessage({ role: "user", content: "CLEAN_SEED_USER_DIAGNOSTICS_HISTORY", timestamp: FIXED_NOW - 1_000 });
      sessionManager.appendMessage(ai.fauxAssistantMessage("CLEAN_SEED_ASSISTANT_DIAGNOSTICS_HISTORY", { timestamp: FIXED_NOW - 500 }));
    }
    const messagingFactory = (piApi: unknown) => {
      factoryCalls.messaging += 1;
      createMechaMessagingExtension(piApi, {
        resolveCurrentAgentId: () => AGENT_ID,
        createClient: () => {
          const client = createStrictClient();
          createdClient = client;
          return client;
        },
        createCoordinator: () => createStrictCoordinator(),
        setTimeoutImpl,
        clearTimeoutImpl,
        setIntervalImpl,
        clearIntervalImpl,
        nowImpl: () => FIXED_NOW,
        getSubagentsService: () => undefined,
        nativeSubagentTrace,
        operationalTools: {
          reminderLock: {
            guard: async () => reportUnexpected("reminderLock.guard"),
            clear: () => {},
          },
        },
      });
    };
    const resourceLoader = new coding.DefaultResourceLoader({
      cwd: projectDir,
      agentDir,
      settingsManager,
      noSkills: true,
      noPromptTemplates: true,
      noThemes: true,
      noContextFiles: true,
      extensionFactories: [messagingFactory],
    });
    await resourceLoader.reload();
    const loaded = resourceLoader.getExtensions();
    assert.equal(loaded.errors.length, 0, JSON.stringify(loaded.errors));
    assert.equal(loaded.extensions.filter(({ path }: { path: string }) => path.startsWith("<inline:")).length, 1);
    assert.equal(factoryCalls.messaging, 1, "the current checkout Messaging factory must be supplied inline exactly once");
    const sessionResult = await coding.createAgentSession({
      cwd: projectDir,
      agentDir,
      modelRuntime,
      model: faux.getModel(),
      settingsManager,
      sessionManager,
      resourceLoader,
      noTools: "all",
    });
    session = sessionResult.session;
    assert.equal(sessionResult.extensionsResult.errors.length, 0, JSON.stringify(sessionResult.extensionsResult.errors));
    const events: Array<Record<string, unknown>> = [];
    session.subscribe((event: Record<string, unknown>) => events.push(event));
    await session.bindExtensions({ mode: "json", uiContext });
    assert.equal(session.extensionRunner.hasHandlers("context"), true, "installed Pi did not install the context handler");
    assert.equal(session.autoCompactionEnabled, false, "automatic compaction must be disabled before the prompt");
    assert.equal(typeof session.sessionManager.getSessionFile(), "string");
    assert.equal(session.sessionManager.getSessionFile().startsWith(sessionDir), true);
    assert.ok(createdClient);

    await session.prompt(CURRENT_PROMPT);
    assert.equal(session.isIdle, true, "installed Pi prompt did not reach idle");
    assert.equal(faux.state.callCount, 1, "the deterministic faux provider must receive one request");
    assert.equal(fetchCalls.length, 0, "reject-all fetch received an unexpected request");
    assert.equal(unexpectedCalls.length, 0, JSON.stringify(unexpectedCalls));
    assert.equal(events.some((event) => typeof event.type === "string" && event.type.startsWith("compaction")), false, "automatic compaction ran");
    assert.equal(clientCalls.some(({ method }) => method === "connect"), true);
    assert.equal(clientCalls.some(({ method }) => method === "sendGoalState"), true);
    assert.equal(coordinatorCalls.some(({ method }) => method === "onMessageEnd"), true);

    if (dirty) {
      assertSanitizedProviderContext(providerContexts[0] ?? []);
      assert.equal(uiCalls.length, 2);
      assert.deepEqual(uiCalls, [
        { message: "Quarantined malformed tool call(s): bad name", type: "error" },
        { message: `Quarantined orphan tool result(s) before provider request: ${INVALID_CALL_ID}, ${ALIAS_CALL_ID}, ${LATER_CALL_ID}|orphan, ${ERROR_CALL_ID}, ${ABORTED_CALL_ID}`, type: "error" },
      ]);
      assert.equal(traceMalformed(), true);
      const malformedTraceCall = traceCalls.find(({ event }) => event === "malformed-tool-call");
      assert.ok(malformedTraceCall);
      assert.equal(malformedTraceCall.fields.malformedCount, 1);
      assert.deepEqual(malformedTraceCall.fields.malformed, [{
        messageIndex: 1,
        blockIndex: 2,
        toolName: "bad name",
        toolNameLength: 8,
        toolCallId: INVALID_CALL_ID,
      }]);
      assert.ok(sideEffects.indexOf("trace:malformed-tool-call") < sideEffects.indexOf("ui:Quarantined malformed tool call(s): bad name"));
      assert.ok(sideEffects.indexOf("ui:Quarantined malformed tool call(s): bad name") < sideEffects.indexOf(`ui:Quarantined orphan tool result(s) before provider request: ${INVALID_CALL_ID}, ${ALIAS_CALL_ID}, ${LATER_CALL_ID}|orphan, ${ERROR_CALL_ID}, ${ABORTED_CALL_ID}`));
      assert.ok(sideEffects.indexOf(`ui:Quarantined orphan tool result(s) before provider request: ${INVALID_CALL_ID}, ${ALIAS_CALL_ID}, ${LATER_CALL_ID}|orphan, ${ERROR_CALL_ID}, ${ABORTED_CALL_ID}`) < sideEffects.indexOf("provider"));
      assert.equal(providerSawOrphanFile, !unusableDiagnostics);
      assert.equal(orphanFileSeenAtNotice, !unusableDiagnostics);
      if (unusableDiagnostics) {
        assert.equal(existsSync(orphanPath) && statSync(orphanPath).isDirectory(), true);
        assert.equal(existsSync(tracePath) && statSync(tracePath).isDirectory(), true);
      } else {
        const orphanRecords = readJsonLines(orphanPath);
        assert.equal(mode(orphanPath), 0o600);
        assert.ok(statSync(orphanPath).size <= 256 * 1024);
        assert.equal(orphanRecords.length, 1);
        assert.equal(orphanRecords[0]?.quarantined_count, 5);
        assert.deepEqual(orphanRecords[0]?.orphans, [
          { message_index: 2, tool_call_id: INVALID_CALL_ID, provider_tool_call_id: INVALID_CALL_ID, tool_name: "bad name", previous_role: "assistant", next_role: "toolResult", nearest_assistant_index: 1, nearest_assistant_stop_reason: "stop", nearest_assistant_tool_call_ids: ["valid-call", ""], matching_tool_call_later: false },
          { message_index: 3, tool_call_id: ALIAS_CALL_ID, provider_tool_call_id: ALIAS_CALL_ID, tool_name: "alias_tool", previous_role: "toolResult", next_role: "toolResult", nearest_assistant_index: 1, nearest_assistant_stop_reason: "stop", nearest_assistant_tool_call_ids: ["valid-call", ""], matching_tool_call_later: false },
          { message_index: 5, tool_call_id: `${LATER_CALL_ID}|orphan`, provider_tool_call_id: LATER_CALL_ID, tool_name: "later_tool", previous_role: "toolResult", next_role: "assistant", nearest_assistant_index: 1, nearest_assistant_stop_reason: "stop", nearest_assistant_tool_call_ids: ["valid-call", ""], matching_tool_call_later: true },
          { message_index: 10, tool_call_id: ERROR_CALL_ID, provider_tool_call_id: ERROR_CALL_ID, tool_name: "error_tool", previous_role: "assistant", next_role: "assistant", nearest_assistant_index: 9, nearest_assistant_stop_reason: "error", nearest_assistant_tool_call_ids: ["error-call"], matching_tool_call_later: false },
          { message_index: 12, tool_call_id: ABORTED_CALL_ID, provider_tool_call_id: ABORTED_CALL_ID, tool_name: "aborted_tool", previous_role: "assistant", next_role: "custom", nearest_assistant_index: 11, nearest_assistant_stop_reason: "aborted", nearest_assistant_tool_call_ids: ["aborted-call"], matching_tool_call_later: false },
        ]);
        const orphanText = readFileSync(orphanPath, "utf8");
        assert.equal(orphanText.includes(PRIVATE_TOOL_OUTPUT), false);
        assert.equal(orphanText.includes(PRIVATE_ERROR), false);
        assert.equal(orphanText.includes(PRIVATE_SECRET), false);
        assert.equal(orphanText.includes(PRIVATE_PROMPT), false);
        const traceRecords = readJsonLines(tracePath);
        assert.equal(mode(tracePath), 0o600);
        assert.ok(statSync(tracePath).size <= 512 * 1024);
        assert.equal(mode(join(root, "trace")), 0o700);
        const malformedTraceRecord = traceRecords.find((record) => record.event === "malformed-tool-call");
        assert.ok(malformedTraceRecord);
        assert.deepEqual(Object.keys(malformedTraceRecord).sort(), ["event", "timestamp"]);
        assert.equal(malformedTraceRecord.timestamp, FIXED_TRACE_DATE.toISOString());
        const traceText = readFileSync(tracePath, "utf8");
        assert.equal(traceText.includes(PRIVATE_TOOL_OUTPUT), false);
        assert.equal(traceText.includes(PRIVATE_ERROR), false);
        assert.equal(traceText.includes(PRIVATE_SECRET), false);
        assert.equal(traceText.includes(PRIVATE_PROMPT), false);
      }
    } else {
      assert.deepEqual(uiCalls, []);
      assert.equal(traceCalls.some(({ event }) => event === "malformed-tool-call"), false);
      assert.equal(existsSync(orphanPath), false);
      const cleanMessages = providerContexts[0] ?? [];
      assert.equal(cleanMessages.some((message) => messageHasText(message, "CLEAN_SEED_USER_DIAGNOSTICS_HISTORY")), true);
      assert.equal(cleanMessages.some((message) => messageHasText(message, "CLEAN_SEED_ASSISTANT_DIAGNOSTICS_HISTORY")), true);
      assert.equal(cleanMessages.some((message) => message.role === "toolResult"), false);
      assert.equal(providerSawOrphanFile, false);
    }
  } finally {
    // The production shutdown hook performs memory capture. This E2E deliberately rejects every
    // fetch, so dispose the session and close its fake resources without emitting that hook.
    try {
      if (session !== undefined) session.dispose();
      if (createdClient !== undefined && !clientClosed && typeof createdClient.close === "function") createdClient.close();
      if (createdCoordinator !== undefined && !coordinatorClosed && typeof createdCoordinator.close === "function") createdCoordinator.close();
      for (const interval of intervals) {
        if (!interval.cleared) clearIntervalImpl({ id: interval.id });
      }
      for (const timeout of timeouts) {
        if (!timeout.cleared) clearTimeoutImpl({ id: timeout.id });
      }
    } finally {
      process.chdir(originalCwd);
      for (const key of Object.keys(process.env)) delete process.env[key];
      Object.assign(process.env, originalEnvironment);
      if (originalFetch === undefined) delete globalThis.fetch;
      else globalThis.fetch = originalFetch;
      const globalJournal = Object.getOwnPropertyDescriptor(globalThis, Symbol.for("mecha.pi.diagnostic-journal.v1"))?.value;
      const closeJournal = globalJournal !== null && typeof globalJournal === "object" ? Reflect.get(globalJournal, "close") : undefined;
      if (typeof closeJournal === "function") await Reflect.apply(closeJournal, globalJournal, []);
      await rm(root, { recursive: true, force: true });
    }
    assert.equal(fetchCalls.length, 0, `reject-all fetch received an unexpected request during cleanup: ${JSON.stringify(fetchCalls)}`);
    assert.equal(unexpectedCalls.length, 0, JSON.stringify(unexpectedCalls));
    assert.equal(intervals.length, 1, "the extension should install one heartbeat interval");
    assert.equal(intervals.every(({ cleared }) => cleared), true, "all child-local intervals must be cleared");
    assert.equal(timeouts.every(({ cleared }) => cleared), true, "all child-local timeouts must be cleared");
    assert.equal(clientClosed, true, "fake Mailbox client was not closed");
    assert.equal(coordinatorClosed, true, "fake coordinator was not closed");
    assert.equal(timerCalls.some(({ action }) => action === "set-interval"), true);
    assert.equal(timerCalls.some(({ action }) => action === "clear-interval"), true);
  }
}

test("installed Pi reaches the diagnostics/history context hook with isolated sanitation boundaries", { timeout: 60_000 }, async (testContext) => {
  const installed = resolveInstalledPiRoot(testContext);
  if (installed === undefined) return;
  const piRoot = installed.piRoot;
  const codingEntry = join(piRoot, "dist", "index.js");
  const aiRoot = installed.aiRoot;
  const aiEntry = join(aiRoot, "dist", "index.js");
  const coding = await import(pathToFileURL(codingEntry).href);
  const ai = await import(pathToFileURL(aiEntry).href);
  assert.equal(coding.VERSION, EXPECTED_PI_VERSION);
  console.log(`installed-pi-diagnostics-e2e identity=${JSON.stringify({ pi: { name: "@earendil-works/pi-coding-agent", version: EXPECTED_PI_VERSION, root: piRoot }, ai: { name: "@earendil-works/pi-ai", version: EXPECTED_PI_VERSION, root: aiRoot }, runtime: "installed-dist", source: "checkout-inline-factory" })}`);

  const dirtyRoot = await mkdtemp(join(tmpdir(), "mecha-diagnostics-history-installed-pi-"));
  const failureRoot = await mkdtemp(join(tmpdir(), "mecha-diagnostics-history-installed-pi-failure-"));
  const cleanRoot = await mkdtemp(join(tmpdir(), "mecha-diagnostics-history-installed-pi-clean-"));
  try {
    await runScenario({ coding, ai, root: dirtyRoot, dirty: true, unusableDiagnostics: false });
    await runScenario({ coding, ai, root: failureRoot, dirty: true, unusableDiagnostics: true });
    await runScenario({ coding, ai, root: cleanRoot, dirty: false, unusableDiagnostics: false });
  } finally {
    await rm(dirtyRoot, { recursive: true, force: true });
    await rm(failureRoot, { recursive: true, force: true });
    await rm(cleanRoot, { recursive: true, force: true });
  }
});
