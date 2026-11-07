import { test, expect } from "@playwright/test";
import { claudeWorkersHarness } from "./claude-workers-harness";

test("CD-001 CD-002 settings selector persists Claude and explains local preflight and automatic Pi", async ({ page }) => {
  test.setTimeout(120_000);
  const f = await claudeWorkersHarness({}, { defaultModel: null });
  try {
    await f.open(page);
    const openSettings = async () => {
      await page.getByRole("button", { name: "Managed actions", exact: true }).click();
      await page.getByRole("menuitem", { name: "Provider settings", exact: true }).click();
      return page.getByRole("dialog", { name: "Provider settings", exact: true });
    };
    let settings = await openSettings();
    await expect(settings.getByText(/Claude models need the Claude CLI installed and signed in on the router host/i)).toBeVisible();
    await expect(settings.getByText(/Automatic uses a Pi model/i)).toBeVisible();
    const native = settings.locator("select[aria-label='Default model']");
    await expect(native.locator('option[value=""]')).toHaveText("Automatic (Pi model)");
    await settings.getByRole("button", { name: "Automatic (Pi model)", exact: true }).click();
    const options = settings.getByRole("listbox", { name: "Default model options" });
    await expect(options.getByRole("option", { name: "Automatic (Pi model)", exact: true })).toBeVisible();
    await options.getByRole("option").filter({ hasText: "claude-code:haiku" }).click();
    await expect.poll(async () => (await f.request(page, "/v1/models")).body.default_model?.id).toBe("haiku");
    await page.reload();
    settings = await openSettings();
    const picker = settings.locator(".default-model-picker");
    const current = picker.locator("button.model-current");
    await expect(settings.locator("select[aria-label='Default model']")).toHaveValue("claude-code:haiku");
    await current.click();
    const reopenedOptions = picker.getByRole("listbox", { name: "Default model options" });
    await expect(reopenedOptions.getByRole("option").filter({ hasText: "claude-code:haiku" })).toHaveAttribute("aria-selected", "true");
    await reopenedOptions.getByRole("option", { name: "Automatic (Pi model)", exact: true }).click();
    await expect.poll(async () => (await f.request(page, "/v1/models")).body.default_model).toBeNull();
    await expect(current).toHaveText("Automatic (Pi model)");
    await current.click();
    await expect(reopenedOptions.getByRole("option", { name: "Automatic (Pi model)", exact: true })).toHaveAttribute("aria-selected", "true");
    await page.reload();
    settings = await openSettings();
    const reloadedPicker = settings.locator(".default-model-picker");
    await expect(reloadedPicker.locator("button.model-current")).toHaveText("Automatic (Pi model)");
    await reloadedPicker.locator("button.model-current").click();
    await expect(reloadedPicker.getByRole("listbox", { name: "Default model options" }).getByRole("option", { name: "Automatic (Pi model)", exact: true })).toHaveAttribute("aria-selected", "true");
    expect((await f.request(page, "/v1/models")).body.default_model).toBeNull();
    expect(f.violations).toEqual([]);
  } finally { await f.close(); }
});
