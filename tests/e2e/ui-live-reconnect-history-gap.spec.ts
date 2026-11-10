import { expect, test, type WebSocketRoute } from '@playwright/test';
import { execFileSync } from 'node:child_process';
import { setupE2EHarness } from './harness';

// Run via `npm run test:e2e -- tests/e2e/ui-live-reconnect-history-gap.spec.ts`:
// its pretest:e2e hook builds the frontend served by the harness.
test('reconnect fetches history after the first UI-live snapshot', async ({ page }) => {
  test.setTimeout(90_000);
  const h = await setupE2EHarness({ useProxy: true, managed: true });
  const url = h.proxy!.url;
  const agent = 'gap-agent';
  const sql = (code: string) => execFileSync('python3', ['-c', code, h.router.databasePath, h.router.root]);
  let socket: WebSocketRoute | undefined;
  let connections = 0;
  let heldSnapshot: string | Buffer | undefined;
  let releaseSnapshot: (() => void) | undefined;
  const order: string[] = [];
  let initialHistory = false;
  const unexpected: string[] = [];
  try {
    sql(`import sqlite3,sys
c=sqlite3.connect(sys.argv[1])
c.execute("INSERT INTO agents(agent_id,first_seen_at) VALUES('gap-agent','2026-01-01T00:00:00Z')")
c.execute("INSERT INTO managed_agents(agent_id,name,cwd,selected_model_provider,selected_model_id,desired_state,lifecycle_state,activity_state,session_epoch,manifest_revision,manifest_hash,created_at,updated_at) VALUES('gap-agent','gap-agent',?,'mock','mock','running','running','idle',1,1,?,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')",(sys.argv[2],'a'*64))
c.commit()`);
    await page.route('**/*', async route => {
      const u = new URL(route.request().url());
      if (![url, h.frontend.url].includes(u.origin)) { unexpected.push(u.href); return route.abort(); }
      if (u.origin === url && u.pathname === '/v1/history' && u.searchParams.get('agent_id') === agent) {
        if (connections >= 2) order.push('history');
      }
      await route.continue();
    });
    page.on('response', response => {
      const u = new URL(response.url());
      if (connections === 1 && response.ok() && u.origin === url && u.pathname === '/v1/history' && u.searchParams.get('agent_id') === agent) initialHistory = true;
    });
    await page.routeWebSocket('**/*', ws => {
      const u = new URL(ws.url());
      if (u.host !== new URL(url).host && u.host !== new URL(h.frontend.url).host) { unexpected.push(ws.url()); ws.close(); return; }
      const server = ws.connectToServer();
      if (u.pathname !== '/v1/ui-live') return;
      socket = ws;
      const generation = ++connections;
      if (generation !== 2) return;
      const queued: (string | Buffer)[] = [];
      server.onMessage(message => {
        if (!heldSnapshot && JSON.parse(message.toString()).type === 'managed_agents_snapshot') {
          heldSnapshot = message;
          order.push('snapshot-held');
          releaseSnapshot = () => { order.push('snapshot-released'); ws.send(message); for (const item of queued) ws.send(item); queued.length = 0; };
        } else if (heldSnapshot && !order.includes('snapshot-released')) queued.push(message);
        else ws.send(message);
      });
    });
    await page.addInitScript(({ url, agent }) => {
      // Observe fetch at its browser call site, not at the asynchronously delivered
      // Playwright route. The timer marks the end of the reconnect open event and
      // its microtasks; it does not exclude requests scheduled for a later task.
      const evidence = { opens: 0, openTaskFinished: false, historyBeforeBarrier: 0 };
      (window as any).__gapEvidence = evidence;
      const NativeWebSocket = window.WebSocket;
      window.WebSocket = class extends NativeWebSocket {
        constructor(socketUrl: string | URL, protocols?: string | string[]) {
          super(socketUrl, protocols);
          if (new URL(socketUrl, location.href).pathname === '/v1/ui-live') {
            this.addEventListener('open', () => {
              if (++evidence.opens === 2) setTimeout(() => { evidence.openTaskFinished = true; }, 0);
            });
          }
        }
      };
      const nativeFetch = window.fetch;
      window.fetch = function(input, init) {
        const requestUrl = new URL(input instanceof Request ? input.url : input.toString(), location.href);
        if (evidence.opens >= 2 && !evidence.openTaskFinished && requestUrl.origin === url &&
            requestUrl.pathname === '/v1/history' && requestUrl.searchParams.get('agent_id') === agent) {
          evidence.historyBeforeBarrier++;
        }
        return nativeFetch.call(this, input, init);
      };
      localStorage.setItem('mecha.web.activeRouter', url);
      localStorage.setItem('mecha.web.routers', JSON.stringify([url]));
      localStorage.setItem('mecha.web.selectedPeers', JSON.stringify({ [`${url}\u001fuser`]: agent }));
    }, { url, agent });
    await page.goto(h.frontend.url);
    await expect(page.locator('.agent-row, .agent-row-select').filter({ hasText: /gap.agent/i }).first()).toBeVisible();
    await page.locator('.agent-row, .agent-row-select').filter({ hasText: /gap.agent/i }).first().click();
    await expect.poll(() => connections).toBe(1);
    await expect.poll(() => socket !== undefined).toBe(true);
    await expect.poll(() => initialHistory).toBe(true);
    // Close only UI-live: the selected conversation and compatibility transport stay open.
    socket!.close({ code: 1000, reason: 'test reconnect' });
    await expect.poll(() => !!releaseSnapshot, { timeout: 15000 }).toBe(true);
    // Simulate a durable history row with no live event; this does not exercise a production write path.
    sql(`import sqlite3,sys
c=sqlite3.connect(sys.argv[1])
c.execute("INSERT INTO managed_complete_history(agent_id,category,level,message,occurred_at) VALUES('gap-agent','system','info','Persisted in subscription gap','2026-01-01T00:00:01Z')")
c.commit()`);
    // The second open callback and its microtasks have run while the snapshot is
    // held. Inspect browser-side fetch calls through that boundary; route delivery
    // alone cannot establish when the page initiated a request.
    await expect.poll(() => page.evaluate(() => (window as any).__gapEvidence.openTaskFinished)).toBe(true);
    expect(await page.evaluate(() => (window as any).__gapEvidence.historyBeforeBarrier)).toBe(0);
    expect(order).toEqual(['snapshot-held']);
    releaseSnapshot!();
    await expect.poll(() => order.includes('history')).toBe(true);
    expect(order.indexOf('history')).toBeGreaterThan(order.indexOf('snapshot-released'));
    await expect(page.getByText('Persisted in subscription gap', { exact: true })).toBeVisible();
    expect(unexpected).toEqual([]);
  } finally {
    releaseSnapshot?.();
    await h.close();
  }
});
