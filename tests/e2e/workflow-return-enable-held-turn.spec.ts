import { test, expect } from "@playwright/test";
import { createRunningWorkflow, userTurn, workflowHarness } from "./workflow-implicit-goals-harness";

test("IR-012/014: enabling return during a held turn permits the next transition using current permission", async ({ page }) => {
  test.setTimeout(90_000);
  const f = await workflowHarness();
  let release!: () => void;
  const barrier = new Promise<void>(resolve => { release = resolve; });
  const owns = (text: string) => (r: Record<string, unknown>) => Array.isArray(r.input) &&
    JSON.stringify(r.input.filter(item => item?.role === "user").at(-1)).includes(text);
  try {
    const activation = owns("[Workflow Activated: A]");
    f.replies.push({ tool: "workflow_snooze", args: { duration_minutes: 5 }, ownsRequest: activation }, { answer: "Ready", ownsRequest: activation });
    const { agent, a, b } = await createRunningWorkflow(f, page, 600000, false);
    await expect(page.getByText("Ready", { exact: true }).first()).toBeVisible();
    const advance = owns("Advance to B");
    f.replies.push({ tool: "mecha_workflow_transition", args: { action: "transition", target_state: b.id }, ownsRequest: advance },
      { tool: "workflow_snooze", args: { duration_minutes: 5 }, ownsRequest: advance }, { answer: "Reached B", ownsRequest: advance });
    await userTurn(page, "Advance to B");
    await expect(page.getByText("Reached B", { exact: true }).first()).toBeVisible();
    await expect.poll(async () => (await f.api(`/v1/managed-agents/${agent}/workflow-state`)).active_workflow_state_id).toBe(b.id);

    const before = f.requests.length;
    f.replies.push({ answer: "Held turn finished", barrier, ownsRequest: owns("Hold this turn") });
    await userTurn(page, "Hold this turn");
    await expect.poll(() => f.requests.length).toBe(before + 1);
    const heldCount = f.requests.length;
    // Compare durable mailbox and prompt queues while the provider is held.
    const durable = () => ({
      messages: f.rows("SELECT * FROM messages WHERE recipient=? ORDER BY enqueue_sequence", agent),
      prompts: f.rows("SELECT * FROM embedded_prompt_requests WHERE agent_id=? ORDER BY request_id", agent),
      entries: f.rows("SELECT seq, kind, CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? ORDER BY seq", agent),
    });
    const held = durable();
    // The barrier stays closed until both saves return; the test timeout bounds a deadlock.
    for (const [marker, enabled] of [["Intermediate marker", false], ["Final marker", true]] as const) {
      const saved = await f.api(`/v1/workflow-states/${b.id}`, { name: "B", entry_text: marker, allow_exit: true, allow_implicit_return: enabled }, "PUT");
      expect(saved.workflow_state.allow_implicit_return).toBe(enabled);
      expect(f.requests.length).toBe(heldCount);
      expect(durable()).toEqual(held);
    }
    release();
    await expect(page.getByText("Held turn finished", { exact: true }).first()).toBeVisible();
    await expect.poll(() => f.rows("SELECT activity_state FROM managed_agents WHERE agent_id=?", agent)[0]?.activity_state).toBe("idle");
    expect(f.requests.length).toBe(heldCount);

    const effects = () => ({
      binding: f.rows("SELECT active_workflow_state_id, workflow_predecessor_state_id, workflow_binding_generation FROM managed_agents WHERE agent_id=?", agent),
      reminders: f.rows("SELECT * FROM managed_agent_workflow_reminders WHERE agent_id=? ORDER BY reminder_id", agent),
      monitors: f.rows("SELECT * FROM managed_agent_monitors WHERE agent_id=? ORDER BY id", agent),
      history: f.rows("SELECT sequence FROM managed_complete_history WHERE agent_id=? AND message='Workflow state transitioned' ORDER BY sequence", agent),
    });
    const prior = effects();
    const returnTurn = owns("Try the return");
    f.replies.push({ tool: "mecha_workflow_transition", args: { action: "transition", target_state: a.id }, ownsRequest: returnTurn }, { tool: "workflow_snooze", args: { duration_minutes: 5 }, ownsRequest: returnTurn }, { answer: "Return accepted", ownsRequest: returnTurn });
    await userTurn(page, "Try the return");
    await expect(page.getByText("Return accepted", { exact: true }).first()).toBeVisible();
    const guidance = String(f.requests[heldCount]?.instructions);
    expect(guidance).toContain("Final marker");
    expect(guidance).not.toContain("Intermediate marker");
    await expect.poll(async () => (await f.api(`/v1/managed-agents/${agent}/workflow-state`)).active_workflow_state_id).toBe(a.id);
    const after = effects();
    expect(after.binding[0]).toMatchObject({ active_workflow_state_id: a.id, workflow_predecessor_state_id: b.id });
    expect(after.history.length).toBe(prior.history.length + 1);
    await expect.poll(() => f.rows("SELECT activity_state FROM managed_agents WHERE agent_id=?", agent)[0]?.activity_state).toBe("idle");
    expect(f.violations).toEqual([]);
    expect(f.requests.length).toBe(heldCount + 3);
    // The edited text belongs in current instructions, never a queued prompt or
    // steered user message. Check persisted records after the following turn too.
    const persisted = durable();
    expect(persisted.messages.slice(0, held.messages.length)).toEqual(held.messages);
    expect(persisted.messages.slice(held.messages.length)).toHaveLength(1);
    expect(persisted.messages.at(-1)).toMatchObject({ sender: "user", body: "Try the return", state: "acknowledged" });
    expect(JSON.stringify(persisted.prompts)).not.toMatch(/Intermediate marker|Final marker/);
    const userEntries = persisted.entries.filter(row => row.kind === "user");
    expect(JSON.stringify(userEntries)).not.toMatch(/Intermediate marker|Final marker/);
    const nextInputs = f.requests.slice(heldCount).flatMap(r => Array.isArray(r.input) ? r.input.filter(item => item?.role === "user") : []);
    expect(JSON.stringify(nextInputs)).not.toMatch(/Intermediate marker|Final marker/);
    expect(f.replies).toEqual([]);
    expect(f.violations).toEqual([]);
  } finally {
    release();
    await f.close();
  }
});
