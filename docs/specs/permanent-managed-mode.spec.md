# Spec: Permanent Managed Mode

- **ID**: `SPEC-PERMANENT-MANAGED-001`
- **Status**: `Active`
- **Domain**: `AgentManagement`
- **Target E2E Suite**: `tests/e2e/router-initialization.spec.ts`

## 1. Intent & User Outcome

The mailbox router operates exclusively in managed mode. Managed routes (`/v1/managed-agents`, `/v1/models`, `/v1/settings/*`), embedded runtime capabilities, provider auth coordination, and stage manifest validation are permanently active across all router executions. Developers and test harnesses no longer pass `--managed` or configure `MECHA_ROUTER_MANAGED`, eliminating configuration ambiguity and accidental fallback to legacy unmanaged states. Any attempt to supply the obsolete `--managed` flag fails fast with an undefined flag error.

## 2. Core Invariants

1. The mailbox router MUST unconditionally configure and initialize managed mode, manager runtime, provider authentication coordinator, and stage manifest validation on every startup.
2. The `--managed` command-line flag MUST NOT exist on the router CLI; supplying `-managed`, `--managed`, `--managed=true`, or `--managed=false` MUST immediately fail with standard flag rejection (`flag provided but not defined: -managed`).
3. Managed HTTP endpoints (e.g., `GET /v1/managed-agents`, `GET /v1/models`) MUST be active upon router readiness and MUST NOT return `503 Service Unavailable` due to disabled managed mode.
4. Legacy mailbox messaging routes, WebSockets, database attachment stores, and tmux integration MUST continue to function normally with managed mode permanently enabled.
5. The `tools/run_router.sh` launcher script MUST prepare managed directories and auth-helper dependencies unconditionally, without checking or accepting `MECHA_ROUTER_MANAGED=false`.

## 3. User Scenarios (Behavioral Contract)

### Scenario: [SCN-MANAGED-001] Router starts with managed mode permanently active
- **User Goal**: Run the router with default configuration and access managed agent management routes.
- **Given**: Minimal required managed configuration paths (database, manifest, session root, agent directory, etc.) are provided.
- **When**: The router binary is executed without `--managed`.
- **Then**:
  - The router starts cleanly and signals `MECHA_V2_ROUTER_READY`.
  - Managed routes such as `GET /v1/managed-agents` return `200 OK` rather than `503 Service Unavailable`.
  - Embedded runtime, provider auth coordinator, and root control handlers are initialized and active.

### Scenario: [SCN-MANAGED-002] CLI rejects obsolete `--managed` flag
- **User Goal**: Detect stale automation or launcher scripts that attempt to toggle managed mode.
- **Given**: A caller attempts to execute `mailbox-router` passing `--managed=false` or `--managed=true`.
- **When**: The router parses its command-line flags.
- **Then**:
  - Startup halts immediately before database initialization or listener binding.
  - An error stating `flag provided but not defined: -managed` is emitted to standard error with a nonzero exit code.

### Scenario: [SCN-MANAGED-003] Launcher runs in permanent managed mode
- **User Goal**: Launch the router using `tools/run_router.sh` without requiring managed-mode environment variables.
- **Given**: Standard repository dependencies and environment.
- **When**: `tools/run_router.sh` is executed.
- **Then**:
  - Managed home and session directories are prepared with mode `0700`.
  - Auth helper dependencies are verified and installed if needed.
  - The router is launched with managed routes active and restarts automatically on unexpected exits.

## 4. Automated E2E & Unit Verification Matrix

| Scenario ID | Scenario Description | Test Suite | Test Case Name |
| :--- | :--- | :--- | :--- |
| SCN-MANAGED-001 | Managed routes active by default | `router-proxy/cmd/mailbox-router` | `TestCommandStartsWithDistinctDatabaseLogAttachmentsAndPort` |
| SCN-MANAGED-002 | CLI rejects `--managed` flag | `router-proxy/cmd/mailbox-router` | `TestParseConfigRejectsManagedFlagAndEnforcesPermanentManagedMode` |
| SCN-MANAGED-003 | Launcher operates in permanent mode | `router-proxy/cmd/mailbox-router` | `TestLauncherRestartBehavior` |
