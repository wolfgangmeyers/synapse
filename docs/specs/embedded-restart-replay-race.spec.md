# Spec: Embedded restart concurrent replay

- **Status**: Draft
- **Domain**: Managed Agents

## Intent and invariants

An embedded restart must preserve idempotency when callers race admission while an active turn drains. An accepted operation is replayed, not admitted or interrupted twice. A new request cannot bypass the drain. Acceptance records the HTTP operation; it does not imply that asynchronous restart has completed.

## User scenarios

### Scenario: [ERR-001] Identical request races admission
- **Given**: An embedded agent is restarting and its active turn is still draining; a concurrent caller missed the initial replay lookup.
- **When**: That caller reaches admission with the same request ID, method, path, and body as the committed request.
- **Then**: It receives the same accepted operation as an idempotent replay, without a second restart or interruption.

### Scenario: [ERR-002] Conflicting reuse during drain
- **Given**: A restart has been accepted and is still draining.
- **When**: A caller reuses its request ID with a different path or body.
- **Then**: The request fails with an idempotency conflict, without another restart.

### Scenario: [ERR-003] Distinct request during drain
- **Given**: A restart has been accepted and is still draining.
- **When**: A caller submits a restart with a different request ID.
- **Then**: The agent is reported not ready, and the active runtime is not interrupted again.

## Verification matrix

These mappings identify test coverage, not a claim that tests passed in this review. Automated tests use deterministic local mocks, not live LLMs.

| Scenario | Concrete coverage | Remaining gaps |
| :--- | :--- | :--- |
| ERR-001 | `router-proxy/internal/managed/embedded_restart_replay_race_test.go`: `TestEmbeddedRestartConcurrentAdmissionReplaysWhileDraining` races two manager calls past the initial lookup, verifies one replay and the same operation, and checks that only one operation was recorded. | Manager-level race, not concurrent HTTP/browser E2E. |
| ERR-002 | The same test verifies that a changed body hash or path produces `ManagedIdempotencyConflictError` during drain. | No concurrent HTTP/browser E2E covers these conflicts during the race. |
| ERR-003 | The same test verifies a different request ID produces `ManagedAgentNotReadyError` during drain. After all requests, it asserts exactly one runtime interruption. | No concurrent HTTP/browser E2E covers this race. |

`tests/e2e/embedded-active-restart.spec.ts` separately exercises active-turn cancellation and subsequent prompt and mail through the browser, proxy, router, and store; it does not exercise concurrent replay.

### Scenario: [ERR-004] Owner validation fails
- **Given**: An embedded agent receives a new restart request.
- **When**: Its stored owner fence is stale.
- **Then**: The agent is reported not ready, without accepting or interrupting the restart.
- **When**: Owner validation instead fails due to a database error or request cancellation.
- **Then**: That failure is returned rather than reported as a stale or not-ready agent, and no restart is accepted or interrupted.

Manager-level regression coverage: `TestEmbeddedRestartOwnerValidationErrors`. Browser E2E coverage for these error paths remains a gap.
