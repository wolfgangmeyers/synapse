import { test, expect } from "@playwright/test";
import { readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { claudeWorkersHarness } from "./claude-workers-harness";

test.skip(process.platform !== "linux", "background jobs require Linux");
test("Claude child promotes the same shell and waits for its completion before settling", async ({ page }) => {
  test.setTimeout(120_000);
  const f = await claudeWorkersHarness();
  const release = join(f.fixtureRoot, "child-job-release");
  const starts = join(f.fixtureRoot, "starts");
  const ends = join(f.fixtureRoot, "ends");
  const quote = (s: string) => `'${s.replaceAll("'", "'\\''")}'`;
  const call = (id: string, name: string, input: object) => ({ mcp_call: { id, name, input, require_catalog: true } });
  try {
    await f.open(page);
    expect((await f.request(page, "/v1/settings/default-model", { model: { provider: "claude-code", id: "haiku" } }, "PUT")).status).toBe(200);
    const parent = await f.launch(page);
    await f.configure({ executions_by_input: [
      { contains: "Spawn background child", turns: [[call("spawn", "subagent", { prompt: "Child background exercise", description: "Background child" }), { text: "Spawned" }]] },
      { contains: "Child background exercise", turns: [[
        call("child-bash", "bash", { command: `echo $$ >> ${quote(starts)}; echo before-promotion; while [ ! -f ${quote(release)} ]; do sleep .05; done; echo $$ >> ${quote(ends)}; echo after-promotion; exit 7`, timeout: 1 }),
        { barrier: "promoted" },
        call("child-wait", "wait_background_bash_job", { job_id: "$LAST_JOB_ID" }),
        { text: "Child finished waiting" },
      ]] },
      { contains: "finished with status", turns: [[{ text: "Parent received child completion" }]] },
    ] });
    await f.userTurn(page, "Spawn background child");
    await expect.poll(async () => (await f.records()).some(r => r.kind === "barrier" && r.name === "promoted"), { timeout: 30_000 }).toBe(true);
    const child = String(f.rows("SELECT agent_id FROM managed_subagents WHERE parent_agent_id=?", parent)[0].agent_id);
    const response = (id: string) => f.records().then(rs => rs.find(r => r.kind === "mcp_call_response" && r.tool_id === id));
    const promoted = await response("child-bash") as any;
    expect(promoted.error).toBeNull();
    expect(promoted.result.isError).toBeFalsy();
    const job = JSON.parse(promoted.result.content[0].text);
    expect(job).toMatchObject({ job_id: expect.any(String), status: "running" });
    const startPID = (await readFile(starts, "utf8")).trim();
    expect(startPID).toMatch(/^\d+$/);
    const receipt = (await f.records()).find(r => r.kind === "barrier" && r.name === "promoted")!;
    await f.releaseBarrier(receipt);
    await expect.poll(async () => (await f.records()).some(r => r.kind === "mcp_call_started" && r.tool_id === "child-wait"), { timeout: 20_000 }).toBe(true);
    expect(await response("child-wait")).toBeUndefined();
    expect(f.rows("SELECT status FROM managed_subagents WHERE agent_id=?", child)[0].status).not.toBe("completed");
    expect(f.rows("SELECT body FROM messages WHERE recipient=? AND id LIKE 'subagent-completed-%'", parent)).toHaveLength(0);
    await writeFile(release, "release");
    await expect.poll(async () => !!(await response("child-wait")), { timeout: 30_000 }).toBe(true);
    const waited = await response("child-wait") as any;
    expect(waited.error).toBeNull();
    expect(waited.result.isError).toBeFalsy();
    expect(JSON.parse(waited.result.content[0].text)).toMatchObject({ job_id: job.job_id, status: "failed", exit_code: 7, output: expect.stringContaining("before-promotion") });
    expect(JSON.parse(waited.result.content[0].text).output).toContain("after-promotion");
    expect((await readFile(starts, "utf8")).trim().split("\n")).toEqual([startPID]);
    expect((await readFile(ends, "utf8")).trim()).toBe(startPID);
    await expect.poll(() => f.rows("SELECT status FROM managed_subagents WHERE agent_id=?", child), { timeout: 30_000 }).toEqual([{ status: "completed" }]);
    await expect.poll(() => f.rows("SELECT body FROM messages WHERE recipient=? AND id LIKE 'subagent-completed-%'", parent), { timeout: 20_000 }).toEqual([{ body: expect.stringContaining("Child finished waiting") }]);
    expect(f.violations).toEqual([]);
  } finally { await writeFile(release, "release").catch(() => {}); await f.close(); }
});
