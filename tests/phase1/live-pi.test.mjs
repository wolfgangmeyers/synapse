import assert from "node:assert/strict";
import { createServer } from "node:http";
import { mkdtemp, readFile, readdir, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { spawn } from "node:child_process";
import { createHash } from "node:crypto";
import test from "node:test";

const PI = "/Users/wolfgang.meyers/.nvm/versions/node/v26.2.0/bin/pi";
const PACKAGE = "/Users/wolfgang.meyers/.nvm/versions/node/v26.2.0/lib/node_modules/@earendil-works/pi-coding-agent";

function run(command, args, options) {
  return new Promise((resolve, reject) => {
    const child = spawn(command, args, { ...options, stdio: options.stdio ?? ["ignore", "pipe", "pipe"] });
    let stdout = "";
    let stderr = "";
    const timer = setTimeout(() => child.kill("SIGKILL"), 15_000);
    child.stdout.on("data", (chunk) => { stdout += chunk; });
    child.stderr.on("data", (chunk) => { stderr += chunk; });
    child.on("error", reject);
    child.on("close", (code, signal) => {
      clearTimeout(timer);
      resolve({ code, signal, stdout, stderr });
    });
  });
}

async function filesUnder(directory) {
  const entries = await readdir(directory, { recursive: true }).catch(() => []);
  return entries.filter((entry) => entry.endsWith(".jsonl")).map((entry) => join(directory, entry));
}

async function evidence(path) {
  return (await readFile(path, "utf8").catch(() => "")).split("\n").filter(Boolean).map(JSON.parse);
}

async function fakeProvider({ delayMs = 10, id } = {}) {
  const calls = [];
  const server = createServer((request, response) => {
    let raw = "";
    request.on("data", (chunk) => { raw += chunk; });
    request.on("end", () => {
      const payload = JSON.parse(raw || "{}");
      const serialized = JSON.stringify(payload);
      calls.push({ hasPrimary: serialized.includes(id), hasBusy: serialized.includes(`${id}-busy`) });
      setTimeout(() => {
        response.writeHead(200, { "content-type": "text/event-stream", "cache-control": "no-cache" });
        response.write('data: {"id":"phase1","choices":[{"index":0,"delta":{"role":"assistant","content":"probe complete"},"finish_reason":null}]}\n\n');
        response.write('data: {"id":"phase1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}\n\n');
        response.end("data: [DONE]\n\n");
      }, delayMs);
    });
  });
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  return { url: `http://127.0.0.1:${server.address().port}/v1`, calls, close: () => new Promise((resolve) => server.close(resolve)) };
}

function extensionSource() {
  return `import { appendFileSync } from "node:fs";
const evidencePath = process.env.PROBE_EVIDENCE;
const id = process.env.PROBE_ID;
const mode = process.env.PROBE_MODE;
const record = (event, extra = {}) => appendFileSync(evidencePath, JSON.stringify({ event, ...extra }) + "\\n");
record("module_loaded");
const hasId = (ctx, value = id) => ctx.sessionManager.getEntries().some((entry) => entry.type === "custom_message" && entry.details?.router_id === value);
export default function (pi) {
  record("factory_loaded");
  pi.registerProvider("phase1-probe", {
    baseUrl: process.env.PROBE_URL, apiKey: "probe", api: "openai-completions",
    models: [{ id: "phase1", name: "Phase 1 probe", reasoning: false, input: ["text"], cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 }, contextWindow: 8192, maxTokens: 64 }]
  });
  record("provider_registered");
  pi.on("session_start", (_event, ctx) => {
    record("session_start", { persisted: ctx.sessionManager.isPersisted() });
    if (mode === "inspect") { record("restart_history", { found: hasId(ctx) }); setTimeout(() => process.exit(0), 25); return; }
    const inject = (routerId) => pi.sendMessage({ customType: "phase1-router", content: "[Router message]\\nMessage-ID: " + routerId, display: true, details: { router_id: routerId } }, { deliverAs: "steer", triggerTurn: true });
    inject(id);
    record("injection_requested", { id, display: true, deliverAs: "steer", triggerTurn: true });
    if (mode === "busy") setTimeout(() => { inject(id + "-busy"); record("injection_requested", { id: id + "-busy", display: true, deliverAs: "steer", triggerTurn: true }); }, 35);
  });
  pi.on("context", (event) => record("context", { primary: JSON.stringify(event.messages).includes(id), busy: JSON.stringify(event.messages).includes(id + "-busy") }));
  pi.on("message_end", (event, ctx) => {
    const routerId = event.message.details?.router_id;
    if (event.message.role !== "custom" || ![id, id + "-busy"].includes(routerId)) return;
    record("message_end", { id: routerId, correlated: true, role: event.message.role });
    setTimeout(() => {
      record("post_event_history", { id: routerId, found: hasId(ctx, routerId), session_file: ctx.sessionManager.getSessionFile() ?? null });
      if (mode === "crash" && routerId === id) process.kill(process.pid, "SIGKILL");
    }, 0);
  });
  pi.on("agent_settled", () => { record("agent_settled"); if (mode !== "crash") setTimeout(() => process.exit(0), 25); });
  pi.on("session_shutdown", (event) => record("session_shutdown", { reason: event.reason }));
}`;
}

async function probe({ mode = "idle", noSession = false, providerDelay = 10, root, sessionDir, id }) {
  const evidencePath = join(root, `${mode}.jsonl`);
  const extension = join(root, `${mode}.ts`);
  const provider = await fakeProvider({ delayMs: providerDelay, id });
  await writeFile(extension, extensionSource());
  const env = { ...process.env, PI_CODING_AGENT_DIR: join(root, "agent"), PI_OFFLINE: "1", PROBE_URL: provider.url, PROBE_EVIDENCE: evidencePath, PROBE_ID: id, PROBE_MODE: mode };
  const args = ["--offline", "--approve", "--verbose", "--mode", "rpc", "--no-skills", "--no-context-files", "--no-builtin-tools", "--provider", "phase1-probe", "--model", "phase1", "--session-dir", sessionDir, "--session-id", "phase1-session", "--extension", extension];
  if (noSession) args.splice(1, 0, "--no-session");
  const result = await run(PI, args, { env, cwd: root, stdio: ["pipe", "pipe", "pipe"] });
  await provider.close();
  return { result, evidence: await evidence(evidencePath), calls: provider.calls };
}

function find(events, name) { return events.find((event) => event.event === name); }

test("real isolated Pi idle delivery correlates message_end and records post-event history", { timeout: 30_000 }, async () => {
  const root = await mkdtemp(join(tmpdir(), "mecha-pi-phase1-"));
  try {
    const id = "phase1-idle-1";
    const sessionDir = join(root, "sessions");
    const runResult = await probe({ root, sessionDir, id });
    assert.equal(runResult.result.code, 0, `${runResult.result.stderr}\n${runResult.result.stdout}\n${JSON.stringify(runResult.evidence)}`);
    assert.equal(find(runResult.evidence, "injection_requested")?.display, true);
    assert.equal(find(runResult.evidence, "message_end")?.correlated, true);
    assert.equal(find(runResult.evidence, "post_event_history")?.found, true);
    assert.ok(runResult.calls.length >= 1, "controllable local provider must receive the agent call");
    assert.equal(find(runResult.evidence, "context")?.primary, true, "Pi context event must include the routed custom message");
    const sessions = await filesUnder(sessionDir);
    assert.ok(sessions.length >= 1, "assistant completion must flush the persisted session");
    assert.match(await readFile(sessions[0], "utf8"), new RegExp(id));
  } finally { await rm(root, { recursive: true, force: true }); }
});

test("real isolated Pi busy delivery queues steer until a later provider boundary", { timeout: 30_000 }, async () => {
  const root = await mkdtemp(join(tmpdir(), "mecha-pi-phase1-"));
  try {
    const id = "phase1-busy-1";
    const runResult = await probe({ mode: "busy", providerDelay: 250, root, sessionDir: join(root, "sessions"), id });
    assert.equal(runResult.result.code, 0, runResult.result.stderr);
    assert.equal(runResult.evidence.filter((event) => event.event === "injection_requested").length, 2);
    assert.equal(runResult.evidence.filter((event) => event.event === "message_end" && event.id === id).length, 1);
    assert.equal(runResult.evidence.filter((event) => event.event === "message_end" && event.id === `${id}-busy`).length, 1, "busy routed ID must correlate through message_end");
    assert.equal(runResult.evidence.filter((event) => event.event === "post_event_history" && event.id === `${id}-busy`).some((event) => event.found), true, "busy routed ID must be found through history after emission");
    assert.equal(runResult.calls.length, 2, "local provider must observe two agent-call boundaries");
    assert.equal(runResult.calls[0].hasBusy, false, "busy routed ID must be absent from the first model-call context");
    assert.equal(runResult.calls[1].hasBusy, true, "busy routed ID must be present at the next model-call boundary");
    assert.equal(runResult.evidence.filter((event) => event.event === "context").some((event) => event.primary && event.busy), true, "second steer message must reach the later Pi context boundary");
  } finally { await rm(root, { recursive: true, force: true }); }
});

test("crash after positive history lookup proves a fresh session cannot resume the routed ID", { timeout: 30_000 }, async () => {
  const root = await mkdtemp(join(tmpdir(), "mecha-pi-phase1-"));
  try {
    const id = "phase1-crash-1";
    const sessionDir = join(root, "sessions");
    const crashed = await probe({ mode: "crash", providerDelay: 1_000, root, sessionDir, id });
    assert.equal(crashed.result.signal, "SIGKILL");
    assert.equal(find(crashed.evidence, "message_end")?.correlated, true);
    assert.equal(find(crashed.evidence, "post_event_history")?.found, true);
    assert.equal((await filesUnder(sessionDir)).length, 0, "Pi defers its session file before the first assistant message");
    const resumed = await probe({ mode: "inspect", root, sessionDir, id });
    assert.equal(resumed.result.code, 0, resumed.result.stderr);
    assert.equal(find(resumed.evidence, "restart_history")?.found, false);
  } finally { await rm(root, { recursive: true, force: true }); }
});

test("--no-session leaves only in-memory history and no durable session", { timeout: 30_000 }, async () => {
  const root = await mkdtemp(join(tmpdir(), "mecha-pi-phase1-"));
  try {
    const id = "phase1-no-session-1";
    const sessionDir = join(root, "sessions");
    const runResult = await probe({ noSession: true, root, sessionDir, id });
    assert.equal(runResult.result.code, 0, runResult.result.stderr);
    assert.equal(find(runResult.evidence, "post_event_history")?.found, true);
    assert.equal((await filesUnder(sessionDir)).length, 0);
  } finally { await rm(root, { recursive: true, force: true }); }
});

test("installed Pi artifact remains the pinned 0.80.6 primary source", async () => {
  const expected = {
    "package.json": "5f805b25823010b36e801735f07e57d831f0a474f95dca24ab90bca6e27e0aa0",
    "dist/core/agent-session.js": "b2b3b0e2b95ff88290232da4117920a6fd0cceb06bb5d66bb8f120fc934644e7",
    "dist/core/session-manager.js": "879e80cc6e2371e4b06887e6fb041c323ba4e86f7687bfdac6474c9f61486112",
    "docs/extensions.md": "82ea7eee960ade20310fc96e51d344042588d5e4319f2be29aeb76c65c14fb09",
    "docs/session-format.md": "8c9b05fc65ee5539cfdd14f250a0d153d2a0594490bb3792917271b237264ed5"
  };
  for (const [path, hash] of Object.entries(expected)) {
    assert.equal(createHash("sha256").update(await readFile(join(PACKAGE, path))).digest("hex"), hash, path);
  }
  assert.equal(JSON.parse(await readFile(join(PACKAGE, "package.json"), "utf8")).version, "0.80.6");
});

test("installed source preserves the event-before-persistence ordering under test", async () => {
  const source = await readFile(join(PACKAGE, "dist/core/agent-session.js"), "utf8");
  const eventEmission = source.indexOf("await this._emitExtensionEvent(event);");
  const persistence = source.indexOf("this.sessionManager.appendCustomMessageEntry(event.message.customType");
  assert.ok(eventEmission >= 0 && persistence >= 0 && eventEmission < persistence);
});
