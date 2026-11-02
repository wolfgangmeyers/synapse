import { test, expect } from "@playwright/test";
import { randomUUID } from "node:crypto";
import { join } from "node:path";
import { workflowHarness, model, lastToolOutput, type Reply } from "./workflow-implicit-goals-harness";
import { createOperationalToolDefinitions } from "../../pi-extensions/mecha-messaging/operational-tools";

// RNT-006: external provider only is mocked. Browser/WS, proxy, SDK and SQLite are real.
test("rejects stale launches and preserves worker operations and admin spawn", async ({ page }) => {
  test.setTimeout(120_000);
  const f = await workflowHarness();
  const caller = "worker-tool-caller", child = "admin-spawned-child";
  try {
    await f.api("/v1/managed-agents", { name: caller, cwd: join(f.router.root, "work"), runtime: "embedded", model });
    await expect.poll(() => f.rows("SELECT lifecycle_state FROM managed_agents WHERE agent_id=?", caller)[0]?.lifecycle_state).toBe("running");
    await f.open(page);
    const row = page.locator(`.agent-row[data-agent-id="${caller}"]`);
    await expect(row).toBeVisible();
    await row.locator(".agent-row-select").click();
    async function turn(reply: Reply, final: string, childTurn = false) {
      const start = f.requests.length;
      f.replies.push(reply, { answer: final });
      if (childTurn) f.replies.push({ answer: final });
      const initial = await f.router.advanceClock(1);
      const clock = await f.router.advanceClock(Math.max(1, Date.now() + 2000 - Date.parse(initial.now)));
      await f.api("/v1/messages", { id: randomUUID(), from: "user", to: caller, body: "Execute the fixture call", created_at: new Date(clock.now).toISOString(), source: { kind: "web_ui" } });
      await expect.poll(() => f.requests.length).toBe(start + (childTurn ? 3 : 2));
      await expect(page.getByText(final, { exact: true })).toBeVisible();
      return f.requests[start];
    }
    const operationsBefore = f.rows("SELECT request_id FROM managed_http_operations WHERE ? IS NOT NULL ORDER BY request_id", caller);
    const stale = { action: "launch", name: "forbidden-child", cwd: join(f.router.root, "work"), instruction: "must not start" };
    const request = await turn({ tool: "mecha_worker", args: stale }, "Rejected stale launch");
    const definitions = request.tools as { name: string; parameters: { properties: Record<string, { enum?: string[] }> } }[];
    const worker = definitions.find(t => t.name === "mecha_worker")!;
    expect(worker.parameters.properties.action.enum).toEqual(["list", "send", "cleanup"]);
    expect(Object.keys(worker.parameters.properties).sort()).toEqual(["action", "body", "name", "subject"]);
    expect(definitions.some(t => t.name === "worker_spawn")).toBe(false);
    expect(lastToolOutput(f)).toMatch(/Validation failed|invalid worker action/);
    await turn({ tool: "mecha_worker", args: stale }, "Rejected stale launch again");
    expect(f.rows("SELECT agent_id FROM managed_agents WHERE agent_id=?", "forbidden-child")).toEqual([]);
    expect(f.rows("SELECT effect_id FROM embedded_effects WHERE agent_id=?", caller)).toEqual([]);
    expect(f.rows("SELECT request_id FROM managed_http_operations WHERE ? IS NOT NULL ORDER BY request_id", caller)).toEqual(operationsBefore);
    await turn({ tool: "mecha_worker", args: { action: "list" } }, "Worker list preserved");
    expect(JSON.parse(lastToolOutput(f))).toHaveProperty("workers");

    // Standalone Pi definition and handler also reject old calls before adapters.
    let calls = 0;
    const piWorker = createOperationalToolDefinitions({ runFile: async () => { calls++; throw Error("unexpected subprocess"); } }).find(t => t.name === "mecha_worker")!;
    expect(JSON.stringify(piWorker.parameters)).not.toContain('"launch"');
    expect(JSON.stringify(await piWorker.execute("stale", { action: "launch", alias: "forbidden-child" }))).toContain("unsupported action");
    expect(calls).toBe(0);

    // Enable only the existing admin capability through its real HTTP permission API.
    await f.api(`/v1/managed-agents/${caller}/tools`, { tool_name: "worker_spawn", enabled: true }, "PUT");
    const spawnRequest = await turn({ tool: "worker_spawn", args: { name: child, cwd: join(f.router.root, "work"), instruction: "Run deterministic child turn", model } }, "Admin spawn preserved", true);
    expect((spawnRequest.tools as { name: string }[]).some(t => t.name === "worker_spawn")).toBe(true);
    const outputs = f.requests.slice(-2).flatMap(r => Array.isArray(r.input) ? r.input : []).filter(i => i.type === "function_call_output");
    expect(outputs.some(i => { try { return JSON.parse(i.output).worker_id === child; } catch { return false; } })).toBe(true);
    await expect.poll(() => f.rows("SELECT lifecycle_state FROM managed_agents WHERE agent_id=?", child)[0]?.lifecycle_state).toBe("running");
    expect(f.wsFrames.length).toBeGreaterThan(0);
    expect(f.violations).toEqual([]);
  } finally { await f.close(); }
});
