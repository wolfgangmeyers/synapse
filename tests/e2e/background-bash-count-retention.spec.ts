import { test, expect } from "@playwright/test";
import { writeFile } from "node:fs/promises";
import { join } from "node:path";
import { claudeWorkersHarness } from "./claude-workers-harness";

test.skip(process.platform !== "linux", "Background bash launch is supported only on Linux");

// BB-010: the test-only Go overlay reduces the manager-wide record limit to 3.
test("count retention evicts the oldest terminal job and its output, not a running job", async ({ page }) => {
  test.setTimeout(120_000);
  const f = await claudeWorkersHarness({}, { backgroundCountRetentionFixture: true });
  const release = join(f.fixtureRoot, "retention-release");
  const quote = (s: string) => `'${s.replaceAll("'", "'\\''")}'`;
  const ids: string[] = [];
  try {
    await f.open(page);
    const agent = await f.launch(page);
    const launch = async (label: string, command: string) => {
      await f.configure({ turns_by_input: [{ contains: `Launch ${label}`, steps: [
        { mcp_call: { id: label, name: "launch_background_bash", input: { command }, require_catalog: true } },
        { text: `Launched ${label}` },
      ] }] });
      await f.userTurn(page, `Launch ${label}`);
      await f.final(page, agent, `Launched ${label}`);
      const response = (await f.records()).filter(r => r.kind === "mcp_call_response" && r.tool_id === label);
      expect(response).toHaveLength(1);
      expect(response[0].error).toBeNull();
      const result = response[0].result as { isError?: boolean; content: { text: string }[] };
      expect(result.isError, JSON.stringify(result)).toBeFalsy();
      const id = JSON.parse(result.content[0].text).job_id;
      expect(typeof id).toBe("string");
      ids.push(id);
      return id as string;
    };
    const jobs = async () => {
      const response = await f.request(page, `/v1/managed-agents/${encodeURIComponent(agent)}`, undefined, "GET");
      expect(response.status).toBe(200);
      return response.body.agent?.background_bash_jobs ?? [];
    };
    const waitStatus = async (id: string, status: string) => {
      await expect.poll(async () => (await jobs()).find((j: { id: string }) => j.id === id)?.status,
        { timeout: 15_000 }).toBe(status);
    };
    const running = await launch("running", `n=0; while [ ! -f ${quote(release)} ]; do [ "$n" -lt 1800 ] || exit 1; n=$((n + 1)); sleep 0.1; done`);
    await waitStatus(running, "running");
    const oldest = await launch("oldest", "printf oldest-output");
    await waitStatus(oldest, "succeeded");
    const newer = await launch("newer", "printf newer-output");
    await waitStatus(newer, "succeeded");
    expect((await jobs()).map((j: { id: string }) => j.id).sort()).toEqual([...ids].sort());
    let inspection = 0;
    const get = async (id: string) => {
      const label = `inspect-${++inspection}`;
      await f.configure({ turns_by_input: [{ contains: label, steps: [
        { mcp_call: { id: label, name: "get_background_bash_job", input: { job_id: id }, require_catalog: true } },
        { text: `Inspected ${label}` },
      ] }] });
      await f.userTurn(page, label);
      await f.final(page, agent, `Inspected ${label}`);
      const responses = (await f.records()).filter(r => r.kind === "mcp_call_response" && r.tool_id === label);
      expect(responses).toHaveLength(1);
      expect(responses[0].error).toBeNull();
      return responses[0].result as { isError?: boolean; content: { text: string }[] };
    };
    expect(JSON.parse((await get(oldest)).content[0].text)).toMatchObject({ job_id: oldest, output: "oldest-output", status: "succeeded" });
    const newest = await launch("newest", "printf newest-output");
    await waitStatus(newest, "succeeded");
    expect(new Set(ids).size).toBe(4);
    await expect.poll(async () => (await jobs()).map((j: { id: string }) => j.id).sort()).toEqual([running, newer, newest].sort());
    const missing = await get(oldest);
    expect(missing.isError).toBe(true);
    expect(JSON.stringify(missing.content)).toContain("background bash job not found");
    expect(JSON.stringify(missing.content)).not.toContain("oldest-output");
    expect(JSON.parse((await get(running)).content[0].text)).toMatchObject({ job_id: running, status: "running" });
    expect(JSON.parse((await get(newer)).content[0].text)).toMatchObject({ job_id: newer, status: "succeeded", output: "newer-output" });
    expect(JSON.parse((await get(newest)).content[0].text)).toMatchObject({ job_id: newest, status: "succeeded", output: "newest-output" });
    await writeFile(release, "release");
    await waitStatus(running, "succeeded");
    expect(f.violations).toEqual([]);
  } finally {
    await writeFile(release, "release").catch(() => {});
    await f.close();
  }
});
