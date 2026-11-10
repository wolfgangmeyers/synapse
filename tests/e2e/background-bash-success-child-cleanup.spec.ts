import { test, expect } from "@playwright/test";
import { readFileSync } from "node:fs";
import { writeFile } from "node:fs/promises";
import { join } from "node:path";
import { claudeWorkersHarness, exists } from "./claude-workers-harness";

test.skip(process.platform !== "linux", "Background bash process identity requires Linux");

function processIdentity(pid: number): { state: string; start: string } | undefined {
  try {
    const raw = readFileSync(`/proc/${pid}/stat`, "utf8");
    const fields = raw.slice(raw.lastIndexOf(")") + 2).trim().split(/\s+/);
    if (!/^\d+$/.test(fields[19] ?? "")) throw Error(`invalid process identity: ${pid}`);
    return { state: fields[0], start: fields[19] };
  } catch (error) {
    if (["ENOENT", "ESRCH"].includes((error as NodeJS.ErrnoException).code ?? "")) return undefined;
    throw error;
  }
}

test("successful shell exit cleans its background child and reports success", async ({ page }) => {
  test.setTimeout(90_000);
  const f = await claudeWorkersHarness();
  const ready = join(f.fixtureRoot, "child-pid");
  const release = join(f.fixtureRoot, "release-success");
  const quote = (s: string) => `'${s.replaceAll("'", "'\\''")}'`;
  const call = (id: string, name: string, input: object) => ({ mcp_call: { id, name, input, require_catalog: true } });
  let child: { pid: number; start: string } | undefined;
  try {
    // Both processes have independent leak bounds, even if the test dies before
    // releasing the barrier. Publish the PID only after the complete write.
    const command = `sleep 120 & child=$!; printf '%s\n' "$child" > ${quote(`${ready}.tmp`)}; mv ${quote(`${ready}.tmp`)} ${quote(ready)}; n=0; while [ ! -f ${quote(release)} ]; do [ "$n" -lt 600 ] || exit 1; n=$((n + 1)); sleep 0.1; done; printf success-output; exit 0`;
    await f.configure({ turns_by_input: [{ contains: "Launch success cleanup", steps: [
      call("launch-success", "launch_background_bash", { command }), { text: "Success cleanup launched" },
    ] }] });
    await f.open(page);
    const agent = await f.launch(page);
    const launchedAt = performance.now();
    await f.userTurn(page, "Launch success cleanup");
    const results = (name: string) => f.rows("SELECT CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? AND kind IN ('toolResult','tool_result') ORDER BY rowid", agent)
      .map(row => JSON.parse(String(row.body))).filter(row => row.toolName === name);
    await expect.poll(() => results("launch_background_bash").length, { timeout: 15_000 }).toBe(1);
    const launch = results("launch_background_bash")[0];
    expect(launch.isError, JSON.stringify(launch)).toBeFalsy();
    const jobId = JSON.parse(launch.content[0].text).job_id;
    expect(typeof jobId).toBe("string");
    await expect.poll(() => exists(ready), { timeout: 10_000 }).toBe(true);
    const pid = Number(readFileSync(ready, "utf8"));
    expect(pid).toBeGreaterThan(1);
    const identity = processIdentity(pid);
    expect(identity && !["Z", "X", "x"].includes(identity.state)).toBe(true);
    child = { pid, start: identity!.start };
    await f.idle(agent);
    await writeFile(release, "release");
    const notice = `Background bash job ${jobId} finished: succeeded (exit code 0)`;
    await expect.poll(() => f.rows("SELECT CAST(body AS TEXT) AS body FROM messages WHERE id=?", `background-bash-completed-${jobId}`).map(row => row.body), { timeout: 15_000 }).toEqual([notice]);
    await expect.poll(() => {
      const current = processIdentity(pid);
      return !!current && current.start === identity?.start && !["Z", "X", "x"].includes(current.state);
    }, { timeout: 5_000, message: "the witnessed background child must stop" }).toBe(false);
    // Measured from before launch, not release: the child cannot have reached
    // its 120-second natural expiry anywhere in this observation window.
    expect(performance.now() - launchedAt).toBeLessThan(90_000);
    await expect.poll(async () => JSON.stringify(await f.inputs()), { timeout: 15_000 }).toContain(notice);
    await f.idle(agent);
    await f.configure({ turns_by_input: [{ contains: "Inspect success cleanup", steps: [
      call("get-success", "get_background_bash_job", { job_id: jobId }), { text: "Success cleanup inspected" },
    ] }] });
    await f.userTurn(page, "Inspect success cleanup");
    await expect.poll(() => results("get_background_bash_job").length, { timeout: 15_000 }).toBe(1);
    const result = results("get_background_bash_job")[0];
    expect(result.isError, JSON.stringify(result)).toBeFalsy();
    expect(JSON.parse(result.content[0].text)).toMatchObject({ job_id: jobId, status: "succeeded", exit_code: 0, output: "success-output" });
    expect(f.violations).toEqual([]);
  } finally {
    try {
      await writeFile(release, "release").catch(() => {});
      if (child && processIdentity(child.pid)?.start === child.start) {
        try { process.kill(child.pid, "SIGKILL"); } catch (error) {
          if ((error as NodeJS.ErrnoException).code !== "ESRCH") throw error;
        }
      }
    } finally {
      await f.close();
    }
  }
});
