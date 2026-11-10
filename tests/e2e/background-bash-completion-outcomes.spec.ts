import { test, expect } from "@playwright/test";
import { writeFile } from "node:fs/promises";
import { join } from "node:path";
import { claudeWorkersHarness, exists } from "./claude-workers-harness";

test.skip(process.platform !== "linux", "Background bash launch is supported only on Linux");

for (const outcome of ["success", "killed", "timed_out"] as const) {
  test(`background bash ${outcome} completion reaches worker input`, async ({ page }) => {
    test.setTimeout(120_000);
    const f = await claudeWorkersHarness();
    const release = join(f.fixtureRoot, "completion-release");
    const ready = join(f.fixtureRoot, "completion-ready");
    const quote = (s: string) => `'${s.replaceAll("'", "'\\''")}'`;
    const call = (id: string, name: string, input: object) => ({ mcp_call: { id, name, input, require_catalog: true } });
    try {
      const command = `printf ready > ${quote(ready)}; while [ ! -f ${quote(release)} ]; do sleep 0.05; done; exit 0`;
      await f.configure({ turns_by_input: [{ contains: "Launch completion test", steps: [
        call("launch", "launch_background_bash", { command, ...(outcome === "timed_out" ? { timeout: 30 } : {}) }),
        { text: "Completion job launched" },
      ] }] });
      await f.open(page);
      const agent = await f.launch(page);
      await f.userTurn(page, "Launch completion test");
      const result = (name: string) => f.rows("SELECT CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? AND kind IN ('toolResult','tool_result') ORDER BY rowid", agent)
        .map(row => JSON.parse(String(row.body))).find(row => row.toolName === name);
      await expect.poll(() => !!result("launch_background_bash"), { timeout: 15_000 }).toBe(true);
      const launch = result("launch_background_bash");
      expect(launch).toBeTruthy();
      expect(launch.isError, JSON.stringify(launch)).toBeFalsy();
      const jobId = JSON.parse(launch.content[0].text).job_id;
      expect(typeof jobId).toBe("string");
      expect(jobId.length).toBeGreaterThan(0);
      await expect.poll(() => exists(ready), { timeout: 15_000, message: "background shell must start before completion" }).toBe(true);
      await f.idle(agent);
      if (outcome === "success") {
        await writeFile(release, "release");
      } else if (outcome === "killed") {
        await f.configure({ turns_by_input: [{ contains: "Stop completion job", steps: [
          call("kill", "kill_background_bash_job", { job_id: jobId }), { text: "Completion job stopped" },
        ] }] });
        await f.userTurn(page, "Stop completion job");
        await expect.poll(() => !!result("kill_background_bash_job"), { timeout: 15_000 }).toBe(true);
        expect(result("kill_background_bash_job").isError).toBeFalsy();
      }
      const status = outcome === "success" ? "succeeded" : outcome;
      const notice = `Background bash job ${jobId} finished: ${status}`;
      const expected = outcome === "success" ? `${notice} (exit code 0)` : notice;
      await expect.poll(() => f.rows("SELECT CAST(body AS TEXT) AS body FROM messages WHERE id=?", `background-bash-completed-${jobId}`).map(row => row.body), { timeout: 45_000 }).toEqual([expected]);
      await expect.poll(async () => JSON.stringify(await f.inputs()), { timeout: 15_000 }).toContain(expected);
      expect(f.violations).toEqual([]);
    } finally {
      await writeFile(release, "release").catch(() => {});
      await f.close();
    }
  });
}
