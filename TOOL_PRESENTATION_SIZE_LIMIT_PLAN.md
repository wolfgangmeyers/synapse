# Implementation Plan - Tool Presentation Size Limit Resilience

## 1. Overview & Problem Statement

In Issue #85 ("Tool calls remain stuck as 'Running' in Web UI due to history presentation size limit"), tool calls executed by managed agents can remain permanently stuck in the "Running" state (`active` outcome) in the Web UI conversation timeline.

When a tool call starts, an initial complete history entry is written with `outcome: "active"`, `summary: "Running"`, and `kind: "tool"`. Upon tool completion, `embeddedPersistence.projectMessage` records the terminal outcome (`success` or `failed`) via `db.RecordManagedCompleteHistoryWithDetails`.

However:
1. Long thinking traces or verbose companion text accumulated during the assistant turn are attached to `ManagedHistoryPresentation{Thinking: p.currentThinking, CompanionText: companionText}`.
2. `managedHistoryPresentationJSON` enforces a strict database constraint: `len(encoded) <= 2048`.
3. If `Thinking` and `CompanionText` inflate the serialized presentation JSON beyond 2048 bytes (or fail string safety checks in `validManagedHistoryPresentation`), `RecordManagedCompleteHistoryWithDetails` fails and returns an error.
4. Because the update error is ignored or aborts the completion record update, the tool call entry in `managed_complete_history` remains stuck in its original `"active"` state forever.
5. In addition, `currentThinking` from prior steps or assistant turns wasn't cleanly reset when tool calls began, leading to stale or oversized thinking traces leaking into tool presentation records.

## 2. Existing Behavioral Specification

This implementation plan implements and satisfies:
- **Spec**: `docs/specs/tool-presentation-resilience.spec.md` (`SPEC-TOOL-PRESENTATION-RESILIENCE-001`)
- **Domain**: `AgentManagement`
- **Target E2E Suite**: `tests/e2e/tool-presentation-resilience.spec.ts`

### Invariants:
1. **Thinking State Reset**: Active thinking state is cleared as soon as a tool call begins or tool execution starts.
2. **Bounded Presentation Payload**: Presentation JSON stored in `presentation_json` MUST stay strictly within limits (maximum 2048 bytes). `Thinking` and `CompanionText` must be bounded/sanitized so they cannot trigger serialization or size rejection.
3. **Graceful Fallback**: If recording history completion with full presentation metadata fails (due to validation, sizing, or serialization), the system must gracefully fall back to recording with a safe minimal presentation (`kind: "tool"`, `tool_name: toolName`, `outcome: "success"|"failed"`), ensuring the tool call transitions out of `active`.
4. **Terminal State Guarantee**: Completed tool invocations in Complete history MUST ALWAYS reach a terminal outcome (`success`, `failed`, or `interrupted`).

## 3. Root Cause Analysis

1. **Stale/Accumulated Thinking in Tool Call Presentation**:
   - `p.currentThinking` was maintained across streaming steps. When assistant content produced tool calls, `p.currentThinking` was retained and copied into `ManagedHistoryPresentation.Thinking` on tool completion in `embedded_session.go:443, 459`.
   - Before tool execution, `currentThinking` was not reset when `toolCallReceived` was flagged or inside `BeforeToolExecute` hooks in `embedded_subagent_runner.go` and `embedded_tool_authority.go`.
2. **Unbounded Presentation Payload in `ManagedHistoryPresentation`**:
   - `CompanionText` and `Thinking` are arbitrary model-generated strings that can easily reach tens of kilobytes.
   - `validManagedHistoryPresentation` checked `companionText` up to 65536 bytes, but `managedHistoryPresentationJSON` strictly rejects anything where `len(encoded) > 2048`.
   - Without proactive truncation/bounding before serialization, any large companion text or thinking string causes `managedHistoryPresentationJSON` to return `errors.New("managed history presentation is invalid")`.
3. **No Fallback on Record Failure**:
   - When `RecordManagedCompleteHistoryWithDetails` failed in `embedded_session.go:472`, the error was discarded without updating the record to a terminal state.
   - In `Store.RecordManagedCompleteHistoryWithFullDetails`, if `managedHistoryPresentationJSON(presentation)` returns an error, the function immediately returns an error rather than falling back to a sanitized or default presentation if the original was non-nil but invalid.

## 4. Proposed Implementation Steps

### Step 1: Thinking State Reset on Tool Call Start
- In `router-proxy/internal/managed/embedded_session.go`:
  - When tool calls are received in `projectMessage` (`p.toolCallReceived`), ensure `p.currentThinking = ""` is reset immediately.
- In `router-proxy/internal/managed/embedded_tool_authority.go`:
  - In `BeforeToolExecute`, reset `p.currentThinking = ""` when a tool invocation begins.
- In `router-proxy/internal/managed/embedded_subagent_runner.go`:
  - In `BeforeToolExecute`, reset `p.currentThinking = ""` when child tool invocation begins.

### Step 2: Presentation Field Bounding & Sanitization
- In `router-proxy/internal/store/managed_history.go` (or helper function):
  - Add bounding for presentation text fields before JSON marshaling.
  - Specifically, create `boundManagedHistoryPresentation(pres *ManagedHistoryPresentation) *ManagedHistoryPresentation` or apply limits in `managedHistoryPresentationJSON`:
    - Bound `CompanionText` (e.g. truncate to at most ~512-768 bytes, ensuring UTF-8 rune boundaries).
    - Bound `Thinking` (e.g. truncate to at most ~256-512 bytes, ensuring UTF-8 rune boundaries).
    - Ensure total serialized JSON length fits comfortably within the 2048-byte limit.
- In `router-proxy/internal/managed/embedded_session.go`:
  - Before constructing `pres`, ensure `companionText` and `p.currentThinking` are bounded/truncated safely.

### Step 3: Graceful Fallback on Presentation Failure
- In `router-proxy/internal/store/managed_history.go`:
  - In `RecordManagedCompleteHistoryWithFullDetails`: if `managedHistoryPresentationJSON(presentation)` fails, do NOT fail the history record if the category is `managed_tool` or if fallback is viable. Instead, fall back to a minimal presentation:
    ```go
    fallbackPres := &ManagedHistoryPresentation{
        Kind:     presentation.Kind,
        ToolName: presentation.ToolName,
        Outcome:  presentation.Outcome,
        Summary:  presentation.Summary,
        Error:    presentation.Error,
    }
    ```
    If `fallbackPres` still cannot serialize (e.g. invalid outcome/fields), use a minimal safe presentation `&ManagedHistoryPresentation{Kind: "tool", ToolName: presentation.ToolName, Outcome: presentation.Outcome}` or `defaultManagedHistoryPresentation`.
- In `router-proxy/internal/managed/embedded_session.go`:
  - In tool completion logging (`projectMessage`), if `RecordManagedCompleteHistoryWithDetails` returns an error, log the error and retry once with a guaranteed minimal presentation (`Kind: "tool", ToolName: toolName, Outcome: outcome, Summary: outcomeSummary`) so the sequence/event key is never left in `"active"`.

## 5. Testing Strategy

### 1. Go Unit Tests
- `router-proxy/internal/store/managed_history_test.go`:
  - Test `RecordManagedCompleteHistoryWithDetails` with oversized `Thinking` and `CompanionText` (>2048 bytes). Verify it succeeds and bounds the presentation within <= 2048 bytes.
  - Test graceful fallback: verify that passing an oversized or complex presentation still transitions an existing `active` tool history entry to terminal `success` or `failed`.
- `router-proxy/internal/managed/embedded_session_test.go` (or `embedded_turn_test.go`):
  - Verify that `p.currentThinking` is cleared when a tool call is received/executed.
  - Verify that a tool completing with extensive thinking/companion text emits a terminal history entry with outcome `success`.

### 2. Playwright E2E Tests (`tests/e2e/tool-presentation-resilience.spec.ts`)
- **TPR-001**: "transitions tool call to success when accompanied by long thinking text"
  - Setup a managed agent running in embedded mode with mock LLM emitting verbose thinking text (>4KB) and assistant companion text before calling a tool (e.g. `bash` or `read`).
  - Tool completes.
  - Verify via timeline / REST API / SQLite store that the tool entry has transitioned to `outcome: "success"`, is no longer "Running" in UI, and `presentation_json` <= 2048 bytes.
- **TPR-002**: "falls back to clean minimal presentation if detailed presentation fails"
  - Seed an active tool history record in SQLite (`outcome: "active"`).
  - Simulate completion recording where detailed presentation metadata contains invalid or oversized text.
  - Verify the entry updates to `outcome: "success"` (or `"failed"`) with minimal presentation, timeline badge updates out of "Running", and UI displays the completion.

## 6. Acceptance Criteria Mapping

| Spec Requirement / Scenario | Acceptance Criteria | Planned Verification |
| :--- | :--- | :--- |
| **Invariant 1 & Scenario TPR-001**: Thinking state reset | Active thinking is cleared when tool call begins in session, authority, and subagent runner | Unit tests in `embedded_session_test.go`, E2E test `TPR-001` |
| **Invariant 2 & Scenario TPR-001**: Presentation JSON <= 2048 bytes | `presentation_json` never exceeds 2048 bytes even with large thinking and companion text | Store unit tests in `managed_history_test.go`, E2E test `TPR-001` |
| **Invariant 3 & Scenario TPR-002**: Graceful fallback on error | If presentation validation/serialization fails, fall back to minimal presentation without remaining active | Store unit tests, E2E test `TPR-002` |
| **Invariant 4**: Terminal state guarantee | Tool invocations in Complete history never remain in `active` state after execution concludes | E2E tests `TPR-001` & `TPR-002`, DB query assertion |
