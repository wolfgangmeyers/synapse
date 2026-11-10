import { test, expect } from '@playwright/test';
import { mkdtemp, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { startIsolatedRouter, startRouterProxy } from './harness';

test('startup errors do not disclose arbitrary child output', async () => {
  const dir = await mkdtemp(join(tmpdir(), 'startup-redaction-'));
  // Construct the marker at runtime and never include it in assertion messages.
  const secret = ['private', 'fixture', 'credential'].join('-');
  try {
    const router = join(dir, 'router');
    const proxy = join(dir, 'proxy');
    await writeFile(router, `#!${process.execPath}\nprocess.stdout.write(${JSON.stringify(secret)}); process.stderr.write(${JSON.stringify('managed provider status is unavailable ' + secret)}); setInterval(() => {}, 1000);\n`, { mode: 0o755 });
    await writeFile(proxy, `#!${process.execPath}\nprocess.stderr.write(${JSON.stringify(secret)}, () => process.exit(7));\n`, { mode: 0o755 });
    let routerMessage = '';
    try { await startIsolatedRouter({ repoRoot: resolve('.'), routerExecutableFixture: router, startupTimeoutFixtureMs: 1000 }); }
    catch (error) { routerMessage = String(error); }
    expect(routerMessage.includes('Isolated router timed out during startup.')).toBe(true);
    expect(routerMessage.includes('managed provider status is unavailable')).toBe(true);
    expect(routerMessage.includes(secret)).toBe(false);
    let proxyMessage = '';
    try { await startRouterProxy({ repoRoot: resolve('.'), upstreamPort: 1, proxyExecutableFixture: proxy }); }
    catch (error) { proxyMessage = String(error); }
    expect(proxyMessage.includes('Router proxy exited (code 7).')).toBe(true);
    expect(proxyMessage.includes(secret)).toBe(false);
  } finally { await rm(dir, { recursive: true, force: true }); }
});
