import { test, expect } from "@playwright/test";
import { startEmbeddedSafetyFixture } from "./harness";
import { join } from "node:path";

test("SCN-GWS-002/003: failed automatic turns are paced and exhaust twenty attempts", async ({ page }) => {
  // Mode: real-capable. Only external Codex HTTP is deterministic/local.
  test.setTimeout(120_000);
  const f = await startEmbeddedSafetyFixture();
  try {
    await page.addInitScript(url => {
      localStorage.setItem("mecha.web.activeRouter", url);
      localStorage.setItem("mecha.web.routers", JSON.stringify([url]));
    }, f.proxy.url);
    const frames: string[] = [];
    page.on("websocket", ws => ws.on("framereceived", ({ payload }) => {
      if (frames.length < 256) frames.push(payload.toString());
    }));
    await page.goto(f.frontend.url);
    expect((await f.api("/v1/managed-agents", { name: "goal-safety", cwd: join(f.router.root, "work"), runtime: "embedded", model: { provider: "openai-codex", id: "gpt-6.1-terra" }, initial_instruction: "Start" })).status).toBe(202);
    await page.locator('[data-agent-id="goal-safety"]').click();
    await expect(page.getByText("startup settled", { exact: true })).toBeVisible();
    f.setReply({ failure: true });
    expect((await f.api("/v1/managed-agents/goal-safety/goal", { action: "set", goal: "Continue useful work without claiming completion" }, "PUT")).status).toBe(200);
    await expect.poll(() => f.requests).toBeGreaterThanOrEqual(2);
    await expect(page.getByText("Turn failed: Codex Responses HTTP request failed; turn outcome unknown", { exact: true }).first()).toBeVisible();
    // Durable admission entries catch hidden pre-provider attempts as well.
    const turns = () => Number(f.db.prepare("SELECT count(*) AS n FROM embedded_entries WHERE agent_id='goal-safety' AND kind='user'").get()!.n);
    const settled = async () => {
      await expect.poll(() => f.db.prepare("SELECT activity_state FROM managed_agents WHERE agent_id='goal-safety'").get()!.activity_state).toBe("idle");
      // Idle publication precedes scheduling settlement. Drain observation
      // transports at fixed test time before advancing the policy clock.
      await new Promise(resolve => setTimeout(resolve, 100));
    };
    await settled();
    const firstTurns = turns();
    expect(f.requests, "one startup and one first automatic attempt").toBe(2);
    await f.router.advanceClock(9999);
    // API and browser observation barriers, without sleeping through policy delay.
    await f.api("/v1/managed-agents/goal-safety/goal");
    expect(f.requests, "no automatic retry before T+10000ms").toBe(2);
    expect(turns(), "no hidden pre-model retry before deadline").toBe(firstTurns);
    await f.router.advanceClock(1);
    await expect.poll(() => f.requests, { message: "test clock must drive goal retry at T+10000ms" }).toBe(3);
    for (let automatic = 3; automatic <= 20; automatic++) {
      await settled();
      await f.router.advanceClock(10_000);
      await expect.poll(() => f.requests).toBe(automatic + 1);
    }
    await settled();
    await f.router.advanceClock(30_000);
    expect(f.requests, "no twenty-first automatic provider call").toBe(21);
    expect(turns()).toBe(firstTurns + 19);
    const goal = await f.api("/v1/managed-agents/goal-safety/goal");
    expect(goal.body.goal_state.enabled).toBe(true);
    expect(frames.some(frame => frame.includes("Turn failed:"))).toBe(true);
  } finally { await f.close(); }
});
