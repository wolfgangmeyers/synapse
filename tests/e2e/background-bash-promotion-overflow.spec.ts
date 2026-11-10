import { randomUUID } from "node:crypto";
import { readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { test, expect } from "@playwright/test";
import { workflowHarness, model, userTurn } from "./workflow-implicit-goals-harness";

test.skip(process.platform !== "linux", "background bash requires Linux");

test("foreground overflow drains and preserves bounded output after promotion", async ({ page }) => {
  test.setTimeout(120_000);
  const name = `promotion-overflow-${randomUUID()}`;
  const f = await workflowHarness({
    embeddedRoot: { name, requestId: randomUUID(), model, instruction: "Root bootstrap" },
    initialReplies: [{ answer: "Root ready" }],
  });
  const starts = join(f.router.root, "overflow-starts");
  const overflowed = join(f.router.root, "overflow-written");
  const ends = join(f.router.root, "overflow-ends");
  const release = join(f.router.root, "overflow-release");
  const q = (s: string) => `'${s.replaceAll("'", "'\\''")}'`;
  const results = (tool: string) => f.rows(
    "SELECT CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? AND kind IN ('toolResult','tool_result') ORDER BY rowid", name,
  ).map(row => JSON.parse(String(row.body))).filter(row => row.toolName === tool);
  try {
    await f.open(page);
    await expect(page.locator(`[data-agent-id="${name}"]`)).toBeVisible();
    await page.locator(`[data-agent-id="${name}"]`).click();
    await expect(page.getByText("Root ready", { exact: true }).first()).toBeVisible({ timeout: 30_000 });
    f.replies.push({ tool: "bash", args: {
      command: `echo $$ >> ${q(starts)}; head -c 70000 /dev/zero | tr '\\0' x; touch ${q(overflowed)}; while [ ! -f ${q(release)} ]; do sleep .05; done; echo $$ >> ${q(ends)}; exit 7`, timeout: 3,
    } }, { answer: "Overflow promoted" });
    await userTurn(page, "Run overflowing command");
    // The held command must finish writing even if promotion happens first.
    await expect.poll(async () => {
      return readFile(overflowed).then(() => true, () => false);
    }, { timeout: 30_000, intervals: [10] }).toBe(true);
    await expect(page.getByText("Overflow promoted", { exact: true }).first()).toBeVisible({ timeout: 30_000 });
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
    expect((await readFile(starts, "utf8")).trim().split("\n")).toEqual([pid]);
    expect((await readFile(ends, "utf8")).trim()).toBe(pid);
    f.replies.push({ tool: "get_background_bash_job", args: { job_id: job.job_id } }, { answer: "Overflow inspected" });
    await userTurn(page, "Inspect overflowing command");
    await expect(page.getByText("Overflow inspected", { exact: true }).first()).toBeVisible({ timeout: 30_000 });
    expect(results("get_background_bash_job")).toHaveLength(1);
    const get = results("get_background_bash_job")[0];
    expect(get.isError, JSON.stringify(get)).toBeFalsy();
    const terminal = JSON.parse(get.content[0].text);
    expect(terminal).toMatchObject({ job_id: job.job_id, status: "failed", exit_code: 7, truncated: true });
    expect(terminal.output).toBe("x".repeat(65536));
    expect(f.replies).toEqual([]);
    expect(f.violations).toEqual([]);
  } finally {
    await writeFile(release, "release").catch(() => {});
    await f.close();
  }
});
