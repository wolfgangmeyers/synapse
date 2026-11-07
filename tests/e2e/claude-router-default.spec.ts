import { test, expect } from "@playwright/test";
import { claudeWorkersHarness } from "./claude-workers-harness";

// CD-001/002: worker selection complements the empty-provider root restart test.
test("saved Claude default survives reload and explicit Claude selection takes precedence", async ({ page }) => {
  test.setTimeout(120_000);
  const f = await claudeWorkersHarness({ turns: [[{ text: "Default worker answered" }]] }, { defaultModel: null });
  try {
    await f.open(page);
    const model = { provider: "claude-code", id: "haiku" };
    expect((await f.request(page, "/v1/settings/default-model", { model }, "PUT")).status).toBe(200);
    await page.reload();
    expect((await f.request(page, "/v1/models")).body.default_model).toMatchObject(model);
    const defaultWorker = await f.launch(page, { model: undefined });
    expect(f.tableRows("managed_agents", defaultWorker)[0]).toMatchObject({ selected_model_provider: "claude-code", selected_model_id: "haiku" });
    await f.userTurn(page, "Answer using the saved default");
    await f.final(page, defaultWorker, "Default worker answered");
    expect((await f.starts())[0].argv).toEqual(expect.arrayContaining(["--model", "haiku"]));
    const explicitWorker = await f.launch(page, { model: { provider: "claude-code", id: "sonnet" } });
    expect(f.tableRows("managed_agents", explicitWorker)[0]).toMatchObject({ selected_model_provider: "claude-code", selected_model_id: "sonnet" });
    expect((await f.request(page, "/v1/models")).body.default_model).toMatchObject(model);
    expect(f.violations).toEqual([]);
  } finally { await f.close(); }
});

test("unavailable default Claude fails without fallback and clearing restores Pi selection", async ({ page }) => {
  test.setTimeout(90_000);
  const f = await claudeWorkersHarness({ auth: { loggedIn: false } }, { defaultModel: null });
  try {
    await f.open(page);
    expect((await f.request(page, "/v1/settings/default-model", { model: { provider: "claude-code", id: "haiku" } }, "PUT")).status).toBe(200);
    const launch = async (name: string) => f.request(page, "/v1/managed-agents", { name, cwd: `${f.router.root}/work`, runtime: "embedded" });
    const signedOut = await launch("default-signed-out");
    expect(signedOut.status).toBe(422);
    expect(signedOut.body.error.code).toBe("CLAUDE_UNAVAILABLE");
    expect(signedOut.body.error.message).toMatch(/host|install|sign.in/i);
    expect(f.tableRows("managed_agents", "default-signed-out")).toEqual([]);
    await f.breakExecutable();
    const missing = await launch("default-missing-cli");
    expect(missing.status).toBe(422);
    expect(missing.body.error.code).toBe("CLAUDE_UNAVAILABLE");
    expect(f.tableRows("managed_agents", "default-missing-cli")).toEqual([]);
    expect(await f.starts()).toEqual([]);
    expect((await f.request(page, "/v1/settings/default-model", { model: null }, "PUT")).status).toBe(200);
    await page.reload();
    expect((await f.request(page, "/v1/models")).body.default_model).toBeNull();
    const pi = await launch("automatic-pi");
    expect(pi.status, JSON.stringify(pi.body)).toBe(202);
    expect(f.tableRows("managed_agents", "automatic-pi")[0].selected_model_provider).not.toBe("claude-code");
    expect(f.violations).toEqual([]);
  } finally { await f.close(); }
});
