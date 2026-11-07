import { test, expect } from "@playwright/test";
import { claudeWorkersHarness, type ClaudeWorkersHarness } from "./claude-workers-harness";

const selected = { provider: "claude-code", id: "haiku" };

test.describe("Claude foreground evaluator QA regressions", () => {
  test.setTimeout(90_000);
  let f: ClaudeWorkersHarness | undefined;
  test.afterEach(async () => { await f?.close(); f = undefined; });

  test("configured Claude default becomes signed out after foreground admission", async ({ page }) => {
    f = await claudeWorkersHarness({ turns_by_input: [{ contains: "REASON: Take the next actionable step", steps: [{ barrier: "foreground" }, { text: "Foreground completed before sign-out" }] }] }, { defaultModel: null });
    await f.open(page);
    expect((await f.request(page, "/v1/settings/default-model", { model: selected }, "PUT")).status).toBe(200);
    const agent = await f.launch(page);
    await f.api(`/v1/managed-agents/${agent}/goal`, { action: "set", goal: "Do not continue when signed out" }, "PUT");
    await expect.poll(async () => (await f!.records()).some(row => row.kind === "barrier" && row.name === "foreground"), { timeout: 15_000 }).toBe(true);
    const receipt = (await f.records()).find(row => row.kind === "barrier" && row.name === "foreground")!;
    await f.configure({ auth: { loggedIn: false } });
    await f.releaseBarrier(receipt);
    await expect(page.getByText("Foreground completed before sign-out", { exact: true }).first()).toBeVisible();
    await expect(page.getByText(/goal evaluation unavailable: CLI unavailable or signed out/i).first()).toBeVisible();
    await expect.poll(() => f!.wsFrames.some(frame => /goal evaluation unavailable: CLI unavailable or signed out/i.test(frame))).toBe(true);
    expect(f.tableRows("embedded_goal_evaluations", agent).map(row => row.state)).toEqual(["UNKNOWN"]);
    expect((await f.records()).filter(row => row.kind === "evaluator")).toHaveLength(0);
    await f.advance(60_000);
    expect(f.tableRows("embedded_goal_continuations", agent)).toEqual([]);
    expect(await f.inputs()).toHaveLength(1);
    expect(f.violations).toEqual([]);
  });

  test("superseding a held evaluator rejects its result without a failure diagnostic", async ({ page }) => {
    f = await claudeWorkersHarness({ turns_by_input: [{ contains: "REASON: Take the next actionable step", steps: [{ text: "Original foreground completed" }] }] }, { defaultModel: null });
    await f.configureEvaluator({ replies: [{ barrier: "failing", exit: 7 }] });
    await f.open(page);
    expect((await f.request(page, "/v1/settings/default-model", { model: selected }, "PUT")).status).toBe(200);
    const agent = await f.launch(page);
    await f.api(`/v1/managed-agents/${agent}/goal`, { action: "set", goal: "Original goal" }, "PUT");
    await expect.poll(async () => (await f!.records()).some(row => row.kind === "evaluator"), { timeout: 15_000 }).toBe(true);
    const pid = Number((await f.records()).find(row => row.kind === "evaluator")?.pid);
    expect(pid).toBeGreaterThan(1);
    await f.api(`/v1/managed-agents/${agent}/goal`, { action: "set", goal: "Revised goal" }, "PUT");
    expect(() => process.kill(pid, 0)).not.toThrow();
    await f.releaseEvaluator("failing");
    await expect.poll(async () => (await f!.records()).some(row => row.kind === "evaluator_barrier_released" && row.pid === pid), { timeout: 15_000 }).toBe(true);
    await expect.poll(() => f!.rows("SELECT execution_json FROM embedded_sessions WHERE agent_id=? AND current=1", agent)[0]?.execution_json, { timeout: 15_000 }).toBeNull();
    await f.idle(agent);
    expect(f.tableRows("embedded_goal_evaluations", agent).map(row => row.state)).toEqual(["UNKNOWN"]);
    expect(f.tableRows("managed_complete_history", agent).filter(row => row.level === "error" && (row.category === "managed_goal_evaluation" || row.category === "managed_goal"))).toEqual([]);
    expect(f.wsFrames.some(frame => /goal evaluation unavailable/i.test(frame))).toBe(false);
    await expect(page.getByText(/goal evaluation unavailable/i)).toHaveCount(0);
    await f.advance(60_000);
    expect(f.tableRows("embedded_goal_continuations", agent)).toEqual([]);
    expect(await f.inputs()).toHaveLength(1);
    expect(f.violations).toEqual([]);
  });
});
