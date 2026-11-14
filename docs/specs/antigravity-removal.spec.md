# Spec: Supported providers after Antigravity removal

- **ID**: `SPEC-PROVIDERS-ANTIGRAVITY-REMOVAL-001`
- **Status**: `Draft`
- **Domain**: `Settings`
- **Target E2E Suite**: `tests/e2e/antigravity-removal.spec.ts`

## 1. Intent & User Outcome

Remove Antigravity from the router and web UI. Google Gemini API-key access, Google Vertex AI, and Codex continue working. Existing local credential files are not deleted or externally revoked.

## 2. Core Invariants

1. Antigravity cannot authenticate, refresh credentials, appear in current provider/model catalogs, launch a managed agent or subagent, or execute search grounding.
2. No production route uses internal Cloud Code endpoints, Antigravity OAuth client credentials, extension loading, or Antigravity model aliases.
3. `google` means legitimate Gemini API-key access, not an alias for Antigravity. `google-vertex` retains ADC access. `openai-codex` retains router-owned credentials and routing.
4. Gemini message conversion, tool declarations/results, images, thinking signatures, transcript pruning, usage accounting, and streaming remain available to supported Google routes.
5. Search grounding uses Gemini API-key credentials and Vertex ADC only, retaining the supported fallback order (API key before Vertex).
6. Omitted child selections preserve existing supported parent provider/model inheritance. Only the actual missing/default Antigravity fallback becomes `google-vertex/gemini-3.8-flash`. Omission alone must not override a supported inherited Google or Codex selection. No unrelated inheritance behavior changes.
7. Stale persisted Antigravity selections are unavailable; they never silently switch providers. Historical records and migration history may retain inert names, but cannot reactivate provider support.
8. No local credential deletion, external revocation, deployment, restart, Android/app change, or unrelated cleanup is part of this change.
9. Automated verification uses local deterministic provider mocks, never live LLMs or external services. No accumulating runtime data is introduced.
10. Provider snapshot refresh requires exactly four unique supported providers: `google`, `google-vertex`, `openai-codex`, and `cursor`. Refresh of a legacy five-provider SQLite snapshot removes stale Antigravity status and model rows while preserving the four supported providers and their refreshed catalogs. Status, models, and auth revision change atomically. Invalid/incomplete refreshes leave the previous snapshot and revision unchanged.

## 3. User Scenarios (Behavioral Contract)

### Scenario: [AGR-001] Supported provider settings
- **User Goal**: Configure supported providers without obsolete choices.
- **Given**: A real router, proxy, SQLite store, and web UI are running with deterministic local auth fixtures.
- **When**: The user opens provider settings and the model picker.
- **Then**: Gemini API-key, Vertex AI, and Codex remain available. Antigravity does not appear as an auth method, provider, or model group.

### Scenario: [AGR-002] Reject obsolete auth and model requests
- **User Goal**: Get an explicit failure for an obsolete configuration.
- **Given**: An Antigravity credential record and stale selection exist in isolated test state.
- **When**: A client requests Antigravity login/logout, selects an Antigravity model, or attempts managed execution with that selection.
- **Then**: The request is rejected through the existing unsupported/unavailable error contract. No refresh, extension load, provider request, credential mutation, or cross-provider fallback occurs. Supported catalogs omit stale Antigravity entries.

### Scenario: [AGR-003] Supported managed routing
- **User Goal**: Continue using Gemini API-key, Vertex AI, and Codex agents.
- **Given**: Each supported provider is configured against a local deterministic mock.
- **When**: A managed agent executes a turn including a tool call and tool result.
- **Then**: The intended provider receives its expected auth and payload, the agent finishes successfully, and the response reaches the browser through router notifications. Google routes retain shared Gemini conversion. Codex retains its own credential boundary.

### Scenario: [AGR-004] Supported subagent routing and default
- **User Goal**: Run child agents on supported providers.
- **Given**: Managed parents with supported Google API-key, Vertex, and Codex selections, a separate fixture with a genuinely missing selection, and local provider mocks.
- **When**: Children are requested with selections omitted under each supported parent, with no effective selection after existing inheritance, under a stale Antigravity parent, and with explicit model arguments (supported or removed).
- **Then**: The public subagent tool accepts no model argument; explicit model arguments are rejected before child creation, including supported models. Omitted selections inherit each supported parent provider/model unchanged. Only the genuinely missing/default fallback uses Vertex Flash. A stale Antigravity parent is unavailable before child execution. Persisted child selections and requests received by local mocks agree. No internal Cloud Code endpoint is contacted.

### Scenario: [AGR-005] Search grounding without Antigravity
- **User Goal**: Search with supported Google credentials.
- **Given**: Local deterministic Gemini and Vertex grounding mocks.
- **When**: Search executes with an API key, with only ADC, after a Gemini failure with valid ADC, or without either credential.
- **Then**: API-key and Vertex paths succeed as applicable, API-key failure falls back to Vertex, and absent credentials return a clear error identifying supported credentials. Antigravity credentials alone never enable search or trigger a network request.

### Scenario: [AGR-006] Legacy launch recipes cannot load removed extensions
- **User Goal**: Recover supported agents without loading obsolete provider code.
- **Given**: Stored launch metadata contains a historical Antigravity extension field.
- **When**: A supported agent is resumed or reset.
- **Then**: No Antigravity extension or related environment setting is forwarded. Supported Google, Vertex, Codex, and Cursor routing is preserved. Recovery validates the original persisted recipe hash before comparing effective recipes with only the retired extension field removed. Other launch configuration differences still fail recovery under the default strict policy; corrupt historical hashes are rejected. Historical schema compatibility does not expose a supported Antigravity setting.

### Scenario: [AGR-007] Refresh a legacy provider snapshot
- **User Goal**: Refresh supported provider settings without stale Antigravity entries or losing supported providers.
- **Given**: Isolated SQLite contains a legacy five-provider snapshot, including Antigravity status and models, and the real router/helper uses deterministic local provider fixtures.
- **When**: The user refreshes provider settings through the browser and the helper returns the four supported providers.
- **Then**: Refresh succeeds. SQLite and browser/API catalogs contain exactly Google API-key, Vertex, Codex, and Cursor provider statuses with their expected refreshed models and no Antigravity status/models. The auth revision advances atomically with the refreshed snapshot. A repeated refresh does not restore Antigravity.
- **Error Path**: An incomplete, duplicate, or obsolete-provider snapshot is rejected. Failed or canceled refresh leaves status, models, and auth revision unchanged.

## 4. Automated E2E Verification Matrix

Browser evidence and supporting Go-boundary evidence are separate below. A recorded six-test browser run passed 6/6; supporting Go and SQLite checks do not establish full browser acceptance. Broad Go and app suites were not claimed green. Missing/stale parent fixtures inject persisted state after ordinary admission, before the real subagent tool runs; they prove inheritance/execution handling, not public admission or legacy recovery.

| Scenario ID | Verified boundary | Actual test title / Go test |
| :--- | :--- | :--- |
| AGR-001 | Browser settings/auth methods and searchable launch chooser | `AGR-001 offers only supported provider methods and models in actual settings and chooser` |
| AGR-002 | Browser-origin REST rejection and unchanged credential fixture | `refreshes a legacy snapshot to four providers and rejects obsolete selections` |
| AGR-003 | Browser + WS + proxy/router + real tools + SQLite + local provider wires | `AGR-003 routes Gemini Vertex and Codex full tool turns to browser WS results` |
| AGR-004 | Actual child execution under each supported parent | `AGR-004 inherits Google Vertex and Codex parent selections through actual child execution` |
| AGR-004 | Missing inheritance fallback and stale parent rejection | `AGR-004 uses Vertex Flash only for a genuinely missing inherited selection and rejects a stale removed parent` |
| AGR-004 | Public tool rejects all explicit model arguments before child creation | `AGR-004 rejects explicit child model arguments including removed provider before execution` |
| AGR-005 | Supporting Go HTTP grounding boundary; **not Playwright** | `TestAntigravityRemovalGroundingAPIKeyThenVertex`; `TestEmbeddedGoogleSearch_VertexIntegration`; `TestEmbeddedGoogleSearch_ConfiguredVertexGlobalGrounding`; `TestEmbeddedGoogleSearch_ConfiguredVertexRegionalLocationUsesModelEndpoint`; `TestEmbeddedGoogleSearch_ToolGating`; `TestEmbeddedGoogleSearch_ToolSchema` in `router-proxy/internal/managed/embedded_google_search_test.go` |
| AGR-006 | Supporting Go supervisor/SQLite recovery with fake process/RPC boundary; **not browser or real process recovery** | `TestColdRecoveryLegacyAntigravityRecipe` in `router-proxy/internal/managed/root_cold_recovery_antigravity_test.go` (12 provider/variant subcases) |
| AGR-007 | Browser-origin legacy refresh, stale-row cleanup and revision advance | `refreshes a legacy snapshot to four providers and rejects obsolete selections` |
| AGR-007 | Supporting Go SQLite transaction boundary for invalid/incomplete/duplicate snapshots, cancellation, forced rollback, exact refreshed models and repeat refresh; **not Playwright** | `TestAntigravityRemovalLegacySnapshotAtomicRefresh` in `router-proxy/internal/store/antigravity_removal_test.go` |

All six browser titles above belong to `tests/e2e/antigravity-removal.spec.ts`. The six-test suite does not claim browser coverage for AGR-005/006 or AGR-007 error/repeat-refresh paths. AGR-002 browser coverage rejects auth/default selection; stale execution rejection is exercised by the AGR-004 parent fixture, not direct obsolete root admission. The specification remains Draft: supporting Go tests are not substitutes for missing browser scenarios. Completion of the scoped fixture suite does not mean every invariant has full browser lifecycle coverage.
