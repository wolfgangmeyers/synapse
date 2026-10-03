# Implementation Plan: Google Sign-In Authentication Methods

## Executive Summary & Baseline

This plan details the implementation and verification for adding Vertex AI ADC and Antigravity OAuth login alongside the existing Google API key, unified in the router WebUI under a single Google settings section.

- **Requirements Baseline**: `docs/google-sign-in-requirements.md` (hash `f0be151c45e212a891c36345c73611c3fd023caa5810614d73a7638cddd4bb90`) defines R1–R5.
- **Technical Specification**: `docs/google-sign-in-technical-spec.md` (hash `8126ff71783fb4e9ba8bf72f8a6515b1985d613c90e9baa4b10ca3ba6a8b41d3`) defines component architecture, protocol, and constraints.
- **Specification-Review Status Evidence**:
  - Requirements candidate signed off by requirements persona (`cc0eced0`).
  - Revised spec (`44f72c`) signed off by security (`3cd56540`), architecture (`8a8e1312`), and test (`1bea7131`) personas.
  - Final delta confirmed the technical specification's final design snapshot requiring all 7 status columns (`provider, label, authenticated, status, account_label, updated_at, error`) across the 5 providers (`openai-codex`, `google`, `cursor`, `google-vertex`, `antigravity`) plus the table rendering fix (note: store production code itself remains unmodified prior to Phase 2.3); requirements confirmed final (`8126ff71`).
  - Independent validations completed by `610738ac` (architecture and auth isolation), `dc8ce95d` (contracts; removed mistaken ADC key warning), and `80656213` (test seams and mock boundaries).
  - No production implementation code has been committed or pushed in Phase 2.
- **Existing Worktree Baseline**:
  - `auth-helper`: 9 core helper tests passing (`npm test` in `auth-helper/` runs clean).
  - WebUI: 6 component tests passing in `web/router-ui-react/tests/component/provider-auth.test.tsx`.
  - Node dependencies are symlinked from shared paths. No packages may be installed into `node_modules`.

---

## Approved Requirements & Constraints Mapping

| Req | Description | Implementation Seam | Verification Seam |
| :--- | :--- | :--- | :--- |
| **R1** | Retain existing Google API key support (`GEMINI_API_KEY`). | `auth-helper/providers.mjs`, `web/.../ProviderAuthDialog.tsx` | Helper regression suite, WebUI component test |
| **R2** | Add Vertex AI ADC support. | `auth-helper/providers.mjs`, `router-proxy/cmd/mailbox-router/managed_adapters.go` | Helper ADC synthetic fixture test, Go router full auth test |
| **R3** | Add Antigravity personal-account login support. | `auth-helper/providers.mjs`, `router-proxy/cmd/mailbox-router/managed_adapters.go` | Helper OAuth PKCE & refresh mock test, Go router safe URL test |
| **R4** | Single Google settings section with method dropdown selector. | `web/router-ui-react/src/components/ProviderAuthDialog.tsx` | WebUI component tests for selector, label, and status binding |
| **R5** | Prompt for GCP Project ID and Region under Vertex ADC. | `auth-helper/providers.mjs`, WebUI dialog prompt handlers | Helper interactive multi-turn prompt test, WebUI prompt input test |

### Hard System Constraints
1. **Zero External Calls in Tests**: All automated tests deny outbound egress. Token exchange, userinfo discovery, CodeAssist API, and Vertex stream generation must use local deterministic HTTP mocks or preload interceptors.
2. **Store Boundaries & Privacy**: Secret API keys and OAuth refresh tokens live in the managed profile `auth.json` resolved via explicit `authPath` provided by router / active Pi `AuthStorage` (not unconditionally hardcoded `~/.pi/agent/auth.json`). `MECHA_HOST_HOME` is used exclusively for host ADC file discovery; Vertex secrets remain in host ADC files, while GCP Project ID, Region, and absolute ADC path are stored in `auth.json`. Router SQLite stores only non-secret metadata (`account_label`, `status`, `available`).
3. **Execution Runtime & Extension Isolation**: Managed Pi runs with `--no-extensions` by default. When Antigravity is configured, it receives `-e <resolved-antigravity-extension>`. Built-in `google` and `google-vertex` pass no extension flags.
4. **Credential Independence**: Switching dropdown selection or signing out of one method never mutates or erases credentials of another method.
5. **No Direct Store Bypass**: File writes to `auth.json` must use Pi `AuthStorage` locked operations (`modify`, `delete`) through `ModelRuntime` under the active profile directory.

---

## Red-First Integration & Unit Test Inventory

Per canonical `dev-loop` workflow, every test listed below must be authored in red state before corresponding implementation code is written.

### 1. Acceptance Criteria Mapping to Named Tests

| AC | Requirement | Test Name | File | Failure Condition (Red Bar) |
| :--- | :--- | :--- | :--- | :--- |
| **AC1** | R1 (API Key) | `Subtest: Google API key login preserves GEMINI_API_KEY and exposes Gemini developer catalog` | `auth-helper/auth-helper.test.mjs` | Fails if API key format is rejected or if existing Gemini developer catalog projection breaks. |
| **AC2** | R2 (Vertex ADC) | `Subtest: Vertex AI ADC login succeeds using host Application Default Credentials without prompting for API key` | `auth-helper/auth-helper.test.mjs` | Fails if `auth-helper` requires an API key secret or cannot resolve host ADC file. |
| **AC3** | R3 (Antigravity Login) | `Subtest: Antigravity login executes OAuth PKCE, captures redirect, and stores account tokens` | `auth-helper/auth-helper.test.mjs` | Fails if prompt URL is invalid, callback paste is rejected, or token payload cannot be saved. |
| **AC4** | R4 (Single Dropdown) | `renders exactly one Google section with an auth method selector switching between methods` | `web/router-ui-react/tests/component/provider-auth.test.tsx` | Fails if multiple Google cards render or selecting a method fails to update visible status. |
| **AC5** | R5 (Vertex Prompts) | `Subtest: Vertex AI ADC login prompts sequentially for Google Cloud Project ID and Region` | `auth-helper/auth-helper.test.mjs` | Fails if helper skips project or region prompt, or omits `us-central1` default region. |
| **AC6** | R2 (ADC Isolation) | `TestManagedVertexADCIsolationUnderManagedHome` | `router-proxy/cmd/mailbox-router/embedded_browser_controls_auth_test.go` | Fails if managed child under isolated `$HOME` fails to locate host ADC via `GOOGLE_APPLICATION_CREDENTIALS`. |
| **AC7** | R3 (Antigravity Stream & Mock) | `TestManagedAntigravityExecutionAndTokenRefresh` | `router-proxy/cmd/mailbox-router/embedded_browser_controls_auth_test.go` | Fails if process args lack `-e`, or mock streaming response fails under child isolated home, or runtime provider fails to refresh expired token during stream setup. |
| **AC8** | R1, R2, R3 (Coexistence) | `Subtest: Multiple Google authentication methods coexist independently without cross-logout deletion` | `auth-helper/auth-helper.test.mjs` | Fails if signing out of `google` deletes `google-vertex` or `antigravity` credentials in `auth.json`. |

### 2. Supporting Integration & Migration Tests

- **Migration 086 & Foreign Key Integrity**:
  - File: `router-proxy/internal/store/migrations_086_test.go`
  - Tests:
    - `TestMigration086PreservesExistingRowsAndEnforcesForeignKeys`: Populates pre-086 database with `managed_provider_status` and `managed_provider_models`, executes migration 086 staging swap (temporary snapshot copy without in-transaction `PRAGMA foreign_keys = OFF` or unsafe table renames), executes `PRAGMA foreign_key_check`, verifies zero FK violations, verifies default rows for `google-vertex` and `antigravity`, and confirms index `managed_provider_models_available_idx`.
- **Launch Recipe Digest & Lifecycle Stability**:
  - Files: `router-proxy/internal/store/managed_root_recovery_test.go`, `router-proxy/internal/managed/config_test.go`
  - Tests:
    - `TestManagedLaunchRecipeDigestLegacyStability`: Verifies a recipe struct without `AntigravityExtension` serializes with `antigravity_extension` omitted (`omitempty`), yielding the exact golden SHA256 digest of pre-086 recipes.
    - `TestManagedLaunchRecipeDigestWithAntigravity`: Verifies a recipe with non-empty `AntigravityExtension` produces an updated, deterministic digest.
    - `TestManagedLaunchRecipeSupervisorBuilders`: Verifies recipe validation, digest computation, and SQL builder generation across supervisor reset (`supervisor_reset_s1.go`), restart (`supervisor_restart_s3.go`), and cold recovery (`root_cold_recovery.go`).
- **Prompt URL Security Guard**:
  - File: `router-proxy/cmd/mailbox-router/managed_adapters_test.go`
  - Tests:
    - `TestSafeProviderPromptURL`: Verifies `safeProviderPromptURL` accepts `https://accounts.google.com/o/oauth2/v2/auth?...` for provider `antigravity`, accepts `https://auth.openai.com/...` for `openai-codex`, and rejects non-HTTPS, unknown hostnames, credentials in URL, port numbers, or path mismatches.
- **Auth Helper Missing Extension Fallback**:
  - File: `auth-helper/auth-helper.test.mjs`
  - Tests:
    - `Subtest: Missing Antigravity extension path disables provider registration without triggering external package install`: Verifies that pointing to an absent extension directory returns a clean actionable error for Antigravity while keeping `openai-codex`, `google`, `google-vertex`, and `cursor` fully functional.

---

## Safe Execution Ordering (TDD Phasing)

Implementation follows an explicit four-owner dependency handshake. Store types and migration schema contracts are established first before Go runtime compilation, allowing Split A (auth-helper), Split B (store/migration), Split C (managed runtime), and Split D (WebUI) to develop red-first unit and integration tests independently in parallel:

```
[Phase 2.1: Store Contract & Red Tests First]
       │
       ▼
[Phase 2.2: Auth Helper & Protocols] ────► Verify Helper Tests (node --test)
       │
       ▼
[Phase 2.3: Go Store Migration 086] ─────► Verify Migration & FK Tests
       │
       ▼
[Phase 2.4: Go Backend Core Plumbing] ───► Verify Process, Recipe, Adapters, Coordinator
       │
       ▼
[Phase 2.5: WebUI React Dialog & Guard] ─► Verify Component & Transport Tests
       │
       ▼
[Phase 2.6: End-to-End Behavioral Proof] ─► Verify Full-Stack Integration & Coverage
```

### Phase 2.1: Red-First Test Creation & Store Contract Handshake
1. Lock store schema types and 5-provider enum contract (`router-proxy/internal/store/managed.go`).
2. Write red unit/integration tests in `auth-helper/auth-helper.test.mjs`.
3. Write red Go tests in `router-proxy/internal/store/migrations_086_test.go` and `router-proxy/cmd/mailbox-router/embedded_browser_controls_auth_test.go`.
4. Write red UI component and transport guard tests in `web/router-ui-react/tests/component/provider-auth.test.tsx`, `web/router-ui-react/tests/unit/protocol-guards.test.ts`, and `web/router-ui-react/tests/transport/http-contract.test.ts`.
5. Run each test suite to confirm expected red failures matching target features.

### Phase 2.2: Auth-Helper Implementation
1. Update `auth-helper/protocol.mjs`:
   - Expand `PROVIDERS` set to include `google-vertex` and `antigravity`.
   - Update provider snapshot count check to expect 5 providers (`openai-codex`, `google`, `google-vertex`, `antigravity`, `cursor`).
2. Update `auth-helper/providers.mjs`:
   - Implement `google-vertex` login handler: resolve host ADC file path using `MECHA_HOST_HOME` only, prompt for `GOOGLE_CLOUD_PROJECT` and `GOOGLE_CLOUD_LOCATION` (default `us-central1`), verify ADC file exists, write native `ApiKeyCredential` to `auth.json` via active `AuthStorage`.
   - Implement `antigravity` loader using Pi bundled `jiti-static-loader.js` referencing the canonical host path (default `~/.pi/agent/npm/node_modules/pi-antigravity/src/index.ts` expanded to absolute path before isolation) with module aliases (`@earendil-works/pi-ai`, `@earendil-works/pi-ai/compat`, `@earendil-works/pi-coding-agent`).
   - Deliberately register supported model and auth exports via `jiti` without invoking the full UI extension factory (preserving signed spec runtime partial registration).
   - If Antigravity extension path is missing or invalid, record an actionable error for Antigravity while keeping other providers fully functional.
3. Verify all `auth-helper` tests pass green.

### Phase 2.3: Go Store Migration 086
1. Create `router-proxy/internal/store/migrations/086_google_auth_methods.sql`:
   - Add `antigravity_extension` column to `managed_launch_recipes`.
   - Perform safe temporary snapshot staging swap for `managed_provider_status` and `managed_provider_models` with expanded CHECK constraints without disabling foreign keys in-transaction.
   - Insert default rows for `google-vertex` and `antigravity`.
2. Update store definitions and validation in `router-proxy/internal/store/managed.go`:
   - Expand `managedProviderNames` slice to include `google-vertex` and `antigravity`.
   - Update `managedProviderLabels` map with human-readable labels for `google-vertex` and `antigravity`.
   - Update `CommitManagedProviderSnapshot` to enforce `len(providers) != 5`, update membership validation across all 5 providers (`openai-codex`, `google`, `cursor`, `google-vertex`, `antigravity`), and maintain accurate 7-column projection (`provider, label, authenticated, status, account_label, updated_at, error`).
   - Add regression tests in `router-proxy/internal/store/managed_test.go` verifying len==5 enforcement, invalid provider rejection, duplicate rejection, and atomic model catalog replacement.
3. Verify migration passes `TestMigration086PreservesExistingRowsAndEnforcesForeignKeys` and `PRAGMA foreign_key_check`.

### Phase 2.4: Go Backend Core Plumbing
1. Update `router-proxy/cmd/mailbox-router/managed_adapters.go`:
   - Forward `MECHA_HOST_HOME`, host `GOOGLE_APPLICATION_CREDENTIALS`, and `CLOUDSDK_CONFIG` in child environment for host ADC discovery. (Synthetic allowlist preserving `$PATH` without host credentials is test-only).
   - Update `safeProviderPromptURL` to allow `accounts.google.com/o/oauth2/v2/auth` for `antigravity`.
2. Update `router-proxy/internal/managed/auth.go`:
   - Update `validProviderAuthProvider` to accept `google-vertex` and `antigravity`.
3. Update `router-proxy/internal/store/managed_root_recovery.go`:
   - Add `AntigravityExtension string` with `json:"antigravity_extension,omitempty"`.
   - Plumb SQL queries and digest validation across supervisor reset, restart, and cold recovery.
4. Update `router-proxy/internal/managed/config.go` & `process.go`:
   - Plumb `AntigravityExtension` into `Config`, CLI flags (`--managed-antigravity-extension`), and `CommandSpec` (resolves absolute host path before isolation, appending `-e <resolved-path>` argument; relative path is forbidden as it breaks worker CWD).
5. Update `tools/run_router.sh` to wire the shared configured Antigravity extension entry path identically for helper and managed runtime.

### Phase 2.5: WebUI React Implementation
1. Update `web/router-ui-react/src/protocol/types.ts` & `src/protocol/guards.ts`:
   - Expand `ManagedProvider` union type to `"openai-codex" | "google" | "cursor" | "google-vertex" | "antigravity"`.
   - Update type guards for provider responses, prompt requests, and status views.
2. Update `web/router-ui-react/src/transport/http-client.ts`:
   - Update provider type unions and parameter guards for auth endpoints.
3. Update `web/router-ui-react/src/components/ProviderAuthDialog.tsx`:
   - Consolidate Google providers under a single section with an Auth Method `<select>` dropdown (`api_key` -> `google`, `vertex_adc` -> `google-vertex`, `antigravity` -> `antigravity`).
   - Bind status indicators, connect buttons, and sequential prompt forms (Project ID / Region for Vertex ADC; OAuth URL link / callback paste for Antigravity).
4. Verify WebUI component and transport guard tests pass green.

### Phase 2.6: End-to-End Behavioral Proof & Coverage
1. Execute full Go integration tests with `-buildvcs=false -coverprofile=coverage.out`.
2. Execute full auth helper tests with test-only mock preloads.
3. Run coverage instrumentation across all changed packages and verify zero blocking or should-add gaps.

---

## File Ownership Splits for Implementation

To support safe parallel execution across separate subagents or workers without edit collisions:

### Split A: Auth Helper & Protocols (`auth-helper/`)
- `auth-helper/protocol.mjs`
- `auth-helper/providers.mjs`
- `auth-helper/auth-helper.test.mjs`
- `auth-helper/test-preload-mock-fetch.mjs` (test-only mock helper)

### Split B: Go Backend Storage & Migration (`router-proxy/internal/store/`)
- `router-proxy/internal/store/migrations/086_google_auth_methods.sql`
- `router-proxy/internal/store/managed.go`
- `router-proxy/internal/store/managed_test.go`
- `router-proxy/internal/store/managed_root_recovery.go`
- `router-proxy/internal/store/managed_root_recovery_operator.go`
- `router-proxy/internal/store/managed_root_recovery_test.go`
- `router-proxy/internal/store/migrations_086_test.go`

### Split C: Go Backend Managed Runtime & Router (`router-proxy/internal/managed/`, `router-proxy/cmd/`)
- `router-proxy/cmd/mailbox-router/main.go`
- `router-proxy/cmd/mailbox-router/managed_adapters.go`
- `router-proxy/cmd/mailbox-router/managed_adapters_test.go`
- `router-proxy/cmd/mailbox-router/embedded_browser_controls_auth_test.go`
- `router-proxy/internal/managed/auth.go`
- `router-proxy/internal/managed/config.go`
- `router-proxy/internal/managed/config_test.go`
- `router-proxy/internal/managed/process.go`
- `router-proxy/internal/managed/process_test.go`
- `router-proxy/internal/managed/supervisor.go`
- `router-proxy/internal/managed/supervisor_reset_s1.go`
- `router-proxy/internal/managed/supervisor_restart_s3.go`
- `router-proxy/internal/managed/root_cold_recovery.go`
- `tools/run_router.sh`

### Split D: WebUI React Frontend (`web/router-ui-react/`)
- `web/router-ui-react/src/protocol/types.ts`
- `web/router-ui-react/src/protocol/guards.ts`
- `web/router-ui-react/src/transport/http-client.ts`
- `web/router-ui-react/src/components/ProviderAuthDialog.tsx`
- `web/router-ui-react/tests/component/provider-auth.test.tsx`
- `web/router-ui-react/tests/unit/protocol-guards.test.ts`
- `web/router-ui-react/tests/transport/http-contract.test.ts`

---

## Automated Test Mocking & Boundary Architecture

### 1. Test Bootstrap Egress Denial & Mock Endpoints
Automated test environments strictly mock external endpoints at the process boundary without external network access:
- **Node.js Subprocesses (`auth-helper`)**:
  - Runner uses `NODE_OPTIONS="--import ./test-preload-mock-fetch.mjs"`.
  - The preload intercepts `globalThis.fetch` and `undici.request`.
  - Handled endpoints reflect exact endpoints from installed `pi-antigravity/src/auth/oauth.ts` and client calls:
    - OAuth authorize: `https://accounts.google.com/o/oauth2/v2/auth` (`AUTH_URL`)
    - Local callback: `http://localhost:51121/oauth-callback` (`REDIRECT_URI`)
    - Token exchange & refresh: `https://oauth2.googleapis.com/token` (`TOKEN_URL`)
    - Userinfo discovery: `https://www.googleapis.com/oauth2/v1/userinfo?alt=json`
    - Cloud Code / CodeAssist client: `https://daily-cloudcode-pa.googleapis.com` (`DEFAULT_ENDPOINT`)
    - Any other endpoint must be verified directly against installed package source before authoring mocks rather than inventing paths.
  - Synthetic ADC environment uses an explicit test-only allowlist: preserves essential tool `$PATH` while denying host gcloud credentials.
  - Any request outside the mocked endpoints throws an unhandled network error (`EgressBlocked: external network call forbidden in test`).
- **Go Tests (`router-proxy`)**:
  - `httptest.Server` provides local loopback endpoints.
  - No production endpoints are configured or reachable.

### 2. Interactive Stdin Harness for Multi-Turn Prompts
The `auth-helper` test harness runs helper subprocesses with piped stdio:
- Harness reads JSONL lines from helper stdout.
- When an event of type `"prompt"` is encountered, the harness correlates `prompt.id` to `prompt_id` and writes a corresponding JSON response to stdin matching the exact contract:
  ```json
  {"type":"response","prompt_id":"prompt-1","value":"test-gcp-project"}
  ```
- Named fixtures test callback cancel/retry cleanup, token refresh, and sequential prompts (Project ID followed by Region) to ensure clean termination without hanging.

### 3. Actual SDK Streaming Mock Proof (AC7)
To fulfill AC7, the integration test in `embedded_browser_controls_auth_test.go` exercises direct managed Pi SDK streaming with external-boundary mocks only for both Vertex AI and Antigravity:
- Spawns managed child agent configured with provider `antigravity` (and counterpart for `google-vertex`).
- Injects a mock local streaming handler representing the external model endpoint.
- Managed process initiates an actual prompt turn and receives at least one streamed text chunk.
- Simulates token expiration and verifies runtime managed provider executes token refresh / credential resolution during stream setup.
- Named fixtures test token expiration handling, stub RPC response streaming assertions, callback cancellation, retry cleanup, and stream closure.

---

## Coverage Measurement Strategy

To satisfy Phase 4 coverage gates without test flakiness or unmonitored packages:

1. **Go Coverage**:
   - Must run across all affected packages together with an explicit coverage profile:
     ```bash
     go test -buildvcs=false -coverprofile=coverage.out -coverpkg=./cmd/mailbox-router,./internal/managed,./internal/store,./internal/transport ./cmd/mailbox-router ./internal/store ./internal/managed
     ```
   - All newly added functions and branches (e.g. `safeProviderPromptURL`, `ValidateManagedLaunchRecipe`, `validProviderAuthProvider`, migration 086 logic) must achieve 100% path coverage.
2. **WebUI Coverage**:
   - Run scoped Vitest coverage targeting transport guards (`tests/unit/protocol-guards.test.ts`), HTTP contracts (`tests/transport/http-contract.test.ts`), and dialog components (`tests/component/provider-auth.test.tsx`):
     ```bash
     npx vitest run tests/component/provider-auth.test.tsx tests/unit/protocol-guards.test.ts tests/transport/http-contract.test.ts --coverage.enabled=true --coverage.provider=v8 --coverage.reportsDirectory=/tmp/scoped-vitest-coverage
     ```
3. **Auth Helper Coverage**:
   - `auth-helper` subprocesses run with `NODE_V8_COVERAGE=/tmp/auth-helper-coverage`.
   - Normalizer script parses raw V8 coverage range blocks into standard per-function and per-branch metrics over `protocol.mjs` and `providers.mjs`.
4. **Retention of Coverage Artifacts**:
   - Test evidence and coverage artifacts are bounded to supervisor run directories under a safe owned prefix (`/tmp/mecha-test-runs/`), retaining at most the last 3 runs or 7 days. Automated wrapper prunes old directories on start and runs trap cleanup on exit. No new production TTL feature is introduced.

---

## Verification Invariants

Verification must satisfy the following strict invariants:
- **Requirements Scope**: Addresses R1–R5 from `docs/google-sign-in-requirements.md` without adding unrequested human requirements or refactoring unrelated UI.
- **Store & Secret Separation**: Secrets remain in host ADC files or managed profile `auth.json`. SQLite stores only non-secret metadata.
- **Launch Recipe Stability**: Non-empty `AntigravityExtension` updates digest; empty extension omits field via `omitempty` to preserve legacy SHA256 stability across reset, restart, and cold recovery.
- **Foreign Key Safety**: Migration 086 staging swap verified with `PRAGMA foreign_key_check` without turning off foreign keys inside transactions.

---

## Concrete Environmental Defaults

1. **Antigravity Extension Local Host Path**:
   - Canonical installed host source path default: `~/.pi/agent/npm/node_modules/pi-antigravity/src/index.ts`.
   - The path must be expanded to an absolute path on the host before managed child agent process isolation, as a relative `node_modules` path breaks under worker cwd.
   - Shared configured entry path is used identically across auth helper and managed runtime (managed command spec appends `-e <resolved-absolute-path>`).
   - If `--managed-antigravity-extension` is omitted or points to an absent path, helper disables Antigravity provider registration and returns an actionable error on login attempts, keeping all other providers (`openai-codex`, `google`, `google-vertex`, `cursor`) fully operational.
2. **WebUI Initial Google Method Fallback Order**:
   - When opening the Google settings section, if none of the Google methods are authenticated, the UI defaults to `"api_key"`. If one is authenticated, it defaults to the authenticated method. If multiple are authenticated, priority order is `"api_key"`, then `"vertex_adc"`, then `"antigravity"`.
