import { test, expect, type Page, type Locator } from "@playwright/test";
import { claudeWorkersHarness } from "./claude-workers-harness";

// SPEC-SETTINGS-DEFAULT-MODEL-001 DM-003/DM-004. Real browser + real router;
// only the network edge is faulted, and only where the router cannot be made to
// fail on demand from the UI.

const haiku = { provider: "claude-code", id: "haiku" };
const sonnet = { provider: "claude-code", id: "sonnet" };

async function openProviderSettings(page: Page): Promise<Locator> {
  await page.getByRole("button", { name: "Managed actions", exact: true }).click();
  await page.getByRole("menuitem", { name: "Provider settings", exact: true }).click();
  return page.getByRole("dialog", { name: "Provider settings", exact: true });
}

async function chooseDefault(settings: Locator, optionText: string) {
  const picker = settings.locator(".default-model-picker");
  await picker.locator("button.model-current").click();
  await picker.getByRole("listbox", { name: "Default model options" })
    .getByRole("option").filter({ hasText: optionText }).click();
}

test("successful default save is not reported as failed when the provider-auth refresh fails", async ({ page }) => {
  test.setTimeout(120_000);
  const f = await claudeWorkersHarness({}, { defaultModel: null });
  try {
    await f.open(page);
    const settings = await openProviderSettings(page);
    const current = settings.locator(".default-model-picker button.model-current");
    await expect(current).toHaveText("Automatic (Pi model)");

    // Fault: only the post-save catalog refresh fails at the network layer.
    // The PUT is not intercepted, so the router really commits Sonnet.
    await page.route("**/v1/provider-auth", route =>
      route.request().method() === "GET" ? route.abort("connectionreset") : route.continue());
    await chooseDefault(settings, "claude-code:sonnet");

    await expect(settings.getByRole("status")).toHaveText("New sessions will use Claude Sonnet.");
    await expect(settings.getByRole("status")).not.toContainText("could not be saved");
    await expect.poll(async () => (await f.request(page, "/v1/models")).body.default_model).toEqual(sonnet);
    await expect(current).toHaveText("Claude Sonnet");
    expect(f.violations).toEqual([]);
  } finally { await f.close(); }
});

test("rejected default save reports not saved and keeps the previous selection", async ({ page }) => {
  test.setTimeout(120_000);
  const f = await claudeWorkersHarness({}, { defaultModel: null });
  try {
    await f.open(page);
    // Seed a previously saved default through the real router.
    expect((await f.request(page, "/v1/settings/default-model", { model: haiku }, "PUT")).status).toBe(200);
    await page.reload();

    const settings = await openProviderSettings(page);
    const current = settings.locator(".default-model-picker button.model-current");
    await expect(current).toHaveText("Claude Haiku");

    // Fault: the router rejects the selected save with an error envelope.
    await page.route("**/v1/settings/default-model", route =>
      route.request().method() === "PUT"
        ? route.fulfill({ status: 503, contentType: "application/json", body: JSON.stringify({ error: { code: "INTERNAL", message: "injected rejection" } }) })
        : route.continue());
    await chooseDefault(settings, "claude-code:sonnet");

    await expect(settings.getByRole("status")).toHaveText("Default model could not be saved.");
    await expect(current).toHaveText("Claude Haiku");
    expect((await f.request(page, "/v1/models")).body.default_model).toEqual(haiku);

    // After reload the previously saved default is still the one shown.
    await page.unroute("**/v1/settings/default-model");
    await page.reload();
    const reloaded = await openProviderSettings(page);
    await expect(reloaded.locator(".default-model-picker button.model-current")).toHaveText("Claude Haiku");
    expect((await f.request(page, "/v1/models")).body.default_model).toEqual(haiku);
    expect(f.violations).toEqual([]);
  } finally { await f.close(); }
});
