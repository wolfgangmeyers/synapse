# Claude sidecar and local CLI evidence

## Local CLI contract evidence

Root and evaluator fake-CLI tests check invocation arguments, not real CLI enforcement. The included opt-in [real CLI contract experiment](../tests/contract/README.md) documents an earlier host-local observation on Claude Code 2.1.316: `--tools "" --strict-mcp-config` advertised no built-in tools, while a positive control advertised Bash. Its user-level stdio MCP comparison also observed no server startup or advertised tools with strict mode, versus a working non-strict control. These are scoped advertisement and startup observations, not proof of tool execution restrictions or general MCP enforcement; a current passing run is not implied by inclusion. Managed settings, hooks and plugins remain unverified here. These flags are not an OS sandbox. Supported-version documentation and real CLI enforcement evidence remain outstanding.

## Sidecar retirement

The in-repository Claude sidecar and tmux REST client are absent; this does not establish that the separate launcher or live deployments are obsolete. No supported real-client private-tmux/mailbox replacement or complete retired-assertion map is established in this candidate. Inventory and migration, including safe terminal injection and delivery/ACK behavior, remain outstanding under CD-011; a fake sidecar is not equivalent evidence.
