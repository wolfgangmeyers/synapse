import { test, expect } from '@playwright/test';
import { execFile, spawn } from 'node:child_process';
import { promisify } from 'node:util';
import { resolve, join } from 'node:path';
import { WebSocket } from 'ws';
import { allocatePort, startIsolatedRouter, startRouterProxy } from './harness';

const exec = promisify(execFile);
const repo = resolve(import.meta.dirname, '../..');

test('same-ID source switch refetches and fences the previous selection', async ({ page }) => {
  test.setTimeout(90_000);
  const router = await startIsolatedRouter({ repoRoot: repo, managed: true });
  let proxy: Awaited<ReturnType<typeof startRouterProxy>> | undefined;
  let vite: ReturnType<typeof spawn> | undefined;
  let socket: WebSocket | undefined;
  try {
    proxy = await startRouterProxy({ repoRoot: repo, upstreamPort: router.port });
    await exec('python3', ['-c', `
import sqlite3,sys
c=sqlite3.connect(sys.argv[1])
c.execute("INSERT INTO managed_agents(agent_id,name,cwd,selected_model_provider,selected_model_id,desired_state,lifecycle_state,activity_state,session_epoch,manifest_revision,manifest_hash,created_at,updated_at) VALUES('same-worker','same-worker',?,'mock','mock','running','running','idle',1,1,?,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')",(sys.argv[2],'a'*64))
c.execute("INSERT INTO conversation_timeline(id,agent_id,seq,kind,role,content,metadata,created_at) VALUES('shared-old','same-worker',1,'message','assistant','Original history','{}',1767225600000)")
c.commit()
`, router.databasePath, router.root]);
    const url = proxy.url;
    socket = new WebSocket(`${url.replace(/^http/, 'ws')}/v1/agents`);
    await new Promise<void>((done, reject) => {
      socket!.on('open', () => socket!.send(JSON.stringify({ version: 'v1', frame_id: 'register', type: 'register', agent_id: 'same-worker', instance_id: 'same-instance', agent_role: 'agent', client_type: 'test' })));
      socket!.on('message', data => { if (JSON.parse(data.toString()).type === 'registered') done(); });
      socket!.on('error', reject);
    });
    const port = await allocatePort();
    const frontend = `http://127.0.0.1:${port}`;
    vite = spawn(resolve(repo, 'web/router-ui-react/node_modules/.bin/vite'), ['--host', '127.0.0.1', '--port', String(port), '--strictPort'], { cwd: join(repo, 'web/router-ui-react'), stdio: 'pipe' });
    await expect.poll(async () => { try { return (await fetch(frontend)).status; } catch { return 0; } }, { timeout: 20_000 }).toBe(200);
    await page.addInitScript(({ url }) => { localStorage.clear(); localStorage.setItem('mecha.web.activeRouter', url); localStorage.setItem('mecha.web.routers', JSON.stringify([url])); }, { url });
    await page.goto(frontend);
    const rows = page.locator('.agent-row-select').filter({ hasText: /same.worker/i });
    await expect(rows).toHaveCount(2);
    await rows.nth(0).click();
    await expect(page.getByText('Original history', { exact: true })).toBeVisible();
    let release!: () => void;
    const gate = new Promise<void>(resolve => { release = resolve; });
    let observed!: () => void;
    const requested = new Promise<void>(resolve => { observed = resolve; });
    let rawPeer = '';
    await page.route('**/v1/conversation?*', async route => {
      const request = new URL(route.request().url());
      if (!request.searchParams.has('before_seq')) {
        rawPeer = request.searchParams.get('agent_id') ?? '';
        const response = await route.fetch();
        observed();
        await gate;
        await route.fulfill({ response });
      } else await route.continue();
    });
    try {
      await rows.nth(1).click();
      await Promise.race([requested, new Promise<never>((_, reject) => setTimeout(() => reject(new Error('source switch did not refetch')), 5000))]);
      expect(rawPeer).toBe('same-worker');
      await expect(page.getByText('Original history', { exact: true })).toHaveCount(0);
    } finally { release(); }
    await expect(page.getByText('Original history', { exact: true })).toBeVisible();
  } finally {
    socket?.terminate(); vite?.kill('SIGTERM'); await proxy?.close(); await router.close();
  }
});
