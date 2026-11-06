import { createServer } from "node:http";
import { once } from "node:events";
import { DatabaseSync } from "node:sqlite";
import { test, expect } from "@playwright/test";
import { claudeWorkersHarness, type ClaudeWorkersHarness } from "./claude-workers-harness";

// The router is constructed normally by the harness: no injected MCP manager.
// Only Claude and the remote MCP server are deterministic local fixtures.
test("configured MCP tools reach Claude through normal router startup", async ({ page }) => {
  test.setTimeout(120_000);
  const calls: unknown[] = [];
  const server = createServer(async (req, res) => {
    let raw = "";
    for await (const chunk of req) raw += chunk;
    const rpc = JSON.parse(raw);
    res.setHeader("Content-Type", "application/json");
    if (rpc.method === "notifications/initialized") { res.writeHead(202).end(); return; }
    let result: object;
    if (rpc.method === "initialize") {
      result = { protocolVersion: "2024-11-05", capabilities: { tools: {} }, serverInfo: { name: "fixture", version: "1" } };
    } else if (rpc.method === "tools/list") {
      result = { tools: [{ name: "lookup", description: "Look up a fixture", inputSchema: { type: "object", properties: { query: { type: "string" } }, required: ["query"] } }] };
    } else if (rpc.method === "tools/call") {
      calls.push(rpc.params);
      result = { content: [{ type: "text", text: "fixture-result-42" }] };
    } else {
      res.writeHead(400).end(); return;
    }
    res.end(JSON.stringify({ jsonrpc: "2.0", id: rpc.id, result }));
  });
  server.listen(0, "127.0.0.1");
  await once(server, "listening");
  const address = server.address();
  if (!address || typeof address === "string") throw Error("missing fixture address");
  let f: ClaudeWorkersHarness | undefined;
  try {
    f = await claudeWorkersHarness({ executions_by_input: [
      { contains: "Use configured lookup", turns: [[
        { mcp_call: { id: "remote-lookup", name: "fixture_lookup", input: { query: "example" }, require_catalog: true } },
        { text: "Configured MCP call finished" },
      ]] },
      { contains: "Inspect inactive catalog", turns: [[
        { mcp_call: { id: "inactive-catalog", name: "scratchpad", input: { action: "read" }, require_catalog: true } },
        { text: "Inactive catalog checked" },
      ]] },
    ] });
    await f.open(page);
    const created = await f.request(page, "/v1/settings/mcp-servers", { name: "Fixture", url: `http://127.0.0.1:${address.port}/mcp`, auth_mode: "none" });
    expect(created.status).toBe(201);
    expect(created.body.server).toMatchObject({ status: "connected", tools_count: 1 });
    expect(f.rows("SELECT name, prefixed_name FROM managed_mcp_tools WHERE server_id=?", created.body.server.id)).toEqual([{ name: "lookup", prefixed_name: "fixture_lookup" }]);
    // Start a new router with the server already persisted. Do not touch the MCP
    // settings endpoints after restart: discovery must not depend on lazy setup.
    const oldPID = f.router.process.pid;
    await f.restart();
    expect(f.router.process.pid).not.toBe(oldPID);
    expect(f.rows("SELECT status FROM managed_mcp_servers WHERE id=?", created.body.server.id)).toEqual([{ status: "connected" }]);
    const agent = await f.launch(page);
    await f.userTurn(page, "Use configured lookup");
    await f.final(page, agent, "Configured MCP call finished");
    expect((await f.records()).filter(row => row.kind === "catalog").some(row => Array.isArray(row.names) && row.names.includes("fixture_lookup"))).toBe(true);
    expect(calls).toEqual([{ name: "lookup", arguments: { query: "example" } }]);
    const results = f.rows("SELECT CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? AND kind IN ('toolResult','tool_result')", agent)
      .map(row => JSON.parse(String(row.body)));
    expect(results).toEqual(expect.arrayContaining([expect.objectContaining({ toolName: "fixture_lookup", isError: false, content: [expect.objectContaining({ type: "text", text: "fixture-result-42" })] })]));

    // Simulate an authentication failure while stopped. The saved tool stays in
    // SQLite, but the next Claude process must not see or invoke it.
    const catalogCount = (await f.records()).filter(row => row.kind === "catalog").length;
    await f.router.restart(async databasePath => {
      const db = new DatabaseSync(databasePath);
      try { db.prepare("UPDATE managed_mcp_servers SET status='needs_auth' WHERE id=?").run(created.body.server.id); }
      finally { db.close(); }
    });
    expect(f.rows("SELECT name FROM managed_mcp_tools WHERE server_id=?", created.body.server.id)).toEqual([{ name: "lookup" }]);
    const inactiveAgent = await f.launch(page);
    await f.userTurn(page, "Inspect inactive catalog");
    await f.final(page, inactiveAgent, "Inactive catalog checked");
    const catalogs = (await f.records()).filter(row => row.kind === "catalog");
    expect(catalogs).toHaveLength(catalogCount + 1);
    expect(catalogs.at(-1)?.names).toContain("scratchpad");
    expect(catalogs.at(-1)?.names).not.toContain("fixture_lookup");
    expect(calls).toEqual([{ name: "lookup", arguments: { query: "example" } }]);
    expect(f.violations).toEqual([]);
  } finally {
    try { await f?.close(); } finally {
      server.closeAllConnections();
      await new Promise<void>((resolve, reject) => server.close(error => error ? reject(error) : resolve()));
    }
  }
});
