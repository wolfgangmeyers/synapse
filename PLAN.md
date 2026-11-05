# Plan: Embedded inbox recovery and automatic wake safety

## Scope and execution gates

Target: current `main`; no branch/worktree, no unrelated edits. Requirements are `SPEC.md`, `SPEC-MAILBOX-001` and `SPEC-GOAL-001`. Production edits follow independently authored failing regression tests. The user explicitly directs execution without router-managed workflows or additional approval gates. Tests and implementation have separate authors. Preserve the user-stopped operational worker; any live router restart requires a durable wakeup reminder first. No messages to terminated `question` or unrelated workers.

## Requirement-to-delivery mapping

| Requirements | Intended owned files / realization | Proof |
| --- | --- | --- |
| MSR-001–004: ordinary/reset/worker/router recovery | `router-proxy/internal/store/embedded_router.go`: transactional reconciliation inside reservation allocation; existing reset and dispatch boundaries unchanged | Real SQLite regression: reserve with no ACK/append-key/reply evidence, reset, deliver oldest then later; cold reopen/restart; Playwright browser/proxy/REST/WebSocket with local model mock |
| MSR-005–007: live/stale ownership and committed-effect replay | Same reservation transaction checks exact retired same-agent/same-generation tuple with old epoch strictly less than new, immutable envelope/target and no ACK/append-key/reply evidence; retain attempt/key | Two store handles/races, live-owner rejection, old append rejected, cross-agent/generation and equal/future-epoch/non-retired mismatches not relocated, ACKed-but-unappended uncertain outcome not replayed, fault rollback and existing retention tests |
| GWS-001–003: pacing, failures, finite consecutive chain | `embedded_goal_continuation.go`, `embedded_session.go`, `supervisor.go`; explicit automatic-turn origin; common admission/settlement safety for reserved and fallback turns; positive default delay | Deterministic clock before/at deadline; consecutive automatic failures count exactly 20 attempts, including pre-model failures; suppressed checks count separately, not as attempts; healthy DONE cancels |
| GWS-004: stale owner origin and containment | `embedded_runner.go`: publish acquired owner to cache before subsequent fallible startup/recovery work; validate cached tuple and persisted owner ID, fail mismatch without reacquisition or takeover; `embedded_session.go`: preserve StopError for scheduling | Deterministically arrange cached owner A, recovery claim B, failed recovery, child completion; assert publication before the injected failure, cached tuple/owner-ID mismatch fails without reacquisition, no stale-cache provider dispatch, no invalid-owner spin and no competing takeover |
| GWS-005–006: chain safety and deliberate recovery | Reuse existing bounded goal/failure state where compatible; preserve ordinary deliberate-user reset semantics | Repeated idle/fallback/timer checks cannot renew their own chain; suppressed checks do not enter admission or consume attempts; invalid-owner hold stops automation until deliberate valid recovery. No new durable lifetime/cross-restart budget or fresh-budget API proof |
| GWS-007: cancellation and snooze | Existing goal controls/continuation timers, revalidate authoritative state at fire | Snooze before deadline, goal off, workflow exit and stale timer races; preserve locked-goal rejection |
| Bounded state | Reuse current per-agent policy row, no growing retry queue/ledger | Current row count remains bounded; new goal/agent deletion cleanup and existing history retention remain correct |

## Ordered delivery

1. Independently inspect this plan for correctness and scope while regression authoring proceeds; do not use router-managed workflows or wait for another authorization. Record actual evidence, not invented sign-off.
2. Test author reads scenario contracts and existing test fixtures (not production implementation). Implement isolated Go regression guards and `tests/e2e/mailbox-session-recovery.spec.ts`, `tests/e2e/goal-wake-safety.spec.ts` using real router/proxy/browser/SQLite and deterministic local SSE model mock. A test-only restart fixture may preserve the isolated DB. No external services/LLMs.
3. Run guards against unchanged production and capture assertion-level red output. Setup/build errors are blockers, not red proof. Commit test-only scoped changes before code handoff.
4. Separate implementation author fixes only production, never edits tests. Start with deterministic owner-origin reproduction; do not repair an unproven historical hypothesis blindly. Reconcile only safely unstarted mail; never delete or skip accepted work to hide blockage.
5. Implement shared consecutive-chain pacing/accounting and ownership failure holds using compatible existing bounded state; existing failure state may suffice. Preserve deliberate-user reset semantics. Do not reinterpret evaluator fields, mandate new schema, add a durable lifetime/cross-restart counter or invent a fresh-budget recovery API. Persistence is only where existing semantics naturally support it.
6. Run focused Go tests, affected managed/store/transport regressions, real-boundary Playwright suites, existing workflow-goal regression and relevant schedule regression. Disable test retries; preserve raw failure output. Measure coverage using existing Go tooling, including per-file changed scope.
7. Independent review of bounded diff and requirement mappings after proof. Return valid issues to their respective authors; rerun affected proof and re-review within the review cap. Write the spec-delivery report and concise RCA/CAPA; distinguish confirmed causes, historically unlogged identity and remaining limitations.
8. Verify remote owner and effective email, commit only scoped requested changes to `main`, push `origin/main`. Before authorized live restart set wakeup reminder, then verify health/source identity and affected agent recovery without reviving the explicitly stopped worker implicitly.

## Known proof risks

- `docs/architecture/state.md` is absent. No new store, schema/public API or authority boundary is selected by this plan.
- Generic E2E harness does not wire its mock LLM into embedded execution; use the existing lower-level local Codex SSE fixture pattern.
- The incident's cached owner A was not logged. Reproduce the candidate sequence; do not report it as conclusively observed history.
- Test-clock support for goal scheduling must be demonstrated, not assumed from scheduled-workflow tests.
- Current owner acquisition is not an exclusive lease between multiple managers. This task must not claim to add that guarantee or weaken current session fencing.

## Current evidence and remaining work

See [delivery report](docs/specs/reports/report-20261105-1650-main.md) for exact TC inventories and raw assertions. Focused Go: five top-level passes (three managed, two store); subcases are not added to counts. Existing browser: six passes; new goal: one pass in 11.4s, committed with harness as `52115ec4`. New mailbox combined ordinary/reset TC: one reported pass in 6.6s, committed `0f51282b` under `46170b06`, plus five repeated passes with no skips. Eight observed browser passes do not complete fourteen scenario contracts.

Selected broad managed execution still has seven failed parents; baseline controls match invocation/mail failures, while turn-error circuit failure has a different baseline signature. Full store/managed timeouts and broad transport 200-second no-output timeout are not passes. Focused transport: two passes, one cold-owner recovery failure matching baseline line 82.

Review `7070756f` remains unresolved: automatic goal-set resets cap; legitimate post-failure context reset renews old identity and falsely holds; passive mail/system wake clears deliberate hold. All three guard REDs committed: `9d686f81`, `edcefded`, `b22cbadc`; production owner `f53` fixing, GREEN/re-review pending. No completion or main-push readiness claim. Parent must check report before commit; docs owner does not commit. Preserve concurrent production/guard work. Remote divergence (20 commits, `56d36dc5`) awaits integration assessment; parent finalizes evidence after integration.

Full acceptance remains required: finish unimplemented scenario TCs, dependency/race and bounded cleanup proof, review changed-file acceptance mapping. Do not narrow the contract or count supporting Go invariants as full browser coverage. Confirmed incident mechanisms remain stale reservation and fallback self-reset/no-delay; historical cached owner A remains unlogged, deterministically reproduced hypothesis only.
