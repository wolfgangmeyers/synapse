#!/usr/bin/env node
import { createServer } from 'node:http';
import { chmod, mkdir, readFile, rename, rm, writeFile } from 'node:fs/promises';
import { homedir } from 'node:os';
import { dirname, join } from 'node:path';
import { randomBytes } from 'node:crypto';
import { Command, Option } from 'commander';
import open from 'open';
import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { auth, } from '@modelcontextprotocol/sdk/client/auth.js';
import { StreamableHTTPClientTransport } from '@modelcontextprotocol/sdk/client/streamableHttp.js';
const SERVER_URL = 'https://mcp.linear.app/mcp';
const VOICE_REVIEW_CONFIRMATION_FLAG = '--confirm-wolfgang-voice-review';
const COMMENT_TOOLS = new Set(['save_comment', 'save_diff_comment']);
function requiresVoiceReviewConfirmation(tool, args) {
    return COMMENT_TOOLS.has(tool) || (tool === 'submit_diff_review' && typeof args.body === 'string');
}
function assertVoiceReviewConfirmation(tool, args, confirmed) {
    if (requiresVoiceReviewConfirmation(tool, args) && !confirmed) {
        throw new Error(`Human-facing Linear posts under Wolfgang's name require the Wolfgang voice review. Pass ${VOICE_REVIEW_CONFIRMATION_FLAG}.`);
    }
}
// OAuth clients register their redirect URI. Keep this stable so the registered
// client remains usable across invocations.
const OAUTH_PORT = 3389;
const CONFIG_PATH = join(process.env.XDG_CONFIG_HOME ?? join(homedir(), '.config'), 'linear-mcp-cli', 'credentials.json');
async function loadCredentials() {
    try {
        return JSON.parse(await readFile(CONFIG_PATH, 'utf8'));
    }
    catch (error) {
        if (error.code === 'ENOENT')
            return {};
        throw new Error(`Cannot read ${CONFIG_PATH}: ${error.message}`);
    }
}
async function saveCredentials(credentials) {
    await mkdir(dirname(CONFIG_PATH), { recursive: true, mode: 0o700 });
    const temporary = `${CONFIG_PATH}.${process.pid}.tmp`;
    await writeFile(temporary, `${JSON.stringify(credentials, null, 2)}\n`, { mode: 0o600 });
    await chmod(temporary, 0o600);
    await rename(temporary, CONFIG_PATH);
}
class FileOAuthProvider {
    credentials = {};
    expectedState;
    callbackUrl;
    constructor(port) {
        this.callbackUrl = `http://127.0.0.1:${port}/callback`;
    }
    async init() {
        this.credentials = await loadCredentials();
        return this;
    }
    get redirectUrl() { return this.callbackUrl; }
    get clientMetadata() {
        return {
            client_name: 'linear-mcp-cli',
            redirect_uris: [this.callbackUrl],
            grant_types: ['authorization_code', 'refresh_token'],
            response_types: ['code'],
            token_endpoint_auth_method: 'none',
        };
    }
    async state() {
        const state = randomBytes(32).toString('base64url');
        this.expectedState = state;
        return state;
    }
    validState(state) { return Boolean(state && state === this.expectedState); }
    clientInformation() { return this.credentials.clientInformation; }
    async saveClientInformation(value) {
        this.credentials.clientInformation = value;
        await saveCredentials(this.credentials);
    }
    tokens() { return this.credentials.tokens; }
    async saveTokens(value) {
        this.credentials.tokens = value;
        await saveCredentials(this.credentials);
    }
    async redirectToAuthorization(url) {
        console.error(`Opening Linear login in your browser…\n${url}`);
        await open(url.toString());
    }
    async saveCodeVerifier(value) {
        this.credentials.codeVerifier = value;
        await saveCredentials(this.credentials);
    }
    codeVerifier() {
        if (!this.credentials.codeVerifier)
            throw new Error('OAuth code verifier is missing; run login again.');
        return this.credentials.codeVerifier;
    }
    async saveDiscoveryState(value) {
        this.credentials.discovery = value;
        await saveCredentials(this.credentials);
    }
    discoveryState() { return this.credentials.discovery; }
    async invalidateCredentials(scope) {
        if (scope === 'all')
            this.credentials = {};
        if (scope === 'client')
            delete this.credentials.clientInformation;
        if (scope === 'tokens')
            delete this.credentials.tokens;
        if (scope === 'verifier')
            delete this.credentials.codeVerifier;
        if (scope === 'discovery')
            delete this.credentials.discovery;
        await saveCredentials(this.credentials);
    }
}
async function withClient(run) {
    const credentials = await loadCredentials();
    if (!credentials.tokens)
        throw new Error('Not logged in. Run: linear-mcp login');
    const provider = await new FileOAuthProvider(OAUTH_PORT).init();
    const transport = new StreamableHTTPClientTransport(new URL(SERVER_URL), { authProvider: provider });
    const client = new Client({ name: 'linear-mcp-cli', version: '1.0.0' });
    try {
        await client.connect(transport);
        return await run(client);
    }
    finally {
        await transport.close().catch(() => undefined);
    }
}
async function login() {
    let provider;
    let resolveCallback;
    let rejectCallback;
    const callback = new Promise((resolve, reject) => { resolveCallback = resolve; rejectCallback = reject; });
    const server = createServer((request, response) => {
        const url = new URL(request.url ?? '/', 'http://127.0.0.1');
        if (url.pathname !== '/callback') {
            response.writeHead(404).end();
            return;
        }
        if (url.searchParams.get('error')) {
            response.writeHead(400, { 'content-type': 'text/html' }).end('<h1>Linear authorization was denied.</h1>You can close this tab.');
            rejectCallback(new Error(`Linear authorization failed: ${url.searchParams.get('error_description') ?? url.searchParams.get('error')}`));
            return;
        }
        if (!provider?.validState(url.searchParams.get('state')) || !url.searchParams.get('code')) {
            response.writeHead(400, { 'content-type': 'text/html' }).end('<h1>Invalid OAuth callback.</h1>You can close this tab.');
            rejectCallback(new Error('Rejected OAuth callback with missing code or invalid state.'));
            return;
        }
        response.writeHead(200, { 'content-type': 'text/html' }).end('<h1>Linear connected.</h1>You can close this tab and return to your terminal.');
        resolveCallback({ code: url.searchParams.get('code') });
    });
    await new Promise((resolve, reject) => {
        server.once('error', reject);
        server.listen(OAUTH_PORT, '127.0.0.1', () => resolve());
    });
    try {
        const address = server.address();
        if (!address || typeof address === 'string')
            throw new Error('Could not start OAuth callback server.');
        provider = await new FileOAuthProvider(OAUTH_PORT).init();
        const started = await auth(provider, { serverUrl: SERVER_URL });
        if (started !== 'REDIRECT')
            throw new Error('Linear did not request interactive authorization.');
        console.error('Waiting for Linear authorization callback…');
        const { code } = await callback;
        await auth(provider, { serverUrl: SERVER_URL, authorizationCode: code });
        console.log('Connected to Linear.');
    }
    finally {
        server.close();
    }
}
const program = new Command()
    .name('linear-mcp')
    .description('Standalone CLI client for Linear’s remote MCP server')
    .version('1.0.0');
program.command('login').description('Sign in to Linear with OAuth').action(login);
program.command('logout').description('Remove locally stored OAuth credentials').action(async () => {
    await rm(CONFIG_PATH, { force: true });
    console.log('Logged out locally.');
});
program.command('status').description('Show whether local OAuth credentials exist').action(async () => {
    const credentials = await loadCredentials();
    console.log(credentials.tokens ? `Logged in (credentials: ${CONFIG_PATH})` : 'Not logged in. Run: linear-mcp login');
});
program.command('tools').description('List Linear MCP tools').option('--json', 'print raw JSON').action(async (options) => {
    const result = await withClient(client => client.listTools());
    if (options.json)
        console.log(JSON.stringify(result.tools, null, 2));
    else
        for (const tool of result.tools)
            console.log(`${tool.name}${tool.description ? `\t${tool.description}` : ''}`);
});
program.command('call <tool> <arguments-json>').description('Call a Linear MCP tool with a JSON object').addOption(new Option(VOICE_REVIEW_CONFIRMATION_FLAG).hideHelp()).action(async (tool, rawArguments, options) => {
    let args;
    try {
        args = JSON.parse(rawArguments);
    }
    catch {
        throw new Error('arguments-json must be a valid JSON object.');
    }
    if (!args || Array.isArray(args) || typeof args !== 'object')
        throw new Error('arguments-json must be a JSON object.');
    assertVoiceReviewConfirmation(tool, args, options.confirmWolfgangVoiceReview === true);
    const result = await withClient(client => client.callTool({ name: tool, arguments: args }));
    console.log(JSON.stringify(result, null, 2));
});
program.parseAsync().catch((error) => {
    console.error(`Error: ${error.message}`);
    process.exitCode = 1;
});
