import { randomUUID } from "node:crypto";
import { test, expect } from "@playwright/test";
import { workflowHarness, model, type WorkflowHarness } from "./workflow-implicit-goals-harness";

// SPEC-MODEL-CATALOG-RESILIENCE-001:
// 1. Unfamiliar or heterogeneous provider names in /v1/models and malformed entries MUST NOT
//    cause the frontend client to reject the entire model catalog with "router returned invalid model catalog".
// 2. Model catalog errors/failures MUST NOT render an intrusive error banner below the agent header.

test.describe("Model Catalog Resilience and Localized Error Display", () => {
  test.setTimeout(90_000);
  let f: WorkflowHarness;
  let agentId: string;

  test.beforeEach(async () => {
    f = await workflowHarness({ restartCodexConsumerFixture: true });
    agentId = `mcr-agent-${randomUUID()}`;
  });

  test.afterEach(async () => {
    f?.releaseHeld();
    await f?.close();
  });

  async function launchAndSelectAgent(page: import("@playwright/test").Page) {
    await f.open(page);
    await expect.poll(() => f.wsFrames.some(raw => JSON.parse(raw).type === "registered"), { timeout: 20_000 }).toBe(true);

    await f.api("/v1/managed-agents", { name: agentId, cwd: `${f.router.root}/work`, runtime: "embedded", model });
    await expect(page.locator(`[data-agent-id="${agentId}"]`)).toBeVisible();
    await expect.poll(() => f.rows("SELECT lifecycle_state, activity_state, accepting_prompts FROM managed_agents WHERE agent_id=?", agentId)[0], { timeout: 20_000 }).toEqual({ lifecycle_state: "running", activity_state: "idle", accepting_prompts: 1 });

    // Select the agent
    await page.locator(`[data-agent-id="${agentId}"]`).click();
    await expect(page.locator(".peer-name-label")).toBeVisible();
  }

  test("SCN-MODEL-001 & SCN-MODEL-002: heterogeneous providers and malformed entries are parsed resiliently without error banner", async ({ page }) => {
    // Intercept /v1/models to return heterogeneous/unknown providers alongside malformed items
    await page.route("**/v1/models", async (route) => {
      if (route.request().method() === "GET") {
        await route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify({
            auth_revision: 10,
            models: [
              { provider: "openai-codex", id: "gpt-6.1-terra", name: "GPT-6.1 Terra", available: true },
              { provider: "google", id: "gemini-3.8-flash", name: "Gemini 3.8 Flash (Google)", available: true },
              { provider: "cursor", id: "cursor-small", name: "Cursor Small", available: true },
              { provider: "google-vertex", id: "vertex-flash", name: "Vertex Flash", available: true },
              { provider: "custom-ollama", id: "llama-3.3-70b", name: "Llama 3.3 70B", available: true },
              // Malformed entries that should be filtered gracefully without dropping the catalog
              null,
              { provider: "", id: "empty-provider", available: true },
              { provider: "google", id: "", available: true },
              { id: "missing-provider", available: true },
              { provider: 123, id: "invalid-provider-type" },
            ],
            default_model: { provider: "openai-codex", id: "gpt-6.1-terra" },
          }),
        });
        return;
      }
      await route.continue();
    });

    await launchAndSelectAgent(page);

    // Verify NO error banner with "router returned invalid model catalog" or "invalid response" is rendered
    await expect(page.locator(".agent-error-banner, [role='alert'].agent-error-banner")).not.toBeVisible();
    await expect(page.getByText("Router returned invalid model catalog", { exact: false })).not.toBeVisible();

    // Verify model picker in conversation header shows current model and opens options list
    const modelPickerButton = page.locator(".conversation-header .model-controls .model-picker button.model-current");
    await expect(modelPickerButton).toBeVisible();
    await modelPickerButton.click();

    const optionsDialog = page.getByRole("dialog");
    await expect(optionsDialog).toBeVisible();

    // Check that models from diverse providers are available in the picker
    await expect(optionsDialog.getByText("GPT-6.1 Terra", { exact: false })).toBeVisible();
    await expect(optionsDialog.getByText("Gemini 3.8 Flash (Google)", { exact: false })).toBeVisible();
    await expect(optionsDialog.getByText("Cursor Small", { exact: false })).toBeVisible();
    await expect(optionsDialog.getByText("Vertex Flash", { exact: false })).toBeVisible();
    await expect(optionsDialog.getByText("Llama 3.3 70B", { exact: false })).toBeVisible();

    // Verify no global banner was produced
    await expect(page.locator(".agent-error-banner")).not.toBeVisible();
    expect(f.violations).toEqual([]);
  });

  test("SCN-MODEL-003: model catalog fetch failure does not display global red error banner below agent header", async ({ page }) => {
    // Intercept /v1/models to fail with HTTP 500 error
    await page.route("**/v1/models", async (route) => {
      if (route.request().method() === "GET") {
        await route.fulfill({
          status: 500,
          contentType: "application/json",
          body: JSON.stringify({ error: { code: "INTERNAL", message: "Catalog database locked" } }),
        });
        return;
      }
      await route.continue();
    });

    await launchAndSelectAgent(page);

    // Invariant: Model catalog errors MUST NOT be passed to Conversation agentError,
    // and MUST NOT render an intrusive red banner across the active conversation view.
    await expect(page.locator(".agent-error-banner")).not.toBeVisible();
    await expect(page.locator("[role='alert'].agent-error-banner")).not.toBeVisible();

    // Verify user can still view conversation header and composer without interruption
    await expect(page.locator(".conversation-header")).toBeVisible();
    await expect(page.getByPlaceholder("Message an agent or drop images here")).toBeVisible();

    // The error is localized to the model picker emptyMessage
    const modelPickerButton = page.locator(".conversation-header .model-controls .model-picker button.model-current");
    await expect(modelPickerButton).toBeVisible();
    await modelPickerButton.click();

    // Should indicate models could not be loaded inside the model picker popup, not as a page error
    const dialog = page.getByRole("dialog");
    await expect(dialog.getByText("Catalog database locked", { exact: false })).toBeVisible();

    // Banner remains absent
    await expect(page.locator(".agent-error-banner")).not.toBeVisible();
    expect(f.violations).toEqual([]);
  });
});
