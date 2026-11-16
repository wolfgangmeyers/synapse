import { mkdtemp, readFile, rename, rm, symlink, writeFile } from 'node:fs/promises';
import { existsSync } from 'node:fs';
import { join } from 'node:path';
import { tmpdir } from 'node:os';
import { randomUUID } from 'node:crypto';
import { expect, test, type Page } from '@playwright/test';
import { claudeWorkersHarness, type ClaudeWorkersHarness } from './claude-workers-harness';

async function send(f: ClaudeWorkersHarness, page: Page, agent: string) {
  const { now } = await f.router.advanceClock(1);
  await page.clock.setFixedTime(new Date(now));
  const id = randomUUID();
  const result = await f.request(page, '/v1/messages', { id, from: 'user', to: agent, body: 'Preflight privacy mail', created_at: new Date(now).toISOString() });
  expect([200, 201, 202]).toContain(result.status);
  return id;
}

const errors = (f: ClaudeWorkersHarness, agent: string) => f.rows("SELECT message,message_content FROM managed_complete_history WHERE agent_id=? AND level='error'", agent);

test('CSR-003 confirmed missing executable settles once with an actionable spawn hint', async ({ page }) => {
  test.setTimeout(90_000);
  let log = '';
  // Isolate router PATH from host Claude installations without changing the
  // harness or allowing a missing fixture to invoke a real executable.
  const originalPath = process.env.PATH;
  const tools = await mkdtemp(join(tmpdir(), 'claude-preflight-tools-'));
  await symlink(process.execPath, join(tools, 'node'));
  process.env.PATH = [tools, ...(originalPath ?? '').split(':').filter(dir => dir && !existsSync(join(dir, 'claude')))].join(':');
  let f: ClaudeWorkersHarness;
  try {
    f = await claudeWorkersHarness({ turns: [[{ text: 'Fixture ready' }]] }, { noCodexProjectionFixture: true, waitTimeout: 30_000,
      onRouterSpawnFixture: child => child.stderr?.on('data', chunk => { log += String(chunk); }) });
  } finally {
    if (originalPath === undefined) delete process.env.PATH; else process.env.PATH = originalPath;
  }
  try {
    await f.open(page);
    const agent = await f.launch(page, { initial_instruction: 'Launch fixture' });
    await f.final(page, agent, 'Fixture ready');
    await rename(join(f.fixtureRoot, 'bin/claude'), join(f.fixtureRoot, 'bin/claude.saved'));
    const id = await send(f, page, agent);
    await expect.poll(() => f.rows('SELECT state FROM messages WHERE id=?', id)[0]?.state, { timeout: 15_000 }).toBe('dead_letter');
    expect(f.rows('SELECT * FROM claude_start_retries WHERE message_id=?', id)).toHaveLength(0);
    await expect.poll(() => errors(f, agent)).toHaveLength(1);
    const history = JSON.stringify(errors(f, agent));
    expect(history).toMatch(/startup failed \(spawn\)/i);
    expect(history).toMatch(/install the Claude CLI on the router host/i);
    expect(history).not.toContain(f.fixtureRoot);
    await expect.poll(() => log.split('\n').find(line => line.includes('Claude definite start failure') && line.includes(id)) ?? '', { timeout: 10_000 }).toContain(`[embedded:${agent}] Claude definite start failure (message: ${id}, attempt: 1, category: spawn):`);
    expect(log.split('\n').find(line => line.includes('Claude definite start failure') && line.includes(id))).toContain('definite Claude acquisition or start failure');
    await expect(page.getByText('Claude startup failed (spawn). Check or install the Claude CLI on the router host, then retry the message.', { exact: false }).first()).toBeVisible();
    await f.router.advanceClock(400_000);
    expect(errors(f, agent)).toHaveLength(1);
    expect((await f.inputs()).filter(row => JSON.stringify(row).includes('Preflight privacy mail'))).toHaveLength(0);
  } finally { try { await f.close(); } finally { await rm(tools, { recursive: true, force: true }); } }
});

test('CSR-003 ambiguous preflight retries and keeps synthetic secrets out of diagnostics', async ({ page }) => {
  test.setTimeout(120_000);
  const f = await claudeWorkersHarness({ turns: [[{ text: 'Fixture ready' }]] }, { noCodexProjectionFixture: true, waitTimeout: 30_000 });
  try {
    await f.open(page);
    const agent = await f.launch(page, { initial_instruction: 'Launch fixture' });
    await f.final(page, agent, 'Fixture ready');
    const binary = join(f.fixtureRoot, 'bin/claude');
    const count = join(f.fixtureRoot, 'privacy-count');
    const secret = 'synthetic-secret-' + randomUUID();
    await writeFile(count, '0');
    await writeFile(binary, `#!${process.execPath}\n` + `
const fs = require('node:fs');
const count = ${JSON.stringify(count)};
fs.writeFileSync(count, String(Number(fs.readFileSync(count, 'utf8')) + 1));
console.log(${JSON.stringify('invalid-json ')} + ${JSON.stringify(secret)});
console.error(${JSON.stringify(secret)});
`, { mode: 0o700 });
    const id = await send(f, page, agent);
    const retry = () => f.rows('SELECT executions,in_flight FROM claude_start_retries WHERE message_id=?', id)[0];
    await expect.poll(retry, { timeout: 30_000 }).toMatchObject({ executions: 1, in_flight: 0 });
    expect(f.rows('SELECT state FROM messages WHERE id=?', id)[0]?.state).not.toBe('dead_letter');
    expect(errors(f, agent)).toHaveLength(0);
    for (const [delay, n] of [[5_000, 2], [30_000, 3], [120_000, 4], [180_000, 5]] as const) {
      await f.router.advanceClock(delay);
      await expect.poll(retry, { timeout: 30_000 }).toMatchObject({ executions: n, in_flight: 0 });
    }
    expect(Number(await readFile(count, 'utf8'))).toBe(5);
    await f.router.advanceClock(60_000);
    await expect.poll(() => f.rows('SELECT state FROM messages WHERE id=?', id)[0]?.state, { timeout: 30_000 }).toBe('dead_letter');
    await expect.poll(() => errors(f, agent)).toHaveLength(1);
    const hint = 'Claude startup failed (unknown). Inspect the router logs and Claude CLI on the router host, then retry the message.';
    await expect(page.getByText(hint, { exact: false }).first()).toBeVisible({ timeout: 10_000 });
    await expect.poll(() => f.wsFrames.some(frame => frame.includes('Claude startup failed (unknown)')), { timeout: 10_000 }).toBe(true);
    expect(JSON.stringify(errors(f, agent))).toContain(hint);
    expect(JSON.stringify(errors(f, agent))).not.toContain(secret);
    expect(JSON.stringify(f.wsFrames)).not.toContain(secret);
    expect(await page.locator('body').innerText()).not.toContain(secret);
    expect((await f.inputs()).filter(row => JSON.stringify(row).includes('Preflight privacy mail'))).toHaveLength(0);
  } finally { await f.close(); }
});
