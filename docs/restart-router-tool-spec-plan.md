# Specification & Plan: `mecha_restart_router` Tool for Embedded Agents

## Problem Statement

Managed embedded agent sessions (root and worker agents running inside the Go Mailbox router) may modify backend Go code, apply database migrations, rebuild binaries, or require a process restart of the mailbox router daemon to apply changes.

Embedded agents currently lack a safe programmatic mechanism to trigger a router restart:
1. Executing `bash` commands like `pkill mailbox-router` or `kill <pid>` abruptly kills the process mid-turn, severing in-flight database transactions, dropping uncommitted agent turns, and orphaning active subagents without cleanup.
2. Direct process exits or raw `SIGTERM` signals fail to coordinate with the supervisor wrapper `tools/run_router.sh`. Specifically, exit code 0, 130, or 143 halts the supervisor loop permanently, whereas `run_router.sh` expects exit code 75 (`EX_TEMPFAIL`) and `ROUTER_EXIT_FILE` updates to initiate an automatic restart loop.
3. Autonomous LLMs might restart the router while sibling workers or subagents are actively executing long-running tasks.

We need a dedicated embedded tool—`mecha_restart_router`—with a rejection-first safeguard state machine, active worker/subagent detection, atomic exit coordination, and clean transcript persistence before process exit.

---

## Detailed Requirements

### 1. Role Availability
- **Eligible roles**: Embedded agents with `role == "root"` or `role == "worker"`.
- **Ineligible**: Subagents (`EmbeddedSubagentTools` and `EmbeddedSubagentPublicTools`), legacy Pi extensions, and external tmux workers must **never** receive the `mecha_restart_router` tool declaration.

### 2. Safeguard State Machine
The tool protects concurrent workloads using a lane-scoped rejection state machine with a 60-second TTL:

1. **No concurrent active processes**:
   - If no other active workers or subagents are detected, the restart succeeds immediately. No prior rejection or `force` flag is required.
2. **Concurrent active processes present (Initial Attempt)**:
   - If other workers (active turns or goals) or subagents (queued or running) are active, the call **fails with a rejection**.
   - The manager records `restartRejectedAt = m.clock.Now()` and conflict details on the caller's lane with a 60-second TTL.
   - The rejection message informs the agent of the conflict and provides instructions.
3. **Attempt with `force=true` WITHOUT prior valid rejection**:
   - The call **fails immediately**. The error explicitly states that `force=true` cannot be used until the request has been rejected by the first step.
   - This prevents autonomous LLMs from guessing or pre-emptively setting `force=true` on their first try.
4. **Attempt with `force=true` WITH prior valid rejection within 60s**:
   - The manager validates that a prior rejection occurred on this lane within the last 60 seconds (`m.clock.Now().Sub(lane.restartRejectedAt) <= 60*time.Second`).
   - The restart proceeds even though other workers or subagents are active.
   - Lane rejection state is cleared upon acceptance.

### 3. Rejection Message Guidance
When rejected due to active sibling workers or subagents, the tool result message must:
- List the specific active conflicts (sibling workers with active turns/goals, active/queued subagents).
- Explicitly instruct the worker to either:
  1. Try again later, suggesting setting a reminder via `mecha_reminder(action="create", delay_minutes=...)` to retry once concurrent work settles.
  2. Re-invoke with `force=true` if the need is critical enough to justify interrupting active workers.

### 4. Detection Logic
Before approving a restart, the manager inspects:
- **Active Subagents**: Both in-memory (`m.subagentsActive`) and durable store (`m.store.ManagedSubagents`) across all agents for subagents in `"queued"` or `"running"` state.
- **Active Sibling Workers**: Sibling embedded agents (`agentID != callerAgentID`) with in-flight turns (checked via `m.activeEmbeddedSessions`) or active goals/continuation keys (checked via `m.store.GetActiveGoalContinuations` or lane goal status).

### 5. Process Exit Protocol
- **Exit Code**: Exit with code 75 (`EX_TEMPFAIL`). Do not use exit code 0 or raw `SIGTERM`.
- **Exit File Handshake**: If `ROUTER_EXIT_FILE` environment variable is set, write `"75\n"` to that path before exiting. `tools/run_router.sh` reads this file to determine the exit code and re-runs the router.
- **Graceful Flush**: Return a success tool result to the agent session first. The turn transcript, tool call, and tool result must be committed to SQLite before process termination.
- An asynchronous goroutine triggers the exit after a short delay (e.g., 150-250ms), giving SQLite transactions time to settle.

### 6. Clock & Concurrency Guarantees
- **Clock**: All timestamps, TTL checks, and durations must use `m.clock.Now()` from the Manager's deterministic clock.
- **Manager Restarting State**: An atomic boolean flag `restarting atomic.Bool` on `Manager` prevents multiple restart sequences from running concurrently.
- **Single Exit Execution**: A `sync.Once` guard guarantees the exit callback or termination goroutine executes exactly once across the lifetime of the process.

---

## State Machine Diagram

```
                 [ mecha_restart_router(reason, force) ]
                                   |
                     +-------------v-------------+
                     | Active sibling workers or |
                     | active subagents present? |
                     +-------------+-------------+
                                   |
                        No         |        Yes
               +-------------------+        +-------------------+
               |                                                |
               v                                                v
      +-----------------+                              +-----------------+
      | Return SUCCESS  |                              |  force == true? |
      | Trigger Exit 75 |                              +--------+--------+
      +-----------------+                                       |
                                                       No       |       Yes
                                            +-------------------+       +-------------------+
                                            |                                               |
                                            v                                               v
                          +-----------------------------------+           +-----------------------------------+
                          | Set lane.restartRejectedAt = now  |           | Prior valid rejection on lane     |
                          | Return REJECTED:                  |           | within 60s (m.clock)?             |
                          | - Conflict details                |           +-----------------+-----------------+
                          | - Instruct mecha_reminder retry   |                             |
                          | - Or force=true if critical       |                    Yes      |       No
                          +-----------------------------------+            +----------------+       +----------------+
                                                                           |                                         |
                                                                           v                                         v
                                                          +---------------------------------+       +---------------------------------+
                                                          | Clear lane rejection state      |       | Return REJECTED:                |
                                                          | Return SUCCESS                  |       | "force=true cannot be used      |
                                                          | Trigger Exit 75                 |       | until rejected by first step"   |
                                                          +---------------------------------+       +---------------------------------+
```

---

## Tool Specification

### Declaration

- **Name**: `mecha_restart_router`
- **Description**: "Request a controlled restart of the mailbox router process. Automatically restarts if no other workers or subagents are active. If other workers or subagents are active, requires a two-step rejection safeguard before force can be used."
- **Parameters**:
```json
{
  "type": "object",
  "properties": {
    "reason": {
      "type": "string",
      "description": "Required rationale for restarting the router (e.g., applied migration, rebuilt binary)."
    },
    "force": {
      "type": "boolean",
      "description": "Force restart even if concurrent workers or active subagents are running. Cannot be set on the first attempt; only valid after an initial rejection within 60 seconds."
    }
  },
  "required": ["reason"],
  "additionalProperties": false
}
```

### Scenario Invocations & Messages

#### Scenario A: Clean Restart (No Sibling Workers, No Subagents)
- Invocation: `{"reason": "rebuilt router binary"}`
- Checks: Sibling active turns = 0, active subagents = 0.
- Result:
```
[RESTART SCHEDULED]
Router restart initiated (reason: "rebuilt router binary").
Process exiting with code 75 for runner supervisor reload in 200ms.
```

#### Scenario B: Active Conflicts Present, First Attempt (`force` omitted or `false`)
- Invocation: `{"reason": "rebuilt router binary"}`
- Checks: Sibling worker `worker-1` has an active goal; subagent `subagent-102` is `running`.
- State Update: Caller lane records `restartRejectedAt = m.clock.Now()`, `restartConflictReason = "..."`.
- Result:
```
[RESTART REJECTED - ACTIVE PROCESSES RUNNING]
Cannot restart router while other workers or subagents are active:
- Sibling worker: worker-1 (active goal)
- Subagent: subagent-102 (parent: worker-1, status: running)

Recommended next steps:
1. Try again later. You can create a reminder to retry: mecha_reminder(action="create", delay_minutes=2, reason="Retry router restart")
2. If your change is critical enough to justify interrupting these active processes, re-run with force=true within 60 seconds:
   mecha_restart_router(reason="rebuilt router binary", force=true)
```

#### Scenario C: First Attempt with `force=true` (No Prior Rejection)
- Invocation: `{"reason": "emergency fix", "force": true}`
- Checks: Lane has no `restartRejectedAt` record within 60s.
- Result:
```
[RESTART REJECTED - INVALID FORCE USAGE]
The force flag cannot be used until the request has been evaluated and rejected by the first step.
Please call mecha_restart_router without force first so active system state can be checked.
```

#### Scenario D: Second Attempt with `force=true` (Valid Prior Rejection within 60s)
- Invocation: `{"reason": "emergency fix", "force": true}`
- Checks: Lane `restartRejectedAt` is 15s old (`<= 60s`).
- State Update: Lane rejection state cleared.
- Result:
```
[RESTART SCHEDULED - FORCED]
Router restart initiated with force override (reason: "emergency fix").
Active workers and subagents will be interrupted.
Process exiting with code 75 for runner supervisor reload in 200ms.
```

#### Scenario E: Attempt with `force=true` After Rejection Expired (> 60s)
- Invocation: `{"reason": "rebuilt router", "force": true}`
- Checks: Lane `restartRejectedAt` is 75s old (`> 60s`).
- Result:
```
[RESTART REJECTED - PRIOR ATTEMPT EXPIRED]
Prior rejection occurred more than 60 seconds ago. Active system conditions may have changed.
Call mecha_restart_router without force to evaluate current active processes.
```

---

## Implementation Details

### 1. Lane State
In `router-proxy/internal/managed/supervisor.go` (`managedAgentLane` struct):
```go
type managedAgentLane struct {
    // ... existing fields ...
    restartRejectedAt     time.Time
    restartConflictDetail string
}
```

### 2. Manager Fields & Exit Coordination
In `router-proxy/internal/managed/manager.go`:
```go
type Manager struct {
    // ... existing fields ...
    restarting          atomic.Bool
    restartOnce         sync.Once
    restartExitTrigger  func(code int) // test hook; defaults to production exit handler
}
```

Production exit implementation:
```go
func defaultProcessExitTrigger(code int) {
    if exitFile := os.Getenv("ROUTER_EXIT_FILE"); exitFile != "" {
        _ = os.WriteFile(exitFile, []byte(strconv.Itoa(code)+"\n"), 0o600)
    }
    os.Exit(code)
}
```

### 3. Active Process Inspection
In `router-proxy/internal/managed/embedded_restart_router.go`:
- `collectActiveConflicts(callerAgentID string) ([]string, error)`
  - Iterates `m.subagentsActive` and `m.store.ManagedSubagents` for status `"queued"` or `"running"`.
  - Iterates `m.activeEmbeddedSessions` and checks sibling agent active turns and active goals.
  - Returns formatted descriptions of all conflicting active entities.

### 4. Tool Execution Handler
- Extract `reason` and optional `force` (default `false`).
- Fetch conflicts via `collectActiveConflicts(owner.agentID)`.
- If len(conflicts) == 0:
  - Trigger restart and return success message.
- If len(conflicts) > 0 and `!force`:
  - Record `lane.restartRejectedAt = m.clock.Now()`.
  - Return rejection with conflict list, `mecha_reminder` suggestion, and 60s `force=true` option.
- If len(conflicts) > 0 and `force`:
  - Check `lane.restartRejectedAt`. If zero or `m.clock.Now().Sub(lane.restartRejectedAt) > 60*time.Second`, reject with invalid/expired force usage message.
  - If valid within 60s: clear lane state, trigger restart, return forced success message.

### 5. Asynchronous Delayed Trigger
```go
func (m *Manager) scheduleRouterRestart(reason string) {
    if !m.restarting.CompareAndSwap(false, true) {
        return
    }
    go func() {
        // Sleep brief duration (real or simulated) to allow SQLite turn commit
        m.clock.Sleep(200 * time.Millisecond)
        m.restartOnce.Do(func() {
            trigger := m.restartExitTrigger
            if trigger == nil {
                trigger = defaultProcessExitTrigger
            }
            trigger(75) // EX_TEMPFAIL
        })
    }()
}
```

---

## Behavioral Functional Testing Standard

In accordance with the repository testing policy, we require behavioral functional tests that exercise the full embedded agent session lifecycle hooked together:
- Run a full router manager with deterministic clock, store, and a mock LLM.
- The mock LLM executes a multi-turn or tool-calling sequence calling `mecha_restart_router`.
- Verify that:
  1. The tool call and its tool result are durably written to the session transcript in SQLite.
  2. The turn reaches its natural settled completion state.
  3. The restart trigger callback fires with exit code 75.
  4. If `ROUTER_EXIT_FILE` is configured in the environment, verify `"75\n"` is written to the file.
- Automated tests mock dependencies at the process/clock boundary and never invoke external LLM APIs.

---

## Test Suite Plan

File: `router-proxy/internal/managed/embedded_restart_router_test.go`

1. **`TestRestartRouter_NoConflicts_SucceedsImmediately`**:
   - Zero active subagents, zero sibling workers.
   - Call tool without force.
   - Assert immediate success response.
   - Assert exit trigger fired with code 75.

2. **`TestRestartRouter_WithConflicts_RejectsAndRecordsLaneTimestamp`**:
   - Seed active subagent in memory/store.
   - Call tool without force.
   - Assert tool result contains conflict details, `mecha_reminder` retry instruction, and force instruction.
   - Assert exit trigger was NOT fired.
   - Assert lane `restartRejectedAt` equals `clock.Now()`.

3. **`TestRestartRouter_ForceWithoutPriorRejection_Fails`**:
   - Seed active sibling worker.
   - Call tool with `force=true` on first attempt.
   - Assert rejected with error stating `force` cannot be used until rejected by the first step.
   - Assert exit trigger was NOT fired.

4. **`TestRestartRouter_ForceWithPriorRejectionWithin60s_Succeeds`**:
   - Seed active subagent.
   - Step 1: Call without force -> receives rejection, sets `restartRejectedAt`.
   - Clock advances 20 seconds (`< 60s`).
   - Step 2: Call with `force=true`.
   - Assert success response and exit trigger fired with code 75.

5. **`TestRestartRouter_ForceWithExpiredRejection_Fails`**:
   - Step 1: Call without force -> receives rejection.
   - Clock advances 65 seconds (`> 60s`).
   - Step 2: Call with `force=true`.
   - Assert rejected with expired rejection message.
   - Assert exit trigger was NOT fired.

6. **`TestRestartRouter_RoleExclusion`**:
   - Verify `mecha_restart_router` is present in tool definitions for `"root"` and `"worker"`.
   - Verify `mecha_restart_router` is absent from `EmbeddedSubagentTools` and `EmbeddedSubagentPublicTools`.

7. **`TestRestartRouter_BehavioralLifecycle_TranscriptCommittedBeforeExit`**:
   - Run embedded session turn with mock LLM emitting `mecha_restart_router`.
   - Capture mock restart trigger invocation via channel.
   - Verify turn completes cleanly.
   - Read SQLite database transcript: verify tool call and tool result records are persisted in durable store before exit trigger callback completes.
   - Verify `ROUTER_EXIT_FILE` contents match `"75\n"`.

8. **`TestRestartRouter_Concurrency_AtomicSingleExit`**:
   - Simultaneously invoke restart triggers from multiple goroutines.
   - Verify `restartOnce` and `restarting` atomic flag guarantee exactly one exit trigger execution.

---

## Acceptance Criteria

1. **Process Exit Protocol**: Uses exit code 75 (`EX_TEMPFAIL`) and writes `"75\n"` to `ROUTER_EXIT_FILE` if set. Never exits 0 or sends raw `SIGTERM`.
2. **State Machine Safeguard**:
   - Immediate success if no concurrent active entities.
   - First call with conflicts rejected; records rejection timestamp on caller's lane with 60s TTL.
   - First call with `force=true` rejected with clear guidance.
   - Second call with `force=true` within 60s succeeds despite active conflicts.
3. **Rejection Guidance**: Message lists conflict details and instructs agent to either wait/retry via `mecha_reminder` or use `force=true` if critical.
4. **Role Isolation**: Tool exposed only to `root` and `worker` embedded agents; subagents never receive it.
5. **Detection Coverage**: Reliably detects queued/running subagents and sibling workers with active turns or goals.
6. **Clock & Concurrency**: Controlled by `m.clock.Now()`, `atomic.Bool`, and `sync.Once`.
7. **End-to-End Behavioral Durability**: Full session lifecycle test confirms transcript persistence to SQLite prior to process exit trigger.
