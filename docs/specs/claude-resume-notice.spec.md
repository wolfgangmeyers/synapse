# Spec: Claude Code Startup Resume Notice Handling and Unconsumed Message Accuracy

- **ID**: `SPEC-CLAUDE-RESUME-NOTICE-001`
- **Status**: Draft
- **Domain**: AgentManagement
- **Target E2E Suite**: `tests/e2e/claude-resume-notice.spec.ts`

---

## 1. Intent & User Outcome

Managed Claude Code processes may emit startup notices when resuming existing sessions or workspaces (such as notifications of interrupted or killed background shell jobs). When a message is delivered to a managed Claude worker that emits a background resume notice before echoing the admitted user message, the runtime must skip the notice turn and admit the delivered message so that it is processed and answered normally. Furthermore, if a failure occurs before Claude admits/echoes the input, the runtime must accurately report the input as unconsumed rather than marking the outcome uncertain, allowing safe, prompt retry and redelivery without leaving the mailbox or conversation in an indeterminate state.

---

## 2. Core Invariants

1. Claude startup resume notices (e.g. killed background process notifications) that precede the echo of admitted user input MUST be skipped and MUST NOT satisfy the pending user turn or consume its prompt.
2. The user turn MUST complete with the answer generated for the user's actual admitted input, not the resume notice.
3. Pre-admission failures (failures occurring before Claude echoes and admits the user prompt) MUST NOT mark the message outcome as uncertain.
4. Pre-admission failures MUST classify the message as unconsumed (`ErrClaudeUnconsumed`), resetting delivery state (`claude_stdin_started` reset to 0) so that the message remains eligible for retry and redelivery.

---

## 3. User Scenarios (Behavioral Contract)

### Scenario: [SCN-001] Resume notice skipped on Claude startup
- **User Goal**: Send a message to a Claude worker that restarts or resumes with residual background job notices without the user message being dropped or incorrectly answered with startup noise.
- **Given**:
  - A managed Claude worker is launched or resumed for an agent.
  - Claude emits a background resume notice (e.g. assistant tool/notice turn followed by a result) prior to echoing the user input.
- **When**:
  - A user message is delivered to the Claude worker.
  - Claude processes the startup notice, finishes that initial notice turn, and then echoes the delivered user input.
  - Claude produces an assistant answer for the user input.
- **Then**:
  - The runtime skips the resume notice turn without failing or prematurely finishing the user turn.
  - The user input is admitted upon echo.
  - The turn completes successfully returning the real assistant answer corresponding to the user input.

### Scenario: [SCN-002] Accurate error reporting for unconsumed message
- **User Goal**: Ensure that if a Claude process fails before admitting delivered mail, the system knows the input was unconsumed and safely redelivers or retries it rather than abandoning it as outcome uncertain.
- **Given**:
  - A message is dispatched to a managed Claude worker.
  - Claude has not yet echoed or admitted the user input (pre-admission phase).
- **When**:
  - The Claude process fails, exits, or encounters an I/O error prior to echoing the user prompt.
- **Then**:
  - The runtime reports the failure as an unconsumed error (`ErrClaudeUnconsumed`).
  - The mail transaction or turn record is NOT marked as outcome uncertain.
  - The delivery tracking state (e.g., `claude_stdin_started`) is reset to 0.
  - The message remains unconsumed and eligible for immediate redelivery or retry.

---

## 4. Automated E2E Verification Matrix

| Scenario ID | Scenario Description | E2E Test Suite | Test Case Name |
| :--- | :--- | :--- | :--- |
| `SCN-001` | Resume notice skipped on Claude startup | `tests/e2e/claude-resume-notice.spec.ts` | `skips background resume notice and delivers real answer` |
| `SCN-002` | Accurate error reporting for unconsumed message | `tests/e2e/claude-resume-notice.spec.ts` | `pre-admission failure marks message as unconsumed for retry` |
