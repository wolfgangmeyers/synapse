import assert from "node:assert/strict";
import { createServer } from "node:http";
import { mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { spawn } from "node:child_process";
import test from "node:test";

const PI = "/Users/wolfgang.meyers/.nvm/versions/node/v26.2.0/bin/pi";
const delay = (milliseconds) => new Promise((resolve) => setTimeout(resolve, milliseconds));

async function fakeProvider() {
  const server = createServer((request, response) => {
    request.resume();
    request.on("end", () => {
      response.writeHead(200, { "content-type": "text/event-stream", "cache-control": "no-cache" });
      response.write('data: {"id":"phase1-lifecycle","choices":[{"index":0,"delta":{"role":"assistant","content":"probe complete"},"finish_reason":null}]}\n\n');
      response.write('data: {"id":"phase1-lifecycle","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}\n\n');
      response.end("data: [DONE]\n\n");
    });
  });
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  return { url: `http://127.0.0.1:${server.address().port}/v1`, close: () => new Promise((resolve) => server.close(resolve)) };
}

function extensionSource() {
  return `import { appendFileSync } from "node:fs";
const evidencePath = process.env.PROBE_EVIDENCE;
const id = process.env.PROBE_ID;
const record = (event, extra = {}) => appendFileSync(evidencePath, JSON.stringify({ event, ...extra }) + "\\n");
const hasId = (ctx) => ctx.sessionManager.getEntries().some((entry) => entry.type === "custom_message" && entry.details?.router_id === id);
export default function (pi) {
  let resource;
  pi.registerProvider("phase1-lifecycle", {
    baseUrl: process.env.PROBE_URL, apiKey: "probe", api: "openai-completions",
    models: [{ id: "phase1", name: "Phase 1 lifecycle probe", reasoning: false, input: ["text"], cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 }, contextWindow: 8192, maxTokens: 64 }]
  });
  pi.registerCommand("phase1-reload", { handler: async (_args, ctx) => { await ctx.reload(); return; } });
  pi.on("session_start", (event, ctx) => {
    resource = setInterval(() => {}, 1_000);
    record("session_start", { reason: event.reason, found_router_id: hasId(ctx), session_file: ctx.sessionManager.getSessionFile() ?? null });
    if (event.reason === "startup") pi.sendMessage({ customType: "phase1-lifecycle-router", content: "[Router message]\\nMessage-ID: " + id, display: true, details: { router_id: id } }, { deliverAs: "steer", triggerTurn: true });
  });
  pi.on("message_end", (event) => {
    if (event.message.role === "custom" && event.message.details?.router_id === id) record("message_end", { id, correlated: true });
  });
  pi.on("agent_settled", () => record("agent_settled"));
  pi.on("session_shutdown", (event) => {
    const released = resource !== undefined;
    if (resource !== undefined) clearInterval(resource);
    resource = undefined;
    record("session_shutdown", { reason: event.reason, resource_released: released, target_session_file: event.targetSessionFile ?? null });
  });
}`;
}

class RpcProcess {
  constructor(child) {
    this.child = child;
    this.events = [];
    this.responses = new Map();
    this.waiters = new Map();
    this.buffer = "";
    child.stdout.on("data", (chunk) => this.read(chunk.toString()));
  }

  read(chunk) {
    this.buffer += chunk;
    while (true) {
      const newline = this.buffer.indexOf("\n");
      if (newline < 0) return;
      const line = this.buffer.slice(0, newline).replace(/\r$/, "");
      this.buffer = this.buffer.slice(newline + 1);
      if (!line) continue;
      const item = JSON.parse(line);
      this.events.push(item);
      if (item.type === "response" && item.id) {
        this.responses.set(item.id, item);
        this.waiters.get(item.id)?.(item);
        this.waiters.delete(item.id);
      }
    }
  }

  command(id, type, fields = {}) {
    this.child.stdin.write(`${JSON.stringify({ id, type, ...fields })}\n`);
    if (this.responses.has(id)) return Promise.resolve(this.responses.get(id));
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => { this.waiters.delete(id); reject(new Error(`timed out waiting for ${type}`)); }, 10_000);
      this.waiters.set(id, (response) => { clearTimeout(timer); resolve(response); });
    });
  }
}

async function evidence(path) {
  return (await readFile(path, "utf8").catch(() => "")).split("\n").filter(Boolean).map(JSON.parse);
}

async function waitFor(path, predicate) {
  const deadline = Date.now() + 12_000;
  while (Date.now() < deadline) {
    const entries = await evidence(path);
    if (predicate(entries)) return entries;
    await delay(20);
  }
  throw new Error(`timed out waiting for lifecycle evidence: ${JSON.stringify(await evidence(path))}`);
}

async function waitForClose(child) {
  return new Promise((resolve) => child.once("close", (code, signal) => resolve({ code, signal })));
}

test("real isolated Pi external process exercises reload, new, resume, fork, and every shutdown reason", { timeout: 45_000 }, async () => {
  const root = await mkdtemp(join(tmpdir(), "mecha-pi-lifecycle-phase1-"));
  const evidencePath = join(root, "evidence.jsonl");
  const extension = join(root, "probe.ts");
  const provider = await fakeProvider();
  await writeFile(extension, extensionSource());
  const child = spawn(PI, ["--offline", "--approve", "--verbose", "--mode", "rpc", "--no-skills", "--no-context-files", "--no-builtin-tools", "--provider", "phase1-lifecycle", "--model", "phase1", "--session-dir", join(root, "sessions"), "--session-id", "phase1-lifecycle", "--extension", extension], {
    cwd: root,
    env: { ...process.env, PI_CODING_AGENT_DIR: join(root, "agent"), PI_OFFLINE: "1", PROBE_EVIDENCE: evidencePath, PROBE_ID: "phase1-lifecycle-id", PROBE_URL: provider.url },
    stdio: ["pipe", "pipe", "pipe"]
  });
  const rpc = new RpcProcess(child);
  try {
    await waitFor(evidencePath, (entries) => entries.some((entry) => entry.event === "agent_settled"));
    const initial = await rpc.command("state-initial", "get_state");
    assert.equal(initial.success, true);
    const originalSession = initial.data.sessionFile;
    assert.ok(originalSession, "initial assistant completion must produce a persisted session for resume");

    assert.equal((await rpc.command("reload", "prompt", { message: "/phase1-reload" })).success, true);
    await waitFor(evidencePath, (entries) => entries.some((entry) => entry.event === "session_start" && entry.reason === "reload"));

    assert.equal((await rpc.command("new", "new_session")).success, true);
    await waitFor(evidencePath, (entries) => entries.some((entry) => entry.event === "session_start" && entry.reason === "new"));

    assert.equal((await rpc.command("resume", "switch_session", { sessionPath: originalSession })).success, true);
    await waitFor(evidencePath, (entries) => entries.some((entry) => entry.event === "session_start" && entry.reason === "resume"));

    assert.equal((await rpc.command("fork", "clone")).success, true);
    await waitFor(evidencePath, (entries) => entries.some((entry) => entry.event === "session_start" && entry.reason === "fork"));

    child.kill("SIGTERM");
    const closed = await waitForClose(child);
    assert.equal(closed.signal, null);

    const entries = await evidence(evidencePath);
    for (const reason of ["reload", "new", "resume", "fork", "quit"]) {
      assert.equal(entries.some((entry) => entry.event === "session_shutdown" && entry.reason === reason && entry.resource_released), true, `${reason} must release the session-scoped resource`);
    }
    assert.equal(entries.some((entry) => entry.event === "session_start" && entry.reason === "new" && entry.found_router_id === false), true, "new session must not deduplicate against the unrelated source branch");
    assert.equal(entries.some((entry) => entry.event === "session_start" && entry.reason === "resume" && entry.found_router_id === true), true, "resume must carry its persisted source branch history");
    assert.equal(entries.some((entry) => entry.event === "session_start" && entry.reason === "fork" && entry.found_router_id === true), true, "fork must carry its source branch history");
  } finally {
    if (!child.killed) child.kill("SIGKILL");
    await provider.close();
    await rm(root, { recursive: true, force: true });
  }
});
