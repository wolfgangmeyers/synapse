import { test, expect } from "@playwright/test";
import { claudeWorkersHarness, type ClaudeWorkersHarness } from "./claude-workers-harness";

test("CC-015 large stderr permits checked finals across fresh executions", async ({ page }) => {
  test.setTimeout(60_000);
  let f: ClaudeWorkersHarness | undefined;
  try {
    const stderr = "s".repeat(65_536) + "!";
    f = await claudeWorkersHarness({ turns: [[{ stderr }, { text: "Bounds foreground settled" }]] });
    await f.open(page);
    const agent = await f.launch(page);
    await f.userTurn(page, "Establish bounds foreground consumption");
    await f.final(page, agent, "Bounds foreground settled");
    const acks = f.tableRows("embedded_acks", agent);
    expect(acks).toHaveLength(1);
    await f.configure({ turns: [[{ text: "Bounds retry checked" }]] });
    await f.userTurn(page, "Continue after stderr flood");
    await f.final(page, agent, "Bounds retry checked");
    expect(await f.starts()).toHaveLength(2);
    const after = f.tableRows("embedded_acks", agent);
    expect(after).toHaveLength(2);
    expect(after).toContainEqual(acks[0]);
    expect(new Set(after.map(row => row.message_id)).size).toBe(2);
    expect(f.requests).toEqual([]); expect(f.violations).toEqual([]);
  } finally { await f?.close(); }
});
