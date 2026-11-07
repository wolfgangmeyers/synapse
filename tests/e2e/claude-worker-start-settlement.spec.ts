import { writeFile } from 'node:fs/promises';
import { join } from 'node:path';
import { randomUUID } from 'node:crypto';
import { test, expect } from '@playwright/test';
import { claudeWorkersHarness } from './claude-workers-harness';

for (const failure of ['exec-start', 'exit'] as const) test(`CD-004 worker ${failure} is visible and never succeeds`, async ({page}) => {
  test.setTimeout(100_000);
  const f = await claudeWorkersHarness({turns:[[{text:'Fixture launch complete'}]]});
  try {
    await f.open(page);
    const agent = await f.launch(page, {initial_instruction:'Launch the fixture'});
    await f.final(page, agent, 'Fixture launch complete');
    const before = f.rows(`SELECT count(*) AS n FROM embedded_entries WHERE agent_id=? AND kind='assistant'`,agent)[0].n;
    if (failure === 'exec-start') await f.breakExecutable();
    else await writeFile(join(f.fixtureRoot,'bin','claude'), '#!/bin/sh\nif [ "$1" = auth ]; then echo \'{"loggedIn":true}\'; exit 0; fi\nexit 73\n', {mode:0o700});
    const {now}=await f.router.advanceClock(1);
    await page.clock.setFixedTime(new Date(now));
    const id=randomUUID();
    const sent=await f.request(page,'/v1/messages',{id,from:'user',to:agent,body:'Failure fixture request',created_at:new Date(now).toISOString()});
    expect([200,201,202]).toContain(sent.status);
    await expect.poll(()=>f.rows(`SELECT state FROM messages WHERE id=?`,id)[0]?.state,{timeout:45000}).toBe('dead_letter');
    await expect(page.getByText(/Claude could not start or acquire a process/).first()).toBeVisible({timeout:10000});
    const history=f.rows(`SELECT level,message,message_content FROM managed_complete_history WHERE agent_id=? AND level='error'`,agent);
    expect(history).toHaveLength(1); expect(history[0].message).toContain('retry');
    expect(f.rows(`SELECT count(*) AS n FROM embedded_entries WHERE agent_id=? AND kind='assistant'`,agent)[0].n).toBe(before);
    expect(f.requests).toHaveLength(0);
  } finally { await f.close(); }
});
