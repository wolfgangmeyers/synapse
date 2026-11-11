import { test, expect } from "@playwright/test";
import { open } from "node:fs/promises";
import { constants } from "node:fs";
import { execFileSync } from "node:child_process";
import { join } from "node:path";
import { claudeWorkersHarness } from "./claude-workers-harness";

test.skip(process.platform !== "linux", "Background bash launch requires Linux");

test("widget counts active jobs and shows retained bounded output and exit codes", async ({ page }) => {
  test.setTimeout(120_000);
  const f = await claudeWorkersHarness({}, { backgroundCountRetentionFixture: true });
  const release = join(f.fixtureRoot, "widget-release");
  execFileSync("mkfifo", [release]);
  const fifo = await open(release, constants.O_RDWR | constants.O_NONBLOCK);
  const quote = (s: string) => `'${s.replaceAll("'", "'\\''")}'`;
  const launch = (id: string, command: string) => ({ mcp_call: { id, name: "launch_background_bash", input: { command }, require_catalog: true } });
  try {
    await f.open(page);
    const agent = await f.launch(page);
    const widget = page.getByRole("region", { name: "Background shell jobs" });
    await expect(widget.getByRole("button", { name: /shell jobs 0/i })).toBeVisible();
    await widget.getByRole("button").click();
    await expect(page.getByRole("dialog")).toContainText("No retained shell jobs");
    await page.getByRole("button", { name: "Close dialog" }).click();
    await f.configure({ turns_by_input: [{ contains: "Start widget jobs", steps: [
      launch("held", `printf 'before\\n'; read -r x < ${quote(release)}; printf 'after\\n'; printf 'error-text\\n\\377\\n' >&2; exit 7`),
      launch("large", "head -c 70000 /dev/zero | tr '\\0' x"),
      { text: "Widget jobs launched" },
    ] }] });
    await f.userTurn(page, "Start widget jobs");
    await f.final(page, agent, "Widget jobs launched");
    const responses = (await f.records()).filter(r => r.kind === "mcp_call_response" && ["held", "large"].includes(String(r.tool_id)));
    expect(responses).toHaveLength(2);
    const ids = responses.map(r => JSON.parse((r.result as { content: { text: string }[] }).content[0].text).job_id as string);
    await expect(widget.getByRole("button", { name: /shell jobs 1/i })).toBeVisible({ timeout: 15000 });
    await widget.getByRole("button").click();
    const dialog = page.getByRole("dialog");
    const held = dialog.locator("li").filter({ hasText: ids[0] });
    const large = dialog.locator("li").filter({ hasText: ids[1] });
    await expect(held.locator("pre code")).toContainText("before");
    await expect(large).toContainText("Output truncated", { timeout: 15000 });
    await expect(large.locator("pre code")).toHaveText("x".repeat(65536));
    await fifo.write("release\n");
    await expect(widget.getByRole("button", { name: /shell jobs 0/i })).toBeVisible({ timeout: 15000 });
    await expect(held).toContainText("failed", { timeout: 15000 });
    await expect(held).toContainText("Exit code: 7");
    await expect(held.locator("pre code")).toHaveText("before\nafter\nerror-text\n�\n");
    await expect(large).toContainText("Exit code: 0");
    // Exercise explicit retry and detail/projection disagreement
    // through the real router, modifying only the first two browser responses.
    await page.getByRole("button", { name: "Close dialog" }).click();
    let detailRequests = 0;
    const detailPattern = `**/background-bash-jobs/${ids[0]}`;
    await page.route(detailPattern, async route => {
      detailRequests++;
      if (detailRequests === 1) {
        await route.fulfill({ status: 503, contentType: "application/json", body: JSON.stringify({ error: "temporary" }) });
      } else if (detailRequests === 2) {
        const response = await route.fetch();
        await route.fulfill({ response, json: { ...(await response.json()), status: "running" } });
      } else {
        await route.continue();
      }
    });
    await widget.getByRole("button").click();
    await expect(held).toContainText("Could not load output");
    await expect(held).not.toContainText("Output unavailable or expired");
    await held.getByRole("button", { name: "Retry" }).click();
    await expect(held).toContainText(`${ids[0]}: failed`);
    await expect(held.locator("pre code")).toHaveText("before\nafter\nerror-text\n�\n");
    await page.unroute(detailPattern);
    const missing = await f.request(page, `/v1/managed-agents/${encodeURIComponent(agent)}/background-bash-jobs/missing-job`, undefined, "GET");
    expect(missing.status).toBe(404);
    await page.getByRole("button", { name: "Close dialog" }).click();
    const other = await f.launch(page);
    const forbidden = await f.request(page, `/v1/managed-agents/${encodeURIComponent(other)}/background-bash-jobs/${encodeURIComponent(ids[0])}`, undefined, "GET");
    expect(forbidden.status).toBe(404);
    expect(JSON.stringify(forbidden.body)).not.toContain("error-text");
    const projection = await f.request(page, `/v1/managed-agents/${encodeURIComponent(agent)}`, undefined, "GET");
    expect(JSON.stringify(projection.body.agent.background_bash_jobs)).not.toContain("error-text");
    expect(f.violations).toEqual([]);
  } finally { await fifo.write("release\n").catch(() => {}); await fifo.close(); await f.close(); }
});
