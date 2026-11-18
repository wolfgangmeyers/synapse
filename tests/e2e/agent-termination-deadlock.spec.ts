import { randomUUID } from "node:crypto";
import { test, expect } from "@playwright/test";
import { claudeWorkersHarness } from "./claude-workers-harness";

const call = (id: string, name: string, input: object) => ({ mcp_call: { id, name, input } });
const text = (value: string) => ({ text: value });

test.describe("Agent Termination Deadlock Resilience (SPEC-AGENT-TERMINATION-DEADLOCK-001)", () => {
  test.setTimeout(120_000);

  test("SCN-TERM-001: router APIs and health check stay responsive during agent termination", async ({ page }) => {
    // SCN-TERM-001:
    // A managed agent is launched.
    // While initiating termination (DELETE /v1/managed-agents/:id), concurrent requests
    // to /v1/health and /v1/managed-agents must respond promptly without wedging or deadlocking.
    const f = await claudeWorkersHarness({
      turns: [[text("Agent ready for termination test")]],
    });

    try {
      await f.open(page);
      const agent = await f.launch(page);
      await f.userTurn(page, "Initialize agent");
      await f.final(page, agent, "Agent ready for termination test");

      // Verify healthy baseline
      const initialHealth = await fetch(`${f.router.httpUrl}/v1/health`, { signal: AbortSignal.timeout(5000) });
      expect(initialHealth.status).toBe(200);
      const initialHealthBody = await initialHealth.json();
      expect(initialHealthBody.status).toBe("ok");

      // Initiate termination via DELETE
      const deletePromise = fetch(`${f.router.httpUrl}/v1/managed-agents/${agent}`, {
        method: "DELETE",
        headers: { "X-Request-ID": randomUUID() },
        signal: AbortSignal.timeout(15000),
      });

      // Concurrently poll /v1/health and /v1/managed-agents while DELETE is executing / processing
      const concurrentHealthPromise = fetch(`${f.router.httpUrl}/v1/health`, {
        signal: AbortSignal.timeout(5000),
      });

      const concurrentListPromise = fetch(`${f.router.httpUrl}/v1/managed-agents`, {
        signal: AbortSignal.timeout(5000),
      });

      const [deleteRes, healthRes, listRes] = await Promise.all([
        deletePromise,
        concurrentHealthPromise,
        concurrentListPromise,
      ]);

      expect(deleteRes.status).toBe(202);
      expect(healthRes.status).toBe(200);
      const healthBody = await healthRes.json();
      expect(healthBody.status).toBe("ok");

      expect(listRes.status).toBe(200);
      const listBody = await listRes.json();
      expect(Array.isArray(listBody.agents)).toBe(true);

      // Verify agent reaches terminated/removed state
      await expect.poll(() => f.tableRows("managed_agents", agent).length, { timeout: 15000 }).toBe(0);

      // Verify subsequent router health and managed-agents requests continue to respond immediately
      const postTermHealth = await fetch(`${f.router.httpUrl}/v1/health`, { signal: AbortSignal.timeout(5000) });
      expect(postTermHealth.status).toBe(200);
      expect((await postTermHealth.json()).status).toBe("ok");

      const postTermList = await fetch(`${f.router.httpUrl}/v1/managed-agents`, { signal: AbortSignal.timeout(5000) });
      expect(postTermList.status).toBe(200);
    } finally {
      await f.close();
    }
  });

  test("SCN-TERM-002: worker self-termination does not wedge router APIs or web UI polling", async ({ page }) => {
    // SCN-TERM-002:
    // A managed worker agent invokes the `mecha_worker_terminate` tool.
    // The tool execution schedules self-termination.
    // Router endpoints (/v1/health, /v1/managed-agents) and Web UI polling remain responsive.
    const args = { reason: "Self-terminating worker task finished", cleanup_worktree: false };
    const f = await claudeWorkersHarness({
      continue_after_mcp_error: true,
      executions_by_input: [
        {
          contains: "Execute self-termination",
          turns: [[
            call("call-term-1", "mecha_worker_terminate", args),
            text("Worker scheduled self termination"),
          ]],
        },
      ],
    });

    try {
      await f.open(page);
      const agent = await f.launch(page);

      // Start Web UI / API polling in parallel
      let pollingErrors = 0;
      let stopPolling = false;
      const pollingPromise = (async () => {
        while (!stopPolling) {
          try {
            const h = await fetch(`${f.router.httpUrl}/v1/health`, { signal: AbortSignal.timeout(2000) });
            if (h.status !== 200) pollingErrors++;
            const l = await fetch(`${f.router.httpUrl}/v1/managed-agents`, { signal: AbortSignal.timeout(2000) });
            if (l.status !== 200) pollingErrors++;
          } catch {
            pollingErrors++;
          }
          await new Promise((r) => setTimeout(r, 100));
        }
      })();

      // Trigger the turn that invokes mecha_worker_terminate
      await f.userTurn(page, "Execute self-termination");

      // Verify agent is cleanly terminated and removed from active managed_agents
      await expect.poll(() => f.tableRows("managed_agents", agent).length, { timeout: 20000 }).toBe(0);

      // Stop background polling and verify zero timeouts/errors occurred
      stopPolling = true;
      await pollingPromise;
      expect(pollingErrors).toBe(0);

      // Verify the agent archive recorded "terminated"
      const archives = f.tableRows("managed_agent_archives", agent);
      expect(archives.length).toBeGreaterThan(0);
      expect(archives[0].terminal_outcome).toBe("terminated");

      // Health and API check still responding immediately
      const healthRes = await fetch(`${f.router.httpUrl}/v1/health`, { signal: AbortSignal.timeout(5000) });
      expect(healthRes.status).toBe(200);
      expect((await healthRes.json()).status).toBe("ok");
    } finally {
      await f.close();
    }
  });

  test("SCN-TERM-003: subagent cascade cancellation does not wedge router transport", async ({ page }) => {
    // SCN-TERM-003:
    // A parent agent spawns an active subagent (held in running state).
    // Terminating the parent agent cascades cancellation to active subagents.
    // The router transport, /v1/health, and /v1/managed-agents must not hang or deadlock.
    const spawnInput = { prompt: "Long-running subagent work", description: "Active child task" };
    const f = await claudeWorkersHarness({
      continue_after_mcp_error: true,
      turns: [[
        call("spawn-child-1", "subagent", spawnInput),
        text("Subagent spawn dispatched"),
      ]],
    });

    try {
      // Hold the subagent's provider reply so it remains active/running
      f.replies.push({ hold: true });

      await f.open(page);
      const parentAgent = await f.launch(page);
      await f.userTurn(page, "Start background subagent");
      await f.final(page, parentAgent, "Subagent spawn dispatched");

      // Verify subagent is running
      await expect.poll(() => f.rows("SELECT status FROM managed_subagents WHERE parent_agent_id=?", parentAgent), { timeout: 20000 })
        .toEqual([{ status: "running" }]);

      // Now terminate the parent agent via DELETE
      const deletePromise = fetch(`${f.router.httpUrl}/v1/managed-agents/${parentAgent}`, {
        method: "DELETE",
        headers: { "X-Request-ID": randomUUID() },
        signal: AbortSignal.timeout(15000),
      });

      // Concurrently verify health check responds without blocking
      const healthRes = await fetch(`${f.router.httpUrl}/v1/health`, { signal: AbortSignal.timeout(5000) });
      expect(healthRes.status).toBe(200);
      expect((await healthRes.json()).status).toBe("ok");

      const deleteRes = await deletePromise;
      expect(deleteRes.status).toBe(202);

      // Verify subagent is no longer actively running (either marked stopped or pruned with parent termination)
      await expect.poll(() => {
        const rows = f.rows("SELECT status FROM managed_subagents WHERE parent_agent_id=?", parentAgent);
        if (rows.length === 0) return "pruned";
        return rows[0].status;
      }, { timeout: 20000 }).toMatch(/^(stopped|completed|pruned)$/);

      // Verify parent is removed from active managed_agents
      await expect.poll(() => f.tableRows("managed_agents", parentAgent).length, { timeout: 20000 }).toBe(0);

      // Router remains fully responsive
      const finalHealth = await fetch(`${f.router.httpUrl}/v1/health`, { signal: AbortSignal.timeout(5000) });
      expect(finalHealth.status).toBe(200);
      expect((await finalHealth.json()).status).toBe("ok");

      const finalList = await fetch(`${f.router.httpUrl}/v1/managed-agents`, { signal: AbortSignal.timeout(5000) });
      expect(finalList.status).toBe(200);
    } finally {
      await f.close();
    }
  });
});
