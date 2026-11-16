import { test, expect } from "@playwright/test";
import { antigravityWorkersHarness } from "./antigravity-workers-harness";

test("Antigravity worker shows its source tag after launch and reload", async ({ page }) => {
  test.setTimeout(90_000);
  const f = await antigravityWorkersHarness();
  try {
    await f.open(page);
    const agent = await f.launch(page);
    const tag = page.locator(`[data-agent-id="${agent}"] .agent-name-line .agent-source`);
    await expect(tag).toHaveText("antigravity");
    await page.reload();
    await expect(tag).toBeVisible();
    await expect(tag).toHaveText("antigravity");
  } finally {
    await f.close();
  }
});
