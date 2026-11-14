import { randomUUID } from "node:crypto";
import { test, expect, type Page } from "@playwright/test";
import { workflowHarness, model, type WorkflowHarness } from "./workflow-implicit-goals-harness";

// Individual restart through browser, proxy, router and SQLite with a local model fixture.

test.describe("Individual managed restart", () => {
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

  const lifecycleRows = () => f.rows("SELECT action_id, agent_id, kind, source, state FROM managed_lifecycle_actions ORDER BY action_id");

  async function raw(path: string, method: string, body?: string, headers: Record<string, string> = {}) {
    const res = await fetch(f.proxy.url + path, { method, headers, ...(body === undefined ? {} : { body }) });
    return { status: res.status, text: await res.text() };
  }

  test("RAR-003 individual restart is accepted, recorded, reflected in UI, and leaves other agents untouched", async ({ page }) => {
    await launchTwo(page);
    const otherBefore = f.rows("SELECT * FROM managed_agents WHERE agent_id=?", b);
    const actionsBefore = lifecycleRows();

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
    expect(f.violations).toEqual([]);
  });

  test("RAR-003 direct REST restart is idempotent per request id and rejects unknown agents", async ({ page }) => {
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
