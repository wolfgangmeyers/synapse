import { randomUUID } from "node:crypto";
import { test, expect, type Page } from "@playwright/test";
import { claudeWorkersHarness, type ClaudeWorkersHarness } from "./claude-workers-harness";

// CC-019 (AC4), minimal slice: input arriving during a running Claude turn is
// written to the same process while that turn is still active, echoed, and then
// consumed by THAT turn (its steer step), not by a later execution. Mail is ACKed
// only on its exact checked echo. Uses the fake CLI with steer_mid_turn:true.
const text = (value: string) => ({ text: value });
const script = () => ({ steer_mid_turn: true, turns: [[
  text("First turn partial"), { barrier: "mid-turn" }, text("First turn done"), { steer: "Steered answer" },
]] });

test.describe("Claude mid-turn steering acceptance", () => {
  test.setTimeout(90_000);
  let f: ClaudeWorkersHarness | undefined;
  let agent = "";
  test.afterEach(async () => {
    try { await f?.close(); } finally { f = undefined; agent = ""; }
  });

  // Starts a fresh process and holds its first turn at the mid-turn barrier (still active).
  async function startHeldTurn(page: Page, firstTurn: string) {
    f = await claudeWorkersHarness(script());
    await f.open(page); agent = await f.launch(page);
    await f.userTurn(page, firstTurn);
    await expect.poll(async () => (await f!.records()).some(row => row.kind === "barrier" && row.name === "mid-turn")).toBe(true);
    const barrier = (await f.records()).find(row => row.kind === "barrier" && row.name === "mid-turn")!;
    expect(await f.starts()).toHaveLength(1);
    const first = (await f.inputs())[0];
    return { barrier, first };
  }

  // The held turn's text is followed by the steer step's message in the same turn,
  // so the runtime persists it as non-terminal (stopReason toolUse), not stop. It is
  // visible and checked-persisted, but cannot satisfy final(), which requires stop.
  async function expectIntermediateText(page: Page, agent: string, text: string) {
    await expect(page.getByText(text, { exact: true }).first()).toBeVisible();
    await expect.poll(() => f!.rows(`SELECT json_extract(part.value, '$.text') AS text
      FROM embedded_entries e
      JOIN embedded_sessions s ON s.agent_id=e.agent_id AND s.session_id=e.session_id
        AND s.epoch=e.epoch AND s.generation=e.generation AND s.current=1
      JOIN json_each(e.body, '$.content') part
      WHERE e.agent_id=? AND e.kind='assistant'
        AND json_extract(e.body, '$.role')='assistant'
        AND json_extract(e.body, '$.stopReason')='toolUse'
        AND json_extract(part.value, '$.type')='text'`, agent)
      .some(row => row.text === text)).toBe(true);
  }

  // The steered input must be consumed by the first turn's steer step: same turn
  // input, no separate execution. Checked before the final texts so the proof
  // cannot be satisfied by a later turn answering it.
  async function expectAbsorbedByActiveTurn(turnInput: unknown, steeredInput: unknown) {
    const absorbed = (await f!.records()).filter(row => row.kind === "steer_absorbed");
    expect(absorbed).toEqual([expect.objectContaining({ turn_input_id: turnInput, input_id: steeredInput })]);
  }

  test("CC-019 steers mid-turn mail into the active turn with checked echo ACK", async ({ page }) => {
    const { barrier, first } = await startHeldTurn(page, "same text");
    await expect.poll(() => f!.tableRows("embedded_acks", agent).length).toBe(1);
    const firstAck = f.tableRows("embedded_acks", agent)[0];

    const messageId = randomUUID();
    const { now } = await f.router.advanceClock(1);
    const sent = await f.request(page, "/v1/messages", { id: messageId, from: "steer-fixture", to: agent, body: "same text", created_at: new Date(now).toISOString() });
    expect(sent.status).toBe(201); expect(sent.body.message.id).toBe(messageId);

    // Active delivery: written to stdin and echoed while the turn is still held.
    await expect.poll(async () => (await f!.inputs()).length, { timeout: 10_000 }).toBe(2);
    await expect.poll(async () => (await f!.records()).some(row => row.kind === "steer_echo"), { timeout: 10_000 }).toBe(true);
    const steered = (await f.inputs())[1];
    expect(JSON.stringify(steered.message)).toContain("[Direct message from steer-fixture]: same text");
    const echo = (await f.records()).find(row => row.kind === "steer_echo")!;
    expect(echo.input_id).toBe(steered.uuid);

    // ACK only after the exact checked echo, before the held turn is released.
    await expect.poll(() => f!.tableRows("embedded_acks", agent).some(row => row.message_id === messageId), { timeout: 10_000 }).toBe(true);
    expect((await f.records()).some(row => row.kind === "barrier_released")).toBe(false);
    const attempt = f.tableRows("embedded_attempts", agent).find(row => row.message_id === messageId);
    expect(attempt).toBeTruthy();
    const ackForSteered = f.tableRows("embedded_acks", agent).find(row => row.message_id === messageId);
    expect(ackForSteered?.attempt_id).toBe(attempt!.attempt_id);
    // Equal text keeps distinct identities; the first ACK is untouched.
    expect(messageId).not.toBe(firstAck.message_id);
    expect(f.tableRows("embedded_acks", agent).filter(row => row.message_id === firstAck.message_id)).toEqual([firstAck]);
    // Same process: no second execution was started while the turn is held.
    expect(await f.starts()).toHaveLength(1);

    // Release the held turn: the active turn answers the steered input at its steer step.
    await f.releaseBarrier(barrier);
    await expectIntermediateText(page, agent, "First turn done");
    await f.final(page, agent, "Steered answer");
    await expectAbsorbedByActiveTurn(first.uuid, steered.uuid);
    const acks = f.tableRows("embedded_acks", agent);
    expect(acks).toHaveLength(2);
    expect(acks.filter(row => row.message_id === messageId)).toHaveLength(1);
    expect(acks.filter(row => row.message_id === firstAck.message_id)).toEqual([firstAck]);
    expect(await f.starts()).toHaveLength(1);
    expect(await f.inputs()).toHaveLength(2);
  });

  test("CC-019 steers a direct user send into the active turn", async ({ page }) => {
    const { barrier, first } = await startHeldTurn(page, "direct first");

    // Direct composer send while the first turn is held.
    await f!.userTurn(page, "direct steer");
    await expect.poll(async () => (await f!.inputs()).length, { timeout: 10_000 }).toBe(2);
    await expect.poll(async () => (await f!.records()).some(row => row.kind === "steer_echo"), { timeout: 10_000 }).toBe(true);
    const steered = (await f.inputs())[1];
    expect(JSON.stringify(steered.message)).toContain("direct steer");
    const echo = (await f.records()).find(row => row.kind === "steer_echo")!;
    expect(echo.input_id).toBe(steered.uuid);
    // Same process: the direct send did not start a second execution.
    expect(await f.starts()).toHaveLength(1);

    await f.releaseBarrier(barrier);
    await expectIntermediateText(page, agent, "First turn done");
    await f.final(page, agent, "Steered answer");
    await expectAbsorbedByActiveTurn(first.uuid, steered.uuid);
    expect(await f.starts()).toHaveLength(1);
    expect(await f.inputs()).toHaveLength(2);
  });
});
