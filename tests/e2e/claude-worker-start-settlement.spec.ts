import { readFile, rename, writeFile } from 'node:fs/promises';
import { appendFileSync } from 'node:fs';
import { join } from 'node:path';
import { randomUUID } from 'node:crypto';
import { test, expect } from '@playwright/test';
import { claudeWorkersHarness as createHarness } from './claude-workers-harness';

// Opt-in private diagnostics capture startup even if fixture creation fails.
// Raw stderr can contain sensitive data; use only with synthetic fixtures.
function claudeWorkersHarness(script: object) {
  const log = process.env.CLAUDE_START_E2E_STDERR;
  return createHarness(script, {
    noCodexProjectionFixture: true,
    waitTimeout: 30_000,
    onRouterSpawnFixture: log ? child => child.stderr?.on('data', chunk => appendFileSync(log, chunk, { mode: 0o600 })) : undefined,
  });
}

// Clock contract: advancing the router's injected clock wakes Claude retry
// timers as well as changing Now(). No test-side offer is sent after the mail.
// The executable is replaced only after the launch turn has completed. Each
// auth invocation is recorded before its deterministic preflight response.
async function installPreflightFixture(root: string, failures: number, response = 'malformed') {
  const binary = join(root, 'bin', 'claude');
  // Node treats unknown extensions as errors for this ESM fixture.
  const saved = `${binary}.saved.mjs`;
  const count = join(root, 'preflight-count');
  await rename(binary, saved);
  await writeFile(count, '0');
  const output = response === 'signed-out' ? '{"loggedIn":false}' : 'not-json';
  await writeFile(binary, `#!${process.execPath}\n` + `
const fs = require('node:fs');
const { spawnSync } = require('node:child_process');
const args = process.argv.slice(2);
if (args[0] === 'auth') {
  const file = ${JSON.stringify(count)};
  const n = Number(fs.readFileSync(file, 'utf8')) + 1;
  fs.writeFileSync(file, String(n));
  if (n <= ${failures}) { console.log(${JSON.stringify(output)}); process.exit(0); }
}
const child = spawnSync(${JSON.stringify(saved)}, args, { stdio: 'inherit', env: process.env });
process.exit(child.status ?? 1);
`, { mode: 0o700 });
  return async () => Number(await readFile(count, 'utf8'));
}

// A CLI invocation is not a completed failure: synchronize clock changes with
// the committed, cleanup-complete retry record instead of process output.
async function waiting(f: Awaited<ReturnType<typeof claudeWorkersHarness>>, id: string, executions: number) {
  await expect.poll(() => f.rows('SELECT executions,in_flight FROM claude_start_retries WHERE message_id=?', id)[0],
    { timeout: 45_000 }).toMatchObject({ executions, in_flight: 0 });
  return f.rows('SELECT * FROM claude_start_retries WHERE message_id=?', id)[0];
}

async function send(f: Awaited<ReturnType<typeof claudeWorkersHarness>>, page: import('@playwright/test').Page, agent: string) {
  const { now } = await f.router.advanceClock(1);
  await page.clock.setFixedTime(new Date(now));
  const id = randomUUID();
  const result = await f.request(page, '/v1/messages', { id, from: 'user', to: agent, body: 'Recover this accepted mail', created_at: new Date(now).toISOString() });
  expect([200, 201, 202]).toContain(result.status);
  return id;
}

for (const failures of [1, 2]) test(`CSR-001 recovers after ${failures} definite startup failure(s) without a ping`, async ({ page }) => {
  test.setTimeout(90_000);
  const f = await claudeWorkersHarness({ turns_by_input: [{ contains: 'Launch the fixture', steps: [{ text: 'Fixture launch complete' }] }, { contains: 'Recover this accepted mail', steps: [{ text: 'Recovered answer' }] }] });
  try {
    await f.open(page);
    const agent = await f.launch(page, { initial_instruction: 'Launch the fixture' });
    await f.final(page, agent, 'Fixture launch complete');
    const count = await installPreflightFixture(f.fixtureRoot, failures);
    const id = await send(f, page, agent);
    await expect.poll(count, { timeout: 15_000 }).toBe(1);
    // Wait for the failed turn to finish before advancing the frozen clock.
    await waiting(f, id, 1);
    await f.router.advanceClock(4_999);
    expect(await count()).toBe(1);
    await f.router.advanceClock(1);
    await expect.poll(count, { timeout: 8_000, message: 'first retry must wake on test clock' }).toBe(2);
    if (failures === 2) {
      await waiting(f, id, 2);
      await f.router.advanceClock(29_999);
      expect(await count()).toBe(2);
      await f.router.advanceClock(1);
      await expect.poll(count, { timeout: 8_000 }).toBe(3);
    }
    await f.final(page, agent, 'Recovered answer');
    expect((await f.inputs()).filter(row => JSON.stringify(row).includes('Recover this accepted mail'))).toHaveLength(1);
    expect(f.rows("SELECT * FROM managed_complete_history WHERE agent_id=? AND level='error'", agent)).toHaveLength(0);
    expect(f.rows('SELECT state FROM messages WHERE id=?', id)[0]?.state).not.toBe('dead_letter');
    expect(f.rows('SELECT * FROM claude_start_retries WHERE message_id=?', id)).toHaveLength(0);
  } finally { await f.close(); }
});

test('CSR-002 persistent failure settles only at the fixed deadline', async ({ page }) => {
  test.setTimeout(300_000);
  const f = await claudeWorkersHarness({ turns: [[{ text: 'Fixture launch complete' }]] });
  try {
    await f.open(page);
    const agent = await f.launch(page, { initial_instruction: 'Launch the fixture' });
    await f.final(page, agent, 'Fixture launch complete');
    const count = await installPreflightFixture(f.fixtureRoot, 99);
    const id = await send(f, page, agent);
    await expect.poll(count, { timeout: 15_000 }).toBe(1);
    await waiting(f, id, 1);
    for (const [delay, expected] of [[5_000, 2], [30_000, 3], [120_000, 4], [180_000, 5]] as const) {
      await f.router.advanceClock(delay - 1);
      expect(await count()).toBe(expected - 1);
      expect(f.rows('SELECT state FROM messages WHERE id=?', id)[0]?.state).not.toBe('dead_letter');
      await f.router.advanceClock(1);
      await waiting(f, id, expected);
      expect(await count()).toBe(expected);
    }
    await f.router.advanceClock(59_999);
    expect(await count()).toBe(5);
    expect(f.rows('SELECT state FROM messages WHERE id=?', id)[0]?.state).not.toBe('dead_letter');
    await f.router.advanceClock(1);
    await expect.poll(() => f.rows('SELECT state FROM messages WHERE id=?', id)[0]?.state, { timeout: 45_000 }).toBe('dead_letter');
    expect(await count()).toBe(5);
    const history = f.rows("SELECT message,message_content FROM managed_complete_history WHERE agent_id=? AND level='error'", agent);
    expect(history).toHaveLength(1);
    expect(JSON.stringify(history)).toMatch(/unknown/i);
    expect(JSON.stringify(history)).not.toContain(f.fixtureRoot);
  } finally { await f.close(); }
});

test('CSR-003 confirmed signed-out preflight settles promptly as auth', async ({ page }) => {
  test.setTimeout(90_000);
  const f = await claudeWorkersHarness({ turns: [[{ text: 'Fixture launch complete' }]] });
  try {
    await f.open(page);
    const agent = await f.launch(page, { initial_instruction: 'Launch the fixture' });
    await f.final(page, agent, 'Fixture launch complete');
    const count = await installPreflightFixture(f.fixtureRoot, 99, 'signed-out');
    const id = await send(f, page, agent);
    await expect.poll(() => f.rows('SELECT state FROM messages WHERE id=?', id)[0]?.state, { timeout: 8_000 }).toBe('dead_letter');
    expect(await count()).toBe(1);
    const history = f.rows("SELECT message,message_content FROM managed_complete_history WHERE agent_id=? AND level='error'", agent);
    expect(history).toHaveLength(1);
    expect(JSON.stringify(history)).toMatch(/auth/i);
    expect(JSON.stringify(history)).not.toContain(f.fixtureRoot);
  } finally { await f.close(); }
});

for (const failure of ['exec-start', 'exit'] as const) test(`CD-004 ${failure} fails visibly without producing an answer`, async ({ page }) => {
  test.setTimeout(90_000);
  const f = await claudeWorkersHarness({ turns: [[{ text: 'Fixture launch complete' }]] });
  try {
    await f.open(page);
    const agent = await f.launch(page, { initial_instruction: 'Launch the fixture' });
    await f.final(page, agent, 'Fixture launch complete');
    const before = f.rows("SELECT count(*) AS n FROM embedded_entries WHERE agent_id=? AND kind='assistant'", agent)[0].n;
    if (failure === 'exec-start') await f.breakExecutable();
    else await writeFile(join(f.fixtureRoot, 'bin', 'claude'), '#!/bin/sh\nif [ "$1" = auth ]; then echo \'{"loggedIn":true}\'; exit 0; fi\nexit 73\n', { mode: 0o700 });
    const id = await send(f, page, agent);
    // Both failures are ambiguous during preflight or process acquisition.
    // Advance only after each failure and cleanup have been durably recorded.
    {
      await waiting(f, id, 1);
      for (const [delay, n] of [[5_000, 2], [30_000, 3], [120_000, 4], [180_000, 5]] as const) {
        await f.router.advanceClock(delay);
        await waiting(f, id, n);
      }
      await f.router.advanceClock(60_000);
    }
    await expect.poll(() => f.rows('SELECT state FROM messages WHERE id=?', id)[0]?.state, { timeout: 15_000 }).toBe('dead_letter');
    await expect(page.getByText(`Claude startup failed (${failure === 'exec-start' ? 'unknown' : 'probe-died'}).`, { exact: false }).first()).toBeVisible({ timeout: 10_000 });
    const history = f.rows("SELECT message,message_content FROM managed_complete_history WHERE agent_id=? AND level='error'", agent);
    expect(history).toHaveLength(1);
    expect(JSON.stringify(history)).toMatch(failure === 'exec-start' ? /unknown/i : /probe-died/i);
    expect(history[0].message).toContain(failure === 'exec-start'
      ? 'Inspect the router logs and Claude CLI on the router host, then retry the message.'
      : 'Inspect the router logs and Claude CLI health, then retry the message.');
    expect(JSON.stringify(history)).not.toContain(f.fixtureRoot);
    expect(f.rows("SELECT count(*) AS n FROM embedded_entries WHERE agent_id=? AND kind='assistant'", agent)[0].n).toBe(before);
    expect(f.rows('SELECT * FROM claude_start_retries WHERE message_id=?', id)).toHaveLength(0);
    expect(f.requests).toHaveLength(0);
  } finally { await f.close(); }
});

test('CSR-005 waiting retry survives router restart without resetting its budget', async ({ page }) => {
  test.setTimeout(90_000);
  const f = await claudeWorkersHarness({ turns_by_input: [
    { contains: 'Launch the fixture', steps: [{ text: 'Fixture launch complete' }] },
    { contains: 'Recover this accepted mail', steps: [{ text: 'Recovered after restart' }] },
  ] });
  try {
    await f.open(page);
    const agent = await f.launch(page, { initial_instruction: 'Launch the fixture' });
    await f.final(page, agent, 'Fixture launch complete');
    await installPreflightFixture(f.fixtureRoot, 1);
    const id = await send(f, page, agent);
    const original = await waiting(f, id, 1);
    await f.restart();
    const after = await waiting(f, id, 1);
    expect(after.deadline).toBe(original.deadline);
    expect(after.next_eligible).toBe(original.next_eligible);
    // A separate restart continuation may perform its own preflight. The
    // durable retry row, not global auth invocations, proves no retry ran.
    expect(after.executions).toBe(1);
    // Restart creates a new injected clock; advance to the persisted absolute
    // eligibility rather than assuming that its initial instant is unchanged.
    const { now } = await f.router.advanceClock(1);
    const delta = Date.parse(String(original.next_eligible)) - Date.parse(now) + 1_000;
    // The clock endpoint returns whole seconds; include its truncated fraction.
    await f.router.advanceClock(Math.max(1, delta));
    // Auth completion is not mail submission: restart can first run a separate
    // continuation. Wait for the exact accepted mail at the CLI boundary.
    await expect.poll(async () => (await f.inputs()).filter(row => JSON.stringify(row).includes('Recover this accepted mail')).length,
      { timeout: 30_000 }).toBe(1);
    await f.final(page, agent, 'Recovered after restart');
    expect((await f.inputs()).filter(row => JSON.stringify(row).includes('Recover this accepted mail'))).toHaveLength(1);
    expect(f.rows('SELECT * FROM claude_start_retries WHERE message_id=?', id)).toHaveLength(0);
  } finally { await f.close(); }
});
