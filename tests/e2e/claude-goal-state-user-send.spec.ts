import { test, expect, type Page } from "@playwright/test";
import { claudeWorkersHarness, type ClaudeWorkersHarness } from "./claude-workers-harness";
import { settled } from "./workflow-implicit-goals-harness";

// Real-boundary acceptance: browser -> router HTTP/WS -> embedded Claude runtime
// -> disposable fake CLI (tests/fixtures/fake-claude). Goal evaluation uses the
// existing loopback evaluator fixture; no production configuration or real
// Claude CLI is involved. Each test checks a user send after a goal state
// transition and asserts the Claude response is persisted and survives reload.
const text = (value: string) => ({ text: value });

async function reopenAgent(page: Page, agent: string) {
  await page.reload();
  await page.locator(`[data-agent-id="${agent}"]`).click();
}

test.describe("Claude user send after goal state transitions", () => {
  test.setTimeout(90_000);
  let f: ClaudeWorkersHarness | undefined;
  let agent = "";
  test.afterEach(async ({}, info) => {
    try {
      if (f && info.status !== info.expectedStatus) await info.attach("claude-goal-state-boundaries", {
        contentType: "application/json", body: Buffer.from(JSON.stringify({ records: await f.records(),
          requests: f.requests, violations: f.violations,
          entries: agent ? f.rows("SELECT kind,epoch,seq,CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? ORDER BY epoch,seq", agent) : [],
        }, null, 2)),
      });
    } finally { await f?.close(); f = undefined; agent = ""; }
  });

  test("applied goal turned off: next user send persists Claude response across reload", async ({ page }) => {
    f = await claudeWorkersHarness({
      // Goal-phase FIFO turn runs in the first process. Post-off input is keyed
      // by marker because turning the goal off restarts the CLI process, which
      // restarts FIFO `turns` from the beginning.
      turns: [[text("Goal turn answer")]],
      turns_by_input: [{ contains: "Post-off marker", steps: [text("Post-off answer")] }],
    });
    await f.open(page); agent = await f.launch(page);
    f.replies.push({ answer: "VERDICT: NOT_DONE\nREASON: More work", evaluation: true });
    await f.api(`/v1/managed-agents/${agent}/goal`, { action: "set", goal: "Goal under test" }, "PUT");
    await f.final(page, agent, "Goal turn answer");
    await expect.poll(() => settled(f!, agent).map(row => row.state)).toEqual(["NOT_DONE"]);
    expect(f.tableRows("embedded_goal_applied_receipts", agent).length).toBeGreaterThan(0);

    // Turn the applied goal off while its continuation is still pending.
    await f.api(`/v1/managed-agents/${agent}/goal`, { action: "off" }, "PUT");
    expect((await f.api(`/v1/managed-agents/${agent}/goal`)).goal_state.enabled).toBe(false);
    expect(f.tableRows("embedded_goal_continuations", agent).map(row => row.state)).toEqual(["invalidated"]);

    await f.userTurn(page, "Post-off marker");
    await f.final(page, agent, "Post-off answer");
    await reopenAgent(page, agent);
    await expect(page.getByText("Post-off answer", { exact: true }).first()).toBeVisible();

    // The post-off turn must not spawn a new evaluation or continuation.
    expect(f.tableRows("embedded_goal_evaluations", agent)).toHaveLength(1);
    expect(f.requests).toHaveLength(1);
    expect(f.violations).toEqual([]);
  });

  test("completed goal: next user send persists Claude response across reload", async ({ page }) => {
    f = await claudeWorkersHarness({
      turns: [[text("Completing goal answer")]],
      turns_by_input: [{ contains: "After completion marker", steps: [text("After completion answer")] }],
    });
    await f.open(page); agent = await f.launch(page);
    f.replies.push({ answer: "VERDICT: COMPLETED\nREASON: Finished", evaluation: true });
    await f.api(`/v1/managed-agents/${agent}/goal`, { action: "set", goal: "Complete under test" }, "PUT");
    await f.final(page, agent, "Completing goal answer");
    await expect.poll(() => settled(f!, agent).map(row => row.state)).toEqual(["DONE"]);
    expect((await f.api(`/v1/managed-agents/${agent}/goal`)).goal_state.enabled).toBe(false);

    await f.userTurn(page, "After completion marker");
    await f.final(page, agent, "After completion answer");
    await reopenAgent(page, agent);
    await expect(page.getByText("After completion answer", { exact: true }).first()).toBeVisible();

    // A completed goal must not evaluate or continue the post-completion turn.
    expect(f.tableRows("embedded_goal_evaluations", agent)).toHaveLength(1);
    expect(f.requests).toHaveLength(1);
    expect(f.violations).toEqual([]);
  });
});
