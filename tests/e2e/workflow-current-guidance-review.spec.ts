import { test, expect } from "@playwright/test";
import { createRunningWorkflow, userTurn, workflowHarness, type WorkflowHarness } from "./workflow-implicit-goals-harness";

test("next user turns use edited guidance and the current implicit return option", async ({ page }) => {
  test.setTimeout(60_000);
  const f: WorkflowHarness = await workflowHarness();
  try {
    f.replies.push({ tool: "workflow_snooze", args: { justification: "Waiting for CI process", duration_minutes: 5 } }, { answer: "Ready" });
    const { agent, b } = await createRunningWorkflow(f, page, 600000, false);
    await expect(page.getByText("Ready", { exact: true }).first()).toBeVisible();
    // Transitioning clears A's snooze. Snooze B within the same turn so an
    // autonomous continuation cannot consume the next user turn's reply.
    f.replies.push({ tool: "mecha_workflow_transition", args: { action: "transition", target_state: b.id } },
      { tool: "workflow_snooze", args: { justification: "Waiting for CI process", duration_minutes: 5 } }, { answer: "At B" });
    await userTurn(page, "Go to B");
    await expect(page.getByText("At B", { exact: true }).first()).toBeVisible();
    await expect.poll(async () => (await f.api(`/v1/managed-agents/${agent}/workflow-state`)).active_workflow_state_id).toBe(b.id);

    await f.api(`/v1/workflow-states/${b.id}`, { name: "B", entry_text: "Fresh guidance marker", allow_exit: true, allow_implicit_return: true }, "PUT");
    const ownsUserTurn = (text: string) => (request: Record<string, unknown>) => {
      const input = request.input;
      if (!Array.isArray(input)) return false;
      const last = input.at(-1);
      return last?.role === "user" && Array.isArray(last.content) &&
        last.content.some((part: { text?: string }) => part.text === `[Direct message from user]: ${text}`);
    };
    f.replies.push({ answer: "Read enabled guidance", ownsRequest: ownsUserTurn("Read current guidance") });
    await userTurn(page, "Read current guidance");
    await expect(page.getByText("Read enabled guidance", { exact: true }).first()).toBeVisible();
    const enabledRequest = f.requests.find(ownsUserTurn("Read current guidance"));
    expect(enabledRequest).toBeDefined();
    const enabled = String(enabledRequest?.instructions);
    expect(enabled).toContain("Fresh guidance marker");
    expect(enabled).toMatch(/### Allowed Transitions\n- A(?:\:|\n)/);

    await f.api(`/v1/workflow-states/${b.id}`, { name: "B", entry_text: "Newer guidance marker", allow_exit: true, allow_implicit_return: false }, "PUT");
    f.replies.push({ answer: "Read disabled guidance", ownsRequest: ownsUserTurn("Read updated guidance") });
    await userTurn(page, "Read updated guidance");
    await expect(page.getByText("Read disabled guidance", { exact: true }).first()).toBeVisible();
    const disabledRequest = f.requests.find(ownsUserTurn("Read updated guidance"));
    expect(disabledRequest).toBeDefined();
    const disabled = String(disabledRequest?.instructions);
    expect(disabled).toContain("Newer guidance marker");
    expect(disabled).not.toContain("Fresh guidance marker");
    expect(disabled).not.toMatch(/### Allowed Transitions\n- A(?:\:|\n)/);
    expect((await f.api(`/v1/managed-agents/${agent}/workflow-state`)).active_workflow_state_id).toBe(b.id);
    expect(f.violations).toEqual([]);
  } finally {
    await f.close();
  }
});
