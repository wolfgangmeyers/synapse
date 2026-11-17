import { test, expect } from "@playwright/test";
import { randomUUID } from "node:crypto";
import { join } from "node:path";
import { userTurn, workflowHarness } from "./workflow-implicit-goals-harness";

test("IR-009: implicit return installs normal entry effects and matches an explicit transition", async ({ page }) => {
  test.setTimeout(120_000);
  const f = await workflowHarness();
  const owns = (text: string) => (r: Record<string, unknown>) => Array.isArray(r.input) &&
    JSON.stringify(r.input.filter(item => item?.role === "user").at(-1)).includes(text);
  try {
    await f.open(page);
    const workflow = await f.api("/v1/workflows", { name: `Entry ${randomUUID()}`, snooze_duration_ms: 600000,
      variables: [{ name: "REPO", label: "Repo", type: "text", required: true }] });
    const { workflow_state: b } = await f.api("/v1/workflow-states", { workflow_id: workflow.id, name: "B", allow_implicit_return: true,
      reminders: [{ id: "b", type: "one_shot", body: "B reminder", delay_seconds: 600 }],
      monitors: [{ name: "B watcher", question_instructions: "Check B", nudge_template: "B nudge", interval_seconds: 3600 }] });
    const { workflow_state: a } = await f.api("/v1/workflow-states", { workflow_id: workflow.id, name: "A", allow_exit: true,
      entry_text: "Inspect {{REPO}}", allowed_tools: ["scratchpad", "workflow_snooze"],
      reminders: [{ id: "a", type: "one_shot", body: "Inspect reminder {{REPO}}", delay_seconds: 600 }],
      monitors: [{ name: "Watch {{REPO}}", question_instructions: "Check {{REPO}}", nudge_template: "Nudge {{REPO}}", interval_seconds: 3600 }],
      transitions: [{ target_state_id: b.id, description: "Advance" }] });
    await f.api(`/v1/workflows/${workflow.id}`, { name: workflow.name, entry_state_id: a.id, snooze_duration_ms: 600000,
      variables: [{ name: "REPO", label: "Repo", type: "text", required: true }] }, "PUT");
    f.replies.push({ tool: "workflow_snooze", args: { justification: "Waiting for CI process", duration_minutes: 5 }, ownsRequest: owns("[Workflow Activated: A]") },
      { answer: "Ready", ownsRequest: owns("[Workflow Activated: A]") });
    const run = await f.api(`/v1/workflows/${workflow.id}/run`, { cwd: join(f.router.root, "work"), variables: { REPO: "sample-repo" } });
    const agent = run.agent_id;
    await page.locator('button[role="tab"]:has-text("Background")').click();
    await page.locator(`[data-agent-id="${agent}"]`).click();
    async function wait(answer: string) {
      await expect(page.getByText(answer, { exact: true }).first()).toBeVisible();
      await expect.poll(() => f.rows("SELECT CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? AND kind='assistant'", agent)
        .some(row => String(row.body).includes(answer))).toBe(true);
      await expect.poll(() => f.rows("SELECT activity_state FROM managed_agents WHERE agent_id=?", agent)[0]?.activity_state).toBe("idle");
    }
    await wait("Ready");
    async function turn(text: string, args: object, answer: string, snooze = true) {
      const owner = owns(text);
      f.replies.push({ tool: "mecha_workflow_transition", args, ownsRequest: owner });
      if (snooze) f.replies.push({ tool: "workflow_snooze", args: { justification: "Waiting for CI process", duration_minutes: 5 }, ownsRequest: owner });
      f.replies.push({ answer, ownsRequest: owner });
      await userTurn(page, text); await wait(answer);
      const request = f.requests.at(-1)!;
      const input = request.input;
      expect(Array.isArray(input)).toBe(true);
      const call = input.filter((item: Record<string, unknown>) => item.name === "mecha_workflow_transition").at(-1);
      const output = input.find((item: Record<string, unknown>) => item.type === "function_call_output" && item.call_id === call?.call_id);
      expect(output).toBeDefined();
      return { output: String(output.output), request };
    }
    const goal = () => f.api(`/v1/managed-agents/${agent}/goal`);
    const effects = () => ({
      reminders: f.rows("SELECT r.body FROM reminders r JOIN managed_agent_workflow_reminders w ON w.reminder_id=r.id WHERE w.agent_id=?", agent),
      monitors: f.rows("SELECT name, question_instructions, nudge_template FROM managed_agent_monitors WHERE agent_id=?", agent),
    });
    const moveB = await turn("Advance to B", { action: "transition", target_state: b.id }, "At B");
    expect(moveB.output).toContain("Successfully transitioned");
    expect(effects()).toEqual({ reminders: [{ body: "B reminder" }], monitors: [{ name: "B watcher", question_instructions: "Check B", nudge_template: "B nudge" }] });
    const beforeFrames = f.wsFrames.length;
    const returned = await turn("Implicit return", { action: "transition", target_state: a.id, reason: "Reviewed return" }, "Returned");
    expect(returned.output).toContain("Inspect sample-repo");
    expect(effects()).toEqual({ reminders: [{ body: "Inspect reminder sample-repo" }], monitors: [{ name: "Watch sample-repo", question_instructions: "Check sample-repo", nudge_template: "Nudge sample-repo" }] });
    const activeGoal = (await goal()).goal_state;
    expect(activeGoal).toMatchObject({ enabled: true, goal: `Complete active workflow: ${workflow.name}` });
    const locked = await fetch(`${f.proxy.url}/v1/managed-agents/${agent}/goal`, { method: "PUT", headers: { "Content-Type": "application/json", "X-Request-ID": randomUUID() }, body: JSON.stringify({ action: "set", goal: "Override" }) });
    expect(locked.status).toBe(409);
    expect(JSON.stringify(await locked.json())).toContain("manual goal control is disabled");
    const names = (returned.request.tools as { name: string }[]).map(t => t.name);
    expect(names).toContain("scratchpad");
    expect(names).not.toContain("bash");
    const history = f.rows("SELECT sequence, presentation_json, message_content FROM managed_complete_history WHERE agent_id=? AND message='Workflow state transitioned' ORDER BY sequence", agent);
    expect(JSON.parse(String(history.at(-1)?.presentation_json))).toMatchObject({ from: "B", to: "A", outcome: "success" });
    expect(history.at(-1)?.message_content).toContain("Reviewed return");
    await expect.poll(() => f.wsFrames.slice(beforeFrames).map(raw => JSON.parse(raw)).some(frame => frame.type === "history_entry" && frame.view === "complete" && frame.entry?.id === `complete:${history.at(-1)?.sequence}`)).toBe(true);
    // Add an explicit reverse edge, repeat the same transition, and compare effects.
    await turn("Advance again", { action: "transition", target_state: b.id }, "At B again");
    await f.api(`/v1/workflow-states/${b.id}`, { name: "B", allow_implicit_return: true,
      transitions: [{ target_state_id: a.id, description: "Explicit reverse" }] }, "PUT");
    const explicit = await turn("Explicit return", { action: "transition", target_state: a.id, reason: "Explicit review" }, "Explicitly returned");
    expect(explicit.output).toContain("Inspect sample-repo");
    expect(effects()).toEqual({ reminders: [{ body: "Inspect reminder sample-repo" }], monitors: [{ name: "Watch sample-repo", question_instructions: "Check sample-repo", nudge_template: "Nudge sample-repo" }] });
    expect((await goal()).goal_state).toMatchObject({ enabled: true, goal: activeGoal.goal });
    const exited = await turn("Exit from A", { action: "exit", reason: "Finished review" }, "Exited", false);
    expect(exited.output).toContain("Successfully exited");
    expect(effects()).toEqual({ reminders: [], monitors: [] });
    expect((await goal()).goal_state).toMatchObject({ enabled: false, goal: "" });
    const exitHistory = f.rows("SELECT message_content FROM managed_complete_history WHERE agent_id=? AND message='Workflow exited'", agent);
    expect(exitHistory.at(-1)?.message_content).toContain("Finished review");
    expect(f.replies).toEqual([]);
    expect(f.violations).toEqual([]);
  } finally { await f.close(); }
});
