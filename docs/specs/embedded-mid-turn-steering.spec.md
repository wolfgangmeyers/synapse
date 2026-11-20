# Spec: Embedded Mid-Turn Steering and Clean Tool Completion

- **ID**: `SPEC-STEERING-TOOL-COMPLETION-001`
- **Status**: `Active`
- **Domain**: `AgentManagement`
- **Target Suite**: `router-proxy/internal/managed/embedded_mail_test.go`

---

## 1. Intent & User Outcome

When a user or external supervisor sends a message (or high-priority mailbox message arrives) while an embedded agent is executing or preparing a batch of tool calls, pending and in-flight tool calls must not be synthetically aborted or replaced with error outcomes.

Instead, in-flight and batched tool calls execute to their natural completion (clean execution, success or tool-specific execution failure, never synthetic cancellation due to steering). The incoming steering message is preserved in the queue and delivered immediately as the prompt for the subsequent turn. The model observes all completed tool results alongside the steering prompt before synthesizing its next action or response, eliminating premature tool aborts and jarring synthetic error transcripts.

---

## 2. Core Invariants

1. **Uninterrupted Tool Batch Execution**: In-flight and batched tool calls MUST NOT be skipped, interrupted, or cancelled due to incoming steering messages or mailbox mail.
2. **Natural Tool Outcomes**: Tool results MUST reflect the actual execution outcome of the tool command; synthetic errors stating `"Tool call was not executed: interrupted by user steering message"` MUST NEVER be emitted during normal steering delivery.
3. **Prompt Delivery on Immediate Next Turn**: Steering messages arriving mid-turn MUST be delivered to the agent loop as the user message prompt on the turn immediately following the completed tool batch.
4. **Acknowledged Delivery**: Steered messages MUST transition to `acknowledged` upon delivery to the agent turn loop.
5. **Standard Outbox Invariant**: Final assistant text responding to a steered turn is persisted in conversation history and embedded entries for the user; outbound mailbox dispatch requires an explicit send tool rather than auto-routing final turn text into the mail outbox.

---

## 3. User Scenarios (Behavioral Contract)

### Scenario: [SCN-001] Mid-turn steering preserves execution of all batched tools
- **User Goal**: Send a directive to an agent currently executing multiple tools without corrupting or discarding unexecuted tools in the active batch.
- **Given**:
  - An embedded agent is active and issues a batch of multiple tool calls (e.g., Tool 1 and Tool 2).
  - Tool 1 begins execution.
- **When**:
  - A user or supervisor sends a steering message while Tool 1 is in-flight.
  - Tool 1 finishes execution.
- **Then**:
  - Tool 2 is NOT skipped or aborted; Tool 2 executes cleanly to its natural completion.
  - The transcript contains successful tool execution results for both Tool 1 and Tool 2.
  - Neither tool result contains synthetic error text indicating it was interrupted by user steering.

### Scenario: [SCN-002] Steering message delivered as subsequent prompt with full tool context
- **User Goal**: Ensure the agent receives the steering prompt together with complete results from prior tools.
- **Given**:
  - An embedded agent has completed execution of its active tool batch while a steering message was queued.
- **When**:
  - The turn loop advances to the next step.
- **Then**:
  - The steering message content is injected into the transcript as the user prompt for the next turn.
  - The model observes both completed tool results and the steering message content.
  - The agent synthesizes its response addressing the steering directive.
  - The steering message record state transitions to `acknowledged`.

---

## 4. Automated Verification Matrix

| Scenario ID | Scenario Description | Test Suite | Test Case Name |
| :--- | :--- | :--- | :--- |
| `SCN-001` | Batched tools execute cleanly to completion without abortion when steered | `router-proxy/internal/managed/embedded_mail_test.go` | `TestEmbeddedMailMultiToolCleanCompletionWithMidTurnSteering` |
| `SCN-002` | Queued steering message delivered on subsequent turn with full context | `router-proxy/internal/managed/embedded_mail_test.go` | `TestEmbeddedMailMultiToolCleanCompletionWithMidTurnSteering` |
| `SCN-002` | Single-tool mid-turn steering delivery and acknowledgement | `router-proxy/internal/managed/embedded_mail_test.go` | `TestEmbeddedMailMidTurnSteering` |
