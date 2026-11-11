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
    await expect(widget.getByRole("button", { name: /shell jobs 0/i })).toHaveCSS("animation-name", "none");
    await widget.getByRole("button", { name: /shell jobs [0-9]+/i }).click();
    await expect(page.getByRole("dialog")).toContainText("No retained shell jobs");
    await page.getByRole("button", { name: "Close dialog" }).click();
    await f.configure({ turns_by_input: [{ contains: "Start widget jobs", steps: [
      launch("held", `printf 'before\\n'; read -r x < ${quote(release)}; printf 'middle\\n'; read -r x < ${quote(release)}; printf 'after\\n'; printf 'error-text\\n\\377\\n' >&2; exit 7`),
      launch("large", "head -c 70000 /dev/zero | tr '\\0' x"),
      { text: "Widget jobs launched" },
    ] }] });
    await f.userTurn(page, "Start widget jobs");
    await f.final(page, agent, "Widget jobs launched");
    const responses = (await f.records()).filter(r => r.kind === "mcp_call_response" && ["held", "large"].includes(String(r.tool_id)));
    expect(responses).toHaveLength(2);
    const ids = responses.map(r => JSON.parse((r.result as { content: { text: string }[] }).content[0].text).job_id as string);
    await expect(widget.getByRole("button", { name: /shell jobs 1/i })).toBeVisible({ timeout: 15000 });
    await expect(widget.getByRole("button", { name: /shell jobs [0-9]+/i })).toHaveCSS("animation-name", "header-subagents-pulse");
    let reads = 0;
    page.on("request", request => { if (request.url().includes("/background-bash-jobs/")) reads++; });
    await widget.getByRole("button", { name: /shell jobs [0-9]+/i }).click();
    const dialog = page.getByRole("dialog");
    await expect(dialog).not.toContainText("router restart");
    await expect(dialog).not.toContainText("Results are held in memory");
    for (const id of ids) await expect(dialog).not.toContainText(id);
    await expect(dialog.getByLabel("Filter:")).toHaveValue("active");
    await expect(dialog.locator(".background-bash-job-list li")).toHaveCount(1);
    const label = await dialog.locator(".background-bash-job-list li button span:first-child").innerText();
    expect(label).toMatch(/^Started /);
    await dialog.locator(".background-bash-job-list li").nth(0).getByRole("button").click();
    const held = dialog.locator(".subagent-drilldown-view");
    await expect(held).toContainText(label);
    await expect(held).not.toContainText(ids[0]);
    await expect(held.locator("pre code")).toContainText("before");
    await fifo.write("continue\n");
    await expect(held.locator("pre code")).toContainText("middle", { timeout: 10000 });
    expect(reads).toBeGreaterThanOrEqual(2);
    await dialog.getByRole("button", { name: "Back to shell jobs" }).click();
    await dialog.getByLabel("Filter:").selectOption("completed");
    await expect(dialog.locator(".background-bash-job-list li")).toHaveCount(1);
    await dialog.locator(".background-bash-job-list li").first().getByRole("button").click();
    const large = dialog.locator(".subagent-drilldown-view");
    await expect(large).toContainText("Output truncated", { timeout: 15000 });
    await expect(large.locator("pre code")).toHaveText("x".repeat(65536));
    await expect(large).toContainText("Exit code: 0");
    await dialog.getByRole("button", { name: "Back to shell jobs" }).click();
    await dialog.getByLabel("Filter:").selectOption("active");
    await dialog.locator(".background-bash-job-list li").nth(0).getByRole("button").click();
    await fifo.write("release\n");
    await expect(widget.getByRole("button", { name: /shell jobs 0/i })).toBeVisible({ timeout: 15000 });
    await expect(held).toContainText("failed", { timeout: 15000 });
    await expect(held).toContainText("Exit code: 7");
    await expect(held.locator("pre code")).toHaveText("before\nmiddle\nafter\nerror-text\n�\n");
    await expect(widget.getByRole("button", { name: /shell jobs [0-9]+/i })).toHaveCSS("animation-name", "none");
    // Exercise explicit retry and detail/projection disagreement
    // through the real router, modifying only the first two browser responses.
    await page.getByRole("button", { name: "Close dialog" }).click();
    const readsAfterClose = reads;
    await page.waitForTimeout(2500);
    expect(reads).toBe(readsAfterClose);
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
    await widget.getByRole("button", { name: /shell jobs [0-9]+/i }).click();
    await dialog.getByLabel("Filter:").selectOption("failed");
    await dialog.locator(".background-bash-job-list li").nth(0).getByRole("button").click();
    await expect(held).toContainText("Could not load output");
    await expect(held).not.toContainText("Output unavailable or expired");
    await held.getByRole("button", { name: "Retry" }).click();
    await expect(held).toContainText("failed");
    await expect(held).not.toContainText(ids[0]);
    await expect(held.locator("pre code")).toHaveText("before\nmiddle\nafter\nerror-text\n�\n");
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
