import { randomUUID } from 'node:crypto';
import { test, expect } from '@playwright/test';
import { claudeWorkersHarness } from './claude-workers-harness';

test.describe('Claude Code Startup Resume Notice and Unconsumed Handling', () => {
  test('skips background resume notice and delivers real answer', async ({ page }) => {
    test.setTimeout(90000);
    // Turns configured so that on first input, Claude emits an unadmitted resume notice turn
    // (assistant text + result) before echoing the user input and answering with the real answer.
    const f = await claudeWorkersHarness({
      turns: [
        [{ text: 'Notice: background bash command killed' }],
        [{ text: 'real answer to user prompt' }],
      ],
    });
    try {
      await f.open(page);
      const agent = await f.launch(page);

      const messageId = randomUUID();
      const { now } = await f.router.advanceClock(1);
      const sent = await f.request(page, '/v1/messages', {
        id: messageId,
        from: 'user-sender',
        to: agent,
        body: 'real user prompt',
        created_at: new Date(now).toISOString(),
      });
      expect(sent.status).toBe(201);

      // Verify the final real answer arrives in the UI and conversation timeline
      await f.final(page, agent, 'real answer to user prompt');

      // Verify the message was acknowledged and not marked outcome uncertain
      await expect.poll(() => f.tableRows('embedded_acks', agent).filter(r => r.message_id === messageId).length).toBe(1);

      const messageRow = f.rows('SELECT claude_stdin_started, state FROM messages WHERE id=?', messageId)[0];
      expect(messageRow).toBeDefined();
      expect(messageRow.state).toBe('acknowledged');

      // ClaudeMailHeld: claude_stdin_started AND state='queued'
      // When delivered, it is not held as outcome uncertain
      const isHeld = Boolean(messageRow.claude_stdin_started && messageRow.state === 'queued');
      expect(isHeld).toBe(false);

      // Verify exactly 1 user input was received by Claude
      expect(await f.inputs()).toHaveLength(1);
    } finally {
      await f.close();
    }
  });

  test('pre-admission failure marks message as unconsumed for retry', async ({ page }) => {
    test.setTimeout(90000);
    // Script configured with a crash before user echo admission
    const f = await claudeWorkersHarness({
      turns: [
        [{ crash: 1 }],
      ],
    });
    try {
      await f.open(page);
      const agent = await f.launch(page);

      const messageId = randomUUID();
      const { now } = await f.router.advanceClock(1);
      const sent = await f.request(page, '/v1/messages', {
        id: messageId,
        from: 'sender',
        to: agent,
        body: 'important retryable task',
        created_at: new Date(now).toISOString(),
      });
      expect(sent.status).toBe(201);

      // Wait for the delivery attempt to fail and verify claude_stdin_started is reset to 0
      // rather than being left at 1 (which would mean held as outcome uncertain).
      await expect.poll(() => {
        const rows = f.rows('SELECT claude_stdin_started, state FROM messages WHERE id=?', messageId);
        if (rows.length === 0) return null;
        return rows[0].claude_stdin_started;
      }, { timeout: 30000 }).toBe(0);

      const messageRow = f.rows('SELECT claude_stdin_started, state FROM messages WHERE id=?', messageId)[0];
      expect(messageRow.claude_stdin_started).toBe(0);
      // The message is queued and eligible for retry rather than being dead-lettered / held uncertain
      expect(messageRow.state).toBe('queued');

      // Verify no ack was recorded for this failed attempt
      expect(f.tableRows('embedded_acks', agent).filter(r => r.message_id === messageId)).toHaveLength(0);
    } finally {
      await f.close();
    }
  });
});
