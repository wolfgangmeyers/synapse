import { test, expect } from "@playwright/test";
import { readFile, open, type FileHandle } from "node:fs/promises";
import { constants, readFileSync } from "node:fs";
import { execFileSync } from "node:child_process";
import { join } from "node:path";
import { claudeWorkersHarness } from "./claude-workers-harness";

test.skip(process.platform !== "linux", "background jobs require Linux");
test("Claude child promotes the same shell and inspects its completion without a wait tool", async ({ page }) => {
  test.setTimeout(120_000);
  const f = await claudeWorkersHarness();
  const release = join(f.fixtureRoot, "child-job-release");
  const starts = join(f.fixtureRoot, "starts");
  const ends = join(f.fixtureRoot, "ends");
  const quote = (s: string) => `'${s.replaceAll("'", "'\\''")}'`;
  const call = (id: string, name: string, input: object) => ({ mcp_call: { id, name, input, require_catalog: true } });
  let fifo: FileHandle | undefined;
  try {
    execFileSync("mkfifo", [release]);
    fifo = await open(release, constants.O_RDWR | constants.O_NONBLOCK);
    await f.open(page);
    expect((await f.request(page, "/v1/settings/default-model", { model: { provider: "claude-code", id: "haiku" } }, "PUT")).status).toBe(200);
    const parent = await f.launch(page);
    await f.configure({ executions_by_input: [
      { contains: "Spawn background child", turns: [[call("spawn", "subagent", { prompt: "Child background exercise", description: "Background child" }), { text: "Spawned" }]] },
      { contains: "Child background exercise", turns: [[
        { barrier: "before-bash" },
        call("child-bash", "bash", { command: `echo $$ >> ${quote(starts)}; echo before-promotion; read -r line < ${quote(release)}; echo $$ >> ${quote(ends)}; echo after-promotion; exit 7`, timeout: 1 }),
        { barrier: "promoted" },
        call("child-get", "get_background_bash_job", { job_id: "$LAST_JOB_ID" }),
        { text: "Child inspected job" },
      ]] },
      { contains: "finished with status", turns: [[{ text: "Parent received child completion" }]] },
    ] });
    await f.userTurn(page, "Spawn background child");
    await expect.poll(async () => (await f.records()).some(r => r.kind === "barrier" && r.name === "before-bash"), { timeout: 30_000 }).toBe(true);
    const child = String(f.rows("SELECT agent_id FROM managed_subagents WHERE parent_agent_id=?", parent)[0].agent_id);
    const timerPath = `/v1/test/clock/timer?agent_id=${encodeURIComponent(child)}`;
    expect((await f.request(page, timerPath, {})).status).toBe(200);
    const beforeBash = (await f.records()).find(r => r.kind === "barrier" && r.name === "before-bash")!;
    await f.releaseBarrier(beforeBash);
    await expect.poll(async () => (await f.request(page, timerPath)).body.registered, { timeout: 30_000 }).toBe(true);
    await f.router.advanceClock(1000);
    await expect.poll(async () => (await f.records()).some(r => r.kind === "barrier" && r.name === "promoted"), { timeout: 30_000 }).toBe(true);

    const response = (id: string) => f.records().then(rs => rs.find(r => r.kind === "mcp_call_response" && r.tool_id === id));
    const promoted = await response("child-bash") as any;
    expect(promoted.error).toBeNull();
    expect(promoted.result.isError).toBeFalsy();
    const job = JSON.parse(promoted.result.content[0].text);
    expect(job).toMatchObject({ job_id: expect.any(String), status: "running" });
    const startPID = (await readFile(starts, "utf8")).trim();
    expect(startPID).toMatch(/^\d+$/);
    const receipt = (await f.records()).find(r => r.kind === "barrier" && r.name === "promoted")!;
    const catalog = (await f.records()).find(r => r.kind === "catalog" && r.pid === receipt.pid);
    expect(catalog?.names).not.toContain("wait_background_bash_job");
    expect(catalog?.names).toContain("get_background_bash_job");
    await fifo.write("release\n");
    await expect.poll(async () => { try { return (await readFile(ends, "utf8")).trim(); } catch { return ""; } }, { timeout: 30_000 }).toBe(startPID);
    await expect.poll(() => f.rows("SELECT body FROM messages WHERE id=?", `background-bash-completed-${job.job_id}`).length, { timeout: 30_000 }).toBe(1);
    await f.releaseBarrier(receipt);
    await expect.poll(async () => !!(await response("child-get")), { timeout: 30_000 }).toBe(true);
    const got = await response("child-get") as any;
    expect(got.error).toBeNull();
    expect(got.result.isError).toBeFalsy();
    expect(JSON.parse(got.result.content[0].text)).toMatchObject({ job_id: job.job_id, status: "failed", exit_code: 7, output: expect.stringContaining("before-promotion") });
    expect(JSON.parse(got.result.content[0].text).output).toContain("after-promotion");
    expect((await readFile(starts, "utf8")).trim().split("\n")).toEqual([startPID]);
    expect((await readFile(ends, "utf8")).trim()).toBe(startPID);
    await expect.poll(() => f.rows("SELECT status FROM managed_subagents WHERE agent_id=?", child), { timeout: 30_000 }).toEqual([{ status: "completed" }]);
    await expect.poll(() => f.rows("SELECT body FROM messages WHERE recipient=? AND id LIKE 'subagent-completed-%'", parent), { timeout: 20_000 }).toEqual([{ body: expect.stringContaining("Child inspected job") }]);
    expect(f.violations).toEqual([]);
  } finally { await fifo?.close(); await f.close(); }
});

test("finishing a Claude child retires its still-running background job", async ({ page }) => {
  test.setTimeout(120_000);
  const f = await claudeWorkersHarness();
  const gate = join(f.fixtureRoot, "held-job-gate");
  const started = join(f.fixtureRoot, "held-job-pid");
  const quote = (s: string) => `'${s.replaceAll("'", "'\\''")}'`;
  const call = (id: string, name: string, input: object) => ({ mcp_call: { id, name, input, require_catalog: true } });
  const identity = (pid: number) => {
    try {
      const stat = readFileSync(`/proc/${pid}/stat`, "utf8");
      const fields = stat.slice(stat.lastIndexOf(")") + 2).trim().split(/\s+/);
      return { state: fields[0], start: fields[19] };
    } catch (error) {
      if (["ENOENT", "ESRCH"].includes((error as NodeJS.ErrnoException).code ?? "")) return undefined;
      throw error;
    }
  };
  let fifo: FileHandle | undefined;
  let processID: { pid: number; start: string } | undefined;
  try {
    execFileSync("mkfifo", [gate]);
    fifo = await open(gate, constants.O_RDWR | constants.O_NONBLOCK);
    await f.open(page);
    expect((await f.request(page, "/v1/settings/default-model", { model: { provider: "claude-code", id: "haiku" } }, "PUT")).status).toBe(200);
    const parent = await f.launch(page);
    await f.configure({ executions_by_input: [
      { contains: "Spawn held child", turns: [[call("spawn-held", "subagent", { prompt: "Hold background job", description: "Held background child" }), { text: "Spawned" }]] },
      { contains: "Hold background job", turns: [[
        call("launch-held", "launch_background_bash", { command: `echo $$ > ${quote(started)}; read -r line < ${quote(gate)}; echo unexpected-release` }),
        { barrier: "held-job-launched" }, { text: "Child finished without waiting" },
      ]] },
      { contains: "finished with status", turns: [[{ text: "Parent received held child completion" }]] },
    ] });
    await f.userTurn(page, "Spawn held child");
    await expect.poll(async () => (await f.records()).some(r => r.kind === "barrier" && r.name === "held-job-launched"), { timeout: 30_000 }).toBe(true);
    await expect.poll(async () => (await readFile(started, "utf8").catch(() => "")).trim(), { timeout: 30_000 }).toMatch(/^\d+$/);
    const pid = Number((await readFile(started, "utf8")).trim());
    const before = identity(pid);
    expect(before && !["Z", "X", "x"].includes(before.state)).toBe(true);
    expect(before?.start).toMatch(/^\d+$/);
    processID = { pid, start: before!.start };
    const launch = (await f.records()).find(r => r.kind === "mcp_call_response" && r.tool_id === "launch-held") as any;
    expect(launch.error).toBeNull();
    expect(launch.result.isError).toBeFalsy();
    expect(JSON.parse(launch.result.content[0].text)).toMatchObject({ job_id: expect.any(String), status: "running" });
    const child = String(f.rows("SELECT agent_id FROM managed_subagents WHERE parent_agent_id=?", parent)[0].agent_id);
    const receipt = (await f.records()).find(r => r.kind === "barrier" && r.name === "held-job-launched")!;
    const catalog = (await f.records()).find(r => r.kind === "catalog" && r.pid === receipt.pid);
    expect(catalog?.names).not.toContain("wait_background_bash_job");
    await f.releaseBarrier(receipt);
    await expect.poll(() => f.rows("SELECT status FROM managed_subagents WHERE agent_id=?", child), { timeout: 30_000 }).toEqual([{ status: "completed" }]);
    await expect.poll(() => {
      const now = identity(pid);
      return !!now && now.start === before!.start && !["Z", "X", "x"].includes(now.state);
    }, { timeout: 30_000, message: "retirement must stop the held child job" }).toBe(false);
    await expect.poll(() => f.rows("SELECT body FROM messages WHERE recipient=? AND id LIKE 'subagent-completed-%'", parent), { timeout: 20_000 }).toEqual([{ body: expect.stringContaining("Child finished without waiting") }]);
    expect(f.violations).toEqual([]);
  } finally {
    if (processID && identity(processID.pid)?.start === processID.start) {
      try { process.kill(processID.pid, "SIGKILL"); } catch (error) { if ((error as NodeJS.ErrnoException).code !== "ESRCH") throw error; }
    }
    await fifo?.close();
    await f.close();
  }
});
