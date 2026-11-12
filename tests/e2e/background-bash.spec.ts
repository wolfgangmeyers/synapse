import { test, expect } from "@playwright/test";
import { readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { claudeWorkersHarness } from "./claude-workers-harness";

test.skip(process.platform !== "linux", "Background bash launch is supported only on Linux");

// Provisional wire contract: MCP text content is JSON with job_id; get accepts
// { job_id }. Reconcile this with the backend owner when its interface lands.
// These assertions are acceptance tests, not evidence of full spec coverage.
function checkedResult(f: Awaited<ReturnType<typeof claudeWorkersHarness>>, agent: string, name: string) {
  const results = f.rows("SELECT CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? AND kind IN ('toolResult','tool_result')", agent)
    .map(row => JSON.parse(String(row.body)));
  const result = results.find(row => row.toolName === name);
  expect(result, `checked MCP ${name} must return a tool result`).toBeTruthy();
  expect(result.isError, JSON.stringify(result)).toBeFalsy();
  expect(Array.isArray(result.content), JSON.stringify(result)).toBe(true);
  const texts = result.content.filter((part: { type?: string; text?: unknown }) => part.type === "text" && typeof part.text === "string");
  expect(texts, JSON.stringify(result)).toHaveLength(1);
  return JSON.parse(texts[0].text);
}

test("managed worker launches, lists, gets and kills a real background shell", async ({ page }) => {
  test.setTimeout(120_000);
  const f = await claudeWorkersHarness();
  const release = join(f.fixtureRoot, "background-release");
  const output = join(f.fixtureRoot, "background-output");
  const quote = (s: string) => `'${s.replaceAll("'", "'\\''")}'`;
  const command = `while [ ! -f ${quote(release)} ]; do sleep 0.05; done; printf done > ${quote(output)}`;
  try {
    await f.configure({ turns_by_input: [{ contains: "Launch a background shell", steps: [
      { mcp_call: { id: "background-launch", name: "launch_background_bash", input: { command }, require_catalog: true } },
      { text: "Launch returned" },
    ] }] });
    await f.open(page);
    const agent = await f.launch(page);
    // A parentless managed HTTP agent is a worker, not the router root.
    expect(f.rows("SELECT role FROM embedded_sessions WHERE agent_id=? AND current=1", agent)).toEqual([{ role: "worker" }]);
    expect(f.tableRows("managed_agents", agent)[0].allow_inline_tools).toBe(0);
    await f.userTurn(page, "Launch a background shell");
    await expect.poll(async () => (await f.records()).some(row => row.kind === "catalog"),
      { message: "Claude must discover the managed MCP tool catalog" }).toBe(true);
    const catalog = (await f.records()).find(row => row.kind === "catalog");
    expect(catalog?.names).not.toContain("wait_background_bash_job");
    expect(catalog?.names).toEqual(expect.arrayContaining([
      "launch_background_bash", "list_background_bash_jobs", "get_background_bash_job", "kill_background_bash_job",
    ]));
    await f.final(page, agent, "Launch returned");
    const launch = checkedResult(f, agent, "launch_background_bash");
    expect(typeof launch.job_id, JSON.stringify(launch)).toBe("string");
    expect(launch.job_id.trim()).not.toBe("");
    expect(Date.parse(launch.deadline) - Date.parse(launch.started_at)).toBe(600_000);
    const jobId: string = launch.job_id;
    // The command cannot finish before release; check the actual returned ID
    // through a second checked tool call rather than accepting an ID label.
    await f.configure({ turns_by_input: [{ contains: "Inspect the launched background job", steps: [
      { mcp_call: { id: "background-get", name: "get_background_bash_job", input: { job_id: jobId }, require_catalog: true } },
      { text: "Snapshot returned" },
    ] }] });
    await f.userTurn(page, "Inspect the launched background job");
    await f.final(page, agent, "Snapshot returned");
    const snapshot = checkedResult(f, agent, "get_background_bash_job");
    expect(snapshot.job_id).toBe(jobId);
    expect(snapshot.status).toBe("running");
    await writeFile(release, "release");
    await expect.poll(async () => {
      try { return await readFile(output, "utf8"); }
      catch (error) { if ((error as NodeJS.ErrnoException).code === "ENOENT") return ""; throw error; }
    }, { timeout: 10_000 }).toBe("done");
    // Completion wakes the worker as a separate input. An unconditional
    // fixture script would replay its launch on this notification.
    await expect.poll(async () => JSON.stringify(await f.inputs()), { timeout: 10_000 })
      .toContain(`Background bash job ${jobId} finished`);
    await f.idle(agent);
    await f.configure({ turns_by_input: [{ contains: "Launch another background job", steps: [
      { mcp_call: { id: "background-launch-kill", name: "launch_background_bash", input: { command: "sleep 60" }, require_catalog: true } },
      { text: "Kill target launched" },
    ] }] });
    await f.userTurn(page, "Launch another background job");
    await f.final(page, agent, "Kill target launched");
    const launches = f.rows("SELECT CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? AND kind IN ('toolResult','tool_result') ORDER BY rowid", agent)
      .map(row => JSON.parse(String(row.body))).filter(row => row.toolName === "launch_background_bash");
    expect(launches).toHaveLength(2);
    expect(launches[1].isError, JSON.stringify(launches[1])).toBeFalsy();
    const killId = JSON.parse(launches[1].content[0].text).job_id;
    expect(typeof killId).toBe("string");
    expect(killId).not.toBe(jobId);
    await f.configure({ turns_by_input: [{ contains: "List and stop the launched background job", steps: [
      { mcp_call: { id: "background-list", name: "list_background_bash_jobs", input: {}, require_catalog: true } },
      { mcp_call: { id: "background-kill", name: "kill_background_bash_job", input: { job_id: killId }, require_catalog: true } },
      { text: "List and kill returned" },
    ] }] });
    await f.userTurn(page, "List and stop the launched background job");
    await f.final(page, agent, "List and kill returned");
    const list = checkedResult(f, agent, "list_background_bash_jobs");
    expect(JSON.stringify(list)).toContain(killId);
    expect(checkedResult(f, agent, "kill_background_bash_job")).toMatchObject({ job_id: killId, status: "killed" });
    expect(f.tableRows("managed_agents", agent)[0].allow_inline_tools).toBe(0);
    expect(f.violations).toEqual([]);
  } finally {
    await writeFile(release, "release").catch(() => {});
    await f.close();
  }
});

// BB-003/004/007/008/009: exercise the managed MCP calls through the
// deterministic Claude fixture; UI and notice assertions use live projections.
test("completion, shared polling budget and header activity", async ({ page }) => {
  test.setTimeout(120_000);
  const f = await claudeWorkersHarness();
  const release = join(f.fixtureRoot, "budget-release");
  const quote = (s: string) => `'${s.replaceAll("'", "'\\''")}'`;
  const call = (id: string, name: string, input: object) => ({ mcp_call: { id, name, input, require_catalog: true } });
  const results = (agent: string) => f.rows("SELECT CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? AND kind IN ('toolResult','tool_result') ORDER BY rowid", agent).map(row => JSON.parse(String(row.body)));
  try {
    await f.configure({ turns: [[call("launch-budget", "launch_background_bash", { command: `printf early; while [ ! -f ${quote(release)} ]; do sleep 0.05; done; printf out; printf err >&2; exit 23` }), { text: "Budget job launched" }]] });
    await f.open(page);
    const agent = await f.launch(page);
    await f.userTurn(page, "Launch budget job");
    await f.final(page, agent, "Budget job launched");
    const job = checkedResult(f, agent, "launch_background_bash");
    const widget = page.getByRole("region", { name: "Background shell jobs" });
    await expect(widget.getByRole("button", { name: /shell jobs 1/i })).toBeVisible();
    await expect(widget.getByText(job.job_id)).toBeHidden();
    await widget.getByRole("button", { name: /shell jobs/i }).click();
    await expect(widget).not.toContainText(job.job_id);
    await expect(widget).toContainText("Started ");
    await expect(widget).toContainText("running");
    await page.getByRole("button", { name: "Close dialog" }).click();
    await f.configure({ turns: [[
      call("budget-list-1", "list_background_bash_jobs", {}),
      call("budget-get-1", "get_background_bash_job", { job_id: job.job_id }),
      call("budget-list-2", "list_background_bash_jobs", {}),
      call("budget-get-denied", "get_background_bash_job", { job_id: job.job_id }),
      { text: "Budget checked" },
    ]] });
    await f.userTurn(page, "Check polling budget");
    await f.final(page, agent, "Budget checked");
    const reads = results(agent).filter(r => ["list_background_bash_jobs", "get_background_bash_job"].includes(r.toolName));
    expect(reads).toHaveLength(4);
    expect(reads.slice(0, 3).map(r => !!r.isError)).toEqual([false, false, false]);
    const listed = JSON.parse(reads[0].content[0].text);
    expect(listed).toEqual(expect.arrayContaining([expect.objectContaining({ job_id: job.job_id, status: "running", truncated: false })]));
    expect(listed.every((entry: object) => !("output" in entry))).toBe(true);
    const listedAgain = JSON.parse(reads[2].content[0].text);
    expect(listedAgain).toEqual(expect.arrayContaining([expect.objectContaining({ job_id: job.job_id, status: "running" })]));
    expect(JSON.parse(reads[1].content[0].text)).toMatchObject({ job_id: job.job_id, status: "running" });
    expect(reads[3].isError, JSON.stringify(reads[3])).toBe(true);
    expect(JSON.stringify(reads[3])).toContain("wait for the completion notification or continue other work");
    await writeFile(release, "release");
    await widget.getByRole("button", { name: /shell jobs/i }).click();
    await widget.getByLabel("Filter:").selectOption("failed");
    await expect(widget).toContainText("failed", { timeout: 10_000 });
    await expect.poll(() => f.rows("SELECT body FROM messages WHERE id=?", `background-bash-completed-${job.job_id}`).map(r => String(r.body)), { timeout: 10_000 }).toEqual([expect.stringContaining("exit code 23")]);
    await expect.poll(async () => JSON.stringify(await f.inputs()), { timeout: 10_000 }).toContain(`Background bash job ${job.job_id} finished: failed (exit code 23)`);
    await page.getByRole("button", { name: "Close dialog" }).click();
    await f.idle(agent);
    await f.configure({ turns: [[call("terminal-get", "get_background_bash_job", { job_id: job.job_id }), { text: "Terminal checked" }]] });
    await f.userTurn(page, "Check terminal job");
    await f.final(page, agent, "Terminal checked");
    const terminalResult = results(agent).filter(r => r.toolName === "get_background_bash_job").at(-1);
    expect(terminalResult.isError, JSON.stringify(terminalResult)).toBeFalsy();
    const terminal = JSON.parse(terminalResult.content[0].text);
    expect(terminal).toMatchObject({ job_id: job.job_id, status: "failed", exit_code: 23 });
    expect(terminal.output).toContain("early");
    expect(terminal.output).toContain("out");
    expect(terminal.output).toContain("err");
    expect(f.violations).toEqual([]);
  } finally {
    await writeFile(release, "release").catch(() => {});
    await f.close();
  }
});

test("kill returns terminal state across repeated requests", async ({ page }) => {
  test.setTimeout(120_000);
  const f = await claudeWorkersHarness();
  const call = (id: string, name: string, input: object) => ({ mcp_call: { id, name, input, require_catalog: true } });
  const all = (agent: string, name: string) => f.rows("SELECT CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? AND kind IN ('toolResult','tool_result') ORDER BY rowid", agent).map(r => JSON.parse(String(r.body))).filter(r => r.toolName === name);
  try {
    await f.configure({ turns: [[call("launch-kill", "launch_background_bash", { command: "sleep 60" }), { text: "Kill job launched" }]] });
    await f.open(page);
    const agent = await f.launch(page);
    await f.userTurn(page, "Launch kill job");
    await f.final(page, agent, "Kill job launched");
    const job = checkedResult(f, agent, "launch_background_bash");
    await f.configure({ turns_by_input: [{ contains: "Kill twice", steps: [call("kill-once", "kill_background_bash_job", { job_id: job.job_id }), call("kill-twice", "kill_background_bash_job", { job_id: job.job_id }), { text: "Kill checked" }] }] });
    await f.userTurn(page, "Kill twice");
    await f.final(page, agent, "Kill checked");
    const kills = all(agent, "kill_background_bash_job");
    expect(kills).toHaveLength(2);
    for (const result of kills) {
      expect(result.isError, JSON.stringify(result)).toBeFalsy();
      expect(JSON.parse(result.content[0].text)).toMatchObject({ job_id: job.job_id, status: "killed" });
    }
    expect(f.violations).toEqual([]);
  } finally { await f.close(); }
});


test("timeout reaches a terminal snapshot", async ({ page }) => {
  test.setTimeout(120_000);
  const f = await claudeWorkersHarness();
  const call = (id: string, name: string, input: object) => ({ mcp_call: { id, name, input, require_catalog: true } });
  try {
    await f.configure({ turns_by_input: [{ contains: "Launch timeout job", steps: [call("launch-timeout", "launch_background_bash", { command: "sleep 60", timeout: 1 }), { text: "Timeout job launched" }] }] });
    await f.open(page);
    const agent = await f.launch(page);
    await f.userTurn(page, "Launch timeout job");
    await f.final(page, agent, "Timeout job launched");
    const job = checkedResult(f, agent, "launch_background_bash");
    await page.getByRole("region", { name: "Background shell jobs" }).getByRole("button", { name: /shell jobs/i }).click();
    await page.getByRole("dialog").getByLabel("Filter:").selectOption("failed");
    await expect(page.getByRole("dialog", { name: "Background shell jobs" })).toContainText("timed_out", { timeout: 10_000 });
    await page.getByRole("button", { name: "Close dialog" }).click();
    await f.configure({ turns_by_input: [{ contains: "Inspect timeout", steps: [call("timeout-get", "get_background_bash_job", { job_id: job.job_id }), { text: "Timeout checked" }] }] });
    await f.userTurn(page, "Inspect timeout");
    await f.final(page, agent, "Timeout checked");
    expect(checkedResult(f, agent, "get_background_bash_job")).toMatchObject({ job_id: job.job_id, status: "timed_out" });
    expect(f.violations).toEqual([]);
  } finally { await f.close(); }
});

test("worker header follows live jobs across worker switches", async ({ page }) => {
  test.setTimeout(120_000);
  const f = await claudeWorkersHarness();
  const releaseA = join(f.fixtureRoot, "switch-release-a");
  const releaseB = join(f.fixtureRoot, "switch-release-b");
  const quote = (s: string) => `'${s.replaceAll("'", "'\\''")}'`;
  const script = (release: string, message: string) => ({ turns: [[
    { mcp_call: { id: `launch-${message}`, name: "launch_background_bash", input: { command: `while [ ! -f ${quote(release)} ]; do sleep 0.05; done` }, require_catalog: true } },
    { text: message },
  ]] });
  try {
    await f.open(page);
    const first = await f.launch(page);
    await f.configure(script(releaseA, "First job launched"));
    await f.userTurn(page, "Start first job");
    await f.final(page, first, "First job launched");
    const firstId = checkedResult(f, first, "launch_background_bash").job_id;
    const widget = page.getByRole("region", { name: "Background shell jobs" });
    await widget.getByRole("button", { name: /shell jobs/i }).click();
    await expect(widget.locator(".background-bash-job-list li")).toHaveCount(1);
    await page.getByRole("button", { name: "Close dialog" }).click();
    const second = await f.launch(page);
    await expect(widget.getByRole("button", { name: /shell jobs 0/i })).toBeVisible();
    await f.configure(script(releaseB, "Second job launched"));
    await f.userTurn(page, "Start second job");
    await f.final(page, second, "Second job launched");
    const secondId = checkedResult(f, second, "launch_background_bash").job_id;
    expect(secondId).not.toBe(firstId);
    await widget.getByRole("button", { name: /shell jobs/i }).click();
    await expect(widget.locator(".background-bash-job-list li")).toHaveCount(1);
    await expect(widget).not.toContainText(firstId);
    await page.getByRole("button", { name: "Close dialog" }).click();
    await page.locator(`[data-agent-id="${first}"]`).click();
    await widget.getByRole("button", { name: /shell jobs/i }).click();
    await expect(widget.locator(".background-bash-job-list li")).toHaveCount(1);
    await expect(widget).not.toContainText(secondId);
    await writeFile(releaseA, "release");
    await widget.getByLabel("Filter:").selectOption("completed");
    await expect(widget).toContainText("succeeded", { timeout: 10_000 });
    await page.getByRole("button", { name: "Close dialog" }).click();
    await page.locator(`[data-agent-id="${second}"]`).click();
    await widget.getByRole("button", { name: /shell jobs/i }).click();
    await expect(widget.locator(".background-bash-job-list li")).toHaveCount(1);
    await expect(widget).toContainText("running");
    expect(f.violations).toEqual([]);
  } finally {
    await Promise.all([writeFile(releaseA, "release"), writeFile(releaseB, "release")]).catch(() => {});
    await f.close();
  }
});

// BB-006: exercise a child-scoped MCP grant, not just catalog visibility.
test("subagent can invoke background job tools for its own job through its checked route", async ({ page }) => {
  test.setTimeout(120_000);
  const names = ["launch_background_bash", "list_background_bash_jobs", "get_background_bash_job", "kill_background_bash_job"];
  const f = await claudeWorkersHarness({ executions_by_input: [
    { contains: "Start restricted child", turns: [[{ mcp_call: { id: "spawn-bg-child", name: "subagent", input: { prompt: "Restricted background child", description: "Restricted background child" }, require_catalog: true } }, { text: "Child requested" }]] },
    { contains: "Restricted background child", turns: [[
      { mcp_call: { id: "child-read", name: "read", input: { path: "evidence.txt" }, require_catalog: true } },
      { barrier: "background-child-held" },
      { mcp_call: { id: "child-own-launch", name: names[0], input: { command: "exec sleep 120" }, require_catalog: true } },
      { mcp_call: { id: "child-own-list", name: names[1], input: {}, require_catalog: true } },
      { mcp_call: { id: "child-own-get", name: names[2], input: { job_id: "$LAST_JOB_ID" }, require_catalog: true } },
      { mcp_call: { id: "child-own-kill", name: names[3], input: { job_id: "$LAST_JOB_ID" }, require_catalog: true } },
      { text: "Child finished" },
    ]] },
    { contains: "finished with status", turns: [[{ text: "Child notice received" }]] },
  ] }, { defaultModel: null });
  try {
    await writeFile(join(f.router.root, "work", "evidence.txt"), "fixture");
    await f.open(page);
    expect((await f.request(page, "/v1/settings/default-model", { model: { provider: "claude-code", id: "haiku" } }, "PUT")).status).toBe(200);
    const parent = await f.launch(page);
    await f.userTurn(page, "Start restricted child");
    await expect.poll(async () => (await f.records()).some(r => r.kind === "barrier" && r.name === "background-child-held"), { timeout: 30_000 }).toBe(true);
    const receipt = (await f.records()).find(r => r.kind === "barrier" && r.name === "background-child-held")!;
    const catalog = (await f.records()).find(r => r.kind === "catalog" && r.pid === receipt.pid);
    expect(catalog).toBeDefined();
    expect(catalog!.names).not.toContain("wait_background_bash_job");
    for (const name of names) expect(catalog!.names).toContain(name);
    // The child must produce each checked tool call; a raw MCP request has no witness.
    await f.releaseBarrier(receipt);
    const result = async (id: string) => {
      await expect.poll(async () => (await f.records()).some(r => r.kind === "mcp_call_response" && r.tool_id === id && r.pid === receipt.pid), { timeout: 30_000 }).toBe(true);
      const response = (await f.records()).find(r => r.kind === "mcp_call_response" && r.tool_id === id && r.pid === receipt.pid)!;
      expect(response.error, id).toBeNull();
      const value = response.result as { isError?: boolean; content: { text: string }[] };
      expect(value.isError, id).not.toBe(true);
      return JSON.parse(value.content[0].text);
    };
    const job = await result("child-own-launch");
    expect(typeof job.job_id).toBe("string");
    expect(job.job_id.length).toBeGreaterThan(0);
    const list = await result("child-own-list");
    expect(list.some((item: { job_id: string }) => item.job_id === job.job_id)).toBe(true);
    expect(await result("child-own-get")).toMatchObject({ job_id: job.job_id, status: "running" });
    expect(await result("child-own-kill")).toMatchObject({ job_id: job.job_id, status: "killed" });
    expect(f.rows("SELECT agent_id FROM managed_subagents WHERE parent_agent_id=?", parent)).toHaveLength(1);
    expect(f.violations).toEqual([]);
  } finally { await f.close(); }
});

// Invalid overrides must fail at the checked worker tool boundary without starting a command.
test("worker rejects invalid timeout overrides", async ({ page }) => {
  test.setTimeout(120_000);
  const f = await claudeWorkersHarness();
  const marker = join(f.fixtureRoot, "invalid-timeout-effect");
  const values = [0, -1, 1.5, "1", 9223372037];
  try {
    await f.configure({ turns_by_input: [{ contains: "Test invalid timeouts", steps: [
      ...values.map((timeout, i) => ({ mcp_call: { id: `invalid-timeout-${i}`, name: "launch_background_bash", input: { command: `touch '${marker}'`, timeout }, require_catalog: true } })),
      { text: "Invalid timeouts checked" },
    ] }] });
    await f.open(page);
    const agent = await f.launch(page);
    await f.userTurn(page, "Test invalid timeouts");
    await f.final(page, agent, "Invalid timeouts checked");
    const results = f.rows("SELECT CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? AND kind IN ('toolResult','tool_result') ORDER BY rowid", agent)
      .map(row => JSON.parse(String(row.body))).filter(row => row.toolName === "launch_background_bash");
    expect(results).toHaveLength(values.length);
    expect(results.every(row => row.isError), JSON.stringify(results)).toBe(true);
    await expect.poll(async () => { try { await readFile(marker); return true; } catch (error) { if ((error as NodeJS.ErrnoException).code === "ENOENT") return false; throw error; } }).toBe(false);
    expect(f.violations).toEqual([]);
  } finally { await f.close(); }
});
