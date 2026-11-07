import { test, expect } from "@playwright/test";
import { claudeWorkersHarness } from "./claude-workers-harness";
import { model } from "./workflow-implicit-goals-harness";

// CD-002: prove selection by running a turn, not only by checking admission.
test("explicit Pi executes over a saved Claude default", async ({ page }) => {
  test.setTimeout(120_000);
  const f = await claudeWorkersHarness({}, { defaultModel: null, omitGoogle: true, expectedCodexModels: [model.id, "gpt-5.3-codex-spark"] });
  try {
    await f.open(page);
    const saved = { provider: "claude-code", id: "haiku" };
    expect((await f.request(page, "/v1/settings/default-model", { model: saved }, "PUT")).status).toBe(200);
    const agent = await f.launch(page, { model });
    expect(f.tableRows("managed_agents", agent)[0]).toMatchObject({ selected_model_provider: model.provider, selected_model_id: model.id });
    f.replies.push({ answer: "Explicit Pi answered" });
    await f.userTurn(page, "Use explicit Pi");
    await f.final(page, agent, "Explicit Pi answered");
    expect(f.requests).toHaveLength(1);
    expect(f.requests[0].model).toBe(model.id);
    expect(await f.starts()).toEqual([]);
    expect((await f.request(page, "/v1/models")).body.default_model).toMatchObject(saved);

    expect((await f.request(page, "/v1/settings/default-model", { model: null }, "PUT")).status).toBe(200);
    await page.reload();
    const catalog = (await f.request(page, "/v1/models")).body;
    expect(catalog.default_model).toBeNull();
    const first = catalog.models.find((entry: { provider: string; available: boolean }) => entry.provider !== "claude-code" && entry.available);
    expect(first).toMatchObject({ provider: "openai-codex", id: "gpt-5.3-codex-spark" });
    const automatic = await f.launch(page, { model: undefined });
    expect(f.tableRows("managed_agents", automatic)[0]).toMatchObject({ selected_model_provider: first.provider, selected_model_id: first.id });
    f.replies.push({ answer: "Automatic Pi answered" });
    await f.userTurn(page, "Use automatic Pi");
    await f.final(page, automatic, "Automatic Pi answered");
    expect(f.requests).toHaveLength(2);
    expect(f.requests[1].model).toBe(first.id);
    expect(await f.starts()).toEqual([]);
    expect(f.violations).toEqual([]);
  } finally { await f.close(); }
});

test("explicit Claude executes over a saved Pi default", async ({ page }) => {
  test.setTimeout(120_000);
  const f = await claudeWorkersHarness({ turns: [[{ text: "Explicit Claude answered" }]] });
  try {
    await f.open(page);
    const agent = await f.launch(page, { model: { provider: "claude-code", id: "haiku" } });
    expect(f.tableRows("managed_agents", agent)[0]).toMatchObject({ selected_model_provider: "claude-code", selected_model_id: "haiku" });
    await f.userTurn(page, "Use explicit Claude");
    await f.final(page, agent, "Explicit Claude answered");
    expect((await f.starts())[0].argv).toEqual(expect.arrayContaining(["--model", "haiku"]));
    expect(f.requests).toEqual([]);
    expect((await f.request(page, "/v1/models")).body.default_model).toMatchObject(model);
    expect(f.violations).toEqual([]);
  } finally { await f.close(); }
});

test("unset default selects the first available Pi catalog model", async ({ page }) => {
  test.setTimeout(120_000);
  const f = await claudeWorkersHarness({}, { defaultModel: null });
  try {
    await f.open(page);
    const catalog = (await f.request(page, "/v1/models")).body;
    expect(catalog.default_model).toBeNull();
    const first = catalog.models.find((entry: { provider: string; available: boolean }) => entry.provider !== "claude-code" && entry.available);
    expect(first).toBeTruthy();
    const agent = await f.launch(page, { model: undefined });
    expect(f.tableRows("managed_agents", agent)[0]).toMatchObject({ selected_model_provider: first.provider, selected_model_id: first.id });
    // The first model is Google in this fixture; its provider is not mocked.
    // Do not send a turn that could leave the isolated test boundary.
    expect(f.requests).toEqual([]);
    expect(await f.starts()).toEqual([]);
    expect(f.violations).toEqual([]);
  } finally { await f.close(); }
});
