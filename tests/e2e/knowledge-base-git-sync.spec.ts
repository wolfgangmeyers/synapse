import { test, expect } from "@playwright/test";
import { setupE2EHarness, type E2ETestHarness } from "./harness";
import * as fs from "fs";
import * as path from "path";

test.describe("Knowledge Base Git Sync & Markdown Browser E2E", () => {
  let harness: E2ETestHarness;
  let kbDir: string;

  test.beforeAll(async () => {
    kbDir = path.join("/tmp", `mecha-kb-e2e-${Date.now()}`);
    fs.mkdirSync(kbDir, { recursive: true });

    // Seed an initial INDEX.md and a topic markdown file
    fs.writeFileSync(
      path.join(kbDir, "INDEX.md"),
      "# Knowledge Base Index\n\n## Guides\n- [System Architecture](system-architecture.md)\n"
    );
    fs.writeFileSync(
      path.join(kbDir, "system-architecture.md"),
      "# System Architecture\n\nThis is the system architecture documentation.\n"
    );

    process.env.MECHA_KB_DIR = kbDir;

    harness = await setupE2EHarness({
      useProxy: true,
    });
  });

  test.afterAll(async () => {
    if (harness) {
      await harness.close();
    }
    if (kbDir && fs.existsSync(kbDir)) {
      try {
        fs.rmSync(kbDir, { recursive: true, force: true });
      } catch {}
    }
  });

  test("Opens Knowledge Base dialog, verifies Git config, and browses markdown documents", async ({
    page,
  }) => {
    const targetRouterUrl = harness.proxy ? harness.proxy.url : harness.router.httpUrl;

    // Open Web UI and point to test router
    await page.goto(harness.frontend.url);
    await page.evaluate(
      ({ routerUrl }) => {
        localStorage.setItem("mecha.web.activeRouter", routerUrl);
        localStorage.setItem("mecha.web.routers", JSON.stringify([routerUrl]));
      },
      { routerUrl: targetRouterUrl }
    );
    await page.reload();

    // 0. Verify that before opening, the dialog is not visible
    const closedDialog = page.locator('dialog.kb-dialog');
    await expect(closedDialog).not.toBeVisible();
    await page.screenshot({ path: "test-results/kb-dialog-closed.png" });

    // 1. Open the managed actions dropdown menu from the sidebar
    const trigger = page.locator(".managed-actions-trigger");
    await expect(trigger).toBeVisible({ timeout: 10000 });
    await trigger.click();

    // Debug: Log inner HTML of .managed-actions-menu
    const menu = page.locator(".managed-actions-menu");
    await expect(menu).toBeVisible({ timeout: 5000 });
    const menuHtml = await menu.innerHTML();
    console.log("=== MANAGED ACTIONS MENU INNER HTML ===");
    console.log(menuHtml);
    console.log("========================================");

    // 2. Click "Knowledge Base" menu item
    const kbMenuItem = page.locator('button[role="menuitem"]:has-text("Knowledge Base")');
    await expect(kbMenuItem).toBeVisible({ timeout: 5000 });
    await kbMenuItem.click();

    // 3. Verify the Knowledge Base dialog opened
    const dialog = page.locator('dialog.dialog-shell[open]');
    await expect(dialog).toBeVisible({ timeout: 5000 });
    await expect(dialog.locator(".dialog-title")).toContainText("Knowledge Base");

    // 4. Verify GitHub Sync panel elements exist
    const gitSyncPanel = dialog.locator(".kb-github-sync");
    await expect(gitSyncPanel).toBeVisible();
    const gitUrlInput = gitSyncPanel.locator(".kb-github-url-input");
    await expect(gitUrlInput).toBeVisible();

    // Configure and save a repo URL
    const testRepoUrl = "https://github.com/example/test-kb-repo.git";
    await gitUrlInput.fill(testRepoUrl);
    const saveBtn = gitSyncPanel.locator(".kb-github-save-btn");
    await expect(saveBtn).toBeEnabled();
    await saveBtn.click();
    await expect(saveBtn).toBeDisabled(); // Saved state

    // 5. Verify Markdown Browser renders INDEX.md by default
    const browser = dialog.locator(".kb-browser");
    await expect(browser).toBeVisible();
    const markdownBody = browser.locator(".markdown-body");
    await expect(markdownBody).toBeVisible();
    await expect(markdownBody).toContainText("Knowledge Base Index");
    await expect(markdownBody).toContainText("System Architecture");

    // 6. Click the link to "System Architecture" topic
    const topicLink = markdownBody.locator('a:has-text("System Architecture")');
    await expect(topicLink).toBeVisible();
    await topicLink.click();

    // 7. Verify navigation into topic document
    await expect(markdownBody).toContainText("This is the system architecture documentation.");
    const backBtn = browser.locator(".kb-back-btn");
    await expect(backBtn).toBeVisible();

    // 8. Click "Back to Index" and verify return to root INDEX.md
    await backBtn.click();
    await expect(markdownBody).toContainText("Knowledge Base Index");
    await expect(backBtn).not.toBeVisible();

    // Verify dialog sizing: calc(100vw - 48px) and calc(100dvh - 48px)
    const box = await dialog.boundingBox();
    const viewport = page.viewportSize();
    expect(box).not.toBeNull();
    if (box && viewport) {
      expect(box.width).toBeCloseTo(viewport.width - 48, -1);
      expect(box.height).toBeCloseTo(viewport.height - 48, -1);
    }

    // Take screenshot of open dialog
    await page.screenshot({ path: "test-results/kb-dialog-open.png" });

    // 9. Test closing the dialog
    const closeBtn = dialog.locator('header .icon-btn-round[aria-label="Close dialog"]');
    await expect(closeBtn).toBeVisible();
    await closeBtn.click();
    await expect(closedDialog).not.toBeVisible();
  });
});
