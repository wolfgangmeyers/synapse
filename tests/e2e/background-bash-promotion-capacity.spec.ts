import { randomUUID } from "node:crypto";
import { readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { test, expect } from "@playwright/test";
import { workflowHarness, model, userTurn } from "./workflow-implicit-goals-harness";

test.skip(process.platform !== "linux", "background bash requires Linux");

test("full background pool permits fast bash and cleans up rejected promotion", async ({ page }) => {
  test.setTimeout(180_000);
  const name = `promotion-capacity-${randomUUID()}`;
  const f = await workflowHarness({ embeddedRoot: { name, requestId: randomUUID(), model, instruction: "Root bootstrap" }, initialReplies: [{ answer: "Root ready" }] });
  const release = join(f.router.root, "capacity-release");
  const pidFile = join(f.router.root, "promotion-pid");
  const poolPids = join(f.router.root, "pool-pids");
  let launchedPids: number[] = [];
  const q = (s: string) => `'${s.replaceAll("'", "'\\''")}'`;
  const results = (tool: string) => f.rows("SELECT CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? AND kind IN ('toolResult','tool_result') ORDER BY rowid", name)
    .map(row => JSON.parse(String(row.body))).filter(row => row.toolName === tool);
  // Keep all legitimate background jobs blocked during each negative observation.
  // Three seconds spans the promotion timeout and multiple 100ms recovery retries.
  const assertNoCompletionNotice = async () => {
    const requestCount = f.requests.length;
    expect(f.replies).toEqual([]);
    const deadline = Date.now() + 3_000;
    do {
      expect(f.rows("SELECT id, body FROM messages WHERE recipient=? AND id LIKE 'background-bash-completed-%'", name)).toEqual([]);
      expect(JSON.stringify(f.requests)).not.toContain("Background bash job ");
      expect(f.requests).toHaveLength(requestCount);
      expect(f.violations).toEqual([]);
      await expect(page.getByText(/Background bash job .* finished/)).toHaveCount(0);
      await page.waitForTimeout(100);
    } while (Date.now() < deadline);
  };
  try {
    await f.open(page);
    await expect(page.locator(`[data-agent-id="${name}"]`)).toBeVisible();
    await page.locator(`[data-agent-id="${name}"]`).click();
    await expect(page.getByText("Root ready", { exact: true }).first()).toBeVisible({ timeout: 30_000 });
    f.replies.push(...Array.from({ length: 32 }, () => ({ tool: "launch_background_bash", args: { command: `echo $$ >> ${q(poolPids)}; for ((i=0; i<3000; i++)); do [ -f ${q(release)} ] && break; sleep .1; done` } })), { answer: "Pool filled" });
    await userTurn(page, "Fill pool");
    await expect(page.getByText("Pool filled", { exact: true }).first()).toBeVisible({ timeout: 90_000 });
    expect(results("launch_background_bash")).toHaveLength(32);
    expect(results("launch_background_bash").every(r => !r.isError)).toBe(true);
    await expect.poll(async () => (await readFile(poolPids, "utf8").catch(() => "")).trim().split("\n").filter(Boolean).length, { timeout: 30_000 }).toBe(32);
    launchedPids = (await readFile(poolPids, "utf8")).trim().split("\n").map(Number);
    expect(new Set(launchedPids).size).toBe(32);
    expect(launchedPids.every(pid => pid > 1)).toBe(true);
    f.replies.push({ tool: "bash", args: { command: "printf fast-foreground-ok", timeout: 3 } }, { answer: "Fast foreground checked" });
    await userTurn(page, "Check fast foreground");
    await expect(page.getByText("Fast foreground checked", { exact: true }).first()).toBeVisible({ timeout: 45_000 });
    expect(results("bash")).toHaveLength(1);
    expect(results("bash")[0].isError, JSON.stringify(results("bash")[0])).toBeFalsy();
    expect(JSON.stringify(results("bash")[0].content)).toContain("fast-foreground-ok");
    await assertNoCompletionNotice();

    f.replies.push({ tool: "bash", args: { command: `echo $$ > ${q(pidFile)}; for ((i=0; i<3000; i++)); do sleep .1; done`, timeout: 1 } }, { answer: "Rejected promotion checked" });
    await userTurn(page, "Check rejected promotion");
    await expect(page.getByText("Rejected promotion checked", { exact: true }).first()).toBeVisible({ timeout: 45_000 });
    const bash = results("bash");
    expect(bash).toHaveLength(2);
    expect(bash[1].isError, JSON.stringify(bash[1])).toBe(true);
    expect(JSON.stringify(bash[1].content)).toContain("background bash capacity reached during promotion");
    const pid = Number((await readFile(pidFile, "utf8")).trim());
    expect(pid).toBeGreaterThan(1);
    await expect.poll(() => { try { process.kill(pid, 0); return true; } catch (e) { if ((e as NodeJS.ErrnoException).code === "ESRCH") return false; throw e; } }, { timeout: 10_000 }).toBe(false);
    await assertNoCompletionNotice();
    expect(f.replies).toEqual([]);
    expect(f.violations).toEqual([]);
  } finally {
    await writeFile(release, "release").catch(() => {});
    try {
      await f.close();
    } finally {
      if (launchedPids.length) {
        await expect.poll(() => launchedPids.filter(pid => {
          try { process.kill(pid, 0); return true; } catch (e) { if ((e as NodeJS.ErrnoException).code === "ESRCH") return false; throw e; }
        }), { timeout: 10_000 }).toEqual([]);
      }
    }
  }
});
