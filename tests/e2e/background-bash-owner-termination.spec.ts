import { test, expect } from "@playwright/test";
import { readFileSync } from "node:fs";
import { createServer, type Socket } from "node:net";
import { claudeWorkersHarness } from "./claude-workers-harness";

test.skip(process.platform !== "linux", "Background bash process identity requires Linux");

function identity(pid: number): { start: string; state: string } | undefined {
  try {
    const stat = readFileSync(`/proc/${pid}/stat`, "utf8");
    const fields = stat.slice(stat.lastIndexOf(")") + 2).trim().split(/\s+/);
    if (!/^\d+$/.test(fields[19] ?? "")) throw Error(`invalid process identity: ${pid}`);
    return { state: fields[0], start: fields[19] };
  } catch (error) {
    if (["ENOENT", "ESRCH"].includes((error as NodeJS.ErrnoException).code ?? "")) return undefined;
    throw error;
  }
}

// BB-011: terminate a real Claude owner while its background process group runs.
test("owner termination cleans running job and revokes its checked grant", async ({ page }) => {
  test.setTimeout(90_000);
  const f = await claudeWorkersHarness();
  const sockets = new Set<Socket>();
  let handshake = "";
  const server = createServer(socket => {
    sockets.add(socket);
    socket.on("close", () => sockets.delete(socket));
    socket.on("error", () => {});
    socket.on("data", data => { handshake += data.toString(); });
  });
  try {
    await new Promise<void>((resolve, reject) => { server.once("error", reject); server.listen(0, "127.0.0.1", resolve); });
    const address = server.address();
    if (!address || typeof address === "string") throw Error("missing fixture socket address");
    // Natural expiry bounds leaks even if router cleanup fails.
    const command = `sleep 30 & child=$!; printf '%s %s\\n' "$$" "$child" > /dev/tcp/127.0.0.1/${address.port}; wait "$child"`;
    await f.configure({ turns_by_input: [{ contains: "Launch termination job", steps: [
      { mcp_call: { id: "launch-termination", name: "launch_background_bash", input: { command }, require_catalog: true } },
      { barrier: "owner-before-termination" }, { text: "Owner released" },
    ] }] });
    await f.open(page);
    const agent = await f.launch(page);
    // This precedes the actual process start, so expiry cannot explain a
    // cleanup observed within the 25-second window below.
    const beforeLaunch = performance.now();
    await f.userTurn(page, "Launch termination job");
    await expect.poll(async () => (await f.records()).some(r => r.kind === "barrier" && r.name === "owner-before-termination"), { timeout: 20_000 }).toBe(true);
    const results = f.rows("SELECT CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? AND kind IN ('toolResult','tool_result') ORDER BY rowid", agent)
      .map(row => JSON.parse(String(row.body))).filter(row => row.toolName === "launch_background_bash");
    expect(results).toHaveLength(1);
    expect(results[0].isError, JSON.stringify(results[0])).toBeFalsy();
    const jobId = JSON.parse(results[0].content[0].text).job_id;
    expect(typeof jobId).toBe("string");
    await expect.poll(() => handshake, { timeout: 5_000 }).toMatch(/^\d+ \d+\n$/);
    const pids = handshake.trim().split(" ").map(Number);
    expect(new Set(pids).size).toBe(2);
    const captured = pids.map(identity);
    expect(captured.every(item => item && !["Z", "X", "x"].includes(item.state))).toBe(true);
    const grant = await f.grant();
    // Replay the already checked launch; the tool-use id makes this idempotent.
    const live = await f.mcp(grant, "launch_background_bash", { command }, "launch-termination");
    expect(live.status, JSON.stringify(live.body)).toBe(200);
    expect(live.body.error, JSON.stringify(live.body)).toBeUndefined();
    expect(live.body.result, JSON.stringify(live.body)).toBeTruthy();
    expect(live.body.result.isError, JSON.stringify(live.body)).not.toBe(true);
    expect(JSON.parse(live.body.result.content[0].text).job_id).toBe(jobId);
    const response = await f.request(page, `/v1/managed-agents/${agent}`, undefined, "DELETE");
    expect(response.status).toBe(202);
    // Successful termination removes the live agent row; desired_state is transient.
    await expect.poll(() => f.tableRows("managed_agents", agent).length, { timeout: 10_000 }).toBe(0);
    await expect.poll(() => pids.map((pid, i) => {
      const current = identity(pid);
      return !!current && current.start === captured[i]?.start && !["Z", "X", "x"].includes(current.state);
    }), { timeout: 10_000, message: "owner termination must stop the captured shell and child" }).toEqual([false, false]);
    expect(performance.now() - beforeLaunch).toBeLessThan(25_000);
    for (const [name, input] of [["get_background_bash_job", { job_id: jobId }], ["launch_background_bash", { command: "sleep 1" }]] as const) {
      const stale = await f.mcp(grant, name, input, `stale-${name}`);
      expect(stale.status, JSON.stringify(stale.body)).toBe(401);
      expect(stale.body.error).toEqual({ code: -32001, message: "invalid process grant" });
    }
    expect(f.violations).toEqual([]);
  } finally {
    for (const socket of sockets) socket.destroy();
    if (server.listening) await new Promise<void>(resolve => server.close(() => resolve()));
    await f.close();
  }
});
