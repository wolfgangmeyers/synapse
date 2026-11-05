# Persistent Goal and Workflow Snooze Plan

## 1. Problem & Root Cause

When either `goal_snooze` or `workflow_snooze` is called:
1. Both tools already compute a valid RFC3339 `DueAt` timestamp and save it to `managed_agent_policies.snoozed_until` via `SetManagedGoalSnoozeUntil`.
2. Both tools also record a preference in `embedded_goal_snooze_preferences` keyed specifically to the initiating turn `(user_seq, user_digest)`.
3. However, when an intermediate turn occurs (e.g. user steering message, inbound mailbox delivery, or system reminder):
   - The new turn settles with a new `user_seq`.
   - `reserveEmbeddedGoalContinuation` in `router-proxy/internal/store/embedded_goal_continuation.go` strictly queries `embedded_goal_snooze_preferences` joining on `p.user_seq = e.UserSeq`.
   - Because the intermediate turn did not call snooze, no row matches.
   - `due` evaluates to `NULL`, scheduling an immediate continuation (~10s) and destroying the remaining snooze window.

As Wolfgang observed: **Snooze should never be per-message.** Snooze is a persistent agent-level delay until a timestamp in the future.

---

## 2. Objective

Snooze must persist across intermediate turns for both explicit goals and active workflows:
- If an agent has a `snoozed_until` timestamp in `managed_agent_policies` where `snoozed_until > now`, subsequent continuations reserved for that agent must honor `snoozed_until`.
- Workflow transitions (`transition`) and workflow exits (`exit`) must clear `snoozed_until`.
- Goal completion (`CompleteEmbeddedGoalEvaluation`) must clear `snoozed_until`.
- If an explicit turn specifies a later snooze preference, the later `due_at` wins.
- Zero schema migrations or new tables are required: `managed_agent_policies.snoozed_until` already exists.

---

## 3. Implementation Steps

### A. Continuation Resolution (`router-proxy/internal/store/embedded_goal_continuation.go`)
In `reserveEmbeddedGoalContinuation(ctx context.Context, e EmbeddedGoalEvaluation)`:
1. Check `embedded_goal_snooze_preferences` for any turn-specific preference (existing logic).
2. Query `snoozed_until` from `managed_agent_policies WHERE agent_id = ?`:
   ```go
   var policySnoozedUntil sql.NullString
   _ = s.db.QueryRowContext(ctx, `SELECT snoozed_until FROM managed_agent_policies WHERE agent_id=?`, e.AgentID).Scan(&policySnoozedUntil)
   if policySnoozedUntil.Valid && strings.TrimSpace(policySnoozedUntil.String) != "" {
       if t, err := time.Parse(time.RFC3339Nano, policySnoozedUntil.String); err == nil && t.After(s.clock.Now()) {
           if !due.Valid || t.After(mustParseTime(due.String)) {
               due = policySnoozedUntil
           }
       }
   }
   ```
3. Insert continuation row with resolved `due_at`.

### B. Lifecycle Snooze Clearing
1. **Workflow Transitions (`router-proxy/internal/managed/embedded_workflow_transition.go`)**:
   - In `case "transition":`, clear `snoozed_until`:
     ```go
     _ = m.store.SetManagedGoalSnoozeUntil(ctx, owner.binding.AgentID, "")
     ```
2. **Goal Completion (`router-proxy/internal/store/embedded_goal_evaluation.go`)**:
   - In `CompleteEmbeddedGoalEvaluation`, include `snoozed_until=NULL` in the SQL update when completing a goal:
     ```sql
     UPDATE managed_agent_policies SET goal_enabled=0,evaluator_state='done',last_outcome='COMPLETED',snoozed_until=NULL,updated_at=?,evaluator_reason=? WHERE agent_id=? AND applied_revision=? AND projection_state='applied'
     ```

---

## 4. Verification & Testing

### Go Unit Tests
1. **`router-proxy/internal/store/`**:
   - `TestEmbeddedGoalContinuation_HonorsPolicySnoozedUntil`:
     - Set `snoozed_until` in `managed_agent_policies` 10 minutes in the future.
     - Call `reserveEmbeddedGoalContinuation` for a turn (`user_seq=2`) with no matching preference in `embedded_goal_snooze_preferences`.
     - Verify `due_at` equals `snoozed_until`.
   - `TestEmbeddedGoalEvaluation_ClearsSnoozedUntilOnCompletion`:
     - Verify `CompleteEmbeddedGoalEvaluation` clears `snoozed_until` to NULL on completion.
2. **`router-proxy/internal/managed/`**:
   - `TestWorkflowTransition_ClearsSnoozedUntil`:
     - Verify executing `workflow_transition` with action `transition` clears `snoozed_until`.

### Playwright E2E Test
- **`tests/e2e/workflow-snooze-persistence.spec.ts`**:
  1. Launch managed agent with active workflow.
  2. Agent executes `workflow_snooze` for 5 minutes.
  3. Send an intermediate steer/user message during the snooze window.
  4. Wait for turn settlement and verify continuation `due_at` is preserved and does not fire immediately.
