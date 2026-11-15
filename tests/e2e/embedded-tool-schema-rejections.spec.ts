import { test, expect } from "@playwright/test";
import { randomUUID } from "node:crypto";
import { mkdir, writeFile, stat } from "node:fs/promises";
import { join } from "node:path";
import { workflowHarness, model } from "./workflow-implicit-goals-harness";

// Unknown/filtered tools can be rejected by Codex before host admission.
for (const scenario of ["fractional-offset", "fractional-limit", "wrong-type", "missing-path", "non-read-wrong-type", "non-read-missing", "unknown", "canonical-unknown", "hostile-name", "filtered"] as const) {
  test(`provider rejection: ${scenario}`, async ({ page }) => {
    test.setTimeout(120_000);
    const f = await workflowHarness();
    const agent = `schema-${scenario}`;
    const dir = join(f.router.root, "work");
    const path = join(dir, "private.txt");
    const secret = "fixture-argument-secret-8574";
    const sentinel = join(dir, "rejected-command-ran");
    try {
      await mkdir(dir, { recursive: true });
      await writeFile(path, "fixture-file-secret-8574\n");
      await f.api("/v1/managed-agents", { name: agent, cwd: dir, runtime: "embedded", model });
      await expect.poll(() => f.rows("SELECT lifecycle_state FROM managed_agents WHERE agent_id=?", agent)[0]?.lifecycle_state, { timeout: 30_000 }).toBe("running");
      await f.open(page);
      await page.locator(`.agent-row[data-agent-id="${agent}"] .agent-row-select`).click();
      if (scenario === "filtered") await f.api(`/v1/managed-agents/${agent}/tools`, { tool_name: "scratchpad", enabled: false }, "PUT");
      const tool = scenario === "canonical-unknown" ? "mecha_worker.launch" : scenario === "unknown" ? "nonexistent_issue45_tool" : scenario === "hostile-name" ? `bad\n${secret}${"x".repeat(300)}` : scenario === "filtered" ? "scratchpad" : scenario.startsWith("non-read-") ? "bash" : "read";
      const args = scenario === "fractional-offset" ? { path, offset: 1.5 } : scenario === "fractional-limit" ? { path, limit: 1.5 } : scenario === "wrong-type" ? { path, offset: secret } : scenario === "missing-path" ? { offset: 1 } : scenario === "non-read-wrong-type" ? { command: [`touch ${sentinel}`] } : {};
      f.replies.push({ tool, args }, { answer: "rejection handled" });
      const initial = await f.router.advanceClock(1);
      const clock = await f.router.advanceClock(Math.max(1, Date.now() + 2000 - Date.parse(initial.now)));
      await f.api("/v1/messages", { id: randomUUID(), from: "user", to: agent, body: "Exercise rejected call", created_at: new Date(clock.now).toISOString(), source: { kind: "web_ui" } });
      await expect.poll(() => f.requests.length, { timeout: 20000 }).toBeGreaterThanOrEqual(1);
      const entries = () => f.rows("SELECT kind, CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? ORDER BY seq", agent);
      const callId = "call_response_1_0";
      // The SDK transcript retains both the provider call ID and item ID;
      // function_call_output uses only the provider call ID on the wire.
      const transcriptCallId = `${callId}|fc_response_1_0`;
      const results = () => entries().filter(e => e.kind === "toolResult")
        .map(e => JSON.parse(String(e.body)))
        .filter(body => body.toolCallId === transcriptCallId);
      await expect.poll(() => results().length, { timeout: 20000, message: "correlated tool result must be persisted" }).toBe(1);
      const result = results()[0];
      expect(result.isError).toBe(true);
      const diagnostics = result.content?.filter((c: any) => c.type === "text").map((c: any) => c.text).join("\n") ?? "";
      await expect.poll(() => f.requests.length, { timeout: 20000, message: "provider must receive the tool result" }).toBeGreaterThanOrEqual(2);
      const outputs = (f.requests[1].input as any[]).filter(item => item.type === "function_call_output" && item.call_id === callId);
      expect(outputs).toHaveLength(1);
      expect(String(outputs[0].output)).toContain(diagnostics);
      expect(diagnostics).not.toEqual("");
      expect(diagnostics).not.toContain(secret);
      expect(diagnostics).not.toContain("fixture-file-secret-8574");
      if (scenario === "hostile-name") {
        // Inspect the new diagnostic, not the provider's persisted input/history.
        expect(diagnostics.length).toBeLessThanOrEqual(256);
        expect(diagnostics).not.toMatch(/[\u0000-\u001f\u007f-\u009f]/);
        expect(diagnostics).not.toContain("x".repeat(65));
      }
      expect(f.rows("SELECT effect_id FROM embedded_effects WHERE agent_id=?", agent)).toEqual([]);
      if (scenario === "non-read-wrong-type") {
        await expect(stat(sentinel)).rejects.toMatchObject({ code: "ENOENT" });
      }
      const advertised = (f.requests[0].tools as { name: string }[]).map(t => t.name);
      if (scenario === "filtered") expect(advertised).not.toContain("scratchpad");
      if (scenario === "canonical-unknown") {
        expect(advertised).not.toContain("mecha_worker.launch");
        expect(advertised).not.toContain("mecha_worker_launch");
        await expect.poll(() => entries().some(e => String(e.body).includes("rejection handled")), { timeout: 20000 }).toBe(true);
      }
      if (scenario === "fractional-offset" || scenario === "fractional-limit") {
        const read = (f.requests[0].tools as any[]).find(t => t.name === "read");
        for (const field of ["offset", "limit"]) {
          expect(read.parameters.properties[field].type).toEqual(expect.arrayContaining(["integer", "null"]));
          expect(read.parameters.properties[field].type).toHaveLength(2);
        }
        expect(diagnostics).toMatch(/integer|invalid|schema/i);
        expect(diagnostics).not.toContain("fixture-file-secret-8574");
      }
      if (scenario === "wrong-type" || scenario === "missing-path") {
        expect(advertised).toContain("read");
        expect(diagnostics).toMatch(/read/i);
        expect(diagnostics).toMatch(scenario === "wrong-type" ? /offset|integer|number/i : /path|required|missing/i);
      }
      if (scenario.startsWith("non-read-")) {
        expect(advertised).toContain("bash");
        expect(diagnostics).toMatch(/bash/i);
        expect(diagnostics).toMatch(/command/i);
        expect(diagnostics).toMatch(scenario === "non-read-missing" ? /missing|required/i : /string/i);
      }
      if (scenario === "unknown" || scenario === "canonical-unknown" || scenario === "hostile-name" || scenario === "filtered") {
        expect(diagnostics).toMatch(/unavailable|unknown|not found/i);
      }
      expect(f.violations).toEqual([]);
    } finally { await f.close(); }
  });
}
