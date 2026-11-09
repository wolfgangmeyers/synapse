import { test, expect } from "@playwright/test";
import { claudeWorkersHarness } from "./claude-workers-harness";

const call = (id: string, name: string, input: object) => ({ mcp_call: { id, name, input } });
const text = (value: string) => ({ text: value });

test("native Claude Bash results settle a managed child and reach its parent", async ({ page }) => {
  test.setTimeout(90_000);
  const answer = "Native Bash child answer 91ab";
  const f = await claudeWorkersHarness({ executions_by_input: [
    { contains: "Launch native bash child", turns: [[call("spawn", "subagent", { prompt: "Native bash child task", description: "Native Bash child" }), text("Child requested")]] },
    { contains: "Native bash child task", turns: [[
      { tool: { id: "native-ok", name: "Bash", input: { command: "printf native-success-91ab" }, result: "native-success-91ab" } },
      { tool: { id: "native-error", name: "Bash", input: { command: "exit 7" }, result: "native-error-91ab", is_error: true } },
      text(answer),
    ]] },
    { contains: "finished with status", turns: [[text(`Native child notification received: ${answer}`)]] },
  ] }, { defaultModel: null });
  try {
    await f.open(page);
    expect((await f.request(page, "/v1/settings/default-model", { model: { provider: "claude-code", id: "haiku" } }, "PUT")).status).toBe(200);
    const parent = await f.launch(page);
    await f.userTurn(page, "Launch native bash child");
    await expect.poll(() => f.rows("SELECT agent_id,status,result_text,error FROM managed_subagents WHERE parent_agent_id=?", parent), { timeout: 20_000 })
      .toEqual([{ agent_id: expect.any(String), status: "completed", result_text: expect.stringContaining(answer), error: "" }]);
    const child = String(f.rows("SELECT agent_id FROM managed_subagents WHERE parent_agent_id=?", parent)[0].agent_id);
    const entries = f.rows("SELECT kind,CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=?", child);
    const serialized = JSON.stringify(entries);
    expect(serialized).toContain("native-success-91ab");
    expect(serialized).toContain("native-error-91ab");
    expect(entries.some(row => /tool_?result/i.test(String(row.kind)) && JSON.stringify(JSON.parse(String(row.body))).includes("native-error-91ab") && JSON.parse(String(row.body)).isError === true)).toBe(true);
    await expect.poll(() => f.rows("SELECT body FROM messages WHERE recipient=? AND id LIKE 'subagent-completed-%'", parent), { timeout: 20_000 })
      .toEqual([{ body: expect.stringContaining(answer) }]);
    await expect.poll(async () => JSON.stringify(await f.inputs()), { timeout: 20_000 }).toContain(answer);
    await f.final(page, parent, `Native child notification received: ${answer}`);
    expect(f.violations).toEqual([]);
  } finally { await f.close(); }
});
