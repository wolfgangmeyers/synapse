import { stat, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { test, expect } from "@playwright/test";
import { claudeWorkersHarness } from "./claude-workers-harness";

const call = (id: string, name: string, input: object) => ({ mcp_call: { id, name, input, require_catalog: true } });
const text = (value: string) => ({ text: value });

test("scheduler slot remains held until cancelled tool cleanup finishes", async ({ page }) => {
  test.setTimeout(150_000);
  const f = await claudeWorkersHarness({ executions_by_input: [
    { contains: "Start held child", turns: [[call("spawn-held", "subagent", { prompt: "Held capacity work", description: "Held capacity child" }), text("Held child submitted")]] },
    { contains: "Start queued child", turns: [[call("spawn-queued", "subagent", { prompt: "Queued capacity work", description: "Queued capacity child" }), text("Queued child submitted")]] },
    { contains: "Held capacity work", turns: [[call("capacity-held", "bash", { command: "echo late" }), text("Late child completion")]] },
    { contains: "Queued capacity work", turns: [[text("Queued child completed")]] },
    { contains: "was stopped", turns: [[text("Stopped notice")]] },
    { contains: "finished with status", turns: [[text("Completion notice")]] },
  ] }, { defaultModel: null, capacityFixture: true });
  const release = () => writeFile(join(f.router.root, "capacity-release"), "release");
  try {
    await f.open(page);
    expect((await f.request(page, "/v1/settings/default-model", { model: { provider: "claude-code", id: "haiku" } }, "PUT")).status).toBe(200);
    const parent = await f.launch(page);
    await f.userTurn(page, "Start held child");
    const children = () => f.rows("SELECT agent_id,parent_agent_id,description,status,turn_count FROM managed_subagents WHERE parent_agent_id IN (?,?) ORDER BY description", parent, second);
    const second = await f.launch(page);
    expect(second).not.toBe(parent);
    await expect.poll(async () => stat(join(f.router.root, "capacity-entered")).then(() => true, () => false), { timeout: 40_000 }).toBe(true);
    await page.locator(`[data-agent-id="${second}"]`).click();
    await f.userTurn(page, "Start queued child");
    await expect.poll(() => children().length, { timeout: 40_000 }).toBe(2);
    const held = children().find(r => r.description === "Held capacity child")!;
    const queued = children().find(r => r.description === "Queued capacity child")!;
    expect(held.parent_agent_id).toBe(parent);
    expect(queued.parent_agent_id).toBe(second);
    await page.locator(".conversation-header-subagents-row").getByRole("button", { name: /^Subagents \d+$/ }).click();
    const queuedCard = page.locator(".subagent-card").filter({ hasText: "Queued capacity child" });
    await expect(queuedCard.locator(".subagent-status-pill")).toHaveText("queued");
    await page.getByRole("button", { name: "Close dialog", exact: true }).click();
    await page.locator(`[data-agent-id="${parent}"]`).click();
    await page.locator(".conversation-header-subagents-row").getByRole("button", { name: /^Subagents \d+$/ }).click();
    const heldCard = page.locator(".subagent-card").filter({ hasText: "Held capacity child" });
    await expect(heldCard.locator(".subagent-status-pill")).toHaveText("running");
    await heldCard.getByRole("button", { name: "Stop", exact: true }).click();
    await expect.poll(() => children().find(r => r.agent_id === held.agent_id)?.status, { timeout: 30_000 }).toBe("stopped");
    await page.locator("#subagent-filter-select").selectOption("all");
    await expect(heldCard.locator(".subagent-status-pill")).toHaveText("stopped");
    // Settlement is prompt, but capacity remains held while cleanup is blocked.
    await expect.poll(() => children().find(r => r.agent_id === queued.agent_id)?.status, { timeout: 10_000 }).toBe("queued");
    expect((await f.inputs()).some(i => JSON.stringify(i).includes("Queued capacity work"))).toBe(false);
    expect(await stat(join(f.router.root, "capacity-returned")).then(() => true, () => false)).toBe(false);
    await release();
    await expect.poll(async () => stat(join(f.router.root, "capacity-returned")).then(() => true, () => false), { timeout: 10_000 }).toBe(true);
    await expect.poll(() => children().find(r => r.agent_id === queued.agent_id)?.status, { timeout: 45_000 }).toBe("completed");
    // The queued child completing proves slot release, which requires bridge cleanup
    // to acquire the tool serialization lock after the late handoff exits.
    expect(children().find(r => r.agent_id === held.agent_id)?.status).toBe("stopped");
    expect(f.rows("SELECT body FROM embedded_entries WHERE agent_id=? AND body LIKE '%capacity-late-success-marker%'", held.agent_id)).toEqual([]);
    expect((await f.inputs()).filter(i => JSON.stringify(i).includes("Queued capacity work"))).toHaveLength(1);
    await page.getByRole("button", { name: "Close dialog", exact: true }).click();
    await page.locator(`[data-agent-id="${second}"]`).click();
    await page.locator(".conversation-header-subagents-row").getByRole("button", { name: /^Subagents \d+$/ }).click();
    await page.locator("#subagent-filter-select").selectOption("all");
    await expect(page.locator(".subagent-card").filter({ hasText: "Queued capacity child" }).locator(".subagent-status-pill")).toHaveText("completed");
    // Retry through the parent model and real MCP boundary after the late return.
    await page.getByRole("button", { name: "Close dialog", exact: true }).click();
    await page.locator(`[data-agent-id="${parent}"]`).click();
    await f.configure({ executions_by_input: [
      { contains: "Retry stopped child", turns: [[call("retry-stopped", "subagent", { resume: held.agent_id, prompt: "Forbidden retry execution" }), text("Retry checked")]] },
      { contains: "was stopped", turns: [[text("Stopped notice")]] },
      { contains: "finished with status", turns: [[text("Completion notice")]] },
    ], continue_after_mcp_error: true });
    await f.userTurn(page, "Retry stopped child");
    await f.final(page, parent, "Retry checked");
    const parentEntries = f.rows("SELECT CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=?", parent);
    expect(parentEntries.some(r => String(r.body).includes("one attempt"))).toBe(true);
    expect(f.rows("SELECT attempt,status FROM managed_subagents WHERE agent_id=?", held.agent_id)).toEqual([{ attempt: 1, status: "stopped" }]);
    expect((await f.inputs()).some(i => JSON.stringify(i).includes("Forbidden retry execution"))).toBe(false);
    const parentHistory = f.rows("SELECT * FROM managed_complete_history WHERE agent_id=?", parent);
    expect(JSON.stringify({ parentEntries, parentHistory, inputs: await f.inputs() })).not.toContain("capacity-late-success-marker");
    expect(f.violations).toEqual([]);
  } finally { await release(); await f.close(); }
});
