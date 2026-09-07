# Phase 1 validation

This package is the fixed thin Pi extension only. It registers the generated managed-tool manifest and carries control messages. It does not open a router, Mailbox, or tmux connection and does not implement router handlers.

## Reproducible checks

```sh
npm run check:mecha-rpc
npm run test:mecha-rpc
npm run test:mecha-rpc:native
npm --prefix pi-extensions/mecha-rpc run test:coverage
npm --prefix pi-extensions/mecha-rpc run build
```

`test:coverage` uses the package-local `c8` development dependency and writes its JSON report to `/tmp/mecha-rpc-coverage/`. It instruments only `pi-extensions/mecha-rpc/**/*.ts`.

## Coverage and boundary tests

The 21 focused tests cover the strict manifest grammar, malformed envelope inputs, exact 16 KiB control content and details boundaries, closed hidden-command envelopes, all eight custom-message detail schemas, registration failures at positions 1, 8, and 16, child bindings, stale child rejection, and child-stream append delegation.

The child test uses a UUID-shaped child session ID and the fork manager's in-memory binding. It verifies that the child receives a manager-issued `ctx1_` reference, the parent extension receives the pending request, the parent routes the result through `appendControlResultV1`, and child inline calls do not consume root credits.

## Stock Pi proof

`tests/mecha_rpc_native_pi_e2e.test.ts` runs installed Pi `0.85.0` in RPC mode with this extension and the approved `pi-subagents` fork entry. It uses a local deterministic OpenAI-compatible provider and temporary configuration only.

The proof checks all 15 application commands, the 16 generated tools on the real child's first model request, raw model tool-call capture, and correlation from that generated control request ID to the later result. A replay of that result produces no second custom result.

The companion `pi-subagents` change adds the public owner-bound inline-factory hook that installs this child extension before the child session binds. Router, durable-ledger, and browser acceptance work remain outside this Phase 1 extension package.
