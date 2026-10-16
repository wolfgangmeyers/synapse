# SPEC: Embedded Worker Context Thresholds & Compaction Hint Removal

## 1. Problem Statement

Embedded worker agents in the Mecha system manage long-running context using scratchpad persistence and epoch-based context resets (`context_reset`). However, the context management subsystem has several limitations and legacy artifacts:

1. **Outdated Compaction Hints**:
   - Embedded workers previously relied on automatic or manual context compaction. Compaction has been replaced by context reset and scratchpad persistence.
   - Diagnostic messages, guidance strings, and tool returns (e.g., in `mecha_context`) still contain legacy compaction guidance and deprecation warnings ("Context compaction is deprecated and has been replaced by context reset and scratchpad...").
   - These compaction hints confuse LLMs, waste context tokens, and fail to clearly guide the model to update the scratchpad and trigger `context_reset`.

2. **Hardcoded Context Thresholds**:
   - Warning and auto-reset thresholds are currently hardcoded with arbitrary magic numbers (40% warning / 50% reset for models with $\ge$ 1M window; 70% warning / 80% reset for models with $<$ 1M window) in `router-proxy/internal/managed/embedded_context_reset.go` and `models.go`.
   - Operators and users cannot tune these thresholds globally or on a per-model basis.
   - For fast, lower-latency models or models with rapid degradation, operators require earlier warning thresholds (default 10%) to proactively encourage scratchpad notes and self-directed context resets, and an automatic fallback reset threshold (default 20%) before context bloat compromises reasoning.

3. **Missing Configuration Interface and Dynamic Evaluation**:
   - There is no REST API or UI dialog to configure context threshold settings.
   - Changes cannot be applied dynamically without modifying Go source code and restarting the router.

---

## 2. User Scenarios & Requirements

### 2.1 Clean Removal of Embedded Worker Compaction Hints
- Embedded tools (specifically `mecha_context`) and system guidance must remove all obsolete mentions of "compaction" or "compaction is deprecated".
- `mecha_context` (action=`status`) guidance and descriptions must present clean, modern context-management semantics:
  - Current token usage, context window, and percentage.
  - Active warning and reset threshold percentages.
  - Scratchpad entry counts.
  - Clear guidance directing the worker to save key plans, findings, and milestones to the `scratchpad` tool and invoke `context_reset` when approaching context limits.
- Error messages for unsupported actions on `mecha_context` must cleanly state that only `action=status` is supported (the tool is an informational context status tool; context resets are performed via `context_reset` / `reset`).

### 2.2 Configurable Two-Stage Context Thresholds
The router must support a two-stage threshold evaluation mechanism for embedded worker sessions:
1. **Stage 1: Warning Threshold (`warning_percent`, default: 10%)**:
   - When context token usage meets or exceeds `warning_percent` but remains below `reset_percent`, the router injects an in-band reminder message to the worker's mailbox.
   - The warning reminds the worker that it is approaching the context threshold and instructs it to:
     1. Summarize and persist critical context, plans, and state to `scratchpad`.
     2. Call `context_reset` to restart with a clean context and fresh headroom.
2. **Stage 2: Fallback Auto-Reset Threshold (`reset_percent`, default: 20%)**:
   - When context token usage meets or exceeds `reset_percent`, the router executes an immediate fallback automatic context reset (`ExecuteContextReset`).
   - The worker is reset to epoch $N+1$, carrying over the genesis prompt, scratchpad entries, and recent conversation turns, followed by the wake prompt.

### 2.3 Global Router Configuration & Per-Model Overrides
- **Global Settings**: Default warning and fallback auto-reset percentages applied across all models when no specific override matches. Default values: `warning_percent: 10`, `reset_percent: 20`.
- **Per-Model Overrides**: Specific threshold overrides keyed by model identifier (or exact provider/model prefix, e.g. `google-vertex/gemini-3.8-flash`, `openai-codex/gpt-5.6-terra`, or model ID).
  - Each override defines custom `warning_percent` and `reset_percent`.
  - Enables aggressive resets on large/expensive models and relaxed thresholds on small or specialized models.
- **Persistence**: Threshold settings are stored durably in the router's database (`managed_settings` or dedicated table).

### 2.4 Dynamic Turn Evaluation in `checkContextResetAfterTurn`
- In `checkContextResetAfterTurn(ctx, binding, model, session)`:
  - Read active threshold configuration for the session's active model (matching per-model override first, falling back to global settings, falling back to compile-time defaults).
  - Calculate `currentTokens` from `session.History()`.
  - Compare `currentTokens` against `reset_percent * model.ContextWindow / 100` and `warning_percent * model.ContextWindow / 100`.
  - If hard reset threshold is exceeded: trigger `ExecuteContextReset(ctx, binding, "threshold_exceeded")`.
  - Else if warning threshold is exceeded: emit in-band warning reminder if not already emitted in the current session epoch.

### 2.5 Web UI Configuration Dialog
- A configuration dialog accessible from the router web UI (e.g., Settings menu or agent header controls).
- **Design Patterns**:
  - Uses standard `DialogShell` component.
  - Compact circular buttons (`.icon-btn-round`: 24px x 24px, `border-radius: 999px`, translucent background, subtle border, hover states).
  - Translucent inputs, sliders, or number fields matching dark glass aesthetic (`rgba(...)`, subtle borders).
  - Responsive validation feedback with error banners / status messages.
- **Functionality**:
  - Input fields for Global Warning Percentage and Global Reset Percentage.
  - Table / card list of Per-Model Overrides with Add, Edit, and Delete actions.
  - Model selection dropdown populated with known models from `/v1/models`.
  - Form validation preventing invalid threshold configurations ($0 < \text{warning} < \text{reset} \le 100$).
  - Save button committing changes via `PUT /v1/settings/context-thresholds`.

### 2.6 End-to-End Behavioral Functional Tests
- End-to-end integration tests using deterministic mock LLMs (no external LLM calls):
  - Mock worker accumulates tokens past the configured warning percentage: verify warning mailbox reminder is delivered with correct phrasing and scratchpad guidance.
  - Mock worker continues and exceeds configured reset percentage: verify automated fallback context reset triggers, epoch increments, and scratchpad/recent entries are preserved.
  - Verification of custom per-model thresholds taking precedence over global settings.
  - Verification that compaction strings are absent from all tool schemas, guidance strings, and warning reminders.

---

## 3. API Contracts

### 3.1 `GET /v1/settings/context-thresholds`
Fetches the current context threshold configuration.

#### Response (HTTP 200 OK)
```json
{
  "global": {
    "warning_percent": 10,
    "reset_percent": 20
  },
  "model_overrides": {
    "google-vertex/gemini-3.8-flash": {
      "warning_percent": 15,
      "reset_percent": 25
    },
    "openai-codex/gpt-5.6-terra": {
      "warning_percent": 10,
      "reset_percent": 20
    }
  }
}
```

### 3.2 `PUT /v1/settings/context-thresholds`
Updates the context threshold configuration.

#### Request Body
```json
{
  "global": {
    "warning_percent": 10,
    "reset_percent": 20
  },
  "model_overrides": {
    "google-vertex/gemini-3.8-flash": {
      "warning_percent": 15,
      "reset_percent": 25
    }
  }
}
```

#### Validation Rules
1. `global` object is required:
   - `warning_percent` must be an integer or float such that $1 \le \text{warning\_percent} \le 99$.
   - `reset_percent` must be an integer or float such that $1 \le \text{reset\_percent} \le 100$.
   - **Constraint**: `warning_percent < reset_percent`.
2. `model_overrides` is an optional object:
   - Keys must be non-empty model identifiers or provider/model strings.
   - Each entry must specify `warning_percent` and `reset_percent` satisfying $1 \le \text{warning\_percent} < \text{reset\_percent} \le 100$.
3. If validation fails: Return `HTTP 400 Bad Request` with structured error `{ "error": "invalid_request", "message": "<specific validation error>" }`.

#### Response (HTTP 200 OK)
Returns the validated and applied configuration matching the `GET` contract.

---

## 4. Edge Cases & Constraints

1. **Single-Epoch Rate Limiting for In-Band Warning**:
   - The warning message must be emitted at most **once** per agent session epoch (`binding.Epoch`).
   - The lane state tracking (`lane.warningEpoch` and `lane.warningEmitted`) must prevent repeated warning messages across successive turns within the same epoch.
   - When an auto-reset or voluntary `context_reset` advances the session to epoch $N+1$, `lane.warningEmitted` is reset to `false` and `lane.warningEpoch` is updated to $N+1$.

2. **Large Single-Turn Context Jumps**:
   - If a large file read or extensive tool output causes the session token count to jump directly past the fallback auto-reset threshold (e.g. from 5% straight to 25%), the router must immediately trigger `ExecuteContextReset` without getting stuck trying to emit a pre-warning turn first.

3. **Preservation of Scratchpad and Recent History**:
   - Fallback auto-reset must strictly preserve:
     1. System instructions and agent identity prompt.
     2. Scratchpad entries via `FetchScratchpadRecent`.
     3. Last 4–6 messages from conversation history to maintain conversational continuity.
   - The agent receives the standard `ContextResetWakePrompt` on the new epoch.

4. **Missing or Invalid Per-Model Overrides**:
   - If a model override entry is empty, deleted, or references an unrecognized model ID, the router must fall back to the configured global thresholds.
   - If global thresholds in the database are corrupted or absent, fall back to safe hardcoded defaults: `warning_percent: 10`, `reset_percent: 20`.

5. **Models with Zero or Unknown Context Window**:
   - If `model.ContextWindow <= 0`, context threshold evaluation is disabled; `checkContextResetAfterTurn` takes no threshold action, avoiding division-by-zero or spurious resets.

6. **Concurrency and Non-Blocking Updates**:
   - Reading context threshold settings during turn checks must be lock-protected or read from an atomic cache to avoid blocking active worker turns during PUT updates.

---

## 5. Acceptance Criteria

- **AC1: Clean Embedded Worker Guidance**:
  - All compaction hints, compaction references, and "compaction is deprecated" strings are removed from `mecha_context` output, parameter descriptions, and system prompts for embedded workers.
  - `mecha_context` guidance cleanly instructs the agent to save notes to `scratchpad` and invoke `context_reset` before reaching threshold boundaries.
- **AC2: Configurable Global Thresholds**:
  - Global warning and reset thresholds default to 10% and 20% respectively.
  - Setting new global thresholds via `PUT /v1/settings/context-thresholds` takes effect immediately for subsequent turn evaluations without router restart.
- **AC3: Per-Model Overrides**:
  - Per-model overrides can be set and retrieved via the API.
  - When a turn completes for an agent using a model with an override, the override's warning and reset percentages are evaluated instead of global defaults.
- **AC4: Warning Reminder In-Band Delivery**:
  - When context token usage crosses `warning_percent` (and remains below `reset_percent`), an in-band reminder message is delivered to the agent's mailbox instructing it to use `scratchpad` and call `context_reset`.
  - The warning reminder is delivered exactly once per epoch.
- **AC5: Automatic Fallback Context Reset**:
  - When context token usage crosses `reset_percent`, the router executes automatic context reset: the session advances to the next epoch, preserves scratchpad and recent messages, and dispatches the wake turn.
- **AC6: Web UI Settings Modal**:
  - The router React web UI contains a dedicated Context Thresholds configuration dialog utilizing `DialogShell` and compact round icons.
  - Users can view and adjust global warning/reset percentages and add/edit/remove per-model overrides with live form validation ($0 < \text{warning} < \text{reset} \le 100$).
- **AC7: Automated Behavioral Tests**:
  - End-to-end integration tests using mock LLMs verify warning emission at 10%, fallback reset at 20%, per-model override behavior, and complete absence of compaction hints.
