import { test, expect } from "@playwright/test";
import { createRunningWorkflow, userTurn, workflowHarness } from "./workflow-implicit-goals-harness";

test("managed state admin exposes the opt-in and updates live return authority", async ({ page }) => {
  test.setTimeout(90_000);
  const f = await workflowHarness();
  try {
    const owns = (text: string) => (request: Record<string, unknown>) =>
      Array.isArray(request.input) && JSON.stringify(request.input.filter(item => item?.role === "user").at(-1)).includes(text);
    f.replies.push({ tool: "workflow_snooze", args: { justification: "Waiting for CI process", duration_minutes: 5 }, ownsRequest: owns("[Workflow Activated: A]") },
      { answer: "Ready", ownsRequest: owns("[Workflow Activated: A]") });
    const { agent, workflow, a, b } = await createRunningWorkflow(f, page, 600000, false);
    const wait = async (answer: string) => {
      await expect(page.getByText(answer, { exact: true }).first()).toBeVisible();
      await expect.poll(() => f.rows("SELECT activity_state FROM managed_agents WHERE agent_id=?", agent)[0]?.activity_state).toBe("idle");
    };
    await wait("Ready");
    await f.api(`/v1/managed-agents/${agent}/tools`, { tool_name: "workflow_state_save", enabled: true }, "PUT");
    const turn = async (text: string, tool: string, args: object) => {
      const ownsRequest = owns(text);
      f.replies.push({ tool, args, ownsRequest }, { tool: "workflow_snooze", args: { justification: "Waiting for CI process", duration_minutes: 5 }, ownsRequest }, { answer: `Done ${text}`, ownsRequest });
      await userTurn(page, text);
      await wait(`Done ${text}`);
      const input = f.requests.at(-1)?.input;
      expect(Array.isArray(input)).toBe(true);
      const call = input.filter((item: Record<string, unknown>) => item.name === tool).at(-1);
      const result = input.find((item: Record<string, unknown>) => item.type === "function_call_output" && item.call_id === call?.call_id);
      expect(result).toBeDefined();
      return String(result.output);
    };
    const save = (text: string, args: object) => turn(text, "workflow_state_save", { workflow_id: workflow.id, ...args });
    await save("Create default", { name: "Default" });
    const request = f.requests.find(r => Array.isArray(r.tools) && r.tools.some((t: { name?: string }) => t.name === "workflow_state_save"));
    const definition = (request?.tools as { name: string; parameters: { properties: Record<string, { type: string }>; required?: string[] } }[]).find(t => t.name === "workflow_state_save");
    expect(definition?.parameters.properties.allow_implicit_return.type).toBe("boolean");
    expect(definition?.parameters.required ?? []).not.toContain("allow_implicit_return");
    const states = () => f.api(`/v1/workflows/${workflow.id}`);
    expect((await states()).states.find((s: { name: string }) => s.name === "Default").allow_implicit_return).toBe(false);
    await save("Create enabled", { name: "Enabled", allow_implicit_return: true });
    expect((await states()).states.find((s: { name: string }) => s.name === "Enabled").allow_implicit_return).toBe(true);
    await save("Create disabled", { name: "Disabled", allow_implicit_return: false });
    expect((await states()).states.find((s: { name: string }) => s.name === "Disabled").allow_implicit_return).toBe(false);
    await turn("Advance", "mecha_workflow_transition", { action: "transition", target_state: b.id });
    expect((await f.api(`/v1/managed-agents/${agent}/workflow-state`)).active_workflow_state_id).toBe(b.id);
    await save("Enable B", { state_id: b.id, allow_implicit_return: true });
    await save("Omit flag", { state_id: b.id, description: "preserve" });
    expect((await f.api(`/v1/workflow-states/${b.id}`)).workflow_state.allow_implicit_return).toBe(true);
    await turn("Return enabled", "mecha_workflow_transition", { action: "transition", target_state: a.id });
    expect((await f.api(`/v1/managed-agents/${agent}/workflow-state`)).active_workflow_state_id).toBe(a.id);
    await turn("Advance again", "mecha_workflow_transition", { action: "transition", target_state: b.id });
    await save("Disable B", { state_id: b.id, allow_implicit_return: false });
    expect((await f.api(`/v1/workflow-states/${b.id}`)).workflow_state.allow_implicit_return).toBe(false);
    expect(await turn("Return disabled", "mecha_workflow_transition", { action: "transition", target_state: a.id })).toBe('workflow state "B" has no outgoing transitions');
    expect((await f.api(`/v1/managed-agents/${agent}/workflow-state`)).active_workflow_state_id).toBe(b.id);
    expect(f.replies).toEqual([]);
    expect(f.violations).toEqual([]);
  } finally { await f.close(); }
});
