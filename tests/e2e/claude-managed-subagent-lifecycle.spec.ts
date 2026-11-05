import { test, expect } from "@playwright/test";
import { claudeWorkersHarness } from "./claude-workers-harness";

const call = (id: string, name: string, input: object) => ({ mcp_call: { id, name, input } });
const text = (value: string) => ({ text: value });

test("Claude managed subagent lifecycle and restriction", async ({ page }) => {
  test.setTimeout(120_000);
  const spawn = { prompt: "Return child evidence", description: "Lifecycle child", max_turns: 3 };
  const f = await claudeWorkersHarness({ continue_after_mcp_error: true, executions_by_input: [
    { contains: "Launch lifecycle child", turns: [[call("spawn-one", "subagent", spawn), text("Spawn requested")]] },
    { contains: "finished with status", turns: [[text("Child notification received")]] },
  ] });
  try {
    f.replies.push({ answer: "Deterministic child evidence" });
    await f.open(page);
    const agent = await f.launch(page);
    await f.userTurn(page, "Launch lifecycle child");
    await f.final(page, agent, "Spawn requested");
    await expect.poll(() => f.rows("SELECT agent_id,status,error FROM managed_subagents WHERE parent_agent_id=?", agent), { timeout: 20_000 })
      .toEqual([{ agent_id: expect.any(String), status: "completed", error: "" }]);
    const child = String(f.rows("SELECT agent_id FROM managed_subagents WHERE parent_agent_id=?", agent)[0].agent_id);
    await expect.poll(() => f.rows("SELECT body FROM messages WHERE recipient=? AND id LIKE 'subagent-completed-%'", agent))
      .toEqual([{ body: expect.stringContaining("Deterministic child evidence") }]);
    await expect.poll(async () => JSON.stringify(await f.inputs()), { timeout: 20_000 }).toContain("Deterministic child evidence");
    await f.final(page, agent, "Child notification received");
    const notice = f.rows("SELECT id,state FROM messages WHERE recipient=? AND id LIKE 'subagent-completed-%'", agent)[0];
    expect(notice.state).not.toBe("queued");
    await expect.poll(() => f.tableRows("embedded_acks", agent).some(row => JSON.stringify(row).includes(String(notice.id))), { timeout: 20_000 }).toBe(true);
    await f.idle(agent);
    await f.configure({ continue_after_mcp_error: true, executions_by_input: [
      { contains: "Retrieve lifecycle child", turns: [[call("result-one", "get_subagent_result", { agent_id: child }), text("Result retrieved")]] },
      { contains: "Denied lifecycle child", turns: [[call("spawn-denied", "subagent", spawn), text("Denied spawn finished")]] },
      { contains: "finished with status", turns: [[text("Child notification received")]] },
    ] });
    await f.userTurn(page, "Retrieve lifecycle child");
    await f.final(page, agent, "Result retrieved");
    const results = () => f.rows("SELECT CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? AND kind IN ('toolResult','tool_result')", agent).map(row => JSON.parse(String(row.body)));
    expect(results().find(row => row.toolName === "subagent" && !row.isError)).toBeTruthy();
    const result = results().find(row => row.toolName === "get_subagent_result" && !row.isError);
    expect(JSON.stringify(result)).toContain("Deterministic child evidence");
    await page.getByRole("button", { name: "More agent controls" }).click();
    await page.getByRole("menu", { name: "Agent controls menu" }).getByRole("button", { name: "Worker tools", exact: true }).click();
    const toggle = page.getByRole("switch", { name: "subagent", exact: true });
    await expect(toggle).toBeAttached();
    if (await toggle.isChecked()) await toggle.locator("xpath=ancestor::label").click();
    await expect(toggle).not.toBeChecked();
    await page.getByRole("button", { name: "Close worker tools" }).click();
    await f.userTurn(page, "Denied lifecycle child");
    await f.final(page, agent, "Denied spawn finished");
    expect(results().some(row => row.toolName === "subagent" && row.isError)).toBe(true);
    expect(f.rows("SELECT agent_id FROM managed_subagents WHERE parent_agent_id=?", agent)).toHaveLength(1);
    expect(f.violations).toEqual([]);
  } finally { await f.close(); }
});


test("Claude spawn requires a router default", async ({ page }) => {
  test.setTimeout(90_000);
  const f = await claudeWorkersHarness({ continue_after_mcp_error: true, turns: [[
    call("no-default", "subagent", { prompt: "Must not start" }), text("No default handled"),
  ]] }, { defaultModel: null });
  try {
    await f.open(page);
    const agent = await f.launch(page);
    await f.userTurn(page, "Try without a default");
    await f.final(page, agent, "No default handled");
    const results = f.rows("SELECT CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? AND kind IN ('toolResult','tool_result')", agent).map(row => JSON.parse(String(row.body)));
    expect(results.some(row => row.toolName === "subagent" && row.isError && JSON.stringify(row).includes("configured router default model"))).toBe(true);
    expect(f.rows("SELECT agent_id FROM managed_subagents WHERE parent_agent_id=?", agent)).toEqual([]);
    expect(f.violations).toEqual([]);
  } finally { await f.close(); }
});
