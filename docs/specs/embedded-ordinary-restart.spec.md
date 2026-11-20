# Spec: Embedded Ordinary Restart

- **ID**: `SPEC-EMBEDDED-ORDINARY-RESTART-001`
- **Status**: `Active`
- **Domain**: `AgentManagement`
- **Target E2E Suite**: `tests/e2e/embedded-active-restart.spec.ts`, `tests/e2e/individual-restart.spec.ts`

---

## 1. Intent & User Outcome

Managed agents running in embedded runtimes (such as Pi or Claude Code process runners) may need to be restarted to refresh underlying runtime processes, clear stuck execution loops, or recover from transient provider timeouts without discarding existing session context.

Unlike a full Context Reset—which generates a brand-new session ID, increments the session epoch, and clears conversation history—an **Ordinary Restart** preserves the agent's session ID (`pi_session_id`), session epoch (`session_epoch`), and checked transcript history. The user observes prompt interruption if a turn was active, a clean transition to idle, honest error reporting on interrupted prompts without partial corruptions, rejection of stale tool executions and late evaluator verdicts, and seamless continuation of prompts and queued mail against the existing session context once restarted.

---

## 2. Core Invariants

1. **Session & History Preservation**:
   - Ordinary Restart MUST preserve `pi_session_id`, `session_epoch`, and all checked entries in the conversation transcript. It MUST NOT create a new session ID or increment `session_epoch`.
2. **Honest Turn Interruption**:
   - When an active turn (user prompt, mail turn, or goal loop) is interrupted by a restart, the in-flight model/tool execution MUST be aborted promptly.
   - The pending prompt HTTP call MUST fail honestly with an error indicating unavailability/cancellation (`PROMPT_UNAVAILABLE` or HTTP 409), NEVER reporting false success.
   - Partial assistant text, unacknowledged tool results, or incomplete output MUST NEVER be appended or committed as a successful assistant turn in durable history.
3. **Generation Fencing & Lane Barriers**:
   - Every accepted ordinary restart MUST increment `lane.claudeRestartGeneration` under `lane.selection.Lock()`.
   - In-flight turn callbacks, tool completions, and evaluator verdicts MUST check their turn's captured generation against `lane.claudeRestartGeneration.Load()` before attempting mutations; mismatched generations MUST fail closed and be discarded without mutating state.
   - Admission MUST close (`resetPending = true`, `resetRunning = true`) until the serving lane completely drains in-flight turn callbacks (`lane.serial.Lock()`) and finishes process shutdown.
   - Late tool executions and stale process grants (`stale process grant`) MUST NOT append tool results or mutate scratchpad/session data after restart.
4. **Concurrent & Idempotent Convergence**:
   - Replays with identical `X-Request-ID`, method, path, and body hash MUST return the cached HTTP 202 response without triggering a secondary interruption or restarting draining.
   - Conflicting reuse of `X-Request-ID` MUST fail with HTTP 409 (`IDEMPOTENCY_CONFLICT`).
   - Distinct requests arriving while the lane is draining MUST fail with `ManagedAgentNotReadyError` (HTTP 409/503) without disrupting the in-flight drain.
5. **Deterministic Router Crash Recovery**:
   - A router crash across any restart handoff boundary (post-reservation before drain, mid-drain, or during startup reconciliation) MUST NOT leave an agent permanently wedged, locked in draining, or unable to accept future prompts.
   - Startup reconciliation routines (`ReconcileManagedRecipientSources`, `ReconcileMessageTargets`, `ReconcileInterruptedSubagents`, `RecoverManagedDirectAttempts`) MUST be idempotent.
   - On reboot, volatile reset flags start clear (`false`), and a fresh owner lease is acquired on first turn admission.
6. **Peer Agent Isolation & Mail Reoffer**:
   - Restarting an embedded agent MUST NOT disrupt or cancel turns executing on independent peer agents.
   - Queued or pending mailbox messages for the restarting agent MUST be preserved and automatically reoffered upon restart completion.

---

## 3. User Scenarios (Behavioral Contract)

### Scenario: [EOR-001] Restart Idle Embedded Agent
- **User Goal**: Restart an idle embedded agent to refresh process state while retaining conversation context.
- **Given**:
  - An embedded agent `agent-1` is running, idle, and accepting prompts (`accepting_prompts = 1`).
  - The conversation contains prior user and assistant turns.
- **When**:
  - The user triggers Restart via the UI menu or sends `POST /v1/managed-agents/agent-1/restart` with header `X-Request-ID: req-1`.
- **Then**:
  - The router responds with HTTP 202 and records a terminal operation receipt in `managed_http_operations`.
  - The agent's `pi_session_id` and `session_epoch` remain identical to their pre-restart values.
  - The agent remains in `running` lifecycle state and returns to `idle`.
  - A subsequent prompt sent to `agent-1` executes against the preserved session context, with prior conversation history retained.

### Scenario: [EOR-002] Restart Active Turn with Honest Interruption
- **User Goal**: Interrupt a hung or long-running turn and recover the session cleanly.
- **Given**:
  - An embedded agent `agent-active` is processing a prompt or executing a tool.
  - The client is awaiting the HTTP response of `POST /v1/managed-agents/agent-active/prompt`.
- **When**:
  - An ordinary restart request is submitted for `agent-active`.
- **Then**:
  - The active turn's execution context is cancelled and the underlying engine process is stopped.
  - The waiting prompt request terminates with an honest error status (`PROMPT_UNAVAILABLE` / HTTP 409).
  - No incomplete or corrupted assistant message is saved as a successful turn in durable complete history.
  - The agent lane drains and reopens in `idle` state.
  - Subsequent prompt requests succeed against the preserved session without history corruption.

### Scenario: [EOR-003] Stale Incarnation Fencing (Late Tool Results, ACKs, Evaluator Verdicts)
- **User Goal**: Prevent late callbacks or obsolete evaluations from an interrupted turn from corrupting the fresh incarnation.
- **Given**:
  - An embedded agent has an active turn executing a tool or running a background goal evaluator.
  - Ordinary restart is accepted and `lane.claudeRestartGeneration` is incremented.
- **When**:
  - The delayed tool subprocess completes and attempts to return a tool result via the MCP bridge or callback.
  - An in-flight mail handler attempts to acknowledge (`AppendEmbeddedMailAndACK`) using the pre-restart owner token.
  - A pre-restart background goal evaluator returns a verdict.
- **Then**:
  - The late tool result is rejected under the generation barrier (`stale process grant` or generation mismatch).
  - The mail ACK fails closed (`ErrStaleEmbeddedFence`).
  - The stale evaluator verdict is discarded without updating goal counters or scheduling a continuation.
  - No stale data is committed to the agent's transcript or state.

### Scenario: [EOR-004] Concurrent and Idempotent Restart Convergence
- **User Goal**: Ensure network retries and concurrent restart clicks converge safely without double interruptions.
- **Given**:
  - An ordinary restart request `req-restart` has been accepted and is currently draining the agent lane.
- **When**:
  - A concurrent request arrives with the same `X-Request-ID: req-restart` and identical payload.
- **Then**:
  - The router returns the cached HTTP 202 response as an idempotent replay without interrupting the engine a second time.
- **When**:
  - A concurrent request arrives with `X-Request-ID: req-restart` but conflicting body parameters.
- **Then**:
  - The request is rejected with HTTP 409 (`IDEMPOTENCY_CONFLICT`).
- **When**:
  - A distinct restart request arrives with a different `X-Request-ID: req-distinct` while draining is active.
- **Then**:
  - The request is rejected with `ManagedAgentNotReadyError` (HTTP 409/503), protecting the active drain.

### Scenario: [EOR-005] Router Interruption During Handoff Boundary and Settlement
- **User Goal**: Ensure the agent recovers safely and does not get stuck in a wedged state if the router crashes during restart.
- **Given**:
  - An ordinary restart is admitted (operation committed to `managed_http_operations`), but the router crashes before drain completes or during recovery reconciliation.
- **When**:
  - The router starts up and runs its startup recovery sequence.
- **Then**:
  - In-memory lane barriers (`resetPending`, `resetRunning`) initialize clear (`false`).
  - Idempotent startup reconcilers (`ReconcileManagedRecipientSources`, `ReconcileMessageTargets`, `ReconcileInterruptedSubagents`) run to completion.
  - The agent is in `running` lifecycle state and acquires a fresh owner lease upon first admission.
  - The agent accepts new prompts and mail normally without operator intervention.

### Scenario: [EOR-006] Queued Mail Reoffer and Unaffected Peer Isolation
- **User Goal**: Deliver queued mail to restarted agents while peer workers continue running uninterrupted.
- **Given**:
  - Agent A and Agent B are active embedded workers.
  - Agent B is actively processing a long prompt.
  - Agent A has incoming mail queued in its inbox.
- **When**:
  - An ordinary restart is submitted and executed for Agent A.
- **Then**:
  - Agent B continues executing its prompt to completion without interruption or state change.
  - Agent A interrupts its active turn, drains its lane, and restarts.
  - Upon reopening admission, Agent A's queued mail is automatically reoffered and processed.
  - The completed mail turn is acknowledged under Agent A's current generation.

---

## 4. Automated E2E Verification Matrix

| Scenario ID | Scenario Description | E2E / Integration Test Suite | Test Case Name / Target Scope |
| :--- | :--- | :--- | :--- |
| `EOR-001` | Idle embedded restart preserves session | `tests/e2e/individual-restart.spec.ts` | `RAR-003 individual restart is accepted, recorded, reflected in UI, and leaves other agents untouched` |
| `EOR-002` | Active turn cancellation with honest interruption | `tests/e2e/embedded-active-restart.spec.ts` | `individual restart interrupts an active embedded agent and preserves usable prompt and mail` |
| `EOR-003` | Stale incarnation fencing (tool results, ACKs, verdicts) | `router-proxy/internal/managed/claude_restart_empty_callback_test.go`, `tests/e2e/claude-restart-evaluator-fence.spec.ts`, `tests/e2e/claude-code-recovery.spec.ts` | `TestRestartRejectsStaleForegroundEmptyCallback`, `restart cancels held evaluator and fences stale verdict`, `stale process grant rejection` |
| `EOR-004` | Concurrent and idempotent restart convergence | `router-proxy/internal/managed/embedded_restart_replay_race_test.go`, `tests/e2e/embedded-active-restart.spec.ts` | `TestEmbeddedRestartConcurrentAdmissionReplaysWhileDraining`, `concurrent HTTP requests exercise proxy/router/store replay` |
| `EOR-005` | Router interruption during handoff and settlement | `router-proxy/internal/store/managed_test.go`, `router-proxy/internal/managed/claude_code_orphan_test.go`, startup reconciliation unit/integration tests | `TestManagedReadyReconcilesReservedQueuedMessages`, `TestClaimedWakeCrashAfterFinalAppendReconcilesExactOrigin` |
| `EOR-006` | Queued mail reoffer and unaffected peer isolation | `tests/e2e/individual-restart.spec.ts`, `tests/e2e/claude-code-restart-lost-wake.spec.ts` | `RAR-003 restart of one agent while another is busy does not disturb the busy agent`, `successful Claude restart reoffers mail whose offer hit the reset gate` |
