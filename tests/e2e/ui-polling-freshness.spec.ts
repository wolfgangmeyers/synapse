import { join } from "node:path";
import { test, expect } from "@playwright/test";
import { claudeWorkersHarness } from "./claude-workers-harness";

test("live projections avoid repeated fanout and recover after disconnect", async ({ page }) => {
  test.setTimeout(90_000);
  const f = await claudeWorkersHarness({ turns: [[
    { mcp_call: { id: "spawn-fallback", name: "subagent", input: { prompt: "Wait for stop", description: "Fallback child" } } },
    { text: "Fallback child launched" },
  ]] });
  try {
    let liveFrames = 0, recoveredHistory = 0;
    let measureRecovery = false;
    page.on("websocket", ws => {
      if (!ws.url().endsWith("/v1/ui-live")) return;
      ws.on("framereceived", frame => {
        if (String(frame.payload).includes('"type":"managed_children_snapshot"')) liveFrames++;
      });
    });
    let managed = 0, children = 0, agents = 0, goals = 0;
    page.on("request", request => {
      const path = new URL(request.url()).pathname;
      if (path === "/v1/managed-agents") managed++;
      if (/\/v1\/managed-agents\/[^/]+\/children$/.test(path)) children++;
      if (path === "/v1/agents") agents++;
      if (measureRecovery && path === "/v1/history") recoveredHistory++;
      if (/\/v1\/managed-agents\/[^/]+\/goal$/.test(path)) goals++;
    });
    await page.addInitScript(() => {
      const Native = window.WebSocket;
      let blocked = false;
      const live = new Set<WebSocket>();
      Object.assign(window, { blockLive(value: boolean) { blocked = value; if (value) live.forEach(socket => socket.close()); } });
      window.WebSocket = class extends Native {
        constructor(url: string | URL, protocols?: string | string[]) {
          super(url, protocols);
          if (String(url).endsWith("/v1/ui-live")) {
            live.add(this);
            this.addEventListener("open", () => { if (blocked) this.close(); });
            this.addEventListener("close", () => live.delete(this));
          }
        }
      };
    });
    await f.open(page);
    await expect.poll(() => liveFrames).toBeGreaterThan(0);
    expect(managed).toBe(1);
    const agent = await f.launch(page);
    await page.waitForTimeout(1500);
    const before = { managed, children, agents, goals };
    await page.waitForTimeout(6500);
    expect(managed - before.managed).toBe(0);
    expect(children - before.children).toBe(0);
    expect(agents - before.agents).toBeLessThanOrEqual(3);
    expect(goals).toBe(0);
    expect(children).toBe(0);
    await page.evaluate(() => (window as unknown as { blockLive(value: boolean): void }).blockLive(true));
    const fallback = managed;
    await expect.poll(() => managed - fallback, { timeout: 8000 }).toBeGreaterThan(0);
    expect(children).toBe(0);
    expect(goals).toBe(0);
    f.replies.push({ hold: true });
    await f.userTurn(page, "Launch fallback child");
    // The response must exist before reconnect, so this checks recovery of a missed update.
    await expect.poll(() => f.rows("SELECT 1 FROM embedded_entries WHERE agent_id=? AND kind='assistant' AND body LIKE ?", agent, "%Fallback child launched%").length).toBeGreaterThan(0);
    await expect.poll(() => f.rows("SELECT status FROM managed_subagents WHERE parent_agent_id=?", agent)).toEqual([{ status: "running" }]);
    await expect(page.getByRole("button", { name: /Subagents/ })).toBeVisible();
    await page.getByRole("button", { name: /Subagents/ }).click();
    await expect(page.getByText("Fallback child", { exact: true })).toBeVisible();
    measureRecovery = true;
    await page.evaluate(() => (window as unknown as { blockLive(value: boolean): void }).blockLive(false));
    const oldFrames = liveFrames;
    await expect.poll(() => liveFrames, { timeout: 12000 }).toBeGreaterThan(oldFrames);
    await expect.poll(() => recoveredHistory).toBeGreaterThan(0);
    await f.final(page, agent, "Fallback child launched", undefined, false);
  } finally { await f.close(); }
});

test("child viewer refreshes selected history without rapid list fanout", async ({ page }) => {
  test.setTimeout(90_000);
  const f = await claudeWorkersHarness({ turns: [[
    { mcp_call: { id: "spawn-view", name: "subagent", input: { prompt: "Wait for stop", description: "Viewer child" } } },
    { text: "Child launched" },
  ]] });
  try {
    f.replies.push({ hold: true });
    await f.open(page);
    const agent = await f.launch(page);
    await f.userTurn(page, "Launch viewer child");
    await f.final(page, agent, "Child launched");
    await expect.poll(() => f.rows("SELECT status FROM managed_subagents WHERE parent_agent_id=?", agent)).toEqual([{ status: "running" }]);
    let history = 0;
    page.on("request", req => { const url = new URL(req.url()); if (url.pathname === "/v1/history" && url.searchParams.get("agent_id") !== agent) history++; });
    await page.getByRole("button", { name: /Subagents/ }).click();
    await expect(page.getByText("Viewer child", { exact: true })).toBeVisible();
    await expect.poll(() => history).toBeGreaterThan(0);
    const before = history;
    await page.waitForTimeout(4500);
    expect(history - before).toBe(0);
    await page.getByText("Viewer child", { exact: true }).click();
    await expect.poll(() => history - before, { timeout: 5000 }).toBeGreaterThanOrEqual(2);
  } finally { await f.close(); }
});


test("switching worker views fetches only visible details and stays fresh", async ({ page }) => {
  test.setTimeout(90_000);
  const f = await claudeWorkersHarness();
  try {
    let perAgent = 0;
    page.on("request", request => {
      if (/\/v1\/managed-agents\/[^/]+\/(children|goal)$/.test(new URL(request.url()).pathname) && request.method() === "GET") perAgent++;
    });
    let workflowReads = 0;
    let releaseOld!: () => void;
    let oldFetched!: () => void;
    const oldReady = new Promise<void>(resolve => { oldFetched = resolve; });
    const release = new Promise<void>(resolve => { releaseOld = resolve; });
    await page.route("**/v1/workflow-states", async route => {
      workflowReads++;
      if (workflowReads === 1) {
        const response = await route.fetch();
        oldFetched();
        await release;
        await route.fulfill({ response });
      } else await route.continue();
    });
    await f.open(page);
    expect(workflowReads).toBe(0);
    const agent = await f.launch(page);
    const state = await f.api("/v1/workflow-states", { name: "Background state", description: "Test", entry_text: "Test", goal: "Finish" });
    const workflow = await f.api("/v1/workflows", { name: "Background test", entry_state_id: state.workflow_state.id, variables: [] });
    f.replies.push({ hold: true });
    const run = await f.api(`/v1/workflows/${workflow.id}/run`, { cwd: join(f.router.root, "work"), variables: {} });
    const background = run.agent_id;
    await expect(page.locator(`[data-agent-id="${agent}"]`)).toBeVisible();
    await expect(page.locator(`[data-agent-id="${background}"]`)).toHaveCount(0);
    await page.getByRole("tab", { name: /Background/ }).click();
    await oldReady;
    await page.getByRole("tab", { name: "Agents" }).click();
    const hiddenReads = workflowReads;
    await page.waitForTimeout(1000);
    expect(workflowReads).toBe(hiddenReads);
    await page.getByRole("tab", { name: /Background/ }).click();
    await expect.poll(() => workflowReads).toBeGreaterThan(hiddenReads);
    releaseOld();
    await expect(page.locator(`[data-agent-id="${agent}"]`)).toHaveCount(0);
    await expect(page.locator(`[data-agent-id="${background}"]`)).toBeVisible();
    await page.getByRole("tab", { name: "Agents" }).click();
    await expect(page.locator(`[data-agent-id="${agent}"]`)).toBeVisible();
    await expect(page.locator(`[data-agent-id="${background}"]`)).toHaveCount(0);
    expect(perAgent).toBe(0);
  } finally { await f.close(); }
});

for (const runtime of ["claude", "pi"] as const) {
  test(`${runtime} goal indicator follows live changes and HTTP fallback`, async ({ page }) => {
    test.setTimeout(90_000);
    const f = await claudeWorkersHarness();
    try {
      await page.addInitScript(() => {
        const Native = window.WebSocket;
        let blocked = false;
        const sockets = new Set<WebSocket>();
        Object.assign(window, { blockGoalLive(value: boolean) { blocked = value; if (value) sockets.forEach(socket => socket.close()); } });
        window.WebSocket = class extends Native {
          constructor(url: string | URL, protocols?: string | string[]) {
            super(url, protocols);
            if (String(url).endsWith("/v1/ui-live")) {
              sockets.add(this);
              this.addEventListener("open", () => { if (blocked) this.close(); });
              this.addEventListener("close", () => sockets.delete(this));
            }
          }
        };
      });
      await f.open(page);
      const agent = await f.launch(page, runtime === "pi" ? { model: { provider: "openai-codex", id: "gpt-6.1-terra" } } : {});
      const flag = page.locator(`[data-agent-id="${agent}"] .goal-indicator`);
      f.replies.push({ hold: true });
      await f.api(`/v1/managed-agents/${agent}/goal`, { action: "set", goal: "Fresh goal" }, "PUT");
      await expect(flag).toBeVisible();
      expect((await f.api(`/v1/managed-agents/${agent}/goal`)).goal_state).toMatchObject({ enabled: true, goal: "Fresh goal" });
      await f.api(`/v1/managed-agents/${agent}/goal`, { action: "off" }, "PUT");
      await expect(flag).toHaveCount(0);
      await page.evaluate(() => (window as unknown as { blockGoalLive(value: boolean): void }).blockGoalLive(true));
      await f.api(`/v1/managed-agents/${agent}/goal`, { action: "set", goal: "Fallback goal" }, "PUT");
      await expect(flag).toBeVisible({ timeout: 12000 });
      await f.api(`/v1/managed-agents/${agent}/goal`, { action: "off" }, "PUT");
      await expect(flag).toHaveCount(0, { timeout: 12000 });
      await page.evaluate(() => (window as unknown as { blockGoalLive(value: boolean): void }).blockGoalLive(false));
    } finally { await f.close(); }
  });
}

test("embedded self-goal is projected live while the turn continues", async ({ page }) => {
  test.setTimeout(90_000);
  const f = await claudeWorkersHarness({}, { omitGoogle: true });
  const frames: { type?: string; agent_id?: string; changes?: { goal_state?: { enabled?: boolean; goal?: string } } }[] = [];
  let goalReads = 0;
  page.on("request", request => {
    if (request.method() === "GET" && /\/v1\/managed-agents\/[^/]+\/goal$/.test(new URL(request.url()).pathname)) goalReads++;
  });
  page.on("websocket", socket => {
    if (new URL(socket.url()).pathname !== "/v1/ui-live") return;
    socket.on("framereceived", frame => {
      try { frames.push(JSON.parse(String(frame.payload))); } catch { /* Ignore non-JSON frames. */ }
    });
  });
  try {
    await f.open(page);
    const agent = await f.launch(page, { model: { provider: "openai-codex", id: "gpt-6.1-terra" } });
    await expect.poll(() => frames.some(frame => frame.type === "managed_agents_snapshot")).toBe(true);
    const flag = page.locator(`[data-agent-id="${agent}"] .goal-indicator`);
    await expect(flag).toHaveCount(0);
    const start = frames.length;
    f.replies.push({ tool: "goal", args: { action: "set", goal: "Finish the live projection task" } }, { hold: true });
    await f.userTurn(page, "Set your goal and keep working");
    await expect.poll(() => frames.slice(start).some(frame =>
      frame.type === "managed_agent_delta" && frame.agent_id === agent &&
      frame.changes?.goal_state?.enabled === true && frame.changes?.goal_state?.goal === "Finish the live projection task"
    ), { timeout: 15_000 }).toBe(true);
    await expect(flag).toBeVisible();
    await expect.poll(() => f.requests.length).toBe(2); // The second provider response remains held open.
    expect(goalReads).toBe(0);
    expect(f.violations).toEqual([]);
  } finally { await f.close(); }
});
