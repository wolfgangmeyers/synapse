import { test, expect } from '@playwright/test';
import { execFile, spawn } from 'node:child_process';
import { promisify } from 'node:util';
import { resolve, join } from 'node:path';
import { allocatePort, startIsolatedRouter, startMockLLMServer, startRouterProxy } from './harness';

const exec = promisify(execFile);
const repo = resolve(import.meta.dirname, '../..');

test('two source pages preserve the visible anchor when the second response is delayed', async ({ page }) => {
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
for i in range(80):
 c.execute("INSERT INTO conversation_timeline(id,agent_id,seq,kind,role,content,metadata,created_at) VALUES(?, 'combined-agent', ?, 'message', 'assistant', ?, '{}', ?)",(f'timeline-{i}',i+1,f'Timeline item {i:02d}',1767225600000+i*2000))
 c.execute("INSERT INTO managed_complete_history(agent_id,category,level,message,occurred_at) VALUES('combined-agent','system','info',?,?)",(f'Activity item {i:02d}',f'2026-01-01T00:{(i*2+1)//60:02d}:{(i*2+1)%60:02d}Z'))
c.commit()
`, router.databasePath, router.root]);
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
    await expect(page.getByText('Activity item 79', { exact: true })).toBeVisible();
    await expect(page.getByText('Timeline item 79', { exact: true })).toBeVisible();
    // Reveal the initially deferred combined window before requesting older source pages.
    await page.getByRole('button', { name: /load older history/i }).click();
    await expect(page.getByText('Timeline item 30', { exact: true })).toHaveCount(1);
    const anchor = page.getByText('Timeline item 55', { exact: true });
    await anchor.scrollIntoViewIfNeeded();
    const before = await anchor.boundingBox();
    let release!: () => void;
    const gate = new Promise<void>(resolve => { release = resolve; });
    let observed!: () => void;
    const requested = new Promise<void>(resolve => { observed = resolve; });
    let historyRequests = 0;
    let timelineRequests = 0;
    await page.route('**/*', async route => {
      const u = new URL(route.request().url());
      if (u.origin === url && u.pathname === '/v1/conversation' && u.searchParams.has('before_seq')) timelineRequests++;
      if (u.origin === url && u.pathname === '/v1/history' && u.searchParams.has('before')) {
        historyRequests++;
        const response = await route.fetch();
        observed();
        await gate;
        await route.fulfill({ response });
      } else await route.continue();
    });
    try {
      await page.getByRole('button', { name: /load older history/i }).click();
      await Promise.race([requested, new Promise<never>((_, reject) => setTimeout(() => reject(new Error('older activity request not observed')), 10000))]);
      await expect(page.getByText('Timeline item 00', { exact: true })).toHaveCount(1);
      await expect(page.getByText('Activity item 00', { exact: true })).toHaveCount(0);
      // Keep the second response gated until the first prepend and viewport stop moving.
      await expect.poll(async () => {
        return anchor.evaluate(async (element, originalY) => {
          const positions: number[] = [];
          for (let i = 0; i < 10; i++) {
            await new Promise<void>(resolve => setTimeout(resolve, 50));
            positions.push(element.getBoundingClientRect().y);
          }
          return Math.max(
            Math.abs(positions[positions.length - 1] - originalY),
            Math.max(...positions) - Math.min(...positions),
          );
        }, before!.y);
      }, { timeout: 10_000 }).toBeLessThanOrEqual(3);
    } finally { release(); }
    await expect(page.getByText('Activity item 00', { exact: true })).toHaveCount(1);
    await expect.poll(async () => {
      const after = await anchor.boundingBox();
      return Math.abs(after!.y - before!.y);
    }).toBeLessThanOrEqual(3);
    expect(timelineRequests).toBe(1);
    expect(historyRequests).toBe(1);
    for (const i of [0, 30, 55, 79]) {
      await expect(page.getByText(`Timeline item ${String(i).padStart(2, '0')}`, { exact: true })).toHaveCount(1);
      await expect(page.getByText(`Activity item ${String(i).padStart(2, '0')}`, { exact: true })).toHaveCount(1);
    }
  } finally {
    vite?.kill('SIGTERM');
    await proxy?.close();
    await router.close();
    await mock.close();
  }
});
