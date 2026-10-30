import { test, expect } from "@playwright/test";
import { setupE2EHarness, type E2EHarness } from "./harness";

test.describe("Server Management Dropdown Menu (SPEC-SETTINGS-002)", () => {
  let harness: E2EHarness;

  test.beforeAll(async () => {
    harness = await setupE2EHarness({
      useProxy: true,
    });
  });

  test.afterAll(async () => {
    if (harness) {
      await harness.close();
    }
  });

  test("SCN-SERVER-001: renders server menu in 2-column grid with icons and no pins", async ({ page }) => {
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

    const trigger = page.locator(".managed-actions-trigger");
    await expect(trigger).toBeVisible({ timeout: 10000 });
    await trigger.click();

    const menu = page.locator(".managed-actions-menu");
    await expect(menu).toBeVisible({ timeout: 5000 });

    // Check header
    const headerTitle = menu.locator(".managed-actions-header .managed-actions-title");
    await expect(headerTitle).toBeVisible();
    await expect(headerTitle).toHaveText("Server Controls");

    // Check grid layout
    const grid = menu.locator(".managed-actions-grid");
    await expect(grid).toBeVisible();

    const gridStyle = await grid.evaluate((el) => {
      const computed = window.getComputedStyle(el);
      return {
        display: computed.display,
        gridTemplateColumns: computed.gridTemplateColumns,
      };
    });
    expect(gridStyle.display).toBe("grid");
    // Grid template columns should be 2 columns
    expect(gridStyle.gridTemplateColumns.split(" ").length).toBe(2);

    // Check items have icons and matching labels
    const tiles = grid.locator(".managed-action-tile");
    const count = await tiles.count();
    expect(count).toBeGreaterThanOrEqual(5);

    for (let i = 0; i < count; i++) {
      const tile = tiles.nth(i);
      const icon = tile.locator(".managed-action-tile-icon");
      await expect(icon).toBeVisible();

      // Ensure no pin button is present
      const pinBtn = tile.locator(".agent-control-pin-btn");
      await expect(pinBtn).toHaveCount(0);
    }

    // Capture screenshots to verify:
    // - 2 columns
    // - FontAwesome icons on each item
    // - No pin buttons
    // - Matching dark glassmorphic styling
    await menu.screenshot({ path: "test-results/server-menu-popup.png" });
    await page.screenshot({ path: "test-results/server-menu-page.png" });

    // Verify specific icons
    const restartItem = grid.locator('.managed-action-tile:has-text("Restart router")');
    await expect(restartItem.locator("i.fa-arrows-rotate")).toBeVisible();

    const mcpItem = grid.locator('.managed-action-tile:has-text("MCP Servers")');
    await expect(mcpItem.locator("i.fa-network-wired")).toBeVisible();

    const kbItem = grid.locator('.managed-action-tile:has-text("Knowledge Base")');
    await expect(kbItem.locator("i.fa-book")).toBeVisible();

    const workflowItem = grid.locator('.managed-action-tile:has-text("Workflows")');
    await expect(workflowItem.locator("i.fa-diagram-project")).toBeVisible();
  });

  test("SCN-SERVER-002: invokes action and closes menu on item click", async ({ page }) => {
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

    const trigger = page.locator(".managed-actions-trigger");
    await expect(trigger).toBeVisible({ timeout: 10000 });
    await trigger.click();

    const menu = page.locator(".managed-actions-menu");
    await expect(menu).toBeVisible({ timeout: 5000 });

    // Click "MCP Servers"
    const mcpBtn = menu.locator('button[role="menuitem"]:has-text("MCP Servers")');
    await expect(mcpBtn).toBeVisible();
    await mcpBtn.click();

    // Menu should close immediately
    await expect(menu).not.toBeVisible({ timeout: 3000 });

    // The MCP dialog should open
    const dialog = page.locator("dialog.dialog-shell[open]");
    await expect(dialog).toBeVisible({ timeout: 5000 });
    await expect(dialog.locator(".dialog-title")).toContainText("MCP");

    // Close the dialog
    const closeBtn = dialog.locator(".dialog-close");
    if (await closeBtn.isVisible()) {
      await closeBtn.click();
    }
  });

  test("SCN-SERVER-003: dismisses server menu on outside click or Escape", async ({ page }) => {
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

    const trigger = page.locator(".managed-actions-trigger");
    await expect(trigger).toBeVisible({ timeout: 10000 });

    // Test dismissal via Escape
    await trigger.click();
    const menu = page.locator(".managed-actions-menu");
    await expect(menu).toBeVisible({ timeout: 5000 });

    await page.keyboard.press("Escape");
    await expect(menu).not.toBeVisible({ timeout: 3000 });

    // Test dismissal via outside click
    await trigger.click();
    await expect(menu).toBeVisible({ timeout: 5000 });

    // Click outside on the body or header
    await page.locator(".brand-main-title").click();
    await expect(menu).not.toBeVisible({ timeout: 3000 });
  });
});
