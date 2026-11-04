# Spec: Background Worker Cleanup

- **ID**: `SPEC-BACKGROUND-CLEANUP-001`
- **Status**: `Draft` (sweep implemented; full acceptance coverage pending)
- **Domain**: `Workflows`
- **Target E2E Suite**: `tests/e2e/background-expiry.spec.ts`, `tests/e2e/background-cleanup.spec.ts`, `tests/e2e/background-scheduled-expiry.spec.ts`, `tests/e2e/background-active-retry.spec.ts`

## Intent

A regular sweep automatically sends every background workflow worker at least 24 hours old through the existing worker termination path. Age is measured from creation, not inactivity or terminal state. This applies to dynamic and scheduled instances, including existing instances. Active and sleeping workers are not exempt. The sweep runs on startup and every minute. Dispatch and termination can be delayed by downtime, blocked workers or failures; there is no completion deadline.

Only background worker instances are in scope. Ordinary agents, workflow definitions and recurring schedules are unaffected; future scheduled runs continue normally. Cancellation, lifecycle, delivery, history, errors and UI/API behavior follow the existing termination path. Existing handling of terminated or failed workers remains unchanged. Workflow exit and unbinding retain their existing meanings. This policy adds no separate cleanup protocol or one-hour retention promise.

## User scenarios

### SCN-BWC-001: Old background workers

- **Given** dynamic and scheduled background workers at least 24 hours old, including active, idle, sleeping and terminal instances.
- **When** the regular sweep runs.
- **Then** it uses the existing worker termination path for each, including its existing handling of already terminal workers. Activity and sleep do not exempt a worker or reset its age.

### SCN-BWC-002: Younger worker

- **Given** a background worker less than 24 hours old.
- **When** the sweep runs.
- **Then** this policy does not terminate it. Recent activity does not change its creation age.

### SCN-BWC-003: Existing workers and restart

- **Given** a background worker already present before this policy was enabled, or one that became old while the router was stopped.
- **When** the regular sweep runs after startup.
- **Then** it uses the same age rule and existing termination path. Repeated sweeps follow the existing path's handling of workers already terminating or terminated.

### SCN-BWC-004: Unrelated behavior

- **Given** an ordinary agent, a recurring schedule and background workers in various lifecycle states.
- **When** the sweep handles old background workers.
- **Then** the ordinary agent and schedule remain unaffected, future scheduled launches still work, and existing workflow exit and completed-worker cleanup behavior remain unchanged.

### SCN-BWC-005: Termination failure

- **Given** an old background worker whose termination request fails.
- **When** the sweep runs again.
- **Then** it retries through the existing termination path. Age alone is not reported as successful termination; existing error behavior applies.

### SCN-BWC-006: A termination stalls

- **Given** an expired worker whose termination is blocked and other expired workers.
- **When** the sweep runs again while that termination is still in progress.
- **Then** every other expired worker can still be dispatched, even when many terminations are blocked. Each worker has at most one expiry termination in flight. A busy worker waits using the same termination semantics as a direct request, however long that takes; repeated sweeps do not replace the waiter. Failed attempts are retried on a later sweep. Cancellation prevents new dispatches.


## Automated verification and gaps

Existing `background-expiry.spec.ts` exercises startup and real dynamic launch; `background-cleanup.spec.ts` exercises startup and periodic dispatch through router, proxy, SQLite, browser and WebSocket. Go tests cover boundaries, blocking and deduplication. Scheduled expiry and subsequent launch are covered by `background-scheduled-expiry.spec.ts`. `background-active-retry.spec.ts` observes dispatch during a held active provider turn, then checks admission failures and retry after release. Full acceptance remains pending, including the full lifecycle-state matrix and stalled-worker concurrency at the browser boundary.

| Scenario | Acceptance coverage |
| --- | --- |
| SCN-BWC-001 | Old dynamic and scheduled instances across active, sleeping and terminal states go through the existing termination path. |
| SCN-BWC-002 | A younger worker is not terminated; activity does not reset the creation-age threshold. |
| SCN-BWC-003 | Existing old workers and workers aged during downtime are handled after startup; repeated sweeps use existing termination behavior. |
| SCN-BWC-004 | Ordinary agents and schedule definitions survive, future launches work, and existing exit and completed-worker cleanup behavior is retained. |
| SCN-BWC-005 | A termination failure is not reported as success and a later sweep retries. |
| SCN-BWC-006 | A stalled termination does not block another worker; repeated sweeps do not overlap its termination. |
| SCN-BWC-007 | Go tests cover concurrent manual termination policies and unresolved actions; browser acceptance remains pending. |

Supporting lifecycle/store tests cover age boundaries, stalled dispatch, deduplication and failures, but do not replace E2E coverage.

### SCN-BWC-007: Concurrent manual termination

- **Given** an expired worker has an active manual termination, including a chosen worktree cleanup policy or a stop failure.
- **When** expiry runs concurrently or the active action is left unresolved.
- **Then** expiry does not resume or alter that action or invent its cleanup policy. New expiry requests use the normal DELETE default of cleaning up the worktree.
