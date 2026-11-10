import { test, expect } from "@playwright/test";
import { readFileSync } from "node:fs";
import { readFile, readdir, writeFile } from "node:fs/promises";
import { basename, join } from "node:path";
import { claudeWorkersHarness } from "./claude-workers-harness";

function stat(pid: number) {
  try {
    const raw = readFileSync(`/proc/${pid}/stat`, "utf8");
    const fields = raw.slice(raw.lastIndexOf(")") + 2).trim().split(/\s+/);
    return { state: fields[0], group: Number(fields[2]), start: fields[19] };
  } catch (e) {
    if (["ENOENT", "ESRCH"].includes((e as NodeJS.ErrnoException).code ?? "")) return undefined;
    throw e;
  }
}

test.skip(process.platform !== "linux", "Linux process identity and orphan recovery");
for (const mode of ["promoted", "foreground"] as const) for (const mismatch of [false, true]) {
  test(`Claude child ${mode} Bash survives router crash and ${mismatch ? "unsafe identity fences admission" : "verified identity is recovered"}`, async ({ page }) => {
    test.setTimeout(120_000);
    const f = await claudeWorkersHarness();
    const leaderFile = join(f.fixtureRoot, "bash-leader");
    const childFile = join(f.fixtureRoot, "bash-descendant");
    const releaseFile = join(f.fixtureRoot, "bash-release");
    const orphanDir = join(f.router.root, "managed-home", "sessions", "claude-orphans");
    type Identity = { pid: number; start: string; group: number };
    let owned: Identity[] = [];
    const executable = (id: Identity) => {
      const current = stat(id.pid);
      return !!current && current.start === id.start && !/[ZXx]/.test(current.state);
    };
    // The shell publishes its own identity and waits before starting a descendant.
    // Even a missing/partial descendant marker leaves a verified group leader.
    const witness = async () => {
      for (const [path, isLeader] of [[leaderFile, true], [childFile, false]] as const) {
        let text: string;
        try { text = await readFile(path, "utf8"); } catch (e) {
          if ((e as NodeJS.ErrnoException).code === "ENOENT") continue;
          throw e;
        }
        if (!/^\d+\s*$/.test(text)) continue;
        const pid = Number(text.trim());
        const current = stat(pid);
        const group = isLeader ? pid : owned[0]?.pid;
        if (pid > 1 && current && current.group === group && current.start && !owned.some(id => id.pid === pid)) owned.push({ pid, ...current });
      }
    };
    const call = (id: string, name: string, input: object) => ({ mcp_call: { id, name, input, require_catalog: true } });
    try {
      await f.open(page);
      expect((await f.request(page, "/v1/settings/default-model", { model: { provider: "claude-code", id: "haiku" } }, "PUT")).status).toBe(200);
      await f.launch(page);
      // Both group members have a bounded lifetime if the runner itself dies.
      // The shell waits for a real descendant instead of replacing itself.
      const command = `echo $$ > '${leaderFile}'; for ((i=0;i<200;i++)); do [ -f '${releaseFile}' ] && break; sleep .05; done; [ -f '${releaseFile}' ] || exit 1; sleep 180 & child=$!; trap 'kill "$child" 2>/dev/null || :' EXIT; echo "$child" > '${childFile}'; wait "$child"`;
      await f.configure({ executions_by_input: [
        { contains: "Spawn crash child", turns: [[call("spawn", "subagent", { prompt: "Run crash shell", description: "Crash shell" }), { text: "Spawned" }]] },
        { contains: "Run crash shell", turns: [[call("bash", "bash", { command, timeout: mode === "promoted" ? 1 : 120 }), { barrier: "bash-returned" }, { text: "Done" }]] },
      ] });
      await f.userTurn(page, "Spawn crash child");
      await expect.poll(async () => { await witness(); return owned.length; }, { timeout: 30_000 }).toBe(1);
      await writeFile(releaseFile, "start");
      await expect.poll(async () => { await witness(); return owned.length; }, { timeout: 10_000 }).toBe(2);
      const [leader, descendant] = owned;
      const pid = leader.pid;
      expect(descendant.pid).not.toBe(pid);
      expect(leader.group).toBe(pid);
      expect(descendant.group).toBe(pid);
      expect(owned.map(executable)).toEqual([true, true]);
      const witnessedAt = performance.now();
      if (mode === "promoted") {
        await expect.poll(async () => (await f.records()).some(r => r.kind === "barrier" && r.name === "bash-returned"), { timeout: 20_000 }).toBe(true);
        const response = (await f.records()).find(r => r.kind === "mcp_call_response" && r.tool_id === "bash") as any;
        expect(response.error).toBeNull();
        expect(JSON.parse(response.result.content[0].text)).toMatchObject({ job_id: expect.any(String), status: "running" });
      } else {
        expect((await f.records()).some(r => r.kind === "mcp_call_response" && r.tool_id === "bash")).toBe(false);
      }
      expect(owned.map(executable)).toEqual([true, true]);
      const router = f.router.process;
      const exited = new Promise<void>(resolve => router.once("exit", () => resolve()));
      router.kill("SIGKILL");
      await exited;
      expect(owned.map(executable), "both witnessed processes survive the router crash").toEqual([true, true]);
      const records = await readdir(orphanDir);
      const matching: string[] = [];
      for (const name of records) {
        const path = join(orphanDir, name);
        const record = JSON.parse(await readFile(path, "utf8"));
        if (record.Identity.PID === pid) matching.push(path);
      }
      expect(matching).toHaveLength(1);
      let mismatchedRecord: string | undefined;
      if (mismatch) {
        const record = JSON.parse(await readFile(matching[0], "utf8"));
        expect(record.Identity.StartMarker).toBe(`linux:${leader.start}`);
        record.Identity.StartMarker += "-mismatch";
        mismatchedRecord = JSON.stringify(record);
        await writeFile(matching[0], mismatchedRecord);
      }
      await f.restart();
      expect((await fetch(f.proxy.url + "/v1/health")).status).toBe(200);
      if (mismatch) {
        expect(owned.map(executable), "unverified group must not be signalled").toEqual([true, true]);
        expect(await readdir(orphanDir)).toEqual([basename(matching[0])]);
        expect(await readFile(matching[0], "utf8")).toBe(mismatchedRecord);
        const starts = (await f.starts()).length;
        const rejected = await f.request(page, "/v1/managed-agents", { name: "fenced-claude", cwd: `${f.router.root}/work`, runtime: "embedded", model: { provider: "claude-code", id: "sonnet" } }, "POST");
        expect(rejected.status).toBe(503);
        expect(rejected.body.error.code).toBe("CLAUDE_ADMISSION_FENCED");
        expect(rejected.body.error.message).toMatch(/orphan recovery failed/i);
        expect((await f.starts()).length).toBe(starts);
        expect(f.rows("SELECT agent_id FROM managed_agents WHERE agent_id=?", "fenced-claude")).toEqual([]);
        expect(owned.map(executable), "both processes remain alive after rejection").toEqual([true, true]);
      } else {
        // A zombie is no longer executable; this test cannot reap a non-child.
        await expect.poll(() => owned.map(executable), { timeout: 10_000 }).toEqual([false, false]);
        expect(performance.now() - witnessedAt, "recovery must precede natural 180s expiry").toBeLessThan(60_000);
        expect(await readdir(orphanDir)).not.toContain(basename(matching[0]));
        await f.configure({ turns: [[{ text: "Recovered" }]] });
        const agent = await f.launch(page);
        await f.userTurn(page, "Confirm recovery");
        await f.final(page, agent, "Recovered", undefined, false);
      }
      expect(f.violations).toEqual([]);
    } finally {
      try {
        const errors: unknown[] = [];
        try {
          // Allow a command already dispatched at failure to publish its leader.
          const started = (await f.records()).some(r => r.kind === "mcp_call_started" && r.tool_id === "bash");
          if (started) await expect.poll(async () => { await witness(); return owned.length > 0; }, { timeout: 10_000, message: "dispatched shell must publish a capturable leader" }).toBe(true);
          else await witness();
        } catch (e) { errors.push(e); }
        // If the descendant marker is partial or absent, the verified leader's
        // group is still the cleanup boundary. Also attempt each captured PID,
        // even if an earlier signal fails.
        if (owned[0]) {
          try {
            const leader = stat(owned[0].pid);
            if (leader && leader.start === owned[0].start && leader.group === owned[0].pid && !/[ZXx]/.test(leader.state)) process.kill(-owned[0].pid, "SIGKILL");
          } catch (e) { if ((e as NodeJS.ErrnoException).code !== "ESRCH") errors.push(e); }
        }
        for (const id of owned) {
          try { if (executable(id)) process.kill(id.pid, "SIGKILL"); }
          catch (e) { if ((e as NodeJS.ErrnoException).code !== "ESRCH") errors.push(e); }
        }
        try {
          if (owned.length) await expect.poll(() => owned.map(executable), { timeout: 10_000, message: "teardown must stop witnessed processes" }).toEqual(owned.map(() => false));
        } catch (e) { errors.push(e); }
        if (errors.length) throw new AggregateError(errors, "crash fixture cleanup failed");
      } finally { await f.close(); }
    }
  });
}
