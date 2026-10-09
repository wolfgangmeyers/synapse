# Plan: Workflow State Tool Restrictions

Implement `SPEC.md` for Workflow State Tool Restrictions using the dev-loop workflow (TDD, integration tests for regression prevention, robot review).

## Overview & Architecture

When an embedded agent is assigned an active workflow state that configures `allowed_tools`, only those tools should be exposed in the agent's session and admitted for execution. By default (`allowed_tools` empty or null), all standard tools remain available.

### Components to Update
1. **Database Schema & Store**:
   - Migration `093_workflow_state_allowed_tools.sql`: adds `allowed_tools_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(allowed_tools_json))` to `workflow_states`.
   - Update `router-proxy/internal/store/workflow_states.go`:
     - Add `AllowedTools []string` to `WorkflowState` and `WorkflowStateRequest`.
     - Tool whitelist validation (`mecha_mailbox_send`, `goal`, `goal_snooze`, `mecha_context`, `mecha_reminder`, `context_reset`, `reset`, `scratchpad`, `bash`, `read`, `edit`, `write`, `subagent`, `get_subagent_result`, `steer_subagent`, `mecha_worker`).
     - JSON serialization & deserialization in `CreateWorkflowState`, `UpdateWorkflowState`, `GetWorkflowState`, `ListWorkflowStates`.
     - Store unit tests in `router-proxy/internal/store/workflow_states_test.go`.

2. **HTTP Transport Layer**:
   - `router-proxy/internal/transport/workflow_states_http.go`:
     - Ensure `AllowedTools` is unmarshaled on POST/PUT and returned on GET/POST/PUT.
     - Return 400 Bad Request with descriptive error on invalid tool names.
     - Transport unit tests in `router-proxy/internal/transport/workflow_states_http_test.go`.

3. **Managed Runtime & Embedded Session Filtering**:
   - `router-proxy/internal/managed/embedded_session.go`:
     - In `runEmbeddedSessionTurnScoped`, inspect `saved.ActiveWorkflowStateID`.
     - Fetch active `WorkflowState` from store if set.
     - If `len(state.AllowedTools) > 0`:
       - Filter the constructed `tools []agent.AgentTool` slice so only allowed tools are included in `coding.NewSession`. Note: handle `context_reset` / `reset` aliasing (if either is allowed, both internal tool instances are allowed).
     - In tool admission / `BeforeToolCall` execution fence:
       - If tool is not allowed by the active workflow state, return `&agent.PolicyDenial{Reason: ...}`.

4. **Integration Tests (Mandatory Regression Prevention)**:
   - Integration test in `router-proxy/internal/store/workflow_state_binding_test.go` or `router-proxy/internal/managed/embedded_workflow_activation_test.go`:
     - Create workflow state with restricted tools (e.g. only `read`, `bash`).
     - Bind state to managed agent (`SetManagedAgentActiveWorkflowState`).
     - Verify active session tools reflect only allowed tools.
     - Verify invocation of a disallowed tool (e.g. `write` or `subagent`) is denied at runtime.
     - Transition agent to state with all tools allowed (empty `allowed_tools`) or clear active state, verify tools are restored and allowed.

5. **Web UI (React/TypeScript)**:
   - `web/router-ui-react/src/domain/workflow-states.ts`: Add `allowed_tools: string[]` to interface and request types.
   - `web/router-ui-react/src/components/WorkflowStatesPage.tsx`:
     - Add Tool Restrictions section in the state editor modal.
     - Available tools checklist with quick toggles ("All Tools (Default)" vs "Custom Selection").
     - Show tool restriction badge/summary in the state list card.
   - Component test in `web/router-ui-react/tests/component/workflow-states.test.tsx` verifying tool selection, rendering, and payload submission.

---

## Dev-Loop Phases

### Phase 1: Specification Review
- Run robot-review on `SPEC.md`.
- Run pr-validate on findings.
- Iterate until sign-off.

### Phase 2: Plan Review
- Run robot-review on `PLAN.md`.
- Run pr-validate on findings.
- Iterate until sign-off.

### Phase 3: TDD Implementation
- Subagent 1: DB Migration 093 & Store layer changes with unit tests.
- Subagent 2: HTTP transport endpoints with request validation & unit tests.
- Subagent 3: Runtime session filtering and tool admission fence in `embedded_session.go`.
- Subagent 4: Integration tests verifying end-to-end tool filtering, admission denial, and state switching.
- Subagent 5: Web UI TypeScript domain, WorkflowStatesPage component, and component tests.

### Phase 4: Verification & Coverage
- Run `go test` and `npm test` across all changed packages.
- Run `test-coverage` analysis.
- Run robot-review on full implementation diff.
- Run pr-validate.
- Final sign-off and completion.
