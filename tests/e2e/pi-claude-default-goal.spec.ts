import { randomUUID } from "node:crypto";
import { test, expect, type Page, type WebSocket } from "@playwright/test";
import { claudeWorkersHarness } from "./claude-workers-harness";
import { model, userTurn } from "./workflow-implicit-goals-harness";

// History is a live-only broadcast. An agent row can arrive over HTTP before
// the UI-live subscription exists, so wait for its initial projection first.
function observeLiveSubscription(page: Page) {
  const sockets: { socket: WebSocket; snapshot: boolean }[] = [];
  page.on("websocket", socket => {
    if (new URL(socket.url()).pathname !== "/v1/ui-live") return;
    const observed = { socket, snapshot: false };
    sockets.push(observed);
    socket.on("framereceived", frame => {
      try {
        if (JSON.parse(String(frame.payload)).type === "managed_agents_snapshot") observed.snapshot = true;
      } catch { /* Other frames are checked by the application. */ }
    });
  });
  return async () => {
    await expect.poll(() => sockets.some(({ socket, snapshot }) => snapshot && !socket.isClosed()),
      { message: "UI-live socket must be subscribed before starting the goal", timeout: 8000 }).toBe(true);
  };
}

async function expectCompletedGoalNotification(frames: string[], agent: string) {
  await expect.poll(() => frames.flatMap(frame => {
    try { return [JSON.parse(frame)]; } catch { return []; }
  }).some(frame => frame?.type === "history_entry" && frame.view === "complete" &&
    frame.entry?.agent_id === agent && frame.entry?.message === "Goal Evaluation • COMPLETED" &&
    frame.entry?.presentation?.kind === "goal_evaluation" &&
    frame.entry?.presentation?.outcome === "completed")).toBe(true);
}

test("Pi foreground keeps its evaluator after the Claude default changes", async ({ page }) => {
  test.setTimeout(90_000);
  const f = await claudeWorkersHarness();
  const waitForLive = observeLiveSubscription(page);
  try {
    await f.open(page);
    const agent = `pi-default-goal-${randomUUID()}`;
    await f.api("/v1/managed-agents", { name: agent, cwd: `${f.router.root}/work`, runtime: "embedded", model });
    await expect(page.locator(`[data-agent-id="${agent}"]`)).toBeVisible();
    await page.locator(`[data-agent-id="${agent}"]`).click();
    expect((await f.request(page, "/v1/settings/default-model", { model: { provider: "claude-code", id: "haiku" } }, "PUT")).status).toBe(200);
    f.replies.push({ tool: "goal", args: { action: "set", goal: "Finish the Pi foreground task" } }, { answer: "Work finished" }, { evaluation: true, answer: "VERDICT: COMPLETED\nREASON: done" });
    await waitForLive();
    await userTurn(page, "Set a goal and complete the task.");
    await expect(page.getByText("Work finished", { exact: true }).first()).toBeVisible();
    expect((await f.records()).filter(row => row.kind === "evaluator")).toHaveLength(0);
    await expect.poll(() => f.tableRows("embedded_goal_evaluations", agent).map(row => row.state)).toEqual(["DONE"]);
    await expectCompletedGoalNotification(f.wsFrames, agent);
    expect(f.violations).toEqual([]);
  } finally { await f.close(); }
});

for (const mode of ["unset", "explicit-pi", "failed-claude"] as const) {
  test(`Pi foreground goal evaluator: ${mode}`, async ({ page }) => {
    test.setTimeout(90_000);
    const f = await claudeWorkersHarness({}, { defaultModel: null, ...(mode === "explicit-pi" ? { expectedCodexModel: "gpt-6-sol" } : {}) });
    const waitForLive = observeLiveSubscription(page);
    try {
      await f.open(page);
      const agent = `pi-goal-${randomUUID()}`;
      await f.api("/v1/managed-agents", { name: agent, cwd: `${f.router.root}/work`, runtime: "embedded", model: mode === "explicit-pi" ? { provider: model.provider, id: "gpt-6-sol" } : model });
      await expect(page.locator(`[data-agent-id="${agent}"]`)).toBeVisible();
      await page.locator(`[data-agent-id="${agent}"]`).click();
      if (mode === "explicit-pi") {
        expect((await f.request(page, "/v1/settings/default-model", { model }, "PUT")).status).toBe(200);
      } else if (mode === "failed-claude") {
        expect((await f.request(page, "/v1/settings/default-model", { model: { provider: "claude-code", id: "haiku" } }, "PUT")).status).toBe(200);
        await f.configureEvaluator({ replies: [{ exit: 7 }] });
      }
      f.replies.push({ tool: "goal", args: { action: "set", goal: "Finish the Pi task" } }, { answer: "Pi work finished" });
      f.replies.push({ evaluation: true, answer: "VERDICT: COMPLETED\nREASON: done" });
      await waitForLive();
      await userTurn(page, "Set a goal and finish the task.");
      await expect(page.getByText("Pi work finished", { exact: true }).first()).toBeVisible();
      await expect.poll(() => f.tableRows("embedded_goal_evaluations", agent).map(row => row.state)).toEqual(["DONE"]);
      await expectCompletedGoalNotification(f.wsFrames, agent);
      expect(f.requests).toHaveLength(3);
      if (mode === "explicit-pi") {
        expect(f.requests.map(request => request.model)).toEqual(["gpt-6-sol", "gpt-6-sol", "gpt-6-sol"]);
      }
      expect((await f.records()).filter(row => row.kind === "evaluator")).toHaveLength(0);
      expect(f.violations).toEqual([]);
    } finally { await f.close(); }
  });
}
