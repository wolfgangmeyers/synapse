import { test, expect, type Page } from "@playwright/test";
import { DatabaseSync } from "node:sqlite";
import { randomUUID } from "node:crypto";
import { createRunningWorkflow, userTurn, workflowHarness, type WorkflowHarness } from "./workflow-implicit-goals-harness";

test.describe("Workflow return edge acceptance", () => {
  test.setTimeout(90_000);
  let f: WorkflowHarness;
  test.beforeEach(async () => { f = await workflowHarness(); });
  test.afterEach(async () => { if (f) await f.close(); });

  const owns = (text: string) => (request: Record<string, unknown>) => Array.isArray(request.input) &&
    JSON.stringify(request.input.filter(item => item?.role === "user").at(-1)).includes(text);
  async function wait(page: Page, agent: string, answer: string) {
    await expect(page.getByText(answer, { exact: true }).first()).toBeVisible();
    await expect.poll(() => f.rows("SELECT CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? AND kind='assistant'", agent)
      .some(row => String(row.body).includes(answer))).toBe(true);
    await expect.poll(() => f.rows("SELECT activity_state FROM managed_agents WHERE agent_id=?", agent)[0]?.activity_state).toBe("idle");
  }
  async function move(page: Page, agent: string, target: string, label: string) {
    const text = `Request ${label}`;
    const owner = owns(text);
    f.replies.push({ tool: "mecha_workflow_transition", args: { action: "transition", target_state: target }, ownsRequest: owner },
      { tool: "workflow_snooze", args: { duration_minutes: 5 }, ownsRequest: owner }, { answer: label, ownsRequest: owner });
    await userTurn(page, text);
    await wait(page, agent, label);
    const input = f.requests.at(-1)?.input;
    expect(Array.isArray(input)).toBe(true);
    const call = input.filter((item: Record<string, unknown>) => item.name === "mecha_workflow_transition").at(-1);
    const result = input.find((item: Record<string, unknown>) => item.type === "function_call_output" && item.call_id === call?.call_id);
    expect(result).toBeDefined();
    return String(result.output);
  }
  function seed(sql: string, ...params: string[]) {
    const db = new DatabaseSync(f.router.databasePath);
    try { db.prepare(sql).run(...params); } finally { db.close(); }
  }
  function effects(agent: string) {
    return {
      binding: f.rows("SELECT active_workflow_state_id, workflow_predecessor_state_id, workflow_binding_generation FROM managed_agents WHERE agent_id=?", agent),
      reminders: f.rows("SELECT * FROM managed_agent_workflow_reminders WHERE agent_id=? ORDER BY reminder_id", agent),
      monitors: f.rows("SELECT * FROM managed_agent_monitors WHERE agent_id=? ORDER BY id", agent),
      history: f.rows("SELECT sequence FROM managed_complete_history WHERE agent_id=? AND message='Workflow state transitioned' ORDER BY sequence", agent),
    };
  }
  async function start(page: Page) {
    const owner = owns("[Workflow Activated: A]");
    f.replies.push({ tool: "workflow_snooze", args: { duration_minutes: 5 }, ownsRequest: owner }, { answer: "Ready", ownsRequest: owner });
    const run = await createRunningWorkflow(f, page, 600000, false);
    await wait(page, run.agent, "Ready");
    return run;
  }

  test("IR-005/006: disabled predecessor needs an explicit edge after return", async ({ page }) => {
    const { agent, a, b } = await start(page);
    await f.api(`/v1/workflow-states/${b.id}`, { name: "B", allow_exit: true, allow_implicit_return: true, transitions: [] }, "PUT");
    const initialA = (await f.api(`/v1/workflow-states/${a.id}`)).workflow_state;
    expect(initialA.allow_implicit_return).toBe(false);
    expect(initialA.transitions).toEqual([expect.objectContaining({ target_state_id: b.id })]);
    expect((await f.api(`/v1/workflow-states/${b.id}`)).workflow_state.allow_implicit_return).toBe(true);
    await move(page, agent, b.id, "Initial B");
    expect(effects(agent).binding[0]).toMatchObject({ active_workflow_state_id: b.id, workflow_predecessor_state_id: a.id });
    await f.api(`/v1/workflow-states/${a.id}`, { name: "A", allow_implicit_return: false, transitions: [] }, "PUT");
    expect((await f.api(`/v1/workflow-states/${a.id}`)).workflow_state.transitions).toHaveLength(0);
    await move(page, agent, a.id, "Implicit return to A");
    const before = effects(agent);
    const historyBefore = f.rows("SELECT * FROM managed_complete_history WHERE agent_id=? AND message='Workflow state transitioned' ORDER BY sequence", agent);
    expect(before.binding[0]).toMatchObject({ active_workflow_state_id: a.id, workflow_predecessor_state_id: b.id });
    expect(before.reminders).toEqual([]);
    expect(before.monitors).toEqual([]);
    const frames = f.wsFrames.length;
    expect(await move(page, agent, b.id, "Disabled A rejects B")).toMatch(/not allowed|not permitted|invalid|denied|no outgoing transitions/i);
    expect(effects(agent)).toEqual(before);
    expect(f.rows("SELECT * FROM managed_complete_history WHERE agent_id=? AND message='Workflow state transitioned' ORDER BY sequence", agent)).toEqual(historyBefore);
    expect(f.wsFrames.length).toBeLessThan(2048);
    expect(f.wsFrames.slice(frames).map(raw => JSON.parse(raw)).filter(frame => frame.type === "history_entry" &&
      frame.entry?.agent_id === agent && frame.entry?.message === "Workflow state transitioned")).toHaveLength(0);

    await f.api(`/v1/workflow-states/${a.id}`, { name: "A", allow_implicit_return: false,
      transitions: [{ target_state_id: b.id, description: "Explicit advance" }] }, "PUT");
    const definition = (await f.api(`/v1/workflow-states/${a.id}`)).workflow_state;
    expect(definition.allow_implicit_return).toBe(false);
    expect(definition.transitions).toEqual([expect.objectContaining({ target_state_id: b.id })]);
    const successFrames = f.wsFrames.length;
    await move(page, agent, b.id, "Explicit advance while off");
    expect(effects(agent).binding[0]).toMatchObject({ active_workflow_state_id: b.id, workflow_predecessor_state_id: a.id });
    const history = f.rows("SELECT sequence, presentation_json FROM managed_complete_history WHERE agent_id=? AND message='Workflow state transitioned' ORDER BY sequence", agent);
    expect(history.map(row => JSON.parse(String(row.presentation_json)))).toEqual([
      expect.objectContaining({ kind: "state", outcome: "success", from: "A", to: "B" }),
      expect.objectContaining({ kind: "state", outcome: "success", from: "B", to: "A" }),
      expect.objectContaining({ kind: "state", outcome: "success", from: "A", to: "B" }),
    ]);
    await expect.poll(() => f.wsFrames.slice(successFrames).map(raw => JSON.parse(raw)).find(frame =>
      frame.type === "history_entry" && frame.view === "complete" && frame.entry?.id === `complete:${history[2].sequence}`)).toMatchObject({
      entry: { agent_id: agent, message: "Workflow state transitioned", presentation: { kind: "state", outcome: "success", from: "A", to: "B" } },
    });
    expect(f.wsFrames.length).toBeLessThan(2048);
    expect(f.violations).toEqual([]);
    expect(f.replies).toEqual([]);
  });

  test("IR-008: deleted predecessor is rejected without effects and an explicit edge remains usable", async ({ page }) => {
    const { agent, workflow, a, b } = await start(page);
    const { workflow_state: c } = await f.api("/v1/workflow-states", { workflow_id: workflow.id, name: "C" });
    await f.api(`/v1/workflow-states/${b.id}`, { name: "B", allow_exit: true, allow_implicit_return: true,
      reminders: [{ id: "keep", type: "one_shot", body: "Keep reminder", delay_seconds: 600 }],
      transitions: [{ target_state_id: c.id, description: "Continue" }] }, "PUT");
    await move(page, agent, b.id, "At B");
    expect(effects(agent).binding[0].workflow_predecessor_state_id).toBe(a.id);
    // Deleting the predecessor through the supported API leaves no eligible return.
    await f.api(`/v1/workflow-states/${a.id}`, undefined, "DELETE");
    const before = effects(agent);
    expect(before.reminders).toHaveLength(1);
    const frames = f.wsFrames.length;
    const rejected = await move(page, agent, a.id, "Deleted return rejected");
    expect(rejected).toMatch(/not allowed|not permitted|invalid|denied|no outgoing transitions/i);
    expect(rejected).toContain("C");
    expect(effects(agent)).toEqual(before);
    expect(f.wsFrames.slice(frames).map(raw => JSON.parse(raw)).filter(frame => frame.type === "history_entry" &&
      frame.entry?.agent_id === agent && frame.entry?.message === "Workflow state transitioned")).toHaveLength(0);
    await move(page, agent, c.id, "Explicit edge after deletion");
    expect(effects(agent).binding[0].active_workflow_state_id).toBe(c.id);
    expect(f.violations).toEqual([]);
    expect(f.replies).toEqual([]);
  });

  test("IR-006/008: legacy self and cross-workflow edges work; foreign predecessor cannot return", async ({ page }) => {
    const { agent, a, b } = await start(page);
    const other = await f.api("/v1/workflows", { name: `Foreign ${randomUUID()}` });
    const { workflow_state: foreign } = await f.api("/v1/workflow-states", { workflow_id: other.id, name: "Foreign", allow_implicit_return: true });
    // Current creation/update validation rejects self edges. Seed only the legacy
    // definition, not the transition or its effects; all actions run through UI/tool.
    seed("UPDATE workflow_states SET transitions_json=? WHERE id=?", JSON.stringify([
      { target_state_id: a.id, description: "Legacy self" }, { target_state_id: foreign.id, description: "Cross workflow" },
      { target_state_id: b.id, description: "Continue" },
    ]), a.id);
    await move(page, agent, a.id, "Legacy self transition");
    expect(effects(agent).history).toHaveLength(1);
    expect(effects(agent).binding[0].active_workflow_state_id).toBe(a.id);
    await move(page, agent, foreign.id, "Cross workflow transition");
    expect(effects(agent).binding[0].active_workflow_state_id).toBe(foreign.id);
    // The binding remains in the original workflow, so A is eligible from
    // Foreign. After returning, Foreign is outside that active workflow.
    await move(page, agent, a.id, "Return to original workflow");
    await f.api(`/v1/workflow-states/${a.id}`, { name: "A", allow_implicit_return: true,
      transitions: [{ target_state_id: b.id, description: "Continue" }] }, "PUT");
    const before = effects(agent);
    const rejected = await move(page, agent, foreign.id, "Foreign predecessor rejected");
    expect(rejected).toMatch(/not allowed|not permitted|invalid|denied|no outgoing transitions/i);
    expect(effects(agent)).toEqual(before);
    expect(f.violations).toEqual([]);
    expect(f.replies).toEqual([]);
  });
});
