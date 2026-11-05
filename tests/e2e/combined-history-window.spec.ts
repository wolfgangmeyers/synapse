import { test, expect } from '@playwright/test';
import { execFile, spawn } from 'node:child_process';
import { promisify } from 'node:util';
import { resolve, join } from 'node:path';
import { allocatePort, startIsolatedRouter, startMockLLMServer, startRouterProxy } from './harness';

const exec = promisify(execFile);
const repo = resolve(import.meta.dirname, '../..');

for (const scenario of ['interleaved', 'tied', 'zero-contribution'] as const) test(`combined history refetches discarded candidates without gaps: ${scenario}`, async ({ page }, testInfo) => {
  test.setTimeout(90_000);
  const network: object[] = [];
  const browserErrors: object[] = [];
  const responseReads: Promise<void>[] = [];
  const relevant = (url: string) => {
    const path = new URL(url).pathname;
    return path === '/v1/history' || path === '/v1/conversation';
  };
  page.on('request', request => {
    if (relevant(request.url())) network.push({ event: 'request', url: request.url(), method: request.method() });
  });
  page.on('requestfailed', request => {
    if (relevant(request.url())) network.push({ event: 'requestfailed', url: request.url(), failure: request.failure() });
  });
  page.on('response', response => {
    if (!relevant(response.url())) return;
    const entry: { event: string; url: string; status: number; body?: string } = {
      event: 'response', url: response.url(), status: response.status(),
    };
    network.push(entry);
    responseReads.push(response.text().then(body => { entry.body = body; }, error => { entry.body = `Unable to read response: ${String(error)}`; }));
  });
  page.on('pageerror', error => browserErrors.push({ event: 'pageerror', message: error.stack || error.message }));
  page.on('console', message => {
    if (message.type() === 'error') browserErrors.push({ event: 'console', text: message.text() });
  });
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
    await page.locator('.agent-row, .agent-row-select').filter({ hasText: /combined.agent/i }).first().click();
    await expect(page.getByText('Activity item 39', { exact: true })).toBeVisible();
    if (scenario !== 'zero-contribution') await expect(page.getByText('Timeline item 39', { exact: true })).toBeVisible();
    if (scenario !== 'tied') await expect(page.getByText('Timeline item 00', { exact: true })).toHaveCount(0);
    await expect(page.locator('[data-scroll-anchor]')).toHaveCount(50);
    const olderRequests: string[] = [];
    page.on('request', (request) => {
      const url = new URL(request.url());
      if (url.pathname === '/v1/history' || url.pathname === '/v1/conversation') olderRequests.push(request.url());
    });
    await page.getByRole('button', { name: /load older history/i }).click();
    await expect(page.getByText('Timeline item 00', { exact: true })).toBeVisible();
    if (scenario !== 'zero-contribution') expect(olderRequests.some((url) => new URL(url).pathname === '/v1/history')).toBe(true);
    expect(olderRequests.some((url) => new URL(url).pathname === '/v1/conversation')).toBe(true);
    for (let i = 0; i < 40; i++) {
      const number = String(i).padStart(2, '0');
      await expect(page.getByText(`Timeline item ${number}`, { exact: true })).toHaveCount(1);
      await expect(page.getByText(`Activity item ${number}`, { exact: true })).toHaveCount(1);
    }
    await expect(page.locator('[data-scroll-anchor]')).toHaveCount(80);
    await expect(page.getByRole('button', { name: /load older history/i })).toHaveCount(0);
  } catch (error) {
    // Preserve the page and completed REST bodies before shutting down the servers.
    await Promise.race([Promise.allSettled(responseReads), new Promise(resolve => setTimeout(resolve, 2000))]);
    await testInfo.attach('history-network-and-browser-errors', {
      body: JSON.stringify({ network, browserErrors }, null, 2), contentType: 'application/json',
    });
    try {
      await testInfo.attach('failure-dom', { body: await page.content(), contentType: 'text/html' });
      await testInfo.attach('failure-screenshot', { body: await page.screenshot(), contentType: 'image/png' });
    } catch (captureError) {
      await testInfo.attach('failure-dom-capture-error', { body: String(captureError), contentType: 'text/plain' });
    }
    throw error;
  } finally {
    vite?.kill('SIGTERM');
    await proxy?.close();
    await router.close();
    await mock.close();
  }
});
