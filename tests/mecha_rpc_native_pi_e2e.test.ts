import assert from "node:assert/strict";
import { execFileSync, spawn } from "node:child_process";
import { createHash, randomUUID } from "node:crypto";
import { existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, realpathSync, rmSync, statSync, writeFileSync } from "node:fs";
import { createServer, type ServerResponse } from "node:http";
import { tmpdir } from "node:os";
import { isAbsolute, join, relative, resolve } from "node:path";
import { createInterface } from "node:readline";
import test from "node:test";

import { hashCanonicalJson } from "../pi-extensions/mecha-rpc/manifest.ts";
import { CUSTOM_MESSAGE_TYPES, encodeHiddenEnvelope } from "../pi-extensions/mecha-rpc/protocol.ts";

const ROOT = resolve(import.meta.dirname, "..");
const EXTENSION = join(ROOT, "pi-extensions/mecha-rpc/index.ts");
const SUBAGENTS_EXTENSION = "/Users/wolfgang/code/pi-packages/packages/pi-subagents/src/index.ts";
const REGISTRATION_PROBE = join(ROOT, "tests/fixtures/mecha-rpc/stock-pi-registration-probe.ts");
const TRANSITION_PROBE = join(ROOT, "tests/fixtures/mecha-rpc/stock-pi-transition-probe.ts");
const AC090_PROBE = join(ROOT, "tests/fixtures/mecha-rpc/stock-pi-ac090-probe.ts");
const G4_EXPLICIT_ENTRY_ID_PROBE = join(ROOT, "tests/fixtures/mecha-rpc/g4-explicit-entry-id-probe.ts");
const RUNTIME_GENERATION = "123e4567-e89b-42d3-a456-426614174000";
const ACCEPTANCE_EVIDENCE = join(ROOT, "tests/fixtures/mecha-rpc/acceptance-evidence-v1.json");
const ACCEPTANCE_ARTIFACT_DIR = "/tmp/mecha-rpc-acceptance";
const EXPECTED_COMMANDS = new Set([
  "mecha-bootstrap-v1", "mecha-install-tools-v1", "mecha-control-result-v1", "mecha-mailbox-deliver-v1", "mecha-policy-apply-v1", "mecha-child-snapshot-v1", "mecha-post-compaction-notification-v1",
  "goal", "goal-lock", "goal-snooze", "goal-snooze-turn-limit", "goal-snooze-budget", "goal-snooze-budget-refill", "mecha-parent-message", "inline-tools", "mecha-reply",
]);

type JsonRecord = Record<string, unknown>;
type PendingResponse = { resolve: (value: JsonRecord) => void; reject: (reason: Error) => void };

type PiSourceLock = {
  schema: "mecha.pi.g4-source-lock/v1";
  pi: {
    source_root: string;
    source_remote: string;
    public_base: { commit: string; tree: string };
    scoped: {
      commit: string;
      tree: string;
      merge_base: string;
      clean: boolean;
      changed_paths: string[];
      package_lock_sha256: string;
      pnpm_lock_path: null;
      pnpm_lock_sha256: null;
      package_name: string;
      package_version: string;
    };
    tarball: { path: string; sha256: string };
    installed: { package_real_path: string; executable_real_path: string; executable_sha256: string; package_version: string };
  };
  pi_packages: { path: string; commit: string; tree: string };
};

type LockedPiTarget = {
  bin: string;
  packageRoot: string;
  sourceLock: PiSourceLock;
  version: string;
};

function sha256File(path: string): string {
  return createHash("sha256").update(readFileSync(path)).digest("hex");
}

function requireEnvironmentPath(name: "MECHA_PI_BIN" | "MECHA_PI_PACKAGE_ROOT" | "MECHA_PI_SOURCE_LOCK"): string {
  const value = process.env[name];
  if (value === undefined || value.length === 0 || !isAbsolute(value)) throw new Error(`${name} must be an absolute path`);
  return value;
}

function asRecord(value: unknown, message: string): JsonRecord {
  if (value === null || typeof value !== "object" || Array.isArray(value)) throw new Error(message);
  return value as JsonRecord;
}

function readSourceLock(path: string): PiSourceLock {
  const lock = JSON.parse(readFileSync(path, "utf8")) as unknown;
  const root = asRecord(lock, "MECHA_PI_SOURCE_LOCK must contain an object");
  if (root.schema !== "mecha.pi.g4-source-lock/v1") throw new Error("MECHA_PI_SOURCE_LOCK schema is invalid");
  return lock as PiSourceLock;
}

function commandOutput(command: string, arguments_: readonly string[], cwd = ROOT): string {
  return execFileSync(command, arguments_, { cwd, encoding: "utf8" }).trim();
}

function resolveLockedPiTarget(): LockedPiTarget {
  const sourceLockPath = requireEnvironmentPath("MECHA_PI_SOURCE_LOCK");
  const packageRoot = realpathSync(requireEnvironmentPath("MECHA_PI_PACKAGE_ROOT"));
  const bin = realpathSync(requireEnvironmentPath("MECHA_PI_BIN"));
  const lock = readSourceLock(sourceLockPath);
  const packageJson = asRecord(JSON.parse(readFileSync(join(packageRoot, "package.json"), "utf8")) as unknown, "Pi package manifest is invalid");
  if (packageJson.name !== "@earendil-works/pi-coding-agent" || packageJson.version !== "0.85.0") {
    throw new Error("MECHA_PI_PACKAGE_ROOT is not the locked Pi 0.85.0 package");
  }
  const packageRelativeBin = relative(packageRoot, bin);
  if (packageRelativeBin.length === 0 || packageRelativeBin.startsWith("..") || isAbsolute(packageRelativeBin)) {
    throw new Error("MECHA_PI_BIN must resolve within MECHA_PI_PACKAGE_ROOT");
  }
  if (!statSync(bin).isFile()) throw new Error("MECHA_PI_BIN must resolve to an executable file");
  const sourceRoot = realpathSync(lock.pi.source_root);
  const packageLockPath = join(sourceRoot, "package-lock.json");
  if (lock.pi.public_base.commit !== "107d79f11072bbc8a3a757ed7fd69596bee7d68c" || lock.pi.public_base.tree !== "f5103239060686ea2983e18906857b3df54428f5") {
    throw new Error("Pi source lock does not use public Pi v0.85.0");
  }
  if (lock.pi.scoped.merge_base !== lock.pi.public_base.commit || !lock.pi.scoped.clean) {
    throw new Error("Pi source lock is not a clean scoped branch from public Pi v0.85.0");
  }
  if (existsSync(join(sourceRoot, "pnpm-lock.yaml")) || lock.pi.scoped.pnpm_lock_path !== null || lock.pi.scoped.pnpm_lock_sha256 !== null) {
    throw new Error("Pi v0.85.0 source lock must record the package-lock-only checkout");
  }
  if (sha256File(packageLockPath) !== lock.pi.scoped.package_lock_sha256) throw new Error("Pi package-lock hash mismatch");
  const changedPaths = commandOutput("git", ["-C", sourceRoot, "diff", "--name-only", `${lock.pi.public_base.commit}..${lock.pi.scoped.commit}`]).split("\n").filter(Boolean);
  if (JSON.stringify(changedPaths) !== JSON.stringify(lock.pi.scoped.changed_paths) || changedPaths.some((path) => !path.startsWith("packages/coding-agent/src/") && !path.startsWith("packages/coding-agent/test/"))) {
    throw new Error("Pi source lock has paths outside the G4 Pi scope");
  }
  if (
    commandOutput("git", ["-C", sourceRoot, "rev-parse", "HEAD"]) !== lock.pi.scoped.commit ||
    commandOutput("git", ["-C", sourceRoot, "rev-parse", "HEAD^{tree}"]) !== lock.pi.scoped.tree ||
    commandOutput("git", ["-C", sourceRoot, "merge-base", "HEAD", lock.pi.public_base.commit]) !== lock.pi.public_base.commit ||
    commandOutput("git", ["-C", sourceRoot, "config", "--get", "remote.origin.url"]) !== lock.pi.source_remote ||
    commandOutput("git", ["-C", sourceRoot, "status", "--porcelain"]) !== ""
  ) throw new Error("Pi source lock does not match its source checkout");
  const piPackagesPath = realpathSync(lock.pi_packages.path);
  if (
    lock.pi_packages.commit !== "06ed583c720ed0fe6d6cc099b26eeb384795ea39" ||
    lock.pi_packages.tree !== "18aa55133ecd46fc7eb9a4a3b9265e8ec247863e" ||
    commandOutput("git", ["-C", piPackagesPath, "rev-parse", "HEAD"]) !== lock.pi_packages.commit ||
    commandOutput("git", ["-C", piPackagesPath, "rev-parse", "HEAD^{tree}"]) !== lock.pi_packages.tree ||
    realpathSync(SUBAGENTS_EXTENSION) !== join(piPackagesPath, "packages/pi-subagents/src/index.ts")
  ) throw new Error("Pi subagents compatibility checkout does not match the authoritative G4 source");
  if (
    sha256File(lock.pi.tarball.path) !== lock.pi.tarball.sha256 ||
    packageRoot !== lock.pi.installed.package_real_path ||
    bin !== lock.pi.installed.executable_real_path ||
    sha256File(bin) !== lock.pi.installed.executable_sha256 ||
    packageJson.version !== lock.pi.installed.package_version
  ) throw new Error("Installed Pi package does not match the source lock");
  return { bin, packageRoot, sourceLock: lock, version: commandOutput(bin, ["--version"]) };
}

const PI_TARGET = resolveLockedPiTarget();

class StockPiRpc {
  readonly events: JsonRecord[] = [];
  private readonly pending = new Map<string, PendingResponse>();
  private nextId = 1;
  private readonly stderr: string[] = [];
  private readonly process;
  private readonly exited: Promise<number | null>;

  constructor(home: string, baseUrl: string, options: {
    persistSession?: boolean;
    runtimeGeneration?: string;
    sessionPath?: string;
    extensions?: readonly string[];
    probeMode?: string;
    probeName?: string;
    testCommandFailureAt?: number;
  } = {}) {
    configureDeterministicProvider(home, baseUrl);
    const persistSession = options.persistSession === true;
    const runtimeGeneration = options.runtimeGeneration ?? RUNTIME_GENERATION;
    const sessionArgs = options.sessionPath === undefined
      ? persistSession ? ["--session-dir", join(home, "sessions")] : ["--no-session"]
      : ["--session", options.sessionPath];
    const extensions = options.extensions ?? [EXTENSION, SUBAGENTS_EXTENSION];
    this.process = spawn(PI_TARGET.bin, [
      "--mode", "rpc", ...sessionArgs, "--no-extensions", "--no-skills", "--no-prompt-templates", "--no-themes", "--no-context-files", "--no-builtin-tools", "--approve", "--offline",
      "--provider", "mecha-router-test", "--model", "deterministic-model",
      ...extensions.flatMap((extension) => ["-e", extension]),
    ], {
      cwd: ROOT,
      env: {
        ...process.env,
        PI_CODING_AGENT_DIR: join(home, ".pi", "agent"),
        TMPDIR: join(home, "tmp"),
        MECHA_RPC_RUNTIME_GENERATION: runtimeGeneration,
        MECHA_RPC_SESSION_EPOCH: "1",
        MECHA_TEST_BASE_URL: baseUrl,
        PI_OFFLINE: "1",
        ...(options.probeMode === undefined ? {} : { MECHA_RPC_TEST_PROBE_MODE: options.probeMode }),
        ...(options.probeName === undefined ? {} : { MECHA_RPC_TEST_PROBE_NAME: options.probeName }),
        ...(options.testCommandFailureAt === undefined ? {} : { MECHA_RPC_TEST_FAIL_COMMAND_REGISTRATION_AT: String(options.testCommandFailureAt) }),
      },
      stdio: ["pipe", "pipe", "pipe"],
    });
    createInterface({ input: this.process.stdout }).on("line", (line) => this.receive(line));
    createInterface({ input: this.process.stderr }).on("line", (line) => this.stderr.push(line));
    this.process.once("error", (error) => this.rejectAll(error));
    this.exited = new Promise((resolveExit) => {
      this.process.once("exit", (code) => {
        resolveExit(code);
        this.rejectAll(new Error(`stock Pi exited with ${String(code)}: ${this.stderr.join("\n")}`));
      });
    });
  }

  request(command: JsonRecord): Promise<JsonRecord> {
    const id = `request-${this.nextId++}`;
    return new Promise<JsonRecord>((resolveResponse, reject) => {
      this.pending.set(id, { resolve: resolveResponse, reject });
      this.process.stdin.write(`${JSON.stringify({ id, ...command })}\n`);
    });
  }

  diagnostics(): string {
    return this.stderr.join("\n");
  }

  async waitForExit(timeoutMs = 10_000): Promise<number | null> {
    return await Promise.race([
      this.exited,
      new Promise<never>((_resolve, reject) => setTimeout(() => reject(new Error(`Timed out waiting for stock Pi exit: ${this.diagnostics()}`)), timeoutMs)),
    ]);
  }

  async stop(): Promise<void> {
    this.process.stdin.end();
    if (this.process.exitCode !== null) return;
    await new Promise<void>((resolveStop) => {
      const timeout = setTimeout(() => {
        this.process.kill("SIGTERM");
        resolveStop();
      }, 3_000);
      this.process.once("exit", () => {
        clearTimeout(timeout);
        resolveStop();
      });
    });
  }

  private receive(line: string): void {
    let message: JsonRecord;
    try {
      const parsed: unknown = JSON.parse(line);
      if (parsed === null || typeof parsed !== "object" || Array.isArray(parsed)) return;
      message = parsed as JsonRecord;
    } catch {
      return;
    }
    const id = message.id;
    if (typeof id === "string" && message.type === "response") {
      const pending = this.pending.get(id);
      if (pending !== undefined) {
        this.pending.delete(id);
        pending.resolve(message);
        return;
      }
    }
    this.events.push(message);
  }

  private rejectAll(error: Error): void {
    for (const pending of this.pending.values()) pending.reject(error);
    this.pending.clear();
  }
}

function configureDeterministicProvider(home: string, baseUrl: string): void {
  const agentDir = join(home, ".pi", "agent");
  mkdirSync(agentDir, { recursive: true });
  writeFileSync(join(agentDir, "models.json"), JSON.stringify({
    providers: {
      "mecha-router-test": {
        baseUrl,
        api: "openai-completions",
        apiKey: "deterministic-test-key",
        models: [{
          id: "deterministic-model",
          name: "Deterministic model",
          reasoning: false,
          input: ["text"],
          cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
          contextWindow: 16_384,
          maxTokens: 1_024,
        }],
      },
    },
  }), "utf8");
}

async function startDeterministicProvider(options: { firstToolCall?: boolean } = {}): Promise<{ baseUrl: string; requests: JsonRecord[]; stop: () => Promise<void> }> {
  const requests: JsonRecord[] = [];
  const server = createServer(async (request, response) => {
    if (request.method !== "POST" || request.url !== "/v1/chat/completions") {
      response.writeHead(404).end();
      return;
    }
    const chunks: Buffer[] = [];
    for await (const chunk of request) chunks.push(Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk));
    const parsed: unknown = JSON.parse(Buffer.concat(chunks).toString("utf8"));
    if (parsed !== null && typeof parsed === "object" && !Array.isArray(parsed)) requests.push(parsed as JsonRecord);
    response.writeHead(200, { "content-type": "text/event-stream", "cache-control": "no-cache" });
    if (requests.length === 1 && options.firstToolCall !== false) {
      response.write(`data: ${JSON.stringify({ id: "deterministic", model: "deterministic-model", choices: [{ index: 0, delta: { role: "assistant", tool_calls: [{ index: 0, id: "raw-goal-call", type: "function", function: { name: "goal", arguments: JSON.stringify({ action: "status" }) } }] }, finish_reason: null }] })}\n\n`);
      response.write(`data: ${JSON.stringify({ id: "deterministic", model: "deterministic-model", choices: [{ index: 0, delta: {}, finish_reason: "tool_calls" }] })}\n\n`);
    } else {
      response.write(`data: ${JSON.stringify({ id: "deterministic", model: "deterministic-model", choices: [{ index: 0, delta: { role: "assistant", content: "Managed tools are available." }, finish_reason: null }] })}\n\n`);
      response.write(`data: ${JSON.stringify({ id: "deterministic", model: "deterministic-model", choices: [{ index: 0, delta: {}, finish_reason: "stop" }] })}\n\n`);
    }
    response.end("data: [DONE]\n\n");
  });
  await new Promise<void>((resolveListen, rejectListen) => {
    server.once("error", rejectListen);
    server.listen(0, "127.0.0.1", resolveListen);
  });
  const address = server.address();
  if (address === null || typeof address === "string") throw new Error("deterministic provider did not bind a TCP port");
  return {
    baseUrl: `http://127.0.0.1:${address.port}/v1`,
    requests,
    stop: () => new Promise<void>((resolveStop, rejectStop) => server.close((error) => error === undefined ? resolveStop() : rejectStop(error))),
  };
}

type ChildHandoffProvider = {
  baseUrl: string;
  requests: JsonRecord[];
  childRequests: JsonRecord[];
  releaseChildTurn: () => void;
  stop: () => Promise<void>;
};

function requestToolNames(request: JsonRecord): Set<string> {
  const tools = request.tools;
  if (!Array.isArray(tools)) return new Set();
  return new Set(tools.flatMap((tool) => {
    if (tool === null || typeof tool !== "object" || Array.isArray(tool)) return [];
    const definition = (tool as JsonRecord).function;
    return definition !== null && typeof definition === "object" && !Array.isArray(definition)
      && typeof (definition as JsonRecord).name === "string"
      ? [(definition as JsonRecord).name as string]
      : [];
  }));
}

function streamCompletion(response: ServerResponse, message: JsonRecord): void {
  response.writeHead(200, { "content-type": "text/event-stream", "cache-control": "no-cache" });
  response.write(`data: ${JSON.stringify({ id: "deterministic", model: "deterministic-model", choices: [{ index: 0, delta: message, finish_reason: null }] })}\n\n`);
  response.write(`data: ${JSON.stringify({ id: "deterministic", model: "deterministic-model", choices: [{ index: 0, delta: {}, finish_reason: "stop" }] })}\n\n`);
  response.end("data: [DONE]\n\n");
}

function streamToolCall(response: ServerResponse, id: string, name: string, argumentsValue: JsonRecord): void {
  response.writeHead(200, { "content-type": "text/event-stream", "cache-control": "no-cache" });
  response.write(`data: ${JSON.stringify({ id: "deterministic", model: "deterministic-model", choices: [{ index: 0, delta: { role: "assistant", tool_calls: [{ index: 0, id, type: "function", function: { name, arguments: JSON.stringify(argumentsValue) } }] }, finish_reason: null }] })}\n\n`);
  response.write(`data: ${JSON.stringify({ id: "deterministic", model: "deterministic-model", choices: [{ index: 0, delta: {}, finish_reason: "tool_calls" }] })}\n\n`);
  response.end("data: [DONE]\n\n");
}

type HeldRootProvider = { baseUrl: string; requests: JsonRecord[]; release: () => void; stop: () => Promise<void> };

async function startHeldRootProvider(): Promise<HeldRootProvider> {
  const requests: JsonRecord[] = [];
  let releaseHeldResponse: (() => void) | undefined;
  const server = createServer(async (request, response) => {
    if (request.method !== "POST" || request.url !== "/v1/chat/completions") {
      response.writeHead(404).end();
      return;
    }
    const chunks: Buffer[] = [];
    for await (const chunk of request) chunks.push(Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk));
    const parsed: unknown = JSON.parse(Buffer.concat(chunks).toString("utf8"));
    if (parsed === null || typeof parsed !== "object" || Array.isArray(parsed)) {
      response.writeHead(400).end();
      return;
    }
    requests.push(parsed as JsonRecord);
    if (requests.length === 1) {
      streamToolCall(response, "held-root-goal", "goal", { action: "status" });
      return;
    }
    releaseHeldResponse = () => streamCompletion(response, { role: "assistant", content: "Held root turn complete." });
  });
  await new Promise<void>((resolveListen, rejectListen) => {
    server.once("error", rejectListen);
    server.listen(0, "127.0.0.1", resolveListen);
  });
  const address = server.address();
  if (address === null || typeof address === "string") throw new Error("held root provider did not bind a TCP port");
  return {
    baseUrl: `http://127.0.0.1:${address.port}/v1`,
    requests,
    release() {
      if (releaseHeldResponse === undefined) throw new Error("held root continuation is not waiting for a control result");
      const release = releaseHeldResponse;
      releaseHeldResponse = undefined;
      release();
    },
    stop: () => new Promise<void>((resolveStop, rejectStop) => server.close((error) => error === undefined ? resolveStop() : rejectStop(error))),
  };
}

async function startChildHandoffProvider(): Promise<ChildHandoffProvider> {
  const requests: JsonRecord[] = [];
  const childRequests: JsonRecord[] = [];
  let parentSpawned = false;
  let childGoalSent = false;
  let releaseHeldChildTurn: (() => void) | undefined;
  const server = createServer(async (request, response) => {
    if (request.method !== "POST" || request.url !== "/v1/chat/completions") {
      response.writeHead(404).end();
      return;
    }
    const chunks: Buffer[] = [];
    for await (const chunk of request) chunks.push(Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk));
    const parsed: unknown = JSON.parse(Buffer.concat(chunks).toString("utf8"));
    if (parsed === null || typeof parsed !== "object" || Array.isArray(parsed)) {
      response.writeHead(400).end();
      return;
    }
    const payload = parsed as JsonRecord;
    requests.push(payload);
    const names = requestToolNames(payload);
    if (names.has("subagent")) {
      if (!parentSpawned) {
        parentSpawned = true;
        streamToolCall(response, "spawn-stock-child", "subagent", {
          prompt: "Use goal status, then wait for the control result.",
          description: "Managed child handoff",
          subagent_type: "general-purpose",
        });
      } else {
        streamCompletion(response, { role: "assistant", content: "Parent turn complete." });
      }
      return;
    }

    childRequests.push(payload);
    if (!childGoalSent) {
      childGoalSent = true;
      streamToolCall(response, "child-managed-goal", "goal", { action: "status" });
      return;
    }
    releaseHeldChildTurn = () => streamCompletion(response, { role: "assistant", content: "Child turn complete." });
  });
  await new Promise<void>((resolveListen, rejectListen) => {
    server.once("error", rejectListen);
    server.listen(0, "127.0.0.1", resolveListen);
  });
  const address = server.address();
  if (address === null || typeof address === "string") throw new Error("child handoff provider did not bind a TCP port");
  return {
    baseUrl: `http://127.0.0.1:${address.port}/v1`,
    requests,
    childRequests,
    releaseChildTurn() {
      if (releaseHeldChildTurn === undefined) throw new Error("child continuation is not waiting for its control result");
      const release = releaseHeldChildTurn;
      releaseHeldChildTurn = undefined;
      release();
    },
    stop: () => new Promise<void>((resolveStop, rejectStop) => server.close((error) => error === undefined ? resolveStop() : rejectStop(error))),
  };
}

function recordsIn(directory: string): JsonRecord[] {
  if (!existsSync(directory)) return [];
  const files: string[] = [];
  const collect = (path: string): void => {
    for (const entry of readdirSync(path)) {
      const candidate = join(path, entry);
      if (statSync(candidate).isDirectory()) collect(candidate);
      else if (candidate.endsWith(".jsonl")) files.push(candidate);
    }
  };
  collect(directory);
  return files.flatMap((file) => readFileSync(file, "utf8").split("\n").flatMap((line) => {
    if (line.length === 0) return [];
    try {
      const parsed: unknown = JSON.parse(line);
      return parsed !== null && typeof parsed === "object" && !Array.isArray(parsed) ? [parsed as JsonRecord] : [];
    } catch {
      return [];
    }
  }));
}

function customRecords(records: JsonRecord[], customType: string): JsonRecord[] {
  return records.flatMap((entry) => {
    if (entry.customType === customType) return [entry];
    const message = entry.message;
    return message !== null && typeof message === "object" && !Array.isArray(message) && (message as JsonRecord).customType === customType
      ? [message as JsonRecord]
      : [];
  });
}

async function waitFor<T>(probe: () => T | undefined, description: string, timeoutMs = 10_000): Promise<T> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const value = probe();
    if (value !== undefined) return value;
    await new Promise((resolveDelay) => setTimeout(resolveDelay, 20));
  }
  throw new Error(`Timed out waiting for ${description}`);
}

function readManifest(): JsonRecord {
  return JSON.parse(readFileSync(join(ROOT, "router/contract/managed-tools-v1.json"), "utf8")) as JsonRecord;
}

function supplementalManifest(base: JsonRecord, capabilityId = "zz_stock_supplemental"): JsonRecord {
  const desired = structuredClone(base);
  const tools = desired.tools;
  if (!Array.isArray(tools)) throw new Error("managed manifest tools are unavailable");
  const definition = {
    capability_id: capabilityId,
    label: "Stock supplemental tool",
    description: "A bounded stock-Pi supplemental definition.",
    parameters: { type: "object", additionalProperties: false, required: [], properties: {} },
    operation_id: "stock.supplemental",
  };
  const definitionSha = hashCanonicalJson(definition);
  tools.push({
    ...definition,
    name: `${definition.capability_id}__${definitionSha}`,
    definition_id: `def_${definitionSha}`,
    definition_sha256: definitionSha,
  });
  tools.sort((left, right) => {
    const leftCapability = left !== null && typeof left === "object" && !Array.isArray(left) ? (left as JsonRecord).capability_id : "";
    const rightCapability = right !== null && typeof right === "object" && !Array.isArray(right) ? (right as JsonRecord).capability_id : "";
    return Buffer.compare(Buffer.from(String(leftCapability), "utf8"), Buffer.from(String(rightCapability), "utf8"));
  });
  desired.revision = 2;
  return desired;
}

function changedDefinitionManifest(base: JsonRecord): JsonRecord {
  const desired = structuredClone(base);
  const tools = desired.tools;
  if (!Array.isArray(tools)) throw new Error("managed manifest tools are unavailable");
  const original = tools.find((tool) => tool !== null && typeof tool === "object" && !Array.isArray(tool)
    && (tool as JsonRecord).capability_id === "goal");
  if (original === undefined || original === null || typeof original !== "object" || Array.isArray(original)) throw new Error("goal definition is unavailable");
  const tool = original as JsonRecord;
  const definition: JsonRecord = {
    capability_id: tool.capability_id,
    label: tool.label,
    description: "Changed goal definition for the fresh stock-Pi generation.",
    parameters: tool.parameters,
    operation_id: tool.operation_id,
    ...(tool.prompt_snippet === undefined ? {} : { prompt_snippet: tool.prompt_snippet }),
    ...(tool.prompt_guidelines === undefined ? {} : { prompt_guidelines: tool.prompt_guidelines }),
  };
  const definitionSha = hashCanonicalJson(definition);
  Object.assign(tool, {
    ...definition,
    name: `goal__${definitionSha}`,
    definition_id: `def_${definitionSha}`,
    definition_sha256: definitionSha,
  });
  desired.revision = 2;
  return desired;
}

type AcceptanceProvenance = {
  readonly candidate: {
    readonly mecha_developer_head: string;
    readonly pi_subagents_head: string;
    readonly installed_pi_version: string;
  };
  readonly run: { readonly run_id: string; readonly started_at: string };
  readonly directory: string;
};

function acceptanceProvenance(): AcceptanceProvenance {
  const actual = {
    mecha_developer_head: commandOutput("git", ["rev-parse", "HEAD"]),
    pi_subagents_head: commandOutput("git", ["-C", resolve(ROOT, "../pi-packages"), "rev-parse", "HEAD"]),
    installed_pi_version: commandOutput(PI_TARGET.bin, ["--version"]),
  };
  const supplied = {
    mecha_developer_head: process.env.MECHA_RPC_ACCEPTANCE_MECHA_HEAD,
    pi_subagents_head: process.env.MECHA_RPC_ACCEPTANCE_FORK_HEAD,
    installed_pi_version: process.env.MECHA_RPC_ACCEPTANCE_PI_VERSION,
    run_id: process.env.MECHA_RPC_ACCEPTANCE_RUN_ID,
    started_at: process.env.MECHA_RPC_ACCEPTANCE_STARTED_AT,
    directory: process.env.MECHA_RPC_ACCEPTANCE_RUN_DIR,
  };
  const suppliedValues = Object.values(supplied);
  if (suppliedValues.some((value) => value !== undefined) && suppliedValues.some((value) => value === undefined || value === "")) {
    throw new Error("Phase 1 acceptance evidence requires a complete run identity");
  }
  if (supplied.run_id !== undefined) {
    assert.deepEqual({
      mecha_developer_head: supplied.mecha_developer_head,
      pi_subagents_head: supplied.pi_subagents_head,
      installed_pi_version: supplied.installed_pi_version,
    }, actual, "acceptance runner identity must match the sources and Pi actually exercised");
    return {
      candidate: actual,
      run: { run_id: supplied.run_id, started_at: supplied.started_at! },
      directory: supplied.directory!,
    };
  }
  const runId = randomUUID();
  return {
    candidate: actual,
    run: { run_id: runId, started_at: new Date().toISOString() },
    directory: join(ACCEPTANCE_ARTIFACT_DIR, actual.mecha_developer_head, runId),
  };
}

const ACCEPTANCE_PROVENANCE = acceptanceProvenance();

function writeAcceptanceArtifact(name: string, evidence: JsonRecord): void {
  mkdirSync(ACCEPTANCE_PROVENANCE.directory, { recursive: true });
  writeFileSync(join(ACCEPTANCE_PROVENANCE.directory, name), `${JSON.stringify({
    protocol: "mecha.phase1-stock-proof/v2",
    artifact_id: name,
    status: "passed",
    generated_at: new Date().toISOString(),
    candidate: ACCEPTANCE_PROVENANCE.candidate,
    run: ACCEPTANCE_PROVENANCE.run,
    evidence,
  }, null, 2)}\n`, "utf8");
}

function successful(response: JsonRecord): void {
  assert.equal(response.success, true, JSON.stringify(response));
}

async function bootstrapCurrentManifest(rpc: StockPiRpc, desired: JsonRecord, generation: string, deliveryId: string): Promise<void> {
  successful(await rpc.request({
    type: "prompt",
    message: `/mecha-bootstrap-v1 ${encodeHiddenEnvelope({
      protocol: "mecha.bootstrap/v1", delivery_id: deliveryId, runtime_generation: generation, session_epoch: 1,
      desired_manifest: desired, manifest_sha256: hashCanonicalJson(desired), restored_request_ids: [],
      inline_policy: { enabled: false, credits: {} }, recall_projection: { content: "" },
    })}`,
  }));
}

function sessionFileFromState(response: JsonRecord): string {
  const data = response.data;
  if (data === null || typeof data !== "object" || Array.isArray(data) || typeof (data as JsonRecord).sessionFile !== "string") {
    throw new Error(`stock Pi did not return a persistent session file: ${JSON.stringify(response)}`);
  }
  return (data as JsonRecord).sessionFile as string;
}

function latestUserEntryId(response: JsonRecord): string {
  const data = response.data;
  if (data === null || typeof data !== "object" || Array.isArray(data) || !Array.isArray((data as JsonRecord).entries)) {
    throw new Error(`stock Pi did not return session entries: ${JSON.stringify(response)}`);
  }
  const entries = (data as JsonRecord).entries as unknown[];
  for (const entry of entries.toReversed()) {
    if (entry === null || typeof entry !== "object" || Array.isArray(entry)) continue;
    const message = (entry as JsonRecord).message;
    if (message !== null && typeof message === "object" && !Array.isArray(message)
      && (message as JsonRecord).role === "user" && typeof (entry as JsonRecord).id === "string") {
      return (entry as JsonRecord).id as string;
    }
  }
  throw new Error("stock Pi session has no user entry suitable for a fork");
}

function customMessages(events: JsonRecord[], customType: string): JsonRecord[] {
  return events.filter((event) => {
    if (event.type !== "message_end") return false;
    const message = event.message;
    return message !== null && typeof message === "object" && !Array.isArray(message)
      && (message as JsonRecord).customType === customType;
  });
}

test("locked Pi G4 persists deterministic root results through get_entry and preserves stock RPC behavior", { timeout: 45_000 }, async () => {
  const home = mkdtempSync(join(tmpdir(), "mecha-rpc-g4-"));
  mkdirSync(join(home, "tmp"), { recursive: true });
  const provider = await startHeldRootProvider();
  const rootResultId = "123e4567-e89b-42d3-a456-426614174050";
  const rootEntryId = `mcr1_${rootResultId.replaceAll("-", "")}`;
  const directEntryId = "mcr1_123e4567e89b42d3a456426614174051";
  let rpc = new StockPiRpc(home, provider.baseUrl, {
    persistSession: true,
    extensions: [EXTENSION, SUBAGENTS_EXTENSION, G4_EXPLICIT_ENTRY_ID_PROBE],
  });
  try {
    const manifest = readManifest();
    const manifestHash = hashCanonicalJson(manifest);
    await bootstrapCurrentManifest(rpc, manifest, RUNTIME_GENERATION, "123e4567-e89b-42d3-a456-426614174049");

    const beforeRootAppend = await rpc.request({ type: "get_entry", entryId: rootEntryId });
    successful(beforeRootAppend);
    assert.deepEqual(beforeRootAppend.data, { entry: null });

    const rootTurn = rpc.request({ type: "prompt", message: "Queue a managed root result." });
    const requestEvent = await waitFor(() => customMessages(rpc.events, "mecha.control.request.v1").at(-1), "root control request");
    const requestDetails = (requestEvent.message as JsonRecord).details;
    assert.ok(requestDetails !== null && typeof requestDetails === "object" && !Array.isArray(requestDetails));
    await waitFor(() => provider.requests.length >= 2 ? true : undefined, "held root continuation");

    const resultEnvelope = encodeHiddenEnvelope({
      protocol: "mecha.control/v1", result_id: rootResultId,
      request_id: (requestDetails as JsonRecord).request_id,
      target_session_epoch: 1, runtime_generation: RUNTIME_GENERATION, manifest_sha256: manifestHash,
      status: "ok", content: "Held root result persisted.", details: { source: "g4-native" },
    });
    const appendDuringStream = rpc.request({ type: "prompt", message: `/mecha-control-result-v1 ${resultEnvelope}` });
    await new Promise((resolveDelay) => setTimeout(resolveDelay, 50));
    assert.equal(rpc.events.filter((event) => event.type === "message_end" && event.entryId === rootEntryId).length, 0);
    const beforeRelease = await rpc.request({ type: "get_entry", entryId: rootEntryId });
    successful(beforeRelease);
    assert.deepEqual(beforeRelease.data, { entry: null });

    provider.release();
    successful(await appendDuringStream);
    successful(await rootTurn);
    const rootLookup = await rpc.request({ type: "get_entry", entryId: rootEntryId });
    successful(rootLookup);
    const rootData = rootLookup.data;
    assert.ok(rootData !== null && typeof rootData === "object" && !Array.isArray(rootData));
    const rootEntry = (rootData as JsonRecord).entry;
    assert.ok(rootEntry !== null && typeof rootEntry === "object" && !Array.isArray(rootEntry));
    assert.deepEqual({
      type: (rootEntry as JsonRecord).type,
      id: (rootEntry as JsonRecord).id,
      customType: (rootEntry as JsonRecord).customType,
      content: (rootEntry as JsonRecord).content,
      display: (rootEntry as JsonRecord).display,
      details: (rootEntry as JsonRecord).details,
    }, {
      type: "custom_message", id: rootEntryId, customType: "mecha.control.result.v1",
      content: "Held root result persisted.", display: false,
      details: {
        protocol: "mecha.control/v1", result_id: rootResultId,
        request_id: (requestDetails as JsonRecord).request_id,
        target_session_epoch: 1, runtime_generation: RUNTIME_GENERATION, manifest_sha256: manifestHash,
        status: "ok", details: { source: "g4-native" },
      },
    });
    assert.equal(JSON.stringify(rootEntry).includes(rootEntryId), true);
    assert.equal(JSON.stringify((rootEntry as JsonRecord).details).includes(rootEntryId), false);
    assert.equal(rpc.events.filter((event) => event.type === "message_end" && event.entryId === rootEntryId).length, 1);

    successful(await rpc.request({ type: "prompt", message: `/mecha-control-result-v1 ${resultEnvelope}` }));
    assert.equal(rpc.events.filter((event) => event.type === "message_end" && event.entryId === rootEntryId).length, 1);

    for (const mode of ["first", "duplicate", "changed"]) {
      successful(await rpc.request({ type: "prompt", message: `/g4-explicit-entry-id-probe ${directEntryId} ${mode}` }));
      await waitFor(() => customMessages(rpc.events, "g4.explicit-entry-id-probe.v1").find((event) => {
        const details = (event.message as JsonRecord).details;
        return details !== null && typeof details === "object" && !Array.isArray(details)
          && (details as JsonRecord).entry_id === directEntryId && (details as JsonRecord).mode === mode ? event : undefined;
      }), `direct Pi seam ${mode}`);
    }
    const probeEvents = customMessages(rpc.events, "g4.explicit-entry-id-probe.v1").map((event) => (event.message as JsonRecord).details as JsonRecord)
      .filter((details) => details.entry_id === directEntryId);
    assert.deepEqual(probeEvents.map((details) => ({ mode: details.mode, outcome: details.outcome_kind, rejected: details.rejected })), [
      { mode: "first", outcome: "accepted", rejected: false },
      { mode: "duplicate", outcome: "already_present", rejected: false },
      { mode: "changed", outcome: undefined, rejected: true },
    ]);
    const directLookup = await rpc.request({ type: "get_entry", entryId: directEntryId });
    successful(directLookup);
    assert.ok((directLookup.data as JsonRecord).entry !== null);
    assert.equal(rpc.events.filter((event) => event.type === "message_end" && event.entryId === directEntryId).length, 1);

    const sessionPath = sessionFileFromState(await rpc.request({ type: "get_state" }));
    await rpc.stop();
    rpc = new StockPiRpc(home, provider.baseUrl, {
      sessionPath,
      extensions: [EXTENSION, SUBAGENTS_EXTENSION, G4_EXPLICIT_ENTRY_ID_PROBE],
    });
    const resumedLookup = await rpc.request({ type: "get_entry", entryId: rootEntryId });
    successful(resumedLookup);
    assert.deepEqual(resumedLookup.data, rootLookup.data);
    successful(await rpc.request({ type: "get_state" }));
    successful(await rpc.request({ type: "abort" }));
    const entriesResponse = await rpc.request({ type: "get_entries" });
    successful(entriesResponse);
    assert.ok(Array.isArray((entriesResponse.data as JsonRecord).entries));

    writeAcceptanceArtifact("pi-g4-lifecycle.json", {
      source_lock: PI_TARGET.sourceLock as unknown as JsonRecord,
      installed_pi_package_root: PI_TARGET.packageRoot,
      installed_pi_executable: PI_TARGET.bin,
      installed_pi_version: PI_TARGET.version,
      get_entry_before_append: beforeRootAppend.data as JsonRecord,
      get_entry_after_append: rootLookup.data as JsonRecord,
      get_entry_after_resume: resumedLookup.data as JsonRecord,
      root_entry_id: rootEntryId,
      direct_entry_id: directEntryId,
    });
  } finally {
    await rpc.stop();
    await provider.stop();
    rmSync(home, { recursive: true, force: true });
  }
});

test("stock Pi 0.85 advertises the managed manifest through a deterministic real-model turn", { timeout: 30_000 }, async () => {
  const version = await new Promise<string>((resolveVersion, reject) => {
    const child = spawn(PI_TARGET.bin, ["--version"], { stdio: ["ignore", "pipe", "pipe"] });
    let output = "";
    child.stdout.on("data", (chunk: Buffer) => { output += chunk.toString(); });
    child.once("error", reject);
    child.once("exit", (code) => code === 0 ? resolveVersion(output.trim()) : reject(new Error(`pi --version exited with ${String(code)}`)));
  });
  assert.match(version, /0\.85\.0/);
  const evidenceRegistry = JSON.parse(readFileSync(ACCEPTANCE_EVIDENCE, "utf8")) as JsonRecord;
  assert.equal(evidenceRegistry.protocol, "mecha.phase1-acceptance-contract/v1");
  assert.equal(evidenceRegistry.required_pi_version, version);
  const requiredPrompts = evidenceRegistry.required_prompts;
  assert.ok(requiredPrompts !== null && typeof requiredPrompts === "object" && !Array.isArray(requiredPrompts));
  const hiddenPrompts = (requiredPrompts as JsonRecord).hidden;
  const legacyPrompts = (requiredPrompts as JsonRecord).legacy;
  assert.ok(Array.isArray(hiddenPrompts));
  assert.ok(Array.isArray(legacyPrompts));
  assert.deepEqual(new Set([...hiddenPrompts, ...legacyPrompts]), EXPECTED_COMMANDS);

  const home = mkdtempSync(join(tmpdir(), "mecha-rpc-stock-pi-"));
  const provider = await startDeterministicProvider();
  const rpc = new StockPiRpc(home, provider.baseUrl);
  try {
    const commandResponse = await rpc.request({ type: "get_commands" });
    successful(commandResponse);
    const data = commandResponse.data;
    assert.ok(data !== null && typeof data === "object" && !Array.isArray(data));
    const commands = (data as JsonRecord).commands;
    assert.ok(Array.isArray(commands));
    const names = new Set(commands.flatMap((command) => command !== null && typeof command === "object" && !Array.isArray(command) && typeof (command as JsonRecord).name === "string" ? [(command as JsonRecord).name] : []));
    const managedCommands = new Set([...names].filter((name) => name.startsWith("mecha-") || name === "goal" || name.startsWith("goal-") || name === "inline-tools"));
    assert.deepEqual(managedCommands, EXPECTED_COMMANDS);

    const manifest = readManifest();
    const manifestHash = hashCanonicalJson(manifest);
    const bootstrap = encodeHiddenEnvelope({
      protocol: "mecha.bootstrap/v1",
      delivery_id: "123e4567-e89b-42d3-a456-426614174001",
      runtime_generation: RUNTIME_GENERATION,
      session_epoch: 1,
      desired_manifest: manifest,
      manifest_sha256: manifestHash,
      restored_request_ids: [],
      inline_policy: { enabled: false, credits: {} },
      recall_projection: { content: "" },
    });
    const bootstrapResponse = await rpc.request({ type: "prompt", message: `/mecha-bootstrap-v1 ${bootstrap}` });
    successful(bootstrapResponse);

    const firstModelResponse = await rpc.request({ type: "prompt", message: "Confirm the managed tools are ready." });
    successful(firstModelResponse);
    for (let attempt = 0; provider.requests.length === 0 && attempt < 100; attempt++) await new Promise((resolveDelay) => setTimeout(resolveDelay, 10));
    assert.ok(provider.requests.length >= 1);
    const advertisedTools = provider.requests[0]!.tools;
    assert.ok(Array.isArray(advertisedTools));
    const advertisedNames = new Set(advertisedTools.flatMap((tool) => tool !== null && typeof tool === "object" && !Array.isArray(tool)
      && (tool as JsonRecord).function !== null && typeof (tool as JsonRecord).function === "object" && !Array.isArray((tool as JsonRecord).function)
      && typeof ((tool as JsonRecord).function as JsonRecord).name === "string" ? [((tool as JsonRecord).function as JsonRecord).name] : []));
    const manifestTools = manifest.tools;
    assert.ok(Array.isArray(manifestTools));
    const expectedToolNames = new Set(manifestTools.flatMap((tool) => tool !== null && typeof tool === "object" && !Array.isArray(tool) && typeof (tool as JsonRecord).name === "string" ? [(tool as JsonRecord).name] : []));
    for (const name of expectedToolNames) assert.ok(advertisedNames.has(name), `Pi did not advertise managed tool ${name}`);
    assert.deepEqual(advertisedNames, new Set([...expectedToolNames, "subagent", "get_subagent_result", "steer_subagent"]));
    assert.equal(expectedToolNames.size, 16);
    const advertisedDefinitions = new Map(advertisedTools.flatMap((tool) => {
      if (tool === null || typeof tool !== "object" || Array.isArray(tool)) return [];
      const definition = (tool as JsonRecord).function;
      return definition !== null && typeof definition === "object" && !Array.isArray(definition) && typeof (definition as JsonRecord).name === "string"
        ? [[(definition as JsonRecord).name as string, definition as JsonRecord]] : [];
    }));
    for (const manifestTool of manifestTools) {
      if (manifestTool === null || typeof manifestTool !== "object" || Array.isArray(manifestTool)) continue;
      const expected = manifestTool as JsonRecord;
      const name = expected.name;
      assert.equal(typeof name, "string");
      const advertised = advertisedDefinitions.get(name);
      assert.ok(advertised, `Pi did not advertise the ${name} definition`);
      assert.equal(advertised.description, expected.description);
      assert.deepEqual(advertised.parameters, expected.parameters);
    }

    for (let attempt = 0; !rpc.events.some((event) => event.type === "message_end" && (event.message as JsonRecord | undefined)?.customType === "mecha.control.request.v1") && attempt < 100; attempt++) {
      await new Promise((resolveDelay) => setTimeout(resolveDelay, 10));
    }
    const rawControlEvent = rpc.events.find((event) => event.type === "message_end" && (event.message as JsonRecord | undefined)?.customType === "mecha.control.request.v1");
    assert.ok(rawControlEvent);
    const controlDetails = (rawControlEvent.message as JsonRecord).details as JsonRecord;
    assert.equal(controlDetails.tool_call_id, "raw-goal-call");
    assert.deepEqual(controlDetails.arguments, { action: "status" });
    assert.equal(typeof controlDetails.request_id, "string");

    const result = encodeHiddenEnvelope({
      protocol: "mecha.control/v1",
      result_id: "123e4567-e89b-42d3-a456-426614174002",
      request_id: controlDetails.request_id,
      target_session_epoch: 1,
      runtime_generation: RUNTIME_GENERATION,
      manifest_sha256: manifestHash,
      status: "ok",
      content: "The router completed the request.",
      details: {},
    });
    const resultResponse = await rpc.request({ type: "prompt", message: `/mecha-control-result-v1 ${result}` });
    successful(resultResponse);

    await new Promise((resolveDelay) => setTimeout(resolveDelay, 50));
    const accepted = customMessages(rpc.events, "mecha.tools.accepted.v1");
    const completions = customMessages(rpc.events, "mecha.control.result.v1");
    assert.equal(accepted.length, 1);
    assert.equal(completions.length, 1);
    const completion = completions[0]!;
    const message = completion.message as JsonRecord;
    assert.equal(message.display, false);
    assert.equal(message.content, "The router completed the request.");
    const details = message.details as JsonRecord;
    assert.deepEqual(Object.keys(details).sort(), ["details", "manifest_sha256", "protocol", "request_id", "result_id", "runtime_generation", "status", "target_session_epoch"]);
    assert.equal(details.request_id, controlDetails.request_id);

    const duplicateResponse = await rpc.request({ type: "prompt", message: `/mecha-control-result-v1 ${result}` });
    successful(duplicateResponse);
    await new Promise((resolveDelay) => setTimeout(resolveDelay, 50));
    assert.equal(customMessages(rpc.events, "mecha.control.result.v1").length, 1);

    const supplemental = supplementalManifest(manifest);
    const supplementalHash = hashCanonicalJson(supplemental);
    const supplementalTool = (supplemental.tools as JsonRecord[]).find((tool) => tool.capability_id === "zz_stock_supplemental");
    assert.ok(supplementalTool);
    const supplementalEnvelope = encodeHiddenEnvelope({
      protocol: "mecha.tools-install/v1", delivery_id: "123e4567-e89b-42d3-a456-426614174003",
      runtime_generation: RUNTIME_GENERATION, session_epoch: 1, base_manifest_sha256: manifestHash,
      desired_manifest: supplemental, manifest_sha256: supplementalHash, added_name: supplementalTool.name,
    });
    successful(await rpc.request({ type: "prompt", message: `/mecha-install-tools-v1 ${supplementalEnvelope}` }));
    successful(await rpc.request({ type: "prompt", message: `/mecha-install-tools-v1 ${supplementalEnvelope}` }));
    assert.equal(customMessages(rpc.events, "mecha.tools.accepted.v1").length, 3, "accepted supplemental replay returns a receipt without a second registration");
    const requestsBeforeSupplementalPrompt = provider.requests.length;
    successful(await rpc.request({ type: "prompt", message: "Confirm the supplemental managed definition." }));
    const supplementalRequest = await waitFor(() => provider.requests[requestsBeforeSupplementalPrompt], "17-tool supplemental model request");
    const supplementalManagedNames = new Set((supplemental.tools as JsonRecord[]).map((tool) => String(tool.name)));
    assert.equal([...requestToolNames(supplementalRequest)].filter((name) => supplementalManagedNames.has(name)).length, 17, "the provider request has all 17 managed tools");
    assert.ok(requestToolNames(supplementalRequest).has(String(supplementalTool.name)));

    const supplementalToolName = String(supplementalTool.name);
    const invalidInstallations: Array<{ label: string; envelope: JsonRecord }> = [];
    const malformedBase = structuredClone(supplemental);
    invalidInstallations.push({ label: "stale base hash", envelope: {
      protocol: "mecha.tools-install/v1", delivery_id: "123e4567-e89b-42d3-a456-426614174031",
      runtime_generation: RUNTIME_GENERATION, session_epoch: 1, base_manifest_sha256: "b".repeat(64),
      desired_manifest: malformedBase, manifest_sha256: hashCanonicalJson(malformedBase), added_name: supplementalToolName,
    } });
    const changedExisting = changedDefinitionManifest(supplemental);
    invalidInstallations.push({ label: "changed baseline definition", envelope: {
      protocol: "mecha.tools-install/v1", delivery_id: "123e4567-e89b-42d3-a456-426614174032",
      runtime_generation: RUNTIME_GENERATION, session_epoch: 1, base_manifest_sha256: supplementalHash,
      desired_manifest: changedExisting, manifest_sha256: hashCanonicalJson(changedExisting), added_name: supplementalToolName,
    } });
    const removedExisting = structuredClone(supplemental);
    (removedExisting.tools as JsonRecord[]).splice((removedExisting.tools as JsonRecord[]).findIndex((tool) => tool.capability_id === "goal"), 1);
    invalidInstallations.push({ label: "removed baseline definition", envelope: {
      protocol: "mecha.tools-install/v1", delivery_id: "123e4567-e89b-42d3-a456-426614174033",
      runtime_generation: RUNTIME_GENERATION, session_epoch: 1, base_manifest_sha256: supplementalHash,
      desired_manifest: removedExisting, manifest_sha256: hashCanonicalJson(removedExisting), added_name: supplementalToolName,
    } });
    const reordered = structuredClone(supplemental);
    (reordered.tools as JsonRecord[]).reverse();
    invalidInstallations.push({ label: "reordered definitions", envelope: {
      protocol: "mecha.tools-install/v1", delivery_id: "123e4567-e89b-42d3-a456-426614174034",
      runtime_generation: RUNTIME_GENERATION, session_epoch: 1, base_manifest_sha256: supplementalHash,
      desired_manifest: reordered, manifest_sha256: hashCanonicalJson(reordered), added_name: supplementalToolName,
    } });
    const twoAdditions = supplementalManifest(supplemental, "zz_second_supplemental");
    twoAdditions.revision = 3;
    invalidInstallations.push({ label: "two additions", envelope: {
      protocol: "mecha.tools-install/v1", delivery_id: "123e4567-e89b-42d3-a456-426614174035",
      runtime_generation: RUNTIME_GENERATION, session_epoch: 1, base_manifest_sha256: supplementalHash,
      desired_manifest: twoAdditions, manifest_sha256: hashCanonicalJson(twoAdditions), added_name: supplementalToolName,
    } });
    const overCapacity = structuredClone(supplemental);
    const overCapacityTools = overCapacity.tools as JsonRecord[];
    for (let index = 0; index < 16; index++) {
      const definition = {
        capability_id: `zz_more_${String(index).padStart(2, "0")}`,
        label: `Extra ${index}`,
        description: "An over-capacity supplemental test definition.",
        parameters: { type: "object", additionalProperties: false, required: [], properties: {} },
        operation_id: `stock.extra.${index}`,
      };
      const definitionSha = hashCanonicalJson(definition);
      overCapacityTools.push({ ...definition, name: `${definition.capability_id}__${definitionSha}`, definition_id: `def_${definitionSha}`, definition_sha256: definitionSha });
    }
    overCapacityTools.sort((left, right) => Buffer.compare(Buffer.from(String(left.capability_id), "utf8"), Buffer.from(String(right.capability_id), "utf8")));
    overCapacity.revision = 3;
    invalidInstallations.push({ label: "over capacity", envelope: {
      protocol: "mecha.tools-install/v1", delivery_id: "123e4567-e89b-42d3-a456-426614174036",
      runtime_generation: RUNTIME_GENERATION, session_epoch: 1, base_manifest_sha256: supplementalHash,
      desired_manifest: overCapacity, manifest_sha256: hashCanonicalJson(overCapacity), added_name: supplementalToolName,
    } });
    invalidInstallations.push({ label: "stale runtime fence", envelope: {
      protocol: "mecha.tools-install/v1", delivery_id: "123e4567-e89b-42d3-a456-426614174037",
      runtime_generation: "123e4567-e89b-42d3-a456-426614174099", session_epoch: 1, base_manifest_sha256: supplementalHash,
      desired_manifest: supplemental, manifest_sha256: supplementalHash, added_name: supplementalToolName,
    } });
    const requestsBeforeInvalidInstalls = provider.requests.length;
    for (const invalid of invalidInstallations) {
      successful(await rpc.request({ type: "prompt", message: `/mecha-install-tools-v1 ${encodeHiddenEnvelope(invalid.envelope)}` }));
    }
    await waitFor(() => customMessages(rpc.events, "mecha.tools.rejected.v1").length >= invalidInstallations.length ? true : undefined, "supplemental invalid-installation rejections");
    assert.equal(provider.requests.length, requestsBeforeInvalidInstalls, "invalid supplemental updates never make a model request");
    const requestsBeforeUnchangedPrompt = provider.requests.length;
    successful(await rpc.request({ type: "prompt", message: "Confirm rejected supplemental updates left the registry unchanged." }));
    const unchangedRequest = await waitFor(() => provider.requests[requestsBeforeUnchangedPrompt], "unchanged supplemental model request");
    assert.equal([...requestToolNames(unchangedRequest)].filter((name) => supplementalManagedNames.has(name)).length, 17);
    assert.ok(requestToolNames(unchangedRequest).has(supplementalToolName));

    successful(await rpc.request({ type: "prompt", message: `/mecha-mailbox-deliver-v1 ${encodeHiddenEnvelope({
      protocol: "mecha.mailbox-deliver/v1", message_id: "stock-mail", content: "Stock managed mail.",
      runtime_generation: RUNTIME_GENERATION, session_epoch: 1, manifest_sha256: supplementalHash,
    })}` }));
    successful(await rpc.request({ type: "prompt", message: `/mecha-policy-apply-v1 ${encodeHiddenEnvelope({
      protocol: "mecha.policy/v1", runtime_generation: RUNTIME_GENERATION, session_epoch: 1, manifest_sha256: supplementalHash,
      policy: { inline: { enabled: false, credits: {} }, recall: { content: "" } },
    })}` }));
    successful(await rpc.request({ type: "prompt", message: `/mecha-child-snapshot-v1 ${encodeHiddenEnvelope({
      protocol: "mecha.child-snapshot/v1", runtime_generation: RUNTIME_GENERATION, session_epoch: 1, manifest_sha256: supplementalHash,
    })}` }));
    await waitFor(() => customMessages(rpc.events, "mecha.mailbox.appended.v1").length === 1
      && customMessages(rpc.events, "mecha.policy.applied.v1").length === 1
      && customMessages(rpc.events, "mecha.child.snapshot.v1").length === 1 ? true : undefined, "three hidden-command custom messages");

    const observedLegacy = new Set<string>();
    for (const [index, name] of (legacyPrompts as unknown[]).entries()) {
      assert.equal(typeof name, "string");
      const raw = `stock raw argument ${index}`;
      successful(await rpc.request({ type: "prompt", message: `/${name} ${raw}` }));
      await waitFor(() => customMessages(rpc.events, "mecha.control.request.v1").find((event) => {
        const message = event.message as JsonRecord;
        const details = message.details;
        return details !== null && typeof details === "object" && !Array.isArray(details)
          && (details as JsonRecord).legacy_command === name && (details as JsonRecord).raw_arguments === raw;
      }), `legacy ${name} control frame`);
      observedLegacy.add(name);
    }
    assert.deepEqual(observedLegacy, new Set(legacyPrompts as string[]));
    writeAcceptanceArtifact("stock-pi-0.85-parent-commands.json", {
      protocol: "mecha.phase1-stock-proof/v1", pi_version: version, runtime_generation: RUNTIME_GENERATION,
      prompted_hidden: hiddenPrompts, prompted_legacy: legacyPrompts, accepted_manifest_sizes: [16, 17],
      supplemental_replay: true, custom_message_types: ["mecha.mailbox.appended.v1", "mecha.policy.applied.v1", "mecha.child.snapshot.v1"],
    });
  } finally {
    await rpc.stop();
    await provider.stop();
    rmSync(home, { recursive: true, force: true });
  }
});

test("stock Pi 0.85 fails closed on every reserved command or tool collision and on partial command registration", { timeout: 60_000 }, async () => {
  const collisionCases = [...EXPECTED_COMMANDS].flatMap((name) => [
    { mode: "command", name },
    { mode: "tool", name },
  ]);
  for (const collision of collisionCases) {
    const home = mkdtempSync(join(tmpdir(), "mecha-rpc-stock-collision-"));
    const provider = await startDeterministicProvider({ firstToolCall: false });
    const rpc = new StockPiRpc(home, provider.baseUrl, {
      extensions: [REGISTRATION_PROBE, EXTENSION, SUBAGENTS_EXTENSION],
      probeMode: collision.mode,
      probeName: collision.name,
    });
    try {
      void rpc.request({ type: "get_commands" }).catch(() => {});
      assert.equal(await rpc.waitForExit(), 1, `${collision.mode} collision for ${collision.name} must terminate this runtime`);
      assert.equal(provider.requests.length, 0, `${collision.mode} collision for ${collision.name} must precede model use`);
      const errorEvent = rpc.events.find((event) => event.type === "extension_error" && typeof event.error === "string");
      assert.ok(errorEvent, `stock Pi did not report the ${collision.name} collision`);
      assert.match(String(errorEvent.error), new RegExp(`Managed command collision: ${collision.name}`));
    } finally {
      await rpc.stop();
      await provider.stop();
      rmSync(home, { recursive: true, force: true });
    }
  }

  const registrationFailures: JsonRecord[] = [];
  for (const position of [1, 8, 16]) {
    const home = mkdtempSync(join(tmpdir(), "mecha-rpc-stock-command-failure-"));
    const provider = await startDeterministicProvider({ firstToolCall: false });
    const rpc = new StockPiRpc(home, provider.baseUrl, {
      extensions: [EXTENSION, SUBAGENTS_EXTENSION],
      testCommandFailureAt: position,
    });
    try {
      void rpc.request({ type: "get_commands" }).catch(() => {});
      const exitCode = await rpc.waitForExit();
      assert.equal(exitCode, 1, `forced registration failure at ${position} must terminate stock Pi`);
      assert.equal(provider.requests.length, 0, `partial registration at ${position} must not reach a model turn`);
      const errorEvent = rpc.events.find((event) => event.type === "extension_error" && typeof event.error === "string");
      assert.ok(errorEvent, `stock Pi did not report the forced registration failure at ${position}`);
      assert.match(String(errorEvent.error), new RegExp(`forced managed command registration failure at ${position}`));
      registrationFailures.push({ position, exit_code: exitCode, model_requests: provider.requests.length, partial_runtime_usable: false });
    } finally {
      await rpc.stop();
      await provider.stop();
      rmSync(home, { recursive: true, force: true });
    }
  }
  writeAcceptanceArtifact("stock-pi-0.85-command-registration-failures.json", {
    protocol: "mecha.phase1-stock-proof/v1", pi_version: "0.85.0",
    forced_registration_failures: registrationFailures,
  });
});

test("stock Pi 0.85 rejects an isolated supplemental collision without changing the accepted manifest", { timeout: 30_000 }, async () => {
  const baseline = readManifest();
  const accepted = supplementalManifest(baseline);
  const acceptedHash = hashCanonicalJson(accepted);
  const collisionTarget = supplementalManifest(accepted, "zz_stock_collision");
  collisionTarget.revision = 3;
  const collisionTool = (collisionTarget.tools as JsonRecord[]).find((tool) => tool.capability_id === "zz_stock_collision");
  assert.ok(collisionTool);
  const collisionName = String(collisionTool.name);
  const acceptedManagedNames = new Set((accepted.tools as JsonRecord[]).map((tool) => String(tool.name)));

  const home = mkdtempSync(join(tmpdir(), "mecha-rpc-stock-supplemental-collision-"));
  const provider = await startDeterministicProvider({ firstToolCall: false });
  const rpc = new StockPiRpc(home, provider.baseUrl, {
    extensions: [REGISTRATION_PROBE, EXTENSION, SUBAGENTS_EXTENSION],
    probeMode: "tool",
    probeName: collisionName,
  });
  try {
    await bootstrapCurrentManifest(rpc, baseline, RUNTIME_GENERATION, "123e4567-e89b-42d3-a456-426614174041");
    const acceptedTool = (accepted.tools as JsonRecord[]).find((tool) => tool.capability_id === "zz_stock_supplemental");
    assert.ok(acceptedTool);
    successful(await rpc.request({ type: "prompt", message: `/mecha-install-tools-v1 ${encodeHiddenEnvelope({
      protocol: "mecha.tools-install/v1", delivery_id: "123e4567-e89b-42d3-a456-426614174042",
      runtime_generation: RUNTIME_GENERATION, session_epoch: 1, base_manifest_sha256: hashCanonicalJson(baseline),
      desired_manifest: accepted, manifest_sha256: acceptedHash, added_name: acceptedTool.name,
    })}` }));
    await waitFor(() => customMessages(rpc.events, "mecha.tools.accepted.v1").some((event) => {
      const details = (event.message as JsonRecord).details;
      return details !== null && typeof details === "object" && !Array.isArray(details)
        && (details as JsonRecord).manifest_sha256 === acceptedHash ? true : undefined;
    }), "accepted 17-tool manifest receipt");

    const requestsBeforeCollision = provider.requests.length;
    successful(await rpc.request({ type: "prompt", message: `/mecha-install-tools-v1 ${encodeHiddenEnvelope({
      protocol: "mecha.tools-install/v1", delivery_id: "123e4567-e89b-42d3-a456-426614174043",
      runtime_generation: RUNTIME_GENERATION, session_epoch: 1, base_manifest_sha256: acceptedHash,
      desired_manifest: collisionTarget, manifest_sha256: hashCanonicalJson(collisionTarget), added_name: collisionName,
    })}` }));
    const rejection = await waitFor(() => customMessages(rpc.events, "mecha.tools.rejected.v1").at(-1));
    const rejectionDetails = (rejection.message as JsonRecord).details;
    assert.ok(rejectionDetails !== null && typeof rejectionDetails === "object" && !Array.isArray(rejectionDetails));
    assert.match(String((rejectionDetails as JsonRecord).reason), new RegExp(`Managed tool name collision: ${collisionName}`));
    assert.equal(provider.requests.length, requestsBeforeCollision, "the rejected collision staging path must not make a model request");

    successful(await rpc.request({ type: "prompt", message: "Confirm the collision left the accepted managed tools unchanged." }));
    const unchangedRequest = await waitFor(() => provider.requests[requestsBeforeCollision], "post-collision model request");
    const actualManagedNames = new Set([...requestToolNames(unchangedRequest)].filter((name) => acceptedManagedNames.has(name)));
    assert.deepEqual(actualManagedNames, acceptedManagedNames);
    assert.equal(requestToolNames(unchangedRequest).has(collisionName), true, "the isolated collision probe remains the only target with that name");

    writeAcceptanceArtifact("stock-pi-0.85-supplemental-collision.json", {
      protocol: "mecha.phase1-stock-proof/v1", pi_version: "0.85.0", collision_target_name: collisionName,
      accepted_manifest_sha256: acceptedHash, accepted_managed_tool_count: acceptedManagedNames.size,
      collision_rejected: true, staging_model_requests: 0,
      post_rejection_managed_tool_count: actualManagedNames.size,
      post_rejection_manifest_sha256: acceptedHash,
    });
  } finally {
    await rpc.stop();
    await provider.stop();
    rmSync(home, { recursive: true, force: true });
  }
});

test("stock Pi 0.85 hands an accepted parent manifest to a real child before its first turn and appends the correlated result through the parent", { timeout: 45_000 }, async () => {
  const home = mkdtempSync(join(tmpdir(), "mecha-rpc-stock-child-"));
  mkdirSync(join(home, "tmp"), { recursive: true });
  const provider = await startChildHandoffProvider();
  const rpc = new StockPiRpc(home, provider.baseUrl, { persistSession: true });
  try {
    const manifest = readManifest();
    const manifestHash = hashCanonicalJson(manifest);
    const bootstrap = encodeHiddenEnvelope({
      protocol: "mecha.bootstrap/v1",
      delivery_id: "123e4567-e89b-42d3-a456-426614174021",
      runtime_generation: RUNTIME_GENERATION,
      session_epoch: 1,
      desired_manifest: manifest,
      manifest_sha256: manifestHash,
      restored_request_ids: [],
      inline_policy: { enabled: false, credits: {} },
      recall_projection: { content: "" },
    });
    successful(await rpc.request({ type: "prompt", message: `/mecha-bootstrap-v1 ${bootstrap}` }));
    successful(await rpc.request({ type: "prompt", message: `/mecha-install-tools-v1 ${encodeHiddenEnvelope({
      protocol: "mecha.tools-install/v1", delivery_id: "123e4567-e89b-42d3-a456-426614174024",
      runtime_generation: RUNTIME_GENERATION, session_epoch: 1, base_manifest_sha256: manifestHash,
      desired_manifest: manifest, manifest_sha256: manifestHash, added_name: "missing-supplemental-tool",
    })}` }));
    await waitFor(() => customMessages(rpc.events, "mecha.tools.rejected.v1").length > 0 ? true : undefined, "rejected native supplemental evidence");
    successful(await rpc.request({ type: "prompt", message: `/mecha-child-snapshot-v1 ${encodeHiddenEnvelope({
      protocol: "mecha.child-snapshot/v1", runtime_generation: RUNTIME_GENERATION, session_epoch: 1,
      manifest_sha256: manifestHash,
    })}` }));

    const parentTurn = rpc.request({ type: "prompt", message: "Start the managed child." });
    const childRequest = await waitFor(() => provider.childRequests[0], "the first stock Pi child model request");
    const advertisedNames = requestToolNames(childRequest);
    const manifestTools = manifest.tools;
    assert.ok(Array.isArray(manifestTools));
    assert.equal(manifestTools.length, 16);
    const advertisedDefinitions = new Map((childRequest.tools as unknown[]).flatMap((tool) => {
      if (tool === null || typeof tool !== "object" || Array.isArray(tool)) return [];
      const definition = (tool as JsonRecord).function;
      return definition !== null && typeof definition === "object" && !Array.isArray(definition)
        && typeof (definition as JsonRecord).name === "string"
        ? [[(definition as JsonRecord).name as string, definition as JsonRecord]]
        : [];
    }));
    for (const manifestTool of manifestTools) {
      assert.ok(manifestTool !== null && typeof manifestTool === "object" && !Array.isArray(manifestTool));
      const expected = manifestTool as JsonRecord;
      const name = expected.name;
      assert.equal(typeof name, "string");
      assert.ok(advertisedNames.has(name), `child did not receive managed tool ${name}\n${rpc.diagnostics()}`);
      const advertised = advertisedDefinitions.get(name);
      assert.ok(advertised, `child did not receive ${name}'s definition`);
      assert.equal(advertised.description, expected.description);
      assert.deepEqual(advertised.parameters, expected.parameters);
    }
    assert.equal(advertisedNames.has("subagent"), false);
    assert.equal(advertisedNames.has("get_subagent_result"), false);
    assert.equal(advertisedNames.has("steer_subagent"), false);

    const childControlRequest = await waitFor(() => {
      const entry = customRecords(recordsIn(join(home, "sessions")), "mecha.control.request.v1").find((candidate) => {
        const details = candidate.details;
        return details !== null && typeof details === "object" && !Array.isArray(details)
          && (details as JsonRecord).tool_call_id === "child-managed-goal";
      });
      if (entry === undefined || entry.details === null || typeof entry.details !== "object" || Array.isArray(entry.details)) return undefined;
      return entry.details as JsonRecord;
    }, "the child's persisted raw managed request");
    assert.equal(childControlRequest.tool_name, "goal");
    assert.deepEqual(childControlRequest.arguments, { action: "status" });
    assert.equal(typeof childControlRequest.request_id, "string");
    assert.match(String(childControlRequest.context_ref), /^ctx1_[A-Za-z0-9_-]{43}$/);

    const result = encodeHiddenEnvelope({
      protocol: "mecha.control/v1",
      result_id: "123e4567-e89b-42d3-a456-426614174022",
      request_id: childControlRequest.request_id,
      target_session_epoch: 1,
      runtime_generation: RUNTIME_GENERATION,
      manifest_sha256: manifestHash,
      status: "ok",
      content: "Child control completed.",
      details: { source: "stock-child-e2e" },
    });
    successful(await rpc.request({ type: "prompt", message: `/mecha-control-result-v1 ${result}` }));

    await waitFor(() => provider.childRequests[1] === undefined ? undefined : true, "the child continuation held during result append");
    provider.releaseChildTurn();
    successful(await parentTurn);
    successful(await rpc.request({ type: "prompt", message: `/mecha-mailbox-deliver-v1 ${encodeHiddenEnvelope({
      protocol: "mecha.mailbox-deliver/v1", message_id: "stock-child-mail", content: "Stock child evidence mail.",
      runtime_generation: RUNTIME_GENERATION, session_epoch: 1, manifest_sha256: manifestHash,
    })}` }));
    successful(await rpc.request({ type: "prompt", message: `/mecha-policy-apply-v1 ${encodeHiddenEnvelope({
      protocol: "mecha.policy/v1", runtime_generation: RUNTIME_GENERATION, session_epoch: 1, manifest_sha256: manifestHash,
      policy: { inline: { enabled: false, credits: {} }, recall: { content: "" } },
    })}` }));

    await waitFor(() => {
      const deltas = customMessages(rpc.events, "mecha.child.delta.v1");
      return deltas.some((event) => {
        const details = (event.message as JsonRecord).details;
        const changes = details !== null && typeof details === "object" && !Array.isArray(details) ? (details as JsonRecord).changes : undefined;
        return changes !== null && typeof changes === "object" && !Array.isArray(changes) && (changes as JsonRecord).lifecycle_state === "completed";
      }) ? true : undefined;
    }, "the parent lifecycle completion delta");

    const persistedResult = await waitFor(() => customRecords(recordsIn(join(home, "sessions")), "mecha.control.result.v1").find((entry) => {
      const details = entry.details;
      return details !== null && typeof details === "object" && !Array.isArray(details)
        && (details as JsonRecord).request_id === childControlRequest.request_id;
    }), "the service-appended child control result");
    assert.equal(persistedResult.display, false);
    assert.equal(persistedResult.content, "Child control completed.");
    assert.equal((persistedResult.details as JsonRecord).request_id, childControlRequest.request_id);
    assert.equal((persistedResult.details as JsonRecord).result_id, "123e4567-e89b-42d3-a456-426614174022");
    const accepted = customRecords(recordsIn(join(home, "sessions")), "mecha.tools.accepted.v1");
    assert.ok(accepted.length >= 2, `expected accepted child manifest\n${rpc.diagnostics()}`);
    assert.ok(accepted.every((entry) => (entry.details as JsonRecord).manifest_sha256 === manifestHash));

    const stale = encodeHiddenEnvelope({
      protocol: "mecha.control/v1",
      result_id: "123e4567-e89b-42d3-a456-426614174023",
      request_id: childControlRequest.request_id,
      target_session_epoch: 1,
      runtime_generation: RUNTIME_GENERATION,
      manifest_sha256: manifestHash,
      status: "ok",
      content: "Late child result.",
      details: {},
    });
    // Pi's RPC prompt protocol acknowledges command dispatch even when the
    // hidden command rejects its payload. The durable observable is that a
    // stale child result cannot append another service-owned result.
    successful(await rpc.request({ type: "prompt", message: `/mecha-control-result-v1 ${stale}` }));
    await new Promise((resolveDelay) => setTimeout(resolveDelay, 50));
    assert.equal(customRecords(recordsIn(join(home, "sessions")), "mecha.control.result.v1").length, 1);
    writeAcceptanceArtifact("stock-pi-0.85-real-child.json", {
      protocol: "mecha.phase1-stock-proof/v1", runtime_generation: RUNTIME_GENERATION,
      parent_first_request: true, child_first_request: true, child_manifest_size: 16,
      child_context_result_append: true, stale_child_result_appended: false,
    });
    const sessionRecords = recordsIn(join(home, "sessions"));
    const observedCustomMessageTypes = CUSTOM_MESSAGE_TYPES.map((customType) => {
      const rpcEvents = customMessages(rpc.events, customType).length;
      const persistedMessages = customRecords(sessionRecords, customType).length;
      const observedCount = rpcEvents + persistedMessages;
      assert.ok(observedCount > 0, `native run did not observe ${customType}`);
      return {
        custom_type: customType,
        observed_count: observedCount,
        sources: ["installed-stock-pi-rpc", "installed-stock-pi-session"],
      };
    });
    writeAcceptanceArtifact("stock-pi-0.85-custom-message-types.json", {
      protocol: "mecha.phase1-stock-proof/v1",
      observed_custom_message_types: observedCustomMessageTypes,
      phase2_exclusions: ["router dispatch and persistence", "managed HTTP and authentication", "mailbox recovery", "browser acceptance and release gates"],
    });
  } finally {
    await rpc.stop();
    await provider.stop();
    rmSync(home, { recursive: true, force: true });
  }
});

test("stock Pi 0.85 proves AC-090 duplicate, disposer, and owner-release factory lifecycle against real child spawns", { timeout: 45_000 }, async () => {
  const home = mkdtempSync(join(tmpdir(), "mecha-rpc-stock-ac090-"));
  const provider = await startDeterministicProvider({ firstToolCall: false });
  const rpc = new StockPiRpc(home, provider.baseUrl, {
    persistSession: true,
    extensions: [EXTENSION, SUBAGENTS_EXTENSION, AC090_PROBE],
  });
  const manifest = readManifest();
  const manifestHash = hashCanonicalJson(manifest);
  const assertChildTools = async (requestIndex: number, markerExpected: boolean, description: string): Promise<void> => {
    const request = await waitFor(() => provider.requests[requestIndex], description);
    const names = requestToolNames(request);
    const managed = new Set((manifest.tools as JsonRecord[]).map((tool) => String(tool.name)));
    assert.equal([...names].filter((name) => managed.has(name)).length, 16, `${description} lost a Mecha child definition`);
    assert.equal(names.has("phase1_ac090_marker"), markerExpected, description);
  };
  try {
    await bootstrapCurrentManifest(rpc, manifest, RUNTIME_GENERATION, "123e4567-e89b-42d3-a456-426614174071");
    successful(await rpc.request({ type: "prompt", message: "/phase1-ac090-duplicate-mecha" }));
    const duplicateReport = await waitFor(() => customMessages(rpc.events, "phase1.ac090.v1").find((event) => {
      const details = (event.message as JsonRecord).details;
      return details !== null && typeof details === "object" && !Array.isArray(details) && (details as JsonRecord).action === "duplicate-mecha" ? event : undefined;
    }), "AC-090 duplicate report");
    assert.equal(((duplicateReport.message as JsonRecord).details as JsonRecord).duplicate_rejected, true);

    successful(await rpc.request({ type: "prompt", message: "/phase1-ac090-spawn" }));
    await assertChildTools(0, true, "child spawned with the initial marker factory");
    successful(await rpc.request({ type: "prompt", message: "/phase1-ac090-dispose" }));
    successful(await rpc.request({ type: "prompt", message: "/phase1-ac090-spawn" }));
    await assertChildTools(1, false, "child spawned after marker factory disposal");
    successful(await rpc.request({ type: "prompt", message: "/phase1-ac090-reregister" }));

    successful(await rpc.request({ type: "new_session" }));
    await bootstrapCurrentManifest(rpc, manifest, RUNTIME_GENERATION, "123e4567-e89b-42d3-a456-426614174072");
    successful(await rpc.request({ type: "prompt", message: "/phase1-ac090-spawn" }));
    await assertChildTools(2, false, "child spawned after parent owner release");
    writeAcceptanceArtifact("stock-pi-0.85-ac090-child-factory-lifecycle.json", {
      protocol: "mecha.phase1-stock-proof/v1", manifest_sha256: manifestHash,
      duplicate_factory_rejected: true, disposer_removes_future_child_factory: true,
      owner_release_removes_future_child_factory: true, real_child_spawns: 3,
    });
  } finally {
    await rpc.stop();
    await provider.stop();
    rmSync(home, { recursive: true, force: true });
  }
});

test("stock Pi 0.85 requires a complete current manifest after process, resume, switch, fork, and reload generations", { timeout: 60_000 }, async () => {
  const home = mkdtempSync(join(tmpdir(), "mecha-rpc-stock-lifecycle-"));
  const provider = await startDeterministicProvider({ firstToolCall: false });
  const changed = changedDefinitionManifest(readManifest());
  const changedGoal = (changed.tools as JsonRecord[]).find((tool) => tool.capability_id === "goal")!;
  const changedGoalName = String(changedGoal.name);
  const assertCurrentTurn = async (rpc: StockPiRpc, label: string): Promise<void> => {
    const requestsBeforePrompt = provider.requests.length;
    successful(await rpc.request({ type: "prompt", message: `Confirm the ${label} manifest.` }));
    const request = await waitFor(() => provider.requests[requestsBeforePrompt], `${label} current model request`);
    assert.ok(requestToolNames(request).has(changedGoalName), `${label} did not advertise the changed current definition`);
    assert.equal(requestToolNames(request).has("goal"), false, `${label} leaked the old definition name`);
  };
  let rpc = new StockPiRpc(home, provider.baseUrl, {
    persistSession: true,
    runtimeGeneration: "123e4567-e89b-42d3-a456-426614174081",
    extensions: [EXTENSION, SUBAGENTS_EXTENSION, TRANSITION_PROBE],
  });
  try {
    await bootstrapCurrentManifest(rpc, changed, "123e4567-e89b-42d3-a456-426614174081", "123e4567-e89b-42d3-a456-426614174081");
    const originalSessionFile = sessionFileFromState(await rpc.request({ type: "get_state" }));
    await assertCurrentTurn(rpc, "initial");

    successful(await rpc.request({ type: "prompt", message: "/phase1-transition new 123e4567-e89b-42d3-a456-426614174082" }));
    await bootstrapCurrentManifest(rpc, changed, "123e4567-e89b-42d3-a456-426614174082", "123e4567-e89b-42d3-a456-426614174082");
    await assertCurrentTurn(rpc, "new-session");

    successful(await rpc.request({ type: "prompt", message: `/phase1-transition switch 123e4567-e89b-42d3-a456-426614174083 ${originalSessionFile}` }));
    await bootstrapCurrentManifest(rpc, changed, "123e4567-e89b-42d3-a456-426614174083", "123e4567-e89b-42d3-a456-426614174083");
    await assertCurrentTurn(rpc, "session-switch");

    successful(await rpc.request({ type: "prompt", message: "Create a user entry for the lifecycle fork." }));
    const forkEntryId = latestUserEntryId(await rpc.request({ type: "get_entries" }));
    successful(await rpc.request({ type: "prompt", message: `/phase1-transition fork 123e4567-e89b-42d3-a456-426614174084 ${forkEntryId}` }));
    await bootstrapCurrentManifest(rpc, changed, "123e4567-e89b-42d3-a456-426614174084", "123e4567-e89b-42d3-a456-426614174084");
    await assertCurrentTurn(rpc, "fork");

    successful(await rpc.request({ type: "prompt", message: "/phase1-transition reload 123e4567-e89b-42d3-a456-426614174085" }));
    await bootstrapCurrentManifest(rpc, changed, "123e4567-e89b-42d3-a456-426614174085", "123e4567-e89b-42d3-a456-426614174085");
    await assertCurrentTurn(rpc, "extension-reload");
    const resumableSessionFile = sessionFileFromState(await rpc.request({ type: "get_state" }));

    await rpc.stop();
    rpc = new StockPiRpc(home, provider.baseUrl, {
      runtimeGeneration: "123e4567-e89b-42d3-a456-426614174086",
      sessionPath: resumableSessionFile,
      extensions: [EXTENSION, SUBAGENTS_EXTENSION, TRANSITION_PROBE],
    });
    await bootstrapCurrentManifest(rpc, changed, "123e4567-e89b-42d3-a456-426614174086", "123e4567-e89b-42d3-a456-426614174086");
    await assertCurrentTurn(rpc, "process-resume");

    await rpc.stop();
    rpc = new StockPiRpc(home, provider.baseUrl, {
      runtimeGeneration: "123e4567-e89b-42d3-a456-426614174087",
      extensions: [EXTENSION, SUBAGENTS_EXTENSION, TRANSITION_PROBE],
    });
    await bootstrapCurrentManifest(rpc, changed, "123e4567-e89b-42d3-a456-426614174087", "123e4567-e89b-42d3-a456-426614174087");
    await assertCurrentTurn(rpc, "process-restart");
    writeAcceptanceArtifact("stock-pi-0.85-lifecycle-generations.json", {
      protocol: "mecha.phase1-stock-proof/v1", changed_definition_name: changedGoalName,
      fresh_generation_causes: ["process-restart", "resume", "session-switch", "fork", "extension-reload"],
      complete_manifest_before_next_turn: true,
    });
  } finally {
    await rpc.stop();
    await provider.stop();
    rmSync(home, { recursive: true, force: true });
  }
});

test("stock Pi 0.85 rejects an old generation and bootstraps a changed definition in a fresh process", { timeout: 30_000 }, async () => {
  const home = mkdtempSync(join(tmpdir(), "mecha-rpc-stock-restart-"));
  const provider = await startDeterministicProvider({ firstToolCall: false });
  const generation = "123e4567-e89b-42d3-a456-426614174099";
  const rpc = new StockPiRpc(home, provider.baseUrl, { runtimeGeneration: generation });
  try {
    const changed = changedDefinitionManifest(readManifest());
    const changedHash = hashCanonicalJson(changed);
    const stale = encodeHiddenEnvelope({
      protocol: "mecha.bootstrap/v1", delivery_id: "123e4567-e89b-42d3-a456-426614174090",
      runtime_generation: RUNTIME_GENERATION, session_epoch: 1, desired_manifest: changed, manifest_sha256: changedHash,
      restored_request_ids: [], inline_policy: { enabled: false, credits: {} }, recall_projection: { content: "" },
    });
    successful(await rpc.request({ type: "prompt", message: `/mecha-bootstrap-v1 ${stale}` }));
    await waitFor(() => customMessages(rpc.events, "mecha.tools.rejected.v1").length === 1 ? true : undefined, "stale bootstrap rejection");

    const current = encodeHiddenEnvelope({
      protocol: "mecha.bootstrap/v1", delivery_id: "123e4567-e89b-42d3-a456-426614174091",
      runtime_generation: generation, session_epoch: 1, desired_manifest: changed, manifest_sha256: changedHash,
      restored_request_ids: [], inline_policy: { enabled: false, credits: {} }, recall_projection: { content: "" },
    });
    successful(await rpc.request({ type: "prompt", message: `/mecha-bootstrap-v1 ${current}` }));
    successful(await rpc.request({ type: "prompt", message: "Confirm the restarted current manifest." }));
    const firstRequest = await waitFor(() => provider.requests[0], "fresh generation model request");
    const names = requestToolNames(firstRequest);
    const changedGoal = (changed.tools as JsonRecord[]).find((tool) => tool.capability_id === "goal")!;
    assert.equal(typeof changedGoal.name, "string");
    assert.ok(names.has(changedGoal.name));
    assert.equal(names.has("goal"), false, "a changed definition cannot reuse the old model-visible name");
    writeAcceptanceArtifact("stock-pi-0.85-fresh-generation.json", {
      protocol: "mecha.phase1-stock-proof/v1", runtime_generation: generation,
      stale_generation_rejected: true, changed_definition_name: changedGoal.name,
      fresh_manifest_size: (changed.tools as JsonRecord[]).length,
    });
  } finally {
    await rpc.stop();
    await provider.stop();
    rmSync(home, { recursive: true, force: true });
  }
});
