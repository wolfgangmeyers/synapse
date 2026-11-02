# Workflow evidence retention plan

## Current scoped validation

Wolfgang authorized the bounded retention remedy and supporting fixes, with explicit carry-to-completion authority and no hold. Older scope prohibitions, approval gates, failure dispositions and workflow phases below are historical snapshots, not current instructions.

Fresh post-QA-fix isolated browser validation passed 39/39 tests across four suites (344.322s), exit 0: no failures, skips, flakes or retries; source/dist drift 0 and owned survivors 0. Validated patch SHA256: `dfd51eafda05e90a9d3a4f3b0c73968e73acb2a772fdadacb9671af2de325f24`. Current Go verification passed Store 11 and managed 6 top-level tests; vet and build exited 0. These are scoped selections, not a full-repository run.

All six QA lanes completed; fixes independently accepted, including the semantics-equivalent dead-code subtraction, stale-owner notice negative regression and documentation corrections. The actual workflow advanced to OptionalCodeReview; no additional optional review was requested. This final status-only documentation update does not require another 39-case run.

Earlier browser 39/39 (327.369s), managed 16, Store retention/execution 32 and surviving guards 7 remain historical supporting evidence, not final-source reruns. Former stale-owner rows now pass; obsolete skips were retired with scoped replacements, not counted as passes. Earlier reds remain historical. No full-repository-suite, full-coverage, merge, integration, shipping or deployment acceptance is claimed; no commit or push is claimed.

Evidence: `/tmp/workflow-retention-qa-final-acceptance.md`; independent acceptances: `/tmp/workflow-retention-final-docs-verification.md`, `/tmp/workflow-retention-qa-notice-negative-verification.md`, `/tmp/workflow-retention-qa-subtraction-verification.md`. Historical evidence: `/tmp/workflow-retention-isolated39-final.md` and `/tmp/workflow-final78-collected-results.md`. The active 001–016 contract, A/D/R/H bounds, exact execution protection and UNKNOWN/no-replay fences remain unchanged.

## Decision
Age-bound pending evidence; fixed-size protection only for actual execution.
Five tables: applied receipts, evaluations, continuations, snooze calls/preferences.
No coalescing, transcript pruning, ordinals, forced execution stop or approval gate.
The previous one-closure assumption is false: the existing eight-reservation proof
and store source generalize to arbitrary N in one session/revision. One timer does
not supersede accepted work. Source review, not independent test execution.

## Implementation contract (spec first)
- Preserve existing sessions/transcripts. Add immutable router admission time A on
  user entries, backfilled from trustworthy router time or migration time once.
  Never trust arbitrary SDK/client timestamps or modify bodies/digests/sequences.
- Persist monotonic session expiry authority. Reopen, rollback clock, restore,
  import/upsert and owner replacement cannot reset it or grant replay permission.
- New evidence identities/calls/preference versions close at A+30d inclusive.
  Missing evidence never permits recreation. UNKNOWN never permits retry.
- New reservation effective immutable D must be <=A+31d. Validate policy deadline
  contributors too: the tool's 24h limit alone does not prove this envelope.
  Reject abnormal admission explicitly, without clamping D or fabricating verdicts.
- Pending expires at R=max(A+30d,D)+30d (NULL D uses A+30d). Ordinary closure
  age is therefore <=61d. Preserve ALL accepted reservations and exact dependencies
  until R, without a count cap. Start requires D<=now<R plus existing fences.
- At R atomically reject admission as retention-expired, legally invalidate reserved
  work and prune dependencies. Overdue D alone is NOT disposal authority. This
  explicitly replaces indefinite pending acceptance; no silent supersession.
- Historical UNKNOWN/started state alone gets no protection. Pruning must preserve
  uncertainty/no-replay through surviving immutable time/session fences. Never
  change started to reserved or UNKNOWN to failed. Running evaluator may finish
  its verdict but cannot create missing continuation after admission expiry.

## Exact current execution
- SDK/evaluator execute under the same lane.serial. Claims queued before that
  lock are NOT actual execution. Install evaluator protection after acquiring it,
  before evaluator I/O, with expiry recheck. Continuation source protection commits
  atomically with started CAS. Persist SDK user key before provider/tool admission.
- Existing owner/current fields and historical provider-entry markers lack exact
  origin. Add ONE bounded validated JSON execution record on existing session row:
  kind, owner token, full source evaluation key and current SDK user key, exact
  referenced call IDs. Reuse existing key structs; no ordinal/ledger subsystem.
  Parent coordinates exact schema/API with store owner 3bef and managed audit 09ff.
- Continuation source can differ from its newly created SDK user. At most two exact
  closures, ten five-table rows/session; evaluator cannot overlap SDK on that lane.
  Freeze referenced calls at expiry; no historical-call lists or old-turn churn.
- Clear by exact execution identity/token on actual settlement. Fence old owner
  effects before replacing authority; lease expiry alone does not prove execution
  stopped. Crash leftovers stay UNKNOWN, never retries. Restore cannot restore old
  slot snapshots/cutoffs. Cleared expired identities cannot regain protection.
- Any separately authorized deployment assumes a cold router restart, not hot binary replacement. No hot-rollout
  backfill requirement. New manager installs execution authority before executing;
  old durable UNKNOWN rows are not surviving old-binary execution. Do not guess
  current work from newest started row, assistant sequence or activity state.

## Legacy drain
- Preserve saved D unchanged. Backfill missing A with immutable migration time once.
  Freeze H=max(migration UTC+61d, maximum valid legacy saved D+30d) once on existing
  retention metadata. Legacy R=max(A+30d,D)+30d capped by H. NULL D uses A+30d.
  H declares a finite database-specific legacy drain bound, not a universal 61d
  bound. It includes valid extreme/policy-derived saved deadlines, never refreshes,
  and needs no new history. Invalid authority grants no start and retires by H.
- No new promise that each reservation executes forever. Before R it remains
  accepted subject to existing gates; after R expiry is explicit and irreversible.

## Atomic cleanup and validation requirements
1. Update latest behavioral spec before behavioral production changes.
2. Forward migration after actual tip; preserve existing migrations, identity/result
   guards, owner cascades/FKs. Replace only five DELETE guards with matching
   expired/unprotected predicate. Critical admission gates also need SQL guards.
3. Shared transactional gates cover claim/completion/reserve/start/receipt/snooze
   save/replay/repair/recovery/restore and raw SQL/import paths.
4. Startup before recovery and existing 60-second sweep: one BEGIN IMMEDIATE
   advances fences, checks exact execution, invalidates and deletes atomically.
   Lock/fault retries next sweep; no unbounded sweep log. Delete preferences before
   calls, continuations before evaluations; check receipt closure and FK integrity.
5. Startup and sweep pruning are implemented. Exact execution entry/exit,
   source/current separation and owner fences remain mandatory; the scoped evidence
   above is not full-suite or deployment acceptance. The earlier prune-disabled
   instruction was a planning-time gate, not a current instruction.
6. Real SQLite: arbitrary-N future-D survival before R, inclusive boundaries,
   no early/duplicate/refreshed start, deadline envelope, exact current beyond R,
   queued UNKNOWN expiry/no replay, repeated NOT_DONE, migration/H/reopen/clock,
   lifecycle rollback, start/sweep races, injected rollback and foreign_key_check.
7. Playwright: real browser/REST/WS/proxy/router/SQLite, automatic cleanup and fresh
   work, future-D recovery before R and rejection after R, actual current preserved.
   Deterministic local provider/time mocks only. No external LLMs/services.
8. Planning-time record: no tests had run when this plan was written. Current scoped
   results are recorded above; preserve unrelated work.

Source: store embedded_goal_continuation/evaluation/snooze; managed
embedded_goal_continuation/session; migrations 065/077/083; existing proof/log.
Concrete report: /tmp/workflow-retention-closure-resolution.md (<=100 lines).
