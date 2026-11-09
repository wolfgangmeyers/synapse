import { readFile, writeFile } from "node:fs/promises";
import { test, expect } from "@playwright/test";
import { claudeWorkersHarness } from "./claude-workers-harness";

const call = (id: string, name: string, input: object) => ({ mcp_call: { id, name, input, require_catalog: true } });
const text = (value: string) => ({ text: value });

// SPEC-CLAUDE-DEFAULT-001 CD-006: real browser/router/SQLite/WS, fake local CLI.
test("delegates a policy-scoped Claude-default child and retrieves its persisted result", async ({ page }) => {
  test.setTimeout(120_000);
  const evidence = "Confined file evidence 8e1b";
  const answer = "Child completed with scoped evidence 8e1b";
  const f = await claudeWorkersHarness({ executions_by_input: [
    { contains: "Launch default child", turns: [[call("spawn-child", "subagent", { prompt: "Read child-evidence.txt for child task", description: "Claude default child", max_turns: 3 }), text("Child requested")]] },
    { contains: "Read child-evidence.txt for child task", turns: [[call("child-read", "read", { path: "child-evidence.txt" }), text(answer)]] },
    { contains: "finished with status", turns: [[text("Child completion received")]] },
  ] }, { defaultModel: null });
  try {
    await writeFile(`${f.router.root}/work/child-evidence.txt`, evidence);
    await f.open(page);
    expect((await f.request(page, "/v1/settings/default-model", { model: { provider: "claude-code", id: "haiku" } }, "PUT")).status).toBe(200);
    const parent = await f.launch(page);
    await f.userTurn(page, "Launch default child");
    await expect.poll(() => f.rows("SELECT agent_id,status,result_text,error FROM managed_subagents WHERE parent_agent_id=?", parent), { timeout: 30_000 })
      .toEqual([{ agent_id: expect.any(String), status: "completed", result_text: expect.stringContaining(answer), error: "" }]);
    const child = String(f.rows("SELECT agent_id FROM managed_subagents WHERE parent_agent_id=?", parent)[0].agent_id);
    expect(f.tableRows("managed_agents", child)[0]).toMatchObject({ selected_model_provider: "claude-code", selected_model_id: "haiku" });
    const records = await f.records();
    const childInputs = records.filter(row => row.kind === "stdin" && typeof row.line === "string" && JSON.parse(row.line).type === "user" && row.line.includes("Read child-evidence.txt for child task"));
    expect(childInputs).toHaveLength(1);
    const childStart = records.find(row => row.kind === "start" && row.pid === childInputs[0].pid);
    expect(childStart).toBeTruthy();
    const argv = childStart!.argv;
    if (!Array.isArray(argv)) throw Error("missing child process argv");
    expect(argv.filter(arg => arg === "--tools")).toHaveLength(1);
    expect(argv[argv.indexOf("--tools") + 1]).toBe("Bash,Read,Write,Edit,Glob,Grep,ToolSearch");
    const entries = () => f.rows("SELECT CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? AND kind IN ('toolResult','tool_result')", child).map(row => JSON.parse(String(row.body)));
    expect(entries().some(row => row.toolName === "read" && !row.isError && JSON.stringify(row).includes(evidence))).toBe(true);
    await expect.poll(() => f.rows("SELECT id,state,body FROM messages WHERE recipient=? AND id LIKE 'subagent-completed-%'", parent), { timeout: 20_000 })
      .toEqual([{ id: expect.any(String), state: expect.any(String), body: expect.stringContaining(answer) }]);
    const notice = f.rows("SELECT id FROM messages WHERE recipient=? AND id LIKE 'subagent-completed-%'", parent)[0];
    await expect.poll(() => f.tableRows("embedded_acks", parent).some(row => JSON.stringify(row).includes(String(notice.id))), { timeout: 20_000 }).toBe(true);
    await expect.poll(() => f.wsFrames.some(frame => frame.includes(answer)), { timeout: 20_000 }).toBe(true);
    await f.final(page, parent, "Child completion received");
    await f.configure({ executions_by_input: [
      { contains: "Retrieve default child", turns: [[call("retrieve-child", "get_subagent_result", { agent_id: child }), text("Child result retrieved")]] },
    ] });
    await f.userTurn(page, "Retrieve default child");
    await f.final(page, parent, "Child result retrieved");
    const parentResults = f.rows("SELECT CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? AND kind IN ('toolResult','tool_result')", parent).map(row => JSON.parse(String(row.body)));
    expect(parentResults.some(row => row.toolName === "get_subagent_result" && !row.isError && JSON.stringify(row).includes(answer))).toBe(true);
    expect(f.rows("SELECT id FROM messages WHERE recipient=? AND id LIKE 'subagent-completed-%'", parent)).toHaveLength(1);
    expect(f.violations).toEqual([]);
  } finally { await f.close(); }
});

// CD-007: stopping a Claude child during a real tool call settles once.
test("stops a Claude-default child running a tool", async ({ page }) => {
  test.setTimeout(90_000);
  const f = await claudeWorkersHarness({ executions_by_input: [
    { contains: "Start cancellable child", turns: [[call("spawn-cancel", "subagent", { prompt: "Run cancellable child command", description: "Cancellable child" }), text("Child started")]] },
    { contains: "Run cancellable child command", turns: [[call("long-command", "bash", { command: "echo child-started > started.txt; sleep 30" }), text("Unexpected completion")]] },
    { contains: "was stopped", turns: [[text("Child stop received")]] },
  ] }, { defaultModel: null });
  try {
    await f.open(page);
    expect((await f.request(page, "/v1/settings/default-model", { model: { provider: "claude-code", id: "haiku" } }, "PUT")).status).toBe(200);
    const parent = await f.launch(page);
    await f.userTurn(page, "Start cancellable child");
    await f.final(page, parent, "Child started");
    await expect.poll(() => f.rows("SELECT status FROM managed_subagents WHERE parent_agent_id=?", parent), { timeout: 20_000 }).toEqual([{ status: "running" }]);
    await expect.poll(async () => { try { return await readFile(`${f.router.root}/work/started.txt`, "utf8"); } catch { return ""; } }, { timeout: 20_000 }).toContain("child-started");
    await page.getByRole("button", { name: /Subagents/ }).click();
    await expect(page.getByText("Cancellable child", { exact: true })).toBeVisible();
    await page.getByRole("button", { name: "Stop", exact: true }).click();
    await expect.poll(() => f.rows("SELECT status FROM managed_subagents WHERE parent_agent_id=?", parent), { timeout: 10_000 }).toEqual([{ status: "stopped" }]);
    await expect.poll(() => f.rows("SELECT id FROM messages WHERE recipient=? AND id LIKE 'subagent-terminated-%'", parent).length, { timeout: 10_000 }).toBe(1);
    expect(f.rows("SELECT id FROM messages WHERE recipient=? AND id LIKE 'subagent-completed-%'", parent)).toHaveLength(0);
    expect(f.violations).toEqual([]);
  } finally { await f.close(); }
});
