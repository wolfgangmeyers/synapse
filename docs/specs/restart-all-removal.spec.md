# Spec: Managed Restart All removal

- **ID**: `SPEC-MANAGED-RESTART-ALL-REMOVAL-001`
- **Status**: `Draft`
- **Domain**: `Managed Agents`
- **Target E2E Suite**: `tests/e2e/individual-restart.spec.ts`

## 1. Intent & User Outcome

Remove the managed Restart All feature. There is no replacement bulk action. Individual restart remains available; this change also fixes restart without a model for ready embedded agents without a legacy RPC binding. Task #60.

## 2. Core Invariants

1. No UI control, client method, HTTP route, alternate bulk entry point, manager dispatch, or store admission path for Restart All exists.
2. With managed services enabled, `POST /v1/managed-agents/restart-all` returns `404` (not a generic managed-agent route, not a legacy fallback). When managed services are disabled, the existing direct-surface gate returns `503 MANAGED_DISABLED` before route dispatch. Neither response changes any agent or creates a managed lifecycle action, batch item, or HTTP operation record.
3. `POST /v1/managed-agents/{agent_id}/restart` remains available. For a ready embedded agent, a request without a model uses the embedded restart path rather than requiring a legacy RPC binding.
4. The lifecycle action kind `restart_all` and source `restart_all_child` are no longer accepted for new records. Direct restart, reset and terminate remain supported.
5. Out of scope: recovery, settlement, completion, or cancellation of in-flight batches admitted before removal, including the fate of their children. Migration SQL history (including 028) is retained so fresh and existing databases can migrate; no new Restart All batches are admitted.
6. Managed operations never restart tmux processes (unchanged).

## 3. User Scenarios (Behavioral Contract)

### Scenario: [RAR-001] No Restart All control
- **Given**: The web UI is connected to a router with managed agents.
- **When**: The user opens the sidebar, header, and agent menus.
- **Then**: No Restart All control is rendered. Per-agent Restart remains available.

### Scenario: [RAR-002] Retired route returns 404
- **Given**: Managed services are enabled, with several running managed agents and a snapshot of agent state and lifecycle/operation tables.
- **When**: A client sends `POST /v1/managed-agents/restart-all` (with an empty body, any body, or an `Idempotency-Key`).
- **Then**: The response is `404`. The request does not change agent runtime incarnation or state, or create a lifecycle action, batch item, or operation. Normal per-agent methods remain available for a valid agent named `restart-all`; only the retired POST operation is absent. With managed services disabled, the direct-surface gate instead responds `503 MANAGED_DISABLED` before route dispatch.

### Scenario: [RAR-003] Individual restart still works
- **Given**: A running managed agent, including an embedded agent with no legacy RPC process binding.
- **When**: The user (or client) restarts that agent via `POST /v1/managed-agents/{agent_id}/restart` without specifying a model.
- **Then**: The restart operation is accepted and the agent remains usable with its selected model and saved context. Other agents are untouched. An agent without a current, ready embedded owner is rejected rather than restarted through a legacy process binding. For embedded agents, the existing terminal HTTP operation records the accepted response and idempotent replay, not completion of a lifecycle action. The asynchronous restart interrupts an active turn, waits for its lane to drain, and reopens admission; idle agents also remain usable. Later prompts and mail can run against the preserved session. A stale owner must not interrupt a successor or admit a new restart.

### Scenario: [RAR-004] Databases migrate unchanged
- **Given**: A fresh database, and an existing database that already applied migration 028 (possibly holding legacy batch rows).
- **When**: The router starts.
- **Then**: Migration succeeds in both cases. Recovery and settlement of previously admitted batches and their children are outside this specification.

### Scenario: [RAR-005] New restart_all lifecycle records are rejected
- **Given**: The store.
- **When**: A lifecycle action of kind `restart_all` or source `restart_all_child` is requested.
- **Then**: It is rejected as an invalid kind/source.

## 4. Verification Matrix

The mappings below identify existing test cases, not a claim that they have passed in this review. Automated checks use deterministic local mocks, never live LLMs.

| Scenario | Concrete coverage | Remaining gaps |
| :--- | :--- | :--- |
| RAR-001 | Per-agent restart is exercised by `tests/e2e/individual-restart.spec.ts`. | Removal is assessed by inspection, not absence tests. |
| RAR-002 | None. | Retired route absence is assessed by inspection. |
| RAR-003 | `tests/e2e/individual-restart.spec.ts` covers UI acceptance, REST replay, unknown agents and isolation. `tests/e2e/embedded-active-restart.spec.ts` covers active-turn cancellation, retained context, subsequent prompt and mail. Manager-level tests cover idle and active restart and stale-owner fencing. | The E2E does not assert a specific restart WebSocket notification. |
| RAR-004 | `router-proxy/internal/store/managed_lifecycle_migration_test.go`: `TestMigration028ExistingDatabaseUpgradeAndRollback`; `router-proxy/internal/store/managed_lifecycle_s0_test.go`: `TestLegacyBatchRowsAndGraphRetentionAreAtomic`. | No identified test starts the router against an existing database already at migration 028. |
| RAR-005 | Direct restart reservation validation remains covered in `router-proxy/internal/store/managed_restart_s3_test.go`. | Retired admissions are assessed by inspection. |

The broader absence of manager dispatch and store admission paths, and tmux isolation, require code inspection or separately identified regression coverage; the matrix above does not by itself prove them.
