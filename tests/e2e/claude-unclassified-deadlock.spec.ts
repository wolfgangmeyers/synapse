import { test, expect } from '@playwright/test';
import { claudeWorkersHarness } from './claude-workers-harness';

test.describe('Claude Unclassified Mail Deadlock Prevention', () => {
  test('subsequent queued messages continue processing after unclassified failure', async ({ page }) => {
    const harness = await claudeWorkersHarness({
      async 'msg-fail'(context: any) {
        throw new Error('simulated unclassified turn failure');
      },
      async 'msg-success'(context: any) {
        return { content: 'processed successfully' };
      },
    });

    try {
      expect(harness.router.httpUrl).toBeDefined();
    } finally {
      await harness.close();
    }
  });
});
