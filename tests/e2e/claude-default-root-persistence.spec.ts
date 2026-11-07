import { randomUUID } from "node:crypto";
import { test, expect } from "@playwright/test";
import { claudeWorkersHarness } from "./claude-workers-harness";
import { model as piModel } from "./workflow-implicit-goals-harness";

// CD-003: root launch snapshots the default; later default changes do not change its engine.
test("omitted-model Claude root retains its model across default change and restart", async ({ page }) => {
  test.setTimeout(120_000);
  const f = await claudeWorkersHarness({ turns_by_input: [
    { contains: "Before default switch", steps: [{ text: "Claude before switch" }] },
    { contains: "After default switch", steps: [{ text: "Claude after switch" }] },
  ] }, { defaultModel: null });
  const agent = `default-root-${randomUUID()}`;
  try {
    await f.open(page);
    expect((await f.request(page, "/v1/settings/default-model", { model: { provider: "claude-code", id: "sonnet" } }, "PUT")).status).toBe(200);
    const launched = await f.request(page, "/v1/managed-agents", { name: agent, cwd: `${f.router.root}/work`, runtime: "embedded" }, "POST", true);
    expect(launched.status, JSON.stringify(launched.body)).toBe(202);
    await expect(page.locator(`[data-agent-id="${agent}"]`)).toBeVisible();
    await page.locator(`[data-agent-id="${agent}"]`).click();
    await f.userTurn(page, "Before default switch");
    await f.final(page, agent, "Claude before switch");
    expect(f.tableRows("managed_agents", agent)[0]).toMatchObject({ selected_model_provider: "claude-code", selected_model_id: "sonnet" });
    expect((await f.request(page, "/v1/settings/default-model", { model: piModel }, "PUT")).status).toBe(200);
    const rejected = await f.request(page, `/v1/managed-agents/${agent}/restart`, { model: piModel });
    expect(rejected.status).toBe(422);
    expect(rejected.body.error.code).toBe("INVALID_ENGINE_SELECTION");
    expect(rejected.body.error.message).toBe("Cross-engine model changes are not supported; launch a new worker");
    expect(f.tableRows("managed_agents", agent)[0]).toMatchObject({ selected_model_provider: "claude-code", selected_model_id: "sonnet" });
    await f.restart();
    await page.reload();
    expect((await f.request(page, "/v1/models")).body.default_model).toMatchObject(piModel);
    expect(f.tableRows("managed_agents", agent)[0]).toMatchObject({ selected_model_provider: "claude-code", selected_model_id: "sonnet" });
    await expect(page.locator(`[data-agent-id="${agent}"]`)).toBeVisible();
    await page.locator(`[data-agent-id="${agent}"]`).click();
    await f.userTurn(page, "After default switch");
    await f.final(page, agent, "Claude after switch");
    const starts = await f.starts();
    expect(starts.length).toBeGreaterThanOrEqual(2);
    const argv = starts.at(-1)?.argv ?? [];
    const modelIndex = argv.indexOf("--model");
    expect(modelIndex).toBeGreaterThanOrEqual(0);
    expect(argv[modelIndex + 1]).toBe("sonnet");
    expect(f.violations).toEqual([]);
  } finally { await f.close(); }
});
