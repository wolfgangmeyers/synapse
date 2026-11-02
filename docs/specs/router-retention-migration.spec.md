# Spec: Router Retention Migration Recovery

- **ID**: `SPEC-ROUTER-MIGRATION-001`
- **Status**: `Active`
- **Domain**: `Settings`
- **Target E2E Suite**: `tests/e2e/router-retention-migration.spec.ts`

## 1. Intent & User Outcome

The router starts after upgrading an existing database, including databases missing older goal-evidence guards. Users keep their saved conversations and workflow evidence without resetting the database or manually changing the migration ledger.

## 2. Core Invariants

1. Migration 123 replaces its six predecessor triggers whether each old trigger is present or absent. Missing old triggers do not skip installation of the new guards.
2. Schema changes, retention backfills, replacement guards, and the migration ledger entry commit together. Other migration errors still fail startup and roll back migration 123; they are not ignored.
3. Upgrading preserves transcript bytes and digests and unexpired evidence. It retains the existing retention policy, including bounded expiry and protected pending work.
4. Reopening the same database does not reapply migration 123, reset admission times, extend saved retention deadlines, or duplicate evidence.
5. Fresh databases and databases with all predecessor triggers still start normally.
6. This repair adds no accumulating application data or new retention policy. Disposable automated-test databases are removed when the test finishes.

## 3. User Scenarios (Behavioral Contract)

### Scenario: [SCN-RMIG-001] Upgrade with missing old guards
- **User Goal**: Recover router startup without losing saved work.
- **Given**: A database has completed migration 122, contains saved transcript and goal evidence, and is missing one or more triggers replaced by migration 123.
- **When**: The user starts the upgraded router and later restarts it against the same database.
- **Then**: Startup succeeds, migration 123 is recorded exactly once, the replacement guards enforce protected-evidence immutability, saved evidence and transcript bytes survive, and retention deadlines remain unchanged across restart. Browser REST and WebSocket calls work through the router proxy.

### Scenario: [SCN-RMIG-002] Start with intact or fresh schema
- **User Goal**: Continue starting normally without a manual repair.
- **Given**: The database is new or has completed migration 122 with all predecessor guards intact.
- **When**: The user starts the router.
- **Then**: Migration 123 succeeds, all six replacement guards exist, and the browser can reach the healthy router and register a WebSocket connection.

### Scenario: [SCN-RMIG-003] Unrelated migration failure remains visible and retryable
- **User Goal**: Avoid accepting an incomplete upgrade as a healthy router.
- **Given**: A migration-122 database has an unrelated schema conflict that makes migration 123 fail after its backfill begins.
- **When**: The user starts the router.
- **Then**: Startup fails before serving requests, migration 123 is not recorded, its schema and backfill changes roll back, and saved data remains intact. After the conflict is removed, normal startup completes the migration without duplicate data.

## 4. Automated E2E Verification Matrix

| Scenario ID | Scenario Description | E2E Test Suite | Test Case Name |
| :--- | :--- | :--- | :--- |
| SCN-RMIG-001 | Missing predecessors and restart | `tests/e2e/router-retention-migration.spec.ts` | `upgrades missing guards without losing evidence and reopens safely` |
| SCN-RMIG-002 | Intact and fresh schemas | `tests/e2e/router-retention-migration.spec.ts` | `starts with intact guards or a fresh database` |
| SCN-RMIG-003 | Atomic failure and retry | `tests/e2e/router-retention-migration.spec.ts` | `rolls back unrelated migration errors and retries normally` |

Supporting Go tests independently exercise each missing predecessor trigger, all missing triggers, preserved evidence, enforced guards, and transaction rollback through `Store.Open`.
