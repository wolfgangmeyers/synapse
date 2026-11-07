import { test, expect } from "@playwright/test";
import { claudeWorkersHarness, type ClaudeWorkersHarness } from "./claude-workers-harness";
const selected = { provider: "claude-code", id: "haiku" };
const text = (value: string) => ({ text: value });

test.describe("Claude default goal evaluator", () => {
  test.setTimeout(90_000);
  let f: ClaudeWorkersHarness | undefined;
  test.afterEach(async () => { await f?.close(); f = undefined; });

  test("evaluates Claude goals without tools and completes the loop", async ({ page }) => {
    f = await claudeWorkersHarness({ turns_by_input: [
      { contains: "REASON: Take the next actionable step", steps: [text("First local step")] },
      { contains: "REASON: One local step remains", steps: [text("Final local step")] },
    ] }, { defaultModel: null });
    await f.configureEvaluator({ replies: [
      { output: "VERDICT: NOT_DONE\nREASON: One local step remains" },
      { output: "VERDICT: COMPLETED\nREASON: Finished" },
    ] });
    await f.open(page);
    expect((await f.request(page, "/v1/settings/default-model", { model: selected }, "PUT")).status).toBe(200);
    const agent = await f.launch(page);
    await f.api(`/v1/managed-agents/${agent}/goal`, { action: "set", goal: "Finish local work" }, "PUT");
    await f.final(page, agent, "First local step");
    await expect.poll(() => f!.tableRows("embedded_goal_continuations", agent).length).toBe(1);
    expect(f.tableRows("embedded_goal_evaluations", agent).map(row => row.state)).toEqual(["NOT_DONE"]);
    await f.advance(10_000);
    await f.final(page, agent, "Final local step");
    await expect.poll(() => f!.tableRows("embedded_goal_evaluations", agent).map(row => row.state)).toEqual(["NOT_DONE", "DONE"]);
    const evaluations = (await f.records()).filter(row => row.kind === "evaluator");
    expect(evaluations).toHaveLength(2);
    for (const invocation of evaluations) {
      const argv = invocation.argv as string[];
      expect(argv[argv.indexOf("--model") + 1]).toBe("haiku");
      expect(argv[argv.indexOf("--tools") + 1]).toBe("");
      expect(argv).toContain("--strict-mcp-config");
      expect(argv).not.toContain("--mcp-config");
      expect(String(invocation.input)).toContain("Finish local work");
      expect(String(invocation.input).length).toBeLessThan(30000);
    }
    expect(f.wsFrames.some(frame => frame.includes("Goal Evaluation"))).toBe(true);
    const inputs = await f.inputs(); await f.advance(60_000); await f.idle(agent);
    expect(await f.inputs()).toEqual(inputs);
    expect(f.violations).toEqual([]);
  });

  test("fences a superseded goal evaluation", async ({ page }) => {
    f = await claudeWorkersHarness({ turns_by_input: [{ contains: "REASON: Take the next actionable step", steps: [text("Original goal turn")] }] }, { defaultModel: null });
    await f.configureEvaluator({ replies: [{ barrier: "held", output: "VERDICT: NOT_DONE\nREASON: stale" }] });
    await f.open(page);
    expect((await f.request(page, "/v1/settings/default-model", { model: selected }, "PUT")).status).toBe(200);
    const agent = await f.launch(page);
    await f.api(`/v1/managed-agents/${agent}/goal`, { action: "set", goal: "Original goal" }, "PUT");
    await expect.poll(async () => (await f!.records()).some(row => row.kind === "evaluator")).toBe(true);
    await f.api(`/v1/managed-agents/${agent}/goal`, { action: "set", goal: "Revised goal" }, "PUT");
    const pid = Number((await f.records()).find(row => row.kind === "evaluator")?.pid);
    await f.releaseEvaluator("held");
    await expect.poll(() => { try { process.kill(pid, 0); return true; } catch { return false; } }).toBe(false);
    expect(f.tableRows("embedded_goal_evaluations", agent).map(row => row.state)).toEqual(["UNKNOWN"]);
    expect(f.wsFrames.some(frame => frame.includes("Goal Evaluation • NOT_DONE"))).toBe(false);
    await f.advance(60_000);
    expect(f.tableRows("embedded_goal_continuations", agent)).toEqual([]);
  });

  test("missing evaluator default shows actionable configuration diagnostic", async ({ page }) => {
    f = await claudeWorkersHarness({ turns_by_input: [{ contains: "REASON: Take the next actionable step", steps: [text("Turn before missing default")] }] }, { defaultModel: null });
    await f.open(page);
    const agent = await f.launch(page);
    await f.api(`/v1/managed-agents/${agent}/goal`, { action: "set", goal: "Stop without an evaluator default" }, "PUT");
    await expect(page.getByText(/goal evaluation unavailable: configure an authenticated router default/i).first()).toBeVisible();
    await expect.poll(() => f!.wsFrames.some(frame => /configure an authenticated router default/i.test(frame))).toBe(true);
    expect(f.tableRows("embedded_goal_evaluations", agent).map(row => row.state)).toEqual(["UNKNOWN"]);
    await f.advance(60_000);
    expect(f.tableRows("embedded_goal_continuations", agent)).toEqual([]);
  });

  test("failed Claude evaluator is visible and never schedules a continuation", async ({ page }) => {
    f = await claudeWorkersHarness({ turns_by_input: [{ contains: "REASON: Take the next actionable step", steps: [text("Turn before evaluator failure")] }] }, { defaultModel: null });
    await f.configureEvaluator({ replies: [{ exit: 7 }] });
    await f.open(page);
    expect((await f.request(page, "/v1/settings/default-model", { model: selected }, "PUT")).status).toBe(200);
    const agent = await f.launch(page);
    await f.api(`/v1/managed-agents/${agent}/goal`, { action: "set", goal: "Do not continue on failure" }, "PUT");
    await expect(page.getByText(/goal evaluation unavailable: CLI process failed/i).first()).toBeVisible();
    await expect.poll(() => f!.wsFrames.some(frame => /goal evaluation unavailable/i.test(frame))).toBe(true);
    expect(f.tableRows("embedded_goal_evaluations", agent).map(row => row.state)).toEqual(["UNKNOWN"]);
    await f.advance(60_000);
    expect(f.tableRows("embedded_goal_continuations", agent)).toEqual([]);
    expect(await f.inputs()).toHaveLength(1);
  });
  test("invalid evaluator output retains inconclusive continuation semantics", async ({ page }) => {
    f = await claudeWorkersHarness({ turns_by_input: [
      { contains: "REASON: Take the next actionable step", steps: [text("Initial progress")] },
      { contains: "Inconclusive evaluator output", steps: [text("Progress after inconclusive verdict")] },
    ] }, { defaultModel: null });
    await f.configureEvaluator({ replies: [ { output: "VERDICT: NOT COMPLETED\nREASON: work remains" }, { output: "VERDICT: COMPLETED\nREASON: Finished" } ] });
    await f.open(page);
    expect((await f.request(page, "/v1/settings/default-model", { model: selected }, "PUT")).status).toBe(200);
    const agent = await f.launch(page);
    await f.api(`/v1/managed-agents/${agent}/goal`, { action: "set", goal: "Finish after inconclusive" }, "PUT");
    await expect.poll(() => f!.tableRows("embedded_goal_continuations", agent).length).toBe(1);
    expect(f.tableRows("embedded_goal_evaluations", agent).map(row => row.state)).toEqual(["NOT_DONE"]);
    await f.advance(10_000);
    await f.final(page, agent, "Progress after inconclusive verdict");
    await expect.poll(() => f!.tableRows("embedded_goal_evaluations", agent).map(row => row.state)).toEqual(["NOT_DONE", "DONE"]);
  });

});
