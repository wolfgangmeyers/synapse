import { test, expect } from "@playwright/test";
import { claudeWorkersHarness } from "./claude-workers-harness";

test("Claude worker shows its source tag after launch and reload", async ({ page }) => {
  test.setTimeout(90_000);
  const f = await claudeWorkersHarness();
  try {
    await f.open(page);
    const agent = await f.launch(page);
    const tag = page.locator(`[data-agent-id="${agent}"] .agent-name-line .agent-source`);
    await expect(tag).toHaveText("claude");
    await page.reload();
    await expect(tag).toBeVisible();
    await expect(tag).toHaveText("claude");
  } finally {
    await f.close();
  }
});
