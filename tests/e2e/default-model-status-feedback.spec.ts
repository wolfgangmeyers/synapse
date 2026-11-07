import { test, expect, type Page, type Locator } from "@playwright/test";
import { claudeWorkersHarness } from "./claude-workers-harness";

// SPEC-SETTINGS-DEFAULT-MODEL-001 DM-003/DM-004 styling and refresh-note coverage.
// Real browser + real router; only the network edge is faulted where needed.

// --danger in web/router-ui-react/src/app.css (#ffb4ab) as computed by the browser.
const DANGER_RGB = "rgb(255, 180, 171)";
const REFRESH_NOTE = "Could not refresh the model list. Reload to check availability.";

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

test("rejected default save shows the status in the danger color with error styling", async ({ page }) => {
  test.setTimeout(120_000);
  const f = await claudeWorkersHarness({}, { defaultModel: null });
  try {
    await f.open(page);
    const settings = await openProviderSettings(page);
    const status = settings.getByRole("status");

    // Fault: the router rejects the save with an error envelope.
    await page.route("**/v1/settings/default-model", route =>
      route.request().method() === "PUT"
        ? route.fulfill({ status: 503, contentType: "application/json", body: JSON.stringify({ error: { code: "INTERNAL", message: "injected rejection" } }) })
        : route.continue());
    await chooseDefault(settings, "claude-code:sonnet");

    await expect(status).toHaveText("Default model could not be saved.");
    await expect(status).toHaveClass(/\berror\b/);
    await expect(status).toHaveCSS("color", DANGER_RGB);
    expect(f.violations).toEqual([]);
  } finally { await f.close(); }
});

test("successful default save is not styled as an error", async ({ page }) => {
  test.setTimeout(120_000);
  const f = await claudeWorkersHarness({}, { defaultModel: null });
  try {
    await f.open(page);
    const settings = await openProviderSettings(page);
    const status = settings.getByRole("status");

    await chooseDefault(settings, "claude-code:sonnet");

    await expect(status).toHaveText("New sessions will use Claude Sonnet.");
    await expect(status).not.toHaveClass(/\berror\b/);
    await expect(status).not.toHaveCSS("color", DANGER_RGB);
    expect(f.violations).toEqual([]);
  } finally { await f.close(); }
});

test("failed model-list refresh after a successful save shows the neutral refresh note", async ({ page }) => {
  test.setTimeout(120_000);
  const f = await claudeWorkersHarness({}, { defaultModel: null });
  try {
    await f.open(page);
    const settings = await openProviderSettings(page);
    const status = settings.getByRole("status");

    // Open the picker and let the initial catalog load before injecting the fault;
    // a failed initial load would leave no options to choose from.
    const picker = settings.locator(".default-model-picker");
    await picker.locator("button.model-current").click();
    const sonnetOption = picker.getByRole("listbox", { name: "Default model options" })
      .getByRole("option").filter({ hasText: "claude-code:sonnet" });
    await expect(sonnetOption).toBeVisible();

    // Fault: only the post-save model-list refresh fails at the network layer.
    // The PUT is not intercepted, so the router really commits Sonnet.
    await page.route("**/v1/models", route =>
      route.request().method() === "GET" ? route.abort("connectionreset") : route.continue());
    await sonnetOption.click();

    const note = settings.getByText(REFRESH_NOTE, { exact: true });
    await expect(note).toBeVisible();
    await expect(note).not.toHaveClass(/\berror\b/);
    await expect(note).not.toHaveCSS("color", DANGER_RGB);

    // The note does not alter the success status or the selector.
    await expect(status).toHaveText("New sessions will use Claude Sonnet.");
    await expect(status).not.toHaveClass(/\berror\b/);
    await expect(status).not.toContainText("could not be saved");

    await page.unroute("**/v1/models");
    expect((await f.request(page, "/v1/models")).body.default_model).toEqual({ provider: "claude-code", id: "sonnet" });
    expect(f.violations).toEqual([]);
  } finally { await f.close(); }
});
