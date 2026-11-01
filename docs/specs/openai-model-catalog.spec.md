# Spec: OpenAI GPT-6.1 Terra Catalog

- **ID**: `SPEC-OPENAI-CATALOG-001`
- **Status**: `Active`
- **Domain**: `AgentManagement`
- **Target E2E Suite**: `tests/e2e/openai-model-catalog.spec.ts`

## 1. Intent & User Outcome

A user with an authenticated ChatGPT/Codex provider can explicitly select `gpt-6.1-terra`, launch an agent, and receive its response. Selection must resolve to an executable model, not a display-only entry. Existing defaults and saved selections stay unchanged.

This contract covers the affected catalog, authentication projection, selection, and existing ChatGPT/Codex embedded execution path. It does not expand API-key `openai` embedded admission or execution, add providers, refresh the whole catalog, change search's hardcoded `gpt-5.5`, or introduce live account-entitlement discovery.

## 2. Core Invariants

1. The exact model ID is `gpt-6.1-terra`. Never substitute Sol, another model, or another provider. `openai` API-key access and `openai-codex` OAuth access remain distinct.
2. Central catalog metadata used for selection and execution describes Terra consistently: context window `1050000`, maximum output tokens `128000`, text/image input, text output, and Responses tool support. Public API costs per million tokens are input `$1`, cached input `$0.05`, output `$5`. These are metadata, not a promise of OAuth billing terms or account entitlement.
3. ChatGPT/Codex selection availability remains gated by usable provider authentication. A public model release or authenticated provider is not proof of per-model account access. The existing catalog projection need not query an account-specific model entitlement service.
4. Existing defaults, saved model/provider selections, and metadata for existing models remain unchanged. Adding Terra alone never selects it automatically.
5. Admission and execution use the same model identity and compatible catalog metadata. A successful catalog listing alone does not prove execution support.
6. Authentication and provider rejection retain existing visible failure behavior. Never silently retry with a different model/provider. Never expose credentials in UI, catalog responses, or diagnostics.
7. No new accumulating store is required. Existing catalog/session retention remains unchanged. Disposable test state and mock histories are bounded to a test and removed at teardown. The disposable provider fixture retains at most 16 captured requests with request bodies limited to 2 MiB each, excludes authentication headers from history, and clears history and removes owned runtime/auth/database resources during teardown, including failure paths.
8. Startup registers SDK models first, then the validated supplemental store, then shipped Terra metadata only if `openai-codex/gpt-6.1-terra` is still missing. The shipped fallback never overwrites an SDK or validated-store definition. Repeated initialization is idempotent: Terra appears once and existing definitions remain unchanged.
9. A clean missing or empty supplemental store still adds missing Terra using shipped metadata. Malformed or unreadable stores import no invalid supplement and retain the original store error unchanged, but do not skip the missing-only shipped fallback. The same startup call must both register missing Terra and preserve that error.

## 3. User Scenarios (Behavioral Contract)

### Scenario: [TERRA-001] Select and execute Terra
- **User Goal**: Run an agent with the newly released model.
- **Given**: ChatGPT/Codex has usable authentication, and its deterministic test provider accepts Terra.
- **When**: The user opens Launch managed agent, explicitly selects `gpt-6.1-terra` under `openai-codex`, supplies valid launch fields, and launches.
- **Then**: The picker lists Terra once. The launch is admitted with that exact provider/ID. The stored agent selection retains that pair. The outbound Responses request uses `gpt-6.1-terra`, and the streamed assistant answer appears through the real WebSocket-backed conversation. Reloading retains the selection and answer.

### Scenario: [TERRA-002] Image input and Responses tool round trip
- **User Goal**: Use Terra's advertised input and tool capabilities.
- **Given**: An authenticated Terra agent and a deterministic Responses provider fixture.
- **When**: The user sends text with an image to an idle agent, and the provider uses the published local attachment reference to request the existing harmless `read` tool before returning its final answer.
- **Then**: The initial request retains the user text and local image reference. The real runtime reads the uploaded image and sends its actual image content in a Responses tool continuation; image bytes need not appear in the first request. The tool result provides evidence of the real harmless read, and the final answer appears in the conversation. A text path alone is not proof of image transport. The resolved execution model has the exact context/output limits, input types, and public cost metadata in invariant 2, rather than inherited metadata from a different model. This catalog patch does not introduce automatic initial idle attachment hydration; input metadata remains text/image.

### Scenario: [TERRA-003] Signed-out provider cannot select Terra
- **User Goal**: Avoid launching a model without the required provider authentication.
- **Given**: ChatGPT/Codex is signed out and no usable Codex credential exists.
- **When**: The user opens the picker, and a stale client separately submits a launch with `openai-codex/gpt-6.1-terra`.
- **Then**: Terra is not offered as an available Codex selection. The stale launch is rejected by the existing authentication/model-availability contract. No Terra agent is admitted and no provider request occurs. Another provider's credentials do not satisfy Codex authentication.

### Scenario: [TERRA-004] Existing models and defaults remain unchanged
- **User Goal**: Continue using existing configuration without opting into Terra.
- **Given**: An existing explicit model selection and the router's existing default model configuration.
- **When**: The catalog includes Terra, the user reloads, then launches with the existing explicit selection and separately with Use router default.
- **Then**: Saved provider/ID and existing model metadata are unchanged. The default configuration is unchanged. Each launch uses its original expected model, not Terra. Both executions still complete through the existing transport.

### Scenario: [TERRA-005] Provider denies Terra access
- **User Goal**: Understand that provider authentication does not guarantee model access.
- **Given**: ChatGPT/Codex is authenticated, Terra is selectable, and the deterministic provider returns a model-access rejection for Terra.
- **When**: The user launches Terra and execution reaches the provider.
- **Then**: The normal execution failure is visible in agent/conversation state. There is no fabricated success, model substitution, or cross-provider retry. The stored selected provider/ID remains Terra, and credentials are absent from public output.

## 4. Automated E2E Verification Matrix

The implemented suite maps the five scenarios below and covers bounded fixture retention and cleanup in `fixture retention and close remove owned resources`.

| Scenario ID | Scenario Description | E2E Test Suite | Test Case Name |
| :--- | :--- | :--- | :--- |
| `TERRA-001` | Selection, execution, persistence | `tests/e2e/openai-model-catalog.spec.ts` | `selects Terra and persists a streamed answer` |
| `TERRA-002` | Metadata, image, tools | `tests/e2e/openai-model-catalog.spec.ts` | `executes Terra image and Responses tool round trip with exact metadata` |
| `TERRA-003` | Authentication gating | `tests/e2e/openai-model-catalog.spec.ts` | `hides signed-out Terra and rejects stale launch` |
| `TERRA-004` | Compatibility | `tests/e2e/openai-model-catalog.spec.ts` | `preserves existing selections metadata and defaults` |
| `TERRA-005` | Account access rejection | `tests/e2e/openai-model-catalog.spec.ts` | `surfaces Terra access rejection without substitution` |

### Boundary and fixture requirements

Use headless Chromium, the built React UI, real router REST/WebSocket handlers, transparent router-proxy, isolated SQLite, and the real embedded runtime/catalog resolution. Exercise at least the successful selection through a direct cross-origin browser connection as well as proxy transport. Do not replace catalog or launch responses with Playwright route stubs.

Seed only disposable local authentication, not personal credentials. Replace issuer/model HTTP calls at the external boundary with deterministic local Responses SSE fixtures. Capture outbound requests, including the tool-result request. For TERRA-002, derive the `read` path from the actual uploaded attachment reference in the initial request, execute the real runtime tool, and verify that the Responses continuation carries the uploaded image bytes as image content plus the matching tool-call result. Decode and validate the image payload against the upload; a text path, fabricated image content, or mocked tool result is not proof. Read persisted selections through router state and, where needed, isolated SQLite. Observe browser rendering and WebSocket updates, not just HTTP success.

The `tests/e2e/harness.ts` embedded mode supports real embedded launches with deterministic local Responses SSE fixtures. The build-tagged `embedded_restart_test_only` transport in `router-proxy/cmd/mailbox-router/embedded_restart_fake_enabled.go` provides a fail-closed local boundary seam. Its resolver accepts `openai-codex/gpt-6-sol` and `openai-codex/gpt-6.1-terra`, copying their registered catalog definitions rather than fabricating a Terra definition. The image/tool test verifies read-on-demand image transport, not automatic initial image hydration. Supporting Go catalog/startup tests verify exact resolved metadata and fallback behavior. These checks supplement, not replace, the browser lifecycle tests.
