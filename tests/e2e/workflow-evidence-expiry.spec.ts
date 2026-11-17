import { createServer, type ServerResponse } from "node:http";
import { randomUUID } from "node:crypto";
import { DatabaseSync } from "node:sqlite";
import { join, resolve } from "node:path";
import { writeFile } from "node:fs/promises";
import { test, expect } from "@playwright/test";
import { startIsolatedRouter, startRouterProxy, serveFrontendDist, type IsolatedRouter, type IsolatedProxy, type IsolatedFrontend } from "./harness";
import { closeWorkflowProxyChild, createRunningWorkflow, model, settled, userTurn, type WorkflowHarness } from "./workflow-implicit-goals-harness";

const DAY = 86400000;
const tables = ["embedded_goal_applied_receipts", "embedded_goal_evaluations", "embedded_goal_continuations", "embedded_goal_snooze_calls", "embedded_goal_snooze_preferences"];
type Reply = { tool: string; args: object } | { answer: string } | { hold: true };

// Test-local external provider mock: unlike the frozen shared harness, this
// exposes normal response release. No production hook or mutable SQL fixture.
async function fixture() {
  const requests: Record<string, unknown>[] = [];
  const violations: string[] = [];
  const replies: Reply[] = [];
  const held: { response: ServerResponse; id: string }[] = [];
  function respond(res: ServerResponse, id: string, reply: Exclude<Reply, { hold: true }>) {
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
    res.end(events.map(event => `data: ${JSON.stringify(event)}\n\n`).join(""));
  }
  const server = createServer((req, res) => {
    let body = "";
    req.on("data", chunk => { body += chunk; if (body.length > 2097152) req.destroy(); });
    req.on("end", () => {
      const request = JSON.parse(body);
      requests.push(request);
      const reply = replies.shift();
      if (req.url !== "/codex/responses" || req.method !== "POST" || request.model !== model.id ||
          !req.headers.authorization?.endsWith(".terra-test-secret") || req.headers["chatgpt-account-id"] !== "terra-test-account" ||
          String(request.instructions).toLowerCase().startsWith("goal:") || !reply || requests.length > 128) {
        violations.push(`unexpected provider request ${requests.length}`); res.writeHead(400).end(); return;
      }
      const id = `expiry_${requests.length}`;
      if ("hold" in reply) held.push({ response: res, id });
      else respond(res, id, reply);
    });
  });
  let ownedRouter: IsolatedRouter | undefined;
  let ownedProxy: IsolatedProxy | undefined;
  let ownedFrontend: IsolatedFrontend | undefined;
  let proxyStderr = "";
  const close = async () => {
    // Attempt every owned close, including the mock, even if another close fails.
    const results = await Promise.allSettled([
      (async () => {
        try { await closeWorkflowProxyChild(ownedProxy); }
        finally { await ownedProxy?.close(); }
      })(),
      ownedRouter?.close(), ownedFrontend?.close(),
      (async () => {
        for (const item of held) item.response.destroy();
        server.closeAllConnections();
        await new Promise<void>(done => server.close(() => done()));
      })(),
    ]);
    for (const result of results) if (result.status === "rejected") throw result.reason;
  };
  try {
    await new Promise<void>((done, reject) => {
      server.once("error", reject);
      server.listen(0, "127.0.0.1", done);
    });
    const address = server.address();
    if (!address || typeof address === "string") throw Error("missing mock address");
    const repoRoot = resolve(import.meta.dirname, "../..");
    const router = ownedRouter = await startIsolatedRouter({ repoRoot, managed: true, testClock: true, embedded: { endpoint: `http://127.0.0.1:${address.port}` } });
    const proxy = ownedProxy = await startRouterProxy({ repoRoot, upstreamPort: router.port });
    proxy.process.stderr?.on("data", (chunk: Buffer) => { proxyStderr = (proxyStderr + chunk.toString()).slice(-8192); });
    // The shared helper resolves on stdout READY, printed before ListenAndServe.
    // Match the established workflow harness: require actual HTTP readiness.
    await expect.poll(async () => {
      try { return (await fetch(proxy.url + "/v1/health", { signal: AbortSignal.timeout(2000) })).status; }
      catch { return 0; }
    }, { timeout: 5000 }).toBe(200);
    const frontend = ownedFrontend = await serveFrontendDist(join(repoRoot, "web/router-ui-react/dist"));
    const f: WorkflowHarness = {
      router, proxy, frontend, requests, violations, replies, wsFrames: [], clock: 0,
      async restart() { await router.restart(); },
      close,
      async advance(delta) { await f.api("/v1/test/clock/advance", { delta_ms: delta }); f.clock += delta; return { now: new Date(f.clock).toISOString() }; },
      async api(path, body, method = "POST") {
        const response = await fetch(proxy.url + path, body === undefined ? undefined : { method, headers: { "Content-Type": "application/json", "X-Request-ID": randomUUID() }, body: JSON.stringify(body) });
        const text = await response.text();
        expect(response.ok, `${method} ${path}: ${response.status} ${text}`).toBe(true);
        return text ? JSON.parse(text) : null;
      },
      rows(sql, agent) {
        const db = new DatabaseSync(router.databasePath, { readOnly: true });
        try { return db.prepare(sql).all(agent); } finally { db.close(); }
      },
      async open(page) {
        page.setDefaultTimeout(8000);
        page.on("websocket", socket => socket.on("framereceived", frame => { if (f.wsFrames.length < 2048) f.wsFrames.push(String(frame.payload)); }));
        await page.addInitScript(url => {
          localStorage.setItem("mecha.web.activeRouter", url);
          localStorage.setItem("mecha.web.routers", JSON.stringify([url]));
        }, proxy.url);
        await page.goto(frontend.url);
        await expect(page.getByRole("button", { name: "Managed actions", exact: true })).toBeVisible();
      },
    };
    await expect.poll(async () => (await f.api("/v1/models")).models.some(entry => entry.provider === model.provider && entry.id === model.id && entry.available), { timeout: 8000 }).toBe(true);
    await f.api("/v1/settings/default-model", { model }, "PUT");
    return Object.assign(f, {
      release(reply: string | Exclude<Reply, { hold: true }>) {
        expect(held).toHaveLength(1);
        const item = held.shift()!;
        expect(item.response.destroyed).toBe(false);
        respond(item.response, item.id, typeof reply === "string" ? { answer: reply } : reply);
      },
    });
  } catch (error) {
    // Single overwritten file, bounded tails and closed projections: no payloads,
    // credentials, arbitrary error strings or unbounded diagnostic histories.
    try {
      const health = async (url: string | undefined) => {
        if (!url) return "unallocated";
        try {
          const response = await fetch(url + "/v1/health", { signal: AbortSignal.timeout(2000) });
          return response.status === 200 ? "ready" : "http-error";
        } catch { return "unreachable"; }
      };
      const processState = (child: IsolatedProxy["process"] | undefined) => !child ? "unallocated"
        : child.signalCode !== null ? "signaled" : child.exitCode !== null ? "exited" : "running";
      const stderrState = (tail: string) => ({
        present: tail.length > 0,
        bindFailure: /address already in use|bind:/.test(tail),
        connectionRefused: /connection refused/.test(tail),
        panic: /panic:/.test(tail),
      });
      const [routerHealth, proxyHealth] = await Promise.all([health(ownedRouter?.httpUrl), health(ownedProxy?.url)]);
      await writeFile("/tmp/workflow-expiry-setup-failure.json", JSON.stringify({
        router: { process: processState(ownedRouter?.process), health: routerHealth, stderr: stderrState(ownedRouter?.readinessStderr().slice(-8192) ?? "") },
        proxy: { process: processState(ownedProxy?.process), health: proxyHealth, stderr: stderrState(proxyStderr) },
      }), { mode: 0o600 });
    } catch { /* Diagnostics must never replace the setup failure. */ }
    try { await close(); } catch { /* Preserve the original setup error. */ }
    throw error;
  }
}

function evidence(f: WorkflowHarness, agent: string) {
  return Object.fromEntries(tables.map(table => [table, f.rows(`SELECT * FROM ${table} WHERE agent_id=? ORDER BY rowid`, agent)]));
}
function execution(f: WorkflowHarness, agent: string) {
  return f.rows("SELECT execution_json FROM embedded_sessions WHERE agent_id=? AND current=1", agent)[0]?.execution_json;
}
function reservation(f: WorkflowHarness, agent: string) {
  return f.rows("SELECT * FROM embedded_goal_continuations WHERE agent_id=? ORDER BY user_seq", agent)[0];
}

// SCN-WFIG-014 backfill. These tests do not claim historical RED evidence.
test.describe("SCN-WFIG-014 pending expiry and active execution", () => {
  let f: Awaited<ReturnType<typeof fixture>>;
  test.beforeEach(async () => { f = undefined; f = await fixture(); });
  test.afterEach(async ({}, info) => {
    try {
      if (f && info.status !== info.expectedStatus) await writeFile(`/tmp/workflow-expiry-${info.testId.replace(/[^a-z0-9]/gi, "_")}.json`, JSON.stringify({ requests: f.requests, violations: f.violations, sessions: f.rows("SELECT * FROM embedded_sessions WHERE ? IS NOT NULL", "all"), evidence: tables.map(table => f.rows(`SELECT * FROM ${table} WHERE ? IS NOT NULL`, "all")) }, null, 2));
    } finally { await f?.close(); }
  });

  test("reserved pending survives R-1 then expires at R; supported unblock cannot resurrect it", async ({ page }) => {
    f.replies.push({ tool: "workflow_snooze", args: { justification: "Waiting for CI process", duration_ms: DAY } }, { answer: "Pending expiry wait saved" });
    const run = await createRunningWorkflow(f, page, 600000, false);
    await expect.poll(() => settled(f, run.agent).length).toBe(1);
    const original = reservation(f, run.agent);
    expect(original.state).toBe("reserved");
    const before = evidence(f, run.agent);
    const transcript = f.rows("SELECT * FROM embedded_entries WHERE agent_id=? ORDER BY seq", run.agent);
    const session = f.rows("SELECT session_id,epoch,generation FROM embedded_sessions WHERE agent_id=?", run.agent);
    const admission = Number(f.rows("SELECT retention_admitted_at_ms FROM embedded_entries WHERE agent_id=? AND seq=" + original.user_seq, run.agent)[0].retention_admitted_at_ms);
    expect(Number(original.retention_expires_at_ms)).toBe(Math.max(admission + 30 * DAY, Date.parse(String(original.due_at))) + 30 * DAY);
    // Native supported blocked-state API. A new user turn must not retire the
    // saved reservation: prove that contract rather than seed a blocked row.
    f.replies.push({ tool: "mecha_blocked", args: { action: "set", reason: "Human decision required before continuing" } }, { answer: "Waiting for the human decision" });
    await userTurn(page, "Pause autonomous progress pending my decision. Mark yourself blocked.");
    await expect(page.getByText("Waiting for the human decision", { exact: true }).first()).toBeVisible();
    await expect.poll(() => execution(f, run.agent)).toBeNull();
    expect(f.rows("SELECT blocked FROM managed_agents WHERE agent_id=?", run.agent)[0].blocked).toBe(1);
    expect(reservation(f, run.agent)).toEqual(original);
    const count = f.requests.length;
    const r = Number(original.retention_expires_at_ms);
    expect(Number.isSafeInteger(r)).toBe(true);
    await f.advance(r - f.clock - 1);
    expect(reservation(f, run.agent)).toEqual(original);
    // All exact original dependencies survive, even if blocking added a receipt.
    const retained = evidence(f, run.agent);
    for (const table of tables) for (const row of before[table]) expect(retained[table]).toContainEqual(row);
    expect(f.requests).toHaveLength(count);
    await f.advance(1);
    await expect.poll(() => tables.map(table => evidence(f, run.agent)[table].length)).toEqual([0, 0, 0, 0, 0]);
    expect(f.requests).toHaveLength(count);
    const afterExpiry = f.rows("SELECT * FROM embedded_entries WHERE agent_id=? ORDER BY seq", run.agent);
    for (const row of transcript) expect(afterExpiry).toContainEqual(row);
    expect(f.rows("SELECT session_id,epoch,generation FROM embedded_sessions WHERE agent_id=?", run.agent)).toEqual(session);
    expect(f.rows("SELECT * FROM pragma_foreign_key_check WHERE ? IS NOT NULL", run.agent)).toEqual([]);
    f.replies.push({ tool: "mecha_blocked", args: { action: "clear" } }, { tool: "workflow_snooze", args: { justification: "Waiting for CI process", duration_ms: DAY } }, { answer: "Human decision received; fresh work saved" });
    await userTurn(page, "Decision supplied. Clear blocked state and snooze the new work for one day.");
    await expect(page.getByText("Human decision received; fresh work saved", { exact: true }).first()).toBeVisible();
    await expect.poll(() => execution(f, run.agent)).toBeNull();
    expect(f.rows("SELECT blocked FROM managed_agents WHERE agent_id=?", run.agent)[0].blocked).toBe(0);
    await f.advance(60000);
    await expect.poll(() => f.rows("SELECT * FROM embedded_goal_continuations WHERE agent_id=?", run.agent).length).toBe(1);
    const fresh = f.rows("SELECT * FROM embedded_goal_continuations WHERE agent_id=?", run.agent);
    expect(fresh[0].user_seq).not.toBe(original.user_seq);
    expect(fresh[0].state).toBe("reserved");
    for (let i = 0; i < 2; i++) await f.advance(60000);
    for (const table of tables) {
      const seq = table === "embedded_goal_applied_receipts" ? "turn_entry_seq" : "user_seq";
      expect(f.rows(`SELECT * FROM ${table} WHERE agent_id=? AND ${seq}=${original.user_seq}`, run.agent), table).toEqual([]);
    }
    expect(f.requests).toHaveLength(count + 3);
    expect(f.violations).toEqual([]);
    expect(f.replies).toEqual([]);
  });

  test("real held continuation preserves exact source/current past R, settles normally then prunes", async ({ page }) => {
    f.replies.push({ tool: "workflow_snooze", args: { justification: "Waiting for CI process", duration_ms: DAY } }, { answer: "Execution expiry wait saved" });
    const run = await createRunningWorkflow(f, page, 600000, false);
    await expect.poll(() => settled(f, run.agent).length).toBe(1);
    const source = reservation(f, run.agent);
    f.replies.push({ tool: "workflow_snooze", args: { justification: "Waiting for CI process", duration_ms: DAY } }, { hold: true });
    await f.advance(Date.parse(String(source.due_at)) - f.clock);
    await expect.poll(() => f.requests.length).toBe(4);
    await expect.poll(() => reservation(f, run.agent).state).toBe("started");
    const slot = execution(f, run.agent);
    expect(typeof slot).toBe("string");
    const identity = JSON.parse(String(slot));
    expect(identity.kind).toBe("sdk");
    expect(identity.source.UserSeq).toBe(source.user_seq);
    expect(identity.source.UserDigest).toBe(source.user_digest);
    expect(identity.source.GoalRevision).toBe(source.goal_revision);
    const verdict = settled(f, run.agent)[0];
    expect(identity.source.FinalSeq).toBe(verdict.final_seq);
    expect(identity.source.FinalDigest).toBe(verdict.final_digest);
    const current = f.rows("SELECT * FROM embedded_entries WHERE agent_id=? AND seq=" + identity.current.UserSeq, run.agent)[0];
    expect(identity.current.UserDigest).toBe(current.digest);
    const started = reservation(f, run.agent);
    expect(identity.current.UserSeq).not.toBe(identity.source.UserSeq);
    const protectedRows = evidence(f, run.agent);
    for (const table of tables) expect(protectedRows[table].length, table).toBeGreaterThan(0);
    const transcript = f.rows("SELECT * FROM embedded_entries WHERE agent_id=? ORDER BY seq", run.agent);
    // Past both the original reservation R and current user's 60-day horizon.
    await f.advance(61 * DAY);
    expect(execution(f, run.agent)).toBe(slot);
    expect(reservation(f, run.agent)).toEqual(started);
    expect(evidence(f, run.agent)).toEqual(protectedRows);
    expect(f.rows("SELECT * FROM embedded_entries WHERE agent_id=? ORDER BY seq", run.agent)).toEqual(transcript);
    expect(f.requests).toHaveLength(4);
    // Once the expired execution settles, an active unsnoozed workflow may
    // dispatch fresh idle work (kickoffIdleGoalContinuation's nil-source path).
    // Hold it to prove identity, not merely tolerate an extra provider request.
    f.replies.push({ hold: true });
    f.release("Held execution completed normally");
    await expect(page.getByText("Held execution completed normally", { exact: true }).first()).toBeVisible();
    // Settlement may immediately start fresh idle work. A transient null slot
    // is not observable reliably; prove replacement by its full identity.
    await expect.poll(() => f.requests.length).toBe(5);
    const freshSlot = execution(f, run.agent);
    const freshIdentity = JSON.parse(String(freshSlot));
    const settledTranscript = f.rows("SELECT * FROM embedded_entries WHERE agent_id=? AND seq<" + freshIdentity.current.UserSeq + " ORDER BY seq", run.agent);
    const lastSettledSeq = Number(settledTranscript.at(-1)!.seq);
    expect(freshIdentity.kind).toBe("sdk");
    expect(freshIdentity.source).toBeNull();
    expect(freshIdentity.token).toBeGreaterThan(identity.token);
    expect(freshIdentity.current.UserSeq).toBeGreaterThan(lastSettledSeq);
    const freshUser = f.rows("SELECT * FROM embedded_entries WHERE agent_id=? AND seq=" + freshIdentity.current.UserSeq, run.agent)[0];
    expect(freshUser.kind).toBe("user");
    expect(freshIdentity.current.UserDigest).toBe(freshUser.digest);
    expect(Number(freshUser.retention_admitted_at_ms)).toBe(f.clock);
    const requestInput = f.requests[4].input;
    expect(Array.isArray(requestInput)).toBe(true);
    // Exact historical request prefix plus the completed response and NEW user.
    expect(requestInput).toEqual([
      ...f.requests[3].input as unknown[],
      expect.objectContaining({ role: "assistant", content: [expect.objectContaining({ text: "Held execution completed normally" })] }),
      expect.objectContaining({ role: "user", content: [expect.objectContaining({ text: expect.stringContaining("or call workflow_snooze.") })] }),
    ]);
    const assertNoExpiredReplay = () => {
      for (const table of tables) {
        const seq = table === "embedded_goal_applied_receipts" ? "turn_entry_seq" : "user_seq";
        expect(f.rows(`SELECT * FROM ${table} WHERE agent_id=? AND ${seq} IN (${identity.source.UserSeq},${identity.current.UserSeq})`, run.agent), table).toEqual([]);
      }
    };
    // Clearing execution revokes protection; physical deletion is owned by the
    // automatic cleanup sweep (spec bound: 60 seconds), not SDK settlement.
    await f.advance(60000);
    expect(f.requests).toHaveLength(5);
    expect(execution(f, run.agent)).toBe(freshSlot);
    assertNoExpiredReplay();
    for (let i = 0; i < 2; i++) {
      await f.advance(60000);
      expect(f.requests).toHaveLength(5);
      expect(execution(f, run.agent)).toBe(freshSlot);
      assertNoExpiredReplay();
    }
    // Normal fresh SDK settlement, with a real snooze preventing another idle
    // dispatch. Neither blocking nor termination stands in for completion.
    f.replies.push({ answer: "Fresh workflow dispatch snoozed normally" });
    f.release({ tool: "workflow_snooze", args: { justification: "Waiting for CI process", duration_ms: DAY } });
    await expect(page.getByText("Fresh workflow dispatch snoozed normally", { exact: true }).first()).toBeVisible();
    await expect.poll(() => execution(f, run.agent)).toBeNull();
    await expect.poll(() => reservation(f, run.agent)?.state).toBe("reserved");
    expect(reservation(f, run.agent).user_seq).toBe(freshIdentity.current.UserSeq);
    for (let i = 0; i < 2; i++) await f.advance(60000);
    assertNoExpiredReplay();
    expect(f.requests).toHaveLength(6);
    for (const row of settledTranscript) expect(f.rows("SELECT * FROM embedded_entries WHERE agent_id=? ORDER BY seq", run.agent)).toContainEqual(row);
    expect(f.rows("SELECT * FROM pragma_foreign_key_check WHERE ? IS NOT NULL", run.agent)).toEqual([]);
    expect(f.wsFrames.some(frame => frame.includes(run.agent))).toBe(true);
    expect(f.violations).toEqual([]);
    expect(f.replies).toEqual([]);
  });
});
