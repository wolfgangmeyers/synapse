import { createServer, type ServerResponse } from "node:http";
import { execFileSync } from "node:child_process";
import { randomUUID } from "node:crypto";
import { DatabaseSync } from "node:sqlite";
import { open, mkdir, readdir, rm, writeFile } from "node:fs/promises";
import { join, resolve } from "node:path";
import { expect, type Page } from "@playwright/test";
import { startIsolatedRouter, startRouterProxy, serveFrontendDist, type IsolatedRouter, type IsolatedProxy, type IsolatedFrontend } from "./harness";

export type Reply = { tool: string; args: object } | { answer: string; evaluation?: true; advanceMS?: number; barrier?: Promise<void>; onRelease?: (destroyed: boolean) => void } | { failure: 503; evaluation: true; barrier?: Promise<void>; onRelease?: (destroyed: boolean) => void } | { hold: true };
export const model = { provider: "openai-codex", id: "gpt-6.1-terra" };

// ResolveModel bypasses the worker resolver for Claude evaluations. Only this
// Codex identity has a loopback-only transport in the tagged router launcher.
// Guard both Node REST and browser control helpers, not just individual specs.
export function assertLocalEvaluatorDefault(path: string, body?: object) {
  if (path !== "/v1/settings/default-model" || body === undefined) return;
  if (!("model" in body)) throw Error("Fixture default-model request is missing model");
  const selected = body.model;
  if (selected === null) return;
  if (typeof selected !== "object" || selected === null ||
    !("provider" in selected) || selected.provider !== model.provider ||
    !("id" in selected) || selected.id !== model.id) {
    throw Error("Fixture evaluator default must be null or the loopback-only Codex model; Google is not mocked");
  }
}

// go run owns a separate executable: close that child before closing its parent.
export async function closeWorkflowProxyChild(proxy: IsolatedProxy | undefined) {
  if (!proxy?.process.pid || proxy.process.exitCode !== null) return;
  let children = "";
  try { children = execFileSync("ps", ["-o", "pid=", "--ppid", String(proxy.process.pid)], { encoding: "utf8" }).trim(); }
  catch (e) { if (e.status !== 1) throw e; }
  for (const pid of children.split(/\s+/).filter(Boolean)) {
    try { process.kill(Number(pid), "SIGTERM"); } catch (e) { if (e.code !== "ESRCH") throw e; }
  }
}

// Only the provider boundary is mocked. Every response must be explicitly queued;
// unexpected evaluator/autonomous requests fail closed and remain observable.
export type WorkflowHarnessOptions = {
  // Keep the evaluator on the loopback-only Codex transport, or deliberately
  // clear the configured default. Never select seeded Google credentials.
  defaultModel?: typeof model | null;
  omitGoogle?: boolean;
  // Expected model for requests to the local Codex mock; defaults to Terra.
  expectedCodexModel?: string;
  expectedCodexModels?: string[];
  kbDir?: (home: string) => string;
  capacityFixture?: boolean;
  mcpQueueFixture?: boolean;
  backgroundExpiryInterval?: string;
  piReviewBarrier?: boolean;
  initialReplies?: Reply[];
  embeddedRoot?: { name: string; requestId: string; model: { provider: string; id: string }; instruction: string };
};
export async function workflowHarness(options: WorkflowHarnessOptions = {}) {
  const requests: Record<string, unknown>[] = [];
  const violations: string[] = [];
  const replies: Reply[] = [...(options.initialReplies ?? [])];
  const wsFrames: string[] = [];
  const held: ServerResponse[] = [];
  let fixture: { advance(ms: number): Promise<unknown> } | undefined;
  const server = createServer((req, res) => {
    let body = "";
    req.on("data", chunk => {
      body += chunk;
      if (Buffer.byteLength(body) > 2 * 1024 * 1024) { violations.push("body limit"); req.destroy(); }
    });
    req.on("end", async () => {
      if (req.method !== "POST" || req.url !== "/codex/responses" || requests.length >= 128) {
        violations.push(`unexpected route ${req.method} ${req.url}`); res.writeHead(400).end(); return;
      }
      if (!req.headers.authorization?.endsWith(".terra-test-secret") || req.headers["chatgpt-account-id"] !== "terra-test-account") {
        violations.push("synthetic auth mismatch"); res.writeHead(401).end(); return;
      }
      const parsed = JSON.parse(body);
      requests.push(parsed);
      const reply = replies.shift();
      const isEvaluation = typeof parsed.instructions === "string" && parsed.instructions.toLowerCase().startsWith("goal:");
      if (reply && (("evaluation" in reply && reply.evaluation === true) !== isEvaluation)) {
        violations.push(`unexpected ${isEvaluation ? "evaluator" : "worker"} request ${requests.length}: ${JSON.stringify({ instructions: parsed.instructions, input: parsed.input })}`);
        res.writeHead(400).end(); return;
      }
      if (!reply || parsed.model !== (options.expectedCodexModels?.[requests.length - 1] ?? options.expectedCodexModel ?? model.id)) {
        violations.push(`unexpected provider request ${requests.length}: ${parsed.model}`); res.writeHead(400).end(); return;
      }
      if ("hold" in reply) { held.push(res); return; }
      if (("answer" in reply || "failure" in reply) && reply.barrier) {
        held.push(res);
        await reply.barrier;
        reply.onRelease?.(res.destroyed);
        if (res.destroyed) return;
      }
      if ("failure" in reply) {
        res.writeHead(reply.failure, { "Content-Type": "application/json" });
        res.end(JSON.stringify({ error: { message: "deterministic evaluator failure", type: "server_error" } }));
        return;
      }
      if ("answer" in reply && reply.advanceMS) await fixture!.advance(reply.advanceMS);
      const id = `response_${requests.length}`;
      const events: object[] = [{ type: "response.created", response: { id } }];
      if ("tool" in reply) {
        const item = { type: "function_call", id: `fc_${id}`, call_id: `call_${id}`, name: reply.tool, arguments: JSON.stringify(reply.args) };
        events.push({ type: "response.output_item.added", output_index: 0, item: { ...item, arguments: "" } }, { type: "response.output_item.done", output_index: 0, item });
      } else {
        const item = { type: "message", id: `msg_${id}`, role: "assistant", content: [] };
        events.push({ type: "response.output_item.added", output_index: 0, item },
          { type: "response.content_part.added", output_index: 0, content_index: 0, part: { type: "output_text", text: "" } },
          { type: "response.output_text.delta", output_index: 0, content_index: 0, delta: reply.answer },
          { type: "response.output_text.done", output_index: 0, content_index: 0, text: reply.answer },
          { type: "response.output_item.done", output_index: 0, item: { ...item, status: "completed", content: [{ type: "output_text", text: reply.answer }] } });
      }
      events.push({ type: "response.completed", response: { id, status: "completed" } });
      res.writeHead(200, { "Content-Type": "text/event-stream" });
      res.end(events.map(e => `data: ${JSON.stringify(e)}\n\n`).join(""));
    });
  });
  await new Promise<void>(r => server.listen(0, "127.0.0.1", r));
  const address = server.address();
  if (!address || typeof address === "string") throw Error("missing mock address");
  let router: IsolatedRouter | undefined;
  let proxy: IsolatedProxy | undefined;
  let frontend: IsolatedFrontend | undefined;
  let lastModels: unknown = null;
  let checkingModels = false;

  // Diagnostics use a closed projection, not regex-only secret redaction.
  // No free-form errors/messages, account labels, URLs, auth or payload fields.
  const sanitize = (value: unknown): unknown => {
    if (Array.isArray(value)) return value.slice(-128).map(sanitize);
    if (!value || typeof value !== "object") return null;
    const result: Record<string, unknown> = {};
    for (const [key, item] of Object.entries(value)) {
      if (["models", "providers"].includes(key)) result[key] = sanitize(item);
      else if (["available", "authenticated", "auth_revision"].includes(key) &&
        (typeof item === "boolean" || typeof item === "number")) result[key] = item;
      else if (["provider", "id", "status", "type", "event", "operation", "level"].includes(key) &&
        typeof item === "string") {
        // Known fixture/catalog values and lifecycle enums only.
        result[key] = /^(openai-codex|google|google-vertex|cursor|gpt-6\.1-terra|authenticated|unauthenticated|available|unavailable|ready|pending|error|failed|info|warn|startup|managed_[a-z_]+)$/.test(item) ? item : "[omitted]";
      } else if (["timestamp", "time", "updated_at"].includes(key) && typeof item === "string" &&
        /^\d{4}-\d{2}-\d{2}T[\d:.+Z-]+$/.test(item)) result[key] = item;
    }
    return result;
  };
  const captureReadiness = async () => {
    if (!router) return;
    // One bounded directory per worker; retain at most 8 worker artifacts.
    const dir = "/tmp/workflow-retention-readiness-failures";
    await mkdir(dir, { recursive: true, mode: 0o700 });
    const file = `worker-${process.pid}.json`;
    const existing = (await readdir(dir)).filter(name => /^worker-\d+\.json$/.test(name) && name !== file).sort();
    for (const old of existing.slice(0, Math.max(0, existing.length - 7))) await rm(join(dir, old), { force: true });
    let providerStatus: unknown = { unavailable: true };
    try {
      // Existing read-only route: managed_http.go handleProviderAuthGET.
      const response = await fetch(router.httpUrl + "/v1/provider-auth", { signal: AbortSignal.timeout(2000) });
      providerStatus = { httpStatus: response.status, snapshot: sanitize(await response.json()) };
    } catch { /* Diagnostics must not replace the readiness failure. */ }
    const jsonl: unknown[] = [];
    try {
      const handle = await open(router.logPath, "r");
      try {
        const size = (await handle.stat()).size;
        const buffer = Buffer.alloc(Math.min(size, 32768));
        const { bytesRead } = await handle.read(buffer, 0, buffer.length, Math.max(0, size - buffer.length));
        const lines = buffer.subarray(0, bytesRead).toString("utf8").split("\n");
        if (size > buffer.length) lines.shift();
        for (const line of lines.slice(-64)) {
          try { jsonl.push(sanitize(JSON.parse(line))); } catch { /* partial line */ }
        }
      } finally { await handle.close(); }
    } catch { /* Log may not exist yet. */ }
    await writeFile(join(dir, file), JSON.stringify({
      lastModels, providerStatus, routerStderr: router.readinessStderr(), routerJSONL: jsonl,
    }).slice(0, 65536), { mode: 0o600 });
  };
  const close = async () => {
    try {
      await closeWorkflowProxyChild(proxy);
    } finally {
      for (const res of held) res.destroy();
      await Promise.all([proxy?.close(), router?.close(), frontend?.close()]);
      server.closeAllConnections();
      await new Promise<void>(r => server.close(() => r()));
      requests.length = replies.length = held.length = 0;
    }
  };
  try {
    const repoRoot = resolve(import.meta.dirname, "../..");
    router = await startIsolatedRouter({ repoRoot, managed: true, testClock: true, capacityFixture: options.capacityFixture, mcpQueueFixture: options.mcpQueueFixture, piReviewBarrier: options.piReviewBarrier, fakeEmbeddedCatalog: true, backgroundExpiryInterval: options.backgroundExpiryInterval, embeddedRoot: options.embeddedRoot, embedded: { endpoint: `http://127.0.0.1:${address.port}`, kbDir: options.kbDir, omitGoogle: options.omitGoogle } });
    proxy = await startRouterProxy({ repoRoot, upstreamPort: router.port });
    await expect.poll(async () => {
      try { return (await fetch(proxy!.url + "/v1/health")).status; } catch { return 0; }
    }, { timeout: 5000 }).toBe(200);
    frontend = await serveFrontendDist(process.env.MECHA_E2E_FRONTEND_DIST || join(repoRoot, "web/router-ui-react/dist"));
    const f = { router, proxy, frontend, requests, violations, replies, wsFrames, close,
      providerUrl: `http://127.0.0.1:${address.port}/codex/responses`,
      releaseHeld() { for (const res of held.splice(0)) res.destroy(); },
      async restart() {
        // Keep the provider server, request history and queued replies alive.
        // Do not replay browser lease/instance identity: normal reconnect owns it.
        await router!.restart();
      },
      clock: 0,
      async advance(delta: number) {
        await router!.advanceClock(delta);
        // The endpoint formats RFC3339 seconds. Preserve millisecond precision
        // locally so D-1/+1 assertions do not silently round the shared clock.
        f.clock += delta;
        return { now: new Date(f.clock).toISOString() };
      },
      async api(path: string, body?: object, method = body === undefined ? "GET" : "POST") {
        assertLocalEvaluatorDefault(path, body);
        const res = await fetch(proxy!.url + path, {
          method,
          headers: { ...(body === undefined ? {} : { "Content-Type": "application/json" }), "X-Request-ID": randomUUID() },
          ...(body === undefined ? {} : { body: JSON.stringify(body) }),
        });
        const text = await res.text();
        expect(res.ok, `${method} ${path}: ${res.status} ${text}`).toBeTruthy();
        return text ? JSON.parse(text) : null;
      },
      rows(sql: string, ...params: string[]) {
        const db = new DatabaseSync(router!.databasePath, { readOnly: true });
        try { return db.prepare(sql).all(...params); } finally { db.close(); }
      },
      async open(page: Page) {
        page.setDefaultTimeout(8000);
        page.on("websocket", socket => {
          expect(socket.url()).toContain(`127.0.0.1:${proxy!.port}`);
          socket.on("framereceived", frame => {
            if (wsFrames.length < 2048) wsFrames.push(String(frame.payload));
          });
        });
        await page.addInitScript(url => {
          const Native = window.WebSocket;
          const sockets: { socket: WebSocket; identity: Record<string, string> }[] = [];
          Object.assign(window, { workflowSendControl(frame: object) {
            const source = sockets.find(s => s.socket.readyState === Native.OPEN && s.identity.lease_id);
            if (!source) throw Error("UI has no registered Mailbox WS session");
            source.socket.send(JSON.stringify({ version: "v1", ...source.identity, ...frame }));
          } });
          window.WebSocket = class extends Native {
            constructor(url: string | URL, protocols?: string | string[]) {
              super(url, protocols);
              if (!String(url).endsWith("/v1/agents")) return;
              const entry = { socket: this, identity: {} };
              sockets.push(entry);
              this.addEventListener("message", event => {
                const frame = JSON.parse(String(event.data));
                if (frame.type === "registered") entry.identity = {
                  agent_id: frame.agent_id, instance_id: frame.instance_id, lease_id: frame.lease_id,
                };
              });
            }
          };
          localStorage.setItem("mecha.web.activeRouter", url);
          localStorage.setItem("mecha.web.routers", JSON.stringify([url]));
        }, proxy!.url);
        await page.goto(frontend!.url);
        await expect(page.getByRole("button", { name: "Managed actions", exact: true })).toBeVisible();
      },
    };
    fixture = f;
    // Provider discovery is asynchronous; select only after the real router
    // catalog advertises the deterministic fixture model as available.
    checkingModels = true;
    await expect.poll(async () => {
      const catalog = await f.api("/v1/models");
      lastModels = sanitize(catalog);
      return catalog.models.some((entry: { provider: string; id: string; available: boolean }) =>
        entry.provider === model.provider && entry.id === model.id && entry.available);
    }, { timeout: 8000 }).toBe(true);
    checkingModels = false;
    await f.api("/v1/settings/default-model", { model: options.defaultModel === undefined ? model : options.defaultModel }, "PUT");
    return f;
  } catch (error) {
    try {
      if (checkingModels) await captureReadiness();
    } catch { /* Keep original failure and always remove the disposable DB. */ }
    finally { await close(); }
    throw error;
  }
}
export type WorkflowHarness = Awaited<ReturnType<typeof workflowHarness>>;

// Join receipt -> evaluation -> reservation and the actual saved final by the
// complete identity. DONE has no reservation; retain it with nullable continuation
// fields. Other evaluations still require reservation evidence, excluding UNKNOWN.
// No production evidence is inserted or updated by tests.
export function settled(f: WorkflowHarness, agent: string) {
  return f.rows(`SELECT e.*, c.state AS continuation_state, c.due_at, c.started_at,
    r.turn_key, final.body AS final_body FROM embedded_goal_evaluations e
    JOIN embedded_goal_applied_receipts r ON r.agent_id=e.agent_id AND r.session_id=e.session_id
      AND r.epoch=e.epoch AND r.generation=e.generation AND r.turn_entry_seq=e.user_seq
      AND r.turn_entry_digest=e.user_digest AND r.goal_revision=e.goal_revision
    JOIN embedded_entries final ON final.agent_id=e.agent_id AND final.session_id=e.session_id
      AND final.epoch=e.epoch AND final.generation=e.generation AND final.seq=e.final_seq AND final.digest=e.final_digest
    LEFT JOIN embedded_goal_continuations c ON c.agent_id=e.agent_id AND c.session_id=e.session_id
      AND c.epoch=e.epoch AND c.generation=e.generation AND c.user_seq=e.user_seq
      AND c.user_digest=e.user_digest AND c.goal_revision=e.goal_revision
    JOIN embedded_append_keys final_receipt ON final_receipt.agent_id=e.agent_id
      AND final_receipt.session_id=e.session_id AND final_receipt.epoch=e.epoch
      AND final_receipt.generation=e.generation AND final_receipt.entry_seq=e.final_seq
    WHERE e.agent_id=? AND (c.agent_id IS NOT NULL OR e.state='DONE') ORDER BY e.user_seq`, agent);
}

export async function createRunningWorkflow(f: WorkflowHarness, page: Page, duration = 600000, reminders = true) {
  await f.open(page);
  const workflow = await f.api("/v1/workflows", { name: `Snooze ${randomUUID()}`, snooze_duration_ms: duration });
  f.clock = Date.parse(workflow.created_at);
  const { workflow_state: b } = await f.api("/v1/workflow-states", { workflow_id: workflow.id, name: "B", entry_text: "B guidance", allow_exit: true });
  const { workflow_state: a } = await f.api("/v1/workflow-states", { workflow_id: workflow.id, name: "A", entry_text: "A guidance", allow_exit: false,
    transitions: [{ target_state_id: b.id, description: "Advance" }],
    reminders: reminders ? [{ id: "steer", type: "one_shot", body: "State reminder steering", delay_seconds: 60 }] : [] });
  await f.api(`/v1/workflows/${workflow.id}`, { name: workflow.name, entry_state_id: a.id, snooze_duration_ms: duration }, "PUT");
  const run = await f.api(`/v1/workflows/${workflow.id}/run`, { cwd: join(f.router.root, "work") });
  await page.locator('button[role="tab"]:has-text("Background")').click();
  await expect(page.locator(`[data-agent-id="${run.agent_id}"]`)).toBeVisible();
  await page.locator(`[data-agent-id="${run.agent_id}"]`).click();
  return { agent: run.agent_id, workflow, a, b };
}

export async function userTurn(page: Page, text: string) {
  const composer = page.getByRole("textbox", { name: "Message", exact: true });
  await expect(composer).toBeVisible();
  await composer.fill(text);
  await composer.press("Enter");
}

// Inspect the actual provider-visible tool result, never echoed user input.
export function lastToolOutput(f: WorkflowHarness) {
  const input = f.requests.at(-1)?.input;
  expect(Array.isArray(input)).toBe(true);
  const results = input.filter((item: Record<string, unknown>) => item.type === "function_call_output");
  expect(results.length).toBeGreaterThan(0);
  return String(results.at(-1).output);
}
