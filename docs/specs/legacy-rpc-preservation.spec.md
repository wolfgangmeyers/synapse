# Spec: Legacy RPC preservation after Pi removal

- **ID**: `SPEC-AGENT-LEGACY-RPC-001`
- **Status**: `Draft`
- **Domain**: `AgentManagement`
- **Target E2E Suite**: `tests/e2e/legacy-rpc-preservation.spec.ts`

## Intent & User Outcome

Protect existing Pi RPC sessions when the runtime that could recover them is no longer installed.

## Core Invariants

1. Legacy RPC agents are never treated as embedded agents, resumed, migrated, reaped, or modified by the replacement router.
2. Unfinished legacy recovery and session data remain intact for an operator using a compatible version.
3. Startup reports an explicit unsupported legacy RPC error before opening the existing database for migrations, pruning, or reconciliation. This is a database preservation guarantee, not a guarantee that no other startup files or directories are touched. The router does not claim the old recovery contract still works.

## User Scenarios

### Scenario: Existing RPC agent on restart
- **Given** a database containing an RPC agent, including an unfinished operation or session.
- **When** the replacement router starts managed recovery.
- **Then** startup fails with a clear unsupported legacy RPC error, before migrations, pruning, or recovery run; the records and session files remain unchanged. This includes child records and databases predating the runtime column.

### Scenario: Database cannot be inspected
- **Given** an existing database cannot be read, or corruption is encountered during the preflight inspection.
- **When** startup checks for legacy RPC agents.
- **Then** startup fails closed rather than proceeding with migrations or recovery. This inspection is not a full database integrity check and does not guarantee detection of all corruption.

### Scenario: RPC agent in the WAL
- **Given** an existing database has an RPC agent in an uncheckpointed WAL.
- **When** startup checks for legacy RPC agents.
- **Then** it detects the agent and fails closed without migrating or reconciling the database.

### Scenario: Embedded-only database
- **Given** a database with no RPC agents.
- **When** the replacement router starts managed recovery.
- **Then** this preservation fence does not prevent normal embedded recovery.

## Coverage

SQLite tests cover pre-column databases, in-flight child preservation, corruption encountered during inspection, an inaccessible database, and an uncheckpointed WAL row. Browser E2E covers repeated real-router restarts, session preservation, and embedded recovery.
