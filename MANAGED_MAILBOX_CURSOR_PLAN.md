# Managed Mailbox Readiness Backlog Recovery Plan

## Preconditions

- Preserve live `router-log-viewer-phase1`; never restart, terminate, or modify it.
- Leave existing archive/log-viewer checkpoint files and other dirty work untouched.
- Capture compatibility evidence first: router health commit, checkout commit, extension source/version, installed Pi SDK/runtime (`0.85.0`), and literal `get_entries` envelope.
- The backlog-overflow incident and nullable-`leafId` edge case are separate claims; WebUI bundle diagnosis remains a third evidence track.

## 1. Test the read-loop incident before changing it

Extend focused `rpc.go` tests/helpers for `rpcClient.readLoop`:

1. Feed >128 mixed strict-valid Pi 0.85 AgentSessionEvents: exact ordinary names `agent_start`, `agent_end`, `agent_settled`, `turn_start`, `turn_end`, `message_start`, `message_update`, `message_end`, `tool_execution_start`, `tool_execution_update`, `tool_execution_end`, `queue_update`, `compaction_start`, `compaction_end`, `entry_appended`, `session_info_changed`, `thinking_level_changed`, `auto_retry_start`, `auto_retry_end`, `summarization_retry_scheduled`, `summarization_retry_attempt_start`, `summarization_retry_finished`, `bash_execution_update`, and every exact nested message-update assistant variant (`start`, `text_start`, `text_delta`, `text_end`, `thinking_start`, `thinking_delta`, `thinking_end`, `toolcall_start`, `toolcall_delta`, `toolcall_end`, `done`, `error`). Then reusable `get_state`/`get_entries` succeed and backlog is empty.
2. Feed structurally valid unknown `future_event_v1`: 128 remains live; 129 preserves `ErrReadinessBacklogFull` terminalization.
3. Confirm retained readiness receipts exactly `mecha.tools.accepted.v1`, `mecha.tools.rejected.v1`, `mecha.policy.applied.v1`, `mecha.child.snapshot.v1`; exact active/admitted restart `agent_settled`, with otherwise strict-valid settled frames discarded; and `mecha.mailbox.appended.v1` direct/stale matcher behavior.
4. Cover semantic exceptions before discard, malformed-known/nested frames, terminal callback once/all waiters fail, pre-waiter/stale receipt, pre-matcher post-write mailbox receipt, out-of-order concurrent and late response IDs, and readiness serialization or keyed matching. A matching pending ID with malformed or wrong-command response enters the bounded readiness backlog and leaves its waiter armed. An exact-valid late or unknown ID is discarded. A valid matched response routes without incrementing `eventSequence`. Responses use the literal `type:"response"` envelope.

## 2. Implement narrow semantic classification

In `router-proxy/internal/managed/rpc.go:rpcClient.readLoop`, introduce or extend a classifier at the final backlog decision. Preserve the existing precedence: response waiters, root/child/parent semantic handlers, direct mailbox matcher, root results, post-compaction handling, and settlement boundaries retain their existing behavior.

Only after those consumers decline a frame:

- discard only strict-valid exact AgentSessionEvent names and nested message-update variants enumerated in the audit; no prefix or broad-shape discard;
- retain only `mecha.tools.accepted.v1`, `mecha.tools.rejected.v1`, `mecha.policy.applied.v1`, `mecha.child.snapshot.v1`, exact active/admitted `agent_settled`, direct/stale `mecha.mailbox.appended.v1`, and unknown valid forms in bounded backlog;
- route agent_start parent activity, built-in read/bash/edit/write lifecycle, compaction, root/control result, child delta, parent activity, and recognized custom message_end through their established semantic/matcher paths; `message_start` never becomes receipt. Root-control and built-in lifecycle are anti-poison channels: valid records use typed redacted callbacks, and malformed/future candidates are safely discarded without raw readiness-backlog or store retention;
- register each request-ID waiter before writing its request; route only matching literal `{id,type:"response",command,success,...}` IDs. A malformed or wrong-command response for a pending ID remains a bounded alarm and leaves that waiter armed. Exact-valid late or unknown IDs are discarded. Matched responses do not increment `eventSequence`;
- keep evaluator cleanup armed after a malformed abort response until an exact-valid follow-up. An exact rejected abort response has `success:false` and a nonempty error and resolves as a normal RPC failure;
- for readiness/direct receipts that can arrive after command write before later matcher installation, use matcher serialization plus captured watermark and bounded backlog to retain post-watermark receipts and classify <=watermark as stale; use serialization/keyed matching;
- never discard a frame an active waiter/restart/stale classifier consumes.

Add a bounded/redacted transition fact if this incident’s prepare/retry/dead-letter lifecycle paths are reached; do not log frame contents.

### Current incident phase boundary

This phase closes only the bounded RPC incident: source-valid Pi 0.85 ordinary events, strict literal response routing, exact direct matcher ownership, settlement fanout, anti-poison root/builtin channels, and exact compaction/root envelopes. Compaction is closed by variant: successful end permits nested `usage`/`details` in `result` and cannot have top-level `errorMessage`; failed end owns nonempty `errorMessage` and has no `result`; malformed/future candidates become bounded alarms. The RPC serializer subscription is authoritative over later extension enrichment: wire `turn_start` is `{type}` and wire `turn_end` is `{type,message,toolResults}`. A pending-root provisional `agent_settled` is consumed for direct ownership without emitting direct settled or parent idle; matching root result plus final settlement produce the intended direct and parent fanout once each. The typed `RecoveryBoundary`, append-entry persistence, CAS, direct managed-launch proof, Complete Activity evidence, and WebUI router-bundle diagnosis remain required later phases; none is claimed complete by the RPC gate.

## 3. Keep and fix cursor edge behavior separately

Extend `router-proxy/internal/managed/rpc_test.go:TestManagedDirectRecoveryUsesExactPersistedEntry`:

These are required later and remain pending:

- Pi 0.85 empty `{"entries":[],"leafId":null}` maps to private typed `no_prior_entry` without failure;
- nonempty and branched responses use the last append-order entry ID, never active-tree `leafId`;
- malformed envelope/entry cases fail safely;
- append cursor recovery sends only a real entry ID; no-prior/legacy never cursorless-scan and process-loss releases for bounded fresh retry.

The later implementation replaces the string-only `DirectMailboxRPC.PrepareManagedMailbox` result with `RecoveryBoundary{Mode RecoveryMode, AppendEntryID string}`. Valid values are exactly: `append_entry_id` with nonempty `AppendEntryID`; `no_prior_entry` with empty ID; and persistence-only `legacy_unknown` with empty ID. Add migration-owned `session_entry_recovery_mode` alongside historical nullable `SessionEntryWatermark`; validate the pair on every load/write. `MarkManagedDirectPrompted` CASes fence + allocated attempt state, writes both fields atomically, and returns success only when exactly that attempt transitions to prompted. `dispatchDirectInLane` calls Prompt only after this CAS succeeds; any Prepare/CAS error releases the allocated attempt and asserts zero command writes. `RecoverManagedMailbox` sends `since` only in `append_entry_id`; no-prior/legacy release to fresh bounded retry after process loss. Persist mode/boundary before any direct prompt send.

## 4. Activity and UI truthfulness

Use `Manager.recordManagedDeliveryFailure`, release/retry transitions, `internal/store/delivery_test.go`, and managed-history patterns to emit redacted `managed_delivery_prepare_failed`, `managed_delivery_prepare_retry`, and `managed_delivery_dead_lettered` facts with safe kind/code/retryability/timestamp/opaque delivery hash. Repeated preparation failure must be non-ready and never present a false active row.

In `managed_http.go`, `App.tsx`, `Conversation`/`MessageList`, and `managed-history.test.tsx`, retain prior valid managed-list/history data on request/schema failure, show safe retryable error state, issue retry only from click, and never mark selected Complete Activity ready after failure.

## 5. Exact-target validation

1. Run focused Go/frontend behavior tests and changed-package coverage.
2. Later, add a composed managed-path integration harness: run real Go `rpcClient` against a pinned Pi 0.85 JSONL fixture/child emitting audit-approved ordinary shapes before real `get_entries`/prompt requests, while store/manager observes durable attempt state. Prove initial prompt, managed state transition, persisted boundary/mode before command write, >128 ordinary events, later opaque marker, direct command write, `direct_prompted`, injection acknowledgment, and redacted durable Complete Activity. After a terminal state, prove only fresh runtime generation completing readiness restores ready. Keep `tests/delivery_coordinator_e2e.test.ts` and `tests/mecha_rpc_native_pi_e2e.test.ts` as complementary extension/runtime evidence, not substitutes. This proof and Complete Activity behavior are pending; they are not shipped by this plan.
3. Separately collect `/v1/managed-agents` and selected `/v1/history?...view=complete` status/schema/projection/count plus served bundle hash/markers and DOM count. Do not attribute UI emptiness before that comparison.
4. Run independent robot review and `pr-validate`; resolve only validated scoped findings.

## Non-goals

No launcher changes, no live-process changes, no archive/log-viewer edits, no raw history/frames in instrumentation, and no stale bundle deployment claim folded into the backlog or cursor repair.
