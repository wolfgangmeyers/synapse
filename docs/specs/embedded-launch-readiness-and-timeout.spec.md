# Spec: Embedded Launch Readiness and Timeout Resilience

- **ID**: `SPEC-AGENT-LAUNCH-TIMEOUT-001`
- **Status**: `Accepted`
- **Domain**: `AgentManagement`
- **Date**: `2025-11-20`
- **Authors**: `Wolfgang / Synapse Team`
- **Related-Issues**: `#127`
- **Target E2E Suite**: `router-proxy/internal/store/managed_launch_recovery_test.go`, `router-proxy/internal/managed/embedded_failure_regression_test.go`, `tests/e2e/embedded-stale-relaunch.spec.ts`

---

## 1. Intent & User Outcome / Scope and Context

Managed embedded agents run AI agent workflows in-process inside the router daemon. When a user or supervisory system creates an embedded agent via `POST /v1/managed-agents`, the router reserves the launch in SQLite (`lifecycle_state = 'starting'`, `desired_state = 'running'`) with an associated pending HTTP launch operation, initializes the session, binds the root session to an owner lease, and invokes the embedded runner.

Previously (Issue #127), `ReadyEmbeddedRoot` was invoked only *after* `runner(runCtx, agent)` completed. Because `runner` executes the embedded agent session and its initial turn, any agent whose initial turn performed multi-step tool calls, deep reasoning, or file operations exceeding the default 2-minute launch timeout remained in `lifecycle_state = 'starting'`. The periodic background sweep (`TimeoutStuckLaunches`) inspected:
```sql
WHERE desired_state = 'running' AND lifecycle_state = 'starting' AND created_at < ?
```
and aborted the active, healthy agent with `last_error_code = 'LAUNCH_TIMEOUT'`, killing the in-flight turn and causing `ReadyEmbeddedRoot` to fail upon completion with `stale embedded readiness`.

This specification establishes that:
1. **Immediate Launch Readiness**: As soon as the embedded session is initialized and bound to the agent/owner lease, the agent MUST immediately transition to `lifecycle_state = 'running'` and `accepting_prompts = 1`, and its pending launch operation MUST be marked terminal *before* blocking on initial turn or runner execution.
2. **Active Launch Timeout Immunity**: The stuck launch sweep (`TimeoutStuckLaunches`) MUST NEVER abort any agent whose `activity_state` is `'active'`.
3. **Recent Activity Protection**: The stuck launch sweep MUST evaluate `updated_at < ?` (rather than `created_at < ?`), ensuring that starting agents undergoing active lease renewal or state updates are not prematurely aborted.
4. **Resilient Inactivity Reaping**: Genuinely stuck launches—agents remaining in `starting` with `activity_state != 'active'` and no updates for longer than the timeout period—continue to be safely reaped with `LAUNCH_TIMEOUT`, releasing slots and allowing clean re-launch.

---

## 2. Core Invariants

1. **Readiness Precedes Turn Execution**: `ReadyEmbeddedRoot` and launch operation completion MUST be executed immediately upon acquiring owner lease and binding the embedded session, before blocking on runner invocation or initial prompt execution.
2. **Active Turn Immunity**: Any agent with `activity_state = 'active'` MUST NOT be timed out or transitioned to `failed` by `TimeoutStuckLaunches`, regardless of elapsed time since launch creation.
3. **Heartbeat & Timestamp Freshness**: `TimeoutStuckLaunches` MUST filter using `updated_at < cutoff`, ensuring any agent receiving updates or lease renewals within the timeout window is protected from cancellation.
4. **Idempotent Readiness State Preservation**: `ReadyEmbeddedRoot` MUST NOT overwrite an already `'active'` `activity_state` to `'idle'`. If the agent is already `'active'`, it remains `'active'` while transitioning `lifecycle_state` to `'running'`.
5. **Bounded Stuck Launch Cleanup**: Agents genuinely stuck in `lifecycle_state = 'starting'` without active execution (`activity_state != 'active'`) and with `updated_at < cutoff` must be transitioned to `failed` with error code `LAUNCH_TIMEOUT`, freeing their slot reservation.

---

## 3. User Scenarios (Behavioral Contract)

### Scenario: [SCN-1] Embedded agent launch readiness upon session binding (Happy Path)
- **User Goal**: Launch an embedded agent and have it immediately enter `running` state once its session is bound, without waiting for initial prompt turns to finish.
- **Given**:
  - The router receives a valid `POST /v1/managed-agents` request for an embedded agent.
  - The agent reservation is created with `lifecycle_state = 'starting'`.
- **When**:
  - `startEmbeddedRoot` acquires the embedded owner lease and binds the session.
- **Then**:
  - `ReadyEmbeddedRoot` is invoked immediately.
  - The agent record transitions to `lifecycle_state = 'running'`, `activity_state = 'idle'`, `accepting_prompts = 1`, and `started_at` is populated.
  - The launch HTTP operation is marked terminal (`state = 'terminal'`).
  - The agent begins executing its session runner. Subsequent API queries show the agent as `running`.

### Scenario: [SCN-2] Long-running initial turn executes without timeout kill (Happy Path / Long Turn)
- **User Goal**: Allow an embedded agent's initial prompt or turn to execute for longer than 2 minutes (e.g. running complex tools, code execution, or multi-step reasoning) without being killed by the launch watchdog.
- **Given**:
  - An embedded agent has completed session binding and transitioned to `lifecycle_state = 'running'`.
  - The agent begins its initial turn, setting `activity_state = 'active'`.
  - The initial turn runs continuously for more than 2 minutes (e.g. 150 seconds).
- **When**:
  - The periodic background sweep `TimeoutStuckLaunches(ctx, 2*time.Minute)` executes.
- **Then**:
  - The sweep does not match the agent because its `lifecycle_state` is `'running'` (and its `activity_state` is `'active'`).
  - The agent's turn is not canceled or aborted.
  - The turn finishes successfully, and the agent transitions to `activity_state = 'idle'`.

### Scenario: [SCN-3] Genuine stuck launch timed out after inactivity period (Error / Failure Path)
- **User Goal**: Automatically recover from a genuinely stalled or crashed launch where session binding never succeeded.
- **Given**:
  - An agent launch reservation was created in `lifecycle_state = 'starting'` with `activity_state = 'starting'`.
  - Due to a fatal host initialization error, crash, or deadlock prior to session bind, `ReadyEmbeddedRoot` was never called.
  - The agent remains in `lifecycle_state = 'starting'` with `activity_state != 'active'`.
  - The agent has not been updated for longer than the timeout duration (`updated_at < now - 2m`).
- **When**:
  - `TimeoutStuckLaunches(ctx, 2*time.Minute)` executes.
- **Then**:
  - The stuck agent is identified by the query.
  - The agent record is updated to `lifecycle_state = 'failed'`, `activity_state = 'failed'`, `slot_held = 0`, `last_error_code = 'LAUNCH_TIMEOUT'`, and `last_error_message` indicating launch timeout.
  - The pending launch HTTP operation is marked terminal with `terminal_code = 'LAUNCH_TIMEOUT'`.
  - The agent slot is released, permitting subsequent re-launch attempts with the same agent ID.

### Scenario: [SCN-4] Active or recently updated starting launch protected from premature timeout (Edge / Recovery Path)
- **User Goal**: Prevent premature timeout of a starting agent that is actively progressing or updating heartbeats during a complex multi-stage initialization.
- **Given**:
  - An agent is in `lifecycle_state = 'starting'`.
  - The agent was created more than 2 minutes ago (`created_at < now - 2m`), but is either actively executing (`activity_state = 'active'`) or recently updated heartbeats (`updated_at >= now - 2m`).
- **When**:
  - `TimeoutStuckLaunches(ctx, 2*time.Minute)` executes.
- **Then**:
  - The query condition `activity_state != 'active' AND updated_at < ?` excludes the agent.
  - The agent is NOT marked as failed.
  - The agent is permitted to complete its startup sequence and reach readiness.

---

## 4. Acceptance Criteria

1. **Runner Ordering**: In `router-proxy/internal/managed/embedded_runner.go`, `ReadyEmbeddedRoot` and `MarkManagedOperationTerminal` must execute immediately after owner acquisition and `m.embeddedOwners` registration, strictly *before* calling `runner(runCtx, agent)` or `dispatchEmbeddedStartupWake`.
2. **Watchdog Query Safety**: In `router-proxy/internal/store/managed.go` (`TimeoutStuckLaunches`), the selection query must filter by `activity_state != 'active'` and use `updated_at < ?` rather than `created_at < ?`.
3. **Readiness Activity Semantics**: In `router-proxy/internal/store/embedded_router.go` (`ReadyEmbeddedRoot`), the query and update must safely transition starting agents to running without clobbering an already `'active'` `activity_state` to `'idle'`.
4. **Failure Handling**: If `ReadyEmbeddedRoot` fails during startup, `failEmbeddedOwner` is called with code `EMBEDDED_READY_FAILED`. If the subsequent `runner` fails, error handling marks the owner failed appropriately without race conditions against a premature timeout.
5. **Slot Recovery**: Genuine stuck launches timed out by `TimeoutStuckLaunches` must release `slot_held` and terminalize the associated `managed_http_operations` row.

---

## 5. Testing Requirements & Verification Matrix

| Scenario ID | Scenario Description | Test Suite | Test Case Name |
| :--- | :--- | :--- | :--- |
| `SCN-1` | Embedded agent transitions to running on session bind before runner blocks | `router-proxy/internal/managed/embedded_failure_regression_test.go` | `TestEmbeddedRunnerReadinessBeforeTurnBlocksAndTimeoutImmunity` |
| `SCN-2` | Long-running turn is not aborted by `TimeoutStuckLaunches` | `router-proxy/internal/managed/embedded_failure_regression_test.go` | `TestEmbeddedRunnerReadinessBeforeTurnBlocksAndTimeoutImmunity` |
| `SCN-3` | Inactive stuck launch past timeout is failed and frees slot | `router-proxy/internal/store/managed_launch_recovery_test.go` | `TestStuckLaunchTimeoutAndRelaunchOverwrite` |
| `SCN-4` | Active or recently updated starting agent is not timed out | `router-proxy/internal/store/managed_launch_recovery_test.go` | `TestTimeoutStuckLaunchesImmunityForActiveAndProgressingAgents` |
| `SCN-5` | Readiness CAS, idempotency, and active activity state preservation | `router-proxy/internal/store/embedded_root_ready_test.go` | `TestEmbeddedRootStartingBoundReadyCASAndCrashReopen`, `TestEmbeddedRootReadyPreservesActiveActivityState` |
| `SCN-6` | Readiness failure during startup marks owner failed with `EMBEDDED_READY_FAILED` | `router-proxy/internal/managed/embedded_failure_regression_test.go` | `TestEmbeddedRunnerReadinessFailure` |
| `SCN-7` | Runner failure after readiness marks owner failed with `EMBEDDED_START_FAILED` | `router-proxy/internal/managed/embedded_failure_regression_test.go` | `TestEmbeddedRunnerStartupFailureAfterReadiness` |
