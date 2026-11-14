import { randomUUID } from "node:crypto";
import { test, expect } from "@playwright/test";
import { workflowHarness, model } from "./workflow-implicit-goals-harness";

// RAR-003: the real browser, proxy, router and SQLite store; only the model
// provider is local and scripted. Hold its first response until restart cancels it.
test("individual restart interrupts an active embedded agent and preserves usable prompt and mail", async ({ page }) => {
  test.setTimeout(90_000);
  const f = await workflowHarness({ restartCodexConsumerFixture: true });
  let release!: () => void;
  const barrier = new Promise<void>(resolve => { release = resolve; });
  let cancelled: boolean | undefined;
  const agent = `restart-active-${randomUUID()}`;
  try {
    await f.open(page);
    await f.api("/v1/managed-agents", { name: agent, cwd: `${f.router.root}/work`, runtime: "embedded", model });
    await expect(page.locator(`[data-agent-id="${agent}"]`)).toBeVisible();
    await page.locator(`[data-agent-id="${agent}"]`).click();
    const agentRow = () => f.rows("SELECT pi_session_id, session_epoch, selected_model_provider, selected_model_id FROM managed_agents WHERE agent_id=?", agent)[0];
    await expect.poll(() => f.rows("SELECT accepting_prompts FROM managed_agents WHERE agent_id=?", agent)[0]?.accepting_prompts).toBe(1);
    const before = agentRow();
    expect(before.pi_session_id).toBeTruthy();
    expect(before).toMatchObject({ selected_model_provider: model.provider, selected_model_id: model.id });
    f.replies.push({ answer: "Remembered prior answer" });
    await f.api(`/v1/managed-agents/${agent}/prompt`, { message: "Remember prior context" });
    await expect.poll(() => f.rows("SELECT activity_state FROM managed_agents WHERE agent_id=?", agent)[0]?.activity_state).toBe("idle");
    f.replies.push({ answer: "stale interrupted answer", barrier, onRelease: destroyed => { cancelled = destroyed; } });
    const activePrompt = fetch(`${f.proxy.url}/v1/managed-agents/${agent}/prompt`, {
      method: "POST", headers: { "Content-Type": "application/json", "X-Request-ID": randomUUID() },
      body: JSON.stringify({ message: "Hold active turn" }),
    }).then(async response => ({ status: response.status, body: await response.text() }));
    await expect.poll(() => f.requests.length).toBe(2);
    const restartID = randomUUID();
    const restart = async (id: string, body: object = {}) => {
      const response = await fetch(`${f.proxy.url}/v1/managed-agents/${agent}/restart`, {
        method: "POST", headers: { "Content-Type": "application/json", "X-Request-ID": id }, body: JSON.stringify(body),
      });
      return { status: response.status, body: await response.text() };
    };
    // Concurrent HTTP requests exercise proxy/router/store replay, but do not
    // force both handlers past the internal lookup before admission.
    const [first, replay] = await Promise.all([restart(restartID), restart(restartID)]);
    expect([200, 202], first.body).toContain(first.status);
    expect(replay).toEqual(first);
    expect(f.rows("SELECT operation_id FROM managed_http_operations WHERE request_id=?", restartID)).toHaveLength(1);
    const conflict = await restart(restartID, { model });
    expect(conflict.status, conflict.body).toBe(409);
    expect(JSON.parse(conflict.body).error.code).toBe("IDEMPOTENCY_CONFLICT");
    release();
    await expect.poll(() => cancelled, { timeout: 15_000 }).toBe(true);
    const promptOutcome = await activePrompt;
    expect(promptOutcome.status, promptOutcome.body).toBe(409);
    expect(JSON.parse(promptOutcome.body).error.code).toBe("PROMPT_UNAVAILABLE");
    await expect.poll(() => f.rows("SELECT activity_state FROM managed_agents WHERE agent_id=?", agent)[0]?.activity_state, { timeout: 15_000 }).toBe("idle");
    expect(agentRow()).toEqual(before);
    expect(f.rows("SELECT session_id, epoch FROM embedded_sessions WHERE agent_id=? AND current=1", agent)).toEqual([{ session_id: before.pi_session_id, epoch: before.session_epoch }]);

    f.replies.push({ answer: "Prompt after restart answered" });
    await f.api(`/v1/managed-agents/${agent}/prompt`, { message: "Prompt after restart" });
    await expect(page.getByText("Prompt after restart answered", { exact: true }).first()).toBeVisible({ timeout: 15_000 });
    await expect.poll(() => f.rows("SELECT activity_state FROM managed_agents WHERE agent_id=?", agent)[0]?.activity_state).toBe("idle");
    f.replies.push({ answer: "Mail after restart answered" });
    const mailID = randomUUID();
    const now = (await f.advance(1)).now;
    await f.api("/v1/messages", { id: mailID, from: "restart-fixture", to: agent, subject: "Restart follow-up", body: "Mail after restart", created_at: now, source: { kind: "agent" } });
    await expect(page.getByText("Mail after restart answered", { exact: true }).first()).toBeVisible({ timeout: 15_000 });
    await expect.poll(() => f.rows("SELECT message_id FROM embedded_acks WHERE message_id=?", mailID)).toHaveLength(1);
    expect(f.requests).toHaveLength(4);
    const restartContext = JSON.stringify(f.requests[2].input);
    expect(restartContext).toContain("Remember prior context");
    expect(restartContext).toContain("Remembered prior answer");
    expect(f.requests.map(r => r.model)).toEqual([model.id, model.id, model.id, model.id]);
    expect(f.wsFrames.some(frame => frame.includes("stale interrupted answer"))).toBe(false);
    expect(f.violations).toEqual([]);
  } finally {
    release();
    await f.close();
  }
});
