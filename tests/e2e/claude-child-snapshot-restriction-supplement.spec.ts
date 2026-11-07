import { test, expect } from "@playwright/test";
import { claudeWorkersHarness } from "./claude-workers-harness";

const call = (id: string, name: string, input: object, require_catalog = false) =>
  ({ mcp_call: { id, name, input, require_catalog } });

// CD-006/007: hold a real child across a default change, then attempt to
// delegate from its child-scoped MCP grant. The fake CLI is the external edge.
test("Claude child keeps its admitted model and cannot spawn a nested child", async ({ page }) => {
  test.setTimeout(120_000);
  const answer = "Snapshot child finished 7bf2";
  const f = await claudeWorkersHarness({ continue_after_mcp_error: true, executions_by_input: [
    { contains: "Launch snapshot child", turns: [[call("spawn", "subagent", { prompt: "Snapshot child task", description: "Snapshot child" }, true), { text: "Spawn requested" }]] },
    { contains: "Snapshot child task", turns: [[{ barrier: "child-held" }, call("nested", "subagent", { prompt: "Forbidden nested task", description: "Forbidden nested child" }), { text: answer }]] },
    { contains: "finished with status", turns: [[{ text: "Snapshot notice received" }]] },
  ] }, { defaultModel: null });
  try {
    await (await import("node:fs/promises")).writeFile(`${f.router.root}/work/evidence.txt`, "snapshot evidence");
    await f.open(page);
    expect((await f.request(page, "/v1/settings/default-model", { model: { provider: "claude-code", id: "haiku" } }, "PUT")).status).toBe(200);
    const parent = await f.launch(page);
    await f.userTurn(page, "Launch snapshot child");
    await expect.poll(async () => (await f.records()).some(row => row.kind === "barrier" && row.name === "child-held"), { timeout: 30_000 }).toBe(true);
    const receipt = (await f.records()).find(row => row.kind === "barrier" && row.name === "child-held")!;
    const children = () => f.rows("SELECT agent_id,status,result_text,error FROM managed_subagents WHERE parent_agent_id=?", parent);
    expect(children()).toHaveLength(1);
    const child = String(children()[0].agent_id);
    expect(f.tableRows("managed_agents", child)[0]).toMatchObject({ selected_model_provider: "claude-code", selected_model_id: "haiku" });
    const start = (await f.records()).find(row => row.kind === "start" && row.pid === receipt.pid);
    expect(start).toBeDefined();
    expect(Array.isArray(start!.argv)).toBe(true);
    const argv = start!.argv as string[];
    expect(argv.indexOf("--model")).toBeGreaterThanOrEqual(0);
    expect(argv[argv.indexOf("--model") + 1]).toBe("haiku");
    expect((await f.request(page, "/v1/settings/default-model", { model: { provider: "claude-code", id: "sonnet" } }, "PUT")).status).toBe(200);
    await f.releaseBarrier(receipt);
    await expect.poll(() => children(), { timeout: 30_000 }).toEqual([{ agent_id: child, status: "failed", result_text: expect.any(String), error: expect.stringContaining("unacknowledged tool result") }]);
    expect(f.tableRows("managed_agents", child)[0]).toMatchObject({ selected_model_provider: "claude-code", selected_model_id: "haiku" });
    expect(f.rows("SELECT agent_id FROM managed_subagents WHERE parent_agent_id=?", child)).toEqual([]);
    const records = await f.records();
    const catalog = records.find(row => row.kind === "catalog" && row.pid === receipt.pid);
    expect(catalog).toBeDefined();
    expect(catalog!.names).toContain("read");
    expect(catalog!.names).not.toContain("subagent");
    const denied = records.filter(row => row.kind === "mcp_call_response" && row.pid === receipt.pid && row.tool_id === "nested");
    expect(denied).toEqual([expect.objectContaining({
      name: "subagent", error: { code: -32602, message: "invalid tool or arguments" }, result: null,
    })]);
    const notificationId = `subagent-completed-${child}-1`;
    await expect.poll(() => f.rows("SELECT id, sender, subject, body, state FROM messages WHERE recipient=?", parent).filter(row => row.id === notificationId), { timeout: 30_000 }).toEqual([
      expect.objectContaining({
        id: notificationId, sender: "system", subject: `[System Notice] Subagent Failed: ${child}`,
        body: expect.stringContaining(`Task ${child} (Snapshot child) finished with status failed: Claude child completed with unacknowledged tool result`),
      }),
    ]);
    await expect.poll(async () => (await f.inputs()).map(input => JSON.stringify(input)).find(input => input.includes(`Task ${child} (Snapshot child) finished with status failed:`)), { timeout: 30_000 }).toBeDefined();
    const notice = (await f.inputs()).map(input => JSON.stringify(input)).find(input => input.includes(`Task ${child} (Snapshot child) finished with status failed:`))!;
    expect(notice).toContain("unacknowledged tool result");
    expect(notice).not.toContain(answer);
    expect(f.violations).toEqual([]);
  } finally { await f.close(); }
});
