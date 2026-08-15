---
name: supervisor-review
description: Supervisor readiness interrogation and quality gate before accepting worker done, blocked, parked, commit/push, draft PR, or review-ready state transitions.
---

# Supervisor Review Skill

Use this skill when a worker says work is done, ready for review, ready for commit/push, blocked, parked, validated, or otherwise asks the supervisor to accept a state transition.

This is a quality gate on supervisor tracking. Use it to find shortcomings before Wolfgang does. A worker's readiness report is a claim, not evidence.

## Required outcome

Do not accept readiness until the worker's claim has been checked against the actual requirements, diff, runtime, and evidence. Every missing answer must become either:

- a direct, evidence-backed answer;
- a concrete worker instruction with exact next steps;
- a bounded verification subagent result; or
- a recorded valid deferral with the requirement, owner, and follow-up condition.

Generic directions such as "be thorough" are not enough.

## Proportional readiness pass

For each draft PR, review-ready, commit/push, or final-readiness gate, run one proportional readiness pass for the immutable evidence key: immutable base, target tree/head, stated requirements, and relevant external evidence. Record an immutable identifier for the target (a commit/tree hash, or a diff/tree hash for an uncommitted target) and the version or timestamp of relevant external evidence. Material production changes receive all applicable review, requirements-compliance, coverage, security, and live-proof checks in that pass. Docs-only or trivial changes receive only applicable checks. Reuse a completed pass for later commit, push, or readiness transitions when the key is unchanged. Rerun only after a material target change, new material review feedback, or new material evidence. Do not repeat same-target checks or hold an already-authorized push for already-completed review.

For material targets, run all three bounded independent background subagents below where they apply. For docs-only or trivial targets, launch only applicable checks; do not substitute a worker report, supervisor inspection, prior pass, or "equivalent result" for an applicable check.

1. **Overengineering and codebase consistency:** Enumerate every new identity, service, permission, deployment step, and configuration surface introduced by the change. For each one, cite the requirement or external contract that makes it necessary. Report the smallest viable design that preserves all requirements, and fail the gate when the implementation adds unsupported machinery. Also verify the implementation matches existing codebase patterns, feature flags, generated contracts, and naming conventions. Flag new flags, slugs, roles, or access-control categories unless Wolfgang explicitly approved them. For security- or IAM-sensitive changes, require an immutable reference to the approved spec or plan by content hash or version, with reapproval after any change. Require a principal -> role -> resource scope -> allowed operations matrix that marks create, drop, list, IAM, and DDL as allowed or denied. Require a requirement-to-artifact ledger. Removing a required workflow or acceptance artifact fails the gate. Identify every named acceptance check and verify direct evidence that it ran. Execute only non-mutating checks during this review. Stateful or mutating acceptance checks require an explicit separate execution gate and direct evidence from that gated run. Unit-test scripts alone do not satisfy named acceptance checks.
2. **Documentation and comments:** Verify required docs, specs, migration comments, generated artifacts, and user-facing copy are present and accurate. Check that rationale is concise and useful. Reject noisy comments, overly verbose comments, comments that restate code, and prose that reads like a proof dump.
3. **Final robot-review cleanliness:** Verify robot-review ran after each material target change, new material review feedback, or new material evidence on the updated head. Any resulting pass with Blocking or Should Address findings must be fixed and followed by another applicable robot-review pass. The gate passes only when the final pass reports zero Blocking and zero Should Address. Require a final focus ledger: focus area -> reviewer/persona -> result/disposition. Any skipped focus area needs a concrete justification.

Keep each subagent bounded: give it the repository path, branch/head, base branch, task requirements, and the exact question above. It must inspect direct evidence and return pass/fail with file paths or commands that support its conclusion. It should not mutate files or runtime state.

Collect all applicable subagent results for the completed pass before accepting readiness. If any result is missing, inconclusive, or contradicts the worker's report, send the worker back with the specific gap.

## Supervisor interrogation checklist

Answer these questions with direct evidence or a recorded, valid deferral:

1. **Goal and requirements:** What exact user/ticket/spec requirements were supposed to be satisfied? Are all of them mapped to files, tests, and live proof? Did the worker change the solution shape, omit a negative constraint, or turn an explicit requirement into a follow-up?
2. **Data and environment:** Was representative data actually loaded into the worker's isolated environment? Which seed/load command, agency/user, ports, and row counts prove it? Does the page/API show representative rows/data, or only an empty shell/login/resources page? Browser validation must use a new tab/window, not Wolfgang's active tab.
3. **Tests and coverage:** Which backend, frontend, API, integration, regression, and edge-case tests prove the behavior? Did the worker run the `test-coverage` skill when dev-loop applies, or only talk about coverage? If coverage could not run, is the exact tool failure recorded and is the normal test baseline strong enough to continue?
4. **Browser/live proof:** For user-visible or service behavior, did a real browser/end-to-end/dev-loop check run against the correct worktree, branch, services, and loaded data? Were console errors, Vite overlays, failed requests, mobile/narrow width, exports/downloads, and the exact acceptance path checked?
5. **Permissions and security:** Are route/API gates server-side, not only frontend? Is the correct existing permission used unless Wolfgang approved a new one? Are negative authorization, object-level access, tenant isolation, and admin/role semantics tested? Are secrets/auth state kept out of logs and commits?
6. **Scope and diff hygiene:** Does every changed file belong to the task? Are planning files, local state, unrelated lockfiles, generated artifacts, deleted files, or broad refactors intentionally included? If not, send the worker back to revert/exclude them before commit.
7. **Documentation, KB, and durable knowledge:** Did the worker check the relevant KB before starting, and is that check recorded durably enough to audit? Did it update or preserve required docs, specs, KB entries, migration comments, generated contracts, and user-facing copy? If a reusable lesson or product fact was learned, is it captured where future agents will find it?
8. **Review completeness:** Did robot-review run after the final fixes with relevant focus areas and clean Blocking/Should Address results? Were review findings independently mapped to requirements or real risks? Did fixes introduce new issues requiring another pass?
9. **Blocker reality:** If the worker claims a blocker, what explicit requirement, external contract, credential boundary, safety risk, or irreversible decision makes it real? If the blocker is local setup, missing data, auth state, runtime ownership, or a reversible prerequisite, direct the worker to fix it and continue.
10. **PR, handoff, and done-state proof:** What is the next irreversible or user-visible action being authorized? Before committing, pushing, parking, completing, or cleaning up, does direct evidence show the artifact is reviewable, repeatable, demoable, and scoped? If a PR exists or will be pushed, is the branch/head correct, CI/watch/reminder path attached, and human-facing PR body accurate to the final diff?
11. **Production topology and source of truth:** For production-service changes, is this a real vertical seam in the deployed system, or only a helper that works inside one process? How many processes, hosts, regions, or tasks can run this code? What is the durable source of truth? How does state reach every process that can receive traffic? What happens after restart, deploy, or partial rollout? What cardinality is expected now and at scale? Does process memory grow with global data or only local ownership? What happens on cache miss? What routing, ownership, readiness, or publish-ordering rule makes the local state correct? A helper that is correct in isolation is not enough if the next PR still has to answer the core production lifecycle question.

A readiness report that lacks data proof, coverage/test detail, browser/live proof, documentation/KB handling, permission negatives, or diff-scope classification is incomplete by default.

## Worker mailbox loop

Every readiness pass must go through a Mailbox V2 instruction/results loop before the supervisor accepts readiness. Complete it once for each immutable evidence key, even when preliminary checks are clean. Reuse completed worker results for later transitions when the key is unchanged; do not require a fresh mailbox loop for a same-target transition.

Send the worker a Mailbox V2 instruction with exact next steps or the final evidence ledger to provide. In a Pi supervisor session, use the native `mecha_mailbox_send` tool. Use `/Users/wolfgang/code/mecha-developer/.claude/skills/supervisor/tools/mailbox_send.py --to <agent-id> --body <text>` as the fallback when the native tool is unavailable. The fallback infers the sender from the current tmux window; add `--from` only when that runtime explicitly requires it. Do not type into the worker terminal.

The message must include:

- the specific missing evidence, requirement miss, stale review pass, diff-scope issue, invalid blocker, or clean-preliminary-gate evidence the worker must confirm;
- the exact files, commands, URLs, checks, or artifacts the worker needs to inspect or produce;
- whether the worker must run `test-coverage`, browser/live proof, generated contract updates, or final robot-review again;
- a requirement that the worker reply by mailbox with results; and
- an instruction that the worker launch its own independent bounded subagents when useful for codebase consistency, documentation/comment review, robot-review focus areas, test triage, or requirements verification.

Useful worker prompts:

- `Before I can accept this as done, reply with an evidence ledger for requirements, changed-file scope, tests/coverage, data/browser proof, permissions, docs/KB, review findings, and blockers. Name paths, commands, URLs, seed counts, and skipped gates.`
- `You said this is blocked. What requirement or external contract makes it hard, what KB/repo/current-code workaround did you check, and does it prevent the primary artifact?`
- `Before I can accept review readiness, run robot-review after each material target change, new material review feedback, or new material evidence until the final applicable pass has zero Blocking and zero Should Address. Reply with the final focus ledger: focus area -> reviewer/persona -> result/disposition. Run any missing relevant areas on the final head and justify any skipped areas.`

Receive the worker's results by mailbox. Then decide whether the answers are satisfactory. If they are still vague, stale, based only on summary, or missing direct evidence, repeat this gate and send the worker back with the concrete unresolved items.

## Acceptance decision

Accept readiness only when the supervisor has collected the worker's mailbox results, all applicable independent subagent results for the completed immutable-target pass, and direct evidence for the relevant checklist items.

If the gate passes, proceed with the already-authorized next action: commit authorization, push authorization, draft PR creation, review-ready handoff, parking, blocker escalation, or completion state. If the gate fails, keep the task active and assign the next concrete action.
