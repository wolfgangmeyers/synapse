import { test, expect } from "@playwright/test";
import { setupE2EHarness, type E2ETestHarness } from "./harness";
import { WebSocket } from "ws";
import { DatabaseSync } from "node:sqlite";

test.describe("Background Workflows & Scheduled Execution E2E", () => {
  let harness: E2ETestHarness;

  test.beforeAll(async () => {
    harness = await setupE2EHarness({ useProxy: true, managed: true });
  });

  test.afterAll(async () => {
    if (harness) {
      await harness.close();
    }
  });

  test("Scenario 1 & 2: Dynamic Background Workflow Launch and Missing Required Variables Validation", async ({
    page,
  }) => {
    const targetRouterUrl = harness.proxy ? harness.proxy.url : harness.router.httpUrl;

    // 1. Create a Workflow with required and optional variables and an entry state
    const wfStateRes = await fetch(`${targetRouterUrl}/v1/workflow-states`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        name: "Audit State",
        description: "Auditing repo {{REPOSITORY}} on branch {{BRANCH}}",
        entry_text: "Run security and code audits on {{REPOSITORY}}",
        goal: "Complete code audit for {{REPOSITORY}}",
      }),
    });
    expect(wfStateRes.status).toBe(201);
    const wfStateData = await wfStateRes.json();
    const entryStateId = wfStateData.workflow_state.id;

    const wfRes = await fetch(`${targetRouterUrl}/v1/workflows`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        name: "Code Audit",
        description: "Autonomous code audit workflow",
        entry_state_id: entryStateId,
        variables: [
          {
            name: "REPOSITORY",
            label: "Repository",
            type: "text",
            required: true,
          },
          {
            name: "BRANCH",
            label: "Branch",
            type: "text",
            required: false,
            default_value: "main",
          },
        ],
      }),
    });
    expect(wfRes.status).toBe(201);
    const wfData = await wfRes.json();
    const workflowId = wfData.id;

    // --- Scenario 2: Launch workflow with missing required variables -> 400 Bad Request ---
    const badRunRes = await fetch(`${targetRouterUrl}/v1/workflows/${workflowId}/run`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        variables: {},
      }),
    });
    expect(badRunRes.status).toBe(400);
    const badRunData = await badRunRes.json();
    expect(badRunData.error).toContain("missing required workflow parameter(s): REPOSITORY");

    // Also test whitespace-only parameter
    const blankRunRes = await fetch(`${targetRouterUrl}/v1/workflows/${workflowId}/run`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        variables: { REPOSITORY: "   " },
      }),
    });
    expect(blankRunRes.status).toBe(400);

    // --- Scenario 1: Launch background workflow with valid variables -> HTTP 201 and is_background = 1 ---
    const runRes = await fetch(`${targetRouterUrl}/v1/workflows/${workflowId}/run`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        variables: { REPOSITORY: "mecha-developer", BRANCH: "feature-bg" },
      }),
    });
    expect(runRes.status).toBe(201);
    const runData = await runRes.json();
    expect(runData.agent_id).toMatch(/^bg-wf-/);
    expect(runData.workflow_id).toBe(workflowId);

    // Verify in SQLite database that is_background == 1
    const db = new DatabaseSync(harness.router.databasePath);
    const agentRow = db
      .prepare("SELECT agent_id, is_background, active_workflow_id FROM managed_agents WHERE agent_id = ?")
      .get(runData.agent_id) as { agent_id: string; is_background: number; active_workflow_id: string } | undefined;
    expect(agentRow).toBeDefined();
    expect(agentRow?.is_background).toBe(1);
    expect(agentRow?.active_workflow_id).toBe(workflowId);

    // Connect standard agent to verify tab separation in UI
    const stdAgentId = "std-worker-1";
    const agentWs = new WebSocket(`${targetRouterUrl.replace(/^http/, "ws")}/v1/agents`);
    await new Promise<void>((resolve, reject) => {
      agentWs.on("open", () => {
        agentWs.send(
          JSON.stringify({
            version: "v1",
            frame_id: "agent-reg-std",
            type: "register",
            agent_id: stdAgentId,
            instance_id: "inst-" + Date.now(),
            agent_role: "agent",
            client_type: "test",
            ack_confirmation: "correlated_v1",
            conversation_sync: "v1",
            delivery_receipts: "delivery_items_v1",
          })
        );
      });
      agentWs.on("message", (data) => {
        try {
          const parsed = JSON.parse(data.toString());
          if (parsed.type === "registered") resolve();
        } catch {}
      });
      agentWs.on("error", reject);
    });

    // Navigate to UI and verify tab separation
    page.on("console", (msg) => console.log("PAGE CONSOLE:", msg.type(), msg.text()));
    page.on("pageerror", (err) => console.log("PAGE ERROR:", err));

    await page.goto(harness.frontend.url);
    await page.evaluate(
      ({ routerUrl }) => {
        localStorage.setItem("mecha.web.activeRouter", routerUrl);
        localStorage.setItem("mecha.web.routers", JSON.stringify([routerUrl]));
      },
      { routerUrl: targetRouterUrl }
    );
    await page.reload();

    // Debug: fetch /v1/managed-agents from page context
    const managedAgentsData = await page.evaluate(async (routerUrl) => {
      try {
        const res = await fetch(`${routerUrl}/v1/managed-agents`);
        return { status: res.status, body: await res.json() };
      } catch (err: any) {
        return { error: err.message };
      }
    }, targetRouterUrl);
    console.log("PAGE EVALUATE /v1/managed-agents:", JSON.stringify(managedAgentsData, null, 2));

    // Verify "Agents" tab shows standard worker but NOT the background worker
    const agentsTab = page.locator('button[role="tab"]:has-text("Agents")');
    await expect(agentsTab).toBeVisible({ timeout: 5000 });
    await agentsTab.click();

    await expect(page.locator(`[data-agent-id="${stdAgentId}"]`)).toBeVisible({ timeout: 5000 });
    await expect(page.locator(`[data-agent-id="${runData.agent_id}"]`)).toHaveCount(0);

    // Switch to "Background" tab and verify the background worker is displayed
    const bgTab = page.locator('button[role="tab"]:has-text("Background")');
    await expect(bgTab).toBeVisible();
    await bgTab.click();

    await expect(page.locator(`[data-agent-id="${runData.agent_id}"]`)).toBeVisible({ timeout: 5000 });
    await expect(page.locator(`[data-agent-id="${stdAgentId}"]`)).toHaveCount(0);

    agentWs.close();
    db.close();
  });

  test("Scenario 3: Create, list, edit, and trigger a workflow schedule via /v1/workflow-schedules", async ({
    page,
  }) => {
    const targetRouterUrl = harness.proxy ? harness.proxy.url : harness.router.httpUrl;

    // 1. Fetch available workflows or create one if none exist
    let wfsRes = await fetch(`${targetRouterUrl}/v1/workflows`);
    let wfs = await wfsRes.json();
    let targetWf = wfs[0];
    if (!targetWf) {
      const createWfRes = await fetch(`${targetRouterUrl}/v1/workflows`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          name: "Schedule Workflow",
          description: "Workflow for schedule testing",
          variables: [{ name: "REPOSITORY", label: "Repository", type: "text", required: true }],
        }),
      });
      targetWf = await createWfRes.json();
    }
    expect(targetWf).toBeDefined();

    // 2. Attempt to create schedule with missing required variables -> 400 Bad Request
    const badSchedRes = await fetch(`${targetRouterUrl}/v1/workflow-schedules`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        workflow_id: targetWf.id,
        name: "Nightly Audit",
        schedule: {
          days_mode: "every_day",
          time_mode: "repeating_interval",
          interval: { value: 1, unit: "hours" },
        },
        variables: {},
      }),
    });
    expect(badSchedRes.status).toBe(400);

    // 3. Create schedule with valid required variables and structured schedule -> 201 Created
    const createSchedRes = await fetch(`${targetRouterUrl}/v1/workflow-schedules`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        workflow_id: targetWf.id,
        name: "Nightly Audit",
        schedule: {
          days_mode: "every_day",
          time_mode: "repeating_interval",
          interval: { value: 1, unit: "hours" },
        },
        variables: { REPOSITORY: "mecha-developer" },
      }),
    });
    expect(createSchedRes.status).toBe(201);
    const createdSched = (await createSchedRes.json()).schedule;
    expect(createdSched.id).toBeDefined();
    expect(createdSched.name).toBe("Nightly Audit");
    expect(createdSched.schedule.time_mode).toBe("repeating_interval");
    expect(createdSched.schedule.interval.value).toBe(1);

    // 4. List workflow schedules via REST API
    const listRes = await fetch(`${targetRouterUrl}/v1/workflow-schedules`);
    expect(listRes.status).toBe(200);
    const listData = await listRes.json();
    const found = listData.schedules.find((s: any) => s.id === createdSched.id);
    expect(found).toBeDefined();

    // 5. Edit workflow schedule via PATCH with structured schedule updates
    const editRes = await fetch(`${targetRouterUrl}/v1/workflow-schedules/${createdSched.id}`, {
      method: "PATCH",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        name: "Hourly Audit Sweep",
        schedule: {
          days_mode: "every_day",
          time_mode: "repeating_interval",
          interval: { value: 2, unit: "hours" },
        },
      }),
    });
    expect(editRes.status).toBe(200);
    const updatedSched = (await editRes.json()).schedule;
    expect(updatedSched.name).toBe("Hourly Audit Sweep");
    expect(updatedSched.schedule.interval.value).toBe(2);

    // 6. Test clock advancement trigger: advance the clock to trigger schedule execution
    const advanceResult = await harness.advanceClock(2 * 60 * 60 * 1000 + 1000);
    expect(advanceResult.advanced_ms).toBeGreaterThan(0);

    // Verify the schedule was executed and advanced
    const postAdvanceRes = await fetch(`${targetRouterUrl}/v1/workflow-schedules/${createdSched.id}`);
    expect(postAdvanceRes.status).toBe(200);
    const postAdvanceSched = (await postAdvanceRes.json()).schedule;
    expect(postAdvanceSched.last_run_at).not.toBeNull();
    expect(postAdvanceSched.last_run_status).toBe("success");

    // 7. Open UI and verify Workflow Schedules Modal displays the schedule in the list view
    await page.goto(harness.frontend.url);
    await page.evaluate(
      ({ routerUrl }) => {
        localStorage.setItem("mecha.web.activeRouter", routerUrl);
        localStorage.setItem("mecha.web.routers", JSON.stringify([routerUrl]));
      },
      { routerUrl: targetRouterUrl }
    );
    await page.reload();

    // Open managed actions dropdown
    const trigger = page.locator(".managed-actions-trigger");
    await expect(trigger).toBeVisible({ timeout: 5000 });
    await trigger.click();

    // Click "Workflow Schedules" menuitem
    const schedulesMenuItem = page.locator('button[role="menuitem"]:has-text("Workflow Schedules")');
    await expect(schedulesMenuItem).toBeVisible({ timeout: 5000 });
    await schedulesMenuItem.click();

    // Verify modal is displayed with schedule in list view
    const modal = page.locator("dialog.workflow-schedules-dialog");
    await expect(modal).toBeVisible({ timeout: 5000 });
    await expect(modal.locator(".dialog-title")).toContainText("Workflow Schedules");
    await expect(modal.locator("text=Hourly Audit Sweep")).toBeVisible({ timeout: 5000 });
  });

  test("SCN-001 & SCN-002: Specific Days and Specific Times schedule fires and UI creation workflow via Workflows window", async ({
    page,
  }) => {
    const targetRouterUrl = harness.proxy ? harness.proxy.url : harness.router.httpUrl;

    // 1. Create a dedicated workflow for UI testing and schedule firing
    const wfStateRes = await fetch(`${targetRouterUrl}/v1/workflow-states`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        name: "Morning Health Entry",
        description: "Entry state for daily health sweeps",
        entry_text: "Execute health check for {{REPO}}",
        goal: "Health check complete",
      }),
    });
    expect(wfStateRes.status).toBe(201);
    const entryState = (await wfStateRes.json()).workflow_state;

    const wfRes = await fetch(`${targetRouterUrl}/v1/workflows`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        name: "Daily Health Sweep",
        description: "Scheduled health sweep workflow",
        entry_state_id: entryState.id,
        variables: [{ name: "REPO", label: "Repository Name", type: "text", required: true }],
      }),
    });
    expect(wfRes.status).toBe(201);
    const wf = await wfRes.json();

    // 2. Open Workflows window -> Schedules tab -> click "+ Add Schedule"
    await page.goto(harness.frontend.url);
    await page.evaluate(
      ({ routerUrl }) => {
        localStorage.setItem("mecha.web.activeRouter", routerUrl);
        localStorage.setItem("mecha.web.routers", JSON.stringify([routerUrl]));
      },
      { routerUrl: targetRouterUrl }
    );
    await page.reload();

    // Open managed actions dropdown and click "Workflows"
    const trigger = page.locator(".managed-actions-trigger");
    await expect(trigger).toBeVisible({ timeout: 5000 });
    await trigger.click();

    const workflowsMenuItem = page.locator('button[role="menuitem"]:has-text("Workflows")');
    await expect(workflowsMenuItem).toBeVisible({ timeout: 5000 });
    await workflowsMenuItem.click();

    // Verify Workflows page is loaded
    await expect(page.locator(".workflows-page")).toBeVisible({ timeout: 5000 });

    // Click "Schedules" tab in brand-header-tabs
    const schedulesTab = page.locator('.brand-header-tab:has-text("Schedules")');
    await expect(schedulesTab).toBeVisible({ timeout: 5000 });
    await schedulesTab.click();

    // Verify WorkflowSchedulesView is rendered and click "+ Add Schedule"
    const addScheduleBtn = page.locator('button:has-text("+ Add Schedule")');
    await expect(addScheduleBtn).toBeVisible({ timeout: 5000 });
    await addScheduleBtn.click();

    // Verify Add Schedule form is displayed
    await expect(page.locator("text=Add Schedule")).toBeVisible({ timeout: 5000 });

    // Fill in Schedule Name
    await page.fill("#schedule-name", "Weekday Morning Health Sweep");

    // Select Target Workflow
    const workflowSelectBtn = page.locator(".model-picker button.model-current");
    await expect(workflowSelectBtn).toBeVisible({ timeout: 5000 });
    await workflowSelectBtn.click();

    const targetOption = page.locator(`.model-options button:has-text("${wf.name}")`);
    await expect(targetOption).toBeVisible({ timeout: 5000 });
    await targetOption.click();

    // Interact with StructuredSchedulePicker:
    // Select "Specific days"
    const specificDaysBtn = page.locator('button.segmented-toggle-btn:has-text("Specific days")');
    await expect(specificDaysBtn).toBeVisible({ timeout: 5000 });
    await specificDaysBtn.click();

    // Click "Weekdays" preset
    const weekdaysPresetBtn = page.locator('.specific-days-controls button:has-text("Weekdays")');
    await expect(weekdaysPresetBtn).toBeVisible({ timeout: 5000 });
    await weekdaysPresetBtn.click();

    // Select "At specific times"
    const specificTimesBtn = page.locator('button.segmented-toggle-btn:has-text("At specific times")');
    await expect(specificTimesBtn).toBeVisible({ timeout: 5000 });
    await specificTimesBtn.click();

    // Set time input to "08:30"
    const timeInput = page.locator('.specific-times-container input[type="time"]');
    await expect(timeInput).toBeVisible({ timeout: 5000 });
    await timeInput.fill("08:30");

    // Verify live summary chip reflects the plain-English summary
    const summaryChip = page.locator(".schedule-summary-chip");
    await expect(summaryChip).toContainText("Runs Monday through Friday at 08:30 AM");

    // Fill in required variable REPO
    const repoVarInput = page.locator("#var-REPO");
    await expect(repoVarInput).toBeVisible({ timeout: 5000 });
    await repoVarInput.fill("reflexivity/mecha-developer");

    // Submit form: click "Create Schedule"
    const submitBtn = page.locator('button[type="submit"]:has-text("Create Schedule")');
    await expect(submitBtn).toBeVisible({ timeout: 5000 });
    await submitBtn.click();

    // Verify it transitions back to list view and displays the created schedule with plain-English summary
    await expect(page.locator("strong.mcp-server-name:has-text('Weekday Morning Health Sweep')")).toBeVisible({ timeout: 5000 });
    await expect(page.locator("text=Runs Monday through Friday at 08:30 AM")).toBeVisible({ timeout: 5000 });

    // Fetch the created schedule via API to get ID and next_run_at
    const schedListRes = await fetch(`${targetRouterUrl}/v1/workflow-schedules`);
    expect(schedListRes.status).toBe(200);
    const schedListData = await schedListRes.json();
    const createdSched = schedListData.schedules.find(
      (s: any) => s.name === "Weekday Morning Health Sweep"
    );
    expect(createdSched).toBeDefined();
    expect(createdSched.next_run_at).toBeDefined();

    // 3. Advance clock to next_run_at time and verify schedule fires and spawns background worker
    const nextRunTime = new Date(createdSched.next_run_at).getTime();
    // Advance clock past next_run_at (up to 7 days + 1 minute if needed to hit the next designated clock time)
    const advanceRes = await harness.advanceClock(7 * 24 * 60 * 60 * 1000);
    expect(advanceRes.advanced_ms).toBeGreaterThan(0);

    // Verify schedule executed
    const verifySchedRes = await fetch(`${targetRouterUrl}/v1/workflow-schedules/${createdSched.id}`);
    expect(verifySchedRes.status).toBe(200);
    const verifySched = (await verifySchedRes.json()).schedule;
    expect(verifySched.last_run_at).not.toBeNull();
    expect(verifySched.last_run_status).toBe("success");

    // Verify background worker was spawned
    const agentsRes = await fetch(`${targetRouterUrl}/v1/managed-agents`);
    expect(agentsRes.status).toBe(200);
    const agentsData = await agentsRes.json();
    const spawnedBgAgent = agentsData.agents.find(
      (a: any) => a.is_background === true && a.active_workflow_id === wf.id
    );
    expect(spawnedBgAgent).toBeDefined();
    expect(spawnedBgAgent.agent_id).toMatch(/^bg-wf-/);
  });

  test("Scenario 4: Background tab displays active background workers followed by completed workers without a segmented toggle", async ({
    page,
  }) => {
    const targetRouterUrl = harness.proxy ? harness.proxy.url : harness.router.httpUrl;
    const db = new DatabaseSync(harness.router.databasePath);

    // Seed two background workers directly in DB: one active/running, one completed/terminated
    const activeBgId = "bg-worker-active-1";
    const completedBgId = "bg-worker-completed-1";
    const nowIso = new Date().toISOString();

    const sha256Hex = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08";

    db.prepare(`
      INSERT INTO managed_agents (
        agent_id, name, cwd, selected_model_provider, selected_model_id,
        desired_state, lifecycle_state, activity_state, runtime, session_epoch,
        manifest_revision, manifest_hash, created_at, updated_at, is_background
      ) VALUES (?, ?, '/tmp', 'google', 'gemini-3.8-flash', 'running', 'running', 'active', 'embedded', 1, 1, ?, ?, ?, 1)
    `).run(activeBgId, activeBgId, sha256Hex, nowIso, nowIso);

    db.prepare(`
      INSERT INTO managed_agents (
        agent_id, name, cwd, selected_model_provider, selected_model_id,
        desired_state, lifecycle_state, activity_state, runtime, session_epoch,
        manifest_revision, manifest_hash, created_at, updated_at, is_background
      ) VALUES (?, ?, '/tmp', 'google', 'gemini-3.8-flash', 'terminated', 'terminated', 'stopped', 'embedded', 1, 1, ?, ?, ?, 1)
    `).run(completedBgId, completedBgId, sha256Hex, nowIso, nowIso);

    // Bump projection version
    db.prepare("UPDATE managed_settings SET projection_version = projection_version + 1 WHERE singleton_id = 1").run();
    db.close();

    // Load page
    await page.goto(harness.frontend.url);
    await page.evaluate(
      ({ routerUrl }) => {
        localStorage.setItem("mecha.web.activeRouter", routerUrl);
        localStorage.setItem("mecha.web.routers", JSON.stringify([routerUrl]));
      },
      { routerUrl: targetRouterUrl }
    );
    await page.reload();

    // Click Background tab
    const bgTab = page.locator('button[role="tab"]:has-text("Background")');
    await expect(bgTab).toBeVisible({ timeout: 5000 });
    await bgTab.click();

    // Assert that segmented toggle control does NOT exist in the background tab
    const segmentedToggle = page.locator('.segmented-toggle-group[aria-label="Background worker state"]');
    await expect(segmentedToggle).toHaveCount(0);

    // Both active worker and completed worker are displayed together
    await expect(page.locator(`[data-agent-id="${activeBgId}"]`)).toBeVisible({ timeout: 5000 });
    await expect(page.locator(`[data-agent-id="${completedBgId}"]`)).toBeVisible({ timeout: 5000 });

    // Verify ordering: active worker comes before completed worker
    const listItems = page.locator(".sidebar-content [data-agent-id]");
    const activeEl = page.locator(`.sidebar-content [data-agent-id="${activeBgId}"]`);
    const completedEl = page.locator(`.sidebar-content [data-agent-id="${completedBgId}"]`);
    await expect(activeEl).toBeVisible();
    await expect(completedEl).toBeVisible();

    const allIds = await listItems.evaluateAll((elements) =>
      elements.map((el) => el.getAttribute("data-agent-id"))
    );
    const activeIndex = allIds.indexOf(activeBgId);
    const completedIndex = allIds.indexOf(completedBgId);
    expect(activeIndex).toBeGreaterThanOrEqual(0);
    expect(completedIndex).toBeGreaterThanOrEqual(0);
    expect(activeIndex).toBeLessThan(completedIndex);
  });

  test("Scenario 5: Verify 1-hour retention policy / pruning behavior for background workers", async () => {
    const db = new DatabaseSync(harness.router.databasePath);

    // Insert an old terminated background worker (terminated 65 minutes ago)
    const oldTerminatedBgId = "bg-worker-old-terminated";
    const recentTerminatedBgId = "bg-worker-recent-terminated";
    const sixtyFiveMinAgo = new Date(Date.now() - 65 * 60 * 1000).toISOString();
    const tenMinAgo = new Date(Date.now() - 10 * 60 * 1000).toISOString();

    db.prepare(`
      INSERT INTO managed_agents (
        agent_id, name, cwd, selected_model_provider, selected_model_id,
        desired_state, lifecycle_state, activity_state, runtime, session_epoch,
        manifest_revision, manifest_hash, created_at, updated_at, is_background
      ) VALUES (?, ?, '/tmp', 'google', 'gemini-3.8-flash', 'terminated', 'terminated', 'stopped', 'embedded', 1, 1, 'hash', ?, ?, 1)
    `).run(oldTerminatedBgId, oldTerminatedBgId, sixtyFiveMinAgo, sixtyFiveMinAgo);

    db.prepare(`
      INSERT INTO managed_agents (
        agent_id, name, cwd, selected_model_provider, selected_model_id,
        desired_state, lifecycle_state, activity_state, runtime, session_epoch,
        manifest_revision, manifest_hash, created_at, updated_at, is_background
      ) VALUES (?, ?, '/tmp', 'google', 'gemini-3.8-flash', 'terminated', 'terminated', 'stopped', 'embedded', 1, 1, 'hash', ?, ?, 1)
    `).run(recentTerminatedBgId, recentTerminatedBgId, tenMinAgo, tenMinAgo);

    // Verify both exist before pruning
    const beforeOld = db.prepare("SELECT agent_id FROM managed_agents WHERE agent_id = ?").get(oldTerminatedBgId);
    const beforeRecent = db.prepare("SELECT agent_id FROM managed_agents WHERE agent_id = ?").get(recentTerminatedBgId);
    expect(beforeOld).toBeDefined();
    expect(beforeRecent).toBeDefined();

    // Execute pruning query matching store.PruneExpiredBackgroundAgents (1 hour threshold)
    const cutoff = new Date(Date.now() - 60 * 60 * 1000).toISOString();
    const result = db
      .prepare(
        "DELETE FROM managed_agents WHERE is_background = 1 AND (lifecycle_state = 'terminated' OR desired_state = 'terminated') AND updated_at < ?"
      )
      .run(cutoff);

    expect(result.changes).toBeGreaterThanOrEqual(1);

    // Verify the 65-min old worker was pruned while the 10-min old worker remains intact
    const afterOld = db.prepare("SELECT agent_id FROM managed_agents WHERE agent_id = ?").get(oldTerminatedBgId);
    const afterRecent = db.prepare("SELECT agent_id FROM managed_agents WHERE agent_id = ?").get(recentTerminatedBgId);
    expect(afterOld).toBeUndefined();
    expect(afterRecent).toBeDefined();

    db.close();
  });
});
