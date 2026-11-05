#!/usr/bin/env node
// Explicit one-off audit: node tools/audits/cli-archive-migration.audit.mjs
import assert from 'node:assert/strict';
import { fileURLToPath } from 'node:url';
import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { readFileSync, readdirSync, statSync } from 'node:fs';
import { resolve, relative, join } from 'node:path';

// Real repository/filesystem boundary audit, not a runtime/API smoke test.
const source = resolve(fileURLToPath(new URL('.', import.meta.url)), '../..');
const archive = process.env.MECHA_ARCHIVE_ROOT || resolve(source, '../mecha-archive');
const originalSha = '82d72a8c531af5c1d60ca81d49e589a17e737a5d';
const counts = { fireflies: 7, jira: 8, linear: 8, notion: 7 };
const git = (...args) => execFileSync('git', ['-C', source, ...args]);
const hash = (bytes) => createHash('sha256').update(bytes).digest('hex');
const paths = () => git('ls-tree', '-r', '--name-only', originalSha, '--',
  ...Object.keys(counts).map(name => `tools/${name}-mcp-cli`), 'tools/extract_alien_symbols.py').toString().trim().split('\n');
function expectedBytes(path, original) {
  if (!path.endsWith('/README.md')) return original;
  const name = path.split('/')[1];
  const old = /^(fireflies|jira)-/.test(name)
    ? `~/code/mecha-developer/tools/${name}` : `~/code/${name}`;
  return Buffer.from(original.toString().replaceAll(old, `~/code/mecha-archive/tools/${name}`));
}
function filesUnder(dir) {
  return readdirSync(dir, { withFileTypes: true }).flatMap(entry => {
    const path = join(dir, entry.name);
    return entry.isDirectory() ? filesUnder(path) : [relative(archive, path)];
  });
}

check('preserves all original blobs, modes, counts and provenance', () => {
  const provenance = readFileSync(join(archive, 'docs/mcp-cli-move-provenance.md'), 'utf8');
  assert.ok(provenance.includes(originalSha));
  const all = paths();
  assert.equal(all.length, 31);
  for (const [name, count] of Object.entries(counts)) {
    const dir = `tools/${name}-mcp-cli`;
    const expected = all.filter(path => path.startsWith(`${dir}/`));
    assert.equal(expected.length, count);
    assert.deepEqual(filesUnder(join(archive, dir)).sort(), expected.sort());
    assert.ok(provenance.includes(`| \`${dir}\` | ${count} |`));
  }
  for (const path of all) {
    const original = git('show', `${originalSha}:${path}`);
    const actual = readFileSync(join(archive, path));
    const mode = git('ls-tree', originalSha, '--', path).toString().split(' ')[0];
    if (path === 'tools/extract_alien_symbols.py') {
      const utility = readFileSync(join(archive, 'tools/extract_alien_symbols.provenance.md'), 'utf8');
      for (const evidence of [originalSha, path, `Bytes: ${original.length}`,
        `Git mode: \`${mode}\``, `SHA-256: \`${hash(original)}\``]) {
        assert.ok(utility.includes(evidence), evidence);
      }
    } else {
      assert.ok(provenance.includes(`| \`${path}\` | \`${mode}\` | ${original.length} | \`${hash(original)}\` |`), path);
    }
    assert.deepEqual(actual, expectedBytes(path, original), path);
    assert.equal(Boolean(statSync(join(archive, path)).mode & 0o111), mode === '100755', path);
  }
});

check('uses archive install directories in all four READMEs', () => {
  let instructions = 0;
  for (const name of Object.keys(counts)) {
    const content = readFileSync(join(archive, `tools/${name}-mcp-cli/README.md`), 'utf8');
    const dirs = content.split('\n').filter(line => line.startsWith('cd '));
    assert.equal(dirs.length, name === 'jira' ? 2 : 1);
    for (const line of dirs) assert.equal(line, `cd ~/code/mecha-archive/tools/${name}-mcp-cli`);
    instructions += dirs.length;
  }
  assert.equal(instructions, 5);
});

check('rejects corrupted archive bytes rather than checking source absence', () => {
  for (const path of paths()) {
    const original = git('show', `${originalSha}:${path}`);
    const actual = readFileSync(join(archive, path));
    const expected = expectedBytes(path, original);
    assert.equal(hash(actual), hash(expected), path);
    const corrupted = Buffer.concat([actual, Buffer.from('corruption')]);
    assert.notEqual(hash(corrupted), hash(expected), path);
  }
});

// Assertions and missing Git objects/files throw: there is no skip or fallback.
function check(name, run) {
  run();
  console.log(`PASS ${name}`);
}
