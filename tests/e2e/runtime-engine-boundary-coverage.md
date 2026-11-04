# Pi runtime boundary coverage

Scope: SPEC-RUNTIME-ENGINE-001 / root CLAUDE_CODE_ADAPTER_PLAN.md phase 1.
This is partial implementation, not a passing acceptance claim.

## Scoped execution evidence

The run at `/home/wolfgang/.mecha-developer/test-runs/claude-browser-final.cHX8YNPp`
reports 65 passed, 0 failed, with `playwright_exit=0` and matching recorded
source-before/source-after hashes. Its scope includes nine Chromium cases in
`runtime-engine-boundary.spec.ts` (RE-001–004, three RE-006 cases, RE-007 and
RE-008), plus the listed Claude suites. See `scope.json` and `run.log` there
for exact test titles and invocation. This is scoped evidence, not a full Pi
regression-suite or full behavioral-matrix pass. No tests ran in this docs update.

The table maps assertions, not whole scenario acceptance. Existing tests listed
outside that run are supporting coverage only; their execution is not implied.
Changed shared paths and existing regressions, including reset release/redelivery,
remain required. Unrelated expanded Pi matrix coverage is deferred, not passed.

## Mapping

| Requirement | New suite assertions | Existing coverage reused | Remaining gap |
|---|---|---|---|
| RE-001 | Exact multiline Unicode text, browser attachment reference/order, joined input/ACK/append key, exactly one visible final-text match, WS final, projected tokens, no automatic reply outbox | openai-model-catalog.spec.ts: `executes Terra image and Responses tool round trip with exact metadata` checks real image read, call/result pairing and decoded pixels | Exact text-match count is asserted, not DOM bubble identity; image bytes are proved by existing suite, not the new text-only scripted response |
| RE-002 | Equal-text distinct message IDs, held provider barrier, acceptance without second ACK, release followed by two checked ACKs in original order | None claimed | Byte-match/head-fallback branches not independently forced |
| RE-003 | Interrupt at held provider, unchanged epoch, next request includes previous answer, preserved ACKs, termination desired state | None claimed | Partial streamed output, callback settlement ordering, independent resource-close effects, concurrent termination/admission progress and fencing, and committed termination permission-error handoff/retry remain unverified by this browser case |
| RE-004 | Failing real local read tool, provider-visible error, checked tool result before final, live final | None claimed | Router addressed mail, tool-card/companion ordering, synthesis nudge, controlled post-intent/pre-completion crash and UNKNOWN non-replay |
| RE-005 | No duplicate test added | workflow-implicit-goals.spec.ts SCN-WFIG-004/008/009 (one exact snoozed continuation), SCN-WFIG-011 (DONE), SCN-WFIG-003/005/010 (workflow lifecycle); goal-empty-breaker.spec.ts (keyed checked receipts/finals and capped empty-response continuation) | Active-session tool refresh and auxiliary requests excluding ordinary receipts/entry markers; existing mapped tests were not all executed here |
| RE-006 | Separate browser/tool resets, epoch 2, scratchpad retained, recent answer and reset notice in actual resumed provider input | workflow-evidence-retention.spec.ts: `same-DB reopen preserves future D and browser reconnect without early or duplicate start` covers scheduled continuation reopen only | Threshold reset, preservation window/order, claimed interrupted wake scoped final, Continue without duplicate user; scheduled continuation is NOT claimed-wake proof |
| RE-007 | Actual SQLite user-append rejection, zero ACK/provider requests or user/subsequent entries; initial system entry remains | None claimed | Stale binding, checked assistant/tool failure, stale usage, settlement-before-deregistration |
| RE-008 | Held active turn reset, old unconsumed reservation/key absent and no ACK during held wake, fresh epoch-2 attempt with unchanged accepted identity/body, checked ACK on redelivery, visible final | goal-empty-breaker.spec.ts covers empty/whitespace continuation nudges, not StopError or thinking-only | StopError, thinking-only, absent-final synthesis and standalone interrupt reservation cleanup remain uncovered; rollback, late callback and other reset/echo races still require applicable E2E verification |
| RE-009 | Not asserted | None | Applicable helper validation and failed/stale consumption need boundary E2E evidence where shared helpers handle them |
| RE-010 | RE-006 browser reset at 1000px and 1280px | Claude `claude-code-recovery.spec.ts` CC-009 interrupt at both widths, in the green run | Only those controls and viewports are asserted |

## Historical fixture notes

Coordinate these with other fixture owners before integration:
- workflow-implicit-goals-harness.ts: optional `barrier` on an answer; hold the
  actual HTTP response until explicit release, then emit the ordinary SSE.
  Existing reply scripts unchanged. Close destroys held HTTP responses.
- harness.ts: embedded router startup wait 15s → 45s. Credential consumer and
  catalog run sequentially; isolated direct checks measured 6.325s and 8.889s.
  The longer wait exposed a separate unavailable catalog rather than fixing it.
  Non-embedded timeout remains 15s. No readiness assertions weakened.

## Historical execution evidence (before this prep)

Initial environment: no checkout node_modules or UI dist. Local untracked
symlinks were added for root/UI/auth-helper dependencies (left for validation;
do not commit them). UI built using
`cd web/router-ui-react && node node_modules/vite/bin/vite.js build` (passed,
20.52s). Full `npm --prefix web/router-ui-react run build` was interrupted at
the command deadline during tsc; no full typecheck pass claimed.

- `npx playwright test tests/e2e/goal-empty-breaker.spec.ts`: first failed
  resolution of @playwright/test, then 2 failed at Go VCS stamping. Direct
  build reported `error obtaining VCS status: exit status 128`.
- `GOFLAGS=-buildvcs=false npx playwright test tests/e2e/goal-empty-breaker.spec.ts`:
  2 failed (credential synchronization/startup timeout). After local auth-helper
  dependency link, 2 failed startup timeout. These are not controlled clean
  pre-extraction baselines: another agent was editing Go files concurrently.
- `GOFLAGS=-buildvcs=false npx playwright test tests/e2e/runtime-engine-boundary.spec.ts`:
  initial 8/8 failed before test bodies, mostly 15s startup timeout; last case
  reached model readiness failure. Log: /tmp/runtime-boundary-tests.log.
- After bounded startup increase, RE-001 alone failed model readiness after
  48s: empty catalog, `managed provider status is unavailable`. Sanitized
  readiness: /tmp/workflow-retention-readiness-failures/worker-525015.json.
- Combined rerun: 2 existing baseline failures, 1 interrupted new test,
  7 not run (stopped redundant startup retries). /tmp/runtime-boundary-final.log.
- Final latest-source run with `--max-failures=1`: 1 failed at model readiness
  (36.8s), 8 not run; this invocation discovered a temporary ninth stale-owner
  case subsequently removed because termination would confound its fencing
  assertion. Final discovery: 8 tests. Log: /tmp/runtime-boundary-latest.log.
- `git diff --check -- tests/e2e` passed.

The historical runs above are superseded for the scoped cases by the green run recorded at the top. They do not establish broader acceptance.
