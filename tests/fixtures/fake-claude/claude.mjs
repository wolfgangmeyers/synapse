#!/usr/bin/env node
// Local protocol fixture: loopback-only HTTP and one fixed, bounded child.
import { readFileSync, readSync, statSync, openSync, writeSync, closeSync, chmodSync } from 'node:fs';
import { request as httpRequest, validateHeaderName, validateHeaderValue } from 'node:http';
import { spawn } from 'node:child_process';
import { dirname, join } from 'node:path';
import { randomUUID } from 'node:crypto';

const MAX_LINE = 16 * 1024 * 1024;
const MAX_QUEUED = 6 * 1024 * 1024;
const MAX_RECORD = 8 * 1024 * 1024;
const args = process.argv.slice(2);
let ending = false;
let fixtureChild;
function exit(code) {
  if (ending) return;
  ending = true;
  process.stdin.pause();
  const finish = () => process.stdout.write('', () => process.stderr.write('', () => process.exit(code)));
  if (fixtureChild && fixtureChild.exitCode === null && fixtureChild.signalCode === null) {
    fixtureChild.once('close', finish);
    fixtureChild.kill('SIGKILL');
  } else finish();
}
for (const signal of ['SIGTERM', 'SIGINT']) {
  const handler = () => {
    if (fixtureChild) exit(signal === 'SIGTERM' ? 143 : 130);
    else { process.removeListener(signal, handler); process.kill(process.pid, signal); }
  };
  process.on(signal, handler);
}
function fail(message) {
  process.stderr.write(`fake-claude: ${message}\n`);
  exit(2);
}
function emit(value) { process.stdout.write(JSON.stringify(value) + '\n'); }
process.stdout.on('error', () => process.exit(2));
process.stdin.on('error', error => fail(error.message));

try {
  const limit = Number(process.env.FAKE_CLAUDE_RECORD_LIMIT ?? MAX_RECORD);
  if (!Number.isSafeInteger(limit) || limit < 1 || limit > MAX_RECORD) throw Error('invalid record limit');
  const recordPath = process.env.FAKE_CLAUDE_RECORD;
  function record(value) {
    if (!recordPath) return;
    const line = JSON.stringify({ pid: process.pid, ...value, timestamp: new Date().toISOString() }) + '\n';
    const fd = openSync(recordPath, 'a', 0o600);
    try {
      chmodSync(recordPath, 0o600);
      if (statSync(recordPath).size + Buffer.byteLength(line) > limit) throw Error('record limit exceeded');
      writeSync(fd, line);
    } finally { closeSync(fd); }
  }
  const envKeys = ['TMUX', 'TMUX_PANE', 'CLAUDECODE', 'CLAUDE_CODE_ENTRYPOINT',
    'FAKE_CLAUDE_SCRIPT', 'FAKE_CLAUDE_RECORD', 'FAKE_CLAUDE_RECORD_LIMIT'];
  record({ kind: 'start', argv: args, cwd: process.cwd(), env: Object.fromEntries(
    envKeys.filter(key => process.env[key] !== undefined).map(key => [key, process.env[key]])) });

  let config = {};
  if (process.env.FAKE_CLAUDE_SCRIPT) {
    if (statSync(process.env.FAKE_CLAUDE_SCRIPT).size > 1024 * 1024) throw Error('script exceeds 1 MiB');
    config = JSON.parse(readFileSync(process.env.FAKE_CLAUDE_SCRIPT, 'utf8'));
  }
  if (!config || Array.isArray(config) || typeof config !== 'object') throw Error('script must be object');
  const allowed = ['auth', 'startup', 'turns', 'resume_missing', 'refuse_model', 'capabilities', 'continue_after_mcp_error', 'turns_by_input', 'executions_by_input', 'ignore_sighup', 'replay_control', 'replay_role', 'contextWindow', 'resultModel', 'interrupt_output'];
  for (const key of Object.keys(config)) if (!allowed.includes(key)) throw Error(`unknown script key: ${key}`);
  if (config.replay_control !== undefined && !['skip', 'hold'].includes(config.replay_control)) throw Error('invalid replay_control');
  if (config.interrupt_output !== undefined && (!Array.isArray(config.interrupt_output) || config.interrupt_output.length !== 2 || config.interrupt_output.some(x => typeof x !== 'string' || !x || x.length > 1024))) throw Error('interrupt_output requires two bounded strings');
  if (config.ignore_sighup !== undefined && typeof config.ignore_sighup !== 'boolean') throw Error('ignore_sighup must be boolean');
  if (config.ignore_sighup) process.on('SIGHUP', () => {});
  function validateSteps(steps) {
    if (!Array.isArray(steps)) throw Error('steps must be array');
    for (const step of steps) {
      if (!step || Object.keys(step).length !== 1) throw Error('step needs exactly one action');
      const [key] = Object.keys(step);
      const value = step[key];
      if (['text', 'thinking', 'raw', 'stderr'].includes(key)) {
        if (typeof value !== 'string' && !(key === 'raw' && value &&
          typeof value.text === 'string' && Number.isSafeInteger(value.repeat) && value.repeat >= 0 &&
          Buffer.byteLength(value.text) * value.repeat <= 32 * 1024 * 1024)) {
          throw Error(`${key} must be string (raw also accepts bounded text/repeat)`);
        }
      } else if (key === 'barrier') {
        if (typeof value !== 'string' || !/^[a-z0-9-]+$/.test(value)) throw Error('invalid barrier');
      } else if (key === 'wait') {
        if (value !== 'interrupt') throw Error('wait must be interrupt');
      } else if (key === 'hang') {
        if (value !== true) throw Error('hang must be true');
      } else if (key === 'crash') {
        if (!Number.isInteger(value) || value < 1 || value > 255) throw Error('invalid crash exit code');
      } else if (key === 'emit') {
        if (!value || typeof value !== 'object' || typeof value.type !== 'string') throw Error('emit needs event type');
      } else if (key === 'tool') {
        if (!value || typeof value.id !== 'string' || typeof value.name !== 'string') throw Error('tool needs id/name');
      } else if (key === 'mcp_call') {
        if (!value || typeof value.id !== 'string' || !value.id || typeof value.name !== 'string' ||
          !/^[a-zA-Z0-9_-]+$/.test(value.name) || ('input' in value &&
          (!value.input || Array.isArray(value.input) || typeof value.input !== 'object')) ||
          Object.keys(value).some(k => !['id', 'name', 'input'].includes(k))) throw Error('invalid mcp_call');
      } else if (key === 'spawn_child') {
        if (!value || Array.isArray(value) || typeof value !== 'object' || Object.keys(value).length) throw Error('spawn_child needs {}');
      } else if (key === 'rate_limit') {
        if (!value || !Number.isFinite(value.resetsAt)) throw Error('rate_limit needs resetsAt');
      } else throw Error(`unknown action: ${key}`);
    }
  }
  validateSteps(config.startup ?? []);
  if (config.turns !== undefined && !Array.isArray(config.turns)) throw Error('turns must be array');
  for (const steps of config.turns ?? []) validateSteps(steps);
  if (config.turns_by_input !== undefined) {
    if (!Array.isArray(config.turns_by_input) || config.turns_by_input.length > 64) throw Error('invalid turns_by_input');
    for (const rule of config.turns_by_input) {
      if (!rule || typeof rule.contains !== 'string' || !rule.contains || Object.keys(rule).some(key => !['contains', 'steps'].includes(key))) throw Error('invalid input rule');
      validateSteps(rule.steps);
    }
  }
  if (config.executions_by_input !== undefined) {
    if (!Array.isArray(config.executions_by_input) || !config.executions_by_input.length || config.executions_by_input.length > 64 || config.turns_by_input !== undefined || config.turns !== undefined) throw Error('invalid executions_by_input');
    for (const rule of config.executions_by_input) {
      if (!rule || typeof rule.contains !== 'string' || !rule.contains || !Array.isArray(rule.turns) || Object.keys(rule).some(key => !['contains', 'turns'].includes(key))) throw Error('invalid execution rule');
      for (const steps of rule.turns) validateSteps(steps);
    }
  }
  if (config.capabilities !== undefined && (!Array.isArray(config.capabilities) ||
    !config.capabilities.every(value => typeof value === 'string'))) throw Error('invalid capabilities');

  if (args.join(' ') === 'auth status --json') {
    const auth = config.auth ?? {};
    if (auth.hang) {
      // Bounded by caller's process deadline; no stdin EOF exit in timeout scenarios.
      setInterval(() => {}, 1000);
    } else {
      if (auth.raw !== undefined) process.stdout.write(auth.raw + '\n');
      else emit({ loggedIn: auth.loggedIn ?? true, authMethod: 'claude.ai', subscriptionType: 'max' });
      exit(auth.exit_code ?? (auth.loggedIn === false ? 1 : 0));
    }
  } else {
    const options = {};
    const booleans = ['-p', '--verbose', '--replay-user-messages'];
    const values = ['--input-format', '--output-format', '--model', '--permission-mode',
      '--session-id', '--resume', '--mcp-config', '--append-system-prompt-file'];
    for (let i = 0; i < args.length; i++) {
      if (booleans.includes(args[i])) options[args[i]] = true;
      else if (values.includes(args[i]) && typeof args[i + 1] === 'string') options[args[i]] = args[++i];
      else throw Error(`unsupported CLI argument: ${args[i]}`);
    }
    if (!options['-p'] || options['--input-format'] !== 'stream-json' ||
      options['--output-format'] !== 'stream-json') throw Error('requires -p and stream-json formats');
    if (options['--session-id'] && options['--resume']) throw Error('session-id and resume are exclusive');
    function mcpConfig() {
      try {
        const path = options['--mcp-config'];
        if (!path || statSync(path).size > 1024 * 1024) throw Error('config missing or exceeds 1 MiB');
        const fd = openSync(path, 'r');
        const bytes = Buffer.alloc(1024 * 1024 + 1);
        let size = 0;
        try {
          while (size < bytes.length) {
            const count = readSync(fd, bytes, size, bytes.length - size, null);
            if (!count) break;
            size += count;
          }
        } finally { closeSync(fd); }
        if (size > 1024 * 1024) throw Error('config exceeds 1 MiB');
        const server = JSON.parse(bytes.subarray(0, size).toString('utf8'))?.mcpServers?.mecha;
        if (!server || server.type !== 'http' || typeof server.url !== 'string') throw Error('requires mcpServers.mecha HTTP server');
        const url = new URL(server.url);
        if (url.protocol !== 'http:' || !['127.0.0.1', '[::1]'].includes(url.hostname) ||
          url.username || url.password || url.hash) throw Error('URL must be literal loopback HTTP without credentials/fragment');
        const headers = server.headers ?? {};
        if (!headers || Array.isArray(headers) || typeof headers !== 'object' || Object.keys(headers).length > 32) throw Error('invalid headers');
        for (const [key, value] of Object.entries(headers)) {
          if (typeof value !== 'string' || value.length > 8192 || ['host', 'content-length', 'transfer-encoding', 'connection'].includes(key.toLowerCase())) throw Error('invalid header');
          validateHeaderName(key); validateHeaderValue(key, value);
        }
        return { url, headers };
      } catch { throw Error('MCP invalid config (requires bounded standard loopback HTTP mecha server)'); }
    }
    function mcpRequest(server, method, params, state, notification = false) {
      return new Promise((resolve, reject) => {
        const rpcId = `mcp-${id()}`;
        const body = JSON.stringify({ jsonrpc: '2.0', ...(notification ? {} : { id: rpcId }), method, params });
        let timer;
        const req = httpRequest(server.url, { method: 'POST', headers: {
          'Content-Type': 'application/json', Accept: 'application/json, text/event-stream', 'MCP-Protocol-Version': '2025-03-26', ...server.headers,
          'Content-Length': Buffer.byteLength(body)
        } }, res => {
          if (notification && res.statusCode === 202) { res.resume(); resolve(null); return; }
          if (res.statusCode !== 200) { reject(Error('MCP HTTP status failure')); res.destroy(); req.destroy(); return; }
          const chunks = []; let size = 0;
          res.on('data', chunk => {
            size += chunk.length;
            if (size > 1024 * 1024) { reject(Error('MCP response exceeds 1 MiB')); res.destroy(); req.destroy(); }
            else chunks.push(chunk);
          });
          res.on('error', reject);
          res.on('end', () => {
            try {
              const rpc = JSON.parse(Buffer.concat(chunks).toString('utf8'));
              if (rpc.jsonrpc !== '2.0' || rpc.id !== rpcId || rpc.error || !rpc.result) throw Error('invalid/error JSON-RPC response');
              resolve(rpc.result);
            } catch { reject(Error('MCP invalid/error JSON-RPC response')); }
          });
        });
        state.abort = () => req.destroy(Error('interrupted'));
        timer = setTimeout(() => req.destroy(Error('deadline exceeded')), 5000);
        req.on('error', () => reject(Error('MCP HTTP request failed')));
        req.on('close', () => clearTimeout(timer));
        req.end(body);
      });
    }
    async function callMcp(server, tool, state) {
      const init = await mcpRequest(server, 'initialize', { protocolVersion: '2025-03-26', capabilities: {}, clientInfo: { name: 'fake-claude', version: '1' } }, state);
      if (init.protocolVersion !== '2025-03-26') throw Error('MCP unsupported protocol version');
      await mcpRequest(server, 'notifications/initialized', {}, state, true);
      const list = await mcpRequest(server, 'tools/list', {}, state);
      if (!Array.isArray(list.tools)) throw Error('MCP invalid tools list');
      await mcpRequest(server, 'ping', {}, state);
      const result = await mcpRequest(server, 'tools/call', { name: tool.name, arguments: tool.input ?? {}, _meta: { 'claudecode/toolUseId': tool.id } }, state);
      if (!Array.isArray(result.content) || ('isError' in result && typeof result.isError !== 'boolean')) throw Error('MCP invalid tool result');
      return result;
    }
    const session = options['--session-id'] ?? options['--resume'] ?? '00000000-0000-4000-8000-000000000001';
    const models = { sonnet: 'claude-sonnet-5-5', haiku: 'claude-haiku-5-5', opus: 'claude-opus-5-5', fable: 'claude-fable-5-1' };
    let model = models[options['--model']] ?? options['--model'] ?? models.sonnet;
    let sequence = 0;
    let turn = 0;
    let executionTurns;
    let active = null;
    let blocked = false;
    let keepAlive;
    let eof = false;
    const pending = [];
    let queuedBytes = 0;
    const usage = { input_tokens: 100, output_tokens: 10, cache_creation_input_tokens: 20, cache_read_input_tokens: 30 };
    const id = () => `fake-${++sequence}`;
    function init() {
      emit({ type: 'system', subtype: 'init', session_id: session, uuid: id(), model,
        permissionMode: options['--permission-mode'] ?? 'default', tools: [], mcp_servers: [],
        capabilities: config.capabilities ?? ['interrupt_receipt_v1'] });
    }
    function assistant(content, extra = {}) {
      emit({ type: 'assistant', session_id: session, uuid: id(), parent_tool_use_id: null,
        message: { id: id(), type: 'message', role: 'assistant', model, content,
          stop_reason: content.some(block => block.type === 'tool_use') ? 'tool_use' : 'end_turn',
          stop_sequence: null, usage }, ...extra });
    }
    function result(error, text = '') {
      emit({ type: 'result', subtype: error ? 'error_during_execution' : 'success',
        session_id: session, uuid: id(), is_error: error, result: text,
        ...(error ? { errors: [text] } : {}), num_turns: 1, duration_ms: 1, duration_api_ms: 1,
        total_cost_usd: 0, usage, permission_denials: [],
        modelUsage: { [config.resultModel ?? model]: { inputTokens: 150, outputTokens: 10, contextWindow: config.contextWindow ?? 1000000, costUSD: 0 } } });
    }
    function barrier(name, inputID, resume) {
      if (!process.env.FAKE_CLAUDE_SCRIPT) throw Error('barrier requires script path');
      const invocation = randomUUID();
      const path = join(dirname(process.env.FAKE_CLAUDE_SCRIPT), `barrier-${invocation}`);
      record({ kind: 'barrier', name, invocation, pid: process.pid, input_id: inputID });
      const timer = setInterval(() => {
        try { readFileSync(path); } catch (error) { if (error.code === 'ENOENT') return; clearInterval(timer); fail(String(error)); return; }
        clearInterval(timer);
        record({ kind: 'barrier_released', name, invocation, pid: process.pid, input_id: inputID });
        if (!ending) resume();
      }, 10);
    }
    function pump() {
      if (ending || active || blocked) return;
      const next = pending.shift();
      if (!next) { if (eof) exit(0); return; }
      queuedBytes -= next.bytes;
      const input = JSON.stringify(next.value.message?.content);
      if (config.executions_by_input && !executionTurns) {
        const matches = config.executions_by_input.filter(rule => input.includes(rule.contains));
        if (matches.length !== 1) throw Error('execution input must match exactly one rule');
        executionTurns = matches[0].turns;
      }
      const inputRule = config.turns_by_input?.find(rule => input.includes(rule.contains));
      active = { inputID: next.value.uuid, steps: inputRule?.steps ?? (executionTurns ?? config.turns)?.[turn++] ?? [{ text: 'fake answer' }], index: 0, text: '' };
      init();
      const proceed = () => {
        if (options['--replay-user-messages'] && config.replay_control !== 'skip') {
          emit({ ...next.value, isReplay: true, message: { ...next.value.message, role: config.replay_role ?? next.value.message.role } });
        }
        runSteps(active);
      };
      if (config.replay_control === 'hold') barrier('replay', next.value.uuid, proceed); else proceed();
    }
    function runSteps(state) {
      while (!ending && state.index < state.steps.length) {
        const step = state.steps[state.index++];
        if ('text' in step) {
          state.text = (state.text ?? '') + step.text;
          assistant([{ type: 'text', text: step.text }]);
        }
        else if ('thinking' in step) assistant([{ type: 'thinking', thinking: step.thinking, signature: 'fake' }]);
        else if ('emit' in step) emit(step.emit);
        else if ('barrier' in step) { barrier(step.barrier, state.inputID, () => runSteps(state)); return; }
        else if ('raw' in step) process.stdout.write((typeof step.raw === 'string'
          ? step.raw : step.raw.text.repeat(step.raw.repeat)) + '\n');
        else if ('stderr' in step) process.stderr.write(step.stderr);
        else if ('crash' in step) { exit(step.crash); return; }
        else if ('wait' in step || 'hang' in step) {
          state.wait = 'hang' in step ? 'hang' : 'interrupt';
          blocked = true;
          keepAlive ??= setInterval(() => {}, 1000);
          return;
        } else if ('mcp_call' in step || 'spawn_child' in step) {
          blocked = true;
          let work;
          try {
            if ('mcp_call' in step) {
              const tool = step.mcp_call;
              const server = mcpConfig();
              assistant([{ type: 'tool_use', id: tool.id, name: `mcp__mecha__${tool.name}`, input: tool.input ?? {} }]);
              // Flush the actual producer before opening the HTTP request.
              work = new Promise(resolve => process.stdout.write('', resolve))
                .then(() => ending || state.cancelled ? null : callMcp(server, tool, state))
                .then(response => {
                  if (ending || state.cancelled) return;
                  emit({ type: 'user', session_id: session, uuid: id(), parent_tool_use_id: null,
                    message: { role: 'user', content: [{ type: 'tool_result', tool_use_id: tool.id,
                      content: response.content, is_error: response.isError ?? false }] } });
                });
            } else {
              if (fixtureChild) throw Error('only one fixture child permitted per process');
              fixtureChild = spawn(process.execPath, ['-e',
                `${config.ignore_sighup ? "process.on('SIGHUP', () => {});" : ''}setTimeout(() => process.exit(0), 60000); process.send("ready");`],
                { stdio: ['ignore', 'ignore', 'ignore', 'ipc'], env: {} });
              work = new Promise((resolve, reject) => {
                fixtureChild.once('error', reject);
                fixtureChild.once('message', () => {
                  try {
                    if (ending) { resolve(); return; }
                    record({ kind: 'child', pid: fixtureChild.pid, parent_pid: process.pid, max_lifetime_ms: 60000 });
                    emit({ type: 'system', subtype: 'fixture_child', pid: fixtureChild.pid,
                      parent_pid: process.pid, max_lifetime_ms: 60000 });
                    fixtureChild.disconnect();
                    resolve();
                  } catch (error) { reject(error); }
                });
              });
            }
          } catch (error) { fail(error.message); return; }
          work.then(() => {
            if (ending || state.cancelled) return;
            state.abort = undefined;
            blocked = false;
            runSteps(state);
          }).catch(error => {
            if (ending || state.cancelled) return;
            if (config.continue_after_mcp_error && 'mcp_call' in step) {
              emit({ type: 'system', subtype: 'fixture_mcp_error', message: error.message });
              state.abort = undefined; blocked = false; runSteps(state);
            } else fail(error.message);
          });
          return;
        } else if ('tool' in step) {
          const tool = step.tool;
          assistant([{ type: 'tool_use', id: tool.id, name: tool.name, input: tool.input ?? {} }]);
          emit({ type: 'user', session_id: session, uuid: id(), parent_tool_use_id: null,
            message: { role: 'user', content: [{ type: 'tool_result', tool_use_id: tool.id,
              content: tool.result ?? '', is_error: tool.is_error ?? false }] } });
        } else if ('rate_limit' in step) {
          const info = { status: 'rejected', rateLimitType: 'five_hour', ...step.rate_limit };
          emit({ type: 'rate_limit_event', rate_limit_info: info, session_id: session, uuid: id() });
          assistant([{ type: 'text', text: 'Usage limit reached' }], { is_api_error_message: true,
            api_error: 'usage_limit_reached', api_error_params: { rate_limit_info: info } });
          result(true, 'Usage limit reached');
          active = null;
          pump();
          return;
        }
      }
      if (state === active) { result(false, state.text); active = null; }
      pump();
    }
    function receive(value, bytes) {
      if (value.type === 'user') {
        if (!value.message || value.message.role !== 'user') throw Error('invalid user record');
        if (pending.length >= 64 || queuedBytes + bytes > MAX_QUEUED) throw Error('pending input limit exceeded');
        pending.push({ value, bytes }); queuedBytes += bytes; pump();
      } else if (value.type === 'control_request') {
        const request = value.request;
        const error = request?.subtype === 'set_model' && config.refuse_model
          ? 'model switch refused' : !['interrupt', 'set_model'].includes(request?.subtype) ? 'unsupported control' : null;
        if (request?.subtype === 'set_model' && !error) {
          if (typeof request.model !== 'string') throw Error('set_model needs model');
          model = models[request.model] ?? request.model;
        }
        if (!error && request?.subtype === 'interrupt' && active && config.interrupt_output) {
          for (const value of config.interrupt_output) assistant([{ type: 'text', text: value }]);
          record({ kind: 'interrupt_output', values: config.interrupt_output, input_id: active.inputID });
        }
        emit({ type: 'control_response', response: { subtype: error ? 'error' : 'success',
          request_id: value.request_id, ...(error ? { error } : { response: request.subtype === 'set_model' ? { model } : {} }) } });
        if (error) return;
        if (request.subtype === 'set_model') emit({ type: 'user', session_id: session, uuid: id(),
          message: { role: 'user', content: `<local-command-stdout>Set model to ${model}</local-command-stdout>` } });
        else if (active && active.wait !== 'hang') {
          active.cancelled = true; active.abort?.();
          result(true, 'Interrupted'); active = null; blocked = false;
          clearInterval(keepAlive); keepAlive = undefined;
          pump();
        }
      } else throw Error(`unsupported stdin record: ${value.type}`);
    }

    if (config.resume_missing && options['--resume']) {
      const message = `No conversation found with session ID: ${session}`;
      process.stderr.write(message + '\n');
      emit({ type: 'result', subtype: 'error_during_execution', is_error: true,
        errors: [message], num_turns: 0, session_id: session });
      exit(1);
    } else {
      // Startup never creates an automatic foreground result.
      runSteps({ steps: config.startup ?? [], index: 0 });
      let fragments = [];
      let lineBytes = 0;
      process.stdin.setEncoding('utf8');
      process.stdin.on('data', chunk => {
        if (ending) return;
        try {
          const parts = chunk.split('\n');
          for (let i = 0; i < parts.length && !ending; i++) {
            fragments.push(parts[i]);
            lineBytes += Buffer.byteLength(parts[i]);
            if (lineBytes > MAX_LINE) throw Error('stdin line limit exceeded');
            if (i < parts.length - 1) {
              const line = fragments.join('');
              const bytes = lineBytes;
              fragments = []; lineBytes = 0;
              record({ kind: 'stdin', line });
              receive(JSON.parse(line), bytes);
            }
          }
        } catch (error) { fail(error.message); }
      });
      process.stdin.on('end', () => {
        if (ending) return;
        if (lineBytes) { fail('stdin requires newline-terminated JSON'); return; }
        eof = true; pump();
      });
    }
  }
} catch (error) { fail(error.message); }
