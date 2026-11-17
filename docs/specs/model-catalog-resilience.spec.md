# Spec: Model Catalog Resilience and Localized Error Display

- **ID**: `SPEC-MODEL-CATALOG-RESILIENCE-001`
- **Status**: `Active`
- **Domain**: `Conversation`
- **Target E2E Suite**: `tests/e2e/model-catalog-resilience.spec.ts`

## 1. Intent & User Outcome

Wolfgang can select and view managed agent models without interruption or false-positive global error banners. When the router returns models from diverse, heterogeneous, or newly supported providers (such as `antigravity`, `claude-code`, custom providers, or unknown future engines), the Web UI safely parses and accepts valid models rather than rejecting the entire catalog with "router returned invalid model catalog". Furthermore, if model fetching encounters an error or network issue, the error is contained and displayed subtly within the Model Picker dropdown or contextual controls without rendering an intrusive, full-width red error banner across the active conversation view.

## 2. Core Invariants

1. Unfamiliar or heterogeneous provider names in `/v1/models` MUST NOT cause the frontend client to reject the entire model catalog or throw `invalidResponse("Router returned invalid model catalog")`.
2. Each catalog item with a non-empty `id` and non-empty `provider` string MUST be parsed as a valid model choice; extra, custom, or forward-compatible properties MUST NOT cause rejection.
3. Malformed individual model entries in a catalog array (e.g., missing `id` or missing `provider`) MUST be filtered out gracefully without aborting the parsing of other valid model choices.
4. Local engine and alternative providers including `antigravity` and `claude-code` MUST be recognized and accepted as valid models.
5. Model catalog fetch or parsing failures (`managedHeaderModelError`) MUST NOT be passed as `agentError` to the `<Conversation>` view and MUST NOT render an intrusive error banner below the agent header.
6. Model catalog errors MUST remain localized to the `<ModelPicker>` (e.g. through `emptyMessage`) or relevant contextual dropdown status.

## 3. User Scenarios (Behavioral Contract)

### Scenario: [SCN-MODEL-001] Heterogeneous and unknown model providers are accepted safely
- **User Goal**: View and select models even when the catalog includes models from new, custom, or alternative providers.
- **Given**: A connected managed agent and a router returning `/v1/models` containing models from built-in providers (`openai-codex`, `google`, `google-vertex`), local engines (`claude-code`, `antigravity`), custom providers (`custom-ollama`), or unknown third-party providers (`aws-bedrock`, `openrouter`).
- **When**: The Web UI requests `GET /v1/models` to populate header model options.
- **Then**: The HTTP client successfully parses the catalog without throwing `invalidResponse`.
- **And**: All valid models from recognized and unrecognized providers are retained in the parsed model list.
- **And**: No "router returned invalid model catalog" banner is displayed to the user.

### Scenario: [SCN-MODEL-002] Malformed individual model entries are filtered without dropping the entire catalog
- **User Goal**: Access valid available models even if an individual catalog entry is malformed.
- **Given**: A router returning `/v1/models` with an array containing several valid models and one or more malformed items (such as `null`, an item missing an `id`, or an item missing a `provider`).
- **When**: The Web UI requests `GET /v1/models`.
- **Then**: The HTTP client filters out the malformed entries and successfully yields all valid model entries.
- **And**: The catalog fetch succeeds rather than failing the entire request with an `HttpRequestError`.

### Scenario: [SCN-MODEL-003] Model catalog fetch or parse error does not render an intrusive conversation error banner
- **User Goal**: Read and interact with an agent conversation without intrusive error banners when model options fail to load.
- **Given**: A selected managed agent in the conversation view.
- **When**: The `/v1/models` HTTP request fails (e.g. network timeout, 500 error, or completely invalid JSON payload).
- **Then**: An intrusive `<Conversation agentError={...}>` banner is NOT rendered below the agent header.
- **And**: The error message (e.g., "Models could not be loaded.") is passed only to the header's `<ModelPicker>` via `emptyMessage`, indicating the failure within the model picker control itself.
- **And**: Wolfgang can continue reading history and composing messages without distraction or obstruction.

### Scenario: [SCN-MODEL-004] Antigravity and Claude Code models are recognized in model options
- **User Goal**: Select Antigravity or Claude Code models for managed agents supporting those engines.
- **Given**: A managed agent running with an `antigravity` or `claude-code` model engine.
- **When**: The model catalog returns entries with `provider: "antigravity"` or `provider: "claude-code"`.
- **Then**: The models are accepted by `modelChoice` and `models(value)` schema parsing.
- **And**: The matching models appear in the agent header `<ModelPicker>` options for selection.

## 4. Automated Verification Matrix

| Scenario ID | Scenario Description | Test File | Test Case Name |
| :--- | :--- | :--- | :--- |
| `SCN-MODEL-001` | Unknown and heterogeneous model providers parsed safely | `web/router-ui-react/tests/transport/http-contract.test.ts` | `decodes provider models for supported and unknown providers without rejecting catalog` |
| `SCN-MODEL-002` | Malformed items filtered without rejecting entire catalog | `web/router-ui-react/tests/transport/http-contract.test.ts` | `filters malformed model items from catalog instead of rejecting entire response` |
| `SCN-MODEL-003` | Model catalog error does not render conversation agentError banner | `web/router-ui-react/tests/component/model-catalog-resilience.test.tsx` | `does not render intrusive conversation agentError banner when model catalog fetch fails` |
| `SCN-MODEL-004` | Antigravity and Claude Code models accepted | `web/router-ui-react/tests/transport/http-contract.test.ts` | `accepts antigravity and claude-code models in model catalog` |
