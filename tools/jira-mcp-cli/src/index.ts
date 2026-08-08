#!/usr/bin/env node
import { createServer } from 'node:http';
import { chmod, mkdir, readFile, rename, rm, writeFile } from 'node:fs/promises';
import { homedir } from 'node:os';
import { dirname, isAbsolute, join } from 'node:path';
import { pathToFileURL } from 'node:url';
import { randomBytes } from 'node:crypto';
import { Command } from 'commander';
import open from 'open';
import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import {
  auth,
  type OAuthDiscoveryState,
  type OAuthClientProvider,
} from '@modelcontextprotocol/sdk/client/auth.js';
import type { OAuthClientInformationMixed, OAuthClientMetadata, OAuthTokens } from '@modelcontextprotocol/sdk/shared/auth.js';
import { StreamableHTTPClientTransport } from '@modelcontextprotocol/sdk/client/streamableHttp.js';

export const SERVER_URL = 'https://mcp.atlassian.com/v1/mcp/authv2';
// OAuth clients register their redirect URI. Keep this stable so the registered
// client remains usable across invocations.
export const OAUTH_PORT = 3391;
export function configHome(): string {
  const configured = process.env.XDG_CONFIG_HOME;
  if (configured && isAbsolute(configured)) return configured;
  return join(homedir(), '.config');
}
export const CONFIG_PATH = join(configHome(), 'jira-mcp-cli', 'credentials.json');
export const CREATE_ISSUE_TOOL = 'createJiraIssue';
export const COMMENT_TOOL = 'addCommentToJiraIssue';

export type StoredCredentials = {
  clientInformation?: OAuthClientInformationMixed;
  tokens?: OAuthTokens;
  codeVerifier?: string;
  discovery?: OAuthDiscoveryState;
};

type JsonRecord = Record<string, unknown>;

type ToolListItem = {
  name: string;
  description?: string;
  inputSchema?: unknown;
  [key: string]: unknown;
};

type CreateIssueOptions = {
  cloudId?: string;
  description?: string;
  fields?: JsonRecord;
  extraArguments?: JsonRecord;
};

type CommentOptions = {
  cloudId?: string;
  commentId?: string;
  extraArguments?: JsonRecord;
};

type CliDependencies = {
  callTool: (tool: string, args: JsonRecord) => Promise<unknown>;
  listTools: () => Promise<ToolListItem[]>;
  loadCredentials: () => Promise<StoredCredentials>;
  login: () => Promise<void>;
  logout: () => Promise<void>;
};

export async function loadCredentials(): Promise<StoredCredentials> {
  try {
    return JSON.parse(await readFile(CONFIG_PATH, 'utf8')) as StoredCredentials;
  } catch (error: unknown) {
    if ((error as NodeJS.ErrnoException).code === 'ENOENT') return {};
    throw new Error(`Cannot read ${CONFIG_PATH}: ${(error as Error).message}`);
  }
}

export function hasUsableTokens(credentials: StoredCredentials): boolean {
  return Boolean(
    credentials.tokens
      && typeof credentials.tokens.access_token === 'string'
      && credentials.tokens.access_token.length > 0
      && typeof credentials.tokens.token_type === 'string'
      && credentials.tokens.token_type.length > 0,
  );
}

export async function saveCredentials(credentials: StoredCredentials): Promise<void> {
  const directory = dirname(CONFIG_PATH);
  await mkdir(directory, { recursive: true, mode: 0o700 });
  await chmod(directory, 0o700);
  const temporary = `${CONFIG_PATH}.${process.pid}.tmp`;
  await writeFile(temporary, `${JSON.stringify(credentials, null, 2)}\n`, { mode: 0o600 });
  await chmod(temporary, 0o600);
  await rename(temporary, CONFIG_PATH);
}

class FileOAuthProvider implements OAuthClientProvider {
  private credentials: StoredCredentials = {};
  private expectedState?: string;
  private readonly callbackUrl: string;

  constructor(port: number) {
    this.callbackUrl = `http://127.0.0.1:${port}/callback`;
  }

  async init(): Promise<this> {
    this.credentials = await loadCredentials();
    return this;
  }

  get redirectUrl(): string { return this.callbackUrl; }
  get clientMetadata(): OAuthClientMetadata {
    return {
      client_name: 'jira-mcp-cli',
      redirect_uris: [this.callbackUrl],
      grant_types: ['authorization_code', 'refresh_token'],
      response_types: ['code'],
      token_endpoint_auth_method: 'none',
    };
  }
  async state(): Promise<string> {
    const state = randomBytes(32).toString('base64url');
    this.expectedState = state;
    return state;
  }
  validState(state: string | null): boolean { return Boolean(state && state === this.expectedState); }
  clientInformation(): OAuthClientInformationMixed | undefined { return this.credentials.clientInformation; }
  async saveClientInformation(value: OAuthClientInformationMixed): Promise<void> {
    this.credentials.clientInformation = value;
    await saveCredentials(this.credentials);
  }
  tokens(): OAuthTokens | undefined { return this.credentials.tokens; }
  async saveTokens(value: OAuthTokens): Promise<void> {
    this.credentials.tokens = value;
    await saveCredentials(this.credentials);
  }
  async redirectToAuthorization(url: URL): Promise<void> {
    console.error(`Opening Atlassian login in your browser...\n${url}`);
    await open(url.toString());
  }
  async saveCodeVerifier(value: string): Promise<void> {
    this.credentials.codeVerifier = value;
    await saveCredentials(this.credentials);
  }
  codeVerifier(): string {
    if (!this.credentials.codeVerifier) throw new Error('OAuth code verifier is missing; run login again.');
    return this.credentials.codeVerifier;
  }
  async saveDiscoveryState(value: OAuthDiscoveryState): Promise<void> {
    this.credentials.discovery = value;
    await saveCredentials(this.credentials);
  }
  discoveryState(): OAuthDiscoveryState | undefined { return this.credentials.discovery; }
  async invalidateCredentials(scope: 'all' | 'client' | 'tokens' | 'verifier' | 'discovery'): Promise<void> {
    if (scope === 'all') this.credentials = {};
    if (scope === 'client') delete this.credentials.clientInformation;
    if (scope === 'tokens') delete this.credentials.tokens;
    if (scope === 'verifier') delete this.credentials.codeVerifier;
    if (scope === 'discovery') delete this.credentials.discovery;
    await saveCredentials(this.credentials);
  }
}

async function withClient<T>(run: (client: Client) => Promise<T>): Promise<T> {
  const credentials = await loadCredentials();
  if (!hasUsableTokens(credentials)) throw new Error('Not logged in. Run: jira-mcp login');
  const provider = await new FileOAuthProvider(OAUTH_PORT).init();
  const transport = new StreamableHTTPClientTransport(new URL(SERVER_URL), { authProvider: provider });
  const client = new Client({ name: 'jira-mcp-cli', version: '1.0.0' });
  try {
    await client.connect(transport);
    return await run(client);
  } finally {
    await transport.close().catch(() => undefined);
  }
}

async function login(): Promise<void> {
  let provider: FileOAuthProvider | undefined;
  let resolveCallback!: (value: { code: string }) => void;
  let rejectCallback!: (reason: Error) => void;
  const callback = new Promise<{ code: string }>((resolve, reject) => { resolveCallback = resolve; rejectCallback = reject; });
  const server = createServer((request, response) => {
    const url = new URL(request.url ?? '/', 'http://127.0.0.1');
    if (url.pathname !== '/callback') { response.writeHead(404).end(); return; }
    if (url.searchParams.get('error')) {
      response.writeHead(400, { 'content-type': 'text/html' }).end('<h1>Atlassian authorization was denied.</h1>You can close this tab.');
      rejectCallback(new Error(`Atlassian authorization failed: ${url.searchParams.get('error_description') ?? url.searchParams.get('error')}`));
      return;
    }
    if (!provider?.validState(url.searchParams.get('state')) || !url.searchParams.get('code')) {
      response.writeHead(400, { 'content-type': 'text/html' }).end('<h1>Invalid OAuth callback.</h1>You can close this tab.');
      rejectCallback(new Error('Rejected OAuth callback with missing code or invalid state.'));
      return;
    }
    response.writeHead(200, { 'content-type': 'text/html' }).end('<h1>Atlassian connected.</h1>You can close this tab and return to your terminal.');
    resolveCallback({ code: url.searchParams.get('code')! });
  });

  await new Promise<void>((resolve, reject) => {
    server.once('error', reject);
    server.listen(OAUTH_PORT, '127.0.0.1', () => resolve());
  });
  try {
    const address = server.address();
    if (!address || typeof address === 'string') throw new Error('Could not start OAuth callback server.');
    provider = await new FileOAuthProvider(OAUTH_PORT).init();
    const started = await auth(provider, { serverUrl: SERVER_URL });
    if (started === 'AUTHORIZED') {
      console.log('Connected to Atlassian Rovo MCP.');
      return;
    }
    if (started !== 'REDIRECT') throw new Error('Atlassian did not request interactive authorization.');
    console.error('Waiting for Atlassian authorization callback...');
    const { code } = await callback;
    await auth(provider, { serverUrl: SERVER_URL, authorizationCode: code });
    console.log('Connected to Atlassian Rovo MCP.');
  } finally {
    server.close();
  }
}

export function parseJsonObject(raw: string, label = 'JSON'): JsonRecord {
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch {
    throw new Error(`${label} must be a valid JSON object.`);
  }
  if (!parsed || Array.isArray(parsed) || typeof parsed !== 'object') {
    throw new Error(`${label} must be a JSON object.`);
  }
  return parsed as JsonRecord;
}

function parseField(values: string[] | undefined): JsonRecord {
  const fields: JsonRecord = {};
  for (const value of values ?? []) {
    const equalsAt = value.indexOf('=');
    if (equalsAt <= 0) throw new Error('--field values must use name=json syntax.');
    const name = value.slice(0, equalsAt);
    const rawJson = value.slice(equalsAt + 1);
    try {
      fields[name] = JSON.parse(rawJson);
    } catch {
      throw new Error(`--field ${name} value must be valid JSON.`);
    }
  }
  return fields;
}

export function buildCreateIssueArguments(projectKey: string, issueTypeName: string, summary: string, options: CreateIssueOptions = {}): JsonRecord {
  const args: JsonRecord = {
    projectKey,
    issueTypeName,
    summary,
  };
  if (options.cloudId) args.cloudId = options.cloudId;
  if (options.description) args.description = options.description;
  if (options.fields && Object.keys(options.fields).length > 0) args.additional_fields = options.fields;
  return { ...args, ...(options.extraArguments ?? {}) };
}

export function buildCommentArguments(issueIdOrKey: string, commentBody: string, options: CommentOptions = {}): JsonRecord {
  const args: JsonRecord = {
    issueIdOrKey,
    commentBody,
  };
  if (options.cloudId) args.cloudId = options.cloudId;
  if (options.commentId) args.commentID = options.commentId;
  return { ...args, ...(options.extraArguments ?? {}) };
}

export async function collectTools(client: { listTools: (params?: { cursor?: string }) => Promise<{ tools: ToolListItem[]; nextCursor?: string }> }): Promise<ToolListItem[]> {
  const tools: ToolListItem[] = [];
  let cursor: string | undefined;
  do {
    const result = await client.listTools(cursor ? { cursor } : undefined);
    tools.push(...result.tools.map(tool => ({ ...tool })));
    cursor = result.nextCursor;
  } while (cursor);
  return tools;
}

async function listTools(): Promise<ToolListItem[]> {
  return withClient(client => collectTools(client));
}

async function callMcpTool(tool: string, args: JsonRecord): Promise<unknown> {
  return withClient(client => client.callTool({ name: tool, arguments: args }));
}

function printResult(result: unknown, json: boolean): void {
  if (json || typeof result !== 'object' || result === null) {
    console.log(JSON.stringify(result, null, 2));
    return;
  }
  console.log(JSON.stringify(result, null, 2));
}

function defaultDependencies(): CliDependencies {
  return {
    callTool: callMcpTool,
    listTools,
    loadCredentials,
    login,
    logout: async () => { await rm(CONFIG_PATH, { force: true }); },
  };
}

export function createProgram(overrides: Partial<CliDependencies> = {}): Command {
  const dependencies = { ...defaultDependencies(), ...overrides };
  const program = new Command()
    .name('jira-mcp')
    .description('Standalone CLI client for Atlassian Rovo MCP Jira tools')
    .version('1.0.0');

  program.command('login').description('Sign in to Atlassian with OAuth').action(dependencies.login);
  program.command('logout').description('Remove locally stored OAuth credentials').action(async () => {
    await dependencies.logout();
    console.log('Logged out locally.');
  });
  program.command('status').description('Show whether local OAuth credentials exist').action(async () => {
    const credentials = await dependencies.loadCredentials();
    console.log(hasUsableTokens(credentials) ? `Logged in (credentials: ${CONFIG_PATH})` : 'Not logged in. Run: jira-mcp login');
  });
  program.command('tools').description('List Atlassian Rovo MCP tools').option('--json', 'print raw JSON').action(async (options) => {
    const tools = await dependencies.listTools();
    if (options.json) console.log(JSON.stringify(tools, null, 2));
    else for (const tool of tools) console.log(`${tool.name}${tool.description ? `\t${tool.description}` : ''}`);
  });
  program.command('call <tool> <arguments-json>').description('Call an Atlassian Rovo MCP tool with a JSON object').action(async (tool, rawArguments) => {
    const args = parseJsonObject(rawArguments, 'arguments-json');
    const result = await dependencies.callTool(tool, args);
    console.log(JSON.stringify(result, null, 2));
  });
  program.command('create-issue <project-key> <issue-type> <summary>')
    .description('Create a Jira issue through the Atlassian Rovo MCP createJiraIssue tool')
    .requiredOption('--cloud-id <cloud-id>', 'Atlassian cloudId returned by getAccessibleAtlassianResources')
    .option('--description <markdown>', 'issue description text')
    .option('--field <name=json>', 'add a custom fields entry; may be repeated', (value, previous: string[] = []) => [...previous, value], [])
    .option('--args <json>', 'merge additional top-level MCP arguments, overriding generated values')
    .option('--tool <tool-name>', `MCP tool name to call (default: ${CREATE_ISSUE_TOOL})`, CREATE_ISSUE_TOOL)
    .option('--json', 'print raw JSON result')
    .action(async (projectKey, issueType, summary, options) => {
      const args = buildCreateIssueArguments(projectKey, issueType, summary, {
        cloudId: options.cloudId,
        description: options.description,
        fields: parseField(options.field),
        extraArguments: options.args ? parseJsonObject(options.args, '--args') : undefined,
      });
      const result = await dependencies.callTool(options.tool, args);
      printResult(result, Boolean(options.json));
    });
  program.command('comment <issue-id-or-key> <body>')
    .description('Post a Jira issue comment through the Atlassian Rovo MCP addCommentToJiraIssue tool')
    .requiredOption('--cloud-id <cloud-id>', 'Atlassian cloudId returned by getAccessibleAtlassianResources')
    .option('--comment-id <comment-id>', 'existing comment ID to update if the live tool supports it')
    .option('--args <json>', 'merge additional top-level MCP arguments, overriding generated values')
    .option('--tool <tool-name>', `MCP tool name to call (default: ${COMMENT_TOOL})`, COMMENT_TOOL)
    .option('--json', 'print raw JSON result')
    .action(async (issueIdOrKey, body, options) => {
      const args = buildCommentArguments(issueIdOrKey, body, {
        cloudId: options.cloudId,
        commentId: options.commentId,
        extraArguments: options.args ? parseJsonObject(options.args, '--args') : undefined,
      });
      const result = await dependencies.callTool(options.tool, args);
      printResult(result, Boolean(options.json));
    });

  return program;
}

export async function runCli(argv = process.argv): Promise<void> {
  await createProgram().parseAsync(argv);
}

const entrypoint = process.argv[1] ? pathToFileURL(process.argv[1]).href : undefined;
if (entrypoint === import.meta.url) {
  runCli().catch((error: Error) => {
    console.error(`Error: ${error.message}`);
    process.exitCode = 1;
  });
}
