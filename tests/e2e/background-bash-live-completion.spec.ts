import { test, expect } from "@playwright/test";
import { writeFile } from "node:fs/promises";
import { join } from "node:path";
import { claudeWorkersHarness } from "./claude-workers-harness";

test.skip(process.platform !== "linux", "Background bash launch is supported only on Linux");

test("background completion reaches the live projection and selected agent header", async ({ page }) => {
  test.setTimeout(120_000);
  const f = await claudeWorkersHarness();
  const release = join(f.fixtureRoot, "live-completion-release");
  const quoted = `'${release.replaceAll("'", "'\\''")}'`;
  const frames: string[] = [];
  page.on("websocket", socket => {
    if (new URL(socket.url()).pathname === "/v1/ui-live") {
      socket.on("framereceived", frame => frames.push(String(frame.payload)));
    }
  });
  try {
    await f.configure({ turns_by_input: [{ contains: "Launch live job", steps: [
      { mcp_call: { id: "launch-live", name: "launch_background_bash", input: { command: `while [ ! -f ${quoted} ]; do sleep 0.05; done; exit 0` }, require_catalog: true } },
      { text: "Live job launched" },
    ] }] });
    await f.open(page);
    const agent = await f.launch(page);
    await f.userTurn(page, "Launch live job");
    // Completion is checked on the dedicated UI-live socket below, not the shared frame collector.
    await f.final(page, agent, "Live job launched", undefined, false);
    const result = f.rows("SELECT CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? AND kind IN ('toolResult','tool_result') ORDER BY rowid", agent)
      .map(row => JSON.parse(String(row.body))).find(row => row.toolName === "launch_background_bash");
    expect(result?.isError, JSON.stringify(result)).toBeFalsy();
    const jobId = JSON.parse(result.content[0].text).job_id;
    expect(typeof jobId).toBe("string");
    expect(jobId.length).toBeGreaterThan(0);
    const widget = page.getByRole("region", { name: "Background shell jobs" });
    await widget.getByRole("button", { name: /shell jobs/i }).click();
    const job = widget.locator(".background-bash-job-list li").first();
    await expect(job).toContainText("running");
    const beforeCompletion = frames.length;
    await writeFile(release, "release");
    await expect.poll(() => frames.slice(beforeCompletion).some(raw => {
      const frame = JSON.parse(raw);
      const agents = frame.type === "managed_agents_snapshot" ? frame.agents :
        frame.type === "managed_agent_delta" ? [frame.changes] : [];
      return agents.some((record: { agent_id?: string; background_bash_jobs?: { id: string; status: string }[] }) =>
        record.agent_id === agent && record.background_bash_jobs?.some(item => item.id === jobId && item.status === "succeeded"));
    }), { timeout: 15_000, message: "completion must appear in the UI live WebSocket projection" }).toBe(true);
    await widget.getByLabel("Filter:").selectOption("completed");
    await expect(job).toContainText("succeeded", { timeout: 15_000 });
    // The browser must never be sent a backwards or same-version agent update
    // while launch and completion snapshots race with other publications.
    const versions = frames.map(raw => JSON.parse(raw)).filter(frame =>
      frame.type === "managed_agents_snapshot" || frame.type === "managed_agent_delta" || frame.type === "managed_agent_removed"
    ).map(frame => frame.projection_version as number);
    expect(versions.length).toBeGreaterThan(1);
    for (let i = 1; i < versions.length; i++) expect(versions[i]).toBeGreaterThan(versions[i - 1]);
    expect(f.violations).toEqual([]);
  } finally {
    await writeFile(release, "release").catch(() => {});
    await f.close();
  }
});
