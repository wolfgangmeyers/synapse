# fireflies-mcp-cli

A standalone command-line MCP client for Fireflies. It talks directly to `https://api.fireflies.ai/mcp`; it does **not** add any MCP tools to pi, Claude Code, or another agent.

## Install / run

```sh
cd ~/code/mecha-developer/tools/fireflies-mcp-cli
npm install
npm run build
ln -sf "$PWD/dist/index.js" /opt/homebrew/bin/fireflies-mcp
fireflies-mcp login      # opens the Fireflies OAuth flow in your browser
fireflies-mcp tools
fireflies-mcp call <tool-name> '{"key":"value"}'
```

Useful commands:

```sh
fireflies-mcp status
fireflies-mcp logout
fireflies-mcp tools --json
```

OAuth client registration, tokens, and refresh tokens are stored only in
`~/.config/fireflies-mcp-cli/credentials.json`, with directory/file permissions
restricted to the current user. Do not commit or share that file.

If credentials are revoked or a command reports authentication failure, run
`fireflies-mcp login` again.

## Authentication notes

Fireflies documents OAuth as the normal auth flow for the remote MCP endpoint, so this CLI uses the MCP SDK OAuth helper for `fireflies-mcp login` and stores only OAuth credentials locally.

The Fireflies MCP server responds to stateless JSON-RPC POST requests after OAuth. The `tools` and `call` commands use that direct MCP request path with the saved OAuth token because Fireflies does not behave like the Linear and Notion Streamable HTTP servers once the SDK transport is connected.

Fireflies also documents API-key authentication for MCP clients that support custom headers. For those clients, configure the same endpoint with an `Authorization: Bearer <Fireflies API key>` header. This CLI does not collect or store API keys; use `fireflies-mcp login` for the local CLI flow.

## Rate limits

Fireflies documents these API limits:

- Free: 50 requests/day
- Pro: 500 requests/day
- Business and Enterprise: 60 requests/minute
- Special cases: Add to Live is 3 requests/20 minutes; Share Meeting is 10 requests/hour

Fireflies rate-limit errors can include a retry time in `extensions.metadata.retryAfter`; MCP responses may also surface the same information as text such as `Too many requests. Please retry after ...`. Sources: Fireflies API docs, **Limits** (`https://docs.fireflies.ai/fundamentals/limits`) and **Error codes** (`https://docs.fireflies.ai/miscellaneous/error-codes`).

The CLI keeps a small local rate-limit state file at `~/.config/fireflies-mcp-cli/rate-limit.json`. It waits at least 1.1 seconds between MCP requests, which keeps ordinary serial use below the documented Business/Enterprise limit, and it records server-provided retry times so later commands stop before making another request. By default it exits with a clear retry time instead of sleeping until a long reset window. If you intentionally want the command to wait until the reset time, run it with:

```sh
FIREFLIES_MCP_WAIT_FOR_RATE_LIMIT=1 fireflies-mcp call <tool-name> '{"key":"value"}'
```

For diagnostics, the interval can be overridden with `FIREFLIES_MCP_REQUEST_INTERVAL_MS`, but normal use should leave the default in place.
