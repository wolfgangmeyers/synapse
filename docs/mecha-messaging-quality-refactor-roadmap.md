# Mecha Messaging quality refactor roadmap

This is the replacement for the missing quality-refactor audit. It is a behavior-preserving extraction roadmap for `pi-extensions/mecha-messaging/index.ts`. The file is still the composition root for mailbox delivery, Pi lifecycle events, compaction, history cleanup, diagnostics, and model/control projections.

The goal is to reduce the size and coupling of that composition root without changing the Mailbox V2 protocol, Pi event behavior, public exports, or the data shown to the router and its clients.

## Update status

The scoped lifecycle refactor was completed in lifecycle commit `cbbeedcb16902457fa8c21241c452201a21a7e8d`, with tree `ed2dd944b08186a02df90b8d02fac1fff4de4aa8`. This status is checked against extraction base commit `0cdcaf74f54dc961abd16aa3be2fec8c433647d1` and base tree `a8101f1c45b7385f7a0694cd8b25c56c2b0df39e`.

Validation evidence:

- 23 focused lifecycle, differential, native-Pi, package-discovery, and export tests passed.
- The isolated native-Pi E2E passed 1/1 with TSX, temporary module-resolution, router SQLite, and session paths, a packaged Wolfgang fork native `19.2.2` artifact from source commit `3be57...`, and installed Pi `0.84.3`.
- Typecheck, lint, generated mailbox-client drift checks, and `git diff --check` passed.
- The differential comparator and representative lifecycle mutation assertions passed. This is mutation coverage for the stated assertions, not a complete mutation-testing run.
- Full-suite validation, aggregate coverage, and independent review have not been completed. These results do not claim final review or commit.

## Root public exports

The Messaging root publicly exports these delivery-coordinator functions:

- `routeMailboxEnvelope`
- `mailboxSessionDeliveryState`
- `spawnNativeSentinel`
- `captureNativeSentinelSpawner`
- `createPiMailboxDeliveryCoordinator`

Coordinator implementation types such as `MailboxMessage`, `MailboxDelivery`, `SessionManager`, `MailboxPi`, `MailboxClient`, and `SpawnSubagent` remain module-local. `DeliveryCoordinator` and `DeliveryCoordinatorOptions` are type exports of the extracted module for its implementation boundary; they are not re-exported by the Messaging root.

## Completed slices

### Goal extraction

`pi-extensions/mecha-goal/index.ts` now owns the Goal state machine:

- Goal state, outcome counts, lock, snooze, wakeups, and evaluator calls.
- Goal tools and commands.
- Goal persistence and restoration.
- Goal control request handling and Goal state events.
- Compaction-aware Goal scheduling.

Messaging still owns the thin bridge around that package. It receives Goal projections, emits meaningful activity, publishes the current Goal state through the mailbox client, and forwards remote controls to the Goal event bus. That bridge is part of the Messaging integration contract. It is not a second Goal implementation.

The extraction preserved these contracts:

- Loading Goal and Messaging as separate Pi packages registers the complete Goal surface once. Messaging registers no Goal tools or commands.
- The Goal event names remain stable: `goal:state`, `goal:state-request`, `goal:meaningful-activity`, `goal:worker-id`, `goal:control-request`, and `goal:control-state`.
- A control request has a bounded response wait and returns a normalized full state when the Goal handler is unavailable or returns malformed data.
- Meaningful activity is emitted only for successful, identified real-tool completions and native subagent launches. Mailbox and control-plane work does not count as progress.

Evidence: commit `9d76e13a6` (`Split Goal into its own Pi package`) and `tests/goal_extraction_parity.test.ts`, `tests/mecha_messaging_goal_bridge.test.ts`, and `tests/mecha_messaging_package.test.ts`.

### Compaction policy extraction

`pi-extensions/mecha-messaging/compaction-policy.ts` now owns policy data and pure policy decisions:

- Default levels, context-size tiers, and model-specific overrides.
- Environment parsing and legacy comma-separated input.
- Policy normalization and validation.
- Model-key matching and tier selection.
- Advisory and mandatory threshold selection.
- Threshold and post-compaction message formatting.

Messaging still owns the Pi lifecycle around those decisions: pausing delivery, waiting for an idle boundary, aborting a run for a hard threshold, calling `context.compact`, and publishing the result.

The extraction preserved these contracts:

- Default ordinary levels remain 40/50/60/70/80/90, with the 90 percent level mandatory.
- Large-context and Opus policies keep their existing tiers and thresholds.
- Model-specific policy wins in the existing normalized key order. `MECHA_PI_MODEL_KEY` remains the fallback.
- Invalid policies keep the existing validation failures.
- Handoff notes are trimmed and bounded. The compaction summary itself is not copied into the user-visible handoff message.
- The Messaging package continues to expose the existing policy helpers.

Evidence: commit `2b27be8b0` (`Extract compaction policy module`) and `tests/mecha_messaging_compaction_policy.test.ts`.

## Completed and validated locally

### Delivery coordinator

The delivery coordinator is implemented locally in `pi-extensions/mecha-messaging/delivery-coordinator.ts` and wired from `index.ts`. The coordinator tests and isolated E2E validation listed below passed.

**Behavior and contracts to preserve.**

- `mailboxSessionDeliveryState` must distinguish `absent`, `history_unverified`, and `durable`. A branch entry alone is not enough when the session is not persisted or the session file cannot be verified.
- A normal message is injected with `deliverAs: "steer"` and `triggerTurn: true`.
- The coordinator sends `injection_requested` after the local injection. It sends `ack` only after the matching custom message and delivery items are durable in the session file.
- Checkpoint failures keep the delivery pending and retry after the existing delay. They must not turn a queued Pi message into a false negative acknowledgment.
- Replayed attempts do not duplicate an already durable message. A new attempt can be reinjected only after the existing pending-delivery delay and only when the prior attempt is still absent.
- Session identity checks prevent a late event from one Pi session from acknowledging another session's delivery.
- Sentinel messages are handed to one native subagent per message ID. A failed handoff sends `nack` with the existing reason. A successful handoff records a non-display status and acknowledges the delivery without exposing the scheduled prompt.
- Arrival indicators remain best-effort UI hints. They do not provide delivery durability.
- `close()` cancels checkpoint timers and clears pending, arrival, sentinel, and persistence-error state.

**Risks.** The important ordering is spread across the router lease, the Pi session branch, the persisted JSONL file, and `message_end`. A seemingly harmless reorder can acknowledge a message before it can be recovered, reinject a message into the wrong session, or create a second native child. Keep delivery-item identity, attempt numbers, and timer injection points unchanged.

**Tests.** Keep the direct coordinator and state tests in `tests/mailbox_v2_delivery.test.ts`. Keep sentinel coverage in `tests/sentinel_native_subagent.test.ts` and the protocol/client tests that assert receipt fields. Add a small module-level matrix for: durable duplicate, unpersisted history, unreadable session file, checkpoint retry, new-attempt reinjection, receipt-send failure, close during retry, and duplicate sentinel delivery.

**Isolated E2E validation.** Use a temporary SQLite router, a temporary session file, a fake Pi `sendMessage`, and a real WebSocket mailbox client. Enqueue a message, deliver it, append the custom session entry, and verify the receipt order. Restart the router and replay the same message. Verify that the durable entry is acknowledged without a second Pi injection. Repeat with a failed checkpoint and with a sentinel whose spawn succeeds and fails. No live router, settings file, or service is needed.

## Remaining slices

Each slice should move existing code with the smallest possible adapter. Keep the current exports until callers and tests have moved. Do not combine an extraction with a protocol change, a timing change, or a new feature.

### 1. Activity lifecycle — completed locally

**Status and evidence.** The approved boundary is implemented in the current working tree. `pi-extensions/mecha-messaging/activity-lifecycle.ts` owns the settled-notification scheduler, owner-scoped native service reads, native child tracking and tombstones, lifecycle snapshot reconciliation, activity heartbeat, and activity projection assembly. `index.ts` remains the composition root: it keeps Pi event registration, Goal activity, mailbox pause state, compaction orchestration, model and diagnostics behavior, and the existing root adapters. The extraction does not add a lifecycle publication path: typed lifecycle frames still come from heartbeat polling.

The differential harness used the pre-extraction TypeScript baseline and passed with no unlisted mismatch. The focused lifecycle, differential, native-Pi, package-discovery, and export tests passed: 23 passed, 0 failed, 0 skipped. The isolated native-Pi E2E passed 1/1 using TSX, temporary module-resolution, router SQLite, and session paths, the packaged Wolfgang fork native `19.2.2` artifact at source commit `3be57...`, and installed Pi `@earendil-works/pi-coding-agent` `0.84.3`. The differential comparator and representative lifecycle mutation assertions passed. Full-suite validation, aggregate coverage, complete mutation testing, and independent review remain outstanding. The implementation is recorded in lifecycle commit `cbbeedcb16902457fa8c21241c452201a21a7e8d`.

**Scope.** Extract the settled-notification scheduler, native subagent tracking, lifecycle snapshot reconciliation, activity heartbeat, and activity projection assembly. The Goal meaningful-activity adapter can remain in the composition root until its dependency on the Goal package is explicitly separated.

**Behavior and contracts to preserve.**

- Visible activity state is `compacting` during compaction. When it is not compacting, Pi's live `isIdle()` value wins when available; the heartbeat latch is the older-context fallback.
- Started and terminal native-subagent events update the local projection, while the lifecycle snapshot service remains the polling source of truth when it is available.
- Polling continues after the parent settles so missed lifecycle events can be repaired. The existing heartbeat and refresh bounds remain in force.
- Public subagent summaries contain only bounded IDs, type, description, and status. Prompts, output, results, errors, and internal timestamps stay out of router-facing payloads.
- The 100-subagent projection bound and the bounded terminal tombstone set remain in place.
- Terminal events remove live children. Tombstones prevent a late start from reviving an old child ID, while a genuinely newer start can reuse that ID.
- Legacy service shapes keep their existing compatibility behavior. A missing or failing native service produces a retryable operational signal and does not break normal activity publication.
- Settled notifications remain separate from lifecycle publication. Child activity suppresses the parent settled notification until the existing cooldown and disposal rules allow it.
- Activity publication remains best-effort and must not affect mailbox delivery or the Goal action result.

**Risks.** This area has several time-based sources: Pi events, native lifecycle events, snapshot polling, queued timers, and compaction boundaries. Duplicate starts, unmatched terminal events, stale snapshots, and a reused child ID can all produce misleading UI state. Do not make event handlers publish lifecycle state if the current polling contract says only the timer may do so.

**Tests.** The focused scheduler, projection, differential, native-Pi, package-discovery, export, settled-notification, activity-frame, and redacted-trace coverage passed as part of the 23-test focused run. Router compatibility coverage also passed in the JavaScript compatibility suite. The completed tests cover snapshot replacement, old-service fallback, tombstone reuse, missing-event expiry, bounds, and compaction masking.

**Isolated E2E validation.** This validation passed once against a temporary router with a fake native service supporting lifecycle snapshots and older list methods. It covered start, terminal, missed-event, snapshot-replacement, parent-settled, controlled-timer, WebSocket frame, child-count, ordering, redaction, router-restart, and compaction-mask behavior.

### 2. Compaction orchestration — completed locally

**Status and completion evidence.** Commit `a63bb49203a670c0d6b5dc0531621f5fe35bb04c` (tree `cee1e5c7a78f04125a1b21d537ef2f8ca65f6f93`) adds `pi-extensions/mecha-messaging/compaction-orchestration.ts`, which now owns the compaction request slots, logical mailbox pause, policy state, threshold watermark, capture boundary, hard-compaction guard, and post-compaction handoff. `index.ts` remains the composition root and keeps Pi event registration, client/coordinator ownership, settled-notification ordering, and shutdown ordering.

The actual focused compaction command passed 24 tests: 15 direct orchestration tests, 4 differential-harness tests, 1 isolated fake-Pi E2E test, and 4 installed-Pi tests. The fake-Pi E2E covered seven scenarios: self-compaction success, non-idle self-compaction, child-armed self-compaction, hard-threshold handling, capture with a successful tool result, mailbox gating while logically paused, and shutdown cleanup. It did not exercise advisory-threshold notification, an active Pi stream, or a cancellation-insensitive tool. Advisory-threshold notification is covered by the direct orchestration tests. Active-stream compaction and the cancellation-insensitive tool are covered only by the optional installed-Pi tests when an installed package is available. Plain `npm test` exited successfully: 432 package tests passed with 3 expected installed-Pi skips, and 391 JavaScript tests passed with 2 unrelated environment skips. With `MECHA_PI_PACKAGE_ROOT` explicitly set to the installed package, all four tests in `tests/pi_hard_compaction_e2e.test.ts` passed. With no installed package, the three runtime-dependent tests skipped; the unrelated router-startup test still ran. A configured missing or wrong-identity package root failed those three runtime-dependent tests; it did not skip.

Typecheck, Mecha Messaging lint, and `git diff --check` passed. A 29-mutation proof also passed against temporary copies of the extracted module and root fixture. All 29 mutations were killed, including self-compaction request and completion state, hard-compaction guards and callbacks, logical pause and capture/handoff behavior, policy and model forwarding, root tool/event ordering, and shutdown ownership and ordering. Each temporary mutation was reverted before the next one; the temporary directory was removed. No mutation runner, permanent mutation dependency, or mutated source was left in the repository. This proof does not claim live-router, provider, or native-Pi behavior beyond the separate installed-Pi test run.

**Scope.** Extract the lifecycle and request code around `pauseMailboxForCompaction`, `resumeMailboxAfterCompaction`, `requestSelfCompaction`, `runPendingSelfCompaction`, `enforceHardCompaction`, `monitorContextThreshold`, and post-compaction handoff publication. Import the already-extracted policy module. Do not move policy parsing back into this module.

**Behavior and contracts to preserve.**

- A parent mailbox pauses before capture and compaction. It publishes `compacting` and does not accept ordinary mailbox delivery until the compaction lifecycle resumes.
- Self-compaction waits for Pi's idle boundary. Hard compaction aborts the current run, then compacts after Pi reaches the boundary. A provider or tool that ignores cancellation can delay that boundary.
- Child sessions do not pause or run parent capture logic.
- Capture receives the messages Pi selected for summarization plus the turn-prefix messages. Capture failures are contained and do not corrupt the compaction result.
- Successful completion resets threshold notification state, resumes the mailbox, and schedules the bounded handoff note. A handoff-send failure does not convert successful compaction into a failed tool call.
- Failed or unavailable compaction clears the pending request, resumes the mailbox where possible, and keeps the existing user-visible error behavior.
- Mandatory thresholds keep aborting or compacting according to the current policy. Advisory thresholds publish one notification per crossed level.
- The status response continues to expose usage, selected tier, matched model key, notification progress, and configuration errors.

**Risks.** Compaction is the highest-coupling extraction. It touches delivery, activity, memory capture, Pi abort semantics, event ordering, and user-visible messages. The dangerous cases are cancellation before `session_compact`, a callback that fires after a newer request, an unavailable `compact` function, and a tool that never observes its abort signal. Preserve the pending-request identity checks and callback behavior before simplifying anything.

**Tests.** `tests/mecha_messaging_compaction_policy.test.ts` remains the pure policy suite. `tests/pi_hard_compaction_e2e.test.ts` optionally covers installed-Pi stream abort, hard-guard ordering, and a cancellation-insensitive tool when an installed package is available. `tests/mecha_messaging_compaction_orchestration.test.ts` covers the extracted state machine. `tests/mecha_messaging_compaction_orchestration_differential.test.ts` compares ordered baseline behavior. `tests/mecha_messaging_compaction_orchestration_e2e.test.ts` covers the isolated fake-Pi path. `tests/memory_capture_e2e.test.ts` and `tests/memory_capture_e2e.test.mjs` retain capture deadline, source-bound, and apply coverage.

**Isolated E2E validation.** The extraction E2E uses a child process, temporary HOME, Pi settings, project, session, SQLite, memory, and fake-`pi` paths. It does not require a router, network, credentials, or installed Pi. It covers self-compaction success, non-idle and child-armed self-compaction, hard-threshold handling, capture with a successful tool result, logical mailbox gating, and shutdown cleanup. It does not cover advisory-threshold notification, an active Pi stream, or a cancellation-insensitive tool. Advisory-threshold notification is covered by the direct orchestration tests. The separate installed-Pi tests optionally cover active-stream compaction and the cancellation-insensitive tool when the package is available; missing packages skip for CI compatibility, while configured or discovered invalid package roots fail.

### 3. Diagnostics and history sanitation

**Scope.** Extract the bounded JSONL diagnostic writers, native trace field filtering, malformed tool-call quarantine, orphan tool-result quarantine, and their context-event adapters. Keep the context hook in `index.ts` until the extracted module has a clear return contract.

**Behavior and contracts to preserve.**

- Invalid tool-call names are removed before a provider request. Orphan tool results are removed when no earlier valid assistant tool call owns them.
- Provider tool-call IDs continue to use the existing provider-suffix normalization. Error and aborted assistant messages do not create valid ownership.
- The sanitized message list preserves all valid messages and their order. A clean input returns the original list shape.
- Diagnostics are deduplicated per session and call identity, bounded by record and file limits, and best-effort. A diagnostic failure never changes provider flow.
- Diagnostic records contain operational metadata only. They do not include source message bodies, prompts, tool output, error details, or capture evidence that belongs in the memory system.
- Native trace fields remain allowlisted by type and length. Lifecycle snapshots remain redacted and bounded.
- Memory-capture diagnostics retain their existing trigger, status, reason, record-count, rotation, and file-permission behavior.

**Risks.** Sanitation changes the provider input, so object identity, array order, tool-call ownership, and “valid but unusual” content matter. The diagnostic path is also a privacy boundary. Do not spread raw event objects into logs, and do not make a best-effort logger capable of throwing into the context handler.

**Tests.** Keep quarantine and diagnostic assertions in `tests/mailbox_v2_delivery.test.ts` and `tests/mecha_messaging_package.test.ts`. Add a focused table for null blocks, `tool_use` aliases, invalid names, provider-suffixed IDs, orphan results after errors, later matching calls, repeated context events, file rotation, malformed metadata, and secret text exclusion. Keep memory-capture validation in `tests/memory_capture.test.ts` and its E2E tests.

**Isolated E2E validation.** Feed a fake installed-Pi context handler an adversarial history and capture the exact messages passed to the fake provider. Use a temporary `HOME` and diagnostic paths. Assert that invalid calls and orphan results disappear, valid history keeps its order, repeated events produce one diagnostic, files stay within their byte bounds, and sensitive source text does not appear. Run the same check with diagnostics unavailable and verify the provider request still completes.

### 4. Model and control projection

**Scope.** Extract model metadata normalization, catalog projection, current-model projection, model selection and switching, the short-lived post-switch override, model-control result normalization, and the adjacent Goal control bridge adapters. Keep the Goal state machine in `mecha-goal` and keep wire serialization in `mailbox-client.ts`.

**Behavior and contracts to preserve.**

- Provider and model IDs are required for a usable model projection. Optional names and numeric limits are retained only when valid and bounded.
- Malformed registry entries are ignored. The current model is `null` when it cannot be projected. The catalog remains bounded at the current maximum and reports truncation through the mailbox client.
- A model switch checks the registry before calling `pi.setModel`. It preserves the existing error codes for invalid input, unavailable registry, unavailable model, rejected switch, and failed switch.
- A Pi implementation that mutates the model and throws afterward is recognized when the post-call projection confirms the requested model.
- The short-lived override publishes a successful switch before a fresh Pi context snapshot arrives. It clears when the context changes, the native projection reflects the switch, or the model changes locally.
- Remote control responses are matched to the requested control and target. Control-plane state cannot be confused with ordinary activity or Goal state.
- Goal controls retain the one-second response bound, full-state fallback, revision checks, and separate typed event channel.

**Risks.** This slice crosses the Pi model registry, mailbox-client frames, router control fencing, UI projections, and the Goal bridge. The main failure is returning an optimistic model forever or accepting a model that the registry did not provide. Keep identity comparisons on provider plus ID, and keep wire contracts in the client layer.

**Tests.** Keep model projection and extension tests in `tests/pi_extension_standalone.test.ts` and `tests/mecha_messaging_package.test.ts`. Keep client and router control coverage in `tests/model_catalog_transport.test.mjs`, `tests/mailbox_client_goal_control.test.mjs`, `tests/router_web_ui.test.mjs`, and `tests/goal_transport.test.ts`. Add direct tests for malformed catalogs, one-thousand-item bounds, throw-after-mutation, stale context clearing, control timeout, and cross-target response rejection.

**Isolated E2E validation.** Use a temporary router and WebSocket clients, a fake Pi registry with malformed and valid entries, and a fake `setModel`. Verify catalog frames, activity projection, a successful switch, a rejected switch, a switch that throws after mutation, reconnect behavior, and a Goal control request through the EventBus. Assert the exact provider/ID projection and error code. Do not use the live router or browser settings.

## Recommended sequencing

1. **Activity lifecycle.** Complete and validated in the current working tree. The delivery coordinator and activity lifecycle now have separate, documented boundaries.
2. **Compaction orchestration.** It depends on the delivery and activity boundaries and has the most timing risk. Extract it after those interfaces are explicit.
3. **Diagnostics and history sanitation.** It is mostly a pure transformation and a best-effort sink. Doing it after the timing work keeps provider-input changes separate from lifecycle changes.
4. **Model and control projection.** It has the broadest public surface across Pi, the router, the web UI, and Goal. Leave it until the other composition-root state has clearer ownership.

For every slice: move code without changing behavior, add or preserve module-level tests, run package typecheck and lint, run the relevant isolated E2E, then run the full Mecha Messaging test command. Compare the public export list and protocol fixtures before and after. Stop if a diff changes a wire field, event name, timing bound, or error code without a separate product decision.

## Documentation standard for future extractions

Every extracted module needs short comments for behavior that a reader could accidentally “simplify.” Comments should explain the reason and the contract:

- Why an acknowledgment waits for a durable session checkpoint.
- Why compaction masks ordinary activity and why polling, not an event, owns a snapshot.
- Why a projection is redacted or bounded.
- Why a callback checks request identity before changing state.
- Why a diagnostic writer drops fields even when the source object contains them.

Put the comment beside the non-obvious branch or boundary. State the external behavior it protects and the failure edge it covers. Do not restate the next line of code. Keep rejected alternatives in the roadmap or a durable decision note when the reason will matter during a later extraction.

An extraction note should name the source symbols, the new owner, preserved contracts, explicit exclusions, tests, isolated E2E setup, and sequencing dependencies. Use symbol names and commit IDs as anchors. Line numbers will move.
