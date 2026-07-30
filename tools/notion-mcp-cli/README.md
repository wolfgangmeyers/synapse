# notion-mcp-cli

A standalone command-line MCP client for Notion. It talks directly to `https://mcp.notion.com/mcp`; it does **not** add any MCP tools to pi, Claude Code, or another agent.

## Install / run

```sh
cd ~/code/notion-mcp-cli
npm install
npm run build
npm link                 # makes `notion-mcp` available on your PATH
notion-mcp login         # opens the Notion OAuth flow in your browser
notion-mcp tools
notion-mcp call <tool-name> '{"key":"value"}'
```

Useful commands:

```sh
notion-mcp status
notion-mcp logout
notion-mcp tools --json
```

OAuth client registration, tokens, and refresh tokens are stored only in
`~/.config/notion-mcp-cli/credentials.json`, with directory/file permissions
restricted to the current user. Do not commit or share that file.

If credentials are revoked or a command reports authentication failure, run
`notion-mcp login` again.
