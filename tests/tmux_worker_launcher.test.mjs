import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { resolve } from 'node:path';

test('documented launcher loads active implementation and default model', () => {
  const result=spawnSync('python3',[resolve('.claude/skills/supervisor/tools/worker_launch.py'),'--help'],{encoding:'utf8',timeout:10000});
  assert.equal(result.status,0,result.stderr);
  assert.match(result.stdout,/gemini-3\.8-flash/);
  assert.match(result.stdout,/google-vertex/);
});
