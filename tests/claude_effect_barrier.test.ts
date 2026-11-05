import { test } from 'node:test';
import assert from 'node:assert/strict';
import { effectBarrier, type EffectReceipt } from './e2e/claude-code-effect-recovery-harness.ts';
const scope: EffectReceipt = { phase:'intent', agent_id:'test', session_id:'s', epoch:1, generation:'g', owner_token:1, effect_id:'canonical' };
function send(b: Awaited<ReturnType<typeof effectBarrier>>, body: object | string = scope, signal?: AbortSignal) {
  return fetch(b.endpoint+'/barrier',{method:'POST',headers:{Authorization:`Bearer ${b.token}`},body:typeof body==='string'?body:JSON.stringify(body),signal});
}
test('arrival deadline rejects; close rejects outstanding arrival', { timeout:2000 }, async()=>{
 const b=await effectBarrier({arrivalMS:30});
 try { b.arm(scope); await assert.rejects(b.arrived,/arrival deadline/); } finally {await b.close();}
 const c=await effectBarrier(); c.arm(scope); const rejected=assert.rejects(c.arrived,/closed/); await c.close(); await rejected;
});
test('body timeout cleared during hold; hold expiry is separate', {timeout:2000}, async()=>{
 const b=await effectBarrier({bodyMS:20,holdMS:100}); let response;
 try {b.arm(scope); response=send(b).then(r=>r.status, e=>e); await b.arrived;
 assert.equal(await response,504); assert.equal(await b.finished,'timeout'); assert.throws(()=>b.release(),/no held/);
 } finally {await b.close(); await response;}
});
test('disconnect clears held state and cannot release dead connection', {timeout:2000}, async()=>{
 const b=await effectBarrier(); const controller=new AbortController(); let response;
 try {b.arm(scope); response=send(b,scope,controller.signal).then(r=>r.status,e=>e); await b.arrived; controller.abort();
 await response; assert.equal(await b.finished,'disconnect'); assert.throws(()=>b.release(),/no held/);
 } finally {controller.abort();await b.close();await response;}
});
test('oversize rejects arrival; wrong identity leaves arm available', {timeout:2000}, async()=>{
 const b=await effectBarrier(); let response;
 try {b.arm(scope); assert.equal((await send(b,{...scope,effect_id:'wrong'})).status,409);
 const rejected=assert.rejects(b.arrived,/oversize/); response=send(b,'x'.repeat(4097)).then(r=>r.status,e=>e);
 await rejected; await response;
 } finally {await b.close();await response;}
});
