# Spec: Subagent Completion Forward Guidance

- **ID**: `SPEC-SUBAGENT-COMPLETION-GUIDANCE-001`
- **Status**: `Active`
- **Domain**: `AgentManagement`
- **Target E2E Suite**: `router-proxy/internal/managed/embedded_subagent_results_test.go`, `router-proxy/internal/managed/embedded_subagent_runner_test.go`, `router-proxy/internal/domain/message_test.go`, `tests/e2e/claude-managed-subagent-lifecycle.spec.ts`

---

## 1. Intent & User Outcome

When an automated background subagent finishes, is stopped, or is interrupted, the parent agent is notified via an automated system wake notification. Previously, trailing negative phrasing (*"Do not reply to this message — messages to 'system' are not accepted."*) caused strict instruction-following models to emit silent turns (no text and no tool calls) because they treated the negative directive as a prohibition against producing output.

This specification requires that all automated system notices delivered to agents provide clear, affirmative forward momentum guidance: instructing the agent to proceed immediately with its next action or tool call, or summarize the result to the user, while making clear that incoming messages cannot be addressed back to `system`.

---

## 2. Core Invariants

1. **No Negative Speech Constraints**: Automated system wake and lifecycle notifications delivered to agents MUST NOT instruct the agent not to speak or reply generally.
2. **Affirmative Forward Guidance**: System notifications MUST affirmatively guide the model to take its next tool call / action or summarize results to the user.
3. **System Unreplyability Guard**: Outbound mailbox messages targeting `"system"` (`mecha_mailbox_send`) remain rejected at admission, preventing accidental loops without suppressing conversational turns.
4. **Backward-Compatible Deduplication**: Mailbox content helpers (`MailboxContent`, `BatchItem`) must detect both legacy and current system notification footers to prevent duplicate trailing notices when formatting messages.

---

## 3. User Scenarios (Behavioral Contract)

### Scenario: [SCN-001] Subagent completion wake prompt contains affirmative forward guidance
- **Given**:
  - A background subagent finishes with status `completed` (or `failed`).
- **When**:
  - The router delivers the automated wake notice (`subagent-completed-<id>-<attempt>`) from `system` to the parent agent.
- **Then**:
  - The notification body ends with:
    `---\n*Automated notification from system. Messages to 'system' are not accepted; proceed directly with your next action or tool call, or summarize the result to the user.*`
  - The notification does NOT contain `"Do not reply to this message"`.

### Scenario: [SCN-002] Subagent stopped notification contains affirmative forward guidance
- **Given**:
  - A subagent is stopped via `StopSubagent`.
- **When**:
  - The router enqueues the `subagent-terminated-<id>-<attempt>` notice for the parent agent.
- **Then**:
  - The notification body ends with the affirmative forward guidance footer.
  - The notification does NOT contain `"Do not reply to this message"`.

### Scenario: [SCN-003] Subagent restart-interrupted notification contains affirmative forward guidance
- **Given**:
  - A router restart interrupts active subagents and `ReconcileInterruptedSubagents` runs.
- **When**:
  - The router enqueues the `subagent-interrupted-<id>-<attempt>` notice for the parent agent.
- **Then**:
  - The notification body ends with the affirmative forward guidance footer.
  - The notification does NOT contain `"Do not reply to this message"`.

### Scenario: [SCN-004] MailboxContent and BatchItem append forward guidance and deduplicate
- **Given**:
  - A message from `system` without a trailing system notice footer.
- **When**:
  - `MailboxContent(body, "system")` or `BatchItem(msg)` is called.
- **Then**:
  - The returned content includes the forward guidance footer.
  - If called repeatedly on already-footered content (new or legacy), no duplicate footer is appended.

---

## 4. Automated E2E & Unit Verification Matrix

| Scenario ID | Scenario Description | Test Suite | Test Case Name |
| :--- | :--- | :--- | :--- |
| `SCN-001` | Subagent completion wake prompt forward guidance | `router-proxy/internal/managed/embedded_subagent_results_test.go` | `TestSubagentNotificationFormatting` |
| `SCN-001` | Parent wake prompt delivers forward guidance | `router-proxy/internal/managed/embedded_subagent_runner_test.go` | `TestSubagentRunnerParentIdleWake` |
| `SCN-001` | Sender is system with forward guidance | `router-proxy/internal/managed/embedded_subagent_runner_test.go` | `TestSubagentCompletionMailboxNotificationSenderIsSystem` |
| `SCN-002` | Subagent stopped notification forward guidance | `router-proxy/internal/managed/embedded_subagent_stop_notify_test.go` | `TestStopSubagentNotifiesParentWorker` |
| `SCN-002` | Runner stop notification forward guidance | `router-proxy/internal/managed/embedded_subagent_runner_test.go` | `TestStopSubagentNotifiesParentWorker` |
| `SCN-003` | Interrupted subagent forward guidance | `router-proxy/internal/managed/embedded_subagent_restart_notify_test.go` | `TestReconcileInterruptedSubagentsNotifiesParent` |
| `SCN-004` | MailboxContent system footer & deduplication | `router-proxy/internal/domain/message_test.go` | `TestMailboxContentSystemNotice` |
| `SCN-004` | BatchItem system footer & deduplication | `router-proxy/internal/domain/message_test.go` | `TestBatchItemSystemNotice` |
