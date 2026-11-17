import { test, expect } from "@playwright/test";
import { randomUUID } from "node:crypto";
import { createRunningWorkflow, userTurn, workflowHarness } from "./workflow-implicit-goals-harness";

test("IR-003: REST create and list expose both return settings", async ({ page }) => {
  test.setTimeout(90_000);
  const f = await workflowHarness();
  try {
    await f.open(page);
    const wf = await f.api("/v1/workflows", { name: `REST ${randomUUID()}` });
    const off = (await f.api("/v1/workflow-states", { workflow_id: wf.id, name: "Off", allow_implicit_return: false })).workflow_state;
    const on = (await f.api("/v1/workflow-states", { workflow_id: wf.id, name: "On", allow_implicit_return: true })).workflow_state;
    expect(off.allow_implicit_return).toBe(false);
    expect(on.allow_implicit_return).toBe(true);
    const list = (await f.api("/v1/workflow-states")).workflow_states;
    expect(list.find((s: { id: string }) => s.id === off.id).allow_implicit_return).toBe(false);
    expect(list.find((s: { id: string }) => s.id === on.id).allow_implicit_return).toBe(true);
  } finally { await f.close(); }
});

test("IR-005/007/010: explicit reverse is listed once; exit and fresh activation clear return authority", async ({ page }) => {
  test.setTimeout(90_000);
  const f = await workflowHarness();
  const owns = (text: string) => (request: Record<string, unknown>) => Array.isArray(request.input) &&
    JSON.stringify(request.input.filter(item => item?.role === "user").at(-1)).includes(text);
  async function wait(agent: string, answer: string) {
    await expect(page.getByText(answer, { exact: true }).first()).toBeVisible();
    await expect.poll(() => f.rows("SELECT CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? AND kind='assistant'", agent)
      .some(row => String(row.body).includes(answer))).toBe(true);
    await expect.poll(() => f.rows("SELECT activity_state FROM managed_agents WHERE agent_id=?", agent)[0]?.activity_state).toBe("idle");
  }
  async function turn(agent: string, text: string, args: object, answer: string, snooze = true) {
    const owner = owns(text);
    f.replies.push({ tool: "mecha_workflow_transition", args, ownsRequest: owner });
    if (snooze) f.replies.push({ tool: "workflow_snooze", args: { justification: "Waiting for CI process", duration_minutes: 5 }, ownsRequest: owner });
    f.replies.push({ answer, ownsRequest: owner });
    await userTurn(page, text);
    await wait(agent, answer);
    const request = f.requests.findLast(r => Array.isArray(r.input) && r.input.some((i: Record<string, unknown>) => i.type === "function_call_output" && i.name !== "workflow_snooze") && owns(text)(r));
    const input = request?.input;
    expect(Array.isArray(input)).toBe(true);
    const call = input.filter((i: Record<string, unknown>) => i.name === "mecha_workflow_transition").at(-1);
    return String(input.find((i: Record<string, unknown>) => i.type === "function_call_output" && i.call_id === call?.call_id)?.output);
  }
  try {
    const activation = owns("[Workflow Activated: A]");
    f.replies.push({ tool: "workflow_snooze", args: { justification: "Waiting for CI process", duration_minutes: 5 }, ownsRequest: activation }, { answer: "Ready", ownsRequest: activation });
    const { agent, workflow, a, b } = await createRunningWorkflow(f, page, 600000, false);
    await wait(agent, "Ready");
    await f.api(`/v1/workflow-states/${b.id}`, { name: "B", allow_exit: true, allow_implicit_return: true,
      transitions: [{ target_state_id: a.id, description: "Explicit reverse" }] }, "PUT");
    await turn(agent, "Advance", { action: "transition", target_state: b.id }, "At B");
    const owner = owns("Inspect guidance");
    f.replies.push({ answer: "Guidance inspected", ownsRequest: owner });
    await userTurn(page, "Inspect guidance");
    await wait(agent, "Guidance inspected");
    const guidance = String(f.requests.findLast(owner)?.instructions);
    const transitions = guidance.split("### Allowed Transitions")[1]?.split("### Workflow Exit")[0];
    expect(transitions).toBeDefined();
    expect(transitions.match(/^- A(?:\s|:|$)/gm)).toHaveLength(1);
    expect(transitions).toContain("Explicit reverse");
    expect(await turn(agent, "Exit workflow", { action: "exit" }, "Exited", false)).toMatch(/successfully exited/i);
    let binding = f.rows("SELECT active_workflow_state_id, workflow_predecessor_state_id FROM managed_agents WHERE agent_id=?", agent)[0];
    expect(binding.active_workflow_state_id || null).toBeNull();
    expect(binding.workflow_predecessor_state_id || null).toBeNull();
    // A fresh binding at B has no predecessor. Remove the explicit reverse edge
    // so a subsequent attempt tests implicit authority, not graph authority.
    await f.api(`/v1/workflow-states/${b.id}`, { name: "B", allow_exit: true, allow_implicit_return: true, transitions: [] }, "PUT");
    const fresh = owns("[Workflow Activated: B]");
    f.replies.push({ tool: "workflow_snooze", args: { justification: "Waiting for CI process", duration_minutes: 5 }, ownsRequest: fresh }, { answer: "Fresh B", ownsRequest: fresh });
    await f.api(`/v1/managed-agents/${agent}/workflow-state`, { action: "activate", workflow_id: workflow.id, workflow_state_id: b.id }, "PUT");
    await wait(agent, "Fresh B");
    binding = f.rows("SELECT active_workflow_state_id, workflow_predecessor_state_id FROM managed_agents WHERE agent_id=?", agent)[0];
    expect(binding.active_workflow_state_id).toBe(b.id);
    expect(binding.workflow_predecessor_state_id || null).toBeNull();
    const before = f.rows("SELECT sequence FROM managed_complete_history WHERE agent_id=? AND message='Workflow state transitioned' ORDER BY sequence", agent);
    expect(await turn(agent, "Try stale return", { action: "transition", target_state: a.id }, "Rejected stale return")).toMatch(/not allowed|not permitted|invalid|denied|no outgoing transitions/i);
    expect(f.rows("SELECT sequence FROM managed_complete_history WHERE agent_id=? AND message='Workflow state transitioned' ORDER BY sequence", agent)).toEqual(before);
    expect(f.rows("SELECT active_workflow_state_id, workflow_predecessor_state_id FROM managed_agents WHERE agent_id=?", agent)[0]).toEqual(binding);
    expect(f.replies).toEqual([]);
    expect(f.violations).toEqual([]);
  } finally { await f.close(); }
});
