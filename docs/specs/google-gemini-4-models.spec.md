# Behavioral Specification: Google and Vertex AI Gemini 4 Model Support

**Status**: Accepted
**Scope**: Model Catalog (`auth-helper`), Embedded Managed Execution (`router-proxy`), Parameter Normalization, and Diagnostics

---

## 1. Problem Statement

Google has introduced Gemini 4 generation models, including `gemini-4-argon`, `gemini-4-carbon`, and `gemini-4-hydrogen`. Users operating with Google API Key and Google Cloud Vertex AI ADC providers require first-class support for these models in both catalog discovery and managed embedded execution.

Previous behavior had several limitations:
1. `auth-helper/providers.mjs` only injected `gemini-4-argon`, leaving out `gemini-4-carbon` and `gemini-4-hydrogen`.
2. Vertex AI model routing required global endpoints for preview and Gemini 4 models, but `vertexModelRequiresGlobal` only checked `gemini-4` and `argon` substrings.
3. When reasoning is disabled or minimal (`opts.Reasoning == "off"`), Gemini 4 models reject `thinkingBudget: 0` and `thinkingLevel: "MINIMAL"`, requiring `thinkingLevel: "LOW"`.
4. Standard agent tools (such as `read`) declare parameter types with JSON schema unions (e.g. `type: ["integer", "null"]`). Vertex AI protobuf validation strictly rejects array-valued `type` fields in tool parameters (`Proto field is not repeating, cannot start list`), causing turn requests to fail with HTTP 400.
5. Unbounded error messages from provider APIs could violate the SQLite CHECK constraint on `managed_complete_history.message` (`length BETWEEN 1 AND 512`), resulting in unhandled transaction aborts.

---

## 2. User Scenarios & Expected Behavior

### Scenario 1: Model Catalog Discovery
- **Given** an authenticated Google developer API key or Vertex AI ADC environment
- **When** the model catalog is fetched from the router via `/v1/models` or provider catalog reflection
- **Then** `gemini-4-argon`, `gemini-4-carbon`, and `gemini-4-hydrogen` are present for both `google` and `google-vertex` providers with:
  - Context window: 1,048,576 tokens
  - Maximum output: 65,536 tokens
  - Reasoning enabled: `true` with level map (`low`, `medium`, `high`)
  - Input modalities: `["text", "image"]`

### Scenario 2: Global Endpoint Routing on Vertex AI
- **Given** a managed worker configured with `google-vertex/gemini-4-carbon` or `google-vertex/gemini-4-hydrogen`
- **When** an embedded streaming request is initiated
- **Then** the Vertex AI host defaults to the global prediction endpoint (`aiplatform.googleapis.com` or location-adjusted global endpoint) rather than a regional endpoint that does not host Gemini 4 models.

### Scenario 3: Thinking Level Configuration with Minimal or Disabled Reasoning
- **Given** a request directed to any `gemini-4-*` model with reasoning set to `off`
- **When** the payload generation config is constructed
- **Then** `thinkingConfig` is set to `{"thinkingLevel": "LOW"}` without `includeThoughts`, avoiding unsupported `thinkingBudget: 0` or `THINKING_LEVEL_MINIMAL` provider errors.

### Scenario 4: Parameter Union Type Normalization for Gemini Tools
- **Given** an embedded agent with tools declaring nullable fields via type unions (e.g. `type: ["integer", "null"]`)
- **When** `sanitizeGeminiParameters` processes tool declarations before calling Google or Vertex AI
- **Then** the parameter schema is flattened to `"type": "integer"` and `"nullable": true`, satisfying the non-repeating protobuf schema contract.

### Scenario 5: Bounded Error History Recording
- **Given** an upstream API error with an extended JSON message body exceeding 512 bytes
- **When** the operational event is recorded to `managed_complete_history`
- **Then** the message text is safely truncated with ellipsis to 512 bytes so the transaction succeeds and the full error remains accessible in details/logs.

---

## 3. Verification & Acceptance Criteria

- [x] Unit tests verify `auth-helper` registers `gemini-4-argon`, `gemini-4-carbon`, and `gemini-4-hydrogen` for both `google` and `google-vertex`.
- [x] Unit tests verify `vertexModelRequiresGlobal` correctly identifies all Gemini 4 model identifiers.
- [x] Unit tests verify thinking level configuration emits `"thinkingLevel": "LOW"` for Gemini 4 models when reasoning is off.
- [x] Unit tests verify `sanitizeGeminiParameters` converts `["<type>", "null"]` to singular type with `nullable: true`.
- [x] Unit tests verify `managed_complete_history` bounds messages > 512 bytes.
- [x] Live end-to-end multi-turn verification on an isolated router with Vertex AI `gemini-4-hydrogen`.
