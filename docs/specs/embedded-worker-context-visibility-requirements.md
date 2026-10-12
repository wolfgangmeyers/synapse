# Embedded Worker Context Visibility Requirements

## Overview

When running embedded workers in the Mecha Go router, agents operate within bounded LLM context windows (e.g. 1M for Gemini, 200k for Claude, 128k for GPT-4o). While the router and runtime track token counts internally to trigger scratchpad reminders and context resets, users and supervisors currently have no visibility into an active worker's context utilization in the WebUI.

This feature introduces real-time context token and percentage visibility directly into the WebUI conversation header for embedded workers.

---

## Resolved Human Decisions

| Area | Decision |
| --- | --- |
| **Placement** | Top conversation header (`ConversationHeader.tsx`), adjacent to the agent identity and status controls. |
| **Visual representation** | Compact progress bar with text label (e.g., `"44% of 1M"`, `"72% of 200k"`). Tooltip or title showing exact token count (e.g. `440,120 / 1,000,000 tokens`). |
| **Calculation standard** | Matches Pi's canonical context usage calculation: uses provider-reported usage when available (`totalTokens` or `input + output + cacheRead + cacheWrite`), plus character-heuristic estimates for trailing messages post-assistant turn, divided by model `contextWindow`. |
| **Color coding / thresholds** | Matches system thresholds: < 50% normal (subtle/neutral/brand accent), 50%–79% warning (amber/yellow), ≥ 80% critical (red/coral). |
| **Scope** | Embedded router-managed workers (`runtime == "embedded"`). Graceful omission or placeholder when context usage is unknown or agent is disconnected/starting. |
| **Real-time updates** | Propagated via existing UI-live WebSocket updates (`managed_agents_snapshot` / `managed_agent_delta`) and REST polling endpoints (`/v1/managed-agents`), updating immediately when turns complete. |

---

## Requirements

### 1. Backend Data Model & Projection

- **R1.1 Context Usage Schema:**
  - Define `ContextUsage` structure on the managed agent projection:
    ```json
    {
      "tokens": 440120,
      "context_window": 1048576,
      "percent": 41.97
    }
    ```
  - `tokens` is integer count of estimated context tokens (or `null` if undetermined).
  - `context_window` is integer max token limit for the active model.
  - `percent` is float percentage `(tokens / context_window) * 100`, rounded to 1 decimal place or integer.
- **R1.2 Public Agent Serialization:**
  - Include `context_usage` in `managed.PublicAgent` / `managed.PublicAgentWithGoal` when available.
  - Included in `/v1/managed-agents` (list and single GET) and the `managed_agents_snapshot` and `managed_agent_delta` UI-live WebSocket events.
- **R1.3 Live Calculation in Embedded Session:**
  - For active embedded sessions (`*coding.Session`), the router manager retrieves current context tokens using `coding.EstimateContextTokens(session.History())` (or provider usage blend) and the active model's `ContextWindow`.
  - Upon completion of an agent turn or prompt execution, updated usage is cached in the agent record/lane and published to UI-live.

### 2. Frontend Data Model & State

- **R2.1 Protocol Types:**
  - Update `web/router-ui-react/src/protocol/types.ts` `ManagedAgent` interface to include optional `context_usage?: { tokens: number | null; context_window: number; percent: number | null }`.
- **R2.2 Agent Projection & State:**
  - Update `Agent` interface in `web/router-ui-react/src/state/agent-projection.ts` to include `context_usage`.
  - Ensure `mergeAgentSources` and delta handlers preserve `context_usage`.

### 3. User Experience & Component Design

- **R3.1 Conversation Header Progress Bar:**
  - In `ConversationHeader.tsx`, render a context usage component when `agent.context_usage` is present and valid.
  - Display format:
    - Text label: `"<percent>% of <window>"` (e.g., `"44% of 1M"`, `"68% of 200k"`, `"12% of 128k"`).
    - Progress bar: A slim, accessible bar indicating percentage filled.
    - Hover title/tooltip: Detailed token breakdown `"<tokens> / <context_window> tokens (<percent>%)"`.
- **R3.2 Threshold Styling:**
  - Normal (< 50%): theme accent / muted green/blue.
  - Warning (50% – 79%): amber / warning color (aligns with pre-reset warning zone for 1M models).
  - High / Critical (≥ 80%): coral / red (approaching hard compaction or reset threshold).
- **R3.3 Layout & Responsiveness:**
  - Kept compact to avoid crowding out prompt controls, model picker, workflow state buttons, or alien text.
  - Visually integrated with dark/light themes via CSS variables.

### 4. Non-Goals
- Manual context reset triggering from clicking the bar (context reset remains automatic or via `context_reset` tool).
- Modifying legacy tmux worker projections that do not report token counts.
