import { test, expect } from "@playwright/test";
import { claudeWorkersHarness } from "./claude-workers-harness";

test.skip(process.platform !== "linux", "Background bash launch is supported only on Linux");

// BB-003 / BB-010: exercise capture limits and unknown IDs through the
// managed worker's MCP route, real shell, router, SQLite and browser.
test("large job output is truncated and unknown jobs are rejected", async ({ page }) => {
  test.setTimeout(120_000);
  const f = await claudeWorkersHarness();
  const call = (id: string, name: string, input: object) => ({ mcp_call: { id, name, input, require_catalog: true } });
  const results = (agent: string, name: string) => f.rows("SELECT CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? AND kind IN ('toolResult','tool_result') ORDER BY rowid", agent)
    .map(row => JSON.parse(String(row.body))).filter(row => row.toolName === name);
  try {
    await f.configure({ turns_by_input: [{ contains: "Launch output job", steps: [
      call("launch-output", "launch_background_bash", { command: "head -c 70000 /dev/zero | tr '\\0' x" }),
      { text: "Output job launched" },
    ] }] });
    await f.open(page);
    const agent = await f.launch(page);
    await f.userTurn(page, "Launch output job");
    await f.final(page, agent, "Output job launched");
    const launch = results(agent, "launch_background_bash")[0];
    expect(launch.isError, JSON.stringify(launch)).toBeFalsy();
    const job = JSON.parse(launch.content[0].text);
    expect(typeof job.job_id).toBe("string");
    await page.getByRole("region", { name: "Background shell jobs" }).getByRole("button", { name: /shell jobs/i }).click();
    await page.getByRole("dialog").getByLabel("Filter:").selectOption("completed");
    await expect(page.getByRole("region", { name: "Background shell jobs" })).toContainText("succeeded", { timeout: 10_000 });
    await page.getByRole("button", { name: "Close dialog" }).click();
    await f.configure({ turns_by_input: [{ contains: "Inspect output job", steps: [
      call("list-output", "list_background_bash_jobs", {}),
      call("get-output", "get_background_bash_job", { job_id: job.job_id }),
      call("get-missing", "get_background_bash_job", { job_id: "missing-job-id" }),
      { text: "Output inspected" },
    ] }] });
    await f.userTurn(page, "Inspect output job");
    await f.final(page, agent, "Output inspected");
    const listResult = results(agent, "list_background_bash_jobs")[0];
    expect(listResult.isError, JSON.stringify(listResult)).toBeFalsy();
    const list = JSON.parse(listResult.content[0].text);
    const listed = list.find((entry: { job_id: string }) => entry.job_id === job.job_id);
    expect(listed).toMatchObject({ status: "succeeded", truncated: true });
    expect(listed).not.toHaveProperty("output");
    const gets = results(agent, "get_background_bash_job");
    expect(gets).toHaveLength(2);
    expect(gets[0].isError, JSON.stringify(gets[0])).toBeFalsy();
    const snapshot = JSON.parse(gets[0].content[0].text);
    expect(snapshot).toMatchObject({ job_id: job.job_id, status: "succeeded", exit_code: 0, truncated: true });
    expect(snapshot.output).toBe("x".repeat(65536));
    expect(gets[1].isError, JSON.stringify(gets[1])).toBe(true);
    expect(JSON.stringify(gets[1].content)).toContain("background bash job not found");
    expect(f.violations).toEqual([]);
  } finally { await f.close(); }
});
