# steve-mcp-cli

A standalone command-line MCP client for Steve / PA Wolfgang's remote MCP server (`https://pa-wolfgang.steve3.rogercalls.com/mcp`). It talks directly to the MCP endpoint via Streamable HTTP transport with OAuth 2.0 PKCE authentication; it does **not** add MCP tools directly to pi, Claude Code, or another agent.

## Install / setup

```sh
cd ~/code/mecha-developer/tools/steve-mcp-cli
npm install
npm run build
npm link                 # makes `steve-mcp` available on your PATH
```

Alternatively, symlink directly to your PATH:
```sh
ln -s /Users/wolfgang/code/mecha-developer/tools/steve-mcp-cli/dist/index.js /opt/homebrew/bin/steve-mcp
```

## Usage

```sh
steve-mcp login          # opens Steve OAuth flow in your browser (AuthKit)
steve-mcp status         # checks local credentials and tests server connection
steve-mcp tools          # lists available MCP tools
steve-mcp tools --json   # lists tools as JSON
steve-mcp call <tool-name> '{"key":"value"}'  # invokes a tool
steve-mcp prompts        # lists prompts
steve-mcp resources      # lists resources
steve-mcp logout         # removes local credentials
```

## Configuration & Credentials

OAuth client registration, tokens, and refresh tokens are stored in:
`~/.config/steve-mcp-cli/credentials.json` (or `$XDG_CONFIG_HOME/steve-mcp-cli/credentials.json`), with directory permissions restricted to `0700` and file permissions to `0600`.

Environment variables:
- `STEVE_MCP_SERVER_URL`: override server URL (defaults to `https://pa-wolfgang.steve3.rogercalls.com/mcp`)
- `STEVE_MCP_OAUTH_PORT`: local callback port for OAuth flow (defaults to `3391`)
