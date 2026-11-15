import type { ChildProcess } from "node:child_process";
import { copyFile, mkdir, readFile, rm, writeFile } from "node:fs/promises";
import { basename, join } from "node:path";
import { test, expect } from "@playwright/test";
import { workflowHarness, createRunningWorkflow, settled, type WorkflowHarness } from "./workflow-implicit-goals-harness";

// Install the exit listener before signalling; an already-dead or stuck process
// must not hang either the scenario or failure diagnostics.
async function crashRouter(child: ChildProcess): Promise<void> {
  if (child.exitCode !== null || child.signalCode !== null) return;
  await new Promise<void>((resolve, reject) => {
    let timer: ReturnType<typeof setTimeout>;
    const finish = (error?: Error) => {
      clearTimeout(timer);
      child.removeListener("exit", onExit);
      child.removeListener("error", onError);
      error ? reject(error) : resolve();
    };
    const onExit = () => finish();
    const onError = (error: Error) => finish(error);
    child.once("exit", onExit);
    child.once("error", onError);
    timer = setTimeout(() => finish(new Error("router did not exit after SIGKILL")), 5000);
    if (child.exitCode !== null || child.signalCode !== null) finish();
    else if (!child.kill("SIGKILL") && child.exitCode === null && child.signalCode === null) finish(new Error("SIGKILL was not sent"));
  });
}

// A build overlay pauses the live router immediately before the real claim.
// The final and receipt are produced normally; SIGKILL is the only failure.
test("SCN-WFIG-019: saved final is checked after restart without replay", async ({ page }, testInfo) => {
  test.setTimeout(180000);
  let f: WorkflowHarness | undefined;
  let failure: unknown;
  try {
    f = await workflowHarness({ restartCodexConsumerFixture: true, preclaimBarrierFixture: true });
    await writeFile(join(f.router.root, "preclaim-armed"), "armed");
    f.replies.push({ tool: "workflow_snooze", args: { duration_ms: 300000 } }, { answer: "Preclaim saved final" });
    const current = await createRunningWorkflow(f, page, 600000, false);
    await expect.poll(async () => {
      try { return await readFile(join(f!.router.root, "preclaim-reached"), "utf8"); } catch (error) { if ((error as NodeJS.ErrnoException).code === "ENOENT") return ""; throw error; }
    }, { timeout: 30000 }).toBe(current.agent);
    await expect.poll(() => {
      const head = f!.rows("SELECT kind,body FROM embedded_entries WHERE agent_id=? ORDER BY seq DESC LIMIT 1", current.agent)[0];
      return head?.kind === "assistant" && Buffer.from(head.body as Uint8Array).toString("utf8").includes("Preclaim saved final");
    }).toBe(true);
    await expect.poll(() => f!.rows("SELECT execution_json FROM embedded_sessions WHERE agent_id=? AND current=1", current.agent)[0]?.execution_json).toBeTruthy();
    expect(f.rows("SELECT * FROM embedded_goal_evaluations WHERE agent_id=?", current.agent)).toHaveLength(0);
    expect(f.rows("SELECT * FROM embedded_goal_continuations WHERE agent_id=?", current.agent)).toHaveLength(0);
    expect(f.rows("SELECT lifecycle_state,last_error_code FROM managed_agents WHERE agent_id=?", current.agent)[0].lifecycle_state).not.toBe("failed");
    const oldOwner = Number(f.rows("SELECT owner_token FROM embedded_sessions WHERE agent_id=? AND current=1", current.agent)[0].owner_token);
    expect(f.rows("SELECT * FROM embedded_goal_applied_receipts WHERE agent_id=?", current.agent).length).toBeGreaterThan(0);
    const originalInputs = f.requests.map(request => JSON.stringify(request.input));
    const originalRequests = f.requests.length;
    expect(originalRequests).toBe(2);
    await crashRouter(f.router.process);
    await rm(join(f.router.root, "preclaim-armed"));
    await f.restart();
    await expect.poll(() => settled(f!, current.agent).length, { timeout: 15000 }).toBe(1);
    const recovered = settled(f, current.agent)[0];
    expect(recovered.state).toBe("NOT_DONE");
    expect(recovered.continuation_state).toBe("reserved");
    expect(recovered.started_at).toBeNull();
    expect(Number(recovered.owner_token)).toBeGreaterThan(oldOwner);
    expect(Buffer.from(recovered.final_body as Uint8Array).toString("utf8")).toContain("Preclaim saved final");
    expect(f.requests).toHaveLength(originalRequests);
    await crashRouter(f.router.process);
    f.replies.push({ answer: "Restart context restored" }, { hold: true });
    await f.restart();
    const nudges = () => f!.requests.filter(r => Array.isArray(r.input) && JSON.stringify(r.input.filter(item => item.role === "user").at(-1)?.content).includes(`The workflow ${current.workflow.name} is currently active in state A.`));
    // The clock is newly initialized on restart. Its response is rounded down
    // to seconds, so leave a full two-second guard before the durable deadline.
    const clock = await f.router.advanceClock(1);
    const due = Date.parse(String(recovered.due_at));
    const beforeDueDelta = due - Date.parse(clock.now) - 2000;
    expect(beforeDueDelta).toBeGreaterThan(0);
    await f.router.advanceClock(beforeDueDelta);
    await page.waitForTimeout(500);
    expect(f.requests).toHaveLength(originalRequests);
    expect(nudges()).toHaveLength(0);
    expect(settled(f, current.agent)[0].continuation_state).toBe("reserved");
    await f.router.advanceClock(3000);
    // Readiness can precede timer registration; tick to run any zero-delay timer.
    await expect.poll(async () => {
      if (!nudges().length) await f!.router.advanceClock(1000);
      return nudges().length;
    }, { timeout: 15000 }).toBe(1);
    await expect.poll(() => settled(f!, current.agent)[0]?.continuation_state).toBe("started");
    await f.router.advanceClock(60000);
    await page.waitForTimeout(500);
    expect(nudges()).toHaveLength(1);
    expect(f.requests).toHaveLength(originalRequests + 1);
    expect(f.requests.slice(0, originalRequests).map(request => JSON.stringify(request.input))).toEqual(originalInputs);
    expect(f.requests[originalRequests]).toBe(nudges()[0]);
    const sameFinal = settled(f, current.agent).filter(row => row.final_digest === recovered.final_digest);
    expect(sameFinal).toHaveLength(1);
    expect(sameFinal[0].continuation_state).toBe("started");
    expect(f.violations).toEqual([]);
  } catch (error) { failure = error; throw error; }
  finally {
    const errors: unknown[] = [];
    if (failure && f) {
      try {
        await crashRouter(f.router.process);
        const dir = testInfo.outputPath("preclaim-diagnostics");
        await mkdir(dir, { recursive: true, mode: 0o700 });
        for (const path of [f.router.databasePath, `${f.router.databasePath}-wal`, `${f.router.databasePath}-shm`, f.router.logPath]) {
          try { await copyFile(path, join(dir, basename(path))); }
          catch (error) { if ((error as NodeJS.ErrnoException).code !== "ENOENT" || path === f.router.databasePath) throw error; }
        }
        await writeFile(join(dir, "router-stderr.txt"), f.router.stderr());
        await writeFile(join(dir, "provider-diagnostics.json"), JSON.stringify({ requests: f.requests, violations: f.violations, clock: f.clock }, null, 2));
        console.error(`Preclaim diagnostics: ${dir}`);
      } catch (error) { errors.push(error); }
    }
    try { await f?.close(); } catch (error) { errors.push(error); }
    if (errors.length) throw new AggregateError(failure ? [failure, ...errors] : errors, "preclaim test diagnostics or cleanup failed", { cause: failure });
  }
});
