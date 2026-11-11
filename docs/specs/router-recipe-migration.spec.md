# Spec: Router Launch Recipe Migration Recovery

- **ID**: `SPEC-ROUTER-RECIPE-MIGRATION-001`
- **Status**: `Active`
- **Domain**: `Settings`
- **Target E2E Suite**: `tests/e2e/router-recipe-migration.spec.ts`

## 1. Intent & User Outcome

The router starts after upgrading its saved launch recipes, even when an older database contains recipes whose agent records are already missing. Users keep their saved work without deleting history, inventing replacement agents, or manually changing the migration ledger.

## 2. Core Invariants

1. Migration 131 makes the retired RPC extension optional. Every other recipe constraint, the recipe hash index, and the agent relationship with cascading deletes remain enforced.
2. Every saved recipe survives unchanged, including its row identity, fields, hash, and timestamps. Existing foreign-key diagnostics remain unchanged; the upgrade neither repairs historical debt nor introduces new violations.
3. Foreign-key enforcement is suspended only for this table rebuild, on the exclusively owned migration connection, outside the transaction. It is restored and checked before the connection can serve normal work, including after failure.
4. Schema changes and the migration ledger commit together. An unrelated failure rolls everything back, remains visible, and allows normal retry. Databases that already completed migration 131 are not rebuilt again.
5. Fresh databases, healthy older databases, and older databases with orphaned recipes all start normally. Browser REST and WebSocket connections work through the router proxy after upgrade.
6. This repair introduces no accumulating application data or new retention policy. Test databases are disposable and removed by the test harness.

## 3. User Scenarios (Behavioral Contract)

### Scenario: [SCN-RREC-001] Preserve older recipes and historical relationship debt
- **User Goal**: Recover startup without deleting saved work.
- **Given**: A database has completed migration 130 and contains a valid recipe, recipes with missing agent parents, sparse recipe row identities, and unrelated historical foreign-key debt.
- **When**: The user starts the upgraded router and later restarts it on the same database.
- **Then**: Startup succeeds, migration 131 is recorded exactly once, saved recipe rows and foreign-key diagnostics are unchanged, and the browser can use REST and WebSocket connections through the proxy. Normal writes still reject invalid relationships.

### Scenario: [SCN-RREC-002] Start fresh or upgrade a healthy database
- **User Goal**: Continue using normal launches after upgrading.
- **Given**: The database is new or has completed migration 130 with a valid saved recipe and no relationship debt.
- **When**: The user starts the router.
- **Then**: Startup succeeds, existing recipes are unchanged, an empty retired RPC extension is accepted, and other recipe constraints, indexes, and cascading agent relationships remain intact. Browser REST and WebSocket connections work.

### Scenario: [SCN-RREC-003] Fail atomically and retry after a schema conflict
- **User Goal**: Avoid an incomplete upgrade or weakened database checks.
- **Given**: A migration-130 database contains saved recipes and an unrelated schema conflict that makes migration 131 fail after its table copy begins.
- **When**: The user starts the router.
- **Then**: Startup fails before serving requests, recipes and the old schema remain unchanged, and migration 131 is not recorded. After the conflict is removed, normal startup succeeds without data loss or duplicate migration entries, and invalid relationship writes are rejected.

## 4. Automated E2E Verification Matrix

| Scenario ID | Scenario Description | E2E Test Suite | Test Case Name |
| :--- | :--- | :--- | :--- |
| SCN-RREC-001 | Historical debt, row preservation, and reopen | `tests/e2e/router-recipe-migration.spec.ts` | `preserves orphaned recipes and relationship debt across upgrade and restart` |
| SCN-RREC-002 | Healthy and fresh schema constraints | `tests/e2e/router-recipe-migration.spec.ts` | `starts fresh and upgrades healthy recipes without weakening constraints` |
| SCN-RREC-003 | Atomic failure and retry | `tests/e2e/router-recipe-migration.spec.ts` | `rolls back a recipe rebuild conflict and retries normally` |

Supporting Go tests exercise the real migration runner, exact row and diagnostic preservation, connection settings after success and failure, rejection of newly introduced foreign-key violations, and unchanged recipe validation.
