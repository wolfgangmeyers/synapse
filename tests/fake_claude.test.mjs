import assert from 'node:assert/strict';
import test from 'node:test';
import { spawn } from 'node:child_process';
import { mkdtemp, readFile, writeFile, rm, stat } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { createInterface } from 'node:readline';
import { createServer } from 'node:http';
import { once } from 'node:events';

const binary = fileURLToPath(new URL('./fixtures/fake-claude/claude.mjs', import.meta.url));
const session = '11111111-1111-4111-8111-111111111111';
const flags = ['-p', '--input-format', 'stream-json', '--output-format', 'stream-json', '--verbose',
  '--replay-user-messages', '--model', 'sonnet', '--permission-mode', 'bypassPermissions', '--session-id', session];
const user = (uuid, content = 'same text') => ({ type: 'user', uuid, session_id: session,
  parent_tool_use_id: null, message: { role: 'user', content } });
const control = (request_id, subtype, rest = {}) => ({ type: 'control_request', request_id, request: { subtype, ...rest } });

async function launch(t, config = {}, args = flags, env = {}, spawnOptions = {}) {
  const root = await mkdtemp(join(tmpdir(), 'fake-claude-'));
  const script = join(root, 'script.json');
  const record = join(root, 'record.jsonl');
  await writeFile(script, JSON.stringify(config));
  const child = spawn(binary, args, { cwd: root, env: { PATH: process.env.PATH,
    FAKE_CLAUDE_SCRIPT: script, FAKE_CLAUDE_RECORD: record, ...env }, stdio: ['pipe', 'pipe', 'pipe'], ...spawnOptions });
  const closed = new Promise((resolve, reject) => {
    child.once('error', reject);
    child.once('close', (code, signal) => resolve({ code, signal }));
  });
  closed.catch(() => {});
  let stderr = '';
  child.stderr.setEncoding('utf8').on('data', chunk => { stderr += chunk; });
  const lines = createInterface({ input: child.stdout })[Symbol.asyncIterator]();
  t.after(async () => {
    child.kill('SIGKILL');
    await closed.catch(() => {});
    await rm(root, { recursive: true, force: true });
  });
  return { child, closed, record, root, stderr: () => stderr,
    send: value => child.stdin.write(JSON.stringify(value) + '\n'),
    raw: async () => { const line = await lines.next(); assert.equal(line.done, false); return line.value; },
    next: async () => { const line = await lines.next(); assert.equal(line.done, false); return JSON.parse(line.value); },
    records: async () => (await readFile(record, 'utf8')).trim().split('\n').map(JSON.parse) };
}

test('executable auth status records argv/cwd and selected environment only', { timeout: 5000 }, async t => {
  const f = await launch(t, {}, ['auth', 'status', '--json'], { TMUX: 'sentinel', ANTHROPIC_API_KEY: 'never-record' });
  assert.deepEqual(await f.next(), { loggedIn: true, authMethod: 'claude.ai', subscriptionType: 'max' });
  assert.equal((await f.closed).code, 0);
  const [start] = await f.records();
  assert.equal(start.kind, 'start');
  assert.deepEqual(start.argv, ['auth', 'status', '--json']);
  assert.equal(start.cwd, f.root);
  assert.equal(start.env.TMUX, 'sentinel');
  assert.ok(!JSON.stringify(start).includes('never-record'));
  assert.equal((await stat(f.record)).mode & 0o777, 0o600);
});

test('scripted signed-out and malformed auth', { timeout: 5000 }, async t => {
  for (const auth of [{ loggedIn: false }, { raw: '{bad', exit_code: 2 }]) {
    const f = await launch(t, { auth }, ['auth', 'status', '--json']);
    const raw = await f.raw();
    if (auth.raw) assert.equal(raw, auth.raw);
    else assert.equal(JSON.parse(raw).loggedIn, false);
    assert.equal((await f.closed).code, auth.exit_code ?? 1);
  }
});

test('reuses process, preserves IDs/images, emits assistant/tool/result with usage', { timeout: 5000 }, async t => {
  const f = await launch(t, { turns: [[{ text: 'hello' }, { tool: { id: 'call-1', name: 'Read', input: { file_path: 'x' }, result: 'contents' } }], [{ text: 'second' }]] });
  const input = user('input-1', [{ type: 'text', text: 'seed' }, { type: 'image', source: { type: 'base64', media_type: 'image/png', data: 'AA==' } }]);
  f.send(input);
  const init = await f.next();
  assert.equal(init.subtype, 'init');
  assert.equal(init.session_id, session);
  assert.ok(init.capabilities.includes('interrupt_receipt_v1'));
  assert.deepEqual(await f.next(), { ...input, isReplay: true });
  const answer = await f.next();
  assert.equal(answer.message.content[0].text, 'hello');
  assert.equal(answer.message.model, 'claude-sonnet-5-5');
  assert.ok(answer.message.usage.input_tokens > 0);
  assert.equal((await f.next()).message.content[0].id, 'call-1');
  assert.deepEqual((await f.next()).message.content[0], { type: 'tool_result', tool_use_id: 'call-1', content: 'contents', is_error: false });
  assert.equal((await f.next()).subtype, 'success');
  f.send(user('input-2'));
  await f.next();
  assert.equal((await f.next()).uuid, 'input-2');
  assert.equal((await f.next()).message.content[0].text, 'second');
  assert.equal((await f.next()).type, 'result');
  f.child.stdin.end();
  assert.equal((await f.closed).code, 0);
  const records = await f.records();
  assert.equal(records.filter(r => r.kind === 'start').length, 1);
  assert.deepEqual(records.filter(r => r.kind === 'stdin').map(r => JSON.parse(r.line)), [input, user('input-2')]);
});

test('interrupt queues equal-text IDs until result; model switch correlates', { timeout: 5000 }, async t => {
  const f = await launch(t, { turns: [[{ text: 'partial' }, { wait: 'interrupt' }], [{ text: 'next' }]] });
  f.send(user('first'));
  await f.next(); await f.next();
  assert.equal((await f.next()).message.content[0].text, 'partial');
  f.send(user('second'));
  f.send(control('interrupt-1', 'interrupt'));
  const receipt = await f.next();
  assert.equal(receipt.response.request_id, 'interrupt-1');
  assert.equal(receipt.response.subtype, 'success');
  assert.equal((await f.next()).subtype, 'error_during_execution');
  await f.next();
  assert.equal((await f.next()).uuid, 'second');
  await f.next(); await f.next();
  f.send(control('model-1', 'set_model', { model: 'haiku' }));
  const switched = await f.next();
  assert.equal(switched.response.request_id, 'model-1');
  assert.equal(switched.response.response.model, 'claude-haiku-5-5');
  assert.equal((await f.next()).isReplay, undefined);
  f.send(user('third'));
  assert.equal((await f.next()).model, 'claude-haiku-5-5');
  await f.next(); await f.next(); await f.next();
  f.child.stdin.end();
  assert.equal((await f.closed).code, 0);
});

test('steer_mid_turn: a mid-turn input is absorbed by the active turn at its steer step', { timeout: 5000 }, async t => {
  const f = await launch(t, { steer_mid_turn: true, turns: [[{ text: 'partial' }, { barrier: 'mid' }, { text: 'tail' }, { steer: 'steered answer' }]] });
  f.send(user('first'));
  await f.next(); await f.next();
  assert.equal((await f.next()).message.content[0].text, 'partial');
  f.send(user('second'));
  const echo = await f.next();
  assert.equal(echo.uuid, 'second');
  assert.equal(echo.isReplay, true);
  assert.deepEqual((await f.records()).filter(row => row.kind === 'steer_echo').map(row => row.input_id), ['second']);
  // Nothing new starts while the turn is held: the steered input is not a turn.
  const [barrier] = (await f.records()).filter(row => row.kind === 'barrier');
  await writeFile(join(f.root, `barrier-${barrier.invocation}`), 'release');
  assert.equal((await f.next()).message.content[0].text, 'tail');
  assert.equal((await f.next()).message.content[0].text, 'steered answer');
  assert.equal((await f.next()).subtype, 'success');
  f.child.stdin.end();
  assert.equal((await f.closed).code, 0);
  const rows = await f.records();
  assert.deepEqual(rows.filter(row => row.kind === 'steer_absorbed').map(row => [row.turn_input_id, row.input_id]), [['first', 'second']]);
  assert.equal(rows.filter(row => row.kind === 'start').length, 1);
  assert.equal(rows.filter(row => row.kind === 'stdin').length, 2);
});

test('steer_mid_turn: input not absorbed before the turn ends is answered by a later execution', { timeout: 5000 }, async t => {
  const f = await launch(t, { steer_mid_turn: true, turns: [[{ text: 'partial' }, { barrier: 'mid' }], [{ text: 'next answer' }]] });
  f.send(user('first'));
  await f.next(); await f.next();
  assert.equal((await f.next()).message.content[0].text, 'partial');
  f.send(user('second'));
  assert.equal((await f.next()).uuid, 'second');
  const [barrier] = (await f.records()).filter(row => row.kind === 'barrier');
  await writeFile(join(f.root, `barrier-${barrier.invocation}`), 'release');
  assert.equal((await f.next()).subtype, 'success');
  assert.equal((await f.next()).type, 'system');
  assert.equal((await f.next()).message.content[0].text, 'next answer');
  assert.equal((await f.next()).subtype, 'success');
  f.child.stdin.end();
  assert.equal((await f.closed).code, 0);
  const rows = await f.records();
  assert.equal(rows.filter(row => row.kind === 'steer_absorbed').length, 0);
  assert.equal(rows.filter(row => row.kind === 'start').length, 1);
});

test('model refusal and missing resume have probe-shaped failures', { timeout: 5000 }, async t => {
  const f = await launch(t, { refuse_model: true });
  f.send(control('no-model', 'set_model', { model: 'opus' }));
  assert.equal((await f.next()).response.subtype, 'error');
  f.child.stdin.end();
  assert.equal((await f.closed).code, 0);
  const r = await launch(t, { resume_missing: true }, [...flags.slice(0, -2), '--resume', session]);
  assert.equal((await r.next()).num_turns, 0);
  assert.equal((await r.closed).code, 1);
  assert.match(r.stderr(), /No conversation found with session ID:/);
});

test('scripted background events, usage rejection, malformed output and crash', { timeout: 5000 }, async t => {
  const notification = { type: 'system', subtype: 'task_notification', task_id: 'task-1', summary: 'done' };
  const f = await launch(t, { startup: [{ emit: notification }], turns: [[{ rate_limit: { resetsAt: 2000000000 } }]] });
  assert.deepEqual(await f.next(), notification);
  f.send(user('limit'));
  await f.next(); await f.next();
  assert.equal((await f.next()).rate_limit_info.resetsAt, 2000000000);
  assert.equal((await f.next()).api_error, 'usage_limit_reached');
  assert.equal((await f.next()).is_error, true);
  f.child.stdin.end(); await f.closed;
  const bad = await launch(t, { startup: [{ raw: '{malformed' }, { stderr: 'diagnostic' }, { crash: 7 }] });
  assert.equal(await bad.raw(), '{malformed');
  assert.equal((await bad.closed).code, 7);
  assert.equal(bad.stderr(), 'diagnostic');
});

test('hang can be killed/reaped without children or retained state', { timeout: 5000 }, async t => {
  const f = await launch(t, { turns: [[{ hang: true }]] });
  f.send(user('hang'));
  await f.next(); await f.next();
  f.child.kill('SIGTERM');
  assert.equal((await f.closed).signal, 'SIGTERM');
  assert.deepEqual((await f.records()).map(r => r.kind), ['start', 'stdin']);
});

test('record retention fails closed at byte cap', { timeout: 5000 }, async t => {
  const f = await launch(t, {}, flags, { FAKE_CLAUDE_RECORD_LIMIT: '1024' });
  f.send(user('large', 'x'.repeat(2048)));
  assert.equal((await f.closed).code, 2);
  assert.match(f.stderr(), /record limit/);
  assert.ok((await stat(f.record)).size <= 1024);
});

test('invalid scripts and unsupported CLI invocations fail visibly', { timeout: 5000 }, async t => {
  for (const [config, args] of [[{ turns: [[{ typo: true }]] }, flags], [{}, ['--not-claude']]]) {
    const f = await launch(t, config, args);
    f.send(user('invalid'));
    assert.equal((await f.closed).code, 2);
    assert.match(f.stderr(), /fake-claude:/);
  }
});

test('result includes answer text; no replay flag means no input echo', { timeout: 5000 }, async t => {
  const f = await launch(t, { turns: [[{ thinking: 'considering' }, { text: 'answer' }]], capabilities: [] },
    flags.filter(flag => flag !== '--replay-user-messages'));
  f.send(user('no-replay'));
  assert.deepEqual((await f.next()).capabilities, []);
  assert.equal((await f.next()).message.content[0].type, 'thinking');
  await f.next();
  assert.equal((await f.next()).result, 'answer');
  f.child.stdin.end();
  assert.equal((await f.closed).code, 0);
});

test('pending input count and byte bounds fail separately without echoing queued inputs', { timeout: 10000 }, async t => {
  for (const byCount of [true, false]) {
    const f = await launch(t, { turns: [[{ wait: 'interrupt' }]] }, flags,
      { FAKE_CLAUDE_RECORD_LIMIT: String(8 * 1024 * 1024) });
    f.send(user('active'));
    await f.next(); await f.next();
    if (byCount) {
      for (let i = 0; i < 65; i++) f.send(user(`pending-${i}`));
    } else f.send(user('too-large', 'x'.repeat(6 * 1024 * 1024)));
    assert.equal((await f.closed).code, 2);
    assert.match(f.stderr(), /pending input limit/);
  }
});

test('stdin line bound and truncated records fail visibly', { timeout: 10000 }, async t => {
  for (const line of ['x'.repeat(16 * 1024 * 1024 + 1), '{"type":"user"}']) {
    const f = await launch(t);
    // A benign control response proves startup finished before large writes.
    f.send(control('ready', 'interrupt'));
    await f.next();
    f.child.stdin.on('error', () => {}); // Expected EPIPE if the bound trips mid-write.
    f.child.stdin.end(line);
    assert.equal((await f.closed).code, 2);
    assert.match(f.stderr(), /stdin (line limit|requires newline)/);
  }
});

test('script supports oversized stdout lines for transport scanner tests', { timeout: 10000 }, async t => {
  const f = await launch(t, { startup: [{ raw: { text: 'x', repeat: 16 * 1024 * 1024 + 1 } }, { crash: 9 }] });
  assert.equal((await f.raw()).length, 16 * 1024 * 1024 + 1);
  assert.equal((await f.closed).code, 9);
});

async function mcpServer(t, reply) {
  const server = createServer(reply);
  server.listen(0, '127.0.0.1');
  await once(server, 'listening');
  t.after(() => new Promise(resolve => { server.close(resolve); server.closeAllConnections(); }));
  return `http://127.0.0.1:${server.address().port}/mcp`;
}
async function mcpFixture(t, reply, script = { turns: [[{ mcp_call: { id: 'tool-17', name: 'scratchpad', input: { text: 'hello' } } }]] }) {
  const url = await mcpServer(t, reply);
  const f = await launch(t, script, [...flags, '--mcp-config', 'mcp.json']);
  await writeFile(join(f.root, 'mcp.json'), JSON.stringify({ mcpServers: { mecha: {
    type: 'http', url, headers: { Authorization: 'Bearer local-test-token', 'X-Fixture': 'pass-through' }
  } } }));
  return f;
}

test('MCP producer precedes real HTTP, config headers and IDs pass through, exact result preserved', { timeout: 8000 }, async t => {
  const methods = [];
  let release;
  const gate = new Promise(resolve => { release = resolve; });
  let incoming;
  const received = new Promise(resolve => { incoming = resolve; });
  const actual = { content: [{ type: 'text', text: 'committed' }, { type: 'image', data: 'AA==', mimeType: 'image/png' }], isError: true };
  const f = await mcpFixture(t, async (req, res) => {
    let body = ''; for await (const chunk of req) body += chunk;
    const call = JSON.parse(body);
    methods.push(call.method);
    if (call.method !== 'tools/call') {
      if (call.method === 'notifications/initialized') { res.statusCode = 202; res.end(); return; }
      res.end(JSON.stringify({jsonrpc:'2.0', id:call.id, result: call.method === 'initialize' ? {protocolVersion:'2025-03-26'} : call.method === 'tools/list' ? {tools:[]} : {}})); return;
    }
    incoming({ call, headers: req.headers });
    await gate; // Response waits until test observes the real producer.
    res.setHeader('Content-Type', 'application/json');
    res.end(JSON.stringify({ jsonrpc: '2.0', id: call.id, result: actual }));
  });
  f.send(user('mcp-user'));
  await f.next(); await f.next();
  const producer = (await f.next()).message.content[0];
  assert.deepEqual(producer, { type: 'tool_use', id: 'tool-17', name: 'mcp__mecha__scratchpad', input: { text: 'hello' } });
  const { call, headers } = await received;
  assert.equal(headers.authorization, 'Bearer local-test-token');
  assert.equal(headers['x-fixture'], 'pass-through');
  assert.deepEqual(methods, ['initialize', 'notifications/initialized', 'tools/list', 'ping', 'tools/call']);
  assert.equal(call.method, 'tools/call');
  assert.equal(call.jsonrpc, '2.0');
  assert.deepEqual(call.params, { name: 'scratchpad', arguments: { text: 'hello' }, _meta: { 'claudecode/toolUseId': producer.id } });
  release();
  assert.deepEqual((await f.next()).message.content[0], { type: 'tool_result', tool_use_id: producer.id, content: actual.content, is_error: true });
  assert.equal((await f.next()).subtype, 'success');
  f.child.stdin.end(); assert.equal((await f.closed).code, 0);
  assert.ok(!(await readFile(f.record, 'utf8')).includes('local-test-token'));
});

test('MCP protocol, HTTP, and malformed response failures never emit successful tool results', { timeout: 10000 }, async t => {
  for (const mode of ['rpc-error', 'wrong-id', 'http-error', 'malformed', 'oversize']) {
    const f = await mcpFixture(t, async (req, res) => {
      let body = ''; for await (const chunk of req) body += chunk;
      const { id } = JSON.parse(body);
      res.statusCode = mode === 'http-error' ? 403 : 200;
      res.end(mode === 'malformed' ? '{bad' : mode === 'oversize' ? 'x'.repeat(1024 * 1024 + 1) : JSON.stringify({ jsonrpc: '2.0', id: mode === 'wrong-id' ? 'other' : id,
        ...(mode === 'rpc-error' ? { error: { code: -32000, message: 'denied' } } : { result: { content: [] } }) }));
    });
    f.send(user(mode)); await f.next(); await f.next(); await f.next();
    assert.equal((await f.closed).code, 2);
    assert.match(f.stderr(), /fake-claude: MCP/);
  }
});

test('MCP validates bounded standard config and refuses non-loopback URLs', { timeout: 8000 }, async t => {
  for (const content of ['x'.repeat(1024 * 1024 + 1), '{}', JSON.stringify({ mcpServers: { mecha: { type: 'http', url: 'http://192.0.2.1/mcp' } } })]) {
    const f = await launch(t, { turns: [[{ mcp_call: { id: 'bad', name: 'goal' } }]] }, [...flags, '--mcp-config', 'mcp.json']);
    await writeFile(join(f.root, 'mcp.json'), content);
    f.send(user('invalid-config'));
    assert.equal((await f.closed).code, 2);
    assert.match(f.stderr(), /MCP/);
  }
});

test('spawned child has recorded PID, survives a turn, and process-group termination reaps it', { timeout: 8000, skip: process.platform === 'win32' }, async t => {
  const f = await launch(t, { turns: [[{ spawn_child: {} }], []] }, flags, {}, { detached: true });
  let pid;
  t.after(() => { if (pid) { try { process.kill(pid, 'SIGKILL'); } catch {} } });
  f.send(user('child'));
  await f.next(); await f.next();
  const event = await f.next();
  assert.equal(event.subtype, 'fixture_child');
  pid = event.pid;
  assert.ok(Number.isInteger(pid) && pid > 1);
  process.kill(pid, 0);
  assert.equal((await f.next()).type, 'result');
  f.send(user('next')); await f.next(); await f.next(); await f.next();
  process.kill(pid, 0);
  const records = await f.records();
  assert.equal(records.find(r => r.kind === 'start').pid, f.child.pid);
  const childRecord = records.find(r => r.kind === 'child');
  assert.equal(childRecord.pid, pid);
  assert.equal(childRecord.parent_pid, f.child.pid);
  assert.notEqual(childRecord.pid, childRecord.parent_pid);
  process.kill(-f.child.pid, 'SIGTERM');
  await f.closed;
  assert.throws(() => process.kill(pid, 0), { code: 'ESRCH' });
});

test('interrupt aborts actual MCP request without late results or replaying the effect', { timeout: 8000 }, async t => {
  let notify;
  const received = new Promise(resolve => { notify = resolve; });
  let calls = 0;
  const f = await mcpFixture(t, (req, res) => {
    calls++;
    req.resume();
    res.on('close', () => notify());
    req.on('end', () => notify());
    // Intentionally no response: interrupt must cancel the socket.
  }, { turns: [[{ mcp_call: { id: 'interrupt-call', name: 'goal', input: {} } }], [{ text: 'after interrupt' }]] });
  f.send(user('interrupt-mcp')); await f.next(); await f.next(); await f.next();
  await received;
  f.send(control('cancel-http', 'interrupt'));
  assert.equal((await f.next()).response.request_id, 'cancel-http');
  assert.equal((await f.next()).subtype, 'error_during_execution');
  f.send(user('after'));
  assert.equal((await f.next()).subtype, 'init');
  assert.equal((await f.next()).uuid, 'after');
  assert.equal((await f.next()).message.content[0].text, 'after interrupt');
  assert.equal((await f.next()).subtype, 'success');
  f.child.stdin.end(); assert.equal((await f.closed).code, 0);
  assert.equal(calls, 1);
});

test('normal EOF kills and reaps the fixed child', { timeout: 8000 }, async t => {
  const f = await launch(t, { turns: [[{ spawn_child: {} }]] });
  f.send(user('child-eof')); await f.next(); await f.next();
  const { pid } = await f.next();
  t.after(() => { try { process.kill(pid, 'SIGKILL'); } catch {} });
  await f.next();
  f.child.stdin.end();
  assert.equal((await f.closed).code, 0);
  assert.throws(() => process.kill(pid, 0), { code: 'ESRCH' });
});

test('input-selected recovery foreground answer is not consumed by startup notice', { timeout: 5000 }, async t => {
  const f = await launch(t, {
    turns: [[{ text: 'notice only' }]],
    turns_by_input: [{ contains: 'unique foreground', steps: [{ text: 'foreground only' }] }],
  });
  for (const [uuid, input, expected] of [['notice', 'recovery notice', 'notice only'], ['foreground', 'unique foreground', 'foreground only']]) {
    f.send(user(uuid, input));
    let event;
    do { event = await f.next(); } while (event.type !== 'result');
    assert.equal(event.result, expected);
  }
});

test('execution input selects a process-local script; auth does not select it', { timeout: 5000 }, async t => {
  const config = { executions_by_input: [
    { contains: 'first execution', turns: [[{ text: 'first' }], [{ text: 'first followup' }]] },
    { contains: 'second execution', turns: [[{ text: 'second' }]] }
  ] };
  const auth = await launch(t, config, ['auth', 'status', '--json']);
  assert.equal((await auth.next()).loggedIn, true);
  assert.equal((await auth.closed).code, 0);
  for (const [input, expected] of [['first execution', ['first', 'first followup']], ['second execution', ['second']]]) {
    const f = await launch(t, config);
    for (const answer of expected) {
      f.send(user(answer, input));
      assert.equal((await f.next()).subtype, 'init');
      await f.next();
      assert.equal((await f.next()).message.content[0].text, answer);
      assert.equal((await f.next()).type, 'result');
    }
    f.child.stdin.end();
    assert.equal((await f.closed).code, 0);
  }
});

test('execution selection stays fixed across inputs and is fresh on resume', { timeout: 5000 }, async t => {
  const config = { executions_by_input: [
    { contains: 'alpha', turns: [[{ text: 'alpha first' }], [{ text: 'alpha second' }], [{ text: 'alpha third' }]] },
    { contains: 'beta', turns: [[{ text: 'beta first' }]] },
  ] };
  const runTurn = async (f, id, input, expected) => {
    f.send(user(id, input));
    assert.equal((await f.next()).subtype, 'init');
    assert.equal((await f.next()).uuid, id);
    assert.equal((await f.next()).message.content[0].text, expected);
    assert.equal((await f.next()).result, expected);
  };
  const first = await launch(t, config);
  await runTurn(first, 'first', 'alpha', 'alpha first');
  await runTurn(first, 'different-match', 'beta', 'alpha second');
  await runTurn(first, 'no-match', 'neither', 'alpha third');
  first.child.stdin.end();
  assert.equal((await first.closed).code, 0);

  const resumed = await launch(t, config, [...flags.slice(0, -2), '--resume', session]);
  await runTurn(resumed, 'resumed', 'beta', 'beta first');
  resumed.child.stdin.end();
  assert.equal((await resumed.closed).code, 0);
});

test('execution selection fails closed for no match, ambiguous match, and conflicting config', { timeout: 5000 }, async t => {
  const rules = [{ contains: 'one', turns: [] }, { contains: 'two', turns: [] }];
  for (const input of ['neither', 'one and two']) {
    const f = await launch(t, { executions_by_input: rules });
    f.send(user('input', input));
    assert.equal((await f.closed).code, 2);
    assert.match(f.stderr(), /exactly one rule/);
  }
  const f = await launch(t, { executions_by_input: rules, turns: [] });
  assert.equal((await f.closed).code, 2);
  assert.match(f.stderr(), /invalid executions_by_input/);
});
