import { test, expect } from "@playwright/test";
import { claudeWorkersHarness } from "./claude-workers-harness";

test("view and stop a Claude managed child", async ({ page }) => {
  test.setTimeout(90_000);
  const f = await claudeWorkersHarness({ turns: [[
    { mcp_call: { id: "spawn-view", name: "subagent", input: { prompt: "Wait for stop", description: "Viewer child" } } },
    { text: "Child launched" },
  ]] });
  try {
    f.replies.push({ hold: true });
    await f.open(page);
    const agent = await f.launch(page);
    await f.userTurn(page, "Launch viewer child");
    await f.final(page, agent, "Child launched");
    await expect.poll(() => f.rows("SELECT status FROM managed_subagents WHERE parent_agent_id=?", agent)).toEqual([{ status: "running" }]);
    await page.getByRole("button", { name: /Subagents/ }).click();
    await expect(page.getByText("Viewer child", { exact: true })).toBeVisible();
    await page.getByRole("button", { name: "Stop", exact: true }).click();
    await expect.poll(() => f.rows("SELECT status FROM managed_subagents WHERE parent_agent_id=?", agent), { timeout: 20_000 }).toEqual([{ status: "stopped" }]);
    expect(f.violations).toEqual([]);
  } finally { await f.close(); }
});
