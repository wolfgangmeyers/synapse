import { test, expect } from "@playwright/test";
import { createRunningWorkflow, userTurn, workflowHarness } from "./workflow-implicit-goals-harness";

test("IR-004: late REST enable preserves rejected effects and installs return effects", async ({ page }) => {
  test.setTimeout(90_000);
  const f = await workflowHarness();
  const owns = (text: string) => (request: Record<string, unknown>) => Array.isArray(request.input) &&
    JSON.stringify(request.input.filter(item => item?.role === "user").at(-1)).includes(text);
  try {
    f.replies.push({ tool: "workflow_snooze", args: { justification: "Waiting for CI process", duration_minutes: 5 }, ownsRequest: owns("[Workflow Activated: A]") },
      { answer: "Ready", ownsRequest: owns("[Workflow Activated: A]") });
    const { agent, a, b } = await createRunningWorkflow(f, page, 600000, false);
    async function wait(answer: string) {
      await expect(page.getByText(answer, { exact: true }).first()).toBeVisible();
      await expect.poll(() => f.rows("SELECT CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? AND kind='assistant'", agent)
        .some(row => String(row.body).includes(answer))).toBe(true);
      await expect.poll(() => f.rows("SELECT activity_state FROM managed_agents WHERE agent_id=?", agent)[0]?.activity_state).toBe("idle");
    }
    async function move(text: string, target: string, answer: string) {
      const owner = owns(text);
      f.replies.push({ tool: "mecha_workflow_transition", args: { action: "transition", target_state: target }, ownsRequest: owner },
        { tool: "workflow_snooze", args: { justification: "Waiting for CI process", duration_minutes: 5 }, ownsRequest: owner }, { answer, ownsRequest: owner });
      await userTurn(page, text); await wait(answer);
      const input = f.requests.at(-1)?.input;
      expect(Array.isArray(input)).toBe(true);
      const call = input.filter((item: Record<string, unknown>) => item.name === "mecha_workflow_transition").at(-1);
      const result = input.find((item: Record<string, unknown>) => item.type === "function_call_output" && item.call_id === call?.call_id);
      expect(result).toBeDefined();
      return String(result.output);
    }
    await wait("Ready");
    const reminder = (id: string) => ({ id, type: "one_shot", body: `${id} reminder`, delay_seconds: 600 });
    const monitor = (name: string) => ({ name, question_instructions: `Check ${name}`, nudge_template: `Nudge ${name}`, interval_seconds: 3600 });
    await f.api(`/v1/workflow-states/${a.id}`, { name: "A", entry_text: "A guidance", allow_exit: true,
      transitions: [{ target_state_id: b.id, description: "Advance" }], reminders: [reminder("A")], monitors: [monitor("A monitor")] }, "PUT");
    const bDefinition = { name: "B", allow_exit: true, reminders: [reminder("B")], monitors: [monitor("B monitor")] };
    await f.api(`/v1/workflow-states/${b.id}`, { ...bDefinition, allow_implicit_return: false }, "PUT");
    expect((await f.api(`/v1/workflow-states/${b.id}`)).workflow_state.allow_implicit_return).toBe(false);
    expect(await move("Advance to B", b.id, "At B")).toContain("Successfully transitioned");
    // The initial monitor evaluation is asynchronous; snapshot only once settled.
    await expect.poll(() => f.rows("SELECT last_evaluated_at FROM managed_agent_monitors WHERE agent_id=?", agent)[0]?.last_evaluated_at).toBeTruthy();
    const effects = () => ({
      binding: f.rows("SELECT active_workflow_state_id, workflow_predecessor_state_id, workflow_binding_generation FROM managed_agents WHERE agent_id=?", agent),
      links: f.rows("SELECT * FROM managed_agent_workflow_reminders WHERE agent_id=? ORDER BY reminder_id", agent),
      reminders: f.rows("SELECT * FROM reminders WHERE target=? ORDER BY id", agent),
      monitors: f.rows("SELECT * FROM managed_agent_monitors WHERE agent_id=? ORDER BY id", agent),
      history: f.rows("SELECT sequence, presentation_json FROM managed_complete_history WHERE agent_id=? AND message='Workflow state transitioned' ORDER BY sequence", agent),
    });
    const before = effects();
    expect(before.binding[0]).toMatchObject({ active_workflow_state_id: b.id, workflow_predecessor_state_id: a.id });
    expect(before.links).toHaveLength(1);
    expect(before.reminders).toHaveLength(1);
    expect(before.reminders[0]).toMatchObject({ body: "B reminder", state: "scheduled", interval_ms: null, linked_message_id: null });
    const oldReminderID = before.reminders[0].id;
    expect(before.links[0].reminder_id).toBe(oldReminderID);
    expect(before.monitors).toHaveLength(1);
    expect(before.monitors[0].name).toBe("B monitor");
    const rejectedFrames = f.wsFrames.length;
    expect(await move("Try disabled return", a.id, "Rejected return")).toBe('workflow state "B" has no outgoing transitions');
    expect(effects()).toEqual(before);
    expect(f.wsFrames.length).toBeLessThan(2048);
    expect(f.wsFrames.slice(rejectedFrames).map(raw => JSON.parse(raw)).filter(frame => frame.type === "history_entry" &&
      frame.entry?.agent_id === agent && frame.entry?.message === "Workflow state transitioned")).toHaveLength(0);
    await f.api(`/v1/workflow-states/${b.id}`, { ...bDefinition, allow_implicit_return: true }, "PUT");
    expect((await f.api(`/v1/workflow-states/${b.id}`)).workflow_state.allow_implicit_return).toBe(true);
    expect(effects()).toEqual(before);
    const returnFrames = f.wsFrames.length;
    expect(await move("Return after REST enable", a.id, "Returned to A")).toContain("Successfully transitioned");
    const after = effects();
    expect(after.binding[0]).toMatchObject({ active_workflow_state_id: a.id, workflow_predecessor_state_id: b.id });
    expect(after.links).toHaveLength(1);
    expect(after.reminders.map(row => row.body)).toEqual(["A reminder"]);
    expect(f.rows("SELECT id FROM reminders WHERE id=?", oldReminderID)).toEqual([]);
    expect(after.links[0].reminder_id).toBe(after.reminders[0].id);
    expect(after.monitors.map(row => row.name)).toEqual(["A monitor"]);
    expect(after.history.map(row => JSON.parse(String(row.presentation_json)))).toEqual([
      expect.objectContaining({ kind: "state", outcome: "success", from: "A", to: "B" }),
      expect.objectContaining({ kind: "state", outcome: "success", from: "B", to: "A" }),
    ]);
    await expect.poll(() => f.wsFrames.slice(returnFrames).map(raw => JSON.parse(raw)).find(frame => frame.type === "history_entry" &&
      frame.view === "complete" && frame.entry?.id === `complete:${after.history[1].sequence}`)).toMatchObject({
      entry: { agent_id: agent, message: "Workflow state transitioned", presentation: { kind: "state", outcome: "success", from: "B", to: "A" } },
    });
    expect(f.wsFrames.length).toBeLessThan(2048);
    expect(f.violations).toEqual([]);
    expect(f.replies).toEqual([]);
  } finally { await f.close(); }
});
