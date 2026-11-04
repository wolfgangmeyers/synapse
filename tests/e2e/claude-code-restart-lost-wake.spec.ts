import { createServer, type ServerResponse } from "node:http";
import { randomBytes, randomUUID } from "node:crypto";
import { writeFile, rm } from "node:fs/promises";
import { test, expect } from "@playwright/test";
import { claudeWorkersHarness } from "./claude-workers-harness";

test("successful Claude restart reoffers mail whose offer hit the reset gate", async ({ page }) => {
  test.setTimeout(90_000);
  const token = randomBytes(32).toString("hex");
  const held = new Map<string, ServerResponse>();
  let launched = "";
  const gated = new Map<string, { agent: string; message: string }>();
  const server = createServer((req, res) => {
    const phase = req.url?.slice(1) ?? "";
    if (req.method !== "POST" || !["restart", "gated", "launch"].includes(phase) || req.headers.authorization !== `Bearer ${token}` || (phase === "restart" && held.has(phase))) { req.resume(); res.writeHead(403).end(); return; }
    let raw = "";
    req.on("data", chunk => { raw += String(chunk); if (raw.length > 4096) req.destroy(); });
    req.on("end", () => { try { const body = JSON.parse(raw); if (body.phase !== phase || typeof body.agent_id !== "string" || !body.agent_id) throw Error(); if (phase === "gated") {
        if (!["initial", "resolved"].includes(body.site) || typeof body.message_id !== "string" || gated.has(body.site)) throw Error();
        gated.set(body.site, { agent: body.agent_id, message: body.message_id }); res.writeHead(204).end();
      } else if (phase === "launch") { if (launched) throw Error(); launched = body.agent_id; res.writeHead(204).end(); }
      else { res.setHeader("X-Test-Agent", body.agent_id); held.set(phase, res); } } catch { res.writeHead(400).end(); } });
  });
  await new Promise<void>(resolve => server.listen(0, "127.0.0.1", resolve));
  const address = server.address(); if (!address || typeof address === "string") throw Error("no listener");
  let f: Awaited<ReturnType<typeof claudeWorkersHarness>> | undefined;
  let config = "";
  try {
    f = await claudeWorkersHarness({ turns: [[{ text: "Restart mail delivered" }]] });
    await f.open(page); const agent = `claude-${randomUUID()}`;
    const messageId = randomUUID(), body = `restart lost wake ${messageId}`;
    config = f.router.databasePath + ".restart-mail-barrier.json";
    await writeFile(config, JSON.stringify({ enabled: "1", endpoint: `http://127.0.0.1:${address.port}`, token, agent_id: agent, message_id: messageId }), { mode: 0o600 });
    const launch = await f.request(page, "/v1/managed-agents", { name: agent, cwd: `${f.router.root}/work`, runtime: "embedded", model: { provider: "claude-code", id: "sonnet" } }, "POST", true);
    expect(launch.status).toBe(202);
    await expect(page.locator(`[data-agent-id="${agent}"]`)).toBeVisible();
    await page.locator(`[data-agent-id="${agent}"]`).click();
    // This receipt follows the launch offer's mail dispatch and goal check.
    await expect.poll(() => launched, { timeout: 7000 }).toBe(agent);
    const restartID = randomUUID();
    const restart = () => page.evaluate(async ({ url, restartID }) => {
      const response = await fetch(url, { method: "POST", credentials: "omit",
        headers: { "Content-Type": "application/json", "X-Request-ID": restartID }, body: "{}" });
      return { status: response.status, body: await response.json() };
    }, { url: `${f!.proxy.url}/v1/managed-agents/${agent}/restart`, restartID });
    const accepted = await restart();
    expect(accepted.status).toBe(202);
    await expect.poll(() => held.get("restart")?.getHeader("X-Test-Agent"), { timeout: 7000 }).toBe(agent);
    expect(gated.size).toBe(0);
    const { now } = await f.router.advanceClock(1);
    await page.clock.setFixedTime(new Date(now));
    const sent = await f.request(page, "/v1/messages", { id: messageId, from: "restart-fixture", to: agent, body, created_at: new Date(now).toISOString() });
    expect(sent.status).toBe(201); expect(sent.body.message.id).toBe(messageId);
    // afterEnqueue schedules two offers. The receipts are sent only after each
    // gated dispatch has released the serial lane, before restarting can proceed.
    await expect.poll(() => [...gated.entries()].sort(), { timeout: 7000 }).toEqual([["initial", { agent, message: messageId }], ["resolved", { agent, message: messageId }]]);
    const replay = await restart();
    expect(replay.status).toBe(202);
    expect(replay.body).toEqual(accepted.body);
    expect(held.get("restart")?.writableEnded).toBe(false);
    expect(f.rows("SELECT state FROM messages WHERE id=?", messageId)[0].state).toBe("queued");
    expect(f.tableRows("embedded_attempts", agent).filter(row => row.message_id === messageId)).toHaveLength(0);
    // No further message, prompt, interrupt, or lifecycle operation triggers a wake.
    held.get("restart")!.writeHead(204).end();
    await expect.poll(() => f!.tableRows("embedded_acks", agent).filter(row => row.message_id === messageId).length, { timeout: 12000 }).toBe(1);
    const attempts = f.tableRows("embedded_attempts", agent).filter(row => row.message_id === messageId);
    const acks = f.tableRows("embedded_acks", agent).filter(row => row.message_id === messageId);
    expect(attempts).toHaveLength(1); expect(acks).toHaveLength(1);
    expect(acks[0].attempt_id).toBe(attempts[0].attempt_id);
    expect(Number(acks[0].entry_seq)).toBeGreaterThan(0);
    const checked = f.rows(`SELECT e.kind, CAST(e.body AS TEXT) AS body FROM embedded_acks a
      JOIN embedded_entries e ON e.agent_id=a.agent_id AND e.session_id=a.session_id
        AND e.epoch=a.epoch AND e.generation=a.generation AND e.seq=a.entry_seq
      WHERE a.message_id=?`, messageId);
    expect(checked).toHaveLength(1); expect(checked[0].kind).toBe("user");
    expect(String(checked[0].body)).toContain(body);
    const inputs = await f.inputs();
    expect(inputs.filter(input => JSON.stringify(input).includes(body))).toHaveLength(1);
    await f.final(page, agent, "Restart mail delivered");
    expect(f.tableRows("embedded_attempts", agent).filter(row => row.message_id === messageId)).toHaveLength(1);
    expect(f.tableRows("embedded_acks", agent).filter(row => row.message_id === messageId)).toHaveLength(1);
  } finally {
    if (config) await rm(config, { force: true });
    for (const response of held.values()) if (!response.writableEnded && !response.destroyed) response.writeHead(503).end();
    server.closeAllConnections(); await new Promise<void>(resolve => server.close(() => resolve()));
    await f?.close();
  }
});
