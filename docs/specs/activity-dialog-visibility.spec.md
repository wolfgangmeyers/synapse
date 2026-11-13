# Spec: Activity dialog visibility

- **ID**: `SPEC-ACTIVITY-DIALOG-001`
- **Status**: `Active`
- **Domain**: `AgentManagement`
- **Target E2E Suite**: `tests/e2e/activity-dialog-visibility.spec.ts`

## 1. Intent & User Outcome

The selected agent's shell jobs and subagent controls show their dialogs only when requested, without obscuring the conversation otherwise.

## 2. Core Invariants

1. A closed activity dialog and its contents are not visible or interactive.
2. Opening either activity dialog shows its content; closing it restores access to the conversation and both controls.

## 3. User Scenarios

### Scenario: [ADV-001] Open and close activity dialogs
- **Given**: A managed agent is selected with no shell jobs or subagents.
- **When**: The user opens, closes, and reopens each activity dialog, including closing with Escape.
- **Then**: Each dialog displays its empty state only while open; closed dialogs do not obscure or block the conversation or the other control.

## 4. Automated E2E Verification Matrix

| Scenario ID | E2E Test Suite | Test Case Name |
| :--- | :--- | :--- |
| `ADV-001` | `tests/e2e/activity-dialog-visibility.spec.ts` | `activity dialogs stay hidden until opened and work after closing` |
