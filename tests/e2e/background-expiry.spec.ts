import { test, expect } from "@playwright/test";
import { DatabaseSync } from "node:sqlite";
import { setupE2EHarness } from "./harness";

test("startup sweep terminates old background workflows through managed lifecycle", async ({ page }) => {
  const h = await setupE2EHarness({ useProxy: true, managed: true });
  try {
    const url = h.proxy!.url;
    const state = await fetch(`${url}/v1/workflow-states`, {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ name: "Expiry state", description: "test", entry_text: "test", goal: "test" }),
    });
    expect(state.status).toBe(201);
    const stateID = (await state.json()).workflow_state.id;
    const workflow = await fetch(`${url}/v1/workflows`, {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ name: "Expiry workflow", description: "test", entry_state_id: stateID, variables: [] }),
    });
    expect(workflow.status).toBe(201);
    const workflowID = (await workflow.json()).id;
    const launch = async () => {
      const response = await fetch(`${url}/v1/workflows/${workflowID}/run`, {
        method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ variables: {} }),
      });
      expect(response.status).toBe(201);
      return (await response.json()).agent_id as string;
    };
    const old = await launch();
    await h.advanceClock(2000);
    const young = await launch();
    await page.goto(h.frontend.url);
    await h.router.restart(async (path) => {
      const db = new DatabaseSync(path);
      try {
        db.prepare("UPDATE managed_agents SET created_at = ? WHERE agent_id = ?").run("2000-01-01T00:00:00Z", old);
      } finally { db.close(); }
    });
    await expect.poll(async () => {
      const db = new DatabaseSync(h.router.databasePath);
      try { return db.prepare("SELECT agent_id FROM managed_agents WHERE agent_id = ?").get(old); }
      finally { db.close(); }
    }, { timeout: 20000 }).toBeUndefined();
    const db = new DatabaseSync(h.router.databasePath);
    try {
      expect(db.prepare("SELECT agent_id FROM managed_agents WHERE agent_id = ?").get(young)).toBeDefined();
      expect(db.prepare("SELECT kind FROM managed_lifecycle_actions WHERE agent_id = ? AND kind = 'terminate'").get(old)).toBeDefined();
    } finally { db.close(); }
    const response = await fetch(`${url}/v1/workflows/${workflowID}`);
    expect(response.ok).toBeTruthy();
  } finally { await h.close(); }
});
