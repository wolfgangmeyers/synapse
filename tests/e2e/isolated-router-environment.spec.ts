import { test, expect } from '@playwright/test';
import { readFile, mkdtemp, writeFile, chmod, rm } from 'node:fs/promises';
import { resolve, join } from 'node:path';
import { tmpdir } from 'node:os';
import type { ChildProcess } from 'node:child_process';
import { startIsolatedRouter, startRouterProxy } from './harness';

test('router excludes inherited machine environment', async () => {
  test.skip(process.platform !== 'linux', 'process environment inspection requires Linux');
  const before = process.env.GOOGLE_APPLICATION_CREDENTIALS;
  process.env.GOOGLE_APPLICATION_CREDENTIALS = '/nonexistent/machine-credential-sentinel';
  let router: Awaited<ReturnType<typeof startIsolatedRouter>> | undefined;
  try {
    router = await startIsolatedRouter({ repoRoot: resolve(import.meta.dirname, '../..'), managed: true });
    const env = (await readFile(`/proc/${router.process.pid}/environ`, 'utf8')).split('\0');
    expect(env.includes('GOOGLE_APPLICATION_CREDENTIALS=/nonexistent/machine-credential-sentinel')).toBe(false);
    expect(env.includes(`HOME=${router.root}/managed-home`)).toBe(true);
    expect(env.includes(`CLOUDSDK_CONFIG=${router.root}/config/gcloud`)).toBe(true);
  } finally {
    if (before === undefined) delete process.env.GOOGLE_APPLICATION_CREDENTIALS;
    else process.env.GOOGLE_APPLICATION_CREDENTIALS = before;
    await router?.close();
  }
});

for (const mode of ['exit', 'timeout', 'ready'] as const) {
  test(`router ${mode} diagnostics never expose synthetic secrets`, async () => {
    const root = await mkdtemp(join(tmpdir(), 'router-diagnostic-test-'));
    const executable = join(root, 'router');
    const sentinel = 'SYNTHETIC_SECRET_DIAGNOSTIC_83ab';
    await writeFile(executable, `#!${process.execPath}\nprocess.stdout.write(${JSON.stringify(sentinel)}); process.stderr.write(${JSON.stringify(sentinel)}); ${mode === 'exit' ? 'process.exit(7)' : mode === 'ready' ? `process.stdout.write('MECHA_V2_ROUTER_READY'); setInterval(() => {}, 1000)` : 'setInterval(() => {}, 1000)'}\n`);
    await chmod(executable, 0o700);
    let router: Awaited<ReturnType<typeof startIsolatedRouter>> | undefined;
    let message = '';
    try {
      try { router = await startIsolatedRouter({ repoRoot: resolve(import.meta.dirname, '../..'), routerExecutableFixture: executable, startupTimeoutFixtureMs: 300 }); }
      catch (error) { message = String(error); }
      if (mode === 'ready') {
        expect(!!router).toBe(true);
        await new Promise(r => setTimeout(r, 100));
        expect(router!.stderr().includes(sentinel)).toBe(false);
        expect(router!.readinessStderr().includes(sentinel)).toBe(false);
      } else {
        expect(!!message).toBe(true);
        expect(message.includes(sentinel)).toBe(false);
        expect(message.includes(mode === 'exit' ? 'code 7' : 'timed out')).toBe(true);
      }
    } finally { await router?.close(); await rm(root, { recursive: true, force: true }); }
  });
}

test('missing explicit auth dependencies fail before router startup', async () => {
  const previous = process.env.MECHA_E2E_AUTH_NODE_MODULES;
  process.env.MECHA_E2E_AUTH_NODE_MODULES = '/nonexistent/e2e-dependencies';
  let message = '';
  let router: Awaited<ReturnType<typeof startIsolatedRouter>> | undefined;
  try {
    try {
      router = await startIsolatedRouter({ repoRoot: resolve(import.meta.dirname, '../..'), managed: true, embedded: { endpoint: 'http://127.0.0.1:1' }, fakeEmbeddedCatalog: true });
    } catch (error) { message = String(error); }
    expect(message.includes('E2E auth-helper dependencies unavailable')).toBe(true);
    expect(!!router).toBe(false);
  } finally {
    await router?.close();
    if (previous === undefined) delete process.env.MECHA_E2E_AUTH_NODE_MODULES;
    else process.env.MECHA_E2E_AUTH_NODE_MODULES = previous;
  }
});

for (const kind of ['router', 'proxy'] as const) {
  for (const mode of ['exit', 'timeout'] as const) {
    test(`${kind} ${mode} consumes sentinel safely and terminates`, async () => {
      const root = await mkdtemp(join(tmpdir(), 'fixture-output-proof-'));
      const script = join(root, 'process');
      const proof = join(root, 'proof');
      const sentinel = 'SYNTHETIC_ONLY_DIAGNOSTIC_74cd';
      await writeFile(script, `#!${process.execPath}\nconst fs=require('node:fs'); process.on('SIGQUIT',()=>{}); process.on('SIGTERM',()=>{}); process.stdout.write(${JSON.stringify(sentinel)},()=>process.stderr.write(${JSON.stringify(sentinel)},()=>{fs.writeFileSync(${JSON.stringify(proof)},'written'); ${mode === 'exit' ? 'process.exit(7)' : 'setInterval(()=>{},1000)'} }));\n`);
      await chmod(script, 0o700);
      let message = '';
      let stdoutReceived = false, stderrReceived = false;
      const observe = (child: ChildProcess) => {
        child.stdout!.on('data', chunk => { if (String(chunk).includes(sentinel)) stdoutReceived = true; });
        child.stderr!.on('data', chunk => { if (String(chunk).includes(sentinel)) stderrReceived = true; });
      };
      const start = Date.now();
      try {
        try {
          if (kind === 'router') await startIsolatedRouter({ repoRoot: resolve(import.meta.dirname, '../..'), routerExecutableFixture: script, startupTimeoutFixtureMs: 500, onRouterSpawnFixture: observe });
          else await startRouterProxy({ repoRoot: resolve(import.meta.dirname, '../..'), upstreamPort: 1, proxyExecutableFixture: script, startupTimeoutFixtureMs: 500, onSpawn: observe });
        } catch (error) { message = String(error); }
        expect((await readFile(proof, 'utf8')) === 'written').toBe(true);
        expect(stdoutReceived).toBe(true);
        expect(stderrReceived).toBe(true);
        expect(message.includes(sentinel)).toBe(false);
        expect(message.includes(mode === 'exit' ? 'code 7' : 'timed out')).toBe(true);
        expect(Date.now() - start < 6000).toBe(true);
      } finally { await rm(root, { recursive: true, force: true }); }
    });
  }
}

test('proxy excludes inherited synthetic credentials', async () => {
  const root = await mkdtemp(join(tmpdir(), 'proxy-env-proof-'));
  const script = join(root, 'proxy');
  const previous = process.env.MECHA_TEST_PROXY_SECRET;
  process.env.MECHA_TEST_PROXY_SECRET = 'synthetic-proxy-secret';
  await writeFile(script, `#!${process.execPath}\nprocess.stdout.write(process.env.MECHA_TEST_PROXY_SECRET === undefined ? 'ENV_CLEAN\\n' : 'ENV_INHERITED\\n'); process.stdout.write('MECHA_ROUTER_PROXY_READY\\n'); setInterval(()=>{},1000);\n`);
  await chmod(script, 0o700);
  let clean = false, inherited = false;
  let proxy: Awaited<ReturnType<typeof startRouterProxy>> | undefined;
  try {
    proxy = await startRouterProxy({ repoRoot: resolve(import.meta.dirname, '../..'), upstreamPort: 1, proxyExecutableFixture: script, onSpawn: child => {
      child.stdout!.on('data', chunk => { const value = String(chunk); clean ||= value.includes('ENV_CLEAN'); inherited ||= value.includes('ENV_INHERITED'); });
    } });
    expect(clean).toBe(true);
    expect(inherited).toBe(false);
  } finally {
    await proxy?.close();
    if (previous === undefined) delete process.env.MECHA_TEST_PROXY_SECRET;
    else process.env.MECHA_TEST_PROXY_SECRET = previous;
    await rm(root, { recursive: true, force: true });
  }
});
