import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createTmuxWindowMonitor, createClaudeSidecar, waitForWindowIdentity, createTmuxInjector } from '../tools/tmux-worker/claude_sidecar.mjs';

test('monitor retries unavailable inventory and stops only for confirmed absence', async () => {
  let tick, missing = 0, clears = 0, state = 'error';
  const monitor = createTmuxWindowMonitor({window:'worker', tmuxRest:{list:async()=>{
    if (state === 'error') throw new Error('offline');
    if (state === 'bad') return {};
    return {session:'mecha-private',windows:state === 'present' ? [{name:'worker'}] : []};
  }}, setIntervalImpl:fn=>(tick=fn, 1), clearIntervalImpl:()=>clears++, onMissing:()=>missing++});
  for (const next of ['error','bad','present']) { state=next; await tick(); assert.equal(missing,0); }
  state='absent'; await tick(); await tick();
  assert.equal(missing,1); assert.equal(clears,1); monitor.close();
});

test('failed injection does not acknowledge the delivery', async () => {
  const receipts=[];
  const sidecar=createClaudeSidecar({agentId:'worker',client:{sendReceipt:(kind)=>receipts.push(kind)},inject:async()=>{throw new Error('offline')}});
  await assert.rejects(sidecar.onEnvelope({id:'id',from:'sender',to:'worker',body:'test'}, {attemptNumber:1}));
  assert.deepEqual(receipts,['injection_requested']);
});

test('replacement window is missing to the original sidecar', async () => {
  let tick, missing = 0;
  createTmuxWindowMonitor({window:'worker', windowId:'@1', tmuxRest:{list:async()=>({windows:[{name:'worker',window_id:'@2'}]})},setIntervalImpl:fn=>(tick=fn,1),clearIntervalImpl:()=>{},onMissing:()=>missing++});
  await tick(); assert.equal(missing,1);
});


test('startup retries transient inventory then binds identity', async () => {
 let calls=0, sleeps=0; const server='0123456789abcdef0123456789abcdef';
 const identity=await waitForWindowIdentity({window:'worker',tmuxRest:{list:async()=>{calls++; if(calls===1) throw Error('offline'); if(calls===2) return {}; return {windows:[{name:'worker',window_id:'@0',server_id:server}]};}},sleep:async()=>{sleeps++;}});
 assert.deepEqual(identity,{windowId:'@0',serverId:server}); assert.equal(sleeps,2);
 await assert.rejects(waitForWindowIdentity({window:'worker',tmuxRest:{list:async()=>({windows:[]})}}),/tmux_window_missing/);
});

test('same window ID on a new server is not accepted', async () => {
 const a='aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', b='bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'; let tick, missing=0;
 createTmuxWindowMonitor({window:'worker',windowId:'@0',serverId:a,tmuxRest:{list:async()=>({windows:[{name:'worker',window_id:'@0',server_id:b}]})},setIntervalImpl:fn=>(tick=fn,1),clearIntervalImpl:()=>{},onMissing:()=>missing++});
 await tick(); assert.equal(missing,1);
 let sent=0; const inject=createTmuxInjector({windowId:'@0',serverId:a,tmuxRest:{list:async()=>({windows:[{name:'worker',window_id:'@0',server_id:b}]}),keys:async()=>sent++},enterDelayMs:0});
 await assert.rejects(inject({window:'worker',text:'secret'})); assert.equal(sent,0);
});
