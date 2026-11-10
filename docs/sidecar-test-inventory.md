# Claude sidecar and local CLI evidence

## Local CLI contract evidence

Root and evaluator fake-CLI tests check invocation arguments, not real CLI enforcement. The included opt-in [real CLI contract experiment](../tests/contract/README.md) documents an earlier host-local observation on Claude Code 2.1.316: `--tools "" --strict-mcp-config` advertised no built-in tools, while a positive control advertised Bash. Its user-level stdio MCP comparison also observed no server startup or advertised tools with strict mode, versus a working non-strict control. These are scoped advertisement and startup observations, not proof of tool execution restrictions or general MCP enforcement; a current passing run is not implied by inclusion. Managed settings, hooks and plugins remain unverified here. These flags are not an OS sandbox. Supported-version documentation and real CLI enforcement evidence remain outstanding.

## Restored tmux sidecar

The historical launcher, sidecar and mailbox client live in `tools/tmux-worker/`. The documented supervisor launcher entry point delegates to that implementation. The private-tmux Playwright test exercises the launcher, actual sidecar and router with a deterministic terminal stand-in; it does not establish live Claude CLI behavior or a complete retired-assertion map.
