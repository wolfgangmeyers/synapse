# Spec: Fresh-machine Router Initialization

- **ID**: `SPEC-ROUTER-INIT-001`
- **Status**: `Active`
- **Domain**: `Settings`
- **Target E2E Suite**: `tests/e2e/router-initialization.spec.ts`

## 1. Intent & User Outcome

A developer runs `tools/initialize.sh` once after cloning the repository. It installs application dependencies, prepares local directories, and prints the commands to start the router and Web UI and the remaining provider sign-in step. Repeating initialization preserves existing local state and source changes. The router accepts ordinary non-writable ancestor permissions and requires a private credential directory.

## 2. Core Invariants

1. Initialization checks Git, Node.js (at least 22.19), npm, Go, and make before installations or local-state changes. Missing prerequisites produce actionable errors and a nonzero exit; the script does not install operating-system packages or invoke sudo.
2. Upstream Pi is installed with npm only when missing. The Wolfgang-maintained `pi-packages` fork is cloned on `main` beside this checkout only when absent; existing checkouts are not reset, pulled, switched, or overwritten. Its declared pnpm version installs frozen workspace dependencies. A non-user-writable global npm prefix produces an actionable error rather than invoking sudo.
3. Initialization installs locked repository, auth-helper, thin RPC extension, and Web UI dependencies and builds both the UI and router. Each failed step stops immediately with its name and the underlying command's diagnostics. Success is printed only after all steps pass.
4. New runtime directories use private permissions. The script repairs the application's state root and required runtime leaves to 0700, never the user's home or other ancestors, and preserves existing credential, database, and configuration contents.
5. Initialization never starts services, signs into providers, imports credentials, or changes the user's normal Pi extension configuration.
6. The Codex credential directory must be owned by the current user and mode 0700. User-owned ancestors may be 0750 or 0755 but must not be group- or world-writable. Credential files retain their existing owner, regular-file, single-link, 0600, and no-symlink requirements; ancestor checks remain unchanged.
7. The script does not create accumulating logs, histories, or archives. Dependency caches retain their package-manager-owned cleanup policy; no new application retention mechanism is introduced.

## 3. User Scenarios (Behavioral Contract)

### Scenario: [SCN-INIT-001] Initialize a fresh checkout
- **User Goal**: Prepare a machine for managed router startup with one command.
- **Given**: Required host commands are installed; Pi, the sibling fork, dependencies, and runtime directories are missing.
- **When**: The developer runs `tools/initialize.sh` from any working directory.
- **Then**: The script installs missing Pi, clones the maintained fork, installs its frozen workspace and all required application dependencies, builds the UI, prepares private runtime directories, and prints launch and provider sign-in instructions, including the explicit sibling-fork path when starting the router.

### Scenario: [SCN-INIT-002] Repeat initialization with existing state
- **User Goal**: Repair setup without losing local work or credentials.
- **Given**: Pi, a valid sibling fork with local edits, runtime state, and credentials already exist; the application state directory is 0775.
- **When**: The developer runs initialization again.
- **Then**: Pi is reused, the fork is not cloned or updated, dependencies are refreshed from lockfiles, the application state root is 0700, home permissions are unchanged, and existing data and fork edits are preserved.

### Scenario: [SCN-INIT-003] Missing or unsupported prerequisites
- **User Goal**: Know what to install before setup can proceed.
- **Given**: A required host command is missing, or Node is older than 22.19.
- **When**: The developer runs initialization.
- **Then**: The script exits nonzero with the missing command or supported Node version identified, without package installation or runtime-state changes.

### Scenario: [SCN-INIT-004] Installation or build failure
- **User Goal**: See the actual failing setup step.
- **Given**: A package install or UI build command fails.
- **When**: Initialization reaches that command.
- **Then**: Its diagnostics and the named failing step are shown, subsequent steps do not execute, and success is not reported.

### Scenario: [SCN-INIT-005] Existing invalid fork path
- **User Goal**: Preserve an unrelated directory at the expected fork location.
- **Given**: The sibling `pi-packages` path exists without the required `pi-subagents` source entry.
- **When**: Initialization runs.
- **Then**: It reports the path and required source entry, exits nonzero, and neither replaces nor modifies that directory.

### Scenario: [SCN-INIT-006] Start with ordinary credential-directory permissions
- **User Goal**: Keep credentials in a private directory without restricting ordinary ancestor permissions.
- **Given**: A user-owned ancestor has mode 0750 or 0755, and the credential directory is user-owned with mode 0700; no credential file exists.
- **When**: The managed router starts with that credential path.
- **Then**: Startup succeeds, browser REST and WebSocket connections work with the real SQLite-backed router, and the provider remains signed out until explicit login. Directory permissions are not silently changed by the credential store.

### Scenario: [SCN-INIT-007] Retain credential-file validation
- **User Goal**: Avoid accepting malformed credential storage or a non-private credential directory.
- **Given**: A configured credential file is a symlink or has mode 0644, or its containing directory has mode 0750, 0755, or 0775.
- **When**: The managed router starts.
- **Then**: Startup still fails before listener binding without exposing credential contents.

## 4. Automated E2E Verification Matrix

| Scenario ID | Scenario Description | E2E Test Suite | Test Case Name |
| :--- | :--- | :--- | :--- |
| SCN-INIT-001 | Fresh checkout | `tests/e2e/router-initialization.spec.ts` | `initializes a fresh checkout from another working directory` |
| SCN-INIT-002 | Repeat safely | `tests/e2e/router-initialization.spec.ts` | `preserves existing state and fork edits on rerun` |
| SCN-INIT-003 | Prerequisites | `tests/e2e/router-initialization.spec.ts` | `reports prerequisite errors before installing` |
| SCN-INIT-004 | Failed step | `tests/e2e/router-initialization.spec.ts` | `stops at the failing install or build step` |
| SCN-INIT-005 | Invalid fork path | `tests/e2e/router-initialization.spec.ts` | `preserves an invalid existing fork path` |
| SCN-INIT-006 | Directory permissions | `tests/e2e/router-initialization.spec.ts` | `starts with ordinary ancestor-directory permissions` |
| SCN-INIT-007 | File validation | `tests/e2e/router-initialization.spec.ts` | `rejects unsafe credential files before binding`; `rejects non-private credential directory` |
