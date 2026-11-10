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
    const agent = await f.launch(page);
    await expect.poll(() => liveFrames).toBeGreaterThan(0);
    await page.waitForTimeout(1500);
    const before = { managed, children, agents, goals };
    await page.waitForTimeout(6500);
    expect(managed - before.managed).toBe(0);
    expect(children - before.children).toBe(0);
    expect(agents - before.agents).toBeLessThanOrEqual(3);
    expect(goals - before.goals).toBeGreaterThan(0);
    await page.evaluate(() => (window as unknown as { blockLive(value: boolean): void }).blockLive(true));
    const fallback = managed;
    await expect.poll(() => managed - fallback, { timeout: 8000 }).toBeGreaterThan(0);
    f.replies.push({ hold: true });
    await f.userTurn(page, "Launch fallback child");
    await f.final(page, agent, "Fallback child launched", undefined, false);
    await expect.poll(() => f.rows("SELECT status FROM managed_subagents WHERE parent_agent_id=?", agent)).toEqual([{ status: "running" }]);
    await expect(page.getByRole("button", { name: /Subagents/ })).toBeVisible();
    await page.getByRole("button", { name: /Subagents/ }).click();
    await expect(page.getByText("Fallback child", { exact: true })).toBeVisible();
    measureRecovery = true;
    await page.evaluate(() => (window as unknown as { blockLive(value: boolean): void }).blockLive(false));
    const oldFrames = liveFrames;
    await expect.poll(() => liveFrames, { timeout: 12000 }).toBeGreaterThan(oldFrames);
    await expect.poll(() => recoveredHistory).toBeGreaterThan(0);
    await expect(page.getByText("Fallback child launched", { exact: true }).first()).toBeVisible();
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
