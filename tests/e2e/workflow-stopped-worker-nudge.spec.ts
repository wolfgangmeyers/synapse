import { randomUUID } from "node:crypto";
import { writeFile } from "node:fs/promises";
import { test, expect, type Page } from "@playwright/test";
import { workflowHarness, model, type WorkflowHarness } from "./workflow-implicit-goals-harness";
import { claudeWorkersHarness } from "./claude-workers-harness";

// SCN-WFIG-017: only the external provider/CLI is mocked. SQLite observations
// are read-only; workflow activation, native tool and continuations are real.
const guidance = (name: string, state: string) =>
  `The workflow ${name} is currently active in state ${state}. You must either make progress towards completing the workflow, call workflow_snooze, exit the workflow with mecha_workflow_transition (action exit) when an exit is allowed, or call mecha_blocked with the reason.`;

async function setup(f: WorkflowHarness, page: Page, agent: string) {
  const workflow = await f.api("/v1/workflows", { name: `Stopped ${randomUUID()}` });
  f.clock = Date.parse(workflow.created_at);
  const { workflow_state: b } = await f.api("/v1/workflow-states", { workflow_id: workflow.id, name: "B", entry_text: "B guidance", allow_exit: true });
  const { workflow_state: a } = await f.api("/v1/workflow-states", { workflow_id: workflow.id, name: "A", entry_text: "A guidance", allow_exit: false, transitions: [{ target_state_id: b.id, description: "Advance" }] });
  await f.api(`/v1/workflows/${workflow.id}`, { name: workflow.name, entry_state_id: a.id }, "PUT");
  await f.api(`/v1/managed-agents/${agent}/workflow-state`, { action: "activate", workflow_state_id: a.id }, "PUT");
  const goal = (await f.api(`/v1/managed-agents/${agent}/goal`)).goal_state;
  expect(goal.enabled).toBe(true);
  await expect(page.locator(`[data-agent-id="${agent}"]`)).toBeVisible();
  return { workflow, goal };
}

function goalPolicy(f: WorkflowHarness, agent: string) {
  return f.rows("SELECT desired_revision FROM managed_agent_policies WHERE agent_id=?", agent);
}

async function assertDistinctSettlements(f: WorkflowHarness, agent: string) {
  const rows = () => f.rows(`SELECT e.user_seq,e.user_digest,e.goal_revision,e.final_seq,e.final_digest,
    e.state,c.state AS continuation_state FROM embedded_goal_evaluations e
    JOIN embedded_goal_continuations c ON c.agent_id=e.agent_id AND c.session_id=e.session_id
      AND c.epoch=e.epoch AND c.generation=e.generation AND c.user_seq=e.user_seq
      AND c.user_digest=e.user_digest AND c.goal_revision=e.goal_revision
      AND c.final_seq=e.final_seq AND c.final_digest=e.final_digest
    JOIN embedded_entries final ON final.agent_id=e.agent_id AND final.session_id=e.session_id
      AND final.epoch=e.epoch AND final.generation=e.generation AND final.seq=e.final_seq AND final.digest=e.final_digest
    JOIN embedded_append_keys receipt ON receipt.agent_id=final.agent_id AND receipt.session_id=final.session_id
      AND receipt.epoch=final.epoch AND receipt.generation=final.generation AND receipt.entry_seq=final.seq
    WHERE e.agent_id=? ORDER BY e.final_seq`, agent);
  await expect.poll(() => rows().filter(r => r.state === "NOT_DONE" && r.continuation_state === "started").length).toBeGreaterThanOrEqual(2);
  const started = rows().filter(r => r.state === "NOT_DONE" && r.continuation_state === "started");
  expect(new Set(started.map(r => r.final_seq)).size).toBe(started.length);
  expect(new Set(started.map(r => r.final_digest)).size).toBe(started.length);
}

const tickTrace: object[] = [];
async function clockUntil(f: WorkflowHarness, predicate: () => Promise<boolean> | boolean) {
  // Allow the actual runtime and its checked settlement to run between clock
  // ticks. Advancing all deadlines before CLI startup races scheduler admission.
  for (let i = 0; i < 30; i++) {
    if (await predicate()) return;
    // Do not advance the router clock while a worker is active: its real CLI
    // startup and MCP calls must not time out as a side effect of polling.
    const busy = f.rows("SELECT count(*) AS n FROM managed_agents WHERE activity_state != 'idle' AND desired_state = 'running'");
    const before = f.clock;
    if (Number(busy[0]?.n) === 0) await f.advance(10_000);
    tickTrace.push({ wall: new Date().toISOString(), before, after: f.clock, busy: busy[0] });
    await new Promise(resolve => setTimeout(resolve, 500));
  }
  await expect.poll(predicate, { timeout: 5000 }).toBe(true);
}

async function capture(f: WorkflowHarness, name: string, extra: object = {}) {
  const tables = ["managed_agents", "managed_agent_policies", "embedded_goal_evaluations", "embedded_goal_continuations", "embedded_entries", "embedded_append_keys"];
  const rows = Object.fromEntries(tables.map(table => [table, f.rows(`SELECT * FROM ${table}`)]));
  const evidence = { name, clock: f.clock, tickTrace, rows, requests: f.requests, violations: f.violations, ...extra };
  const path = `/tmp/nudge-evidence-${name}-${process.pid}.json`;
  await writeFile(path, JSON.stringify(evidence, null, 2));
  console.log(`NUDGE_EVIDENCE ${path}`);
}

test.describe("SCN-WFIG-017 stopped workflow nudge", () => {
  test.setTimeout(120_000);

  test("Claude native transition receives current-state nudge without another user message", async ({ page }) => {
    const f = await claudeWorkersHarness({ turns_by_input: [
      { contains: "Workflow Activated: A", steps: [{ text: "Activation settled" }] },
      { contains: "currently active in state A", steps: [
        { mcp_call: { id: "advance-workflow", name: "mecha_workflow_transition", input: { action: "transition", target_state: "B" }, require_catalog: true } },
        { text: "Native transition settled" },
      ] },
      { contains: "currently active in state B", steps: [{ barrier: "state-b-nudge" }, { text: "State B nudge received" }] },
    ] });
    try {
      await f.open(page);
      const agent = await f.launch(page);
      const { workflow, goal } = await setup(f, page, agent);
      const policy = goalPolicy(f, agent);
      await clockUntil(f, async () => (await f.records()).some(r => r.kind === "mcp_call_response" && r.tool_id === "advance-workflow"));
      const transition = (await f.records()).find(r => r.kind === "mcp_call_response" && r.tool_id === "advance-workflow")!;
      expect(transition.error).toBeNull();
      expect(JSON.stringify(transition.result)).not.toContain('"isError":true');
      await expect(page.getByText("Native transition settled", { exact: true }).first()).toBeVisible();
      await clockUntil(f, async () => (await f.records()).some(r => r.kind === "barrier" && r.name === "state-b-nudge"));
      const inputs = await f.inputs();
      const rendered = inputs.map(input => JSON.stringify(input.message.content));
      expect(rendered.filter(text => text.includes(guidance(workflow.name, "A")))).toHaveLength(1);
      expect(rendered.filter(text => text.includes(guidance(workflow.name, "B")))).toHaveLength(1);
      expect((await f.api(`/v1/managed-agents/${agent}/goal`)).goal_state).toMatchObject({ enabled: true, goal: goal.goal });
      expect(goalPolicy(f, agent)).toEqual(policy);
      expect(f.wsFrames.some(frame => frame.includes("Native transition settled"))).toBe(true);
      // Repeated wake checks while this exact nudge is held cannot start it twice.
      for (let i = 0; i < 3; i++) { await f.advance(10_000); await f.api(`/v1/managed-agents/${agent}/goal`); }
      expect(await f.inputs()).toEqual(inputs);
      const firstBarrier = (await f.records()).find(r => r.kind === "barrier" && r.name === "state-b-nudge")!;
      await f.releaseBarrier(firstBarrier);
      await expect(page.getByText("State B nudge received", { exact: true }).first()).toBeVisible();
      await clockUntil(f, async () => (await f.records()).filter(r => r.kind === "barrier" && r.name === "state-b-nudge").length === 2);
      const later = await f.inputs();
      expect(later).toHaveLength(inputs.length + 1);
      expect(JSON.stringify(later.at(-1).message.content)).toContain(guidance(workflow.name, "B"));
      for (let i = 0; i < 3; i++) { await f.advance(10_000); await f.api(`/v1/managed-agents/${agent}/goal`); }
      expect(await f.inputs()).toEqual(later);
      await assertDistinctSettlements(f, agent);
      expect(f.requests).toEqual([]); // workflows do not call the auxiliary evaluator
      expect(f.violations).toEqual([]);
    } catch (error) {
      await capture(f, "claude", { records: await f.records(), inputs: await f.inputs() });
      await test.info().attach("claude-nudge-evidence", { contentType: "application/json", body: Buffer.from(JSON.stringify({ records: await f.records(), inputs: await f.inputs(), violations: f.violations }, null, 2)) });
      throw error;
    } finally { if (test.info().status === "passed") await capture(f, "claude-pass", { records: await f.records(), inputs: await f.inputs() }); await f.close(); }
  });

  test("Pi shared path nudges the distinct transition stop with current-state guidance", async ({ page }) => {
    const f = await workflowHarness();
    try {
      await f.open(page);
      const agent = `stopped-${randomUUID()}`;
      await f.api("/v1/managed-agents", { name: agent, cwd: `${f.router.root}/work`, runtime: "embedded", model });
      await expect(page.locator(`[data-agent-id="${agent}"]`)).toBeVisible();
      await page.locator(`[data-agent-id="${agent}"]`).click();
      f.replies.push({ answer: "Activation settled" }, { tool: "mecha_workflow_transition", args: { action: "transition", target_state: "B" } },
        { answer: "Native transition settled" }, { answer: "State B nudge received" }, { hold: true });
      const { workflow, goal } = await setup(f, page, agent);
      const policy = goalPolicy(f, agent);
      await clockUntil(f, () => f.requests.length >= 3);
      await expect(page.getByText("Native transition settled", { exact: true }).first()).toBeVisible();
      await clockUntil(f, () => f.requests.length >= 4);
      const latestUser = (request: Record<string, unknown>) => {
        const input = request.input as { role?: string; content?: unknown }[];
        return [...input].reverse().find(item => item.role === "user")?.content;
      };
      expect(latestUser(f.requests[1])).toEqual([{ type: "input_text", text: guidance(workflow.name, "A") }]);
      expect(latestUser(f.requests[3])).toEqual([{ type: "input_text", text: guidance(workflow.name, "B") }]);
      expect((await f.api(`/v1/managed-agents/${agent}/goal`)).goal_state).toMatchObject({ enabled: true, goal: goal.goal });
      expect(goalPolicy(f, agent)).toEqual(policy);
      expect(f.wsFrames.some(frame => frame.includes("Native transition settled"))).toBe(true);
      await expect(page.getByText("State B nudge received", { exact: true }).first()).toBeVisible();
      await clockUntil(f, () => f.requests.length >= 5);
      expect(latestUser(f.requests[4])).toEqual([{ type: "input_text", text: guidance(workflow.name, "B") }]);
      await assertDistinctSettlements(f, agent);
      for (let i = 0; i < 3; i++) { await f.advance(10_000); await f.api(`/v1/managed-agents/${agent}/goal`); }
      expect(f.requests).toHaveLength(5);
      expect(f.violations).toEqual([]);
    } catch (error) { await capture(f, "pi"); throw error; } finally { if (test.info().status === "passed") await capture(f, "pi-pass"); await f.close(); }
  });
});
