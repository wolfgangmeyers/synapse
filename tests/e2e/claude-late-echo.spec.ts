import { randomUUID } from 'node:crypto';
import { test, expect } from '@playwright/test';
import { claudeWorkersHarness } from './claude-workers-harness';

test('late echo after first result acknowledges and answers mail', async ({page}) => {
  test.setTimeout(90000);
  // Default fixture queues mid-turn input without echo, then pumps it after result.
  const f = await claudeWorkersHarness({turns_by_input:[
    {contains:'initial marker',steps:[{text:'partial marker'},{barrier:'late-window'},{text:'first final'}]},
    {contains:'late marker',steps:[{barrier:'slow-response'},{text:'late answer'}]},
  ]});
  try {
    await f.open(page); const agent = await f.launch(page);
    await f.userTurn(page,'initial marker');
    await expect.poll(async () => (await f.records()).some(r=>r.kind==='barrier' && r.name==='late-window')).toBe(true);
    const barrier=(await f.records()).find(r=>r.kind==='barrier' && r.name==='late-window')!;
    const id=randomUUID(); const {now}=await f.router.advanceClock(1);
    const sent=await f.request(page,'/v1/messages',{id,from:'late-fixture',to:agent,body:'late marker',created_at:new Date(now).toISOString()});
    expect(sent.status).toBe(201);
    await expect.poll(async()=> (await f.inputs()).length,{timeout:10000}).toBe(2);
    expect(f.tableRows('embedded_acks',agent).some(r=>r.message_id===id)).toBe(false);
    await f.releaseBarrier(barrier);
    await expect.poll(async () => (await f.records()).some(r=>r.kind==='barrier' && r.name==='slow-response')).toBe(true);
    const slow = (await f.records()).find(r=>r.kind==='barrier' && r.name==='slow-response')!;
    await expect.poll(() => f.tableRows('embedded_acks',agent).filter(r=>r.message_id===id).length).toBe(1);
    // Cross the real 30-second deadline only after the checked echo.
    await new Promise(resolve => setTimeout(resolve, 31000));
    await f.releaseBarrier(slow);
    await f.final(page,agent,'late answer');
    await expect.poll(() => f.tableRows('embedded_acks',agent).filter(r=>r.message_id===id).length).toBe(1);
    expect(await f.inputs()).toHaveLength(2);
    expect(await f.starts()).toHaveLength(1);
  } finally { await f.close(); }
});

test('sequential late steers do not time out during an earlier response', async ({page}) => {
  test.setTimeout(120000);
  const f = await claudeWorkersHarness({turns_by_input:[
    {contains:'initial sequential',steps:[{barrier:'queue-steers'},{text:'initial answer'}]},
    {contains:'first sequential',steps:[{barrier:'first-response'},{text:'first answer'}]},
    {contains:'second sequential',steps:[{text:'second answer'}]},
  ]});
  try {
    await f.open(page); const agent = await f.launch(page);
    await f.userTurn(page,'initial sequential');
    await expect.poll(async () => (await f.records()).some(r=>r.kind==='barrier' && r.name==='queue-steers')).toBe(true);
    const ids = [randomUUID(), randomUUID()];
    for (let i = 0; i < ids.length; i++) {
      const {now} = await f.router.advanceClock(1);
      const sent = await f.request(page,'/v1/messages',{id:ids[i],from:'sequential-fixture',to:agent,body:i === 0 ? 'first sequential' : 'second sequential',created_at:new Date(now).toISOString()});
      expect(sent.status).toBe(201);
      await expect.poll(async()=> (await f.inputs()).length,{timeout:10000}).toBe(i+2);
    }
    const initial = (await f.records()).find(r=>r.kind==='barrier' && r.name==='queue-steers')!;
    await f.releaseBarrier(initial);
    await expect.poll(async () => (await f.records()).some(r=>r.kind==='barrier' && r.name==='first-response')).toBe(true);
    const slow = (await f.records()).find(r=>r.kind==='barrier' && r.name==='first-response')!;
    await expect.poll(() => f.tableRows('embedded_acks',agent).filter(r=>r.message_id===ids[0]).length).toBe(1);
    expect(f.tableRows('embedded_acks',agent).some(r=>r.message_id===ids[1])).toBe(false);
    await new Promise(resolve => setTimeout(resolve, 31000));
    await f.releaseBarrier(slow);
    await f.final(page,agent,'second answer');
    for (const id of ids) await expect.poll(() => f.tableRows('embedded_acks',agent).filter(r=>r.message_id===id).length).toBe(1);
    expect(await f.inputs()).toHaveLength(3);
    expect(await f.starts()).toHaveLength(1);
  } finally { await f.close(); }
});

test('missing second echo after first steered result retires the process', async ({page}) => {
  test.setTimeout(120000);
  const f = await claudeWorkersHarness({replay_hold_contains:'missing second',turns_by_input:[
    {contains:'initial missing',steps:[{barrier:'queue-missing'},{text:'initial answer'}]},
    {contains:'first missing',steps:[{text:'first answer'}]},
    {contains:'missing second',steps:[{text:'never answered'}]},
  ]});
  try {
    await f.open(page); const agent = await f.launch(page);
    await f.userTurn(page,'initial missing');
    await expect.poll(async () => (await f.records()).some(r=>r.kind==='barrier' && r.name==='queue-missing')).toBe(true);
    const ids = [randomUUID(), randomUUID()];
    for (let i = 0; i < ids.length; i++) {
      const {now} = await f.router.advanceClock(1);
      const sent = await f.request(page,'/v1/messages',{id:ids[i],from:'missing-fixture',to:agent,body:i === 0 ? 'first missing' : 'missing second',created_at:new Date(now).toISOString()});
      expect(sent.status).toBe(201);
      await expect.poll(async()=> (await f.inputs()).length,{timeout:10000}).toBe(i+2);
    }
    await f.releaseBarrier((await f.records()).find(r=>r.kind==='barrier' && r.name==='queue-missing')!);
    await expect.poll(async () => (await f.records()).some(r=>r.kind==='barrier' && r.name==='replay'), {timeout:10000}).toBe(true);
    await expect.poll(() => f.tableRows('embedded_acks',agent).filter(r=>r.message_id===ids[0]).length).toBe(1);
    const pid = Number((await f.starts())[0].pid);
    await expect.poll(() => { try { process.kill(pid, 0); return false; } catch (e: any) { if (e.code === 'ESRCH') return true; throw e; } }, {timeout:45000, intervals:[100,500]}).toBe(true);
    expect(f.tableRows('embedded_acks',agent).some(r=>r.message_id===ids[1])).toBe(false);
    expect(await f.inputs()).toHaveLength(3);
    expect(await f.starts()).toHaveLength(1);
  } finally { await f.close(); }
});
