# Workflow snooze recovery plan

## Current scoped validation

Wolfgang authorized the bounded retention remedy and supporting fixes, with explicit carry-to-completion authority and no hold. Older scope prohibitions, approval gates, failure dispositions and workflow phases below are historical snapshots, not current instructions.

Fresh post-QA-fix isolated browser validation passed 39/39 tests across four suites (344.322s), exit 0: no failures, skips, flakes or retries; source/dist drift 0 and owned survivors 0. Validated patch SHA256: `dfd51eafda05e90a9d3a4f3b0c73968e73acb2a772fdadacb9671af2de325f24`. Current Go verification passed Store 11 and managed 6 top-level tests; vet and build exited 0. These are scoped selections, not a full-repository run.

All six QA lanes completed; fixes independently accepted, including the semantics-equivalent dead-code subtraction, stale-owner notice negative regression and documentation corrections. The actual workflow advanced to OptionalCodeReview; no additional optional review was requested. This final status-only documentation update does not require another 39-case run.

Earlier browser 39/39 (327.369s), managed 16, Store retention/execution 32 and surviving guards 7 remain historical supporting evidence, not final-source reruns. Former stale-owner rows now pass; obsolete skips were retired with scoped replacements, not counted as passes. Earlier reds remain historical. No full-repository-suite, full-coverage, merge, integration, shipping or deployment acceptance is claimed; no commit or push is claimed.

Evidence: `/tmp/workflow-retention-qa-final-acceptance.md`; independent acceptances: `/tmp/workflow-retention-final-docs-verification.md`, `/tmp/workflow-retention-qa-notice-negative-verification.md`, `/tmp/workflow-retention-qa-subtraction-verification.md`. Historical evidence: `/tmp/workflow-retention-isolated39-final.md` and `/tmp/workflow-final78-collected-results.md`. The active 001–016 contract, A/D/R/H bounds, exact execution protection and UNKNOWN/no-replay fences remain unchanged.

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


## Scope and authority

Recover existing workflow snooze with maintained persisted specs and behavioral E2E coverage. The original recovery scope excluded retention expansion. Wolfgang's completion mandate supersedes that exclusion for the bounded retention remedy and supporting fixes; other exclusions remain.

Wolfgang explicitly replaced the parent's workflow with **Simple Dev Plan**. No replaced dev-loop PR/preview gate, PR, new branch/worktree, definition change, or recreation of old gates. Parent's implicit goal is already enabled and locked. Do not manually set a goal.

Historical approval record: the earlier correction pass edited only this plan, `SPEC.md`, and `docs/specs/workflow-implicit-goals.spec.md`, without production/test edits, tests, services, commits, pushes, deployment, or workflow advancement. Independent spec rereview `782aac91` completed with no blocking contradictions. Independent plan reviewer `89bcbf43` found no validated technical blocker and four nonblocking factual/status corrections. Parent approved the corrected plan at 02:09Z and reread the full plan and spec at 02:12Z. Implementation is authorized and underway under Simple Dev Plan. Coverage remains pending/unverified, not accepted or proved passing. This record-only update does not execute implementation, tests, services, commits, pushes, deployment, or branch changes.

Authoritative contract: `docs/specs/workflow-implicit-goals.spec.md`, `SPEC-WORKFLOWS-002`, scenarios 001–016. Maintain it through implementation. `SPEC.md` remains local/uncommitted, without gitignore changes. Existing `docs/specs/INDEX.md` entry is present. Read AGENTS.md, workflow-management skill, behavioral standard, actual git diff and existing implicit-goal/tool/persistent-snooze plans. This plan supersedes conflicting recovery instructions only.

## Historical checkout and ownership (initial inspection)

Rechecked `main` at `29dec4bf2fb05c075f9af9f56ec95fdc2cb14a1f`. Earlier reported baseline: 12 dirty tracked files and three untracked. Actual inspection: 13 dirty tracked and four untracked, including spec writer's authoritative spec diff and `SPEC.md`. This plan adds one untracked file.

Tracked dirty files:
- `router-proxy/internal/managed/`: `embedded_goal_continuation.go`, `embedded_session.go`, `embedded_workflow_transition.go`, `embedded_workflow_transition_test.go`.
- `router-proxy/internal/store/`: `embedded_goal_continuation.go`, `embedded_goal_continuation_due_test.go`, `embedded_goal_evaluation.go`, `embedded_goal_snooze.go`, `embedded_router.go`, `native_tools.go`.
- `docs/specs/workflow-implicit-goals.spec.md`, `tests/e2e/background-workflows.spec.ts`, `tests/e2e/workflow-implicit-goals.spec.ts`.

Existing untracked: `PERSISTENT_WORKFLOW_SNOOZE_PLAN.md`, `SPEC.md`, managed `embedded_workflow_snooze_tool.go` and `embedded_workflow_snooze_tool_test.go`.

Dirty does not establish ownership or approval. Preserve unrelated hunks/files. No reset, stash, blanket restore/add, or overwrite. Recheck HEAD/status before implementation/staging. Original implementation ownership on main (current handoff/status below): store `0db3d629` owns `router-proxy/internal/store/**`; managed `92165413` owns `router-proxy/internal/managed/**`; E2E `3089d90c` owns `tests/e2e/workflow-implicit-goals.spec.ts` and its suite-local helper. Parent owns integration and conditional transport changes. Independent reviewer owns acceptance review. A local Codex protocol mock needs no Codex worker launch.

## Acceptance map

Numbers mean `SCN-WFIG-xxx`.

| Criterion | Required outcome | Scenarios |
| --- | --- | --- |
| A | Preserve default 600000 ms/custom configuration, absent state goals, implicit goal/revision/lock through intermediate turns/native transitions. Reject manual REST/WS/native set/off. Preserve legacy import. | 001, 002, 003, 005, 006 |
| B | Actual native schema admits `{}`, either integer duration, omitted reason. Positive minutes precede positive ms; otherwise workflow default then 600000. Clamp workflow_snooze effective duration to 1000–86400000 ms. Reject invalid types, fractions, unknown keys and unrepresentable numbers before mutation. | 007, 012 |
| C | Active workflow settles NOT_DONE without injected or production auxiliary evaluator call. Preserve ordinary goal evaluation. Exact prompt below. | 008, 011 |
| D | Policy snoozed_until persists across new user/mailbox/reminder identities. Latest relevant valid future policy/saved-preference deadline wins. Never rebuild saved D at settlement. Parse instants and canonicalize. | 004, 013 |
| E | D−1 ms: zero autonomous starts. At D: one eligible admission for current reservation. Duplicate dispatch cannot restart it. Preserve pacing/caps, ownership/revision/head, blocked/mail/subagent/termination checks and unknown-outcome no-replay fences. | 009, 012, 013 |
| F | Transition retires snooze without goal reset. Exit/deactivation clear binding/goal/lock/snooze. Explicit-goal completion clears snooze. Retired evidence/timers cannot revive work. Failures/denials never report false success. | 003, 005, 010, 011, 012 |
| G | Bound the five evidence tables under SCN-WFIG-014 while preserving pending reservations until R, exact running execution and surviving UNKNOWN/no-replay fences. | 014; invariant 10 |

Exact continuation input, without added quotes/headers/suffix:

`The workflow <workflow name> is currently active in state <state name>. You must either make progress towards completing the workflow or call workflow_snooze.`

Existing name fallbacks: `Workflow` / `active`. Name lookup failure cannot authorize auxiliary evaluation. Policy/workflow DB failure cannot silently authorize unsnoozed work.

Validate finite integral representability before conversion. Valid oversized minutes clamp **before multiplication**, using the maximum threshold. Invalid/unrepresentable numbers reject, not clamp/default. Test both separately.

Preserve `goal_snooze` current argument shape, workflow default and override cap: configured duration capped at 24 hours, not an unconditional 24-hour override allowance. Require durable cross-turn protection for the alias. Do not invent cap parity or outside-workflow denial features.

Keep agent policy authoritative across turns, with turn preferences as fenced evidence. NULL/empty/expired/malformed policy values impose no future hold. Malformed continuation deadlines remain denied at start. Missing rows differ from query errors. Persist preference/policy/reported outcome consistently through existing fenced transactions. Do not weaken argument/result checking to accept merely `ok:true`.

## Historical recovery candidate files (original scope inventory)

This is the original recovery inventory; current forward migrations also include 121–123.

Approved implementation scope under the ownership split above. Every changed file must map to a criterion. Additional paths require a validated blocker and plan update.

| Paths | Minimal work | Criteria |
| --- | --- | --- |
| This plan; authoritative spec | Maintained plan/evidence and scenario matrix. Maintain approval/scope facts without implying verified coverage. | A–G |
| managed `embedded_workflow_snooze_tool.go`, `_test.go` | Optional schema, safe numbers/defaults, fenced persistence/results. Test native schema, not only Execute. | B, D, F |
| managed `embedded_session.go` | Registration/admission and deterministic settlement. Failed snooze calls must not imply success. | B, C, F |
| managed `embedded_goal_continuation.go` | Exact prompt and necessary timer retirement. | C, E, F |
| managed `embedded_workflow_transition.go`, `_test.go` | Error-aware transition/exit cleanup preserving goal/revision/lock. | A, F |
| store `embedded_goal_snooze.go`, `embedded_router.go` | Atomic policy/call evidence; original argument and exact result validation through existing fences. | B, D, F |
| store `embedded_goal_continuation.go`, `embedded_goal_continuation_due_test.go` | Saved deadline maximum/canonicalization, query errors, eligibility/single-start proof. | D–F |
| store `embedded_goal_evaluation.go` | Explicit completion cleanup, no active-workflow completion. | C, F |
| store `native_tools.go`, `workflow_tools.go` | Catalog/whitelist consistency, no new escape-tool policy. | B |
| `tests/e2e/workflow-implicit-goals.spec.ts` | Real scenarios 001–013, replace fabricated regression, browser/REST/WS/read-only DB assertions. | A–G |
| Suite-local E2E helper | Reuse isolated embedded options, clock and proxy through suite-local composition. No shared harness/model/default change. | D, E |
| managed `embedded_sustained_durability_test.go` | Supporting shared-clock, duplicate, cleanup and uncertainty proof, never E2E replacement. | D–G |

Managed/store paths above are under `router-proxy/internal/`. Validated lifecycle APIs are in store `managed.go`; `managed_workflow_state.go` does not exist. Store `workflow_tools.go` owns the whitelist. Managed alias/lifecycle boundaries include `embedded_goal_snooze_tool.go` and `embedded_workflow_admin_tools.go`, with supporting tests as needed. Parent alone owns conditional `transport/managed_http.go`, only if the store contract cannot absorb REST cleanup. Existing evaluation/invocation tests support faults. Retention work is authorized by the completion mandate and governed by the authoritative spec's latest retention contract.

No UI production edit: `Snooze (minutes)` already exists. No implicit-goal redesign; only the approved migration-120 exception below. Leave old untracked persistent plan untouched and excluded unless separately assigned.

## Historical unsupported-test assessment (original recovery scope)

Added E2E006 SQL-seeds agent/policy/session/evaluation/continuation, copies D into its own INSERT, then reads D back. It proves neither reservation code nor cross-turn recovery. During implementation remove only that recovery-owned test and unused DatabaseSync/randomUUID imports. Replace with real **004**. Reserve **006** for legacy import.

Background retention diff adds a 200 ms sleep, busy_timeout=5000, synchronous spin-wait/lock-message retry, paired insert retry and delete retry. No controlled evidence connects them to snooze recovery. Paired inserts can partially succeed and duplicate on retry. Current wrapper is lock-message-specific, not retry-any; do not broaden it.

Confirm hunk ownership against HEAD and parent/history. Remove only confirmed recovery-owned sleep/busy-timeout/retry edits. Background hunk authorship remains unproven: preserve/exclude and report. The earlier correction pass removed nothing; this record-only update removes nothing. Direct SQL pruning is not product retention proof.

## Historical approved recovery implementation order

1. Parent approved the corrected plan at 02:09Z and reread the full plan and spec at 02:12Z after completed independent reviews. Implementation is underway with the owners recorded above. Capture HEAD/status/hunk ownership. No PR/preview gate or manual goal set.
2. Write failing native-schema/numeric, deadline, atomic-error and cleanup tests. Add real Playwright 004 before behavior edits. Inherited dirty production code is unverified: capture honest pre-recovery red evidence without resetting the dirty checkout. Never claim retroactive TDD.
3. Repair tool validation/persistence (B/F), settlement/prompt (C), deadline resolution (D/E), then lifecycle retirement (F). Run matching tests after each step. Preserve ordinary evaluation and existing fences.
4. Complete 001–013 and supporting faults/corruption tests. Planned commands: `(cd router-proxy && go test ./internal/store ./internal/managed)`; `npm run test:e2e -- tests/e2e/workflow-implicit-goals.spec.ts`. Broaden relevant transport/catalog/lifecycle and Playwright checks based on actual diff. These commands are planned evidence, not results recorded by this update.
5. Independent coverage/diff review. Map every scenario to actual executable assertions and every file to A–G/blocker. Resolve findings and rerun affected checks. Unsupported hunks never enter staging.
6. After authorized implementation acceptance, parent commits scoped hunks on main, excludes SPEC.md/unrelated work, pushes origin/main and verifies local HEAD equals `git ls-remote origin refs/heads/main`. Record both SHAs. No commit/push in this assignment.

## Real-boundary E2E

Use Chromium actions/rendered lifecycle, real REST and WS through router-proxy, router/managed runtime and SQLite. Assert WS notifications/REST state, not just unused components. Disposable homes/DBs/ports/synthetic credentials; teardown processes/data on failure.

Reuse `embedded_restart_test_only`, `EMBEDDED_RESTART_FAKE_ENABLE=1`, literal-loopback `EMBEDDED_RESTART_FAKE_ENDPOINT`. Inspected seam supports `openai-codex/gpt-6-sol` and `gpt-6.1-terra`. Start deterministic local Codex SSE mock before isolated embedded router. Capture all requests and reject unexpected ones. No external LLM/services or production credentials. Use existing startIsolatedRouter embedded options; generic setupE2EHarness alone does not establish provider isolation. Verify proxy/WS paths.

Shared fakeclock is confirmed in main.go: store Clock at 306–307, manager Clock at 370–371. transport/http.go handleTestClockAdvance and harness advanceClock already exist. No new production seam needed.

004: real five-minute workflow_snooze tool call, final answer, persisted returned D. Deliver user steering/mailbox/reminder turns separately through supported inputs. Mock finals without new snooze. Each source must create new turn/final receipt, new NOT_DONE/reservation, same D, no new preference, unchanged goal/revision/lock, zero early starts. Read-only SQLite joins agent/session/epoch/generation/user_seq/user_digest/goal_revision and final receipt identity. Never seed receipts/evaluations/preferences/continuations.

008: provider request logs prove only expected worker requests and zero auxiliary evaluation. Assert exact prompt and no verdict/inconclusive text. Helper tests alone are insufficient.

009: advance to D−1 ms, condition-poll settlement, assert zero starts. Advance 1 ms to D, observe one eligible admission with normal pacing. Exercise duplicate timer/idle/recovery dispatch, assert no repeat of that reservation. Distinguish later legitimate turns. No five-minute wall waits/arbitrary sleeps. Managed fixed-clock proof supports but never replaces Playwright.

010/011: native transition/exit, supported deactivate, explicit completion and rejected operations while snoozed. Advance beyond old D; old actionable evidence/timers cannot revive work. Transition preserves goal revision/lock.

### Separate failure evidence

Executable Playwright boundaries: native schema/tool admission for malformed/unknown/fractional inputs; REST/WS/native goal-lock denial; invalid native transition/exit; mock provider request accounting and controlled response/connection failures. These do not prove SQLite write atomicity.

Supporting fixtures: existing `embeddedGoalContinuationFault("before_reserve")` for reservation transaction failure; stale session/revision invocation fixtures; disposable SQLite fault triggers for policy-write/cleanup rollback; corrupt policy/continuation fixture values for parse/start denial. Confirm exact fixture/test paths before editing. These are supporting fault/corruption tests, **not E2E**.

DB-failure/corruption checks are supporting fixtures, not Playwright proof. They are separate from every public scenario 001–013, not unmet public fault-injection scenarios. No new public fault-injection requirement or production fault endpoint is added. No SQL-seeded passing E2E, policy waiver, or complete-coverage claim.

Public 013 must use real `workflow_snooze` / `goal_snooze` calls to prove saved call-clock D, no settlement-time restart, earlier/later replacement (long→short and short→long), and expired-policy normal pacing. Calls atomically mirror D to policy and overwrite the active same-turn preference; continuation reads only the current turn's preference. Sequential calls prove replacement, not unequal-deadline max selection. No independent public embedded policy writer yielding unequal relevant deadlines was established.

Historical inspection status: The latest-valid-future maximum in criterion D/invariant 6 stays fully mandatory. Require a separate disposable store fixture with valid current-turn call/preference evidence and unequal future policy D, both orderings, asserting max by parsed instant and no settlement-time restart. This supporting evidence is unverified, not public 013/E2E coverage. Canonical timestamp, malformed policy/continuation, query-error and stale-evidence checks remain supporting and unverified. Real 004 remains primary regression.

Validation paths: managed `embedded_workflow_snooze_tool.go` / `embedded_goal_snooze_tool.go`; store `embedded_goal_snooze.go:179–342` (shared immediate transaction, call-clock D, preference upsert/policy mirror), `embedded_goal_continuation.go:188–239` (current-turn evidence and policy max); managed `direct_controls.go:17–28` / `embedded_browser_controls.go:37–40` (embedded control supports goal.control only); `managed_goal_snooze.go` / store `managed_goal_policy.go:SnoozeManagedGoalPolicy` (legacy fenced path, not an independent embedded writer). Store `SetManagedGoalSnoozeUntil` is not a public API. Inspection validates reachability, not runtime/test success.

Historical inspection status: WS002 implementation is now present under owner `14ce`: `router-proxy/internal/transport/websocket.go` has a reject-only workflow guard after source lease/role validation and before legacy target lookup. It matches the exact target `managed.pi_session_id` and returns the existing `WORKFLOW_ACTIVE` error with `request_id: frameID`. This does not add general embedded WS dispatch or require post-deactivation WS success. Focused supporting WS tests in new `websocket_workflow_goal_test.go` passed (2.429s), including stale/blank/agent-ID-alias targets, non-UI callers, bad leases, and no-workflow cases. Legacy WS tests and independent full coverage remain pending. The mandatory WS set/off lock contract is unchanged.

Historical inspection status: Parent authorized this factual spec/plan boundary correction, not a policy waiver or behavior weakening. Prior plan approval at 02:09Z and full reread at 02:12Z remain unchanged. This pass owns only the three docs, with no code/tests/services/commit/push/branch actions. Coverage stays pending.

### Scoped WS002 transport repair (14ce9e4d)

Parent authorized reject-only `transport/websocket.go` plus narrow WS transport tests on shared main, no commit/push/restart. Validated blocker: embedded workers have no `r.sessions` target, so legacy resolution returned `target_not_connected` before workflow lock enforcement. Match nonempty current `ManagedAgentRecord.PiSessionID` exactly to `target_instance_id`; never substitute agent ID, register a fake session/lease, or add embedded dispatch. After source lease/role validation, set/off against this identity with either active workflow binding returns existing error protocol `code: WORKFLOW_ACTIVE`, `request_id: <frame_id>`. Existing frame-to-control correlation remains unchanged. Stale/empty target identities and non-workflow agents continue to legacy resolution.

File mapping: `transport/websocket.go` → A/F denial without mutation; new `transport/websocket_workflow_goal_test.go` → A/F set/off full policy/binding preservation plus source/stale-target checks; `transport/transport_ordering_test.go` → A legacy correlation regression fixture only. The latter needs explicit `GoalControlAvailable: true`: cloned template DBs set empty migrations, hiding capability discovery and hanging existing goal tests waiting for results. No product capability behavior changed.

Evidence: `/tmp/ws-workflow-red.log` records set/off expected WORKFLOW_ACTIVE versus actual target_not_connected before guard (supporting checks passed); `/tmp/ws-workflow-green.log` records focused green. Final focused existing+new control command: `cd router-proxy && go test ./internal/transport -run '^(TestControlResultsCorrelateToExactSessionsLeasesAndControlIDs|TestGoalResultAfterTargetReconnectUsesDurableCorrelation|TestStaleGoalResultCannotSatisfyNewerPendingControl|TestGoalControlCurrentManagedWorkflowRejectsWithoutMutation|TestGoalControlManagedWorkflowPreservesSourceAndTargetChecks)$' -count=1 -timeout=30s -v`, PASS 1.786s (`/tmp/ws-legacy-focused.log`). Broad `-run 'Goal|ControlResults'` timed out and included an ordinary-goal visibility failure and missing native `MECHA_PI_SOURCE_LOCK`; not claimed green. Public WS E2E remains separate owner 36634d4d's work, not established by supporting transport tests.

## Historical owner-lifetime retention assessment (superseded by bounded retention contract)

Reuse existing policy and evidence tables; no new accumulating table or recovery-attempt multiplier. Timer rearm replaces only the per-agent timer, not a reservation update. Accepted distinct calls/turns retain evidence under the existing storage model. Newly admitted `workflow_snooze` calls and migration-120 same-turn reactivation can add immutable call evidence versus HEAD (reactivation reuses the preference row); deterministic NOT_DONE paths can also reserve newly accepted work. Unchanged storage shape is not unchanged accumulation.

Migration `083_embedded_goal_owner_cascade.sql` protects `embedded_goal_applied_receipts`, `embedded_goal_evaluations`, `embedded_goal_continuations`, `embedded_goal_snooze_calls`, and `embedded_goal_snooze_preferences` until actual owner deletion. `CompleteManagedTermination` supplies transactional owner deletion/cascade, with rollback guards. Goal off/reset, workflow exit and merely terminated desired state retain history. Terminal-subagent cleanup defaults to 24 hours, but does not bound ordinary long-lived owners. `goal_control_desired_state` pruning and detached archive limits do not bound these five tables.

The retention shipping gate **FAILS, unwaived**: no finite live-owner age/count bound or cleanup deadline is established. Implementation and independent validation may continue, not shipping. No retention remedy is authorized. Do not delete immutable/UNKNOWN/start evidence, weaken guards, or add a retention subsystem to manufacture compliance. An approved bounded remedy, implementation, tests and documented limit are required before shipping. This disposition records evidence and a shipping gate, not a new recovery behavior promise. Source: `/tmp/workflow-recovery-retention-disposition.md`.

## Historical evidence ledger and completion (superseded above)

This section records the earlier recovery snapshot. Its phase, scope, freeze, authorization, exclusions and pending statements are historical; current scoped validation and the latest retention contract supersede them.

| Gate | Required record | State |
| --- | --- | --- |
| Spec | Writer resolves review; independent rereview disposition | Rereview 782aac91 complete; no blocking contradictions |
| Plan | Independent findings/resolution, parent approval before code | Review 89bcbf43 complete with minor corrections; parent approved corrected plan 02:09Z, reread full plan/spec 02:12Z; implementation underway |
| TDD | Baseline/candidate identity, commands, red/green output | Reported honest reds and focused greens; exact red artifact/candidate correlation and broad checks incomplete (snapshot below) |
| Behavioral coverage | Independent scenario → test/assertions/artifact audit, browser/WS/REST/DB joins, mock requests | Pending |
| Supporting coverage | Numeric/error/fence/clock tests and explicit E2E limits | Pending |
| Retention | Owner-deletion cleanup, no live-owner numeric bound | Superseded: retention shipping gate FAILS unwaived; no unchanged-accumulation claim |
| File audit | Every changed file → criterion/blocker, unrelated hunks excluded | Pending |
| Main integration | Scoped commit/push, local/remote main SHA match | Future implementation only |
| Production | Separate restart/deployment approval, actual executable provenance, approved smoke proof | Not authorized |

Current running router is an older binary, not candidate verification. Earlier SPEC.md process/hash observations were not revalidated here. Disposable tests build candidate source. Production install/restart needs separate approval before action, then executable hash/build provenance and approved smoke evidence. Code verification and deployment remain separate states.

Historical correction-pass gaps (superseded in status by the snapshot below, not all still untouched): actual schema optionality, durable atomic/error/result integrity, saved-deadline repair, lifecycle retirement, real cross-turn/expiry coverage, supporting DB-failure/corruption proof, pre-existing live-agent retention-policy gap, background hunk ownership. None is marked verified by this plan.

## Historical recovery status snapshot (superseded by current final ledger; bounded docs-only update)

This section records the earlier recovery snapshot. Its phase, scope, freeze, authorization, exclusions and pending statements are historical; current scoped validation and the latest retention contract supersede them.

Store `0db3d629` and managed `92165413` finished edits at 02:25Z. This records implementation and focused evidence, not final acceptance. Original E2E owner `3089d90c` left the assignment incomplete and was replaced by `36634d4d`. Verifier `de3244` owns pending full managed/store/combined coverage. Independent audit `4e991` is active. No final review, integration, or shipping acceptance is claimed.

| Evidence | Historical status / limit |
| --- | --- |
| TDD / recovery checks | Honest red failures and subsequent focused greens are reported. No retroactive TDD claim for inherited dirty code. Exact red commands/output and candidate identities still need verifier correlation. Broad checks remain incomplete. |
| Managed focused | `/tmp/managed-recovery-green.log`: exit 0, 33.066s. Log tail checked in this docs pass. Supporting evidence, not full managed coverage. |
| Store focused | `/tmp/store-final.log`: exit 0, 96.291s. Log tail checked in this docs pass. Final addenda and full store verification remain PENDING. |
| REST independent focused | `TestManagedAgentWorkflowDeactivateAtomicHTTP`: green, 5.130s. Related focused checks green, 7.008s (reported evidence, not rerun here). |
| E2E | Original owner reported 004/008/009 green. 011 failed at the fixture. Remaining scenarios and full suite remain pending under `36634d4d`. Initial greens are not independent complete scenario acceptance. |
| Full checks / audit | Managed/store/combined coverage pending `de3244`. Audit `4e991` active. No complete applicable-scenario coverage claim. |

Validated added paths, all under `router-proxy/internal/`, map to the existing criteria (not expanded behavior):

- Store `workflow_snooze_arguments.go` (B: shared numeric/default validation), `managed.go` (A/F: atomic lifecycle cleanup), `embedded_browser_controls.go` and `embedded_browser_controls_test.go` (A/F: lock and cleanup). Supporting recovery fixtures: `workflow_snooze_recovery_test.go` (B–F).
- Managed `embedded_browser_controls.go` and new `embedded_browser_workflow_lock_test.go` (A/F), new `embedded_workflow_settlement_test.go` (C), and new `embedded_workflow_retirement_test.go` (F). Existing alias/admin boundaries remain in scope.
- Transport `managed_http.go` and `managed_workflow_state_test.go` (F): atomic deactivate uses store lifecycle cleanup, removing the redundant ignored goal write. Transport `websocket.go` and new `websocket_workflow_goal_test.go` (A): reject-only WS guard, not general dispatch.
- E2E suite-local helper is `tests/e2e/workflow-implicit-goals-harness.ts`. It remains owned with the suite by `36634d4d`; its presence is not full-suite proof.

The retention baseline gap remains unwaived: owner-lifetime cleanup is not a numeric bound for long-lived agents, and retention shipping acceptance is unresolved. No retention subsystem is authorized. Background-test authorship remains unproven: preserve/exclude those hunks. `PERSISTENT_WORKFLOW_SNOOZE_PLAN.md` stays untouched. `SPEC.md` must never be committed.

This pass edits exactly `WORKFLOW_SNOOZE_RECOVERY_PLAN.md`, local `SPEC.md`, and factual status/scope in `docs/specs/workflow-implicit-goals.spec.md`. No code/test edits, test execution, services, workflow advancement, commit, or push. Existing behavioral invariants and all 001–013 requirements remain intact. Stop edits after recording this snapshot.

## Historical narrow preference repair exception (store3d30 resumed)

Parent reviewed migration 083 and the shared save path and explicitly approved one exception to the earlier no-migration scope: add forward migration `120`, replacing **only** `embedded_goal_snooze_preference_transition`. Do not edit old migrations, add tables, or build retention machinery. This assignment owns only this plan and `router-proxy/internal/store/**` on shared main `29dec4bf`; preserve all other work. Stop if a broader relaxation is needed.

Allow inactive→active only for the exact next preference version and a different immutable call matching full preference identity, version, and due time. Preserve identity immutability, retirement semantics, call/delete guards, owner cascade, continuation and unknown-start fences. The shared save path may set `active=1` with affected-row verification only after accepting a new fully fenced call. In the same transaction retire other active preferences only on a successful new call, never on an unsnoozed turn. Current-invocation replay remains denied; historical read-only replay must not reactivate anything. Maps to B/D/F, especially 012/013; no public contract is weakened.

Known red evidence: `embedded_goal_preference_repair_test.go`, `/tmp/store-preference-red.log`, `/tmp/store-preference-baseline.sha256`. Same-turn transition fails for alias/native; prior-long/new-short selects the superseded preference. Corrupt identity/deadline and numeric controls already pass. Require focused green evidence, raw trigger rejection checks, installed-119 forward application and reopen, and existing owner-cascade/unknown-start checks. E2E owner `99fce` consumes repaired shared source; parent independently validates afterward. No full blind runs, services/restarts, commit or push. Return command/log/exit/hash evidence through final result/steer, not supervisor mail. Prior task transcript was not found in local Pi session files; the explicit parent-approved handoff and existing artifacts define this narrow scope.

### Historical preference repair implementation evidence

Only this plan, store `embedded_goal_snooze.go`, new `migrations/120_embedded_goal_snooze_preference_reactivation.sql`, and supporting `embedded_goal_preference_repair_test.go` were edited in the resumed assignment. Both 0→1 activation and 1→1 replacement now require matching immutable call identity/version/DueAt. Missing DueAt excludes the call from EXISTS, so NOT EXISTS rejects the update; malformed JSON is rejected by the unchanged call CHECK. Retirement and every other trigger remain unchanged.

Installed-119 fixture saves and retires history before opening with 120, checks migration installed once and other trigger SQL unchanged, reopens, denies historical replay reactivation, checks raw same-call/version/missing-call/stale-identity/wrong-deadline/missing-DueAt updates for inactive and active rows, then saves a fresh fenced call and reopens its active preference. Separate fault fixtures prove ignored-upsert affected-row denial and retirement-error rollback preserve old call/preference/policy.

Red rerun: `/tmp/store-preference-resumed-red.command`, `.log`, `.exit` (1). Baseline source hashes matched `/tmp/store-preference-baseline.sha256`. Initial green `/tmp/store-preference-green.log` predates the parent's both-transition guard correction. Final focused command is `/tmp/store-preference-stable.command`, with output/exit in matching `.log`/`.exit`; includes repair, all numeric normalization, migration083, owner termination/cascade and continuation unknown/start checks. Earlier `/tmp/store-preference-focused.log` was interrupted; `/tmp/store-preference-guards.log` reached PASS (35.549s), but its exit sidecar was stale after harness interruption, so use the stable rerun for captured exit evidence. No full-suite or E2E acceptance claim. Parent and E2E owner 99fce independently validate the stable source next. No commit/push/restart.
