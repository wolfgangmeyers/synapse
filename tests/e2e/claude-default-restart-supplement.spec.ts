import { test, expect } from "@playwright/test";
import { claudeWorkersHarness } from "./claude-workers-harness";

// CD-001: the saved selection, not an existing agent snapshot, survives restart.
test("saved Claude default selects a new omitted-model worker after router restart", async ({ page }) => {
  test.setTimeout(120_000);
  const f = await claudeWorkersHarness({ turns: [[{ text: "Restart default answered" }]] }, { defaultModel: null });
  try {
    await f.open(page);
    const model = { provider: "claude-code", id: "haiku" };
    expect((await f.request(page, "/v1/settings/default-model", { model }, "PUT")).status).toBe(200);
    await f.restart();
    await page.reload();
    expect((await f.request(page, "/v1/models")).body.default_model).toMatchObject(model);
    const agent = await f.launch(page, { model: undefined });
    expect(f.tableRows("managed_agents", agent)[0]).toMatchObject({ selected_model_provider: "claude-code", selected_model_id: "haiku" });
    await f.userTurn(page, "Answer after router restart");
    await f.final(page, agent, "Restart default answered");
    const argv = (await f.starts())[0].argv;
    expect(Array.isArray(argv)).toBe(true);
    expect(argv).toEqual(expect.arrayContaining(["--model"]));
    if (!Array.isArray(argv)) throw new Error("missing Claude argv");
    expect(argv[argv.indexOf("--model") + 1]).toBe("haiku");
    expect(f.requests).toEqual([]);
    expect(f.violations).toEqual([]);
  } finally { await f.close(); }
});
