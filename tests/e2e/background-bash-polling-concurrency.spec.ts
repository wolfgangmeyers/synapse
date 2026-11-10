import { test, expect } from "@playwright/test";
import { writeFile } from "node:fs/promises";
import { join } from "node:path";
import { claudeWorkersHarness } from "./claude-workers-harness";

test.skip(process.platform !== "linux", "Background bash launch is supported only on Linux");

// Parallel fixture requests exercise the routed batch; they do not prove server-side
// overlap. Atomic concurrent credit consumption is covered by the Go budget test.
test("parallel list and get requests share credits and refill", async ({ page }) => {
  test.setTimeout(240_000);
  const phase = (name: string) => console.log(`[polling-e2e ${new Date().toISOString()}] ${name}`);
  phase("harness start");
  const f = await claudeWorkersHarness();
  phase("harness ready");
  const release = join(f.fixtureRoot, "poll-release");
  const quote = (s: string) => `'${s.replaceAll("'", "'\\''")}'`;
  const tool = (id: string, name: string, input: object) => ({ id, name, input, require_catalog: true });
  const results = async () => (await f.records()).filter(r => r.kind === "mcp_call_response");
  try {
    await f.configure({ turns_by_input: [{ contains: "Start poll job", steps: [
      { mcp_call: tool("launch", "launch_background_bash", { command: `while [ ! -f ${quote(release)} ]; do sleep 0.05; done` }) },
      { text: "Job ready" },
    ] }] });
    phase("open UI");
    await f.open(page);
    phase("launch agent");
    const agent = await f.launch(page);
    phase("start job turn");
    await f.userTurn(page, "Start poll job");
    await f.final(page, agent, "Job ready");
    phase("job final received");
    const launch = (await results()).find(r => r.tool_id === "launch") as { error?: unknown; result: { content: { text: string }[]; isError?: boolean } };
    expect(launch.error).toBeFalsy();
    expect(launch.result.isError).toBeFalsy();
    const job = JSON.parse(launch.result.content[0].text).job_id;
    expect(typeof job).toBe("string");
    const calls = [
      tool("poll-list-1", "list_background_bash_jobs", {}),
      tool("poll-get-1", "get_background_bash_job", { job_id: job }),
      tool("poll-list-2", "list_background_bash_jobs", {}),
      tool("poll-get-2", "get_background_bash_job", { job_id: job }),
      tool("poll-list-3", "list_background_bash_jobs", {}),
      tool("poll-get-3", "get_background_bash_job", { job_id: job }),
    ];
    await f.configure({ turns_by_input: [{ contains: "Poll together", steps: [{ mcp_parallel: calls }, { text: "Polls returned" }] }] });
    const start = performance.now();
    phase("parallel poll turn");
    await f.userTurn(page, "Poll together");
    await expect(page.getByText("Polls returned", { exact: true }).first()).toBeVisible({ timeout: 30_000 });
    await f.final(page, agent, "Polls returned");
    phase("parallel poll final received");
    const batch = (await results()).filter(r => String(r.tool_id).startsWith("poll-"));
    expect(batch).toHaveLength(6);
    expect(performance.now() - start).toBeLessThan(55_000);
    const successes = batch.filter(r => !(r.result as { isError?: boolean }).isError);
    const denied = batch.filter(r => (r.result as { isError?: boolean }).isError);
    expect(successes).toHaveLength(3);
    expect(denied).toHaveLength(3);
    for (const row of batch) {
      expect(row.error, JSON.stringify(row)).toBeFalsy();
      const result = row.result as { isError?: boolean; content: { type: string; text: string }[] };
      expect(result.content).toEqual([expect.objectContaining({ type: "text", text: expect.any(String) })]);
      if (result.isError) {
        expect(result.content[0].text).toContain("wait for the completion notification or continue other work");
      } else {
        const value = JSON.parse(result.content[0].text);
        if (String(row.tool_id).includes("list")) {
          expect(value).toEqual(expect.arrayContaining([expect.objectContaining({ job_id: job, status: "running" })]));
        } else {
          expect(value).toMatchObject({ job_id: job, status: "running" });
        }
      }
    }
    // The last debit can occur at any point in the batch. Wait a full refill
    // interval after it completes, plus a small scheduling margin.
    phase("refill wait start");
    await new Promise(resolve => setTimeout(resolve, 62_000));
    phase("refill wait complete");
    await f.configure({ turns_by_input: [{ contains: "Poll after refill", steps: [{ mcp_parallel: [
      tool("refill-get", "get_background_bash_job", { job_id: job }),
      tool("refill-list", "list_background_bash_jobs", {}),
    ] }, { text: "Refill checked" }] }] });
    phase("refill poll turn");
    await f.userTurn(page, "Poll after refill");
    await f.final(page, agent, "Refill checked");
    phase("refill final received");
    const refill = (await results()).filter(r => String(r.tool_id).startsWith("refill-"));
    // Bound time since before the first debit so a second credit cannot refill.
    expect(performance.now() - start).toBeLessThan(115_000);
    expect(refill).toHaveLength(2);
    expect(refill.filter(r => !(r.result as { isError?: boolean }).isError)).toHaveLength(1);
    expect(refill.filter(r => (r.result as { isError?: boolean }).isError)).toHaveLength(1);
    for (const row of refill) {
      expect(row.error, JSON.stringify(row)).toBeFalsy();
      const result = row.result as { isError?: boolean; content: { type: string; text: string }[] };
      expect(result.content).toEqual([expect.objectContaining({ type: "text", text: expect.any(String) })]);
      if (result.isError) {
        expect(result.content[0].text).toContain("wait for the completion notification or continue other work");
      } else {
        const value = JSON.parse(result.content[0].text);
        if (row.tool_id === "refill-list") expect(value).toEqual(expect.arrayContaining([expect.objectContaining({ job_id: job, status: "running" })]));
        else expect(value).toMatchObject({ job_id: job, status: "running" });
      }
    }
    expect(f.violations).toEqual([]);
  } finally {
    phase("cleanup release");
    await writeFile(release, "release").catch(() => {});
    phase("cleanup close start");
    await f.close();
    phase("cleanup close complete");
  }
});
