import { DatabaseSync } from 'node:sqlite';
import { randomUUID } from 'node:crypto';
import { test, expect } from '@playwright/test';
import { claudeWorkersHarness } from './claude-workers-harness';

const diagnostic = 'Claude mail could not be classified safely and is blocked from automatic retry. Inspect the router logs and conversation before retrying or restarting the router.';

test('unclassified diagnostic is retried after a witnessed SQLite write failure', async ({ page }) => {
  test.setTimeout(100_000);
  const f = await claudeWorkersHarness({ turns: [[{ text: 'Fixture launch complete' }]] });
  let db: DatabaseSync | undefined;
  try {
    await f.open(page);
    const agent = await f.launch(page, { initial_instruction: 'Launch the fixture' });
    await f.final(page, agent, 'Fixture launch complete');
    const starts = await f.starts();
    const inputs = await f.inputs();
    expect(starts).toHaveLength(1);
    expect(inputs).toHaveLength(1);
    db = new DatabaseSync(f.router.databasePath);
    db.exec('PRAGMA busy_timeout=5000');
    // Match EnterEmbeddedSDKExecution(nil): a valid, owner-fenced SDK token.
    const enter = db.prepare(`UPDATE embedded_sessions SET execution_token=execution_token+1,
      execution_json=json_object('kind','sdk','owner',owner_token,'token',execution_token+1,'source',NULL,'current',NULL)
      WHERE agent_id=? AND current=1 AND execution_json IS NULL
      RETURNING owner_token,execution_token`);
    let execution: ReturnType<typeof enter.get>;
    await expect.poll(() => { execution = enter.get(agent); return execution !== undefined; }, { timeout: 10_000 }).toBe(true);
    if (!execution || typeof execution.owner_token !== 'number' || typeof execution.execution_token !== 'number') throw new Error('Invalid execution provenance');
    db.exec(`CREATE TRIGGER diagnostic_fault BEFORE INSERT ON managed_complete_history WHEN NEW.message = '${diagnostic.replaceAll("'", "''")}' BEGIN SELECT RAISE(ABORT,'diagnostic fault'); END`);
    const { now } = await f.router.advanceClock(1);
    await page.clock.setFixedTime(new Date(now));
    const id = randomUUID();
    const sending = f.request(page, '/v1/messages', { id, from: 'user', to: agent, body: 'Diagnostic retry fixture', created_at: new Date(now).toISOString() });
    // A positive router error is the handshake: an absent row alone cannot prove an attempted write.
    await expect.poll(() => f.router.stderr(), { timeout: 20_000 }).toMatch(/cannot record blocked Claude mail diagnostic:.*diagnostic fault/);
    const failure = f.router.stderr().split('\n').find(line => line.includes(`embedded:${agent}`) && line.includes('cannot record blocked Claude mail diagnostic:') && line.includes('diagnostic fault'));
    expect(failure).toBeTruthy();
    expect(f.rows('SELECT id FROM messages WHERE id=?', id)).toHaveLength(1);
    const cleared = db.prepare('UPDATE embedded_sessions SET execution_json=NULL WHERE agent_id=? AND current=1 AND owner_token=? AND execution_token=? AND json_extract(execution_json,\'$.kind\')=\'sdk\' AND json_extract(execution_json,\'$.owner\')=? AND json_extract(execution_json,\'$.token\')=?').run(agent, execution.owner_token, execution.execution_token, execution.owner_token, execution.execution_token);
    expect(cleared.changes).toBe(1);
    db.exec('DROP TRIGGER diagnostic_fault');
    const sent = await sending;
    expect([200, 201, 202]).toContain(sent.status);
    await expect.poll(() => f.rows('SELECT message FROM managed_complete_history WHERE agent_id=? AND message=?', agent, diagnostic).length, { timeout: 15_000 }).toBe(1);
    await expect(page.getByText(diagnostic, { exact: true }).first()).toBeVisible();
    await expect.poll(() => f.wsFrames.some(raw => {
      try {
        const frame = JSON.parse(raw);
        return frame.type === 'history_entry' && frame.view === 'complete' &&
          frame.entry?.agent_id === agent && frame.entry?.message === diagnostic;
      } catch { return false; }
    })).toBe(true);
    // The scheduler uses wall time. Observe beyond its five-second retry interval,
    // not merely immediately after the first successful diagnostic write.
    await new Promise(resolve => setTimeout(resolve, 6_000));
    expect(f.rows('SELECT message FROM managed_complete_history WHERE agent_id=? AND message=?', agent, diagnostic)).toHaveLength(1);
    expect(f.rows('SELECT claude_stdin_started FROM messages WHERE id=?', id)).toEqual([{ claude_stdin_started: 0 }]);
    expect(f.rows('SELECT * FROM embedded_acks WHERE message_id=?', id)).toHaveLength(0);
    expect(await f.starts()).toEqual(starts);
    expect(await f.inputs()).toEqual(inputs);
    expect(f.violations).toEqual([]);
  } finally { db?.close(); await f.close(); }
});
