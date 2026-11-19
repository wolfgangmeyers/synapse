# Behavioral Specification: Google and Vertex AI Gemini 4 and Gemini 3.8 Model Support

**Status**: Accepted
**Scope**: Model Catalog (`auth-helper`), Embedded Managed Execution (`router-proxy`), Parameter Normalization, Thought Signature Handling, and Diagnostics

---

## 1. Problem Statement

Google has introduced Gemini 4 generation models (`gemini-4-argon`, `gemini-4-carbon`, `gemini-4-hydrogen`) as well as Gemini 3.8 models (`gemini-3.8-flash`). Users operating with Google API Key and Google Cloud Vertex AI ADC providers require first-class support for these models in both catalog discovery and managed embedded execution.

Previous behavior had several limitations:
1. `auth-helper/providers.mjs` only injected `gemini-4-argon`, leaving out `gemini-4-carbon` and `gemini-4-hydrogen`.
2. Vertex AI model routing required global endpoints for preview and Gemini 4 models, but `vertexModelRequiresGlobal` only checked `gemini-4` and `argon` substrings.
3. When reasoning is disabled or minimal (`opts.Reasoning == "off"`), Gemini 4 models reject `thinkingBudget: 0` and `thinkingLevel: "MINIMAL"`, requiring `thinkingLevel: "LOW"`. Gemini 3.8 models continue using `thinkingLevel: "MINIMAL"`.
4. Standard agent tools (such as `read`) declare parameter types with JSON schema unions (e.g. `type: ["integer", "null"]`). Vertex AI protobuf validation strictly rejects array-valued `type` fields in tool parameters (`Proto field is not repeating, cannot start list`), causing turn requests to fail with HTTP 400.
5. Unbounded error messages from provider APIs could violate the SQLite CHECK constraint on `managed_complete_history.message` (`length BETWEEN 1 AND 512`), resulting in unhandled transaction aborts.
6. Vertex AI strictly validates thought signatures on Gemini 3.8 Flash (`gemini-3.8-flash`) as well as Gemini 4 models. When dummy signatures (`"skip_thought_signature_validator"`) are injected into tool call history or backfilled function calls, Vertex AI rejects the request or emits empty continuation turns (`finishReason: "STOP"`, `parts: []`), causing agent stalls. Gemini 3.8 models require strict thought signature handling: authentic thought signatures must be replayed, while dummy thought signatures must be omitted completely.

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

### Scenario 6: Authentic Thought Signature Replay in Multi-Turn Tool Interaction
- **Given** a managed agent interacting with a Gemini 4 model (`gemini-4-hydrogen`, `gemini-4-argon`, `gemini-4-carbon`) or Gemini 3.8 model (`gemini-3.8-flash`).
- **When** the upstream Google/Vertex SSE stream returns tool calls accompanied by part-level `thoughtSignature` (or `thought_signature`).
- **Then** `scanGeminiSSEStream` populates `ai.ToolCall.ThoughtSignature`.
- **When** subsequent turns replay historical messages via `convertMessagesToGoogleContents`.
- **Then** the serialized model turn includes the authentic `thoughtSignature` in the function call part.

### Scenario 7: Omission of Dummy Signatures for Strict Thought Signature Models (Gemini 4 and Gemini 3.8) on Interrupted and Backfilled Tool Calls
- **Given** a transcript being converted for a strict thought signature model: Gemini 4 (`gemini-4-hydrogen`, `gemini-4-argon`, `gemini-4-carbon`) or Gemini 3.8 (`gemini-3.8-flash`).
- **When** historical tool calls lack an authentic thought signature (empty or undefined), or when missing function calls are synthesized/backfilled for orphaned or extra tool results.
- **Then** `convertMessagesToGoogleContents` omits `thought_signature` and `thoughtSignature` entirely (both properties undefined/absent on the serialized function call part) rather than injecting `"skip_thought_signature_validator"`.
- **And** over the wire in HTTP payloads to Google and Vertex AI endpoints, no dummy signature strings appear in function call objects, avoiding empty continuation turns and agent stalls on Gemini 3.8 Flash.
- **And** for legacy non-strict models (e.g. `gemini-2.5-flash`), `"skip_thought_signature_validator"` is retained for backward compatibility.

---

## 3. Verification & Acceptance Criteria

- [x] Unit tests verify `auth-helper` registers `gemini-4-argon`, `gemini-4-carbon`, and `gemini-4-hydrogen` for both `google` and `google-vertex`.
- [x] Unit tests verify `vertexModelRequiresGlobal` correctly identifies all Gemini 4 model identifiers.
- [x] Unit tests verify thinking level configuration emits `"thinkingLevel": "LOW"` for Gemini 4 models when reasoning is off, while preserving `"thinkingLevel": "MINIMAL"` for Gemini 3.8 models.
- [x] Unit tests verify `sanitizeGeminiParameters` converts `["<type>", "null"]` to singular type with `nullable: true`.
- [x] Unit tests verify `managed_complete_history` bounds messages > 512 bytes.
- [x] Unit tests verify `scanGeminiSSEStream` captures part-level `thoughtSignature` / `thought_signature` into `ai.ToolCall.ThoughtSignature`.
- [x] Unit tests verify `convertMessagesToGoogleContents` preserves authentic thought signatures for both Gemini 4 and Gemini 3.8 models and omits dummy signatures on missing/backfilled/interrupted calls.
- [x] Unit tests verify backward compatibility for legacy models (e.g. `gemini-2.5-flash`) preserving `"skip_thought_signature_validator"`.
- [x] E2E Playwright tests (`tests/e2e/gemini-4-multi-turn.spec.ts`) verify multi-turn chaining across multiple tool calls with distinct signatures and confirm over-the-wire omission of dummy signatures for Gemini 4 and Gemini 3.8 Flash on tool calls without authentic signatures.
- [x] Live end-to-end multi-turn verification on an isolated router with Vertex AI `gemini-4-hydrogen`.
