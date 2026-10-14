#!/usr/bin/env node
import { createServer } from 'node:http';
import { chmod, mkdir, readFile, rename, rm, writeFile } from 'node:fs/promises';
import { homedir } from 'node:os';
import { dirname, join } from 'node:path';
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

const SERVER_URL = process.env.STEVE_MCP_SERVER_URL ?? 'https://pa-wolfgang.steve3.rogercalls.com/mcp';
const OAUTH_PORT = Number(process.env.STEVE_MCP_OAUTH_PORT ?? '3391');
const CONFIG_PATH = join(process.env.XDG_CONFIG_HOME ?? join(homedir(), '.config'), 'steve-mcp-cli', 'credentials.json');

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

  get redirectUrl(): string {
    return this.callbackUrl;
  }

  get clientMetadata(): OAuthClientMetadata {
    return {
      client_name: 'steve-mcp-cli',
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

  validState(state: string | null): boolean {
    return Boolean(state && state === this.expectedState);
  }

  clientInformation(): OAuthClientInformationMixed | undefined {
    return this.credentials.clientInformation;
  }

  async saveClientInformation(value: OAuthClientInformationMixed): Promise<void> {
    this.credentials.clientInformation = value;
    await saveCredentials(this.credentials);
  }

  tokens(): OAuthTokens | undefined {
    return this.credentials.tokens;
  }

  async saveTokens(value: OAuthTokens): Promise<void> {
    this.credentials.tokens = value;
    await saveCredentials(this.credentials);
  }

  async redirectToAuthorization(url: URL): Promise<void> {
    console.error(`Opening Steve login in your browser…\n${url}`);
    await open(url.toString());
  }

  async saveCodeVerifier(value: string): Promise<void> {
    this.credentials.codeVerifier = value;
    await saveCredentials(this.credentials);
  }

  codeVerifier(): string {
    if (!this.credentials.codeVerifier) {
      throw new Error('OAuth code verifier is missing; run login again.');
    }
    return this.credentials.codeVerifier;
  }

  async saveDiscoveryState(value: OAuthDiscoveryState): Promise<void> {
    this.credentials.discovery = value;
    await saveCredentials(this.credentials);
  }

  discoveryState(): OAuthDiscoveryState | undefined {
    return this.credentials.discovery;
  }

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
  if (!credentials.tokens) {
    throw new Error('Not logged in. Run: steve-mcp login');
  }

  const provider = await new FileOAuthProvider(OAUTH_PORT).init();
  const transport = new StreamableHTTPClientTransport(new URL(SERVER_URL), {
    authProvider: provider,
  });

  const client = new Client(
    { name: 'steve-mcp-cli', version: '1.0.0' }
  );

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

  const callback = new Promise<{ code: string }>((resolve, reject) => {
    resolveCallback = resolve;
    rejectCallback = reject;
  });

  const server = createServer((request, response) => {
    const url = new URL(request.url ?? '/', 'http://127.0.0.1');
    if (url.pathname !== '/callback') {
      response.writeHead(404).end();
      return;
    }

    if (url.searchParams.get('error')) {
      response.writeHead(400, { 'content-type': 'text/html' }).end('<h1>Steve authorization was denied.</h1>You can close this tab.');
      rejectCallback(new Error(`Steve authorization failed: ${url.searchParams.get('error_description') ?? url.searchParams.get('error')}`));
      return;
    }

    if (!provider?.validState(url.searchParams.get('state')) || !url.searchParams.get('code')) {
      response.writeHead(400, { 'content-type': 'text/html' }).end('<h1>Invalid OAuth callback.</h1>You can close this tab.');
      rejectCallback(new Error('Rejected OAuth callback with missing code or invalid state.'));
      return;
    }

    response.writeHead(200, { 'content-type': 'text/html' }).end('<h1>Steve connected.</h1>You can close this tab and return to your terminal.');
    resolveCallback({ code: url.searchParams.get('code')! });
  });

  await new Promise<void>((resolve, reject) => {
    server.once('error', reject);
    server.listen(OAUTH_PORT, '127.0.0.1', () => resolve());
  });

  try {
    const address = server.address();
    if (!address || typeof address === 'string') {
      throw new Error('Could not start OAuth callback server.');
    }

    provider = await new FileOAuthProvider(OAUTH_PORT).init();
    const started = await auth(provider, { serverUrl: SERVER_URL });
    if (started !== 'REDIRECT') {
      throw new Error('Steve server did not request interactive authorization.');
    }

    console.error('Waiting for Steve authorization callback…');
    const { code } = await callback;
    await auth(provider, { serverUrl: SERVER_URL, authorizationCode: code });
    console.log('Connected to Steve.');
  } finally {
    server.close();
  }
}

const program = new Command()
  .name('steve-mcp')
  .description('Standalone CLI client for Steve / PA Wolfgang remote MCP server')
  .version('1.0.0');

program
  .command('login')
  .description('Sign in to Steve with OAuth')
  .action(login);

program
  .command('logout')
  .description('Remove locally stored OAuth credentials')
  .action(async () => {
    await rm(CONFIG_PATH, { force: true });
    console.log('Logged out locally.');
  });

program
  .command('status')
  .description('Show whether local OAuth credentials exist and test server connection')
  .action(async () => {
    const credentials = await loadCredentials();
    if (!credentials.tokens) {
      console.log('Not logged in. Run: steve-mcp login');
      return;
    }

    console.log(`Logged in (credentials: ${CONFIG_PATH})`);
    try {
      await withClient(async (client) => {
        const serverVersion = client.getServerVersion();
        const serverInfo = serverVersion ? ` (server: ${serverVersion.name} ${serverVersion.version})` : '';
        console.log(`Connection test: OK${serverInfo}`);
      });
    } catch (err) {
      console.error(`Connection test failed: ${(err as Error).message}`);
      process.exitCode = 1;
    }
  });

program
  .command('tools')
  .description('List Steve MCP tools')
  .option('--json', 'print raw JSON')
  .action(async (options) => {
    const result = await withClient(client => client.listTools());
    if (options.json) {
      console.log(JSON.stringify(result.tools, null, 2));
    } else {
      for (const tool of result.tools) {
        console.log(`${tool.name}${tool.description ? `\t${tool.description}` : ''}`);
      }
    }
  });

program
  .command('call <tool> <arguments-json>')
  .description('Call a Steve MCP tool with a JSON object')
  .action(async (tool, rawArguments) => {
    let args: Record<string, unknown>;
    try {
      args = JSON.parse(rawArguments);
    } catch {
      throw new Error('arguments-json must be a valid JSON object.');
    }
    if (!args || Array.isArray(args) || typeof args !== 'object') {
      throw new Error('arguments-json must be a JSON object.');
    }
    const result = await withClient(client => client.callTool({ name: tool, arguments: args }));
    console.log(JSON.stringify(result, null, 2));
  });

program
  .command('prompts')
  .description('List Steve MCP prompts')
  .option('--json', 'print raw JSON')
  .action(async (options) => {
    const result = await withClient(client => client.listPrompts());
    if (options.json) {
      console.log(JSON.stringify(result.prompts, null, 2));
    } else {
      if (result.prompts.length === 0) {
        console.log('No prompts available.');
      }
      for (const prompt of result.prompts) {
        console.log(`${prompt.name}${prompt.description ? `\t${prompt.description}` : ''}`);
      }
    }
  });

program
  .command('resources')
  .description('List Steve MCP resources')
  .option('--json', 'print raw JSON')
  .action(async (options) => {
    const result = await withClient(client => client.listResources());
    if (options.json) {
      console.log(JSON.stringify(result.resources, null, 2));
    } else {
      if (result.resources.length === 0) {
        console.log('No resources available.');
      }
      for (const resource of result.resources) {
        console.log(`${resource.uri}\t${resource.name}${resource.description ? `\t${resource.description}` : ''}`);
      }
    }
  });

program.parseAsync().catch((error: Error) => {
  console.error(`Error: ${error.message}`);
  process.exitCode = 1;
});
