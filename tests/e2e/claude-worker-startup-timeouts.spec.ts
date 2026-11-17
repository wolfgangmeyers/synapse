import { test, expect } from '@playwright/test';
import { claudeWorkersHarness } from './claude-workers-harness';

test.describe('Claude worker startup timeouts (SPEC-CLAUDE-STARTUP-TIMEOUTS-001)', () => {
  test('SCN-CST-001 slow startup with 6s preflight auth succeeds within 15s timeout', async ({ page }) => {
    test.setTimeout(90_000);
    const f = await claudeWorkersHarness({
      turns: [[{ text: 'Slow startup complete' }]],
    });

    try {
      await f.open(page);
      const agent = await f.launch(page, {
        initial_instruction: 'Test slow startup',
      });
      await f.final(page, agent, 'Slow startup complete');

      const row = f.rows('SELECT lifecycle_state, activity_state FROM managed_agents WHERE agent_id=?', agent)[0];
      expect(row?.lifecycle_state).toBe('running');
    } finally {
      await f.close();
    }
  });

  test('SCN-CST-002 preflight failure attributes preflight_auth in launch error response', async ({ page }) => {
    test.setTimeout(90_000);
    const f = await claudeWorkersHarness({
      auth: { loggedIn: false },
    });

    try {
      await f.open(page);
      const res = await f.request(page, '/v1/managed-agents', {
        name: 'claude-fail-worker',
        runtime: 'embedded',
        model: { provider: 'claude-code', id: 'sonnet' },
        cwd: `${f.router.root}/work`, initial_instruction: 'Test auth failure',
      });
      expect(res.status).toBe(422);
      expect(res.body.error.code).toBe('CLAUDE_UNAVAILABLE');
      expect(res.body.error.message).toContain("startup failed at step 'preflight_auth'");
    } finally {
      await f.close();
    }
  });
});
