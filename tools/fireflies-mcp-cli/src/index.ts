#!/usr/bin/env node
import { createServer } from 'node:http';
import { chmod, mkdir, readFile, rename, rm, writeFile } from 'node:fs/promises';
import { homedir } from 'node:os';
import { dirname, join } from 'node:path';
import { randomBytes } from 'node:crypto';
import { Command } from 'commander';
import open from 'open';
import {
  auth,
  type OAuthDiscoveryState,
  type OAuthClientProvider,
} from '@modelcontextprotocol/sdk/client/auth.js';
import type { OAuthClientInformationMixed, OAuthClientMetadata, OAuthTokens } from '@modelcontextprotocol/sdk/shared/auth.js';

const SERVER_URL = 'https://api.fireflies.ai/mcp';
// OAuth clients register their redirect URI. Keep this stable so the registered
// client remains usable across invocations.
const OAUTH_PORT = 3390;
const CONFIG_DIR = join(process.env.XDG_CONFIG_HOME ?? join(homedir(), '.config'), 'fireflies-mcp-cli');
const CONFIG_PATH = join(CONFIG_DIR, 'credentials.json');
const RATE_LIMIT_PATH = join(CONFIG_DIR, 'rate-limit.json');
const REQUEST_INTERVAL_MS = Number(process.env.FIREFLIES_MCP_REQUEST_INTERVAL_MS ?? '1100');
const WAIT_FOR_RATE_LIMIT = process.env.FIREFLIES_MCP_WAIT_FOR_RATE_LIMIT === '1';

type StoredCredentials = {
  clientInformation?: OAuthClientInformationMixed;
  tokens?: OAuthTokens;
  codeVerifier?: string;
  discovery?: OAuthDiscoveryState;
};

async function loadCredentials(): Promise<StoredCredentials> {
  try {
    return JSON.parse(await readFile(CONFIG_PATH, 'utf8')) as StoredCredentials;
  } catch (error: unknown) {
    if ((error as NodeJS.ErrnoException).code === 'ENOENT') return {};
    throw new Error(`Cannot read ${CONFIG_PATH}: ${(error as Error).message}`);
  }
}

async function saveCredentials(credentials: StoredCredentials): Promise<void> {
  await mkdir(dirname(CONFIG_PATH), { recursive: true, mode: 0o700 });
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
      client_name: 'fireflies-mcp-cli',
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
    console.error(`Opening Fireflies login in your browser…\n${url}`);
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

type JsonRpcResponse<T> = {
  result?: T;
  error?: { message?: string; code?: number; data?: unknown };
};

type Tool = {
  name: string;
  description?: string;
};

type RateLimitState = {
  lastRequestAt?: number;
  retryAfterAt?: number;
};

async function loadRateLimitState(): Promise<RateLimitState> {
  try {
    return JSON.parse(await readFile(RATE_LIMIT_PATH, 'utf8')) as RateLimitState;
  } catch (error: unknown) {
    if ((error as NodeJS.ErrnoException).code === 'ENOENT') return {};
    throw new Error(`Cannot read ${RATE_LIMIT_PATH}: ${(error as Error).message}`);
  }
}

async function saveRateLimitState(state: RateLimitState): Promise<void> {
  await mkdir(dirname(RATE_LIMIT_PATH), { recursive: true, mode: 0o700 });
  const temporary = `${RATE_LIMIT_PATH}.${process.pid}.tmp`;
  await writeFile(temporary, `${JSON.stringify(state, null, 2)}\n`, { mode: 0o600 });
  await chmod(temporary, 0o600);
  await rename(temporary, RATE_LIMIT_PATH);
}

function sleep(milliseconds: number): Promise<void> {
  return new Promise(resolve => setTimeout(resolve, milliseconds));
}

function formatTime(milliseconds: number): string {
  return new Date(milliseconds).toISOString();
}

function parseRetryAfter(value: string | null): number | undefined {
  if (!value) return undefined;
  const numeric = Number(value);
  if (Number.isFinite(numeric)) {
    return numeric > 1_000_000_000_000 ? numeric : Date.now() + numeric * 1000;
  }
  const parsed = Date.parse(value);
  return Number.isNaN(parsed) ? undefined : parsed;
}

function findRetryAfter(value: unknown): number | undefined {
  if (typeof value === 'string') {
    const retryAfterMatch = value.match(/retry after ([^.\n]+)/i);
    return parseRetryAfter(retryAfterMatch?.[1] ?? null);
  }
  if (!value || typeof value !== 'object') return undefined;
  if (Array.isArray(value)) {
    for (const item of value) {
      const found = findRetryAfter(item);
      if (found) return found;
    }
    return undefined;
  }

  const record = value as Record<string, unknown>;
  const metadata = record.extensions && typeof record.extensions === 'object'
    ? (record.extensions as Record<string, unknown>).metadata
    : undefined;
  if (metadata && typeof metadata === 'object') {
    const retryAfter = (metadata as Record<string, unknown>).retryAfter;
    if (typeof retryAfter === 'number') return retryAfter > 1_000_000_000_000 ? retryAfter : Date.now() + retryAfter * 1000;
    if (typeof retryAfter === 'string') return parseRetryAfter(retryAfter);
  }

  for (const item of Object.values(record)) {
    const found = findRetryAfter(item);
    if (found) return found;
  }
  return undefined;
}

async function waitForRateLimitTurn(): Promise<void> {
  const state = await loadRateLimitState();
  const now = Date.now();
  if (state.retryAfterAt && state.retryAfterAt > now) {
    const delay = state.retryAfterAt - now;
    if (!WAIT_FOR_RATE_LIMIT) {
      throw new Error(`Fireflies rate limit is active until ${formatTime(state.retryAfterAt)}. Set FIREFLIES_MCP_WAIT_FOR_RATE_LIMIT=1 to wait instead of exiting.`);
    }
    await sleep(delay);
  }

  const current = Date.now();
  const nextAllowedAt = (state.lastRequestAt ?? 0) + REQUEST_INTERVAL_MS;
  if (nextAllowedAt > current) await sleep(nextAllowedAt - current);
}

async function recordRequestFinished(retryAfterAt?: number): Promise<void> {
  const state = await loadRateLimitState();
  state.lastRequestAt = Date.now();
  if (retryAfterAt && retryAfterAt > Date.now()) state.retryAfterAt = retryAfterAt;
  else if (state.retryAfterAt && state.retryAfterAt <= Date.now()) delete state.retryAfterAt;
  await saveRateLimitState(state);
}

async function postMcp<T>(token: string, payload: Record<string, unknown>): Promise<T | undefined> {
  await waitForRateLimitTurn();
  const response = await fetch(SERVER_URL, {
    method: 'POST',
    headers: {
      accept: 'application/json, text/event-stream',
      authorization: `Bearer ${token}`,
      'content-type': 'application/json',
    },
    body: JSON.stringify(payload),
  });
  const body = await response.text();
  const retryAfterAt = parseRetryAfter(response.headers.get('retry-after')) ?? findRetryAfter(body);
  await recordRequestFinished(retryAfterAt);

  if (!response.ok) {
    const retryText = retryAfterAt ? ` Retry after ${formatTime(retryAfterAt)}.` : '';
    throw new Error(`Fireflies MCP request failed (${response.status}): ${body || response.statusText}.${retryText}`);
  }
  if (response.status === 202 || !body.trim()) return undefined;

  const contentType = response.headers.get('content-type') ?? '';
  const rawJson = contentType.includes('text/event-stream')
    ? body.split('\n').find(line => line.startsWith('data: '))?.slice('data: '.length)
    : body;
  if (!rawJson) return undefined;

  const parsed = JSON.parse(rawJson) as JsonRpcResponse<T>;
  const parsedRetryAfterAt = findRetryAfter(parsed);
  if (parsedRetryAfterAt) await recordRequestFinished(parsedRetryAfterAt);
  if (parsed.error) {
    const retryText = parsedRetryAfterAt ? ` Retry after ${formatTime(parsedRetryAfterAt)}.` : '';
    throw new Error(`${parsed.error.message ?? `Fireflies MCP error ${parsed.error.code ?? ''}`.trim()}.${retryText}`);
  }
  return parsed.result;
}

async function assertNotRateLimited(result: unknown): Promise<void> {
  const retryAfterAt = findRetryAfter(result);
  if (!retryAfterAt) return;
  await recordRequestFinished(retryAfterAt);
  throw new Error(`Fireflies rate limit is active until ${formatTime(retryAfterAt)}.`);
}

async function withToken<T>(run: (token: string) => Promise<T>): Promise<T> {
  const credentials = await loadCredentials();
  const token = credentials.tokens?.access_token;
  if (!token) throw new Error('Not logged in. Run: fireflies-mcp login');
  return run(token);
}

async function initializeMcp(token: string): Promise<void> {
  await postMcp(token, {
    jsonrpc: '2.0',
    id: 1,
    method: 'initialize',
    params: {
      protocolVersion: '2024-11-05',
      capabilities: {},
      clientInfo: { name: 'fireflies-mcp-cli', version: '1.0.0' },
    },
  });
  await postMcp(token, { jsonrpc: '2.0', method: 'notifications/initialized', params: {} });
}

async function listTools(): Promise<Tool[]> {
  return withToken(async (token) => {
    await initializeMcp(token);
    const result = await postMcp<{ tools: Tool[] }>(token, { jsonrpc: '2.0', id: 2, method: 'tools/list', params: {} });
    const tools = result?.tools ?? [];
    await assertNotRateLimited(tools);
    return tools;
  });
}

async function callTool(name: string, args: Record<string, unknown>): Promise<unknown> {
  return withToken(async (token) => {
    await initializeMcp(token);
    const result = await postMcp(token, { jsonrpc: '2.0', id: 2, method: 'tools/call', params: { name, arguments: args } });
    await assertNotRateLimited(result);
    return result;
  });
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
      response.writeHead(400, { 'content-type': 'text/html' }).end('<h1>Fireflies authorization was denied.</h1>You can close this tab.');
      rejectCallback(new Error(`Fireflies authorization failed: ${url.searchParams.get('error_description') ?? url.searchParams.get('error')}`));
      return;
    }
    if (!provider?.validState(url.searchParams.get('state')) || !url.searchParams.get('code')) {
      response.writeHead(400, { 'content-type': 'text/html' }).end('<h1>Invalid OAuth callback.</h1>You can close this tab.');
      rejectCallback(new Error('Rejected OAuth callback with missing code or invalid state.'));
      return;
    }
    response.writeHead(200, { 'content-type': 'text/html' }).end('<h1>Fireflies connected.</h1>You can close this tab and return to your terminal.');
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
    if (started !== 'REDIRECT') throw new Error('Fireflies did not request interactive authorization.');
    console.error('Waiting for Fireflies authorization callback…');
    const { code } = await callback;
    await auth(provider, { serverUrl: SERVER_URL, authorizationCode: code });
    console.log('Connected to Fireflies.');
  } finally {
    server.close();
  }
}

const program = new Command()
  .name('fireflies-mcp')
  .description('Standalone CLI client for Fireflies remote MCP server')
  .version('1.0.0');
program.command('login').description('Sign in to Fireflies with OAuth').action(login);
program.command('logout').description('Remove locally stored OAuth credentials').action(async () => {
  await rm(CONFIG_PATH, { force: true });
  console.log('Logged out locally.');
});
program.command('status').description('Show whether local OAuth credentials exist').action(async () => {
  const credentials = await loadCredentials();
  const rateLimit = await loadRateLimitState();
  console.log(credentials.tokens ? `Logged in (credentials: ${CONFIG_PATH})` : 'Not logged in. Run: fireflies-mcp login');
  if (rateLimit.retryAfterAt && rateLimit.retryAfterAt > Date.now()) {
    console.log(`Fireflies rate limit active until ${formatTime(rateLimit.retryAfterAt)}`);
  }
});
program.command('tools').description('List Fireflies MCP tools').option('--json', 'print raw JSON').action(async (options) => {
  const tools = await listTools();
  if (options.json) console.log(JSON.stringify(tools, null, 2));
  else for (const tool of tools) console.log(`${tool.name}${tool.description ? `\t${tool.description}` : ''}`);
});
program.command('call <tool> <arguments-json>').description('Call a Fireflies MCP tool with a JSON object').action(async (tool, rawArguments) => {
  let args: Record<string, unknown>;
  try { args = JSON.parse(rawArguments); } catch { throw new Error('arguments-json must be a valid JSON object.'); }
  if (!args || Array.isArray(args) || typeof args !== 'object') throw new Error('arguments-json must be a JSON object.');
  const result = await callTool(tool, args);
  console.log(JSON.stringify(result, null, 2));
});

program.parseAsync().catch((error: Error) => {
  console.error(`Error: ${error.message}`);
  process.exitCode = 1;
});
