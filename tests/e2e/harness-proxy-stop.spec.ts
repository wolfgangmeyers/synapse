import { test, expect } from '@playwright/test';
import { mkdtemp, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { startRouterProxy } from './harness';

test('proxy cleanup does not wait for a descendant holding its pipes', async () => {
  const dir = await mkdtemp(join(tmpdir(), 'proxy-stop-test-'));
  const oldPath = process.env.PATH;
  try {
    const proxyScript = `#!${process.execPath}\nconst {spawn}=require('node:child_process');\nspawn(process.execPath,['-e','setTimeout(() => {}, 5000)'],{stdio:['ignore','inherit','inherit']});\nconsole.log('MECHA_ROUTER_PROXY_READY');\nsetInterval(() => {}, 1000);\n`;
    await writeFile(join(dir, 'go'), `#!${process.execPath}\nconst fs=require('node:fs'); const args=process.argv; fs.writeFileSync(args[args.indexOf('-o')+1],${JSON.stringify(proxyScript)},{mode:0o755});\n`, { mode: 0o755 });
    process.env.PATH = `${dir}:${oldPath}`;
    const proxy = await startRouterProxy({ repoRoot: resolve('.'), upstreamPort: 1 });
    const started = Date.now();
    await proxy.close();
    expect(Date.now() - started).toBeLessThan(3000);
    expect(proxy.process.exitCode !== null || proxy.process.signalCode !== null).toBe(true);
  } finally {
    process.env.PATH = oldPath;
    await rm(dir, { recursive: true, force: true });
  }
});

test('proxy startup reports an exit without waiting for inherited pipes', async () => {
  const dir = await mkdtemp(join(tmpdir(), 'proxy-exit-test-'));
  const oldPath = process.env.PATH;
  try {
    const proxyScript = `#!${process.execPath}\nconst {spawn}=require('node:child_process');\nspawn(process.execPath,['-e','setTimeout(() => {}, 5000)'],{stdio:['ignore','inherit','inherit']});\nconsole.error('proxy fixture failed');\nprocess.exit(7);\n`;
    await writeFile(join(dir, 'go'), `#!${process.execPath}\nconst fs=require('node:fs'); const args=process.argv; fs.writeFileSync(args[args.indexOf('-o')+1],${JSON.stringify(proxyScript)},{mode:0o755});\n`, { mode: 0o755 });
    process.env.PATH = `${dir}:${oldPath}`;
    const started = Date.now();
    await expect(startRouterProxy({ repoRoot: resolve('.'), upstreamPort: 1 })).rejects.toThrow(/Router proxy exited \(code 7\)\./);
    expect(Date.now() - started).toBeLessThan(3000);
  } finally {
    process.env.PATH = oldPath;
    await rm(dir, { recursive: true, force: true });
  }
});
