import { randomUUID } from "node:crypto";
import { test, expect } from "@playwright/test";
import { claudeWorkersHarness } from "./claude-workers-harness";

test("CD-003 root uses checked role tools and resumes its snapshot after router restart", async ({ page }) => {
  test.setTimeout(120_000);
  const name = `root-${randomUUID()}`;
  const f = await claudeWorkersHarness({ turns_by_input: [{ contains: "Root bootstrap", steps: [{ text: "Root booted" }] }] }, {
    embeddedRoot: { name, requestId: randomUUID(), model: { provider: "claude-code", id: "sonnet" }, instruction: "Root bootstrap" },
  });
  try {
    await f.open(page);
    await expect(page.locator(`[data-agent-id="${name}"]`)).toBeVisible();
    await page.locator(`[data-agent-id="${name}"]`).click();
    await f.final(page, name, "Root booted", undefined, false);
    expect(f.rows("SELECT role FROM embedded_sessions WHERE agent_id=? AND current=1", name)).toEqual([{ role: "root" }]);
    const first = (await f.starts())[0];
    expect(first.systemPrompt).toContain("embedded root running");
    expect(first.systemPrompt).not.toContain("embedded worker running");
    expect(first.systemPrompt).toContain(name);
    expect(first.systemPrompt).toContain("### Responsiveness & Communication");
    expect(first.systemPrompt).toContain("always acknowledge the user with a brief conversational response first");
    expect(first.argv).toEqual(expect.arrayContaining(["--tools", "--strict-mcp-config", "--model", "sonnet"]));
    const argv = first.argv as string[];
    expect(argv[argv.indexOf("--tools") + 1]).toBe("Bash,Read,Write,Edit,Glob,Grep,ToolSearch");
    expect(argv).not.toContain("--setting-sources");
    await f.configure({ turns_by_input: [{ contains: "Record root fact", steps: [
      { mcp_call: { id: "root-scratch", name: "scratchpad", input: { action: "append", content: "root durable fact" }, require_catalog: true } },
      { text: "Root fact saved" },
    ] }] });
    await f.userTurn(page, "Record root fact");
    await f.final(page, name, "Root fact saved");
    expect(f.tableRows("scratchpad_entries", name).map(row => row.content)).toContain("root durable fact");
    await f.api(`/v1/managed-agents/${name}/tools`, { tool_name: "scratchpad", enabled: false }, "PUT");
    await f.configure({ turns_by_input: [{ contains: "Denied root tool", steps: [
      { mcp_call: { id: "root-denied", name: "scratchpad", input: { action: "append", content: "forbidden root fact" } } },
      { text: "Root denial handled" },
    ] }] });
    await f.userTurn(page, "Denied root tool");
    await f.final(page, name, "Root denial handled");
    expect(f.tableRows("scratchpad_entries", name).map(row => row.content)).not.toContain("forbidden root fact");
    expect(f.rows("SELECT CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? AND kind IN ('toolResult','tool_result')", name).map(row => JSON.parse(String(row.body))).some(row => row.toolName === "scratchpad" && row.isError)).toBe(true);
    await f.api(`/v1/managed-agents/${name}/tools`, { tool_name: "scratchpad", enabled: true }, "PUT");
    await f.configure({ turns_by_input: [{ contains: "Run slow root tool", steps: [
      { mcp_call: { id: "root-slow", name: "bash", input: { command: "sleep 6; printf root-slow-complete" }, require_catalog: true } },
      { text: "Slow root tool finished" },
    ] }] });
    await f.userTurn(page, "Run slow root tool");
    await expect(page.getByText("Slow root tool finished", { exact: true }).first()).toBeVisible({ timeout: 20_000 });
    await f.final(page, name, "Slow root tool finished");
    expect(f.rows("SELECT CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? AND kind IN ('toolResult','tool_result')", name).map(row => JSON.parse(String(row.body))).some(row => row.toolName === "bash" && !row.isError && JSON.stringify(row).includes("root-slow-complete"))).toBe(true);
    const childPrompt = "Root delegated evidence";
    f.replies.push({ answer: "Root child result" });
    await f.configure({ executions_by_input: [
      { contains: "Delegate root task", turns: [[{ mcp_call: { id: "root-delegate", name: "subagent", input: { prompt: childPrompt, description: "Root delegated task" }, require_catalog: true } }, { text: "Root delegated" }]] },
      { contains: "finished with status", turns: [[{ text: "Root child notification" }]] },
    ] });
    await f.userTurn(page, "Delegate root task");
    await f.final(page, name, "Root delegated");
    await expect.poll(() => f.rows("SELECT status FROM managed_subagents WHERE parent_agent_id=?", name), { timeout: 20_000 }).toEqual([{ status: "completed" }]);
    await expect.poll(() => f.rows("SELECT body FROM messages WHERE recipient=? AND id LIKE 'subagent-completed-%'", name)).toEqual([{ body: expect.stringContaining("Root child result") }]);
    await f.final(page, name, "Root child notification");
    const before = f.rows("SELECT selected_model_provider, selected_model_id FROM managed_agents WHERE agent_id=?", name);
    await f.configure({ turns_by_input: [{ contains: "After recovery", steps: [{ text: "Root recovered" }] }] });
    await f.restart();
    await expect.poll(() => f.tableRows("managed_agents", name)[0]?.accepting_prompts, { timeout: 20_000 }).toBe(1);
    await page.reload();
    await expect(page.locator(`[data-agent-id="${name}"]`)).toBeVisible();
    await page.locator(`[data-agent-id="${name}"]`).click();
    await f.userTurn(page, "After recovery");
    await f.final(page, name, "Root recovered");
    expect(f.rows("SELECT selected_model_provider, selected_model_id FROM managed_agents WHERE agent_id=?", name)).toEqual(before);
    expect(f.rows("SELECT role FROM embedded_sessions WHERE agent_id=? AND current=1", name)).toEqual([{ role: "root" }]);
    await f.api(`/v1/managed-agents/${name}`, undefined, "DELETE");
    await expect.poll(() => f.tableRows("managed_agents", name)[0]?.desired_state).toBe("terminated");
    expect(f.violations).toEqual([]);
  } finally { await f.close(); }
});
