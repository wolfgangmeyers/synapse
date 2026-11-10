import { test, expect } from "@playwright/test";
import { randomUUID } from "node:crypto";
import { createRunningWorkflow, userTurn, workflowHarness, type WorkflowHarness } from "./workflow-implicit-goals-harness";

test.describe("Implicit workflow return", () => {
  test.setTimeout(60_000);
  let f: WorkflowHarness;
  let checkContinuationAfterClose: (() => void) | undefined;
  test.beforeEach(async () => { checkContinuationAfterClose = undefined; f = await workflowHarness(); });
  test.afterEach(async () => {
    if (f) await f.close();
    checkContinuationAfterClose?.();
  });

  async function waitForAnswer(page: import("@playwright/test").Page, agent: string, answer: string) {
    await expect(page.getByText(answer, { exact: true }).first()).toBeVisible();
    // Rendering and the prior idle state can both precede persistence of this turn.
    await expect.poll(() => f.rows("SELECT CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? AND kind='assistant'", agent)
      .some(row => String(row.body).includes(answer))).toBe(true);
    await expect.poll(() => f.rows("SELECT activity_state FROM managed_agents WHERE agent_id=?", agent)[0]?.activity_state).toBe("idle");
  }

  test("IR-001/003: defaults off and preserves omitted updates but accepts explicit false", async ({ page }) => {
    await f.open(page);
    const workflow = await f.api("/v1/workflows", { name: `Return ${randomUUID()}` });
    const { workflow_state: state } = await f.api("/v1/workflow-states", { workflow_id: workflow.id, name: "Start" });
    expect(state.allow_implicit_return).toBe(false);
    const path = `/v1/workflow-states/${state.id}`;
    expect((await f.api(path, { name: "Start", allow_implicit_return: true }, "PUT")).workflow_state.allow_implicit_return).toBe(true);
    expect((await f.api(path, { name: "Renamed" }, "PUT")).workflow_state.allow_implicit_return).toBe(true);
    expect((await f.api(path)).workflow_state.allow_implicit_return).toBe(true);
    expect((await f.api(`/v1/workflows/${workflow.id}`)).states[0].allow_implicit_return).toBe(true);
    expect((await f.api(path, { name: "Renamed", allow_implicit_return: false }, "PUT")).workflow_state.allow_implicit_return).toBe(false);
  });

  test("IR-002: accessible editor setting is off by default and survives reload", async ({ page }) => {
    await f.open(page);
    const workflow = await f.api("/v1/workflows", { name: `Editor ${randomUUID()}` });
    await f.api("/v1/workflow-states", { workflow_id: workflow.id, name: "Editor state" });
    await page.evaluate(() => { window.location.hash = "#/workflows"; });
    await page.getByText(workflow.name, { exact: true }).first().click();
    const configure = page.getByRole("button", { name: "Configure Editor state" });
    await configure.click();
    const dialog = page.locator("dialog.state-config-dialog[open]");
    const toggle = dialog.getByRole("checkbox", { name: /implicit return|return.*previous state/i }).or(dialog.getByRole("switch", { name: /implicit return|return.*previous state/i }));
    await expect(toggle).toHaveCount(1);
    await expect(dialog.locator('label[aria-label="Allow implicit return"]')).toBeVisible();
    await expect(toggle).not.toBeChecked();
    await expect(dialog).toContainText(/previous state.*different.*active workflow/i);
    await dialog.locator('label[aria-label="Allow implicit return"]').click();
    await expect(toggle).toBeChecked();
    await dialog.getByRole("button", { name: "Save State Settings" }).click();
    await expect(dialog).toHaveCount(0);
    await page.reload();
    await page.getByText(workflow.name, { exact: true }).first().click();
    await configure.click();
    await expect(toggle).toBeChecked();
  });

  test("IR-001/002/004/005/007/009: tool return is denied when off and succeeds after enabling", async ({ page }) => {
    const owns = (text: string) => (request: Record<string, unknown>) =>
      Array.isArray(request.input) && JSON.stringify(request.input.filter(item => item?.role === "user").at(-1)).includes(text);
    const queueTurn = (text: string, target: string, answer: string, reason?: string) => {
      const ownsRequest = owns(text);
      f.replies.push(
        { tool: "mecha_workflow_transition", args: { action: "transition", target_state: target, ...(reason ? { reason } : {}) }, ownsRequest },
        // A successful transition clears the prior snooze; avoid an autonomous
        // goal continuation consuming the next user turn's scripted response.
        { tool: "workflow_snooze", args: { duration_minutes: 5 }, ownsRequest },
        { answer, ownsRequest },
      );
    };
    const transitionOutput = () => {
      const input = f.requests.at(-1)?.input;
      expect(Array.isArray(input)).toBe(true);
      const call = input.filter((item: Record<string, unknown>) => item.name === "mecha_workflow_transition").at(-1);
      const result = input.find((item: Record<string, unknown>) => item.type === "function_call_output" && item.call_id === call?.call_id);
      expect(result).toBeDefined();
      return String(result.output);
    };
    f.replies.push({ tool: "workflow_snooze", args: { duration_minutes: 5 }, ownsRequest: owns("[Workflow Activated: A]") }, { answer: "Ready", ownsRequest: owns("[Workflow Activated: A]") });
    const { agent, workflow, a, b } = await createRunningWorkflow(f, page, 600000, false);
    await waitForAnswer(page, agent, "Ready");
    await f.api(`/v1/workflow-states/${b.id}`, { name: "B", entry_text: "B guidance", allow_exit: true,
      reminders: [{ id: "b-reminder", type: "one_shot", body: "B reminder", delay_seconds: 600 }] }, "PUT");
    queueTurn("Advance to B", "B", "Reached B");
    await userTurn(page, "Advance to B");
    await waitForAnswer(page, agent, "Reached B");
    await expect.poll(async () => (await f.api(`/v1/managed-agents/${agent}/workflow-state`)).active_workflow_state_id).toBe(b.id);
    const history = () => f.rows("SELECT sequence, presentation_json, message_content FROM managed_complete_history WHERE agent_id=? AND message='Workflow state transitioned' ORDER BY sequence", agent);
    const effects = () => ({
      binding: f.rows("SELECT active_workflow_state_id, workflow_predecessor_state_id, workflow_binding_generation FROM managed_agents WHERE agent_id=?", agent),
      reminders: f.rows("SELECT * FROM managed_agent_workflow_reminders WHERE agent_id=? ORDER BY reminder_id", agent),
      monitors: f.rows("SELECT * FROM managed_agent_monitors WHERE agent_id=? ORDER BY id", agent),
      history: history(),
    });
    const beforeRejected = effects();
    expect(beforeRejected.reminders).toHaveLength(1);
    const rejectedFrames = f.wsFrames.length;
    queueTurn("Try returning while disabled", "A", "First return attempted");
    await userTurn(page, "Try returning while disabled");
    await waitForAnswer(page, agent, "First return attempted");
    expect(transitionOutput()).toMatch(/no outgoing transitions/i);
    expect((await f.api(`/v1/managed-agents/${agent}/workflow-state`)).active_workflow_state_id).toBe(b.id);
    expect(effects()).toEqual(beforeRejected);
    expect(f.wsFrames.length).toBeLessThan(2048);
    expect(f.wsFrames.slice(rejectedFrames).map(raw => JSON.parse(raw)).filter(frame =>
      frame.type === "history_entry" && frame.entry?.agent_id === agent && frame.entry?.message === "Workflow state transitioned")).toHaveLength(0);

    // Change B through the real editor and verify A remains independently off.
    await page.evaluate(() => { window.location.hash = "#/workflows"; });
    await page.getByText(workflow.name, { exact: true }).first().click();
    await page.getByRole("button", { name: "Configure B" }).click();
    const dialog = page.locator("dialog.state-config-dialog[open]");
    const toggle = dialog.getByRole("checkbox", { name: /implicit return|return.*previous state/i }).or(dialog.getByRole("switch", { name: /implicit return|return.*previous state/i }));
    await expect(toggle).not.toBeChecked();
    await dialog.locator('label[aria-label="Allow implicit return"]').click();
    await dialog.getByRole("button", { name: "Save State Settings" }).click();
    await expect(dialog).toHaveCount(0);
    expect((await f.api(`/v1/workflow-states/${a.id}`)).workflow_state.allow_implicit_return).toBe(false);
    expect((await f.api(`/v1/workflow-states/${b.id}`)).workflow_state.allow_implicit_return).toBe(true);
    await page.reload();
    await page.getByRole("button", { name: "Dashboard" }).click();
    await page.locator('button[role="tab"]:has-text("Background")').click();
    await page.locator(`[data-agent-id="${agent}"]`).click();
    const returnFrames = f.wsFrames.length;
    queueTurn("Return to A after enabling", "a", "Returned to A", "Return after review");
    await userTurn(page, "Return to A after enabling");
    await waitForAnswer(page, agent, "Returned to A");
    await expect.poll(async () => (await f.api(`/v1/managed-agents/${agent}/workflow-state`)).active_workflow_state_id).toBe(a.id);
    expect(JSON.stringify(f.requests.at(-1)?.input)).toContain("A guidance");
    const transitions = history();
    expect(transitions.map(row => JSON.parse(String(row.presentation_json)))).toEqual([
      expect.objectContaining({ kind: "state", outcome: "success", from: "A", to: "B" }),
      expect.objectContaining({ kind: "state", outcome: "success", from: "B", to: "A" }),
    ]);
    expect(transitions[1].message_content).toContain("Return after review");
    const returnSequence = transitions[1].sequence;
    await expect.poll(() => f.wsFrames.slice(returnFrames).map(raw => JSON.parse(raw)).find(frame =>
      frame.type === "history_entry" && frame.view === "complete" && frame.entry?.id === `complete:${returnSequence}`)).toMatchObject({
      entry: { agent_id: agent, message: "Workflow state transitioned", presentation: { kind: "state", outcome: "success", from: "B", to: "A" } },
    });
    expect(effects().reminders).toHaveLength(0);
    expect(f.rows("SELECT active_workflow_state_id FROM managed_agents WHERE agent_id=?", agent)[0].active_workflow_state_id).toBe(a.id);

    // An explicit edge remains usable; disabling B after arrival must immediately
    // prevent the implicit return even though the predecessor is still tracked.
    queueTurn("Advance again", b.id, "Back at B");
    await userTurn(page, "Advance again");
    await waitForAnswer(page, agent, "Back at B");
    await expect.poll(async () => (await f.api(`/v1/managed-agents/${agent}/workflow-state`)).active_workflow_state_id).toBe(b.id);
    await page.evaluate(() => { window.location.hash = "#/workflows"; });
    await page.getByText(workflow.name, { exact: true }).first().click();
    await page.getByRole("button", { name: "Configure B" }).click();
    await expect(toggle).toBeChecked();
    await dialog.locator('label[aria-label="Allow implicit return"]').click();
    await expect(toggle).not.toBeChecked();
    await dialog.getByRole("button", { name: "Save State Settings" }).click();
    await expect(dialog).toHaveCount(0);
    await page.reload();
    await page.getByText(workflow.name, { exact: true }).first().click();
    await page.getByRole("button", { name: "Configure B" }).click();
    await expect(toggle).not.toBeChecked();
    expect((await f.api(`/v1/workflow-states/${a.id}`)).workflow_state.allow_implicit_return).toBe(false);
    expect((await f.api(`/v1/workflow-states/${b.id}`)).workflow_state.allow_implicit_return).toBe(false);
    await dialog.getByRole("button", { name: "Cancel", exact: true }).click();
    await page.getByRole("button", { name: "Workflows", exact: true }).click();
    await page.getByRole("button", { name: "Dashboard" }).click();
    await page.locator('button[role="tab"]:has-text("Background")').click();
    await page.locator(`[data-agent-id="${agent}"]`).click();
    const beforeDisabledReturn = effects();
    expect(beforeDisabledReturn.reminders).toHaveLength(1);
    const disabledFrames = f.wsFrames.length;
    queueTurn("Try returning after disabling", a.id, "Second return attempted");
    await userTurn(page, "Try returning after disabling");
    await waitForAnswer(page, agent, "Second return attempted");
    expect(transitionOutput()).toMatch(/no outgoing transitions/i);
    expect((await f.api(`/v1/managed-agents/${agent}/workflow-state`)).active_workflow_state_id).toBe(b.id);
    expect(effects()).toEqual(beforeDisabledReturn);
    expect(f.wsFrames.length).toBeLessThan(2048);
    expect(f.wsFrames.slice(disabledFrames).map(raw => JSON.parse(raw)).filter(frame =>
      frame.type === "history_entry" && frame.entry?.agent_id === agent && frame.entry?.message === "Workflow state transitioned")).toHaveLength(0);
    expect(f.replies).toEqual([]);
    expect(f.violations).toEqual([]);
  });

  test("IR-001/011: old imports default off and definition round trips retain both values", async ({ page }) => {
    await f.open(page);
    const workflow = await f.api("/v1/workflows/import", {
      schema_version: 1, exported_at: "2026-03-30T10:00:00Z",
      workflow: { name: `Import ${randomUUID()}`, variables: [] }, entry_state_key: "a",
      states: [
        { key: "a", name: "A", is_entry_state: true, allowed_tools: [], transitions: [] },
        { key: "b", name: "B", allow_implicit_return: true, allowed_tools: [], transitions: [] },
      ],
    });
    const detail = await f.api(`/v1/workflows/${workflow.id}`);
    expect(detail.states.find((s: { name: string }) => s.name === "A").allow_implicit_return).toBe(false);
    expect(detail.states.find((s: { name: string }) => s.name === "B").allow_implicit_return).toBe(true);
    const bundle = await f.api(`/v1/workflows/${workflow.id}/export`);
    expect(bundle.states.find((s: { name: string }) => s.name === "A").allow_implicit_return).toBe(false);
    expect(bundle.states.find((s: { name: string }) => s.name === "B").allow_implicit_return).toBe(true);
    expect(JSON.stringify(bundle)).not.toMatch(/predecessor|previous_state/);
    expect(bundle.states.every((s: { transitions: unknown[] }) => s.transitions.length === 0)).toBe(true);
    const imported = await f.api("/v1/workflows/import", { ...bundle, workflow: { ...bundle.workflow, name: `Round trip ${randomUUID()}` } });
    const importedDetail = await f.api(`/v1/workflows/${imported.id}`);
    expect(importedDetail.states.find((s: { name: string }) => s.name === "A").allow_implicit_return).toBe(false);
    expect(importedDetail.states.find((s: { name: string }) => s.name === "B").allow_implicit_return).toBe(true);
    const copy = await f.api(`/v1/workflows/${workflow.id}/duplicate`, { name: `Copy ${randomUUID()}` });
    const copyDetail = await f.api(`/v1/workflows/${copy.id}`);
    expect(copyDetail.states.find((s: { name: string }) => s.name === "A").allow_implicit_return).toBe(false);
    expect(copyDetail.states.find((s: { name: string }) => s.name === "B").allow_implicit_return).toBe(true);
  });

  test("IR-004/005/008: only immediate predecessor and repeated returns", async ({ page }) => {
    f.replies.push({ tool: "workflow_snooze", args: { duration_minutes: 5 } }, { answer: "Ready" });
    const { agent, workflow, a, b } = await createRunningWorkflow(f, page, 600000, false);
    await waitForAnswer(page, agent, "Ready");
    const { workflow_state: c } = await f.api("/v1/workflow-states", { workflow_id: workflow.id, name: "C", allow_implicit_return: true });
    await f.api(`/v1/workflow-states/${b.id}`, { name: "B", entry_text: "B guidance", allow_exit: true, allow_implicit_return: true, transitions: [{ target_state_id: c.id, description: "Forward" }] }, "PUT");
    async function move(target: string, answer: string, expected: string, denied = false) {
      const ownsRequest = (request: Record<string, unknown>) => Array.isArray(request.input) &&
        JSON.stringify(request.input.filter(item => item?.role === "user").at(-1)).includes(`Attempt ${answer}`);
      // Successful transitions clear the snooze; re-snooze before the final so
      // the active goal cannot race the next scripted user turn.
      f.replies.push({ tool: "mecha_workflow_transition", args: { action: "transition", target_state: target }, ownsRequest },
        { tool: "workflow_snooze", args: { duration_minutes: 5 }, ownsRequest }, { answer, ownsRequest });
      const before = denied ? {
        binding: f.rows("SELECT active_workflow_state_id, workflow_predecessor_state_id, workflow_binding_generation FROM managed_agents WHERE agent_id=?", agent),
        reminders: f.rows("SELECT * FROM managed_agent_workflow_reminders WHERE agent_id=? ORDER BY reminder_id", agent),
        monitors: f.rows("SELECT * FROM managed_agent_monitors WHERE agent_id=? ORDER BY id", agent),
        history: f.rows("SELECT sequence FROM managed_complete_history WHERE agent_id=? AND message='Workflow state transitioned' ORDER BY sequence", agent),
      } : undefined;
      await userTurn(page, `Attempt ${answer}`);
      await waitForAnswer(page, agent, answer);
      if (denied) {
        const input = f.requests.at(-1)?.input;
        expect(Array.isArray(input)).toBe(true);
        const call = input.filter((item: Record<string, unknown>) => item.name === "mecha_workflow_transition").at(-1);
        const result = input.find((item: Record<string, unknown>) => item.type === "function_call_output" && item.call_id === call?.call_id);
        expect(String(result?.output)).toMatch(/not allowed|not permitted|invalid|denied|no outgoing transitions/i);
        expect(String(result?.output)).toMatch(/\bB\b/i);
        expect({
          binding: f.rows("SELECT active_workflow_state_id, workflow_predecessor_state_id, workflow_binding_generation FROM managed_agents WHERE agent_id=?", agent),
          reminders: f.rows("SELECT * FROM managed_agent_workflow_reminders WHERE agent_id=? ORDER BY reminder_id", agent),
          monitors: f.rows("SELECT * FROM managed_agent_monitors WHERE agent_id=? ORDER BY id", agent),
          history: f.rows("SELECT sequence FROM managed_complete_history WHERE agent_id=? AND message='Workflow state transitioned' ORDER BY sequence", agent),
        }).toEqual(before);
      }
      await expect.poll(async () => (await f.api(`/v1/managed-agents/${agent}/workflow-state`)).active_workflow_state_id).toBe(expected);
    }
    try {
    await move(b.id, "At B", b.id);
    await move(c.id, "At C", c.id);
    await move(a.id, "Rejected A", c.id, true);
    await move("Missing state", "Rejected missing", c.id, true);
    // Remove the forward edge so B → C can only succeed as a repeated return.
    await f.api(`/v1/workflow-states/${b.id}`, { name: "B", entry_text: "B guidance", allow_exit: true, allow_implicit_return: true, transitions: [] }, "PUT");
    await move("b", "Returned B", b.id);
    await move(c.id, "Returned C", c.id);
    await move(c.id, "Rejected self", c.id, true);
    expect(f.violations).toEqual([]);
    } finally {
      if (f.violations.length) {
        console.log("IR-004 diagnostics", JSON.stringify({ violations: f.violations, requests: f.requests,
          entries: f.rows("SELECT seq, kind, CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? ORDER BY seq", agent),
          policy: f.rows("SELECT * FROM managed_agent_policies WHERE agent_id=?", agent),
          evaluations: f.rows("SELECT * FROM embedded_goal_evaluations WHERE agent_id=?", agent),
          continuations: f.rows("SELECT * FROM embedded_goal_continuations WHERE agent_id=?", agent),
          agent: f.rows("SELECT * FROM managed_agents WHERE agent_id=?", agent) }));
      }
    }
  });

  test("IR-010: restart retains predecessor; reactivation clears it", async ({ page }) => {
    // Each scripted response belongs to the most recent user input, not merely
    // whichever request happens to arrive first (including a restart wake).
    // Transitions and reactivation clear the previous snooze. Re-snooze in
    // each owned turn so autonomous goal continuation cannot steal a reply.
    const owns = (text: string) => (request: Record<string, unknown>) => {
      if (!Array.isArray(request.input)) return false;
      const user = request.input.filter(item => item?.role === "user").at(-1);
      return Array.isArray(user?.content) && user.content.some((part: { type?: string; text?: string }) =>
        part.type === "input_text" && (text.startsWith("[Workflow Activated:")
          ? typeof part.text === "string" && part.text.split("\n").includes(text)
          : part.text === `[Direct message from user]: ${text}`));
    };
    f.replies.push({ tool: "workflow_snooze", args: { duration_minutes: 5 }, ownsRequest: owns("[Workflow Activated: A]") }, { answer: "Ready", ownsRequest: owns("[Workflow Activated: A]") });
    const { agent, workflow, a, b } = await createRunningWorkflow(f, page, 600000, false);
    await waitForAnswer(page, agent, "Ready");
    await f.api(`/v1/workflow-states/${b.id}`, { name: "B", entry_text: "B guidance", allow_exit: true, allow_implicit_return: true }, "PUT");
    f.replies.push({ tool: "mecha_workflow_transition", args: { action: "transition", target_state: b.id }, ownsRequest: owns("Go to B") }, { tool: "workflow_snooze", args: { duration_minutes: 5 }, ownsRequest: owns("Go to B") }, { answer: "At B", ownsRequest: owns("Go to B") });
    await userTurn(page, "Go to B");
    await waitForAnswer(page, agent, "At B");
    await f.restart();
    await expect.poll(async () => (await f.api(`/v1/managed-agents/${agent}/workflow-state`)).active_workflow_state_id).toBe(b.id);
    await page.reload();
    await page.locator('button[role="tab"]:has-text("Background")').click();
    await page.locator(`[data-agent-id="${agent}"]`).click();
    f.replies.push({ tool: "mecha_workflow_transition", args: { action: "transition", target_state: a.id }, ownsRequest: owns("Return after restart") }, { tool: "workflow_snooze", args: { duration_minutes: 5 }, ownsRequest: owns("Return after restart") }, { answer: "Returned after restart", ownsRequest: owns("Return after restart") });
    await userTurn(page, "Return after restart");
    await waitForAnswer(page, agent, "Returned after restart");
    await expect.poll(async () => (await f.api(`/v1/managed-agents/${agent}/workflow-state`)).active_workflow_state_id).toBe(a.id);
    // Reactivation may admit a B goal continuation before its activation message.
    // Own that exact request independently; never consume the activation script.
    const continuationText = `The workflow ${workflow.name} is currently active in state B. You must either make progress towards completing the workflow, call workflow_snooze, exit the workflow with mecha_workflow_transition (action exit) when an exit is allowed, or call mecha_blocked with the reason.`;
    const ownsBContinuation = (request: Record<string, unknown>) => {
      if (!Array.isArray(request.input)) return false;
      const user = request.input.filter(item => item?.role === "user").at(-1);
      return user?.content?.length === 1 && user.content[0]?.type === "input_text" && user.content[0]?.text === continuationText;
    };
    let continuationCallID: string | undefined;
    const ownsContinuationStart = (request: Record<string, unknown>) => {
      if (!ownsBContinuation(request) || !Array.isArray(request.input) || request.input.at(-1)?.role !== "user") return false;
      // The mock assigns call IDs from the one-based provider request number.
      continuationCallID = `call_response_${f.requests.indexOf(request) + 1}_0`;
      return true;
    };
    const ownsContinuationResult = (request: Record<string, unknown>) => {
      if (!ownsBContinuation(request) || !continuationCallID || !Array.isArray(request.input)) return false;
      const lastUser = request.input.findLastIndex(item => item?.role === "user");
      const turn = request.input.slice(lastUser + 1);
      if (turn.length !== 2 || turn[0]?.type !== "function_call" || turn[0].name !== "workflow_snooze" ||
          turn[0].call_id !== continuationCallID || turn[1]?.type !== "function_call_output" || turn[1].call_id !== continuationCallID) return false;
      try {
        const output = JSON.parse(turn[1].output);
        return output?.ok === true && output.duration_ms === 300_000 && output.workflow === workflow.name &&
          typeof output.snoozed_until === "string" && output.snoozed_until.length > 0;
      } catch { return false; }
    };
    const continuationReplies = [
      { tool: "workflow_snooze", args: { duration_minutes: 5 }, ownsRequest: ownsContinuationStart },
      { answer: "B continuation snoozed", ownsRequest: ownsContinuationResult },
    ];
    f.interleavedOwners.push(ownsBContinuation);
    f.interleavedReplies.push(...continuationReplies);
    checkContinuationAfterClose = () => {
      expect([0, 2], "continuation reply pair must be fully consumed or untouched").toContain(
        continuationReplies.filter(reply => f.interleavedReplies.includes(reply)).length,
      );
      expect(f.replies).toEqual([]);
      expect(f.violations).toEqual([]);
    };
    f.replies.push({ tool: "workflow_snooze", args: { duration_minutes: 5 }, ownsRequest: owns("[Workflow Activated: B]") }, { answer: "Reactivation guidance received", ownsRequest: owns("[Workflow Activated: B]") });
    await f.api(`/v1/managed-agents/${agent}/workflow-state`, { action: "activate", workflow_id: workflow.id, workflow_state_id: b.id }, "PUT");
    await waitForAnswer(page, agent, "Reactivation guidance received");
    f.replies.push({ tool: "mecha_workflow_transition", args: { action: "transition", target_state: a.id }, ownsRequest: owns("Try return after replacement") }, { tool: "workflow_snooze", args: { duration_minutes: 5 }, ownsRequest: owns("Try return after replacement") }, { answer: "Fresh return attempted", ownsRequest: owns("Try return after replacement") });
    await userTurn(page, "Try return after replacement");
    await waitForAnswer(page, agent, "Fresh return attempted");
    const input = f.requests.at(-1)?.input;
    expect(Array.isArray(input)).toBe(true);
    const transition = input.filter((item: Record<string, unknown>) => item.name === "mecha_workflow_transition").at(-1);
    const result = input.find((item: Record<string, unknown>) => item.type === "function_call_output" && item.call_id === transition?.call_id);
    expect(String(result?.output)).toMatch(/not allowed|not permitted|invalid|denied|no outgoing transitions/i);
    expect((await f.api(`/v1/managed-agents/${agent}/workflow-state`)).active_workflow_state_id).toBe(b.id);
    await f.api(`/v1/managed-agents/${agent}/workflow-state`, { action: "deactivate" }, "PUT");
    const cleared = f.rows("SELECT active_workflow_state_id, workflow_predecessor_state_id FROM managed_agents WHERE agent_id=?", agent)[0];
    expect(cleared.active_workflow_state_id || null).toBeNull();
    expect(cleared.workflow_predecessor_state_id || null).toBeNull();
    expect(f.replies).toEqual([]);
    expect(f.violations).toEqual([]);
  });

});
