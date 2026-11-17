import { test, expect } from "@playwright/test";
import { createRunningWorkflow, userTurn, workflowHarness } from "./workflow-implicit-goals-harness";

for (const ordering of ["before transaction", "after commit"] as const) {
  test(`IR-015: target definition edit ${ordering} preserves installed entry effects`, async ({ page }) => {
    test.setTimeout(120_000);
    const f = await workflowHarness();
    const owns = (text: string) => (r: Record<string, unknown>) => Array.isArray(r.input) &&
      JSON.stringify(r.input.filter(item => item?.role === "user").at(-1)).includes(text);
    try {
      f.replies.push({ tool: "workflow_snooze", args: { justification: "Waiting for CI process", duration_minutes: 5 }, ownsRequest: owns("[Workflow Activated: A]") },
        { answer: "Ready", ownsRequest: owns("[Workflow Activated: A]") });
      const { agent, b } = await createRunningWorkflow(f, page, 600000, false);
      const wait = async (answer: string) => {
        await expect(page.getByText(answer, { exact: true }).first()).toBeVisible();
        await expect.poll(() => f.rows("SELECT activity_state FROM managed_agents WHERE agent_id=?", agent)[0]?.activity_state).toBe("idle");
      };
      await wait("Ready");
      const definition = (version: string) => ({ name: "B", entry_text: `Guidance ${version}`, allow_exit: true,
        reminders: [version === "old"
          ? { id: "reminder-old", type: "one_shot", subject: "Subject old", body: "Reminder old", delay_seconds: 600 }
          : { id: "reminder-new", type: "recurring", subject: "Subject new", body: "Reminder new", delay_seconds: 1200, interval_seconds: 1800 }],
        monitors: [{ name: `Monitor ${version}`, description: `Description ${version}`,
          question_instructions: `Question ${version}`, nudge_template: `Nudge ${version}`,
          interval_seconds: version === "old" ? 3600 : 7200, cooldown_seconds: version === "old" ? 120 : 240,
          max_alerts: version === "old" ? 2 : 4, trailing_messages_count: version === "old" ? 6 : 8 }] });
      const save = (version: string) => f.api(`/v1/workflow-states/${b.id}`, definition(version), "PUT");
      const effects = () => ({
        reminders: f.rows(`SELECT id, target, creator, subject, body, created_at, next_due_at, interval_ms, state
          FROM reminders WHERE target=? ORDER BY id`, agent),
        links: f.rows(`SELECT agent_id, workflow_state_id, reminder_id
          FROM managed_agent_workflow_reminders WHERE agent_id=? ORDER BY reminder_id`, agent),
        monitors: f.rows(`SELECT agent_id, workflow_state_id, name, description, enabled, interval_seconds,
          cooldown_seconds, max_alerts, trailing_messages_count, question_type, question_instructions,
          condition_operator, condition_threshold, nudge_template, fallback_mode
          FROM managed_agent_monitors WHERE agent_id=?`, agent),
      });
      const checkEffects = (version: "old" | "new") => {
        const installed = effects();
        expect(installed.reminders).toHaveLength(1);
        const reminder = installed.reminders[0];
        expect(reminder).toMatchObject({ target: agent, creator: "system",
          subject: `Subject ${version}`, body: `Reminder ${version}`, state: "scheduled",
          interval_ms: version === "old" ? null : 1800000 });
        expect(installed.links).toEqual([{ agent_id: agent, workflow_state_id: b.id, reminder_id: reminder.id }]);
        expect(Date.parse(String(reminder.created_at))).toBe(f.clock);
        expect(Date.parse(String(reminder.next_due_at)) - Date.parse(String(reminder.created_at)))
          .toBe((version === "old" ? 600 : 1200) * 1000);
        expect(installed.monitors).toEqual([{ agent_id: agent, workflow_state_id: b.id, name: `Monitor ${version}`,
          description: `Description ${version}`, enabled: 1, interval_seconds: version === "old" ? 3600 : 7200,
          cooldown_seconds: version === "old" ? 120 : 240, max_alerts: version === "old" ? 2 : 4,
          trailing_messages_count: version === "old" ? 6 : 8, question_type: "noul",
          question_instructions: `Question ${version}`, condition_operator: "gte", condition_threshold: "0.8",
          nudge_template: `Nudge ${version}`, fallback_mode: "skip" }]);
      };
      await save("old");
      if (ordering === "before transaction") {
        const saved = await save("new");
        expect(saved.workflow_state).toMatchObject(definition("new"));
      }
      const owner = owns("Enter B for ordering");
      f.replies.push({ tool: "mecha_workflow_transition", args: { action: "transition", target_state: b.id }, ownsRequest: owner },
        { tool: "workflow_snooze", args: { justification: "Waiting for CI process", duration_minutes: 5 }, ownsRequest: owner }, { answer: "Entered B", ownsRequest: owner });
      await userTurn(page, "Enter B for ordering");
      await wait("Entered B");
      expect(JSON.stringify(f.requests.at(-1)?.input)).toContain("Successfully transitioned to workflow state: B");
      checkEffects(ordering === "before transaction" ? "new" : "old");
      if (ordering === "after commit") {
        const saved = await save("new");
        expect(saved.workflow_state).toMatchObject(definition("new"));
        checkEffects("old");
      }
      expect((await f.api(`/v1/workflow-states/${b.id}`)).workflow_state).toMatchObject(definition("new"));
      expect(f.replies).toEqual([]); expect(f.violations).toEqual([]);
    } finally {
      await f.close();
    }
  });
}
