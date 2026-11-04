import { test, expect } from "@playwright/test";
import { claudeWorkersHarness } from "./claude-workers-harness";

const answer = (text: string, tokens: number) => [{ emit: { type: "assistant", message: {
  id: text, type: "message", role: "assistant", model: "claude-sonnet-5-5",
  content: [{ type: "text", text }], stop_reason: "end_turn", stop_sequence: null,
  usage: { input_tokens: tokens, cache_creation_input_tokens: 0, cache_read_input_tokens: 0, output_tokens: 1 },
} } }];

test("reported window drives UI and thresholds; missing report falls back", async ({ page }) => {
  test.setTimeout(90000);
  const f = await claudeWorkersHarness({ contextWindow: 200000, resultModel: "claude-sonnet-5-5", turns: [answer("Window observed", 30000)] });
  try {
    await f.open(page);
    expect((await f.request(page, "/v1/settings/context-thresholds", { warning_percent: 20, reset_percent: 50, model_thresholds: {} }, "PUT")).status).toBe(200);
    const agent = await f.launch(page, { initial_instruction: "Start window test" });
    await f.final(page, agent, "Window observed");
    await expect.poll(() => f.tableRows("managed_agents", agent)[0]?.context_window).toBe(200000);
    await expect(page.getByText(/of 200k/i).first()).toBeVisible();
    await f.userTurn(page, "another turn");
    await expect.poll(async () => (await f.inputs()).length).toBeGreaterThanOrEqual(2);
    await expect.poll(() => f.tableRows("managed_agents", agent)[0]?.context_window).toBe(200000);
  } finally { await f.close(); }
});

test("invalid window falls back to one million", async ({ page }) => {
  test.setTimeout(90000);
  const f = await claudeWorkersHarness({ contextWindow: 0, turns: [answer("Fallback observed", 30000)] });
  try {
    await f.open(page);
    const agent = await f.launch(page, { initial_instruction: "Start fallback test" });
    await f.final(page, agent, "Fallback observed");
    await expect.poll(() => f.tableRows("managed_agents", agent)[0]?.context_window).toBe(1000000);
    await expect(page.getByText(/of 1M/i).first()).toBeVisible();
  } finally { await f.close(); }
});

test("reported window triggers warning and reset at configured percentages", async ({ page }) => {
  test.setTimeout(90000);
  // Each foreground execution starts a fresh fixture process; route by input
  // rather than relying on a turn counter shared across processes.
  const f = await claudeWorkersHarness({ contextWindow: 200000, turns: [answer("Below warning", 30000)], turns_by_input: [
    { contains: "cross warning", steps: answer("Warning crossing", 40000) },
    { contains: "cross reset", steps: answer("Reset crossing", 100000) },
  ] });
  try {
    await f.open(page);
    expect((await f.request(page, "/v1/settings/context-thresholds", { warning_percent: 20, reset_percent: 50, model_thresholds: {} }, "PUT")).status).toBe(200);
    const agent = await f.launch(page, { initial_instruction: "Threshold start" });
    await f.final(page, agent, "Below warning");
    await f.userTurn(page, "cross warning");
    await f.final(page, agent, "Warning crossing");
    await expect.poll(() => f.rows("SELECT * FROM managed_complete_history WHERE agent_id=? AND message='Context usage threshold warning'", agent).length).toBe(1);
    await f.userTurn(page, "cross reset");
    await expect.poll(() => f.tableRows("managed_agents", agent)[0]?.session_epoch, { timeout: 20000 }).toBe(2);
  } finally { await f.close(); }
});
