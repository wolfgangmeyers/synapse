# Technical Specification: Google Sign-In Methods

## Problem

The router currently supports Google authentication only through a single API key field in the WebUI. Wolfgang needs three distinct authentication methods:
1. Google AI Studio API key (`GEMINI_API_KEY`).
2. Google Cloud Vertex AI Application Default Credentials (`ADC`).
3. Google Antigravity OAuth login using a personal subscription.

The WebUI must unify these methods under a single Google settings section with an authentication method selector rather than separate provider cards.

---

## Requirements

This specification implements the settled requirements from `docs/google-sign-in-requirements.md`:
- **R1**: Retain existing Google API key support.
- **R2**: Add Vertex AI Application Default Credentials (ADC) support.
- **R3**: Add Antigravity personal-account login support using a personal subscription.
- **R4**: Provide a single Google tab with an authentication method dropdown selector.
- **R5**: Prompt for Google Cloud Project ID and Region when Vertex AI ADC is selected.

---

## Constraints

1. **Storage privacy and authoritative paths**:
   - Google AI Studio API key and Antigravity OAuth tokens live in `~/.pi/agent/auth.json`.
   - Vertex AI ADC secrets live exclusively in host ADC files (`~/.config/gcloud/application_default_credentials.json` or an external service account key file), not in `auth.json`. `auth.json` stores only the non-secret GCP Project ID, Region, and pointer path.
   - The router SQLite database stores only non-secret provider metadata: status (`authenticated` vs `signed_out`), account label (masked email or GCP project ID), and model catalog.
2. **Zero external network calls in automated tests**: External Google OAuth, UserInfo, CodeAssist, and Vertex streaming endpoints must be mocked deterministically in automated tests. Outbound egress outside mocks must be denied.
3. **Execution runtime isolation**: Managed Pi processes run with `--no-extensions` by default. Built-in providers (`google`, `google-vertex`) need no extension flags. Antigravity requires passing the extension path via `-e`.
4. **Credential mutation boundary**: Changing the UI dropdown switches the active configuration view. It does not sign out or mutate other configured authentication methods.
5. **No direct credential store bypass**: Credential modifications must use Pi `AuthStorage` locked operations (`modify`, `delete`) through `ModelRuntime`. Direct writes that bypass file locking are prohibited.
6. **No external installs**: Tests and runtime helpers rely only on existing pre-installed packages or local fixtures. No external network package installation.

---

## Observed Code vs. Proposed Technical Choices

| Seam | Observed Current Code | Proposed Technical Choice | Rationale |
| :--- | :--- | :--- | :--- |
| **Auth helper entrypoint** | `auth-helper/index.mjs` accepts only `openai-codex`, `google`, `cursor` | Keep `auth-helper/index.mjs`. Add `google-vertex` and `antigravity` to `PROVIDERS` in `protocol.mjs` and `providers.mjs` | Preserves existing JSONL stdio protocol contract without extra IPC channels. |
| **Provider status count check** | `auth-helper/protocol.mjs` enforces `statuses.length !== 3` | Update check to enforce 5 providers (`openai-codex`, `google`, `google-vertex`, `antigravity`, `cursor`) | Helper reports a complete snapshot across all configured backends. |
| **Database CHECK constraints** | `managed_provider_status` and `managed_provider_models` check `provider IN ('openai-codex','google','cursor')` | Migration `086_google_auth_methods.sql` expands allowed values to include `'google-vertex'` and `'antigravity'` | Preserves existing rows, foreign keys, index, and columns `account_label` and `error`. |
| **Durable launch recipes** | `managed_launch_recipes` stores `cursor_extension` but lacks Antigravity extension column | Migration `086` adds `antigravity_extension TEXT NOT NULL DEFAULT '' CHECK (length(CAST(antigravity_extension AS BLOB)) <= 4096)` | Supports cold recovery and restart validation for Antigravity workers. |
| **Launch recipe digest stability** | `ManagedLaunchRecipeDigest` computes SHA256 over struct fields after clearing hash and timestamps | Add `AntigravityExtension string` with JSON tag `antigravity_extension,omitempty` to `ManagedLaunchRecipe` struct | Using `omitempty` ensures empty values are omitted, preserving identical JSON serialization and SHA256 digests for legacy recipes. Non-empty values participate in the digest. |
| **Coordinator validation** | `validProviderAuthProvider` allows only 3 providers | Accept `google-vertex` and `antigravity` | Enables `/v1/provider-auth/{provider}/{action}` routes for all methods. |
| **Prompt URL sanitization** | `safeProviderPromptURL` allows only `auth.openai.com` | Allow `accounts.google.com/o/oauth2/v2/auth` for `provider == "antigravity"` | Prevents open redirect while permitting Google OAuth authorization URL. |
| **Antigravity module loading** | Node 22 type stripping rejects TS files under `node_modules` | Load extension using Pi bundled jiti static loader with `@earendil-works/pi-ai` alias | Reuses Pi extension loader and avoids custom duplicate OAuth code. |
| **Antigravity runtime registration** | `ModelRuntime` lacks `ExtensionAPI.registerCommand` | Import specific exports (`getCurrentAntigravityCatalog`, `loginAntigravity`, `refreshAntigravityToken`, `getApiKey`) and call `runtime.registerProvider("antigravity", ...)` | Avoids calling unsupported UI command registration on `ModelRuntime`. |
| **Antigravity missing fallback** | Missing extension path or missing package | When extension is absent, disable helper registration and return an explicit safe error | Prevents partial provider registration; rejects external package installs. |
| **Vertex ADC isolation** | Managed environment sets `$HOME` to `~/.mecha-developer/managed-pi`, breaking `os.homedir()` ADC lookup | Forward host `GOOGLE_APPLICATION_CREDENTIALS` and `CLOUDSDK_CONFIG`, resolve absolute path to host ADC, store in `credential.env.GOOGLE_APPLICATION_CREDENTIALS` | Compatible with `@google/genai` `keyFilename` option without hardcoding user home in code. |
| **Vertex ADC credential shape** | Not implemented in helper | Native env-only `ApiKeyCredential` shape: `{ type: "api_key", env: { GOOGLE_CLOUD_PROJECT, GOOGLE_CLOUD_LOCATION, GOOGLE_APPLICATION_CREDENTIALS } }` | Native Pi credential format without speculative placeholder markers. |
| **Managed Pi extension wiring** | `process.go` passes `-e` only for MechaRPC, PiSubagents, Cursor | Plumb `AntigravityExtension` through `Config`, `CommandSpec`, `LaunchRecipe`, and CLI flags matching `CursorExtension` | `--no-extensions` disables extension autoloading from `settings.json`. |
| **WebUI provider layout** | `ProviderAuthDialog.tsx` renders one row per provider | Unify `google`, `google-vertex`, and `antigravity` under single Google section with an Auth Method `<select>` | Eliminates UI clutter while maintaining accessible form labels. |
| **UI Transport & Guards** | UI transport and guards union only 3 providers | Expand provider unions to `"openai-codex" | "google" | "cursor" | "google-vertex" | "antigravity"` | Enforces end-to-end type safety across transport boundaries. |

---

## Component Architecture & Implementation Design

### 1. WebUI: Single Google Section (`web/router-ui-react/src/components/ProviderAuthDialog.tsx`)

The dialog displays a single Google section with an authentication method selector.

1. **State & Mapping**:
   - `selectedGoogleMethod`: `"api_key"` | `"vertex_adc"` | `"antigravity"`. Defaults to the first authenticated Google provider, or `"api_key"`.
   - Provider mapping:
     - `"api_key"` -> `"google"`
     - `"vertex_adc"` -> `"google-vertex"`
     - `"antigravity"` -> `"antigravity"`
2. **View Presentation**:
   - Status line displays connection state for the selected method:
     - For `google`: "Connected" or "Not connected".
     - For `google-vertex`: "Connected (project: <project-id>)" or "Not connected". Helper text notes that `gcloud auth application-default login` must be completed on the host.
     - For `antigravity`: "Connected (<user-email>)" or "Not connected".
   - The Connect / Sign Out button dispatches to the selected provider.
3. **Prompt Flow**:
   - `google`: secret prompt for API key.
   - `google-vertex`: prompt 1 for GCP Project ID (`text`), prompt 2 for GCP Region (`text`, placeholder `us-central1`).
   - `antigravity`: displays link to Google OAuth page. Text input allows pasting the callback URL for remote/headless sessions.
4. **Transport & Guards**:
   - `web/router-ui-react/src/transport/http-client.ts`, `src/protocol/types.ts`, and `src/protocol/guards.ts` expand provider unions to include `"google-vertex"` and `"antigravity"`.
   - Form inputs maintain accessible `<label>` and `aria-labelledby` attributes.

### 2. Auth Helper (`auth-helper/`)

#### A. Host Home Capture & ADC Resolution
When `router-proxy` spawns `auth-helper`, `managed.ChildEnvironment` isolates `$HOME` to `~/.mecha-developer/managed-pi`.

To prevent breaking ADC lookup:
1. `router-proxy/cmd/mailbox-router/managed_adapters.go` captures `MECHA_HOST_HOME` (`os.UserHomeDir()` or `os.Getenv("HOME")`) and forwards host `GOOGLE_APPLICATION_CREDENTIALS` and `CLOUDSDK_CONFIG` before isolation.
2. In `auth-helper/providers.mjs`, when logging in to `google-vertex`:
   - Prompts for `GOOGLE_CLOUD_PROJECT` (text).
   - Prompts for `GOOGLE_CLOUD_LOCATION` (text, default `us-central1`).
   - Resolves absolute host ADC file path in standard order:
     - `process.env.GOOGLE_APPLICATION_CREDENTIALS`
     - `path.join(process.env.CLOUDSDK_CONFIG, "application_default_credentials.json")` (if set)
     - `path.join(process.env.MECHA_HOST_HOME, ".config", "gcloud", "application_default_credentials.json")`
   - Verifies file existence on host. If missing, login rejects with guidance to run `gcloud auth application-default login`.
   - Writes native `ApiKeyCredential` to `auth.json` via Pi `AuthStorage` under lock:
     ```json
     {
       "google-vertex": {
         "type": "api_key",
         "env": {
           "GOOGLE_CLOUD_PROJECT": "<project-id>",
           "GOOGLE_CLOUD_LOCATION": "<location>",
           "GOOGLE_APPLICATION_CREDENTIALS": "<absolute-adc-path>"
         }
       }
     }
     ```
3. When `runtime.getAvailable()` runs, `@earendil-works/pi-ai` verifies `ctx.fileExists(adcPath)` and surfaces built-in Vertex models.

#### B. Antigravity Extension Loading & Registration
`pi-antigravity` source is loaded using Pi's bundled jiti static loader:
```javascript
import { createJiti } from "@earendil-works/pi-coding-agent/dist/core/extensions/jiti-static-loader.js";
import { fileURLToPath } from "node:url";

const piAiPath = fileURLToPath(import.meta.resolve("@earendil-works/pi-ai/compat"));
const piCodingPath = fileURLToPath(import.meta.resolve("@earendil-works/pi-coding-agent"));

const jiti = createJiti(import.meta.url, {
  alias: {
    "@earendil-works/pi-ai": piAiPath,
    "@earendil-works/pi-ai/compat": piAiPath,
    "@earendil-works/pi-coding-agent": piCodingPath,
  },
});

const modelsMod = await jiti.import(antigravityModelsPath);
const authMod = await jiti.import(antigravityAuthPath);

const catalog = modelsMod.getCurrentAntigravityCatalog();

runtime.registerProvider("antigravity", {
  name: "Google Antigravity",
  models: catalog.models,
  oauth: {
    name: "Google Antigravity",
    login: authMod.loginAntigravity,
    refreshToken: authMod.refreshAntigravityToken,
    getApiKey: authMod.getApiKey,
  },
});
```

If the extension path does not exist on disk, helper registration is disabled and returns an explicit safe error indicating the extension is missing. The helper must not perform partial provider registration or trigger external package installations.

### 3. Router Go Backend (`router-proxy/`)

#### A. Database Migration (`router-proxy/internal/store/migrations/086_google_auth_methods.sql`)
1. **Launch Recipes Table**: Adds column `antigravity_extension TEXT NOT NULL DEFAULT '' CHECK (length(CAST(antigravity_extension AS BLOB)) <= 4096)` to `managed_launch_recipes`.
2. **Provider Status & Models Swap**:
   - Expands CHECK constraints on `managed_provider_status` and `managed_provider_models` to allow `'google-vertex'` and `'antigravity'`.
   - Migration staging steps:
     - Create a temporary copied status data table (all original 034 columns: `provider`, `label`, `authenticated`, `status`, `account_label`, `updated_at`, `error`) without constraints to snapshot existing status rows.
     - Copy existing data from `managed_provider_status` into the temporary status data table.
     - Create a temporary copied model data table without foreign key constraints to snapshot existing model rows.
     - Copy existing data from `managed_provider_models` into the temporary model data table.
     - Drop old child table `managed_provider_models`.
     - Drop old parent table `managed_provider_status`.
     - Create expanded parent table `managed_provider_status` with updated CHECK constraint and all original columns (`provider`, `label`, `authenticated`, `status`, `account_label`, `updated_at`, `error`).
     - Create expanded child table `managed_provider_models` with updated CHECK constraint, matching original columns (`provider`, `model_id`, `name`, `available`, `updated_at`), primary key, and `FOREIGN KEY (provider) REFERENCES managed_provider_status(provider) ON DELETE CASCADE`.
     - Restore parent rows from the temporary status snapshot table into `managed_provider_status`.
     - Restore child rows from the temporary model snapshot table into `managed_provider_models`.
     - Drop temporary status snapshot table and temporary model snapshot table.
     - Recreate index `managed_provider_models_available_idx ON managed_provider_models (available, provider, model_id)`.
     - Insert default signed-out rows for `google-vertex` and `antigravity`.
   - Migration behavior tests explicitly run `PRAGMA foreign_key_check` on the database connection to verify constraint integrity after execution.

#### B. Safe Prompt URL Sanitization (`router-proxy/cmd/mailbox-router/managed_adapters.go`)
Update `safeProviderPromptURL`:
```go
func safeProviderPromptURL(provider, raw string) string {
    if raw == "" || len(raw) > 2048 {
        return ""
    }
    parsed, err := url.Parse(raw)
    if err != nil || parsed.Scheme != "https" || parsed.Port() != "" || parsed.User != nil || parsed.Fragment != "" {
        return ""
    }
    if provider == "openai-codex" && strings.EqualFold(parsed.Hostname(), "auth.openai.com") {
        return parsed.String()
    }
    if provider == "antigravity" && strings.EqualFold(parsed.Hostname(), "accounts.google.com") && parsed.Path == "/o/oauth2/v2/auth" {
        return parsed.String()
    }
    return ""
}
```

#### C. Managed Process & Recipe Plumbing
1. **Config**: Add `AntigravityExtension string` to `managed.Config`.
2. **CLI**: Add `--managed-antigravity-extension` flag in `router-proxy/cmd/mailbox-router/main.go`. Wire default in `tools/run_router.sh`.
3. **CommandSpec**: Add `AntigravityExtension string` in `router-proxy/internal/managed/process.go`. Append `-e` flag in `Args()` when non-empty.
4. **LaunchRecipe Struct & Digest**:
   - In `router-proxy/internal/store/managed_root_recovery.go`:
     - Add `AntigravityExtension string` with JSON tag `antigravity_extension,omitempty` to `ManagedLaunchRecipe`.
     - `ValidateManagedLaunchRecipe` validates `AntigravityExtension`:
       ```go
       if recipe.AntigravityExtension != "" && !validRecipeValue(recipe.AntigravityExtension, 4096) {
           return errors.New("managed launch recipe contains an invalid value")
       }
       ```
     - Digest computation in `ManagedLaunchRecipeDigest` uses `json.Marshal(recipe)`. By using `omitempty`, an empty `AntigravityExtension` field is omitted from the JSON payload. This maintains the exact byte serialization and SHA256 digest of pre-existing recipes. Non-empty values are serialized and participate in the digest.
5. **Persistence**:
   - `loadManagedLaunchRecipeQuery` and `putManagedLaunchRecipeTx` read and write `antigravity_extension`.
   - Forward `AntigravityExtension` across supervisor restarts and recovery flows: `supervisor.go`, `supervisor_reset_s1.go`, `supervisor_restart_s3.go`, and `root_cold_recovery.go`.

---

## Auth-to-Model Execution Flow

1. User selects auth method in dropdown and completes sign-in.
2. Helper writes credential via `ModelRuntime` under Pi `AuthStorage` lock.
3. Helper emits terminal snapshot containing models for that provider.
4. Router commits models to `managed_provider_models` SQLite table.
5. User selects model in WebUI (e.g. `google-vertex:gemini-2.5-flash` or `antigravity:gemini-3.8-flash`).
6. Router spawns worker with `--provider <provider> --model <model-id>`.
7. For `antigravity`, `-e <antigravity-extension-path>` is included in arguments.
8. Pi reads `auth.json`. For `google-vertex`, `@google/genai` initializes with the absolute ADC path in `GOOGLE_APPLICATION_CREDENTIALS`. For `antigravity`, the extension attaches the stored OAuth access token and refreshes as needed.

---

## Acceptance Criteria

- **AC1 (R1 - API Key)**: Existing Google API key login accepts `GEMINI_API_KEY`, saves `{ type: "api_key", key }`, and exposes Gemini Developer API models.
- **AC2 (R2 - Vertex ADC)**: Vertex AI ADC login succeeds using host Application Default Credentials without prompting for an API key.
- **AC3 (R3 - Antigravity Login)**: Antigravity login executes OAuth PKCE via installed extension, captures redirect on port 51121, supports manual callback URL paste, and stores tokens with user email and project ID.
- **AC4 (R4 - Single Tab & Dropdown)**: Provider settings dialog renders one unified Google section with an authentication method dropdown. Changing selection updates visible status and action buttons.
- **AC5 (R5 - Project ID & Region Prompts)**: Connecting via Vertex AI ADC prompts sequentially for Google Cloud Project ID and Region (defaulting to `us-central1`).
- **AC6 (Isolated ADC Execution)**: Managed Pi child processes running under isolated `ManagedHome` successfully authenticate with Vertex AI ADC using the resolved absolute ADC path.
- **AC7 (Antigravity Runtime Execution)**: A managed agent launched with an Antigravity model receives the `-e <antigravity-extension>` flag under `--no-extensions`, actually streams one mocked response via provider SDK under managed child isolated HOME, and successfully renews an expired fake token. Merely starting without provider error is insufficient.
- **AC8 (Method Independence & Coexistence)**: Connecting or signing out of one Google method does not delete credentials or modify connection state for other configured Google methods. Existing Google API key operations continue to work alongside Vertex ADC and Antigravity.

---

## Concrete Behavioral Integration Test Plan

### 1. Auth-Helper Protocol Integration Tests (`auth-helper/auth-helper.test.mjs`)
- **Child boundary mocking**: Test runner passes a test-only preload module via `NODE_OPTIONS="--import ./test-preload-mock-fetch.mjs"` to intercept `fetch` and `undici` requests for `oauth2.googleapis.com` and `daily-cloudcode-pa.googleapis.com`. Unmocked outbound egress is rejected. No production endpoint overrides.
- **Interactive stdin test helper**: Helper test harness maintains open stdin and matches `prompt.id` to provide correlated multi-turn responses.
- **Vertex ADC flow with synthetic fixture**: Test creates a temporary fixture file `application_default_credentials.json` and sets `GOOGLE_APPLICATION_CREDENTIALS`. Requests `google-vertex` login, responds to Project ID and Region prompts, verifies `auth.json` contains `google-vertex` block with absolute ADC path, and checks that snapshot contains Vertex models.
- **Antigravity OAuth flow**: Mock token exchange and project discovery. Send login request, assert prompt contains valid Google OAuth URL, submit callback URL, verify `auth.json` contains valid OAuth tokens.
- **Cancellation & state mismatch**: Verify helper cancels on abort signal, cleans up port 51121, and returns an error when state mismatch is detected.
- **Expired credential refresh**: Verify expired Antigravity OAuth credential refreshes during execution without manual re-login.
- **Extension absence fallback**: Verify helper disables Antigravity provider registration and returns a clean error if the extension path is missing.
- **API key coexistence & logout regression**: Connect Google API key, connect Vertex ADC, and connect Antigravity concurrently. Assert all credentials exist in `auth.json`. Sign out of `google` (API key); assert `google-vertex` and `antigravity` credentials and rows remain intact. Sign out of `antigravity`; assert `google-vertex` credential remains intact.

### 2. Router Full-Stack Integration Tests (`router-proxy/cmd/mailbox-router/embedded_browser_controls_auth_test.go`)
- **Safe prompt URL validation**: Verify `safeProviderPromptURL` accepts `accounts.google.com/o/oauth2/v2/auth` for `antigravity` and rejects unauthorized URLs or domains.
- **Full auth round-trip**: POST `/v1/provider-auth/google-vertex/login`. Receive prompts, respond via `/v1/provider-auth/operations/{id}/response`. Verify database updates and `/v1/models` returns Vertex models.
- **Migration & FK check**: Apply `086_google_auth_methods.sql` over an existing database populated with models and statuses. Execute `PRAGMA foreign_key_check` on the database connection to verify constraint integrity, and verify existing rows and indexes are preserved.
- **Golden old digest fixture & new path tests**: Verify that a legacy recipe fixture without `antigravity_extension` produces the exact same golden SHA256 digest under `ManagedLaunchRecipeDigest`. Verify that adding a non-empty `antigravity_extension` produces a changed, valid SHA256 digest.
- **Launch recipe & restart validation**: Verify an agent launched with `antigravity` persists `antigravity_extension` in its recipe and cold recovery succeeds.

### 3. WebUI Component Tests (`web/router-ui-react/tests/component/provider-auth.test.tsx`)
- **Single tab layout**: Render dialog with 5 providers. Verify exactly one Google section exists with a method dropdown.
- **Dropdown switching**: Switch dropdown between API Key, Vertex ADC, and Antigravity. Verify status and buttons update to reflect that method's state.
- **Prompt handling**: Trigger Vertex ADC connect. Verify Project ID and Region inputs render and submit values. Trigger Antigravity connect. Verify OAuth link renders.
- **Method coexistence in UI**: Verify signing out of one method does not alter the rendered authenticated status of the other methods in the dropdown.
