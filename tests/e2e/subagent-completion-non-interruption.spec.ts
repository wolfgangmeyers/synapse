import { test, expect } from "@playwright/test";
import { randomUUID } from "node:crypto";
import { stat, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { workflowHarness, model } from "./workflow-implicit-goals-harness";

test.describe("Subagent completion non-interruption and user steering", () => {
  test.setTimeout(120_000);

  test("subagent completion notice mid-turn does NOT interrupt remaining tools in batch", async ({ page }) => {
    const f = await workflowHarness();
    try {
      const agent = `agent-${randomUUID()}`;
      await f.api("/v1/managed-agents", { name: agent, cwd: join(f.router.root, "work"), runtime: "embedded", model });
      await expect.poll(() => f.rows("SELECT lifecycle_state FROM managed_agents WHERE agent_id=?", agent)[0]?.lifecycle_state).toBe("running");

      await f.open(page);
      const row = page.locator(`.agent-row[data-agent-id="${agent}"]`);
      await expect(row).toBeVisible();
      await row.locator(".agent-row-select").click();

      const step1Active = join(f.router.root, `step1_active_${randomUUID()}.txt`);
      const step1Finish = join(f.router.root, `step1_finish_${randomUUID()}.txt`);
      const step2Marker = join(f.router.root, `step2_marker_${randomUUID()}.txt`);

      const step1Cmd = `touch '${step1Active}' && while [ ! -f '${step1Finish}' ]; do sleep 0.02; done && echo step1_done`;
      const step2Cmd = `touch '${step2Marker}' && echo step2_done`;

      // Turn 1: model emits two tool calls in the same turn
      f.replies.push({
        tools: [
          { tool: "bash", args: { command: step1Cmd, timeout: 10 } },
          { tool: "bash", args: { command: step2Cmd, timeout: 10 } },
        ],
      });
      // Turn 1 completion after both tools run
      f.replies.push({ answer: "Both tools completed successfully." });
      // Turn 2 completion when the subagent completion notice is processed
      f.replies.push({ answer: "Acknowledged subagent completion on next turn." });

      // Start multi-tool turn
      const clock1 = await f.router.advanceClock(1000);
      await f.api("/v1/messages", {
        id: randomUUID(),
        from: "user",
        to: agent,
        body: "begin multi-tool turn",
        created_at: new Date(clock1.now).toISOString(),
        source: { kind: "web_ui" },
      });

      // Wait for tool 1 to start executing
      await expect.poll(async () => {
        try { await stat(step1Active); return true; } catch { return false; }
      }, { timeout: 15_000 }).toBe(true);

      // While tool 1 is still executing, enqueue a subagent completion notice
      const subagentMsgId = `subagent-completed-sub1-${randomUUID().slice(0, 8)}`;
      const clock2 = await f.router.advanceClock(1000);
      await f.api("/v1/messages", {
        id: subagentMsgId,
        from: "system",
        to: agent,
        body: "Subagent sub1 completed: task finished",
        created_at: new Date(clock2.now).toISOString(),
      });

      // Release tool 1 so execution advances to tool 2
      await writeFile(step1Finish, "ok");

      // Verify tool 2 executed (step2Marker exists)
      await expect.poll(async () => {
        try { await stat(step2Marker); return true; } catch { return false; }
      }, { timeout: 15_000 }).toBe(true);

      // Verify turn 1 completed with both tools executed
      await expect.poll(() => f.rows("SELECT CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? AND kind='assistant'", agent)
        .some(r => String(r.body).includes("Both tools completed successfully.")), { timeout: 20_000 }).toBe(true);

      // SCN-003: Verify no tool result in the multi-tool turn contains "interrupted by user steering message"
      const toolResults = f.rows("SELECT CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? AND kind IN ('toolResult','tool_result')", agent)
        .map(r => JSON.parse(String(r.body)));
      expect(toolResults.some(r => JSON.stringify(r).includes("interrupted by user steering message"))).toBe(false);

      // Verify turn 2 processes the subagent completion notice
      await expect.poll(() => f.rows("SELECT CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? AND kind='assistant'", agent)
        .some(r => String(r.body).includes("Acknowledged subagent completion on next turn.")), { timeout: 20_000 }).toBe(true);

      // Verify subagent message was acknowledged
      await expect.poll(() => f.rows("SELECT state FROM messages WHERE id=?", subagentMsgId)[0]?.state).toBe("acknowledged");

      // Verify agent returns to idle
      await expect.poll(() => f.rows("SELECT activity_state FROM managed_agents WHERE agent_id=?", agent)[0]?.activity_state).toBe("idle");
    } finally {
      await f.close();
    }
  });

  test("user steering message mid-turn interrupts pending tools and is addressed immediately", async ({ page }) => {
    const f = await workflowHarness();
    try {
      const agent = `agent-${randomUUID()}`;
      await f.api("/v1/managed-agents", { name: agent, cwd: join(f.router.root, "work"), runtime: "embedded", model });
      await expect.poll(() => f.rows("SELECT lifecycle_state FROM managed_agents WHERE agent_id=?", agent)[0]?.lifecycle_state).toBe("running");

      await f.open(page);
      const row = page.locator(`.agent-row[data-agent-id="${agent}"]`);
      await expect(row).toBeVisible();
      await row.locator(".agent-row-select").click();

      const step1Active = join(f.router.root, `step1_active_${randomUUID()}.txt`);
      const step1Finish = join(f.router.root, `step1_finish_${randomUUID()}.txt`);
      const step2Marker = join(f.router.root, `step2_marker_${randomUUID()}.txt`);

      const step1Cmd = `touch '${step1Active}' && while [ ! -f '${step1Finish}' ]; do sleep 0.02; done && echo step1_done`;
      const step2Cmd = `touch '${step2Marker}' && echo step2_done`;

      // Turn 1: model emits two tool calls
      f.replies.push({
        tools: [
          { tool: "bash", args: { command: step1Cmd, timeout: 10 } },
          { tool: "bash", args: { command: step2Cmd, timeout: 10 } },
        ],
      });
      // Model reply when steering is delivered mid-turn
      f.replies.push({ answer: "Halted pending tools and addressed user steering message." });

      // Start multi-tool turn
      const clock1 = await f.router.advanceClock(1000);
      await f.api("/v1/messages", {
        id: randomUUID(),
        from: "user",
        to: agent,
        body: "begin multi-tool turn with steering",
        created_at: new Date(clock1.now).toISOString(),
        source: { kind: "web_ui" },
      });

      // Wait for tool 1 to start executing
      await expect.poll(async () => {
        try { await stat(step1Active); return true; } catch { return false; }
      }, { timeout: 15_000 }).toBe(true);

      // While tool 1 is executing, send intentional user steering message
      const steerMsgId = randomUUID();
      const clock2 = await f.router.advanceClock(1000);
      await f.api("/v1/messages", {
        id: steerMsgId,
        from: "user",
        to: agent,
        body: "urgent steer directive: halt tools and respond",
        created_at: new Date(clock2.now).toISOString(),
        source: { kind: "web_ui" },
      });

      // Allow tool 1 to finish
      await writeFile(step1Finish, "ok");

      // Verify agent responds to the steering message
      await expect.poll(() => f.rows("SELECT CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? AND kind='assistant'", agent)
        .some(r => String(r.body).includes("Halted pending tools and addressed user steering message.")), { timeout: 20_000 }).toBe(true);

      // Verify tool 2 was NEVER executed
      let tool2Ran = false;
      try { await stat(step2Marker); tool2Ran = true; } catch { tool2Ran = false; }
      expect(tool2Ran).toBe(false);

      // Verify tool 2 result was marked as interrupted
      const toolResults = f.rows("SELECT CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? AND kind IN ('toolResult','tool_result')", agent)
        .map(r => JSON.parse(String(r.body)));
      expect(toolResults.some(r => JSON.stringify(r).includes("interrupted by user steering message"))).toBe(true);

      // Verify steering message was acknowledged
      await expect.poll(() => f.rows("SELECT state FROM messages WHERE id=?", steerMsgId)[0]?.state).toBe("acknowledged");

      // Verify agent settles to idle
      await expect.poll(() => f.rows("SELECT activity_state FROM managed_agents WHERE agent_id=?", agent)[0]?.activity_state).toBe("idle");
    } finally {
      await f.close();
    }
  });

  test("concurrent system notification and user steering prioritizes user steer and defers notice", async ({ page }) => {
    const f = await workflowHarness();
    try {
      const agent = `agent-${randomUUID()}`;
      await f.api("/v1/managed-agents", { name: agent, cwd: join(f.router.root, "work"), runtime: "embedded", model });
      await expect.poll(() => f.rows("SELECT lifecycle_state FROM managed_agents WHERE agent_id=?", agent)[0]?.lifecycle_state).toBe("running");

      await f.open(page);
      const row = page.locator(`.agent-row[data-agent-id="${agent}"]`);
      await expect(row).toBeVisible();
      await row.locator(".agent-row-select").click();

      const step1Active = join(f.router.root, `step1_active_${randomUUID()}.txt`);
      const step1Finish = join(f.router.root, `step1_finish_${randomUUID()}.txt`);
      const step2Marker = join(f.router.root, `step2_marker_${randomUUID()}.txt`);

      const step1Cmd = `touch '${step1Active}' && while [ ! -f '${step1Finish}' ]; do sleep 0.02; done && echo step1_done`;
      const step2Cmd = `touch '${step2Marker}' && echo step2_done`;

      // Turn 1: model emits two tool calls in the same turn
      f.replies.push({
        tools: [
          { tool: "bash", args: { command: step1Cmd, timeout: 10 } },
          { tool: "bash", args: { command: step2Cmd, timeout: 10 } },
        ],
      });
      // Model reply when steering is delivered mid-turn
      f.replies.push({ answer: "Halted pending tools and addressed user steering message." });
      // Turn 2 reply when deferred subagent completion notice is admitted
      f.replies.push({ answer: "Acknowledged deferred subagent notice on subsequent turn." });

      // Start multi-tool turn
      const clock1 = await f.router.advanceClock(1000);
      await f.api("/v1/messages", {
        id: randomUUID(),
        from: "user",
        to: agent,
        body: "begin multi-tool turn with concurrent notice and steer",
        created_at: new Date(clock1.now).toISOString(),
        source: { kind: "web_ui" },
      });

      // Wait for tool 1 to start executing
      await expect.poll(async () => {
        try { await stat(step1Active); return true; } catch { return false; }
      }, { timeout: 15_000 }).toBe(true);

      // Enqueue subagent-completed-* notice mid-turn
      const subagentMsgId = `subagent-completed-sub1-${randomUUID().slice(0, 8)}`;
      const clock2 = await f.router.advanceClock(1000);
      await f.api("/v1/messages", {
        id: subagentMsgId,
        from: "system",
        to: agent,
        body: "Subagent sub1 completed: task finished",
        created_at: new Date(clock2.now).toISOString(),
      });

      // Immediately enqueue user steering message (source_kind: 'web_ui')
      const steerMsgId = randomUUID();
      const clock3 = await f.router.advanceClock(1000);
      await f.api("/v1/messages", {
        id: steerMsgId,
        from: "user",
        to: agent,
        body: "urgent user steer directive: halt tools and respond",
        created_at: new Date(clock3.now).toISOString(),
        source: { kind: "web_ui" },
      });

      // Release tool 1 so execution reaches the steer check
      await writeFile(step1Finish, "ok");

      // Verify tool 2 was NEVER executed
      let tool2Ran = false;
      try { await stat(step2Marker); tool2Ran = true; } catch { tool2Ran = false; }
      expect(tool2Ran).toBe(false);

      // Verify tool 2 result was marked as interrupted by user steering message
      await expect.poll(() => {
        const toolResults = f.rows("SELECT CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? AND kind IN ('toolResult','tool_result')", agent)
          .map(r => JSON.parse(String(r.body)));
        return toolResults.some(r => JSON.stringify(r).includes("interrupted by user steering message"));
      }, { timeout: 15_000 }).toBe(true);

      // Verify agent responds to user steering message
      await expect.poll(() => f.rows("SELECT CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? AND kind='assistant'", agent)
        .some(r => String(r.body).includes("Halted pending tools and addressed user steering message.")), { timeout: 20_000 }).toBe(true);

      // Verify user steering message was acknowledged
      await expect.poll(() => f.rows("SELECT state FROM messages WHERE id=?", steerMsgId)[0]?.state).toBe("acknowledged");

      // Verify subagent notice was deferred and is admitted on the following turn without being dropped
      await expect.poll(() => f.rows("SELECT CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? AND kind='assistant'", agent)
        .some(r => String(r.body).includes("Acknowledged deferred subagent notice on subsequent turn.")), { timeout: 20_000 }).toBe(true);

      // Verify subagent completion message was acknowledged on the second turn
      await expect.poll(() => f.rows("SELECT state FROM messages WHERE id=?", subagentMsgId)[0]?.state).toBe("acknowledged");

      // Verify agent returns to idle
      await expect.poll(() => f.rows("SELECT activity_state FROM managed_agents WHERE agent_id=?", agent)[0]?.activity_state).toBe("idle");
    } finally {
      await f.close();
    }
  });
});
