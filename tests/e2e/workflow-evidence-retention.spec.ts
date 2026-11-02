import { readFile, writeFile } from "node:fs/promises";
import { test, expect } from "@playwright/test";
import { workflowHarness, createRunningWorkflow, settled, type WorkflowHarness } from "./workflow-implicit-goals-harness";

const DAY = 86400000;
const RETENTION = 30 * DAY;
const tables = [
  "embedded_goal_applied_receipts", "embedded_goal_evaluations", "embedded_goal_continuations",
  "embedded_goal_snooze_calls", "embedded_goal_snooze_preferences",
] as const;

// Read each table independently: an empty join must not hide orphaned evidence.
function evidence(f: WorkflowHarness, agent: string) {
  return Object.fromEntries(tables.map(table => [table, f.rows(`SELECT * FROM ${table} WHERE agent_id=? ORDER BY rowid`, agent)]));
}

// Exercise the existing real sweep through the reverse proxy, not Store prune.
async function advance(f: WorkflowHarness, delta: number) {
  await f.api("/v1/test/clock/advance", { delta_ms: delta });
  f.clock += delta;
}

test.describe("SCN-WFIG-014: automatic workflow evidence retention", () => {
  let f: WorkflowHarness;
  test.beforeEach(async () => { f = await workflowHarness(); });
  test.afterEach(async ({ page }, info) => {
    try {
      if (f && info.status !== info.expectedStatus) {
        const diagnostic = {
          requests: f.requests, violations: f.violations, replies: f.replies,
          sessions: f.rows("SELECT * FROM embedded_sessions WHERE ? IS NOT NULL", "diagnostic"),
          entries: f.rows("SELECT * FROM embedded_entries WHERE ? IS NOT NULL ORDER BY seq", "diagnostic"),
          evaluations: f.rows("SELECT * FROM embedded_goal_evaluations WHERE ? IS NOT NULL", "diagnostic"),
          continuations: f.rows("SELECT * FROM embedded_goal_continuations WHERE ? IS NOT NULL", "diagnostic"),
          completeHistory: f.rows("SELECT * FROM managed_complete_history WHERE ? IS NOT NULL ORDER BY sequence", "diagnostic"),
          historyAPI: await Promise.all(f.rows("SELECT agent_id FROM embedded_sessions WHERE ? IS NOT NULL", "diagnostic").map(row =>
            f.api(`/v1/history?agent_id=${row.agent_id}&view=complete&limit=5000`))),
          browserText: (await page.locator("body").innerText().catch(String)).slice(-24000),
          wsFrames: f.wsFrames.slice(-50),
          routerLog: await readFile(f.router.logPath, "utf8").catch(String),
        };
        await writeFile(`/tmp/workflow-retention-browser-${info.title.startsWith("same-DB") ? "reopen" : "prune"}-diagnostic.json`, JSON.stringify(diagnostic, null, 2));
      }
    } finally {
      const root = f?.router.root;
      const child = f?.router.process;
      await f?.close();
      await writeFile(`/tmp/workflow-retention-browser-${info.title.startsWith("same-DB") ? "reopen" : "prune"}-cleanup.json`, JSON.stringify({ root, pid: child?.pid, exitCode: child?.exitCode, signalCode: child?.signalCode }));
    }
  });

  test("same-DB reopen preserves future D and browser reconnect without early or duplicate start", async ({ page }) => {
    f.replies.push({ tool: "workflow_snooze", args: { duration_ms: 300000 } }, { answer: "Reopen wait saved" });
    const current = await createRunningWorkflow(f, page, 600000, false);
    await expect(page.getByText("Reopen wait saved", { exact: true }).first()).toBeVisible();
    await expect.poll(() => settled(f, current.agent).length).toBe(1);
    const before = settled(f, current.agent)[0];
    const live = evidence(f, current.agent);
    const binding = await f.api(`/v1/managed-agents/${current.agent}/workflow-state`);
    const policy = f.rows("SELECT desired_revision,goal_text,goal_enabled,snoozed_until FROM managed_agent_policies WHERE agent_id=?", current.agent);
    const originalChild = f.router.process;
    const root = f.router.root;
    // The normal cold-restore notice is separate from deadline admission.
    // Its deterministic reply and the later held continuation share one queue.
    f.replies.push({ answer: "Restart context restored" }, { hold: true });
    const frameStart = f.wsFrames.length;
    await f.restart();
    expect(originalChild.exitCode !== null || originalChild.signalCode !== null).toBe(true);
    expect(f.router.process.pid).not.toBe(originalChild.pid);
    expect(f.router.root).toBe(root);
    await expect.poll(async () => (await fetch(f.proxy.url + "/v1/health")).status).toBe(200);
    await expect.poll(() => settled(f, current.agent).length).toBe(1);
    expect(evidence(f, current.agent)).toEqual(live);
    expect(await f.api(`/v1/managed-agents/${current.agent}/workflow-state`)).toEqual(binding);
    expect(f.rows("SELECT desired_revision,goal_text,goal_enabled,snoozed_until FROM managed_agent_policies WHERE agent_id=?", current.agent)).toEqual(policy);
    // Recovery notice is intentionally not evaluated/reserved. Wait for its
    // durable final and cleared execution before testing a genuinely fresh load.
    await expect.poll(() => f.rows("SELECT execution_json FROM embedded_sessions WHERE agent_id=?", current.agent)[0].execution_json).toBeNull();
    await expect.soft.poll(async () => JSON.stringify(await f.api(`/v1/history?agent_id=${current.agent}&view=complete&limit=5000`))).toContain("Restart context restored");
    await page.reload();
    await expect.poll(() => f.wsFrames.slice(frameStart).map(text => JSON.parse(text)).some(frame => frame.type === "registered")).toBe(true);
    await page.locator('button[role="tab"]:has-text("Background")').click();
    await expect(page.locator(`[data-agent-id="${current.agent}"]`)).toBeVisible();
    await page.locator(`[data-agent-id="${current.agent}"]`).click();
    await expect(page.getByText("Reopen wait saved", { exact: true }).first()).toBeVisible();
    await expect.soft(page.getByText("Restart context restored", { exact: true }).first()).toBeVisible();
    expect(settled(f, current.agent)).toEqual([before]);
    const lastUserText = (request: Record<string, unknown>) => {
      const input = request.input;
      if (!Array.isArray(input)) return "";
      const last = input.filter(item => item.role === "user").at(-1);
      return JSON.stringify(last?.content);
    };
    const noticeRequests = () => f.requests.filter(request => lastUserText(request).includes("The mailbox router was restarted."));
    const continuationRequests = () => f.requests.filter(request => lastUserText(request).includes(`The workflow ${current.workflow.name} is currently active in state A.`));
    expect(noticeRequests()).toHaveLength(1);
    expect(continuationRequests()).toHaveLength(0);
    expect(settled(f, current.agent)[0].due_at).toBe(before.due_at);
    expect(settled(f, current.agent)[0].started_at).toBeNull();
    // The clock API rounds to seconds. A supported workflow creation returns
    // the precise store clock; no fixture SQL or deadline rewrites are needed.
    const anchor = await f.api("/v1/workflows", { name: `Reopen clock ${current.agent}` });
    f.clock = Date.parse(anchor.created_at);
    await advance(f, Date.parse(String(before.due_at)) - f.clock - 1);
    expect(continuationRequests()).toHaveLength(0);
    expect(evidence(f, current.agent)).toEqual(live);
    await advance(f, 1);
    await expect.poll(() => continuationRequests().length).toBe(1);
    await expect.poll(() => settled(f, current.agent)[0].continuation_state).toBe("started");
    const started = settled(f, current.agent)[0];
    expect(started.due_at).toBe(before.due_at);
    expect(started.started_at).toBe(before.due_at);
    for (let i = 0; i < 3; i++) await advance(f, 1);
    expect(continuationRequests()).toHaveLength(1);
    expect(noticeRequests()).toHaveLength(1);
    expect(settled(f, current.agent)[0].started_at).toBe(started.started_at);
    expect(f.violations).toEqual([]);
    expect(f.replies).toEqual([]);
    // afterEach destroys the held response and waits for the owned child exit.
  });

  test("automatic inclusive 30-day sweep prunes retired evidence and preserves real current future-D work", async ({ page }) => {
    // Historical AND current evidence are produced by real tools/runtime here.
    // No SQL seeding, timestamp UPDATE, direct DELETE, or disabled SQL guards.
    f.replies.push({ tool: "workflow_snooze", args: { duration_ms: DAY } }, { answer: "Historical wait saved" });
    const old = await createRunningWorkflow(f, page, 600000, false);
    const admittedAt = f.clock;
    await expect(page.getByText("Historical wait saved", { exact: true }).first()).toBeVisible();
    await expect.poll(() => settled(f, old.agent).length).toBe(1);
    await f.api(`/v1/managed-agents/${old.agent}/workflow-state`, { action: "deactivate" }, "PUT");
    await expect.poll(() => settled(f, old.agent)[0].continuation_state).toBe("invalidated");
    const history = evidence(f, old.agent);
    for (const table of tables) expect(history[table], table).toHaveLength(1);
    const transcript = f.rows("SELECT * FROM embedded_entries WHERE agent_id=? ORDER BY seq", old.agent);
    const historicalSessions = f.rows("SELECT session_id,epoch,generation FROM embedded_sessions WHERE agent_id=?", old.agent);

    await advance(f, RETENTION - 1);
    expect(evidence(f, old.agent)).toEqual(history);
    expect(f.requests).toHaveLength(2); // Retired D did not wake old work.

    f.replies.push({ tool: "workflow_snooze", args: { duration_ms: 300000 } }, { answer: "Current wait saved" });
    const boundaryNow = f.clock;
    const current = await createRunningWorkflow(f, page, 600000, false);
    // createRunningWorkflow reads an API timestamp; retain exact injected ms.
    f.clock = boundaryNow;
    await expect(page.getByText("Current wait saved", { exact: true }).first()).toBeVisible();
    await expect.poll(() => settled(f, current.agent).length).toBe(1);
    const live = evidence(f, current.agent);
    const turn = settled(f, current.agent)[0];
    expect(turn.state).toBe("NOT_DONE");
    expect(turn.continuation_state).toBe("reserved");
    expect(Date.parse(String(turn.due_at))).toBeGreaterThan(f.clock);
    const binding = await f.api(`/v1/managed-agents/${current.agent}/workflow-state`);
    const policy = f.rows("SELECT desired_revision,goal_text,goal_enabled,snoozed_until FROM managed_agent_policies WHERE agent_id=?", current.agent);

    await advance(f, 1); // Inclusive boundary, automatic runSweep.
    expect(f.clock).toBe(admittedAt + RETENTION);
    await expect.poll(() => Object.values(evidence(f, old.agent)).map(rows => rows.length)).toEqual([0, 0, 0, 0, 0]);
    expect(f.rows("SELECT * FROM embedded_entries WHERE agent_id=? ORDER BY seq", old.agent)).toEqual(transcript);
    expect(f.rows("SELECT session_id,epoch,generation FROM embedded_sessions WHERE agent_id=?", old.agent)).toEqual(historicalSessions);
    expect(evidence(f, current.agent)).toEqual(live);
    expect(await f.api(`/v1/managed-agents/${current.agent}/workflow-state`)).toEqual(binding);
    expect(f.rows("SELECT desired_revision,goal_text,goal_enabled,snoozed_until FROM managed_agent_policies WHERE agent_id=?", current.agent)).toEqual(policy);
    expect(f.rows("SELECT evidence_expired_through_ms FROM embedded_sessions WHERE agent_id=?", old.agent)[0].evidence_expired_through_ms).toBe(admittedAt);

    await advance(f, Date.parse(String(turn.due_at)) - f.clock - 1);
    expect(f.requests).toHaveLength(4);
    expect(settled(f, current.agent)[0].started_at).toBeNull();
    f.replies.push({ hold: true });
    await advance(f, 1);
    await expect.poll(() => f.requests.length).toBe(5);
    await expect.poll(() => settled(f, current.agent)[0].continuation_state).toBe("started");
    const started = settled(f, current.agent)[0];
    expect(started.due_at).toBe(turn.due_at);
    expect(Date.parse(String(started.started_at))).toBe(Date.parse(String(turn.due_at)));
    expect(JSON.stringify(f.requests.at(-1)!.input)).toContain(`The workflow ${current.workflow.name} is currently active in state A.`);
    for (let i = 0; i < 3; i++) await advance(f, 1);
    expect(f.requests).toHaveLength(5);
    expect(settled(f, current.agent)[0].started_at).toBe(started.started_at);
    expect(Object.values(evidence(f, old.agent)).map(rows => rows.length)).toEqual([0, 0, 0, 0, 0]);
    expect(f.rows("SELECT * FROM pragma_foreign_key_check WHERE ? IS NOT NULL", old.agent)).toEqual([]);
    expect(f.wsFrames.some(frame => frame.includes(current.agent))).toBe(true);
    expect(f.violations).toEqual([]);
    expect(f.replies).toEqual([]);
  });
});
