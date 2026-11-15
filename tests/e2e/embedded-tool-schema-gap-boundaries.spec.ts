import { test, expect } from "@playwright/test";
import { randomUUID } from "node:crypto";
import { mkdir } from "node:fs/promises";
import { join } from "node:path";
import { workflowHarness, model } from "./workflow-implicit-goals-harness";

async function waitForFinalAndIdle(f: Awaited<ReturnType<typeof workflowHarness>>, agent: string, answer: string) {
  await expect.poll(() => f.rows("SELECT CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? AND kind='assistant'", agent)
    .some(row => String(row.body).includes(answer)), { timeout: 20_000 }).toBe(true);
  await expect.poll(() => f.rows("SELECT activity_state FROM managed_agents WHERE agent_id=?", agent)[0]?.activity_state, { timeout: 20_000 }).toBe("idle");
}

test("nullable pagination with invalid path reports a safe path error", async ({ page }) => {
  test.setTimeout(120_000);
  const f = await workflowHarness();
  const agent = "schema-null-invalid";
  const dir = join(f.router.root, "work");
  try {
    await mkdir(dir, { recursive: true });
    await f.api("/v1/managed-agents", { name: agent, cwd: dir, runtime: "embedded", model });
    await expect.poll(() => f.rows("SELECT lifecycle_state FROM managed_agents WHERE agent_id=?", agent)[0]?.lifecycle_state, { timeout: 30_000 }).toBe("running");
    await f.open(page);
    await page.locator(`.agent-row[data-agent-id="${agent}"] .agent-row-select`).click();
    f.replies.push({ tool: "read", args: { path: ["private-value-8574"], offset: null, limit: null } }, { answer: "rejection handled" });
    const initial = await f.router.advanceClock(1);
    const clock = await f.router.advanceClock(Math.max(1, Date.now() + 2000 - Date.parse(initial.now)));
    await f.api("/v1/messages", { id: randomUUID(), from: "user", to: agent, body: "Try the invalid read", created_at: new Date(clock.now).toISOString(), source: { kind: "web_ui" } });
    await expect.poll(() => f.requests.length, { timeout: 20_000 }).toBeGreaterThanOrEqual(2);
    await waitForFinalAndIdle(f, agent, "rejection handled");
    const read = (f.requests[0].tools as any[]).find(t => t.name === "read");
    for (const field of ["offset", "limit"]) expect(read.parameters.properties[field].type).toEqual(expect.arrayContaining(["integer", "null"]));
    const output = (f.requests[1].input as any[]).find(x => x.type === "function_call_output" && x.call_id === "call_response_1_0");
    expect(output).toBeDefined();
    expect(output.output).toMatch(/path/i);
    expect(output.output).toMatch(/string/i);
    expect(output.output).not.toMatch(/offset|limit|private-value-8574/i);
    const results = f.rows("SELECT CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? AND kind='toolResult'", agent).map(r => JSON.parse(String(r.body)));
    expect(results.filter(r => r.toolCallId === "call_response_1_0|fc_response_1_0")).toEqual([expect.objectContaining({ isError: true })]);
    expect(f.rows("SELECT effect_id FROM embedded_effects WHERE agent_id=?", agent)).toEqual([]);
    expect(f.violations).toEqual([]);
  } finally { await f.close(); }
});

test("invalid reminder records no intent while a valid reminder records an observed intent", async ({ page }) => {
  test.setTimeout(120_000);
  const f = await workflowHarness();
  const agent = "schema-intent-control";
  const dir = join(f.router.root, "work");
  try {
    await mkdir(dir, { recursive: true });
    await f.api("/v1/managed-agents", { name: agent, cwd: dir, runtime: "embedded", model });
    await expect.poll(() => f.rows("SELECT lifecycle_state FROM managed_agents WHERE agent_id=?", agent)[0]?.lifecycle_state, { timeout: 30_000 }).toBe("running");
    await f.open(page);
    await page.locator(`.agent-row[data-agent-id="${agent}"] .agent-row-select`).click();
    f.replies.push({ tool: "mecha_reminder", args: { action: "create", body: ["private-reminder-8574"], delay_seconds: 3600 } }, { answer: "rejection handled" });
    const first = await f.router.advanceClock(1);
    const firstClock = await f.router.advanceClock(Math.max(1, Date.now() + 2000 - Date.parse(first.now)));
    await f.api("/v1/messages", { id: randomUUID(), from: "user", to: agent, body: "Try invalid reminder", created_at: new Date(firstClock.now).toISOString(), source: { kind: "web_ui" } });
    await expect.poll(() => f.requests.length, { timeout: 20_000 }).toBeGreaterThanOrEqual(2);
    await waitForFinalAndIdle(f, agent, "rejection handled");
    const rejected = (f.requests[1].input as any[]).find(x => x.type === "function_call_output" && x.call_id === "call_response_1_0");
    expect(rejected.output).toMatch(/body|string/i);
    expect(rejected.output).not.toContain("private-reminder-8574");
    expect(f.rows("SELECT effect_id FROM embedded_effects WHERE agent_id=?", agent)).toEqual([]);
    expect(f.rows("SELECT id FROM reminders WHERE target=? OR creator=?", agent, agent)).toEqual([]);
    expect(f.rows("SELECT call_id FROM embedded_own_reminder_calls WHERE agent_id=?", agent)).toEqual([]);
    f.replies.push({ tool: "mecha_reminder", args: { action: "create", body: "intent control", delay_seconds: 3600 } }, { answer: "reminder scheduled" });
    const initial = await f.router.advanceClock(1);
    const clock = await f.router.advanceClock(Math.max(1, Date.now() + 2000 - Date.parse(initial.now)));
    await f.api("/v1/messages", { id: randomUUID(), from: "user", to: agent, body: "Schedule a reminder", created_at: new Date(clock.now).toISOString(), source: { kind: "web_ui" } });
    await expect.poll(() => f.requests.length, { timeout: 20_000 }).toBeGreaterThanOrEqual(4);
    await waitForFinalAndIdle(f, agent, "reminder scheduled");
    const output = (f.requests[3].input as any[]).find(x => x.type === "function_call_output" && x.call_id === "call_response_3_0");
    expect(output).toBeDefined();
    expect(f.rows("SELECT effect_id, state FROM embedded_effects WHERE agent_id=?", agent)).toEqual([expect.objectContaining({ effect_id: "call_response_3_0|fc_response_3_0", state: "observed" })]);
    const receipts = f.rows("SELECT call_id, action, state, reminder_id FROM embedded_own_reminder_calls WHERE agent_id=?", agent);
    expect(receipts).toHaveLength(1);
    expect(receipts[0]).toMatchObject({ call_id: "call_response_3_0|fc_response_3_0", action: "create", state: "observed" });
    expect(receipts[0].reminder_id).toBeTruthy();
    expect(f.rows("SELECT id, target, body, state FROM reminders WHERE target=? OR creator=?", agent, agent)).toEqual([
      expect.objectContaining({ id: receipts[0].reminder_id, target: agent, body: "intent control", state: "scheduled" }),
    ]);
    expect(f.violations).toEqual([]);
  } finally { await f.close(); }
});
