# Phase 1 decision log

## Installed Pi evidence

The isolated tests target `/Users/wolfgang.meyers/.nvm/versions/node/v26.2.0/lib/node_modules/@earendil-works/pi-coding-agent`, version `0.80.6`.

| Artifact | SHA-256 |
|---|---|
| `package.json` | `5f805b25823010b36e801735f07e57d831f0a474f95dca24ab90bca6e27e0aa0` |
| `dist/core/agent-session.js` | `b2b3b0e2b95ff88290232da4117920a6fd0cceb06bb5d66bb8f120fc934644e7` |
| `dist/core/session-manager.js` | `879e80cc6e2371e4b06887e6fb041c323ba4e86f7687bfdac6474c9f61486112` |
| `docs/extensions.md` | `82ea7eee960ade20310fc96e51d344042588d5e4319f2be29aeb76c65c14fb09` |
| `docs/session-format.md` | `8c9b05fc65ee5539cfdd14f250a0d153d2a0594490bb3792917271b237264ed5` |

The documented `message_end.message` event exposes the custom message, and `ctx.sessionManager.getEntries()` exposes `custom_message` entries. The source confirms the event handler runs before `appendCustomMessageEntry`; the test deliberately performs the lookup on a following event-loop turn.

## Live isolated probe result

`tests/phase1/live-pi.test.mjs` starts real external Pi RPC-mode processes with a fresh temporary `PI_CODING_AGENT_DIR`, `--session-dir`, disposable extension, and a local controllable provider. It writes only event names, IDs, modes, and boolean observations to its temporary evidence file.

It proves in separate runs that an idle message is emitted with `display: true`, `deliverAs: "steer"`, and `triggerTurn: true`; is correlated through `message_end`; reaches the `context` event; and is found by a post-event `getEntries()` lookup. The busy run proves the busy routed ID is absent from the first provider context, present at the next agent-call boundary, correlated through `message_end`, and found by a post-event history lookup. The test harness does not use tmux, a live mailbox, installed extension settings, or any persistent test state.

## Hard no-go: fresh-session crash boundary

The crash test is deterministic: the disposable extension receives the correlated `message_end`, yields one event-loop turn, verifies the routed ID through `getEntries()`, and sends itself `SIGKILL` before the fake provider can finish the first assistant response. It then starts a replacement Pi process against the same temporary session directory.

The first process has a positive history lookup, but Pi has no `.jsonl` session file. The replacement process cannot find the routed ID. This matches `SessionManager._persist()`: before any assistant entry it defers initial file creation. Therefore the required combined event-plus-positive-*persisted*-session lookup does **not** survive the specified crash/restart boundary. `getEntries()` at that point is in-memory branch state, not durable evidence.

Disposition: **no-go. Do not start Phase 2.** A router must not acknowledge on this mechanism. The `--no-session` probe separately confirms only in-memory history and no durable session, so it remains diagnostic at-least-once behavior.

## TUI and lifecycle evidence

`tests/phase1/tui-editor.test.mjs` runs Pi through `/usr/bin/expect`, which supplies a real pseudo-terminal. The disposable extension verifies `ctx.mode === "tui"`, sets a unique editor sentinel through the documented UI API, and observes the same sentinel through documented `ctx.ui.getEditorText()` after idle and busy custom-message delivery and settlement. The test also inspects the captured PTY transcript (without printing it on success) and finds the idle `display: true` custom-message ID. In the separate busy run, the routed ID is absent from the first provider request, present in the second request and Pi context, absent from the PTY transcript before the first assistant response marker, and visible after that marker. This proves the busy custom message is not rendered during the active call and is rendered at the following agent-call boundary. This proves the bottom editor remains unchanged without relying on RPC's documented no-op editor observation.

`tests/phase1/live-lifecycle.test.mjs` runs a real isolated external RPC Pi process and drives documented RPC operations for reload, new session, resume, and clone/fork, then terminates it with `SIGTERM` for quit. The extension allocates one test interval per session runtime and records its release during each `session_shutdown` reason: `reload`, `new`, `resume`, `fork`, and `quit`. The new branch does not contain the source routed ID; resumed and forked source branches do. These are lifecycle/resource facts only, not a durable acknowledgement guarantee.

## Router stack decision

Select **Node 26.2.0** for the future router, but do not implement it unless the acknowledgement blocker is separately cleared. This supersedes the earlier preliminary Python-router recommendation in `harness_spec.md`: it was made before Phase 1 evidence. The evidence is operational, not probe-language-based: Pi already runs on the installed Node runtime; this isolated harness runs its tests there; Python is `3.14.5` with neither `websockets` nor `jsonschema` installed; and the repository had no existing router stack to preserve. Node therefore has the smaller new router-server surface while allowing the Pi extension and router to consume the same JSON fixtures. This is not a requirement to rewrite unrelated migration-facing CLIs and does not imply that Node tests selected Node.

The selected, maintained dependencies are pinned in the isolated Phase 1 harness: `ws@8.21.0` for WebSocket server/client framing, plus `ajv@8.20.0` and `ajv-formats@3.0.1` for Draft 2020-12 schema validation and calendar-date validation. No handwritten WebSocket framing or dependency-free schema validator is introduced. AJV validates the envelope and separate `client-frame.schema.json` and `server-frame.schema.json`; a valid frame is rejected by the opposite direction schema. The rejection-fixture ledger now enumerates omission of every required field for each of the five client and five server frame types, plus a version mismatch for every frame type, including server frames. It also covers every required envelope field, frame identifier bounds, structured errors, unknown fields, identifier min/max/character bounds, duplicate-ID dispositions, and exact/over UTF-8-byte boundaries for both the 4 KiB default and 64 KiB configured-producer policies. The schema carries a separately AJV-validated body-size policy: `default` is exactly 4096 bytes and `configured-producer` is capped at 65536 bytes. JSON Schema `maxLength` remains a character-counted structural guard, so the fixture test uses Node's standard `Buffer.byteLength(..., "utf8")` as the byte-boundary oracle. That is a transport-limit assertion, not a replacement JSON Schema validator; any future implementation must enforce the same trust-boundary check before persistence. The pinned packages support the Phase 1 fixtures only; no production router exists.

## Coverage assessment

`node --test --experimental-test-coverage tests/phase1/*.test.mjs` reports 100% only because this phase has no production source module to instrument; Node does not count the test files as production coverage. The material evidence ledger is therefore behavioral rather than a percentage: idle delivery, busy boundary/context correlation, PTY/TUI rendered-transcript delivery and editor preservation, crash/restart loss, `--no-session`, live reload/new/resume/fork/quit resource cleanup, every required client/server frame-field omission and version mismatch, all other protocol fixture classes, and duplicate-ID dispositions each have named tests. The durable acknowledgement and replay-deduplication guarantee remains explicitly disproven above.
