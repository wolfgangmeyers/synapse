import { readFile, writeFile } from "node:fs/promises";
import { randomUUID } from "node:crypto";
import { DatabaseSync } from "node:sqlite";
import { test, expect, type Page } from "@playwright/test";
import { workflowHarness, userTurn as composeUserTurn, model, type WorkflowHarness } from "./workflow-implicit-goals-harness";

// SPEC-RUNTIME-ENGINE-001. Only the external provider is scripted. See
// runtime-engine-boundary-coverage.md for reused tests and remaining gaps.
test.describe("Pi runtime boundary", () => {
  test.setTimeout(60_000);
  let f: WorkflowHarness;
  let agent: string;
  test.beforeEach(async () => { f = await workflowHarness(); agent = `boundary-${randomUUID()}`; });
  test.afterEach(async ({}, info) => {
    try {
      if (f && info.status !== info.expectedStatus) {
        await writeFile(info.outputPath("boundary-diagnostics.json"), JSON.stringify({
          requests: f.requests, violations: f.violations, replies: f.replies,
          log: await readFile(f.router.logPath, "utf8"),
          entries: f.rows("SELECT kind, CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? ORDER BY epoch,seq", agent),
          agents: f.rows("SELECT * FROM managed_agents WHERE agent_id=?", agent),
        }, null, 2));
      }
      if (f && info.status !== info.expectedStatus) await info.attach("runtime-boundary", {
        contentType: "application/json", body: Buffer.from(JSON.stringify({
          requests: f.requests, violations: f.violations, replies: f.replies,
          entries: f.rows("SELECT *, CAST(body AS TEXT) AS text FROM embedded_entries WHERE agent_id=? ORDER BY epoch,seq", agent),
          attempts: f.rows("SELECT * FROM embedded_attempts WHERE agent_id=?", agent),
          acks: f.rows("SELECT * FROM embedded_acks WHERE agent_id=?", agent),
        }, null, 2)),
      });
    } finally { await f?.close(); }
  });
  async function userTurn(page: Page, text: string) {
    // Match the router's frozen test clock, as claude-workers-harness does.
    // Wall-clock composer times can hide checked answers outside the timeline.
    const { now } = await f.router.advanceClock(1);
    await page.clock.setFixedTime(new Date(now));
    await composeUserTurn(page, text);
  }
  async function launch(page: Page, width = 1920) {
    await page.setViewportSize({ width, height: 800 });
    await f.open(page);
    await f.api("/v1/managed-agents", { name: agent, cwd: `${f.router.root}/work`, runtime: "embedded", model });
    await expect(page.locator(`[data-agent-id="${agent}"]`)).toBeVisible();
    await page.locator(`[data-agent-id="${agent}"]`).click();
  }
  const state = () => f.rows("SELECT activity_state FROM managed_agents WHERE agent_id=?", agent);
  async function idle() { await expect.poll(state).toEqual([{ activity_state: "idle" }]); }
  const attempts = () => f.rows("SELECT * FROM embedded_attempts WHERE agent_id=? ORDER BY rowid", agent);
  const acks = () => f.rows(`SELECT a.message_id, a.epoch, e.kind, k.append_key FROM embedded_acks a
    JOIN embedded_entries e ON e.agent_id=a.agent_id AND e.session_id=a.session_id AND e.epoch=a.epoch
      AND e.generation=a.generation AND e.seq=a.entry_seq
    JOIN embedded_append_keys k ON k.agent_id=e.agent_id AND k.session_id=e.session_id
      AND k.epoch=e.epoch AND k.generation=e.generation AND k.entry_seq=e.seq
    WHERE a.agent_id=? ORDER BY a.rowid`, agent);
  function barrier() {
    let release!: () => void;
    const promise = new Promise<void>(resolve => { release = resolve; });
    return { promise, release };
  }
  async function answer(page: Page, text: string, response: string) {
    f.replies.push({ answer: response });
    await userTurn(page, text);
    await expect(page.getByText(response, { exact: true }).first()).toBeVisible();
    await idle();
  }
  function clean() { expect(f.violations).toEqual([]); expect(f.replies).toEqual([]); }
  async function resetFromControls(page: Page) {
    await page.getByRole("button", { name: "More agent controls", exact: true }).click();
    await page.getByRole("menu", { name: "Agent controls menu" }).getByRole("button", { name: "Reset", exact: true }).click();
  }

  test("RE-001 preserves browser text checked ACK answer and projected usage", async ({ page }) => {
    await launch(page);
    const text = "Exact input: α  two spaces\nsecond line";
    const pixel = await page.evaluate(() => {
      const canvas = document.createElement("canvas"); canvas.width = 2; canvas.height = 2;
      const context = canvas.getContext("2d");
      if (!context) throw Error("Canvas unavailable");
      context.fillStyle = "red"; context.fillRect(0, 0, 2, 2);
      return canvas.toDataURL("image/png").split(",")[1];
    });
    await page.locator('input[type="file"][accept="image/*"]').setInputFiles({
      name: "boundary.png", mimeType: "image/png", buffer: Buffer.from(pixel, "base64"),
    });
    await expect(page.getByRole("img", { name: "boundary.png", exact: true })).toBeVisible();
    await answer(page, text, "Boundary final exactly once");
    await expect(page.getByText("Boundary final exactly once", { exact: true })).toHaveCount(1);
    expect(JSON.stringify(f.requests[0].input)).toContain(JSON.stringify(text).slice(1, -1));
    // Pi currently renders an attachment reference, not inline image bytes.
    // Actual image loading through read is mapped to the existing catalog test.
    const rendered = JSON.stringify(f.requests[0].input);
    expect(rendered).toContain("Attached image:");
    expect(rendered.indexOf("Exact input:")).toBeLessThan(rendered.indexOf("Attached image:"));
    expect(rendered).toContain(f.router.databasePath + ".attachments/");
    expect(attempts()).toHaveLength(1);
    expect(acks()).toEqual([{ message_id: attempts()[0].message_id, epoch: 1, kind: "user", append_key: attempts()[0].append_key }]);
    expect(f.rows("SELECT context_tokens, context_window FROM managed_agents WHERE agent_id=?", agent)[0])
      .toMatchObject({ context_tokens: expect.any(Number), context_window: expect.any(Number) });
    expect(Number(f.rows("SELECT context_tokens FROM managed_agents WHERE agent_id=?", agent)[0].context_tokens)).toBeGreaterThan(0);
    expect(f.rows("SELECT o.* FROM embedded_reply_outbox o JOIN embedded_attempts a ON a.attempt_id=o.attempt_id WHERE a.agent_id=?", agent)).toEqual([]);
    expect(f.wsFrames.some(frame => frame.includes("Boundary final exactly once"))).toBe(true);
    clean();
  });

  test("RE-002 ACKs equal-text steering only on checked consumption", async ({ page }) => {
    await launch(page);
    const gate = barrier();
    f.replies.push({ answer: "First held answer", barrier: gate.promise }, { answer: "Steering consumed answer" });
    await userTurn(page, "identical boundary input");
    await expect.poll(() => f.requests.length).toBe(1);
    await expect.poll(() => acks().length).toBe(1);
    await userTurn(page, "identical boundary input");
    await expect.poll(() => attempts().length).toBe(2);
    expect(new Set(attempts().map(row => row.message_id)).size).toBe(2);
    expect(acks()).toHaveLength(1);
    gate.release();
    await expect(page.getByText("Steering consumed answer", { exact: true }).first()).toBeVisible();
    await idle();
    expect(acks().map(row => row.message_id)).toEqual(attempts().map(row => row.message_id));
    expect(new Set(acks().map(row => row.append_key)).size).toBe(2);
    expect(f.requests).toHaveLength(2);
    clean();
  });

  test("RE-003 interrupts without losing checked history then terminates", async ({ page }) => {
    await launch(page);
    await answer(page, "Keep this fact: cobalt", "Fact saved cobalt");
    f.replies.push({ hold: true });
    await userTurn(page, "Block this turn");
    await expect.poll(() => f.requests.length).toBe(2);
    const before = f.rows("SELECT session_epoch FROM managed_agents WHERE agent_id=?", agent);
    await page.getByRole("button", { name: "Interrupt worker", exact: true }).click();
    await idle();
    expect(f.rows("SELECT session_epoch FROM managed_agents WHERE agent_id=?", agent)).toEqual(before);
    await answer(page, "What fact did I give?", "Still cobalt after interrupt");
    expect(JSON.stringify(f.requests.at(-1)?.input)).toContain("Fact saved cobalt");
    expect(acks()).toHaveLength(3);
    await f.api(`/v1/managed-agents/${agent}`, undefined, "DELETE");
    await expect.poll(() => f.rows("SELECT desired_state FROM managed_agents WHERE agent_id=?", agent))
      .toEqual([{ desired_state: "terminated" }]);
    expect(acks()).toHaveLength(3);
    clean();
  });

  test("RE-004 failed local tool keeps result before synthesized answer", async ({ page }) => {
    await launch(page);
    f.replies.push({ tool: "read", args: { path: `${f.router.root}/work/nonexistent-boundary-file` } }, { answer: "Missing file handled" });
    await userTurn(page, "Read the missing local file then explain the failure");
    await expect(page.getByText("Missing file handled", { exact: true }).first()).toBeVisible();
    await idle();
    const input = JSON.stringify(f.requests[1].input);
    expect(input).toContain("function_call_output");
    expect(input).toMatch(/ENOENT|no such file|not found/i);
    const entries = f.rows("SELECT kind, CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? ORDER BY seq", agent);
    const resultIndex = entries.findIndex(row => row.kind === "toolResult" || row.kind === "tool_result");
    const finalIndex = entries.findIndex(row => String(row.body).includes("Missing file handled"));
    expect(resultIndex).toBeGreaterThan(-1);
    expect(finalIndex).toBeGreaterThan(resultIndex);
    expect(f.wsFrames.some(frame => frame.includes("Missing file handled"))).toBe(true);
    clean();
  });

  for (const { trigger, width } of [{ trigger: "browser", width: 1280 }, { trigger: "browser", width: 1000 }, { trigger: "tool", width: 1920 }] as const) test(`RE-006 ${trigger} reset at ${width}px preserves scratchpad and recent context`, async ({ page }) => {
    if (trigger === "browser") await page.addInitScript(() => localStorage.setItem("mecha.toolbar.pinnedControls.v2", JSON.stringify(["goal", "reminders", "workflow", "inlineTools", "scratchpad", "monitors", "workerTools", "toolSettings", "restart", "reset", "terminate"])));
    await launch(page, width);
    f.replies.push({ tool: "scratchpad", args: { action: "append", content: "Pinned boundary fact: amber" } }, { answer: "Scratchpad saved amber" });
    await userTurn(page, "Remember amber in scratchpad");
    await expect(page.getByText("Scratchpad saved amber", { exact: true }).first()).toBeVisible();
    await idle();
    f.replies.push({ answer: "Resumed after reset" });
    if (trigger === "browser") await resetFromControls(page);
    else {
      f.replies.unshift({ tool: "context_reset", args: { window: 20 } });
      await userTurn(page, "Reset your context now");
    }
    await expect.poll(() => f.rows("SELECT session_epoch FROM managed_agents WHERE agent_id=?", agent)[0]?.session_epoch).toBe(2);
    await expect(page.getByText("Resumed after reset", { exact: true }).first()).toBeVisible();
    await idle();
    const input = JSON.stringify(f.requests.at(-1)?.input);
    expect(input).toContain("Pinned boundary fact: amber");
    expect(input).toContain("Scratchpad saved amber");
    expect(input).toContain("Context reset completed");
    expect(f.rows("SELECT content FROM scratchpad_entries WHERE agent_id=?", agent)).toEqual([{ content: "Pinned boundary fact: amber" }]);
    clean();
  });

  test("RE-007 failed checked user append prevents provider entry and ACK", async ({ page }) => {
    await launch(page);
    // Fault injection at the actual SQLite boundary, not a mocked persistence
    // method. Trigger lives only in this test's disposable database.
    const db = new DatabaseSync(f.router.databasePath);
    try { db.exec(`CREATE TRIGGER boundary_reject_user BEFORE INSERT ON embedded_entries
      WHEN NEW.kind='user' BEGIN SELECT RAISE(ABORT,'boundary injected append failure'); END`); }
    finally { db.close(); }
    await userTurn(page, "This checked write must fail");
    await expect.poll(() => attempts().length).toBe(1);
    await idle();
    expect(acks()).toEqual([]);
    expect(f.requests).toEqual([]);
    // Pi persists its initial system prompt before the checked user append.
    // No user or later transcript entry may survive the injected rejection.
    expect(f.rows("SELECT seq, kind FROM embedded_entries WHERE agent_id=? ORDER BY seq", agent))
      .toEqual([{ seq: 1, kind: "system" }]);
    expect(f.rows("SELECT message_id FROM embedded_acks WHERE agent_id=?", agent)).toEqual([]);
    expect(f.wsFrames.some(frame => frame.includes(agent))).toBe(true);
    clean();
  });

  test("RE-008 approved reset releases and redelivers an unconsumed steer", async ({ page }) => {
    await launch(page);
    f.replies.push({ hold: true });
    await userTurn(page, "Hold current provider turn");
    await expect.poll(() => f.requests.length).toBe(1);
    await userTurn(page, "Unconsumed steer baseline");
    await expect.poll(() => attempts().length).toBe(2);
    const pending = attempts()[1];
    expect(acks()).toHaveLength(1);
    // Reset owns interruption/drain. Hold its wake so release/no-ACK can be
    // observed before mailbox redelivery consumes the replacement reservation.
    const wake = barrier();
    const delivery = barrier();
    f.replies.push({ answer: "Reset wake complete", barrier: wake.promise },
      { answer: "Steer redelivered after reset", barrier: delivery.promise });
    await resetFromControls(page);
    await expect.poll(() => f.requests.length).toBe(2);
    await expect.poll(() => f.rows("SELECT session_epoch FROM managed_agents WHERE agent_id=?", agent)[0]?.session_epoch).toBe(2);
    expect(attempts().filter(row => row.attempt_id === pending.attempt_id)).toEqual([]);
    expect(attempts().filter(row => row.message_id === pending.message_id)).toEqual([]);
    expect(acks().filter(row => row.message_id === pending.message_id)).toEqual([]);
    expect(acks()).toHaveLength(1); // The already-consumed first input survives.
    expect(f.rows("SELECT * FROM embedded_append_keys WHERE agent_id=?", agent)
      .some(row => row.append_key === pending.append_key)).toBe(false);
    wake.release();
    await expect.poll(() => f.requests.length).toBe(3);
    const replacements = attempts().filter(row => row.message_id === pending.message_id);
    expect(replacements).toHaveLength(1);
    const redelivered = replacements[0];
    expect(redelivered).toMatchObject({ message_id: pending.message_id, epoch: 2,
      message_incarnation_id: pending.message_incarnation_id, snapshot_id: pending.snapshot_id,
      accepted_body: pending.accepted_body, accepted_digest: pending.accepted_digest });
    expect(redelivered.attempt_id).not.toBe(pending.attempt_id);
    expect(redelivered.append_key).not.toBe(pending.append_key);
    expect(acks().filter(row => row.message_id === pending.message_id)).toEqual([{
      message_id: pending.message_id, epoch: 2, kind: "user", append_key: redelivered.append_key,
    }]);
    expect(JSON.stringify(f.requests[2].input)).toContain("Unconsumed steer baseline");
    delivery.release();
    await expect(page.getByText("Steer redelivered after reset", { exact: true })).toHaveCount(1);
    await expect(page.getByText("Steer redelivered after reset", { exact: true })).toBeVisible();
    await idle();
    expect(acks()).toHaveLength(2);
    expect(f.requests).toHaveLength(3);
    clean();
  });
});
