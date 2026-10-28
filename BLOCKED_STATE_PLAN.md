# Blocked State Architecture & Implementation Plan

## 1. Executive Summary & Objective

Worker agents occasionally encounter unresolvable blockers requiring human or supervisory intervention (e.g. missing external credentials, ambiguous architectural requirements, external infrastructure down). Today, agents stall silently, spin in goal-continuation loops wasting tokens, or fail turns repeatedly.

This plan introduces an explicit **"blocked" execution state** for managed workers:
1. Agent tool `mecha_blocked` to set or clear the blocked state with a mandatory reason.
2. Prominent guidance that `blocked` is **not a workflow state**, but an **agent execution state** requiring human/user intervention, reserved exclusively for unresolvable blockers.
3. Automatic temporary suppression of goal evaluations and goal continuation loops while an agent is blocked.
4. UI visibility with blocked badges in `AgentRow` (sidebar) and `ConversationHeader`, supporting hover tooltip and a lightweight click popover/card to inspect the reason.

---

## 2. Current Architecture & Integration Points

### 2.1 Goal Continuation Suppression
In `router-proxy/internal/managed/`:
- **Post-Turn Goal Evaluation**: In `embedded_session.go` (`runEmbeddedSessionTurnWithContinuation`), the manager evaluates goals and dispatches continuations via `dispatchEmbeddedGoalContinuation`.
- **Continuation Gates**: In `embedded_goal_continuation.go`, `startEmbeddedGoalContinuation` and `kickoffIdleGoalContinuation` check whether continuation is permissible.
- **Suppression Point**: Checking the agent's blocked state at these gates halts autonomous goal loops without altering or removing the goal configuration. When cleared, goal iterations resume.

### 2.2 UI State Display
In `web/router-ui-react/`:
- **Sidebar (`AgentRow.tsx`)**: Renders workflow status badge inside `.agent-name-line`.
- **Header (`ConversationHeader.tsx`)**: Renders status badges in the header title area.
- The blocked badge sits adjacent to existing status badges in both views.

---

## 3. Specification: `mecha_blocked` Tool

### 3.1 Schema

**Tool Name**: `mecha_blocked`

```json
{
  "type": "object",
  "properties": {
    "action": {
      "type": "string",
      "enum": ["set", "clear"],
      "description": "Action to perform: 'set' to mark the worker as blocked, or 'clear' to unblock."
    },
    "reason": {
      "type": "string",
      "description": "Explanation of why the worker is blocked and what assistance is required. Required when action is 'set'."
    }
  },
  "required": ["action"],
  "additionalProperties": false
}
```

### 3.2 Tool Documentation & Distinction from Workflow State

The tool description and prompt documentation strictly convey:
> `blocked` is **not a workflow state**. It is an **agent execution state** indicating that external human/user assistance is required to make further progress.
>
> Never invoke `mecha_blocked` for ordinary workflow transitions, waiting on subagents, or debugging test failures. It is exclusively for conditions where autonomous progress is impossible without external input (e.g. missing credentials, ambiguous product decisions, broken external environment).
>
> While blocked, automatic goal continuation loops are suspended until unblocked.

### 3.3 Execution Semantics

When `action = "set"`:
- Validates that `reason` is provided and non-empty: `strings.TrimSpace(reason) != ""` (no arbitrary length limits).
- Persists blocked state to `managed_agents`: `blocked = 1`, `blocked_reason = reason`, `blocked_at = now`.
- Publishes updated agent projection over WebSocket.
- Returns structured tool response with warning notice:
  ```json
  {
    "ok": true,
    "blocked": true,
    "reason": "Missing production Stripe test API key",
    "blocked_at": "2026-10-27T16:00:00.000Z",
    "warning": "⚠️ Agent execution is now BLOCKED. Goal continuation loops are suspended until cleared."
  }
  ```

When `action = "clear"`:
- Updates `managed_agents`: `blocked = 0`, `blocked_reason = NULL`, `blocked_at = NULL`.
- Publishes updated agent projection over WebSocket.
- Goal continuations resume on subsequent turns.
- Returns:
  ```json
  {
    "ok": true,
    "blocked": false,
    "cleared_at": "2026-10-27T16:05:00.000Z",
    "message": "Worker is unblocked. Autonomous goal evaluations and continuations have resumed."
  }
  ```

---

## 4. Backend Implementation (`router-proxy`)

### 4.1 Database Migration: `113_managed_agent_blocked_state.sql`

```sql
-- Migration 113: Managed Agent Blocked State
ALTER TABLE managed_agents ADD COLUMN blocked INTEGER NOT NULL DEFAULT 0;
ALTER TABLE managed_agents ADD COLUMN blocked_reason TEXT;
ALTER TABLE managed_agents ADD COLUMN blocked_at TIMESTAMP;
```

### 4.2 Storage Layer: `router-proxy/internal/store/`
- Add `SetAgentBlocked(ctx context.Context, agentID string, blocked bool, reason string) error`.
- Add `GetAgentBlocked(ctx context.Context, agentID string) (bool, string, string, error)`.
- Update `ManagedAgentRecord` in `router-proxy/internal/store/managed.go`:
  ```go
  type ManagedAgentRecord struct {
      ...
      Blocked       bool
      BlockedReason string
      BlockedAt     string
  }
  ```

### 4.3 Tool Catalog Registration
- In `router-proxy/internal/store/workflow_tools.go`:
  Add `mecha_blocked` to `CanonicalWorkflowTools`.
- In `router-proxy/internal/store/native_tools.go`:
  Add `mecha_blocked` to `NativeToolCatalog`:
  ```go
  {ToolName: "mecha_blocked", Category: "Execution Control", Description: "Set or clear blocked execution state when external assistance is required", DefaultEnabled: true},
  ```

### 4.4 Manager Tool Implementation: `embedded_blocked_tool.go`
Create `router-proxy/internal/managed/embedded_blocked_tool.go`:
- Defines tool schema (`action`: `set` | `clear`, optional `reason`).
- Validates: if `action == "set"`, requires `strings.TrimSpace(reason) != ""`.
- Updates DB via `m.store.SetAgentBlocked(...)`.
- Calls `m.publishProjection()`.
- Registers tool in `embedded_session.go` session initialization.

### 4.5 Goal Evaluation & Continuation Suspension
1. **`startEmbeddedGoalContinuationWithPrompt`**:
   Check if agent is blocked; return early to suppress continuation.
2. **`kickoffIdleGoalContinuation`**:
   Check if agent is blocked; skip idle continuation dispatch.
3. **Post-Turn Goal Evaluation (`embedded_session.go`)**:
   Skip post-turn goal evaluation if `blocked == true`.

### 4.6 Projection Update
In `router-proxy/internal/managed/projection.go` (`PublicAgent`):
```go
result["blocked"] = agent.Blocked
if agent.Blocked {
    result["blocked_reason"] = agent.BlockedReason
    result["blocked_at"] = agent.BlockedAt
} else {
    result["blocked_reason"] = nil
    result["blocked_at"] = nil
}
```

---

## 5. Web UI Implementation (`web/router-ui-react`)

### 5.1 Protocol & Agent Types
Update `ManagedAgent` (`src/protocol/types.ts`) and `Agent` (`src/state/agent-projection.ts`):
```typescript
blocked?: boolean;
blocked_reason?: string | null;
blocked_at?: string | null;
```

### 5.2 Visual Styling
- Dark glassmorphic styling consistent with design system.
- Pill badge (height 24px, border-radius 9999px) with amber-red danger styling (`#f87171` / amber accent) and icon (`fa-solid fa-hand`).

### 5.3 Sidebar (`AgentRow.tsx`)
Render blocked pill badge alongside workflow state:
```tsx
{agent.blocked ? (
  <span
    className="agent-row-blocked-badge"
    title={`Blocked: ${agent.blocked_reason || "External assistance required"}`}
  >
    <i className="fa-solid fa-hand" aria-hidden="true" />
    <span className="agent-row-blocked-badge-text">Blocked</span>
  </span>
) : null}
```

### 5.4 Header Badge & Popover (`ConversationHeader.tsx`)
- Render a blocked badge button in the header title area next to workflow status.
- Tooltip displays reason on hover.
- Clicking the badge toggles a clean, lightweight popover card anchored to the badge showing the formatted reason and timestamp (`blocked_at`), with click-outside / Escape to dismiss.
- No heavy modal dialogs or manual unblock action buttons in the UI.

---

## 6. Skills Updates

- **`.claude/skills/worker/SKILL.md`**:
  Document `mecha_blocked(action="set", reason="...")` usage: reserved strictly for when progress is impossible without external help. Call `mecha_blocked(action="clear")` when resolved.
- **`.claude/skills/supervisor/SKILL.md`**:
  Surface blocked workers prominently in status reporting.

---

## 7. Phased Implementation Steps

1. **Migration & Store**: Add migration 113 and store methods in `managed_blocked.go`.
2. **Tool & Continuation Logic**: Implement `embedded_blocked_tool.go`, update `NativeToolCatalog`, and guard goal continuation paths.
3. **UI Display**: Add blocked badge to `AgentRow`, and badge + popover card to `ConversationHeader`.
4. **Skills & Tests**: Update worker/supervisor guidance; add unit and functional tests.
