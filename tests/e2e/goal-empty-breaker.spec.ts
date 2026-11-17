import { randomUUID } from "node:crypto";
import { test, expect } from "@playwright/test";
import { workflowHarness, settled, userTurn, model, createRunningWorkflow, type WorkflowHarness } from "./workflow-implicit-goals-harness";

// SCN-WFIG-016: external provider only is mocked. Native goal execution,
// browser messaging, WebSocket publication, proxy, router and SQLite are real.
// The shared harness opens SQLite read-only; no evidence is seeded here.
test.describe("SCN-WFIG-016 ordinary goal empty-response breaker", () => {
  let f: WorkflowHarness;
  test.beforeEach(async () => { f = await workflowHarness(); });
  test.afterEach(async ({}, info) => {
    try {
      if (f && info.status !== info.expectedStatus) await info.attach("empty-breaker-evidence", {
        contentType: "application/json",
        body: Buffer.from(JSON.stringify({
          requests: f.requests, violations: f.violations,
          entries: f.rows("SELECT * FROM embedded_entries WHERE ? IS NOT NULL ORDER BY seq", "fixture"),
          continuations: f.rows("SELECT * FROM embedded_goal_continuations WHERE ? IS NOT NULL ORDER BY user_seq", "fixture"),
          evaluations: f.rows("SELECT * FROM embedded_goal_evaluations WHERE ? IS NOT NULL ORDER BY user_seq", "fixture"),
        }, null, 2)),
      });
    } finally { await f?.close(); }
  });

  for (const [name, answer] of [["empty", ""], ["whitespace", "   \n\t  "]] as const) {
    test(`${name}: two keyed nudges, idle across repeated wakes, new user work resumes`, async ({ page }) => {
      await f.open(page);
      const agent = `empty-breaker-${randomUUID()}`;
      await f.api("/v1/managed-agents", {
        name: agent, cwd: `${f.router.root}/work`, runtime: "embedded", model,
      });
      await expect(page.locator(`[data-agent-id="${agent}"]`)).toBeVisible();
      await page.locator(`[data-agent-id="${agent}"]`).click();
      expect((await f.api(`/v1/managed-agents/${agent}/workflow-state`)).active_workflow_id).toBeFalsy();
      f.replies.push(
        { tool: "goal", args: { action: "set", goal: "Take concrete action on the regression task" } },
        { answer }, { answer }, { answer },
      );
      await userTurn(page, "Set an ordinary goal and begin the regression task.");
      await expect.poll(() => f.requests.length).toBe(3);
      await expect.poll(() => settled(f, agent).length).toBe(2);
      await f.advance(10000);
      await expect.poll(() => f.requests.length).toBe(4);
      await expect.poll(() => settled(f, agent).map(row => row.continuation_state)).toEqual(["started", "started"]);

      const idle = () => f.rows(`SELECT a.activity_state, s.execution_json FROM managed_agents a
        JOIN embedded_sessions s ON s.agent_id=a.agent_id AND s.current=1 WHERE a.agent_id=?`, agent);
      await expect.poll(idle).toEqual([{ activity_state: "idle", execution_json: null }]);
      expect((await f.api(`/v1/managed-agents/${agent}/goal`)).goal_state).toMatchObject({ enabled: true });
      const continuations = () => f.rows("SELECT * FROM embedded_goal_continuations WHERE agent_id=? ORDER BY user_seq", agent);
      const keyed = settled(f, agent);
      expect(continuations()).toHaveLength(2);
      expect(new Set(keyed.map(row => row.turn_key)).size).toBe(2);
      expect(new Set(keyed.map(row => row.user_seq)).size).toBe(2);
      for (const row of keyed) {
        expect(row.state).toBe("NOT_DONE");
        expect(row.reason).toBe("Empty response without action");
        expect(row.started_at).not.toBeNull();
        expect(String(row.user_digest).length).toBeGreaterThan(0);
        expect(String(row.final_digest).length).toBeGreaterThan(0);
        expect(Number(row.final_seq)).toBeGreaterThan(Number(row.user_seq));
      }
      // Each continuation's actual provider input contains the keyed actionable
      // empty-response nudge, not kickoffIdleGoalContinuation's fresh prompt.
      for (const request of f.requests.slice(2)) {
        expect(JSON.stringify(request.input)).toContain("You emitted an empty response without taking action.");
      }
      const assertNoEvaluation = () => {
        expect(f.requests.every(request => typeof request.instructions !== "string" ||
          !request.instructions.toLowerCase().startsWith("goal:"))).toBe(true);
        expect(f.violations).toEqual([]);
      };
      assertNoEvaluation();
      expect(f.replies).toEqual([]);
      const before = continuations();
      for (let i = 0; i < 3; i++) {
        await f.advance(60000);
        expect(f.requests).toHaveLength(4);
        expect(continuations()).toEqual(before);
        expect(idle()).toEqual([{ activity_state: "idle", execution_json: null }]);
        assertNoEvaluation();
      }

      // A genuine browser user turn resets the paused lane. Disable the goal
      // through the native tool so the legitimate answer needs no evaluator.
      const resumed = `New user work answered after ${name} breaker`;
      f.replies.push({ tool: "goal", args: { action: "off" } }, { answer: resumed });
      await userTurn(page, "New task: turn the goal off and answer my new message normally.");
      await expect(page.getByText(resumed, { exact: true }).first()).toBeVisible();
      await expect.poll(idle).toEqual([{ activity_state: "idle", execution_json: null }]);
      expect(f.requests).toHaveLength(6);
      expect((await f.api(`/v1/managed-agents/${agent}/goal`)).goal_state.enabled).toBe(false);
      expect(f.rows("SELECT CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? AND kind='assistant'", agent)
        .some(row => String(row.body).includes(resumed))).toBe(true);
      expect(f.wsFrames.some(frame => frame.includes(agent))).toBe(true);
      expect(continuations()).toEqual(before);
      expect(f.replies).toEqual([]);
      assertNoEvaluation();
    });
  }

  test("active workflow: empty responses trip circuit breaker after two nudges and halt", async ({ page }) => {
    f.replies.push({ answer: "" }, { answer: "" }, { answer: "" });
    const { agent, workflow } = await createRunningWorkflow(f, page, 600000, false);
    expect((await f.api(`/v1/managed-agents/${agent}/workflow-state`)).active_workflow_id).toBe(workflow.id);

    await expect.poll(() => f.requests.length).toBe(2);
    await expect.poll(() => settled(f, agent).length).toBe(2);
    await f.advance(10000);
    await expect.poll(() => f.requests.length).toBe(3);
    await expect.poll(() => settled(f, agent).map(row => row.continuation_state)).toEqual(["started", "started"]);

    const idle = () => f.rows(`SELECT a.activity_state, s.execution_json FROM managed_agents a
      JOIN embedded_sessions s ON s.agent_id=a.agent_id AND s.current=1 WHERE a.agent_id=?`, agent);
    await expect.poll(idle).toEqual([{ activity_state: "idle", execution_json: null }]);
    expect((await f.api(`/v1/managed-agents/${agent}/goal`)).goal_state).toMatchObject({ enabled: true });

    const continuations = () => f.rows("SELECT * FROM embedded_goal_continuations WHERE agent_id=? ORDER BY user_seq", agent);
    const keyed = settled(f, agent);
    expect(continuations()).toHaveLength(2);
    for (const row of keyed) {
      expect(row.state).toBe("NOT_DONE");
      expect(row.reason).toBe("Empty response without action");
      expect(row.started_at).not.toBeNull();
    }
    for (const request of f.requests.slice(1)) {
      expect(JSON.stringify(request.input)).toContain("You emitted an empty response without taking action.");
    }
    expect(f.replies).toEqual([]);

    const before = continuations();
    for (let i = 0; i < 3; i++) {
      await f.advance(60000);
      expect(f.requests).toHaveLength(3);
      expect(continuations()).toEqual(before);
      expect(idle()).toEqual([{ activity_state: "idle", execution_json: null }]);
    }

    const resumed = "New user work answered after workflow breaker";
    f.replies.push({ answer: resumed });
    await userTurn(page, "Turn workflow work with text");
    await expect(page.getByText(resumed, { exact: true }).first()).toBeVisible();
    await expect.poll(idle).toEqual([{ activity_state: "idle", execution_json: null }]);
  });
});
