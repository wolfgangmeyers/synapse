# Spec: Managed Runtime Contracts

- **ID**: `SPEC-MANAGED-RUNTIME-001`
- **Status**: `Draft`
- **Domain**: `AgentManagement`
- **Target E2E Suite**: No single qualifying Playwright suite yet. Existing composed Go and installed-Pi evidence is mapped below; browser acceptance remains incomplete.

## 1. Intent & User Outcome

Users can launch managed agents, exchange mail, inspect structured state, and recover work without making a terminal the managed runtime's authority. Stable identity, durable delivery, bounded storage, and sender checks must survive runtime replacement.

This is a documentation backfill of surviving contracts, not a runtime change or a declaration of acceptance. It distinguishes current behavior from historical requirements that still need disposition. The [retirement crosswalk](../retirement-crosswalk.md) records those differences. The full [RPC requirements](../archive/architecture/RPC_REQUIREMENTS.html) remain available and binding where no approved replacement is identified. Archiving does not lower their priority or silently replace their acceptance criteria.

Current explanatory sources: [architecture](../router-architecture/architecture.html), [workers](../router-architecture/workers.html), [failures](../router-architecture/failures.html), and [test boundaries](../router-architecture/tests.html). These pages describe specific paths; their subprocess descriptions do not characterize every embedded path.

### Contract classification

This remains **Draft**. “Current contract” below means an observable contract supported by inspected implementation/evidence, not comprehensive E2E acceptance. “Historical unresolved” preserves requirements with missing acceptance or current mismatches; it does not promote them to verified current behavior. “Optional implemented seam” describes existing bridge code, not default enablement. Migration and audit proposals remain proposals, outside active guarantees.

| Scenario | Classification |
|---|---|
| MR-001 | Current launch/replay/readiness contract; joined browser acceptance incomplete |
| MR-002 | Current lifecycle contract; historical termination/recovery-policy acceptance differences unresolved |
| MR-003 | Current extension validation contract; historical full parity, exclusions and production generation matrix unresolved |
| MR-004 | Current control/admission/result contract; historical complete external-effect proof unresolved |
| MR-005 | Current mixed-mail contract; historical router-never-launches-tmux restriction unresolved |
| MR-006 | Current eligibility/retention implementation; historical complete-cap/startup-gate requirements unresolved |
| MR-007 | Current structured snapshot/delta contract; historical only-source-distinction and full child recovery/compaction acceptance unresolved |
| MR-008 | Historical goal contract with selected current slices; exact race acceptance unresolved |
| MR-009 | Historical provider contract; embedded/Pi boundary and full provider parity unresolved |
| MR-010 | Current browser transport contract; joined unrelated-origin browser evidence incomplete |
| MR-011 | Optional implemented bridge seam, not a universal managed-runtime guarantee |

## 2. Core Invariants

1. Agent name, Pi session, process incarnation, session epoch, runtime generation, and manifest identity are distinct. Runtime-bound actions must reject stale identities, not merely match a name.
2. Managed launch acceptance, process start, runtime readiness, host action completion, and Pi transcript receipt are separate observable milestones. A queued tool response proves none of the later milestones.
3. Current lifecycle contract: ordinary restart preserves the saved session. Deliberate context reset changes session identity without changing the worker's identity. Recovery must honor desired state and bounded crash policy, not endlessly resurrect stopped workers.
4. Current extension contract: router-backed tool definitions and operations are router-owned. A manifest is validated before readiness and before a model turn. Existing registered names cannot change meaning in place. Local coding tools are separate from manifest tools.
5. Sender identity comes from the registered runtime, never a tool-supplied `from`. Control traffic cannot fall through into ordinary mailbox handling. Malformed, unknown, conflicting, or stale requests cannot authorize effects.
6. A thin tool's immediate queued/asynchronous result is final for that invocation. A later correlated control result is a separate delivery. Replay must not repeat the admitted operation. A transcript receipt proves append, not the external action's success.
7. Quiet eligibility is determined when a managed send authorization is consumed for the configured canonical recipient. Complete activity cannot evict quiet messages through the complete-history cap. Historical 200-entry complete retention is not the current 5,000-entry implementation: this mismatch remains explicit.
8. Structured parent and child state comes from fenced events and snapshots, never terminal text. Missing evidence cannot fabricate completion. Snapshot repair and old-run rejection are required across reconnect and replacement.
9. Managed HTTP browser compatibility is non-credentialed and origin-independent. No session, cookie, CSRF, or browser credential gate is introduced. The private loopback managed-bridge socket has a separate mutual-proof protocol; its Origin rejection is not browser API authorization.
10. Provider credentials and model availability have runtime-specific owners. The historical Pi-store-only boundary must not be asserted for embedded router credential paths. Neither credentials nor authentication payloads belong in UI status or histories.
11. Tmux remains a separate compatible worker path. Historical §01 also forbids router-launched tmux agents; the current host-launch path is a **historical unresolved conflict**, not an approved exception. Managed Restart All must not restart tmux processes. The worker name `supervisor` grants no lifecycle privilege.
12. Histories and recovery ledgers remain bounded. Existing limits and pruning owners are described below; no new accumulating store is introduced by this spec.

## 3. User Scenarios (Behavioral Contract)

### Scenario: [MR-001] Launch, replay, and readiness
- **User Goal**: Start one usable managed agent.
- **Given**: A valid name, working directory, selected/default model, optional initial instruction, and request identity.
- **When**: The caller launches an agent and repeats the same request.
- **Then**: The saved acceptance is replayed without starting another agent. Process and session binding must succeed before readiness and initial work. Conflicting reuse or invalid input fails without a second launch. HTTP acceptance alone does not claim readiness.

### Scenario: [MR-002] Restart, reset, crash, and termination
- **User Goal**: Recover work without confusing old and new runs.
- **Given**: An existing managed agent and durable session identity.
- **When**: The user restarts, resets context, terminates, or the process/router crashes.
- **Then**: Restart resumes the saved session; reset creates the replacement session while preserving worker identity and retained history. Eligible desired-running roots recover with bounded retries. Stopped/terminated work is not blindly relaunched. Old process actions cannot affect the replacement. Historical termination said “clears router state”; current archive/closed-read behavior is a recorded difference, not proof of that original criterion.

Historical unresolved applicability: managed Pi must use neither `mecha-messaging` nor `mecha-goal`; tmux retains those extensions. Current launch-profile compliance is not proved here.

### Scenario: [MR-003] Manifest installation and child inheritance
- **User Goal**: Get exactly the approved tools before work begins.
- **Given**: A new runtime generation and router-owned canonical manifest.
- **When**: Startup installs definitions or a child starts.
- **Then**: The full manifest validates before registration/readiness. Malformed, oversized, coercive, unbounded, unsupported, colliding, hash-mismatched, or stale input rejects without partial exposure. Child setup is trusted and parent-bound, not model-selected. Changed definitions require unique hash names and the accepted same-session restart path. Add-only updates require their own accepted protocol; an isolated registration proof does not establish production dispatch parity. Coding-tool availability and root credit guards remain separate.

### Scenario: [MR-004] Control requests and sender authority
- **User Goal**: Request an action once and see its actual outcome.
- **Given**: A ready managed runtime.
- **When**: A thin tool emits a request or retries it after interruption.
- **Then**: The router derives sender identity, validates operation/arguments and the full fence, and durably admits the request. The immediate tool result reports only queued/asynchronous status. Later results/errors correlate separately; fire-and-forget has no later response. Duplicates do not repeat effects; conflicting IDs reject. Unknown host results remain unknown rather than authorizing another launch. Failed operations and delivery are visible in complete history, not quiet history.

### Scenario: [MR-005] Managed and tmux mail
- **User Goal**: Exchange mail across worker sources without loss of ownership.
- **Given**: Managed and legacy workers share a router.
- **When**: Either sends mail or reconnects during delivery.
- **Then**: The router persists mail and its chosen source target. Managed delivery uses a fenced direct prompt and append receipt; tmux delivery uses its current WebSocket lease and acknowledgement. Stale leases/fences cannot acknowledge another incarnation. A timeout is not proof that Pi never appended the message. Restart All selects managed agents only.

### Scenario: [MR-006] Independent histories and retention
- **User Goal**: Read quiet messages without losing them to internal activity.
- **Given**: Managed sends, tool lifecycle activity, and an operator-configured canonical quiet recipient.
- **When**: Messages/activity arrive, the recipient configuration changes, or pruning runs.
- **Then**: Write-time quiet eligibility stays fixed; protocol traffic remains complete-only. Built-in invocation lifecycle updates belong to one persistent invocation entry. Current complete-history pruning keeps at most 5,000 entries per agent (and a separate null-agent bucket). Conversation retention defaults to 200 per pair and 30 days, pruned by the store sweep. This does not satisfy the historical complete cap of 200 without an approved disposition. Page limits are not retention guarantees. The historical missing-canonical-setting startup gate still needs independent acceptance evidence.

### Scenario: [MR-007] Structured state and snapshot repair
- **User Goal**: See which parent and child run is actually active.
- **Given**: A live UI connection and structured runtime telemetry.
- **When**: Work/compaction begins or ends, events are missed, or a parent process/session changes.
- **Then**: Initial/reconnected views use authoritative snapshots followed by deltas. Child identity includes parent session/process and task/run identity. Stale updates cannot attach to a new run. Missing active runs retain bounded interrupted uncertainty and stop elapsed clocks rather than becoming invented terminal results. Compaction clears on completion, error, disconnect, or reconciliation. Actual child-compaction telemetry and every recovery cause still need joined acceptance evidence.

Historical unresolved restriction: the source indicator must be the only user-visible distinction between tmux and managed sources. Current source-dependent controls need disposition against that requirement.

### Scenario: [MR-008] Fenced goal evaluation
- **User Goal**: Continue an unfinished goal without waking a replacement run by mistake.
- **Given**: A managed goal at a real settled boundary.
- **When**: The router claims an evaluator, receives a verdict, snoozes, disables, or replaces the runtime.
- **Then**: Evaluation is a hidden, tool-free operation with bounded input and exact DONE/NOT_DONE verdicts. Policy revision, evaluation, settled boundary, and full runtime fence correlate the outcome. Evaluator settlement does not recursively evaluate or rearm. Timeout/cancellation/stale results cannot finish an old claim. NOT_DONE wakeups remain atomically bound to the original evaluation/runtime, never retargeted. Snooze, due wakeup, meaningful-activity rearm, and explicit off remain required. Existence of evaluator tests alone does not close the exact historical race gate.

### Scenario: [MR-009] Provider authentication and model refresh
- **User Goal**: Change provider context without leaking secrets or using stale credentials.
- **Given**: A supported provider interaction and runtime-specific credential owner.
- **When**: The user logs in/out from the Web UI or chooses a model.
- **Then**: UI status is non-secret and models reflect the shared available context. For the historical Pi-managed contract, current work may finish, then the agent must reload changed credentials through same-session restart before more work. ChatGPT, Google, and Cursor via pi-cursor-sdk were the required minimum. Current embedded credential storage, catalog/default-model changes, and provider support are not assumed equivalent. Their parity and approval remain open in the crosswalk.

### Scenario: Claude startup safety failure
- **User Goal**: Understand why a Claude launch is blocked without exposing local paths.
- **Given**: Claude startup cannot persist its process identity, or preflight cleanup fails.
- **When**: The user launches Claude and subsequently retries.
- **Then**: Admission is fenced on the first launch and retry, reporting a sanitized cleanup or filesystem cause rather than sign-in guidance. A persistence failure is not reported as an unconfirmed process group unless group absence is actually unconfirmed.

### Scenario: [MR-010] Cross-origin managed controls
- **User Goal**: Use the router from any reachable frontend without browser identity gates.
- **Given**: Valid managed HTTP requests from unrelated HTTP(S) origins and a non-browser caller with no Origin.
- **When**: They preflight, launch, inspect state, or request lifecycle actions.
- **Then**: Permissive non-credentialed CORS transports the browser calls, including request/correlation headers. Origin is not identity or authorization; clients omit credentials. Invalid requests still fail normal validation. This does not change network exposure or waive the separate private bridge authentication protocol.

### Scenario: [MR-011] Bridge reconnect and bounded replay
- **User Goal**: Reconnect a bridge without repeating uncertain commands.
- **Given**: A reserved bridge identity, mutual proof, current runtime fence, and durable cursors.
- **When**: The WebSocket disconnects, commands/events replay, or retained replay data is missing.
- **Then**: Only current authenticated identities progress. Durable admission precedes acknowledgement; conflicting duplicates/stale frames reject. A replay gap enters reconstruction rather than pretending success. Bridge buffers/cursors and store ledgers remain bounded. This is an implemented optional transport seam, not a claim that all managed launches use it.

## 4. Automated E2E Verification Matrix

No tests were added or run for this docs-only backfill. These are existing evidence locations, not passing-run reports. **Every row still lacks a complete mapped Playwright browser + router + store + deterministic-model acceptance path.** Keep Draft until that gap is closed. Specific existing test names are given where verified; file-level evidence is deliberately labeled.

| Scenario | Existing evidence | Scope / remaining acceptance |
|---|---|---|
| MR-001 | `router-proxy/internal/transport/managed_launch_behavioral_functional_test.go`; `managed_launch_validation_behavioral_test.go` in the same directory | HTTP/store/controlled child process, not full browser/stock-Pi launch |
| MR-002 | `router-proxy/internal/managed/crash_recovery_behavioral_functional_test.go`; `router-proxy/internal/transport/managed_reset_native_test.go`; `managed_termination_native_test.go` | Controlled recovery and selected installed-Pi lifecycle paths, not joined browser crash matrix |
| MR-003 | `tests/mecha_rpc_manifest.test.ts`; `tests/mecha_rpc_native_pi_e2e.test.ts`; `pi-extensions/mecha-rpc/VALIDATION.md` | Extension/native proof boundaries; full router executor/add-only/child parity not implied |
| MR-004 | `router-proxy/internal/transport/domain1_behavioral_functional_test.go`; `managed_root_control_native_test.go` | Fake process/host seams and selected native goal status, not root tool to real tmux launch |
| MR-005 | `router-proxy/internal/transport/mixed_managed_legacy_behavioral_test.go`; `router-proxy/internal/managed/managed_delivery_append_ack_native_test.go` | Mixed transport and receipt recovery; no universal exactly-once external-effect proof |
| MR-006 | `router-proxy/internal/store/managed_history_test.go`: `TestManagedHistoryKeepsQuietEligibilityAtWriteAndCapsComplete`; `router-proxy/internal/transport/managed_history_retention_behavioral_test.go` | Store/HTTP retention, historical 200 vs current 5,000 mismatch remains |
| MR-007 | `router-proxy/internal/managed/child_snapshot_manager_test.go`: `TestChildDeltaReceiverAppliesArrivalOrderFromOneBindingFIFO`; `router-proxy/internal/store/managed_projection_snapshot_test.go`; `router-proxy/internal/transport/managed_children_test.go` | Structured snapshot/delta evidence, not all child-compaction/replacement browser cases |
| MR-008 | `router-proxy/internal/transport/managed_goal_evaluator_native_test.go`: `TestManagedGoalEvaluatorInstalledPiCoreFunctionalSlice`; `router-proxy/internal/store/managed_goal_evaluator_test.go`: `TestManagedGoalRestartRejectsLateOldCorrelatedEvaluatorResult` | Selected native/store slices. Exact NOT_DONE replacement race gate remains unverified here |
| MR-009 | `router-proxy/internal/managed/auth_test.go`: `TestProviderAuthCoordinatorConsumesPromptOnceAndCommitsOnlySuccess`; `tests/mecha_rpc_auth_e2e.test.ts` | Coordinator/extension evidence, not all providers and embedded/Pi credential-owner parity |
| MR-010 | `router-proxy/internal/transport/managed_http_test.go`; `router-proxy/internal/transport/http_test.go` | HTTP header/route evidence; two actual cross-origin browser launch paths not established here |
| MR-011 | `router-proxy/internal/transport/managed_bridge_server_functional_test.go`: `TestManagedBridgeServerPhase2BEventAdmissionDropReplaysExactlyOnce`; `router-proxy/internal/store/managed_bridge_test.go`; `router-proxy/internal/managedbridge/phase3b_test.go` | Composed bridge/store and process lifecycle fixtures, not all-runtime launch acceptance |

Retention owners: `router-proxy/internal/store/store.go` owns conversation sweeps; `managed_history.go` prunes complete history at write; `managed_bridge.go` bounds its ledger by 24 hours, 50,000 rows, and 64 MiB (live capacity can reject rather than erase needed replay); `managed_control_delivery_receipts_retention_test.go` covers terminal receipt pruning. Bridge defaults keep 128 event/cursor entries and bounded stderr. These limits are separate, not interchangeable guarantees for all histories.

### Subagent admission during shutdown

- When a child is admitted normally, its caller receives its actual queued or running admission status.
- If router shutdown closes task admission after the child enters running state but before execution starts, the spawn caller receives an error rather than a running response. The child eventually has a durable stopped outcome; this does not promise settlement before Close returns. Claude child admission follows the same behavior and releases its reservation when execution never starts.
