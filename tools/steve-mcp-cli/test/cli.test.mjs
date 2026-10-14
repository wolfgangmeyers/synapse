import test from 'node:test';
import assert from 'node:assert/strict';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { resolve } from 'node:path';

const execFileAsync = promisify(execFile);
const binPath = resolve('dist/index.js');

test('CLI displays help message', async () => {
  const { stdout } = await execFileAsync('node', [binPath, '--help']);
  assert.match(stdout, /steve-mcp/);
  assert.match(stdout, /login/);
  assert.match(stdout, /status/);
  assert.match(stdout, /tools/);
  assert.match(stdout, /call/);
  assert.match(stdout, /prompts/);
  assert.match(stdout, /resources/);
});

test('CLI status reports not logged in when credentials do not exist', async () => {
  const env = { ...process.env, XDG_CONFIG_HOME: '/tmp/nonexistent-steve-test-dir' };
  const { stdout } = await execFileAsync('node', [binPath, 'status'], { env });
  assert.match(stdout, /Not logged in/);
});
