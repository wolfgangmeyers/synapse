import { writeFile, rm } from "node:fs/promises";
import { randomUUID } from "node:crypto";
import { test, expect } from "@playwright/test";
import { claudeWorkersHarness } from "./claude-workers-harness";
import { serialBarrier } from "./claude-code-serial-barrier";

test("Claude terminating reaches browser while DELETE waits on serial lane", async ({ page }) => {
  const frames: string[] = [];
  page.on("websocket", socket => {
    if (new URL(socket.url()).pathname !== "/v1/ui-live") return;
    socket.on("framereceived", frame => frames.push(String(frame.payload)));
  });
  const barrier = await serialBarrier();
  let f: Awaited<ReturnType<typeof claudeWorkersHarness>> | undefined;
  let config: string | undefined;
  let prompt: Promise<Response> | undefined;
  let deletion: Promise<Response> | undefined;
  try {
    f = await claudeWorkersHarness();
    await f.open(page);
    const agent = await f.launch(page);
    config = f.router.databasePath + ".serial-barrier.json";
    await writeFile(config, JSON.stringify({ enabled: "1", endpoint: barrier.endpoint, token: barrier.token, agent_id: agent }), { mode: 0o600 });
    const headers = { "Content-Type": "application/json", "X-Request-ID": randomUUID() };
    prompt = fetch(`${f.router.httpUrl}/v1/managed-agents/${agent}/prompt`, { method: "POST", headers, body: JSON.stringify({ message: "Held serial prompt" }), signal: AbortSignal.timeout(15000) });
    void prompt.catch(() => {});
    expect(await barrier.arrived).toBe(agent);
    barrier.assertHeld();
    const frameStart = frames.length;
    let deleteSettled = false;
    deletion = fetch(`${f.router.httpUrl}/v1/managed-agents/${agent}`, { method: "DELETE", headers: { "X-Request-ID": randomUUID() }, signal: AbortSignal.timeout(15000) });
    void deletion.then(() => { deleteSettled = true; }, () => { deleteSettled = true; });
    await expect.poll(() => frames.slice(frameStart).some(raw => {
      const frame = JSON.parse(raw);
      return (frame.type === "managed_agent_delta" && frame.agent_id === agent && frame.changes?.lifecycle_state === "terminating") ||
        (frame.type === "managed_agents_snapshot" && Array.isArray(frame.agents) && frame.agents.some((record: { agent_id?: string; lifecycle_state?: string }) => record.agent_id === agent && record.lifecycle_state === "terminating"));
    }), { message: "ui-live publishes terminating while serial lane is held" }).toBe(true);
    await expect(page.locator(`[data-agent-id="${agent}"]`)).toHaveClass(/\bterminating\b/);
    barrier.assertHeld();
    expect(deleteSettled).toBe(false);
    expect(f.tableRows("managed_agents", agent)[0].lifecycle_state).toBe("terminating");
    await rm(config); config = undefined;
    barrier.release();
    await barrier.finished;
    expect((await deletion).status).toBe(202);
    await prompt;
  } finally {
    if (config) await rm(config, { force: true });
    await barrier.close();
    await Promise.allSettled([prompt, deletion].filter((p): p is Promise<Response> => p !== undefined));
    if (f) await f.close();
  }
});
