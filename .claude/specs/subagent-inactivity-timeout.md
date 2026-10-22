# Subagent Inactivity Timeout Specification

## 1. Problem Statement & Motivation

Subagents running inside the harness / router lifecycle can occasionally hang or stall indefinitely without making forward progress. This can occur due to hung external processes, deadlocked tool executions, unhandled LLM streaming stalls, or network drops during agent turns. Currently, stalled subagent sessions can stay in `running` status perpetually or until a broad hard maximum deadline triggers, consuming supervisor resources, holding lock contention, and preventing parent agents and supervisors from receiving timely failure signals.

To improve reliability and system responsiveness, we require an **inactivity timeout** mechanism: if a subagent produces no observable activity (no turns, tool invocations, or message streaming chunks) for 3 minutes, the session must be automatically aborted, transitioned to `failed`, and tagged with an explicit inactivity failure error.

---

## 2. Acceptance Criteria

1. **3-Minute Inactivity Threshold**:
   - The inactivity timeout threshold must be 3 minutes (180 seconds) of continuous lack of observable activity.
2. **Activity Tracking Across All Lifecycle Events**:
   - Activity timestamps must be updated whenever forward progress occurs:
     - Beginning of a new agent turn.
     - Emission/reception of streaming message chunks (tokens/deltas).
     - Initiation or completion of tool calls.
     - Outbound or inbound tool communication and progress notifications.
3. **Dual-Layer Protection (Runner Watchdog + Router Sweep)**:
   - **Layer 1 (Runner / Local Process Watchdog)**:
     - The runner managing the subagent instance tracks last activity and cancels the active execution context/session if inactive for $\ge 3$ minutes.
     - Directly aborts the executing subagent harness/process.
   - **Layer 2 (Router Sweep / Sweeper Task)**:
     - The router background sweep/reconciliation loop checks active subagent records against their `last_active_at` timestamp.
     - If a runner hangs, crashes silently, or fails to clean itself up within 3 minutes + grace period, the router sweeper marks the session as `failed`, records the termination reason, and cleans up runner tracking.
4. **Session Abortion & Terminal Status**:
   - When triggered, the subagent session must immediately be aborted and transitioned to status `failed`.
   - The failure reason / error message must be distinct and unambiguous:
     - e.g., `"Subagent aborted due to inactivity timeout (no activity for 3 minutes)"`.
5. **Supervisor / Parent Notification**:
   - The router and harness must publish/broadcast the terminal failure state so parent agents and supervisors receive immediate notification of the aborted subagent.

---

## 3. Architecture & Dual-Layer Implementation Plan

### Layer 1: Runner Watchdog
- Maintain an atomic `last_activity_time` (or tick channel / reset timer) within the runner execution context.
- Touch `last_activity_time` whenever:
  - An LLM chunk is received.
  - A tool execution starts, updates, or returns.
  - A turn starts or finishes.
- If `now - last_activity_time >= 3m`, fire cancellation on the runner context, terminate the process / harness turn, and post terminal error payload to the router.

### Layer 2: Router Sweeper
- Track `last_active_at` on active subagent session records in the store.
- Update `last_active_at` via heartbeat or agent turn/progress event updates.
- In the router sweeper loop (running periodically, e.g. every 15-30s):
  - Query active sessions where `status = 'running'` and `now - last_active_at >= 3m`.
  - Mark status as `failed` with error message `"Subagent aborted due to inactivity timeout (no activity for 3 minutes)"`.
  - Emit WebSocket lifecycle event (`subagent.failed` / `agent.updated`).

---

## 4. Testing Requirements

1. **Unit Testing**:
   - **Timer Reset on Activity**:
     - Verify that intermediate events (streaming token chunks, tool start/end) reset the inactivity timer so long-running active tasks do not get prematurely cancelled.
   - **Inactivity Trigger**:
     - Using mock clocks or simulated time advancement, verify that an agent with no turn/chunk/tool activity for 3 minutes triggers cancellation and marks the session as `failed`.
   - **Distinct Error Message**:
     - Assert that the error returned and stored matches the inactivity timeout description.
2. **Integration / Functional Testing**:
   - **Runner Watchdog Abort**:
     - Execute a test subagent whose tool or LLM stream halts/hangs. Verify the runner watchdog terminates the context at 3 minutes (simulated or configured test threshold) and transitions the subagent state to `failed`.
   - **Router Sweep Fallback**:
     - Simulate an unresponsive runner process where the runner fails to abort itself. Verify the router sweeper identifies the inactive subagent record past the threshold and marks it `failed` with the distinct inactivity error message.
