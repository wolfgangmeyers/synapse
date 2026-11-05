import { expect, test, type Page } from '@playwright/test';
import { WebSocket } from 'ws';
import { execFileSync } from 'node:child_process';
import { setupE2EHarness, type E2ETestHarness } from './harness';

const a = 'agent-fence-alpha';
const b = 'agent-fence-beta';
const bubble = (page: Page, text: string) => page.locator('.bubble-wrap').filter({ hasText: text });
const gate = () => {
  let release!: () => void;
  const promise = new Promise<void>(resolve => { release = resolve; });
  return { promise, release };
};

test('SCN-SWITCH-006: an older page from the previous A selection cannot alter the new A window', async ({ page }) => {
  test.setTimeout(60_000);
  let harness: E2ETestHarness | undefined;
  const sockets: WebSocket[] = [];
  const resume = gate();
  try {
    harness = await setupE2EHarness({ useProxy: true });
    const url = harness.proxy!.url;
    for (const agent of [a, b]) {
      const socket = new WebSocket(`${url.replace(/^http/, 'ws')}/v1/agents`);
      sockets.push(socket);
      await new Promise<void>((resolve, reject) => {
        socket.on('open', () => socket.send(JSON.stringify({ version: 'v1', frame_id: `register-${agent}`, type: 'register', agent_id: agent, instance_id: `instance-${agent}`, agent_role: 'agent', client_type: 'test', ack_confirmation: 'correlated_v1', conversation_sync: 'v1', delivery_receipts: 'delivery_items_v1' })));
        socket.on('message', data => { if (JSON.parse(data.toString()).type === 'registered') resolve(); });
        socket.on('error', reject);
      });
    }
    async function post(agent: string, id: string, body: string) {
      const response = await fetch(`${url}/v1/messages`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ id, from: agent, to: 'user', body, created_at: new Date().toISOString(), source: { kind: 'agent' } }) });
      expect([200, 201]).toContain(response.status);
    }
    // Seed the real router store directly; all reads still cross browser, proxy and router.
    execFileSync('python3', ['-c', `
import sqlite3, sys
c = sqlite3.connect(sys.argv[1])
for i in range(1, 66):
 c.execute("INSERT INTO conversation_timeline(id,agent_id,seq,kind,role,content,metadata,created_at) VALUES(?, ?, ?, 'message', 'assistant', ?, '{}', ?)", (f'fence-{i}', sys.argv[2], i, f'Fence history {i:03d}', 1767225600000 + i * 1000))
c.commit()
`, harness.router.databasePath, a]);
    await post(b, 'fence-beta', 'Beta isolated history');

    const allowed = new Set([new URL(harness.frontend.url).origin, new URL(url).origin]);
    await page.route('**/*', route => allowed.has(new URL(route.request().url()).origin) ? route.continue() : route.abort());
    await page.routeWebSocket('**/*', ws => {
      if (new URL(ws.url()).host === new URL(url).host) ws.connectToServer();
      else ws.close();
    });
    await page.addInitScript(({ url, a }) => {
      localStorage.setItem('mecha.web.activeRouter', url);
      localStorage.setItem('mecha.web.routers', JSON.stringify([url]));
      localStorage.setItem('mecha.web.selectedPeers', JSON.stringify({ [`${url}\u001fuser`]: a }));
    }, { url, a });
    await page.goto(harness.frontend.url);
    const select = async (name: 'Alpha' | 'Beta') => {
      await page.locator('.agent-row, .agent-row-select').filter({ hasText: `Agent Fence ${name}` }).first().click();
      await expect(page.locator('header h2', { hasText: `Agent Fence ${name}` })).toBeVisible();
    };
    await expect(bubble(page, 'Fence history 065')).toBeVisible();
    await expect(bubble(page, 'Fence history 001')).toHaveCount(0);

    const fetched = gate(), delivered = gate();
    let held = false;
    let contents: string[] = [];
    await page.route('**/v1/conversation?*', async route => {
      const request = new URL(route.request().url());
      if (held || request.origin !== url || request.searchParams.get('agent_id') !== a || !request.searchParams.has('before_seq')) return route.fallback();
      held = true;
      const response = await route.fetch();
      const json = await response.json();
      contents = json.events.map((event: { content: string }) => event.content);
      fetched.release();
      await resume.promise;
      try { await route.fulfill({ response }); } finally { delivered.release(); }
    });
    await page.getByRole('button', { name: /load older history/i }).click();
    await fetched.promise;
    expect(contents).toContain('Fence history 001');
    await select('Beta');
    await expect(bubble(page, 'Beta isolated history')).toBeVisible();
    await select('Alpha');
    await expect(bubble(page, 'Fence history 065')).toBeVisible();
    await expect(bubble(page, 'Fence history 001')).toHaveCount(0);
    await post(a, 'fence-live', 'Current Alpha live');
    await expect(bubble(page, 'Current Alpha live')).toHaveCount(1);
    resume.release();
    await delivered.promise;
    await expect(bubble(page, 'Fence history 001')).toHaveCount(0);
    await expect(bubble(page, 'Beta isolated history')).toHaveCount(0);
    await expect(bubble(page, 'Fence history 065')).toHaveCount(1);
    await expect(bubble(page, 'Current Alpha live')).toHaveCount(1);
    // The stale page must not consume the new selection's cursor or leave it loading.
    await page.getByRole('button', { name: /load older history/i }).click();
    await expect(bubble(page, 'Fence history 001')).toBeVisible();
    await expect(bubble(page, 'Current Alpha live')).toHaveCount(1);
  } finally {
    resume.release();
    sockets.forEach(socket => socket.terminate());
    await harness?.close();
  }
});
