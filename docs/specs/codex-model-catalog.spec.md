# Spec: Codex Model Catalog and Embedded Launch Parity

- **ID**: `SPEC-LIFECYCLE-CODEX-CATALOG-001`
- **Status**: `Active`
- **Domain**: `AgentManagement`
- **Target E2E Suite**: `tests/e2e/codex-model-catalog.spec.ts`

## 1. Intent & User Outcome

A user signed in to ChatGPT on a freshly initialized router can choose `openai-codex/gpt-6.1-sol` from the models catalog and successfully launch an embedded agent using that exact model, even when the optional Pi models-store file has never existed. Signed-out users are not promised Codex access. The UI and API never advertise an unusable Codex model as available.

## 2. Core Invariants

1. An available Codex catalog entry MUST have a complete, validated model definition resolvable by embedded admission and execution. A display name/ID alone is not sufficient.
2. Authenticated catalog discovery, model selection, and embedded launch MUST agree on the same provider and ID. Launch MUST NOT fall back to another provider or model when an explicit selection fails.
3. Only the owned local Codex credential authorizes Codex availability and embedded launch. Signed-out or unusable credentials MUST NOT cause a Codex model to be offered as available or admitted; existing expiry refresh and expired-but-refreshable behavior MUST remain intact.
4. Built-in SDK model definitions MUST remain authoritative and unchanged when local model entries are loaded. Optional models-store absence MUST NOT make `gpt-6.1-sol` unavailable on a supported build.
5. Tests MUST NOT contact real model services or use real credentials; deterministic local mocks replace external inference at the transport boundary.

## 3. User Scenarios (Behavioral Contract)

### Scenario: [CAT-001] Fresh-machine authenticated discovery and embedded launch
- **User Goal**: Launch the model displayed in the catalog without creating a Pi models-store file.
- **Given**: An isolated router and SQLite database, no Pi agent `models-store.json`, a valid test-owned Codex login, and a deterministic local mock inference endpoint.
- **When**: A browser requests `GET /v1/models`, selects available `openai-codex/gpt-6.1-sol`, and launches an embedded agent with that explicit selection.
- **Then**: The response contains the exact provider, model ID and name `GPT-6.1 Sol` marked available. Launch does not return `MODEL_UNAVAILABLE`; the stored/visible agent retains this exact provider and ID; its embedded execution reaches the mock endpoint rather than a real model service. No models-store file is required or created solely for discovery.

### Scenario: [CAT-002] Signed-out Codex account cannot launch
- **User Goal**: Avoid a misleading available entry when not signed in.
- **Given**: The same fresh isolated router without an owned usable Codex credential and without a models-store file.
- **When**: A browser requests `GET /v1/models` and tries to launch explicitly with `openai-codex/gpt-6.1-sol`.
- **Then**: The catalog does not mark that model available; the launch is rejected with the existing `MODEL_UNAVAILABLE` response, without creating a running agent or contacting the model transport.

### Scenario: [CAT-003] Unknown or mismatched provider selection does not fall back
- **User Goal**: Never run a different provider or unknown model in place of the requested one.
- **Given**: A fresh isolated router with valid test-owned Codex login and a local mock inference endpoint.
- **When**: A browser requests launch with a nonexistent Codex model ID or a non-Codex provider paired with `gpt-6.1-sol`.
- **Then**: The explicit selection is rejected as unavailable; no embedded agent starts and no provider transport request is made for the rejected model.

### Scenario: [CAT-004] Existing Codex SDK models remain usable
- **User Goal**: Use pre-existing authenticated Codex models after the catalog repair.
- **Given**: A fresh isolated router with valid test-owned Codex login, no models-store file, and a local mock inference endpoint.
- **When**: A browser lists models and launches a built-in Codex model such as `openai-codex/gpt-6-sol`.
- **Then**: The built-in model is still advertised with its original metadata and launches under the same Codex authentication/embedded rules; the new entry does not replace or alter it.

## 4. Automated E2E Verification Matrix

| Scenario ID | Scenario Description | E2E Test Suite | Test Case Name |
| :--- | :--- | :--- | :--- |
| `CAT-001` | Fresh authenticated catalog-to-launch parity | `tests/e2e/codex-model-catalog.spec.ts` | `launches advertised gpt-6.1-sol on a fresh install using local mock inference` |
| `CAT-002` | Signed-out discovery and rejection | `tests/e2e/codex-model-catalog.spec.ts` | `does not advertise or launch Codex model when signed out` |
| `CAT-003` | No provider or model substitution | `tests/e2e/codex-model-catalog.spec.ts` | `rejects unknown and cross-provider explicit selections without transport calls` |
| `CAT-004` | Existing SDK model preservation | `tests/e2e/codex-model-catalog.spec.ts` | `retains built-in Codex model catalog and embedded launch` |

Focused Go tests additionally check full metadata registration, validation and SDK-precedence without credential reads or live network requests. Existing malformed optional models-store startup errors remain unchanged.
