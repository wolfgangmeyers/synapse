# Managed RPC bridge (Phase 0)

This package is a disabled contract for a future router-to-Pi managed RPC
bridge. It defines the versioned JSON frames, mutual proof helpers, complete
runtime fence, lifecycle state machine, and bounded command/event cursors.

Phase 0 has no network, WebSocket, process launch, runtime configuration,
feature flag, proxy wiring, manifest wiring, or database state. Existing direct
RPC remains the only runtime path.

Opaque command and event payloads are bounded to 32 KiB. Cursor duplicate
identity is retained in a fixed 128-entry window by default. `PruneThrough`
allows a durable replay owner to move that retention floor explicitly. Durable
admission runs before a new sequence advances or produces an acknowledgement.

The token is an input to verifier derivation only. Tokens and verifiers are
absent from wire types. Proofs and payloads are wire fields, but all frame
String/GoString methods and package errors omit their values.
