import { randomUUID } from "node:crypto";
import { readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { test, expect } from "@playwright/test";
import { workflowHarness, model, userTurn } from "./workflow-implicit-goals-harness";

test.skip(process.platform !== "linux", "background bash requires Linux");

for (const role of ["root", "worker"] as const) {
  test(`${role} promotes regular bash without restarting the shell`, async ({ page }) => {
    test.setTimeout(120_000);
    const name = `promotion-${role}-${randomUUID()}`;
    const f = await workflowHarness(role === "root" ? {
      embeddedRoot: { name, requestId: randomUUID(), model, instruction: "Root bootstrap" },
      initialReplies: [{ answer: "Root ready" }],
    } : {});
    const starts = join(f.router.root, "starts");
    const ends = join(f.router.root, "ends");
    const release = join(f.router.root, "release");
    const q = (s: string) => `'${s.replaceAll("'", "'\\''")}'`;
    const results = (tool: string) => f.rows(
      "SELECT CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? AND kind IN ('toolResult','tool_result') ORDER BY rowid", name,
    ).map(row => JSON.parse(String(row.body))).filter(row => row.toolName === tool);
    try {
      if (role === "worker") await f.api("/v1/managed-agents", { name, cwd: join(f.router.root, "work"), runtime: "embedded", model });
      await f.open(page);
      await expect(page.locator(`[data-agent-id="${name}"]`)).toBeVisible();
      await page.locator(`[data-agent-id="${name}"]`).click();
      if (role === "root") await expect(page.getByText("Root ready", { exact: true }).first()).toBeVisible({ timeout: 30_000 });
      await expect.poll(() => f.rows("SELECT role FROM embedded_sessions WHERE agent_id=? AND current=1", name), { timeout: 30_000 }).toEqual([{ role }]);
      f.replies.push({ tool: "bash", args: { command: `echo $$ >> ${q(starts)}; echo before-promotion; while [ ! -f ${q(release)} ]; do sleep .05; done; echo $$ >> ${q(ends)}; echo after-promotion; exit 7`, timeout: 1 } }, { answer: "Promotion returned" });
      await userTurn(page, "Run slow command");
      await expect(page.getByText("Promotion returned", { exact: true }).first()).toBeVisible({ timeout: 30_000 });
      expect(results("bash")).toHaveLength(1);
      const bash = results("bash")[0];
      expect(bash.isError, JSON.stringify(bash)).toBeFalsy();
      const job = JSON.parse(bash.content[0].text);
      expect(job).toMatchObject({ job_id: expect.any(String), status: "running" });
      expect(job.job_id).not.toBe("");
      const pid = (await readFile(starts, "utf8")).trim();
      expect(pid).toMatch(/^\d+$/);
      process.kill(Number(pid), 0);
      f.replies.push({ answer: "Completion acknowledged" });
      await writeFile(release, "release");
      const notice = `Background bash job ${job.job_id} finished: failed (exit code 7)`;
      await expect.poll(() => f.rows("SELECT CAST(body AS TEXT) AS body FROM messages WHERE id=?", `background-bash-completed-${job.job_id}`).map(row => row.body), { timeout: 30_000 }).toEqual([notice]);
      await expect(page.getByText("Completion acknowledged", { exact: true }).first()).toBeVisible({ timeout: 30_000 });
      expect(JSON.stringify(f.requests)).toContain(notice);
      expect((await readFile(starts, "utf8")).trim().split("\n")).toEqual([pid]);
      expect((await readFile(ends, "utf8")).trim()).toBe(pid);
      f.replies.push({ tool: "get_background_bash_job", args: { job_id: job.job_id } }, { answer: "Terminal inspected" });
      await userTurn(page, "Inspect completed command");
      await expect(page.getByText("Terminal inspected", { exact: true }).first()).toBeVisible({ timeout: 30_000 });
      expect(results("get_background_bash_job")).toHaveLength(1);
      const get = results("get_background_bash_job")[0];
      expect(get.isError, JSON.stringify(get)).toBeFalsy();
      const terminal = JSON.parse(get.content[0].text);
      expect(terminal).toMatchObject({ job_id: job.job_id, status: "failed", exit_code: 7 });
      expect(terminal.output).toContain("before-promotion");
      expect(terminal.output).toContain("after-promotion");
      expect(f.replies).toEqual([]);
      expect(f.violations).toEqual([]);
    } finally {
      await writeFile(release, "release").catch(() => {});
      await f.close();
    }
  });
}

test("root regular bash promotes at its default deadline", async ({ page }) => {
  test.setTimeout(90_000);
  const name = `promotion-default-${randomUUID()}`;
  const f = await workflowHarness({ embeddedRoot: { name, requestId: randomUUID(), model, instruction: "Root bootstrap" }, initialReplies: [{ answer: "Root ready" }] });
  const release = join(f.router.root, "default-release");
  const starts = join(f.router.root, "default-starts");
  const q = (s: string) => `'${s.replaceAll("'", "'\\''")}'`;
  try {
    await f.open(page);
    await page.locator(`[data-agent-id="${name}"]`).click();
    await expect(page.getByText("Root ready", { exact: true }).first()).toBeVisible({ timeout: 30_000 });
    f.replies.push({ tool: "bash", args: { command: `echo $$ >> ${q(starts)}; while [ ! -f ${q(release)} ]; do sleep .05; done` } }, { answer: "Default promoted" });
    const started = Date.now();
    await userTurn(page, "Run default slow command");
    await expect(page.getByText("Default promoted", { exact: true }).first()).toBeVisible({ timeout: 30_000 });
    expect(Date.now() - started).toBeGreaterThanOrEqual(14_000);
    const rows = f.rows("SELECT CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? AND kind IN ('toolResult','tool_result') ORDER BY rowid", name).map(row => JSON.parse(String(row.body))).filter(row => row.toolName === "bash");
    expect(rows).toHaveLength(1);
    expect(rows[0].isError).toBeFalsy();
    const job = JSON.parse(rows[0].content[0].text);
    expect(job).toMatchObject({ job_id: expect.any(String), status: "running" });
    const pid = (await readFile(starts, "utf8")).trim();
    expect(pid).toMatch(/^\d+$/);
    process.kill(Number(pid), 0);
    f.replies.push({ answer: "Default completion acknowledged" });
    await writeFile(release, "release");
    await expect.poll(() => f.rows("SELECT CAST(body AS TEXT) AS body FROM messages WHERE id=?", `background-bash-completed-${job.job_id}`).length, { timeout: 30_000 }).toBe(1);
    expect((await readFile(starts, "utf8")).trim()).toBe(pid);
    expect(f.violations).toEqual([]);
  } finally {
    await writeFile(release, "release").catch(() => {});
    await f.close();
  }
});
