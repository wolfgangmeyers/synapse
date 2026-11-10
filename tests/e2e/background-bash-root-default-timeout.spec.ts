import { randomUUID } from "node:crypto";
import { writeFile } from "node:fs/promises";
import { join } from "node:path";
import { test, expect } from "@playwright/test";
import { claudeWorkersHarness, exists } from "./claude-workers-harness";

test.skip(process.platform !== "linux", "Background bash launch is supported only on Linux");

test("root background shell uses the default deadline and delivers completion notice", async ({ page }) => {
  test.setTimeout(120_000);
  const name = `background-root-default-${randomUUID()}`;
  const f = await claudeWorkersHarness({ turns_by_input: [{ contains: "Root bootstrap", steps: [{ text: "Root ready" }] }] }, {
    embeddedRoot: { name, requestId: randomUUID(), model: { provider: "claude-code", id: "sonnet" }, instruction: "Root bootstrap" },
  });
  const release = join(f.fixtureRoot, "root-default-release");
  const ready = join(f.fixtureRoot, "root-default-ready");
  const quote = (s: string) => `'${s.replaceAll("'", "'\\''")}'`;
  const call = (id: string, tool: string, input: object) => ({ mcp_call: { id, name: tool, input, require_catalog: true } });
  const result = (tool: string) => f.rows(
    "SELECT CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? AND kind IN ('toolResult','tool_result') ORDER BY rowid", name,
  ).map(row => JSON.parse(String(row.body))).find(row => row.toolName === tool);
  try {
    await f.open(page);
    await expect(page.locator(`[data-agent-id="${name}"]`)).toBeVisible();
    await page.locator(`[data-agent-id="${name}"]`).click();
    await f.final(page, name, "Root ready", undefined, false);
    expect(f.rows("SELECT role FROM embedded_sessions WHERE agent_id=? AND current=1", name)).toEqual([{ role: "root" }]);
    const command = `printf ready > ${quote(ready)}; while [ ! -f ${quote(release)} ]; do sleep 0.05; done; exit 0`;
    await f.configure({ turns_by_input: [{ contains: "Launch default job", steps: [
      call("launch", "launch_background_bash", { command }), { text: "Default job launched" },
    ] }] });
    await f.userTurn(page, "Launch default job");
    await f.final(page, name, "Default job launched");
    const launchResult = result("launch_background_bash");
    expect(launchResult, "root launch result").toBeTruthy();
    expect(launchResult.isError, JSON.stringify(launchResult)).toBeFalsy();
    const job = JSON.parse(launchResult.content[0].text);
    expect(typeof job.job_id).toBe("string");
    expect(job.job_id.length).toBeGreaterThan(0);
    expect(Date.parse(job.deadline) - Date.parse(job.started_at)).toBe(600_000);
    await expect.poll(() => exists(ready), { timeout: 15_000 }).toBe(true);
    await f.configure({ turns_by_input: [{ contains: "Inspect default job", steps: [
      call("get", "get_background_bash_job", { job_id: job.job_id }), { text: "Default job inspected" },
    ] }] });
    await f.userTurn(page, "Inspect default job");
    await f.final(page, name, "Default job inspected");
    const get = result("get_background_bash_job");
    expect(get, "root snapshot result").toBeTruthy();
    expect(get.isError, JSON.stringify(get)).toBeFalsy();
    expect(JSON.parse(get.content[0].text)).toMatchObject({ job_id: job.job_id, status: "running", started_at: job.started_at, deadline: job.deadline });
    await f.idle(name);
    await writeFile(release, "release");
    const notice = `Background bash job ${job.job_id} finished: succeeded (exit code 0)`;
    await expect.poll(() => f.rows("SELECT CAST(body AS TEXT) AS body FROM messages WHERE id=?", `background-bash-completed-${job.job_id}`).map(row => row.body), { timeout: 15_000 }).toEqual([notice]);
    await expect.poll(async () => JSON.stringify(await f.inputs()), { timeout: 15_000 }).toContain(notice);
    expect(f.violations).toEqual([]);
  } finally {
    await writeFile(release, "release").catch(() => {});
    await f.close();
  }
});
