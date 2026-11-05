import { expect, test, type Page } from '@playwright/test';
import { WebSocket } from 'ws';
import { setupE2EHarness, type E2ETestHarness } from './harness';

const alpha = 'agent-send-alpha', beta = 'agent-send-beta';
const gate = () => {
  let release!: () => void;
  const promise = new Promise<void>(resolve => { release = resolve; });
  return { promise, release };
};
const bubble = (page: Page, text: string) => page.locator('.bubble-wrap').filter({ hasText: text });

test.describe('Pending send real-boundary regression', () => {
  let h: E2ETestHarness;
  let url: string;
  let sockets: WebSocket[];
  test.setTimeout(60_000);
  test.beforeEach(async () => {
    h = await setupE2EHarness({ useProxy: true }); url = h.proxy!.url; sockets = [];
    await Promise.all([alpha, beta].map(async agent => {
      const socket = new WebSocket(`${url.replace(/^http/, 'ws')}/v1/agents`); sockets.push(socket);
      await new Promise<void>((resolve, reject) => {
        socket.on('open', () => socket.send(JSON.stringify({ version: 'v1', frame_id: `register-${agent}`, type: 'register', agent_id: agent, instance_id: `instance-${agent}`, agent_role: 'agent', client_type: 'test', ack_confirmation: 'correlated_v1', conversation_sync: 'v1', delivery_receipts: 'delivery_items_v1' })));
        socket.on('message', data => { if (JSON.parse(data.toString()).type === 'registered') resolve(); });
        socket.on('error', reject);
      });
    }));
  });
  test.afterEach(async () => { sockets?.forEach(s => s.terminate()); await h?.close(); });

  async function select(page: Page, name: 'Alpha' | 'Beta') {
    await page.locator('.agent-row, .agent-row-select').filter({ hasText: `Agent Send ${name}` }).first().click();
    await expect(page.locator('header h2', { hasText: `Agent Send ${name}` })).toBeVisible();
  }
  async function start(page: Page) {
    const response = await fetch(`${url}/v1/messages`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ id: 'old-alpha', from: alpha, to: 'user', body: 'Old alpha history', created_at: new Date().toISOString(), source: { kind: 'agent' } }) });
    expect([200, 201]).toContain(response.status);
    const unexpected: string[] = [];
    const allowed = new Set([new URL(h.frontend.url).origin, new URL(url).origin]);
    await page.route('**/*', async route => {
      if (allowed.has(new URL(route.request().url()).origin)) return route.fallback();
      unexpected.push(route.request().url());
      await route.abort();
      expect(unexpected).toEqual([]);
    });
    await page.routeWebSocket('**/*', ws => {
      if (new URL(ws.url()).host === new URL(url).host) ws.connectToServer();
      else { unexpected.push(ws.url()); ws.close(); expect(unexpected).toEqual([]); }
    });

    await page.addInitScript(({ url, alpha }) => {
      localStorage.setItem('mecha.web.activeRouter', url);
      localStorage.setItem('mecha.web.routers', JSON.stringify([url]));
      localStorage.setItem('mecha.web.selectedPeers', JSON.stringify({ [`${url}\u001fuser`]: alpha }));
    }, { url, alpha });
    await page.goto(h.frontend.url);
    await expect(bubble(page, 'Old alpha history')).toBeVisible();
  }

  test('accepted image remains available from the thumbnail cache after returning to history', async ({ page }) => {
    await start(page);
    // A tiny PNG, converted by the real composer and cached through IndexedDB.
    const png = await page.evaluate(() => { const canvas = document.createElement('canvas'); canvas.width = 2; canvas.height = 2; return canvas.toDataURL('image/png').split(',')[1]; });
    await page.locator("input[accept='image/*']").setInputFiles({ name: 'test.png', mimeType: 'image/png', buffer: Buffer.from(png, 'base64') });
    await expect(page.locator('.image-preview, .image-preview-item').first()).toBeVisible();
    await page.locator('textarea').first().fill('cached picture');
    await page.locator('textarea').first().press('Enter');
    const sent = bubble(page, 'cached picture');
    await expect(sent).toHaveCount(1);
    await expect.poll(async () => page.evaluate(async () => {
      const dbs = await indexedDB.databases();
      for (const { name } of dbs) {
        if (!name) continue;
        const db = await new Promise<IDBDatabase>((resolve, reject) => { const req = indexedDB.open(name); req.onsuccess = () => resolve(req.result); req.onerror = () => reject(req.error); });
        for (const store of Array.from(db.objectStoreNames)) {
          const count = await new Promise<number>((resolve) => { const req = db.transaction(store).objectStore(store).count(); req.onsuccess = () => resolve(req.result); req.onerror = () => resolve(0); });
          if (/image/i.test(store) && count > 0) { db.close(); return count; }
        }
        db.close();
      }
      return 0;
    })).toBeGreaterThan(0);
    await select(page, 'Beta'); await select(page, 'Alpha');
    await expect(bubble(page, 'cached picture').locator('img')).toHaveAttribute('src', /^blob:/);
  });

  for (const outcome of ['accepted', 'failure'] as const) {
    test(`${outcome} response after switching preserves correct send state without stale history`, async ({ page }) => {
      await start(page);
      const arrived = gate(), release = gate(), completed = gate(), historyArrived = gate(), historyRelease = gate(), historyCompleted = gate();
      const text = `Boundary send ${outcome}`;
      let requests = 0;
      await page.route('**/v1/messages', async route => {
        if (new URL(route.request().url()).origin !== url) return route.fallback();
        if (route.request().method() !== 'POST' || !route.request().postData()?.includes(text)) return route.fallback();
        requests++;
        // For success the real proxy/router/SQLite accept the message before the
        // response is held. For failure no write reaches the router.
        const response = outcome === 'accepted' ? await route.fetch() : undefined;
        if (response) expect([200, 201]).toContain(response.status());
        arrived.release();
        await release.promise;
        try {
          if (response) await route.fulfill({ response });
          else await route.fulfill({ status: 503, contentType: 'application/json', body: '{"error":"send unavailable"}' });
        } finally { completed.release(); }
      });
      let holdHistory = true;
      await page.route('**/v1/conversation?*', async route => {
        if (new URL(route.request().url()).origin !== url) return route.fallback();
        const query = new URL(route.request().url()).searchParams;
        if (!holdHistory || query.get('agent_id') !== alpha || query.has('before_seq')) return route.fallback();
        const response = await route.fetch();
        historyArrived.release();
        await historyRelease.promise;
        await route.fulfill({ response });
        historyCompleted.release();
      });
      try {
        await page.locator('textarea').first().fill(text);
        await page.locator('textarea').first().press('Enter');
        await arrived.promise;
        await expect(bubble(page, text)).toHaveCount(1);
        await select(page, 'Beta');
        await expect(bubble(page, text)).toHaveCount(0);
        await expect(bubble(page, 'Old alpha history')).toHaveCount(0);
        await select(page, 'Alpha');
        await historyArrived.promise;
        await expect(bubble(page, 'Old alpha history')).toHaveCount(0);
        await expect(bubble(page, text)).toHaveCount(1);
        release.release(); await completed.promise;
        if (outcome === 'failure') await expect(bubble(page, text).getByText('Failed', { exact: true })).toBeVisible();
        holdHistory = false;
        historyRelease.release(); await historyCompleted.promise;
        await expect(bubble(page, 'Old alpha history')).toBeVisible();
        await expect(bubble(page, text)).toHaveCount(1);
        if (outcome === 'failure') await expect(bubble(page, text).getByText('Failed', { exact: true })).toBeVisible();
        else {
          await expect(bubble(page, text).getByText('Sending…', { exact: true })).toHaveCount(0);
          await select(page, 'Beta');
          await expect(bubble(page, text)).toHaveCount(0);
          await select(page, 'Alpha');
          await expect(bubble(page, text)).toHaveCount(1);
        }
        expect(requests).toBe(1);
      } finally { release.release(); historyRelease.release(); }
    });
  }
});
