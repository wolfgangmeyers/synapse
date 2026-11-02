# Spec: Implicit Workflow Goals & Workflow-Level Snooze

## Current scoped validation

Wolfgang authorized the bounded retention remedy and supporting fixes, with explicit carry-to-completion authority and no hold. Older scope prohibitions, approval gates, failure dispositions and workflow phases below are historical snapshots, not current instructions.

Fresh post-QA-fix isolated browser validation passed 39/39 tests across four suites (344.322s), exit 0: no failures, skips, flakes or retries; source/dist drift 0 and owned survivors 0. Validated patch SHA256: `dfd51eafda05e90a9d3a4f3b0c73968e73acb2a772fdadacb9671af2de325f24`. Current Go verification passed Store 11 and managed 6 top-level tests; vet and build exited 0. These are scoped selections, not a full-repository run.

All six QA lanes completed; fixes independently accepted, including the semantics-equivalent dead-code subtraction, stale-owner notice negative regression and documentation corrections. The actual workflow advanced to OptionalCodeReview; no additional optional review was requested. This final status-only documentation update does not require another 39-case run.

Earlier browser 39/39 (327.369s), managed 16, Store retention/execution 32 and surviving guards 7 remain historical supporting evidence, not final-source reruns. Former stale-owner rows now pass; obsolete skips were retired with scoped replacements, not counted as passes. Earlier reds remain historical. No full-repository-suite, full-coverage, merge, integration, shipping or deployment acceptance is claimed; no commit or push is claimed.

Evidence: `/tmp/workflow-retention-qa-final-acceptance.md`; independent acceptances: `/tmp/workflow-retention-final-docs-verification.md`, `/tmp/workflow-retention-qa-notice-negative-verification.md`, `/tmp/workflow-retention-qa-subtraction-verification.md`. Historical evidence: `/tmp/workflow-retention-isolated39-final.md` and `/tmp/workflow-final78-collected-results.md`. The active 001–016 contract, A/D/R/H bounds, exact execution protection and UNKNOWN/no-replay fences remain unchanged.

## Latest retention contract addendum (supersedes historical retention holds)

Ordinary scoped implementation is authorized, spec first. Scenarios 001–013 remain
required. This addendum supersedes older indefinite-pending/one-closure retention
language, not UNKNOWN/no-replay safety. Current scoped validation is recorded above; this is not full-repository or deployment acceptance.

- All five evidence tables use immutable router user-admission A and monotonic
  existing-session expiry authority. Trustworthy router-time backfill, otherwise
  migration time once. No transcript mutation/pruning, new ordinals or tombstone ledger.
- New identities/calls/versions close inclusively at A+30d. New reservation effective
  saved D must be <=A+31d, including policy contributors; otherwise explicit denial,
  not clamping D or fabricated verdict. Pending lifetime R=max(A+30d,D)+30d
  (NULL D uses A+30d), so ordinary closure age <=61d.
- Preserve every accepted pending reservation and exact dependencies before R.
  Start only when D<=now<R plus existing fences (NULL D has no lower bound).
  At R atomically fence retention-expired admission, invalidate reserved work and
  prune. This explicitly changes indefinite acceptance. Overdue D alone is not
  retirement authority. No coalescing, early start, deadline restart or duplicate.
- Historical UNKNOWN/started state is not actual execution. Surviving fences reject
  replay/recreation after deletion, including repeated NOT_DONE. UNKNOWN never
  becomes failure/retry permission. Exact running evaluator can save its verdict
  after expiry but cannot create a missing continuation for an expired admission.
- Actual SDK/evaluator execution shares lane.serial. Queued claims are unprotected.
  One bounded validated JSON execution record on existing session rows stores kind,
  owner token, full source evaluation key, current SDK user key and exact referenced
  calls, reusing key structs. Parent coordinates schema/API with store/managed owners.
  Install evaluator authority after serial acquisition before I/O; continuation source
  atomically with started CAS; SDK user identity before provider/tool admission.
  Source/current may differ: at most two exact closures, ten five-table rows/session.
  Freeze referenced calls at expiry. Exact current execution is never force-stopped.
- Clear by exact execution identity/token on settlement; fence old owner effects
  before replacement. Lease expiry alone does not prove stopped execution. Restore
  cannot revive cutoffs/slot snapshots or cleared expired identities. Cold router
  restart is deployment assumption; no hot-rollout backfill. Old UNKNOWN is not
  surviving execution; new managers install authority before executing.
- Legacy A is backfilled once and saved D remains unchanged. Freeze migration
  H=max(migration UTC+61d, maximum valid legacy saved D+30d) on existing metadata.
  Legacy R=max(A+30d,D)+30d capped by H. H declares a finite database-specific
  drain bound, including extreme valid D, not a universal legacy 61d claim. No
  refreshed grace. Invalid authority cannot start and retires by H.
- Startup before recovery and every 60 seconds: immediate transaction advances
  fences, validates exact protection, invalidates and prunes atomically. Healthy
  cleanup lag <=60 seconds plus transaction execution. Downtime/locks delay physical
  deletion, never reopen admission. Preserve SQL identity/result guards/FKs/cascades.
- Startup/sweep pruning is implemented; real SQLite and Playwright validation remain
  required boundaries. Current scoped results are recorded above. The earlier
  prune-disabled instruction was a planning-time gate, not a current instruction.

## Historical recovery QA evidence (snapshot only)

This section records the earlier recovery snapshot. Its phase, scope, freeze, authorization, exclusions and pending statements are historical; current scoped validation and the latest retention contract supersede them.

Shared dirty `main@29dec4bf2fb05c075f9af9f56ec95fdc2cb14a1f`. Scenarios 001–013, invariants and scope unchanged. Production remains frozen. Previous “six reviewers next,” old33, old372 and old863 source-correlation claims below are explicitly historical, not current certification.

| Evidence | Snapshot fact / limit |
| --- | --- |
| Six QA dispositions | `/tmp/workflow-recovery-qa-{patterns,complexity,coverage,scope,requirements,documentation}.md`: all six dispositions independently accepted in `/tmp/workflow-recovery-qa-dedup-independent.md`. Patterns/complexity identify the same two duplicates, now fixed. Scope/requirements establish no additional scoped blocker. Coverage retains two deferred gaps below. Two optional WHY comments are not needed; no production thaw. Scoped QA only. |
| Minimal correction | `/tmp/workflow-recovery-qa-dedup.md`, `.diff`, `-before.json` / `-after.json`: exactly three production files under `router-proxy/internal/`: managed `embedded_goal_continuation.go`, `embedded_session.go`, store `embedded_router.go`; plus existing managed `embedded_workflow_settlement_test.go`. Shared name/fallback/exact-guidance helper preserves separate authority reads, bounded reason/full raw details and UNKNOWN/no replay. Shared snooze SELECT preserves query errors, identity predicates and exact-byte checks; workflow-only result conversion remains conditional. Existing test gains absent-ID/missing-row fallback assertions. Independent actual-patch review accepts both fixes. Maps to C/E/F, invariants 5/7, 008/009/012; no contract change. |
| Owner focused execution | **19 top-level passes** in successful bounded selections: store 7 (18.876s), tools 7 (14.932s), remaining invocation 2 (5.904s), boundaries 3 (41.989s), each actual exit 0. Commands `/tmp/workflow-recovery-qa-dedup-command`, `-rerun.command`, `-boundaries.command`; group `-store`, `-tools`, `-remaining-invocation`, `-boundaries` `.log` / `.exit`; aggregate `-log` / `-exit`. Initial managed outer timeout **124 remains incomplete historical evidence**, not overwritten or called green. Four baseline rows excluded. Not full-suite evidence. |
| Independent focused execution | `/tmp/workflow-recovery-qa-dedup-independent-{command,log,exit}`: actual **exit 0**, managed **2** top-level passes (14.368s), store **3** (13.723s). Real managed/SQLite fixtures, no external model calls. Earlier attempt package PASS logs without successful command exit artifact remain uncertified. Not every branch or full-suite evidence. |
| Fresh build / freeze | `/tmp/workflow-recovery-qa-build-verification.md`: vet/build/diff-check each **exit 0**; `/tmp/workflow-recovery-qa-{vet,build,diff-check}.{command,log,exit}`. Fresh **372 production paths** stable, aggregate SHA256 `e055b5d1534b30c9988d2745fd10d561b139f35fe35489c82df3dbc6d9883e7c`; `/tmp/workflow-recovery-qa-{before,after}.manifest.json`. Exactly three authorized production differences versus historical freeze. Built binary not deployed/restarted. |
| Fresh public E2E | FINAL `/tmp/workflow-recovery-qa-e2e.md`: **33 passed (4.3m), actual exit 0**, all33 individual results, no failed/skipped/interrupted/retried cases. Parent read and accepted report, all33 log, command and exit0. Exact `/tmp/workflow-recovery-qa-e2e.{command,log,exit}`, `-individual-results.txt`, `-result.json`. Runner and command shell exited before this refresh. Real browser/REST/WS/proxy/router/SQLite, deterministic local external-provider mock, no external LLM. Old33 remains historical. |
| Fresh source identity | `/tmp/workflow-recovery-qa-e2e-{before,after}.json`: expanded **874** paths, aggregate `8578a83afce526ce0ff0faf97575069ce55098aaf8c3341e0a030ccf2b108238`. `-863-{before,after}.json`: fresh historical-path-set aggregate `fedad676d7c965c77462c0aa5e9331e5bd3f143243e6d61e59d7984b33d05a6f`. `-372-production-{before,after}.json`: 372 aggregate `e055b5d1534b30c9988d2745fd10d561b139f35fe35489c82df3dbc6d9883e7c`. Zero boundary drift. `-manifest-summary.json` records 863/372; `-result.json` records expanded874. Aggregates hash sorted JSON path→content-hash maps, not pretty manifest-file bytes. |
| Historical drift / cleanup | `-historical-diff.json`: exactly **six historical-content changes: three production plus three Go tests**, no E2E TypeScript drift. Production paths above; tests: managed `embedded_workflow_settlement_test.go`, `embedded_workflow_snooze_tool_test.go`, store `workflow_snooze_recovery_test.go`. Old863 equality/no-production-drift statements are historical only. Automatic cleanup: **285 observed owned process identities, 33 observed disposable roots, one Chromium profile gone**. `-owned.json`, `-processes-{before,after}.json`, `-result.json` preserve observations and no newly surviving disposable router/proxy/root. Sampled inventory, not every arbitrarily short-lived process or machine-wide leak guarantee. No manual kills. |
| Coverage disposition | **Two Should Add DEFERRED, nonblocking, NOT covered**: idle late authority-read caller suppression (no deterministic seam confirmed; no hook authorized), and workflow-specific wrong SDK/content/name/id/canceled checked append-observe specialization (existing seam available). Shared goal/alias fences and workflow after-domain assertions are supporting, not equivalent specialization. Former DONE rollback and absent/removed-workflow authority gaps have inspected supporting assertions. Historical 17.6% is selected-test coverage, not current combined/full coverage. |
| Unwaived holds | Four baseline owner failures: `TestEmbeddedGoalInvocationCompletionBoundaries/{goal,goal_snooze}/{stale-owner-append,stale-owner-observe}`. Three internal skips: `TestEmbeddedGoalContinuationOriginalKeyAuthority`, `TestEmbeddedGoalContinuationDomainReceiptAuthority`, `TestEmbeddedGoalContinuationUnknownFailClosed`. **All UNWAIVED. Retention SHIPPING FAIL UNWAIVED**, no authorized remedy; owner deletion is not bounded live-owner retention. No integration/shipping acceptance, full-suite/full-coverage or cold-restart claim. |
| Actual parent acceptance / phase | Parent accepted scoped QA and fresh scoped E2E. Actual transition **QA → Optional Code Review SUCCEEDED**. Current phase **Optional Code Review**; user prompted per state whether additional review is wanted. No additional review requested yet, no Done. Earlier actual Implementation → End to end testing → QA transitions remain historical facts. No release acceptance or transition performed by this docs refresh. |

Exactly three docs refreshed: this recovery plan, authoritative `docs/specs/workflow-implicit-goals.spec.md`, existing local `SPEC.md` (untracked, never commit). Preserve all background-workflows hunks and `PERSISTENT_WORKFLOW_SNOOZE_PLAN.md` untouched/excluded. No production/tests/hooks/retention changes, test execution, services/restart, staging, commit/push or background edits. Earlier evidence below remains explicitly historical, not fresh execution or retroactive TDD proof.

## Historical pre-QA final evidence ledger

This section records the earlier recovery snapshot. Its phase, scope, freeze, authorization, exclusions and pending statements are historical; current scoped validation and the latest retention contract supersede them.

This ledger supersedes earlier pending/failure/status blocks, which remain historical. Shared dirty `main` HEAD: `29dec4bf2fb05c075f9af9f56ec95fdc2cb14a1f`. All 001–013 scenarios, invariants and mandates remain unchanged. Saved evidence is not new execution or retroactive TDD proof. Parent scoped Implementation/E2E acceptance and executed transitions to QA are recorded below, not full-suite or shipping acceptance.

| Evidence | Snapshot fact / limit |
| --- | --- |
| Final scoped review | `/tmp/workflow-recovery-final-review-ledger.md` validates **five production plus two E2E-audit findings resolved**. None discarded as hallucinations or nits. Static follow-up/source correlation, not independent reruns or full-suite/shipping acceptance. Earlier review-pending statements are superseded. |
| Public E2E | `/tmp/workflow-takeover-audit-final.{command,log,exit}` and `/tmp/workflow-recovery-final-e2e-collection.md`: **33 passed (4.1m), exit 0**, no failed/skipped/interrupted/retried cases. Real browser/REST/WS/proxy/router/SQLite, deterministic external model mock. No cold-process-restart proof. Earlier 27-case failure and 32-pass run remain historical. |
| E2E source / cleanup | The original run's **863** paths matched before/after manifest SHA256 `b545ed2b425e2276f0050cf5a65772fe0c6d8bf0e4255344d1db380916d00892`. Independent 071 verification now finds exactly two expected Go-test drifts: `router-proxy/internal/managed/embedded_workflow_snooze_tool_test.go` and `router-proxy/internal/store/workflow_snooze_recovery_test.go`; **no production or E2E TypeScript drift**. The 863-path snapshot is historical, not current whole-tree equality. Saved cleanup observed 46 owned PIDs, seven disposable roots and Chromium profile gone; observation began mid-run, not a complete root inventory or fresh machine-wide leak audit. No E2E rerun. |
| Bounded Go verification / production freeze | `/tmp/workflow-recovery-final-verification-summary.txt` and final command/log/exit artifacts: store goal **28**, store workflow **33**, managed **29**, WS **5** top-level passes, each exit 0; vet/build/diff-check exit 0. **372 production Go/migration/module paths** stable at command boundaries, aggregate SHA256 `8358b2d81ddf4a88b58963de4e3353a2a9a319c82975f9ef316d4e71a2b9a364`, independently correlated by final review. Production remains frozen; later test changes do not establish a new combined test snapshot. Built binary not launched. WS checks are in-process, not browser/proxy proof. Earlier 47-pass timeout remains incomplete; no full-suite claim. |
| Exclusions versus internal skips | Exactly four managed baseline-failing rows are excluded: `TestEmbeddedGoalInvocationCompletionBoundaries/{goal,goal_snooze}/{stale-owner-append,stale-owner-observe}`. Separately, `store-goal.log` internally skips `TestEmbeddedGoalContinuationOriginalKeyAuthority`, `TestEmbeddedGoalContinuationDomainReceiptAuthority`, and `TestEmbeddedGoalContinuationUnknownFailClosed`. **All remain unwaived**; four exclusions does not mean no other skips. Baseline owner-fencing failures are not fixture noise. Marker SQLITE_BUSY baseline 2/5 versus current 0/5 is not a fix/rate claim. Stream-hang initiating assertion remains uncertain and current run drifted. Broader suites remain unaccepted. |
| Coverage analysis / validation | Fully read `/tmp/workflow-recovery-final-coverage-analysis.md` and `/tmp/workflow-recovery-final-coverage-validation.md`: bounded, timed-out/partial original analysis, not complete skill traversal or full-file/historical-test review. **17.6%** is the earlier selected-test atomic profile (`16195c603f38cf73e955393a09ab99c97566d77e859a319e161703078aa480b1`), **not combined final-test coverage**. Validation does not independently recertify percentages/profile hashes or later owner work. `TimeoutStuckLaunches` is unchanged: changed-span attribution is false, not a production coverage gap. |
| DONE rollback assertion added | `/tmp/workflow-completion-rollback.{command,log,exit}`: **exit 0, nine top-level passes, 18.653s**. New `TestRecoveryCompletedGoalRetirementFailureRollsBack` has three fault rows: preference retirement, reserved-continuation invalidation, final policy update. Read assertions in `router-proxy/internal/store/workflow_snooze_recovery_test.go`: disposable SQLite abort; error and changed=false; full before/after snapshots of evaluation/call/preference/continuation/policy rows equal; evaluation UNKNOWN with empty completion timestamp; no partial COMPLETED. Setup establishes active preference, reserved/pending continuation and enabled policy with original D. Parent has read the new assertions. This supplies the formerly missing blocking rollback regression evidence, not a newly observed/fixed production defect or formal acceptance. |
| Managed coverage addenda corrected | `/tmp/workflow-managed-coverage-addenda.log` remains **FAILED (79.642s)**: both new denial rows hit `goal invocation already executed`, not intended `sql: no rows in result set`. Corrected TEST-ONLY fixture removes redundant consuming precheck, records fresh invocation/process generation/provider-turn entry, and captures policy after supported unbind cleanup. `/tmp/workflow-managed-coverage-corrected.{command,log,exit}`: **exit 0, two top-level passes, 4.028s**, both new `remove-binding-after-check={false,true}` rows plus adjacent execution/bypass success. Read exact assertions: callback reached, `sql.ErrNoRows`, full policy/deadline preserved, zero calls/preferences, no commit/completion hook, UNKNOWN retained, fabricated completion/re-admission rejected. No weakened assertions or observed production bug. Corrected focused success is not broad/full-suite success. |
| Independent focused verification | **071 completed** `/tmp/workflow-recovery-addenda-verification.md`, read in full with exact command/log/exit sources `/tmp/workflow-addenda-independent-{store,managed}.{command,log,exit}`. **Store nine top-level passes, 16.550s; managed three top-level passes, 11.575s; both exit 0.** Store includes all three new DONE rollback faults and adjacent lifecycle/completion/fence tests. Managed includes corrected denial, execution/bypass and exact-settlement tests. Independent assertion review confirms concrete fault rollback and domain denial, not every SQL branch. Earlier incomplete 00e provides no acceptance. No full suite, coverage run or percentage recertification. |
| Independent hashes / drift | `/tmp/workflow-addenda-independent-checks.json` and `.before.json` / `.after.json`: **372 frozen production paths unchanged**; final-manifest artifact SHA256 `358d1770f9839a7b7bd5b0c1958fbc31e692c897e0906bb1dc97b443e01ad977`. Historical 863-path manifest artifact SHA256 `079f771f8466da3ed77b157db2ef8b51bff2dc0a611f2935708bc98070a48560`, exactly the two Go-test drifts above. Whole-file test hashes stable across independent runs: managed `5d4b6a568e208253eae14e15d52e55aa0b1660d8ba46ec3448609479621c0e4c`; store `625bbf0d1ee5ec1f7a5fc5fd7482a0c3f38176863db0016f7a0870fa3eba5500`. Manifest artifact hashes differ from aggregate path-content hashes. These checks precede this final docs-only refresh in `WORKFLOW_SNOOZE_RECOVERY_PLAN.md`, `docs/specs/workflow-implicit-goals.spec.md`, and existing `SPEC.md`; those doc edits were allowed, not independently reviewed by 071. |
| Deferred / supporting coverage | Idle empty-prompt late-read-error caller suppression: **Should Add DEFERRED, nonblocking**. No existing deterministic seam identified after initial read; no production hook authorized. Not covered and not a required-missing blocker. Existing post-start/read-error and formatter cases are separately covered, not proof of this idle guard. Shared wrong-SDK fences remain nonblocking supporting evidence, not exhaustive workflow specialization. |
| File audit / scope | `/tmp/workflow-recovery-final-file-audit.md` maps files to A–G / 001–013 or supporting blockers, not correctness approval. Migration **119 unchanged baseline**, **120 only new migration**, narrow preference-trigger exception. Preserve supporting guards. |
| Retention / acceptance | `/tmp/workflow-recovery-retention-disposition.md`: shipping gate **FAILS, unwaived**. Owner deletion does not bound live-owner evidence; unchanged tables/no retry multiplier does not establish bounded accumulation. **No retention remedy authorized.** Parent accepted scoped **Implementation** (approved recovery changes and supporting assertions) and scoped **End to end testing** (saved **33 E2E passes** plus independent source correlation; no rerun needed with unchanged production/TypeScript). Parent reports actual transitions executed: **Implementation → End to end testing → QA**. **Current phase: QA. Six independent reviewers next per workflow state**, not yet claimed completed. Not full-suite, shipping, merge, integration or deployment acceptance. Four baseline failures and three internal skips remain unwaived. |

Public 013 proves saved call-clock D, both-alias replacement and expired-policy pacing, not unequal competing deadlines. Both unequal-future maximum orderings have supporting store assertions per final review/coverage analysis. DB corruption/fault fixtures are supporting boundaries, never public E2E fault APIs. No full-suite, full-coverage or cold-restart claim.

Scope exclusions unchanged: preserve/exclude every `tests/e2e/background-workflows.spec.ts` hunk; local `SPEC.md` remains untracked/uncommitted; `PERSISTENT_WORKFLOW_SNOOZE_PLAN.md` untouched/excluded. This factual refresh edits only this ledger in the plan, authoritative spec and local `SPEC.md` (the existing local spec, not a new `localSPEC.md`). This docs-only refresh makes no production/test edits, test execution, service changes, staging, commit/push, workflow transition or retention remedy. Actual parent-executed transitions and scoped acceptance are recorded above.


- **ID**: `SPEC-WORKFLOWS-002`
- **Status**: `Active` (behavioral contract; scoped execution evidence above, independent acceptance separate)
- **Domain**: `Workflows`
- **Target E2E Suites**: `tests/e2e/workflow-implicit-goals.spec.ts`, `tests/e2e/workflow-evidence-retention.spec.ts`, `tests/e2e/workflow-evidence-expiry.spec.ts`, `tests/e2e/goal-empty-breaker.spec.ts`

## 1. Intent & User Outcome

A workflow agent keeps one locked goal: `Complete active workflow: <workflow name>`. States have no individual goals. Settlement keeps the workflow in progress without an auxiliary model decision.

`workflow_snooze` delays autonomous continuation until a durable agent-level deadline. User messages, mailbox deliveries, and reminders still run. Their settlement neither cancels nor restarts the remaining wait. Workflow transitions retire the old snooze; exit/deactivation restores ordinary goal control.

## 2. Core Invariants

1. **Implicit goal and lock.** Activation enables the workflow-derived goal and lock, using workflow snooze configuration. Manual set/off through REST, WS, and native goal controls is rejected while a workflow is active (REST 409). Intermediate turns and internal transitions preserve goal text, enabled state, lock, and revision. Evaluators cannot complete an active workflow.
2. **Configuration and compatibility.** Workflow `snooze_duration_ms` is positive, default 600000. State goals are absent from schema, REST, editor, and exports. Legacy import discards state goals and defaults missing/non-positive workflow duration to 600000.
3. **Optional native arguments.** `workflow_snooze` accepts optional integer `duration_minutes`, optional integer `duration_ms`, and optional string `reason`. Native JSON schema allows an empty object and either duration independently. Positive minutes precede positive milliseconds. With neither positive, use workflow configuration, otherwise 600000. Clamp effective duration to 1000–86400000 ms. Reject unknown fields, wrong types, fractions, and integers not representable by the existing integer argument boundary without snooze mutation. A valid representable oversized positive duration clamps, rather than failing merely because conversion would overflow: bound minutes before multiplying by 60000. No numeric-format redesign is required. Reason is metadata, not wake/completion authority.
4. **Alias boundary.** Existing `goal_snooze` remains supported in workflows with workflow default and persistent agent-deadline protection. Per `embedded_goal_snooze_tool.go:24–130`, retain its existing argument handling: omitted duration uses the active workflow duration, and positive overrides are capped at that configured duration, itself capped at 86400000 ms. Cap unification and new outside-workflow behavior are out of scope, not unresolved recovery requirements.
5. **Deterministic settlement and exact prompt.** Active workflows settle as `NOT_DONE` without either injected or production auxiliary goal evaluation. Continuation input is exactly `The workflow <workflow name> is currently active in state <state name>. You must either make progress towards completing the workflow or call workflow_snooze.` Substitute names without added quotes. No verdict headers or evaluator-inconclusive suffix. Existing fallback names are `Workflow` and `active` if name lookup fails; name lookup failure does not authorize auxiliary evaluation.
6. **Durable agent delay.** Successful snooze records deadline D using the store clock and reports persisted deadline, effective duration, and workflow name. A new user/mailbox/reminder turn can finish without another snooze call; its own evaluation/reservation must still honor D. Combine relevant future agent-policy and valid saved turn-preference deadlines using the latest. Do not recompute a saved deadline from the later settlement time. Other agents, obsolete revisions, stale sessions, and superseded workflow-state evidence cannot extend the current wait.
7. **Timestamp and failure handling.** NULL, empty, expired, or malformed policy `snoozed_until` establishes no future snooze. Parse timestamps and normalize accepted values to canonical store format, rather than ordering arbitrary text. A malformed saved continuation deadline is denied at the existing start boundary, not treated as permission to start. Database errors are not absence: do not report successful persistence or silently schedule early after a read/write failure. Denied/stale invocations do not mutate policy. Preserve existing fenced invocation and unknown-outcome/no-replay semantics; no new retry protocol is specified.
8. **Timing, pacing, and one admitted start.** No autonomous continuation starts before the effective future deadline. At expiry an otherwise eligible idle worker receives one admitted start for the current reservation. Repeated timer/idle/recovery dispatch cannot start that reservation twice. This is not exactly-once external execution: an unknown provider outcome cannot authorize replay. A genuinely new settled turn can reserve new work. Unsnoozed/expired work follows normal existing pacing and caps; zero-delay continuation is not required. Pending mail, blocked state, subagents, termination, ownership, revision, and semantic-head safeguards remain authoritative.
9. **Lifecycle cleanup.** Accepted internal transition clears old snooze while preserving implicit goal/revision/lock. Exit/deactivation clears binding, goal, lock, and snooze. Ordinary explicit-goal completion clears snooze. Retired preferences/reservations/timers cannot restore the delay or start obsolete work. Rejected transitions/exits preserve binding and snooze. Do not silently describe failed cleanup persistence as successful cleanup.
10. **Bounded evidence and no replay.** The latest retention contract and SCN-WFIG-014 govern the five evidence tables: immutable A, inclusive A+30d admission closure, immutable bounded D, pending R, frozen legacy H and exact execution protection. Owner deletion alone is not a live-owner retention bound. Pruning never grants replay or converts UNKNOWN/started evidence into retry authority. Historical owner-lifetime-only shipping failures below describe the earlier snapshot, not a current authorization prohibition.

## 3. User Scenarios (Behavioral Contract)

### Scenario: [SCN-WFIG-001] Configure workflow snooze without state goals
- **User Goal**: Configure workflow cadence without fragmented goals.
- **Given**: The browser is on Workflows.
- **When**: The user creates a workflow, selects 15 minutes, edits it, and opens a state editor.
- **Then**: Default is 10 minutes; label is `Snooze (minutes)`; 900000 ms persists across edit. State editor, REST, and export contain no state goal.

### Scenario: [SCN-WFIG-002] Activate and lock the implicit goal
- **User Goal**: Keep the worker focused on workflow completion.
- **Given**: A workflow has configured duration.
- **When**: An agent enters through supported launch/assignment paths and manual set/off is attempted through REST, WS, and native controls.
- **Then**: Goal is enabled and locked with inherited duration. Manual control fails (REST 409) without changing goal/revision.

### Scenario: [SCN-WFIG-003] Preserve goal across native transition
- **User Goal**: Advance states without losing focus.
- **Given**: State A has an active implicit goal with recorded revision.
- **When**: An allowed native transition advances to B.
- **Then**: B guidance is delivered; goal text/enabled/lock/revision remain unchanged. REST reactivation alone is not proof of native transition.

### Scenario: [SCN-WFIG-004] Snooze survives intermediate turns
- **User Goal**: Handle input without premature autonomous work.
- **Given**: A real workflow turn snoozes for five minutes, finishes, and settles with future deadline D.
- **When**: Before D a user message, mailbox delivery, or state reminder runs and finishes without another snooze (exercise each source).
- **Then**: The intermediate turn has its own applied receipt, `NOT_DONE`, and reserved continuation joined by full turn identity. Its deadline remains D. No second preference or autonomous start appears. Goal text/enabled/lock/revision remain unchanged. Input is not blocked. Unsnoozed/elapsed control cases use normal continuation pacing, not a fresh snooze delay.

### Scenario: [SCN-WFIG-005] Exit or deactivate workflow
- **User Goal**: Restore ordinary goal control.
- **Given**: An active workflow has a locked implicit goal.
- **When**: Native exit from an exit-permitted state or administrative REST deactivation succeeds.
- **Then**: Workflow/state references clear, goal becomes disabled/empty, lock clears, and manual goal control works. Snoozed cleanup variants are in 010.

### Scenario: [SCN-WFIG-006] Import legacy bundles
- **User Goal**: Reuse older definitions.
- **Given**: A legacy v1 bundle includes state goals and missing/non-positive workflow snooze duration.
- **When**: It is imported through a supported bundle boundary.
- **Then**: Import succeeds, duration is 600000, state goals are discarded and absent from REST/export. This ID remains legacy import, not continuation persistence.

### Scenario: [SCN-WFIG-007] Native optional defaults and overrides
- **User Goal**: Use configured or explicit bounded duration.
- **Given**: A real managed workflow exposes the tool with default/custom configuration.
- **When**: Calls omit all arguments, supply either/both durations, use non-positive overrides, exercise bounds, and use the compatibility alias.
- **Then**: Native schema admits intended omissions. Effective duration, precedence, bounds, alias cap, persisted deadline, and reported result follow invariants 2–6. Parser-only tests are insufficient.

### Scenario: [SCN-WFIG-008] Settle without auxiliary evaluation
- **User Goal**: Get useful guidance without an extra model decision.
- **Given**: A worker is in an active workflow.
- **When**: A real turn settles and a valid continuation is admitted.
- **Then**: Durable verdict is `NOT_DONE`; the goal stays active; continuation input matches invariant 5 exactly. Provider request evidence excludes auxiliary evaluator calls, not ordinary worker model requests.

### Scenario: [SCN-WFIG-009] Expiry starts one current continuation
- **User Goal**: Resume when the wait ends.
- **Given**: Real snooze/intermediate turns produced current reservation D and the worker is otherwise eligible/idle.
- **When**: Shared fakeclock advances to D−1 ms, then +1 ms, followed by repeated dispatch observations for that reservation.
- **Then**: Zero starts before D; one admitted start/provider request at expiry; no duplicate start for that reservation. Later independent reservations are distinguished. Existing safeguards/pacing are not disabled.

### Scenario: [SCN-WFIG-010] Retire snooze on workflow lifecycle changes
- **User Goal**: Avoid carrying old waits into new or finished work.
- **Given**: A workflow has a snooze and reservation.
- **When**: Native transition, native exit, or REST deactivation succeeds (exercise all).
- **Then**: Old snooze clears and old evidence cannot restore it. Transition preserves goal/revision/lock; exit/deactivation clears goal/lock. Advancing beyond old D cannot start obsolete work; valid new-state work remains allowed.

### Scenario: [SCN-WFIG-011] Clear snooze on ordinary goal completion
- **User Goal**: Stop nudges after explicit goal completion.
- **Given**: A non-workflow explicit goal is snoozed.
- **When**: A real turn completes it through the deterministic external evaluation mock.
- **Then**: Policy snooze clears and obsolete reserved work cannot start after former D. Active workflows still cannot be evaluator-completed.

### Scenario: [SCN-WFIG-012] Reject invalid public operations
- **User Goal**: Avoid accidental changes after errors.
- **Given**: A workflow has a valid binding and optional snooze.
- **When**: The real native tool boundary receives malformed duration arguments or a forbidden native transition/exit is requested.
- **Then**: Admission/tool denial is observable and valid binding, goal, and snooze remain unchanged. Exercise through the local provider tool-call response and observe browser/WS/REST state; parser-only denial is not E2E proof. Stale-owner and DB failure fixtures are separate supporting evidence below.

### Scenario: [SCN-WFIG-013] Preserve and replace saved public deadlines
- **User Goal**: Honor the latest accepted snooze without restarting elapsed delays.
- **Given**: Real accepted `workflow_snooze` and compatibility `goal_snooze` calls save call-clock deadline D to policy and the active same-turn preference atomically.
- **When**: The turn settles later without another snooze, additional accepted calls replace D with earlier/later deadlines (exercise long→short and short→long), or policy D expires before a subsequent turn settles.
- **Then**: Settlement preserves saved D, never settlement-time plus duration. Each new accepted call replaces policy and the active same-turn preference with its own returned D, including a shorter wait. Expired policy introduces no fresh hold; normal pacing still applies. Observe real tool results, settlement/reservation and clock boundaries, with read-only DB evidence. Multiple calls prove replacement, not unequal-deadline maximum selection. The mandatory latest-valid-future invariant 6 remains separately required through the supporting store fixture below. Corrupt timestamps and SQL failures also remain separate supporting checks.

### Scenario: [SCN-WFIG-014] Automatically expire evidence without stopping current execution
- **User Goal**: Bound long-lived evidence while preserving uncertain outcomes and accepted deadlines within their explicit lifetime.
- **Given**: A real workflow produces historical evidence, arbitrary-N pending reservations in one unchanged session/revision, queued UNKNOWN claims, and actual SDK/evaluator execution. Router/store share deterministic time.
- **When**: Automatic startup/60-second cleanup crosses A+30d and R, including reopen, cold restart, and later fresh work.
- **Then**: New old-turn identities/calls/versions reject at A+30d. All pending closures survive until R with original D/NOT_DONE/call; D never starts early or refreshes and start is at most once. At R unstarted reservations explicitly expire and cannot replay/reappear. Ordinary closure age is <=61d; legacy drain uses frozen H. Historical UNKNOWN is not retry permission. Exact current source/current closures survive without execution termination, then prune after settlement. Transcript remains intact and fresh work operates normally. Out-of-envelope new D is explicitly denied, never clamped.
- **Error/edge proof**: Real SQLite tests cover inclusive A+30d/D/R, arbitrary-N, policy deadline envelope, legacy extreme D/H and malformed authority, exact source/current distinction, queued claims versus executing evaluator, late verdict without new continuation, repeated NOT_DONE/replay after deletion, backward clock/reopen/lifecycle restore, owner fencing, races/transaction rollback, SQL guards/cascades/FK integrity. These are supporting tests, not browser proof. Playwright exercises real browser/REST/WS/proxy/router/SQLite with deterministic external provider mocks and injected time: automatic expiry, recovery before R, denial after R, actual execution preservation and fresh work. Current scoped results are recorded above; independent QA remains in progress.

Retired tests are not passes. Replacement Store checks do not restore obsolete blanket lineage suppression or establish Manager-routing E2E. The retired heartbeat test asserted a nonexistent ticker; no replacement runtime-heartbeat coverage is claimed.

## 4. Automated E2E Verification Matrix

Preceding snapshot isolated run: 39 passed across the four metadata-listed suites (33 + 2 + 2 + 2), exit 0; that snapshot's source and cleanup evidence is recorded above. The matrix maps existing executable tests, not independent requirement/assertion acceptance or full coverage. Earlier 33-case runs and reds remain historical.

| Scenario | Required observable proof | Current evidence / pending |
| --- | --- | --- |
| SCN-WFIG-001 | Configuration/edit/default/export; no state goals | Implicit-goals suite passed in isolated39; independent assertion/coverage audit pending |
| SCN-WFIG-002 | REST/WS/native lock denial and revision preservation | Implicit-goals suite passed in isolated39; independent assertion/coverage audit pending |
| SCN-WFIG-003 | Native transition and unchanged implicit goal/revision | Implicit-goals suite passed in isolated39; independent assertion/coverage audit pending |
| SCN-WFIG-004 | Separate user/mailbox/reminder identities, same D, no early start | Implicit-goals suite passed in isolated39; independent assertion/coverage audit pending |
| SCN-WFIG-005 | Native exit/deactivation and restored ordinary controls | Implicit-goals suite passed in isolated39; independent assertion/coverage audit pending |
| SCN-WFIG-006 | Legacy import drops state goals and defaults duration | Implicit-goals suite passed in isolated39; independent assertion/coverage audit pending |
| SCN-WFIG-007 | Actual optional schema/defaults/overrides and alias configured cap | Implicit-goals suite passed in isolated39; independent assertion/coverage audit pending |
| SCN-WFIG-008 | Exact prompt, NOT_DONE and no auxiliary evaluator requests | Implicit-goals suite passed in isolated39; independent assertion/coverage audit pending |
| SCN-WFIG-009 | D−1/D boundary and no duplicate current-reservation start | Implicit-goals suite passed in isolated39; independent assertion/coverage audit pending |
| SCN-WFIG-010 | Transition/exit/deactivation retire old snooze and timers | Implicit-goals suite passed in isolated39; independent assertion/coverage audit pending |
| SCN-WFIG-011 | Ordinary completion clears snooze; workflow cannot evaluator-complete | Implicit-goals suite passed in isolated39; independent assertion/coverage audit pending |
| SCN-WFIG-012 | Public invalid-input/forbidden-operation denial preserves state | Implicit-goals suite passed in isolated39; independent assertion/coverage audit pending |
| SCN-WFIG-013 | Real native/alias saved D, shorter/later replacement and expired pacing | Implicit-goals suite passed in isolated39; independent assertion/coverage audit pending |
| SCN-WFIG-014 — automatic retention | Real browser/REST/WS/proxy/router/SQLite; automatic A+30d admission closure and R pruning without manual cleanup, exact current execution preserved, all accepted pending D unchanged before R/no early or duplicate start, explicit R expiry, fresh work; read-only SQLite confirms expired unprotected rows removed | `workflow-evidence-retention.spec.ts`: same-DB reopen/future D/recovery notice and inclusive 30-day sweep (2 passed); `workflow-evidence-expiry.spec.ts`: pending R−1/R/unblock and held exact source/current past R then settlement/pruning (2 passed). Scoped results, not exhaustive edge coverage |
| SCN-WFIG-014 — internal safety support | Real SQLite Go coverage of replay/admission rejection, inclusive cutoff, immutable time, bounded closure, late completion, rollback/reopen/clock/lifecycle recovery, locking and FK integrity | Supporting Store retention/execution 32 and surviving guards 7 top-level passes, preceding late test-only fixture refreshes; NOT browser E2E or every-final-source-test proof |
| SCN-WFIG-015 | Saved cold-recovery reply in complete history and fresh browser | Existing same-DB reopen test in `workflow-evidence-retention.spec.ts` asserts the recovery reply before/after reload; included above, not an extra test |
| SCN-WFIG-016 | Empty/whitespace: two keyed nudges, third-turn halt, repeated idle wakes and new user work | `goal-empty-breaker.spec.ts`: empty and whitespace cases, 2 passed in isolated39; supporting Manager checks remain separate |

### Required validation boundaries

- Real browser, REST, WS, router-proxy, router, and SQLite with deterministic local mock only at the external model boundary. Launch/run/send through supported boundaries. Observe browser/WS lifecycle and REST state; do not merely start unused components.
- Regression SQLite access is read-only. Never SQL-seed receipts, evaluations, preferences, or continuations. Join by agent/session/epoch/generation/user sequence/digest/goal revision and final receipt identity. Prove a new intermediate turn, new `NOT_DONE`/reservation, same D, no extra preference, unchanged goal revision, and no early start.
- Reuse existing opt-in build-tagged loopback provider seam and isolated synthetic credentials. Start the SSE mock before the embedded router. No real LLMs, external services, production credentials, or new production seam.
- Existing executable fakeclock wiring: `cmd/mailbox-router/main.go:306–307` gives the store the test clock and `:370–371` gives the manager the same clock; `internal/transport/http.go:1515` handles `POST /v1/test/clock/advance`. Continuation scheduling uses that clock's `AfterFunc` in `embedded_goal_continuation.go`. This supports Playwright expiry without a new production hook. Use fakeclock, not five-minute wall waits. Bounded polling observes asynchronous settlement. Managed tests sharing store/manager fixed clock support timing/duplicate/error/corruption evidence; they do not replace Playwright.
### Validated reachability and reject-only WS002 guard

Earlier read-only inspection recorded these boundaries, not latest-source passing behavior: managed `embedded_workflow_snooze_tool.go` and `embedded_goal_snooze_tool.go` call store `SaveCurrentEmbeddedWorkflowSnoozePreference` / `SaveCurrentEmbeddedGoalSnoozePreference`. Store `embedded_goal_snooze.go:179–342` uses one immediate transaction, computes D from `s.clock.Now()`, overwrites the active same-turn preference with a higher version, and mirrors D into policy. Store `embedded_goal_continuation.go:188–239` selects only the current evaluation turn's fully fenced active preference, then considers policy. Prior-turn preferences do not compete.

No independent public embedded policy writer yielding unequal relevant policy/preference deadlines was established. `managed/direct_controls.go:17–28` routes embedded controls to `embedded_browser_controls.go:37–40`, which accepts only `goal.control`, not `goal.snooze`. The legacy `managed_goal_snooze.go` / store `managed_goal_policy.go:SnoozeManagedGoalPolicy` fenced path is not such an embedded writer. `SetManagedGoalSnoozeUntil` is a store helper, not a public route (the inspected non-test caller clears policy on transition). Thus public long/short snoozes demonstrate replacement, not max selection. This boundary correction is not a policy waiver or behavior weakening: invariant 6 remains fully mandatory, including unequal relevant deadlines.

Historical inspection status: WS002 implementation is now present under owner `14ce`: `router-proxy/internal/transport/websocket.go` has a reject-only workflow guard after source lease/role validation and before legacy target lookup. It matches the exact target `managed.pi_session_id` and returns the existing `WORKFLOW_ACTIVE` error with `request_id: frameID`. This does not add general embedded WS dispatch or require post-deactivation WS success. Focused supporting WS tests in new `websocket_workflow_goal_test.go` passed (2.429s), including stale/blank/agent-ID-alias targets, non-UI callers, bad leases, and no-workflow cases. Focused existing/new WS checks subsequently passed under verifier `de3244`; independent full public coverage remains pending. The mandatory WS set/off lock contract is unchanged.

Historical inspection status: Parent authorized this factual boundary correction only. Existing corrected-plan approval at 02:09Z and full plan/spec reread at 02:12Z remain recorded, not expanded into acceptance. No code/tests/services/commit/push/branch actions in this pass. Coverage remains pending/unverified.

### Supporting store checks (not Playwright coverage)

The checks remain mandatory supporting evidence, separate from public E2E. Final bounded Go selections pass as recorded above; independent assertion/coverage mapping is pending. Historical inspection gaps below are superseded in run status, not retroactively proved by inspection.

| Check | Existing executable boundary | Evidence status |
| --- | --- | --- |
| Unequal relevant future deadlines (mandatory invariant 6) | Disposable store fixture at `reserveEmbeddedGoalContinuation` in `embedded_goal_continuation.go`; use valid current-turn preference/call evidence and independently set policy to earlier/later future instants, exercise both orderings and assert latest parsed future instant without settlement-time restart; keep stale/expired evidence excluded | Final file audit identifies both-ordering fixtures in workflow_snooze_recovery_test.go; final store selection passes. Independent assertion audit pending; not public 013/E2E proof |
| Reservation DB failure / rollback | Store `embeddedGoalContinuationFault("before_reserve")`, exercised in `embedded_goal_continuation_test.go` and `embedded_goal_continuation_due_test.go` | Final store/managed selections include rollback/read-error checks; independent assertion/gap audit pending, not E2E |
| Invalid saved continuation due | `StartEmbeddedGoalContinuation` timestamp validation in `embedded_goal_continuation.go` with store fixture | Final selection includes malformed continuation denial; independent assertion audit pending |
| Malformed policy deadline | Store reservation resolution / manager eligibility with supporting fixture | Final selection includes canonical/malformed/expired policy checks; independent assertion audit pending |
| Stale invocation | `CheckEmbeddedGoalInvocation` / `SaveCurrentEmbeddedGoalSnoozePreference` fenced store boundary | Final selection includes stale/replay/uncertainty guards; independent audit pending; former four stale-owner rows now pass in the recorded supporting selection |
| Owner-removal cleanup / rollback | `CompleteManagedTermination`, `managedTerminationFault("before_terminalize")`, `embedded_goal_termination_data_test.go` | Existing supporting pattern, not a long-lived-table retention bound |

DB fixtures may deliberately establish unequal relevant deadlines or induce corruption/failure only for these supporting checks. They must not seed the normal Playwright regression's continuations or count as its coverage. No executable public DB-failure/corruption injection route was established; no new production hook is authorized. Complete latest-source supporting coverage remains unverified and separate from every public scenario 001–013; no new public fault-injection requirement is added. The historical owner-lifetime-only retention failure is superseded by the authorized bounded retention contract and current scoped results above; no shipping acceptance is implied. The mandatory 100% applicable-scenario E2E requirement is not satisfied by this document or by supporting fixtures. All 001–013 real-boundary tests remain required. The snapshot below records bounded results and unresolved failures, not complete passing coverage.

## Historical recovery status snapshot (superseded by current final ledger; docs-only, shared main `29dec4bf`)

This section records the earlier recovery snapshot. Its phase, scope, freeze, authorization, exclusions and pending statements are historical; current scoped validation and the latest retention contract supersede them.

Implementation is not accepted. Original store `0db3d629`, managed `92165413`, and WS `14ce` work is complete. Follow-up store owner `cf931` implements the parent-approved preference repair; managed follow-up `1c43` is complete. E2E `1ab` failed with stream `UNKNOWN` and was replaced by active owner `99fce`. Triage follow-up `36241536` is active. These are handoff statuses, not proof of the latest shared source.

| Evidence | Result and limit |
| --- | --- |
| Independent verifier `de3244` | Completed: `/tmp/workflow-recovery-verification-summary.txt`. Focused store/managed coverage green (69s wall), build/vet and focused WS/legacy checks green. Full store timed out at 720s; full managed had failures and timed out at 480s. No full-suite green or latest-source claim. |
| Controlled triage | `/tmp/recovery-suite-diagnosis/report.txt`: eight managed failures reproduce identically on clean HEAD. The 5000-row store seed times out at 100s in both copies, but does not explain all 720s. Compaction owner failure reproduces in both; original stream hang does not. Marker `SQLITE_BUSY` remains unclassified/baseline-unproven. `36241536` follows up; no suite waiver. |
| Managed follow-up `1c43` | Complete: bounded reason/long-name handling, DB errors fail closed, workflow commit uncertainty. `/tmp/managed-review-fixes/green-targeted-final.log`: targeted PASS, 10.157s. Independent `788ed` review found no blockers in scoped static review only, not runtime acceptance. |
| Store follow-up `cf931` | In progress: narrow migration 120 trigger exception and fenced shared-save repair (below). `/tmp/store-preference-red.log` records red evidence. Focused repair green and independent latest-source validation remain pending. Plan/addendum belongs to `cf931`; not edited here. |
| Public E2E | `/tmp/workflow-snooze-full.log`: 27 cases, 21 passed / 6 failed, NOT green. Alias, separate user/mailbox/reminder, same-SDK-turn, and actual shorter-D proof remain pending under `99fce`. Earlier individual greens do not establish complete coverage. |
| Independent coverage `387` | Snapshot audit, not final acceptance. Public alias gap assigned to `99fce`; workflow-commit gap now has a managed test added. Addition is not independent passing/latest-source coverage proof. |

### Narrow preference repair scope

Migration 083 makes an application-only repair impossible. Parent approved only a new forward migration `120` replacing `embedded_goal_snooze_preference_transition`: no old migration edits or new tables; newly accepted calls can increase retained evidence versus HEAD. Store `cf931` implements same-SDK-turn inactive→active reactivation only for a new fully fenced higher-version call, plus retirement of superseded other-turn preferences on a successful new call. Preserve immutable identities, replay/unknown-outcome fences, retirement semantics and owner cascade. No broader trigger relaxation or public contract weakening is authorized. Scope maps to B/D/F (012/013) in the recovery plan; its addendum remains `cf931`'s responsibility.

Existing added-path mapping remains: store argument helpers → B; store/managed browser controls and atomic lifecycle cleanup → A/F; managed settlement/continuation → C/E; retirement and uncertainty fixtures → F; transport REST cleanup → F and reject-only WS guard → A/F. Suite and suite-local `tests/e2e/workflow-implicit-goals-harness.ts` belong to `99fce` for real-boundary A–G proof. Every changed file still needs final criterion/blocker audit.

All invariants and scenarios 001–013 remain required. Supporting fixtures and historical focused artifacts do not prove complete public E2E coverage or latest shared source. Exact source/artifact correlation, final coverage/review and acceptance remain pending.

Retention shipping gap remains unresolved and unwaived: owner-lifetime cleanup is not a numeric age/count bound for long-lived agents. No retention subsystem is authorized. Background-test hunks stay preserved/excluded; `PERSISTENT_WORKFLOW_SNOOZE_PLAN.md` stays untouched. Local `SPEC.md` must never be committed.

This pass edits only `docs/specs/workflow-implicit-goals.spec.md` and local `SPEC.md`. No plan/code/test edits, test execution, acceptance, workflow advancement, services/restarts, commit or push.

### Recovery notice history addendum

#### Scenario: SCN-WFIG-015 — Saved recovery reply survives fresh history loading
- **User Goal**: Read the assistant's actual recovery acknowledgement after reconnecting or reloading.
- **Given**: An entered turn was interrupted and a cold router restart admits its one-shot recovery notice under the current owner.
- **When**: The assistant's recovery reply is checked and saved successfully.
- **Then**: That same reply appears exactly once as an assistant-to-user message in durable complete history, is published through the existing history notification path, and remains available to a fresh browser/history reader. The checked SDK reply is not appended again to manufacture visibility.
- **Edge/error contract**: A failed or stale-owner final append must not publish a successful reply. Notice recovery retains its existing no-goal-evaluation/no-applied-receipt semantics; repairing history does not reserve or force another continuation or change a saved continuation deadline.
- **Verification**: `tests/e2e/workflow-evidence-retention.spec.ts` cold-reopen notice complete-history and fresh-browser assertions (owned by the E2E workstream); supporting real-Manager regression `TestEmbeddedColdInterruptedNoticeFinalHistory` checks cold SQLite reopen, checked interrupted-wake final, exactly one durable/published reply, execution clearing, and absent goal evaluation/receipt/continuation.

### Scenario: [SCN-WFIG-016] Empty-response breaker owns its bounded nudges
- **User Goal**: An enabled ordinary goal must not spin forever without action.
- **Given**: No workflow is active, and the worker returns no text or only whitespace without actionable tool execution (setting the goal alone is not progress).
- **When**: Three consecutive empty turns settle.
- **Then**: The first two empty turns each receive one keyed actionable re-nudge; the third pauses the loop. The auxiliary goal evaluator is not called for these empty turns. Idle goal wake offers must await the claimed empty evaluation and its keyed nudge, never replace it with a fresh prompt or bypass the third-turn breaker. Genuine incoming user/mail or explicit-prompt work resets the prior streak before that work executes, never after its own empty final has been counted; its first empty final counts as one of the three turns. This also applies when that incoming work sets the goal through the native goal tool. New work retains normal reset behavior; a nonempty/actionable turn clears the empty streak.
- **Edge/error**: Both empty content and whitespace-only text obey this bound. A failed/unknown keyed verdict is not permission for an idle fallback retry. Existing owner, semantic-head, snooze and execution fences remain authoritative.
- **Verification**: Supporting real Manager/SDK/SQLite case `TestEmbeddedGoalCircuitBreakerEmptyAssistantHalts`; `goal-empty-breaker.spec.ts` empty and whitespace browser cases passed in isolated39. Independent QA remains in progress; this is scoped evidence, not complete coverage.
