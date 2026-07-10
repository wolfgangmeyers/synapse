import assert from "node:assert/strict";
import { createServer } from "node:http";
import { mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { spawn } from "node:child_process";
import test from "node:test";

const PI = "/Users/wolfgang.meyers/.nvm/versions/node/v26.2.0/bin/pi";
const EXPECT = "/usr/bin/expect";

const delay = (milliseconds) => new Promise((resolve) => setTimeout(resolve, milliseconds));

async function fakeProvider({ holdFirstResponse, id }) {
  const calls = [];
  let firstResponseActive = false;
  let firstResponseCompleted = false;
  let resolveFirstResponseActive;
  let resolveFirstResponseCompleted;
  let releaseFirstResponse;
  const firstResponseActiveSignal = new Promise((resolve) => { resolveFirstResponseActive = resolve; });
  const firstResponseCompletedSignal = new Promise((resolve) => { resolveFirstResponseCompleted = resolve; });
  const firstResponseRelease = new Promise((resolve) => { releaseFirstResponse = resolve; });
  const server = createServer((request, response) => {
    if (request.url === "/_probe/first-request-active") {
      firstResponseActiveSignal.then(() => {
        response.writeHead(200, { "content-type": "application/json" });
        response.end('{"first_request_active":true}');
      });
      return;
    }
    let raw = "";
    request.on("data", (chunk) => { raw += chunk; });
    request.on("end", () => {
      const call = calls.length + 1;
      const requestText = JSON.stringify(JSON.parse(raw || "{}"));
      calls.push({ call, hasPrimary: requestText.includes(id), hasBusy: requestText.includes(`${id}-busy`) });
      const complete = () => {
        response.write('data: {"id":"phase1-tui","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}\n\n');
        response.end("data: [DONE]\n\n");
      };
      response.writeHead(200, { "content-type": "text/event-stream", "cache-control": "no-cache" });
      response.write(`data: {"id":"phase1-tui","choices":[{"index":0,"delta":{"role":"assistant","content":"phase1-agent-boundary-${call}"},"finish_reason":null}]}\n\n`);
      if (call !== 1 || !holdFirstResponse) return complete();
      firstResponseActive = true;
      resolveFirstResponseActive();
      firstResponseRelease.then(() => {
        complete();
        firstResponseActive = false;
        firstResponseCompleted = true;
        resolveFirstResponseCompleted();
      });
    });
  });
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  return {
    calls,
    url: `http://127.0.0.1:${server.address().port}/v1`,
    firstRequestActiveUrl: `http://127.0.0.1:${server.address().port}/_probe/first-request-active`,
    firstResponseActive: firstResponseActiveSignal,
    firstResponseCompleted: firstResponseCompletedSignal,
    releaseFirstResponse,
    isFirstResponseActive: () => firstResponseActive,
    isFirstResponseCompleted: () => firstResponseCompleted,
    close: () => new Promise((resolve) => server.close(resolve)),
  };
}

function extensionSource() {
  return `import { appendFileSync } from "node:fs";
const evidencePath = process.env.PROBE_EVIDENCE;
const id = process.env.PROBE_ID;
const mode = process.env.PROBE_MODE;
const sentinel = "editor-sentinel-" + mode;
const record = (event, extra = {}) => appendFileSync(evidencePath, JSON.stringify({ event, ...extra }) + "\\n");
export default function (pi) {
  pi.registerProvider("phase1-tui", {
    baseUrl: process.env.PROBE_URL, apiKey: "probe", api: "openai-completions",
    models: [{ id: "phase1", name: "Phase 1 TUI probe", reasoning: false, input: ["text"], cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 }, contextWindow: 8192, maxTokens: 64 }]
  });
  pi.on("session_start", (_event, ctx) => {
    record("session_start", { mode: ctx.mode });
    ctx.ui.setEditorText(sentinel);
    const inject = (routerId) => pi.sendMessage({ customType: "phase1-tui-router", content: "[Router message]\\nMessage-ID: " + routerId, display: true, details: { router_id: routerId } }, { deliverAs: "steer", triggerTurn: true });
    inject(id);
    if (mode === "busy") void fetch(process.env.PROBE_FIRST_REQUEST_ACTIVE_URL)
      .then(async (response) => {
        if (!response.ok) throw new Error("first-request-active signal failed");
        const signal = await response.json();
        if (signal.first_request_active !== true) throw new Error("first-request-active signal was not positive");
        inject(id + "-busy");
        record("busy_injection_requested", { id: id + "-busy", first_request_active: true });
      })
      .catch((error) => record("busy_injection_error", { message: error.message }));
  });
  pi.on("context", (event) => record("context", { primary: JSON.stringify(event.messages).includes(id), busy: JSON.stringify(event.messages).includes(id + "-busy") }));
  pi.on("message_end", (event, ctx) => {
    const routerId = event.message.details?.router_id;
    if (event.message.role !== "custom" || ![id, id + "-busy"].includes(routerId)) return;
    record("message_end", { id: routerId, editor: ctx.ui.getEditorText(), unchanged: ctx.ui.getEditorText() === sentinel });
  });
  pi.on("agent_settled", (_event, ctx) => { record("agent_settled", { editor: ctx.ui.getEditorText(), unchanged: ctx.ui.getEditorText() === sentinel }); ctx.shutdown(); });
}`;
}

function runExpect(script, args, options) {
  let child;
  let settled = false;
  let stdout = "";
  let stderr = "";
  const completed = new Promise((resolve, reject) => {
    child = spawn(EXPECT, [script, ...args], options);
    const timer = setTimeout(() => child.kill("SIGKILL"), 25_000);
    child.stdout.on("data", (chunk) => { stdout += chunk; });
    child.stderr.on("data", (chunk) => { stderr += chunk; });
    child.on("error", reject);
    child.on("close", (code, signal) => {
      settled = true;
      clearTimeout(timer);
      resolve({ code, signal, stdout, stderr });
    });
  });
  const waitForOutput = async (predicate) => {
    const deadline = Date.now() + 15_000;
    while (Date.now() < deadline) {
      if (predicate(stdout)) return stdout;
      if (settled) break;
      await delay(20);
    }
    throw new Error("timed out waiting for parent-captured PTY output");
  };
  return { completed, output: () => stdout, waitForOutput, terminate: () => { if (!settled) child.kill("SIGKILL"); } };
}

async function readEvidence(path) {
  return (await readFile(path, "utf8").catch(() => "")).split("\n").filter(Boolean).map(JSON.parse);
}

async function waitForEvidence(path, predicate) {
  const deadline = Date.now() + 15_000;
  while (Date.now() < deadline) {
    const events = await readEvidence(path);
    if (predicate(events)) return events;
    await delay(20);
  }
  throw new Error(`timed out waiting for isolated TUI evidence: ${JSON.stringify(await readEvidence(path))}`);
}

async function startTuiProbe(mode) {
  const root = await mkdtemp(join(tmpdir(), "mecha-pi-tui-phase1-"));
  const evidencePath = join(root, "evidence.jsonl");
  const extension = join(root, "probe.ts");
  const expectScript = join(root, "run.expect");
  const id = `phase1-tui-${mode}`;
  const provider = await fakeProvider({ holdFirstResponse: mode === "busy", id });
  await writeFile(extension, extensionSource());
  await writeFile(expectScript, "set timeout 20\neval spawn -noecho $argv\nexpect eof\n");
  const env = { ...process.env, TERM: "xterm-256color", PI_CODING_AGENT_DIR: join(root, "agent"), PI_OFFLINE: "1", PROBE_EVIDENCE: evidencePath, PROBE_ID: id, PROBE_MODE: mode, PROBE_URL: provider.url, PROBE_FIRST_REQUEST_ACTIVE_URL: provider.firstRequestActiveUrl };
  const args = ["--offline", "--approve", "--verbose", "--no-skills", "--no-context-files", "--no-builtin-tools", "--provider", "phase1-tui", "--model", "phase1", "--session-dir", join(root, "sessions"), "--session-id", "phase1-tui", "--extension", extension];
  const execution = runExpect(expectScript, [PI, ...args], { cwd: root, env, stdio: ["ignore", "pipe", "pipe"] });
  return {
    id,
    provider,
    execution,
    events: () => readEvidence(evidencePath),
    waitForEvents: (predicate) => waitForEvidence(evidencePath, predicate),
    cleanup: async () => {
      provider.releaseFirstResponse();
      execution.terminate();
      await execution.completed.catch(() => {});
      await provider.close();
      await rm(root, { recursive: true, force: true });
    },
  };
}

for (const mode of ["idle", "busy"]) {
  test(`real isolated PTY/TUI ${mode} display delivery preserves the bottom editor`, { timeout: 30_000 }, async () => {
    const probe = await startTuiProbe(mode);
    try {
      const id = probe.id;
      if (mode === "busy") {
        const busyId = `${id}-busy`;
        await probe.provider.firstResponseActive;
        const activeEvents = await probe.waitForEvents((events) => events.some((event) => event.event === "busy_injection_requested" && event.id === busyId && event.first_request_active));
        const firstAssistantMarker = "phase1-agent-boundary-1";
        await probe.execution.waitForOutput((output) => output.includes(firstAssistantMarker));
        assert.equal(probe.provider.isFirstResponseActive(), true, "the first response must remain active when parent-captured PTY output contains its first assistant marker");
        assert.equal(probe.provider.isFirstResponseCompleted(), false, "the test must inspect the live PTY before completing the first response");
        assert.equal(probe.execution.output().includes(busyId), false, "the live PTY must not render the busy ID while the first response is active");
        assert.equal(activeEvents.some((event) => event.event === "busy_injection_error"), false, "busy injection must not fail while waiting for the provider signal");
        probe.provider.releaseFirstResponse();
        await probe.provider.firstResponseCompleted;
      }
      const events = await probe.waitForEvents((items) => items.some((item) => item.event === "agent_settled"));
      const result = await probe.execution.completed;
      assert.equal(result.code, 0, `${result.stderr}\n${result.stdout}`);
      assert.equal(events.find((event) => event.event === "session_start")?.mode, "tui", "expect must provide Pi a real pseudo-terminal");
      const messageEvents = events.filter((event) => event.event === "message_end");
      assert.equal(messageEvents.some((event) => event.id === id && event.unchanged), true);
      assert.equal(result.stdout.includes(id), true, "the displayed idle custom message must render in the PTY transcript");
      if (mode === "busy") {
        const busyId = `${id}-busy`;
        assert.equal(messageEvents.some((event) => event.id === busyId && event.unchanged), true);
        assert.equal(probe.provider.calls.length, 2, "busy delivery must cause a second agent-call boundary");
        assert.equal(probe.provider.calls[0].hasBusy, false, "busy ID must be absent from the active call context");
        assert.equal(probe.provider.calls[1].hasBusy, true, "busy ID must enter the next call context");
        assert.equal(result.stdout.includes(busyId), true, "the busy ID must render after the first response completes");
        assert.equal(events.some((event) => event.event === "context" && event.primary && event.busy), true, "busy custom message must be present at its next Pi context boundary");
      }
      assert.equal(events.find((event) => event.event === "agent_settled")?.unchanged, true, "editor sentinel must survive delivery and settlement");
    } finally {
      await probe.cleanup();
    }
  });
}
