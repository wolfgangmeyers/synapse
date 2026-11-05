import { readFileSync } from "node:fs";
import { test, expect } from "@playwright/test";
import { claudeWorkersHarness, exists, type ClaudeWorkersHarness } from "./claude-workers-harness";

// SPEC-LIFECYCLE-001 CC-009/011/015/016/018 recovery slice.
// Authored acceptance only: no execution/passing claim. No fake or production
// edits. Idle 15min/task 2h expiry is NOT covered: pool timers need a clock hook.
// Router crash/orphan sweep and domain-effect crash certainty remain separate.
const answer = (text: string) => ({ text });
const tool = (id: string, name: string, input: object) => ({ mcp_call: { id, name, input } });
const instruction = "Keep instruction: compass 🧭 north";
const note = "Scratchpad memory: violet 紫";
// A killed orphan can remain a zombie until its adopter reaps it. kill(pid, 0)
// alone counts that non-executable process (or a reused PID) as a live leak.
// Capture both fixture identities before release; never signal a replacement.
function processSnapshot(pid: number): { identity: string; state: string } | undefined {
  if (process.platform !== "linux") return undefined;
  try {
    const raw = readFileSync(`/proc/${pid}/stat`, "utf8");
    const fields = raw.slice(raw.lastIndexOf(")") + 2).trim().split(/\s+/);
    if (!/^\d+$/.test(fields[19] ?? "") || !/^[A-Za-z]$/.test(fields[0] ?? "")) {
      throw Error("invalid fixture process stat");
    }
    return { identity: `${pid}:${fields[19]}`, state: fields[0] };
  } catch (error) { if (error.code === "ENOENT" || error.code === "ESRCH") return undefined; throw error; }
}
function processIdentity(pid: number): string | undefined {
  return processSnapshot(pid)?.identity;
}
function alive(pid: number, captured?: string) {
  if (process.platform === "linux") {
    if (!captured) throw Error("missing captured fixture process identity");
    const current = processSnapshot(pid);
    return current !== undefined && current.identity === captured && !["Z", "X", "x"].includes(current.state);
  }
  try { process.kill(pid, 0); return true; }
  catch (error) { if (error.code === "ESRCH") return false; throw error; }
}

function signalVerified(pid: number, identity: string | undefined, signal: NodeJS.Signals) {
  if (!identity || processIdentity(pid) !== identity) return;
  try { process.kill(pid, signal); }
  catch (error) { if (error.code !== "ESRCH") throw error; }
}
function argvValue(row: Record<string, unknown>, flag: string) {
  if (!Array.isArray(row.argv)) throw Error("fixture start has no argv");
  const index = row.argv.indexOf(flag);
  const value = row.argv[index + 1];
  if (index < 0 || typeof value !== "string") throw Error(`fixture start missing ${flag}`);
  return value;
}


test.describe("Claude recovery and context acceptance", () => {
  test.setTimeout(90_000);
  let f: ClaudeWorkersHarness | undefined;
  let agent = "";
  const children = new Map<number, string | undefined>();
  test.afterEach(async ({}, info) => {
    try {
      if (f && info.status !== info.expectedStatus) await info.attach("claude-recovery", {
        contentType: "application/json", body: Buffer.from(JSON.stringify({
          records: await f.records(), ws: f.wsFrames, requests: f.requests, violations: f.violations,
          entries: agent ? f.rows("SELECT kind,epoch,seq,CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? ORDER BY epoch,seq", agent) : [],
        }, null, 2)),
      });
    } finally {
      try { await f?.close(); }
      finally {
        // spawn_child has a 60s self-limit; don't leave it behind on test failure.
        for (const [pid, identity] of children) signalVerified(pid, identity, "SIGKILL");
        children.clear(); f = undefined; agent = "";
      }
    }
  });
  async function childRecord() {
    await expect.poll(async () => (await f!.records()).some(row => row.kind === "child")).toBe(true);
    const child = (await f!.records()).findLast(row => row.kind === "child")!;
    const pid = Number(child.pid), parent = Number(child.parent_pid);
    if (!Number.isSafeInteger(pid) || pid <= 1 || !Number.isSafeInteger(parent) || parent <= 1) throw Error("invalid controlled child PID");
    const identity = processIdentity(pid), parentIdentity = processIdentity(parent);
    children.set(pid, identity);
    return { pid, identity, parent, parentIdentity };
  }

  for (const action of ["restart", "terminate"] as const) test(`CC-009/015 ${action} stops active process group and removes private grant`, async ({ page }) => {
    f = await claudeWorkersHarness({ turns: [[{ spawn_child: {} }, answer("Partial recovery output"), { wait: "interrupt" }]] });
    await f.open(page); agent = await f.launch(page); await f.userTurn(page, "Start bounded background child");
    const child = await childRecord(); const grant = await f.grant();
    await expect.poll(() => f!.tableRows("embedded_acks", agent).length).toBe(1);
    expect(alive(child.pid, child.identity)).toBe(true); expect(alive(child.parent, child.parentIdentity)).toBe(true);
    await f.configure({ turns: [[answer("Conversation after lifecycle action")]], turns_by_input: [
      { contains: "Continue the preserved conversation", steps: [answer("Conversation after lifecycle action")] },
    ] }); // next process only
    const response = action === "terminate"
      ? await f.request(page, `/v1/managed-agents/${agent}`, undefined, "DELETE")
      : await f.request(page, `/v1/managed-agents/${agent}/restart`, {});
    expect(response.status).toBe(202);
    await expect.poll(() => {
      const childAlive = alive(child.pid, child.identity);
      if (!childAlive) children.delete(child.pid);
      return childAlive || alive(child.parent, child.parentIdentity);
    }, { timeout: 5000 }).toBe(false);
    await expect.poll(() => exists(grant.path)).toBe(false);
    const stale = await f.mcp(grant, "mecha_context", { action: "status" }, "old-lifecycle-grant");
    expect(stale.status).not.toBe(200);
    expect(f.tableRows("embedded_acks", agent)).toHaveLength(1);
    if (action === "restart") {
      // Operation terminal state records admission, not completion of the
      // in-memory restart gate. Send the real composer turn once, without a
      // side-effecting readiness probe or a retry that could mask rejection.
      await f.userTurn(page, "Continue the preserved conversation");
      await f.final(page, agent, "Conversation after lifecycle action");
      const starts = await f.starts();
      expect(starts).toHaveLength(2);
      expect(argvValue(starts[1], "--resume")).toBe(argvValue(starts[0], "--session-id"));
      expect(JSON.stringify((await f.inputs()).at(-1))).toContain("Continue the preserved conversation");
      expect(f.rows("SELECT CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=?", agent)
        .some(row => String(row.body).includes("Start bounded background child"))).toBe(true);
    } else {
      await expect.poll(() => f!.tableRows("managed_agents", agent)[0].desired_state).toBe("terminated");
      expect(await f.starts()).toHaveLength(1);
    }
  });

  test("CC-015/016 missing transcript reseeds once with instruction scratchpad and recent history", async ({ page }) => {
    f = await claudeWorkersHarness({ turns: [[tool("memory-note", "scratchpad", { action: "append", content: note }), answer("Recent history: jade 玉")]] });
    await f.open(page); agent = await f.launch(page, { initial_instruction: instruction });
    await f.final(page, agent, "Recent history: jade 玉");
    const first = (await f.starts())[0]; const oldSession = argvValue(first, "--session-id");
    // The next execution attempts resume; reject it and accept the fresh seed.
    await f.configure({ resume_missing: true, turns: [[answer("Fresh seed after lost transcript")]] });
    await f.userTurn(page, "Continue after transcript loss"); await f.final(page, agent, "Fresh seed after lost transcript");
    const starts = await f.starts(); expect(starts).toHaveLength(3);
    expect(argvValue(starts[1], "--resume")).toBe(oldSession);
    expect(starts[2].argv).not.toContain("--resume");
    expect(argvValue(starts[2], "--session-id")).toMatch(/^[0-9a-f-]{36}$/);
    const seed = JSON.stringify((await f.inputs()).at(-1));
    for (const value of [instruction, note, "Recent history: jade 玉", "Continue after transcript loss"]) expect(seed).toContain(value);
    expect(f.tableRows("embedded_acks", agent)).toHaveLength(1); // initial instruction is non-mail
    expect(f.tableRows("embedded_attempts", agent)).toHaveLength(1);
    expect(f.violations).toEqual([]);
  });

  test("CC-011 reset seed orders instruction notes recent history and wake with valid Unicode under 200k characters", async ({ page }) => {
    // Large history belongs at the CLI output boundary, not the browser's
    // 4KiB composer/mail input boundary. Each output stays below the fixture's
    // 1MiB script limit and the store's per-entry limit.
    // Keep each live history frame within the UI's 65,536 UTF-16-unit
    // limit while exceeding the reset budget across multiple checked turns.
    const oldest = "OLDEST_DROP_CANDIDATE:" + "古🦊".repeat(20000);
    const middle = Array.from({ length: 4 }, (_, i) => `MIDDLE_${i}:` + "中🐺".repeat(20000));
    const newest = "NEWEST_PRESERVE:" + "新🧭".repeat(20000) + ":TAIL_SENTINEL";
    const outputs = [oldest, ...middle, newest];
    f = await claudeWorkersHarness({
      turns: [[tool("unicode-note", "scratchpad", { action: "append", content: note }), answer("History before large turns")]],
      turns_by_input: outputs.map((output, index) => ({ contains: `Produce large fixture history ${index}`, steps: [answer(output)] })),
    });
    await f.open(page); agent = await f.launch(page, { initial_instruction: instruction });
    await f.final(page, agent, "History before large turns");
    for (const [index, output] of outputs.entries()) {
      const prompt = `Produce large fixture history ${index}`;
      const visiblePrefix = output.slice(0, output.indexOf(":") + 1);
      expect(Buffer.byteLength(prompt)).toBeLessThan(4096);
      await f.userTurn(page, prompt); await f.final(page, agent, output, visiblePrefix);
    }
    await f.configure({ turns: [[answer("Bounded Unicode reset complete")]] });
    expect((await f.request(page, `/v1/managed-agents/${agent}/reset`, {})).status).toBe(202);
    await f.final(page, agent, "Bounded Unicode reset complete");
    const input = (await f.inputs()).at(-1);
    const blocks = input.message.content;
    expect(Array.isArray(blocks)).toBe(true);
    const seed = blocks.filter((block: { type: string }) => block.type === "text").map((block: { text: string }) => block.text).join("\n");
    expect(Array.from(seed).length).toBeLessThanOrEqual(200000);
    expect(seed).not.toContain("\ufffd");
    let previous = -1;
    for (const value of [instruction, note, "NEWEST_PRESERVE:", ":TAIL_SENTINEL", "Context reset completed"]) {
      const index = seed.indexOf(value); expect(index).toBeGreaterThan(previous); previous = index;
    }
    expect(seed).toContain(newest); // markers alone would miss truncation inside the Unicode body
    expect(seed).not.toContain("OLDEST_DROP_CANDIDATE:"); // reject partial retention too
    expect(f.tableRows("scratchpad_entries", agent).map(row => row.content)).toContain(note);
    expect(f.tableRows("managed_agents", agent)[0].session_epoch).toBe(2);
    await expect(page.getByText("History before large turns", { exact: true }).first()).toBeVisible();
  });


  for (const width of [1280, 1000]) test(`CC-009 interrupt at ${width}px preserves goal and reaps foreground`, async ({ page }) => {
    await page.setViewportSize({ width, height: 800 });
    f = await claudeWorkersHarness({ turns: [[tool("background-goal", "goal", { action: "set", goal: "Preserve goal after interrupt" }),
      { spawn_child: {} }, answer("Foreground before interrupt"), { wait: "interrupt" }]] }, { defaultModel: null });
    await f.open(page); agent = await f.launch(page); await f.userTurn(page, "Set a goal then hold foreground");
    const child = await childRecord();
    expect(alive(child.pid, child.identity)).toBe(true);
    expect(alive(child.parent, child.parentIdentity)).toBe(true);
    await expect.poll(() => f!.tableRows("embedded_goal_calls", agent).some(row => row.state === "complete")).toBe(true);
    const grant = await f.grant();
    const interrupt = page.getByRole("button", { name: "Interrupt worker", exact: true });
    await expect(interrupt).toBeVisible();
    await interrupt.click({ timeout: 5000 });
    await f.idle(agent);
    await expect.poll(() => alive(child.pid, child.identity) || alive(child.parent, child.parentIdentity), { timeout: 5000 }).toBe(false);
    children.delete(child.pid);
    await expect(interrupt).toHaveCount(0);
    await expect.poll(() => exists(grant.path), { timeout: 5000 }).toBe(false);
    expect((await f.api(`/v1/managed-agents/${agent}/goal`)).goal_state.enabled).toBe(true);
    expect(f.tableRows("embedded_goal_evaluations", agent)).toEqual([]);
    expect(f.requests).toEqual([]); expect(f.violations).toEqual([]);
  });

});
