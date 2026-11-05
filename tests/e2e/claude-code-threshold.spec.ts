import { test, expect, type Page } from "@playwright/test";
import { claudeWorkersHarness, type ClaudeWorkersHarness } from "./claude-workers-harness";

// SPEC-LIFECYCLE-001 / CC-011. Authored acceptance coverage, not run evidence.
// Real browser -> proxy/router -> fake CLI -> checked SQLite -> WS/browser.
// No count-bound, seed-truncation, or Pi-parity coverage is claimed here.
// Supporting Go coverage: embedded_claude_fresh_threshold_test.go and
// TestClaudeReview6bManagerResetWakeThreshold (actual manager reset epilogue).

// The fixture has no `usage` action. Its existing `emit` action accepts the
// actual Claude assistant schema; the automatic success result closes this
// assistant without replacing its usage. Context = input + both cache fields,
// NOT output_tokens or result.modelUsage.inputTokens. No internal mock needed.
function answer(text: string, tokens: number) {
  return [{ emit: { type: "assistant", message: {
    id: `threshold-${text.replaceAll(" ", "-")}`, type: "message", role: "assistant", model: "sonnet",
    content: [{ type: "text", text }], stop_reason: "end_turn", stop_sequence: null,
    usage: { input_tokens: tokens - 50, cache_creation_input_tokens: 20,
      cache_read_input_tokens: 30, output_tokens: 10 },
  } } }];
}
// Executions each start a process; reset rotates the resumed conversation ID.
async function sessionCount(f: ClaudeWorkersHarness) {
  const starts = await f.starts();
  const sessions = starts.map(start => {
    if (!Array.isArray(start.argv)) throw Error("missing CLI argv");
    const index = start.argv.findIndex(arg => arg === "--session-id" || arg === "--resume");
    const id = start.argv[index + 1];
    if (index < 0 || typeof id !== "string") throw Error("missing CLI session ID");
    return id;
  });
  return new Set(sessions).size;
}
const warningText = /Context usage warning: You have reached/;
const warnings = (f: ClaudeWorkersHarness, agent: string) => f.rows(
  "SELECT * FROM managed_complete_history WHERE agent_id=? AND message='Context usage threshold warning' ORDER BY sequence", agent);
const epoch = (f: ClaudeWorkersHarness, agent: string) => f.tableRows("managed_agents", agent)[0]?.session_epoch;

async function settings(f: ClaudeWorkersHarness, page: Page, config: object) {
  const saved = await f.request(page, "/v1/settings/context-thresholds", config, "PUT");
  expect(saved.status, JSON.stringify(saved.body)).toBe(200);
  expect(saved.body).toEqual(config);
  const loaded = await f.request(page, "/v1/settings/context-thresholds");
  expect(loaded.status).toBe(200);
  expect(loaded.body).toEqual(config);
}

async function settledAnswer(f: ClaudeWorkersHarness, page: Page, agent: string, text: string, tokens: number) {
  await f.final(page, agent, text); // visible + checked current-epoch stop + WS + idle
  await expect.poll(() => f.rows(
    "SELECT execution_json FROM embedded_sessions WHERE agent_id=? AND current=1", agent)
    .map(row => row.execution_json)).toEqual([null]);
  await expect.poll(() => f.tableRows("managed_agents", agent)[0]?.context_tokens).toBe(tokens);
  expect(f.tableRows("managed_agents", agent)[0]?.context_window).toBe(1_000_000);
}

async function orderedSeed(f: ClaudeWorkersHarness, parts: string[]) {
  const inputs = await f.inputs();
  const fresh = inputs.filter(input => JSON.stringify(input).includes("Context reset completed"));
  expect(fresh).toHaveLength(1);
  const seed = JSON.stringify(fresh[0]);
  let previous = -1;
  for (const part of parts) {
    const index = seed.indexOf(part);
    expect(index, `seed order: ${part}`).toBeGreaterThan(previous);
    previous = index;
  }
}

test.describe("Claude context thresholds through real settings and reset coordinator", () => {
  test.setTimeout(90_000);
  let f: ClaudeWorkersHarness | undefined;
  let agent = "";
  test.afterEach(async ({}, info) => {
    try {
      if (f && info.status !== info.expectedStatus) await info.attach("threshold-boundaries", {
        contentType: "application/json", body: Buffer.from(JSON.stringify({
          records: await f.records(), ws: f.wsFrames, violations: f.violations,
          sessions: agent ? f.tableRows("embedded_sessions", agent) : [],
          warnings: agent ? warnings(f, agent) : [],
          entries: agent ? f.rows("SELECT epoch,seq,kind,CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? ORDER BY epoch,seq", agent) : [],
        }, null, 2)),
      });
    } finally { await f?.close(); f = undefined; agent = ""; }
  });

  test("CC-011 threshold rotates with ordered seed; oversized fresh wake warns once, suppresses growth, then lower usage rearms", async ({ page }) => {
    f = await claudeWorkersHarness({ turns: [
      [{ mcp_call: { id: "threshold-note", name: "scratchpad", input: { action: "append", content: "Preserve amber plan" } } },
        ...answer("History amber ready", 50_000)],
      answer("Threshold crossing checked", 200_000),
    ] });
    await f.open(page);
    await settings(f, page, { warning_percent: 10, reset_percent: 20, model_thresholds: {} });
    agent = await f.launch(page, { initial_instruction: "Instruction amber first" });
    await settledAnswer(f, page, agent, "History amber ready", 50_000);
    expect(epoch(f, agent)).toBe(1);
    expect(warnings(f, agent)).toHaveLength(0);
    // configure() changes only future CLI processes. Ordinary input rules are
    // separate from reminder turns, so warning delivery cannot shift the script.
    await f.configure({ turns_by_input: [
      { contains: "Context reset completed", steps: answer("Oversized fresh wake checked", 250_000) },
      { contains: "Cross the original threshold", steps: answer("Threshold crossing checked", 200_000) },
      { contains: "Context usage warning:", steps: answer("Fresh warning consumed", 250_000) },
      { contains: "ordinary growth probe", steps: answer("Ordinary growth checked", 300_000) },
      { contains: "ordinary boundary probe", steps: answer("Ordinary boundary checked", 200_000) },
      { contains: "compact below boundary", steps: answer("Below boundary checked", 199_999) },
      { contains: "cross rearmed boundary", steps: answer("Rearmed crossing checked", 200_000) },
    ] });
    await f.userTurn(page, "Cross the original threshold");
    await settledAnswer(f, page, agent, "Fresh warning consumed", 250_000);
    expect(epoch(f, agent)).toBe(2);
    expect(await sessionCount(f)).toBe(2);
    // Reset imports preserved assistant rows into the new epoch; check origin here.
    expect(f.rows(`SELECT epoch FROM embedded_entries WHERE agent_id=? AND epoch=1 AND kind='assistant'
      AND json_extract(body,'$.stopReason')='stop'
      AND CAST(body AS TEXT) LIKE '%Threshold crossing checked%'`, agent).map(row => row.epoch)).toEqual([1]);
    await orderedSeed(f, ["Instruction amber first", "Preserve amber plan", "History amber ready", "Context reset completed"]);
    expect(warnings(f, agent)).toHaveLength(1);
    expect(String(warnings(f, agent)[0].message_content)).toContain("250000 / 1000000");
    await expect(page.getByText(warningText).first()).toBeVisible();
    await expect(page.getByText(/Context Reset \(Epoch 2\)/).first()).toBeVisible();
    expect(f.wsFrames.some(frame => frame.includes("Context usage threshold warning"))).toBe(true);

    // Positive ordinary turns AFTER the fresh warning has itself settled.
    // Absence of a timer tick alone is not evidence that the guard survived.
    for (const [input, output, tokens] of [
      ["ordinary growth probe", "Ordinary growth checked", 300_000],
      ["ordinary boundary probe", "Ordinary boundary checked", 200_000],
    ] as const) {
      await f.userTurn(page, input);
      await settledAnswer(f, page, agent, output, tokens);
      expect(epoch(f, agent)).toBe(2);
      expect(await sessionCount(f)).toBe(2);
      expect(warnings(f, agent)).toHaveLength(1);
    }
    await f.userTurn(page, "compact below boundary");
    await settledAnswer(f, page, agent, "Below boundary checked", 199_999);
    expect(epoch(f, agent)).toBe(2);
    expect(warnings(f, agent)).toHaveLength(1); // warning remains once per epoch
    // The next new process must have headroom. This is not a second forced reset.
    await f.configure({ turns_by_input: [
      { contains: "Context reset completed", steps: answer("Rearmed epoch fresh checked", 50_000) },
      { contains: "cross rearmed boundary", steps: answer("Rearmed crossing checked", 200_000) },
    ] });
    await f.userTurn(page, "cross rearmed boundary");
    await settledAnswer(f, page, agent, "Rearmed epoch fresh checked", 50_000);
    expect(epoch(f, agent)).toBe(3);
    expect(f.rows(`SELECT epoch FROM embedded_entries WHERE agent_id=? AND epoch=2 AND kind='assistant'
      AND json_extract(body,'$.stopReason')='stop'
      AND CAST(body AS TEXT) LIKE '%Rearmed crossing checked%'`, agent).map(row => row.epoch)).toEqual([2]);
    expect(await sessionCount(f)).toBe(3);
    expect(warnings(f, agent)).toHaveLength(1);
    await expect(page.getByText(/Context Reset \(Epoch 3\)/).first()).toBeVisible();
    expect(f.violations).toEqual([]);
  });

  // At 250k: full-key wins (no warning), bare ID wins over global (warning,
  // not reset), globals alone reset. Each variant then proves its own reset
  // boundary through an actual new epoch rather than settings readback alone.
  for (const variant of [
    { name: "provider/model override beats bare ID and global", overrides: {
      sonnet: { warning_percent: 20, reset_percent: 30 },
      "claude-code/sonnet": { warning_percent: 30, reset_percent: 40 },
    }, reset: 400_000, warning: false, earlyReset: false },
    { name: "bare model override beats global", overrides: {
      sonnet: { warning_percent: 20, reset_percent: 30 },
    }, reset: 300_000, warning: true, earlyReset: false },
    { name: "global fallback applies without model override", overrides: {},
      reset: 200_000, warning: false, earlyReset: true },
  ]) test(`CC-011 ${variant.name}`, async ({ page }) => {
    f = await claudeWorkersHarness({ turns_by_input: [
      { contains: "Context reset completed", steps: answer("Precedence fresh epoch checked", 50_000) },
      { contains: "precedence baseline", steps: answer("Precedence baseline checked", 50_000) },
      { contains: "precedence 250k probe", steps: answer("Precedence probe checked", 250_000) },
      { contains: "Context usage warning:", steps: answer("Precedence warning consumed", 250_000) },
      { contains: "precedence ordinary repeat", steps: answer("Precedence repeat checked", 250_000) },
      { contains: "precedence reset boundary", steps: answer("Precedence boundary checked", variant.reset) },
    ] });
    await f.open(page);
    await settings(f, page, { warning_percent: 10, reset_percent: 20, model_thresholds: variant.overrides });
    agent = await f.launch(page);
    await f.userTurn(page, "precedence baseline");
    await settledAnswer(f, page, agent, "Precedence baseline checked", 50_000);
    await f.userTurn(page, "precedence 250k probe");
    if (variant.earlyReset) {
      await settledAnswer(f, page, agent, "Precedence fresh epoch checked", 50_000);
    } else {
      await settledAnswer(f, page, agent, variant.warning ? "Precedence warning consumed" : "Precedence probe checked", 250_000);
      expect(epoch(f, agent)).toBe(1);
      expect(await sessionCount(f)).toBe(1);
      expect(warnings(f, agent)).toHaveLength(variant.warning ? 1 : 0);
      if (variant.warning) await expect(page.getByText(warningText).first()).toBeVisible();
      // A completed next turn is the positive boundary for no early reset and
      // warning-once assertions, not an arbitrary negative observation window.
      await f.userTurn(page, "precedence ordinary repeat");
      await settledAnswer(f, page, agent, "Precedence repeat checked", 250_000);
      expect(epoch(f, agent)).toBe(1);
      expect(warnings(f, agent)).toHaveLength(variant.warning ? 1 : 0);
      await f.userTurn(page, "precedence reset boundary");
      await settledAnswer(f, page, agent, "Precedence fresh epoch checked", 50_000);
    }
    expect(epoch(f, agent)).toBe(2);
    expect(await sessionCount(f)).toBe(2);
    expect(warnings(f, agent)).toHaveLength(variant.warning ? 1 : 0);
    await expect(page.getByText(/Context Reset \(Epoch 2\)/).first()).toBeVisible();
    expect(f.violations).toEqual([]);
  });
});
