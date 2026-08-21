import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import test from 'node:test';

const VOICE_REVIEW_CONFIRMATION_FLAG = '--confirm-wolfgang-voice-review';

const cliPath = new URL('../dist/index.js', import.meta.url).pathname;

function runCli(...args) {
  return spawnSync(process.execPath, [cliPath, ...args], { encoding: 'utf8' });
}

test('rejects comment creation and edits before connecting to Linear without confirmation', () => {
  for (const [tool, args] of [
    ['save_comment', '{"issue":"EMR-1","body":"Comment"}'],
    ['save_diff_comment', '{"urlOrId":"diff-1","body":"Comment"}'],
    ['submit_diff_review', '{"urlOrId":"diff-1","decision":"commented","body":"Comment"}'],
  ]) {
    const result = runCli('call', tool, args);

    assert.equal(result.status, 1);
    assert.match(result.stderr, /Human-facing Linear posts under Wolfgang's name require the Wolfgang voice review\./);
    assert.match(result.stderr, new RegExp(VOICE_REVIEW_CONFIRMATION_FLAG));
    assert.doesNotMatch(result.stderr, /Not logged in/);
  }
});

test('allows normal commands without the confirmation flag', () => {
  const result = runCli('status');

  assert.equal(result.status, 0);
  assert.doesNotMatch(`${result.stdout}${result.stderr}`, /Wolfgang voice review/);
});

test('keeps the confirmation flag out of CLI help', () => {
  const result = runCli('call', '--help');

  assert.equal(result.status, 0);
  assert.doesNotMatch(`${result.stdout}${result.stderr}`, new RegExp(VOICE_REVIEW_CONFIRMATION_FLAG));
});
