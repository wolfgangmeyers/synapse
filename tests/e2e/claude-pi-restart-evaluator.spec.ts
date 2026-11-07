import { createServer } from "node:http";
import { randomBytes } from "node:crypto";
import { writeFile } from "node:fs/promises";
import { test, expect } from "@playwright/test";
import { claudeWorkersHarness } from "./claude-workers-harness";

// The restart scenario and fault-injection tests share the complete ownership scope.
async function withRestartResources<T>(
  server: ReturnType<typeof createServer>, release: () => void,
  setup: () => Promise<T>, body: (resource: T) => Promise<void>, close: (resource: T) => Promise<void>,
) {
  const errors: unknown[] = [];
  let resource: T | undefined;
  let acquired = false;
  try {
    resource = await setup();
    acquired = true;
    await body(resource);
  } catch (error) { errors.push(error); }
  finally {
    for (const cleanup of [release, acquired ? () => close(resource!) : undefined,
      () => new Promise<void>((resolve, reject) => server.close(error => error ? reject(error) : resolve()))]) {
      if (!cleanup) continue;
      try { await cleanup(); } catch (error) { errors.push(error); }
    }
  }
  if (errors.length === 1) throw errors[0];
  if (errors.length > 1) throw new AggregateError(errors, "Restart test and/or cleanup failed", { cause: errors[0] });
}

test("restart ownership closes listener when harness setup fails", async () => {
  const server = createServer();
  await new Promise<void>(resolve => server.listen(0, "127.0.0.1", resolve));
  const failure = new Error("setup");
  const calls: string[] = [];
  await expect(withRestartResources(server, () => { calls.push("release"); },
    async () => { calls.push("setup"); throw failure; },
    async () => { calls.push("body"); }, async () => { calls.push("close"); })).rejects.toBe(failure);
  expect(calls).toEqual(["setup", "release"]);
  expect(server.listening).toBe(false);
});

test("restart ownership preserves body and harness close failures and closes listener", async () => {
  const server = createServer();
  await new Promise<void>(resolve => server.listen(0, "127.0.0.1", resolve));
  const bodyFailure = new Error("body");
  const closeFailure = new Error("close");
  const calls: string[] = [];
  let thrown: unknown;
  try {
    await withRestartResources(server, () => { calls.push("release"); },
      async () => { calls.push("setup"); return {}; },
      async () => { calls.push("body"); throw bodyFailure; },
      async () => { calls.push("close"); throw closeFailure; });
  } catch (error) { thrown = error; }
  expect(calls).toEqual(["setup", "body", "release", "close"]);
  expect(server.listening).toBe(false);
  expect(thrown).toBeInstanceOf(AggregateError);
  if (!(thrown instanceof AggregateError)) throw Error("expected AggregateError");
  expect(thrown.errors).toEqual([bodyFailure, closeFailure]);
  expect(thrown.cause).toBe(bodyFailure);
});


test("restart cancels a held Pi-model evaluator for a Claude foreground", async ({ page }) => {
  test.setTimeout(90_000);
  let release!: () => void;
  const barrier = new Promise<void>(resolve => { release = resolve; });
  let released: boolean | undefined;
  const token = randomBytes(32).toString("hex");
  let readyAgent = "";
  const server = createServer((req, res) => {
    let body = "";
    req.on("data", chunk => { body += chunk; });
    req.on("end", () => {
      if (req.method !== "POST" || req.headers.authorization !== `Bearer ${token}`) { res.writeHead(403).end(); return; }
      const data = JSON.parse(body);
      if (req.url === "/restart-ready" && data.phase === "restart-ready") readyAgent = data.agent_id;
      res.writeHead(204).end();
    });
  });
  await new Promise<void>(resolve => server.listen(0, "127.0.0.1", resolve));
  await withRestartResources(server, release, async () => {
  const address = server.address();
  if (!address || typeof address === "string") throw Error("no listener");
  const f = await claudeWorkersHarness({ turns_by_input: [
    { contains: "REASON: Take the next actionable step", steps: [{ text: "Initial work" }] },
    { contains: "Post restart probe", steps: [{ text: "Post restart answer" }] },
  ] });
  return { f, address };
  }, async ({ f, address }) => {
    f.replies.push({ evaluation: true, answer: "VERDICT: NOT_DONE\nREASON: stale Pi response", barrier,
      onRelease: destroyed => { released = destroyed; } });
    await f.open(page);
    const agent = await f.launch(page);
    await f.api(`/v1/managed-agents/${agent}/goal`, { action: "set", goal: "Finish work" }, "PUT");
    await expect.poll(() => f.requests.length, { timeout: 15_000 }).toBe(1);
    expect(String(f.requests[0].instructions).toLowerCase()).toContain("goal:");
    expect((await f.records()).filter(row => row.kind === "evaluator")).toEqual([]);
    await writeFile(f.router.databasePath + ".restart-mail-barrier.json", JSON.stringify({ enabled: "1", endpoint: `http://127.0.0.1:${address.port}`, token, agent_id: agent }), { mode: 0o600 });
    const oldGeneration = f.tableRows("managed_agents", agent)[0]?.runtime_generation;
    expect(typeof oldGeneration).toBe("string");
    expect(oldGeneration).toBeTruthy();
    expect((await f.request(page, `/v1/managed-agents/${agent}/restart`, {})).status).toBe(202);
    // Claude retains the persisted runtime generation. This receipt is emitted
    // after the committed restart's reset gate reopens, not on old idle state.
    await expect.poll(() => readyAgent, { timeout: 15_000 }).toBe(agent);
    expect(f.tableRows("managed_agents", agent)[0]?.runtime_generation).toBe(oldGeneration);
    f.replies.push({ evaluation: true, answer: "VERDICT: COMPLETED\nREASON: fresh" });
    expect((await f.request(page, `/v1/managed-agents/${agent}/prompt`, { message: "Post restart probe" })).status).toBe(200);
    await f.final(page, agent, "Post restart answer");
    await expect.poll(async () => (await f.starts()).length, { timeout: 15_000 }).toBeGreaterThan(1);
    await expect.poll(() => f.tableRows("embedded_goal_evaluations", agent).map(row => row.state), { timeout: 15_000 }).toEqual(["UNKNOWN", "DONE"]);
    // The mock observes a destroyed response: no stale verdict is delivered.
    release();
    await expect.poll(() => released, { timeout: 15_000 }).toBe(true);
    await expect.poll(() => f.rows("SELECT execution_json FROM embedded_sessions WHERE agent_id=? AND current=1", agent)[0]?.execution_json, { timeout: 15_000 }).toBeNull();
    await f.advance(60_000);
    await f.idle(agent);
    expect(f.tableRows("embedded_goal_evaluations", agent).map(row => row.state)).toEqual(["UNKNOWN", "DONE"]);
    expect(f.tableRows("embedded_goal_continuations", agent)).toEqual([]);
    expect(f.tableRows("managed_complete_history", agent).filter(row => row.level === "error" && (row.category === "managed_goal_evaluation" || row.category === "managed_goal"))).toEqual([]);
    expect(f.wsFrames.some(frame => /goal evaluation unavailable/i.test(frame))).toBe(false);
    await expect(page.getByText(/goal evaluation unavailable/i)).toHaveCount(0);
    expect(await f.inputs()).toHaveLength(2);
    expect(f.wsFrames.some(frame => frame.includes("stale Pi response") || frame.includes("Goal Evaluation • NOT_DONE"))).toBe(false);
    expect(f.requests).toHaveLength(2);
    expect(f.violations).toEqual([]);
  }, async ({ f }) => { await f.close(); });
});
