# Phase 1 Specification: Prove Pi Delivery and Freeze the Protocol

## Scope and isolation

Phase 1 produces evidence and test fixtures only. It does not create a router, install an extension, alter Pi configuration, write live mailbox/reminder state, start a LaunchAgent, or touch tmux. Every external Pi process uses a fresh temporary `PI_CODING_AGENT_DIR` and `--session-dir`; its extension and evidence files live under that same temporary test root.

The installed primary source under test is:

- package: `/Users/wolfgang.meyers/.nvm/versions/node/v26.2.0/lib/node_modules/@earendil-works/pi-coding-agent`
- version: `0.80.6`
- `package.json` SHA-256: `5f805b25823010b36e801735f07e57d831f0a474f95dca24ab90bca6e27e0aa0`
- `dist/core/agent-session.js` SHA-256: `b2b3b0e2b95ff88290232da4117920a6fd0cceb06bb5d66bb8f120fc934644e7`
- `dist/core/session-manager.js` SHA-256: `879e80cc6e2371e4b06887e6fb041c323ba4e86f7687bfdac6474c9f61486112`
- `docs/extensions.md` SHA-256: `82ea7eee960ade20310fc96e51d344042588d5e4319f2be29aeb76c65c14fb09`
- `docs/session-format.md` SHA-256: `8c9b05fc65ee5539cfdd14f250a0d153d2a0594490bb3792917271b237264ed5`

## Delivery and acknowledgement contract

A routed custom message embeds its immutable router ID in both model-readable content and extension `details`. The extension always sends it with `display: true`, `deliverAs: "steer"`, and `triggerTurn: true`.

Phase 1 tested a candidate acknowledgement rule: a correlated `message_end` followed by `ctx.sessionManager.getEntries()` finding the matching `custom_message`. It is **rejected for Pi 0.80.6**. A deterministic crash after both signals but before the first assistant entry produces no session file, and the replacement process cannot find the routed ID. The history lookup was in-memory, not a persisted-session guarantee.

`pi.sendMessage()` returning, a status-line preview, `message_end`, and the candidate lookup are never acknowledgement on this version. Phase 2 remains blocked until a different documented durable boundary is proposed and passes a new isolated crash/restart proof; this specification does not prescribe a replacement acknowledgement behavior.

## Required live evidence

A minimal disposable extension and controllable local test provider must drive real isolated Pi external processes, not mocks or static source-order tests. It must record only IDs, event types, delivery mode, lifecycle reason, and session-entry types—never normal message bodies.

The suite must prove separately:

- **idle:** visible custom delivery, `triggerTurn`, model-context inclusion, correlated `message_end`, positive history lookup, and unchanged bottom editor in a real PTY/TUI;
- **busy:** the busy routed ID is absent from the first provider context, present at the next boundary, correlated through `message_end` and history, and leaves the real PTY/TUI editor unchanged;
- restart/resume proves whether the observed ID is durable before reinjection;
- new-session and fork semantics distinguish an unrelated new branch from a forked source branch;
- reload and every shutdown reason (`quit`, `reload`, `new`, `resume`, `fork`) release test resources in a real isolated external process;
- crash after the candidate signals demonstrates whether a replacement can deduplicate visibly;
- `--no-session` has no durable history lookup and is reported as diagnostic at-least-once delivery only.

Installed source/declaration tests may guard API shapes and ordering, but cannot replace any live acceptance case.

## Protocol freeze

`protocol/v1/` will contain a JSON Schema plus valid and rejected JSON fixtures. It defines exactly these envelope fields and no others:

| Field | Required | Constraint |
|---|---:|---|
| `id` | yes | producer/server idempotency ID; non-empty bounded ASCII identifier |
| `from` | yes | non-empty claimed local sender/service name or normalized allowed email address, bounded to 128 characters |
| `to` | yes | bounded agent ID, not an email address |
| `subject` | no | string, maximum 256 characters |
| `body` | yes | string; default-producer policy maximum 4 KiB UTF-8; explicitly configured local producer maximum 64 KiB UTF-8 |
| `created_at` | yes | timezone-aware UTC timestamp with millisecond precision (`Z`) |

Separate client-to-router and router-to-client schemas cover all v1 client frames (`register`, `lease_renew`, `ack`, `nack`, `ping`) and server frames (`registered`, `message`, `lease_rejected`, `error`, `pong`). Every frame carries `version` and `frame_id`; `message` carries the complete envelope and an attempt; structured errors carry `code`, `message`, optional `field`, and a frame correlation ID.

The rejection fixtures cover each missing required envelope field and every required field of every client and server frame; each frame type has a version-mismatch rejection, including server frames. They also cover unknown envelope and direction-specific frame fields, UTC failures, identifier min/max/character bounds, email recipient IDs, exact and over UTF-8-byte body boundaries for the 4 KiB default and 64 KiB configured-producer policies, duplicate IDs with identical payload (idempotent) and changed payload (conflict), and wrong-direction frames. AJV validates structure; `Buffer.byteLength` separately enforces the 4 KiB UTF-8 transport limit. Phase 1 introduces no handwritten schema validator.

## Router-stack decision criteria

The decision log compares Python and Node from repository and runtime evidence: existing dependencies, maintained HTTP/WebSocket server and client support, schema-validation availability, operational fit with the future Pi TypeScript extension, install/package cost, and test isolation. Probe language is not a selection criterion. Handwritten WebSocket framing is prohibited. The chosen stack pins only its maintained WebSocket and schema-validation dependencies, after selection.

## Go/no-go

Current recommendation: **hard no-go.** The isolated live crash/restart test proves the candidate event-plus-history rule cannot survive the required persistence boundary on Pi 0.80.6. Phase 2 is blocked until a different documented acknowledgement boundary is proposed and passes a new crash/restart proof.
