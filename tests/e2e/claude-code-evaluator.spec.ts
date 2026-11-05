import { test, expect, type Page } from "@playwright/test";
import { claudeWorkersHarness, type ClaudeWorkersHarness } from "./claude-workers-harness";
import { model } from "./workflow-implicit-goals-harness";

// SPEC-LIFECYCLE-001 CC-012/013. Authored acceptance coverage, not run evidence.
// Only the configured openai-codex default is used: the tagged launcher forces
// its HTTP transport to literal loopback and rejects other destinations.
// Never select Google here: the seeded key is NOT a mocked Google transport.
// Gaps: persisted Claude default (correctly rejected by REST), zero-window and
// unconstructible stream require a test-launcher resolver seam; they are not
// simulated by editing production rows or relabeling an unknown model.
// Unauthenticated defaults remain supporting Go coverage, not claimed here.
const text = (value: string) => ({ text: value });
const notDone = { answer: "VERDICT: NOT_DONE\nREASON: One more local step", evaluation: true } as const;
const done = { answer: "VERDICT: COMPLETED\nREASON: Local work finished", evaluation: true } as const;

// DONE has no continuation row. Verify receipt and checked final without
// the older settled() helper's mandatory continuation join.
function verdicts(f: ClaudeWorkersHarness, agent: string) {
  return f.rows(`SELECT e.state FROM embedded_goal_evaluations e
    JOIN embedded_goal_applied_receipts r ON r.agent_id=e.agent_id AND r.session_id=e.session_id
      AND r.epoch=e.epoch AND r.generation=e.generation AND r.turn_entry_seq=e.user_seq
      AND r.turn_entry_digest=e.user_digest AND r.goal_revision=e.goal_revision
    JOIN embedded_entries final ON final.agent_id=e.agent_id AND final.session_id=e.session_id
      AND final.epoch=e.epoch AND final.generation=e.generation AND final.seq=e.final_seq AND final.digest=e.final_digest
    JOIN embedded_append_keys k ON k.agent_id=final.agent_id AND k.session_id=final.session_id
      AND k.epoch=final.epoch AND k.generation=final.generation AND k.entry_seq=final.seq
    WHERE e.agent_id=? AND e.state IN ('NOT_DONE','DONE')
      AND final.kind='assistant' AND json_extract(final.body,'$.stopReason')='stop'
    ORDER BY e.user_seq`, agent).map(row => row.state);
}

async function unavailable(f: ClaudeWorkersHarness, page: Page) {
  await expect(page.getByText(/goal evaluation unavailable/i).first()).toBeVisible();
  await expect.poll(() => f.wsFrames.some(frame => /goal evaluation unavailable/i.test(frame))).toBe(true);
}

function noContinuation(f: ClaudeWorkersHarness, agent: string) {
  expect(f.tableRows("embedded_goal_continuations", agent)).toEqual([]);
  expect(f.violations).toEqual([]);
}

function onlyConfiguredEvaluator(f: ClaudeWorkersHarness, count: number) {
  expect(f.requests).toHaveLength(count);
  expect(f.requests.every(request => request.model === model.id &&
    typeof request.instructions === "string" && request.instructions.toLowerCase().startsWith("goal:"))).toBe(true);
  expect(f.violations).toEqual([]);
}

test.describe("Claude configured Pi evaluator only", () => {
  test.setTimeout(90_000);
  let f: ClaudeWorkersHarness | undefined;
  test.afterEach(async () => { await f?.close(); f = undefined; });

  test("CC-013 absent default never searches catalog and recovers to actual NOT_DONE then DONE", async ({ page }) => {
    f = await claudeWorkersHarness({ turns_by_input: [
      { contains: "REASON: Take the next actionable step to make progress on the goal.", steps: [text("Finished without evaluator")] },
      { contains: "Continue now that the configured default is usable", steps: [text("Resumed with configured evaluator")] },
      { contains: "REASON: One more local step", steps: [text("Goal completed locally")] },
    ] }, { defaultModel: null });
    await f.open(page);
    const agent = await f.launch(page);
    expect((await f.api("/v1/models")).default_model).toBeNull();
    await f.api(`/v1/managed-agents/${agent}/goal`, { action: "set", goal: "Finish the local evaluator task" }, "PUT");
    await f.final(page, agent, "Finished without evaluator");
    await unavailable(f, page);
    await f.advance(60_000);
    await f.idle(agent);
    expect(await f.inputs()).toHaveLength(1);
    expect(f.requests).toEqual([]);
    expect(f.tableRows("embedded_goal_evaluations", agent)).toEqual([]);
    expect(f.tableRows("embedded_goal_applied_receipts", agent)).toHaveLength(1);
    expect(f.tableRows("embedded_acks", agent)).toHaveLength(0); // goal wake is not mailbox consumption
    noContinuation(f, agent);
    expect((await f.api(`/v1/managed-agents/${agent}/goal`)).goal_state.enabled).toBe(true);

    f.replies.push(notDone, done);
    await f.api("/v1/settings/default-model", { model }, "PUT");
    await f.userTurn(page, "Continue now that the configured default is usable");
    await expect.poll(() => verdicts(f!, agent)).toEqual(["NOT_DONE"]);
    await f.advance(10_000);
    await expect.poll(() => verdicts(f!, agent)).toEqual(["NOT_DONE", "DONE"]);
    await f.final(page, agent, "Goal completed locally");
    expect((await f.api(`/v1/managed-agents/${agent}/goal`)).goal_state.enabled).toBe(false);
    onlyConfiguredEvaluator(f, 2);
    const inputs = await f.inputs();
    await f.advance(60_000);
    await f.idle(agent);
    expect(await f.inputs()).toEqual(inputs);
    onlyConfiguredEvaluator(f, 2);
  });

  test("CC-013 provider failure preserves queued mail without fallback and later evaluates NOT_DONE then DONE", async ({ page }) => {
    let release!: () => void;
    const barrier = new Promise<void>(resolve => { release = resolve; });
    try {
      f = await claudeWorkersHarness({ turns_by_input: [
        { contains: "REASON: Take the next actionable step to make progress on the goal.", steps: [text("Final before provider failure")] },
        { contains: "Pending mail must survive evaluator failure", steps: [text("Queued mail consumed after failure"), { wait: "interrupt" }] },
        { contains: "Retry evaluation on a fresh eligible turn", steps: [text("Recovery step incomplete")] },
        { contains: "REASON: One more local step", steps: [text("Recovery step complete")] },
      ] }, { defaultModel: model });
      await f.open(page);
      const agent = await f.launch(page);
      f.replies.push({ failure: 503, evaluation: true, barrier });
      await f.api(`/v1/managed-agents/${agent}/goal`, { action: "set", goal: "Recover without losing pending mail" }, "PUT");
      // The mock records receipt before waiting at the provider boundary.
      await expect.poll(() => f!.requests.length).toBe(1);
      await expect(page.getByText("Final before provider failure", { exact: true }).first()).toBeVisible();
      await expect.poll(() => f!.tableRows("embedded_goal_evaluations", agent).map(row => row.state)).toEqual(["UNKNOWN"]);
      const acks = f.tableRows("embedded_acks", agent);
      await f.userTurn(page, "Pending mail must survive evaluator failure");
      await expect.poll(() => f!.rows("SELECT id,state,body FROM messages WHERE recipient=? AND body LIKE '%Pending mail must survive evaluator failure%'", agent).length).toBe(1);
      const pending = f.rows("SELECT id,state,body FROM messages WHERE recipient=? AND body LIKE '%Pending mail must survive evaluator failure%'", agent)[0];
      expect(pending.state).toBe("queued");
      expect(f.tableRows("embedded_acks", agent)).toEqual(acks);
      expect(await f.inputs()).toHaveLength(1);
      noContinuation(f, agent);
      onlyConfiguredEvaluator(f, 1);

      release();
      await unavailable(f, page);
      await expect.poll(() => f!.tableRows("embedded_acks", agent).some(row => row.message_id === pending.id)).toBe(true);
      // The CLI echo/checked ACK proves delivery, not assistant publication.
      // Text followed by wait:interrupt stays held until the abort result.
      await expect.poll(async () => (await f!.inputs()).map(input => input.message.content))
        .toEqual([
          expect.any(Array),
          [{ type: "text", text: "[Direct message from user]: Pending mail must survive evaluator failure" }],
        ]);
      noContinuation(f, agent);
      onlyConfiguredEvaluator(f, 1);
      expect(f.tableRows("embedded_goal_evaluations", agent).map(row => row.state)).toEqual(["UNKNOWN"]);
      expect(f.rows("SELECT id,state FROM messages WHERE recipient=?", agent).find(row => row.id === pending.id)?.state).toBe("acknowledged");
      // Interrupt the deliberately held mail turn. Aborted work must not gain
      // an evaluator verdict; the next successful foreground input can recover.
      await page.getByRole("button", { name: "Interrupt worker", exact: true }).click();
      await f.idle(agent);
      await expect(page.getByText("Queued mail consumed after failure", { exact: true }).first()).toBeVisible();
      await expect.poll(() => f!.rows(`SELECT e.seq FROM embedded_entries e
        JOIN embedded_acks a ON a.agent_id=e.agent_id AND a.session_id=e.session_id
          AND a.epoch=e.epoch AND a.generation=e.generation AND a.entry_seq<e.seq
        WHERE e.agent_id=? AND a.message_id='${pending.id}' AND e.kind='assistant'
          AND json_extract(e.body,'$.stopReason')='aborted'
          AND json_extract(e.body,'$.content[0].text')='Queued mail consumed after failure'`, agent).length).toBe(1);
      expect(f.wsFrames.some(frame => frame.includes("Queued mail consumed after failure"))).toBe(true);
      expect(f.tableRows("embedded_goal_evaluations", agent).map(row => row.state)).toEqual(["UNKNOWN"]);
      noContinuation(f, agent);
      onlyConfiguredEvaluator(f, 1);
      f.replies.push(notDone, done);
      await f.userTurn(page, "Retry evaluation on a fresh eligible turn");
      await expect.poll(() => verdicts(f!, agent)).toEqual(["NOT_DONE"]);
      await f.advance(10_000);
      await expect.poll(() => verdicts(f!, agent)).toEqual(["NOT_DONE", "DONE"]);
      await f.final(page, agent, "Recovery step complete");
      expect(f.tableRows("embedded_acks", agent).filter(row => row.message_id === pending.id)).toHaveLength(1);
      expect(f.rows("SELECT state FROM messages WHERE recipient=?", agent).some(row => row.state === "dead_letter")).toBe(false);
      expect((await f.api(`/v1/managed-agents/${agent}/goal`)).goal_state.enabled).toBe(false);
      onlyConfiguredEvaluator(f, 3);
    } finally { release(); }
  });
});
