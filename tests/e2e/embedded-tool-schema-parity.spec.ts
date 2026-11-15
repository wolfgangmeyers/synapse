import { test, expect } from "@playwright/test";
import { randomUUID } from "node:crypto";
import { writeFile, mkdir } from "node:fs/promises";
import { join } from "node:path";
import { workflowHarness, model, type Reply } from "./workflow-implicit-goals-harness";

// Provider multi-call responses are not the external multi_tool_use.parallel wrapper.
test("direct and five provider reads honor advertised pagination", async ({ page }) => {
  test.setTimeout(180_000);
  const f = await workflowHarness();
  const agent = "schema-reader";
  const dir = join(f.router.root, "work");
  try {
    await mkdir(dir, { recursive: true });
    const paths = Array.from({ length: 5 }, (_, i) => join(dir, `read-${i}.txt`));
    for (const [i, path] of paths.entries()) await writeFile(path, `first-${i}\nsecond-${i}\nthird-${i}\n`);
    await f.api("/v1/managed-agents", { name: agent, cwd: dir, runtime: "embedded", model });
    await expect.poll(() => f.rows("SELECT lifecycle_state FROM managed_agents WHERE agent_id=?", agent)[0]?.lifecycle_state, { timeout: 30_000 }).toBe("running");
    await f.open(page);
    await page.locator(`.agent-row[data-agent-id="${agent}"] .agent-row-select`).click();
    for (const multi of [false, true]) {
      for (const [variant, pagination] of [["omitted", {}], ["null", { offset: null, limit: null }], ["integer", { offset: 2, limit: 1 }]] as const) {
        const calls = (multi ? paths : paths.slice(0, 1)).map(path => ({ tool: "read", args: { path, ...pagination } }));
        const start = f.requests.length;
        f.replies.push((multi ? { tools: calls } : calls[0]) as Reply, { answer: `done ${multi} ${variant}` });
        const initial = await f.router.advanceClock(1);
        const clock = await f.router.advanceClock(Math.max(1, Date.now() + 2000 - Date.parse(initial.now)));
        await f.api("/v1/messages", { id: randomUUID(), from: "user", to: agent, body: `read ${multi} ${variant}`, created_at: new Date(clock.now).toISOString(), source: { kind: "web_ui" } });
        await expect.poll(() => f.requests.length, { timeout: 20000 }).toBe(start + 2);
        // The provider request can arrive before this turn is persisted and settled.
        await expect.poll(() => f.rows("SELECT CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? AND kind='assistant'", agent)
          .some(row => String(row.body).includes(`done ${multi} ${variant}`)), { timeout: 20_000 }).toBe(true);
        await expect.poll(() => f.rows("SELECT activity_state FROM managed_agents WHERE agent_id=?", agent)[0]?.activity_state, { timeout: 20_000 }).toBe("idle");
        const request = f.requests[start];
        const read = (request.tools as any[]).find(t => t.name === "read");
        expect(read).toBeDefined();
        for (const field of ["offset", "limit"]) {
          expect(read.parameters.properties[field].type).toEqual(expect.arrayContaining(["integer", "null"]));
          expect(read.parameters.properties[field].type).toHaveLength(2);
        }
        const input = f.requests[start + 1].input as any[];
        // The provider input includes earlier turns; match only this response’s calls.
        const outputs = input.filter(item => item.type === "function_call_output" &&
          item.call_id?.startsWith(`call_response_${start + 1}_`));
        expect(outputs).toHaveLength(calls.length);
        for (const [i, output] of outputs.entries()) {
          const expected = variant === "integer"
            ? `second-${i}\n\n[2 more lines in file. Use offset=3 to continue.]`
            : `first-${i}\nsecond-${i}\nthird-${i}\n`;
          expect(output.output, `${variant} call ${i}`).toBe(expected);
        }
      }
    }
    expect(f.wsFrames.length).toBeGreaterThan(0);
    expect(f.violations).toEqual([]);
  } catch (error) {
    console.error("fixture violations", f.violations, "entries", f.rows("SELECT kind, CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? ORDER BY seq", agent));
    throw error;
  } finally { await f.close(); }
});
