import { test, expect } from "@playwright/test";
import { spawn } from "node:child_process";
import { readFileSync } from "node:fs";
import { createServer, type Socket } from "node:net";
import { claudeWorkersHarness } from "./claude-workers-harness";

test.skip(process.platform !== "linux", "Background bash process identity requires Linux");

// PID plus Linux start time prevents a recycled PID from looking like a leak.
// Zombies cannot execute and may be reaped later by the system's init process.
function snapshot(pid: number): { start: string; state: string } | undefined {
  try {
    const raw = readFileSync(`/proc/${pid}/stat`, "utf8");
    const fields = raw.slice(raw.lastIndexOf(")") + 2).trim().split(/\s+/);
    if (!/^\d+$/.test(fields[19] ?? "")) throw Error(`invalid process identity: ${pid}`);
    return { state: fields[0], start: fields[19] };
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code === "ENOENT" || (error as NodeJS.ErrnoException).code === "ESRCH") return undefined;
    throw error;
  }
}

for (const outcome of ["killed", "timed_out"] as const) {
  test(`background bash ${outcome} preserves an unrelated sentinel while stopping its shell and child`, async ({ page }) => {
    test.setTimeout(90_000);
    const f = await claudeWorkersHarness();
    const sentinel = spawn("sleep", ["60"], { stdio: "ignore", detached: true });
    const sentinelPID = sentinel.pid;
    if (!sentinelPID) throw Error("failed to start sentinel");
    const sentinelIdentity = snapshot(sentinelPID);
    expect(sentinelIdentity).toBeTruthy();
    const sockets = new Set<Socket>();
    let handshake = "";
    const server = createServer(socket => {
      sockets.add(socket);
      socket.on("close", () => sockets.delete(socket));
      socket.on("error", () => {});
      socket.on("data", data => { handshake += data.toString(); });
    });
    const call = (id: string, name: string, input: object) => ({ mcp_call: { id, name, input, require_catalog: true } });
    try {
      await new Promise<void>((resolve, reject) => { server.once("error", reject); server.listen(0, "127.0.0.1", resolve); });
      const address = server.address();
      if (!address || typeof address === "string") throw Error("missing fixture socket address");
      // Both processes expire naturally after 25 seconds even if the test or
      // router fails. The socket publishes identities only after child launch.
      const command = `sleep 25 & child=$!; printf '%s %s\\n' "$$" "$child" > /dev/tcp/127.0.0.1/${address.port}; wait "$child"`;
      await f.configure({ turns_by_input: [{ contains: "Launch cleanup job", steps: [
        call("launch", "launch_background_bash", { command, ...(outcome === "timed_out" ? { timeout: 5 } : {}) }),
        { text: "Cleanup job launched" },
      ] }] });
      await f.open(page);
      const agent = await f.launch(page);
      await f.userTurn(page, "Launch cleanup job");
      const results = (name: string) => f.rows("SELECT CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? AND kind IN ('toolResult','tool_result') ORDER BY rowid", agent)
        .map(row => JSON.parse(String(row.body))).filter(row => row.toolName === name);
      await expect.poll(() => results("launch_background_bash").length, { timeout: 15_000 }).toBe(1);
      const launch = results("launch_background_bash")[0];
      expect(launch.isError, JSON.stringify(launch)).toBeFalsy();
      const jobId = JSON.parse(launch.content[0].text).job_id;
      expect(typeof jobId).toBe("string");
      await expect.poll(() => handshake, { timeout: 4_000 }).toMatch(/^\d+ \d+\n$/);
      const witnessedAt = performance.now();
      const pids = handshake.trim().split(" ").map(Number);
      expect(new Set(pids).size).toBe(2);
      const identities = pids.map(pid => snapshot(pid));
      expect(identities.every(item => item && !["Z", "X", "x"].includes(item.state))).toBe(true);
      if (outcome === "killed") {
        await f.idle(agent);
        await f.configure({ turns_by_input: [{ contains: "Stop cleanup job", steps: [
          call("kill", "kill_background_bash_job", { job_id: jobId }), { text: "Cleanup job stopped" },
        ] }] });
        await f.userTurn(page, "Stop cleanup job");
        await expect.poll(() => results("kill_background_bash_job").length, { timeout: 15_000 }).toBe(1);
        const result = results("kill_background_bash_job")[0];
        expect(result.isError, JSON.stringify(result)).toBeFalsy();
        expect(JSON.parse(result.content[0].text)).toMatchObject({ job_id: jobId, status: outcome });
      }
      await expect.poll(() => f.rows("SELECT CAST(body AS TEXT) AS body FROM messages WHERE id=?", `background-bash-completed-${jobId}`).map(row => row.body), { timeout: 15_000 })
        .toEqual([`Background bash job ${jobId} finished: ${outcome}`]);
      await expect.poll(() => pids.map((pid, index) => {
        const current = snapshot(pid);
        return !!current && current.start === identities[index]?.start && !["Z", "X", "x"].includes(current.state);
      }), { timeout: 5_000, message: "both captured processes must stop, not merely the job record" }).toEqual([false, false]);
      // Do not mistake the fixture's 25-second natural expiry for cleanup.
      expect(performance.now() - witnessedAt).toBeLessThan(20_000);
      const aliveSentinel = snapshot(sentinelPID);
      expect(aliveSentinel?.start).toBe(sentinelIdentity?.start);
      expect(["Z", "X", "x"]).not.toContain(aliveSentinel?.state);
      expect(f.violations).toEqual([]);
    } finally {
      sentinel.kill("SIGKILL");
      for (const socket of sockets) socket.destroy();
      if (server.listening) await new Promise<void>(resolve => server.close(() => resolve()));
      await f.close();
    }
  });
}
