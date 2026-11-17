# Spec: Claude Code Worker Startup Timeouts and Milestone Diagnostics

- **ID**: `SPEC-CLAUDE-STARTUP-TIMEOUTS-001`
- **Status**: `Draft` (acceptance pending; verified against `tests/e2e/claude-worker-startup-timeouts.spec.ts`)
- **Domain**: `AgentManagement`
- **Target E2E Suite**: `tests/e2e/claude-worker-startup-timeouts.spec.ts`

---

## 1. Intent & User Outcome

Under heavy host load, disk contention, or cold CLI caches, initializing a Claude Code managed worker can exceed short 5-second limits even when the host environment is healthy. Extending startup-phase timeouts by 3x (to 15 seconds for local preflight auth checks, stdin prompt writes, and control channel readiness probes) allows slow-starting workers to complete initialization successfully without premature failure.

When startup does fail, granular milestone step attribution isolates the exact failure phase among six sequential steps (`preflight_auth`, `mcp_grant_setup`, `process_spawn`, `gate_release`, `control_probe`, `initial_prompt_submit`) using standardized error wrapping and logging (`startup failed at step '<step>': <error>`). Diagnostic precision is achieved without leaking raw host paths, credentials, or process arguments into user-facing timeline entries or `managed_complete_history`. Simultaneously, process teardown, reap, and `Close()` deadlines remain strictly bounded (5s/10s) so failed or canceled processes never stall router lanes or shutdown.

---

## 2. Core Invariants

1. **3x Startup Timeout Extension**: Startup-phase operations—specifically the local preflight authentication check (`auth status --json`), stdin prompt writes, and the control channel readiness probe—must be bounded at 15 seconds (extended 3x from the legacy 5-second limit), allowing healthy but slow invocations to succeed.
2. **Bounded Teardown and Reap Invariance**: Process teardown, SIGTERM/SIGKILL escalation, orphan reaping, and `Close()` deadlines remain strictly bounded at 5 seconds (graceful stop) and 10 seconds (hard reap escalation). Startup extensions must never delay process cancellation or shutdown.
3. **Granular Milestone Step Attribution**: Every Claude worker startup sequence must track and attribute failures to exactly one of six distinct milestone steps:
   - `preflight_auth`: executing and evaluating local CLI auth preflight (`claude auth status --json`).
   - `mcp_grant_setup`: preparing process configuration, generating temporary authentication tokens, and establishing private MCP server credentials.
   - `process_spawn`: executing `cmd.Start()`, configuring stdio pipes, and capturing process group markers.
   - `gate_release`: synchronizing with the startup gate file descriptor to release child execution.
   - `control_probe`: dispatching the pre-turn control channel readiness probe (`{"subtype":"interrupt"}`) and awaiting acknowledgment.
   - `initial_prompt_submit`: rendering and writing the initial turn prompt to the process stdin pipe.
4. **Standard Error Wrapping and Log Formatting**: Internal error wrapping and diagnostic log messages for startup failures must strictly follow the format:
   ```
   startup failed at step '<step>': <error>
   ```
   where `<step>` is one of the six milestone identifiers above.
5. **History Category Allowlist and Privacy Sanitization Preservation**: User-visible conversation entries, turn error messages, and `managed_complete_history` records must only expose sanitized failure categories from the established allowlist (`auth`, `spawn`, `probe-died`, `pool-fenced`, `grant-failed`, `unknown`). Raw paths, filesystem locations, temporary tokens, or shell error strings must be stripped before persisting to history.

---

## 3. User Scenarios (Behavioral Contract)

### Scenario: [SCN-CST-001] Slow startup initializes successfully within 15s
- **User Goal**: Launch or prompt a Claude Code worker on a loaded host where CLI startup takes longer than 5 seconds but completes within 15 seconds.
- **Given**:
  - The router is running and configured for managed Claude Code workers.
  - The host Claude CLI auth status check or process initialization encounters temporary latency (e.g. taking 7 seconds).
- **When**:
  - The user launches a new Claude Code worker or submits a prompt to an existing idle worker.
- **Then**:
  - The preflight check and startup sequence do not abort at the legacy 5-second bound.
  - Startup completes successfully within the 15-second window.
  - The worker transitions to active/idle state and begins processing the turn without error.

### Scenario: [SCN-CST-002] Preflight auth timeout / failure attributes preflight_auth
- **User Goal**: Receive accurate diagnostics when host CLI authentication check hangs or fails.
- **Given**:
  - The host Claude CLI auth status check hangs past 15 seconds or returns a signed-out / invalid auth status.
- **When**:
  - A Claude Code worker startup attempt is initiated.
- **Then**:
  - Startup aborts at the 15-second preflight bound.
  - Internal error wrapping and logs attribute the failure to `startup failed at step 'preflight_auth': <error>`.
  - User-facing error history records the sanitized `auth` category directing the user to sign in on the host.
  - No worker process is leaked or left running.

### Scenario: [SCN-CST-003] Process spawn failure attributes process_spawn
- **User Goal**: Receive accurate diagnostics when the Claude CLI process binary cannot be spawned.
- **Given**:
  - Preflight auth succeeds, but the process binary cannot be executed (e.g. missing executable, execution permission failure, or OS spawn error).
- **When**:
  - The router attempts to spawn the worker child process.
- **Then**:
  - Startup aborts immediately upon spawn failure.
  - Internal error wrapping and logs attribute the failure to `startup failed at step 'process_spawn': <error>`.
  - User-facing error history records the sanitized `spawn` category.
  - Open stdio pipes and pending descriptors are cleaned up.

### Scenario: [SCN-CST-004] Gate release handshake failure attributes gate_release
- **User Goal**: Safely tear down and diagnose failures during the startup gate handshake.
- **Given**:
  - The process spawns, but writing the synchronization handshake to the gate file descriptor fails or produces a short write.
- **When**:
  - Process startup attempts gate release.
- **Then**:
  - Startup aborts at the gate release step.
  - Internal error wrapping and logs attribute the failure to `startup failed at step 'gate_release': <error>`.
  - The spawned process group is killed and confirmed reaped within bounded teardown time (5s/10s).
  - Orphan tracking records are cleaned up without leaking unreaped processes.

### Scenario: [SCN-CST-005] Control channel readiness probe timeout attributes control_probe
- **User Goal**: Detect and diagnose when a spawned process fails to respond to control protocol readiness probing.
- **Given**:
  - The process spawns and passes gate release, but the pre-turn control channel readiness probe does not respond within 15 seconds or exits prematurely during probe.
- **When**:
  - The runtime probes the control channel before submitting initial input.
- **Then**:
  - Startup aborts after the 15-second control probe timeout.
  - Internal error wrapping and logs attribute the failure to `startup failed at step 'control_probe': <error>`.
  - User-facing error history records the sanitized `probe-died` category.
  - The process is terminated and reaped within bounded teardown time (5s/10s).

### Scenario: [SCN-CST-006] Initial prompt submission failure attributes initial_prompt_submit
- **User Goal**: Detect and diagnose when writing the initial prompt to the process stdin pipe fails.
- **Given**:
  - The process is healthy through control probe, but writing the initial rendered prompt to stdin times out after 15 seconds or encounters an immediate broken pipe.
- **When**:
  - The runtime submits the initial turn prompt.
- **Then**:
  - Startup aborts at the initial prompt submission step.
  - Internal error wrapping and logs attribute the failure to `startup failed at step 'initial_prompt_submit': <error>`.
  - The turn is settled cleanly with input hold safety rules applied.
  - The process is torn down and reaped within bounded limits.

### Scenario: [SCN-CST-007] Privacy and sanitization are preserved across milestone errors
- **User Goal**: Ensure error diagnostics never leak private host paths, credentials, or environment data into persisted history or API responses.
- **Given**:
  - A startup failure occurs at any of the six milestone steps with an underlying OS error containing raw paths (e.g. `/home/wolfgang/...`), temporary token values, or environment variables.
- **When**:
  - The router persists the failure into `managed_complete_history` and returns the error response to the client.
- **Then**:
  - Detailed milestone step attribution is captured in internal error logs (`startup failed at step '<step>': <error>`).
  - The persisted `managed_complete_history` and user-facing notifications contain only allowlisted category codes (`auth`, `spawn`, `probe-died`, `pool-fenced`, `grant-failed`, `unknown`) and generic remediation text.
  - No raw filesystem paths, private tokens, or unscrubbed command lines appear in client-accessible history.

---

## 4. Automated E2E Verification Matrix

| Scenario ID | Scenario Description | E2E Test Suite | Test Case Name |
| :--- | :--- | :--- | :--- |
| `SCN-CST-001` | Slow startup initializes successfully within 15s | `tests/e2e/claude-worker-startup-timeouts.spec.ts` | `slow startup completes successfully within 15s window` |
| `SCN-CST-002` | Preflight auth timeout / failure attributes preflight_auth | `tests/e2e/claude-worker-startup-timeouts.spec.ts` | `preflight auth failure attributes preflight_auth milestone` |
| `SCN-CST-003` | Process spawn failure attributes process_spawn | `tests/e2e/claude-worker-startup-timeouts.spec.ts` | `process spawn failure attributes process_spawn milestone` |
| `SCN-CST-004` | Gate release handshake failure attributes gate_release | `tests/e2e/claude-worker-startup-timeouts.spec.ts` | `gate release failure attributes gate_release milestone` |
| `SCN-CST-005` | Control channel readiness probe timeout attributes control_probe | `tests/e2e/claude-worker-startup-timeouts.spec.ts` | `control channel probe timeout attributes control_probe milestone` |
| `SCN-CST-006` | Initial prompt submission failure attributes initial_prompt_submit | `tests/e2e/claude-worker-startup-timeouts.spec.ts` | `initial prompt submission failure attributes initial_prompt_submit milestone` |
| `SCN-CST-007` | Privacy and sanitization are preserved across milestone errors | `tests/e2e/claude-worker-startup-timeouts.spec.ts` | `milestone diagnostics preserve privacy and sanitization in history` |
