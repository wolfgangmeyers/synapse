import { randomUUID } from "node:crypto";
import { performance } from "node:perf_hooks";
import { test, expect } from "@playwright/test";
import { claudeWorkersHarness } from "./claude-workers-harness";

test.skip(process.platform !== "linux", "Background bash launch is supported only on Linux");

// BB-005: spending foreground credits after all four background calls detects debits.
test("root background calls preserve all foreground inline credits", async ({ page }) => {
  test.setTimeout(120_000);
  const name = `background-root-${randomUUID()}`;
  const f = await claudeWorkersHarness({ turns_by_input: [{ contains: "Root bootstrap", steps: [{ text: "Root ready" }] }] }, {
    embeddedRoot: { name, requestId: randomUUID(), model: { provider: "claude-code", id: "sonnet" }, instruction: "Root bootstrap" },
  });
  const call = (id: string, tool: string, input: object) => ({ mcp_call: { id, name: tool, input, require_catalog: true } });
  const results = (tool: string) => f.rows(
    "SELECT CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? AND kind IN ('toolResult','tool_result') ORDER BY rowid", name,
  ).map(row => JSON.parse(String(row.body))).filter(row => row.toolName === tool);
  const success = (tool: string) => {
    const rows = results(tool);
    expect(rows).toHaveLength(1);
    expect(rows[0].isError, JSON.stringify(rows[0])).toBeFalsy();
    return JSON.parse(rows[0].content[0].text);
  };
  try {
    await f.open(page);
    await expect(page.locator(`[data-agent-id="${name}"]`)).toBeVisible();
    await page.locator(`[data-agent-id="${name}"]`).click();
    await f.final(page, name, "Root ready", undefined, false);
    expect(f.rows("SELECT role FROM embedded_sessions WHERE agent_id=? AND current=1", name)).toEqual([{ role: "root" }]);
    expect(f.tableRows("managed_agents", name)[0].allow_inline_tools).toBe(0);
    await f.configure({ turns_by_input: [{ contains: "Launch restricted", steps: [
      call("background-launch", "launch_background_bash", { command: "sleep 180" }),
      { text: "Launch checked" },
    ] }] });
    // Cover every possible debit through the last foreground call, before a credit can refill.
    const creditWindowStart = performance.now();
    await f.userTurn(page, "Launch restricted");
    await f.final(page, name, "Launch checked");
    const job = success("launch_background_bash");
    expect(typeof job.job_id).toBe("string");
    expect(job.job_id).not.toBe("");
    await f.configure({ turns_by_input: [{ contains: "Inspect restricted", steps: [
      call("background-list", "list_background_bash_jobs", {}),
      call("background-get", "get_background_bash_job", { job_id: job.job_id }),
      call("background-kill", "kill_background_bash_job", { job_id: job.job_id }),
      ...[1, 2, 3].map(i => call(`foreground-credit-${i}`, "bash", { command: "printf foreground-credit" })),
      call("foreground-denied", "bash", { command: "printf foreground-should-not-run" }),
      { text: "Background checked" },
    ] }] });
    await f.userTurn(page, "Inspect restricted");
    await f.final(page, name, "Background checked");
    expect(performance.now() - creditWindowStart, "credit audit must finish before the 60s refill").toBeLessThan(60_000);
    expect(JSON.stringify(success("list_background_bash_jobs"))).toContain(job.job_id);
    expect(success("get_background_bash_job")).toMatchObject({ job_id: job.job_id, status: "running" });
    expect(success("kill_background_bash_job")).toMatchObject({ job_id: job.job_id, status: "killed" });
    expect(f.tableRows("managed_agents", name)[0].allow_inline_tools).toBe(0);
    expect(f.violations).toEqual([]);
    const bash = results("bash");
    expect(bash).toHaveLength(4);
    expect(bash.slice(0, 3).every(row => !row.isError), JSON.stringify(bash)).toBe(true);
    expect(bash[3].isError, JSON.stringify(bash[3])).toBe(true);
    expect(JSON.stringify(bash[3])).toContain("Inline tool budget exhausted");
  } finally { await f.close(); }
});
