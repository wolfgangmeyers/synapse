import { test, expect, type Page } from '@playwright/test';
import { WebSocket } from 'ws';
import { setupE2EHarness, type E2ETestHarness } from './harness';

// SPEC-CONV-003: real REST/proxy/router/SQLite/WebSocket/browser boundaries.
// Only pagination and image HTTP responses are controlled, never UI internals.
test.describe('Persistent scroll regressions', () => {
  test.setTimeout(60_000);
  let h: E2ETestHarness;
  let url: string;
  let serial = 0;
  let browserPage: Page;
  let browserRegistered = false;
  let browserRenewals = 0;
  const browserErrors: string[] = [];
  const sockets = new Map<string, WebSocket>();
  const renewals: (() => Promise<void>)[] = [];
  // Install before sending: the router can confirm delivery before POST returns.
  // Each waiter owns its listeners/timer and fails immediately on transport loss.
  function frame(ws: WebSocket, matches: (f: Record<string, unknown>) => boolean, label: string) {
    return new Promise<void>((resolve, reject) => {
      const cleanup = () => {
        clearTimeout(timer);
        ws.off('message', message); ws.off('error', error); ws.off('close', close);
      };
      const finish = (failure?: Error) => { cleanup(); if (failure) reject(failure); else resolve(); };
      const error = (failure: Error) => finish(failure);
      const close = () => finish(new Error(`${label}: recipient socket closed`));
      const message = (raw: import('ws').RawData) => {
        try {
          const f = JSON.parse(raw.toString());
          if (f.type === 'error') finish(new Error(`${label}: ${JSON.stringify(f)}`));
          else if (matches(f)) finish();
        } catch (failure) { finish(failure instanceof Error ? failure : new Error(String(failure))); }
      };
      const timer = setTimeout(() => finish(new Error(`${label}: no confirmation within 5000ms`)), 5000);
      ws.on('message', message); ws.on('error', error); ws.on('close', close);
      if (ws.readyState === WebSocket.CLOSING || ws.readyState === WebSocket.CLOSED) close();
    });
  }
  // Each case gets its own router clock/store. Renew every recipient after each
  // clock advance and await the correlated response before advancing again.
  test.beforeEach(async ({ page }) => {
    browserPage = page; browserRegistered = false; browserRenewals = 0; browserErrors.length = 0;
    // Advance the browser's real renewal timer alongside the accelerated router.
    // Do not replace WebSocket, fabricate renewals, or extend/disable leases.
    await page.clock.install();
    page.on('websocket', ws => {
      if (ws.url() !== `${url.replace(/^http/, 'ws')}/v1/agents`) return;
      const pending = new Set<string>();
      ws.on('framesent', event => {
        const f = JSON.parse(String(event.payload));
        if (f.type === 'lease_renew') pending.add(f.frame_id);
      });
      ws.on('framereceived', event => {
        const f = JSON.parse(String(event.payload));
        if (f.type === 'registered' && f.agent_id === 'user') {
          browserRegistered = true;
          if (pending.delete(f.request_id)) browserRenewals++;
        }
        if (f.type === 'error' || f.type === 'lease_rejected') browserErrors.push(JSON.stringify(f));
      });
      ws.on('close', () => { browserRegistered = false; });
    });
    renewals.length = 0; sockets.clear();
    const started = performance.now();
    h = await setupE2EHarness({ useProxy: true }); url = h.proxy!.url;
    console.log(`SETUP: ${(performance.now() - started).toFixed(0)}ms`);
  });
  test.afterEach(async () => { sockets.forEach(s => s.terminate()); await h?.close(); });
  async function postBatch(agent: string, bodies: string[]) {
    // Ten outstanding rows < Store's 15-row recipient cap. A 20s clock step
    // admits at most 30 sends/minute and stays below the 30s recipient lease.
    // Advance only after the previous batch's durable ACK has completed.
    await h.advanceClock(20_000);
    await Promise.all(renewals.map(renew => renew()));
    if (browserRegistered) {
      const prior = browserRenewals;
      await browserPage.clock.fastForward(10_000);
      await expect.poll(() => browserRenewals, { message: 'browser user lease renewal confirmed by router' }).toBeGreaterThan(prior);
    }
    expect(browserErrors, 'browser transport must not expire during scroll assertions').toEqual([]);
    const ws = sockets.get(agent);
    if (!ws) throw new Error(`No recipient socket for ${agent}`);
    const messages = bodies.map(body => ({ id: `scroll-reg-${++serial}`, body }));
    const pending = new Set(messages.map(message => message.id));
    const batches = new Map<string, string[]>();
    // ClaimNextMessage can merge rows. The router confirms the batch head,
    // after AcknowledgeDelivery has settled every supplied delivery item.
    const acknowledged = frame(ws, f => {
      if (f.type === 'message') {
        const message = f.message;
        if (!message || typeof message !== 'object' || !('id' in message) || typeof message.id !== 'string') {
          throw new Error('delivery missing message ID');
        }
        const items = f.delivery_items;
        const ids = items === undefined ? [message.id] : Array.isArray(items) ? items.map(item => {
          if (!item || typeof item !== 'object' || typeof item.message_id !== 'string') {
            throw new Error('delivery missing item ID');
          }
          return item.message_id;
        }) : [];
        if (!ids.length) throw new Error('delivery missing items');
        batches.set(message.id, ids);
      }
      if (f.type === 'delivery_acknowledged') {
        const head = String(f.message_id);
        for (const id of batches.get(head) ?? [head]) pending.delete(id);
        batches.delete(head);
      }
      return pending.size === 0;
    }, `router confirmed batch ack for ${messages.map(message => message.id).join(', ')}`);
    await Promise.all([acknowledged, (async () => {
      // Preserve history admission order (trim/anchor cases depend on it),
      // but do not serialize POSTs behind per-message delivery/receipt waits.
      for (const { id, body } of messages) {
        const r = await fetch(`${url}/v1/messages`, { method: 'POST', headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ id, from: 'user', to: agent, body,
            created_at: new Date().toISOString(), source: { kind: 'agent' } }) });
        expect([200, 201], await r.text()).toContain(r.status);
      }
    })()]);
  }
  async function post(agent: string, body: string) { await postBatch(agent, [body]); }
  async function seed(agent: string, count = 60) {
    const ws = new WebSocket(`${url.replace(/^http/, 'ws')}/v1/agents`); sockets.set(agent, ws);
    let lease = '';
    const receipted = new Set<string>();
    ws.on('message', raw => {
      const f = JSON.parse(raw.toString());
      if (f.type === 'registered') lease = f.lease_id;
      if (f.type === 'message') {
        // A concurrent enqueue offer may repeat the active batch before ACK.
        // Receipt each incarnation/attempt once, as a real recipient does.
        const key = `${f.message.id}:${f.message_incarnation_id}:${f.attempt_number}`;
        if (receipted.has(key)) return;
        receipted.add(key);
        const receipt = { version: 'v1', agent_id: agent, instance_id: `inst-${agent}`, lease_id: lease,
          message_id: f.message.id, attempt_number: f.attempt_number,
          ...(f.delivery_items ? { delivery_items: f.delivery_items } : {}) };
        ws.send(JSON.stringify({ ...receipt, frame_id: `inject-${f.message.id}`, type: 'injection_requested' }));
        ws.send(JSON.stringify({ ...receipt, frame_id: `ack-${f.message.id}`, type: 'ack' }));
      }
    });
    const registered = frame(ws, f => f.type === 'registered', `register ${agent}`);
    ws.once('open', () => ws.send(JSON.stringify({ version: 'v1', frame_id: `reg-${agent}`, type: 'register',
      agent_id: agent, instance_id: `inst-${agent}`, agent_role: 'agent', client_type: 'test',
      ack_confirmation: 'correlated_v1', conversation_sync: 'v1', delivery_receipts: 'delivery_items_v1' })));
    await registered;
    renewals.push(async () => {
      const id = `renew-${agent}-${serial}`;
      const renewed = frame(ws, f => f.type === 'registered' && f.request_id === id, `renew lease ${agent}`);
      ws.send(JSON.stringify({ version: 'v1', type: 'lease_renew', frame_id: id,
        agent_id: agent, instance_id: `inst-${agent}`, lease_id: lease }));
      await renewed;
    });
    const started = performance.now();
    for (let first = 1; first <= count; first += 10) {
      await postBatch(agent, Array.from({ length: Math.min(10, count - first + 1) },
        (_, offset) => `${agent} retained ${first + offset}\n\nScrollable retained history paragraph.`));
      if (first === 11) console.log(`SEED20 ${agent}: ${(performance.now() - started).toFixed(0)}ms`);
    }
    console.log(`SEED${count} ${agent}: ${(performance.now() - started).toFixed(0)}ms`);
    const retained = await (await fetch(`${url}/v1/conversation?agent_id=${agent}&limit=50`)).json();
    expect(retained.events).toHaveLength(50); expect(retained.next_before_seq).toBeGreaterThan(0);
  }
  const feed = (p: Page) => p.locator('.messages');
  async function bottom(p: Page) {
    await expect.poll(() => feed(p).evaluate(el => Math.abs(el.scrollHeight - el.clientHeight - el.scrollTop)),
      { message: 'true bottom <=2px, not just proximity <=180px' }).toBeLessThanOrEqual(2);
    await stable(p, await feed(p).evaluate(el => el.scrollHeight - el.clientHeight));
  }
  async function scroll(p: Page, mode: 'reading' | 'near' | 'bottom') {
    return feed(p).evaluate((el, mode) => {
      el.scrollTop = mode === 'reading' ? 250 : el.scrollHeight - el.clientHeight - (mode === 'near' ? 100 : 0);
      el.dispatchEvent(new Event('scroll', { bubbles: true })); return el.scrollTop;
    }, mode);
  }
  async function open(p: Page, agent: string, newest = 60) {
    await p.goto(h.frontend.url);
    await p.evaluate(({ url, agent }) => {
      localStorage.setItem('mecha.web.activeRouter', url); localStorage.setItem('mecha.web.routers', JSON.stringify([url]));
      localStorage.setItem('mecha.web.selectedPeers', JSON.stringify({ [`${url}\u001fuser`]: agent }));
    }, { url, agent });
    await p.reload();
    await expect.poll(() => browserRegistered).toBe(true);
    await expect(p.locator('.bubble-wrap').filter({ hasText: `${agent} retained ${newest}` })).toHaveCount(1);
    expect(await feed(p).evaluate(el => el.scrollHeight - el.clientHeight)).toBeGreaterThan(600); await bottom(p);
  }
  async function append(p: Page, agent: string, label: string) {
    await post(agent, `${label}\n\n${'Live paragraph adds measurable height. '.repeat(12)}`);
    await expect(p.locator('.bubble-wrap').filter({ hasText: label })).toHaveCount(1);
  }
  async function settle(p: Page) {
    await p.evaluate(() => new Promise<void>(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))));
  }
  async function stable(p: Page, top: number) {
    await settle(p);
    const deviations = await feed(p).evaluate(async (el, top) => {
      const values: number[] = [];
      const end = performance.now() + 250;
      while (performance.now() < end) {
        await new Promise<void>(resolve => requestAnimationFrame(() => resolve()));
        values.push(Math.abs(el.scrollTop - top));
      }
      return values;
    }, top);
    expect(Math.max(...deviations)).toBeLessThanOrEqual(2);
  }
  type Outcome = 'failure' | 'empty' | 'duplicate' | 'success';
  async function pending(p: Page, agent: string, outcome: Outcome) {
    const retained = await (await fetch(`${url}/v1/conversation?agent_id=${agent}&limit=50`)).json();
    let release!: () => void, requested!: () => void;
    const gate = new Promise<void>(r => { release = r; });
    const seen = new Promise<void>(r => { requested = r; });
    await p.route('**/v1/conversation?**', async route => {
      const u = new URL(route.request().url());
      if (u.searchParams.get('agent_id') !== agent || !u.searchParams.has('before_seq')) { await route.continue(); return; }
      requested(); await gate;
      if (outcome === 'success') { await route.continue(); return; }
      await route.fulfill({ status: outcome === 'failure' ? 503 : 200, contentType: 'application/json',
        body: JSON.stringify(outcome === 'failure' ? { error: 'deterministic pagination outage' } : {
          agent_id: agent, events: outcome === 'empty' ? [] : retained.events, next_before_seq: retained.next_before_seq }) });
    });
    await test.step('request older page through scroll to top', async () => {
      await feed(p).evaluate(el => { el.scrollTop = 50; el.dispatchEvent(new Event('scroll', { bubbles: true })); });
      await seen;
    });
    return { release, fallback: Promise.resolve() };
  }
  async function complete(p: Page, request: { release(): void; fallback: Promise<void> }, expectFallback = true) {
    const response = p.waitForResponse(r => r.url().includes('before_seq=')); request.release();
    await (await response).finished();
    if (expectFallback) await request.fallback;
    await settle(p);
  }
  for (const outcome of ['failure', 'empty', 'duplicate'] as const) {
    test(`SCN-SCROLL-007: pagination ${outcome} releases follow after returning near bottom`, async ({ page: p }) => {
      const a = `scroll-${outcome}`; await seed(a); await open(p, a);
      const count = await p.locator('.bubble-wrap').count();
      const request = await pending(p, a, outcome);
      const reading = await scroll(p, 'reading');
      await complete(p, request);
      expect(await p.locator('.bubble-wrap').count()).toBe(count); await stable(p, reading);
      await append(p, a, `${outcome} reading update`); await stable(p, reading);
      await scroll(p, 'near');
      await append(p, a, `${outcome} resumed first`); await bottom(p);
      await append(p, a, `${outcome} resumed second`); await bottom(p);
    });
    test(`SCN-SCROLL-008: pending pagination ${outcome} switch isolation`, async ({ page: p }) => {
      const a = `isolation-${outcome}-a`, b = `isolation-${outcome}-b`;
      await seed(a); await seed(b); await open(p, a); await scroll(p, 'reading');
      const request = await pending(p, a, outcome);
      const switchTo = async (agent: string) => {
        await p.locator('.agent-row-select').filter({ hasText: new RegExp(agent.replace(/-/g, ' '), 'i') }).click();
        await expect(p.locator('.bubble-wrap').filter({ hasText: `${agent} retained 60` })).toHaveCount(1);
      };
      try {
        await switchTo(b); await bottom(p);
        await append(p, b, `${outcome} B pending`); await bottom(p);
        await complete(p, request, false);
        await append(p, b, `${outcome} B completed`); await bottom(p);
        await switchTo(a); await bottom(p);
        await append(p, a, `${outcome} A returned`); await bottom(p);
      } finally { request.release(); }
    });
  }
  test('SCN-SCROLL-011: successful delayed pagination respects return to bottom', async ({ page: p }) => {
    const a = 'scroll-success'; await seed(a); await open(p, a);
    const count = await p.locator('.bubble-wrap').count(), request = await pending(p, a, 'success');
    try {
      await scroll(p, 'reading');
      await scroll(p, 'bottom'); await complete(p, request);
      await expect(p.locator('.bubble-wrap')).toHaveCount(count + 10); await bottom(p);
      await append(p, a, 'success resumed first'); await bottom(p);
      await append(p, a, 'success resumed second'); await bottom(p);
    } finally { request.release(); }
  });
  for (const mode of ['following', 'reading', 'resumed'] as const) {
    test(`SCN-SCROLL-009: delayed same-ID image growth ${mode}`, async ({ page: p }) => {
      const a = `growth-${mode}`; await seed(a); await open(p, a);
      let release!: () => void, requested!: () => void;
      const gate = new Promise<void>(r => { release = r; }), seen = new Promise<void>(r => { requested = r; });
      const image = `${a}.png`;
      await p.route(`**/v1/attachments/${image}`, async route => {
        requested(); await gate;
        await route.fulfill({ contentType: 'image/svg+xml', body: '<svg xmlns="http://www.w3.org/2000/svg" width="150" height="150"><rect width="150" height="150" fill="blue"/></svg>' });
      });
      try {
        await post(a, `Delayed image ${mode}\n\n/v1/attachments/${image}`); await seen;
        await expect(p.locator('.bubble-wrap').filter({ hasText: `Delayed image ${mode}` })).toHaveCount(1); await bottom(p);
        const count = await p.locator('.bubble-wrap').count(), height = await feed(p).evaluate(el => el.scrollHeight);
        let reading = 0;
        if (mode !== 'following') reading = await scroll(p, 'reading');
        if (mode === 'resumed') await scroll(p, 'near');
        release();
        const img = p.locator(`.bubble-images img[src$="${image}"]`);
        await expect.poll(() => img.evaluate((el: HTMLImageElement) => el.complete && el.naturalHeight > 0)).toBe(true);
        await expect.poll(() => feed(p).evaluate(el => el.scrollHeight)).toBeGreaterThan(height + 50);
        expect(await p.locator('.bubble-wrap').count()).toBe(count);
        if (mode === 'reading') await stable(p, reading); else await bottom(p);
      } finally { release(); }
    });
  }
  async function readingAnchor(p: Page) {
    const labels = await feed(p).evaluate(el => {
      const bounds = el.getBoundingClientRect();
      return Array.from(el.querySelectorAll('.bubble-wrap')).filter(item => {
        const rect = item.getBoundingClientRect();
        return rect.top > bounds.top + 10 && rect.bottom < bounds.bottom - 10;
      }).map(item => item.textContent ?? '');
    });
    expect(labels.length).toBeGreaterThan(0);
    const item = p.locator('.bubble-wrap').filter({ hasText: labels[0]! });
    const offset = await item.evaluate(el => el.getBoundingClientRect().top - el.closest('.messages')!.getBoundingClientRect().top);
    return { item, offset };
  }
  async function anchorStable(p: Page, anchor: Awaited<ReturnType<typeof readingAnchor>>) {
    await settle(p);
    const deviations = await anchor.item.evaluate(async (el, offset) => {
      const values: number[] = [];
      const end = performance.now() + 250;
      do {
        await new Promise<void>(resolve => requestAnimationFrame(() => resolve()));
        const current = el.getBoundingClientRect().top - el.closest('.messages')!.getBoundingClientRect().top;
        values.push(Math.abs(current - offset));
      } while (performance.now() < end);
      return values;
    }, anchor.offset);
    expect(Math.max(...deviations)).toBeLessThanOrEqual(2);
  }
  test('SCN-SCROLL-012: successful prepend preserves visible reading anchor with concurrent live update', async ({ page: p }) => {
    const a = 'anchor-success'; await seed(a); await open(p, a);
    const request = await pending(p, a, 'success');
    await scroll(p, 'reading');
    const anchor = await readingAnchor(p), count = await p.locator('.bubble-wrap').count();
    try {
      await append(p, a, 'concurrent live during prepend'); await anchorStable(p, anchor);
      await complete(p, request);
      await expect(p.locator('.bubble-wrap')).toHaveCount(count + 11);
      await anchorStable(p, anchor);
      await expect(p.locator('.bubble-wrap').filter({ hasText: 'concurrent live during prepend' })).toHaveCount(1);
    } finally { request.release(); }
  });
  test('SCN-SCROLL-013: successful stale pagination cannot contaminate another conversation', async ({ page: p }) => {
    const a = 'stale-success-a', b = 'stale-success-b';
    await seed(a); await seed(b); await open(p, a);
    const request = await pending(p, a, 'success');
    await scroll(p, 'reading');
    const switchTo = async (agent: string) => {
      await p.locator('.agent-row-select').filter({ hasText: new RegExp(agent.replace(/-/g, ' '), 'i') }).click();
      await expect(p.locator('.bubble-wrap').filter({ hasText: `${agent} retained 60` })).toHaveCount(1);
    };
    try {
      await switchTo(b); await bottom(p); await scroll(p, 'reading');
      const anchor = await readingAnchor(p);
      await complete(p, request); await anchorStable(p, anchor);
      await expect(p.locator('.bubble-wrap').filter({ hasText: a })).toHaveCount(0);
      await scroll(p, 'near'); await append(p, b, 'B follows after stale success'); await bottom(p);
      await switchTo(a); await bottom(p); await append(p, a, 'A follows on return'); await bottom(p);
    } finally { request.release(); }
  });
  for (const window of ['bounded', 'expanded'] as const) {
    test(`SCN-SCROLL-014: front trim preserves surviving reading anchor in ${window} window`, async ({ page: p }) => {
      const a = `trim-anchor-${window}`;
      // Seed before opening the browser, with the same real delivery/confirmed
      // ACK path as every other case. Initial retained page contains 131..180.
      await seed(a, 180); await test.step('open 180-message history', () => open(p, a, 180));
      const loadPage = async (count: number) => {
        const request = await pending(p, a, 'success');
        try {
          await complete(p, request);
          await expect(p.locator('.bubble-wrap')).toHaveCount(count);
        } finally { request.release(); }
      };
      await test.step('load window to 100', () => loadPage(100));
      await test.step('load window to 150', () => loadPage(150)); // 31..180: next append removes 31 only.
      if (window === 'expanded') await test.step('load window to 180', () => loadPage(180)); // 1..180: next append removes 1..31.
      const anchorItem = p.locator('.bubble-wrap').filter({ hasText: `${a} retained 150` });
      await expect(anchorItem).toHaveCount(1);
      // 150 survives both known removal ranges. Put it visibly in the reading
      // viewport, far from the top pagination trigger and bottom follow zone.
      await anchorItem.evaluate(item => {
        const container = item.closest('.messages');
        if (!(container instanceof HTMLElement)) throw new Error('missing conversation scroll container');
        container.scrollTop += item.getBoundingClientRect().top - container.getBoundingClientRect().top - 100;
        container.dispatchEvent(new Event('scroll', { bubbles: true }));
      });
      await settle(p);
      expect(await feed(p).evaluate(el => el.scrollTop)).toBeGreaterThan(100);
      expect(await feed(p).evaluate(el => el.scrollHeight - el.clientHeight - el.scrollTop)).toBeGreaterThan(180);
      const offset = await anchorItem.evaluate(item => item.getBoundingClientRect().top - item.closest('.messages')!.getBoundingClientRect().top);
      expect(offset).toBeGreaterThan(0);
      expect(offset + (await anchorItem.boundingBox())!.height).toBeLessThan(await feed(p).evaluate(el => el.clientHeight));
      const anchor = { item: anchorItem, offset };
      const removed = p.locator('.bubble-wrap').filter({ hasText: new RegExp(`${a} retained 31\\b`) });
      await expect(removed).toHaveCount(1);
      await append(p, a, `${window} live trim trigger`);
      await expect(p.locator('.bubble-wrap')).toHaveCount(150);
      await expect(removed).toHaveCount(0);
      await expect(anchorItem).toHaveCount(1);
      await anchorStable(p, anchor);
      await expect(p.locator('.bubble-wrap').filter({ hasText: `${window} live trim trigger` })).toHaveCount(1);
    });
  }
  // Supporting scheduler check: deliberately hold browser rAF callbacks after
  // a synchronous layout follow. This is not claimed as native user-input E2E.
  test('SCN-SCROLL-010: supporting scheduler check cancels queued alignment', async ({ page: p }) => {
    const a = 'queued-follow'; await seed(a); await open(p, a);
    await p.evaluate(() => {
      const native = window.requestAnimationFrame.bind(window);
      const callbacks: FrameRequestCallback[] = [];
      window.requestAnimationFrame = callback => { callbacks.push(callback); return -callbacks.length; };
      Object.defineProperty(window, 'releaseScrollFrames', { configurable: true, value: () => {
        window.requestAnimationFrame = native;
        for (const callback of callbacks) callback(performance.now());
        Reflect.deleteProperty(window, 'releaseScrollFrames');
      } });
    });
    try {
      await append(p, a, 'queued follow trigger');
      // The sync effect has followed, but its settlement frame is held.
      expect(await feed(p).evaluate(el => el.scrollHeight - el.clientHeight - el.scrollTop)).toBeLessThanOrEqual(2);
      await scroll(p, 'reading');
    } finally {
      await p.evaluate(() => { const release = Reflect.get(window, 'releaseScrollFrames'); if (typeof release === 'function') release(); });
    }
    await stable(p, 250);
  });
});
