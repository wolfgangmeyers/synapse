import { test, expect } from '@playwright/test';
import { spawn } from 'node:child_process';
import { mkdtemp, writeFile, readFile, readdir, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';

// A descendant can inherit stderr after the entry exits, so `close` is not a
// deadline. Destroy our pipe and reject directly rather than waiting for it.
async function waitForEntry(proc: ReturnType<typeof spawn>, timeoutMs: number): Promise<number | null> {
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => {
      if (proc.exitCode === null && proc.signalCode === null) proc.kill('SIGKILL');
      proc.stderr?.destroy();
      reject(new Error(`entry timed out after ${timeoutMs}ms`));
    }, timeoutMs);
    proc.once('error', error => { clearTimeout(timer); reject(error); });
    proc.once('close', code => { clearTimeout(timer); resolve(code); });
  });
}

test('entry deadline does not wait for an inherited stderr pipe', async () => {
  const proc = spawn(process.execPath, ['-e', `
    require('node:child_process').spawn(process.execPath, ['-e', 'setTimeout(() => {}, 2500)'], {stdio: ['ignore', 'ignore', 'inherit']});
    setInterval(() => {}, 1000);
  `], { stdio: ['ignore', 'ignore', 'pipe'] });
  const start = Date.now();
  await expect(waitForEntry(proc, 500)).rejects.toThrow(/entry timed out/);
  expect(Date.now() - start).toBeLessThan(2000);
});

for (const target of ['router', 'proxy'] as const) {
  test(`${target} build deadline terminates its POSIX process group and cleans setup`, async () => {
    test.skip(process.platform === 'win32', 'POSIX process group test');
    const dir = await mkdtemp(join(tmpdir(), 'build-deadline-test-'));
    try {
      const ready = join(dir, 'ready');
      const survived = join(dir, 'survived');
      const entry = join(dir, 'entry.mjs');
      const go = join(dir, 'go');
      // The child reports readiness after scheduling its own bounded lifetime.
      // If it survives the group kill, it writes a marker and exits on its own.
      await writeFile(go, `#!${process.execPath}\nconst {spawn}=require('node:child_process'); const fs=require('node:fs'); const c=spawn(process.execPath,['-e',"const fs=require('node:fs'); setTimeout(()=>{fs.writeFileSync(process.env.SURVIVED,'alive'); process.exit(0)},5000); process.send('ready')"],{stdio:['ignore','ignore','ignore','ipc']}); c.on('message',()=>fs.writeFileSync(process.env.READY,'ready')); setInterval(()=>{},1000);\n`, { mode: 0o755 });
      const harness = new URL('./harness.ts', import.meta.url).href;
      await writeFile(entry, `import {startIsolatedRouter,startRouterProxy} from ${JSON.stringify(harness)};\nprocess.on('SIGTERM',()=>process.exit(124));
try { await ${target === 'router' ? 'startIsolatedRouter' : 'startRouterProxy'}({repoRoot:${JSON.stringify(resolve('.'))},upstreamPort:1,buildTimeoutMs:3000}); process.exitCode=2; } catch(e) { console.error(e.message); process.exitCode=/build.*timed out/i.test(e.message)?0:3; }\n`);
      const proc = spawn(process.execPath, ['--experimental-strip-types', entry], { detached: true, env: { ...process.env, TMPDIR: dir, PATH: `${dir}:${process.env.PATH}`, READY: ready, SURVIVED: survived }, stdio: ['ignore', 'ignore', 'pipe'] });
      let output = '';
      proc.stderr!.on('data', chunk => { output += chunk; });
      let outerTimeout = false;
      const timer = setTimeout(() => {
        outerTimeout = true;
        // Only signal the live child via its handle; never reuse recorded PIDs.
        if (proc.exitCode === null && proc.signalCode === null) proc.kill('SIGTERM');
        setTimeout(() => { if (proc.exitCode === null && proc.signalCode === null) proc.kill('SIGKILL'); proc.stderr?.destroy(); }, 1000).unref();
      }, 12_000);
      try {
        const code = await new Promise<number | null>((res, rej) => { proc.once('error', rej); proc.once('close', res); });
        expect(outerTimeout, output).toBe(false);
        expect(code, output).toBe(0);
        expect(await readFile(ready, 'utf8')).toBe('ready');
        // Wait past the marker deadline. Its absence shows the child did not
        // execute the marker callback; it does not prove why.
        await new Promise(resolve => setTimeout(resolve, 5500));
        await expect(readFile(survived, 'utf8')).rejects.toMatchObject({ code: 'ENOENT' });
        expect((await readdir(dir)).filter(name => name.startsWith('mecha-e2e-'))).toEqual([]);
      } finally { clearTimeout(timer); }
    } finally { await rm(dir, { recursive: true, force: true }); }
  });
}

for (const target of ['router', 'proxy'] as const) {
  for (const failure of ['spawn', 'nonzero'] as const) {
    test(`${target} build reports ${failure} diagnostics`, async () => {
      const dir = await mkdtemp(join(tmpdir(), 'build-diagnostic-'));
      const previousPath = process.env.PATH;
      try {
        if (failure === 'nonzero') {
          await writeFile(join(dir, 'go'), `#!${process.execPath}\nprocess.stderr.write('fixture build error'); process.exit(7);\n`, { mode: 0o755 });
        }
        process.env.PATH = dir;
        const { startIsolatedRouter, startRouterProxy } = await import('./harness');
        const build = target === 'router'
          ? startIsolatedRouter({ repoRoot: resolve('.') })
          : startRouterProxy({ repoRoot: resolve('.'), upstreamPort: 1 });
        await expect(build).rejects.toThrow(failure === 'spawn'
          ? /build failed to start:.*ENOENT/
          : /build failed \(7\):.*fixture build error/);
      } finally {
        process.env.PATH = previousPath;
        await rm(dir, { recursive: true, force: true });
      }
    });
  }
}

test('real router and proxy builds start and serve health', async () => {
  test.setTimeout(30_000);
  const { startIsolatedRouter, startRouterProxy } = await import('./harness');
  const router = await startIsolatedRouter({ repoRoot: resolve('.') });
  try {
    const proxy = await startRouterProxy({ repoRoot: resolve('.'), upstreamPort: router.port });
    try { expect((await fetch(`${proxy.url}/v1/health`)).ok).toBeTruthy(); }
    finally { await proxy.close(); }
  } finally { await router.close(); }
});

test.describe('setup timeout budget', () => {
  test.setTimeout(1000);
  test.beforeEach(async () => {
    const { startIsolatedRouter } = await import('./harness');
    // Even a short build deadline must extend the active budget when the
    // build starts inside beforeEach.
    const before = test.info().timeout;
    await expect(startIsolatedRouter({ repoRoot: resolve('.'), buildTimeoutMs: 1 })).rejects.toThrow(/build timed out/);
    expect(test.info().timeout).toBeGreaterThan(before);
  });
  test('extends the active test timeout', async () => {});
});


test.describe('beforeAll accumulated build budget', () => {
  test.describe.configure({ timeout: 1000 });
  let dir: string;
  let elapsed = 0;
  test.beforeAll(async () => {
    dir = await mkdtemp(join(tmpdir(), 'build-budget-'));
    const go = join(dir, 'go');
    await writeFile(go, `#!${process.execPath}\nsetTimeout(()=>process.exit(1),7000);\n`, { mode: 0o755 });
    const oldPath = process.env.PATH;
    process.env.PATH = `${dir}:${oldPath}`;
    const start = Date.now();
    try {
      const { startIsolatedRouter } = await import('./harness');
      for (let i = 0; i < 2; i++)
        await expect(startIsolatedRouter({ repoRoot: resolve('.'), buildTimeoutMs: 7100 })).rejects.toThrow(/build failed/);
      elapsed = Date.now() - start;
    } finally {
      process.env.PATH = oldPath;
      await rm(dir, { recursive: true, force: true });
    }
  });
  test('allows both builds to complete beyond the first extended budget', () => {
    expect(elapsed).toBeGreaterThan(13_100);
  });
});

test('exited build leader with inherited stderr cannot hang setup', async () => {
  test.skip(process.platform === 'win32', 'POSIX fixture');
  const dir = await mkdtemp(join(tmpdir(), 'build-orphan-'));
  const previousPath = process.env.PATH;
  try {
    // The orphan exits on its own; the harness must not signal a potentially
    // reused process-group ID after the leader has exited.
    await writeFile(join(dir, 'go'), `#!${process.execPath}\nconst {spawn}=require('node:child_process'); const c=spawn(process.execPath,['-e','setTimeout(()=>process.exit(),4000)'],{stdio:['ignore','ignore','inherit','ipc']}); c.on('spawn',()=>process.exit(0));\n`, { mode: 0o755 });
    process.env.PATH = `${dir}:${previousPath}`;
    const { startIsolatedRouter } = await import('./harness');
    const start = Date.now();
    await expect(startIsolatedRouter({ repoRoot: resolve('.'), buildTimeoutMs: 500 })).rejects.toThrow(/build timed out/);
    expect(Date.now() - start).toBeLessThan(3000);
  } finally {
    process.env.PATH = previousPath;
    await rm(dir, { recursive: true, force: true });
  }
});

test('live build leader with escaped stderr holder still times out promptly', async () => {
  test.skip(process.platform === 'win32', 'POSIX detached process fixture');
  const dir = await mkdtemp(join(tmpdir(), 'build-escaped-'));
  const previousPath = process.env.PATH;
  const ready = join(dir, 'ready');
  try {
    // The detached child escapes the build group and owns its own short lifetime.
    // Its readiness message proves it holds inherited stderr before the deadline.
    await writeFile(join(dir, 'go'), `#!${process.execPath}\nconst {spawn}=require('node:child_process'); const fs=require('node:fs'); const c=spawn(process.execPath,['-e',"process.send('ready'); setTimeout(()=>process.exit(0),6000)"],{detached:true,stdio:['ignore','ignore','inherit','ipc']}); c.on('message',()=>fs.writeFileSync(${JSON.stringify(ready)},'ready')); setInterval(()=>{},1000);\n`, { mode: 0o755 });
    process.env.PATH = `${dir}:${previousPath}`;
    const { startIsolatedRouter } = await import('./harness');
    const start = Date.now();
    await expect(startIsolatedRouter({ repoRoot: resolve('.'), buildTimeoutMs: 1500 })).rejects.toThrow(/build timed out/);
    expect(await readFile(ready, 'utf8')).toBe('ready');
    expect(Date.now() - start).toBeLessThan(4500);
  } finally {
    process.env.PATH = previousPath;
    await rm(dir, { recursive: true, force: true });
  }
});


test('failed router build closes concurrently started HTTP servers and removes temporary state', async () => {
  const dir = await mkdtemp(join(tmpdir(), 'harness-cleanup-'));
  try {
    await writeFile(join(dir, 'go'), `#!${process.execPath}\nsetTimeout(() => { process.stderr.write('injected build failure'); process.exit(7); }, 500);\n`, { mode: 0o755 });
    const entry = join(dir, 'entry.mjs');
    const harness = new URL('./harness.ts', import.meta.url).href;
    await writeFile(entry, `import { Server } from 'node:http';
import { readdir } from 'node:fs/promises';
import { setupE2EHarness } from ${JSON.stringify(harness)};
const servers = [];
const listen = Server.prototype.listen;
Server.prototype.listen = function(...args) { servers.push(this); return listen.apply(this, args); };
try { await setupE2EHarness(); throw Error('unexpected success'); }
catch (e) { if (!String(e.message).includes('injected build failure')) throw e; }
if (servers.length !== 2 || servers.some(s => s.listening)) throw Error('HTTP servers not cleaned up: ' + servers.length);
if ((await readdir(process.env.TMPDIR)).some(n => n.startsWith('mecha-e2e-'))) throw Error('temporary state leaked');
`);
    const proc = spawn(process.execPath, ['--experimental-strip-types', entry], {
      env: { ...process.env, TMPDIR: dir, PATH: `${dir}:${process.env.PATH}` }, stdio: ['ignore', 'ignore', 'pipe'],
    });
    let output = '';
    proc.stderr!.on('data', chunk => { output += chunk; });
    const code = await waitForEntry(proc, 10_000);
    expect(code, output).toBe(0);
  } finally { await rm(dir, { recursive: true, force: true }); }
});

for (const startupFailure of [true, false]) {
  test(`harness reports cleanup errors ${startupFailure ? 'alongside startup failure' : 'on normal teardown'} and closes other resources`, async () => {
    test.setTimeout(60_000);
    const dir = await mkdtemp(join(tmpdir(), 'harness-errors-'));
    try {
      if (startupFailure) await writeFile(join(dir, 'go'), `#!${process.execPath}\nsetTimeout(()=>{process.stderr.write('startup sentinel');process.exit(7)},500);\n`, { mode: 0o755 });
      const entry = join(dir, 'entry.mjs');
      const harness = new URL('./harness.ts', import.meta.url).href;
      await writeFile(entry, `import { Server } from 'node:http';
import { setupE2EHarness } from ${JSON.stringify(harness)};
const servers = []; const listen = Server.prototype.listen;
Server.prototype.listen = function(...args) { servers.push(this); return listen.apply(this,args); };
const original = Server.prototype.closeAllConnections;
let injected = false;
Server.prototype.closeAllConnections = function() { original.call(this); if (!injected) { injected = true; this.close(); throw Error('cleanup sentinel'); } };
let error;
try { const h = await setupE2EHarness(); await h.close(); } catch(e) { error = e; }
const errors = error instanceof AggregateError ? error.errors : [error];
if (!errors.some(e => String(e?.message).includes('cleanup sentinel'))) throw Error('missing cleanup error');
if (${startupFailure} && !errors.some(e => String(e?.message).includes('startup sentinel'))) throw Error('missing startup error');
if (servers.length !== 2 || servers.some(s => s.listening)) throw Error('remaining server not closed');
`);
      const proc = spawn(process.execPath, ['--experimental-strip-types', entry], { env: { ...process.env, TMPDIR: dir, PATH: `${dir}:${process.env.PATH}` }, stdio: ['ignore', 'ignore', 'pipe'] });
      let output = ''; proc.stderr!.on('data', chunk => { output += chunk; });
      const code = await waitForEntry(proc, 45_000);
      expect(code, output).toBe(0);
      expect((await readdir(dir)).filter(name => name.startsWith('mecha-e2e-'))).toEqual([]);
    } finally { await rm(dir, { recursive: true, force: true }); }
  });
}

test('router readiness timeout does not wait for inherited output pipes', async () => {
  test.skip(process.platform === 'win32', 'POSIX fixture');
  test.setTimeout(25_000);
  const dir = await mkdtemp(join(tmpdir(), 'router-readiness-'));
  const previousPath = process.env.PATH;
  try {
    const router = `#!${process.execPath}\nconst {spawn}=require('node:child_process'); spawn(process.execPath,['-e','setTimeout(()=>{},20000)'],{detached:true,stdio:['ignore','inherit','inherit']}).unref(); process.on('SIGQUIT',()=>{}); setInterval(()=>{},1000);\n`;
    await writeFile(join(dir, 'go'), `#!${process.execPath}\nconst fs=require('node:fs'); const a=process.argv; fs.writeFileSync(a[a.indexOf('-o')+1],${JSON.stringify(router)},{mode:0o755});\n`, { mode: 0o755 });
    process.env.PATH = `${dir}:${previousPath}`;
    const { startIsolatedRouter } = await import('./harness');
    const start = Date.now();
    await expect(startIsolatedRouter({ repoRoot: resolve('.') })).rejects.toThrow(/timed out during startup/);
    expect(Date.now() - start).toBeLessThan(19_000);
  } finally {
    process.env.PATH = previousPath;
    await rm(dir, { recursive: true, force: true });
  }
});
