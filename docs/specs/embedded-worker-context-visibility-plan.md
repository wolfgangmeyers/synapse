# Embedded Worker Context Visibility Implementation Plan

## 1. Overview & Architecture

This implementation plan covers exposing real-time context token usage and window capacity for embedded workers (`runtime == "embedded"`) in both the Go Router backend (`router-proxy/internal/managed/`) and the React WebUI (`web/router-ui-react/`).

### System Architecture Flow:
1. **Calculation & Lane Tracking (Backend)**:
   - When an embedded session turns/prompts execute in `embedded_session.go`, context token usage is computed via `embeddedProjectedTokens(session.History())` and model `ContextWindow`.
   - Resulting `ContextUsage` (`tokens`, `context_window`, `percent`) is stored in the agent's lane state and attached to `store.ManagedAgentRecord` or projection cache.
2. **Wire Projection & Real-Time Broadcast (Backend)**:
   - `PublicAgent` and `PublicAgentWithGoal` project `context_usage`.
   - Propagated over REST (`/v1/managed-agents`, `/v1/managed-agents/{agent_id}`) and WebSocket UI-live frames (`managed_agents_snapshot`, `managed_agent_delta`).
3. **Client Types & State Merge (Frontend)**:
   - `ManagedAgent` (`types.ts`) and `Agent` (`agent-projection.ts`) model `context_usage?: ContextUsage`.
   - `mergeAgentSources` and UI-live delta mergers preserve `context_usage`.
4. **Header Visual Presentation (Frontend)**:
   - `ConversationHeader.tsx` renders a slim context usage progress bar with formatted compact capacity (`"44% of 1M"`), detailed tooltip (`"440,120 / 1,048,576 tokens (42.0%)"`), and three-tier color coding (`<50%`, `50-79%`, `>=80%`).

---

## 2. Implementation Phases & Parallelism Matrix

| Step | Scope | Parallelism / Dependencies |
| --- | --- | --- |
| **Phase 1: Go Router Backend** | `router-proxy/internal/managed/` | Can begin immediately. Backend tasks 1.1-1.3 are sequential. |
| **Phase 2: React Frontend Types & State** | `web/router-ui-react/src/protocol/`, `src/state/` | **Can run in parallel with Phase 1** (schema contract is locked). |
| **Phase 3: WebUI Component & Styling** | `web/router-ui-react/src/components/`, `app.css` | Depends on Phase 2 types. |
| **Phase 4: Tests & Verification** | Go tests & Vitest / React tests | Backend test suite can run in parallel with Frontend test suite. |

---

## 3. Phase 1: Go Router Backend Projection & Calculation

### Step 1.1: ContextUsage Model & Public Projection
- **File**: `router-proxy/internal/managed/projection.go` (and `models.go` / new or existing helper)
- **Actions**:
  1. Define `ContextUsage`:
     ```go
     type ContextUsage struct {
         Tokens        *int     `json:"tokens"`
         ContextWindow int      `json:"context_window"`
         Percent       *float64 `json:"percent"`
     }
     ```
  2. Define helper `CalculateContextUsage(tokens *int, contextWindow int) *ContextUsage`:
     - If `tokens == nil` or `contextWindow <= 0`, return `nil` or struct with `tokens: nil, percent: nil, context_window: contextWindow`.
     - When valid: `percent = math.Round((float64(*tokens)/float64(contextWindow)*100.0)*10) / 10`.
  3. Update `PublicAgent(agent *store.ManagedAgentRecord)`:
     - Check if agent record has attached context usage (or look up in lane/manager projection context).
     - Include `"context_usage": contextUsage` in returned map if present (or omit when nil).
  4. Ensure `PublicAgentWithGoal` preserves `"context_usage"`.

### Step 1.2: Session Tracking & Calculation in Embedded Session Loop
- **File**: `router-proxy/internal/managed/embedded_session.go` & `embedded_automatic_fit.go`
- **Actions**:
  1. Leverage existing `embeddedProjectedTokens(session.History())` in `router-proxy/internal/managed/embedded_automatic_fit.go`.
  2. In `embedded_session.go`:
     - Store current `ContextUsage` on `lane` or `ManagedAgentRecord`.
     - After turn completion, prompt execution, or context reset:
       - Compute `tokens := embeddedProjectedTokens(session.History())`.
       - Retrieve model window: `session.Model.ContextWindow` (fallback to resolved registry model window).
       - Compute `usage := CalculateContextUsage(&tokens, contextWindow)`.
       - Update lane state.
  3. On context reset / compaction:
     - Refresh tokens immediately from post-compaction/post-reset transcript.

### Step 1.3: Real-Time UI-Live Broadcasts
- **File**: `router-proxy/internal/managed/embedded_session.go` (or `manager.go` / `child_snapshot_manager.go`)
- **Actions**:
  1. When updating `lane.contextUsage`, trigger a delta notification or snapshot publish to `uiLive`.
  2. Verify `managed_agent_delta` payload includes `context_usage` in `changes`.
  3. Verify `managed_agents_snapshot` frames serialize `context_usage` for all active embedded agents.

---

## 4. Phase 2: React Frontend Types & Projection Handling

*(Can run in parallel with Phase 1)*

### Step 2.1: Protocol Type Definitions
- **File**: `web/router-ui-react/src/protocol/types.ts`
- **Actions**:
  1. Define and export `ContextUsage`:
     ```typescript
     export interface ContextUsage {
       tokens: number | null;
       context_window: number;
       percent: number | null;
     }
     ```
  2. Update `ManagedAgent` interface to include:
     ```typescript
     context_usage?: ContextUsage;
     ```

### Step 2.2: State Projection & Merging
- **File**: `web/router-ui-react/src/state/agent-projection.ts`
- **Actions**:
  1. Update `Agent` interface:
     ```typescript
     export interface Agent extends ModelAgent {
       // ... existing fields
       context_usage?: ContextUsage;
     }
     ```
  2. In `mergeAgentSources`:
     - Ensure `context_usage` from `ManagedAgent` is copied to the merged `Agent` record:
       ```typescript
       if (managedAgent.context_usage !== undefined) {
         agent.context_usage = managedAgent.context_usage;
       }
       ```
  3. Ensure delta processing preserves or replaces `context_usage` when `changes.context_usage` arrives.

---

## 5. Phase 3: WebUI Component Rendering & Styling

### Step 3.1: Formatting Helpers
- **File**: `web/router-ui-react/src/utils/format.ts` (or `src/components/ConversationHeader.tsx`)
- **Actions**:
  1. Implement `formatCompactTokens(window: number): string`:
     - `>= 1_000_000`: `${(window / 1_000_000).toFixed(window % 1_000_000 === 0 ? 0 : 1)}M` (e.g., `1048576` -> `"1M"`, `2000000` -> `"2M"`).
     - `>= 1_000`: `${Math.round(window / 1_000)}k` (e.g., `200000` -> `"200k"`, `128000` -> `"128k"`).
     - `< 1_000`: `${window}`.
  2. Implement `formatContextUsageLabel(usage: ContextUsage): string`:
     - If `usage.percent === null`: `"- / " + formatCompactTokens(usage.context_window)`.
     - Otherwise: `${Math.round(usage.percent)}% of ${formatCompactTokens(usage.context_window)}`.
  3. Implement `formatContextUsageTooltip(usage: ContextUsage): string`:
     - `${usage.tokens !== null ? usage.tokens.toLocaleString() : 'Unknown'} / ${usage.context_window.toLocaleString()} tokens (${usage.percent !== null ? usage.percent.toFixed(1) + '%' : 'N/A'})`.

### Step 3.2: ContextUsageBar Component in ConversationHeader
- **File**: `web/router-ui-react/src/components/ConversationHeader.tsx`
- **Actions**:
  1. Determine severity threshold class:
     - `percent < 50`: `normal` (theme accent / green / cyan).
     - `50 <= percent < 80`: `warning` (amber / yellow).
     - `percent >= 80`: `critical` (red / coral).
  2. Render component when `agent?.context_usage` is present and `agent.context_usage.context_window > 0`:
     ```tsx
     <div
       className="context-usage-container"
       title={tooltipText}
       aria-label={`Context usage: ${labelText}`}
     >
       <div className="context-usage-track">
         <div
           className={`context-usage-fill ${severityClass}`}
           style={{ width: `${Math.min(100, Math.max(0, percent ?? 0))}%` }}
         />
       </div>
       <span className="context-usage-label">{labelText}</span>
     </div>
     ```
  3. Position within `ConversationHeader`:
     - Render in `.peer-heading` alongside identity and status badges without disrupting layout responsiveness.

### Step 3.3: CSS Styling & Theme Support
- **File**: `web/router-ui-react/src/app.css`
- **Actions**:
  1. Add `.context-usage-container`: flex row, align items center, gap 6px, font size 11-12px, font mono/sans.
  2. Add `.context-usage-track`: width 60px - 80px, height 6px, border radius 3px, background `var(--bg-tertiary, rgba(255,255,255,0.1))`, overflow hidden.
  3. Add `.context-usage-fill`:
     - Base transition on width (0.2s ease).
     - `&.normal`: `background-color: var(--accent-color, #38bdf8)`.
     - `&.warning`: `background-color: #f59e0b`.
     - `&.critical`: `background-color: #ef4444`.
  4. Ensure dark/light mode readability for `.context-usage-label`.

---

## 6. Phase 4: Verification & Functional Tests

### Step 4.1: Backend Go Tests
- **Directory**: `router-proxy/internal/managed/`
- **Actions**:
  1. `projection_test.go`:
     - Add test verifying `PublicAgent` and `PublicAgentWithGoal` serialize `context_usage` with `tokens`, `context_window`, and `percent`.
     - Verify omitted or null handling when `context_usage` is nil.
  2. `embedded_session_test.go` or `embedded_behavioral_lifecycle_test.go`:
     - Verify that completing an agent prompt/turn populates `context_usage`.
     - Verify that a context reset properly refreshes token count and percentage.
     - Run: `go test -v ./router-proxy/internal/managed/... -run TestContextUsage`

### Step 4.2: Frontend React Tests
- **Directory**: `web/router-ui-react/`
- **Actions**:
  1. `tests/component/ConversationHeader.test.tsx`:
     - Add test case: renders context usage bar with label `"42% of 1M"` and `normal` class for `tokens: 440120, context_window: 1048576, percent: 42.0`.
     - Add test case: renders `warning` class for 65% of 200k.
     - Add test case: renders `critical` class for 85% of 128k.
     - Add test case: renders gracefully / omitted when `context_usage` is undefined (e.g. tmux worker).
  2. Run vitest and lint:
     - `npm --prefix web/router-ui-react test tests/component/ConversationHeader.test.tsx`
     - `npm --prefix web/router-ui-react run typecheck`

---

## 7. Review & Landing Checklist
- [ ] No regression on tmux worker projections.
- [ ] Non-embedded agents or agents without token counts omit the progress bar without UI shift or error.
- [ ] Token and percentage calculation exactly matches Pi parity (`embeddedProjectedTokens` / `CalculateContextTokens`).
- [ ] Green test runs for both `router-proxy` Go tests and `web/router-ui-react` Vitest suites.
