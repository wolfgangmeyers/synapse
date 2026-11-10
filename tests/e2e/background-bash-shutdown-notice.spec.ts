import { test, expect } from "@playwright/test";
import { once } from "node:events";
import { join } from "node:path";
import { claudeWorkersHarness, exists } from "./claude-workers-harness";

test.skip(process.platform !== "linux", "Background bash launch is supported only on Linux");

test("router shutdown attempts the ordinary completion notice before closing SQLite", async ({ page }) => {
  test.setTimeout(120_000);
  const f = await claudeWorkersHarness();
  const ready = join(f.fixtureRoot, "shutdown-ready");
  try {
    await f.configure({ turns_by_input: [{ contains: "Launch shutdown job", steps: [
      { mcp_call: { id: "launch", name: "launch_background_bash", input: { command: `printf ready > '${ready}'; exec sleep 30` }, require_catalog: true } },
      { text: "Job launched" },
    ] }] });
    await f.open(page);
    const agent = await f.launch(page);
    await f.userTurn(page, "Launch shutdown job");
    const result = () => f.rows("SELECT CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? AND kind IN ('toolResult','tool_result') ORDER BY rowid", agent)
      .map(row => JSON.parse(String(row.body))).find(row => row.toolName === "launch_background_bash");
    await expect.poll(() => !!result(), { timeout: 15_000 }).toBe(true);
    expect(result().isError).toBeFalsy();
    const jobId = JSON.parse(result().content[0].text).job_id;
    await expect.poll(() => exists(ready), { timeout: 15_000 }).toBe(true);
    await f.idle(agent);
    const child = f.router.process;
    const stopped = once(child, "close");
    child.kill("SIGTERM");
    await stopped;
    const rows = f.rows("SELECT CAST(body AS TEXT) AS body FROM messages WHERE id=?", `background-bash-completed-${jobId}`);
    expect(rows.map(row => row.body)).toEqual([`Background bash job ${jobId} finished: killed`]);
    expect(f.violations).toEqual([]);
  } finally {
    await f.close();
  }
});
