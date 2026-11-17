import { test, expect } from "@playwright/test";
import { setupE2EHarness, type E2ETestHarness } from "./harness";

test.describe("Workflow Implicit Goals & State Modal E2E", () => {
  let harness: E2ETestHarness;

  test.beforeAll(async () => {
    harness = await setupE2EHarness({ useProxy: true, managed: true });
  });

  test.afterAll(async () => {
    if (harness) {
      try { await closeWorkflowProxyChild(harness.proxy); } finally { await harness.close(); }
    }
  });

  test("SCN-WFIG-001: Workflow creation with custom snooze, state modal inspection (no goal field), and end-to-end transitions", async ({
    page,
  }) => {
    const targetRouterUrl = harness.proxy ? harness.proxy.url : harness.router.httpUrl;

    // Load Web UI connected to test harness router
    await page.goto(harness.frontend.url);
    await page.evaluate(
      ({ routerUrl }) => {
        localStorage.setItem("mecha.web.activeRouter", routerUrl);
        localStorage.setItem("mecha.web.routers", JSON.stringify([routerUrl]));
      },
      { routerUrl: targetRouterUrl }
    );
    await page.reload();

    // 1. Navigate to Workflows page via hash navigation or menu
    await page.evaluate(() => {
      window.location.hash = "#/workflows";
    });

    const workflowsTitle = page.locator(".workflows-title h2");
    await expect(workflowsTitle).toBeVisible({ timeout: 10000 });
    await expect(workflowsTitle).toHaveText("Workflows");

    // 2. Open "New Workflow" dialog and verify snooze input exists with default 10 minutes
    const newWorkflowBtn = page.locator('button:has-text("New Workflow")');
    await expect(newWorkflowBtn).toBeVisible({ timeout: 5000 });
    await newWorkflowBtn.click();

    const createDialog = page.locator('dialog.message-dialog[open]');
    await expect(createDialog).toBeVisible({ timeout: 5000 });
    await expect(createDialog.locator("#create-workflow-dialog-title")).toHaveText("New Workflow");

    // Verify goal snooze input is present and defaulted to 10
    const snoozeInput = createDialog.locator("#workflow-snooze-input");
    await expect(snoozeInput).toBeVisible();
    await expect(snoozeInput).toHaveValue("10");
    await expect(createDialog.locator('label[for="workflow-snooze-input"]')).toHaveText("Maximum snooze (minutes)");

    // Set custom workflow details
    const workflowName = "E2E Test Workflow " + Date.now();
    await createDialog.locator("#workflow-name-input").fill(workflowName);
    await createDialog.locator("#workflow-desc-input").fill("Automated E2E test workflow");
    await snoozeInput.fill("15");
    await expect(snoozeInput).toHaveValue("15");

    // Submit workflow creation -> opens canvas
    const submitBtn = createDialog.locator('button[type="submit"]');
    await submitBtn.click();

    // Verify canvas opens with workflow name in header
    const canvasHeader = page.locator(".workflows-title h2");
    await expect(canvasHeader).toBeVisible({ timeout: 5000 });
    await expect(canvasHeader).toHaveText(workflowName);

    // 3. Add an entry state to the workflow
    const addStateBtn = page.locator('button:has-text("Add State")');
    await expect(addStateBtn).toBeVisible();
    await addStateBtn.click();

    const addStateDialog = page.locator('dialog.message-dialog[open]');
    await expect(addStateDialog).toBeVisible();
    await addStateDialog.locator("#canvas-state-name").fill("Initial Planning");
    await addStateDialog.locator("#canvas-state-desc").fill("Step 1 of the workflow");
    await addStateDialog.locator('button[type="submit"]:has-text("Create State")').click();

    // Verify state node is rendered on canvas
    const stateNode = page.locator('.workflow-node-card:has-text("Initial Planning")');
    await expect(stateNode).toBeVisible({ timeout: 5000 });

    // Open State Configuration Modal by clicking the configure button on the state node
    const configBtn = stateNode.getByRole("button", { name: "Configure Initial Planning" });
    await expect(configBtn).toBeVisible();
    await configBtn.click();

    const stateConfigDialog = page.locator('dialog.state-config-dialog[open]');
    await expect(stateConfigDialog).toBeVisible({ timeout: 5000 });
    await expect(stateConfigDialog.locator("#state-config-dialog-title")).toContainText("Configure State: Initial Planning");

    // CONFIRM: No state goal input exists in the state modal!
    const goalInput = stateConfigDialog.locator('#state-goal-input, input[name="goal"], textarea[name="goal"]');
    await expect(goalInput).toHaveCount(0);
    const goalLabel = stateConfigDialog.locator('label:has-text("Goal")');
    await expect(goalLabel).toHaveCount(0);

    // Verify valid fields in the modal: Name, Description, Entry State toggle, Entry Text, Exit Permission
    await expect(stateConfigDialog.locator("#state-name-input")).toHaveValue("Initial Planning");
    await expect(stateConfigDialog.locator("#state-desc-input")).toHaveValue("Step 1 of the workflow");
    await expect(stateConfigDialog.locator("#state-entry-text")).toBeVisible();
    await stateConfigDialog.locator("#state-entry-text").fill("Analyze requirements and prepare implementation plan.");

    // Designate as Entry State
    const entryToggle = stateConfigDialog.locator('label.switch-toggle[aria-label="Designate as Entry State (Workflow Start)"]');
    await entryToggle.click();

    // Save state configuration
    await stateConfigDialog.locator('button[type="submit"]:has-text("Save State Settings")').click();
    await expect(stateConfigDialog).toHaveCount(0);

    // Add a second state "Implementation"
    await addStateBtn.click();
    const addSecondDialog = page.locator('dialog.message-dialog[open]');
    await addSecondDialog.locator("#canvas-state-name").fill("Implementation");
    await addSecondDialog.locator("#canvas-state-desc").fill("Step 2 of the workflow");
    await addSecondDialog.locator('button[type="submit"]:has-text("Create State")').click();

    // Initial planning node entry text: "Analyze requirements and prepare implementation plan." contains "implementation"!
    // Match exact state title instead:
    const secondStateNode = page.locator('.workflow-node-card').filter({ has: page.locator('.workflow-node-name', { hasText: 'Implementation' }) });
    await expect(secondStateNode).toBeVisible({ timeout: 5000 });

    // Configure second state to allow exit
    const configSecondBtn = secondStateNode.getByRole("button", { name: "Configure Implementation" });
    await configSecondBtn.click();
    const secondConfigDialog = page.locator('dialog.state-config-dialog[open]');
    await expect(secondConfigDialog).toBeVisible({ timeout: 5000 });

    // Again confirm: No goal field in second state config modal!
    await expect(secondConfigDialog.locator('#state-goal-input, input[name="goal"], textarea[name="goal"]')).toHaveCount(0);

    await secondConfigDialog.locator("#state-entry-text").fill("Execute all planned code changes.");
    await secondConfigDialog.locator('button[type="submit"]:has-text("Save State Settings")').click();
    await expect(secondConfigDialog).toHaveCount(0);

    // 4. Verify REST API reflects the workflow's configured snooze_duration_ms and state properties
    const listRes = await fetch(`${targetRouterUrl}/v1/workflows`);
    expect(listRes.status).toBe(200);
    const workflowsList = await listRes.json();
    const createdWf = workflowsList.find((w: any) => w.name === workflowName);
    expect(createdWf).toBeDefined();
    // 15 minutes = 900,000 ms
    expect(createdWf.snooze_duration_ms).toBe(900000);

    // Verify states of the workflow
    const wfDetailRes = await fetch(`${targetRouterUrl}/v1/workflows/${createdWf.id}`);
    expect(wfDetailRes.status).toBe(200);
    const wfDetail = await wfDetailRes.json();
    expect(wfDetail.states).toHaveLength(2);
    const state1 = wfDetail.states.find((s: any) => s.name === "Initial Planning");
    const state2 = wfDetail.states.find((s: any) => s.name === "Implementation");
    expect(state1).toBeDefined();
    expect(state2).toBeDefined();
    // Confirm states have NO goal field
    expect(state1.goal).toBeUndefined();
    expect(state2.goal).toBeUndefined();

    await page.getByRole("button", { name: "Workflows", exact: true }).click();
    const card = page.locator(".workflow-card").filter({ hasText: workflowName });
    await card.getByRole("button", { name: "Edit or rename workflow" }).click();
    await expect(page.locator("#rename-snooze-input")).toHaveValue("15");
    await page.getByRole("button", { name: "Save Changes", exact: true }).click();
    await expect(page.locator("#rename-snooze-input")).toHaveCount(0);
    await card.getByRole("button", { name: "Edit or rename workflow" }).click();
    await expect(page.locator("#rename-snooze-input")).toHaveValue("15");
    await page.getByRole("button", { name: "Cancel", exact: true }).click();
    await card.click();
    const exportRes = await fetch(`${targetRouterUrl}/v1/workflows/${createdWf.id}/export`);
    expect(exportRes.status).toBe(200);
    const bundle = await exportRes.json();
    expect(bundle.workflow.snooze_duration_ms).toBe(900000);
    for (const state of bundle.states) expect(state).not.toHaveProperty("goal");

    // 5. Connect transition from Initial Planning -> Implementation
    // We can add transition via PUT /v1/workflow-states/:id
    const addTransitionRes = await fetch(`${targetRouterUrl}/v1/workflow-states/${state1.id}`, {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        name: state1.name,
        description: state1.description,
        entry_text: state1.entry_text,
        allow_exit: false,
        transitions: [
          {
            target_state_id: state2.id,
            description: "Move to implementation once plan is approved",
          },
        ],
      }),
    });
    expect(addTransitionRes.status).toBe(200);

    // 6. Test End-to-End Workflow Execution & State Transition with Implicit Goal:
    // Launch background worker for this workflow
    const runRes = await fetch(`${targetRouterUrl}/v1/workflows/${createdWf.id}/run`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({}),
    });
    expect(runRes.status).toBe(201);
    const runData = await runRes.json();
    const agentId = runData.agent_id;
    expect(agentId).toBeDefined();

    // Verify agent's active goal is the implicit workflow goal:
    const goalRes = await fetch(`${targetRouterUrl}/v1/managed-agents/${agentId}/goal`);
    expect(goalRes.status).toBe(200);
    const goalData = await goalRes.json();
    expect(goalData.goal_state.enabled).toBe(true);
    expect(goalData.goal_state.goal).toBe(`Complete active workflow: ${workflowName}`);
    // Snooze duration inherited from workflow (15 min = 900,000 ms)
    expect(goalData.goal_state.snooze_duration_ms).toBe(900000);

    // Attempting manual goal change is rejected while workflow is active
    const rejectGoalRes = await fetch(`${targetRouterUrl}/v1/managed-agents/${agentId}/goal`, {
      method: "PUT",
      headers: {
        "Content-Type": "application/json",
        "X-Request-ID": "00000000-0000-4000-8000-000000000099",
      },
      body: JSON.stringify({
        action: "set",
        goal: "Tampering with goal",
      }),
    });
    expect(rejectGoalRes.status).toBe(409);
    const rejectData = await rejectGoalRes.json();
    expect(rejectData.error.message).toContain("manual goal control is disabled while workflow");

    // Perform state transition: Initial Planning -> Implementation
    const transitionRes = await fetch(`${targetRouterUrl}/v1/managed-agents/${agentId}/workflow-state`, {
      method: "PUT",
      headers: {
        "Content-Type": "application/json",
        "X-Request-ID": "00000000-0000-4000-8000-000000000100",
      },
      body: JSON.stringify({
        action: "activate",
        workflow_id: createdWf.id,
        workflow_state_id: state2.id,
      }),
    });
    expect(transitionRes.status).toBe(200);

    // Verify goal is STILL active and untouched across state transition!
    const goalAfterTransition = await fetch(`${targetRouterUrl}/v1/managed-agents/${agentId}/goal`);
    expect(goalAfterTransition.status).toBe(200);
    const goalAfterData = await goalAfterTransition.json();
    expect(goalAfterData.goal_state.enabled).toBe(true);
    expect(goalAfterData.goal_state.goal).toBe(`Complete active workflow: ${workflowName}`);

    // Deactivate / exit workflow -> goal is cleared
    const exitRes = await fetch(`${targetRouterUrl}/v1/managed-agents/${agentId}/workflow-state`, {
      method: "PUT",
      headers: {
        "Content-Type": "application/json",
        "X-Request-ID": "00000000-0000-4000-8000-000000000101",
      },
      body: JSON.stringify({
        action: "deactivate",
      }),
    });
    expect(exitRes.status).toBe(200);

    const goalAfterExit = await fetch(`${targetRouterUrl}/v1/managed-agents/${agentId}/goal`);
    expect(goalAfterExit.status).toBe(200);
    const goalAfterExitData = await goalAfterExit.json();
    expect(goalAfterExitData.goal_state.enabled).toBe(false);
    expect(goalAfterExitData.goal_state.goal).toBe("");
  });

});

// Recovery scenarios use a real embedded runtime; the earlier editor test is
// retained separately so UI regressions do not hide provider-boundary failures.
import { workflowHarness, createRunningWorkflow, settled, userTurn, lastToolOutput, model, closeWorkflowProxyChild, type WorkflowHarness } from "./workflow-implicit-goals-harness";
import { createHash, randomUUID } from "node:crypto";

test.describe("Persisted workflow snooze through real boundaries", () => {
  test.setTimeout(120000);
  let f: WorkflowHarness;
  test.beforeEach(async () => { f = await workflowHarness(); });
  test.afterEach(async () => {
    if (!f) return;
    const violations = [...f.violations];
    await f.close();
    expect(violations, "only explicitly queued worker model requests are allowed").toEqual([]);
  });

  for (const snoozeTool of ["workflow_snooze", "goal_snooze"]) test(`SCN-WFIG-004/008/009: ${snoozeTool} survives intermediate turns and admits one exact continuation`, async ({ page }) => {
    f.replies.push({ tool: snoozeTool, args: snoozeTool === "goal_snooze" ? { duration_ms: 300000 } : { justification: "Waiting for external build", duration_minutes: 5 } }, { answer: "Initial snooze saved" });
    const { agent, workflow } = await createRunningWorkflow(f, page);
    await expect.poll(() => settled(f, agent).length).toBe(1);
    await expect(page.getByText("Initial snooze saved", { exact: true }).first()).toBeVisible();
    const initial = settled(f, agent)[0];
    expect(initial.state).toBe("NOT_DONE");
    expect(initial.continuation_state).toBe("reserved");
    const deadline = String(initial.due_at);
    const goal = (await f.api(`/v1/managed-agents/${agent}/goal`)).goal_state;
    expect(goal.enabled).toBe(true);
    const preferences = () => f.rows("SELECT * FROM embedded_goal_snooze_preferences WHERE agent_id=?", agent);
    expect(preferences()).toHaveLength(1);
    const policy = () => f.rows("SELECT desired_revision, snoozed_until FROM managed_agent_policies WHERE agent_id=?", agent)[0];
    const originalPolicy = policy();
    expect(originalPolicy.snoozed_until).toBe(deadline);
    const seen = new Set([initial.turn_key]);
    async function assertIntermediate(count: number, answer: string) {
      await expect.poll(() => settled(f, agent).length).toBe(count);
      const turn = settled(f, agent).at(-1)!;
      expect(seen.has(turn.turn_key)).toBe(false);
      seen.add(turn.turn_key);
      expect(turn.state).toBe("NOT_DONE");
      expect(turn.continuation_state).toBe("reserved");
      expect(turn.due_at).toBe(deadline);
      expect(turn.started_at).toBeNull();
      expect(preferences()).toHaveLength(1);
      expect(policy()).toEqual(originalPolicy);
      expect((await f.api(`/v1/managed-agents/${agent}/goal`)).goal_state).toMatchObject({
        enabled: true, goal: goal.goal, });
      expect(f.rows("SELECT * FROM embedded_goal_continuations WHERE agent_id=? AND state='started'", agent)).toHaveLength(0);
      await expect(page.getByText(answer, { exact: true }).first()).toBeVisible();
    }
    f.replies.push({ answer: "User steering handled without snooze" });
    await userTurn(page, "Keep waiting, acknowledge this user steering");
    await assertIntermediate(2, "User steering handled without snooze");
    f.replies.push({ answer: "Mailbox steering handled without snooze" });
    const now = (await f.advance(1)).now;
    await f.api("/v1/messages", { id: randomUUID(), from: "workflow-fixture", to: agent,
      subject: "Mailbox steering", body: "Acknowledge without snoozing", created_at: now, source: { kind: "agent" } });
    await assertIntermediate(3, "Mailbox steering handled without snooze");
    f.replies.push({ answer: "State reminder handled without snooze" });
    await f.advance(60000);
    await assertIntermediate(4, "State reminder handled without snooze");
    expect(f.requests).toHaveLength(5); // tool, final, user, mailbox, reminder: no evaluator
    const clock = await f.advance(1);
    await f.advance(Date.parse(deadline) - Date.parse(clock.now) - 1);
    expect(f.requests).toHaveLength(5);
    expect(f.rows("SELECT * FROM embedded_goal_continuations WHERE agent_id=? AND state='started'", agent)).toHaveLength(0);
    // Keep the expiry request in flight: later independent reservations cannot
    // be confused with duplicate dispatch of the current reservation.
    f.replies.push({ hold: true });
    await f.advance(1);
    await expect.poll(() => f.requests.length).toBe(6);
    const expected = `The workflow ${workflow.name} is currently active in state A. You must either make progress towards completing the workflow, call workflow_snooze, exit the workflow with mecha_workflow_transition (action exit) when an exit is allowed, or call mecha_blocked with the reason.`;
    const input = f.requests.at(-1)!.input;
    expect(Array.isArray(input)).toBe(true);
    const lastUser = [...(input as { role?: string; content?: unknown }[])].reverse().find(item => item.role === "user");
    expect(lastUser?.content).toEqual([{ type: "input_text", text: expected }]);
    const current = settled(f, agent).at(-1)!;
    expect(current.continuation_state).toBe("started");
    for (let i = 0; i < 3; i++) {
      // Clock advance runs the router sweep/idle dispatch as well as due timers.
      await f.advance(10000);
      await f.api(`/v1/managed-agents/${agent}/goal`);
      expect(f.requests).toHaveLength(6);
      expect(settled(f, agent).at(-1)!.started_at).toBe(current.started_at);
    }
  });

  test("SCN-WFIG-006: legacy import drops state goals and defaults snooze", async ({ page }) => {
    await f.open(page);
    for (const duration of [undefined, 0, -1]) {
      const workflow = await f.api("/v1/workflows/import", {
        schema_version: 1, exported_at: "2026-03-30T10:00:00Z",
        workflow: { name: `Legacy ${randomUUID()}`, description: "Legacy import", snooze_duration_ms: duration, variables: [] },
        entry_state_key: "start", states: [{ key: "start", name: "Legacy state", entry_text: "Legacy guidance", goal: "Discard me",
          is_entry_state: true, allow_exit: true, allowed_tools: [], transitions: [] }],
      });
      expect(workflow.snooze_duration_ms).toBe(600000);
      const detail = await f.api(`/v1/workflows/${workflow.id}`);
      expect(detail.states).toHaveLength(1);
      expect(detail.states[0]).not.toHaveProperty("goal");
      const exported = await f.api(`/v1/workflows/${workflow.id}/export`);
      expect(exported.workflow.snooze_duration_ms).toBe(600000);
      expect(exported.states[0]).not.toHaveProperty("goal");
      await page.evaluate(() => { window.location.hash = "#/workflows"; });
      await page.reload();
      await expect(page.getByText(workflow.name, { exact: true }).first()).toBeVisible();
    }
  });

  const durationCases = [
    { label: "omitted", tool: "workflow_snooze", args: { justification: "Waiting for external build" }, expected: 600000 },
    { label: "custom omitted", tool: "workflow_snooze", args: { justification: "Waiting for external build" }, expected: 900000, configured: 900000 },
    { label: "milliseconds", tool: "workflow_snooze", args: { justification: "Waiting for external build", duration_ms: 300000 }, expected: 300000 },
    { label: "minutes precedence", tool: "workflow_snooze", args: { justification: "Waiting for external build", duration_minutes: 2, duration_ms: 900000 }, expected: 120000 },
    { label: "nonpositive defaults", tool: "workflow_snooze", args: { justification: "Waiting for external build", duration_minutes: 0, duration_ms: -1 }, expected: 600000 },
    { label: "minimum", tool: "workflow_snooze", args: { justification: "Waiting for external build", duration_ms: 1 }, expected: 1000 },
    { label: "configured maximum", tool: "workflow_snooze", args: { justification: "Waiting for external build", duration_ms: 86400001 }, expected: 600000 },
    { label: "representable oversized minutes", tool: "workflow_snooze", args: { justification: "Waiting for external build", duration_minutes: 9007199254740991 }, expected: 600000 },
    { label: "equal maximum", tool: "workflow_snooze", args: { justification: "Waiting for external build", duration_ms: 600000 }, expected: 600000 },
    { label: "alias shorter", tool: "goal_snooze", args: { duration_ms: 120000 }, expected: 120000 },
    { label: "alias default", tool: "goal_snooze", args: {}, expected: 600000 },
    { label: "alias configured cap", tool: "goal_snooze", args: { duration_ms: 86400000 }, expected: 600000 },
  ];
  for (const c of durationCases) test(`SCN-WFIG-007: workflow snooze defaults and overrides: ${c.label}`, async ({ page }) => {
    f.replies.push({ tool: c.tool, args: c.args }, { answer: `Saved ${c.label}` });
    const { agent, workflow } = await createRunningWorkflow(f, page, "configured" in c ? c.configured : 600000);
    await expect.poll(() => settled(f, agent).length).toBe(1);
    const calls = f.rows("SELECT result_json FROM embedded_goal_snooze_calls WHERE agent_id=?", agent);
    expect(calls).toHaveLength(1);
    const result = JSON.parse(String(calls[0].result_json));
    expect(result.DurationMS).toBe(c.expected);
    expect(f.rows("SELECT snoozed_until FROM managed_agent_policies WHERE agent_id=?", agent)[0].snoozed_until).toBe(result.DueAt);
    const nativeResult = JSON.parse(lastToolOutput(f));
    expect(nativeResult.guidance).toMatch(/waiting on (the user|active external)|explicitly call mecha_blocked|call mecha_blocked instead/i);
    if (c.tool === "workflow_snooze") expect(nativeResult).toMatchObject({ ok: true, duration_ms: c.expected, workflow: workflow.name, snoozed_until: result.DueAt, snooze_count: 1 });
    else expect(nativeResult).toMatchObject({ DurationMS: c.expected, DueAt: result.DueAt });
    expect(result.DueAt).toBe(settled(f, agent)[0].due_at);
    const now = await f.advance(1);
    expect(Date.parse(result.DueAt) - Date.parse(now.now)).toBe(c.expected - 1);
    await expect(page.getByText(`Saved ${c.label}`, { exact: true }).first()).toBeVisible();
    expect(f.requests).toHaveLength(2);
  });

  for (const args of [
    { duration_minutes: "5" },
    { duration_ms: 1.5 },
    { duration_ms: null },
    { reason: 3 },
    { unexpected: true },
    { duration_minutes: 1e30 },
    {},
    { justification: "" },
    { justification: "   " },
    { justification: "wait" },
    { justification: null },
    { justification: 12345 },
    { justification: true },
  ]) {
    test(`SCN-WFIG-012: rejected native arguments preserve valid state ${JSON.stringify(args)}`, async ({ page }, info) => {
      let diagnosticAgent: string | undefined;
      try {
      f.replies.push({ tool: "workflow_snooze", args: { justification: "Waiting for external build", duration_minutes: 5 } }, { answer: "Valid wait saved" });
      const { agent } = await createRunningWorkflow(f, page);
      diagnosticAgent = agent;
      await expect.poll(() => settled(f, agent).length).toBe(1);
      const before = f.rows("SELECT desired_revision, goal_text, goal_enabled, snoozed_until FROM managed_agent_policies WHERE agent_id=?", agent);
      const binding = await f.api(`/v1/managed-agents/${agent}/workflow-state`);
      f.replies.push({ tool: "workflow_snooze", args }, { answer: "Invalid call handled" });
      await userTurn(page, "Exercise invalid snooze arguments");
      await expect.poll(() => settled(f, agent).length).toBe(2);
      expect(f.rows("SELECT desired_revision, goal_text, goal_enabled, snoozed_until FROM managed_agent_policies WHERE agent_id=?", agent)).toEqual(before);
      expect(f.rows("SELECT * FROM embedded_goal_snooze_preferences WHERE agent_id=?", agent)).toHaveLength(1);
      // The native result must carry denial, not merely a synthetic final answer.
      expect(lastToolOutput(f)).toMatch(/invalid|denied|must|unknown|argument|integer|schema mismatch|justification is required/i);
      expect(await f.api(`/v1/managed-agents/${agent}/workflow-state`)).toEqual(binding);
      await expect(page.getByText("Invalid call handled", { exact: true }).first()).toBeVisible();
      expect((await f.api(`/v1/managed-agents/${agent}/goal`)).goal_state.enabled).toBe(true);
      } catch (failure) {
        // Failure-only, before afterEach deletes the fixture. Never save auth,
        // system instructions, arbitrary text, HTML or logs. Unknown strings
        // retain hashes and synthetic text markers, not their contents.
        const markers = ["Valid wait saved", "Invalid call handled", "Exercise invalid snooze arguments"];
        const project = (value: unknown, depth = 0): unknown => {
          if (depth > 10) return { truncated: true };
          if (typeof value === "string") return {
            bytes: Buffer.byteLength(value), sha256: createHash("sha256").update(value).digest("hex"),
            markers: markers.filter(text => value.includes(text)),
            denial: /invalid|denied|must|unknown|argument|integer|schema mismatch/i.test(value),
          };
          if (value === null || typeof value === "number" || typeof value === "boolean") return value;
          if (Array.isArray(value)) return value.slice(-64).map(item => project(item, depth + 1));
          if (!value || typeof value !== "object") return null;
          const result: Record<string, unknown> = {};
          for (const key of ["input", "type", "role", "name", "call_id", "agent_id", "session_id", "epoch", "generation", "seq", "digest", "kind", "body", "content", "text", "output", "arguments", "items", "entries", "messages", "events", "data", "payload", "message", "message_content", "category", "sequence", "state", "user_seq", "final_seq", "final_digest", "final_body", "goal_revision", "continuation_state", "due_at", "desired_revision", "goal_enabled", "snoozed_until", "status"]) {
            if (key in value) result[key] = project(Reflect.get(value, key), depth + 1);
          }
          return result;
        };
        const evidence: Record<string, unknown> = { version: 1 };
        const capture = async (key: string, action: () => unknown | Promise<unknown>) => {
          try { evidence[key] = project(await action()); }
          catch { evidence[key] = { unavailable: true }; }
        };
        try {
          await capture("requests", () => f.requests.slice(-16).map(request => ({ input:
            Array.isArray(request.input) ? request.input.filter(item => item && typeof item === "object" &&
              (item.type === "function_call_output" || item.type === "function_call" || item.role === "user" || item.role === "assistant")) : [],
          })));
          if (diagnosticAgent) {
            const agent = diagnosticAgent;
            await capture("settled", () => settled(f, agent));
            await capture("finals", () => f.rows("SELECT seq, kind, digest, CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? AND kind='assistant' ORDER BY seq DESC LIMIT 16", agent));
            await capture("historyRows", () => f.rows("SELECT sequence, category, message_content FROM managed_complete_history WHERE agent_id=? ORDER BY sequence DESC LIMIT 64", agent));
            await capture("historyAPI", async () => {
              const response = await fetch(f.proxy.url + "/v1/history?" + new URLSearchParams({ agent_id: agent, view: "complete", limit: "64" }), { signal: AbortSignal.timeout(2000) });
              const reader = response.body?.getReader();
              if (!reader) return { status: response.status };
              const chunks: Buffer[] = [];
              let bytes = 0;
              try {
                for (;;) {
                  const { done, value } = await reader.read();
                  if (done) break;
                  bytes += value.byteLength;
                  if (bytes > 512 * 1024) return { status: response.status };
                  chunks.push(Buffer.from(value));
                }
                return { status: response.status, data: JSON.parse(Buffer.concat(chunks).toString("utf8")) };
              } finally { await reader.cancel(); }
            });
          }
          await capture("ws", () => f.wsFrames.slice(-64).map(frame => {
            try { return JSON.parse(frame); } catch { return null; }
          }));
          await capture("ui", () => page.locator("body").innerText({ timeout: 2000 }));
          const bounded: Record<string, unknown> = { version: 1 };
          for (const [key, value] of Object.entries(evidence)) {
            bounded[key] = value;
            if (Buffer.byteLength(JSON.stringify(bounded)) > 4 * 1024 * 1024 - 1024) bounded[key] = { truncated: true };
          }
          await info.attach("invalid-arguments-boundaries", { body: Buffer.from(JSON.stringify(bounded)), contentType: "application/json" });
        } catch { /* Diagnostics must never replace the original assertion failure. */ }
        throw failure;
      }
    });
  }


  for (const action of ["transition", "exit", "deactivate"]) test(`SCN-WFIG-003/005/010: workflow lifecycle changes retire snooze: ${action}`, async ({ page }) => {
    f.replies.push({ tool: "workflow_snooze", args: { justification: "Waiting for external build", duration_minutes: 5 } }, { answer: "Lifecycle wait saved" });
    const { agent, b } = await createRunningWorkflow(f, page);
    await expect.poll(() => settled(f, agent).length).toBe(1);
    let old = settled(f, agent)[0];
    const goal = (await f.api(`/v1/managed-agents/${agent}/goal`)).goal_state;
    const revision = f.rows("SELECT desired_revision FROM managed_agent_policies WHERE agent_id=?", agent)[0].desired_revision;
    if (action === "deactivate") {
      await f.api(`/v1/managed-agents/${agent}/workflow-state`, { action: "deactivate" }, "PUT");
    } else {
      f.replies.push({ tool: "mecha_workflow_transition", args: { action: "transition", target_state: "B" } });
      if (action === "exit") f.replies.push({ tool: "workflow_snooze", args: { justification: "Waiting for external build", duration_minutes: 5 } });
      f.replies.push({ answer: "Native B reached" });
      if (action === "transition") f.replies.push({ hold: true });
      await userTurn(page, "Advance natively to B");
      await expect.poll(async () => (await f.api(`/v1/managed-agents/${agent}/workflow-state`)).active_workflow_state_id).toBe(b.id);
      await expect(page.getByText("Native B reached", { exact: true }).first()).toBeVisible();
      expect((await f.api(`/v1/managed-agents/${agent}/goal`)).goal_state).toMatchObject({ enabled: true, goal: goal.goal });
      expect(f.rows("SELECT desired_revision FROM managed_agent_policies WHERE agent_id=?", agent)[0].desired_revision).toBe(revision);
      expect(JSON.stringify(f.requests.at(-1)!.input)).toContain("B guidance");
      if (action === "exit") {
        // Snooze in the transition's SDK turn, before any unsnoozed final
        // can legitimately admit autonomous B work and consume the next reply.
        await expect.poll(() => settled(f, agent).length).toBe(2);
        old = settled(f, agent).at(-1)!;
        expect(old.due_at).not.toBeNull();
        expect(f.rows("SELECT snoozed_until FROM managed_agent_policies WHERE agent_id=?", agent)[0].snoozed_until).toBe(old.due_at);
        f.replies.push({ tool: "mecha_workflow_transition", args: { action: "exit" } }, { answer: "Native exit complete" });
        await userTurn(page, "Exit natively");
        await expect(page.getByText("Native exit complete", { exact: true }).first()).toBeVisible();
      }
    }
    const policy = f.rows("SELECT snoozed_until FROM managed_agent_policies WHERE agent_id=?", agent)[0];
    expect(policy.snoozed_until == null || policy.snoozed_until === "").toBe(true);
    const retired = settled(f, agent).find(t => t.turn_key === old.turn_key)!;
    expect(retired.continuation_state).toBe("invalidated");
    if (action !== "transition") {
      expect((await f.api(`/v1/managed-agents/${agent}/goal`)).goal_state).toMatchObject({ enabled: false, goal: "" });
      const binding = await f.api(`/v1/managed-agents/${agent}/workflow-state`);
      expect(binding.active_workflow_id).toBeFalsy();
      expect(binding.active_workflow_state_id).toBeFalsy();

    }
    const now = await f.advance(1);
    await f.advance(Date.parse(String(old.due_at)) - Date.parse(now.now) + 1);
    expect(settled(f, agent).find(t => t.turn_key === old.turn_key)!.started_at).toBeNull();
    if (action !== "transition") {
      expect(f.replies).toHaveLength(0);
      f.replies.push({ hold: true });
      await f.api(`/v1/managed-agents/${agent}/goal`, { action: "set", goal: "Ordinary goal works again" }, "PUT");
      await f.api(`/v1/managed-agents/${agent}/goal`, { action: "off" }, "PUT");
    }
  });

  test("SCN-WFIG-002/012: implicit workflow goal rejects REST and native controls", async ({ page }) => {
    f.replies.push({ tool: "workflow_snooze", args: { justification: "Waiting for external build", duration_minutes: 5 } }, { answer: "Locked wait saved" });
    const { agent } = await createRunningWorkflow(f, page);
    await expect.poll(() => settled(f, agent).length).toBe(1);
    const before = f.rows("SELECT desired_revision, goal_text, goal_enabled, snoozed_until FROM managed_agent_policies WHERE agent_id=?", agent);
    const bindingBefore = await f.api(`/v1/managed-agents/${agent}/workflow-state`);
    const targetInstance = f.rows("SELECT pi_session_id FROM managed_agents WHERE agent_id=?", agent)[0].pi_session_id;
    const state = (await f.api(`/v1/managed-agents/${agent}/goal`)).goal_state;
    for (const action of ["set", "off"]) {
      const frameId = randomUUID();
      const controlId = randomUUID();
      await page.evaluate(frame => {
        const send = Reflect.get(window, "workflowSendControl");
        send(frame);
      }, { type: "goal_control", frame_id: frameId, control_id: controlId,
        target_agent_id: agent, target_instance_id: targetInstance, action,
        goal: action === "set" ? "Forbidden WS replacement" : "",
        settings: { snooze_enabled: state.snooze_enabled,
          snooze_duration_ms: state.snooze_duration_ms, snooze_turn_limit: state.snooze_turn_limit,
          snooze_budget_cap_ms: state.snooze_budget_cap_ms, snooze_refill_yield_ms: state.snooze_refill_yield_ms,
          snooze_refill_elapsed_ms: state.snooze_refill_elapsed_ms } });
      await expect.poll(() => f.wsFrames.map(text => JSON.parse(text)).find(frame => frame.request_id === frameId)).toMatchObject({
        type: "error", code: "WORKFLOW_ACTIVE", request_id: frameId,
      });
      expect(f.wsFrames.map(text => JSON.parse(text)).filter(frame => frame.type === "goal_control_result" && frame.control_id === controlId)).toHaveLength(0);
      expect(f.rows("SELECT desired_revision, goal_text, goal_enabled, snoozed_until FROM managed_agent_policies WHERE agent_id=?", agent)).toEqual(before);
      expect(await f.api(`/v1/managed-agents/${agent}/workflow-state`)).toEqual(bindingBefore);
      expect((await f.api(`/v1/managed-agents/${agent}/goal`)).goal_state).toMatchObject({ enabled: true, goal: state.goal });
    }
    for (const action of ["set", "off"]) {
      const res = await fetch(`${f.proxy.url}/v1/managed-agents/${agent}/goal`, {
        method: "PUT", headers: { "Content-Type": "application/json", "X-Request-ID": randomUUID() },
        body: JSON.stringify(action === "set" ? { action, goal: "Forbidden replacement" } : { action }),
      });
      expect(res.status).toBe(409);
      expect(await res.text()).toContain("manual goal control is disabled while workflow");
      const failures = f.rows("SELECT message_content FROM managed_complete_history WHERE agent_id=? AND message='Goal failed'", agent).length;
      f.replies.push({ tool: "goal", args: action === "set" ? { action, goal: "Forbidden native replacement" } : { action } });
      await userTurn(page, `Attempt native goal ${action}`);
      // Execute denial is presented before the unknown-outcome fence stops the
      // SDK turn. A fabricated final/model continuation would be incorrect.
      await expect.poll(() => f.rows("SELECT message_content FROM managed_complete_history WHERE agent_id=? AND message='Goal failed'", agent).length).toBe(failures + 1);
      const denial = f.rows("SELECT message_content FROM managed_complete_history WHERE agent_id=? AND message='Goal failed' ORDER BY sequence", agent).at(-1)!;
      expect(denial.message_content).toMatch(/manual goal control is disabled while workflow/);
      await expect(page.getByRole("button", { name: /^Goal failed/ }).first()).toBeVisible();
      expect(f.rows("SELECT desired_revision, goal_text, goal_enabled, snoozed_until FROM managed_agent_policies WHERE agent_id=?", agent)).toEqual(before);
    }
    for (const args of [{ action: "exit" }, { action: "transition", target_state: "Nonexistent" }]) {
      f.replies.push({ tool: "mecha_workflow_transition", args }, { answer: `Denied ${JSON.stringify(args)}` });
      await userTurn(page, "Attempt forbidden workflow operation");
      await expect(page.getByText(`Denied ${JSON.stringify(args)}`, { exact: true }).first()).toBeVisible();
      expect(lastToolOutput(f)).toMatch(/not allowed|not permitted|not found|invalid|denied/i);
      expect(await f.api(`/v1/managed-agents/${agent}/workflow-state`)).toEqual(bindingBefore);
      expect(f.rows("SELECT desired_revision, goal_text, goal_enabled, snoozed_until FROM managed_agent_policies WHERE agent_id=?", agent)).toEqual(before);
    }
  });


  test("SCN-SNOOZE-001/003/004: workflow_snooze tracks cumulative snooze count and escalates warnings", async ({ page }) => {
    f.replies.push(
      { tool: "workflow_snooze", args: { justification: "Waiting for GitHub Actions CI run #1042 to complete", duration_minutes: 5 } },
      { answer: "Snooze 1 completed" },
    );
    const { agent, workflow } = await createRunningWorkflow(f, page);
    await expect.poll(() => settled(f, agent).length).toBe(1);
    await expect(page.getByText("Snooze 1 completed", { exact: true }).first()).toBeVisible();

    // Snooze 1 output checks
    let output = JSON.parse(lastToolOutput(f));
    expect(output).toMatchObject({
      ok: true,
      workflow: workflow.name,
      snooze_count: 1,
    });
    expect(output.guidance).toContain("Workflow snooze count for this run: 1");
    expect(output.warning).toBeUndefined();

    // Snooze 2: advisory warning
    f.replies.push(
      { tool: "workflow_snooze", args: { justification: "Still waiting for remote deployment to roll out", duration_minutes: 5 } },
      { answer: "Snooze 2 completed" },
    );
    await userTurn(page, "Continue waiting on deployment");
    await expect.poll(() => settled(f, agent).length).toBe(2);
    await expect(page.getByText("Snooze 2 completed", { exact: true }).first()).toBeVisible();

    output = JSON.parse(lastToolOutput(f));
    expect(output).toMatchObject({
      ok: true,
      workflow: workflow.name,
      snooze_count: 2,
    });
    expect(output.warning).toContain("Advisory: Workflow snooze count for this run: 2");

    // Snooze 3: advisory warning
    f.replies.push(
      { tool: "workflow_snooze", args: { justification: "Awaiting database migration pipeline to complete", duration_minutes: 5 } },
      { answer: "Snooze 3 completed" },
    );
    await userTurn(page, "Continue waiting on migration");
    await expect.poll(() => settled(f, agent).length).toBe(3);
    await expect(page.getByText("Snooze 3 completed", { exact: true }).first()).toBeVisible();

    output = JSON.parse(lastToolOutput(f));
    expect(output.snooze_count).toBe(3);
    expect(output.warning).toContain("Advisory: Workflow snooze count for this run: 3");

    // Snooze 4: strong warning
    f.replies.push(
      { tool: "workflow_snooze", args: { justification: "Awaiting staging environment health check readiness", duration_minutes: 5 } },
      { answer: "Snooze 4 completed" },
    );
    await userTurn(page, "Continue waiting on health check");
    await expect.poll(() => settled(f, agent).length).toBe(4);
    await expect(page.getByText("Snooze 4 completed", { exact: true }).first()).toBeVisible();

    output = JSON.parse(lastToolOutput(f));
    expect(output.snooze_count).toBe(4);
    expect(output.warning).toContain("Warning: Workflow snooze count for this run: 4");
    expect(output.warning).toContain("Snooze is strictly prohibited as a stalling tactic");
  });

  test("SCN-WFIG-011: completed goals discard snooze through real external evaluation", async ({ page }) => {
    await f.open(page);
    const agent = `explicit-${randomUUID()}`;
    const launched = await f.api("/v1/managed-agents", { name: agent, cwd: `${f.router.root}/work`, runtime: "embedded", model });
    f.clock = Date.parse(launched.agent.created_at);
    await expect(page.locator(`[data-agent-id="${agent}"]`)).toBeVisible();
    await page.locator(`[data-agent-id="${agent}"]`).click();
    f.replies.push({ tool: "goal_snooze", args: { duration_ms: 30000 } }, { answer: "Explicit work waiting" },
      { answer: "VERDICT: NOT_DONE\nREASON: Still waiting", evaluation: true });
    // Enabling an ordinary goal legitimately kicks an idle worker. Queue the
    // exact responses before set, rather than racing that turn with user input.
    await f.api(`/v1/managed-agents/${agent}/goal`, { action: "set", goal: "Finish explicit fixture task" }, "PUT");
    await expect.poll(() => settled(f, agent).length).toBe(1);
    const initial = settled(f, agent)[0];
    expect(initial.state).toBe("NOT_DONE");
    const oldD = String(initial.due_at);
    expect(f.rows("SELECT snoozed_until FROM managed_agent_policies WHERE agent_id=?", agent)[0].snoozed_until).toBe(oldD);
    f.replies.push({ answer: "Explicit task completed" }, { answer: "VERDICT: COMPLETED\nREASON: Task completed", evaluation: true });
    await userTurn(page, "The task is now complete");
    await expect.poll(() => f.rows("SELECT state FROM embedded_goal_evaluations WHERE agent_id=? ORDER BY user_seq", agent).map(t => t.state)).toEqual(["NOT_DONE", "DONE"]);
    const done = f.rows(`SELECT e.*, r.turn_key FROM embedded_goal_evaluations e
      JOIN embedded_goal_applied_receipts r ON r.agent_id=e.agent_id AND r.session_id=e.session_id
        AND r.epoch=e.epoch AND r.generation=e.generation AND r.turn_entry_seq=e.user_seq
        AND r.turn_entry_digest=e.user_digest AND r.goal_revision=e.goal_revision
      JOIN embedded_entries final ON final.agent_id=e.agent_id AND final.session_id=e.session_id
        AND final.epoch=e.epoch AND final.generation=e.generation AND final.seq=e.final_seq AND final.digest=e.final_digest
      JOIN embedded_append_keys receipt ON receipt.agent_id=e.agent_id AND receipt.session_id=e.session_id
        AND receipt.epoch=e.epoch AND receipt.generation=e.generation AND receipt.entry_seq=e.final_seq
      WHERE e.agent_id=? ORDER BY e.user_seq`, agent);
    expect(done).toHaveLength(2);
    expect(done[1].turn_key).not.toBe(initial.turn_key);
    const policy = f.rows("SELECT snoozed_until FROM managed_agent_policies WHERE agent_id=?", agent)[0];
    expect(policy.snoozed_until == null || policy.snoozed_until === "").toBe(true);
    expect(f.rows("SELECT CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? AND kind='assistant'", agent).some(row => String(row.body).includes("Explicit task completed"))).toBe(true);
    await expect(page.getByRole("button", { name: "Set goal", exact: true })).toBeVisible();
    const now = await f.advance(1);
    await f.advance(Date.parse(oldD) - Date.parse(now.now) + 1);
    expect(f.requests).toHaveLength(5);
    expect(settled(f, agent)[0].started_at).toBeNull();
  });

  for (const tool of ["workflow_snooze", "goal_snooze"]) test(`SCN-WFIG-013: ${tool} accepted replacement deadlines persist without settlement restart`, async ({ page }) => {
    f.replies.push({ tool, args: tool === "workflow_snooze" ? { justification: "Waiting for external build", duration_ms: 300000 } : { duration_ms: 300000 } }, { answer: "Original D saved", advanceMS: 10000 });
    const { agent } = await createRunningWorkflow(f, page);
    await expect.poll(() => settled(f, agent).length).toBe(1);
    let previous = String(settled(f, agent)[0].due_at);
    expect(Date.parse(previous) - f.clock).toBe(290000);
    // Public calls replace both policy and preference. This is replacement
    // coverage, not proof of unequal policy/preference maximum resolution.
    for (const minutes of [8, 2]) {
      f.replies.push({ tool, args: tool === "workflow_snooze" ? { justification: "Waiting for external build", duration_ms: minutes * 60000 } : { duration_ms: minutes * 60000 } }, { answer: `Replacement ${minutes}`, advanceMS: 1000 });
      await userTurn(page, `Replace wait with ${minutes} minutes`);
      await expect(page.getByText(`Replacement ${minutes}`, { exact: true }).first()).toBeVisible();
      await expect.poll(() => String(settled(f, agent).at(-1)!.due_at)).not.toBe(previous);
      const current = String(settled(f, agent).at(-1)!.due_at);
      expect(Date.parse(current) - f.clock).toBe(minutes * 60000 - 1000);
      expect(Date.parse(current) > Date.parse(previous)).toBe(minutes === 8);
      expect(f.rows("SELECT snoozed_until FROM managed_agent_policies WHERE agent_id=?", agent)[0].snoozed_until).toBe(current);
      const count = settled(f, agent).length;
      const priorIdentity = settled(f, agent).at(-1)!.turn_key;
      f.replies.push({ answer: `Replacement ${minutes} steering` });
      await userTurn(page, "Acknowledge without moving the saved deadline");
      await expect(page.getByText(`Replacement ${minutes} steering`, { exact: true }).first()).toBeVisible();
      await expect.poll(() => settled(f, agent).length).toBe(count + 1);
      expect(settled(f, agent).at(-1)!.turn_key).not.toBe(priorIdentity);
      expect(settled(f, agent).at(-1)!.state).toBe("NOT_DONE");
      expect(settled(f, agent).at(-1)!.due_at).toBe(current);
      previous = current;
    }
  });

  for (const tool of ["workflow_snooze", "goal_snooze"]) {
    for (const sameTurn of [false, true]) test(`SCN-WFIG-010/013: ${tool} shorter replacement admits at short D (${sameTurn ? "same SDK turn transition" : "new user turn and idle recovery"})`, async ({ page }) => {
      f.replies.push({ tool, args: tool === "workflow_snooze" ? { justification: "Waiting for external build", duration_ms: 300000 } : { duration_ms: 300000 } });
      if (sameTurn) f.replies.push(
        { tool: "mecha_workflow_transition", args: { action: "transition", target_state: "B" } },
        { tool, args: tool === "workflow_snooze" ? { justification: "Waiting for external build", duration_ms: 30000 } : { duration_ms: 30000 } },
      );
      f.replies.push({ answer: sameTurn ? "B short wait saved" : "A long wait saved" });
      const { agent, b } = await createRunningWorkflow(f, page);
      await expect.poll(() => settled(f, agent).length).toBe(1);
      const originalRevision = f.rows("SELECT desired_revision FROM managed_agent_policies WHERE agent_id=?", agent)[0].desired_revision;
      if (!sameTurn) {
        f.replies.push({ tool, args: tool === "workflow_snooze" ? { justification: "Waiting for external build", duration_ms: 30000 } : { duration_ms: 30000 } }, { answer: "Short replacement saved" });
        await userTurn(page, "Replace the old long wait with thirty seconds");
        await expect.poll(() => settled(f, agent).length).toBe(2);
      }
      const calls = f.rows("SELECT result_json FROM embedded_goal_snooze_calls WHERE agent_id=? ORDER BY rowid", agent).map(row => JSON.parse(String(row.result_json)));
      expect(calls).toHaveLength(2);
      expect(calls[0].DurationMS).toBe(300000);
      expect(calls[1].DurationMS).toBe(30000);
      expect(Date.parse(calls[1].DueAt)).toBeLessThan(Date.parse(calls[0].DueAt));
      expect(calls[0].UserSeq === calls[1].UserSeq).toBe(sameTurn);
      const current = settled(f, agent).at(-1)!;
      expect(current.state).toBe("NOT_DONE");
      expect(current.continuation_state).toBe("reserved");
      expect(current.due_at).toBe(calls[1].DueAt);
      expect(f.rows("SELECT snoozed_until FROM managed_agent_policies WHERE agent_id=?", agent)[0].snoozed_until).toBe(calls[1].DueAt);
      const active = f.rows("SELECT * FROM embedded_goal_snooze_preferences WHERE agent_id=? AND active=1", agent);
      expect(active).toHaveLength(1);
      expect(active[0].due_at).toBe(calls[1].DueAt);
      expect(f.rows("SELECT desired_revision FROM managed_agent_policies WHERE agent_id=?", agent)[0].desired_revision).toBe(originalRevision);
      if (sameTurn) expect((await f.api(`/v1/managed-agents/${agent}/workflow-state`)).active_workflow_state_id).toBe(b.id);
      await expect(page.getByText(sameTurn ? "B short wait saved" : "Short replacement saved", { exact: true }).first()).toBeVisible();
      const before = f.requests.length;
      // The shared clock runs the real timer and idle/recovery sweep. Assert
      // actual admission at short D, not just a correctly reported deadline.
      await f.advance(Date.parse(calls[1].DueAt) - f.clock - 1);
      expect(f.requests).toHaveLength(before);
      expect(settled(f, agent).at(-1)!.started_at).toBeNull();
      f.replies.push({ hold: true });
      await f.advance(1);
      await expect.poll(() => f.requests.length).toBe(before + 1);
      await expect.poll(() => settled(f, agent).at(-1)!.continuation_state).toBe("started");
      const admitted = settled(f, agent).at(-1)!;
      expect(Date.parse(String(admitted.started_at))).toBe(Date.parse(calls[1].DueAt));
      for (let i = 0; i < 3; i++) {
        await f.advance(1);
        expect(f.requests).toHaveLength(before + 1);
        expect(settled(f, agent).at(-1)!.started_at).toBe(admitted.started_at);
      }
    });
  }

  for (const tool of ["workflow_snooze", "goal_snooze"]) test(`SCN-WFIG-013: ${tool} saved call-clock deadline and expired-policy pacing`, async ({ page }) => {
    f.replies.push({ tool, args: tool === "workflow_snooze" ? { justification: "Waiting for external build", duration_ms: 30000 } : { duration_ms: 30000 } }, { answer: "Delayed final after expiry", advanceMS: 31000 }, { hold: true });
    const { agent } = await createRunningWorkflow(f, page);
    await expect.poll(() => settled(f, agent).length).toBe(1);
    const call = JSON.parse(String(f.rows("SELECT result_json FROM embedded_goal_snooze_calls WHERE agent_id=?", agent)[0].result_json));
    expect(Date.parse(call.DueAt)).toBe(f.clock - 1000);
    expect(f.rows("SELECT snoozed_until FROM managed_agent_policies WHERE agent_id=?", agent)[0].snoozed_until).toBe(call.DueAt);
    expect(settled(f, agent)[0].state).toBe("NOT_DONE");
    await expect(page.getByText("Delayed final after expiry", { exact: true }).first()).toBeVisible();
    // Expired D imposes no new 30s hold. Idle dispatch may admit immediately;
    // scheduled dispatch otherwise uses the existing normal 10s pacing.
    expect(settled(f, agent)[0].due_at).toBeNull();
    await f.advance(10000);
    await expect.poll(() => f.requests.length).toBe(3);
    expect(settled(f, agent)[0].continuation_state).toBe("started");
    for (let i = 0; i < 3; i++) await f.advance(1);
    expect(f.requests).toHaveLength(3);
  });

});
