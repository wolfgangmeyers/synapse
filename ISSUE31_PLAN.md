# Issue #31 TDD Plan: stock Pi 0.73.1 managed recovery

## Scope and non-goals

Implement the signed-off orderly-stop, failed-row operator, session continuity, UI freshness, explicit completion, and five-trial acceptance contract against unmodified official Pi 0.73.1. Preserve crash-time identity and group safety, current allowlists, bounded retention, and separate extension provenance.

### Wolfgang's direct acceptance sequence

1. Start a task-local isolated router and Web UI.
2. Launch a managed stock-Pi agent and prove it works on a real development task.
3. While that task is in progress, restart only the task-local router.
4. Through an isolated BCB, prove automatic continuation from the correct point within one 30-second deadline. The agent must not get stuck or require a manual reconnect.
5. Repeat the complete sequence as five consecutive fresh trials.

Never touch shared router, UI, or browser state. Do not add unrelated framework or provenance abstractions unless this exact sequence directly requires them.

Do not patch official Pi core. Do not use a Wolfgang/scoped Pi fork, infer the historical `SIGTERM` sender, weaken safety checks, edit database rows manually, change unrelated UI behavior, touch shared services, or broaden acceptance to claim disabled `pi-subagents` parent nudges work.

The current worktree already contains Issue31 implementation work. Preserve it. Migration `059_managed_lifecycle_orphan_repair.sql` exists. The failed-root operator work correctly starts at `060_managed_root_recovery_operator_state.sql`. New work uses `061` and `062`.

## Source-backed stock contract

The retained official evidence is `/home/wolfgang/.cache/mecha-issue31/official-pi/RESULT.json` plus its checked files. The compatibility target is `@mariozechner/pi-coding-agent` `0.73.1`, lightweight tag `v0.73.1`, commit `781152fc24841dc54b22284514604048ebe5e2c9`, root tree `e45fa7f9bfca63ae980fcc47ee2a1f2bd417cb91`, and package tree `b8e81d39531531f87299deadcb1acdcff1445324`. The npm-declared `badlogic/pi-mono` identity redirects to repository id `1035029907` and current `earendil-works/pi` URL. Package and immutable commit select the runtime. The tag alone does not.

Official source and installed declarations establish these boundaries:

| Surface | Stock 0.73.1 evidence | Required design |
|---|---|---|
| CLI session selection | `packages/coding-agent/src/cli/args.ts::parseArgs`, `src/main.ts::resolveSessionPath`, `createSessionManager`; installed `dist/cli/args.d.ts` | Use `--session <private-absolute-path>` for fresh and resume. Do not use ID-prefix lookup, `--resume`, or `--continue`. |
| Session identity/files | `src/core/session-manager.ts::SessionManager`, `_persist`, `newSession`; installed `dist/core/session-manager.d.ts` | Pi generates UUIDv7 ID. Bind `get_state.sessionId`. A missing exact path stays reserved but is not durable until an assistant message, so prime before workload. |
| RPC state/control | `src/modes/rpc/rpc-types.ts::RpcCommand`, `RpcSessionState`; installed `dist/modes/rpc/rpc-types.d.ts` | Use `abort`, `get_state`, and `compact`. Stock RPC has no `get_entry` or `get_entries`. |
| Events | `src/core/agent-session.ts::AgentSessionEvent`, `@mariozechner/pi-agent-core/dist/types.d.ts::AgentEvent` | Accept exact 0.73.1 shapes. `agent_end` has `messages` only. `message_update` has `message` and `assistantMessageEvent`. Never treat `agent_end` as settled. |
| Extension transcript | `src/core/extensions/types.ts::ExtensionContext`, `ReadonlySessionManager`; installed declarations | Scan bounded `context.sessionManager.getEntries()` inside the extension. Keep physical entry IDs opaque. |
| Extension append | `ExtensionAPI.sendMessage` | Stock append accepts no caller entry ID and returns `void`. Reconcile logical identity after persistence. |
| Compaction | `ExtensionContext.compact`, `CompactOptions`, `session_before_compact`, `session_compact`; RPC `compact` | Use callback and RPC completion. There is no `session_compact_failed` or top-level `pi.compact`. |
| Parent settlement | `AgentSession._processAgentEvent`, `_checkCompaction`, `_handleRetryableError` | Public 0.73 APIs cannot reconstruct exact `agent_settled`. Use abort/quiescence or semantic receipts. Keep automatic parent nudges fail-closed. |
| Inline extensions | `src/core/extensions/loader.ts::loadExtensionFromFactory` and official SDK types | Stock accepts `ExtensionFactory[]`, not the later named `InlineExtension` wrapper. |

The official npm artifact remains independently pinned by integrity, shasum, tarball SHA256, installed package-tree SHA256, CLI SHA256, and version output listed in `ISSUE31_SPEC.md`. Acceptance verifies those values and the separate extension lock before starting resources.

## Durable state and migrations

### Existing migration 060

Keep `router-proxy/internal/store/migrations/060_managed_root_recovery_operator_state.sql`. It owns failed-root operator versioning, operation binding, stages, owner/epoch takeover, and bounded resume indexes. Update no numbering back to 059.

Finish the current store and manager behavior in:

- `router-proxy/internal/store/managed_root_recovery.go`
- `router-proxy/internal/store/managed_root_recovery_operator.go`
- `router-proxy/internal/managed/root_recovery_operator.go`
- `router-proxy/internal/managed/supervisor_restart_retry_test.go`
- `router-proxy/internal/managed/supervisor_termination_s5.go`
- `router-proxy/internal/managed/supervisor_termination_s5_test.go`
- `router-proxy/internal/store/managed_root_recovery_migration_test.go`

The state machine remains `reserved -> identity_resolved -> executing -> terminal`. Same request and body replays the result. Same request with a different canonical body conflicts. Owner takeover is CAS-protected. Retry and termination serialize against exact action/agent versions, launch, and alias reuse. Terminal rows retain the existing 24-hour bound.

### Migration 061: stock session path and resume artifact

Add `router-proxy/internal/store/migrations/061_managed_stock_pi_session_resume.sql`.

It must provide one durable provisional session-path reservation keyed by agent, session epoch, and launch operation. Store only a root-relative path plus its hash. The process layer reconstructs and revalidates the private absolute path. The reservation has a nullable Pi session ID until the exact launch/process CAS binds `get_state.sessionId`. Its states are `reserved`, `bound`, `materialized`, and `retired`. A bound/materialized row points to the existing `managed_session_artifacts` row. Unique constraints cover launch operation, agent plus epoch, path hash, and non-null session ID. Terminal reservation metadata is pruned after 24 hours only after the retained artifact owns the path.

Add nullable `resume_artifact_id` foreign keys to `managed_orderly_stop_markers` and `managed_root_recovery_actions`. A confirmed marker and every recoverable action must have one exact retained materialized artifact. Existing unconfirmed/terminal history can remain null and cannot authorize stock resume. Claim copies the marker's artifact ID into the recovery action in the same transaction.

Planned store symbols:

- `ReserveManagedSessionPath`
- `InstallManagedLaunchProcess`
- `BindManagedPiSession`
- `BindManagedSessionArtifact`
- `ConfirmManagedSessionMaterialized`
- `CreateManagedOrderlyStopIntent` with artifact CAS
- `ClaimManagedRootRecovery` with artifact copy

Migration tests start from schema 060 and a populated legacy database. They cover copy/backfill behavior, null legacy history, foreign keys, uniqueness, injected statement failure, transaction rollback, and pruning.

### Migration 062: logical control delivery receipts

Add `router-proxy/internal/store/migrations/062_managed_control_delivery_receipts.sql`.

Extend the root-control ledger with `delivery_protocol`, canonical `delivery_payload_sha256`, reconciliation state, receipt ID, receipt runtime generation/session epoch/manifest hash, and receipt timestamp. The durable stages are `append_pending -> reconcile_pending -> delivered`, with terminal conflict/failure states. The immutable result and payload hash survive router restart. Existing delivered rows are classified `legacy_explicit_entry_v1`. Existing nonterminal rows move to `stock_transcript_receipt_v2` and resume from their immutable result. Preserve the current 30-day and 200-terminal-rows-per-agent control retention bound.

Also add `reconciliation_protocol`, `receipt_payload_sha256`, and `receipt_id` to `managed_direct_delivery_attempts`. Stock-v2 attempts stop using `session_entry_watermark` and `receipt_entry_identity` as authority. Keep those legacy fields for inspection and rollback. Reuse the existing message/attempt expiry. Do not add a general history endpoint or unbounded transcript copy.

## Ordered TDD phases

### Phase 1: RED tests

Add tests first. Every RED must compile and fail on the intended behavior. Record the failing assertion. Mutation checks restore only the mutated lines and verify the complete worktree afterward.

#### Stock launch, path, binding, and priming

Files:

- `router-proxy/internal/managed/process_test.go`
- `router-proxy/internal/managed/supervisor_launch_test.go`
- `router-proxy/internal/managed/session_artifacts_test.go`
- `router-proxy/internal/managed/crash_recovery_behavioral_functional_test.go`
- lifecycle restart/reset tests beside `supervisor_restart_s3.go` and `supervisor_reset_s1.go`
- store tests beside the migration 061 helpers

Named tests:

- `TestCommandSpecStockPiFreshUsesReservedAbsoluteSessionPath`
- `TestCommandSpecStockPiResumeUsesExactRetainedAbsoluteSessionPath`
- `TestCommandSpecStockPiOmitsSessionIDAndApprove`
- `TestManagedLaunchPersistsProcessBeforeBindingGeneratedSessionID`
- `TestManagedLaunchBindsGetStateSessionIDToExactOperationAndProcess`
- `TestManagedLaunchRejectsStaleSessionBindingCallback`
- `TestManagedFreshSessionPrimesAndMaterializesBeforeWorkload`
- `TestManagedSessionHeaderPathAndInodeMustMatchAfterPriming`
- `TestConfirmedOrderlyRecoveryUsesMarkerBoundResumeArtifact`
- `TestMissingChangedOrSymlinkedResumeArtifactStartsNoReplacement`

Cover path traversal, symlink components, pre-existing path, wrong absolute path from `get_state`, empty ID, ID collision, malformed/oversized header, inode replacement, process loss before bind, bind loss before priming, and failure after materialization before ready.

#### Stop, recovery, retry, and termination

Keep existing marker/identity tests. Add or correct:

- `TestSIGTERMRouterCloseReopenOrdering` in `router-proxy/cmd/mailbox-router/main_test.go`. It must start a real managed child. The required order is marker plus artifact intent, RPC abort/quiescence, RPC close, process stop, absence proof, marker confirmation, replacement start, exact session resume, ready.
- Keep the current `--managed=false` case as a command-shutdown smoke test only. It is not Issue31 evidence.
- `TestFailedRootRecoveryRetryReservesExactVersionAndCanonicalRequestIdempotently`
- `TestFailedRootRecoveryRetryAdoptsCurrentRecipeOnlyAfterAbsenceOrExactOwnership`
- `TestFailedRootRecoveryTerminationRetiresUnboundUnsafeRowWithoutSignallingStoredIdentity`
- crash after reservation, crash after identity resolution, owner takeover at every stage, retry/terminate race, retry/launch race, alias reuse, identity change immediately before signal, same-body replay, changed-body conflict, and terminal retention tests.

#### Stock RPC, quiescence, transcript receipts, and compaction

Files:

- `router-proxy/internal/managed/rpc_test.go`
- `router-proxy/internal/managed/root_control_rpc_test.go`
- `router-proxy/internal/managed/root_control_admission_fence_test.go`
- `router-proxy/internal/managed/managed_delivery_append_ack_native_test.go`
- `router-proxy/internal/managed/readiness_canonical_test.go`
- `router-proxy/internal/managed/parent_compaction_test.go`
- `pi-extensions/mecha-rpc/tests` and root `tests/mecha_rpc_*.test.ts`
- `pi-extensions/mecha-messaging` and root `tests/*compaction*.test.ts`

Named tests:

- `TestOrdinaryAgentSessionEventAcceptsStock0731AgentEnd`
- `TestOrdinaryAgentSessionEventAcceptsStock0731MessageUpdate`
- `TestAgentEndNeverCompletesRestartOrParentActivity`
- `TestPreparedRestartRequiresAbortResponseAndQuiescentGetState`
- `TestPreparedRestartRejectsStreamingCompactingOrQueuedState`
- `TestControlResultStockAppendReturnsVoidAndReconcilesTranscript`
- `TestControlResultExactReplayDoesNotAppendAgain`
- `TestControlResultChangedOrDuplicateTranscriptMatchFailsClosed`
- `TestControlResultCrashAfterAppendBeforeReceiptReconcilesOnce`
- `TestControlResultReceiptRejectsStaleRuntimeSessionManifestOrPayload`
- `TestDirectMailboxRecoveryUsesExtensionScanWithoutGetEntriesRPC`
- `TestStockCompactionCallbacksResumeOnSuccessFailureAndAbort`

Add a wire assertion that no request has type `get_entry` or `get_entries`. Mutation tests remove each request ID, payload hash, provider sequence, or runtime-fence comparison and must fail.

#### `pi-subagents` compatibility RED suite

Create a detached task-local checkout from `185381bf4f52a1d86dbb249e5a54346e237d36d6`. Verify package tree `a2e9e77fb9ada0cb455de965fcc882124953c939` before edits. Do not modify `/home/wolfgang/code/pi-packages` or official Pi.

Extend:

- `packages/pi-subagents/test/lifecycle/create-subagent-session.test.ts`
- `test/lifecycle/child-extension-factories.test.ts`
- `test/lifecycle/subagent-session.test.ts`
- `test/lifecycle/subagent-manager.test.ts`
- `test/observation/notification.test.ts`
- `test/handlers/lifecycle.test.ts`
- `test/print-mode.test.ts`
- a new `test/compat/stock-pi-0731-load.test.ts`

Named stock-profile tests:

- `stock Pi 0.73.1 entrypoint loads and registers all tools`
- `stock Pi 0.73.1 unwraps named child factories to ExtensionFactory`
- `stock Pi 0.73.1 includes inline child tools in the first-turn allowlist`
- `stock Pi 0.73.1 child completion follows the owned prompt promise`
- `stock Pi 0.73.1 withholds parent completion nudge after agent_end`
- `stock Pi 0.73.1 never duplicates a withheld nudge across retry or compaction`
- `stock Pi 0.73.1 disposal drops every withheld nudge`

The tests must not expect an automatic nudge. They prove child lifecycle remains usable and the unsupported parent boundary fails closed.

#### UI and acceptance harness RED suite

Extend:

- `web/router-ui-react/tests/unit/projections.test.ts`
- `tests/unit/protocol-guards.test.ts`
- `tests/component/agent-row.test.tsx`
- `tests/component/app-fencing.test.tsx`
- `tests/component/app-integration.test.tsx`
- `tests/acceptance/managed-recovery-harness.test.mjs`
- `tests/acceptance/bcb-runner-selectors.test.mjs`

Name negative tests for:

- router barrier missing, unacknowledged, wrong PID, wrong token, wrong provider sequence, or stale fence
- completion before boundary, wrong token/trial/agent/provider sequence/generation/incarnation, duplicate, or missing transcript persistence
- response observed without a final receipt and receipt observed without the exact persisted response
- signal false and signal throw
- timeout winning over late completion
- timeout evidence captured before cleanup
- cleanup failure
- public/DOM identity leakage
- unchanged or router-inconsistent projection version
- shared-resource before/after mismatch
- runner attempts to synthesize completion
- `response_persisted` supplied as a literal instead of observed evidence
- official lock or extension lock missing, mismatched, drifted, historical, global, or `PATH`-resolved

### Phase 2: production router, store, and extension implementation

#### Launch and session artifact path

Update `router-proxy/internal/managed/process.go`:

- Replace `CommandSpec.SessionID` session selection with validated `SessionPath`.
- `CommandSpec.Args` always passes `--session <absolute-path>` for `CreateNew` and `ResumeExisting`.
- Keep `--session-dir <private-root>` if needed for stock session discovery, but never use ID-prefix lookup.
- Remove `--session-id` and `--approve`.
- Keep fixed `--no-*` resource flags and explicit extension paths. Do not invent permission or trust semantics.

Update `router-proxy/internal/managed/session_artifacts.go`:

- Add exact-path reservation and canonical absolute reconstruction beneath `SessionRoot`.
- Add bounded first-line JSONL header parsing and exact header/session checks.
- Reuse `rootRelativeSessionPath`, `deviceInode`, and `verifyRetainedMaterialized`.
- Replace candidate-ID filename guessing with the durable reserved path.

Update launch paths:

- `router-proxy/internal/managed/supervisor.go`: `Manager.Launch`, `Manager.start`, `readinessGate`, `Manager.Close`
- `supervisor_restart_s3.go::startLifecycleRestartTargetInLane`
- `supervisor_reset_s1.go::startResetTargetInLane`
- `root_cold_recovery.go::recoverManagedRootInLane`
- matching store code in `internal/store/managed.go`, `managed_lifecycle_s0.go`, `managed_reset_s1.go`, `managed_orderly_stop_marker.go`, and `managed_root_recovery.go`

Fresh flow:

1. Reserve the exact path and provisional agent/artifact state with `pi_session_id=NULL`.
2. Start the process and persist PID/start marker/incarnation/runtime generation against the launch operation.
3. Open RPC and read `get_state`.
4. CAS-bind returned `sessionId` and exact `sessionFile` to that process and operation.
5. Run the fixed private priming prompt.
6. Wait for its assistant response and quiescent state.
7. Verify file, bounded header, ID, device/inode, and path.
8. Bind the materialized artifact and continue policy/readiness gates.
9. Admit workload and publish ready only after all checks pass.

Resume flow verifies the marker/action's `resume_artifact_id` before process start, passes its exact absolute path, then repeats `get_state`, header, path, and inode checks before installing the target binding.

#### Orderly shutdown and quiescence

Replace `rpc.go::SettlePreparedRestartTurn`'s `agent_settled` matcher with official RPC `abort` plus `get_state` quiescence. Keep the exact runtime fence and stdout ownership. An already-quiescent source still receives or validates the same state gate. Close ingress first. If queued messages lack router-owned durable replay, fail the controlled stop. Do not consume or synthesize `agent_end` as idle.

Keep `Manager.Close` ordering:

1. reserve marker with exact materialized artifact
2. quiesce source
3. close RPC and wait for admitted callbacks
4. stop the exact process group
5. prove absence
6. confirm marker

Marker persistence or quiescence failure leaves the process untouched where safe. Any later isolated cleanup remains failure cleanup.

#### Official event decoder and activity behavior

Update `router-proxy/internal/managed/rpc.go::ordinaryAgentSessionEvent` and canonical validators for exact stock 0.73.1:

- `agent_end`: `type`, `messages`
- `message_update`: `type`, `message`, `assistantMessageEvent`
- stock tool, queue, retry, session, `compaction_start`, and `compaction_end` shapes

Remove `agent_settled` as an authoritative parent-activity source. Keep it only as a versioned diagnostic if later runtimes emit it. In `readiness_canonical.go::normalizeParentActivityEvent` and `parent_activity.go::handleParentActivityEvent`, do not project stock `agent_end` to idle. Paths that require semantic completion must use their own receipt. Paths without one remain active/unavailable and fail closed.

#### Logical persisted receipts

Update:

- `pi-extensions/mecha-rpc/package.json` and lock metadata: compile against exact official `@mariozechner/pi-coding-agent` `0.73.1` and the pinned task-local `pi-subagents` build
- `pi-extensions/mecha-rpc/index.ts`: import `ExtensionAPI` from `@mariozechner/pi-coding-agent`
- `pi-extensions/mecha-rpc/rpc-client.ts`: remove `ExplicitCustomMessageAppendOutcome`, explicit-ID options, and `sendHiddenCustomMessageWithEntryId`
- `pi-extensions/mecha-rpc/index.ts`: `mecha-control-result-v1`, transcript reconciliation command, root pending-result bookkeeping
- `pi-extensions/mecha-rpc/protocol.ts`: add the stock persisted-receipt schema
- `router-proxy/internal/managed/rpc.go`: replace `ArmRootControlResult`, `SubmitRootControlResult`, and `GetRootControlEntry` with append and reconcile capabilities
- `router-proxy/internal/managed/root_control_manager.go`: `appendRootControlResult`, `reconcileRootControlAppend`, `validRootControlResult`
- `router-proxy/internal/store/managed_root_control_s4.go`: migration 062 states and receipt persistence

Protocol:

1. Router reserves immutable result plus canonical payload hash and current fence.
2. Extension command scans a bounded `context.sessionManager.getEntries()` view for `mecha.control.result.v1` with exact request, result, payload hash, and fence.
3. Zero matches permits one stock `pi.sendMessage` append. One exact match skips append. Any malformed, changed, or duplicate match fails.
4. Router issues a separate reconciliation command after append/quiescence.
5. Extension rescans. Exactly one match returns `mecha.control.result.persisted.v1` with request/result/payload hash, runtime generation, session epoch, manifest hash, and a generated receipt ID.
6. Router accepts the command response only on the current RPC pipe and exact durable fence, then commits delivered.
7. Crash recovery resends the same immutable result and resumes at reconciliation.

Move `rpc.go::RecoverManagedMailbox` off RPC `get_entries`. Use a narrow extension reconciliation command that scans only for the exact message/attempt/fence. Return metadata only. Bound count and serialized byte size. Do not expose general transcript history.

Replace the `pi-extensions/mecha-rpc/index.ts` goal evaluator's `agent_settled` handler too. The router owns the evaluator ID and prompt. After the evaluator assistant message is persisted, the router requires stock `get_state` quiescence and invokes `/mecha-goal-evaluation-finalize-v1`. That command verifies the evaluator ID, current fence, and exact persisted assistant result before returning the fenced `mecha.policy.applied.v1` receipt. Retry, compaction, queued work, stale evaluator state, or missing transcript evidence withholds the receipt. No evaluator path uses `agent_end` as completion.

#### Official compaction

Update `pi-extensions/mecha-messaging/index.ts` and `compaction-orchestration.ts`:

- remove `session_compact_failed` registration
- retain `session_before_compact` abort handling and success-only `session_compact`
- route self-compaction through `context.compact({onComplete,onError})`
- route controlled router compaction through RPC `compact`
- resume paused mailbox state in success, error, abort, and synchronous-throw paths
- remove any parent-settlement-triggered compaction action that cannot be driven by an explicit router quiescence gate
- remove every stock registration for `agent_settled` in `mecha-messaging/index.ts`. Managed context monitoring and deferred compaction resume through router quiescence plus RPC `compact`. Explicit extension compaction uses the command's `ExtensionContext.compact` callbacks. Stock settled notifications and other automatic actions that need the missing boundary stay disabled or pending. Recall policy can refresh before the next agent start, but must not synthesize an idle boundary.

Add tests named `stock compaction never starts from agent_end`, `stock deferred compaction waits for router quiescence`, and `stock compaction callback releases mailbox pause exactly once`.

Update `pi-extensions/mecha-rpc/tests/fixtures/managed-rpc-release-probe.mjs` to receive the command context and call `context.compact`. It must not call top-level `pi.compact` or wait for `session_compact_failed`.

### Phase 3: task-local `pi-subagents` compatibility

Never edit the live `/home/wolfgang/code/pi-packages` checkout for this task. Create a task-local detached checkout at the pinned commit, branch locally, and record the initial commit and package tree. Preserve the resulting commit/tree plus built extension entrypoint in `issue31-extension-provenance-lock.json`.

In `packages/pi-subagents`:

1. Replace source and test imports from `@earendil-works/pi-ai`, `pi-coding-agent`, and `pi-tui` with official `@mariozechner/*` names.
2. In `package.json`, pin the three official peers/dev dependencies to `0.73.1`. Replace `@sinclair/typebox` with exact `typebox` `1.1.24`.
3. In `src/tools/agent-tool.ts`, `steer-tool.ts`, and `get-result-tool.ts`, import `Type` from `typebox` and `Value` from `typebox/value`.
4. In `rollup.dts.config.mjs`, externalize `/^@mariozechner\//` and `typebox`.
5. Define package-owned `ChildExtensionRegistrationV1 = {name, factory}`. Keep owner/name duplicate checks in `src/lifecycle/subagent-manager.ts`, but pass `registrations.map(({factory}) => factory)` to `DefaultResourceLoader` from `src/lifecycle/create-subagent-session.ts`.
6. Accept stock loader path `<inline>` when discovering first-turn child tools. Keep named paths accepted for later runtimes.
7. Add an explicit stock compatibility profile in `src/index.ts` and `src/observation/notification.ts`. `agent_start` may close nudge delivery. No `agent_end`, timer, debounce, microtask, retry event, or compaction event reopens it. Once exact parent settlement is unavailable, completion nudges remain withheld until result consumption or disposal.
8. Remove user/tool text that promises automatic completion under the stock profile in `src/tools/agent-tool.ts` and `src/tools/get-result-report.ts`.
9. Keep child lifecycle completion based on the package-owned child prompt/run promise in `src/lifecycle/subagent.ts`, `subagent-session.ts`, and `subagent-manager.ts`.
10. Update the task-local lockfile and build metadata. Record the adapted commit, package tree, dependency lock hash, and built entrypoint hash. Update the mecha-developer root `package.json` fork check so `check:mecha-rpc` validates `MECHA_PI_EXTENSION_LOCK` and the task-local checkout. It must not require mutable `origin/main` or the live checkout.

The profile must report the nudge capability as disabled. Do not backport `agent_settled`, patch official Pi, or claim `agent_end` is equivalent.

### Phase 4: UI, release proof, and acceptance implementation

#### UI freshness

Update the existing projection path:

- `router-proxy/internal/managed/projection.go`
- `web/router-ui-react/src/protocol/types.ts`
- `src/protocol/guards.ts`
- `src/state/agent-projection.ts`
- `src/state/app-state.ts`
- `src/state/reducer.ts`
- `src/components/AgentRow.tsx`

Stamp each accepted managed row with that frame's `projection_version`. Reject stale or equal overwrites. Render `data-managed-projection-version`, lifecycle, and activity. If projection version is unavailable, render only `runtime_observation_id = SHA-256("managed-runtime-observation-v1\0" + runtime_generation + "\0" + process_incarnation)`. Never render raw identity.

#### Official and extension locks

Add checked-in manifests:

- `web/router-ui-react/tests/acceptance/issue31-official-pi-release-lock.json`
- `web/router-ui-react/tests/acceptance/issue31-extension-provenance-lock.json`

The official lock contains every identity and checksum in `ISSUE31_SPEC.md`, including task-local binary/package containment. The extension lock separately records the pinned/adapted `pi-subagents` commit and package tree, built entrypoint hash, and acceptance probe hash. Preflight writes `missing_fields`, `mismatches`, and `drift` arrays for both locks and exits nonzero before trial allocation on failure.

#### Barrier, completion, and false evidence removal

Update:

- `pi-extensions/mecha-rpc/tests/fixtures/managed-rpc-release-probe.mjs`
- `web/router-ui-react/tests/acceptance/bcb-managed-recovery-runner.mjs`
- `tests/acceptance/managed-recovery-harness.mjs`
- `tests/acceptance/bcb-runner.mjs`
- the isolated acceptance-router fixture under `router-proxy/cmd`

Required flow:

1. Start stock Pi at the fresh reserved path and bind `get_state.sessionId`.
2. Complete the priming turn. Verify materialized file/header/path/inode.
3. Start the unique token workload. The deterministic provider records a monotonically increasing request sequence and holds the first token request active.
4. The probe sends `managed_recovery_in_progress`. The isolated router acknowledges it only after matching token, provider sequence, agent, router PID, and current DB fence. A JSON file may mirror this evidence but is never the acknowledgment source.
5. Recheck the router acknowledgment and active provider request immediately before signalling.
6. Record `performance.now()`, call `child.kill("SIGTERM")`, and require true.
7. On replacement, verify exact session continuity and changed process/runtime identity.
8. Send the acceptance resume envelope automatically. It contains trial token and current router fence.
9. The deterministic provider returns token plus request sequence. The extension verifies its exact response in the session transcript and emits the final receipt.
10. The isolated router compares the receipt to the current binding and persists it. The runner only reads that persisted router evidence.

Delete runner completion synthesis. Replace hard-coded `response_persisted: true` with observed transcript reconciliation plus router receipt. Replace hard-coded shared `mutated:false` with captured before/after identities and comparisons.

### Phase 5: five-trial acceptance

The `--trials 5` loop allocates inside each iteration:

- new task root, database, config, log, HOME, session root, PID file, and artifact directory
- new router, deterministic provider, BCB, and UI ports
- new isolated router/provider/BCB/CfT processes
- new browser profile and dedicated tab
- new logical/actual agent IDs and workload token

No trial reuses a router, database, profile, bridge, tab, task root, or port. After every trial, capture evidence, stop only isolated resources, verify process absence and closed ports, remove task/profile resources, and retain artifacts. Cleanup must finish before the next allocation. Failure stops the sequence and requires a fresh rerun from trial one.

The existing page must reconnect without reload or any manual action. It must show current `Connected`/`ready`, a changed/equal-current safe freshness token, exact private session continuity, changed process/runtime identity, and the explicit current-fence completion receipt before the one absolute deadline.

### Phase 6: canonical gates

Run from `/home/wolfgang/code/mecha-developer` unless the command changes directory:

```sh
git diff --check -- ISSUE31_SPEC.md ISSUE31_PLAN.md router-proxy pi-extensions web/router-ui-react

(cd router-proxy && go test ./internal/store ./internal/managed ./cmd/mailbox-router -run 'Test(CommandSpecStockPi|ManagedLaunch|ManagedFreshSession|ConfirmedOrderly|SIGTERMRouterCloseReopenOrdering|FailedRootRecovery|PreparedRestart|OrdinaryAgentSessionEvent|ControlResult|DirectMailboxRecovery|ManagedOrderly|CrashRecovery)')
(cd router-proxy && go test ./...)
(cd router-proxy && go test -coverprofile=/tmp/issue31-go-cover.out ./...)
go tool cover -func=/tmp/issue31-go-cover.out

npm run check:mecha-messaging
npm run test:mecha-messaging
npm run check:mecha-rpc
npm run test:mecha-rpc
npm --prefix pi-extensions/mecha-rpc run test:coverage

npm --prefix web/router-ui-react test
npm --prefix web/router-ui-react run test:coverage
npm --prefix web/router-ui-react run typecheck
npm --prefix web/router-ui-react run lint
npm --prefix web/router-ui-react run build
```

Run in the pinned task-local `pi-packages` checkout:

```sh
cd "$ISSUE31_PI_SUBAGENTS_CHECKOUT"
git merge-base --is-ancestor 185381bf4f52a1d86dbb249e5a54346e237d36d6 HEAD
test "$(git rev-parse 185381bf4f52a1d86dbb249e5a54346e237d36d6:packages/pi-subagents)" = a2e9e77fb9ada0cb455de965fcc882124953c939
pnpm --filter @gotgenes/pi-subagents check
pnpm --filter @gotgenes/pi-subagents test
pnpm --filter @gotgenes/pi-subagents build:types
pnpm --filter @gotgenes/pi-subagents lint
git rev-parse HEAD
git rev-parse HEAD:packages/pi-subagents
```

Verify the official cache and task-local runtime without network resolution:

```sh
cd /home/wolfgang/.cache/mecha-issue31/official-pi && sha256sum -c SHA256SUMS
/home/wolfgang/.cache/mecha-issue31/official-pi/task-local/node_modules/.bin/pi --version
```

Run acceptance only after every focused and canonical test is green:

```sh
MECHA_PI_SOURCE_LOCK="$PWD/web/router-ui-react/tests/acceptance/issue31-official-pi-release-lock.json" \
MECHA_PI_EXTENSION_LOCK="$PWD/web/router-ui-react/tests/acceptance/issue31-extension-provenance-lock.json" \
MECHA_PI_BIN="/home/wolfgang/.cache/mecha-issue31/official-pi/task-local/node_modules/.bin/pi" \
MECHA_PI_PACKAGE_ROOT="/home/wolfgang/.cache/mecha-issue31/official-pi/task-local/node_modules/@mariozechner/pi-coding-agent" \
npm --prefix web/router-ui-react run test:acceptance:bcb -- --managed-recovery --trials 5
```

BCB and CfT paths remain explicit through `MECHA_BCB_ROOT`, `MECHA_BCB_PYTHON`, and `MECHA_CFT_BIN` or `CHROME_FOR_TESTING_BIN`. The command starts no shared service and uses no provider credential.

### Phase 7: coverage and requirement audit

Map every specification requirement to at least one behavioral test and one evidence field where applicable. Required coverage groups:

- official source/npm/install/cache and separate extension provenance
- fresh path reservation, generated ID binding, priming, header/path/inode validation
- marker intent/confirmation and artifact binding
- crash identity and process-group safety
- exact failed-row retry/termination idempotency and retention
- official event decoding and `agent_end` non-authority
- abort plus `get_state` quiescence
- transcript scan, exact replay, fenced persisted receipt, and no history RPC
- compaction success, failure, abort, and paused-delivery release
- child promise completion and disabled parent nudge behavior
- UI freshness and redaction
- barrier acknowledgment, explicit completion, provider sequence, and current fence
- hard deadline, timeout precedence, failure truth, cleanup failure, rerun from one, and no shared mutation

Run the `test-coverage` skill after the canonical coverage commands. Add tests for every applicable uncovered branch. Isolated unit tests support the owned cross-component tests but do not replace them.

### Phase 8: two review rounds

Review immutable commits, not an uncommitted worktree.

1. **Round one:** migrations 060-062, store CAS/retention, process/session artifacts, supervisor/recovery, RPC/event compatibility, mecha extensions, and task-local `pi-subagents` compatibility. Validate raw findings with a different persona through `pr-validate`. Fix only requirement-backed findings and rerun affected gates.
2. **Round two:** UI freshness, official and extension locks, deterministic provider/probe, acceptance router, harness, artifacts, redaction, cleanup, documentation inventory, and the five-trial evidence. Run `pr-validate` again with a different persona. Rerun affected gates and acceptance after any material change.

Retain review target SHAs, findings, validation dispositions, fixes, and rerun output. Do not count a review against a changed target.

## Exact file ledger

### Router and store

- `router-proxy/internal/managed/process.go`: `CommandSpec`, `Args`, `execProcessFactory.Start`
- `session_artifacts.go`: `sessionFileFromState`, `rootRelativeSessionPath`, `discover`, `verifyRetainedMaterialized`, new path/header helpers
- `supervisor.go`: `Manager.Launch`, `Manager.start`, `readinessGate`, `Manager.Close`, `ReserveRestart`
- `supervisor_restart_s3.go`: `startLifecycleRestartTargetInLane`
- `supervisor_reset_s1.go`: `startResetTargetInLane`
- `root_cold_recovery.go`: `recoverManagedRootInLane`
- `rpc.go`: `ordinaryAgentSessionEvent`, `SettlePreparedRestartTurn`, root-control/direct-mail capabilities and matchers
- `readiness_canonical.go`, `parent_activity.go`, `parent_compaction.go`
- `root_control_manager.go`: append/reconcile/result validation
- `internal/store/managed.go`, `managed_lifecycle_s0.go`, `managed_reset_s1.go`, `managed_orderly_stop_marker.go`, `managed_root_recovery.go`, `managed_root_recovery_operator.go`, `managed_root_control_s4.go`
- migrations `060`, new `061`, and new `062`

### Extensions and task-local `pi-subagents`

- `pi-extensions/mecha-rpc/package.json`, `index.ts`, `rpc-client.ts`, `protocol.ts`
- `pi-extensions/mecha-messaging/index.ts`, `compaction-orchestration.ts`
- root `package.json`: task-local extension-lock validation for `check:mecha-rpc`
- `pi-extensions/mecha-rpc/tests/fixtures/managed-rpc-release-probe.mjs`
- task-local `packages/pi-subagents/package.json`, `rollup.dts.config.mjs`, `pnpm-lock.yaml`
- task-local namespace imports under `packages/pi-subagents/src` and `test`
- task-local `src/index.ts`, `src/observation/notification.ts`, `src/lifecycle/create-subagent-session.ts`, `src/lifecycle/subagent-manager.ts`, `src/tools/agent-tool.ts`, and `src/tools/get-result-report.ts`

### UI and acceptance

- `router-proxy/internal/managed/projection.go`
- `web/router-ui-react/src/protocol/types.ts`, `guards.ts`
- `src/state/agent-projection.ts`, `app-state.ts`, `reducer.ts`
- `src/components/AgentRow.tsx`
- matching unit/component tests
- `tests/acceptance/bcb-managed-recovery-runner.mjs`
- `managed-recovery-harness.mjs`, `managed-recovery-harness.test.mjs`
- `bcb-runner.mjs`, `bcb-runner-selectors.test.mjs`
- new official Pi and extension lock manifests
- isolated acceptance-router fixture under `router-proxy/cmd`
- `router-proxy/README.md` and `web/router-ui-react/tests/acceptance/bcb-acceptance.md` for the required stop/restart inventory and run contract

Every changed file must map to a requirement above. Remove any unrelated cleanup.

## Evidence and current false positives

Do not carry forward these current claims:

- `TestSIGTERMRouterCloseReopenOrdering` with `--managed=false` proves only router command shutdown.
- `managed-recovery-barrier.json` is local runner output, not router acknowledgment.
- A completion object assembled by `bcb-managed-recovery-runner.mjs` is not workload completion.
- `response_persisted: true` is not persistence evidence.
- `shared_resources.mutated: false` is not a before/after shared-state check.

The replacement evidence is a real managed child, router-persisted barrier, extension-produced transcript-reconciled receipt, observed persistence state, and captured shared identities compared before and after.

## Rollback and safety

Migrations 061 and 062 are additive where possible. Keep legacy columns and classify legacy rows. A release rollback first blocks new stock-v2 admissions, drains or terminalizes nonterminal 061/062 operations, verifies no provisional launch or reconciliation owner remains, and then restores the prior binary. Do not delete retained session artifacts, control results, recovery rows, evidence, or shared state. If a shared database contains active new-protocol rows, use a forward compatibility fix instead of destructive downmigration.

On any implementation or acceptance failure, stop only resources whose exact identity belongs to that task. Preserve failure evidence before cleanup. Never signal an unverified PID/group, rewrite rows manually, fall back to another Pi binary, or patch official Pi. The task-local `pi-subagents` checkout may be removed only after its commit/tree, build hashes, tests, and extension lock are retained.
