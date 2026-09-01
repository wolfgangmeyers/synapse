# Mecha Messaging quality refactor roadmap

This is the replacement for the missing quality-refactor audit. It is a behavior-preserving extraction roadmap for `pi-extensions/mecha-messaging/index.ts`. The file is still the composition root for mailbox delivery, Pi lifecycle events, compaction, history cleanup, diagnostics, and model/control projections.

The goal is to reduce the size and coupling of that composition root without changing the Mailbox V2 protocol, Pi event behavior, public exports, or the data shown to the router and its clients.

## Audit status at current base

The current base includes test commit `0f9b48b7488d8cb23907317a53fd700eb9cf5ea9`, tree `f778050a00582b55fe555188076ac604d96629e8`. Completion records below cite earlier immutable commits and trees unless they identify the current scoped validation explicitly.

The current working tree has an uncommitted Mecha Messaging diff. It adds delivery retry and Sentinel handoff fencing, lifecycle bounds and start/terminal race handling, typed snapshot handling across compaction, and the parent-only failed-compaction resume path. The changed source and test files are outside the documentation changes listed below.

Final validation is complete. Validation passed with 350 TypeScript tests, 137 JavaScript tests, and 176 Android tests. Six installed-Pi E2Es passed on Pi `0.84.3`. Strict coverage for the extracted activity-lifecycle module was 100%; this does not claim complete coverage of the composition-root wiring in `index.ts`. TypeScript, lint, checks, generated-client drift, and `git diff --check` passed. Delivery mutation proof passed 51/51 against the 47-case baseline. Lifecycle mutation proof passed 22/22 against the 51-case baseline. Delivery E2E, lifecycle E2E, and source final review are approved. The final diff was read back and matched the scoped changes. The remaining scoped changes are still uncommitted.

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

### Delivery coordinator — current working-tree corrections with scoped validation complete

The immutable delivery extraction is recorded in the earlier extraction history. The current uncommitted diff in `pi-extensions/mecha-messaging/delivery-coordinator.ts` adds two retry corrections: a persisted earlier attempt can acknowledge a later retry without reinjecting it, and a pending delivery is reinjected only after the retry delay when the persisted entry is still absent. Sentinel handoffs now clear completed in-flight state, fence continuations after close, and leave later retries eligible after a lost handoff or acknowledgment.

The current-working-tree tests cover these paths. Final validation passed: delivery mutation proof 51/51 against the 47-case baseline, six installed-Pi E2Es on Pi `0.84.3`, and the focused coverage and repository checks recorded in the audit status. Delivery E2E and source final review are approved. The scoped delivery changes remain uncommitted.

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

**Tests.** Current focused coverage is in `tests/sentinel_native_subagent.test.ts` and the coordinator assertions in the current Messaging test suite. It covers durable retry identity, unverified history, checkpoint retry, delayed reinjection, receipt failures, close fencing, and duplicate Sentinel delivery.

**E2E status.** The current delivery E2E is approved. It verifies persisted retry behavior, receipt ordering, router restart replay, checkpoint failure, and successful and failed Sentinel handoffs without using a shared router or session.

## Completed and pushed through `dee834077`

### Model and control projection — historical immutable evidence

**Status and evidence.** The extraction is complete in historical commit `dee8340775886b77aa39ae9360c5ee6564c168a4`. The final correction for an asynchronous `setModel` rejection after the active context changes was performed in commit `66b78d0a4ca340fe9639481ae49281e1836a54ab`: the failure result now reports the active context's model, matching the baseline catch behavior. The focused direct suite passed 20 tests, the differential suite passed 1 test, and the full Mecha Messaging and repository validation suites passed. Changed-source coverage reached 100 percent for lines, functions, statements, and branches. The isolated installed-Pi E2E passed against the identified installed Pi runtime. Robot Review and independent validation signed off on the final target, and the mutation proof killed every listed mutation.

**Scope.** Extract model metadata normalization, catalog projection, current-model projection, model selection and switching, the short-lived post-switch override, model-control result normalization, and the adjacent Goal control bridge adapters. Keep the Goal state machine in `mecha-goal` and keep wire serialization in `mailbox-client.ts`.

**Behavior and contracts to preserve.**

- Provider and model IDs are required for a usable model projection. Optional names and numeric limits are retained only when valid and bounded.
- Malformed registry entries are ignored. The current model is `null` when it cannot be projected. The catalog remains bounded at the current maximum and reports truncation through the mailbox client.
- A model switch checks the registry before calling `pi.setModel`. It preserves the existing error codes for invalid input, unavailable registry, unavailable model, rejected switch, and failed switch.
- A Pi implementation that mutates the model and throws afterward is recognized when the post-call projection confirms the requested model.
- An asynchronous switch failure that arrives after the active context changes returns the current active context's model in the failure result, matching the baseline catch behavior.
- The short-lived override publishes a successful switch before a fresh Pi context snapshot arrives. It clears when the context changes, the native projection reflects the switch, or the model changes locally.
- Remote control responses are matched to the requested control and target. Control-plane state cannot be confused with ordinary activity or Goal state.
- Goal controls retain the one-second response bound, full-state fallback, revision checks, and separate typed event channel.

**Risks.** This slice crosses the Pi model registry, mailbox-client frames, router control fencing, UI projections, and the Goal bridge. The main failure is returning an optimistic model forever or accepting a model that the registry did not provide. Keep identity comparisons on provider plus ID, and keep wire contracts in the client layer.

**Tests.** `tests/model_control_projection.test.ts` covers malformed catalogs, one-thousand-item bounds, provider-plus-ID identity, throw-after-mutation, stale context clearing, the asynchronous stale-context rejection regression, control timeout, and Goal bridge fallback. `tests/mecha_messaging_model_control_projection_differential.test.ts` compares the extracted extension against the exact baseline fixture. Current-base transport coverage remains in the tests present in the repository; retired router and Web UI test paths are not part of current validation.

**Isolated E2E validation.** This passed against a temporary router and WebSocket clients, a fake Pi registry with malformed and valid entries, and a fake `setModel` in the isolated installed-Pi runtime. It covered catalog and current-model projections, successful, rejected, and throw-after-mutation switches, reconnect behavior, and a Goal control request. It did not use the live router, browser settings, credentials, or a shared session.

## Scoped changes in the current working tree

The current scoped changes have completed validation but remain uncommitted. Each slice should move existing code with the smallest possible adapter. Keep the current exports until callers and tests have moved. Do not combine an extraction with a protocol change, a timing change, or a new feature.

### 1. Activity lifecycle — current fixes with scoped validation complete

**Status and evidence.** The extraction is recorded in immutable commit `cbbeedcb16902457fa8c21241c452201a21a7e8d`, tree `ed2dd944b08186a02df90b8d02fac1fff4de4aa8`. The current working tree adds bounded settled-child state, typed snapshot caps, start/terminal identity fences, compaction-resume reconciliation, and removal of the unsupported `steered` event registration. `pi-extensions/mecha-messaging/activity-lifecycle.ts` remains the lifecycle owner and `index.ts` remains the composition root.

Final validation passed: lifecycle mutation proof 22/22 against the 51-case baseline, six installed-Pi E2Es on Pi `0.84.3`, strict activity-lifecycle module coverage at 100%, and the focused coverage and repository checks recorded in the audit status. This module figure does not represent complete coverage of all composition-root wiring in `index.ts`. Lifecycle E2E and source final review are approved. The scoped lifecycle changes remain uncommitted.

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

**Tests.** Current focused coverage is in `tests/mecha_messaging_activity_lifecycle.test.ts` and `tests/mecha_messaging_activity_lifecycle_differential.test.ts`, with package and compatibility checks in the current test inventory. It covers snapshot replacement, typed and legacy terminal handling, stale-ID reuse, bounds, compaction masking, and startup/shutdown ordering.

**E2E status.** The current lifecycle E2E is approved. The historical native-runtime evidence remains tied to the earlier immutable target.

### 2. Compaction orchestration — current fixes validated; commit pending

**Status and completion evidence.** The extraction is recorded in immutable commit `a63bb49203a670c0d6b5dc0531621f5fe35bb04c`, tree `cee1e5c7a78f04125a1b21d537ef2f8ca65f6f93`. The current working tree adds the parent-only `session_compact_failed` recovery path. It resumes activity without resetting the advisory watermark or changing pending self or hard requests. Final validation is complete; the scoped compaction change is still awaiting its commit.

Historical focused compaction evidence remains tied to the immutable commit above. Current final validation includes the failed-compaction recovery assertion, the six installed-Pi E2Es, and the totals and checks recorded in the audit status above. The scoped compaction change remains uncommitted.

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

**Tests.** `tests/mecha_messaging_compaction_policy.test.ts`, `tests/mecha_messaging_compaction_orchestration.test.ts`, and `tests/mecha_messaging_compaction_orchestration_differential.test.ts` remain the direct and differential coverage. Installed-Pi behavior is covered by the six approved E2Es recorded in the audit status. Retired extraction-E2E paths are not current validation inputs.

**E2E status.** The current compaction behavior was included in the six approved installed-Pi E2Es on Pi `0.84.3`, recorded in the audit status above. The scoped compaction change remains uncommitted.

### 3. Diagnostics and history sanitation

**Status and evidence.** The extraction is complete in this committed slice. `diagnostics-history.ts` now owns the sanitation adapter and bounded diagnostic writers. The root calls `sanitizeContextHistory`, keeps Pi's malformed-filtered `event.messages` assignment, and passes the final orphan-filtered list to provider work. The differential fixture records the orphan-file visibility at assignment, so moving the orphan writer ahead of assignment fails.

The focused direct suite passed 15 tests with 100 percent lines, functions, statements, and branches in `/tmp/mecha-diagnostics-history-coverage/coverage-summary.json`. The differential suite passed 2 tests. The focused package run passed 209 tests, including the installed-Pi E2E with `@earendil-works/pi-coding-agent` and `@earendil-works/pi-ai` `0.84.3`. The full Mecha Messaging run passed 451 tests with 3 unrelated installed-Pi compaction skips. Typecheck, lint, generated-client drift, and `git diff --check` passed. A temporary, uncommitted mutation proof killed all 40 direct/root mutations and restored both source files after every mutation; the complete sets and restored hashes are recorded in `docs/mecha-messaging-diagnostics-history-sanitization-plan.md`.

**Scope.** Extract the bounded JSONL diagnostic writers, native trace field filtering, malformed tool-call quarantine, orphan tool-result quarantine, and their context-event adapters. Keep the context hook in `index.ts` as the Pi composition root.

**Behavior and contracts to preserve.**

- Invalid tool-call names are removed before a provider request. Orphan tool results are removed when no earlier valid assistant tool call owns them.
- Provider tool-call IDs continue to use the existing provider-suffix normalization. Error and aborted assistant messages do not create valid ownership.
- The sanitized message list preserves all valid messages and their order. A clean input returns the original list shape.
- Diagnostics are deduplicated per session and call identity, bounded by record and file limits, and best-effort. A diagnostic failure never changes provider flow.
- Diagnostic records contain operational metadata only. They do not include source message bodies, prompts, tool output, error details, or capture evidence that belongs in the memory system.
- Native trace fields remain allowlisted by type and length. Lifecycle snapshots remain redacted and bounded.
- Memory-capture diagnostics retain their existing trigger, status, reason, record-count, rotation, and file-permission behavior.

**Risks.** Sanitation changes the provider input, so object identity, array order, tool-call ownership, and “valid but unusual” content matter. The diagnostic path is also a privacy boundary. Do not spread raw event objects into logs, and do not make a best-effort logger capable of throwing into the context handler.

**Tests.** Keep quarantine and diagnostic assertions in `tests/mecha_messaging_diagnostics_history.test.ts`, `tests/mecha_messaging_diagnostics_history_differential.test.ts`, and `tests/mecha_messaging_package.test.ts`. Add a focused table for null blocks, `tool_use` aliases, invalid names, provider-suffixed IDs, orphan results after errors, later matching calls, repeated context events, file rotation, malformed metadata, and secret text exclusion. Keep memory-capture validation in `tests/memory_capture.test.ts` and the current installed-Pi diagnostics test.

**Isolated E2E validation.** Feed a fake installed-Pi context handler an adversarial history and capture the exact messages passed to the fake provider. Use a temporary `HOME` and diagnostic paths. Assert that invalid calls and orphan results disappear, valid history keeps its order, repeated events produce one diagnostic, files stay within their byte bounds, and sensitive source text does not appear. Run the same check with diagnostics unavailable and verify the provider request still completes.

## Recommended sequencing

1. **Activity lifecycle.** The extraction and current bounds, race, and compaction fixes are validated. The scoped changes remain uncommitted.
2. **Compaction orchestration.** The extraction and current failed-compaction recovery are validated. The scoped changes remain uncommitted.
3. **Diagnostics and history sanitation.** The extraction is complete and its historical evidence remains tied to its immutable commit.
4. **Model and control projection.** The extraction is complete and its historical evidence remains tied to commit `dee8340775886b77aa39ae9360c5ee6564c168a4`.

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
