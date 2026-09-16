# Managed Mailbox Readiness Backlog Recovery Specification

## Confirmed incident root cause

The live worker already has entries and a current leaf. Its failure detail is `managed readiness receipt backlog is full` (`ErrReadinessBacklogFull`), not missing `leafId`. In `rpcClient.readLoop`, ordinary Pi 0.85 event frames accumulate in the readiness backlog; at 129 retained frames the client terminalizes. A later `PrepareManagedMailbox` / `get_entries` then fails immediately from that terminal state.

Nullable `leafId` is a separate latent empty-session cursor defect. It remains covered as an edge case, but is not described as the incident cause.

## Pi 0.85 compatibility evidence

The independently versioned installed Pi runtime is `0.85.0`. Its `get_entries` request is:

```json
{"type":"get_entries","since":"<optional entry ID>"}
```

Its full response envelope is:

```json
{"id":"1","type":"response","command":"get_entries","success":true,"data":{"entries":[],"leafId":null}}
```

`since` uses append-order entry IDs; `leafId` is nullable active-tree state. RPC subscribes before the later `AgentSession` extension enrichment (`rpc-mode.js` → `toJsonEvent`): the wire `turn_start` is exactly `{type:"turn_start"}`, while `turn_end` is `{type:"turn_end",message,toolResults}`. Internal extension-only `turnIndex`/`timestamp` must not be required on the RPC wire. A literal failed response is `{"id":"1","type":"response","command":"get_entries","success":false,"error":"Entry not found: missing-entry"}` and is delivered only to its matching request-ID waiter, never treated as an event. The compatibility matrix records running-router health and checkout commits, extension source/version, installed Pi SDK/runtime version, and this exact contract.

## Observable behavior

1. `rpcClient.readLoop` semantically classifies Pi event frames. It discards documented ordinary events after classification rather than placing them in the bounded readiness backlog.
2. It retains only waiter-consumable readiness receipts, `agent_settled` restart boundaries, stale mailbox receipts needed for classification/diagnostics, and genuinely unknown semantic events. Unknown-event overflow remains terminal and observable; the change must not hide a new protocol type.
3. More than 128 normal Pi 0.85 events do not terminalize a reusable RPC client: a later `get_entries` succeeds. More than 128 genuine unknown events still fails with the bounded-overflow behavior.
4. The pinned Pi 0.85 long-turn integration, Complete Activity evidence, and direct managed-launch proof remain required later validation. They are not shipped by this document. Evidence must be redacted to state/event names, opaque hashes/identifiers, and timestamps.
5. The nullable-leaf correction is a separately identified latent scope, not incident evidence. The later implementation must model `RecoveryBoundary` as `append_entry_id` (last append-order entry) or private `no_prior_entry` for empty `entries`/`leafId:null`; it must never use `leafId` as `since`, fabricate an ID, or cursorless-scan. A no-prior/legacy process-loss recovery safely releases for fresh retry; a real append cursor recovers later entries only. This typed boundary is pending later implementation.
6. The later direct-dispatch implementation must preserve this order: `AllocateManagedDirectAttempt` creates the attempt; `PrepareManagedMailbox` returns typed boundary; `MarkManagedDirectPrompted` performs the fence/attempt CAS persisting mode plus real ID/null mode; only a successful CAS permits `PromptManagedMailbox` to write the command. Prepare/CAS failure writes no command, releases that attempt, records bounded failure, and schedules retry. A failed command write releases the already-persisted attempt. The required append-entry, CAS, and zero-command-write tests remain pending; this document does not claim them shipped.
7. The router emits distinct bounded/redacted activity facts `managed_delivery_prepare_failed`, `managed_delivery_prepare_retry`, and `managed_delivery_dead_lettered`. The exact terminal threshold is the existing delivery retry budget: failure number reaches the configured maximum attempt count and `releaseManagedDirect` cannot schedule another attempt; then state is dead-lettered/non-ready. Any later successful new attempt with durable boundary, accepted direct prompt, and valid receipt transitions its projection to ready/acknowledged. Before that receipt, every prepare failure is non-ready and has no false active row.
8. Managed-list and selected Complete Activity failures retain prior valid rows, display safe retryable error state, retry only after a click, and never mark history ready after request/schema failure or repeated preparation failure.
9. WebUI router/managed-row/bundle diagnosis remains separate. It records endpoint status/schema/projection/count for `/v1/managed-agents` and selected `/v1/history?...view=complete`, then compares served bundle markers/hash and DOM count before assigning blame.

## Design and boundaries

The event-classification boundary is `rpcClient.readLoop`. It may discard only strict-valid, exact-equality ordinary `AgentSessionEvent` names: `agent_start`, `agent_end`, `agent_settled`, `turn_start`, `turn_end`, `message_start`, `message_update`, `message_end`, `tool_execution_start`, `tool_execution_update`, `tool_execution_end`, `queue_update`, `compaction_start`, `compaction_end`, `entry_appended`, `session_info_changed`, `thinking_level_changed`, `auto_retry_start`, `auto_retry_end`, `summarization_retry_scheduled`, `summarization_retry_attempt_start`, `summarization_retry_finished`, and `bash_execution_update`. `message_update` additionally requires nested `assistantMessageEvent` exact type one of `start`, `text_start`, `text_delta`, `text_end`, `thinking_start`, `thinking_delta`, `thinking_end`, `toolcall_start`, `toolcall_delta`, `toolcall_end`, `done`, `error`.

Before discard, semantic exceptions retain their existing routing: `agent_start` parent activity; exact-shape `agent_settled` only for active/admitted restart, otherwise discarded; built-in read/bash/edit/write lifecycle start/end; compaction; recognized custom `message_end` consumers. Root-control requests/results and built-in lifecycle candidates are anti-poison semantic/control channels, never readiness receipts: valid frames become typed redacted callbacks, while malformed or future candidates are boundedly discarded without retaining raw maps, arguments, results, output, diagnostics, or payload logging. `message_start` never satisfies a receipt. Bounded readiness backlog retains only `mecha.tools.accepted.v1`, `mecha.tools.rejected.v1`, `mecha.policy.applied.v1`, `mecha.child.snapshot.v1`, exact restart settlement, and `mecha.mailbox.appended.v1` for direct matcher or bounded stale diagnostics. Root/control result, child delta, parent activity/compaction use separate semantic channels. Responses must have literal `{id,type:"response",command,success,...}` and route only to matching request ID; late/unknown responses are discarded, never sequenced/backlogged. No prefix, broad shape, or `id + non-null error` rule may discard a frame. Unknown, future, malformed-known, and invalid nested ordinary/readiness frames remain bounded alarms; malformed transport stays terminal. The root-control and built-in lifecycle anti-poison exception above is deliberate and does not widen ordinary-frame discard. Request-ID waiters register before their request write, so a matching response has an owner; late/unknown IDs are discarded. Native compaction is closed by variant: start is `{type,reason}`, successful end includes `result`, `aborted:false`, and `willRetry:false`, aborted end omits result with `aborted:true`, and failed end omits result with a nonempty `errorMessage`; reasons are `manual`, `threshold`, or `overflow`. Root control is likewise closed: RPC `message_end` is exactly `{type,message}` with a `role:"custom"` timestamped nested message, while normalized direct custom control is its explicit five-key form. An armed direct matcher may consume only its exact append receipt, recognized assistant reply, and valid settled boundary; unrelated/malformed frames continue to bounded ordinary alarms. An admitted settled boundary does not duplicate backlog but fans parent activity once; a non-admitted settlement waiter retains the same boundary before its single parent-idle projection. Readiness/direct semantic receipts may arrive after command write but before their later matcher registration: matcher serialization plus a captured watermark and bounded backlog retain post-watermark receipts, while receipts at/below watermark are stale. Bounds are 128 backlog/tasks/direct and 1 MiB line, with no TTL; readiness uses serialization or keyed matching.

The managed RPC client validates the Pi envelope at its boundary. A response with a matching pending ID but malformed shape or the wrong command is a bounded readiness-backlog alarm; its waiter stays armed. An exact-valid response for an unknown or late ID is discarded. A valid matched response routes to its waiter and does not increment `eventSequence`. Rejected responses are exact-valid only when `success:false` has a nonempty `error`; they resolve as ordinary RPC failures.

Evaluator cleanup keeps its abort owner and alarm/pending state after a malformed abort response. The same pending waiter can consume an exact-valid follow-up. An exact rejected abort response has `success:false` and a nonempty error, and resolves as the normal RPC failure; it does not masquerade as malformed cleanup.

Compaction uses closed native variants. `compaction_start` is exactly `{type,reason}`. A successful `compaction_end` has `result`, `aborted:false`, and `willRetry:false`; its result may contain nested `usage` and `details`, and the top level cannot contain `errorMessage`. An aborted end omits `result` and has `aborted:true`; a failed end omits `result` and owns a nonempty `errorMessage`. Malformed or future compaction candidates become bounded alarms. Root-control and built-in lifecycle candidates remain the deliberate anti-poison exception: valid frames use typed redacted callbacks, and malformed/future candidates are boundedly discarded without raw retention.

A pending-root provisional `agent_settled` is consumed by the direct owner but emits neither direct settled nor parent idle while the matching root result and final settlement remain outstanding. The final boundary emits the intended direct and parent fanout once each. Lifecycle activity records expose safe event-kind/code/retryability, timestamp, and opaque safe delivery hash through the managed-history projection, never internal-only event keys, content, paths, process data, or raw RPC payloads. UI transport/state owns distinct list and selected-history failure state and passes safe error plus click-only retry callback to presentation. Complete Activity remains a required later implementation and validation surface.

## Non-goals

- No restart, termination, ownership, or alteration of live `router-log-viewer-phase1`.
- No launcher or unrelated product-code change.
- No raw mailbox/history content, credentials, session paths, process data, or RPC payloads in logs/evidence.
- No edits to archive, log-viewer, or unrelated dirty checkpoint files.

## Validation contract

- Focused read-loop tests: mixed >128 exact ordinary frames including every allowed nested `message_update` variant leave client usable for `get_state`/`get_entries` with empty backlog; unknown `future_event_v1` stays live at 128 and terminalizes `ErrReadinessBacklogFull` at 129. Cover pre-waiter/stale receipts, pre-matcher mailbox receipt, out-of-order concurrent and late response IDs, readiness serialization/keyed-matching proof, semantic exceptions, and terminal callback once/all waiters fail.
- Later long-turn proof must record initial prompt, managed state transition, durable attempt/boundary evidence, `direct_prompted`, opaque later marker injection, acknowledgment, and Complete Activity. Only a fresh runtime generation completing readiness may restore ready after terminal failure. This proof is pending.
- Later cursor validation must cover empty/null, nonempty append-vs-leaf, branched, malformed, retry, and recovery regressions. Typed `RecoveryBoundary`, append-entry persistence, and CAS validation remain pending.
- Focused lifecycle/UI tests and coverage; browser proof of safe retained-row error and click retry.
- Independent robot review and validated findings before delivery.
