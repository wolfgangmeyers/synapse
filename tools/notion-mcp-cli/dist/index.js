#!/usr/bin/env node
"use strict";
var __importDefault = (this && this.__importDefault) || function (mod) {
    return (mod && mod.__esModule) ? mod : { "default": mod };
};
Object.defineProperty(exports, "__esModule", { value: true });
const node_http_1 = require("node:http");
const promises_1 = require("node:fs/promises");
const node_os_1 = require("node:os");
const node_path_1 = require("node:path");
const node_crypto_1 = require("node:crypto");
const commander_1 = require("commander");
const open_1 = __importDefault(require("open"));
const index_js_1 = require("@modelcontextprotocol/sdk/client/index.js");
const auth_js_1 = require("@modelcontextprotocol/sdk/client/auth.js");
const streamableHttp_js_1 = require("@modelcontextprotocol/sdk/client/streamableHttp.js");
const SERVER_URL = 'https://mcp.notion.com/mcp';
// OAuth clients register their redirect URI. Keep this stable so the registered
// client remains usable across invocations.
const OAUTH_PORT = 3388;
const CONFIG_PATH = (0, node_path_1.join)(process.env.XDG_CONFIG_HOME ?? (0, node_path_1.join)((0, node_os_1.homedir)(), '.config'), 'notion-mcp-cli', 'credentials.json');
async function loadCredentials() {
    try {
        return JSON.parse(await (0, promises_1.readFile)(CONFIG_PATH, 'utf8'));
    }
    catch (error) {
        if (error.code === 'ENOENT')
            return {};
        throw new Error(`Cannot read ${CONFIG_PATH}: ${error.message}`);
    }
}
async function saveCredentials(credentials) {
    await (0, promises_1.mkdir)((0, node_path_1.dirname)(CONFIG_PATH), { recursive: true, mode: 0o700 });
    const temporary = `${CONFIG_PATH}.${process.pid}.tmp`;
    await (0, promises_1.writeFile)(temporary, `${JSON.stringify(credentials, null, 2)}\n`, { mode: 0o600 });
    await (0, promises_1.chmod)(temporary, 0o600);
    await (0, promises_1.rename)(temporary, CONFIG_PATH);
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
            client_name: 'notion-mcp-cli',
            redirect_uris: [this.callbackUrl],
            grant_types: ['authorization_code', 'refresh_token'],
            response_types: ['code'],
            token_endpoint_auth_method: 'none',
        };
    }
    async state() {
        const state = (0, node_crypto_1.randomBytes)(32).toString('base64url');
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
        console.error(`Opening Notion login in your browser…\n${url}`);
        await (0, open_1.default)(url.toString());
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
        throw new Error('Not logged in. Run: notion-mcp login');
    const provider = await new FileOAuthProvider(OAUTH_PORT).init();
    const transport = new streamableHttp_js_1.StreamableHTTPClientTransport(new URL(SERVER_URL), { authProvider: provider });
    const client = new index_js_1.Client({ name: 'notion-mcp-cli', version: '1.0.0' });
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
    const server = (0, node_http_1.createServer)((request, response) => {
        const url = new URL(request.url ?? '/', 'http://127.0.0.1');
        if (url.pathname !== '/callback') {
            response.writeHead(404).end();
            return;
        }
        if (url.searchParams.get('error')) {
            response.writeHead(400, { 'content-type': 'text/html' }).end('<h1>Notion authorization was denied.</h1>You can close this tab.');
            rejectCallback(new Error(`Notion authorization failed: ${url.searchParams.get('error_description') ?? url.searchParams.get('error')}`));
            return;
        }
        if (!provider?.validState(url.searchParams.get('state')) || !url.searchParams.get('code')) {
            response.writeHead(400, { 'content-type': 'text/html' }).end('<h1>Invalid OAuth callback.</h1>You can close this tab.');
            rejectCallback(new Error('Rejected OAuth callback with missing code or invalid state.'));
            return;
        }
        response.writeHead(200, { 'content-type': 'text/html' }).end('<h1>Notion connected.</h1>You can close this tab and return to your terminal.');
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
        const started = await (0, auth_js_1.auth)(provider, { serverUrl: SERVER_URL });
        if (started !== 'REDIRECT')
            throw new Error('Notion did not request interactive authorization.');
        console.error('Waiting for Notion authorization callback…');
        const { code } = await callback;
        await (0, auth_js_1.auth)(provider, { serverUrl: SERVER_URL, authorizationCode: code });
        console.log('Connected to Notion.');
    }
    finally {
        server.close();
    }
}
const program = new commander_1.Command()
    .name('notion-mcp')
    .description('Standalone CLI client for Notion’s remote MCP server')
    .version('1.0.0');
program.command('login').description('Sign in to Notion with OAuth').action(login);
program.command('logout').description('Remove locally stored OAuth credentials').action(async () => {
    await (0, promises_1.rm)(CONFIG_PATH, { force: true });
    console.log('Logged out locally.');
});
program.command('status').description('Show whether local OAuth credentials exist').action(async () => {
    const credentials = await loadCredentials();
    console.log(credentials.tokens ? `Logged in (credentials: ${CONFIG_PATH})` : 'Not logged in. Run: notion-mcp login');
});
program.command('tools').description('List Notion MCP tools').option('--json', 'print raw JSON').action(async (options) => {
    const result = await withClient(client => client.listTools());
    if (options.json)
        console.log(JSON.stringify(result.tools, null, 2));
    else
        for (const tool of result.tools)
            console.log(`${tool.name}${tool.description ? `\t${tool.description}` : ''}`);
});
program.command('call <tool> <arguments-json>').description('Call a Notion MCP tool with a JSON object').action(async (tool, rawArguments) => {
    let args;
    try {
        args = JSON.parse(rawArguments);
    }
    catch {
        throw new Error('arguments-json must be a valid JSON object.');
    }
    if (!args || Array.isArray(args) || typeof args !== 'object')
        throw new Error('arguments-json must be a JSON object.');
    const result = await withClient(client => client.callTool({ name: tool, arguments: args }));
    console.log(JSON.stringify(result, null, 2));
});
program.parseAsync().catch((error) => {
    console.error(`Error: ${error.message}`);
    process.exitCode = 1;
});
