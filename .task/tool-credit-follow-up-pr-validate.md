# Tool-credit follow-up pr-validate

Validation target: current uncommitted diff after fetching `origin/main`.

The validator used `failure-operations-reviewer`, different from the reviewers that produced the raw findings. It read the current diff, `TASK.md`, the project instructions, the robot-review and pr-validate skills, and all current raw review outputs.

## Validation summary

- Raw findings checked: 3 underlying claims.
- Retained: 3 (one Blocking, one Should Address, one Question).
- Dropped: 0.
- Blocking: 1.
- Should Address: 1.
- Nits: 0.
- Questions: 1.
- GitHub discussion deduplication: not applicable to this local diff.

## Validated findings

### Blocking

- **Missing context/mailbox regression coverage** — `tests/pi_extension_standalone.test.mjs:375-390` only verifies copied-module loading. It does not behaviorally test the required mailbox wiring or context/compaction handling in `pi-extensions/mecha-messaging.js:1680-1710,1802-1844`. This leaves the explicit preservation requirement in `TASK.md` unprotected.

Protected requirement: preserve context-threshold and mailbox delivery behavior, and add focused tests for preserved context/mailbox behavior.

### Should Address

- **Reset test does not model distinct sessions** — `tests/pi_extension_standalone.test.mjs:287-300` invokes `session_start` with the same fixture and session ID. It verifies reset on the event, but does not model isolation between distinct session contexts. Use a second session context or fixture.

Protected requirement: keep one temporary pool per Pi session and reset it for a new session.

### Question

- Does Pi guarantee only one active top-level session per extension instance? The allowance pool is extension-scoped and reset on `session_start`; if multiple top-level sessions can coexist, state may need session-ID scoping. The current evidence does not establish this as a defect.

## Removed feedback

None. The requirements, API-design, and scope review reported no findings. The data-state reviewer reported no actionable finding; its lifecycle question remains a question because the runtime guarantee was not established.

## Final focus ledger

| Focus area | Result |
|---|---|
| requirements | clean |
| conventions | clean |
| diff-reading | clean |
| anti-patterns | clean |
| security | clean |
| auth-token-safety | removed: no auth or token behavior |
| external-integration | clean |
| performance | removed: no I/O or growth |
| test-coverage | findings: one Blocking, one Should Address |
| architecture | clean |
| error-handling | clean |
| data-integrity | clean |
| api-design | clean |
| observability | removed: no logging or metrics |
| reported-output | clean |
| comments-and-docs | clean |
| naming | clean |
| dead-and-unreachable-code | clean |
| structure-scale | clean |
| observability-noise | removed: no logs or metrics |
| scope | clean |
| visual-consistency | removed: no UI, browser, or screenshots |
| plan-architecture | removed: no current plan; historical router plan excluded |
| plan-completeness | removed: no current plan; historical router plan excluded |
| plan-consistency | removed: no current plan; historical router plan excluded |
| concurrency | clean; lifecycle question retained |
| simplicity | clean |
| architectural-seams | clean |
| configuration-safety | removed: no configuration branching |
| production-risks | clean; lifecycle question retained |
| documentation | removed: no documentation change; `TASK.md` unchanged |
| skills | removed: no skill change |

Final focus counts: **1 Blocking, 1 Should Address, 0 Nits, 1 Question.**

## Follow-up validation after fixes

The independent validator checked the focused clean review against the current diff and passed the 23 standalone tests:

- Blocking: **0**. Context-threshold and mailbox delivery are behaviorally covered.
- Should Address: **0**. Reset coverage uses a distinct session context.
- Nits: **0**.
- Question: **1**. Whether Pi permits concurrent top-level sessions in one extension instance remains unverified and non-blocking.
