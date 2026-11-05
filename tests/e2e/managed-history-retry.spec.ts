import { test, expect } from '@playwright/test';
import { execFile, spawn } from 'node:child_process';
import { promisify } from 'node:util';
import { resolve, join } from 'node:path';
import { allocatePort, startIsolatedRouter, startMockLLMServer, startRouterProxy } from './harness';
const exec = promisify(execFile);
const repo = resolve(import.meta.dirname, '../..');
for (const failure of ['activity', 'conversation', 'both'] as const) test(`retry failed managed history: ${failure}`, async ({ page }) => {
  test.setTimeout(90_000);
  const mock = await startMockLLMServer();
  const router = await startIsolatedRouter({ repoRoot: repo, managed: true });
  let proxy: Awaited<ReturnType<typeof startRouterProxy>> | undefined;
  let vite: ReturnType<typeof spawn> | undefined;
  try {
    proxy = await startRouterProxy({ repoRoot: repo, upstreamPort: router.port });
    await exec('python3', ['-c', `
import sqlite3,sys
c=sqlite3.connect(sys.argv[1])
c.execute("INSERT INTO agents(agent_id,first_seen_at) VALUES('combined-agent','2026-01-01T00:00:00Z')")
c.execute("INSERT INTO managed_agents(agent_id,name,cwd,selected_model_provider,selected_model_id,desired_state,lifecycle_state,activity_state,session_epoch,manifest_revision,manifest_hash,created_at,updated_at) VALUES('combined-agent','combined-agent',?,'mock','mock','running','running','idle',1,1,?,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')",(sys.argv[2],'a'*64))
for i in range(40):
 c.execute("INSERT INTO conversation_timeline(id,agent_id,seq,kind,role,content,metadata,created_at) VALUES(?, 'combined-agent', ?, 'message', 'assistant', ?, '{}', ?)",(f'timeline-{i}',i+1,f'Timeline item {i:02d}',1767225600000+(i*2000 if sys.argv[3]=='interleaved' else (0 if sys.argv[3]=='tied' else i*1000))))
 c.execute("INSERT INTO managed_complete_history(agent_id,category,level,message,occurred_at) VALUES('combined-agent','system','info',?,?)",(f'Activity item {i:02d}',('2026-01-01T00:00:00Z' if sys.argv[3]=='tied' else (f'2026-01-01T01:00:{i:02d}Z' if sys.argv[3]=='zero-contribution' else f'2026-01-01T00:{(i*2+1)//60:02d}:{(i*2+1)%60:02d}Z'))))
c.commit()
`, router.databasePath, router.root, 'interleaved']);
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
    const attempts = { activity: 0, conversation: 0, fallback: 0 };
    await page.route('**/v1/**', async route => {
      const request = new URL(route.request().url());
      if (request.pathname === '/v1/conversations' && request.origin === url && failure !== 'activity' && attempts.fallback++ === 0) return route.fulfill({ status: 503, body: 'unavailable', headers: { 'Access-Control-Allow-Origin': '*' } });
      const kind = request.pathname === '/v1/history' ? 'activity' : request.pathname === '/v1/conversation' ? 'conversation' : undefined;
      if (!kind || request.origin !== url) return route.fallback();
      attempts[kind]++;
      if (attempts[kind] === 1 && (failure === kind || failure === 'both')) return route.fulfill({ status: 503, body: 'unavailable', headers: { 'Access-Control-Allow-Origin': '*' } });
      return route.continue();
    });
    await page.goto(frontend);
    await page.locator('.agent-row, .agent-row-select').filter({ hasText: /combined.agent/i }).first().click();
    const retry = page.getByRole('button', { name: 'Retry history' });
    await expect(retry).toBeVisible();
    if (failure !== 'activity') await expect.poll(() => attempts.fallback).toBeGreaterThan(0);
    await retry.click();
    await expect(retry).toHaveCount(0);
    await expect(page.getByText('Activity item 39', { exact: true })).toBeVisible();
    await expect(page.getByText('Timeline item 39', { exact: true })).toBeVisible();
    if (failure !== 'conversation') expect(attempts.activity).toBeGreaterThan(1);
    if (failure !== 'activity') expect(attempts.conversation).toBeGreaterThan(1);
  } finally {
    vite?.kill('SIGTERM');
    await proxy?.close();
    await router.close();
    await mock.close();
  }
});
