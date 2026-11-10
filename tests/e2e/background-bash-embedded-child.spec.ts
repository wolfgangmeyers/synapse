import { randomUUID } from "node:crypto";
import { readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { test, expect } from "@playwright/test";
import { workflowHarness, model, userTurn, type Reply } from "./workflow-implicit-goals-harness";

test.skip(process.platform !== "linux", "background bash requires Linux");

for (const mode of ["explicit", "promotion"] as const) {
  test(`embedded subagent ${mode} job retains its shell and completes`, async ({ page }) => {
    test.setTimeout(120_000);
    const name = `child-bg-${randomUUID()}`;
    const prompt = `child-task-${randomUUID()}`;
    let childStep = 0;
    let parentStep = 0;
    let command = "";
    let jobId = "";
    let releaseChild!: () => void;
    const childBarrier = new Promise<void>(resolve => { releaseChild = resolve; });
    const f = await workflowHarness({ inspectRequest: request => {
      const input = JSON.stringify(request.input);
      // Child input contains its task but not the parent user message. Parent
      // history can also contain the task as a subagent tool argument.
      const child = input.includes(prompt) && !input.includes("Start child background test");
      if (child) {
        if (childStep === 2) {
          const rows = f.rows("SELECT CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id IN (SELECT agent_id FROM managed_subagents WHERE parent_agent_id=?) AND kind IN ('toolResult','tool_result') ORDER BY rowid", name);
          const launch = rows.map(row => JSON.parse(String(row.body))).find(row => row.toolName === (mode === "explicit" ? "launch_background_bash" : "bash"));
          jobId = JSON.parse(launch.content[0].text).job_id;
        }
        const steps: Reply[] = [
          mode === "explicit" ? { tool: "launch_background_bash", args: { command } } : { tool: "bash", args: { command, timeout: 1 } },
          { tool: "list_background_bash_jobs", args: {} },
          { tool: "get_background_bash_job", args: { job_id: jobId } },
          { answer: "Child job launched", barrier: childBarrier },
        ];
        const reply = steps[childStep++];
        return reply ? { reply } : { error: `unexpected child request ${childStep}` };
      }
      const steps: Reply[] = [
        { tool: "subagent", args: { prompt, description: "Background child", max_turns: 5 } },
        { answer: "Child requested" },
        { answer: "Child completion received" },
      ];
      const reply = steps[parentStep++];
      return reply ? { reply } : { error: `unexpected parent request ${parentStep}` };
    } });
    const starts = join(f.router.root, "child-starts");
    const ends = join(f.router.root, "child-ends");
    const release = join(f.router.root, "child-release");
    const q = (s: string) => `'${s.replaceAll("'", "'\\''")}'`;
    command = `echo $$ >> ${q(starts)}; echo before; while [ ! -f ${q(release)} ]; do sleep .05; done; echo $$ >> ${q(ends)}; echo after; exit 7`;
    try {
      await f.api("/v1/managed-agents", { name, cwd: join(f.router.root, "work"), runtime: "embedded", model });
      await f.open(page);
      await expect(page.locator(`[data-agent-id="${name}"]`)).toBeVisible();
      await page.locator(`[data-agent-id="${name}"]`).click();
      await userTurn(page, "Start child background test");
      await expect.poll(() => f.rows("SELECT agent_id FROM managed_subagents WHERE parent_agent_id=?", name), { timeout: 30_000 }).toHaveLength(1);
      const child = String(f.rows("SELECT agent_id FROM managed_subagents WHERE parent_agent_id=?", name)[0].agent_id);
      const tool = mode === "explicit" ? "launch_background_bash" : "bash";
      const results = () => f.rows("SELECT CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? AND kind IN ('toolResult','tool_result') ORDER BY rowid", child)
        .map(row => JSON.parse(String(row.body))).filter(row => row.toolName === tool);
      await expect.poll(() => results().length, { timeout: 30_000 }).toBe(1);
      const result = results()[0];
      expect(result.isError, JSON.stringify(result)).toBeFalsy();
      const job = JSON.parse(result.content[0].text);
      expect(job).toMatchObject({ job_id: expect.any(String), status: "running" });
      expect(job.job_id).not.toBe("");
      const childResults = (toolName: string) => f.rows("SELECT CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? AND kind IN ('toolResult','tool_result') ORDER BY rowid", child)
        .map(row => JSON.parse(String(row.body))).filter(row => row.toolName === toolName);
      await expect.poll(() => childResults("list_background_bash_jobs").length, { timeout: 30_000 }).toBe(1);
      await expect.poll(() => childResults("get_background_bash_job").length, { timeout: 30_000 }).toBe(1);
      const list = childResults("list_background_bash_jobs")[0];
      const get = childResults("get_background_bash_job")[0];
      expect(list.isError, JSON.stringify(list)).toBeFalsy();
      expect(get.isError, JSON.stringify(get)).toBeFalsy();
      expect(JSON.parse(list.content[0].text)).toEqual([expect.objectContaining({ job_id: job.job_id, status: "running" })]);
      expect(JSON.parse(get.content[0].text)).toMatchObject({ job_id: job.job_id, status: "running" });
      const pid = (await readFile(starts, "utf8")).trim();
      expect(pid).toMatch(/^\d+$/);
      process.kill(Number(pid), 0);
      await writeFile(release, "release");
      await expect.poll(async () => readFile(ends, "utf8").catch(() => ""), { timeout: 30_000 }).toContain(pid);
      expect((await readFile(starts, "utf8")).trim().split("\n")).toEqual([pid]);
      expect((await readFile(ends, "utf8")).trim()).toBe(pid);
      await expect.poll(() => f.rows("SELECT CAST(body AS TEXT) AS body FROM messages WHERE id=? AND recipient=?", `background-bash-completed-${job.job_id}`, child).map(row => row.body), { timeout: 30_000 })
        .toEqual([`Background bash job ${job.job_id} finished: failed (exit code 7)`]);
      releaseChild();
      await expect.poll(() => f.rows("SELECT status, result_text FROM managed_subagents WHERE agent_id=? AND parent_agent_id=?", child, name), { timeout: 30_000 })
        .toEqual([{ status: "completed", result_text: "Child job launched" }]);
      await expect.poll(() => f.rows("SELECT CAST(body AS TEXT) AS body FROM messages WHERE id=? AND recipient=?", `subagent-completed-${child}-1`, name).map(row => row.body), { timeout: 30_000 })
        .toEqual([expect.stringContaining("Child job launched")]);
      expect(f.violations).toEqual([]);
    } finally {
      releaseChild();
      await writeFile(release, "release").catch(() => {});
      await f.close();
    }
  });
}
