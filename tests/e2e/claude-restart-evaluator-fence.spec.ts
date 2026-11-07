import { test, expect } from '@playwright/test';
import { claudeWorkersHarness, type ClaudeWorkersHarness } from './claude-workers-harness';

test.describe('isolated restart candidate', () => {
  test.setTimeout(90000);
  let f: ClaudeWorkersHarness | undefined;
  test.afterEach(async () => { await f?.close(); f = undefined; });
  test('restart cancels held evaluator and fences stale verdict', async ({page}) => {
    f = await claudeWorkersHarness({turns_by_input:[{contains:'REASON: Take the next actionable step',steps:[{text:'Initial work'}]},{contains:'Post restart probe',steps:[{text:'Post restart answer'}]}]}, {defaultModel:null});
    await f.configureEvaluator({replies:[{barrier:'held',output:'VERDICT: NOT_DONE\nREASON: stale'}]});
    await f.open(page);
    expect((await f.request(page,'/v1/settings/default-model',{model:{provider:'claude-code',id:'haiku'}},'PUT')).status).toBe(200);
    const agent=await f.launch(page);
    await f.api(`/v1/managed-agents/${agent}/goal`,{action:'set',goal:'Finish work'},'PUT');
    await expect.poll(async () => (await f!.records()).some(r=>r.kind==='evaluator'),{timeout:15000}).toBe(true);
    const pid=Number((await f.records()).find(r=>r.kind==='evaluator')?.pid);
    expect(pid).toBeGreaterThan(1);
    const beforeStarts = (await f.starts()).length;
    expect((await f.request(page,`/v1/managed-agents/${agent}/restart`,{})).status).toBe(202);
    await expect.poll(()=>{try {process.kill(pid,0);return true;} catch {return false;}},{timeout:15000}).toBe(false);
    await f.releaseEvaluator('held');
    await expect.poll(()=>f!.tableRows('managed_agents',agent)[0].lifecycle_state,{timeout:15000}).toBe('running');
    await expect.poll(async () => {
      const response = await f!.request(page, `/v1/managed-agents/${agent}/prompt`, {message:'Post restart probe'});
      if (response.status !== 200 && response.status !== 409) throw Error(`probe status ${response.status}`);
      return response.status;
    }, {timeout:15000}).toBe(200);
    await f.final(page,agent,'Post restart answer');
    await expect.poll(async () => (await f!.starts()).length, {timeout:15000}).toBeGreaterThan(beforeStarts);
    await expect.poll(() => f!.rows("SELECT execution_json FROM embedded_sessions WHERE agent_id=? AND current=1", agent)[0]?.execution_json, {timeout:15000}).toBeNull();
    await expect.poll(() => f!.tableRows('embedded_goal_evaluations',agent).map(r=>r.state), {timeout:15000}).toEqual(['UNKNOWN','DONE']);
    await expect.poll(() => f!.rows("SELECT execution_json FROM embedded_sessions WHERE agent_id=? AND current=1", agent)[0]?.execution_json, {timeout:15000}).toBeNull();
    await f.advance(60000);
    await f.idle(agent);
    expect(f.tableRows('managed_complete_history',agent).filter(row => row.level === 'error' && (row.category === 'managed_goal_evaluation' || row.category === 'managed_goal'))).toEqual([]);
    expect(f.wsFrames.some(frame => /goal evaluation unavailable/i.test(frame))).toBe(false);
    await expect(page.getByText(/goal evaluation unavailable/i)).toHaveCount(0);
    expect(f.tableRows('embedded_goal_evaluations',agent).map(r=>r.state)).toEqual(['UNKNOWN','DONE']);
    expect(f.tableRows('embedded_goal_continuations',agent)).toEqual([]);
    expect((await f.records()).filter(r=>r.kind==='evaluator_barrier_released' && r.pid===pid)).toEqual([]);
    expect(f.wsFrames.some(frame=>frame.includes('Goal Evaluation • NOT_DONE'))).toBe(false);
    expect(f.violations).toEqual([]);
  });
});
