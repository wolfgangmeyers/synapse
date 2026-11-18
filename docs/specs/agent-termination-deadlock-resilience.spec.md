# Spec: Agent Termination Deadlock Resilience

- **ID**: `SPEC-AGENT-TERMINATION-DEADLOCK-001`
- **Status**: `Draft`
- **Domain**: `AgentManagement`
- **Target E2E Suite**: `tests/e2e/agent-termination-deadlock.spec.ts`

---

## 1. Intent & User Outcome
When an agent is terminated—via the Web UI trash button, the REST API (`DELETE /v1/managed-agents/:id`), or agent self-termination (`mecha_worker_terminate`)—the router must remain completely responsive. All other HTTP and WebSocket APIs (including `/v1/health`, status polling, message sending, timeline retrieval, and other agent operations) must continue answering immediately without hanging or deadlocking, regardless of whether the terminated agent was running an active tool, had active subagents, or required git worktree cleanup.

---

## 2. Core Invariants
1. `GET /v1/health` and all transport endpoints MUST NEVER hang or block behind an agent termination operation or store transaction.
2. Holding router read/write mutexes (`r.mu`) across database I/O or external network/process operations is STRICTLY PROHIBITED.
3. Holding agent lane mutexes (`lane.admission`, `lane.serial`) across projection publishing (`m.publishProjection()`), background bash retirement (`m.retireBackgroundBashAgent()`), or process reaping (`m.reapRestartGroup()`) is STRICTLY PROHIBITED.
4. All cleanup steps in `executeTerminationInLane` (including git worktree removal, RPC closure, and subagent cascading) MUST be strictly timeout-bounded and must never wait indefinitely.
5. In `writeManagedRaw`, `uiLive.publish` MUST NOT block the synchronous HTTP response completion if the publication gate or downstream WebSocket delivery is contended or slow.

---

## 3. User Scenarios (Behavioral Contract)

### Scenario: [SCN-TERM-001] Router health check and REST APIs respond during concurrent agent termination
- **User Goal**: Verify that external health checks and monitoring continue functioning normally while an agent is being terminated.
- **Given**:
  - The router is running and healthy.
  - A managed worker agent is running with an active background bash process or subagent.
- **When**:
  - A client sends `DELETE /v1/managed-agents/:id` to terminate the worker.
  - Concurrently, a health monitoring client polls `GET /v1/health`.
- **Then**:
  - `GET /v1/health` responds immediately with HTTP 200 and `{"status": "ok"}`.
  - The `DELETE` request responds with HTTP 202 Accepted.
  - Subsequent requests to `/v1/managed-agents` and `/v1/agents` continue responding without delay.

### Scenario: [SCN-TERM-002] Worker self-termination cleans up cleanly without locking router APIs
- **User Goal**: Ensure an embedded worker calling `mecha_worker_terminate` stops its session and triggers termination without deadlocking the harness.
- **Given**:
  - A managed embedded worker is running.
- **When**:
  - The worker invokes the `mecha_worker_terminate` tool.
- **Then**:
  - The tool execution signals turn termination.
  - Post-turn processing schedules asynchronous agent termination.
  - The agent is removed from the active agent registry, its assigned worktree is cleaned up (if requested), and no router mutexes remain locked.

### Scenario: [SCN-TERM-003] Subagent cascade cancellation and child cleanup during parent termination
- **User Goal**: Ensure terminating a parent agent cascades cancellation and stops/cleans up active subagents without hanging or wedging router APIs.
- **Given**:
  - A parent agent has spawned an active subagent in running state.
- **When**:
  - The parent agent is terminated via `DELETE /v1/managed-agents/:id`.
- **Then**:
  - Subagents are stopped and cleaned up (or cascade-deleted upon parent termination completion).
  - The router health check and REST endpoints remain fully responsive during and after termination.

---

## 4. Automated E2E Verification Matrix

| Scenario ID | Scenario Description | E2E Test Suite | Test Case Name |
| :--- | :--- | :--- | :--- |
| `SCN-TERM-001` | Router health check and REST APIs respond during concurrent agent termination | `tests/e2e/agent-termination-deadlock.spec.ts` | `router APIs and health check stay responsive during agent termination` |
| `SCN-TERM-002` | Worker self-termination cleans up cleanly without locking router APIs | `tests/e2e/agent-termination-deadlock.spec.ts` | `worker self-termination does not wedge router APIs` |
| `SCN-TERM-003` | Subagent cascade cancellation and child cleanup during parent termination | `tests/e2e/agent-termination-deadlock.spec.ts` | `SCN-TERM-003: subagent cascade cancellation does not wedge router transport` |
