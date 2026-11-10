import { test, expect } from '@playwright/test';
import { createRequire } from 'node:module';
import { resolve } from 'node:path';
import { createServer } from 'node:http';
import { once } from 'node:events';
import { pathToFileURL } from 'node:url';
import { setupE2EHarness } from './harness';

// Bundle only the test entry in memory. Never write or build frontend assets.
const require = createRequire(resolve('web/router-ui-react/package.json'));
const { build } = await import(pathToFileURL(require.resolve('vite')).href);

for (const callback of ['onFrame', 'subscriber']) {
  for (const outcome of ['lower', 'stalled']) {
    test(`UI-live transport replacement during snapshot delivery: ${callback}, ${outcome}`, async ({ page }) => {
      test.setTimeout(60_000);
      const bundle = await build({ configFile: false, logLevel: 'silent', build: { write: false, lib: { entry: resolve('tests/e2e/fixtures/ui-live-replacement-entry.ts'), name: 'ReplacementFixture', formats: ['iife'] } } });
      const outputs = Array.isArray(bundle) ? bundle : [bundle];
      const script = outputs.flatMap((result: { output: any[] }) => result.output).find((item: { type: string }) => item.type === 'chunk').code;
      const h = await setupE2EHarness({ useProxy: true, managed: true });
      const connections: { snapshots: number; opened: boolean }[] = [];
      // A fulfilled navigation has no network address space in Chromium, which
      // can block its loopback WebSocket. Serve the fixture from real loopback.
      const fixture = createServer((req, res) => {
        if (req.url === '/replacement-entry.js') {
          res.writeHead(200, { 'Content-Type': 'text/javascript' });
          res.end(script);
        } else {
          res.writeHead(200, { 'Content-Type': 'text/html' });
          res.end('<!doctype html><script src="/replacement-entry.js"></script>');
        }
      });
      try {
        fixture.listen(0, '127.0.0.1');
        await once(fixture, 'listening');
        const address = fixture.address();
        if (!address || typeof address === 'string') throw new Error('Fixture server has no TCP address');
        await expect.poll(async () => (await fetch(`${h.proxy!.url}/v1/managed-agents`)).status, { timeout: 20_000 }).toBe(200);
        await page.routeWebSocket('**/*', ws => {
          const server = ws.connectToServer();
          const connection = { snapshots: 0, opened: true };
          connections.push(connection);
          const index = connections.length;
          server.onMessage(message => {
            const frame = JSON.parse(message.toString());
            if (frame.type === 'managed_agents_snapshot') connection.snapshots++;
            if (outcome === 'stalled' && index === 2) return;
            // Only the first real snapshot is relevant to this transport test.
            // Suppress all later frames: their upstream versions do not belong
            // to the deliberately rewritten projection version.
            if (frame.type !== 'managed_agents_snapshot' || connection.snapshots !== 1) return;
            frame.projection_version = index === 1 ? 1000 : 1;
            ws.send(JSON.stringify(frame));
          });
        });
        await page.goto(`http://127.0.0.1:${address.port}/replacement-entry.html?router=${encodeURIComponent(h.proxy!.url)}&callback=${callback}`);
        await expect.poll(() => connections[0]?.snapshots).toBeGreaterThan(0);
        await expect.poll(() => page.evaluate(() => (window as any).evidence.events.some((e: any) => e.kind === 'after-replace'))).toBe(true);
        await expect.poll(() => page.evaluate(() => (window as any).evidence.events.some((e: any) => e.kind === 'close' && e.index === 1))).toBe(true);
        let events = await page.evaluate(() => (window as any).evidence.events);
        expect(events.find((e: any) => e.kind === 'replace').state).toBe(2);
        expect(events.find((e: any) => e.kind === 'after-replace').count).toBe(2);
        expect(events.filter((e: any) => e.kind === 'subscriber' && e.version === 1000)).toHaveLength(0);
        if (outcome === 'lower') {
          await expect.poll(() => page.evaluate(() => (window as any).evidence.client.projectionVersion)).toBe(1);
          await expect.poll(() => page.evaluate(() => (window as any).evidence.events.filter((e: any) => e.kind === 'connected').length)).toBe(1);
          await page.waitForTimeout(1200);
          events = await page.evaluate(() => (window as any).evidence.events);
          expect(events.filter((e: any) => e.kind === 'subscriber' && e.version === 1)).toHaveLength(1);
          expect(events.filter((e: any) => e.kind === 'error')).toHaveLength(0);
          expect(connections).toHaveLength(2);
        } else {
          await expect.poll(() => connections[1]?.snapshots).toBeGreaterThan(0);
          await expect.poll(() => page.evaluate(() => (window as any).evidence.events.find((e: any) => e.kind === 'error')?.cause), { timeout: 5000 }).toBe('websocket_snapshot_timeout');
          await expect.poll(() => connections[2]?.snapshots, { timeout: 5000 }).toBeGreaterThan(0);
          await expect.poll(() => page.evaluate(() => (window as any).evidence.events.filter((e: any) => e.kind === 'connected').length)).toBe(1);
          events = await page.evaluate(() => (window as any).evidence.events);
          const start = events.find((e: any) => e.kind === 'socket' && e.index === 2).at;
          const error = events.find((e: any) => e.kind === 'error');
          expect(error.at - start).toBeGreaterThanOrEqual(900);
          expect(error.at - start).toBeLessThan(3000);
          expect(events.filter((e: any) => e.kind === 'subscriber' && e.version === 1)).toHaveLength(1);
        }
        const replacementIndex = outcome === 'lower' ? 2 : 3;
        const sequence = events.filter((e: any) => ['frame', 'first-subscriber', 'subscriber', 'connected'].includes(e.kind) && e.event !== 'disconnected');
        expect(sequence).toEqual([
          { kind: 'frame', version: 1000, index: 1 },
          ...(callback === 'subscriber' ? [{ kind: 'first-subscriber', version: 1000, index: 1 }] : []),
          { kind: 'frame', version: 1, index: replacementIndex },
          { kind: 'first-subscriber', version: 1, index: replacementIndex },
          { kind: 'subscriber', version: 1, index: replacementIndex },
          { kind: 'connected', index: replacementIndex },
          { kind: 'subscriber', event: 'connected', index: replacementIndex },
        ]);
      } finally {
        await page.evaluate(() => (window as any).evidence?.client.close()).catch(() => {});
        try {
          if (fixture.listening) await new Promise<void>((resolve, reject) => fixture.close(error => error ? reject(error) : resolve()));
        } finally {
          await h.close();
        }
      }
    });
  }
}
