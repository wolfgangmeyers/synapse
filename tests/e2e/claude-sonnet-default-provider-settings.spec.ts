import { test, expect } from "@playwright/test";
import { claudeWorkersHarness } from "./claude-workers-harness";

// Real-boundary slice: browser Provider settings -> router HTTP settings -> persisted
// catalog after reload. The Claude CLI is the deterministic disposable fake on PATH.
test("Provider settings saves Claude Sonnet as default and keeps it after reload", async ({ page }) => {
  test.setTimeout(120_000);
  const f = await claudeWorkersHarness({}, { defaultModel: null });
  try {
    await f.open(page);
    const openSettings = async () => {
      await page.getByRole("button", { name: "Managed actions", exact: true }).click();
      await page.getByRole("menuitem", { name: "Provider settings", exact: true }).click();
      return page.getByRole("dialog", { name: "Provider settings", exact: true });
    };

    const settings = await openSettings();
    const picker = settings.locator(".default-model-picker");
    const current = picker.locator("button.model-current");
    await expect(current).toHaveText("Automatic (Pi model)");

    await current.click();
    const options = picker.getByRole("listbox", { name: "Default model options" });
    const sonnetOption = options.getByRole("option").filter({ hasText: "claude-code:sonnet" });
    await expect(sonnetOption).toBeVisible();
    await sonnetOption.click();

    // Success is observable in the UI, the router settings endpoint, and the native mirror.
    await expect(settings.getByRole("status")).toHaveText("New sessions will use Claude Sonnet.");
    await expect.poll(async () => (await f.request(page, "/v1/models")).body.default_model)
      .toEqual({ provider: "claude-code", id: "sonnet" });
    await expect(current).toHaveText("Claude Sonnet");
    await expect(settings.locator("select[aria-label='Default model']")).toHaveValue("claude-code:sonnet");

    // Persistence: a fresh page load must show the saved Sonnet selection.
    await page.reload();
    const reloaded = await openSettings();
    const reloadedPicker = reloaded.locator(".default-model-picker");
    await expect(reloadedPicker.locator("button.model-current")).toHaveText("Claude Sonnet");
    await expect(reloaded.locator("select[aria-label='Default model']")).toHaveValue("claude-code:sonnet");
    await reloadedPicker.locator("button.model-current").click();
    await expect(reloadedPicker.getByRole("listbox", { name: "Default model options" })
      .getByRole("option").filter({ hasText: "claude-code:sonnet" })).toHaveAttribute("aria-selected", "true");
    expect((await f.request(page, "/v1/models")).body.default_model).toEqual({ provider: "claude-code", id: "sonnet" });
    expect(f.violations).toEqual([]);
  } finally { await f.close(); }
});
