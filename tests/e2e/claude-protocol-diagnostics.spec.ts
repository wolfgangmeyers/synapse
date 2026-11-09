import { readFile } from "node:fs/promises";
import { randomUUID } from "node:crypto";
import { basename, dirname, join } from "node:path";
import { test, expect, type Page } from "@playwright/test";
import { claudeWorkersHarness, exists, type ClaudeWorkersHarness } from "./claude-workers-harness";

async function diagnostics(f: ClaudeWorkersHarness) {
  try {
    const raw = await readFile(join(dirname(f.router.logPath), `claude-protocol-${basename(f.router.logPath)}`), "utf8");
    return raw.split("\n").filter(Boolean).map(line => JSON.parse(line));
  } catch (error) { if (error.code === "ENOENT") return []; throw error; }
}
async function sendMail(f: ClaudeWorkersHarness, page: Page, agent: string) {
  const id = randomUUID();
  const { now } = await f.router.advanceClock(1);
  const sent = await f.request(page, "/v1/messages", { id, from: "diagnostic-fixture", to: agent, body: "private-input-secret", created_at: new Date(now).toISOString() });
  expect(sent.status).toBe(201);
  return id;
}
async function checkSettled(f: ClaudeWorkersHarness, agent: string, id: string, grantPath: string) {
  await expect.poll(() => exists(grantPath), { timeout: 15_000 }).toBe(false);
  await f.idle(agent);
  expect(f.rows("SELECT state,claude_stdin_started FROM messages WHERE id=?", id)).toEqual([{ state: "queued", claude_stdin_started: 1 }]);
  expect(f.tableRows("embedded_acks", agent).filter(row => row.message_id === id)).toEqual([]);
  expect(f.rows("SELECT CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? AND kind='assistant'", agent)
    .some(row => String(row.body).includes("FORBIDDEN"))).toBe(false);
  expect(f.rows("SELECT 1 FROM embedded_entries WHERE agent_id=? AND kind='assistant' AND json_extract(body, '$.stopReason')='stop'", agent)).toEqual([]);
  expect(f.rows("SELECT o.* FROM embedded_reply_outbox o JOIN embedded_attempts a ON a.attempt_id=o.attempt_id WHERE a.message_id=?", id)).toEqual([]);
}
for (const scenario of [
  { title: "result before echo is sanitized and remains unacknowledged", mode: "result", reason: "result_with_pending_input" },
  { title: "mismatched echo is sanitized and remains unacknowledged", mode: "identity", reason: "replay_unknown_identity" },
  { title: "content mismatch fails closed without exposing input", mode: "content", reason: "replay_content_mismatch" },
]) {
  test(scenario.title, async ({ page }) => {
    test.setTimeout(90_000);
    const f = await claudeWorkersHarness({ replay_control: "hold" });
    try {
      await f.open(page); const agent = await f.launch(page);
      const id = await sendMail(f, page, agent);
      await expect.poll(async () => (await f.records()).some(row => row.kind === "barrier" && row.name === "replay")).toBe(true);
      const input = (await f.inputs())[0];
      const grant = await f.grant();
      const event = scenario.mode === "result"
        ? { type: "result", subtype: "success", result: "FORBIDDEN private-result-secret" }
        : { type: "user", isReplay: true, uuid: scenario.mode === "content" ? input.uuid : "private-identity-secret", message: { role: "user", content: "private-echo-secret" } };
      await f.emitIdleEvents([event]);
      await expect.poll(async () => (await diagnostics(f)).map(row => row.reason)).toContain(scenario.reason);
      await checkSettled(f, agent, id, grant.path);
      const records = await diagnostics(f);
      expect(records).toHaveLength(1);
      expect(records[0].pending_count).toBe(1);
      expect(records[0].recent).toMatch(/^[a-z_,]+$/);
      expect(JSON.stringify(records[0])).not.toMatch(/private|secret/);
      expect(await readFile(f.router.logPath, "utf8")).not.toContain("claude_protocol_diagnostic");
    } finally { await f.close(); }
  });
}
test("matching echo has no failure diagnostic", async ({ page }) => {
  test.setTimeout(90_000);
  const f = await claudeWorkersHarness({ turns: [[{ text: "normal response" }]] });
  try {
    await f.open(page); const agent = await f.launch(page);
    await f.userTurn(page, "private-input-secret");
    await f.final(page, agent, "normal response");
    expect(f.tableRows("embedded_acks", agent)).toHaveLength(1);
    expect(await diagnostics(f)).toHaveLength(0);
  } finally { await f.close(); }
});
