import { performance } from "node:perf_hooks";
import { join } from "node:path";
import { test, expect } from "@playwright/test";
import { workflowHarness, model, userTurn, type Reply } from "./workflow-implicit-goals-harness";

test.skip(process.platform !== "linux", "Background bash launch is supported only on Linux");

// BB-005: use the embedded Pi worker, whose foreground bash is governed by
// the router. Claude workers use native Bash, which has no router credit policy.
for (const exhaustFirst of [false, true]) {
  test(`worker background calls ${exhaustFirst ? "work at zero foreground credits" : "preserve foreground credits"}`, async ({ page }) => {
    test.setTimeout(120_000);
    // Strict request accounting is a test-fixture invariant, not an exactly-once delivery contract.
    let jobId = "";
    let noticeSeen = false;
    let noticeSeparate = false;
    let lastNoticeIndex = -1;
    let previousInput: string[] = [];
    const f = await workflowHarness({ inspectRequest: request => {
      if (!Array.isArray(request.input)) return undefined;
      const input = request.input as { role?: string; type?: string; name?: string; content?: { text?: string }[] }[];
      const prefix = "[Direct message from system]: ";
      const notices = input.flatMap(item => item.role === "user" ? (item.content ?? []).map(part => part.text ?? "").filter(text => text.includes("Background bash job ")) : []);
      if (!notices.length) return undefined;
      const sanitized = notices.map(text => jobId ? text.replaceAll(jobId, "<job_id>") : text.replace(/job [^\s]+ finished/g, "job <job_id> finished"));
      console.log(`background notice (${notices[0].startsWith(prefix) ? "direct" : "bare"}): ${JSON.stringify(sanitized)}`);
      const bare = `Background bash job ${jobId} finished: killed`;
      if (!jobId || notices.length !== 1 || (notices[0] !== bare && notices[0] !== prefix + bare))
        return { error: `unexpected or duplicate background notice: ${JSON.stringify(sanitized)}` };
      const noticeIndex = input.findIndex(item => item.role === "user" && item.content?.some(part => part.text === notices[0]));
      const currentInput = input.map(item => JSON.stringify(item));
      if (noticeSeen && (noticeIndex !== lastNoticeIndex || currentInput.length <= previousInput.length || previousInput.some((item, index) => item !== currentInput[index]))) return { error: "duplicate or reordered background notice" };
      lastNoticeIndex = noticeIndex;
      previousInput = currentInput;
      const lastIsNotice = noticeIndex === input.length - 1;
      if (!input.some(item => item.type === "function_call" && item.name === "kill_background_bash_job"))
        return { error: "background notice arrived before kill" };
      if (!noticeSeen) {
        noticeSeen = true;
        noticeSeparate = input.some(item => item.role === "assistant" && item.content?.some(part => part.text === "Background checked"));
        if (noticeSeparate) {
          if (!lastIsNotice) return { error: "unexpected continuation after background notice" };
          return { reply: { answer: "Completion acknowledged" } };
        }
      } else if (lastIsNotice) {
        return { error: "duplicate background notice request" };
      }
      return undefined;
    } });
    const name = `credit-audit-${exhaustFirst ? "empty" : "full"}`;
    const call = (tool: string, args: object): Reply => ({ tool, args });
    const bash = () => call("bash", { command: "printf foreground-credit" });
    const results = (tool: string) => f.rows(
      "SELECT CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? AND kind IN ('toolResult','tool_result') ORDER BY rowid", name,
    ).map(row => JSON.parse(String(row.body))).filter(row => row.toolName === tool);
    const success = (tool: string) => {
      const rows = results(tool);
      expect(rows).toHaveLength(1);
      expect(rows[0].isError, JSON.stringify(rows[0])).toBeFalsy();
      return JSON.parse(rows[0].content[0].text);
    };
    async function turn(steps: Reply[], text: string) {
      f.replies.push(...steps, { answer: text });
      const { now } = await f.router.advanceClock(1);
      await page.clock.setFixedTime(new Date(now));
      await userTurn(page, `Run ${text}`);
      await expect(page.getByText(text, { exact: true }).first()).toBeVisible({ timeout: 30_000 });
      await expect.poll(() => f.rows("SELECT activity_state FROM managed_agents WHERE agent_id=?", name)[0]?.activity_state, { timeout: 30_000 }).toBe("idle");
    }
    try {
      await f.api("/v1/managed-agents", { name, cwd: join(f.router.root, "work"), runtime: "embedded", model });
      await f.open(page);
      await expect(page.locator(`[data-agent-id="${name}"]`)).toBeVisible();
      await page.locator(`[data-agent-id="${name}"]`).click();
      await expect.poll(() => f.rows("SELECT role FROM embedded_sessions WHERE agent_id=? AND current=1", name)).toEqual([{ role: "worker" }]);
      expect(f.rows("SELECT allow_inline_tools FROM managed_agents WHERE agent_id=?", name)).toEqual([{ allow_inline_tools: 0 }]);
      const start = performance.now();
      await turn([...(exhaustFirst ? [bash(), bash(), bash()] : []), call("launch_background_bash", { command: "sleep 180" })], "Launch checked");
      const job = success("launch_background_bash");
      expect(typeof job.job_id).toBe("string");
      expect(job.job_id).not.toBe("");
      jobId = job.job_id;
      await turn([
        call("list_background_bash_jobs", {}),
        call("get_background_bash_job", { job_id: job.job_id }),
        call("kill_background_bash_job", { job_id: job.job_id }),
      ], "Background checked");
      await expect.poll(() => noticeSeen, { timeout: 30_000, message: "completion notice must reach the worker provider input" }).toBe(true);
      if (noticeSeparate) await expect(page.getByText("Completion acknowledged", { exact: true }).first()).toBeVisible({ timeout: 30_000 });
      await expect.poll(() => f.rows("SELECT activity_state FROM managed_agents WHERE agent_id=?", name)[0]?.activity_state, { timeout: 30_000 }).toBe("idle");
      await turn([...(!exhaustFirst ? [bash(), bash(), bash()] : []), bash()], "Foreground checked");
      expect(performance.now() - start, "audit must finish before a 60s foreground credit refill").toBeLessThan(60_000);
      expect(JSON.stringify(success("list_background_bash_jobs"))).toContain(job.job_id);
      expect(success("get_background_bash_job")).toMatchObject({ job_id: job.job_id, status: "running" });
      expect(success("kill_background_bash_job")).toMatchObject({ job_id: job.job_id, status: "killed" });
      const foreground = results("bash");
      expect(foreground).toHaveLength(4);
      expect(foreground.slice(0, 3).every(row => !row.isError), JSON.stringify(foreground)).toBe(true);
      expect(foreground[3].isError, JSON.stringify(foreground[3])).toBe(true);
      expect(JSON.stringify(foreground[3])).toContain("Inline tool budget exhausted");
      expect(f.rows("SELECT allow_inline_tools FROM managed_agents WHERE agent_id=?", name)).toEqual([{ allow_inline_tools: 0 }]);
      expect(f.replies).toEqual([]);
      expect(f.violations).toEqual([]);
    } finally {
      try { await f.close(); } finally { expect(f.violations).toEqual([]); }
    }
  });
}
