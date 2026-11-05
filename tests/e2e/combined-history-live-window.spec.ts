import { test, expect } from '@playwright/test';
import { execFile, spawn } from 'node:child_process';
import { promisify } from 'node:util';
import { resolve, join } from 'node:path';
import { allocatePort, startIsolatedRouter, startMockLLMServer, startRouterProxy } from './harness';

const exec = promisify(execFile);
const repo = resolve(import.meta.dirname, '../..');

test('live interleaved with historical sources stays protected while the strict window refetches completely', async ({ page }) => {
  const scenario = 'interleaved';
  test.setTimeout(90_000);
  const mock = await startMockLLMServer();
  const router = await startIsolatedRouter({ repoRoot: repo, managed: true });
  let proxy: Awaited<ReturnType<typeof startRouterProxy>> | undefined;
  let vite: ReturnType<typeof spawn> | undefined;
  try {
    proxy = await startRouterProxy({ repoRoot: repo, upstreamPort: router.port });
    await exec('python3', ['-c', `
import sqlite3,sys,time,datetime
c=sqlite3.connect(sys.argv[1])
c.execute("INSERT INTO agents(agent_id,first_seen_at) VALUES('combined-agent','2026-01-01T00:00:00Z')")
c.execute("INSERT INTO managed_agents(agent_id,name,cwd,selected_model_provider,selected_model_id,desired_state,lifecycle_state,activity_state,session_epoch,manifest_revision,manifest_hash,created_at,updated_at) VALUES('combined-agent','combined-agent',?,'mock','mock','running','running','idle',1,1,?,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')",(sys.argv[2],'a'*64))
# The live message's current timestamp falls strictly inside both old sources.
base=int(time.time()*1000)-40000
for i in range(40):
 c.execute("INSERT INTO conversation_timeline(id,agent_id,seq,kind,role,content,metadata,created_at) VALUES(?, 'combined-agent', ?, 'message', 'assistant', ?, '{}', ?)",(f'timeline-{i}',i+1,f'Timeline item {i:02d}',base+i*2000))
 timestamp=datetime.datetime.fromtimestamp((base+i*2000+1000)/1000,datetime.timezone.utc).isoformat(timespec='milliseconds').replace('+00:00','Z')
 c.execute("INSERT INTO managed_complete_history(agent_id,category,level,message,occurred_at) VALUES('combined-agent','system','info',?,?)",(f'Activity item {i:02d}',timestamp))
c.commit()
`, router.databasePath, router.root, scenario]);
    const port = await allocatePort();
    const frontend = `http://127.0.0.1:${port}`;
    vite = spawn(resolve(repo, 'web/router-ui-react/node_modules/.bin/vite'), ['--host', '127.0.0.1', '--port', String(port), '--strictPort'], { cwd: join(repo, 'web/router-ui-react'), env: { ...process.env, HOME: join(router.root, 'managed-home') }, stdio: 'pipe' });
    await expect.poll(async () => { try { return (await fetch(frontend)).status; } catch { return 0; } }, { timeout: 20_000 }).toBe(200);
    const url = proxy.url;
    await page.addInitScript(({ url }) => {
      localStorage.clear();
      localStorage.setItem('mecha.web.activeRouter', url);
      localStorage.setItem('mecha.web.routers', JSON.stringify([url]));
    }, { url });
    await page.goto(frontend);
    // Hold the real initial snapshots after the proxy/router has produced them.
    // A real REST message then reaches the browser over its WebSocket while both
    // snapshots are in flight; its timestamp falls between seeded source records.
    let release!: () => void;
    const held = new Promise<void>(resolve => { release = resolve; });
    const captured = new Set<string>();
    await page.route('**/v1/{history,conversation}?*', async route => {
      const request = new URL(route.request().url());
      if (request.searchParams.get('agent_id') !== 'combined-agent' || request.searchParams.has('before_seq') || request.searchParams.has('before')) return route.continue();
      const response = await route.fetch();
      captured.add(request.pathname);
      await held;
      await route.fulfill({ response });
    });
    await page.locator('.agent-row, .agent-row-select').filter({ hasText: /combined.agent/i }).first().click();
    try {
      await expect.poll(() => captured.size).toBe(2);
      const response = await fetch(`${url}/v1/messages`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ id: 'combined-live-interleaved', from: 'combined-agent', to: 'user', body: 'Live interleaved item', created_at: new Date().toISOString(), source: { kind: 'agent' } }) });
      expect([200, 201], await response.text()).toContain(response.status);
      await expect(page.getByText('Live interleaved item', { exact: true })).toHaveCount(1);
    } finally { release(); }
    await expect(page.getByText('Activity item 39', { exact: true })).toBeVisible();
    await expect(page.getByText('Timeline item 39', { exact: true })).toBeVisible();
    await expect(page.getByText('Timeline item 00', { exact: true })).toHaveCount(0);
    await expect(page.locator('[data-scroll-anchor]')).toHaveCount(51);
    const olderRequests: string[] = [];
    page.on('request', (request) => {
      const url = new URL(request.url());
      if (url.pathname === '/v1/history' || url.pathname === '/v1/conversation') olderRequests.push(request.url());
    });
    await page.getByRole('button', { name: /load older history/i }).click();
    await expect(page.getByText('Timeline item 00', { exact: true })).toBeVisible();
    expect(olderRequests.some((url) => new URL(url).pathname === '/v1/history')).toBe(true);
    expect(olderRequests.some((url) => new URL(url).pathname === '/v1/conversation')).toBe(true);
    for (let i = 0; i < 40; i++) {
      const number = String(i).padStart(2, '0');
      await expect(page.getByText(`Timeline item ${number}`, { exact: true })).toHaveCount(1);
      await expect(page.getByText(`Activity item ${number}`, { exact: true })).toHaveCount(1);
    }
    await expect(page.locator('[data-scroll-anchor]')).toHaveCount(81);
    await expect(page.getByText('Live interleaved item', { exact: true })).toHaveCount(1);
    await expect(page.getByRole('button', { name: /load older history/i })).toHaveCount(0);
  } finally {
    vite?.kill('SIGTERM');
    await proxy?.close();
    await router.close();
    await mock.close();
  }
});
