# Spec: Mailbox Message Broadcast

- **ID**: `SPEC-MAILBOX-BROADCAST-001`
- **Status**: `Active`
- **Domain**: `Mailbox`
- **Target E2E Suite**: `tests/e2e/mailbox-broadcast.spec.ts`

## 1. Intent & User Outcome

Broadcasting mailbox messages from the web UI to multiple agents simultaneously allows Wolfgang to dispatch operational directives, announcements, or coordination pings to all or selected active workers at once without manually opening each agent's conversation feed and pasting the message one by one. The UI provides quick bulk selection, clear visibility into who will receive the message, per-recipient dispatch tracking, and fault-tolerant execution so failures on individual agents do not disrupt the remaining broadcasts.

## 2. Core Invariants

1. **Individual Message Envelopes**: Dispatches individual message envelopes to each recipient (`POST /v1/messages`), preserving distinct envelope headers and conversation isolation across agents.
2. **Failure Isolation**: Does not halt remaining dispatches if one recipient fails; dispatches to all selected targets continue independently.
3. **Active Worker Default**: Defaults to selecting active/running workers and excluding stopped/terminated workers and the current user identity (`resolvedAgentId`).
4. **Validation Guardrails**: Submit action is disabled when no recipients are selected or the message body is empty/whitespace.
5. **Sender Identity Preservation**: Preserves sender identity from active session (`resolvedAgentId`) for all outgoing envelopes.

## 3. User Scenarios (Behavioral Contract)

### Scenario: [SCN-BROADCAST-001] Accessing broadcast dialog from sidebar server controls menu
- **User Goal**: Open the broadcast compose dialog from the web UI sidebar.
- **Given**:
  - The web UI is open.
  - The sidebar server controls dropdown menu is accessible.
- **When**:
  - The user opens the server controls dropdown menu.
  - The user clicks the "Broadcast Message" action item.
- **Then**:
  - The Broadcast Message modal dialog opens displaying the recipient selection list, message subject and body inputs, and action buttons.
  - Closing the dialog (via Close button, Cancel button, or backdrop/Escape) dismisses the modal and restores full interaction with the underlying interface.

### Scenario: [SCN-BROADCAST-002] Target selection defaults to all active/running workers
- **User Goal**: Automatically target all running workers by default without manual selection.
- **Given**:
  - Agents exist in various states: running/active workers, stopped/terminated workers, and the active session identity (`resolvedAgentId`).
- **When**:
  - The user opens the Broadcast Message dialog.
  - The recipient list loads.
- **Then**:
  - All running/active workers have their checkboxes checked by default.
  - Stopped, paused, or terminated workers are unchecked by default.
  - The current user session identity (`resolvedAgentId`) is excluded from recipient selection.
  - The recipient count badge accurately reflects the number of default-selected active workers.

### Scenario: [SCN-BROADCAST-003] Bulk selection controls and individual checkbox toggles
- **User Goal**: Efficiently customize recipient targets via bulk actions and individual toggles.
- **Given**:
  - The Broadcast Message dialog is open with multiple available agents.
- **When**:
  - The user clicks "Select All", "Deselect All", or "Select Active".
  - The user clicks individual agent checkboxes to toggle their selection.
- **Then**:
  - "Select All" checks every eligible target agent in the list.
  - "Deselect All" unchecks every target agent.
  - "Select Active" checks only running/active workers and unchecks stopped agents.
  - Clicking an individual checkbox toggles that agent's selection state.
  - The selected count badge updates dynamically with every toggle.

### Scenario: [SCN-BROADCAST-004] Form validation on empty body or zero recipients
- **User Goal**: Prevent transmitting empty messages or attempting sends without targets.
- **Given**:
  - The Broadcast Message dialog is open.
- **When**:
  - Zero recipients are selected, OR
  - The message body is empty or contains only whitespace characters.
- **Then**:
  - The "Send Broadcast" submit button is disabled.
  - When at least one recipient is selected AND the body contains non-whitespace text, the "Send Broadcast" button is enabled.

### Scenario: [SCN-BROADCAST-005] Message dispatch and individual mailbox envelope delivery
- **User Goal**: Send independent mailbox envelopes to all selected recipients without cross-contamination.
- **Given**:
  - Two or more recipient agents are selected.
  - A valid broadcast message body (and optional subject) has been entered.
- **When**:
  - The user clicks "Send Broadcast".
- **Then**:
  - The client sends a distinct `POST /v1/messages` request for each selected recipient.
  - Each dispatch uses the sender ID from `resolvedAgentId`, the target agent as recipient, and generates a unique message envelope ID.
  - Each recipient receives the message envelope in its own mailbox feed without message cross-talk.

### Scenario: [SCN-BROADCAST-006] Real-time feedback and per-recipient status indicators
- **User Goal**: Observe live progress and status for each recipient during broadcast dispatch.
- **Given**:
  - The user initiates a broadcast send to multiple recipients.
- **When**:
  - The dispatches are processed.
- **Then**:
  - During in-flight dispatch, the dialog displays an in-progress indicator.
  - Each recipient row displays its dispatch outcome: a success checkmark upon HTTP 200/201 response, or an error indicator upon failure.
  - Once all dispatches settle, the dialog displays a summary of successful and failed sends.

### Scenario: [SCN-BROADCAST-007] Error resilience on partial dispatch failures
- **User Goal**: Ensure failures on one recipient do not interrupt or abort dispatches to others.
- **Given**:
  - Multiple recipients are selected for broadcast.
  - Dispatch to one recipient encounters an error (e.g. backend HTTP 500 or network rejection).
- **When**:
  - The broadcast is executed.
- **Then**:
  - The dispatch loop does not abort; remaining recipient dispatches continue to execution.
  - The failed recipient row displays a distinct error badge and failure message.
  - The successful recipients display success status.
  - The dialog remains open so the user can inspect the failures and take corrective action.

## 4. Automated E2E Verification Matrix

| Scenario ID | Scenario Description | E2E Test Suite | Test Case Name |
| :--- | :--- | :--- | :--- |
| `SCN-BROADCAST-001` | Accessing broadcast dialog from sidebar server controls menu | `tests/e2e/mailbox-broadcast.spec.ts` | `opens broadcast dialog from sidebar server controls menu` |
| `SCN-BROADCAST-002` | Target selection defaults to all active/running workers | `tests/e2e/mailbox-broadcast.spec.ts` | `defaults recipient selection to active running workers` |
| `SCN-BROADCAST-003` | Bulk selection controls and individual checkbox toggles | `tests/e2e/mailbox-broadcast.spec.ts` | `handles bulk selection controls and individual recipient toggles` |
| `SCN-BROADCAST-004` | Form validation on empty body or zero recipients | `tests/e2e/mailbox-broadcast.spec.ts` | `disables submit when no recipients selected or body is empty` |
| `SCN-BROADCAST-005` | Message dispatch and individual mailbox envelope delivery | `tests/e2e/mailbox-broadcast.spec.ts` | `dispatches individual message envelopes to each recipient` |
| `SCN-BROADCAST-006` | Real-time feedback and per-recipient status indicators | `tests/e2e/mailbox-broadcast.spec.ts` | `shows per-recipient progress and status indicators during dispatch` |
| `SCN-BROADCAST-007` | Error resilience on partial dispatch failures | `tests/e2e/mailbox-broadcast.spec.ts` | `continues remaining dispatches when an individual recipient fails` |
