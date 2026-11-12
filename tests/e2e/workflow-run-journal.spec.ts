import { expect, test } from "@playwright/test";
import { createRunningWorkflow, userTurn, workflowHarness, type WorkflowHarness } from "./workflow-implicit-goals-harness";

// WJ-01/02/03: only the external LLM is scripted; the browser, proxy, router,
// managed tools and SQLite are real. See the journal spec for the full matrix.
test.describe("Workflow run journal", () => {
  test.setTimeout(90_000);
  let f: WorkflowHarness;
  test.beforeEach(async () => { f = await workflowHarness(); });
  test.afterEach(async () => { if (f) await f.close(); });

  function owns(text: string) {
    return (request: Record<string, unknown>) => Array.isArray(request.input) &&
      JSON.stringify(request.input.filter((item: any) => item?.role === "user").at(-1)).includes(text);
  }
  function resultFor(tool: string): string {
    for (const request of [...f.requests].reverse()) {
      if (!Array.isArray(request.input)) continue;
      const call = request.input.filter((item: any) => item.name === tool).at(-1);
      const result = request.input.find((item: any) => item.type === "function_call_output" && item.call_id === call?.call_id);
      if (result) return String(result.output);
    }
    throw Error(`No provider-visible result for ${tool}`);
  }
  async function settled(agent: string, answer: string) {
    await expect.poll(() => f.rows("SELECT CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? AND kind='assistant'", agent)
      .some(row => String(row.body).includes(answer))).toBe(true);
    await expect.poll(() => f.rows("SELECT activity_state FROM managed_agents WHERE agent_id=?", agent)[0]?.activity_state).toBe("idle");
  }
  function assertPrompt(output: string) {
    expect(output).toMatch(/journal/i);
    expect(output).toMatch(/outcome/i);
    expect(output).toMatch(/decision/i);
    expect(output).toMatch(/obstacle/i);
    expect(output).toMatch(/next steps/i);
    expect(output).toMatch(/run[_ -]?id/i);
    expect(output).toMatch(/event[_ -]?id/i);
  }

  test("WJ-01/02: transition and exit prompt for a separate human-facing entry", async ({ page }) => {
    f.replies.push({ tool: "workflow_snooze", args: { duration_minutes: 5 }, ownsRequest: owns("[Workflow Activated: A]") },
      { answer: "Journal fixture ready", ownsRequest: owns("[Workflow Activated: A]") });
    const { agent, b } = await createRunningWorkflow(f, page, 600000, false);
    await settled(agent, "Journal fixture ready");
    f.replies.push(
      { tool: "mecha_workflow_transition", args: { action: "transition", target_state: "B", reason: "Decision rationale" }, ownsRequest: owns("Advance journal") },
      { tool: "workflow_snooze", args: { duration_minutes: 5 }, ownsRequest: owns("Advance journal") },
      { answer: "Advanced journal", ownsRequest: owns("Advance journal") },
    );
    await userTurn(page, "Advance journal");
    await settled(agent, "Advanced journal");
    expect((await f.api(`/v1/managed-agents/${agent}/workflow-state`)).active_workflow_state_id).toBe(b.id);
    const transition = resultFor("mecha_workflow_transition");
    expect(transition).toContain("B guidance");
    assertPrompt(transition);

    f.replies.push(
      { tool: "mecha_workflow_transition", args: { action: "exit" }, ownsRequest: owns("Exit journal") },
      { answer: "Exited journal", ownsRequest: owns("Exit journal") },
    );
    await userTurn(page, "Exit journal");
    await settled(agent, "Exited journal");
    expect((await f.api(`/v1/managed-agents/${agent}/workflow-state`)).active_workflow_state_id).toBeFalsy();
    assertPrompt(resultFor("mecha_workflow_transition"));
  });

  test("WJ-04/07: active run survives restart and rejects deletion", async ({ page }) => {
    f.replies.push({ tool: "workflow_snooze", args: { duration_minutes: 5 }, ownsRequest: owns("[Workflow Activated: A]") },
      { answer: "Journal restart ready", ownsRequest: owns("[Workflow Activated: A]") });
    const { agent, workflow } = await createRunningWorkflow(f, page, 600000, false);
    await settled(agent, "Journal restart ready");
    const path = `/v1/managed-agents/${agent}/workflow-journal`;
    const list = await f.api(path);
    function activeRuns(value: unknown): Record<string, any>[] {
      if (Array.isArray(value)) return value.flatMap(activeRuns);
      if (!value || typeof value !== "object") return [];
      const obj = value as Record<string, any>;
      return [...(obj.status === "active" && (obj.run_id || obj.id) ? [obj] : []),
        ...Object.values(obj).flatMap(activeRuns)];
    }
    const runs = activeRuns(list);
    expect(runs).toHaveLength(1);
    expect(JSON.stringify(runs[0])).toContain(workflow.id);
    const id = String(runs[0].run_id ?? runs[0].id);
    const detailPath = `${path}/${encodeURIComponent(id)}`;
    expect(JSON.stringify(await f.api(detailPath))).toContain(workflow.id);
    const deletion = await fetch(f.proxy.url + detailPath, { method: "DELETE" });
    expect(deletion.status).toBe(409);
    expect(JSON.stringify(await f.api(detailPath))).toContain(id);
    await f.restart();
    const after = await f.api(path);
    expect(activeRuns(after).map(run => String(run.run_id ?? run.id))).toEqual([id]);
    expect(JSON.stringify(await f.api(detailPath))).toContain(workflow.id);
  });

  test("WJ-03: chart covers retained visits beyond the first timeline page", async ({ page }) => {
    f.replies.push({ tool: "workflow_snooze", args: { duration_minutes: 5 }, ownsRequest: owns("[Workflow Activated: A]") },
      { answer: "Pagination fixture ready", ownsRequest: owns("[Workflow Activated: A]") });
    const { agent } = await createRunningWorkflow(f, page, 600000, false);
    await settled(agent, "Pagination fixture ready");
    const path = `/v1/managed-agents/${agent}/workflow-journal`;
    const run = (await f.api(path)).runs[0];
    const { DatabaseSync } = await import("node:sqlite");
    const db = new DatabaseSync(f.router.databasePath);
    db.exec("PRAGMA busy_timeout = 10000");
    try {
      const insert = db.prepare("INSERT INTO workflow_journal_events(id,run_id,sequence,kind,occurred_at,workflow_id,workflow_name,state_id,state_name) VALUES(?,?,?,?,?,?,?,?,?)");
      for (let i = 0; i < 100; i++) insert.run(`pagination-note-${i}`, run.id, 1000 + i, "note", "2026-01-01T00:00:00Z", "", "", "", "");
      insert.run("pagination-visit", run.id, 1100, "visit", "2026-01-01T00:00:00Z", "late-w", "Later workflow", "late-s", "Later state");
      insert.run("pagination-end", run.id, 1101, "state_lost", "2026-01-01T00:00:07Z", "", "", "", "");
      db.prepare("UPDATE workflow_journal_runs SET incomplete=1, cutoff_sequence=42 WHERE id=?").run(run.id);
    } finally { db.close(); }
    const response = await f.api(`${path}/${run.id}?limit=1`);
    expect(response.next_cursor).toBeTruthy();
    expect(response.state_totals.find((item: any) => item.state_id === "late-s").duration_ms).toBe(7000);
    await page.getByRole("button", { name: /workflow journal/i }).click();
    const dialog = page.getByRole("dialog", { name: /workflow journal/i });
    await dialog.getByRole("button", { name: /active/i }).first().click();
    await expect(dialog.getByText("Later workflow / Later state")).toBeVisible();
    await expect(dialog.getByText(/retained history begins at sequence 42/i)).toBeVisible();
    await expect(dialog.getByRole("button", { name: /load more visits/i })).toBeVisible();
  });

  test("WJ-03: journal remains accessible after reload", async ({ page }) => {
    f.replies.push({ tool: "workflow_snooze", args: { duration_minutes: 5 }, ownsRequest: owns("[Workflow Activated: A]") },
      { answer: "Journal UI ready", ownsRequest: owns("[Workflow Activated: A]") });
    const { agent, workflow } = await createRunningWorkflow(f, page, 600000, false);
    await settled(agent, "Journal UI ready");
    const action = page.getByRole("button", { name: /workflow journal/i });
    await expect(action).toBeVisible();
    await action.click();
    const dialog = page.getByRole("dialog", { name: /workflow journal/i });
    await expect(dialog).toBeVisible();
    await expect(dialog).toContainText(workflow.name);
    await page.reload();
    await page.locator('button[role="tab"]:has-text("Background")').click();
    await page.locator(`[data-agent-id="${agent}"]`).click();
    await action.click();
    await expect(dialog).toContainText(workflow.name);
  });
});

// These cases deliberately exercise public mutations through the proxy and use
// SQLite only to observe committed identities and side effects.
test.describe("Workflow journal lifecycle boundaries", () => {
  test.setTimeout(90_000);
  let f: WorkflowHarness;
  const owns = (text: string) => (request: Record<string, unknown>) => Array.isArray(request.input) &&
    JSON.stringify(request.input.filter((item: any) => item?.role === "user").at(-1)).includes(text);
  const journal = (agent: string) => `/v1/managed-agents/${agent}/workflow-journal`;
  const runs = (agent: string) => f.rows("SELECT * FROM workflow_journal_runs WHERE agent_id=? ORDER BY started_at,id", agent);
  const events = (run: string) => f.rows("SELECT * FROM workflow_journal_events WHERE run_id=? ORDER BY sequence", run);
  async function ready(page: import("@playwright/test").Page) {
    f.replies.push({ tool: "workflow_snooze", args: { duration_minutes: 5 }, ownsRequest: owns("[Workflow Activated: A]") },
      { answer: "Fixture ready", ownsRequest: owns("[Workflow Activated: A]") });
    const fixture = await createRunningWorkflow(f, page, 600000, false);
    await done(fixture.agent, "Fixture ready");
    return fixture;
  }
  async function done(agent: string, text: string) {
    await expect.poll(() => f.rows("SELECT CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? AND kind='assistant'", agent)
      .some(row => String(row.body).includes(text))).toBe(true);
    await expect.poll(() => f.rows("SELECT activity_state FROM managed_agents WHERE agent_id=?", agent)[0]?.activity_state).toBe("idle");
  }
  async function turn(page: import("@playwright/test").Page, agent: string, prompt: string, tool: string, args: object) {
    f.replies.push({ tool, args, ownsRequest: owns(prompt) },
      { answer: `Done: ${prompt}`, ownsRequest: owns(prompt) });
    await userTurn(page, prompt);
    await done(agent, `Done: ${prompt}`);
  }
  test.beforeEach(async ({}, info) => { f = await workflowHarness(info.title.includes("concurrent keyed") ? { inspectRequest: request => {
    const last = Array.isArray(request.input) ? request.input.filter((item: any) => item.role === "user").at(-1) : null;
    console.log("PROVIDER TRACE", new Date().toISOString(), JSON.stringify(last));
    return undefined;
  } } : {}); });
  test.afterEach(async () => { if (f) await f.close(); });

  test("WJ-02/04: late entry targets exit and deletion removes only terminal history", async ({ page }) => {
    const { agent, workflow } = await ready(page);
    const run = String(runs(agent)[0].id);
    await turn(page, agent, "Advance for exit", "mecha_workflow_transition", { action: "transition", target_state: "B", reason: "Chosen path" });
    await turn(page, agent, "Exit for entry", "mecha_workflow_transition", { action: "exit", reason: "Finished work" });
    const terminal = runs(agent)[0];
    expect(terminal.status).toBe("terminal");
    const exit = events(run).find(event => event.kind === "end");
    expect(exit).toBeTruthy();
    expect(exit!.reason).toBe("Finished work");
    const exitID = String(exit!.id);
    const providerInput = f.requests.flatMap(request => Array.isArray(request.input) ? request.input : []);
    const output = providerInput.filter((item: any) => item.type === "function_call_output").map((item: any) => String(item.output));
    expect(output.some(text => text.includes(run) && text.includes(exitID))).toBe(true);
    await turn(page, agent, "Write exit entry", "workflow_journal_note", { run_id: run, target_event_id: exitID, source: "prompted", text: "Finished with a clear next step." });
    const written = events(run).find(event => event.text === "Finished with a clear next step.");
    expect(written?.target_event_id).toBe(exitID);
    expect(written?.source).toMatch(/prompt/);
    expect(JSON.stringify(await f.api(`${journal(agent)}/${run}`))).toContain("Finished with a clear next step.");
    await f.restart();
    expect(events(run).some(event => event.target_event_id === exitID && event.text === written?.text)).toBe(true);
    const url = f.proxy.url + `${journal(agent)}/${run}`;
    expect((await fetch(url, { method: "DELETE" })).status).toBe(204);
    expect((await fetch(url, { method: "DELETE" })).status).toBe(404);
    expect(events(run)).toHaveLength(0);
    expect((await fetch(f.proxy.url + `/v1/workflows/${workflow.id}`)).status).toBe(200);
    expect(f.rows("SELECT agent_id FROM managed_agents WHERE agent_id=?", agent)).toHaveLength(1);
    await turn(page, agent, "Write to deleted exit", "workflow_journal_note", { run_id: run, target_event_id: exitID, source: "prompted", text: "Must not reappear" });
    expect(runs(agent)).toHaveLength(0);
  });

  test("WJ-02: rejected notes leave no events and prompted entry follows its original transition", async ({ page }) => {
    const { agent, b } = await ready(page);
    const run = String(runs(agent)[0].id);
    const initial = events(run);
    await turn(page, agent, "Blank journal note", "workflow_journal_note", { source: "proactive", text: "   " });
    expect(events(run)).toEqual(initial);
    await turn(page, agent, "Oversized journal note", "workflow_journal_note", { source: "proactive", text: "x".repeat(4097) });
    expect(events(run)).toEqual(initial);
    await turn(page, agent, "Missing journal target", "workflow_journal_note", { source: "prompted", run_id: run, target_event_id: "missing", text: "not attached" });
    expect(events(run)).toEqual(initial);
    await turn(page, agent, "Advance before late entry", "mecha_workflow_transition", { action: "transition", target_state: "B" });
    const visit = events(run).find(event => event.state_id === b.id);
    expect(visit).toBeTruthy();
    await turn(page, agent, "Exit before late entry", "mecha_workflow_transition", { action: "exit" });
    await turn(page, agent, "Late transition entry", "workflow_journal_note", { source: "prompted", run_id: run, target_event_id: visit!.id, text: "Decision recorded later" });
    expect(events(run).find(event => event.text === "Decision recorded later")?.target_event_id).toBe(visit!.id);
    const after = events(run);
    await turn(page, agent, "No active journal note", "workflow_journal_note", { source: "human-requested", text: "no active run" });
    expect(events(run)).toEqual(after);
  });

  test("WJ-09/13: concurrent keyed activation replays and conflicts without duplicate runs", async ({ page }) => {
    const { agent, a, b } = await ready(page);
    const url = f.proxy.url + `/v1/managed-agents/${agent}/workflow-state`;
    const send = (key: string, body: object) => fetch(url, { method: "PUT", headers: { "Content-Type": "application/json", "Idempotency-Key": key, "X-Request-ID": crypto.randomUUID() }, body: JSON.stringify(body) });
    const body = { action: "activate", workflow_state_id: a.id };
    const ownsReactivation = (request: Record<string, unknown>) => owns("[Workflow Activated: A]")(request) || owns("is currently active in state A")(request);
    f.replies.push({ tool: "workflow_snooze", args: { duration_minutes: 5 }, ownsRequest: ownsReactivation },
      { answer: "Reactivation settled", ownsRequest: ownsReactivation });
    const [one, two] = await Promise.all([send("journal-concurrent-activation", body), send("journal-concurrent-activation", body)]);
    expect([one.status, two.status]).toEqual([200, 200]);
    expect(await one.json()).toEqual(await two.json());
    expect(runs(agent)).toHaveLength(2);
    await done(agent, "Reactivation settled");
    const snapshot = runs(agent);
    expect((await send("journal-concurrent-activation", { action: "activate", workflow_state_id: b.id })).status).toBe(409);
    expect(runs(agent)).toEqual(snapshot);
    f.replies.push({ answer: "Restart acknowledged", ownsRequest: owns("The mailbox router was restarted. Your session was restored from saved context.") });
    await f.restart();
    expect((await send("journal-concurrent-activation", body)).status).toBe(200);
    expect(runs(agent)).toEqual(snapshot);
  });

  test("WJ-05: deleting one agent removes its journal without exposing or deleting another", async ({ page }) => {
    const first = await ready(page);
    const firstRun = String(runs(first.agent)[0].id);
    const second = await ready(page);
    const secondRun = String(runs(second.agent)[0].id);
    expect(first.agent).not.toBe(second.agent);
    expect((await fetch(f.proxy.url + `${journal(second.agent)}/${firstRun}`)).status).toBe(404);
    expect((await fetch(f.proxy.url + `${journal(first.agent)}/${secondRun}`)).status).toBe(404);
    expect((await fetch(f.proxy.url + `/v1/managed-agents/${first.agent}`, { method: "DELETE", headers: { "X-Request-ID": crypto.randomUUID() } })).status).toBe(202);
    await expect.poll(() => f.rows("SELECT agent_id FROM managed_agents WHERE agent_id=?", first.agent).length, { timeout: 15000 }).toBe(0);
    expect(runs(first.agent)).toHaveLength(0);
    expect(events(firstRun)).toHaveLength(0);
    expect((await fetch(f.proxy.url + `${journal(first.agent)}/${firstRun}`)).status).toBe(404);
    expect(runs(second.agent).map(run => run.id)).toEqual([secondRun]);
    expect((await fetch(f.proxy.url + `${journal(second.agent)}/${secondRun}`)).status).toBe(200);
  });

  test("WJ-12: rolling eviction preserves the active visit and rejects evicted targets", async ({ page }) => {
    const { agent, a, b } = await ready(page);
    const run = String(runs(agent)[0].id);
    const start = events(run)[0];
    // Seed the long history in one transaction; the boundary-crossing appends
    // below go through the real browser, managed tool, router and store.
    const { DatabaseSync } = await import("node:sqlite");
    const db = new DatabaseSync(f.router.databasePath);
    db.exec("PRAGMA busy_timeout = 10000; BEGIN IMMEDIATE");
    try {
      const insert = db.prepare("INSERT INTO workflow_journal_events(id,run_id,sequence,kind,occurred_at) VALUES(?,?,?,'note',?)");
      for (let seq = 2; seq <= 10000; seq++) insert.run(`retention-seed-${seq}`, run, seq, start.occurred_at);
      db.prepare("UPDATE workflow_journal_runs SET next_sequence=10001 WHERE id=?").run(run);
      db.exec("COMMIT");
    } catch (error) { db.exec("ROLLBACK"); throw error; } finally { db.close(); }
    await turn(page, agent, "Cross retention limit", "workflow_journal_note", { source: "proactive", text: "Current visit survives" });
    let retained = events(run);
    expect(retained).toHaveLength(10000);
    expect(retained.some(event => event.id === start.id)).toBe(false);
    expect(retained.find(event => event.text === "Current visit survives")?.target_event_id).toBe(start.id);
    const detail = await f.api(`${journal(agent)}/${run}?limit=1`);
    expect(detail.partial).toBeTruthy();
    expect(detail.cutoff_sequence).toBe(1);
    expect(detail.current_visit?.state_id).toBe(a.id);
    await page.getByRole("button", { name: /workflow journal/i }).click();
    const dialog = page.getByRole("dialog", { name: /workflow journal/i });
    await dialog.getByRole("button", { name: /active/i }).first().click();
    await expect(dialog.getByText(/retained history begins at sequence 1/i)).toBeVisible();
    await dialog.getByRole("button", { name: /close/i }).first().click();
    await turn(page, agent, "Target evicted visit", "workflow_journal_note", { source: "prompted", run_id: run, target_event_id: start.id, text: "must not attach" });
    expect(events(run)).toEqual(retained);
    await turn(page, agent, "Transition after eviction", "mecha_workflow_transition", { action: "transition", target_state: "B" });
    retained = events(run);
    expect(retained).toHaveLength(10000);
    expect(retained.some(event => event.state_id === b.id && event.kind === "visit")).toBe(true);
    expect((await f.api(`${journal(agent)}/${run}?limit=1`)).cutoff_sequence).toBe(2);
  });

  test("WJ-11: moving current state splits visit without changing binding", async ({ page }) => {
    const { agent, workflow, a } = await ready(page);
    const run = String(runs(agent)[0].id);
    const other = await f.api("/v1/workflows", { name: `${workflow.name} destination` });
    const before = await f.api(`/v1/managed-agents/${agent}/workflow-state`);
    await f.advance(5000);
    await f.api(`/v1/workflow-states/${a.id}`, { workflow_id: other.id, name: "A", entry_text: "A guidance", allow_exit: false }, "PUT");
    const after = await f.api(`/v1/managed-agents/${agent}/workflow-state`);
    expect(after.active_workflow_id).toBe(before.active_workflow_id);
    expect(after.active_workflow_state_id).toBe(a.id);
    const visits = events(run).filter(event => event.state_id === a.id);
    expect(visits.map(event => event.workflow_id)).toEqual([workflow.id, other.id]);
    expect(runs(agent)).toHaveLength(1);
    await f.restart();
    expect(events(run).filter(event => event.state_id === a.id).map(event => event.workflow_id)).toEqual([workflow.id, other.id]);
  });

  test("WJ-03: invalid cursor and cross-agent history are rejected", async ({ page }) => {
    const { agent } = await ready(page);
    const run = String(runs(agent)[0].id);
    expect((await fetch(f.proxy.url + journal(agent) + "?cursor=not-a-cursor")).status).toBe(400);
    expect((await fetch(f.proxy.url + `/v1/managed-agents/missing/workflow-journal/${run}`)).status).toBe(404);
    expect((await fetch(f.proxy.url + `${journal(agent)}/${run}`)).status).toBe(200);
  });
});

// Scheduled launches create a journal through the normal activation transaction.
test("scheduled launch journal survives router restart", async ({ page }) => {
  test.setTimeout(90_000);
  const f = await workflowHarness();
  try {
    const owns = (request: Record<string, unknown>) => Array.isArray(request.input) &&
      JSON.stringify(request.input.filter((item: any) => item?.role === "user").at(-1)).includes("[Workflow Activated: Scheduled]");
    f.replies.push({ tool: "workflow_snooze", args: { duration_minutes: 5 }, ownsRequest: owns },
      { answer: "Scheduled journal ready", ownsRequest: owns });
    await f.open(page);
    const workflow = await f.api("/v1/workflows", { name: "Scheduled journal", snooze_duration_ms: 600000 });
    const { workflow_state: state } = await f.api("/v1/workflow-states", { workflow_id: workflow.id, name: "Scheduled", entry_text: "Scheduled guidance" });
    await f.api(`/v1/workflows/${workflow.id}`, { name: workflow.name, entry_state_id: state.id, snooze_duration_ms: 600000 }, "PUT");
    const created = await f.api("/v1/workflow-schedules", { workflow_id: workflow.id, name: "Journal schedule", variables: {}, target_cwd: `${f.router.root}/work`,
      schedule: { days_mode: "every_day", time_mode: "repeating_interval", interval: { value: 1, unit: "hours" } } });
    expect(created.schedule.next_run_at).toBeTruthy();
    await f.advance(60 * 60 * 1000 + 1000);
    const agents = () => f.rows("SELECT agent_id FROM managed_agents WHERE is_background=1");
    await expect.poll(() => agents().length, { timeout: 15000 }).toBe(1);
    const agent = String(agents()[0].agent_id);
    const runs = () => f.rows("SELECT id,status FROM workflow_journal_runs WHERE agent_id=?", agent);
    expect(runs()).toHaveLength(1);
    const run = String(runs()[0].id);
    await page.locator('button[role="tab"]:has-text("Background")').click();
    await expect(page.locator(`[data-agent-id="${agent}"]`)).toBeVisible();
    // Activation does not imply its worker has finished its turn.
    await expect.poll(() => f.rows("SELECT CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? AND kind='assistant'", agent)
      .some(row => String(row.body).includes("Scheduled journal ready")), { timeout: 15000 }).toBe(true);
    await expect.poll(() => f.rows("SELECT activity_state FROM managed_agents WHERE agent_id=?", agent)[0]?.activity_state, { timeout: 15000 }).toBe("idle");
    // A settled worker may receive the normal restart-awareness turn.
    f.replies.push({ answer: "Restart acknowledged", ownsRequest: request => Array.isArray(request.input) &&
      JSON.stringify(request.input.filter((item: any) => item?.role === "user").at(-1)).includes("The mailbox router was restarted. Your session was restored from saved context.") });
    await f.restart();
    expect(runs()).toHaveLength(1);
    expect(String(runs()[0].id)).toBe(run);
    expect(JSON.stringify(await f.api(`/v1/managed-agents/${agent}/workflow-journal/${run}`))).toContain(workflow.id);
    await f.advance(1000);
    expect(runs()).toHaveLength(1);
  } finally { await f.close(); }
});
