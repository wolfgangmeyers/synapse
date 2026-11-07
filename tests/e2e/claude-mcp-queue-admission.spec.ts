import { createServer } from "node:http";
import { once } from "node:events";
import { test, expect } from "@playwright/test";
import { claudeWorkersHarness, type ClaudeWorkersHarness } from "./claude-workers-harness";

test("queued MCP timeout does not write; exact witnessed retry succeeds", async ({ page }) => {
  test.setTimeout(150_000);
  let release!: () => void;
  const held = new Promise<void>(resolve => { release = resolve; });
  let arrived!: () => void;
  const entered = new Promise<void>(resolve => { arrived = resolve; });
  const server = createServer(async (req, res) => {
    let raw = ""; for await (const chunk of req) raw += chunk;
    const rpc = JSON.parse(raw);
    res.setHeader("Content-Type", "application/json");
    if (rpc.method === "notifications/initialized") { res.writeHead(202).end(); return; }
    let result: object;
    if (rpc.method === "initialize") result = { protocolVersion: "2024-11-05", capabilities: { tools: {} }, serverInfo: { name: "fixture", version: "1" } };
    else if (rpc.method === "tools/list") result = { tools: [{ name: "hold", description: "hold lane", inputSchema: { type: "object", properties: {} } }] };
    else if (rpc.method === "tools/call") { arrived(); await held; result = { content: [{ type: "text", text: "released" }] }; }
    else { res.writeHead(400).end(); return; }
    res.end(JSON.stringify({ jsonrpc: "2.0", id: rpc.id, result }));
  });
  let f: ClaudeWorkersHarness | undefined;
  try {
    server.listen(0, "127.0.0.1"); await once(server, "listening");
    const address = server.address(); if (!address || typeof address === "string") throw Error("no fixture address");
    f = await claudeWorkersHarness({ continue_after_mcp_error: true, executions_by_input: [{ contains: "Hold queue", turns: [[
      { mcp_start: { id: "held-call", name: "fixture_hold", input: {}, require_catalog: true } },
      { barrier: "holder-started" },
      { mcp_call: { id: "expired-write", name: "scratchpad", input: { action: "append", content: "must never persist" }, require_catalog: true } },
      { barrier: "timeout-observed" }, { mcp_join: true }, { text: "Queue finished" },
    ]] }] }, { mcpQueueFixture: true });
    await f.open(page);
    const created = await f.request(page, "/v1/settings/mcp-servers", { name: "Fixture", url: `http://127.0.0.1:${address.port}/mcp`, auth_mode: "none" });
    expect(created.status).toBe(201);
    const agent = await f.launch(page);
    await f.userTurn(page, "Hold queue");
    await Promise.race([entered, new Promise<never>((_, reject) => { const timer = setTimeout(() => reject(Error("MCP fixture not entered")), 20_000); entered.finally(() => clearTimeout(timer)); })]);
    await expect.poll(async () => (await f!.records()).some(row => row.kind === "barrier" && row.name === "holder-started")).toBe(true);
    await f.releaseBarrier((await f.records()).find(row => row.kind === "barrier" && row.name === "holder-started")!);
    await expect.poll(async () => (await f!.records()).some(row => row.kind === "barrier" && row.name === "timeout-observed"), { timeout: 15_000 }).toBe(true);
    expect((await f.records()).filter(row => row.kind === "mcp_call_response" && row.tool_id === "expired-write")).toEqual([
      expect.objectContaining({ error: { code: -32000, message: "tool queue wait timed out" }, result: null }),
    ]);
    expect(f.tableRows("scratchpad_entries", agent).some(row => row.content === "must never persist")).toBe(false);
    release();
    await expect.poll(async () => (await f!.records()).some(row => row.kind === "mcp_call_response" && row.tool_id === "held-call"), { timeout: 15_000 }).toBe(true);
    expect(f.tableRows("scratchpad_entries", agent).some(row => row.content === "must never persist")).toBe(false);
    // Retry the same producer ID and arguments while the turn is still held.
    // This establishes that the timed-out contender had checked authority.
    const retry = await f.mcp(await f.grant(), "scratchpad", { action: "append", content: "must never persist" }, "expired-write");
    expect(retry.status).toBe(200);
    expect(retry.body.error).toBeUndefined();
    expect(retry.body.result.content[0].text).toContain("Appended scratchpad entry");
    expect(f.tableRows("scratchpad_entries", agent).filter(row => row.content === "must never persist")).toHaveLength(1);
    await f.releaseBarrier((await f.records()).find(row => row.kind === "barrier" && row.name === "timeout-observed")!);
    await f.final(page, agent, "Queue finished");
    expect((await f.records()).filter(row => row.kind === "mcp_call_response" && row.tool_id === "held-call")).toEqual([expect.objectContaining({ error: null, result: expect.objectContaining({ content: [{ type: "text", text: "released" }] }) })]);
    expect(f.tableRows("scratchpad_entries", agent).filter(row => row.content === "must never persist")).toHaveLength(1);
  } finally {
    release();
    try { await f?.close(); } finally { server.closeAllConnections(); await new Promise<void>(resolve => server.close(() => resolve())); }
  }
});
