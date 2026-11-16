# Spec: Dynamic Model Providers & Custom Endpoints

- **ID**: `SPEC-SETTINGS-DYNAMIC-PROVIDERS-001`
- **Status**: `Draft`
- **Domain**: `Settings`
- **Target E2E Suite**: `tests/e2e/dynamic-model-providers.spec.ts`

---

## Section 1: Intent & User Outcome

Wolfgang wants to connect local or self-hosted LLM endpoints (such as Ollama, vLLM, LM Studio, or OpenAI-compatible gateways) and third-party custom OpenAI providers directly to Synapse without modifying router code or restarting the daemon.

Users can open Provider Settings, view built-in providers alongside user-configured custom providers, add new custom providers via an OpenAI-compatible API base URL and optional API key, auto-discover or manually enter model IDs, edit or delete existing custom providers, and select custom models as the router default or per-agent launch model in ModelPicker.

---

## Section 2: Core Invariants

1. **OpenAI-Compatible Baseline**: Custom providers use the standard OpenAI completions streaming protocol (`api: "openai-completions"` via Pi AI SDK).
2. **Secret Separation & Security**: API keys are stored encrypted or securely within the router SQLite store with single-user workstation permissions (`0700` leaf directory, non-group/world writable). API keys are never exposed in plaintext over GET responses (masked as `sk-...` or boolean `has_api_key: true`).
3. **No Code Hardcoding of Remote Hostnames or Endpoints**: Hostnames and ports are configured dynamically via user input.
4. **Clean List vs. Form Separation in Dialog**: In accordance with `docs/web-ui-design-system.md`, the Provider Settings dialog separates list viewing of configured providers from the dedicated Add/Edit form view. Inline forms cluttering the list view are forbidden.
5. **Cascading Model Availability**: Deleting or disabling a custom provider immediately removes its models from the active model catalog and prevents new agent launches from selecting those models. Existing agents with running turns fail gracefully with a typed model error if the endpoint is unreachable.
6. **Graceful Fallback on Discovery Failure**: If model discovery against `GET /v1/models` on the custom endpoint times out or fails (e.g. offline Ollama instance), the user is presented with a non-blocking error notice and allowed to enter model IDs manually.
7. **Single-User Data Retention**: Custom provider records persist across router restarts in SQLite. No unbounded audit logs or telemetry are accumulated.

---

## Section 3: User Scenarios (Behavioral Contract)

### Scenario: [DMP-001] User views configured providers list
- **User Goal**: Inspect built-in and existing custom providers.
- **Given**:
  - The Web UI is open and connected to the active router.
- **When**:
  - User opens "Managed actions" menu and clicks "Provider settings".
- **Then**:
  - The Provider Settings dialog opens in **List View**.
  - Built-in providers (ChatGPT, Google, Cursor, Google Vertex AI) are displayed.
  - Any previously configured custom providers are listed with their name, base URL, and active models count.
  - An "+ Add Custom Provider" action button is visible in the dialog header/toolbar.

### Scenario: [DMP-002] User adds a new custom Ollama/OpenAI provider with auto-discovery
- **User Goal**: Register a local Ollama instance running at `http://127.0.0.1:11434/v1`.
- **Given**:
  - The Provider Settings dialog is in List View.
  - A local or mock OpenAI-compatible server is running and responds to `GET /v1/models` with models `["qwen2.5-coder:7b", "llama3.2:latest"]`.
- **When**:
  - User clicks "+ Add Custom Provider".
  - Dialog transitions to **Form View**.
  - User enters Name: "Local Ollama", Base URL: "http://127.0.0.1:11434/v1", leaves API Key blank.
  - User clicks "Discover Models" (or submits form).
- **Then**:
  - The router calls the endpoint's `/v1/models` and populates the discovered model list.
  - User saves the provider.
  - The dialog transitions back to **List View**, showing "Local Ollama" with 2 models available.
  - The models are now listed in ModelPicker as `custom-local-ollama:qwen2.5-coder:7b`.

### Scenario: [DMP-003] User adds custom provider with manual model entry when discovery fails
- **User Goal**: Add a remote endpoint whose `/v1/models` endpoint is protected or unlisted.
- **Given**:
  - The Provider Settings dialog is in Form View.
- **When**:
  - User enters Name: "Private vLLM", Base URL: "http://10.0.0.50:8000/v1", API Key: "secret-token".
  - Discovery fails or user manually adds model ID "deepseek-coder-v2" with display name "DeepSeek Coder V2".
  - User clicks "Save".
- **Then**:
  - The provider is saved with the manually specified model.
  - Dialog transitions back to List View with "Private vLLM" active.

### Scenario: [DMP-004] User selects a custom model as Default Model
- **User Goal**: Set a custom provider model as the global default for new agents.
- **Given**:
  - Custom provider "Local Ollama" with model `qwen2.5-coder:7b` is registered.
  - Provider Settings dialog is open.
- **When**:
  - User opens Default Model picker and selects `custom-local-ollama:qwen2.5-coder:7b`.
- **Then**:
  - Status feedback displays "New sessions will use qwen2.5-coder:7b."
  - `GET /v1/models` returns `default_model: { provider: "custom-local-ollama", id: "qwen2.5-coder:7b" }`.
  - The selection persists across page reload.

### Scenario: [DMP-005] User launches agent with custom model and executes prompt
- **User Goal**: Run an embedded agent session powered by the custom provider.
- **Given**:
  - Agent is configured with model `custom-local-ollama:qwen2.5-coder:7b`.
  - The mock endpoint receives OpenAI-compatible streaming completion requests.
- **When**:
  - User sends message "Hello custom LLM".
- **Then**:
  - The router streams the request via `ai.StreamSimple` using `APIOpenAICompletions` to the custom Base URL.
  - Assistant message streams into the timeline feed.

### Scenario: [DMP-006] User edits or deletes a custom provider
- **User Goal**: Remove or update an existing custom provider.
- **Given**:
  - Provider "Local Ollama" exists in Provider Settings.
- **When**:
  - User clicks the delete button (`.icon-btn-round`) on the provider row.
  - User confirms deletion.
- **Then**:
  - The provider is deleted from the store.
  - Associated models are purged from the catalog.
  - ModelPicker no longer offers those models.

---

## Section 4: Automated E2E Verification Matrix

| Scenario ID | Scenario Description | E2E Test Suite | Test Case Name |
| :--- | :--- | :--- | :--- |
| `DMP-001` | List configured providers | `tests/e2e/dynamic-model-providers.spec.ts` | `displays built-in and configured custom providers in list view` |
| `DMP-002` | Add custom provider with model discovery | `tests/e2e/dynamic-model-providers.spec.ts` | `adds custom provider and discovers models from mock endpoint` |
| `DMP-003` | Add custom provider with manual model entry | `tests/e2e/dynamic-model-providers.spec.ts` | `adds custom provider with manual models on discovery failure` |
| `DMP-004` | Set custom model as router default | `tests/e2e/dynamic-model-providers.spec.ts` | `saves custom model as default and preserves on reload` |
| `DMP-005` | Agent turn execution via custom provider | `tests/e2e/dynamic-model-providers.spec.ts` | `streams agent turn from mock OpenAI compatible custom endpoint` |
| `DMP-006` | Delete custom provider | `tests/e2e/dynamic-model-providers.spec.ts` | `deletes custom provider and cascades model removal from picker` |
