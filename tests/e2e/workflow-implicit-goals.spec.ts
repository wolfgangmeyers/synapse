import { test, expect } from "@playwright/test";
import { setupE2EHarness, type E2ETestHarness } from "./harness";

test.describe("Workflow Implicit Goals & State Modal E2E", () => {
  let harness: E2ETestHarness;

  test.beforeAll(async () => {
    harness = await setupE2EHarness({ useProxy: true, managed: true });
  });

  test.afterAll(async () => {
    if (harness) {
      await harness.close();
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
    expect(goalData.goal_state.goal_lock_enabled).toBe(true);
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
    expect(goalAfterData.goal_state.goal_lock_enabled).toBe(true);

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
    expect(goalAfterExitData.goal_state.goal_lock_enabled).toBe(false);
  });
});
