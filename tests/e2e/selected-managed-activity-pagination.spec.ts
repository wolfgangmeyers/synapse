import { test, expect } from '@playwright/test';
import { execFile, spawn } from 'node:child_process';
import { promisify } from 'node:util';
import { resolve, join } from 'node:path';
import { allocatePort, startIsolatedRouter, startMockLLMServer, startRouterProxy } from './harness';

const exec = promisify(execFile);
const repo = resolve(import.meta.dirname, '../..');

test('selected managed-only history pages without cache, duplicate, or anchor jump', async ({ page }) => {
  test.setTimeout(90_000);
  page.setDefaultTimeout(10_000);
  const mock = await startMockLLMServer();
  const router = await startIsolatedRouter({ repoRoot: repo, managed: true });
  let proxy: Awaited<ReturnType<typeof startRouterProxy>> | undefined;
  let vite: ReturnType<typeof spawn> | undefined;
  try {
    proxy = await startRouterProxy({ repoRoot: repo, upstreamPort: router.port });
    // Seed only the managed history table: these records have no timeline counterpart.
    await exec('python3', ['-c', `
import sqlite3,sys
c=sqlite3.connect(sys.argv[1])
for agent in ('page-alpha','page-beta'):
 # Managed projections require the corresponding durable agent identity.
 c.execute("INSERT INTO agents(agent_id,first_seen_at) VALUES(?,?)",(agent,'2026-01-01T00:00:00Z'))
 c.execute("INSERT INTO managed_agents(agent_id,name,cwd,selected_model_provider,selected_model_id,desired_state,lifecycle_state,activity_state,session_epoch,manifest_revision,manifest_hash,created_at,updated_at) VALUES(?,?,?,'mock','mock','running','running','idle',1,1,'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')",(agent,agent,sys.argv[2]))
for i in range(1,66):
 c.execute("INSERT INTO managed_complete_history(agent_id,category,level,message,occurred_at) VALUES('page-alpha','system','info',?,?)",(f'Managed only item {i:03d}',f'2026-01-01T00:{i//60:02d}:{i%60:02d}Z'))
c.execute("INSERT INTO managed_complete_history(agent_id,category,level,message,occurred_at) VALUES('page-beta','system','info','Older visible beta item','2026-01-01T00:00:00Z')")
for i in range(50):
 c.execute("INSERT INTO managed_complete_history(agent_id,category,level,message,occurred_at,presentation_json) VALUES('page-beta','system','info',?,?,?)",(f'Hidden beta item {i:03d}',f'2026-01-01T00:01:{i:02d}Z', '{"kind":"lifecycle","outcome":"success"}'))
c.commit()
`, router.databasePath, router.root]);
    const port = await allocatePort();
    const frontend = `http://127.0.0.1:${port}`;
    vite = spawn(resolve(repo, 'web/router-ui-react/node_modules/.bin/vite'), ['--host', '127.0.0.1', '--port', String(port), '--strictPort'], { cwd: join(repo, 'web/router-ui-react'), env: { ...process.env, HOME: join(router.root, 'managed-home') }, stdio: 'pipe' });
    await expect.poll(async () => { try { return (await fetch(frontend)).status; } catch { return 0; } }, { timeout: 20_000 }).toBe(200);
    const url = proxy.url;
    const unexpected: string[] = [];
    await page.route('**/*', async route => {
      if ([frontend, url].includes(new URL(route.request().url()).origin)) return route.continue();
      unexpected.push(route.request().url()); await route.abort();
    });
    await page.routeWebSocket('**/*', ws => {
      if ([new URL(url).host, new URL(frontend).host].includes(new URL(ws.url()).host)) ws.connectToServer();
      else { unexpected.push(ws.url()); ws.close(); }
    });
    await page.addInitScript(({ url }) => {
      localStorage.clear();
      localStorage.setItem('mecha.web.activeRouter', url);
      localStorage.setItem('mecha.web.routers', JSON.stringify([url]));
      localStorage.setItem('mecha.web.selectedPeers', JSON.stringify({ [`${url}\u001fuser`]: 'page-beta' }));
    }, { url });
    const pages: { before: string | null; count: number; next: number | null }[] = [];
    page.on('response', async response => {
      const u = new URL(response.url());
      if (u.origin === url && u.pathname === '/v1/history' && u.searchParams.get('agent_id') === 'page-alpha' && u.searchParams.get('view') === 'complete' && response.ok()) {
        const body = await response.json(); pages.push({ before: u.searchParams.get('before'), count: body.entries.length, next: body.next_before ?? null });
      }
    });
    await page.goto(frontend, { waitUntil: 'domcontentloaded', timeout: 20_000 });
    await page.locator('.agent-row, .agent-row-select').filter({ hasText: /page.alpha/i }).first().click();
    await expect(page.getByText('Managed only item 065', { exact: true })).toBeVisible();
    await expect(page.getByText('Managed only item 001', { exact: true })).toHaveCount(0);
    await expect.poll(() => pages.length).toBeGreaterThan(0);
    expect(pages[0]).toMatchObject({ before: null, count: 50 });
    expect(pages[0].next).not.toBeNull();
    const timeline = await (await fetch(`${url}/v1/conversation?agent_id=page-alpha&limit=50`)).text();
    expect(timeline).not.toContain('Managed only item');
    const anchor = page.getByText('Managed only item 030', { exact: true });
    await anchor.scrollIntoViewIfNeeded();
    const before = await anchor.boundingBox();
    await page.getByRole('button', { name: /load older history/i }).click();
    await expect(page.getByText('Managed only item 001', { exact: true })).toHaveCount(1);
    await expect.poll(() => pages.some(p => p.before !== null)).toBe(true);
    const after = await anchor.boundingBox();
    expect(Math.abs(after!.y - before!.y)).toBeLessThanOrEqual(3);
    for (let i = 1; i <= 65; i++) await expect(page.getByText(`Managed only item ${String(i).padStart(3, '0')}`, { exact: true })).toHaveCount(1);
    const betaInitial = page.waitForResponse(response => {
      const u = new URL(response.url());
      return u.origin === url && u.pathname === '/v1/history' && u.searchParams.get('agent_id') === 'page-beta' && u.searchParams.get('view') === 'complete' && !u.searchParams.has('before') && response.ok();
    }, { timeout: 10_000 });
    await page.locator('.agent-row-select').filter({ hasText: /page.beta/i }).first().click();
    const betaPage = await (await betaInitial).json();
    expect(betaPage.entries).toHaveLength(50);
    expect(betaPage.next_before).toBeTruthy();
    await expect(page.getByText(/Hidden beta item/)).toHaveCount(0);
    await expect(page.getByText('Older visible beta item', { exact: true })).toHaveCount(0);
    await page.getByRole('button', { name: /load older history/i }).click();
    await expect(page.getByText('Older visible beta item', { exact: true })).toBeVisible();
    expect(unexpected).toEqual([]);
  } finally {
    vite?.kill('SIGTERM');
    await proxy?.close();
    await router.close();
    await mock.close();
  }
});
