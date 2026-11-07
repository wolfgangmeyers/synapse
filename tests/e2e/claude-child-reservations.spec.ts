import { readFile } from "node:fs/promises";
import { test, expect } from "@playwright/test";
import { claudeWorkersHarness } from "./claude-workers-harness";

const call = (id: string, input: object) => ({ mcp_call: { id, name: "subagent", input, require_catalog: true } });

test("SCN-RES-002 per-parent saturation queues and wakes after a child settles", async ({ page }, testInfo) => {
  test.setTimeout(150_000);
  const spawns = Array.from({ length: 11 }, (_, i) => call(`spawn-${i}`, {
    prompt: `Reservation work [${i}]`, description: `Reservation child ${i}`,
  }));
  const f = await claudeWorkersHarness({ executions_by_input: [
    { contains: "Start reservation children", turns: [[...spawns, { text: "Children submitted" }]] },
    ...Array.from({ length: 10 }, (_, i) => ({ contains: `Reservation work [${i}]`,
      turns: [[{ barrier: `child-${i}` }, { text: `Finished child ${i}` }]] })),
    { contains: "Reservation work [10]", turns: [[{ text: "Queued child finished" }]] },
    { contains: "finished with status", turns: [[{ text: "Child completion received" }]] },
  ] }, { defaultModel: null });
  try {
    await f.open(page);
    expect((await f.request(page, "/v1/settings/default-model", { model: { provider: "claude-code", id: "haiku" } }, "PUT")).status).toBe(200);
    const parent = await f.launch(page);
    await f.userTurn(page, "Start reservation children");
    const children = () => f.rows("SELECT agent_id,description,status,turn_count FROM managed_subagents WHERE parent_agent_id=? ORDER BY description", parent);
    await expect.poll(() => children().length, { timeout: 60_000 }).toBe(11);
    await expect.poll(() => children().filter(row => row.status === "running").length, { timeout: 60_000 }).toBe(10);
    const queued = children().find(row => row.description === "Reservation child 10");
    expect(queued).toMatchObject({ status: "queued", turn_count: 0 });
    expect((await f.inputs()).some(input => JSON.stringify(input).includes("Reservation work [10]"))).toBe(false);
    await expect.poll(async () => (await f.records()).some(row => row.kind === "barrier" && row.name === "child-0"), { timeout: 30_000 }).toBe(true);
    await f.releaseBarrier((await f.records()).find(row => row.kind === "barrier" && row.name === "child-0")!);
    await expect.poll(() => children().find(row => row.agent_id === queued!.agent_id)?.status, { timeout: 45_000 }).toBe("completed");
    expect((await f.inputs()).filter(input => JSON.stringify(input).includes("Reservation work [10]"))).toHaveLength(1);
    expect(f.violations).toEqual([]);
  } finally {
    for (const [name, path] of [["router.jsonl", f.router.logPath], ["fixture.jsonl", `${f.fixtureRoot}/record.jsonl`]] as const) {
      try { await testInfo.attach(name, { body: await readFile(path), contentType: "application/json" }); } catch (error) {
        if (error.code !== "ENOENT") throw error;
      }
    }
    await testInfo.attach("router-stderr", { body: f.router.stderr(), contentType: "text/plain" });
    for (const barrier of (await f.records()).filter(row => row.kind === "barrier" && typeof row.name === "string" && row.name.startsWith("child-"))) {
      try { await f.releaseBarrier(barrier); } catch { /* Already released or process exited. */ }
    }
    await f.close();
  }
});
