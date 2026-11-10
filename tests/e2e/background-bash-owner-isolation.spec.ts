import { test, expect } from "@playwright/test";
import { claudeWorkersHarness } from "./claude-workers-harness";
import { writeFile } from "node:fs/promises";
import { join } from "node:path";

test.skip(process.platform !== "linux", "Background bash launch is supported only on Linux");

// BB-006: each checked MCP call runs under the real managed worker binding.
test("managed owners cannot get or kill each other's background jobs", async ({ page }) => {
  test.setTimeout(120_000);
  const f = await claudeWorkersHarness();
  const release = join(f.fixtureRoot, "owner-isolation-release");
  const quotedRelease = `'${release.replaceAll("'", "'\\''")}'`;
  const call = (id: string, name: string, input: object) => ({ mcp_call: { id, name, input, require_catalog: true } });
  const results = (agent: string, name: string) => f.rows(
    "SELECT CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? AND kind IN ('toolResult','tool_result') ORDER BY rowid", agent,
  ).map(row => JSON.parse(String(row.body))).filter(row => row.toolName === name);
  const payload = (result: any) => JSON.parse(result.content[0].text);
  try {
    await f.open(page);
    // The browser frontend is a separate origin from both proxy and router.
    expect(new URL(page.url()).origin).not.toBe(new URL(f.proxy.url).origin);
    expect(new URL(page.url()).origin).not.toBe(new URL(f.router.httpUrl).origin);
    const owners: string[] = [];
    const jobs: string[] = [];
    for (const label of ["First", "Second"]) {
      const agent = await f.launch(page);
      owners.push(agent);
      await f.configure({ turns: [[call(`launch-${label}`, "launch_background_bash", { command: `while [ ! -f ${quotedRelease} ]; do sleep 0.05; done` }), { text: `${label} launched` }]] });
      await f.userTurn(page, `Launch ${label}`);
      await f.final(page, agent, `${label} launched`);
      const result = results(agent, "launch_background_bash");
      expect(result).toHaveLength(1);
      expect(result[0].isError, JSON.stringify(result[0])).toBeFalsy();
      const id = payload(result[0]).job_id;
      expect(typeof id).toBe("string");
      expect(id.length).toBeGreaterThan(0);
      jobs.push(id);
    }
    expect(owners[0]).not.toBe(owners[1]);
    expect(jobs[0]).not.toBe(jobs[1]);
    // Both owners must converge through the global UI-live projection, with
    // only the selected owner's job shown in its dialog.
    for (let i = 0; i < 2; i++) {
      await page.locator(`[data-agent-id="${owners[i]}"]`).click();
      const widget = page.getByRole("region", { name: "Background shell jobs" });
      await widget.getByRole("button", { name: /shell jobs/i }).click();
      await expect(widget.locator("li").filter({ hasText: jobs[i] })).toContainText("running");
      await expect(widget).not.toContainText(jobs[1 - i]);
      await page.keyboard.press("Escape");
    }
    for (let i = 0; i < 2; i++) {
      await page.locator(`[data-agent-id="${owners[i]}"]`).click();
      await f.configure({ turns: [[
        call(`foreign-get-${i}`, "get_background_bash_job", { job_id: jobs[1 - i] }),
        call(`foreign-kill-${i}`, "kill_background_bash_job", { job_id: jobs[1 - i] }),
        call(`own-get-${i}`, "get_background_bash_job", { job_id: jobs[i] }),
        { text: `Isolation checked ${i}` },
      ]] });
      await f.userTurn(page, `Check isolation ${i}`);
      await f.final(page, owners[i], `Isolation checked ${i}`);
      const gets = results(owners[i], "get_background_bash_job");
      const kills = results(owners[i], "kill_background_bash_job");
      expect(gets).toHaveLength(2);
      expect(kills).toHaveLength(1);
      expect(gets[0].isError, JSON.stringify(gets[0])).toBe(true);
      expect(kills[0].isError, JSON.stringify(kills[0])).toBe(true);
      for (const denied of [gets[0], kills[0]]) {
        expect(JSON.stringify(denied.content)).toContain("background bash job not found");
        expect(JSON.stringify(denied.content)).not.toContain(jobs[1 - i]);
      }
      expect(gets[1].isError, JSON.stringify(gets[1])).toBeFalsy();
      expect(payload(gets[1])).toMatchObject({ job_id: jobs[i], status: "running" });
    }
    // Verify the second attempted foreign kill did not stop the first job either.
    await page.locator(`[data-agent-id="${owners[0]}"]`).click();
    await f.configure({ turns: [[call("first-after-attack", "get_background_bash_job", { job_id: jobs[0] }), { text: "First still running" }]] });
    await f.userTurn(page, "Verify first job survived");
    await f.final(page, owners[0], "First still running");
    const last = results(owners[0], "get_background_bash_job").at(-1);
    expect(last.isError, JSON.stringify(last)).toBeFalsy();
    expect(payload(last)).toMatchObject({ job_id: jobs[0], status: "running" });
    expect(f.violations).toEqual([]);
  } finally {
    await writeFile(release, "release").catch(() => {});
    await f.close();
  }
});
