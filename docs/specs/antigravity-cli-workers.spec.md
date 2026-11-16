# Spec: Antigravity CLI Workers

- **ID**: `SPEC-LIFECYCLE-ANTIGRAVITY-001`
- **Status**: `Draft`
- **Domain**: `AgentManagement`
- **Target E2E Suite**: `tests/e2e/antigravity-cli-workers.spec.ts`

---

## 1. Intent & User Outcome

Users can launch and operate autonomous router-managed workers backed by the Google Antigravity CLI (`agy`) directly from the Mecha web UI and API. An Antigravity worker appears identically to other router-managed workers in the sidebar, timeline, context bar, and conversation header. It uses the host's existing `agy` installation and user sign-in session to execute turns without requiring external API keys.

The user experiences full feature parity with existing embedded workers:
- Real-time conversation timeline rendering thoughts, tool executions (arguments and results), and final syntheses.
- Responsive mid-turn steering (delivering user prompts and mail directly into active execution).
- Full router tool ecosystem support via a loopback Model Context Protocol (MCP) bridge (scratchpad, goal tracking and snooze, async mailbox messaging, progress, reminders, and context reset).
- Safe lifecycle management: graceful interrupts, turn cancellation, process crash recovery, and orphaned process reaping.
- Model selection across Antigravity models (e.g., `gemini-2.5-pro`, `gemini-2.5-flash`, `gemini-3.8-flash`, etc.) without cross-engine contamination.
- Clean isolation with **zero global host configuration mutations**: MCP bridge configuration is passed strictly via ephemeral settings files.

Under the hood, the system parameterizes the battle-tested process lifecycle, MCP loopback bridge (`claude_code_mcp.go`), and orphan tracking logic via a targeted `CLIAdapter` interface. In accordance with GitHub Issue #69, stream-json mode is selected over ACP (Agent Client Protocol) as the primary execution engine to maximize code reuse with Claude Code while preserving an adapter seam for future ACP support.

---

## 2. Core Invariants

1. **Process Isolation & Lifecycle Scope**:
   - Each foreground execution MUST start its own isolated process group (`setpgid`).
   - Stoppage, interrupt, or error cleanup MUST terminate the entire process group (SIGTERM followed by SIGKILL if unconfirmed within 2 seconds).
   - Unconfirmed process exit MUST fence the worker lane to prevent runaway or overlapping processes.
2. **Crash & Orphan Safety**:
   - Every spawned `agy` process MUST be recorded in the provider-specific orphan store (`.mecha/sessions/antigravity-orphans/`) prior to executing turns or writing to stdin.
   - Upon router restart or crash recovery, any surviving orphan processes MUST be discovered and reaped before admitting new turns.
3. **Exact Echo & Reservation Guarantee (Zero Input Loss)**:
   - Every input prompt (composer message, mail steer, system notice) MUST be reserved with a unique identifier prior to stdin submission.
   - A stdin write NEVER constitutes turn consumption. Consumption and mail acknowledgement MUST ONLY occur when the exact echoed user message is observed and durably committed to persistence.
   - If an input write started but cannot be verified by an exact echo prior to process exit or error, the input MUST remain held and fenced from premature redelivery.
4. **Strict Model Engine Boundaries**:
   - The `antigravity` provider represents local CLI execution via `agy`, not cloud API keys or OAuth helper tokens.
   - Existing agents CANNOT switch engines between Pi, Claude Code, and Antigravity; engine switching remains rejected. Model changes within the `antigravity` provider catalog are permitted.
5. **Preflight & Authentication Verification**:
   - Prior to launching an agent or acquiring an execution process, the runtime MUST run a non-interactive, bounded (<= 5s) preflight check (`agy --version` / auth verification).
   - If `agy` is not installed, not on PATH, or signed out, the launch/acquisition MUST fail immediately with clear remediation instructions ("install agy and sign in on host").
6. **Router Tool Authority & Replay Protection**:
   - Router tools exposed via the loopback MCP bridge MUST require an active, valid bearer token bound strictly to the calling agent's process incarnation.
   - Stale processes, stopped incarnations, or cross-agent calls MUST be rejected (HTTP 401/403).
   - Idempotent tool deduplication MUST ensure no double execution of side effects (`mecha_mailbox_send`, `scratchpad`, etc.) within the same execution scope.
7. **Zero Host State Mutation**:
   - CLI execution and MCP integration MUST NOT mutate or overwrite host-global configuration files (such as `~/.gemini/settings.json` or `~/.antigravity/settings.json`).
   - MCP endpoints and authentication tokens MUST be supplied strictly via ephemeral settings files (`--settings <temp_file>`) scoped to the active incarnation lifecycle.
8. **Protocol Selection & Seam (Issue #69)**:
   - The primary execution driver MUST use `stream-json` over stdin/stdout to maximize code reuse with existing line-scanner, echo reservation, and steering infrastructure.
   - The protocol driver MUST be cleanly isolated behind the `CLIAdapter` interface so that an ACP (Agent Client Protocol) driver can be plugged in without refactoring database or router lifecycle layers.

---

## 3. User Scenarios (Behavioral Contract)

### Happy Path Scenarios

#### Scenario: [SCN-AGY-001] Launching an Antigravity Worker
- **User Goal**: Launch an autonomous worker powered by the local Antigravity CLI.
- **Given**:
  - The router is running and `agy` is installed and authenticated on the host.
  - The user opens the "New Agent" dialog in the Web UI.
- **When**:
  - The user selects an Antigravity model (e.g., `antigravity/gemini-2.5-pro` or `antigravity/gemini-2.5-flash`).
  - The user provides an agent name (e.g., `agy-researcher`) and an initial prompt instruction.
  - The user clicks "Launch Agent".
- **Then**:
  - The router runs preflight checks on `agy` successfully within 5 seconds.
  - The agent is created in `managed_agents` with provider `antigravity`.
  - The agent appears in the sidebar with an `antigravity` badge.
  - An ephemeral settings file with loopback MCP server credentials is created with 0600 permissions.
  - An isolated `agy` CLI process is started with `--input-format stream-json --output-format stream-json --dangerously-skip-permissions --settings <ephemeral_settings>`.
  - The initial instruction executes as the first turn, streaming thoughts and tool calls to the timeline.

#### Scenario: [SCN-AGY-002] Multi-Turn Conversation & Timeline Streaming
- **User Goal**: Chat with an Antigravity worker and observe incremental progress.
- **Given**:
  - Agent `agy-researcher` is idle after completing its initial turn.
  - The conversation view for `agy-researcher` is active.
- **When**:
  - The user types "Find the main entry point of this repository and explain its structure" into the composer and submits.
- **Then**:
  - The composer clears, and a user bubble appears in the conversation feed.
  - The agent status shifts to "active".
  - A new execution process is acquired and the prompt is submitted via stream-json.
  - The timeline renders real-time thinking activity and tool executions (e.g., file reads, globbing).
  - Upon turn completion, the final synthesized response renders in the conversation feed.
  - The agent status returns to "idle" and the context window usage metric updates.

#### Scenario: [SCN-AGY-003] Mid-Turn Steering via Mail or Direct Prompt
- **User Goal**: Direct an agent while it is actively executing tools without waiting for turn completion.
- **Given**:
  - Agent `agy-researcher` is in the middle of a lengthy multi-step tool sequence.
- **When**:
  - Another agent sends a mail message or the user submits a follow-up steering prompt in the composer.
- **Then**:
  - The incoming input is reserved and written to the active process's stdin.
  - The running `agy` process receives the steer message and emits an echoed user event.
  - The router correlates the echo with the pending reservation, commits the turn entry, and acknowledges the steer.
  - The agent incorporates the steering guidance into its current execution flow before producing the final response.

#### Scenario: [SCN-AGY-004] Router Tool Execution via Loopback MCP Bridge
- **User Goal**: Allow the Antigravity worker to use Mecha router tools (mailbox, scratchpad, goals).
- **Given**:
  - An Antigravity worker is running a task requiring persistent memory or inter-agent messaging.
- **When**:
  - The agent invokes `scratchpad` to record a key finding or `mecha_mailbox_send` to notify a supervisor.
- **Then**:
  - The `agy` process routes the tool call through the loopback HTTP MCP server using the bearer token from its ephemeral settings file.
  - The MCP server (`claude_code_mcp.go`) verifies the grant, executes the router tool against SQLite persistence, and returns the result.
  - The timeline reflects the tool invocation and result cleanly without prefix clutter.

---

### Non-Happy / Error / Edge Path Scenarios

#### Scenario: [SCN-AGY-005] Launch Refusal on Missing or Unauthenticated CLI
- **User Goal**: Receive clear actionable error feedback when `agy` is not ready.
- **Given**:
  - `agy` is not installed on the system PATH or the local user session is signed out.
- **When**:
  - The user attempts to launch an agent with an `antigravity` model.
- **Then**:
  - The preflight validation fails within 5 seconds.
  - Agent creation is rejected with HTTP 400/422.
  - The UI displays an error banner: "Antigravity CLI unavailable or signed out; run agy to sign in on the host".
  - No orphaned processes, ephemeral settings files, or persistent database records remain.

#### Scenario: [SCN-AGY-006] Interrupting an Active Turn
- **User Goal**: Stop an agent that is executing an unwanted or runaway operation.
- **Given**:
  - An Antigravity worker is actively running a tool loop.
- **When**:
  - The user clicks the "Stop / Interrupt" button in the conversation header.
- **Then**:
  - The router sends an interrupt signal or control request to the process.
  - In-flight pending inputs are cancelled.
  - If the process does not terminate within the grace window, SIGTERM and then SIGKILL are dispatched to its process group.
  - Partial uncommitted outputs are cleanly flushed or discarded according to policy.
  - The conversation feed marks the turn as interrupted/aborted.
  - The agent transitions cleanly to "idle".

#### Scenario: [SCN-AGY-007] Crash Recovery and Startup Orphan Reaping
- **User Goal**: Ensure unexpected router restarts or CLI crashes do not leave background zombie processes.
- **Given**:
  - An `agy` process was running when the router was forcefully stopped (SIGKILL or power loss).
  - An orphan marker file exists in `.mecha/sessions/antigravity-orphans/`.
- **When**:
  - The router restarts and initializes the managed agent subsystem.
- **Then**:
  - Startup orphan recovery scans the `.mecha/sessions/antigravity-orphans/` directory.
  - The surviving `agy` process group is positively identified by its PID and start marker.
  - The orphan process group is killed and reaped.
  - The orphan marker file is cleanly unlinked.
  - Any abandoned ephemeral settings files are removed.
  - The agent's lifecycle state in SQLite is reconciled to "stopped" or prepared for fresh turn entry.

#### Scenario: [SCN-AGY-008] Prevention of Cross-Engine Model Switching
- **User Goal**: Prevent accidental corruption of agent state by switching between incompatible CLI engines.
- **Given**:
  - An agent is running on `antigravity/gemini-2.5-pro`.
- **When**:
  - The user opens model settings and attempts to switch the model to `claude-code/sonnet` or `google-vertex/gemini-2.5-pro`.
- **Then**:
  - The ModelPicker filters choices to matching engine models (`antigravity/*`).
  - If forced via API, the router rejects the update with `unsupported_model_control` or `model_switch_rejected`.

---

## 4. Automated E2E Verification Matrix

| Scenario ID | Scenario Description | E2E Test Suite | Test Case Name |
| :--- | :--- | :--- | :--- |
| `SCN-AGY-001` | Launching an Antigravity Worker | `tests/e2e/antigravity-cli-workers.spec.ts` | `launches antigravity worker and executes initial instruction` |
| `SCN-AGY-002` | Multi-turn conversation and timeline streaming | `tests/e2e/antigravity-cli-workers.spec.ts` | `streams thoughts and tool calls across multi-turn conversation` |
| `SCN-AGY-003` | Mid-turn steering via mail or prompt | `tests/e2e/antigravity-cli-workers.spec.ts` | `steers active turn mid-execution and acknowledges upon exact echo` |
| `SCN-AGY-004` | Router tool execution via MCP bridge | `tests/e2e/antigravity-cli-workers.spec.ts` | `executes router scratchpad and mailbox tools via loopback MCP` |
| `SCN-AGY-005` | Preflight failure on signed-out / missing CLI | `tests/e2e/antigravity-cli-workers.spec.ts` | `rejects launch with actionable message when agy preflight fails` |
| `SCN-AGY-006` | Interrupting an active turn | `tests/e2e/antigravity-cli-workers.spec.ts` | `gracefully interrupts active turn and settles agent to idle` |
| `SCN-AGY-007` | Crash recovery and orphan process reaping | `tests/e2e/antigravity-cli-workers.spec.ts` | `discovers and reaps surviving orphan process groups on startup` |
| `SCN-AGY-008` | Cross-engine model switch rejection | `tests/e2e/antigravity-cli-workers.spec.ts` | `restricts model selection to same engine and rejects cross-engine change` |
