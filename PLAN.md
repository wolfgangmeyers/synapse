# Native Subagent Activity Parity Plan

## Direct-to-main disposition

Implementation is complete and the diff is staged on primary `main`. The remaining path is a direct commit to `main`, then a separate authorized push to `origin/main`; no PR is involved.

## Scope contract

Implement the approved `SPEC.md` behavior on top of current `e753457`:

- Hand-port only the protections from `6b5da28` that match `SPEC.md`.
- Do not cherry-pick `6b5da28` wholesale.
- Preserve unrelated inline-tool allowance changes already present in `e753457`.
- Keep the Pi native subagent manager as the source of truth for parent activity.
- Keep router and web production code as transport and presentation unless tests prove a direct SPEC violation there.

## Expected files

Planning files already dirty and preserved:

- `SPEC.md`
- `PLAN.md`

Staged implementation files:

- `tests/mailbox_v2_delivery.test.mjs`
- `pi-extensions/mecha-messaging.js`
- `tests/router_web_ui.test.mjs`

Expected unchanged files:

- `package.json`
- `package-lock.json`
- `tests/pi_extension_standalone.test.mjs`
- `tools/router_web_ui.mjs`
- `web/router-ui/app.js`
- `web/router-ui/state.js`
- `web/router-ui/markdown.js`
- router production files
- mailbox delivery, lease, reminder, batching, persistence, and SQLite code

Do not recreate `web/router-ui/subagents.js`; it is deleted in the current branch.

If a red test proves a SPEC violation in an expected-unchanged file, stop and update this plan with the evidence before changing that file.

## Non-goals

- No router production changes without direct evidence.
- No web production changes without direct evidence.
- No package or lockfile changes.
- No mailbox delivery, lease, reminder, batching, message persistence, or SQLite behavior changes.
- No markdown-rendering work.
- No web UI helper extraction or restoration.
- No install, PR, or branch action.
- No audit-finding cleanup. `npm ci` reported two high-severity audit findings; they are not part of this task unless they block validation.

## Phase 1: red tests

Contract: tests first. Each new failure must name SPEC behavior, not `6b5da28` internals.

Extension tests in `tests/mailbox_v2_delivery.test.mjs`:

1. Parent-bound native service wins when a child session replaces `globalThis[Symbol.for("@gotgenes/pi-subagents:service")]`.
2. Polling discovers a native subagent start missed by lifecycle events while the parent is settled.
3. Polling discovers a native subagent finish missed by lifecycle events while the parent is settled.
4. Polling continues while the parent is active or waiting for native subagent results.
5. Terminal native statuses, including `steered`, stop contributing to the running count.
6. Native service read failures do not stop polling; diagnostics stay bounded.
7. Existing event-tracked subagent summaries and TTL refresh behavior keep working.
8. Existing inline-tool allowance coverage stays in this file; do not move that regression guard only to `tests/pi_extension_standalone.test.mjs`.

Existing assertion contract updates in `tests/mailbox_v2_delivery.test.mjs`:

1. Find the current settled-parent heartbeat assertions that expect the activity heartbeat timer to clear on settled.
2. Flip them intentionally: settled alone must not clear the heartbeat when native subagent polling may still need to repair missed lifecycle events.
3. Keep shutdown/disconnect cleanup assertions strict so this is a SPEC contract change, not a broad timer-cleanup weakening.

Router/web tests in `tests/router_web_ui.test.mjs`:

1. Use the existing live-browser fixture: `startRouterWebUi` plus `waitForEvent`.
2. Do not import, recreate, or test through deleted `web/router-ui/subagents.js`.
3. Do not refactor or helper-extract `web/router-ui/app.js`.
4. Send activity frames containing `subagent_count` and `subagents`, then treat `/v1/agents` and live `agent_activity` checks as contract proofs.
5. Inspect the rendered `.agent-row` DOM for badge/count behavior, including clearing after a later zero-count frame.
6. Exclude the candidate commit's `matchesActiveScope` scope-filter test; scope filtering is out of scope for this plan.

Regression guard:

- `tests/pi_extension_standalone.test.mjs` should stay unchanged but must run to protect standalone behavior; inline-tool allowance assertions inside `tests/mailbox_v2_delivery.test.mjs` must also be preserved.

## Phase 2: narrow extension implementation

Contract: make Phase 1 pass with the smallest extension-side change.

Steps:

1. Prefer the captured parent subagent service for activity reconciliation. Fall back to the global service only when no captured service exists.
2. Call `ensureActivityHeartbeatTimer()` after coordinator creation so polling starts even if lifecycle events were missed.
3. Call `ensureActivityHeartbeatTimer()` after compaction resume refresh so polling resumes after session restoration.
4. Keep the activity heartbeat available after parent settled events so polling can repair missed native lifecycle events.
5. Keep the heartbeat alive while the parent is active or waiting for native subagent results.
6. Treat `steered` as terminal alongside the existing terminal statuses.
7. Preserve event-tracked summaries, missing-record handling, and TTL refresh behavior.
8. Keep native-service read diagnostics in the catch path bounded so repeated failures do not create unbounded logs or state.
9. Keep timer cleanup on shutdown/disconnect paths.
10. Add comments only for the parent-bound service, polling contract, and bounded diagnostic behavior where the code would otherwise look surprising.

Expected changed file:

- `pi-extensions/mecha-messaging.js`

## Phase 3: router/web contract proof

Contract: prove router and web remain pass-through layers.

Steps:

1. Send activity frames containing `subagent_count` and `subagents` through existing live-browser fixtures.
2. Assert `/v1/agents` contains the received values as a contract proof.
3. Assert live `agent_activity` events contain the received values as a contract proof.
4. Inspect rendered `.agent-row` DOM and assert badges follow received activity and clear on zero.
5. Leave router and web production code unchanged unless these tests expose a real SPEC mismatch.

Expected changed file:

- `tests/router_web_ui.test.mjs`

## Phase 4: validation

Run from `/Users/wolfgang/code/mecha-developer`:

```sh
node --test tests/mailbox_v2_delivery.test.mjs tests/router_web_ui.test.mjs tests/pi_extension_standalone.test.mjs
npm test
npm run check
rm -rf /tmp/native-subagent-activity-coverage
NODE_V8_COVERAGE=/tmp/native-subagent-activity-coverage node --test tests/mailbox_v2_delivery.test.mjs tests/router_web_ui.test.mjs tests/pi_extension_standalone.test.mjs
```

Coverage contract:

- Confirm the new extension branches and router/web received-activity assertions executed.
- If Node's coverage output is too raw for percentages, inspect the generated JSON enough to verify the new branches ran.

Runtime correlation contract:

1. Start a separate bounded Pi parent session from the validated checkout.
2. Launch one native subagent that stays running long enough to observe.
3. Save `/v1/agents` snapshots while it runs and after it finishes.
4. Save a live `agent_activity` frame log covering start and finish.
5. Save browser evidence from the rendered UI, either DOM capture of the `.agent-row` badge state or a screenshot.
6. Compare terminal native state, `/v1/agents`, live web activity, and browser badge behavior.
7. Record whether counts and summaries converge after start and finish.

Use a separate local runtime if live validation needs a router. Do not disturb the existing global router unless the supervisor separately approves it.

## Phase 5: review

Contract: review the final local diff, then validate findings before acting.

Steps:

1. Run robot-review on the final diff with these focuses: requirements, timer/lifecycle correctness, router/web scope, inline-tool regression risk, and test coverage.
2. Run pr-validate on raw findings.
3. Fix only validated in-scope findings.
4. Re-run targeted tests after fixes.
5. Re-run `npm test` and `npm run check` after any production-code change.
6. Record whether a KB update is needed. If the implementation preserves or changes a reusable Mailbox V2 operating lesson, update the KB before final handoff.
7. Next actions are a supervisor-authorized commit first, then a separate supervisor-authorized push to `origin/main`.
