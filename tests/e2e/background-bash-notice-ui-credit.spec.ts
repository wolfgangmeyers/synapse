import { test, expect } from "@playwright/test";
import { writeFile } from "node:fs/promises";
import { join } from "node:path";
import { performance } from "node:perf_hooks";
import { claudeWorkersHarness } from "./claude-workers-harness";

test.skip(process.platform !== "linux", "Background bash launch is supported only on Linux");

test("completion notice and header activity preserve the shared polling pool across turns", async ({ page }) => {
  test.setTimeout(120_000);
  const f = await claudeWorkersHarness();
  const release = join(f.fixtureRoot, "notice-credit-release");
  const runningRelease = join(f.fixtureRoot, "running-credit-release");
  const quote = (s: string) => `'${s.replaceAll("'", "'\\''")}'`;
  const call = (id: string, name: string, input: object) => ({ mcp_call: { id, name, input, require_catalog: true } });
  const results = (agent: string) => f.rows("SELECT CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? AND kind IN ('toolResult','tool_result') ORDER BY rowid", agent).map(row => JSON.parse(String(row.body)));
  try {
    await f.configure({ turns_by_input: [{ contains: "Launch credit jobs", steps: [
      call("notice-job", "launch_background_bash", { command: `i=0; while [ ! -f ${quote(release)} ] && [ "$i" -lt 1200 ]; do sleep 0.1; i=$((i + 1)); done` }),
      call("running-job", "launch_background_bash", { command: `i=0; while [ ! -f ${quote(runningRelease)} ] && [ "$i" -lt 1200 ]; do sleep 0.1; i=$((i + 1)); done` }),
      call("pre-notice-list", "list_background_bash_jobs", {}),
      { text: "Credit jobs launched" },
    ] }] });
    await f.open(page);
    const agent = await f.launch(page);
    // Bound the whole observation: a refill must not hide a debit by UI or notice.
    const start = performance.now();
    await f.userTurn(page, "Launch credit jobs");
    await f.final(page, agent, "Credit jobs launched");
    const launches = results(agent).filter(r => r.toolName === "launch_background_bash");
    expect(launches).toHaveLength(2);
    expect(launches.every(r => !r.isError), JSON.stringify(launches)).toBe(true);
    const [finished, running] = launches.map(r => JSON.parse(r.content[0].text).job_id);
    expect(typeof finished).toBe("string");
    expect(typeof running).toBe("string");
    expect(running).not.toBe(finished);
    const beforeNotice = results(agent).filter(r => r.toolName === "list_background_bash_jobs");
    expect(beforeNotice).toHaveLength(1);
    expect(beforeNotice[0].isError).toBeFalsy();
    expect(JSON.parse(beforeNotice[0].content[0].text)).toEqual(expect.arrayContaining([expect.objectContaining({ job_id: running, status: "running" })]));
    const widget = page.getByRole("region", { name: "Background shell jobs" });
    await widget.getByRole("button", { name: /shell jobs 2/i }).click();
    await expect(widget.locator(".background-bash-job-list li")).toHaveCount(2);
    const projected = await f.request(page, `/v1/managed-agents/${encodeURIComponent(agent)}`, undefined, "GET");
    const entries = projected.body.agent.background_bash_jobs as { id: string; started_at: string }[];
    const expected = [...entries].sort((a, b) => Date.parse(b.started_at) - Date.parse(a.started_at)).map(entry => {
      const date = new Date(entry.started_at);
      return `Started ${date.toLocaleString()} (${String(date.getMilliseconds()).padStart(3, "0")} ms)`;
    });
    await expect(widget.locator(".background-bash-job-list li button span:first-child")).toHaveText(expected);
    await expect(widget.locator(".background-bash-job-list")).toHaveCSS("grid-template-columns", /.+ .+/);
    await writeFile(release, "release");
    await widget.getByLabel("Filter:").selectOption("completed");
    await expect(widget.locator(".background-bash-job-list li").first()).toContainText("succeeded", { timeout: 15_000 });
    await expect.poll(async () => JSON.stringify(await f.inputs()), { timeout: 15_000 }).toContain(`Background bash job ${finished} finished`);
    await page.getByRole("button", { name: "Close dialog" }).click();
    await f.idle(agent);
    await f.configure({ turns_by_input: [{ contains: "Check remaining credits", steps: [
      call("list-1", "list_background_bash_jobs", {}),
      call("get-1", "get_background_bash_job", { job_id: running }),
      call("get-denied", "get_background_bash_job", { job_id: running }),
      { text: "Remaining credits checked" },
    ] }] });
    await f.userTurn(page, "Check remaining credits");
    await f.final(page, agent, "Remaining credits checked");
    expect(performance.now() - start, "no credit can refill during this audit").toBeLessThan(60_000);
    const reads = results(agent).filter(r => ["list_background_bash_jobs", "get_background_bash_job"].includes(r.toolName));
    expect(reads).toHaveLength(4);
    expect(reads.map(r => !!r.isError)).toEqual([false, false, false, true]);
    expect(JSON.parse(reads[0].content[0].text)).toEqual(expect.arrayContaining([expect.objectContaining({ job_id: running, status: "running" })]));
    expect(JSON.parse(reads[1].content[0].text)).toEqual(expect.arrayContaining([expect.objectContaining({ job_id: running, status: "running" })]));
    expect(JSON.parse(reads[2].content[0].text)).toMatchObject({ job_id: running, status: "running" });
    expect(JSON.stringify(reads[3])).toContain("wait for the completion notification or continue other work");
    expect(f.violations).toEqual([]);
  } finally {
    await Promise.all([writeFile(release, "release"), writeFile(runningRelease, "release")]).catch(() => {});
    await f.close();
  }
});
