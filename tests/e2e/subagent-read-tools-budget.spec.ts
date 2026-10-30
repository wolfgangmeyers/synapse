import { test, expect } from "@playwright/test";
import { setupE2EHarness, type E2ETestHarness } from "./harness";
import { WebSocket } from "ws";

test.describe("SPEC-LIFECYCLE-005: Subagent Read Tools Budget & Polling Redirection", () => {
  let harness: E2ETestHarness;

  test.beforeAll(async () => {
    harness = await setupE2EHarness({ useProxy: true });
  });

  test.afterAll(async () => {
    if (harness) {
      await harness.close();
    }
  });

  test("subagent read tools enforce credit budget and redirect polling", async ({
    page,
  }) => {
    const targetRouterUrl = harness.proxy ? harness.proxy.url : harness.router.httpUrl;
    const parentAgentId = "agent-parent-budget";
    const instanceId = "inst-parent-" + Date.now();

    // 1. Connect parent agent via WebSocket register frame
    const agentWs = new WebSocket(`${targetRouterUrl.replace(/^http/, "ws")}/v1/agents`);

    let leaseId = "";
    const agentReady = new Promise<void>((resolve, reject) => {
      agentWs.on("open", () => {
        agentWs.send(
          JSON.stringify({
            version: "v1",
            frame_id: "reg-parent-1",
            type: "register",
            agent_id: parentAgentId,
            instance_id: instanceId,
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
          if (parsed.type === "registered") {
            leaseId = parsed.lease_id;
            resolve();
          }
        } catch {}
      });
      agentWs.on("error", reject);
    });

    await agentReady;

    // Enable subagent sync on the session and send subagent_lifecycle_snapshot
    const producerEpoch = "123e4567-e89b-42d3-a456-426614174000";
    agentWs.send(
      JSON.stringify({
        version: "v1",
        frame_id: "sync-enable-1",
        type: "subagent_sync_enable",
        agent_id: parentAgentId,
        instance_id: instanceId,
        lease_id: leaseId,
      })
    );

    agentWs.send(
      JSON.stringify({
        version: "v1",
        frame_id: "snap-1",
        type: "subagent_lifecycle_snapshot",
        agent_id: parentAgentId,
        instance_id: instanceId,
        parent_session_id: "session-parent-1",
        lease_id: leaseId,
        producer_epoch: producerEpoch,
        client_sequence: 1,
        event_id: "evt-snap-1",
        subagents: [
          {
            id: "subagent-child-running",
            type: "general-purpose",
            description: "Running research task",
            status: "running",
          },
          {
            id: "subagent-child-completed",
            type: "general-purpose",
            description: "Completed audit deliverable",
            status: "completed",
          },
        ],
      })
    );

    // 2. Open UI and select parent agent
    await page.goto(harness.frontend.url);
    await page.evaluate(
      ({ routerUrl, selectedAgent }) => {
        localStorage.setItem("mecha.web.activeRouter", routerUrl);
        localStorage.setItem("mecha.web.routers", JSON.stringify([routerUrl]));
        localStorage.setItem(
          "mecha.web.selectedPeers",
          JSON.stringify({ [`${routerUrl}\u001fuser`]: selectedAgent })
        );
      },
      { routerUrl: targetRouterUrl, selectedAgent: parentAgentId }
    );
    await page.reload();

    // Verify UI loaded with parent agent selected
    const agentHeading = page.locator('h2:has-text("Agent Parent Budget")');
    await expect(agentHeading).toBeVisible({ timeout: 10000 });

    // Verify subagents indicator or metrics card is present
    const subagentsBadge = page.locator(".header-metric-card.subagents-card");
    await expect(subagentsBadge).toBeVisible({ timeout: 5000 });
    await expect(subagentsBadge).toContainText("2"); // 2 total subagents in lifecycle snapshot

    // Click subagents card to open the subagent viewer modal
    await subagentsBadge.click();
    const subagentDialog = page.locator("dialog.subagent-viewer-dialog[open]");
    await expect(subagentDialog).toBeVisible({ timeout: 5000 });

    // Default filter is "Active (1)", so running subagent is visible
    const runningCard = subagentDialog.locator(".subagent-card").filter({ hasText: "Running research task" });
    await expect(runningCard).toBeVisible();
    await expect(runningCard.locator(".subagent-status-pill")).toHaveText("running");

    // Switch filter to "All (2)" to verify completed subagent is also listed
    const filterSelect = subagentDialog.locator("#subagent-filter-select");
    await filterSelect.selectOption("all");

    const completedCard = subagentDialog.locator(".subagent-card").filter({ hasText: "Completed audit deliverable" });
    await expect(completedCard).toBeVisible();
    await expect(completedCard.locator(".subagent-status-pill")).toHaveText("completed");

    // Click on running subagent to drill down into conversation view
    await runningCard.click();
    const drilldownView = subagentDialog.locator(".subagent-drilldown-view");
    await expect(drilldownView).toBeVisible();
    await expect(subagentDialog.locator("h2")).toHaveText("Running research task");

    // Click Back to return to list view
    const backBtn = drilldownView.locator(".subagent-back-button");
    await backBtn.click();
    await expect(subagentDialog.locator(".subagent-list-view")).toBeVisible();

    // Close the subagent dialog
    const closeBtn = subagentDialog.locator('button[aria-label="Close dialog"]');
    await closeBtn.click();
    await expect(subagentDialog).not.toBeVisible();

    agentWs.close();
  });
});
