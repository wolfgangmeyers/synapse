import { test, expect } from "@playwright/test";
import { writeFile } from "node:fs/promises";
import { join } from "node:path";
import { claudeWorkersHarness } from "./claude-workers-harness";

test.skip(process.platform !== "linux", "Background bash launch is supported only on Linux");

// BB-010: admission and release at the documented manager-wide running limit.
test("running job capacity rejects another launch and is released after kill", async ({ page }) => {
  test.setTimeout(180_000);
  const f = await claudeWorkersHarness();
  const release = join(f.fixtureRoot, "capacity-release");
  const quote = (s: string) => `'${s.replaceAll("'", "'\\''")}'`;
  const call = (id: string, name: string, input: object) => ({ mcp_call: { id, name, input, require_catalog: true } });
  const results = async (name: string) => (await f.records())
    .filter(r => r.kind === "mcp_call_response" && r.name === name)
    .map(r => ({ ...(r.result as { isError?: boolean; content: { text: string }[] }), toolCallId: r.tool_id, rpcError: r.error }));
  const command = `while [ ! -f ${quote(release)} ]; do sleep 0.1; done`;
  try {
    await f.configure({ turns_by_input: [{ contains: "Fill capacity", steps: [
      ...Array.from({ length: 32 }, (_, i) => call(`capacity-${i}`, "launch_background_bash", { command })),
      { text: "Capacity filled" },
    ] }] });
    await f.open(page);
    const first = await f.launch(page);
    await f.userTurn(page, "Fill capacity");
    // Each MCP response is progress; do not use the harness's default
    // five-second final-answer wait while 32 real processes are starting.
    for (let i = 0; i < 32; i++) {
      await expect.poll(async () => (await f.records()).some(r => r.kind === "mcp_call_response" && r.tool_id === `capacity-${i}`),
        { timeout: 15_000, message: `launch ${i} responded` }).toBe(true);
    }
    await expect(page.getByText("Capacity filled", { exact: true }).first()).toBeVisible({ timeout: 30_000 });
    await f.final(page, first, "Capacity filled");
    const launches = await results("launch_background_bash");
    expect(launches).toHaveLength(32);
    for (let i = 0; i < 32; i++) {
      const matching = launches.filter(r => r.toolCallId === `capacity-${i}`);
      expect(matching).toHaveLength(1);
      expect(matching[0].rpcError).toBeNull();
      expect(matching[0].isError, JSON.stringify(matching[0])).toBeFalsy();
    }
    const ids = launches.map(launch => JSON.parse(launch.content[0].text).job_id);
    expect(ids.every(id => typeof id === "string" && id.length > 0)).toBe(true);
    expect(new Set(ids).size).toBe(32);

    const second = await f.launch(page);
    expect(second).not.toBe(first);
    await f.configure({ continue_after_mcp_error: true, turns_by_input: [{ contains: "Check shared capacity", steps: [
      call("capacity-other-denied", "launch_background_bash", { command }),
      { text: "Shared capacity checked" },
    ] }] });
    await f.userTurn(page, "Check shared capacity");
    await expect.poll(async () => (await results("launch_background_bash")).some(r => r.toolCallId === "capacity-other-denied"),
      { timeout: 30_000 }).toBe(true);
    await expect(page.getByText("Shared capacity checked", { exact: true }).first()).toBeVisible({ timeout: 30_000 });
    await f.final(page, second, "Shared capacity checked");
    const denied = (await results("launch_background_bash")).filter(r => r.toolCallId === "capacity-other-denied");
    expect(denied).toHaveLength(1);
    expect(denied[0].rpcError).toBeNull();
    expect(denied[0].isError).toBe(true);
    expect(JSON.stringify(denied[0].content)).toContain("background bash capacity reached");

    await page.locator(`[data-agent-id="${first}"]`).click();
    await f.configure({ turns_by_input: [{ contains: "Release capacity", steps: [
      call("capacity-kill", "kill_background_bash_job", { job_id: ids[0] }),
      { text: "Capacity released" },
    ] }] });
    await f.userTurn(page, "Release capacity");
    await expect.poll(async () => (await results("kill_background_bash_job")).some(r => r.toolCallId === "capacity-kill"),
      { timeout: 30_000 }).toBe(true);
    await expect(page.getByText("Capacity released", { exact: true }).first()).toBeVisible({ timeout: 30_000 });
    await f.final(page, first, "Capacity released");
    const kills = (await results("kill_background_bash_job")).filter(r => r.toolCallId === "capacity-kill");
    expect(kills).toHaveLength(1);
    expect(kills[0].rpcError).toBeNull();
    expect(kills[0].isError, JSON.stringify(kills[0])).toBeFalsy();
    expect(JSON.parse(kills[0].content[0].text)).toMatchObject({ job_id: ids[0], status: "killed" });

    await page.locator(`[data-agent-id="${second}"]`).click();
    await f.configure({ turns_by_input: [{ contains: "Replace capacity", steps: [
      call("capacity-relaunch", "launch_background_bash", { command }),
      { text: "Replacement launched" },
    ] }] });
    await f.userTurn(page, "Replace capacity");
    await expect.poll(async () => (await results("launch_background_bash")).some(r => r.toolCallId === "capacity-relaunch"),
      { timeout: 30_000 }).toBe(true);
    await expect(page.getByText("Replacement launched", { exact: true }).first()).toBeVisible({ timeout: 30_000 });
    await f.final(page, second, "Replacement launched");
    const replacements = (await results("launch_background_bash")).filter(r => r.toolCallId === "capacity-relaunch");
    expect(replacements).toHaveLength(1);
    const replacement = replacements[0];
    expect(replacement.rpcError).toBeNull();
    expect(replacement.isError, JSON.stringify(replacement)).toBeFalsy();
    const replacementId = JSON.parse(replacement.content[0].text).job_id;
    expect(typeof replacementId).toBe("string");
    expect(replacementId.length).toBeGreaterThan(0);
    expect(ids).not.toContain(replacementId);
    expect(f.violations).toEqual([]);
  } finally {
    await writeFile(release, "release").catch(() => {});
    await f.close();
  }
});
