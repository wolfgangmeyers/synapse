import { test, expect } from "@playwright/test";
import { claudeWorkersHarness } from "./claude-workers-harness";

// Browser -> proxy -> real router/SQLite -> fake Claude -> private MCP.
test("Claude exposes shared tools and applies worker policy without restarting", async ({ page }) => {
  test.setTimeout(90_000);
  const f = await claudeWorkersHarness({ continue_after_mcp_error: true, executions_by_input: [
    { contains: "Discovery", turns: [[{ barrier: "discovery" }, { mcp_call: { id: "live-kb", name: "kb_list", input: {} } }, { text: "Ready for policy" }]] },
    { contains: "Denied knowledge", turns: [[{ mcp_call: { id: "kb-denied", name: "kb_list", input: {} } }, { text: "Denied complete" }]] },
    { contains: "Workflow denied", turns: [[{ mcp_call: { id: "kb-workflow", name: "kb_list", input: {} } }, { text: "Workflow denied complete" }]] },
    { contains: "Allowed knowledge", turns: [[{ mcp_call: { id: "kb-one", name: "kb_list", input: {} } }, { text: "Knowledge listed" }]] },
  ] });
  try {
    await f.open(page); const agent = await f.launch(page);
    await f.userTurn(page, "Discovery");
    await expect.poll(async () => (await f.records()).some(r => r.kind === "barrier" && r.name === "discovery")).toBe(true);
    const barrier = (await f.records()).find(r => r.kind === "barrier" && r.name === "discovery")!;
    const grant = await f.grant();
    const response = await fetch(grant.url, { method: "POST", headers: { ...grant.headers, "Content-Type": "application/json" }, body: JSON.stringify({ jsonrpc: "2.0", id: 1, method: "tools/list" }) });
    const catalog = (await response.json()).result.tools.map((tool: {name: string}) => tool.name);
    expect(catalog).toEqual(expect.arrayContaining(["kb_list", "kb_read", "mecha_workflow_transition", "mecha_tool_timeout"]));
    expect(catalog).not.toContain("bash");
    const starts = (await f.starts()).length;
    await page.getByRole("button", { name: "More agent controls" }).click();
    const menu = page.getByRole("menu", { name: "Agent controls menu" });
    await expect(menu.getByRole("button", { name: "Inline tools", exact: true })).toHaveCount(0);
    await expect(menu.getByRole("button", { name: "Workflow", exact: true })).toBeVisible();
    await menu.getByRole("button", { name: "Worker tools", exact: true }).click();
    await expect(page.getByText(/Claude native tools are not restricted/)).toBeVisible();
    const toggle = page.getByRole("switch", { name: "kb_list", exact: true });
    await expect(toggle).toBeAttached();
    if (await toggle.isChecked()) await toggle.locator("xpath=ancestor::label").click();
    await expect(toggle).not.toBeChecked();
    await page.getByRole("button", { name: "Close worker tools" }).click();
    expect((await f.starts()).length).toBe(starts);
    await f.releaseBarrier(barrier); await f.final(page, agent, "Ready for policy");
    expect(f.rows("SELECT CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? AND kind IN ('toolResult','tool_result')", agent).map(r => JSON.parse(String(r.body))).some(r => r.toolName === "kb_list" && r.isError)).toBe(true);
    await f.userTurn(page, "Denied knowledge"); await f.final(page, agent, "Denied complete");
    expect(f.rows("SELECT CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? AND kind IN ('toolResult','tool_result')", agent).map(r => JSON.parse(String(r.body))).some(r => r.toolName === "kb_list" && r.isError)).toBe(true);
    await page.getByRole("button", { name: "More agent controls" }).click();
    await page.getByRole("menu", { name: "Agent controls menu" }).getByRole("button", { name: "Worker tools", exact: true }).click();
    await toggle.locator("xpath=ancestor::label").click();
    await expect(toggle).toBeChecked();
    await page.getByRole("button", { name: "Close worker tools" }).click();
    await f.userTurn(page, "Allowed knowledge"); await f.final(page, agent, "Knowledge listed");
    const rows = f.rows("SELECT CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? AND kind IN ('toolResult','tool_result')", agent).map(r => JSON.parse(String(r.body)));
    expect(rows.some(r => r.toolName === "kb_list" && !r.isError)).toBe(true);
    const calls = f.rows("SELECT CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? AND kind='assistant'", agent).map(r => JSON.parse(String(r.body)));
    expect(calls.some(r => r.content?.some((part: { type?: string; name?: string }) => part.type === "toolCall" && part.name === "kb_list"))).toBe(true);
    const workflow = await f.api("/v1/workflows", { name: "Claude policy" });
    const { workflow_state: state } = await f.api("/v1/workflow-states", { workflow_id: workflow.id, name: "Restricted", allowed_tools: ["scratchpad"], allow_exit: true });
    await f.api(`/v1/managed-agents/${agent}/workflow-state`, { action: "activate", workflow_state_id: state.id }, "PUT");
    await f.userTurn(page, "Workflow denied"); await f.final(page, agent, "Workflow denied complete");
    const afterWorkflow = f.rows("SELECT CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? AND kind IN ('toolResult','tool_result')", agent).map(r => JSON.parse(String(r.body)));
    expect(afterWorkflow.filter(r => r.toolName === "kb_list" && r.isError)).toHaveLength(3);
    expect(f.violations).toEqual([]);
  } finally { await f.close(); }
});

test("Claude denies a checked tool when worker policy storage fails", async ({ page }) => {
  test.setTimeout(90_000);
  const f = await claudeWorkersHarness({ continue_after_mcp_error: true, turns: [[
    { barrier: "before-policy-read" },
    { mcp_call: { id: "policy-failure", name: "scratchpad", input: { action: "append", content: "must not persist" } } },
    { text: "Policy failure handled" },
  ]] });
  try {
    await f.open(page);
    const agent = await f.launch(page);
    await f.userTurn(page, "Test policy storage failure");
    await expect.poll(async () => (await f.records()).some(r => r.kind === "barrier" && r.name === "before-policy-read")).toBe(true);
    const barrier = (await f.records()).find(r => r.kind === "barrier" && r.name === "before-policy-read")!;
    // Fault only the isolated fixture database, after admission and before the checked call.
    const { DatabaseSync } = await import("node:sqlite");
    const db = new DatabaseSync(f.router.databasePath);
    try { db.exec("PRAGMA busy_timeout = 5000; DROP TABLE managed_agent_tools"); } finally { db.close(); }
    await f.releaseBarrier(barrier);
    await f.final(page, agent, "Policy failure handled");
    const results = f.rows("SELECT CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? AND kind IN ('toolResult','tool_result')", agent).map(r => JSON.parse(String(r.body)));
    expect(results.some(r => r.toolName === "scratchpad" && r.isError && JSON.stringify(r).includes("worker tool policy lookup failed"))).toBe(true);
    expect(f.tableRows("scratchpad_entries", agent)).toEqual([]);
    expect(f.violations).toEqual([]);
  } finally { await f.close(); }
});
