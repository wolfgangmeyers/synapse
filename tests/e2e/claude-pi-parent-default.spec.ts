import { test, expect } from "@playwright/test";
import { claudeWorkersHarness } from "./claude-workers-harness";
import { model } from "./workflow-implicit-goals-harness";

// CD-006: a running Pi parent keeps its model while a new child snapshots the switched default.
test("Pi parent delegates using the current Claude default", async ({ page }) => {
  test.setTimeout(120_000);
  const answer = "Claude child switch complete";
  const f = await claudeWorkersHarness({ executions_by_input: [
    { contains: "Claude switch child task", turns: [[{ text: answer }]] },
  ] });
  try {
    await f.open(page);
    const parent = await f.launch(page, { model });
    expect((await f.request(page, "/v1/settings/default-model", { model: { provider: "claude-code", id: "haiku" } }, "PUT")).status).toBe(200);
    f.replies.push(
      { tool: "subagent", args: { prompt: "Claude switch child task", description: "Switch child", max_turns: 3 } },
      { answer: "Child requested" },
      { answer: "Child notice received" },
    );
    await f.userTurn(page, "Delegate the switch child");
    await expect.poll(() => f.rows("SELECT a.selected_model_provider AS provider,a.selected_model_id AS id,s.status,s.result_text FROM managed_agents a JOIN managed_subagents s ON s.agent_id=a.agent_id WHERE s.parent_agent_id=?", parent), { timeout: 30_000 })
      .toEqual([{ provider: "claude-code", id: "haiku", status: "completed", result_text: expect.stringContaining(answer) }]);
    expect(f.tableRows("managed_agents", parent)[0]).toMatchObject({ selected_model_provider: model.provider, selected_model_id: model.id });
    await expect.poll(() => f.rows("SELECT id FROM messages WHERE recipient=? AND id LIKE 'subagent-completed-%'", parent).length, { timeout: 20_000 }).toBe(1);
    expect(f.violations).toEqual([]);
  } finally { await f.close(); }
});
