import { test, expect } from '@playwright/test';
import { spawn, spawnSync, type ChildProcess } from 'node:child_process';
import { mkdtempSync, readFileSync, rmSync, existsSync, writeFileSync, chmodSync, mkdirSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { createServer } from 'node:net';
import { createServer as createHttpServer, request as httpRequest } from 'node:http';
import { connect as netConnect } from 'node:net';

// Real router binary, actual sidecar CLI and client, private tmux server, SQLite.
// The pane is a deterministic raw-input stand-in for Claude, not a mock sidecar.
test('router-selected tmux session delivers and sidecar exits when window disappears', async () => {
  test.setTimeout(120000);
  const root = mkdtempSync(join(tmpdir(), 'mw-tmux-'));
  const socket = join(root, 'sock');
  const bytes = join(root, 'pane');
  const workDir = join(root, "work dir ' quoted");
  mkdirSync(workDir);
  const binary = join(root, 'router');
  const tmux = (...args: string[]) => spawnSync('tmux', ['-S', socket, ...args], {encoding:'utf8', stdio:'ignore', timeout:5000, env:{...process.env, HOME:root, PATH:`${root}:${process.env.PATH}`}});
  const children: ChildProcess[] = [];
  let observerCleanup: (() => void) | undefined;
  try {
    const build = spawnSync('go', ['build', '-o', binary, './cmd/mailbox-router'], {cwd:resolve('router-proxy'), encoding:'utf8', timeout:60000});
    expect(build.status, build.stderr).toBe(0);
    // The real launcher starts a deterministic Claude stand-in inside its window.
    const program = `#!/usr/bin/env python3\nimport os,tty\nopen(${JSON.stringify(join(root, "cwd"))},"w").write(os.getcwd())\ntty.setraw(0)\nf=open(${JSON.stringify(bytes)},"ab",buffering=0)\nwhile True:\n b=os.read(0,4096)\n if not b: break\n f.write(b)\n`;
    writeFileSync(join(root,'claude'), program); chmodSync(join(root,'claude'), 0o700);
    const setup = tmux('new-session','-d','-s','mecha-private','-n','anchor');
    expect(setup.status, setup.stderr).toBe(0);
    expect(tmux('set-option','-t','mecha-private','default-command',`env PATH=${root}:${process.env.PATH} bash --noprofile --norc`).status).toBe(0);
    const port = await new Promise<number>((resolvePort, reject) => { const server = createServer(); server.once('error', reject); server.listen(0,'127.0.0.1',()=>{const address=server.address(); if (!address || typeof address === 'string') return reject(new Error('address')); const p=address.port; server.close(()=>resolvePort(p));}); });
    const base = `http://127.0.0.1:${port}`;
    const router = spawn(binary, ['--managed=false','--host=127.0.0.1',`--port=${port}`,`--database=${root}/router.db`,`--log=${root}/router.log`], {env:{...process.env, HOME:root, TMUX:`${socket},0,0`}, stdio:'ignore'}); children.push(router);
    await expect.poll(async()=>{try { return (await fetch(`${base}/v1/health`)).status; } catch {return 0;}}, {timeout:15000}).toBe(200);
    const inventory = await (await fetch(`${base}/v1/tmux/windows`)).json();
    expect(inventory.session).toBe('mecha-private');
    const launch = spawnSync('python3', [resolve('.claude/skills/supervisor/tools/worker_launch.py'),'--agent','claude','--window','worker','--dir',workDir], {encoding:'utf8', timeout:15000, env:{...process.env, HOME:root, MECHA_ROUTER_URL:base}});
    expect(launch.status, launch.stderr + launch.stdout).toBe(0);
    await expect.poll(async()=>{const result = await (await fetch(`${base}/v1/agents`)).json(); return result.agents?.some((a: {agent_id:string})=>a.agent_id==='worker');}, {timeout:10000}).toBe(true);
    await new Promise(r=>setTimeout(r,5500));
    const connected = await (await fetch(`${base}/v1/agents`)).json();
    expect(connected.agents.some((a: {agent_id:string})=>a.agent_id==='worker')).toBe(true);
    expect(readFileSync(join(root,'cwd'),'utf8')).toBe(workDir);
    const response = await fetch(`${base}/v1/messages`, {method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({id:'e4000000-1111-4111-8111-111111111111',from:'sender',to:'worker',body:'private mailbox sentinel',created_at:'2030-01-01T00:00:00.000Z'})});
    expect(response.status).toBe(201);
    await expect.poll(()=>existsSync(bytes) ? readFileSync(bytes,'utf8') : '',{timeout:15000}).toContain('private mailbox sentinel');
    await expect.poll(()=>existsSync(join(root,'.mecha-developer','claude-sidecars','worker.log')) ? readFileSync(join(root,'.mecha-developer','claude-sidecars','worker.log'),'utf8') : '', {timeout:15000}).toContain('delivery_acked');
    // Successful ACK removes the delivered message from the durable active queue.
    await expect.poll(() => {
      const query = spawnSync('python3', ['-c', 'import sqlite3,sys; print(sqlite3.connect(sys.argv[1]).execute("select count(*) from messages where id=?",(sys.argv[2],)).fetchone()[0])', join(root,'router.db'), 'e4000000-1111-4111-8111-111111111111'], {encoding:'utf8',timeout:3000});
      return query.status === 0 ? query.stdout.trim() : 'error';
    }, {timeout:10000}).toBe('0');
    expect(readFileSync(join(root,'.mecha-developer','claude-sidecars','worker.log'),'utf8')).not.toContain('private mailbox sentinel');
    // Restart the actual sidecar independently of the pane's launcher shell.
    // Otherwise killing the window also kills the sidecar through the launcher's
    // EXIT trap and a missing Enter would not exercise the router fence.
    const processList = spawnSync('ps', ['-eo', 'pid=,args='], {encoding:'utf8', timeout:5000});
    expect(processList.status).toBe(0);
    const sidecarProcesses = processList.stdout.split('\n').filter(line => line.includes('tools/tmux-worker/claude_sidecar.mjs') && line.includes(base));
    expect(sidecarProcesses).toHaveLength(1);
    const originalPID = Number(sidecarProcesses[0].trim().split(/\s+/)[0]);
    process.kill(originalPID, 'SIGTERM');
    await expect.poll(async()=>{const result = await (await fetch(`${base}/v1/agents`)).json(); return result.agents?.some((a: {agent_id:string})=>a.agent_id==='worker');},{timeout:10000}).toBe(false);
    // Transparent test-local observer: all HTTP and WebSocket traffic reaches
    // the real router. Hold only inventory responses during replacement so the
    // real monitor cannot terminate the in-flight injector before its Enter.
    let holdInventory = false;
    const pendingInventory: Array<() => void> = [];
    const keyRequests: Array<{keys:string;window_id?:string;server_id?:string;status:number}> = [];
    const observer = createHttpServer((req,res)=>{
      const chunks: Buffer[] = [];
      req.on('data',(chunk:Buffer)=>chunks.push(chunk));
      req.on('end',()=>{
        const body=Buffer.concat(chunks);
        const upstream=httpRequest(`${base}${req.url}`,{method:req.method,headers:req.headers},up=>{
          const parts:Buffer[]=[];
          up.on('data',(chunk:Buffer)=>parts.push(chunk));
          up.on('end',()=>{
            if (req.method==='POST' && req.url==='/v1/tmux/windows/worker/keys') {
              const payload=JSON.parse(body.toString());
              if (payload.keys?.includes('in flight sentinel')) holdInventory = true;
              keyRequests.push({...payload,status:up.statusCode ?? 0});
            }
            const forward=()=>{if (!res.destroyed) {res.writeHead(up.statusCode ?? 502,up.headers);res.end(Buffer.concat(parts));}};
            if (holdInventory && req.method==='GET' && req.url==='/v1/tmux/windows') pendingInventory.push(forward); else forward();
          });
        });
        upstream.on('error',()=>{if (!res.destroyed) {res.statusCode=502;res.end();}});
        upstream.end(body);
      });
    });
    observer.on('upgrade',(req,client,head)=>{
      const upstream=netConnect(port,'127.0.0.1',()=>{
        const headers=req.rawHeaders.reduce((out:string[],value,index)=>{if(index%2===0)out.push(`${value}: ${req.rawHeaders[index+1]}`);return out;},[]);
        upstream.write(`${req.method} ${req.url} HTTP/${req.httpVersion}\r\n${headers.join('\r\n')}\r\n\r\n`);
        if(head.length)upstream.write(head);
        client.pipe(upstream).pipe(client);
      });
      upstream.on('error',()=>client.destroy()); client.on('error',()=>upstream.destroy());
    });
    observerCleanup=()=>{observer.closeAllConnections(); observer.close();};
    await new Promise<void>(r=>observer.listen(0,'127.0.0.1',r));
    const observerAddress=observer.address();
    if (!observerAddress || typeof observerAddress==='string') throw new Error('observer address');
    const observedBase=`http://127.0.0.1:${observerAddress.port}`;
    const independentSidecar = spawn('node',[resolve('tools/tmux-worker/claude_sidecar.mjs'),'--agent-id','worker','--window','worker','--router-url',observedBase], {env:{...process.env,HOME:root},stdio:'ignore'});

    children.push(independentSidecar);
    await expect.poll(async()=>{const result = await (await fetch(`${base}/v1/agents`)).json(); return result.agents?.some((a: {agent_id:string})=>a.agent_id==='worker');},{timeout:10000}).toBe(true);

    // Interrupt a real sidecar delivery after its body arrives but before the
    // five-second Enter. Replacement must receive no bytes or Enter, and no ACK.
    const before = await (await fetch(`${base}/v1/tmux/windows`)).json();
    const old = before.windows.find((w: {name:string})=>w.name==='worker');
    expect(old.window_id).toMatch(/^@\d+$/);
    expect(old.server_id).toMatch(/^[0-9a-f]{32}$/);
    const secondID = 'e4000000-1111-4111-8111-222222222222';
    const second = await fetch(`${base}/v1/messages`, {method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({id:secondID,from:'sender',to:'worker',body:'in flight sentinel',created_at:'2030-01-01T00:00:00.000Z'})});
    expect(second.status).toBe(201);
    await expect.poll(()=>readFileSync(bytes,'utf8'),{timeout:10000}).toContain('in flight sentinel');
    // Configure the replacement's initial pane command before it exists. This
    // leaves no shell/respawn gap in which input could be lost to observation.
    const replacementBytes = join(root, 'replacement-bytes');
    const replacementReady = join(root, 'replacement-ready');
    const rawProgram = `import os,tty\ntty.setraw(0)\nf=open(${JSON.stringify(replacementBytes)},"ab",buffering=0)\nopen(${JSON.stringify(replacementReady)},"w").close()\nwhile True:\n b=os.read(0,4096)\n if not b: break\n f.write(b)\n`;
    const rawScript = join(root,'replacement.py');
    writeFileSync(rawScript,rawProgram);
    expect(tmux('set-option','-t','mecha-private','default-command',`python3 -u ${rawScript}`).status).toBe(0);
    expect((await fetch(`${base}/v1/tmux/windows/worker`, {method:'DELETE'})).status).toBe(204);
    const replacement = await fetch(`${base}/v1/tmux/windows`, {method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({name:'worker'})});
    expect(replacement.status).toBe(201);
    await expect.poll(()=>existsSync(replacementReady),{timeout:5000}).toBe(true);
    const stale = await fetch(`${base}/v1/tmux/windows/worker/keys`, {method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({keys:'must-not-arrive',window_id:old.window_id,server_id:old.server_id})});
    expect(stale.status).toBe(409);
    // Wait for the actual delayed CR request and the real router rejection.
    await expect.poll(()=>keyRequests.some(k=>k.keys==='\r' && k.window_id===old.window_id && k.server_id===old.server_id && k.status===409),{timeout:12000}).toBe(true);
    holdInventory = false;
    pendingInventory.splice(0).forEach(release=>release());
    const received = readFileSync(replacementBytes);
    expect(received.includes(0x0d)).toBe(false);
    expect(received).toHaveLength(0);
    // Positive control: the recorder actually sees a CR via the real router.
    const control = await fetch(`${base}/v1/tmux/windows/worker/keys`, {method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({keys:'control\r'})});
    expect(control.status).toBe(200);
    await expect.poll(()=>readFileSync(replacementBytes).toString('hex'),{timeout:5000}).toBe(Buffer.from('control\r').toString('hex'));
    // Disconnect releases the failed delivery. Require queued, not merely
    // "not acknowledged": dead_letter is not retryable.
    await expect.poll(async()=>{const result = await (await fetch(`${base}/v1/agents`)).json(); return result.agents?.some((a: {agent_id:string})=>a.agent_id==='worker');},{timeout:10000}).toBe(false);
    await expect.poll(()=>{
      const db = spawnSync('python3',['-c','import json,sqlite3,sys; c=sqlite3.connect(sys.argv[1]); print(json.dumps({"message":c.execute("select state from messages where id=?",(sys.argv[2],)).fetchone(),"attempts":c.execute("select attempt_number,state,failure_reason from delivery_attempts where message_id=? order by attempt_number",(sys.argv[2],)).fetchall()}))',join(root,'router.db'),secondID],{encoding:'utf8',timeout:3000});
      if (db.status !== 0) return null;
      const state=JSON.parse(db.stdout);
      const retryable = state.message?.[0] === 'queued' && state.attempts.length > 0 && state.attempts.length < 3 && state.attempts.every((a: [number,string,string]) => (a[1] === 'nacked' && a[2] === 'injection_failed') || (a[1] === 'timed_out' && a[2] === 'delivery_timeout'));
      return retryable ? 'queued-with-failed-attempts' : JSON.stringify(state);
    },{timeout:45000, intervals:[250,500,1000]}).toBe('queued-with-failed-attempts');
    observer.closeAllConnections();
    await new Promise<void>(r=>observer.close(()=>r()));
  } finally {
    observerCleanup?.();
    for (const child of children) if (child.exitCode === null) child.kill('SIGTERM');
    tmux('kill-server');
    await new Promise(r=>setTimeout(r,100));
    rmSync(root,{recursive:true,force:true});
  }
});
