import { readFile, stat } from "node:fs/promises";
import { test, expect } from "@playwright/test";
import { claudeWorkersHarness } from "./claude-workers-harness";

const call = (id: string, name: string, input: object) => ({ mcp_call: { id, name, input, require_catalog: true } });
const text = (value: string) => ({ text: value });

for (const restart of [false, true]) test(`CER-001 stopped child rejects actual historical effect replay${restart ? " after restart" : ""}`, async ({ page }) => {
  test.setTimeout(120_000);
  const effect = call("prior-write", "bash", { command: "printf x >> replay-count.txt" });
  const f = await claudeWorkersHarness({ executions_by_input: [
    { contains: "Start witness child", turns: [[call("spawn-witness", "subagent", { prompt: "Perform witness write", description: "Witness child" }), text("Started witness child")]] },
    { contains: "Perform witness write", turns: [[effect, { barrier: "checked-effect" }]] },
    { contains: "was stopped", turns: [[text("Stopped notice")]] },
  ] }, { defaultModel: null });
  const count = async () => (await readFile(`${f.router.root}/work/replay-count.txt`, "utf8")).length;
  try {
    await f.open(page);
    expect((await f.request(page, "/v1/settings/default-model", { model: { provider: "claude-code", id: "haiku" } }, "PUT")).status).toBe(200);
    const parent = await f.launch(page);
    await f.userTurn(page, "Start witness child");
    await f.final(page, parent, "Started witness child");
    await expect.poll(async () => (await f.records()).some(r => r.kind === "barrier" && r.name === "checked-effect"), { timeout: 30_000 }).toBe(true);
    const child = String(f.rows("SELECT agent_id FROM managed_subagents WHERE parent_agent_id=?", parent)[0].agent_id);
    expect(await count()).toBe(1);
    await page.getByRole("button", { name: /Subagents/ }).click();
    await expect(page.getByText("Witness child", { exact: true })).toBeVisible();
    await page.getByRole("button", { name: "Stop", exact: true }).click();
    await expect.poll(() => f.rows("SELECT status FROM managed_subagents WHERE agent_id=?", child), { timeout: 20_000 }).toEqual([{ status: "stopped" }]);
    await page.keyboard.press("Escape");
    if (restart) { await f.restart(); await page.reload(); await page.locator(`[data-agent-id="${parent}"]`).click(); }
    await f.configure({ executions_by_input: [
      { contains: "Resume witness child", turns: [[call("resume-witness", "subagent", { resume: child, prompt: "Continue witness child" }), text("Resume requested")]] },
      { contains: "Continue witness child", turns: [[effect, text("Replay attempted")]] },
      { contains: "was stopped", turns: [[text("Stopped notice")]] },
      { contains: "finished with status", turns: [[text("Child notice received")]] },
    ], continue_after_mcp_error: true });
    await f.userTurn(page, "Resume witness child");
    await f.final(page, parent, "Resume requested");
    const results = f.rows("SELECT CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? AND kind='toolResult'", parent);
    expect(results.some(r => String(r.body).includes("one attempt"))).toBe(true);
    expect(f.rows("SELECT attempt,status FROM managed_subagents WHERE agent_id=?", child)).toEqual([{ attempt: 1, status: "stopped" }]);
    expect((await f.inputs()).some(i => JSON.stringify(i).includes("Continue witness child"))).toBe(false);
    expect((await f.records()).filter(r => r.kind === "mcp_call_response" && r.tool_id === "prior-write")).toHaveLength(1);
    expect(await count()).toBe(1);
    expect(f.violations).toEqual([]);
  } finally { await f.close(); }
});

for (const restart of [false, true]) test(`CER-002 rejects resume after an interrupted child effect${restart ? " and restart" : ""}`, async ({ page }) => {
  test.setTimeout(120_000);
  const f = await claudeWorkersHarness({ executions_by_input: [
    { contains: "Start uncertain child", turns: [[call("spawn-uncertain", "subagent", { prompt: "Run uncertain command", description: "Uncertain child" }), text("Uncertain child started")]] },
    { contains: "Run uncertain command", turns: [[call("uncertain-command", "bash", { command: "echo started > uncertain.txt; sleep 30" }), text("Unexpected completion")]] },
    { contains: "was stopped", turns: [[text("Stopped notice")]] },
  ] }, { defaultModel: null });
  try {
    await f.open(page);
    expect((await f.request(page, "/v1/settings/default-model", { model: { provider: "claude-code", id: "haiku" } }, "PUT")).status).toBe(200);
    const parent = await f.launch(page);
    await f.userTurn(page, "Start uncertain child");
    await f.final(page, parent, "Uncertain child started");
    await expect.poll(() => f.rows("SELECT agent_id FROM managed_subagents WHERE parent_agent_id=?", parent), { timeout: 20_000 }).not.toEqual([]);
    const child = String(f.rows("SELECT agent_id FROM managed_subagents WHERE parent_agent_id=?", parent)[0].agent_id);
    await expect.poll(() => stat(`${f.router.root}/work/uncertain.txt`).then(() => true, () => false), { timeout: 20_000 }).toBe(true);
    await page.getByRole("button", { name: /Subagents/ }).click();
    await expect(page.getByText("Uncertain child", { exact: true })).toBeVisible();
    await page.getByRole("button", { name: "Stop", exact: true }).click();
    await expect.poll(() => f.rows("SELECT status FROM managed_subagents WHERE agent_id=?", child), { timeout: 15_000 }).toEqual([{ status: "stopped" }]);
    await page.keyboard.press("Escape");
    if (restart) {
      await f.restart();
      await page.reload();
      await page.locator(`[data-agent-id="${parent}"]`).click();
    }
    await f.configure({ executions_by_input: [
      { contains: "Try uncertain resume", turns: [[call("resume-uncertain", "subagent", { resume: child, prompt: "Must not run again" }), text("Resume rejected")]] },
      { contains: "was stopped", turns: [[text("Stopped notice")]] },
    ], continue_after_mcp_error: true });
    await f.userTurn(page, "Try uncertain resume");
    await f.final(page, parent, "Resume rejected");
    const resumeResults = f.rows("SELECT CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? AND kind='toolResult'", parent).map(r => JSON.parse(String(r.body)));
    expect(resumeResults.some(r => r.toolName === "subagent" && r.isError && JSON.stringify(r).includes("one attempt"))).toBe(true);
    expect(f.rows("SELECT attempt FROM managed_subagents WHERE agent_id=?", child)).toEqual([{ attempt: 1 }]);
    expect((await f.inputs()).some(i => JSON.stringify(i).includes("Must not run again"))).toBe(false);
  } finally { await f.close(); }
});
