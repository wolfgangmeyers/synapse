import { test, expect } from "@playwright/test";
import { claudeWorkersHarness } from "./claude-workers-harness";

test("activity dialogs stay hidden until opened and work after closing", async ({ page }) => {
  test.setTimeout(120_000);
  const f = await claudeWorkersHarness();
  try {
    await f.open(page);
    await f.launch(page);
    const shellButton = page.getByRole("region", { name: "Background shell jobs" }).getByRole("button", { name: /shell jobs/i });
    const subagentButton = page.getByRole("button", { name: /subagents/i }).last();
    const shellDialog = page.locator('dialog.subagent-viewer-dialog').filter({ has: page.getByRole("heading", { name: "Background shell jobs", includeHidden: true }) });
    await expect(shellButton).toBeVisible();
    await expect(shellDialog).toHaveCount(1);
    await expect(shellDialog).toBeHidden();
    await shellButton.click();
    await expect(shellDialog).toBeVisible();
    await expect(shellDialog).toContainText("No retained shell jobs");
    await shellDialog.getByRole("button", { name: "Close dialog" }).click();
    await expect(shellDialog).toBeHidden();
    await subagentButton.click();
    const subagentDialog = page.locator('dialog.subagent-viewer-dialog[open]');
    await expect(subagentDialog).toBeVisible();
    await expect(subagentDialog.locator(".subagent-empty-state")).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(subagentDialog).toHaveCount(0);
    await expect(shellDialog).toBeHidden();
    await shellButton.click();
    await expect(shellDialog).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(shellDialog).toBeHidden();
    await subagentButton.click();
    await expect(page.locator('dialog.subagent-viewer-dialog[open]')).toBeVisible();
  } finally {
    await f.close();
  }
});
