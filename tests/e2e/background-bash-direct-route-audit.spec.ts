import { test, expect } from "@playwright/test";
import { readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { claudeWorkersHarness } from "./claude-workers-harness";

test.skip(process.platform !== "linux", "Background bash launch is supported only on Linux");

test("child direct-route audit rejects unwitnessed calls and witnessed foreign-job access", async ({ page }) => {
  test.setTimeout(120_000);
  const names = ["launch_background_bash", "list_background_bash_jobs", "get_background_bash_job", "kill_background_bash_job"];
  const f = await claudeWorkersHarness();
  const release = join(f.fixtureRoot, "subagent-denial-release");
  const quote = (s: string) => `'${s.replaceAll("'", "'\\''")}'`;
  const call = (id: string, name: string, input: object) => ({ mcp_call: { id, name, input, require_catalog: true } });
  try {
    await writeFile(join(f.router.root, "work", "subagent-evidence.txt"), "fixture");
    await f.open(page);
    expect((await f.request(page, "/v1/settings/default-model", { model: { provider: "claude-code", id: "haiku" } }, "PUT")).status).toBe(200);
    const parent = await f.launch(page);
    await f.configure({ turns: [[call("parent-launch", names[0], { command: `while [ ! -f ${quote(release)} ]; do sleep 0.05; done` }), { text: "Job started" }]] });
    await f.userTurn(page, "Start parent job");
    await f.final(page, parent, "Job started");
    const results = (name: string) => f.rows("SELECT CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? AND kind IN ('toolResult','tool_result') ORDER BY rowid", parent)
      .map(row => JSON.parse(String(row.body))).filter(row => row.toolName === name);
    const launched = results(names[0]);
    expect(launched).toHaveLength(1);
    expect(launched[0].isError).toBeFalsy();
    const id = JSON.parse(launched[0].content[0].text).job_id;
    expect(typeof id).toBe("string");
    expect(id.length).toBeGreaterThan(0);
    await f.configure({ executions_by_input: [
      { contains: "Start child", turns: [[call("spawn-child", "subagent", { prompt: "Held restricted child", description: "Held restricted child" }), { text: "Child requested" }]] },
      { contains: "Held restricted child", turns: [[call("child-read", "read", { path: "subagent-evidence.txt" }), { barrier: "restricted-child-held" },
        call("child-foreign-get", names[2], { job_id: id }),
        call("child-foreign-kill", names[3], { job_id: id }),
        { text: "Child finished" }]] },
      { contains: "finished with status", turns: [[{ text: "Child notice received" }]] },
    ] });
    await f.userTurn(page, "Start child");
    await expect.poll(async () => (await f.records()).some(r => r.kind === "barrier" && r.name === "restricted-child-held"), { timeout: 30_000 }).toBe(true);
    const receipt = (await f.records()).find(r => r.kind === "barrier" && r.name === "restricted-child-held")!;
    const catalog = (await f.records()).find(r => r.kind === "catalog" && r.pid === receipt.pid);
    expect(catalog).toBeDefined();
    for (const name of names) expect(catalog!.names).toContain(name);
    // Bind the credential to the exact held child process, not whichever
    // process happened to start most recently.
    const childStart = (await f.starts()).find(r => r.pid === receipt.pid);
    expect(childStart).toBeDefined();
    expect(Array.isArray(childStart!.argv)).toBe(true);
    const argv = childStart!.argv as string[];
    const configIndex = argv.indexOf("--mcp-config");
    expect(configIndex).toBeGreaterThanOrEqual(0);
    const config = JSON.parse(await readFile(argv[configIndex + 1], "utf8"));
    const grant = config.mcpServers.mecha;
    // A raw request has no assistant tool-use producer and must not execute.
    const unwitnessed = await f.mcp(grant, names[3], { job_id: id }, "unwitnessed-child-kill");
    expect(unwitnessed.status).toBe(200);
    expect(unwitnessed.body.error).toMatchObject({ code: -32000, message: "checked tool execution rejected" });
    await f.releaseBarrier(receipt);
    for (const toolId of ["child-foreign-get", "child-foreign-kill"]) {
      await expect.poll(async () => (await f.records()).find(r => r.kind === "mcp_call_response" && r.tool_id === toolId), { timeout: 30_000 }).toBeDefined();
      const response = (await f.records()).find(r => r.kind === "mcp_call_response" && r.tool_id === toolId)!;
      expect(response.error).toBeNull();
      const result = response.result as { isError?: boolean; content: unknown };
      expect(result.isError, toolId).toBe(true);
      expect(JSON.stringify(result.content)).toContain("background bash job not found");
      expect(JSON.stringify(result.content)).not.toContain(id);
    }
    await f.configure({ turns: [[call("parent-verify", names[2], { job_id: id }), { text: "Job verified" }]] });
    await f.userTurn(page, "Verify parent job");
    await f.final(page, parent, "Job verified");
    const get = results(names[2]).at(-1);
    expect(get.isError).toBeFalsy();
    expect(JSON.parse(get.content[0].text)).toMatchObject({ job_id: id, status: "running" });
    expect(f.violations).toEqual([]);
  } finally {
    await writeFile(release, "release").catch(() => {});
    await f.close();
  }
});
