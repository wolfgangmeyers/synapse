import { randomUUID } from "node:crypto";
import { access, readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { test, expect, type Page } from "@playwright/test";
import { workflowHarness, model, type WorkflowHarness } from "./workflow-implicit-goals-harness";

// Supplementary near-cap cases: seed one counter only; decisions and authority remain real.
async function activate(f: WorkflowHarness, page: Page, seed: string) {
  await f.open(page);
  const agent = `accounting-${randomUUID()}`;
  await f.api("/v1/managed-agents", { name: agent, cwd: `${f.router.root}/work`, runtime: "embedded", model });
  await expect(page.locator(`[data-agent-id="${agent}"]`)).toBeVisible();
  await page.locator(`[data-agent-id="${agent}"]`).click();
  const workflow = await f.api("/v1/workflows", { name: `Accounting ${randomUUID()}` });
  f.clock = Date.parse(workflow.created_at);
  const { workflow_state: state } = await f.api("/v1/workflow-states", { workflow_id: workflow.id, name: "A", entry_text: "Work", allow_exit: true });
  await f.api(`/v1/workflows/${workflow.id}`, { name: workflow.name, entry_state_id: state.id }, "PUT");
  await writeFile(join(f.router.root, "nudge-near-cap"), seed, { mode: 0o600 });
  await f.api(`/v1/managed-agents/${agent}/workflow-state`, { action: "activate", workflow_state_id: state.id }, "PUT");
  return agent;
}

async function clockUntil(f: WorkflowHarness, count: number) {
  for (let i = 0; i < 40 && f.requests.length < count; i++) {
    const busy = f.rows("SELECT count(*) AS n FROM managed_agents WHERE activity_state != 'idle' AND desired_state = 'running'");
    if (Number(busy[0]?.n) === 0) await f.advance(10_000);
    await new Promise(resolve => setTimeout(resolve, 250));
  }
  expect(f.requests.length, `provider dispatch ${count}`).toBe(count);
}

test.describe("SCN-GWS-008 near-cap workflow accounting", () => {
  test.setTimeout(120_000);
  for (const [seed, response] of [["unanswered", false], ["attempts", false], ["unanswered", true], ["attempts", true]] as const) {
    test(`${seed}: ${response ? "checked response resets the near-cap streak" : "provider failure does not replay"}`, async ({ page }) => {
      const f = await workflowHarness({ nudgeNearCapFixture: true });
      try {
        f.replies.push({ answer: "Activation settled" }, response ? { answer: "Checked response" } : { failure: 400 });
        if (response) f.replies.push({ hold: true });
        const agent = await activate(f, page, seed);
        await expect(page.getByText("Activation settled", { exact: true }).first()).toBeVisible({ timeout: 30_000 });
        await clockUntil(f, 2);
        await expect(access(join(f.router.root, "nudge-near-cap"))).rejects.toMatchObject({ code: "ENOENT" });
        if (response) {
          await expect(page.getByText("Checked response", { exact: true }).first()).toBeVisible({ timeout: 30_000 });
          await clockUntil(f, 3);
          expect(f.wsFrames.some(frame => frame.includes("Checked response"))).toBe(true);
        } else {
          await expect.poll(() => f.rows("SELECT activity_state FROM managed_agents WHERE agent_id=?", agent)[0]?.activity_state, { timeout: 30_000 }).toBe("idle");
          for (let i = 0; i < 5; i++) {
            await f.advance(10_000);
            await f.api(`/v1/managed-agents/${agent}/goal`);
            await new Promise(resolve => setTimeout(resolve, 300));
          }
          // A failed dispatched turn may leave a durable UNKNOWN effect. It is
          // not authority to manufacture another wake or expect a cap callback.
          expect(f.requests).toHaveLength(2);
        }
        expect((await f.api(`/v1/managed-agents/${agent}/goal`)).goal_state.enabled).toBe(true);
        expect(f.violations).toEqual([]);
      } finally { await f.close(); }
    });
  }
  for (const seed of ["unanswered", "attempts"] as const) {
    test(`${seed}: capped scheduler suppresses a valid pre-send wake`, async ({ page }) => {
      const f = await workflowHarness({ nudgeNearCapFixture: true });
      try {
        f.replies.push({ answer: "Activation settled" });
        const agent = await activate(f, page, `${seed}-cap`);
        await expect(page.getByText("Activation settled", { exact: true }).first()).toBeVisible({ timeout: 30_000 });
        await expect.poll(async () => {
          await f.advance(10_000);
          return readFile(join(f.router.root, "nudge-cap-decisions"), "utf8").catch(() => "");
        }, { timeout: 30_000, intervals: [300] }).toMatch(seed === "unanswered" ? /^20 0$/m : /^0 20$/m);
        expect(f.requests).toHaveLength(1);
        expect((await f.api(`/v1/managed-agents/${agent}/goal`)).goal_state.enabled).toBe(true);
        expect(f.violations).toEqual([]);
      } finally { await f.close(); }
    });
  }

});
