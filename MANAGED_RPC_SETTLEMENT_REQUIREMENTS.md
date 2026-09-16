# Managed RPC Settlement Requirements

## Goal

Fix direct-pipe managed workers that stay falsely busy after a turn ends. The activity indicator must stop, and the worker must accept the next message. These requirements are authoritative for the implementation and its tests.

## Acceptance

- Terminal success settles exactly once and accepts the next message.
- Terminal error settles exactly once and accepts the next message.
- Cancellation settles exactly once and accepts the next message.
- A failed root-control path settles exactly once and accepts the next message.
- A completed ordinary tool path settles exactly once and accepts the next message.
- A failed ordinary tool path settles exactly once and accepts the next message.
- Settlement waits while owned work remains.
- Direct stdin/stdout RPC remains the transport.
- Durable pending messages remain retained.
- The UI activity state reflects durable state.

## Out of scope

- Router or Pi process survival.
- A detached bridge.
- A replacement transport.
- WebSocket or helper architecture.
- Load or scaling work. The 129-event test only proves that unrelated lifecycle events do not block message receipts.

## Tests

Add functional composed/direct RPC coverage. Each terminal outcome must prove that the next input is accepted.
