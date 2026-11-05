import { expect, test } from '@playwright/test';
import { WebSocket } from 'ws';
import { setupE2EHarness, type E2ETestHarness } from './harness';

test.describe('Conversation switch history isolation', () => {
  let harness: E2ETestHarness;
  const sockets: WebSocket[] = [];
  test.setTimeout(60_000);
  test.beforeAll(async () => { harness = await setupE2EHarness({ useProxy: true }); });
  test.afterAll(async () => { sockets.forEach(socket => socket.terminate()); await harness?.close(); });

  test('drops loaded history on switch, reconciles a delayed page and keeps live updates', async ({ page }) => {
    const url = harness.proxy?.url ?? harness.router.httpUrl;
    const a = 'agent-cache-alpha';
    const b = 'agent-cache-beta';
    const pending = new Set<string>();
    const leases = new Map<string, string>();
    async function connect(agent: string) {
      const batches = new Map<string, string[]>();
      let lease = '';
      const instance = `instance-${agent}`;
      const socket = new WebSocket(`${url.replace(/^http/, 'ws')}/v1/agents`);
      sockets.push(socket);
      await new Promise<void>((resolve, reject) => {
        socket.on('open', () => socket.send(JSON.stringify({
          version: 'v1', frame_id: `register-${agent}`, type: 'register', agent_id: agent,
          instance_id: `instance-${agent}`, agent_role: 'agent', client_type: 'test',
          ack_confirmation: 'correlated_v1', conversation_sync: 'v1', delivery_receipts: 'delivery_items_v1',
        })));
        socket.on('message', data => {
          const frame = JSON.parse(data.toString());
          if (frame.type === 'registered') { lease = frame.lease_id; leases.set(agent, lease); resolve(); }
          if (frame.type === 'message') {
            batches.set(frame.message.id, frame.delivery_items?.map((item: { message_id: string }) => item.message_id) ?? [frame.message.id]);
            const receipt = { version: 'v1', agent_id: agent, instance_id: instance, lease_id: lease,
              message_id: frame.message.id, attempt_number: frame.attempt_number,
              ...(frame.delivery_items ? { delivery_items: frame.delivery_items } : {}) };
            socket.send(JSON.stringify({ ...receipt, type: 'injection_requested', frame_id: `inj-${frame.message.id}` }));
            socket.send(JSON.stringify({ ...receipt, type: 'ack', frame_id: `ack-${frame.message.id}` }));
          }
          if (frame.type === 'delivery_acknowledged') {
            for (const id of batches.get(frame.message_id) ?? [frame.message_id]) pending.delete(id);
            batches.delete(frame.message_id);
          }
        });
        socket.on('error', reject);
      });
    }
    async function post(id: string, body: string, seed = false) {
      if (seed) pending.add(id);
      const response = await fetch(`${url}/v1/messages`, {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ id, from: seed ? 'user' : a, to: seed ? a : 'user', body, created_at: new Date().toISOString(), source: { kind: 'agent' } }),
      });
      expect([200, 201]).toContain(response.status);
    }
    await Promise.all([connect(a), connect(b)]);


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
    for (let i = 1; i <= 65; i++) {
      if (i % 10 === 0) {
        await harness.advanceClock(20_000);
        for (const [index, agent] of [a, b].entries()) sockets[index].send(JSON.stringify({ version: 'v1', type: 'lease_renew', frame_id: `renew-${agent}-${i}`, agent_id: agent, instance_id: `instance-${agent}`, lease_id: leases.get(agent) }));
      }
      await post(`cache-seed-${i}`, `Cache history item ${String(i).padStart(3, '0')}`, true);
      await expect.poll(() => pending.size).toBe(0);
    }
    // Seeded messages arrive live; switch away and back to load a fresh bounded page.
    await page.locator('.agent-row, .agent-row-select').filter({ hasText: 'Agent Cache Beta' }).first().click();
    await expect(page.locator('header h2', { hasText: 'Agent Cache Beta' })).toBeVisible();
    await page.locator('.agent-row, .agent-row-select').filter({ hasText: 'Agent Cache Alpha' }).first().click();
    await expect(page.locator('header h2', { hasText: 'Agent Cache Alpha' })).toBeVisible();
    const bubble = (text: string) => page.locator('.bubble-wrap').filter({ hasText: text });
    await expect(bubble('Cache history item 065')).toBeVisible();
    await expect(bubble('Cache history item 001')).toHaveCount(0);
    await page.getByRole('button', { name: /load older history/i }).click();
    await expect(bubble('Cache history item 001')).toBeVisible();

    await page.locator('.agent-row, .agent-row-select').filter({ hasText: 'Agent Cache Beta' }).first().click();
    await expect(page.locator('header h2', { hasText: 'Agent Cache Beta' })).toBeVisible();
    await post('cache-unselected', 'Activity while alpha is unselected');
    await expect(bubble('Activity while alpha is unselected')).toHaveCount(0);

    // Fetch the actual SQLite-backed page, but hold its delivery to the browser.
    // The live message posted after fetch is therefore absent from that snapshot.
    let release!: () => void;
    const gate = new Promise<void>(resolve => { release = resolve; });
    let pageFetched!: () => void;
    const fetched = new Promise<void>(resolve => { pageFetched = resolve; });
    let held = false;
    let snapshotContents: string[] = [];
    let delivered!: () => void;
    const delivery = new Promise<void>(resolve => { delivered = resolve; });
    await page.route('**/v1/conversation?*', async route => {
      const requestUrl = new URL(route.request().url());
      if (!held && requestUrl.searchParams.get('agent_id') === a && !requestUrl.searchParams.has('before_seq')) {
        held = true;
        const response = await route.fetch();
        const snapshot = await response.json();
        snapshotContents = snapshot.events.map((event: { content: string }) => event.content);
        pageFetched();
        await gate;
        await route.fulfill({ response });
        delivered();
      } else await route.continue();
    });
    await page.evaluate(() => {
      const seen: string[] = [];
      Object.assign(window, { __historyFlash: seen });
      new MutationObserver(records => {
        for (const record of records) {
          for (const node of record.addedNodes) if (node.textContent?.includes('Cache history item 001')) seen.push(node.textContent);
          if (record.type === 'characterData' && record.target.textContent?.includes('Cache history item 001')) seen.push(record.target.textContent);
        }
      }).observe(document.body, { subtree: true, childList: true, characterData: true });
    });
    try {
      await page.locator('.agent-row, .agent-row-select').filter({ hasText: 'Agent Cache Alpha' }).first().click();
      await expect(page.locator('header h2', { hasText: 'Agent Cache Alpha' })).toBeVisible();
      await Promise.race([fetched, new Promise<never>((_, reject) => setTimeout(() => reject(new Error('history request not observed')), 5000))]);
      expect(snapshotContents).toEqual([
        ...Array.from({ length: 49 }, (_, i) => `Cache history item ${String(i + 17).padStart(3, '0')}`),
        'Activity while alpha is unselected',
      ]);
      await expect.soft(bubble('Cache history item 001')).toHaveCount(0, { timeout: 1000 });
      await post('cache-during-page', 'Live while history page is delayed');
      await expect.soft(bubble('Live while history page is delayed')).toHaveCount(1, { timeout: 3000 });
    } finally {
      release();
    }
    await delivery;
    await expect(bubble('Cache history item 017')).toBeVisible();
    await expect(bubble('Cache history item 016')).toHaveCount(0);
    expect(await page.evaluate(() => Reflect.get(window, '__historyFlash'))).toEqual([]);
    await expect(bubble('Activity while alpha is unselected')).toHaveCount(1);
    await expect(bubble('Live while history page is delayed')).toHaveCount(1);
    await expect.soft(bubble('Cache history item 001')).toHaveCount(0, { timeout: 1000 });
    expect.soft(await page.locator('.bubble-wrap').count()).toBeLessThanOrEqual(51);
    await post('cache-after-page', 'Live after history page completes');
    await expect(bubble('Live after history page completes')).toHaveCount(1);
    await expect(bubble('Live while history page is delayed')).toHaveCount(1);
  });
});
