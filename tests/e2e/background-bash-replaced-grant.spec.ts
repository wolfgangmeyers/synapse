import { test, expect } from "@playwright/test";
import { readFile } from "node:fs/promises";
import { join } from "node:path";
import { claudeWorkersHarness } from "./claude-workers-harness";

test.skip(process.platform !== "linux", "Background bash launch is supported only on Linux");

test("post-replacement calls with the old Claude grant are denied and leave the successor job running", async ({ page }) => {
  test.setTimeout(90_000);
  const f = await claudeWorkersHarness();
  const marker = join(f.fixtureRoot, "replacement-forbidden-marker");
  const quote = (s: string) => `'${s.replaceAll("'", "'\\''")}'`;
  const call = (id: string, name: string, input: object) => ({ mcp_call: { id, name, input, require_catalog: true } });
  try {
    await f.configure({ turns_by_input: [
      { contains: "Context reset completed", steps: [{ text: "Replacement ready" }] },
      { contains: "Initial grant", steps: [call("old-list", "list_background_bash_jobs", {}), { barrier: "old-grant" }, { text: "Old owner ready" }] },
      { contains: "Start successor job", steps: [call("new-launch", "launch_background_bash", { command: "sleep 120" }), { barrier: "new-grant" }, { text: "Successor job started" }] },
    ] });
    await f.open(page);
    const agent = await f.launch(page, { initial_instruction: "Initial grant" });
    await expect.poll(async () => (await f.records()).some(r => r.kind === "barrier" && r.name === "old-grant"), { timeout: 20_000 }).toBe(true);
    const oldBinding = f.tableRows("managed_agents", agent)[0];
    const oldGrant = await f.grant();
    const oldLive = await f.mcp(oldGrant, "list_background_bash_jobs", {}, "old-list");
    expect(oldLive.status).toBe(200);
    expect(oldLive.body.error, JSON.stringify(oldLive.body)).toBeUndefined();
    expect(oldLive.body.result?.isError).not.toBe(true);
    expect(oldLive.body.result?.content).toEqual([{ type: "text", text: "[]" }]);
    await f.releaseBarrier((await f.records()).find(r => r.kind === "barrier" && r.name === "old-grant")!);
    await f.final(page, agent, "Old owner ready");
    await page.getByRole("button", { name: "More agent controls", exact: true }).click();
    const reset = page.waitForResponse(r => new URL(r.url()).pathname === `/v1/managed-agents/${encodeURIComponent(agent)}/reset` && r.request().method() === "POST");
    await page.getByRole("menu", { name: "Agent controls menu" }).getByRole("button", { name: "Reset", exact: true }).click();
    expect((await reset).status()).toBe(202);
    await f.final(page, agent, "Replacement ready");
    const newBinding = f.tableRows("managed_agents", agent)[0];
    expect(newBinding.session_epoch).toBe(2);
    expect(newBinding.session_epoch).not.toBe(oldBinding.session_epoch);
    if (oldBinding.pi_session_id && newBinding.pi_session_id) {
      expect(newBinding.pi_session_id).not.toBe(oldBinding.pi_session_id);
    }
    await f.userTurn(page, "Start successor job");
    await expect.poll(async () => (await f.records()).some(r => r.kind === "barrier" && r.name === "new-grant"), { timeout: 20_000 }).toBe(true);
    const newGrant = await f.grant();
    expect(newGrant.headers).not.toEqual(oldGrant.headers);
    await f.releaseBarrier((await f.records()).find(r => r.kind === "barrier" && r.name === "new-grant")!);
    await f.final(page, agent, "Successor job started");
    const results = (name: string) => f.rows("SELECT CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? AND kind IN ('toolResult','tool_result') ORDER BY rowid", agent)
      .map(row => JSON.parse(String(row.body))).filter(row => row.toolName === name);
    const launch = results("launch_background_bash").at(-1);
    expect(launch.isError, JSON.stringify(launch)).toBeFalsy();
    const job = JSON.parse(launch.content[0].text).job_id;
    expect(typeof job).toBe("string");
    expect(job.length).toBeGreaterThan(0);
    for (const [name, args] of [
      ["launch_background_bash", { command: `printf forbidden > ${quote(marker)}` }],
      ["list_background_bash_jobs", {}],
      ["get_background_bash_job", { job_id: job }],
      ["kill_background_bash_job", { job_id: job }],
    ] as const) {
      const denied = await f.mcp(oldGrant, name, args, `replaced-${name}`);
      expect(denied.status, JSON.stringify(denied.body)).toBe(401);
      expect(denied.body.error).toEqual({ code: -32001, message: "invalid process grant" });
    }
    // A bounded observation, not a claim that no delayed effect is possible.
    for (let i = 0; i < 10; i++) {
      await expect(readFile(marker)).rejects.toMatchObject({ code: "ENOENT" });
      await new Promise(resolve => setTimeout(resolve, 100));
    }
    await f.configure({ turns: [[call("new-get", "get_background_bash_job", { job_id: job }), { text: "Successor job verified" }]] });
    await f.userTurn(page, "Verify successor job");
    await f.final(page, agent, "Successor job verified");
    const get = results("get_background_bash_job").at(-1);
    expect(get.isError, JSON.stringify(get)).toBeFalsy();
    expect(JSON.parse(get.content[0].text)).toMatchObject({ job_id: job, status: "running" });
    expect(f.violations).toEqual([]);
  } finally {
    // The finite successor limits its lifetime; this test does not prove process cleanup.
    await f.close();
  }
});
