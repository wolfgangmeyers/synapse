# Specification: Inbox Session Recovery and Automatic Goal Wake Safety

## Scope and authority

Smallest observable outcome: a current agent inbox cannot be blocked by an obsolete reservation; automatic goal wakeups are paced and bounded on failed turns and stop on invalid session ownership. Live ownership and committed mail effects remain protected.

Both defect outcomes are explicitly user-authorized. Non-goals: broad runtime rewrite, new store, weakened ownership, new configuration API, unrelated UI redesign, or changed scheduled workflow launch cadence.

Authoritative scenario contracts:
- [SPEC-MAILBOX-001](docs/specs/mailbox-session-recovery.spec.md): `SCN-MSR-001`–`007`.
- [SPEC-GOAL-001](docs/specs/goal-wake-safety.spec.md): `SCN-GWS-001`–`007`.
- [Workflow goal contract](docs/specs/workflow-implicit-goals.spec.md) explicitly qualifies the previous immediate-idle rule. Stock Claude worker evaluator policy is unchanged.

The scenario contracts above define desired behavior. The code-aware realization below is separately attributed investigation evidence; it does not change the scenario authority.

## Observable inputs, outputs, state and errors

| Input/action | Required observable output/state change |
| --- | --- |
| Ordinary accepted mail | Current valid owner consumes once; browser receives resulting reply without reload. |
| Context reset or worker/router restart with safely unstarted reserved pending mail | Once current ownership is established and worker ready, pending mail progresses on the next normal dispatch opportunity, without obsolete-owner cooperation or queue repair. |
| Slow current owner or observer reconnect | Live reservation remains protected; no competing consumer. |
| Late obsolete-owner operation | Rejected without modifying successor delivery or duplicating effects. |
| Recovery/replay after committed consumption/effect | No new consumption of completed mail and no repeat of committed mail effect; still-pending mail progresses. |
| First eligible workflow idle wake | May be immediate with valid ownership, remaining chain allowance and no snooze. |
| Repeated automatic attempts | Default delay is 10 seconds after preceding automatic attempt settles; no overlap. Snooze can extend, not shorten, eligibility. |
| Automatic failure, including before model execution | Counts toward the consecutive-chain cap and obeys pacing; cannot reset its own chain or falsely complete the goal. |
| Twentieth automatic attempt | Exhausts the consecutive-chain cap, including the first immediate attempt; no twenty-first attempt in the consecutive automatic chain. |
| Invalid/obsolete/missing/conflicting owner | Stops automatic attempts against that binding; visible failure, no live-owner takeover. |
| Repeated automatic idle/fallback/timer signals | Cannot reset their own consecutive chain; suppressed checks do not consume attempts. No new durable restart budget is required. |
| Deliberate user recovery/resume | Requires valid ownership; retains existing deliberate-user reset semantics while preserving goal/workflow lock and committed effects. Invalid recovery fails visibly without a loop. |
| DONE, unlocked goal off or legitimate workflow exit/deactivation | Cancels continuation; stale wake cannot reactivate it. |

Default pacing is 10,000 ms; cap is 20 consecutive automatic attempts including the first immediate attempt, not 20 retries plus one. Failures and their automatic notifications cannot reset their own chain. Ordinary deliberate user action retains existing reset semantics. No durable lifetime budget, user-message reset prohibition, cross-restart persistent counter or explicit fresh-budget recovery mechanism is required. Existing supported positive delay overrides obey cap/snooze/ownership safety. An attempt enters admission/turn-start work even if rejected before model dispatch; a check suppressed by delay, snooze, cap, cancellation or an existing owner hold does not count. Initial ownership rejection counts; subsequent hold checks must not re-enter admission. No implementation constant or override is claimed to exist.

## Architecture evidence and high-level component responsibilities

`docs/architecture/state.md` is missing. Architecture alignment cannot be asserted; ADR history is not substituted for current binding state. No architectural choice is made. Any change to stores, schemas/public contracts, authority boundaries, integrations or retention requires a separate architecture evidence/decision step before implementation. No new store, authority path or communication boundary is required by these observable contracts.

The [workflow-management skill](.claude/skills/workflow-management/SKILL.md) documents router-owned workflows backed by SQLite. The [E2E README](tests/e2e/README.md) documents the real router/store/browser boundaries. The following are high-level responsibility boundaries, not verified implementation structure:

- Lifecycle/ownership authority establishes whether ownership is current or replaced; mailbox recovery must use that authority and the narrow unstarted eligibility below rather than infer obsolescence from slowness.
- Mail delivery coordinates recovery of unconsumed work with ownership checks and existing effect replay guarantees.
- Goal scheduling checks ownership, snooze, delay and remaining consecutive-chain allowance before every automatic attempt, including attempts rejected before model execution.
- Recovery controls validate ownership and distinguish deliberate resume from passive events.
- Existing conversation/status transport exposes progress and holds without UI redesign.
- Current per-agent/goal safety bookkeeping stays bounded and is removed with its owning goal/agent. No growing retry ledger or queue is required; existing history retention is unchanged.

## Design alternatives and scoped realization

| Alternative | Potential benefit | Risk / required evidence |
| --- | --- | --- |
| Reconcile obsolete reservations at ownership replacement | Makes lifecycle handoff explicit | Must prove crash/restart recovery and late old-owner fencing, not only clean reset. |
| Reconcile during inbox dispatch against authoritative current ownership | Can recover interrupted handoffs | Must prove live-owner protection, bounded work and repeat/race idempotence. |
| Combine lifecycle reconciliation with dispatch validation | Could cover clean and interrupted handoffs | More coordination; requires evidence from actual design before selection. |
| Clear all reservations or accept old-owner actions | Superficial liveness improvement | Rejected behavior: weakens live ownership and permits duplicate effects. |
| Enforce wake pacing/consecutive-chain cap at automatic admission with settlement feedback | Could cover pre-model and turn failures consistently | Hypothesis only; must identify existing admission boundaries and restart semantics. |
| Pace only on successful model completion | Smaller apparent change | Rejected behavior: pre-model failures can still spin and escape cap. |
| Disable all goals or make all first wakes wait | Avoids some loops | Rejected scope: loses legitimate first immediate workflow continuation. |

Selected realization, based on the subsequent code-aware investigation:

1. Reconcile a retired session's **unstarted** mail reservation lazily in `Store.ReserveEmbeddedMail`'s existing immediate transaction. Require authoritative current binding, unchanged accepted envelope/target, the exact retired session tuple for the same agent and generation with old epoch strictly less than new, and no ACK, append-key or reply evidence. Never relocate ACKed-but-unappended or other started/uncertain outcomes; reject cross-agent/generation, equal/future-epoch, non-retired or mismatched tuples. Retain attempt identity. Never relocate committed admission or use lease expiry as takeover authority. This is narrower than a lifecycle-wide sweep and also covers interrupted reset handoffs.
2. Keep owner acquisition and cache publication coherent under the existing per-agent lane, including startup/recovery failure: publish the acquired identity to the cache before any subsequent fallible startup/recovery work. Validate a cached identity before returning it; a mismatch must fail without reacquiring/replacing ownership or silently seizing a competing live identity. `RenewEmbeddedOwner` currently checks both the session tuple and persisted `owner_id`; tuple equality alone is insufficient cache validation.
3. Account for every automatic attempt at admission and settlement, including fallback idle turns and failures before provider dispatch. Reuse bounded current goal-policy state where its existing semantics permit; do not introduce an unbounded attempt ledger. Automatic origin must be explicit, not inferred from prompt text, so a failed automatic turn cannot reset its own chain. Reuse existing failure state for owner holds where suitable; persistence only where existing semantics naturally support it, without schema or evaluator reinterpretation.
4. Preserve SDK `StopError` as a scheduling failure. Apply default pacing, cap and ownership hold through both reserved continuation and idle-fallback paths; recovery and cancellation must recheck authority before firing timers.

The mailbox blockage and retry amplification are confirmed by durable incident evidence and code control flow. The precise historical cached owner identity was not logged: the failed-recovery cache/publication sequence is a reproducible hypothesis, not a conclusively observed historical transition. A deterministic regression must establish that sequence before its repair is claimed.

These changes remain within existing router, SQLite and per-agent lane boundaries. Missing `docs/architecture/state.md` remains an evidence gap; any required new schema or authority decision must be raised rather than silently selected.

## Planned proof and review criteria

Use the two scenario matrices as 1:1 planned Playwright cases. New suite names are `tests/e2e/mailbox-session-recovery.spec.ts` and `tests/e2e/goal-wake-safety.spec.ts`. Existing filenames were listed, not their executable contents read. Regression targets: `workflow-implicit-goals.spec.ts` and `background-workflows.spec.ts` in `tests/e2e/`.

Proof must exercise real browser, REST/WebSocket, router/proxy and SQLite with deterministic local model/evaluator fixtures. Control race interleavings and clock advancement; do not sleep through 10-second delays. Documented schedule clock support does not establish that goal timers already use it. Observe failed admissions as well as model calls, so pre-model spin cannot hide behind zero LLM requests.

Review acceptance:
1. All seven mail scenarios preserve progress, live-owner protection, stale-owner rejection and no duplicate committed mail effects across reset and both restart types.
2. All seven goal scenarios prove first immediate wake compatibility, subsequent pacing before/at 10 seconds, exactly 20 attempts in a consecutively failing automatic chain, no invalid-binding spin, and explicit valid-owner recovery.
3. Failed automatic turns and repeated idle/fallback/timer signals never reset their own chain; distinguish attempted admission from suppressed checks. Snooze/cancellation remain authoritative. Ordinary deliberate-user reset semantics remain unchanged; no new durable restart-budget proof is required.
4. Numerical policy is explicit and the former unconditional immediate-idle wording is reconciled, not silently contradicted.
5. Implementation review maps each changed production/test file to these criteria and proves bounded cleanup of any new safety state. No unsupported scope additions.

## Evidence limits and delivery

Scenario mappings are planned, not passing-test claims. This docs-only correction changes no production code or tests and runs no tests. Delivery uses separate test/code authors with red/green proof, relevant E2E/regressions and coverage, and independent final review. The user explicitly directs execution without router-managed workflows or further approval gates. Missing architecture evidence and the unlogged historical owner identity remain explicit limitations; neither is represented as resolved by this document.
