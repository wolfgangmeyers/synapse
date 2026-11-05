import { DatabaseSync } from 'node:sqlite';
import { test, expect } from '@playwright/test';
import { claudeWorkersHarness } from './claude-workers-harness';

test.describe('embedded relaunch safety', () => {
  test.setTimeout(90000);
  test('normal termination permits relaunch', async ({ page }) => {
    const f = await claudeWorkersHarness();
    try {
      await f.open(page); const name = await f.launch(page); await f.idle(name);
      const old = f.tableRows('embedded_sessions', name)[0].session_id;
      expect((await f.request(page, `/v1/managed-agents/${name}`, undefined, 'DELETE')).status).toBe(202);
      await expect.poll(() => f.tableRows('managed_agents', name).length).toBe(0);
      expect(f.tableRows('embedded_sessions', name)).toHaveLength(0);
      const result = await f.request(page, '/v1/managed-agents', { name, cwd: `${f.router.root}/work`, runtime: 'embedded', model: { provider: 'claude-code', id: 'sonnet' } }, 'POST', true);
      expect(result.status, JSON.stringify(result.body)).toBe(202);
      expect(f.tableRows('embedded_sessions', name)[0].session_id).not.toBe(old);
    } finally { await f.close(); }
  });
  test('orphan session refuses relaunch without mutation', async ({ page }) => {
    const f = await claudeWorkersHarness();
    try {
      await f.open(page);
      const name = 'orphan-relaunch-test';
      // Fault injection on the isolated test database only: reproduce an old
      // inconsistent store without claiming that ordinary termination causes it.
      const db = new DatabaseSync(f.router.databasePath);
      try { db.exec('PRAGMA busy_timeout=10000; PRAGMA foreign_keys=OFF'); db.prepare("INSERT INTO embedded_sessions(agent_id,session_id,epoch,generation,role,current) VALUES(?, 'old-session',1,'old-generation','root',1)").run(name); } finally { db.close(); }
      const before = f.tableRows('embedded_sessions', name);
      // The independent retention sweep may advance this monotonic cutoff.
      // Compare every other column, including all ownership and execution state.
      const assertSessionPreserved = () => {
        const after = f.tableRows('embedded_sessions', name);
        expect(after).toHaveLength(1);
        const { evidence_expired_through_ms: oldCutoff, ...oldSession } = before[0];
        const { evidence_expired_through_ms: newCutoff, ...newSession } = after[0];
        expect(newSession).toEqual(oldSession);
        expect(Number(newCutoff)).toBeGreaterThanOrEqual(Number(oldCutoff));
      };
      const operationsBefore = f.rows('SELECT * FROM managed_http_operations WHERE ? IS NOT NULL ORDER BY operation_id', name);
      const result = await f.request(page, '/v1/managed-agents', { name, cwd: `${f.router.root}/work`, runtime: 'embedded', model: { provider: 'claude-code', id: 'sonnet' } }, 'POST', true);
      expect(result.status).toBe(409);
      expect(JSON.stringify(result.body)).toContain('stale embedded session');
      assertSessionPreserved();
      expect(f.tableRows('managed_agents', name)).toHaveLength(0);
      expect(f.rows('SELECT * FROM managed_http_operations WHERE ? IS NOT NULL ORDER BY operation_id', name)).toEqual(operationsBefore);

      await page.getByRole('button', { name: 'Managed actions', exact: true }).click();
      await page.getByRole('menuitem', { name: 'Launch Agent', exact: true }).click();
      const form = page.getByRole('dialog', { name: 'Launch managed agent' });
      await form.getByLabel('Name', { exact: true }).fill(name);
      await form.getByLabel('Working folder', { exact: true }).fill(`${f.router.root}/work`);
      await form.getByRole('button', { name: 'Use router default' }).click();
      await page.getByRole('option').filter({ hasText: 'claude-code/sonnet' }).click();
      await form.getByRole('button', { name: 'Launch', exact: true }).click();
      await expect(form.getByRole('alert')).toContainText('stale embedded session');
      assertSessionPreserved();
      expect(f.tableRows('managed_agents', name)).toHaveLength(0);
      expect(f.rows('SELECT * FROM managed_http_operations WHERE ? IS NOT NULL ORDER BY operation_id', name)).toEqual(operationsBefore);
    } finally { await f.close(); }
  });
  test('active name remains reserved', async ({ page }) => {
    const f = await claudeWorkersHarness();
    try {
      await f.open(page); const name = await f.launch(page); await f.idle(name);
      const before = f.tableRows('embedded_sessions', name);
      const result = await f.request(page, '/v1/managed-agents', { name, cwd: `${f.router.root}/work`, runtime: 'embedded', model: { provider: 'claude-code', id: 'sonnet' } }, 'POST', true);
      expect(result.status).toBe(409);
      expect(JSON.stringify(result.body)).toContain('NAME_IN_USE');
      expect(JSON.stringify(result.body)).toContain('active or launching agent');
      expect(f.tableRows('embedded_sessions', name)).toEqual(before);
    } finally { await f.close(); }
  });
});
