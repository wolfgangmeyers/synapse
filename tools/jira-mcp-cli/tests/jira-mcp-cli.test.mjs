import assert from 'node:assert/strict';
import { execFile } from 'node:child_process';
import { chmod, mkdir, mkdtemp, readFile, stat, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { promisify } from 'node:util';
import test from 'node:test';

const execFileAsync = promisify(execFile);
const toolRoot = new URL('..', import.meta.url);

async function runCli(args, env = {}) {
  return execFileAsync(process.execPath, ['dist/index.js', ...args], {
    cwd: toolRoot,
    env: {
      ...process.env,
      XDG_CONFIG_HOME: await mkdtemp(join(tmpdir(), 'jira-mcp-cli-xdg-')),
      ...env,
    },
  });
}

test('builds create-issue MCP arguments', async () => {
  process.env.XDG_CONFIG_HOME = await mkdtemp(join(tmpdir(), 'jira-mcp-cli-import-'));
  const { buildCreateIssueArguments } = await import('../dist/index.js');

  assert.deepEqual(
    buildCreateIssueArguments('ABC', 'Task', 'Write docs', {
      cloudId: 'https://example.atlassian.net',
      description: 'Draft the release notes.',
      fields: { labels: ['docs'] },
      extraArguments: { priority: 'High' },
    }),
    {
      cloudId: 'https://example.atlassian.net',
      projectKey: 'ABC',
      issueTypeName: 'Task',
      summary: 'Write docs',
      description: 'Draft the release notes.',
      additional_fields: { labels: ['docs'] },
      priority: 'High',
    },
  );
});

test('builds comment MCP arguments', async () => {
  process.env.XDG_CONFIG_HOME = await mkdtemp(join(tmpdir(), 'jira-mcp-cli-import-'));
  const { buildCommentArguments } = await import('../dist/index.js');

  assert.deepEqual(
    buildCommentArguments('ABC-123', 'Ready for review.', {
      cloudId: 'https://example.atlassian.net',
      commentId: '10001',
      extraArguments: { commentBody: 'Overridden body.' },
    }),
    {
      cloudId: 'https://example.atlassian.net',
      issueIdOrKey: 'ABC-123',
      commentBody: 'Overridden body.',
      commentID: '10001',
    },
  );
});

test('saves credentials under the XDG config path with restrictive permissions', async () => {
  const xdgConfigHome = await mkdtemp(join(tmpdir(), 'jira-mcp-cli-config-'));
  process.env.XDG_CONFIG_HOME = xdgConfigHome;
  const { CONFIG_PATH, saveCredentials } = await import(`../dist/index.js?config=${Date.now()}`);

  await saveCredentials({ tokens: { access_token: 'test-token', token_type: 'Bearer' } });

  assert.equal(CONFIG_PATH, join(xdgConfigHome, 'jira-mcp-cli', 'credentials.json'));
  assert.deepEqual(JSON.parse(await readFile(CONFIG_PATH, 'utf8')), {
    tokens: { access_token: 'test-token', token_type: 'Bearer' },
  });
  assert.equal((await stat(join(xdgConfigHome, 'jira-mcp-cli'))).mode & 0o777, 0o700);
  assert.equal((await stat(CONFIG_PATH)).mode & 0o777, 0o600);
});

test('repairs pre-existing config directory permissions when saving credentials', async () => {
  const xdgConfigHome = await mkdtemp(join(tmpdir(), 'jira-mcp-cli-config-'));
  const configDir = join(xdgConfigHome, 'jira-mcp-cli');
  await mkdir(configDir, { recursive: true });
  await chmod(configDir, 0o777);
  process.env.XDG_CONFIG_HOME = xdgConfigHome;
  const { CONFIG_PATH, saveCredentials } = await import(`../dist/index.js?configPerms=${Date.now()}`);

  await saveCredentials({ tokens: { access_token: 'test-token', token_type: 'Bearer' } });

  assert.equal((await stat(configDir)).mode & 0o777, 0o700);
  assert.equal((await stat(CONFIG_PATH)).mode & 0o777, 0o600);
});

test('ignores relative XDG config paths for credential storage', async () => {
  process.env.XDG_CONFIG_HOME = 'relative-config';
  const { CONFIG_PATH } = await import(`../dist/index.js?relativeConfig=${Date.now()}`);

  assert.equal(CONFIG_PATH, join(process.env.HOME, '.config', 'jira-mcp-cli', 'credentials.json'));
});

test('create-issue command constructs the expected offline MCP call', async () => {
  process.env.XDG_CONFIG_HOME = await mkdtemp(join(tmpdir(), 'jira-mcp-cli-command-'));
  const { createProgram } = await import(`../dist/index.js?createIssue=${Date.now()}`);
  const calls = [];
  const program = createProgram({
    callTool: async (tool, args) => {
      calls.push({ tool, args });
      return { ok: true };
    },
  });
  program.exitOverride();

  await withCapturedStdout(() => program.parseAsync([
    'node',
    'jira-mcp',
    'create-issue',
    'ABC',
    'Task',
    'Write docs',
    '--cloud-id',
    'https://example.atlassian.net',
    '--description',
    'Draft the release notes.',
    '--field',
    'labels=["docs"]',
    '--args',
    '{"priority":"High"}',
  ]));

  assert.deepEqual(calls, [{
    tool: 'createJiraIssue',
    args: {
      cloudId: 'https://example.atlassian.net',
      projectKey: 'ABC',
      issueTypeName: 'Task',
      summary: 'Write docs',
      description: 'Draft the release notes.',
      additional_fields: { labels: ['docs'] },
      priority: 'High',
    },
  }]);
});

test('comment command supports tool override and argument override offline', async () => {
  process.env.XDG_CONFIG_HOME = await mkdtemp(join(tmpdir(), 'jira-mcp-cli-command-'));
  const { createProgram } = await import(`../dist/index.js?comment=${Date.now()}`);
  const calls = [];
  const program = createProgram({
    callTool: async (tool, args) => {
      calls.push({ tool, args });
      return { ok: true };
    },
  });
  program.exitOverride();

  await withCapturedStdout(() => program.parseAsync([
    'node',
    'jira-mcp',
    'comment',
    'ABC-123',
    'Ready for review.',
    '--cloud-id',
    'https://example.atlassian.net',
    '--comment-id',
    '10001',
    '--tool',
    'addOrEditJiraIssueComment',
    '--args',
    '{"commentBody":"Updated body."}',
  ]));

  assert.deepEqual(calls, [{
    tool: 'addOrEditJiraIssueComment',
    args: {
      cloudId: 'https://example.atlassian.net',
      issueIdOrKey: 'ABC-123',
      commentBody: 'Updated body.',
      commentID: '10001',
    },
  }]);
});

test('tools --json preserves live MCP schema details', async () => {
  process.env.XDG_CONFIG_HOME = await mkdtemp(join(tmpdir(), 'jira-mcp-cli-command-'));
  const { createProgram } = await import(`../dist/index.js?tools=${Date.now()}`);
  const program = createProgram({
    listTools: async () => [{
      name: 'createJiraIssue',
      description: 'Create a Jira issue.',
      inputSchema: {
        type: 'object',
        properties: {
          cloudId: { type: 'string' },
        },
      },
    }],
  });
  program.exitOverride();

  const stdout = await withCapturedStdout(() => program.parseAsync([
    'node',
    'jira-mcp',
    'tools',
    '--json',
  ]));

  assert.deepEqual(JSON.parse(stdout), [{
    name: 'createJiraIssue',
    description: 'Create a Jira issue.',
    inputSchema: {
      type: 'object',
      properties: {
        cloudId: { type: 'string' },
      },
    },
  }]);
});

test('collects paginated MCP tool listings', async () => {
  process.env.XDG_CONFIG_HOME = await mkdtemp(join(tmpdir(), 'jira-mcp-cli-command-'));
  const { collectTools } = await import(`../dist/index.js?pagination=${Date.now()}`);
  const calls = [];

  const tools = await collectTools({
    listTools: async (params) => {
      calls.push(params);
      if (!params?.cursor) {
        return {
          tools: [{ name: 'firstTool', inputSchema: { type: 'object' } }],
          nextCursor: 'page-2',
        };
      }
      return {
        tools: [{ name: 'secondTool', inputSchema: { type: 'object' } }],
      };
    },
  });

  assert.deepEqual(calls, [undefined, { cursor: 'page-2' }]);
  assert.deepEqual(tools.map(tool => tool.name), ['firstTool', 'secondTool']);
});

test('shows command shape in help output', async () => {
  const { stdout } = await runCli(['--help']);

  assert.match(stdout, /Usage: jira-mcp/);
  assert.match(stdout, /login/);
  assert.match(stdout, /tools/);
  assert.match(stdout, /call/);
  assert.match(stdout, /create-issue/);
  assert.match(stdout, /comment/);
});

test('status reports not logged in without credentials', async () => {
  const { stdout } = await runCli(['status']);

  assert.equal(stdout.trim(), 'Not logged in. Run: jira-mcp login');
});

test('status reports not logged in for malformed stored tokens', async () => {
  const xdgConfigHome = await mkdtemp(join(tmpdir(), 'jira-mcp-cli-malformed-'));
  const configDir = join(xdgConfigHome, 'jira-mcp-cli');
  await mkdir(configDir, { recursive: true });
  await writeFile(join(configDir, 'credentials.json'), '{"tokens":{}}\n');

  const { stdout } = await runCli(['status'], { XDG_CONFIG_HOME: xdgConfigHome });

  assert.equal(stdout.trim(), 'Not logged in. Run: jira-mcp login');
});

test('tool commands fail before network work when credentials are missing', async () => {
  await assert.rejects(
    runCli(['tools']),
    error => {
      assert.match(error.stderr, /Error: Not logged in\. Run: jira-mcp login/);
      return true;
    },
  );
});

test('mutating commands fail before network work when credentials are missing', async () => {
  await assert.rejects(
    runCli(['create-issue', 'ABC', 'Task', 'Smoke test', '--cloud-id', 'cloud-smoke']),
    error => {
      assert.match(error.stderr, /Error: Not logged in\. Run: jira-mcp login/);
      return true;
    },
  );
  await assert.rejects(
    runCli(['comment', 'ABC-123', 'Smoke test comment', '--cloud-id', 'cloud-smoke']),
    error => {
      assert.match(error.stderr, /Error: Not logged in\. Run: jira-mcp login/);
      return true;
    },
  );
});

async function withCapturedStdout(run) {
  const originalLog = console.log;
  const lines = [];
  console.log = (...args) => { lines.push(args.join(' ')); };
  try {
    await run();
    return lines.join('\n');
  } finally {
    console.log = originalLog;
  }
}
