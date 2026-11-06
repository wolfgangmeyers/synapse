import { test, expect } from '@playwright/test';
import { createServer } from 'node:http';
import { join } from 'node:path';
import { DatabaseSync } from 'node:sqlite';
import { workflowHarness, model } from './workflow-implicit-goals-harness';

test('persisted MCP tools reach the first embedded turn after router startup', async ({ page }) => {
  test.setTimeout(120_000);
  const calls: string[] = [];
  const mcp = createServer((req, res) => {
    let body = '';
    req.on('data', chunk => body += chunk);
    req.on('end', () => {
      const rpc = JSON.parse(body);
      calls.push(rpc.method);
      const result = rpc.method === 'initialize' ? { protocolVersion: '2024-11-05', serverInfo: { name: 'fixture', version: '1' }, capabilities: { tools: {} } }
        : rpc.method === 'tools/list' ? { tools: [{ name: 'lookup', description: 'Look up a value', inputSchema: { type: 'object', properties: { query: { type: 'string' } } } }] }
        : rpc.method === 'tools/call' ? { content: [{ type: 'text', text: 'fixture result' }], isError: false } : {};
      res.writeHead(200, { 'Content-Type': 'application/json' }).end(JSON.stringify({ jsonrpc: '2.0', id: rpc.id, result }));
    });
  });
  await new Promise<void>(resolve => mcp.listen(0, '127.0.0.1', resolve));
  const address = mcp.address();
  if (!address || typeof address === 'string') throw Error('missing MCP address');
  const f = await workflowHarness();
  try {
    await f.open(page);
    const created = await page.evaluate(async ({ url, mcpURL }) => {
      const response = await fetch(url + '/v1/settings/mcp-servers', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ name: 'Fixture', url: mcpURL, auth_mode: 'none' }) });
      return { status: response.status, body: await response.json() };
    }, { url: f.proxy.url, mcpURL: `http://127.0.0.1:${address.port}/mcp` });
    expect(created.status).toBe(201);
    expect(created.body.server.status).toBe('connected');
    const serverID = created.body.server.id;
    const details = await f.api(`/v1/settings/mcp-servers/${serverID}`);
    const tool = details.tools[0].prefixed_name;
    expect(tool).toBeTruthy();
    // Restart after configuration and before launch: no MCP settings endpoint is
    // touched on the new router before the worker's first model request.
    await f.restart();
    f.replies.push({ tool, args: { query: 'hello' } }, { answer: 'finished MCP lookup' });
    const before = f.requests.length;
    await page.evaluate(async ({ url, cwd, model }) => {
      const response = await fetch(url + '/v1/managed-agents', { method: 'POST', headers: { 'Content-Type': 'application/json', 'X-Request-ID': crypto.randomUUID() }, body: JSON.stringify({ name: 'mcp-startup-worker', cwd, runtime: 'embedded', model, initial_instruction: 'Run lookup' }) });
      if (response.status !== 202) throw Error(`launch failed: ${response.status} ${await response.text()}`);
    }, { url: f.proxy.url, cwd: join(f.router.root, 'work'), model });
    await expect.poll(() => f.requests.length).toBeGreaterThanOrEqual(before + 2);
    const tools = f.requests[before].tools as { name: string }[];
    expect(tools.map(t => t.name)).toContain(tool);
    expect(calls).toContain('tools/call');
    expect(JSON.stringify(f.requests[before + 1].input)).toContain('fixture result');
    // The same persisted worker must also discover and call the active tool
    // during recovery, without a settings request initializing the new router.
    const activeRecoveryStart = f.requests.length;
    const callsBeforeRecovery = calls.filter(method => method === 'tools/call').length;
    f.replies.push({ tool, args: { query: 'after restart' } }, { answer: 'recovered MCP lookup complete' });
    await f.restart();
    await expect.poll(() => f.requests.length).toBeGreaterThanOrEqual(activeRecoveryStart + 2);
    expect((f.requests[activeRecoveryStart].tools as { name: string }[]).map(t => t.name)).toContain(tool);
    expect(calls.filter(method => method === 'tools/call')).toHaveLength(callsBeforeRecovery + 1);
    expect(JSON.stringify(f.requests[activeRecoveryStart + 1].input)).toContain('fixture result');
    // Model a server needing authentication before the next cold start.
    const db = new DatabaseSync(f.router.databasePath);
    try { db.prepare("UPDATE managed_mcp_servers SET status='needs_auth' WHERE id=?").run(serverID); } finally { db.close(); }
    const recoveryStart = f.requests.length;
    f.replies.push({ answer: 'recovered without unavailable tools' });
    await f.restart();
    await expect.poll(() => f.requests.length).toBeGreaterThan(recoveryStart);
    expect((f.requests[recoveryStart].tools as { name: string }[]).map(t => t.name)).not.toContain(tool);
    const inactiveStart = f.requests.length;
    f.replies.push({ answer: 'inactive server startup complete' });
    await page.evaluate(async ({ url, cwd, model }) => {
      const response = await fetch(url + '/v1/managed-agents', { method: 'POST', headers: { 'Content-Type': 'application/json', 'X-Request-ID': crypto.randomUUID() }, body: JSON.stringify({ name: 'mcp-inactive-worker', cwd, runtime: 'embedded', model, initial_instruction: 'Start without unavailable tools' }) });
      if (response.status !== 202) throw Error(`launch failed: ${response.status} ${await response.text()}`);
    }, { url: f.proxy.url, cwd: join(f.router.root, 'work'), model });
    await expect.poll(() => f.requests.length).toBeGreaterThan(inactiveStart);
    expect((f.requests[inactiveStart].tools as { name: string }[]).map(t => t.name)).not.toContain(tool);
    await expect.poll(() => f.wsFrames.length).toBeGreaterThan(0);
    expect(f.violations).toEqual([]);
  } finally {
    await f.close();
    mcp.closeAllConnections();
    await new Promise<void>(resolve => mcp.close(() => resolve()));
  }
});
