import { test, expect } from "@playwright/test";
import { claudeWorkersHarness } from "./claude-workers-harness";

test.skip(process.platform !== "linux", "background jobs require Linux");

test("managed child explicitly manages its own background job but cannot access its parent's job", async ({ page }) => {
  test.setTimeout(120_000);
  const f = await claudeWorkersHarness();
  const call = (id: string, name: string, input: object) => ({ mcp_call: { id, name, input, require_catalog: true } });
  const response = async (id: string) => (await f.records()).find(r => r.kind === "mcp_call_response" && r.tool_id === id) as any;
  const checked = async (id: string) => {
    await expect.poll(async () => !!(await response(id)), { timeout: 30_000 }).toBe(true);
    const result = await response(id);
    expect(result.error, id).toBeNull();
    return result.result;
  };
  const payload = (result: any) => JSON.parse(result.content[0].text);
  try {
    await f.open(page);
    expect(new URL(page.url()).origin).not.toBe(new URL(f.proxy.url).origin);
    expect(new URL(page.url()).origin).not.toBe(new URL(f.router.httpUrl).origin);
    expect((await f.request(page, "/v1/settings/default-model", { model: { provider: "claude-code", id: "haiku" } }, "PUT")).status).toBe(200);
    const parent = await f.launch(page);
    await f.configure({ turns: [[call("parent-launch", "launch_background_bash", { command: "exec sleep 120" }), { text: "Parent job launched" }]] });
    await f.userTurn(page, "Start parent job");
    await f.final(page, parent, "Parent job launched");
    const parentLaunch = await checked("parent-launch");
    expect(parentLaunch.isError).toBeFalsy();
    const foreign = payload(parentLaunch).job_id;
    expect(foreign).toEqual(expect.any(String));
    expect(foreign.length).toBeGreaterThan(0);

    await f.configure({ executions_by_input: [
      { contains: "Spawn explicit child", turns: [[call("spawn", "subagent", { prompt: "Explicit child background task", description: "Explicit background child" }), { text: "Child requested" }]] },
      { contains: "Explicit child background task", turns: [[
        call("child-launch", "launch_background_bash", { command: "exec sleep 120" }),
        call("child-list", "list_background_bash_jobs", {}),
        call("child-get", "get_background_bash_job", { job_id: "$LAST_JOB_ID" }),
        call("foreign-get", "get_background_bash_job", { job_id: foreign }),
        call("foreign-kill", "kill_background_bash_job", { job_id: foreign }),
        call("child-kill", "kill_background_bash_job", { job_id: "$LAST_JOB_ID" }),
        { text: "Child explicit job checked" },
      ]] },
      { contains: "finished with status", turns: [[{ text: "Child completion received" }]] },
    ] });
    await f.userTurn(page, "Spawn explicit child");
    await expect.poll(() => f.rows("SELECT agent_id,status FROM managed_subagents WHERE parent_agent_id=?", parent), { timeout: 30_000 })
      .toEqual([{ agent_id: expect.any(String), status: "completed" }]);
    const child = String(f.rows("SELECT agent_id FROM managed_subagents WHERE parent_agent_id=?", parent)[0].agent_id);
    expect(child).not.toBe(parent);
    const childEntries = JSON.stringify(f.rows("SELECT kind, CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=?", child));
    for (const name of ["launch_background_bash", "list_background_bash_jobs", "get_background_bash_job", "kill_background_bash_job"]) {
      expect(childEntries).toContain(name);
    }
    const launch = await checked("child-launch");
    expect(launch.isError).toBeFalsy();
    const job = payload(launch).job_id;
    expect(job).toEqual(expect.any(String));
    expect(job).not.toBe(foreign);
    const list = await checked("child-list");
    expect(list.isError).toBeFalsy();
    expect(JSON.stringify(list.content)).toContain(job);
    expect(JSON.stringify(list.content)).not.toContain(foreign);
    const get = await checked("child-get");
    expect(get.isError).toBeFalsy();
    expect(payload(get)).toMatchObject({ job_id: job, status: "running" });
    for (const id of ["foreign-get", "foreign-kill"]) {
      const denied = await checked(id);
      expect(denied.isError, id).toBe(true);
      expect(JSON.stringify(denied.content)).toContain("background bash job not found");
      expect(JSON.stringify(denied.content)).not.toContain(foreign);
    }
    const kill = await checked("child-kill");
    expect(kill.isError).toBeFalsy();
    expect(payload(kill)).toMatchObject({ job_id: job, status: "killed" });
    await f.configure({ turns: [[call("parent-verify", "get_background_bash_job", { job_id: foreign }), call("parent-cleanup", "kill_background_bash_job", { job_id: foreign }), { text: "Parent job checked" }]] });
    await f.userTurn(page, "Verify parent job");
    await f.final(page, parent, "Parent job checked");
    const verify = await checked("parent-verify");
    expect(verify.isError).toBeFalsy();
    expect(payload(verify)).toMatchObject({ job_id: foreign, status: "running" });
    const cleanup = await checked("parent-cleanup");
    expect(cleanup.isError).toBeFalsy();
    expect(payload(cleanup)).toMatchObject({ job_id: foreign, status: "killed" });
    expect(f.violations).toEqual([]);
  } finally { await f.close(); }
});
