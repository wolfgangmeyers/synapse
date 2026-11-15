import { test, expect } from '@playwright/test';
import { createServer } from 'node:http';
import { randomUUID } from 'node:crypto';
import { join } from 'node:path';
import { workflowHarness, model } from './workflow-implicit-goals-harness';

// ETS-005a: the MCP server is registered only after the canonical name was rejected.
test('Codex isolates a rejected canonical call after MCP declaration and reload', async ({ page }) => {
  test.setTimeout(120_000);
  const calls: any[] = [];
  const mcp = createServer((req, res) => {
    let body = '';
    req.on('data', chunk => body += chunk);
    req.on('end', () => {
      const rpc = JSON.parse(body);
      if (rpc.method === 'tools/call') calls.push(rpc.params);
      const result = rpc.method === 'initialize' ? { protocolVersion: '2024-11-05', serverInfo: { name: 'fixture', version: '1' }, capabilities: { tools: {} } }
        : rpc.method === 'tools/list' ? { tools: [{ name: 'worker.launch', description: 'Record a call', inputSchema: { type: 'object', properties: { marker: { type: 'string' } }, required: ['marker'] } }] }
        : rpc.method === 'tools/call' ? { content: [{ type: 'text', text: 'MCP positive control executed' }], isError: false } : {};
      res.writeHead(200, { 'Content-Type': 'application/json' }).end(JSON.stringify({ jsonrpc: '2.0', id: rpc.id, result }));
    });
  });
  await new Promise<void>(resolve => mcp.listen(0, '127.0.0.1', resolve));
  const address = mcp.address();
  if (!address || typeof address === 'string') throw Error('missing MCP address');
  const f = await workflowHarness();
  const agent = 'codex-replay-declaration';
  const canonical = 'mecha_worker.launch';
  const alias = 'mecha_worker_launch';
  try {
    await f.api('/v1/managed-agents', { name: agent, cwd: join(f.router.root, 'work'), runtime: 'embedded', model });
    await expect.poll(() => f.rows('SELECT lifecycle_state FROM managed_agents WHERE agent_id=?', agent)[0]?.lifecycle_state, { timeout: 30_000 }).toBe('running');
    await f.open(page);
    f.replies.push({ tool: canonical, args: { marker: 'historical' } }, { answer: 'rejection handled' });
    const initial = await f.router.advanceClock(1);
    const clock = await f.router.advanceClock(Math.max(1, Date.now() + 2000 - Date.parse(initial.now)));
    await f.api('/v1/messages', { id: randomUUID(), from: 'user', to: agent, body: 'Attempt unavailable tool', created_at: new Date(clock.now).toISOString(), source: { kind: 'web_ui' } });
    const entries = () => f.rows('SELECT kind, CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? ORDER BY seq', agent);
    const transcriptId = 'call_response_1_0|fc_response_1_0';
    const results = () => entries().filter(e => e.kind === 'toolResult').map(e => JSON.parse(String(e.body))).filter(e => e.toolCallId === transcriptId);
    await expect.poll(() => results().length, { timeout: 20_000 }).toBe(1);
    await expect.poll(() => entries().some(e => String(e.body).includes('rejection handled')), { timeout: 20_000 }).toBe(true);
    expect((f.requests[0].tools as any[]).map(t => t.name)).not.toContain(alias);
    expect(results()[0].isError).toBe(true);
    const diagnostic = results()[0].content.filter((c: any) => c.type === 'text').map((c: any) => c.text).join('\n');
    expect(diagnostic).toMatch(/unknown|unavailable|not found/i);
    expect(calls).toEqual([]);

    const created = await f.api('/v1/settings/mcp-servers', { name: 'mecha', url: `http://127.0.0.1:${address.port}/mcp`, auth_mode: 'none' });
    expect(created.server.status).toBe('connected');
    const details = await f.api(`/v1/settings/mcp-servers/${created.server.id}`);
    expect(details.tools.map((t: any) => t.prefixed_name)).toContain(canonical);
    const before = f.requests.length;
    f.replies.push({ tool: alias, args: { marker: 'positive' } }, { answer: 'positive complete' });
    await f.restart();
    await expect.poll(() => f.rows('SELECT lifecycle_state FROM managed_agents WHERE agent_id=?', agent)[0]?.lifecycle_state, { timeout: 30_000 }).toBe('running');
    const next = await f.router.advanceClock(2000);
    await f.api('/v1/messages', { id: randomUUID(), from: 'user', to: agent, body: 'Run the newly declared tool', created_at: new Date(next.now).toISOString(), source: { kind: 'web_ui' } });
    await expect.poll(() => f.requests.length, { timeout: 30_000 }).toBeGreaterThanOrEqual(before + 2);
    const replay = f.requests[before];
    expect((replay.tools as any[]).map(t => t.name)).toContain(alias);
    const input = replay.input as any[];
    const historical = input.filter(i => i.type === 'function_call' && i.call_id === 'call_response_1_0');
    const output = input.filter(i => i.type === 'function_call_output' && i.call_id === 'call_response_1_0');
    expect(historical).toHaveLength(1);
    expect(output).toHaveLength(1);
    expect(String(output[0].output)).toContain(diagnostic);
    await expect.poll(() => calls.length, { timeout: 20_000 }).toBe(1);
    expect(calls[0]).toMatchObject({ name: 'worker.launch', arguments: { marker: 'positive' } });
    expect(JSON.stringify(f.requests[before + 1].input)).toContain('MCP positive control executed');
    expect(historical[0].name).not.toBe(alias);
    expect(f.violations).toEqual([]);
  } finally {
    await f.close();
    mcp.closeAllConnections();
    await new Promise<void>(resolve => mcp.close(() => resolve()));
  }
});
