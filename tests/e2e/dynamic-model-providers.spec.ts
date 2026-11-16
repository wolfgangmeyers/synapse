import { test, expect } from "@playwright/test";
import * as http from "node:http";
import { claudeWorkersHarness } from "./claude-workers-harness";

test("Dynamic model providers: add custom provider, discover models, verify catalog, and delete", async ({ page }) => {
  test.setTimeout(120_000);

  // 1. Start an HTTP mock server listening on 127.0.0.1:0 responding to GET /v1/models (or /models)
  const mockServer = http.createServer((req, res) => {
    const url = req.url || "";
    if (req.method === "GET" && (url === "/v1/models" || url === "/models")) {
      res.writeHead(200, { "Content-Type": "application/json" });
      res.end(
        JSON.stringify({
          data: [
            { id: "llama3.2:3b", object: "model" },
            { id: "deepseek-r1:14b", object: "model" },
          ],
        })
      );
      return;
    }
    res.writeHead(404, { "Content-Type": "application/json" });
    res.end(JSON.stringify({ error: "not found" }));
  });

  await new Promise<void>((resolve) => mockServer.listen(0, "127.0.0.1", resolve));
  const port = (mockServer.address() as { port: number }).port;

  const f = await claudeWorkersHarness({}, { defaultModel: null });

  try {
    await f.open(page);

    const openSettings = async () => {
      await page.getByRole("button", { name: "Managed actions", exact: true }).click();
      await page.getByRole("menuitem", { name: "Provider settings", exact: true }).click();
      const dialog = page.getByRole("dialog", { name: "Provider settings", exact: true });
      await expect(dialog).toBeVisible();
      return dialog;
    };

    // 2. Open Provider Settings modal
    const settings = await openSettings();

    // 3. Verify list view shows built-in providers and custom provider section with "+ Add Custom Provider"
    await expect(settings.getByText("ChatGPT", { exact: false })).toBeVisible();
    await expect(settings.getByRole("heading", { name: "Custom Providers" })).toBeVisible();
    const addBtn = settings.getByRole("button", { name: "+ Add Custom Provider" });
    await expect(addBtn).toBeVisible();

    // 4. Click "+ Add Custom Provider" to switch to Form View
    await addBtn.click();

    // In Form View, verify inputs exist
    const nameInput = settings.getByLabel("Provider name");
    const baseUrlInput = settings.getByLabel("Base URL");
    await expect(nameInput).toBeVisible();
    await expect(baseUrlInput).toBeVisible();

    // 5. Fill in:
    //    Name / Label: "Local Ollama"
    //    Base URL: http://127.0.0.1:${port}/v1
    await nameInput.fill("Local Ollama");
    await baseUrlInput.fill(`http://127.0.0.1:${port}/v1`);

    // 6. Click "Test & Discover Models"
    const discoverBtn = settings.getByRole("button", { name: "Test & Discover Models" });
    await expect(discoverBtn).toBeVisible();
    await discoverBtn.click();

    // 7. Wait for discovered models list to show llama3.2:3b and deepseek-r1:14b
    await expect(settings.getByText("llama3.2:3b")).toBeVisible();
    await expect(settings.getByText("deepseek-r1:14b")).toBeVisible();

    // 8. Click "Save Provider"
    const saveBtn = settings.getByRole("button", { name: "Save Provider" });
    await expect(saveBtn).toBeVisible();
    await saveBtn.click();

    // 9. Verify it returns to List View and shows "Local Ollama" with 2 models and .icon-btn-round Edit / Delete buttons
    await expect(settings.getByRole("heading", { name: "Custom Providers" })).toBeVisible();
    await expect(settings.getByText("Local Ollama", { exact: true })).toBeVisible();
    await expect(settings.getByText("2 models", { exact: false })).toBeVisible();

    const editBtn = settings.locator('.icon-btn-round[aria-label="Edit Local Ollama"]');
    const deleteBtn = settings.locator('.icon-btn-round[aria-label="Delete Local Ollama"]');
    await expect(editBtn).toBeVisible();
    await expect(deleteBtn).toBeVisible();

    // 10. Query /v1/models via router HTTP API to verify custom-local-ollama / llama3.2:3b is listed in available models
    await expect.poll(async () => {
      const resp = await f.request(page, "/v1/models");
      const models = (resp.body.models || []) as Array<{ provider: string; id: string; name: string }>;
      return models.some((m) => m.provider === "custom-local-ollama" && m.id === "llama3.2:3b");
    }).toBe(true);

    // Also verify deepseek-r1:14b
    const modelsResp = await f.request(page, "/v1/models");
    const customModels = (modelsResp.body.models || []) as Array<{ provider: string; id: string; name: string }>;
    expect(customModels.some((m) => m.provider === "custom-local-ollama" && m.id === "deepseek-r1:14b")).toBe(true);

    // 11. In List View, click Delete on "Local Ollama". Verify it is removed.
    await deleteBtn.click();

    await expect(settings.getByText("Local Ollama", { exact: true })).not.toBeVisible();
    await expect(settings.getByText("No custom providers configured yet.")).toBeVisible();

    // Verify /v1/models no longer has custom-local-ollama
    await expect.poll(async () => {
      const resp = await f.request(page, "/v1/models");
      const models = (resp.body.models || []) as Array<{ provider: string; id: string; name: string }>;
      return models.some((m) => m.provider === "custom-local-ollama");
    }).toBe(false);

    expect(f.violations).toEqual([]);
  } finally {
    await f.close();
    await new Promise<void>((resolve) => mockServer.close(() => resolve()));
  }
});

test("Dynamic model providers: DMP-003 manual model entry on discovery failure & DMP-004 save as default and reload persistence", async ({ page }) => {
  test.setTimeout(120_000);

  // Start an HTTP mock server that returns 500 error for /v1/models to simulate discovery failure
  const mockServer = http.createServer((_req, res) => {
    res.writeHead(500, { "Content-Type": "application/json" });
    res.end(JSON.stringify({ error: "endpoint unavailable" }));
  });

  await new Promise<void>((resolve) => mockServer.listen(0, "127.0.0.1", resolve));
  const port = (mockServer.address() as { port: number }).port;

  const f = await claudeWorkersHarness({}, { defaultModel: null });

  try {
    await f.open(page);

    const openSettings = async () => {
      await page.getByRole("button", { name: "Managed actions", exact: true }).click();
      await page.getByRole("menuitem", { name: "Provider settings", exact: true }).click();
      const dialog = page.getByRole("dialog", { name: "Provider settings", exact: true });
      await expect(dialog).toBeVisible();
      return dialog;
    };

    // 1. Open Provider Settings
    let settings = await openSettings();

    // 2. Click "+ Add Custom Provider" to switch to Form View
    const addBtn = settings.getByRole("button", { name: "+ Add Custom Provider" });
    await expect(addBtn).toBeVisible();
    await addBtn.click();

    // In Form View, configure provider with the failing mock server URL
    const nameInput = settings.getByLabel("Provider name");
    const baseUrlInput = settings.getByLabel("Base URL");
    await expect(nameInput).toBeVisible();
    await expect(baseUrlInput).toBeVisible();

    await nameInput.fill("Offline Provider");
    await baseUrlInput.fill(`http://127.0.0.1:${port}/v1`);

    // 3. User clicks "Test & Discover Models" and receives an error message (discovery failed)
    const discoverBtn = settings.getByRole("button", { name: "Test & Discover Models" });
    await expect(discoverBtn).toBeVisible();
    await discoverBtn.click();

    // Verify discovery failure error message is displayed
    const errorNotice = settings.locator('[role="alert"]');
    await expect(errorNotice).toBeVisible();
    await expect(errorNotice).toContainText("Model discovery failed");

    // 4. Manually type model tags (e.g. llama3:custom and mistral:7b)
    const manualModelInput = settings.getByLabel("Manual model ID");
    const addModelBtn = settings.getByRole("button", { name: "Add Model" });
    await expect(manualModelInput).toBeVisible();

    await manualModelInput.fill("llama3:custom");
    await addModelBtn.click();
    await expect(settings.getByText("llama3:custom")).toBeVisible();

    await manualModelInput.fill("mistral:7b");
    await addModelBtn.click();
    await expect(settings.getByText("mistral:7b")).toBeVisible();

    // 5. Save successfully
    const saveBtn = settings.getByRole("button", { name: "Save Provider" });
    await expect(saveBtn).toBeVisible();
    await saveBtn.click();

    // Verify dialog transitions back to List View and displays the manual models count
    await expect(settings.getByRole("heading", { name: "Custom Providers" })).toBeVisible();
    await expect(settings.getByText("Offline Provider", { exact: true })).toBeVisible();
    await expect(settings.getByText("2 models", { exact: false })).toBeVisible();

    // Verify provider is created with manual models and they appear in the catalog via /v1/models API
    await expect.poll(async () => {
      const resp = await f.request(page, "/v1/models");
      const models = (resp.body.models || []) as Array<{ provider: string; id: string; name: string }>;
      return (
        models.some((m) => m.provider === "custom-offline-provider" && m.id === "llama3:custom") &&
        models.some((m) => m.provider === "custom-offline-provider" && m.id === "mistral:7b")
      );
    }).toBe(true);

    // 6. DMP-004: In Provider Settings, select the newly created custom model from Default Model picker
    // Open the default model dropdown
    const defaultModelPicker = settings.locator(".default-model-picker");
    await expect(defaultModelPicker).toBeVisible();
    const pickerButton = defaultModelPicker.locator("button.model-current");
    await pickerButton.click();

    // Locate the custom model option in the listbox and click it
    const customModelOption = defaultModelPicker.locator('.model-option:has-text("llama3:custom")');
    await expect(customModelOption).toBeVisible();
    await customModelOption.click();

    // Verify status updates to confirm the new default model
    await expect(settings.locator(".provider-auth-status")).toContainText("New sessions will use llama3:custom");

    // Close the settings dialog
    await settings.getByRole("button", { name: "Close provider settings" }).click();
    await expect(settings).not.toBeVisible();

    // 7. Reload the page, re-open Provider Settings, and verify the custom model is still selected as the active default model
    await page.reload();
    settings = await openSettings();

    // Verify active default model button label displays llama3:custom
    const reloadedPickerButton = settings.locator(".default-model-picker button.model-current");
    await expect(reloadedPickerButton).toContainText("llama3:custom");

    // Also verify via GET /v1/models endpoint
    const checkModelsResp = await f.request(page, "/v1/models");
    expect(checkModelsResp.body.default_model).toEqual({
      provider: "custom-offline-provider",
      id: "llama3:custom",
    });

    // 8. Clean up the provider at the end of the test
    const deleteBtn = settings.locator('.icon-btn-round[aria-label="Delete Offline Provider"]');
    await expect(deleteBtn).toBeVisible();
    await deleteBtn.click();

    await expect(settings.getByText("Offline Provider", { exact: true })).not.toBeVisible();
    await expect(settings.getByText("No custom providers configured yet.")).toBeVisible();

    // Verify catalog no longer has custom-offline-provider
    await expect.poll(async () => {
      const resp = await f.request(page, "/v1/models");
      const models = (resp.body.models || []) as Array<{ provider: string; id: string; name: string }>;
      return models.some((m) => m.provider === "custom-offline-provider");
    }).toBe(false);

    expect(f.violations).toEqual([]);
  } finally {
    await f.close();
    await new Promise<void>((resolve) => mockServer.close(() => resolve()));
  }
});
