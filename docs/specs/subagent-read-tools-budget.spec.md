# Spec: Subagent Read Tools Budget & Redirection

- **ID**: `SPEC-LIFECYCLE-005`
- **Status**: `Active`
- **Domain**: `AgentManagement`
- **Target E2E Suite**: `tests/e2e/subagent-read-tools-budget.spec.ts`

---

## 1. Intent & User Outcome

Managed agents running in the Mecha router harness frequently spawn background subagents for complex or time-consuming subtasks. Previously, aggressive and noisy deterrent warnings (e.g. `**MANDATORY**!!! This is a snapshot only. Do NOT repeatedly pull a running result!!!...`) were injected into tool schemas and running snapshot outputs to discourage busy-wait polling loops. Furthermore, child-specific throttling (`SubagentThrottle`) failed to prevent polling loops that alternated between `list_subagents` and `get_subagent_result`.

This feature replaces noisy deterrent messages and child-specific throttles with a unified, transparent subagent read credit budget modeled after the inline tools budget:
1. Subagent read tools (`get_subagent_result` and `list_subagents`) share a per-session-lane credit pool with a maximum capacity of 3 credits, refilling continuously at 1 credit per minute.
2. The pool starts fully primed with 3 credits.
3. Every invocation of `list_subagents` consumes 1 credit.
4. An invocation of `get_subagent_result` on a running or queued subagent (status polling) consumes 1 credit. Invocations of `get_subagent_result` on completed or terminal subagents retrieve final deliverables and do NOT consume credits.
5. When available credits drop below 1, calls to `list_subagents` and status checks on running subagents fail with a clear, professional error message redirecting the agent to rest, proceed with unblocked work, or await the automatic subagent completion notification.
6. All noisy warning text (`SubagentSnapshotWarning`), aggressive shouting, and child-specific throttles are removed, leaving clean, professional descriptions and error feedback.

---

## 2. Core Invariants

1. **Clean Tool Schema & Outputs**: Neither `get_subagent_result`'s description nor any tool output or error message shall ever contain aggressive shouting warnings (`**MANDATORY**!!!...`). The description for `get_subagent_result` shall be clean and professional.
2. **Unified Pool & Capacity**: The subagent read pool shall have a maximum capacity of 3 credits (`SubagentReadPoolCapacity = 3`) and replenish at 1 credit per minute (`SubagentReadReplenishmentIntervalMS = 60_000`), starting fully primed at 3 credits.
3. **Credit Accounting Guarantee**:
   - `list_subagents` ALWAYS costs 1 credit regardless of filter or subagent states.
   - `get_subagent_result` on subagents in `running` or `queued` state costs 1 credit.
   - `get_subagent_result` on subagents in terminal states (`completed`, `failed`, `stopped`, `timed_out`) costs 0 credits.
4. **Clean Redirection on Exhaustion**: When an agent attempts a credit-consuming subagent read call with insufficient credits (< 1.0), the call MUST be blocked with an informative redirection error directing the agent to rest, work on unblocked tasks, or wait for completion notification, and detailing the refill rate.
5. **Session Lane Persistence**: The credit pool is tracked on the agent's `managedAgentLane`, surviving transient session bindings or subagent child terminations.
6. **No Wait Flag Shouting**: Calling `get_subagent_result` with `"wait"` returns a clean error ("unsupported argument \"wait\": result retrieval is snapshot-only") without shouting warnings.

---

## 3. User Scenarios (Behavioral Contract)

### Scenario: [SCN-001] Agent reads status of running subagent within budget
- **User Goal**: Check the progress of an in-flight subagent during execution without getting shouted at.
- **Given**:
  - Agent has launched background subagent `sub-1` which is currently `running`.
  - Agent has a full subagent read pool (3.0 credits).
- **When**:
  - Agent calls `get_subagent_result(agent_id="sub-1")`.
- **Then**:
  - The call succeeds and returns the running snapshot report.
  - The snapshot report does NOT contain any shouting warnings or `SubagentSnapshotWarning` text.
  - The agent's subagent read pool is debited by 1 credit (remaining: 2.0 credits).

### Scenario: [SCN-002] Agent lists subagents within budget
- **User Goal**: Query the list of active subagents.
- **Given**:
  - Agent has an available subagent read pool with 2.0 credits.
- **When**:
  - Agent calls `list_subagents()`.
- **Then**:
  - The call succeeds and returns the active subagents list.
  - The agent's subagent read pool is debited by 1 credit (remaining: 1.0 credit).

### Scenario: [SCN-003] Subagent read budget exhaustion redirects polling agent
- **User Goal**: Agent is prevented from spinning in a polling loop and given clear guidance on next steps.
- **Given**:
  - Agent has exhausted its 3 subagent read credits (remaining: 0.0 credits).
  - Background subagent `sub-1` is still running.
- **When**:
  - Agent attempts to call `get_subagent_result(agent_id="sub-1")` or `list_subagents()`.
- **Then**:
  - The tool execution fails with an error:
    `"Subagent read budget exhausted: you have used all available subagent read credits. Subagent read tools (get_subagent_result and list_subagents) are intended only for occasional checks; repeated polling of subagent status is disallowed. You must rest, continue unblocked work, or wait for the automatic completion notification instead of polling. (Credits replenish at 1 credit per minute up to a maximum of 3.)"`
  - No credits are subtracted below 0.
  - The running subagent continues executing unaffected.

### Scenario: [SCN-004] Terminal subagent result retrieval is unbudgeted
- **User Goal**: An agent can retrieve deliverables and transcript for a finished subagent even if read credits are exhausted.
- **Given**:
  - Agent's subagent read pool is exhausted (0.0 credits).
  - Background subagent `sub-1` has finished with status `completed`.
- **When**:
  - Agent calls `get_subagent_result(agent_id="sub-1", verbose=true)`.
- **Then**:
  - The call succeeds immediately.
  - The complete result report and transcript are returned.
  - The pool balance remains 0.0 without triggering an exhaustion error.

### Scenario: [SCN-005] Read credit pool replenishes over time
- **User Goal**: Ensure agents can check subagents again after resting or performing other work.
- **Given**:
  - Agent exhausted credits at $T_0$ (0.0 credits remaining).
- **When**:
  - 60 seconds elapse ($T_0 + 60s$).
  - Agent calls `list_subagents()`.
- **Then**:
  - The pool has replenished 1.0 credit.
  - The `list_subagents()` call succeeds.
  - The remaining pool drops back to 0.0.

### Scenario: [SCN-006] Clean rejection of unsupported 'wait' argument
- **User Goal**: Agent attempting to block or wait on subagent results receives a clean error.
- **Given**:
  - Agent calls `get_subagent_result(agent_id="sub-1", wait=true)`.
- **When**:
  - The tool validates arguments.
- **Then**:
  - The tool returns error: `"unsupported argument \"wait\": result retrieval is snapshot-only"`.
  - The error contains no shouting warning text.

---

## 4. Automated E2E & Functional Verification Matrix

| Scenario ID | Scenario Description | Test Suite | Test Case Name |
| :--- | :--- | :--- | :--- |
| `SCN-001` | Running subagent snapshot within budget has clean output | `router-proxy/internal/managed/embedded_subagent_public_tools_test.go` | `TestSubagentReadBudgetScenarios` |
| `SCN-002` | List subagents consumes 1 credit | `router-proxy/internal/managed/embedded_subagent_public_tools_test.go` | `TestSubagentReadBudgetScenarios` |
| `SCN-003` | Budget exhaustion returns redirection error | `router-proxy/internal/managed/embedded_subagent_public_tools_test.go` | `TestSubagentReadBudgetScenarios` |
| `SCN-004` | Terminal subagent result retrieval costs 0 credits | `router-proxy/internal/managed/embedded_subagent_public_tools_test.go` | `TestSubagentReadBudgetScenarios` |
| `SCN-005` | Read credits replenish at 1 per minute | `router-proxy/internal/managed/embedded_subagent_budget_test.go` / `...public_tools_test.go` | `TestSubagentReadBudgetReplenishment` / `TestSubagentReadBudgetScenarios` |
| `SCN-006` | Wait argument rejected cleanly | `router-proxy/internal/managed/embedded_subagent_public_tools_test.go` | `TestSubagentReadBudgetScenarios` |
| `SCN-001`-`006` | End-to-end agent subagent budget & polling redirection | `tests/e2e/subagent-read-tools-budget.spec.ts` | `SPEC-LIFECYCLE-005: Subagent Read Tools Budget & Polling Redirection` |
