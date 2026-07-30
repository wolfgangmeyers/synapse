# linear-mcp-cli

A standalone command-line MCP client for Linear. It talks directly to `https://mcp.linear.app/mcp`; it does **not** add MCP tools to pi, Claude Code, or another agent.

## Install / run

```sh
cd ~/code/linear-mcp-cli
npm install
npm run build
npm link                 # makes `linear-mcp` available on your PATH
linear-mcp login         # opens the Linear OAuth flow in your browser
linear-mcp tools
linear-mcp call <tool-name> '{"key":"value"}'
```

Useful commands:

```sh
linear-mcp status
linear-mcp logout
linear-mcp tools --json
```

OAuth client registration, tokens, and refresh tokens are stored only in
`~/.config/linear-mcp-cli/credentials.json`, with directory/file permissions
restricted to the current user. Do not commit or share that file.

If credentials are revoked or a command reports authentication failure, run
`linear-mcp login` again.
