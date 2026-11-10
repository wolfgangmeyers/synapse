import { test, expect } from "@playwright/test";
import { writeFile } from "node:fs/promises";
import { join } from "node:path";
import { claudeWorkersHarness } from "./claude-workers-harness";

test.skip(process.platform !== "linux", "Background bash launch is supported only on Linux");

test("registered managed list and detail routes project real background job snapshots", async ({ page }) => {
  test.setTimeout(120_000);
  const f = await claudeWorkersHarness();
  const release = join(f.fixtureRoot, "rest-snapshot-release");
  const quote = (s: string) => `'${s.replaceAll("'", "'\\''")}'`;
  try {
    await f.configure({ turns_by_input: [{ contains: "Launch REST snapshot job", steps: [
      { mcp_call: { id: "rest-launch", name: "launch_background_bash", input: {
        command: `printf secret-output; while [ ! -f ${quote(release)} ]; do sleep 0.05; done; exit 0`,
      }, require_catalog: true } },
      { text: "REST job launched" },
    ] }] });
    await f.open(page);
    const agent = await f.launch(page);
    await f.userTurn(page, "Launch REST snapshot job");
    await f.final(page, agent, "REST job launched");
    const result = f.rows("SELECT CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? AND kind IN ('toolResult','tool_result') ORDER BY rowid", agent)
      .map(row => JSON.parse(String(row.body))).find(row => row.toolName === "launch_background_bash");
    expect(result?.isError, JSON.stringify(result)).toBeFalsy();
    const jobId = JSON.parse(result.content[0].text).job_id;
    expect(typeof jobId).toBe("string");
    expect(jobId.length).toBeGreaterThan(0);

    const checkRoutes = async (status: string) => {
      for (const path of ["/v1/managed-agents", `/v1/managed-agents/${encodeURIComponent(agent)}`]) {
        const response = await f.request(page, path, undefined, "GET", true);
        expect(response.status, JSON.stringify(response.body)).toBe(200);
        const record = path.endsWith("managed-agents")
          ? response.body.agents.find((item: { agent_id: string }) => item.agent_id === agent)
          : response.body.agent;
        expect(record?.agent_id).toBe(agent);
        // The job entry contains only its ID and status, with no additional fields.
        expect(record.background_bash_jobs).toEqual([{ id: jobId, status }]);
      }
    };
    await checkRoutes("running");
    await writeFile(release, "release");
    await expect.poll(async () => {
      const response = await f.request(page, `/v1/managed-agents/${encodeURIComponent(agent)}`, undefined, "GET", true);
      return response.body.agent?.background_bash_jobs?.[0]?.status;
    }, { timeout: 15_000 }).toBe("succeeded");
    await checkRoutes("succeeded");
    expect(f.violations).toEqual([]);
  } finally {
    await writeFile(release, "release").catch(() => {});
    await f.close();
  }
});
