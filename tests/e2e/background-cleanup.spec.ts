import { test, expect, type Page } from "@playwright/test";
import { DatabaseSync } from "node:sqlite";
import { WebSocket } from "ws";
import { setupE2EHarness, type E2ETestHarness } from "./harness";

const HOUR = 60 * 60 * 1000;

type Fixture = { id: string; age: number; background: boolean };

// Existing rows exercise upgrade/startup as well as the periodic sweep. The
// recent updated_at deliberately differs from created_at: activity is irrelevant.
function seed(h: E2ETestHarness, fixtures: Fixture[]) {
  const db = new DatabaseSync(h.router.databasePath);
  try {
    db.exec("PRAGMA busy_timeout = 5000");
    const identity = db.prepare("INSERT INTO agents (agent_id, first_seen_at) VALUES (?, ?)");
    const insert = db.prepare(`INSERT INTO managed_agents
      (agent_id, name, cwd, selected_model_provider, selected_model_id,
       desired_state, lifecycle_state, activity_state, runtime, session_epoch,
       manifest_revision, manifest_hash, created_at, updated_at, is_background)
      VALUES (?, ?, '/tmp', 'google', 'gemini-3.8-flash', 'running', 'running', 'stopped',
              'embedded', 1, 1, 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', ?, ?, ?)`);
    for (const f of fixtures) {
      const created = new Date(Date.now() - f.age).toISOString();
      identity.run(f.id, created);
      insert.run(f.id, f.id, created, new Date().toISOString(), Number(f.background));
    }
    db.prepare("UPDATE managed_settings SET projection_version = projection_version + 1 WHERE singleton_id = 1").run();
  } finally { db.close(); }
}

function operations(h: E2ETestHarness, id: string): number {
  const db = new DatabaseSync(h.router.databasePath, { readOnly: true });
  try {
    const row = db.prepare("SELECT count(*) AS n FROM managed_http_operations WHERE method='DELETE' AND canonical_path=?")
      .get(`/v1/managed-agents/${id}`);
    return Number(row?.n);
  } finally { db.close(); }
}

async function records(h: E2ETestHarness): Promise<Array<Record<string, unknown>>> {
  const response = await fetch(`${h.proxy!.url}/v1/managed-agents`);
  expect(response.status).toBe(200);
  return (await response.json()).agents;
}

async function openBackground(page: Page, h: E2ETestHarness) {
  await page.addInitScript(router => {
    localStorage.setItem("mecha.web.activeRouter", router);
    localStorage.setItem("mecha.web.routers", JSON.stringify([router]));
  }, h.proxy!.url);
  await page.goto(h.frontend.url);
  await page.locator('button[role="tab"]:has-text("Background")').click();
}

test.describe("background worker age sweep", () => {
  let h: E2ETestHarness;
  test.beforeEach(async () => { h = await setupE2EHarness({ useProxy: true, managed: true, backgroundExpiryInterval: "250ms" }); });
  test.afterEach(async () => { await h?.close(); });

  test("periodic sweep terminates old background workers, not young or ordinary workers", async ({ page }) => {
    const old = "cleanup-old-background", young = "cleanup-young-background", ordinary = "cleanup-old-ordinary";
    const startup = "cleanup-startup-barrier";
    await h.router.restart(async () => seed(h, [{ id: startup, age: 25 * HOUR, background: true }]));
    // Wait for startup's asynchronous pass to reach its last (only) candidate.
    // The periodic candidates do not exist until that pass has run.
    await expect.poll(() => operations(h, startup), { timeout: 10000 }).toBeGreaterThan(0);
    seed(h, [
      { id: old, age: 23 * HOUR, background: true },
      { id: young, age: HOUR, background: true },
      { id: ordinary, age: 25 * HOUR, background: false },
    ]);
    await openBackground(page, h);
    await expect(page.locator(`[data-agent-id="${old}"]`)).toBeVisible();
    await expect(page.locator(`[data-agent-id="${young}"]`)).toBeVisible();
    const ws = new WebSocket(`${h.proxy!.url.replace(/^http/, "ws")}/v1/ui-live`);
    const frames: string[] = [];
    ws.on("message", data => frames.push(data.toString()));
    try {
      await new Promise<void>((resolve, reject) => { ws.once("open", resolve); ws.once("error", reject); });
      await expect.poll(() => frames.some(frame => frame.includes("managed_agents_snapshot"))).toBe(true);
      frames.length = 0;
      await h.advanceClock(2 * HOUR);
      // A durable DELETE operation proves the actual lifecycle path was invoked,
      // rather than a test-side SQL deletion or projection-only change.
      await expect.poll(() => operations(h, old), { timeout: 10_000 }).toBeGreaterThan(0);
      await expect.poll(async () => {
        const agent = (await records(h)).find(a => a.agent_id === old);
        return !agent || agent.desired_state === "terminated";
      }).toBe(true);
      expect(operations(h, young)).toBe(0);
      expect(operations(h, ordinary)).toBe(0);
      expect((await records(h)).find(a => a.agent_id === young)?.desired_state).toBe("running");
      expect((await records(h)).find(a => a.agent_id === ordinary)?.desired_state).toBe("running");
      await expect.poll(() => frames.some(frame => {
        const event = JSON.parse(frame);
        return event.type === "managed_agent_removed" && event.agent_id === old ||
          event.type === "managed_agents_snapshot" && Array.isArray(event.agents) &&
          (!event.agents.some((agent: { agent_id: string }) => agent.agent_id === old) ||
           event.agents.some((agent: { agent_id: string; desired_state: string }) =>
             agent.agent_id === old && agent.desired_state === "terminated"));
      })).toBe(true);
      await h.advanceClock(24 * HOUR);
      await expect.poll(() => operations(h, young), { timeout: 10_000 }).toBeGreaterThan(0);
      expect(operations(h, ordinary)).toBe(0);
    } finally { ws.close(); }
  });

  test("startup sweep handles pre-existing old workers and preserves young and ordinary workers", async () => {
    const old = "cleanup-startup-old", young = "cleanup-startup-young", ordinary = "cleanup-startup-ordinary";
    // Restart callback runs while the router is stopped: no periodic sweep can
    // account for the operation observed after restart.
    await h.router.restart(async () => seed(h, [
      { id: old, age: 25 * HOUR, background: true },
      { id: young, age: HOUR, background: true },
      { id: ordinary, age: 25 * HOUR, background: false },
    ]));
    await expect.poll(() => operations(h, old), { timeout: 10000 }).toBeGreaterThan(0);
    expect(operations(h, young)).toBe(0);
    expect(operations(h, ordinary)).toBe(0);
  });
});
