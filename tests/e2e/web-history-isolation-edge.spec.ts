import { expect, test, type Page, type Route } from '@playwright/test';
import { WebSocket } from 'ws';
import { setupE2EHarness, type E2ETestHarness } from './harness';

const a = 'agent-edge-alpha';
const b = 'agent-edge-beta';
const bubble = (page: Page, text: string) => page.locator('.bubble-wrap').filter({ hasText: text });
const gate = () => {
  let release!: () => void;
  const promise = new Promise<void>(resolve => { release = resolve; });
  return { promise, release };
};

test.describe('Fresh history isolation edge cases', () => {
  let harness: E2ETestHarness;
  let url: string;
  let sockets: WebSocket[] = [];
  test.setTimeout(60_000);
  test.beforeEach(async () => {
    harness = await setupE2EHarness({ useProxy: true });
    url = harness.proxy!.url;
    sockets = [];
    await Promise.all([a, b].map(async agent => {
      const socket = new WebSocket(`${url.replace(/^http/, 'ws')}/v1/agents`);
      sockets.push(socket);
      await new Promise<void>((resolve, reject) => {
        socket.on('open', () => socket.send(JSON.stringify({ version: 'v1', frame_id: `register-${agent}`, type: 'register', agent_id: agent, instance_id: `instance-${agent}`, agent_role: 'agent', client_type: 'test', ack_confirmation: 'correlated_v1', conversation_sync: 'v1', delivery_receipts: 'delivery_items_v1' })));
        socket.on('message', data => { if (JSON.parse(data.toString()).type === 'registered') resolve(); });
        socket.on('error', reject);
      });
    }));
  });
  test.afterEach(async () => { sockets.forEach(socket => socket.terminate()); await harness?.close(); });

  async function post(agent: string, id: string, body: string) {
    const response = await fetch(`${url}/v1/messages`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ id, from: agent, to: 'user', body, created_at: new Date().toISOString(), source: { kind: 'agent' } }) });
    expect([200, 201]).toContain(response.status);
  }
  async function start(page: Page) {
    const unexpected: string[] = [];
    const allowed = new Set([new URL(harness.frontend.url).origin, new URL(url).origin]);
    await page.route('**/*', async route => {
      if (allowed.has(new URL(route.request().url()).origin)) return route.continue();
      unexpected.push(route.request().url());
      await route.abort();
      expect(unexpected).toEqual([]);
    });
    await page.routeWebSocket('**/*', ws => {
      if (new URL(ws.url()).host === new URL(url).host) ws.connectToServer();
      else { unexpected.push(ws.url()); ws.close(); expect(unexpected).toEqual([]); }
    });

    await page.addInitScript(({ url, a }) => {
      localStorage.setItem('mecha.web.activeRouter', url);
      localStorage.setItem('mecha.web.routers', JSON.stringify([url]));
      localStorage.setItem('mecha.web.selectedPeers', JSON.stringify({ [`${url}\u001fuser`]: a }));
    }, { url, a });
    await page.goto(harness.frontend.url);
    await expect(page.locator('header h2', { hasText: 'Agent Edge Alpha' })).toBeVisible();
  }
  async function select(page: Page, name: 'Alpha' | 'Beta') {
    await page.locator('.agent-row, .agent-row-select').filter({ hasText: `Agent Edge ${name}` }).first().click();
    await expect(page.locator('header h2', { hasText: `Agent Edge ${name}` })).toBeVisible();
  }

  test('stalled initial timeline retries and keeps live content on an empty response', async ({ page }) => {
    await start(page);
    await select(page, 'Beta');
    const held = gate();
    let blocked = true;
    let attempts = 0;
    await page.route('**/v1/conversation?*', async route => {
      const request = new URL(route.request().url());
      if (request.origin !== url || request.searchParams.get('agent_id') !== a || request.searchParams.has('before_seq')) return route.fallback();
      attempts++;
      if (blocked) {
        await held.promise;
        try { await route.continue(); } catch { /* request timed out */ }
      } else await route.continue();
    });
    try {
      await select(page, 'Alpha');
      await post(a, 'retry-live', 'Live during stalled history');
      await expect(bubble(page, 'Live during stalled history')).toBeVisible();
      await expect(page.getByRole('button', { name: 'Retry history' })).toBeVisible({ timeout: 20000 });
      blocked = false;
      await page.getByRole('button', { name: 'Retry history' }).click();
      await expect.poll(() => attempts).toBeGreaterThan(1);
      await expect(bubble(page, 'Live during stalled history')).toHaveCount(1);
    } finally { held.release(); }
  });

  test('rapid A-B-A ignores both delayed snapshots while preserving current live activity', async ({ page }) => {
    await post(a, 'edge-old-a', 'Alpha original history');
    await post(b, 'edge-old-b', 'Beta original history');
    await start(page);
    await expect(bubble(page, 'Alpha original history')).toBeVisible();
    await select(page, 'Beta');
    await expect(bubble(page, 'Beta original history')).toBeVisible();

    const oldA = gate(), oldB = gate(), gotA = gate(), gotB = gate();
    const completedA = gate(), completedB = gate();
    let holdA = true, holdB = true;
    await page.route('**/v1/conversation?*', async route => {
      if (new URL(route.request().url()).origin !== url) return route.fallback();
      const id = new URL(route.request().url()).searchParams.get('agent_id');
      const selected = id === a && holdA ? 'a' : id === b && holdB ? 'b' : '';
      if (!selected || new URL(route.request().url()).searchParams.has('before_seq')) return route.fallback();
      const response = await route.fetch();
      (selected === 'a' ? gotA : gotB).release();
      await (selected === 'a' ? oldA : oldB).promise;
      try { await route.fulfill({ response }); } catch { /* navigation may cancel obsolete requests */ }
      (selected === 'a' ? completedA : completedB).release();
    });
    try {
      await select(page, 'Alpha');
      await gotA.promise;
      await select(page, 'Beta');
      await gotB.promise;
      await post(a, 'edge-new-a', 'Alpha latest snapshot');
      // Subsequent A requests belong to the new selection, not the stale batch.
      holdA = false;
      await select(page, 'Alpha');
      await expect(bubble(page, 'Alpha latest snapshot')).toBeVisible();
      await post(a, 'edge-live-a', 'Alpha live after latest');
      await expect(bubble(page, 'Alpha live after latest')).toHaveCount(1);
      oldB.release(); await completedB.promise;
      oldA.release(); await completedA.promise;
      await expect(bubble(page, 'Beta original history')).toHaveCount(0);
      await expect(bubble(page, 'Alpha latest snapshot')).toHaveCount(1);
      await expect(bubble(page, 'Alpha live after latest')).toHaveCount(1);
    } finally { oldA.release(); oldB.release(); }
  });

  for (const outcome of ['failure', 'empty'] as const) {
    test(`${outcome} fresh history never restores cached history and keeps live events`, async ({ page }) => {
      await post(a, `edge-${outcome}-old`, `Old ${outcome} history`);
      await start(page);
      await expect(bubble(page, `Old ${outcome} history`)).toBeVisible();
      await select(page, 'Beta');
      const fetched = gate(), resume = gate(), finished = gate();
      await page.route('**/v1/conversation?*', async (route: Route) => {
        if (new URL(route.request().url()).origin !== url) return route.fallback();
        const query = new URL(route.request().url()).searchParams;
        if (query.get('agent_id') !== a || query.has('before_seq')) return route.fallback();
        const response = await route.fetch();
        const json = await response.json();
        fetched.release();
        await resume.promise;
        if (outcome === 'failure') await route.fulfill({ status: 503, contentType: 'application/json', body: '{}' });
        else await route.fulfill({ response, json: { ...json, events: [], next_before_seq: null } });
        finished.release();
      });
      // A failed timeline request falls back to the legacy endpoint; fail that too.
      if (outcome === 'failure') await page.route('**/v1/conversations?*', route => {
        if (new URL(route.request().url()).origin !== url) return route.fallback();
        const query = new URL(route.request().url()).searchParams;
        return query.get('peer') === a ? route.fulfill({ status: 503, contentType: 'application/json', body: '{}' }) : route.fallback();
      });
      try {
        await select(page, 'Alpha');
        await fetched.promise;
        await expect(bubble(page, `Old ${outcome} history`)).toHaveCount(0);
        await post(a, `edge-${outcome}-live`, `Live during ${outcome} request`);
        await expect(bubble(page, `Live during ${outcome} request`)).toHaveCount(1);
        resume.release(); await finished.promise;
        if (outcome === 'failure') await expect(page.locator('.conversation-content [role=alert]')).toBeVisible();
        else await expect(page.locator('.timeline-loading-skeleton')).toHaveCount(0);
        await expect(bubble(page, `Old ${outcome} history`)).toHaveCount(0);
        await expect(bubble(page, `Live during ${outcome} request`)).toHaveCount(1);
        await post(a, `edge-${outcome}-later`, `Live after ${outcome} request`);
        await expect(bubble(page, `Live after ${outcome} request`)).toHaveCount(1);
        await expect(bubble(page, `Old ${outcome} history`)).toHaveCount(0);
      } finally { resume.release(); }
    });
  }
});
