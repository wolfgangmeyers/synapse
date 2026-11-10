import { expect, test } from '@playwright/test';
import { execFileSync } from 'node:child_process';
import { setupE2EHarness } from './harness';

// Run against a frontend built from the current source; do not build concurrently
// with other frontend work. `npm run test:e2e` runs the frontend build first.
for (const stall of [true, false]) {
  test(stall ? 'open without snapshot reaches readiness deadline and reconnects' : 'timely snapshot stays connected past readiness deadline', async ({ page }) => {
    test.setTimeout(90_000);
    const h = await setupE2EHarness({ useProxy: true, managed: true });
    const url = h.proxy!.url;
    const connections: { snapshots: number; close?: { code?: number; reason?: string }; markerSent: boolean; snapshotVersion?: number; deltas: { upstreamBase: number; upstreamVersion: number; base: number; version: number; agent: string; urgent: boolean }[] }[] = [];
    const unexpected: string[] = [];
    let isolateDelta = false;
    let pendingManagedReads = 0;
    let blockedManagedReads = 0;
    let suppressedUrgency = 0;
    let releaseDelta: (() => void) | undefined;
    try {
      execFileSync('python3', ['-c', `import sqlite3,sys
c=sqlite3.connect(sys.argv[1])
c.execute("INSERT INTO agents(agent_id,first_seen_at) VALUES('readiness-agent','2026-01-01T00:00:00Z')")
c.execute("INSERT INTO managed_agents(agent_id,name,cwd,selected_model_provider,selected_model_id,desired_state,lifecycle_state,activity_state,session_epoch,manifest_revision,manifest_hash,created_at,updated_at) VALUES('readiness-agent','readiness-agent',?,'mock','mock','running','running','idle',1,1,?,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')",(sys.argv[2],'a'*64))
c.commit()`, h.router.databasePath, h.router.root]);
      await page.route('**/*', async route => {
        const request = new URL(route.request().url());
        if (![url, h.frontend.url].includes(request.origin)) { unexpected.push(request.href); await route.abort(); return; }
        if (request.origin === url && request.pathname === '/v1/managed-agents' && route.request().method() === 'GET') {
          if (isolateDelta) { blockedManagedReads++; await route.abort(); return; }
          pendingManagedReads++;
          try { await route.fulfill({ response: await route.fetch() }); } finally { pendingManagedReads--; }
          return;
        }
        await route.continue();
      });
      await page.routeWebSocket('**/*', ws => {
        const address = new URL(ws.url());
        if (![new URL(url).host, new URL(h.frontend.url).host].includes(address.host)) { unexpected.push(ws.url()); ws.close(); return; }
        const server = ws.connectToServer();
        if (address.pathname !== '/v1/ui-live') {
          server.onMessage(message => {
            if (isolateDelta && JSON.parse(message.toString()).type === 'agent_urgency_changed') { suppressedUrgency++; return; }
            ws.send(message);
          });
          return;
        }
        const connection: (typeof connections)[number] = { snapshots: 0, markerSent: false, deltas: [] };
        connections.push(connection);
        const generation = connections.length;
        ws.onClose((code, reason) => { connection.close = { code, reason }; server.close({ code, reason }); });
        server.onMessage(message => {
          const frame = JSON.parse(message.toString());
          if (isolateDelta && frame.type === 'agent_urgency_changed') { suppressedUrgency++; return; }
          if (frame.type === 'managed_agents_snapshot') connection.snapshots++;
          if (stall && generation === 1) {
            // A valid non-snapshot frame is allowed before readiness, but must
            // not cancel the deadline. Suppress the real upstream frames.
            if (frame.type === 'managed_agents_snapshot' && !connection.markerSent) {
              ws.send(JSON.stringify({ type: 'agent_urgency_changed', agent_id: 'readiness-agent', urgent: false }));
              connection.markerSent = true;
            }
            return;
          }
          if ((!stall && generation === 1 || stall && generation === 2) && frame.type === 'managed_agents_snapshot') {
            // This agent exists only on the wire, never in SQLite or REST.
            // Its row proves the timely or replacement snapshot reached the UI.
            const agent = frame.agents.find((item: { agent_id: string }) => item.agent_id === 'readiness-agent');
            if (agent) {
              // The initial REST snapshot may already have this version. Give the
              // synthetic snapshot a newer version so the reducer applies it and
              // the on-connect REST reconciliation cannot erase the marker.
              frame.projection_version += 1;
              connection.snapshotVersion = frame.projection_version;
              frame.agents.push({ ...agent, agent_id: 'live-snapshot-marker' });
              connection.markerSent = true;
            }
            ws.send(JSON.stringify(frame));
            return;
          }
          // Once a synthetic snapshot advances this connection's projection,
          // translate subsequent agent projection versions by the same offset.
          // Child lifecycle versions are a separate sequence.
          if (connection.markerSent && (!stall || generation !== 1)) {
            const upstreamBase = frame.base_version;
            const upstreamVersion = frame.projection_version;
            if (typeof frame.projection_version === 'number') frame.projection_version++;
            if (typeof frame.base_version === 'number') frame.base_version++;
            if (frame.type === 'managed_agent_delta') connection.deltas.push({ upstreamBase, upstreamVersion, base: frame.base_version, version: frame.projection_version, agent: frame.agent_id, urgent: frame.changes.urgent });
          }
          if (isolateDelta && generation === 2 && frame.type === 'managed_agent_delta' && frame.agent_id === 'readiness-agent' && frame.changes.urgent === true) {
            const translated = JSON.stringify(frame);
            releaseDelta = () => ws.send(translated);
            return;
          }
          ws.send(JSON.stringify(frame));
        });
      });
      await page.addInitScript(router => {
        localStorage.clear();
        localStorage.setItem('mecha.web.activeRouter', router);
        localStorage.setItem('mecha.web.routers', JSON.stringify([router]));
        const evidence: { started: number; opened?: number; closed?: number; urgencyFrames?: number }[] = [];
        (window as any).__readinessSockets = evidence;
        const NativeWebSocket = window.WebSocket;
        window.WebSocket = class extends NativeWebSocket {
          constructor(socketUrl: string | URL, protocols?: string | string[]) {
            super(socketUrl, protocols);
            if (new URL(socketUrl, location.href).pathname === '/v1/ui-live') {
              const item: (typeof evidence)[number] = { started: performance.now() };
              evidence.push(item);
              this.addEventListener('open', () => { item.opened = performance.now(); });
              this.addEventListener('close', () => { item.closed = performance.now(); });
              this.addEventListener('message', event => {
                if (typeof event.data === 'string' && JSON.parse(event.data).type === 'agent_urgency_changed') item.urgencyFrames = (item.urgencyFrames ?? 0) + 1;
              });
            }
          }
        };
      }, url);
      await page.goto(h.frontend.url);
      await expect.poll(() => connections[0]?.snapshots).toBeGreaterThan(0);
      await expect.poll(() => page.evaluate(() => (window as any).__readinessSockets[0]?.opened)).toBeTruthy();
      if (stall) {
        expect(connections[0].markerSent).toBe(true);
        await expect.poll(() => page.evaluate(() => (window as any).__readinessSockets[0]?.urgencyFrames)).toBe(1);
        // Check against socket creation, the actual deadline origin. An early
        // close cannot masquerade as a successful readiness timeout.
        await expect.poll(() => page.evaluate(() => performance.now() - (window as any).__readinessSockets[0].started), { timeout: 12_000 }).toBeGreaterThanOrEqual(8_000);
        expect(connections[0].close).toBeUndefined();
        expect(await page.evaluate(() => (window as any).__readinessSockets[0].closed)).toBeUndefined();
        await expect.poll(() => connections[0]?.close, { timeout: 7_000 }).toEqual({ code: 1000, reason: 'projection recovery' });
        await expect.poll(() => page.evaluate(() => (window as any).__readinessSockets[0]?.closed)).toBeTruthy();
        const first = await page.evaluate(() => (window as any).__readinessSockets[0]);
        expect(first.closed - first.started).toBeGreaterThanOrEqual(9_000);
        // Ten-second deadline plus three seconds for browser and close handshake scheduling.
        expect(first.closed - first.started).toBeLessThanOrEqual(13_000);
        await expect.poll(() => connections[1]?.snapshots, { timeout: 8_000 }).toBeGreaterThan(0);
        expect(connections[1].markerSent).toBe(true);
        await expect(page.locator('.agent-row[data-agent-id="live-snapshot-marker"]')).toBeVisible();
        // Freeze REST reconciliation before the mutation and drain any reads
        // already in flight. Suppress urgency notifications on both sockets.
        // Hold the real delta to prove the badge cannot appear before delivery.
        isolateDelta = true;
        await expect.poll(() => pendingManagedReads).toBe(0);
        await expect(page.locator('.agent-row[data-agent-id="readiness-agent"] .agent-row-urgent-badge')).toHaveCount(0);
        const response = await fetch(`${url}/v1/agents/readiness-agent/urgency`, {
          method: 'PUT', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ urgent: true }),
        });
        expect(response.status).toBe(200);
        await expect.poll(() => connections[1].deltas.find(delta => delta.agent === 'readiness-agent' && delta.urgent === true)).toBeTruthy();
        const delta = connections[1].deltas.find(item => item.agent === 'readiness-agent' && item.urgent === true)!;
        expect(delta.upstreamVersion).toBe(delta.upstreamBase + 1);
        expect(delta.base).toBe(delta.upstreamBase + 1);
        expect(delta.version).toBe(delta.upstreamVersion + 1);
        expect(delta.base).toBe(connections[1].snapshotVersion);
        await expect.poll(() => suppressedUrgency).toBeGreaterThan(0);
        expect(releaseDelta).toBeDefined();
        await expect(page.locator('.agent-row[data-agent-id="readiness-agent"] .agent-row-urgent-badge')).toHaveCount(0);
        releaseDelta!();
        releaseDelta = undefined;
        await expect(page.locator('.agent-row[data-agent-id="readiness-agent"] .agent-row-urgent-badge')).toBeVisible();
        await expect(page.locator('.agent-row[data-agent-id="live-snapshot-marker"]')).toBeVisible();
        await expect.poll(() => page.evaluate(() => performance.now() - (window as any).__readinessSockets[1].started), { timeout: 15_000 }).toBeGreaterThanOrEqual(11_000);
        expect(connections).toHaveLength(2);
        expect(connections[1].close).toBeUndefined();
        expect(await page.evaluate(() => (window as any).__readinessSockets[1].closed)).toBeUndefined();
        expect(blockedManagedReads).toBeGreaterThan(0);
      } else {
        expect(connections[0].markerSent).toBe(true);
        await expect(page.locator('.agent-row[data-agent-id="live-snapshot-marker"]')).toBeVisible();
        await expect.poll(() => page.evaluate(() => performance.now() - (window as any).__readinessSockets[0].started), { timeout: 15_000 }).toBeGreaterThanOrEqual(11_000);
        expect(connections).toHaveLength(1);
        expect(connections[0].close).toBeUndefined();
      }
      expect(unexpected).toEqual([]);
    } finally {
      releaseDelta?.();
      await h.close();
    }
  });
}
