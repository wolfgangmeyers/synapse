# Managed RPC bridge

This package implements a WebSocket client that owns one Pi process and connects to the router's private `/v1/managed-bridge` transport. It is no longer a Phase 0 wire-only placeholder. The executable is [`cmd/managed-pi-bridge`](../../cmd/managed-pi-bridge/main.go), invoked with `--config <path>`; configuration/token files are read through the package's validated file boundary. Tokens are not command-line values or wire fields.

This is an optional implemented transport seam, not a claim that every managed launch uses it. Other direct-RPC and embedded paths coexist. The command/package's older phase comments do not establish current router integration or default enablement.

## Lifecycle and authority

[`bridge.go`](bridge.go) implements connection, mutual proof, resume, command/event pumping, and reconnect. [`process.go`](process.go) owns the Pi subprocess; [`rpc_stream.go`](rpc_stream.go) carries its RPC stream. [`config.go`](config.go) validates bounded configuration and a loopback `ws://` endpoint. [`state.go`](state.go) fences frames by router instance, connection, bridge identity, and the full Pi runtime identity. Stale identities cannot advance the current connection.

The lifecycle proceeds through disconnected, connect, challenged, authenticated, resuming, and active. Replay loss can enter `needs_reconstruction`; stale and stopped states are explicit. Reconnection is not proof that uncertain commands completed. Process-group mode and [`status.go`](status.go) provide verified ownership/status for lifecycle recovery; they do not make a process name sufficient authority to signal a replacement.

The server seam is [`managed_bridge_server.go`](../transport/managed_bridge_server.go), routed by [`router.go`](../transport/router.go). [`store/managed_bridge.go`](../store/managed_bridge.go) persists reservations, runtime binding, command/event ledgers and cursors. Durable admission precedes acknowledgement. Duplicate identity/content is checked before replay; conflicting sequence/content, stale fences, and missing replay evidence do not silently become success.

This private socket uses mutual proof and rejects Origin-bearing requests. It is separate from the public managed browser-control surface, whose permissive non-credentialed CORS does not authorize by Origin. Do not copy the private socket policy onto managed HTTP/browser routes.

## Bounds and retention

- Opaque command/event payloads: at most 32 KiB.
- Default command cursor duplicate window and event outbox: 128 entries each. `PruneThrough` lets a durable replay owner advance the retained floor explicitly.
- Default stderr capture: 16 KiB. Configuration, stdout lines, arguments, environment and queues have explicit limits in `config.go`.
- Reconnect defaults: 100 ms initial delay, bounded by 5 seconds.
- Durable bridge ledger limits: 24 hours, 50,000 rows, 64 MiB, enforced by the store. Required live replay data can block admission at capacity rather than be silently discarded.

Token/verifier derivation stays off wire types. Frame formatting and package errors omit proof/payload values. Do not log raw configuration, token, proof, or event data as diagnostics.

## Existing evidence and limits

[`managedbridge_test.go`](managedbridge_test.go), [`phase1_test.go`](phase1_test.go), and [`phase3b_test.go`](phase3b_test.go) exercise protocol/client/process seams. [`managed_bridge_server_functional_test.go`](../transport/managed_bridge_server_functional_test.go) composes client, WebSocket server and SQLite, including replay after interrupted admission. [`managed_bridge_test.go`](../store/managed_bridge_test.go) exercises the durable ledger.

These files are evidence locations, not a passing-run report or complete browser/runtime acceptance. See the [Draft runtime contract backfill](../../../docs/specs/managed-runtime-contracts.spec.md#scenario-mr-011-bridge-reconnect-and-bounded-replay) and [retirement crosswalk](../../../docs/retirement-crosswalk.md) for remaining acceptance gaps. No runtime behavior or enablement changed with this documentation update.
