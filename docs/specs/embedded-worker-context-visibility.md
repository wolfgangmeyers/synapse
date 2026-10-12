# Embedded Worker Context Visibility Specification

## 1. Overview & Goals

As agents perform turns, tools are called, file diffs are processed, and conversation history grows towards model context window limits.
Pi calculates context token usage using a blend of provider-reported usage and token estimates.
Currently, this information is not exposed to the user or supervisor in the WebUI.

This specification details:
1. How context usage is tracked in the router for embedded workers (`runtime == "embedded"`).
2. How the context usage data model is exposed over the router REST API and UI-Live WebSocket stream.
3. How the WebUI (`router-ui-react`) displays this information in the agent conversation header as an accessible progress bar with human-readable labels (e.g., `"44% of 1M"`) and detailed tooltips.

---

## 2. Token & Percentage Calculation (Pi Parity)

### 2.1 Pi's Calculation Method

In `@earendil-works/pi-coding-agent` (and `@earendil-works/pi-ai`):
1. **`calculateContextTokens(usage)`**:
   - If `usage.totalTokens > 0`, use `usage.totalTokens`.
   - Otherwise, sum components: `usage.input + usage.output + usage.cacheRead + usage.cacheWrite`.
2. **`estimateContextTokens(messages)`**:
   - Scans backward through transcript messages for the most recent valid `assistant` response with non-zero usage (excluding aborted/errored assistant messages).
   - If found:
     - `usageTokens = calculateContextTokens(lastAssistant.usage)`.
     - `trailingTokens` = estimated tokens for all subsequent messages using `estimateMessageTokens(msg)` (`ceil(chars / 4)` heuristic, with fixed 4800 chars for images, plus tool call name and JSON arguments).
     - Total context tokens = `usageTokens + trailingTokens`.
   - If no assistant usage is found:
     - Sums `estimateMessageTokens` for all messages in the transcript (including system instructions).
3. **Post-Compaction / Post-Reset Behavior**:
   - If a compaction or reset entry exists, any assistant usage prior to the boundary is stale.
   - If no post-boundary assistant response exists, `tokens = null`, `percent = null`.
4. **Percentage**:
   - `percent = (tokens / contextWindow) * 100.0`.
   - If `tokens == null` or `contextWindow <= 0`, `percent = null`.

### 2.2 Go Implementation in `router-proxy`

The Go SDK (`github.com/wolfgangmeyers/pi-go-embedded-sdk`) mirrors Pi:
- `ai.EstimateContextTokens(messages []ai.Message)` (or `coding.EstimateContextTokens(messages []agent.AgentMessage)` / `embeddedProjectedTokens`).
- The active embedded session (`*coding.Session`) maintains the live transcript `session.History()`.
- Model context window is available via `session.Model.ContextWindow` (or resolved via `managed.Model` / `ModelRegistry`).

We define a helper function on `Manager`:
```go
type ContextUsage struct {
    Tokens        *int    `json:"tokens"`
    ContextWindow int     `json:"context_window"`
    Percent       *float64 `json:"percent"`
}
```

When an embedded worker completes a turn or handles a prompt in `embedded_session.go`:
- The manager computes `ContextUsage` for the agent.
- `tokens`: calculated from `session.History()` using `embeddedProjectedTokens` (or `ai.EstimateContextTokens`).
- `context_window`: `model.ContextWindow`.
- `percent`: `float64(tokens) / float64(context_window) * 100.0`, rounded to 1 decimal place.
- Stored in-memory on the manager's lane for the agent (`lane.contextUsage`) and optionally on `ManagedAgentRecord` or attached at projection time.

---

## 3. Data Model & Wire Protocol

### 3.1 REST API & WebSocket Projection

#### Field Definition on `ManagedAgent` (`PublicAgent`)
In `router-proxy/internal/managed/projection.go`, `PublicAgent` (and `PublicAgentWithGoal`):
```json
{
  "agent_id": "embedded-worker-1",
  "source": "router-managed",
  "runtime": "embedded",
  "activity_state": "active",
  "model": {
    "provider": "google",
    "id": "gemini-3.8-flash"
  },
  "context_usage": {
    "tokens": 440120,
    "context_window": 1048576,
    "percent": 42.0
  }
}
```
If context usage is not yet determined (e.g. freshly started before first turn), `context_usage` can be omitted, or `tokens: null, percent: null`.

#### Endpoints Affected:
1. `GET /v1/managed-agents`: List of all managed agents includes `context_usage`.
2. `GET /v1/managed-agents/{agent_id}`: Single agent projection includes `context_usage`.
3. WebSocket `/v1/ui-live`:
   - `managed_agents_snapshot`: Agent objects contain `context_usage`.
   - `managed_agent_delta`: Delta updates contain updated `context_usage` when turns complete.

---

## 4. Frontend Architecture (`web/router-ui-react`)

### 4.1 Type Definitions

In `web/router-ui-react/src/protocol/types.ts`:
```typescript
export interface ContextUsage {
  tokens: number | null;
  context_window: number;
  percent: number | null;
}

export interface ManagedAgent {
  // ... existing fields ...
  context_usage?: ContextUsage;
}
```

In `web/router-ui-react/src/state/agent-projection.ts`:
```typescript
export interface Agent extends ModelAgent {
  // ... existing fields ...
  context_usage?: ContextUsage;
}
```
In `mergeAgentSources`, propagate `agent.context_usage` onto the merged `Agent` record.

### 4.2 Formatting & Label Helpers

In `web/router-ui-react/src/utils/format.ts` (or `ConversationHeader.tsx`):
- Format numbers in compact human-readable format:
  - `1_048_576` -> `"1M"`
  - `2_000_000` -> `"2M"`
  - `200_000` -> `"200k"`
  - `128_000` -> `"128k"`
  - `32_768` -> `"32k"`
- Format tokens:
  - Exact: `tokens.toLocaleString()` -> `"440,120"`
- Label display:
  - `"${Math.round(percent)}% of ${formatCompact(context_window)}"` -> e.g. `"42% of 1M"`, `"75% of 200k"`.
- Detailed Title / Tooltip:
  - `"${tokens.toLocaleString()} / ${context_window.toLocaleString()} tokens (${percent.toFixed(1)}%)"`.

### 4.3 UI Component & Header Layout

In `web/router-ui-react/src/components/ConversationHeader.tsx`:
Add a `ContextUsageBar` component rendered in the header when `agent.context_usage` is present and `context_window > 0`:

```tsx
<div className="header-context-usage" title={tooltipText} aria-label={`Context usage: ${percent}% of ${compactWindow}`}>
  <div className="context-usage-track">
    <div
      className={`context-usage-fill ${severityClass}`}
      style={{ width: `${Math.min(100, Math.max(0, percent))}%` }}
    />
  </div>
  <span className="context-usage-label">{label}</span>
</div>
```

#### Placement:
- In the `.peer-heading` subline (next to subagents / operational status), or in the title row adjacent to action buttons.
- Preferred: In `.peer-heading h2`, or alongside `.header-alien-symbols` / `.peer-activity` as a tidy pill/bar that maintains flex layout and does not collapse or overflow on smaller viewports.

#### Severity Styling (CSS):
- `< 50%`: `normal` (Theme accent or subtle teal/cyan, matching standard UI accent).
- `50% - 79%`: `warning` (Amber `#f59e0b` / yellow `#eab308`), indicating the worker has passed the warning threshold where scratchpad reminders begin.
- `≥ 80%`: `critical` (Red `#ef4444` / coral `#f87171`), indicating proximity to compaction / context reset limits.

---

## 5. Test Plan

### 5.1 Router Backend Tests
- **Unit / Projection Test (`router-proxy/internal/managed/projection_test.go`)**:
  - Test `PublicAgent` includes `context_usage` when attached.
  - Test serialization of `tokens`, `context_window`, and `percent`.
- **Embedded Turn Lifecycle Test (`router-proxy/internal/managed/embedded_behavioral_lifecycle_test.go`)**:
  - Run a simulated turn in an embedded session.
  - Verify that `context_usage` is populated with estimated tokens, valid window, and calculated percentage.
  - Verify `uiLive.publish` emits snapshot/delta with `context_usage`.

### 5.2 Frontend React Tests
- **Component Test (`web/router-ui-react/tests/component/ConversationHeader.test.tsx`)**:
  - Render `ConversationHeader` with an agent having `context_usage`:
    - Normal (<50%): verifies percentage text `"42% of 1M"`, progress bar width 42%, and normal class.
    - Warning (65%): verifies warning class and formatted text `"65% of 200k"`.
    - Critical (85%): verifies critical class.
    - No context usage (e.g. tmux agent): verifies component is gracefully omitted.
- **Typecheck & Linter**:
  - Run `npm run typecheck` and `npm run lint` in `web/router-ui-react/`.

---

## 6. Implementation Stages

1. **Stage 1 (Router Backend)**:
   - Add `ContextUsage` struct and update manager's session tracking to record and project context usage.
   - Update `PublicAgent` / `ProjectionListEnvelope` to include `context_usage`.
   - Update `uiLive` publisher to project updated context usage after turns.
2. **Stage 2 (Frontend React)**:
   - Update `protocol/types.ts` and `state/agent-projection.ts`.
   - Add `ContextUsageBar` to `ConversationHeader.tsx` with formatting and styling in `app.css`.
3. **Stage 3 (Verification & Tests)**:
   - Run backend Go tests (`go test ./internal/managed/...`).
   - Run frontend tests (`npm test` in `web/router-ui-react`).
