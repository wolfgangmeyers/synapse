# Spec: Desktop package payload

- **ID**: `SPEC-DESKTOP-001`
- **Status**: `Active`
- **Domain**: `AgentManagement`
- **Target E2E Suite**: `tests/e2e/desktop-package-payload.spec.ts`

## 1. Intent & User Outcome

The unsigned macOS desktop app remains an HTTP web UI client, not a bundled backend. Packaging includes only the files needed by that client, without development tools, credentials, or previous build outputs.

## 2. Core Invariants

1. Both arm64 and x64 remain unsigned macOS targets. Packaging does not change window behavior, router arguments, or local/remote HTTP(S) URL handling.
2. The payload retains root package metadata and its relative main entry, desktop main/config, the ICNS icon, and the PNG at the path resolved by the packaged config.
3. The existing `ws` runtime dependency and its installed runtime closure remain included. Other root dependencies are not desktop dependencies. Source root metadata and dependencies are never modified; staged metadata declares only desktop dependencies and omits development dependencies.
4. Repository backends (router-proxy, router, auth-helper), unrelated source, development dependencies, secrets, and build outputs are excluded by explicit selection, not a growing denylist.
5. Missing required runtime files, icons, or required dependencies fail staging before Electron Packager can run. Temporary payloads are cleaned up after success or failure by the packaging caller.
6. Before staging, packaging resolves the Electron package installed for the source repository and passes its exact version explicitly to Electron Packager for both architectures. A declared semver range is not a version. Missing or invalid installed Electron metadata fails before staging or invoking Packager. Electron remains excluded from the payload.
7. Verification uses synthetic repositories, deterministic local HTTP, and a real headless browser. It does not download Electron, sign, produce a macOS bundle, call external URLs, generate a UI build, or claim platform validation.

## 3. User Scenarios (Behavioral Contract)

### Scenario: [DESKTOP-001] Build a minimal client payload
- **User Goal**: Package a client without backend code or local secrets.
- **Given**: A repository containing desktop files, icons, ws with runtime dependencies, and unrelated development/backend/secret/output files.
- **When**: The production staging path selects and copies its payload.
- **Then**: Metadata, main/config, icons, and ws runtime closure are present; unrelated root dependencies, dev dependencies, backends, secrets, and outputs are absent. Source metadata is unchanged. Both unsigned architectures remain supported.

### Scenario: [DESKTOP-002] Resolve packaged assets and open a local HTTP UI
- **User Goal**: Keep the existing client entry and icon usable.
- **Given**: A staged payload and an isolated local HTTP UI.
- **When**: The packaged main entry is executed with a test Electron boundary and its requested URL is opened in a headless browser.
- **Then**: The configured local router and default agent reach the HTTP UI, window security options remain enabled, and the icon path resolves to the retained PNG. Remote HTTP(S) router parsing remains supported without contacting a remote service.

### Scenario: [DESKTOP-003] Reject an incomplete payload
- **User Goal**: Avoid silently shipping a broken client.
- **Given**: A source missing a required icon, entry/config, or required runtime dependency.
- **When**: The production staging path runs.
- **Then**: It rejects with the missing path/dependency identified, before packaging. Missing or invalid installed Electron metadata also rejects packaging before staging. The caller removes the temporary directory on failure as on success.

## 4. Automated E2E Verification Matrix

| Scenario ID | Scenario Description | E2E Test Suite | Test Case Name |
| :--- | :--- | :--- | :--- |
| DESKTOP-001 | Minimal payload and unsigned targets | `tests/e2e/desktop-package-payload.spec.ts` | `stages only the desktop runtime payload` |
| DESKTOP-002 | Entry, asset, and local HTTP browser boundary | `tests/e2e/desktop-package-payload.spec.ts` | `packaged entry opens the local HTTP UI with its icon` |
| DESKTOP-003 | Missing runtime inputs | `tests/e2e/desktop-package-payload.spec.ts` | `rejects missing required payload inputs` |
| DESKTOP-001, DESKTOP-003 | Packaging boundary and temporary cleanup | `tests/e2e/desktop-package-payload.spec.ts` | `packaging caller uses the selected payload and cleans up on success and failure` |

Supporting Node tests cover config and the same staging path. Router/WebSocket/SQLite lifecycle is outside this client-only change: no backend is launched or packaged. The Electron test boundary records BrowserWindow calls; native Electron launch and macOS bundle validation remain platform checks, not claims of these tests.

The packaging-boundary test invokes the installed `@electron/packager` metadata inference code against the staged payload with the explicit version. It also verifies that inference without that version rejects, without invoking download or platform packaging code.
