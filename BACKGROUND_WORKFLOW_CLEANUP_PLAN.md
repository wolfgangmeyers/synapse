# Background workflow cleanup plan

## Outcome and scope

A regular sweep sends all background workflow workers over 24 hours old through the existing worker termination path. Use creation age, not inactivity or terminal state: active, sleeping, failed and terminated instances are not excluded by this policy. The sweep is periodic, so the timing is approximate. Dynamic and scheduled instances, including existing records, are in scope.

Keep ordinary agents, workflow definitions, recurring schedules, future launches, workflow exit behavior and existing completed-worker cleanup unchanged. Do not add a separate fencing, quiescence, delivery or retention protocol. This is a plan only.

## Specification and investigation

- The target behavior is documented in `docs/specs/background-worker-cleanup.spec.md`. The active `SPEC-BACKGROUND-WORKFLOWS.md` describes the old one-hour completed-worker cleanup as a historical expectation, not implemented; automatic termination does not establish a new retention period.
- Trace the existing worker termination entry point and regular router maintenance, including how it handles already terminal workers and errors. Select background instances by their creation timestamp and age; do not use `updated_at` or infer inactivity. Check existing records and startup behavior so old instances are considered on the regular sweep after restart.
- `PruneExpiredBackgroundAgents` in `router-proxy/internal/store/managed_background.go` is a separate existing pruning operation; do not substitute its terminal/updated-time predicate for age-based termination or change its behavior as part of this task.

## Implementation approach

1. Integrate selection of every background worker older than 24 hours into regular maintenance. Send each through the same termination path used to terminate a worker normally, regardless of its activity or lifecycle state. Use the path's existing behavior for already terminating or terminal instances.
2. Keep failures from preventing maintenance of other instances and retry failed requests on a later sweep, using existing error handling. Do not claim termination merely because an instance is old.
3. Preserve existing behavior elsewhere. Avoid inventing new lifecycle fences, process controls, retention periods or delivery guarantees.

## Verification and review

- Add fast deterministic Playwright E2E coverage using the real browser, REST, WebSocket, proxy, router and SQLite with local model mocks. Exercise actual maintenance and termination, with controlled time or fixtures instead of waiting a day or copying a SQL deletion.
- Cover old dynamic and scheduled workers in active, sleeping and terminal states; a younger worker; activity that does not reset creation age; existing workers and restart; repeated sweeps; and termination failure followed by retry. Verify ordinary agents and recurring schedules are unaffected, future scheduled runs work, and existing workflow exit and completed-worker cleanup behavior remains intact.
- Run relevant focused E2E and lifecycle/store tests, build and lint as applicable, and `git diff --check`. Review the scoped diff against the draft scenarios and report actual results and blockers.
- Preserve unrelated dirty and untracked changes. Inspect overlapping files and hunks before implementation; do not overwrite or include unrelated work. This documentation revision makes no production changes and is not a commit.
