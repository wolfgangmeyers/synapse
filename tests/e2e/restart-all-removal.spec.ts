import { randomUUID } from "node:crypto";
import { test, expect, type Page } from "@playwright/test";
import { workflowHarness, model, type WorkflowHarness } from "./workflow-implicit-goals-harness";

// SPEC-MANAGED-RESTART-ALL-REMOVAL-001 (task #60). Real headless browser, Go
// router-proxy + router, REST, WebSocket and SQLite. Busy-turn scenarios
// use a deterministic local provider response held behind a barrier.
const RETIRED = "/v1/managed-agents/restart-all";
const LIFECYCLE_TABLES = ["managed_lifecycle_actions", "managed_lifecycle_batch_items", "managed_http_operations"];

test.describe("Restart All removal (SPEC-MANAGED-RESTART-ALL-REMOVAL-001)", () => {
  test.setTimeout(90_000);
  let f: WorkflowHarness;
  let a: string;
  let b: string;

  test.beforeEach(async () => {
    f = await workflowHarness({ restartCodexConsumerFixture: true });
    a = `rar-a-${randomUUID()}`;
    b = `rar-b-${randomUUID()}`;
  });
  test.afterEach(async () => { f?.releaseHeld(); await f?.close(); });

  async function ready(name: string) {
    await expect.poll(() => f.rows("SELECT lifecycle_state, activity_state, accepting_prompts FROM managed_agents WHERE agent_id=?", name)[0], { timeout: 20_000 }).toEqual({ lifecycle_state: "running", activity_state: "idle", accepting_prompts: 1 });
  }

  async function launchTwo(page: Page) {
    await f.open(page);
    await expect.poll(() => f.wsFrames.some(raw => JSON.parse(raw).type === "registered"), { timeout: 20_000 }).toBe(true);
    for (const name of [a, b]) {
      await f.api("/v1/managed-agents", { name, cwd: `${f.router.root}/work`, runtime: "embedded", model });
      await expect(page.locator(`[data-agent-id="${name}"]`)).toBeVisible();
      await ready(name);
    }
    await expect.poll(() => f.rows("SELECT agent_id FROM managed_agents ORDER BY agent_id").length).toBeGreaterThanOrEqual(2);
  }

  // Row counts for every lifecycle/operation table that exists. Tables may be
  // dropped by the removal; a missing table is recorded as null, not an error.
  function durableCounts() {
    const present = new Set(f.rows("SELECT name FROM sqlite_master WHERE type='table'").map(r => String(r.name)));
    return Object.fromEntries(LIFECYCLE_TABLES.map(t => [t, present.has(t) ? Number(f.rows(`SELECT count(*) AS n FROM ${t}`)[0].n) : null]));
  }
  const agentRows = () => f.rows("SELECT * FROM managed_agents ORDER BY agent_id");
  const lifecycleRows = () => f.rows("SELECT action_id, agent_id, kind, source, state FROM managed_lifecycle_actions ORDER BY action_id");

  async function raw(path: string, method: string, body?: string, headers: Record<string, string> = {}) {
    const res = await fetch(f.proxy.url + path, { method, headers, ...(body === undefined ? {} : { body }) });
    return { status: res.status, text: await res.text() };
  }

  test("RAR-001 no Restart All control in sidebar, header, agent or server menus; per-agent Restart remains", async ({ page }) => {
    await launchTwo(page);
    const anyRestartAll = page.getByText(/restart[\s_-]*all/i);
    const byTestId = page.locator('[data-testid*="restart-all" i], [id*="restart-all" i], [class*="restart-all" i], [aria-label*="restart all" i], [title*="restart all" i]');
    const assertNone = async (where: string) => {
      expect(await anyRestartAll.count(), `${where}: text`).toBe(0);
      expect(await byTestId.count(), `${where}: attributes`).toBe(0);
    };
    await assertNone("sidebar");

    // Server controls menu keeps "Restart router" but offers no bulk agent restart.
    await page.getByRole("button", { name: "Managed actions", exact: true }).click();
    const server = page.locator(".managed-actions-menu");
    await expect(server).toBeVisible();
    await expect(server.getByText("Restart router", { exact: false })).toBeVisible();
    await assertNone("server menu");
    expect(await server.getByRole("menuitem").allTextContents()).not.toContain(expect.stringMatching(/all/i) as never);
    await page.keyboard.press("Escape");
    await expect(server).not.toBeVisible();

    // Select an agent: header control and the overflow menu.
    await page.locator(`[data-agent-id="${a}"]`).click();
    await expect(page.getByRole("button", { name: "More agent controls", exact: true })).toBeVisible();
    await assertNone("header");
    await page.getByRole("button", { name: "More agent controls", exact: true }).click();
    const menu = page.getByRole("menu", { name: "Agent controls menu" });
    await expect(menu).toBeVisible();
    await expect(menu.getByRole("button", { name: "Restart", exact: true })).toBeVisible();
    await assertNone("agent controls menu");
    await page.keyboard.press("Escape");

    // Keyboard reachability: tabbing through the page never lands on a Restart All control.
    for (let i = 0; i < 40; i++) {
      await page.keyboard.press("Tab");
      const label = await page.evaluate(() => {
        const el = document.activeElement as HTMLElement | null;
        return `${el?.getAttribute("aria-label") ?? ""} ${el?.textContent ?? ""} ${el?.getAttribute("title") ?? ""}`;
      });
      expect(label).not.toMatch(/restart\s*all/i);
    }
    expect(f.violations).toEqual([]);
  });

  test("RAR-001 the shipped UI bundle never calls the retired route", async ({ page }) => {
    const hits: string[] = [];
    page.on("request", req => { if (req.url().includes("restart-all")) hits.push(`${req.method()} ${req.url()}`); });
    await launchTwo(page);
    await page.locator(`[data-agent-id="${a}"]`).click();
    await page.getByRole("button", { name: "More agent controls", exact: true }).click();
    await page.getByRole("menu", { name: "Agent controls menu" }).getByRole("button", { name: "Restart", exact: true }).click();
    await expect.poll(() => f.rows("SELECT state FROM managed_http_operations WHERE canonical_path=? AND method='POST'", `/v1/managed-agents/${a}/restart`).some(r => r.state === "terminal")).toBe(true);
    await ready(a);
    expect(hits).toEqual([]);
    const served = await (await fetch(new URL("/", f.frontend.url))).text();
    const assets = [...served.matchAll(/(?:src|href)="([^"]+\.js)"/g)].map(m => m[1]);
    expect(assets.length).toBeGreaterThan(0);
    for (const asset of assets) {
      const js = await (await fetch(new URL(asset, f.frontend.url))).text();
      expect(js, asset).not.toMatch(/managed-agents\/restart-all/);
    }
  });

  test("RAR-002 POST to the retired route is 404 and leaves agents and lifecycle state unchanged", async ({ page }) => {
    await launchTwo(page);
    const json = { "Content-Type": "application/json" };
    const variants: { name: string; body?: string; headers: Record<string, string> }[] = [
      { name: "empty body, no headers", headers: {} },
      { name: "empty object", body: "{}", headers: { ...json, "X-Request-ID": randomUUID() } },
      { name: "arbitrary body", body: JSON.stringify({ agents: [a, b], model }), headers: { ...json, "X-Request-ID": randomUUID() } },
      { name: "Idempotency-Key", body: "{}", headers: { ...json, "Idempotency-Key": randomUUID(), "X-Request-ID": randomUUID() } },
      { name: "malformed JSON", body: "{not json", headers: { ...json, "X-Request-ID": randomUUID() } },
      { name: "invalid request id", body: "{}", headers: { ...json, "X-Request-ID": "not-a-uuid" } },
    ];
    const agentsBefore = agentRows();
    const countsBefore = durableCounts();
    const lifecycleBefore = lifecycleRows();
    const apiBefore = await f.api("/v1/managed-agents");
    expect(agentsBefore.length).toBeGreaterThanOrEqual(2);

    for (const v of variants) {
      const res = await raw(RETIRED, "POST", v.body, v.headers);
      expect(res.status, `${v.name}: ${res.text}`).toBe(404);
      // Not a managed-agent error envelope for a specific agent and not an accepted operation.
      expect(res.text, v.name).not.toMatch(/operation_id|accepted|batch/i);
    }
    // The same replayed request id must not become a stored operation either.
    const replayId = randomUUID();
    for (let i = 0; i < 2; i++) {
      expect((await raw(RETIRED, "POST", "{}", { ...json, "X-Request-ID": replayId })).status).toBe(404);
    }

    expect(agentRows()).toEqual(agentsBefore);
    expect(lifecycleRows()).toEqual(lifecycleBefore);
    expect(durableCounts()).toEqual(countsBefore);
    expect(await f.api("/v1/managed-agents")).toEqual(apiBefore);
    expect(f.violations).toEqual([]);
    expect(f.requests).toEqual([]);
  });

  test("RAR-002 restart-all remains a valid individual agent ID", async ({ page }) => {
    await launchTwo(page);
    const name = "restart-all";
    await f.api("/v1/managed-agents", { name, cwd: `${f.router.root}/work`, runtime: "embedded", model });
    await expect(page.locator(`[data-agent-id="${name}"]`)).toBeVisible();
    await ready(name);
    const before = durableCounts();
    const headers = { "Content-Type": "application/json", "X-Request-ID": randomUUID() };
    expect((await raw(RETIRED, "POST", "{}", headers)).status).toBe(404);
    expect(durableCounts()).toEqual(before);
    const agent = await raw(RETIRED, "GET");
    expect(agent.status).toBe(200);
    const requestId = randomUUID();
    const restart = await raw(`${RETIRED}/restart`, "POST", "{}", { ...headers, "X-Request-ID": requestId });
    expect([200, 202], restart.text).toContain(restart.status);
    expect(f.rows("SELECT state, response_status FROM managed_http_operations WHERE request_id=?", requestId)).toEqual([{ state: "terminal", response_status: restart.status }]);
    expect(lifecycleRows().filter(r => r.agent_id === name)).toEqual([]);
    await ready(name);
    expect(f.violations).toEqual([]);
  });

  test("RAR-002 retired route stays 404 while an agent is mid-turn and no agent is interrupted", async ({ page }) => {
    await launchTwo(page);
    let release!: () => void;
    const barrier = new Promise<void>(resolve => { release = resolve; });
    f.replies.push({ answer: "Held turn completed", barrier });
    const prompt = f.api(`/v1/managed-agents/${a}/prompt`, { message: "Hold this provider turn" });
    void prompt.catch(() => {}); // Preserve the rejection for the awaited assertion below.
    try {
      await expect.poll(() => f.requests.length, { timeout: 30_000 }).toBe(1);
      const before = f.rows("SELECT agent_id, activity_state, lifecycle_state, desired_state, session_epoch FROM managed_agents ORDER BY agent_id");
      const countsBefore = durableCounts();
      expect((await raw(RETIRED, "POST", "{}", { "Content-Type": "application/json", "X-Request-ID": randomUUID() })).status).toBe(404);
      // Busy agent remains busy on the same provider turn: nothing was aborted or restarted.
      expect(f.rows("SELECT agent_id, activity_state, lifecycle_state, desired_state, session_epoch FROM managed_agents ORDER BY agent_id")).toEqual(before);
      expect(durableCounts()).toEqual(countsBefore);
      expect(f.requests).toHaveLength(1);
    } finally { release(); }
    await prompt;
  });

  test("RAR-003 individual restart is accepted, recorded, reflected in UI, and leaves other agents untouched", async ({ page }) => {
    await launchTwo(page);
    const otherBefore = f.rows("SELECT * FROM managed_agents WHERE agent_id=?", b);
    const actionsBefore = lifecycleRows();
    const retiredHits: string[] = [];
    page.on("request", req => { if (req.url().includes("restart-all")) retiredHits.push(req.url()); });

    await page.locator(`[data-agent-id="${a}"]`).click();
    const restartResponse = page.waitForResponse(res => res.request().method() === "POST" && res.url().endsWith(`/v1/managed-agents/${a}/restart`));
    await page.getByRole("button", { name: "More agent controls", exact: true }).click();
    await page.getByRole("menu", { name: "Agent controls menu" }).getByRole("button", { name: "Restart", exact: true }).click();
    const response = await restartResponse;
    expect([200, 202]).toContain(response.status());

    // Embedded restart stores a terminal HTTP outcome, not a lifecycle action.
    const path = `/v1/managed-agents/${a}/restart`;
    await expect.poll(() => f.rows("SELECT state, response_status FROM managed_http_operations WHERE method='POST' AND canonical_path=?", path)).toEqual([{ state: "terminal", response_status: response.status() }]);
    expect(lifecycleRows()).toEqual(actionsBefore);
    await ready(a);
    await expect(page.locator(`[data-agent-id="${a}"]`)).toBeVisible();
    await expect(page.locator(`[data-agent-id="${b}"]`)).toBeVisible();
    expect(f.rows("SELECT * FROM managed_agents WHERE agent_id=?", b)).toEqual(otherBefore);
    expect(retiredHits).toEqual([]);
    expect(f.violations).toEqual([]);
  });

  test("RAR-003 direct REST restart is idempotent per request id and rejects unknown agents; retired route still 404 afterwards", async ({ page }) => {
    await launchTwo(page);
    const id = randomUUID();
    const first = await raw(`/v1/managed-agents/${a}/restart`, "POST", "{}", { "Content-Type": "application/json", "X-Request-ID": id });
    expect([200, 202]).toContain(first.status);
    await expect.poll(() => f.rows("SELECT state, response_status FROM managed_http_operations WHERE request_id=?", id)).toEqual([{ state: "terminal", response_status: first.status }]);
    await ready(a);
    // Replaying the same request id returns the stored outcome and creates no second operation.
    const replay = await raw(`/v1/managed-agents/${a}/restart`, "POST", "{}", { "Content-Type": "application/json", "X-Request-ID": id });
    expect(replay.status).toBe(first.status);
    expect(replay.text).toBe(first.text);
    expect(f.rows("SELECT operation_id FROM managed_http_operations WHERE request_id=?", id)).toHaveLength(1);
    expect(lifecycleRows().filter(r => r.kind === "restart" && r.agent_id === a)).toHaveLength(0);

    const missing = await raw(`/v1/managed-agents/does-not-exist-${randomUUID()}/restart`, "POST", "{}", { "Content-Type": "application/json", "X-Request-ID": randomUUID() });
    expect(missing.status).toBe(404);

    // Removal and direct restart coexist: after a restart the retired route is still 404 and adds nothing.
    const countsBefore = durableCounts();
    expect((await raw(RETIRED, "POST", "{}", { "Content-Type": "application/json", "X-Request-ID": randomUUID() })).status).toBe(404);
    expect(durableCounts()).toEqual(countsBefore);
    expect(lifecycleRows().filter(r => r.kind === "restart_all")).toEqual([]);
  });

  test("RAR-003 restart of one agent while another is busy does not disturb the busy agent", async ({ page }) => {
    await launchTwo(page);
    let release!: () => void;
    const barrier = new Promise<void>(resolve => { release = resolve; });
    f.replies.push({ answer: "Busy turn completed", barrier });
    const prompt = f.api(`/v1/managed-agents/${b}/prompt`, { message: "Keep B busy" });
    void prompt.catch(() => {});
    try {
      await expect.poll(() => f.requests.length, { timeout: 30_000 }).toBe(1);
      const busyBefore = f.rows("SELECT activity_state, session_epoch, desired_state FROM managed_agents WHERE agent_id=?", b);
      const id = randomUUID();
      const res = await raw(`/v1/managed-agents/${a}/restart`, "POST", "{}", { "Content-Type": "application/json", "X-Request-ID": id });
      expect([200, 202]).toContain(res.status);
      await expect.poll(() => f.rows("SELECT state, response_status FROM managed_http_operations WHERE request_id=?", id)).toEqual([{ state: "terminal", response_status: res.status }]);
      await ready(a);
      expect(lifecycleRows().filter(r => r.agent_id === a)).toEqual([]);
      expect(f.rows("SELECT activity_state, session_epoch, desired_state FROM managed_agents WHERE agent_id=?", b)).toEqual(busyBefore);
      expect(lifecycleRows().filter(r => r.agent_id === b)).toEqual([]);
      expect(f.requests).toHaveLength(1);
    } finally { release(); }
    await prompt;
  });
});
