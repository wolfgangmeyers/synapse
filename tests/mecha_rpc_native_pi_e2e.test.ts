import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, statSync, writeFileSync } from "node:fs";
import { createServer, type ServerResponse } from "node:http";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { createInterface } from "node:readline";
import test from "node:test";

import { hashCanonicalJson } from "../pi-extensions/mecha-rpc/manifest.ts";
import { encodeHiddenEnvelope } from "../pi-extensions/mecha-rpc/protocol.ts";

const ROOT = resolve(import.meta.dirname, "..");
const EXTENSION = join(ROOT, "pi-extensions/mecha-rpc/index.ts");
const SUBAGENTS_EXTENSION = "/Users/wolfgang/code/pi-packages/packages/pi-subagents/src/index.ts";
const RUNTIME_GENERATION = "123e4567-e89b-42d3-a456-426614174000";
const EXPECTED_COMMANDS = new Set([
  "mecha-bootstrap-v1", "mecha-install-tools-v1", "mecha-control-result-v1", "mecha-mailbox-deliver-v1", "mecha-policy-apply-v1", "mecha-child-snapshot-v1",
  "goal", "goal-lock", "goal-snooze", "goal-snooze-turn-limit", "goal-snooze-budget", "goal-snooze-budget-refill", "mecha-parent-message", "inline-tools", "mecha-reply",
]);

type JsonRecord = Record<string, unknown>;
type PendingResponse = { resolve: (value: JsonRecord) => void; reject: (reason: Error) => void };

class StockPiRpc {
  readonly events: JsonRecord[] = [];
  private readonly pending = new Map<string, PendingResponse>();
  private nextId = 1;
  private readonly stderr: string[] = [];
  private readonly process;

  constructor(home: string, baseUrl: string, options: { persistSession?: boolean } = {}) {
    configureDeterministicProvider(home, baseUrl);
    const persistSession = options.persistSession === true;
    const sessionArgs = persistSession ? ["--session-dir", join(home, "sessions")] : ["--no-session"];
    this.process = spawn("pi", [
      "--mode", "rpc", ...sessionArgs, "--no-extensions", "--no-skills", "--no-prompt-templates", "--no-themes", "--no-context-files", "--no-builtin-tools", "--approve", "--offline",
      "--provider", "mecha-router-test", "--model", "deterministic-model",
      "-e", EXTENSION,
      "-e", SUBAGENTS_EXTENSION,
    ], {
      cwd: ROOT,
      env: {
        ...process.env,
        PI_CODING_AGENT_DIR: join(home, ".pi", "agent"),
        TMPDIR: join(home, "tmp"),
        MECHA_RPC_RUNTIME_GENERATION: RUNTIME_GENERATION,
        MECHA_RPC_SESSION_EPOCH: "1",
        MECHA_TEST_BASE_URL: baseUrl,
        PI_OFFLINE: "1",
      },
      stdio: ["pipe", "pipe", "pipe"],
    });
    createInterface({ input: this.process.stdout }).on("line", (line) => this.receive(line));
    createInterface({ input: this.process.stderr }).on("line", (line) => this.stderr.push(line));
    this.process.once("error", (error) => this.rejectAll(error));
    this.process.once("exit", (code) => this.rejectAll(new Error(`stock Pi exited with ${String(code)}: ${this.stderr.join("\n")}`)));
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

async function startDeterministicProvider(): Promise<{ baseUrl: string; requests: JsonRecord[]; stop: () => Promise<void> }> {
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
    if (requests.length === 1) {
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

function successful(response: JsonRecord): void {
  assert.equal(response.success, true, JSON.stringify(response));
}

function customMessages(events: JsonRecord[], customType: string): JsonRecord[] {
  return events.filter((event) => {
    if (event.type !== "message_end") return false;
    const message = event.message;
    return message !== null && typeof message === "object" && !Array.isArray(message)
      && (message as JsonRecord).customType === customType;
  });
}

test("stock Pi 0.85 advertises the managed manifest through a deterministic real-model turn", { timeout: 30_000 }, async () => {
  const version = await new Promise<string>((resolveVersion, reject) => {
    const child = spawn("pi", ["--version"], { stdio: ["ignore", "pipe", "pipe"] });
    let output = "";
    child.stdout.on("data", (chunk: Buffer) => { output += chunk.toString(); });
    child.once("error", reject);
    child.once("exit", (code) => code === 0 ? resolveVersion(output.trim()) : reject(new Error(`pi --version exited with ${String(code)}`)));
  });
  assert.match(version, /0\.85\.0/);

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
    assert.equal(provider.requests.length, 1);
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
  } finally {
    await rpc.stop();
    await provider.stop();
    rmSync(home, { recursive: true, force: true });
  }
});
