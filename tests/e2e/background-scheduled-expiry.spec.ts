import { test, expect } from "@playwright/test";
import { DatabaseSync } from "node:sqlite";
import { setupE2EHarness } from "./harness";

// SCN-BWC-001/004: exercise a real scheduled launch, expiry, and the next launch.
test("expired scheduled worker is terminated without removing its recurring schedule", async ({ page }) => {
  const h = await setupE2EHarness({ useProxy: true, managed: true, backgroundExpiryInterval: "250ms" });
  try {
    const url = h.proxy!.url;
    const create = async (path: string, body: object) => {
      const res = await fetch(`${url}${path}`, {
        method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body),
      });
      const data = await res.json();
      expect(res.status, JSON.stringify(data)).toBe(201);
      return data;
    };
    const state = await create("/v1/workflow-states", { name: "Scheduled expiry state", description: "test", entry_text: "test", goal: "test" });
    const workflow = await create("/v1/workflows", { name: "Scheduled expiry workflow", description: "test", entry_state_id: state.workflow_state.id, variables: [] });
    const created = await create("/v1/workflow-schedules", {
      workflow_id: workflow.id, name: "Recurring expiry test", variables: {},
      schedule: { days_mode: "every_day", time_mode: "repeating_interval", interval: { value: 1, unit: "hours" } },
    });
    const id = created.schedule.id;
    const schedule = async () => {
      const res = await fetch(`${url}/v1/workflow-schedules/${id}`);
      expect(res.status).toBe(200);
      return (await res.json()).schedule;
    };
    const workers = async () => {
      const res = await fetch(`${url}/v1/managed-agents`);
      expect(res.status).toBe(200);
      return (await res.json()).agents.filter((a: { is_background: boolean; active_workflow_id: string }) => a.is_background && a.active_workflow_id === workflow.id);
    };
    const operationCount = (agent: string) => {
      const db = new DatabaseSync(h.router.databasePath, { readOnly: true });
      try {
        return Number(db.prepare("SELECT count(*) AS n FROM managed_http_operations WHERE method='DELETE' AND canonical_path=?").get(`/v1/managed-agents/${agent}`)?.n);
      } finally { db.close(); }
    };

    await h.advanceClock(60 * 60 * 1000 + 1000);
    await expect.poll(async () => (await workers()).length).toBe(1);
    const old = (await workers())[0].agent_id;
    const firstRun = await schedule();
    expect(firstRun.last_run_status).toBe("success");
    await page.addInitScript(router => {
      localStorage.setItem("mecha.web.activeRouter", router);
      localStorage.setItem("mecha.web.routers", JSON.stringify([router]));
    }, url);
    await page.goto(h.frontend.url);
    await page.locator('button[role="tab"]:has-text("Background")').click();
    await expect(page.locator(`[data-agent-id="${old}"]`)).toBeVisible();

    // The test-only subsecond ticker runs the real expiry sweep.
    await h.advanceClock(24 * 60 * 60 * 1000 + 1000);
    await expect.poll(() => operationCount(old), { timeout: 10_000 }).toBeGreaterThan(0);
    await expect.poll(async () => {
      const res = await fetch(`${url}/v1/managed-agents/${old}`);
      if (res.status === 404) return true;
      expect(res.status).toBe(200);
      return (await res.json()).agent.desired_state === "terminated";
    }).toBe(true);
    const afterExpiry = await schedule();
    expect(afterExpiry.enabled).toBe(true);
    expect(afterExpiry.last_run_status).toBe("success");
    const before = new Set((await workers()).map((a: { agent_id: string }) => a.agent_id));
    await h.advanceClock(60 * 60 * 1000 + 1000);
    await expect.poll(async () => (await workers()).some((a: { agent_id: string }) => !before.has(a.agent_id))).toBe(true);
    const afterNext = await schedule();
    expect(afterNext.last_run_status).toBe("success");
    expect(Date.parse(afterNext.last_run_at)).toBeGreaterThan(Date.parse(afterExpiry.last_run_at));
    expect((await fetch(`${url}/v1/workflows/${workflow.id}`)).status).toBe(200);
  } finally { await h.close(); }
});
