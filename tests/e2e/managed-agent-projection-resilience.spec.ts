import { expect, test, type Page } from "@playwright/test";
import { join } from "node:path";
import { model, userTurn, workflowHarness } from "./workflow-implicit-goals-harness";

type AgentView = { agent_id: string; lifecycle_state: string; background_bash_jobs: unknown[] };

async function liveAgents(page: Page, routerUrl: string): Promise<AgentView[]> {
  return page.evaluate((url) => new Promise<AgentView[]>((resolve, reject) => {
    const socket = new WebSocket(url.replace(/^http/, "ws") + "/v1/ui-live");
    const timer = setTimeout(() => { socket.close(); reject(new Error("Timed out waiting for live agent snapshot")); }, 8000);
    socket.onerror = () => { clearTimeout(timer); socket.close(); reject(new Error("Live agent connection failed")); };
    socket.onmessage = event => {
      const frame = JSON.parse(String(event.data));
      if (frame.type !== "managed_agents_snapshot") return;
      clearTimeout(timer);
      socket.close();
      resolve(frame.agents);
    };
  }), routerUrl);
}

// SCN-MANAGED-PROJECTION-001/002/003: real browser, proxy, router, SQLite and a local provider.
test("failed startup preserves agent projections and live updates", async ({ page }) => {
  test.setTimeout(90_000);
  const f = await workflowHarness({ omitGoogle: true });
  const healthy = "projection-healthy";
  const frames: Array<{ type?: string; agent_id?: string; changes?: AgentView; agents?: AgentView[] }> = [];
  let originalLiveSocket: { isClosed(): boolean } | undefined;
  page.on("websocket", socket => {
    if (!socket.url().endsWith("/v1/ui-live") || originalLiveSocket) return;
    originalLiveSocket = socket;
    socket.on("framereceived", frame => {
      try { frames.push(JSON.parse(String(frame.payload))); } catch { /* Ignore non-JSON frames. */ }
    });
  });
  const sawState = (id: string, state: string) => frames.some(frame =>
    (frame.type === "managed_agent_delta" && frame.agent_id === id && frame.changes?.lifecycle_state === state) ||
    (frame.type === "managed_agents_snapshot" && frame.agents?.some(agent => agent.agent_id === id && agent.lifecycle_state === state)));
  try {
    await f.open(page);
    await f.api("/v1/managed-agents", { name: healthy, cwd: join(f.router.root, "work"), runtime: "embedded", model });
    await expect.poll(() => f.rows("SELECT lifecycle_state FROM managed_agents WHERE agent_id=?", healthy)).toEqual([{ lifecycle_state: "running" }]);
    await expect(page.locator(`[data-agent-id="${healthy}"]`)).toBeVisible();
    await expect.poll(() => sawState(healthy, "running")).toBe(true);
    expect(originalLiveSocket?.isClosed()).toBe(false);

    const workflow = await f.api("/v1/workflows", { name: "Failing projection fixture" });
    const { workflow_state: entry } = await f.api("/v1/workflow-states", {
      workflow_id: workflow.id, name: "Start", entry_text: "Run the background task", allow_exit: true,
    });
    await f.api(`/v1/workflows/${workflow.id}`, { name: workflow.name, entry_state_id: entry.id }, "PUT");
    // The provider boundary fails deterministically during the real background launch.
    f.replies.push({ failure: 400 });
    const run = await f.api(`/v1/workflows/${workflow.id}/run`, { cwd: join(f.router.root, "work") });
    const failed = String(run.agent_id);
    await expect.poll(() => f.rows("SELECT lifecycle_state,last_error_code FROM managed_agents WHERE agent_id=?", failed), { timeout: 15_000 })
      .toEqual([{ lifecycle_state: "failed", last_error_code: "EMBEDDED_START_FAILED" }]);
    // The already-connected UI must receive the failure without a reconnect
    // or a test-triggered REST read to force publication.
    await expect.poll(() => sawState(failed, "failed"), { timeout: 10_000 }).toBe(true);
    expect(originalLiveSocket?.isClosed()).toBe(false);
    await page.getByRole("tab", { name: /Background/ }).click();
    await expect(page.locator(`[data-agent-id="${failed}"]`)).toBeVisible();
    await page.getByRole("tab", { name: /^Agents/ }).click();

    for (const path of ["/v1/managed-agents", "/v1/managed-agents?include_children=true"]) {
      const list = await f.api(path);
      expect(list.agents.find((agent: AgentView) => agent.agent_id === healthy)).toMatchObject({ lifecycle_state: "running" });
      expect(list.agents.find((agent: AgentView) => agent.agent_id === failed)).toMatchObject({ lifecycle_state: "failed", background_bash_jobs: [] });
    }
    expect((await f.api(`/v1/managed-agents/${healthy}`)).agent).toMatchObject({ lifecycle_state: "running" });
    expect((await f.api(`/v1/managed-agents/${failed}`)).agent).toMatchObject({ lifecycle_state: "failed", background_bash_jobs: [] });
    const agents = await liveAgents(page, f.proxy.url);
    expect(agents.find(agent => agent.agent_id === healthy)?.lifecycle_state).toBe("running");
    expect(agents.find(agent => agent.agent_id === failed)).toMatchObject({ lifecycle_state: "failed", background_bash_jobs: [] });

    await page.reload();
    await expect(page.locator(`[data-agent-id="${healthy}"]`)).toBeVisible();
    await page.getByRole("tab", { name: /Background/ }).click();
    await expect(page.locator(`[data-agent-id="${failed}"]`)).toBeVisible();
    await page.getByRole("tab", { name: /^Agents/ }).click();
    await page.locator(`[data-agent-id="${healthy}"]`).click();
    f.replies.push({ answer: "Healthy agent still works" });
    await userTurn(page, "Check the healthy agent");
    await expect(page.getByText("Healthy agent still works", { exact: true }).first()).toBeVisible();

    const stopped = "projection-stopped";
    await f.api("/v1/managed-agents", { name: stopped, cwd: join(f.router.root, "work"), runtime: "embedded", model });
    await expect.poll(() => f.rows("SELECT lifecycle_state FROM managed_agents WHERE agent_id=?", stopped)).toEqual([{ lifecycle_state: "running" }]);
    await f.api(`/v1/managed-agents/${stopped}`, undefined, "DELETE");
    await expect.poll(() => f.rows("SELECT agent_id FROM managed_agents WHERE agent_id=?", stopped)).toEqual([]);
    expect((await fetch(`${f.proxy.url}/v1/managed-agents/${stopped}`)).status).toBe(404);
    const afterStop = await f.api("/v1/managed-agents?include_children=true");
    expect(afterStop.agents.find((agent: AgentView) => agent.agent_id === healthy)?.lifecycle_state).toBe("running");
    expect(afterStop.agents.find((agent: AgentView) => agent.agent_id === failed)?.lifecycle_state).toBe("failed");
    expect((await liveAgents(page, f.proxy.url)).find(agent => agent.agent_id === healthy)?.lifecycle_state).toBe("running");
    expect(f.rows("SELECT lifecycle_state FROM managed_agents WHERE agent_id=?", failed)).toEqual([{ lifecycle_state: "failed" }]);
    expect(f.violations).toEqual([]);
  } finally {
    await f.close();
  }
});
