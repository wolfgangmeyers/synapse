import { test, expect } from "@playwright/test";
import { createRunningWorkflow, userTurn, workflowHarness } from "./workflow-implicit-goals-harness";

for (const replacement of ["same state", "other state", "deactivate and reactivate", "different workflow"] as const) {
  test(`IR-010: ${replacement} does not retain return authority`, async ({ page }) => {
    test.setTimeout(120_000);
    const f = await workflowHarness();
    const owns = (text: string) => (request: Record<string, unknown>) => Array.isArray(request.input) &&
      JSON.stringify(request.input.filter(item => item?.role === "user").at(-1)).includes(text);
    try {
      f.replies.push({ tool: "workflow_snooze", args: { duration_minutes: 5 }, ownsRequest: owns("[Workflow Activated: A]") },
        { answer: "Ready", ownsRequest: owns("[Workflow Activated: A]") });
      const { agent, workflow, a, b } = await createRunningWorkflow(f, page, 600000, false);
      const wait = async (answer: string) => {
        await expect(page.getByText(answer, { exact: true }).first()).toBeVisible();
        await expect.poll(() => f.rows("SELECT activity_state FROM managed_agents WHERE agent_id=?", agent)[0]?.activity_state).toBe("idle");
      };
      await wait("Ready");
      await f.api(`/v1/workflow-states/${b.id}`, { name: "B", allow_exit: true, allow_implicit_return: true }, "PUT");
      const c = (await f.api("/v1/workflow-states", { workflow_id: workflow.id, name: "C", allow_exit: true, allow_implicit_return: true })).workflow_state;
      const forward = owns("Advance to B");
      f.replies.push({ tool: "mecha_workflow_transition", args: { action: "transition", target_state: b.id }, ownsRequest: forward },
        { tool: "workflow_snooze", args: { duration_minutes: 5 }, ownsRequest: forward }, { answer: "At B", ownsRequest: forward });
      await userTurn(page, "Advance to B"); await wait("At B");
      const binding = () => f.rows("SELECT active_workflow_id, active_workflow_state_id, workflow_predecessor_state_id, workflow_binding_generation FROM managed_agents WHERE agent_id=?", agent)[0];
      expect(binding()).toMatchObject({ active_workflow_state_id: b.id, workflow_predecessor_state_id: a.id });
      if (replacement === "deactivate and reactivate") {
        await f.api(`/v1/managed-agents/${agent}/workflow-state`, { action: "deactivate" }, "PUT");
        expect(binding().active_workflow_state_id || null).toBeNull();
        expect(binding().workflow_predecessor_state_id || null).toBeNull();
      }
      const replacementWorkflow = replacement === "different workflow"
        ? await f.api("/v1/workflows", { name: "Replacement workflow", snooze_duration_ms: 600000 })
        : workflow;
      const target = replacement === "different workflow"
        ? (await f.api("/v1/workflow-states", { workflow_id: replacementWorkflow.id, name: "D", allow_exit: true, allow_implicit_return: true })).workflow_state
        : replacement === "other state" ? c : b;
      // A replacement may schedule a goal continuation before activation delivery.
      const continuation = owns(`The workflow ${replacementWorkflow.name} is currently active in state ${target.name}.`);
      f.interleavedOwners.push(continuation);
      f.interleavedReplies.push({ tool: "workflow_snooze", args: { duration_minutes: 5 }, ownsRequest: continuation },
        { answer: "Replacement continuation snoozed", ownsRequest: continuation });
      const activation = owns(`[Workflow Activated: ${target.name}]`);
      f.replies.push({ tool: "workflow_snooze", args: { duration_minutes: 5 }, ownsRequest: activation }, { answer: "Replacement ready", ownsRequest: activation });
      await f.api(`/v1/managed-agents/${agent}/workflow-state`, { action: "activate", workflow_id: replacementWorkflow.id, workflow_state_id: target.id }, "PUT");
      await wait("Replacement ready");
      const before = binding();
      expect(before.active_workflow_id).toBe(replacementWorkflow.id);
      expect(before.active_workflow_state_id).toBe(target.id);
      expect(before.workflow_predecessor_state_id || null).toBeNull();
      // Preserve every existing row, while allowing the rejected turn to add its
      // own conversation/tool records. Workflow transition and exit records must
      // not be added, removed, or modified.
      const history = f.rows("SELECT * FROM managed_complete_history WHERE agent_id=? ORDER BY sequence", agent);
      const lastSequence = history.at(-1)?.sequence ?? 0;
      const workflowHistory = () => f.rows("SELECT * FROM managed_complete_history WHERE agent_id=? AND message IN ('Workflow state transitioned', 'Workflow exited') ORDER BY sequence", agent);
      const beforeWorkflowHistory = workflowHistory();
      expect(beforeWorkflowHistory.some(row => row.message === "Workflow state transitioned")).toBe(true);
      const attempt = owns("Try old return");
      f.replies.push({ tool: "mecha_workflow_transition", args: { action: "transition", target_state: a.id }, ownsRequest: attempt },
        { tool: "workflow_snooze", args: { duration_minutes: 5 }, ownsRequest: attempt }, { answer: "Return attempted", ownsRequest: attempt });
      await userTurn(page, "Try old return"); await wait("Return attempted");
      const request = f.requests.findLast(r => attempt(r) && Array.isArray(r.input) && r.input.some((i: Record<string, unknown>) => i.type === "function_call" && i.name === "workflow_snooze") && r.input.some((i: Record<string, unknown>) => i.type === "function_call_output"));
      const input = request?.input;
      expect(Array.isArray(input)).toBe(true);
      const call = input.filter((i: Record<string, unknown>) => i.type === "function_call" && i.name === "mecha_workflow_transition").at(-1);
      const output = input.find((i: Record<string, unknown>) => i.type === "function_call_output" && i.call_id === call?.call_id);
      expect(output?.output).toBe(`workflow state "${target.name}" has no outgoing transitions`);
      expect(binding()).toEqual(before);
      expect(f.rows("SELECT * FROM managed_complete_history WHERE agent_id=? AND sequence<=? ORDER BY sequence", agent, lastSequence)).toEqual(history);
      expect(workflowHistory()).toEqual(beforeWorkflowHistory);
      expect([0, 2]).toContain(f.interleavedReplies.length);
      expect(f.replies).toEqual([]); expect(f.violations).toEqual([]);
    } finally { await f.close(); }
  });
}
