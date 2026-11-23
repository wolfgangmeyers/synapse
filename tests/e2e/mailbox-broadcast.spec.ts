import { test, expect, type Page } from "@playwright/test";
import { DatabaseSync } from "node:sqlite";
import { setupE2EHarness, type E2ETestHarness } from "./harness";

function seedAgents(databasePath: string) {
  const db = new DatabaseSync(databasePath);
  try {
    db.exec("PRAGMA busy_timeout=5000; PRAGMA foreign_keys=ON;");
    const nowIso = new Date().toISOString();
    const sha256Hex = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08";

    const insertIdentity = db.prepare("INSERT OR IGNORE INTO agents (agent_id, first_seen_at) VALUES (?, ?)");
    insertIdentity.run("user", nowIso);
    insertIdentity.run("worker-alpha", nowIso);
    insertIdentity.run("worker-beta", nowIso);
    insertIdentity.run("worker-stopped", nowIso);

    const insertManaged = db.prepare(`
      INSERT OR REPLACE INTO managed_agents (
        agent_id, name, cwd, selected_model_provider, selected_model_id,
        desired_state, lifecycle_state, activity_state, runtime, session_epoch,
        manifest_revision, manifest_hash, created_at, updated_at, is_background
      ) VALUES (?, ?, '/tmp', 'google', 'gemini-3.8-flash', ?, ?, ?, 'embedded', 1, 1, ?, ?, ?, 0)
    `);

    insertManaged.run("worker-alpha", "worker-alpha", "running", "running", "active", sha256Hex, nowIso, nowIso);
    insertManaged.run("worker-beta", "worker-beta", "running", "running", "idle", sha256Hex, nowIso, nowIso);
    insertManaged.run("worker-stopped", "worker-stopped", "terminated", "terminated", "stopped", sha256Hex, nowIso, nowIso);

    db.prepare("UPDATE managed_settings SET projection_version = projection_version + 1 WHERE singleton_id = 1").run();
  } finally {
    db.close();
  }
}

async function openWebUI(page: Page, harness: E2ETestHarness) {
  const targetRouterUrl = harness.proxy ? harness.proxy.url : harness.router.httpUrl;
  await page.goto(harness.frontend.url);
  await page.evaluate(
    ({ routerUrl }) => {
      localStorage.setItem("mecha.web.activeRouter", routerUrl);
      localStorage.setItem("mecha.web.routers", JSON.stringify([routerUrl]));
    },
    { routerUrl: targetRouterUrl }
  );
  await page.reload();
}

async function openBroadcastDialog(page: Page) {
  const trigger = page.locator(".managed-actions-trigger");
  await expect(trigger).toBeVisible({ timeout: 10000 });
  await trigger.click();

  const menu = page.locator(".managed-actions-menu");
  await expect(menu).toBeVisible({ timeout: 5000 });

  const broadcastBtn = menu.locator('.managed-action-tile-btn[aria-label="Broadcast Message"]');
  await expect(broadcastBtn).toBeVisible({ timeout: 5000 });
  await broadcastBtn.click();

  const dialog = page.locator("dialog.broadcast-dialog");
  await expect(dialog).toBeVisible({ timeout: 5000 });
  return dialog;
}

test.describe("Mailbox Message Broadcast (SPEC-MAILBOX-BROADCAST-001)", () => {
  let harness: E2ETestHarness;

  test.beforeAll(async () => {
    harness = await setupE2EHarness({
      useProxy: true,
      managed: true,
    });
    seedAgents(harness.router.databasePath);
  });

  test.afterAll(async () => {
    if (harness) {
      await harness.close();
    }
  });

  test("SCN-BROADCAST-001: opens broadcast dialog from sidebar server controls menu", async ({ page }) => {
    await openWebUI(page, harness);

    // Open broadcast dialog from server menu
    const dialog = await openBroadcastDialog(page);

    // Check title and description
    await expect(dialog.locator(".dialog-title")).toHaveText("Broadcast Message");
    await expect(dialog.locator("header p")).toHaveText("Send a message to multiple agents at once.");

    // Check key inputs and elements
    await expect(dialog.locator(".broadcast-recipient-list")).toBeVisible();
    await expect(dialog.locator(".broadcast-subject-input")).toBeVisible();
    await expect(dialog.locator(".broadcast-body-input")).toBeVisible();

    const cancelBtn = dialog.locator(".broadcast-dialog-actions button:has-text('Cancel')");
    await expect(cancelBtn).toBeVisible();
    const sendBtn = dialog.locator(".broadcast-dialog-actions button:has-text('Send Broadcast')");
    await expect(sendBtn).toBeVisible();

    // Close via close icon button
    const closeBtn = dialog.locator("header button.icon-btn-round[aria-label='Close broadcast dialog']");
    await expect(closeBtn).toBeVisible();
    await closeBtn.click();
    await expect(dialog).toBeHidden();

    // Re-open and close via Cancel button
    await openBroadcastDialog(page);
    await expect(dialog).toBeVisible();
    await cancelBtn.click();
    await expect(dialog).toBeHidden();
  });

  test("SCN-BROADCAST-002: defaults recipient selection to active running workers", async ({ page }) => {
    await openWebUI(page, harness);
    const dialog = await openBroadcastDialog(page);

    // Count label reflects 2 active workers out of 3 available (excluding current user session)
    const countLabel = dialog.locator(".broadcast-count-label");
    await expect(countLabel).toBeVisible();
    await expect(countLabel).toHaveText("Selected 2 of 3 agents (2 active)");

    // Target rows
    const alphaRow = dialog.locator(".broadcast-recipient-row", { hasText: "worker-alpha" });
    const betaRow = dialog.locator(".broadcast-recipient-row", { hasText: "worker-beta" });
    const stoppedRow = dialog.locator(".broadcast-recipient-row", { hasText: "worker-stopped" });
    const userRow = dialog.locator(".broadcast-recipient-row", { hasText: "(user)" });

    // Active workers default to checked
    await expect(alphaRow).toBeVisible();
    await expect(alphaRow.locator("input[type='checkbox']")).toBeChecked();
    await expect(alphaRow.locator(".broadcast-status-badge")).toHaveText("active");

    await expect(betaRow).toBeVisible();
    await expect(betaRow.locator("input[type='checkbox']")).toBeChecked();
    await expect(betaRow.locator(".broadcast-status-badge")).toHaveText("idle");

    // Stopped worker defaults to unchecked
    await expect(stoppedRow).toBeVisible();
    await expect(stoppedRow.locator("input[type='checkbox']")).not.toBeChecked();
    await expect(stoppedRow.locator(".broadcast-status-badge")).toHaveText("stopped");

    // Current user session is excluded
    await expect(userRow).toHaveCount(0);
  });

  test("SCN-BROADCAST-003: handles bulk selection controls and individual recipient toggles", async ({ page }) => {
    await openWebUI(page, harness);
    const dialog = await openBroadcastDialog(page);

    const countLabel = dialog.locator(".broadcast-count-label");
    await expect(countLabel).toHaveText("Selected 2 of 3 agents (2 active)");

    const alphaCheckbox = dialog.locator(".broadcast-recipient-row", { hasText: "worker-alpha" }).locator("input[type='checkbox']");
    const betaCheckbox = dialog.locator(".broadcast-recipient-row", { hasText: "worker-beta" }).locator("input[type='checkbox']");
    const stoppedCheckbox = dialog.locator(".broadcast-recipient-row", { hasText: "worker-stopped" }).locator("input[type='checkbox']");

    // Click "Deselect All"
    await dialog.getByRole("button", { name: "Deselect All", exact: true }).click();
    await expect(countLabel).toHaveText("Selected 0 of 3 agents (2 active)");
    await expect(alphaCheckbox).not.toBeChecked();
    await expect(betaCheckbox).not.toBeChecked();
    await expect(stoppedCheckbox).not.toBeChecked();

    // Click "Select All"
    await dialog.getByRole("button", { name: "Select All", exact: true }).click();
    await expect(countLabel).toHaveText("Selected 3 of 3 agents (2 active)");
    await expect(alphaCheckbox).toBeChecked();
    await expect(betaCheckbox).toBeChecked();
    await expect(stoppedCheckbox).toBeChecked();

    // Click "Select Active"
    await dialog.getByRole("button", { name: "Select Active", exact: true }).click();
    await expect(countLabel).toHaveText("Selected 2 of 3 agents (2 active)");
    await expect(alphaCheckbox).toBeChecked();
    await expect(betaCheckbox).toBeChecked();
    await expect(stoppedCheckbox).not.toBeChecked();

    // Toggle individual checkbox (uncheck alpha)
    await alphaCheckbox.click();
    await expect(countLabel).toHaveText("Selected 1 of 3 agents (2 active)");
    await expect(alphaCheckbox).not.toBeChecked();

    // Toggle individual checkbox again (re-check alpha)
    await alphaCheckbox.click();
    await expect(countLabel).toHaveText("Selected 2 of 3 agents (2 active)");
    await expect(alphaCheckbox).toBeChecked();
  });

  test("SCN-BROADCAST-004: disables submit when no recipients selected or body is empty", async ({ page }) => {
    await openWebUI(page, harness);
    const dialog = await openBroadcastDialog(page);

    const sendBtn = dialog.locator(".broadcast-dialog-actions button:has-text('Send Broadcast')");
    const bodyInput = dialog.locator(".broadcast-body-input");

    // Default: 2 recipients selected, body is empty -> disabled
    await expect(sendBtn).toBeDisabled();

    // Whitespace-only body -> disabled
    await bodyInput.fill("   \n\t  ");
    await expect(sendBtn).toBeDisabled();

    // Valid text with recipients -> enabled
    await bodyInput.fill("Operational announcement");
    await expect(sendBtn).toBeEnabled();

    // Deselect all recipients -> disabled
    await dialog.getByRole("button", { name: "Deselect All", exact: true }).click();
    await expect(sendBtn).toBeDisabled();

    // Re-select one recipient -> enabled
    const alphaCheckbox = dialog.locator(".broadcast-recipient-row", { hasText: "worker-alpha" }).locator("input[type='checkbox']");
    await alphaCheckbox.click();
    await expect(sendBtn).toBeEnabled();

    // Empty body again -> disabled
    await bodyInput.fill("");
    await expect(sendBtn).toBeDisabled();
  });

  test("SCN-BROADCAST-005: dispatches individual message envelopes to each recipient", async ({ page }) => {
    await openWebUI(page, harness);
    const dialog = await openBroadcastDialog(page);

    // Track POST /v1/messages calls
    const sentMessages: Array<{
      id: string;
      from: string;
      to: string;
      subject?: string;
      body: string;
      source?: { kind: string };
    }> = [];

    await page.route("**/v1/messages", async (route) => {
      const req = route.request();
      if (req.method() === "POST") {
        const payload = req.postDataJSON();
        sentMessages.push(payload);
      }
      await route.continue();
    });

    const subjectInput = dialog.locator(".broadcast-subject-input");
    const bodyInput = dialog.locator(".broadcast-body-input");
    const sendBtn = dialog.locator(".broadcast-dialog-actions button:has-text('Send Broadcast')");

    await subjectInput.fill("General Directive");
    await bodyInput.fill("All workers proceed with scheduled verification.");

    await sendBtn.click();

    // Wait for success summary
    const summary = dialog.locator(".broadcast-summary.success");
    await expect(summary).toBeVisible({ timeout: 10000 });
    await expect(summary).toHaveText("Sent to 2 agents successfully.");

    // Verify distinct messages dispatched
    expect(sentMessages.length).toBe(2);

    const alphaMsg = sentMessages.find((m) => m.to === "worker-alpha");
    const betaMsg = sentMessages.find((m) => m.to === "worker-beta");

    expect(alphaMsg).toBeDefined();
    expect(betaMsg).toBeDefined();

    expect(alphaMsg!.from).toBe("user");
    expect(alphaMsg!.to).toBe("worker-alpha");
    expect(alphaMsg!.subject).toBe("General Directive");
    expect(alphaMsg!.body).toBe("All workers proceed with scheduled verification.");
    expect(alphaMsg!.source?.kind).toBe("web_ui");

    expect(betaMsg!.from).toBe("user");
    expect(betaMsg!.to).toBe("worker-beta");
    expect(betaMsg!.subject).toBe("General Directive");
    expect(betaMsg!.body).toBe("All workers proceed with scheduled verification.");
    expect(betaMsg!.source?.kind).toBe("web_ui");

    // Envelopes have unique IDs
    expect(alphaMsg!.id).not.toBe(betaMsg!.id);
  });

  test("SCN-BROADCAST-006: shows per-recipient progress and status indicators during dispatch", async ({ page }) => {
    await openWebUI(page, harness);
    const dialog = await openBroadcastDialog(page);

    const bodyInput = dialog.locator(".broadcast-body-input");
    const sendBtn = dialog.locator(".broadcast-dialog-actions button:has-text('Send Broadcast')");

    await bodyInput.fill("Progress indicator verification");
    await sendBtn.click();

    // Wait for summary completion
    const summary = dialog.locator(".broadcast-summary.success");
    await expect(summary).toBeVisible({ timeout: 10000 });
    await expect(summary).toHaveText("Sent to 2 agents successfully.");

    // Both recipient rows show delivered status checkmark
    const alphaStatus = dialog.locator(".broadcast-recipient-row", { hasText: "worker-alpha" }).locator(".broadcast-recipient-status.delivered");
    const betaStatus = dialog.locator(".broadcast-recipient-row", { hasText: "worker-beta" }).locator(".broadcast-recipient-status.delivered");

    await expect(alphaStatus).toBeVisible();
    await expect(alphaStatus.locator("i.fa-check")).toBeVisible();

    await expect(betaStatus).toBeVisible();
    await expect(betaStatus.locator("i.fa-check")).toBeVisible();
  });

  test("SCN-BROADCAST-007: continues remaining dispatches when an individual recipient fails", async ({ page }) => {
    await openWebUI(page, harness);
    const dialog = await openBroadcastDialog(page);

    // Mock failure on worker-alpha, allow worker-beta to succeed
    await page.route("**/v1/messages", async (route) => {
      const req = route.request();
      if (req.method() === "POST") {
        const payload = req.postDataJSON();
        if (payload?.to === "worker-alpha") {
          await route.fulfill({
            status: 500,
            contentType: "application/json",
            body: JSON.stringify({
              error: {
                code: "delivery_failed",
                message: "Mailbox delivery rejected by agent",
              },
            }),
          });
          return;
        }
      }
      await route.continue();
    });

    const bodyInput = dialog.locator(".broadcast-body-input");
    const sendBtn = dialog.locator(".broadcast-dialog-actions button:has-text('Send Broadcast')");

    await bodyInput.fill("Fault-tolerance broadcast test");
    await sendBtn.click();

    // Summary indicates partial failure
    const errorSummary = dialog.locator(".broadcast-summary.error");
    await expect(errorSummary).toBeVisible({ timeout: 10000 });
    await expect(errorSummary).toHaveText("Sent to 1 agents; 1 failed.");

    // worker-alpha row displays error indicator with error message
    const alphaError = dialog.locator(".broadcast-recipient-row", { hasText: "worker-alpha" }).locator(".broadcast-recipient-status.error");
    await expect(alphaError).toBeVisible();
    await expect(alphaError.locator("i.fa-triangle-exclamation")).toBeVisible();
    await expect(alphaError.locator(".broadcast-error-msg")).toContainText("Mailbox delivery rejected by agent");

    // worker-beta row displays delivered success checkmark
    const betaDelivered = dialog.locator(".broadcast-recipient-row", { hasText: "worker-beta" }).locator(".broadcast-recipient-status.delivered");
    await expect(betaDelivered).toBeVisible();
    await expect(betaDelivered.locator("i.fa-check")).toBeVisible();

    // Dialog remains open for operator inspection
    await expect(dialog).toBeVisible();
  });
});
