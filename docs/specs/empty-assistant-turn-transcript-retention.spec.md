# Spec: Empty Assistant Turn Transcript Retention

- **ID**: `SPEC-TRANSCRIPT-EMPTY-TURN-001`
- **Status**: `Active`
- **Domain**: `AgentManagement`
- **Target Suite**: `router-proxy/internal/managed/embedded_google_auth_test.go`

---

## 1. Intent & User Outcome

Managed agents running against Google Gemini/Vertex providers occasionally generate empty assistant turns (such as turns producing no tool calls, no visible response text, or only whitespace/empty-string placeholders), particularly after background task notices, subagent completion events, or empty tool turns.

Previously, empty assistant turns were completely dropped from converted provider transcripts. Dropping an assistant turn caused the user turns that preceded and followed it (for instance, a subagent completion notice followed by a goal reminder or user prompt) to merge into a single compound user turn. Over time, the model received multi-part user messages headed by stale notifications, leading to context confusion and recurring empty turns.

This specification guarantees that empty assistant turns are retained in provider transcripts as a minimal valid model turn `{"role": "model", "parts": [{"text": "(empty response)"}]}`. Retaining this placeholder preserves strict turn alternation (`user` -> `model` -> `user`), prevents unintended concatenation of distinct user messages, and complies with provider transcript schema requirements.

---

## 2. Core Invariants

1. **Model Turn Structure Retention**: Empty assistant turns (containing no tool calls, no non-empty text, and no thinking tokens) MUST be retained in provider transcripts as a minimal model turn `{"role": "model", "parts": [{"text": "(empty response)"}]}`.
2. **Turn Alternation & Non-Merger**: Retaining the model turn MUST preserve turn alternation and MUST prevent preceding and subsequent user messages (e.g. subagent completion notifications, user prompts, goal reminders) from merging into a single user turn.
3. **Normalization**: Assistant turns containing only whitespace, empty text blocks, or `"(empty response)"` text MUST be normalized to `{"role": "model", "parts": [{"text": "(empty response)"}]}`.
4. **Consecutive User Message Merge**: Consecutive user messages without an intervening assistant turn MUST continue to merge into a single turn with multiple parts according to normal role merging rules.

---

## 3. User Scenarios (Behavioral Contract)

### Scenario: [SCN-001] Empty assistant turn between user prompt and goal reminder retains model turn and prevents user message merger
- **User Goal**: A background task completion notice or user prompt is followed by an empty assistant turn, followed by a goal wake-up reminder. The user prompt and the goal reminder must remain distinct turns.
- **Given**:
  - The message history contains:
    1. A `user` message with text `"Task completion notification"`.
    2. An `assistant` message with no content (empty content array).
    3. A `user` message with text `"NOT_DONE: Please finish the remaining subgoals"`.
- **When**:
  - The router converts the message history to Google provider contents (`convertMessagesToGoogleContents`).
- **Then**:
  - Exactly 3 distinct turns are produced in alternating order:
    1. `role`: `"user"`, `parts`: `[{"text": "Task completion notification"}]`
    2. `role`: `"model"`, `parts`: `[{"text": "(empty response)"}]`
    3. `role`: `"user"`, `parts`: `[{"text": "NOT_DONE: Please finish the remaining subgoals"}]`
  - The two user messages are NOT merged into one compound user turn.

### Scenario: [SCN-002] Empty assistant turn following tool result preserves turn alternation
- **User Goal**: An agent executes a tool, receives a tool result, produces an empty assistant turn, and then receives a user steering prompt or goal reminder. The tool result and user prompt must not collapse into a single turn.
- **Given**:
  - The message history contains:
    1. A `tool_result` message (role `user` with `functionResponse`).
    2. An `assistant` message with no content.
    3. A `user` message with text `"Please continue"`.
- **When**:
  - The router converts the message history to Google provider contents.
- **Then**:
  - The resulting transcript preserves turn alternation:
    1. `role`: `"user"`, `parts`: `[{"functionResponse": ...}]`
    2. `role`: `"model"`, `parts`: `[{"text": "(empty response)"}]`
    3. `role`: `"user"`, `parts`: `[{"text": "Please continue"}]`

### Scenario: [SCN-003] Assistant turn with only whitespace or `"(empty response)"` is normalized to a valid placeholder model turn
- **User Goal**: An assistant turn that contains only whitespace, stripped simulated text, or explicit `"(empty response)"` text is normalized into a minimal valid model turn rather than being omitted.
- **Given**:
  - The message history contains:
    1. A `user` message with text `"Prompt 1"`.
    2. An `assistant` message with content `ai.TextContent{Text: "   \n\t  "}` or `ai.TextContent{Text: "(empty response)"}`.
    3. A `user` message with text `"Prompt 2"`.
- **When**:
  - The router converts the message history to Google provider contents.
- **Then**:
  - The assistant message is converted to a model turn with `{"text": "(empty response)"}`.
  - The resulting transcript contains 3 distinct turns (`user` -> `model` -> `user`), and Prompt 1 and Prompt 2 remain separate.

### Scenario: [SCN-004] Consecutive user messages without intervening assistant turn continue to merge
- **User Goal**: When multiple user messages arrive consecutively without any intervening assistant turn, they must merge into a single user turn containing multiple text parts.
- **Given**:
  - The message history contains:
    1. A `user` message with text `"User message 1"`.
    2. A `user` message with text `"User message 2"`.
- **When**:
  - The router converts the message history to Google provider contents.
- **Then**:
  - Exactly 1 `user` turn is produced.
  - The turn's `parts` slice contains 2 text parts: `"User message 1"` and `"User message 2"`.
