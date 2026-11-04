# Spec: Claude Code Workers (v1)

- **ID**: `SPEC-LIFECYCLE-001`
- **Status**: `Draft`
- **Domain**: `AgentManagement`
- **Target E2E Suite**: `tests/e2e/claude-code-workers.spec.ts` (planned)

Status: Draft; Implementation in progress, acceptance pending · Date: 2026-10-26, revised for per-foreground-execution processes · Plan: [claude-code-workers-plan.md](claude-code-workers-plan.md)

Boundary prerequisite: [runtime-engine-boundary.spec.md](runtime-engine-boundary.spec.md) (Draft). Extract a narrow, behavior-preserving Pi adapter first. Reuse internal persistence types where practical; normalized events are optional targeted cleanup, not a generic engine framework. Wolfgang has authorized implementation; required E2E acceptance remains pending. The goal is near-identical UI with the explicit engine differences below, not exact protocol parity.

Implementation sequencing: [CLAUDE_CODE_ADAPTER_PLAN.md](../../CLAUDE_CODE_ADAPTER_PLAN.md) (Implementation in progress; current evidence and gaps).

## 1. Summary

Router-managed workers can run on Claude Code (the `claude` CLI) as well as on pi-go. A Claude Code worker is launched from the same dialog, appears in the same sidebar, and offers the same conversation experience as an embedded worker:
- live timeline, activity indicator and context bar;
- interrupt and model change;
- context reset that keeps the scratchpad and recent turns;
- scratchpad, progress and reminders;
- goals with auto-continuation;
- restart, terminate and worktrees.

A Claude Code worker is an embedded worker with a different turn engine. Each foreground execution starts its own headless `claude` process, resumes the worker's Claude conversation when available, and stops and reaps the process and its owned children before the execution is settled. An execution is the entire admitted foreground scope, including all Run and Continue calls in that scope; those calls share its process. The next execution starts a fresh process with a new PID after cleanup. Router tools are offered through a process-scoped MCP endpoint. The timeline uses the existing conversation store.

Out of scope: tmux and the legacy mailbox integration (disk mailboxes, `mailbox-deliver.py`, `claude_sidecar.mjs`, `mailbox_send.py`). The existing `worker_launch.py --agent claude` path is left as it is and is not used.

## 2. Goals and non-goals

**Goals**
- **G1. UX parity.** Same UX as embedded workers for the v1 feature set (§3.12). This is the top priority.
- **G2. Subscription billing.** Billed through the host's logged-in Claude subscription, by running the unmodified `claude` CLI.
- **G3. Reuse.** Use the embedded runtime's existing machinery for message delivery, answer recording, history, lifecycle, context reset and goals. Add only what Claude Code needs.
- **G4. No legacy dependencies.** No dependency on tmux or the legacy mailbox integration.

**Non-goals for v1**
- Claude Code as the router root. The root stays on pi-go.
- Deferred to phase 2:
  - showing Claude's own subagents in the subagent pill;
  - workflow states and allowed-tool enforcement;
  - inline-tool allowance;
  - per-worker tool toggles;
  - launching workers from a Claude Code worker (`mecha_worker`).
- API-key billing, and Claude served through Bedrock or Vertex.
- Switching an existing agent between a pi-go model and a Claude Code model.
- Sharing one subscription login across several people.

## 3. Behavior

### 3.1 Local CLI availability

### Local CLI selection and preflight

Claude is installed and logged in on the router host. Login/logout and credentials are managed externally with `claude`; inherit normal CLI user/project configuration. No Claude provider settings row, connect/login/logout route, provider-auth response/event, periodic polling, credential persistence or fixed-provider-count changes.

Keep `claude-code/<alias>` only as an internal engine/model selection key in the existing launch/model pickers. Offer the known aliases without promising current sign-in availability. On launch and each process acquisition (before each execution), resolve `MECHA_CLAUDE_BIN`/PATH/local installation and run a bounded local auth-status check (5 s, no prompt). Missing executable, signed-out, malformed or timed-out status produces a helpful launch/turn error directing the user to install/sign in on the host. Do not create a worker on failed launch preflight. No new polling/cache subsystem; no account identity retained. Engine-specific model choices never become the root/default Pi model or cross-engine switches. Automated tests use a fake executable/auth response only.

### 3.2 Launch
- Choosing a Claude Code model in the launch dialog launches a Claude Code worker. Name, working directory, worktree option and initial instruction behave exactly as they do for embedded workers.
- The worker's first turn is its initial instruction, worded exactly as embedded workers receive it.
- Launch is refused with a clear error when Claude Code is unavailable.

### 3.3 Conversation
- **Incoming messages.** Messages reach a Claude Code worker exactly as they reach embedded workers. That covers the composer (text and images), other agents, reminders, and system notices. When the worker is idle, each message starts a turn.
- **Timeline.** As each step completes, the timeline shows:
  - thinking;
  - tool cards, which go from running to succeeded or failed and show an argument preview plus any assistant text that came just before the call;
  - the final answer.

  It updates per completed step, not per token, which is the same as for embedded workers.
- **Tool names** appear in the router's vocabulary:
  - Claude's own tools: `bash`, `read`, `edit`, `write`, `grep`, `glob`, `web_search`, `web_fetch`, `task`, `todo_write`;
  - router tools appear without any prefix.
- **Answers and outgoing messages.** The turn's final answer appears in the conversation as the worker's answer. Nothing is sent to anyone automatically. A worker messages another agent only by calling `mecha_mailbox_send`. Both rules match embedded workers since commit `0fc20bb`.
- **Activity indicator.** It shows active during a turn and idle otherwise.
- **Context bar.** It shows the context size Claude reports after each model response, against the model's context window.

### 3.4 Messages that arrive during a turn
- Durably accepted mail arriving during an execution stays in the existing router queue for later dispatch; it is not written to that process. A direct HTTP prompt instead waits for the lane and is not durably accepted before reservation. Cancellation or router restart can drop that unaccepted request; no new queue is implied. Admitted inputs run in later foreground executions, with their own answers.
- A message is ACKed only after its exact correlated echo is durably checked. Definitely unsubmitted messages remain eligible after interrupt or reset. If a write started but consumption cannot be checked, hold that input unacknowledged from automatic redelivery, including across restart/reset, until existing expiry; never guess that it was unconsumed.

### 3.5 Tools
- **Claude Code's own tools.** The worker has them all, without permission prompts. That is the same trust level as today's Claude workers and the embedded workers' bash.
- **Router tools.** Offered through the router MCP endpoint, with the same arguments and results they have for embedded workers:
  - `mecha_mailbox_send`, `mecha_send_image`
  - `scratchpad`, `mecha_progress`, `mecha_reminder`
  - `goal`, `goal_snooze`
  - `context_reset`, `mecha_context`
  - `mecha_worker_terminate`
- **Not offered:**
  - pi tools that Claude Code already covers: `bash`, `read`, `edit`, `write`, `google_search`, the subagent tools, `kb_*`, and tool-timeout extension;
  - the phase-2 tools.
- **Bound to the current process.** A router tool call is bound to the calling worker, its current Claude process and its current session. A call from a process the router has stopped, or from an older session, is rejected.
- **Foreground authority.** Every router tool call, including reads, requires the current admitted foreground execution, process grant, checked user and actual checked assistant producer. Calls outside that scope or from stale processes fail closed. Wait at most 5 s for a racing producer; never invent authority.
- **No double effects.** Within the current execution, concurrent duplicates with the same tool-use ID, canonical tool name and arguments must not execute twice; a name or arguments mismatch conflicts. Return a checked result when available within that execution. An unchecked or crash-uncertain effect remains UNKNOWN and is never automatically retried. After process exit/restart, an existing historical canonical effect is generically rejected without fresh execution or result reconstruction; a specific mismatch classification is not required. No plain enqueue or browser-goal shortcut.

### 3.6 Claude Code configuration
- **What the worker loads.** It runs in its working directory with the user's normal Claude Code configuration (user and project settings, CLAUDE.md, skills, plugins, user MCP servers), plus the router tools.
- **What the worker does not inherit.** Terminal-multiplexer variables (`TMUX`, `TMUX_PANE`) and nested-session markers (`CLAUDECODE`). Legacy hooks therefore treat the process as a non-agent context.
- **System prompt additions:**
  - the worker's identity (agent name, message address, working directory);
  - how messages arrive, and that nothing is sent automatically (use `mecha_mailbox_send`);
  - guidance on using the router tools.
- **Model.** The worker runs on the Claude Code model selected for it.

### 3.7 Process lifetime, interrupt, restart, terminate, router restart

**Claude process group safety.** Given a recorded Claude process identity, a zombie leader with live or uncertain group members is not proof of safe orphan cleanup. After router restart, an uncertain group is left untouched and its record retained; recovery may make the router unavailable until an operator safely resolves it and restarts. A local Claude waiter may signal remaining members only while its exact child leader is pinned and its identity and group are verified. It confirms no executable members remain before removing the matching record. This does not change the contract for other managed runtimes.
- **One process per foreground execution.** Start a fresh process for each admitted foreground execution, including goal continuations and reset wakeups. Resume the same session on subsequent executions; use a fresh session and bounded seed after reset or confirmed missing transcript. At most one process per worker is active, and cleanup completes before another starts.
- **No between-turn background work.** Stop and reap the process group and revoke its grant at execution end, including successful completion. Background tasks may run during the foreground execution but are not supported between executions; no idle notification turn or background authority is admitted. Unconfirmed cleanup fences further admission and retains recovery evidence.
- **Interrupt.** Stop the current execution promptly, preserve checked partial output and conversation, and reap its process. If interrupt wins before submission, no input is sent. If a write has started without a checked echo, hold the input as uncertain rather than replaying it. A later execution starts a new process.
- **Restart.** Stop and reap any active process; the next execution resumes the same conversation.

- **Terminate.** Stops any running turn and the worker's process promptly, then removes the worker as for embedded workers. The worktree cleanup option is unchanged.
  - Given a successful checked self-termination result is saved, when the engine emits further assistant output before cleanup completes, then no final or partial assistant output after that result is saved or shown. The checked producer and result remain intact until worker removal. The same rule applies to a successful checked `context_reset` result.
  - Given a Claude turn is active, when a new termination is durably accepted, then admission closes before that exact turn and process are interrupted, and cleanup waits for the turn to drain.
  - Given a newly committed Claude termination, live projection subscribers are woken with the terminating state before interruption, process stop, or turn drain, including when a post-commit error is returned. Replayed, rejected, and unconfirmed reservations do not cause this admission wake; the later cascade still publishes its own update.
  - Given an active turn, when termination is a replay, conflicts, has a stale tool producer, or its durable reservation fails, then it must not interrupt the turn or stop its process.
  - Given concurrent termination requests or a replacement runtime, only a newly accepted reservation may interrupt its captured runtime and process; it must not stop a replacement. Admission remains available while interruption and turn drain wait.
- **Router restart.**
  - A running turn is shown as interrupted, and its tool cards read "Interrupted by restart".
  - Verified leftover Claude process groups are stopped; uncertain identities remain fenced for operator resolution.
  - On startup the worker gets the same interrupted-turn notice embedded workers get, and continues its conversation.
  - No tool effect is automatically repeated.

### 3.8 Model change
- The header model picker on a Claude Code worker lists only Claude Code models. A change takes effect from the next turn, and the conversation is kept.
- The next foreground execution starts a new process with the selected model and resumes the conversation. No in-place model switch is required.
- Embedded pi-go workers never list Claude Code models.
- The router rejects any switch between engines.

### 3.9 Context reset
The triggers and the result are the same as for embedded workers.

**Triggers**
- **The worker calls `context_reset`.** Both reason and window are optional (window defaults to 20, maximum 100). Given a foreground worker, when it calls with `{}`, only a reason, only a window, or both, the checked reset result is saved before its turn stops and reset runs. Missing optional fields must not cause schema refusal. Unknown properties and wrong property types remain rejected. The worker's turn stops, then the reset runs.
- **Turn error after a checked reset result.** Given a foreground turn that calls `context_reset` and durably saves its successful checked result, when the turn subsequently fails, the reset still runs exactly once after all turn callbacks drain. New work waits until the reset has installed its fresh context, then admission resumes. The turn error remains visible.
- **Checked reset result fails.** Given a foreground turn that calls `context_reset`, when its checked result cannot be durably saved, no reset is performed for that call. Its admission gate is released without releasing a concurrent reset's gate; the uncertain outcome and turn error remain visible.
- **Warning threshold crossed.** The worker gets a system reminder, once per epoch, telling it to save its notes to the scratchpad and call `context_reset`.
- **Reset threshold crossed.** The reset runs automatically after the turn only when threshold-reset suppression is not active.
- **The gear menu's Reset.**

**Result**
- A new session epoch starts. The worker's new context contains, in this order:
  1. the initial instruction;
  2. all pinned scratchpad entries (at most 20), marked as pinned, and up to 20 recent unpinned entries;
  3. up to the last N messages of the previous context; for Claude, only the newest contiguous whole turns that fit the seed budget;
  4. the reset wake prompt.
- The worker continues immediately.
- For Claude Code, the worker's old process is stopped and a new one starts on the fresh session, so background work it started stops.
- The timeline shows a "Context reset (Epoch N)" marker, and the history before it stays visible.
- The context bar drops to the new size.
- For Claude Code, the preserved checked messages arrive as a JSON transcript in the first message of a fresh Claude session. Historical images are encoded data in that text transcript, not provider-visible image blocks. Embedded workers keep preserved messages structured.

**Thresholds**
- They use the same configuration as embedded workers: global values plus per-model overrides.
- All four Claude Code models run with a 1M-token window (§6). The default thresholds (10% warning, 20% reset) are therefore 100k and 200k tokens, well above a fresh session's ~42k. No Claude-specific defaults are needed.
- Precedence is unchanged: a configured per-model override, then the global setting, then the system default.
- If the context is still above the reset threshold right after a reset, the router does not reset again. It records a visible warning instead.

**Claude's own compaction**
- It stays on as a safety net. If it happens, the timeline shows a compaction entry and the context bar updates.

### 3.10 Goals
These work the same as for embedded workers.
- **Setting a goal.** Use the goal editor to set, edit, check or turn off a goal. The worker can also use its own `goal` and `goal_snooze` tools.
- **Evaluation.** Each time a turn ends normally with an active, applied goal, the router evaluates the conversation:
  - DONE turns the goal off;
  - NOT_DONE schedules a continuation prompt, after any snooze or the default delay, up to the existing cap.
- The existing circuit breaker still stops the loop after repeated empty turns.
- No Claude-initiated between-turn executions are admitted.
- **Who evaluates.** A router-side pi-go model, not the Claude worker: only the usable configured Pi router default (positive context window and authenticated constructible stream). No fallback search, new setting or Antigravity. Evaluation therefore uses no Claude usage and finishes within the existing time limit.
- **No evaluator available.** If no evaluator model is usable, the router records a visible "goal evaluation unavailable" entry and schedules no continuation. It does not loop on NOT_DONE.
- **Mid-turn changes.** A goal changed during a turn applies at the next reliably observed response boundary after its checked result, otherwise at the next submitted input. No unsupported CLI request gate or goal-set-time receipt is implied.

### 3.11 Usage limits and failures
- **Claude usage limit.**
  - When the limit is reported, a visible entry in the conversation says "Claude usage limit reached; resuming at \<time\>" before waiting for the reset. The interrupted work remains pending until it can resume.
  - The worker pauses. Messages and goal continuations that haven't started stay queued; they are not failed and not retried early.
  - At the reset time, retry the interrupted input first only when known safe to retry. An uncertain submitted input remains held and is never automatically replayed; it does not block later eligible queued inputs or goal continuations after the hold. Queued inputs remain unacknowledged until their own checked echoes.
  - Interrupting a hold ends it promptly and releases unsubmitted messages. Repeated interrupts for the same active answer are idempotent, and its interrupt control cannot reach a subsequent turn.
- **Other failures.** These include a missing CLI, expired sign-in, a crash or malformed output.
  - The turn ends with a visible error in the conversation, as with embedded workers.
  - The worker stays alive.
- **Process cleanup.** Every foreground execution ends by stopping and reaping its process group and revoking the grant. Restart, reset, termination and router shutdown also stop active processes. Recovery safely handles verified leftovers; uncertain cleanup fences admission.

### 3.12 UX parity matrix

| Feature | Embedded | Claude Code v1 |
|---|---|---|
| Launch dialog, worktree, initial instruction | ✓ | ✓ |
| Composer chat with images | ✓ | ✓ |
| Agent-to-agent messages (explicit `mecha_mailbox_send`) | ✓ | ✓ |
| Live timeline: thinking, tool cards, answers | ✓ | ✓ (per step) |
| Activity indicator, context bar | ✓ | ✓ (exact tokens) |
| Agent-sent images | ✓ | ✓ |
| Interrupt, restart, terminate | ✓ | ✓ |
| Model picker | ✓ | ✓ (Claude Code models only) |
| Context reset: tool, thresholds, gear menu | ✓ | ✓ (transcript seed) |
| Scratchpad, progress, reminders, monitors | ✓ | ✓ |
| Goal editor, auto-continuation, snooze | ✓ | ✓ |
| Mid-turn messages folded into the running turn | ✓ | Mail waits queued; direct HTTP prompts wait for the lane |
| Subagent pill and stop-subagent | ✓ | Phase 2 (hidden) |
| Workflow states, inline tools, worker tool toggles | ✓ | Phase 2 (hidden) |

### 3.13 Security and data
- **MCP endpoint.**
  - It is reachable on loopback only.
  - Each Claude process gets a fresh random token when it starts.
  - Tokens never appear on a command line, in logs or in transcripts.
  - The file holding a token is readable only by the user and is deleted when the process exits.
- **Retention.** No new store accumulates data.
  - The router's copy of the conversation follows the existing embedded retention.
  - Claude's own transcripts follow Claude Code's `cleanupPeriodDays` (30 days by default). If a worker's Claude transcript is gone, its next turn starts a fresh Claude session seeded from the router's copy, as in a reset.
  - Captured stderr is capped at 64 KiB per process.
  - Temporary MCP config files are deleted when the process exits and swept at router startup. A process record is deleted only after its process group is confirmed gone; uncertain cleanup retains the record for recovery. There is at most one process record per worker.
  - No Claude process is intentionally kept idle between executions (§3.7).

## 4. High-level design

```
UI composer / agents / reminders
        │  POST /v1/messages (unchanged)
        ▼
router message store ──▶ agent lane ──▶ embedded turn (shared: ownership, activity,
                                         │  ACK, answer, goal eval, reset checks)
                                         │  engine chosen by model provider
                       ┌─────────────────┴──────────────────┐
                       ▼                                    ▼
                 pi-go session                    Claude Code turn engine
                 (unchanged)                       │ stdin: user messages,
                                                   │        control interrupt
                                                   ▼
                                     claude -p (stream-json, one per foreground execution)
                                       │ stdout events      │ MCP tools/call
                                       ▼                    ▼
                                  translator          router MCP endpoint
                                       │                    │
                                       ▼                    ▼
                       conversation sink (transcript   router tool operations
                       mirror, history rows, ACK,      (store), bound to the
                       answers) ──▶ ui-live ──▶ UI      agent's current session
```

**Components and responsibilities**

1. **Engine selection.** The model provider `claude-code` identifies the engine. Agents stay `runtime=embedded`, so every existing embedded path applies to them unless the engine says otherwise.
2. **Turn engine seam.** The embedded turn runs one of two engines behind a small interface: the existing pi-go session, or the new Claude Code turn engine. Both engines share:
   - everything before the turn: ownership, activity, message reservation;
   - everything after it: answer recording, goal evaluation, reset checks, lifecycle.
3. **Claude Code process manager and turn engine.** Start a fresh process for each foreground execution, resume the binding's session or seed a fresh session, translate its events, and stop/reap before releasing the lane. Mid-turn mail remains in the durable router queue; direct HTTP prompts wait for the lane without durable acceptance before reservation. No idle handler, background notification execution, or in-place model switch is needed.

4. **Router MCP endpoint.** A Streamable HTTP MCP server inside the router. It:
   - authenticates the per-process token and maps it to the worker's current process and session;
   - runs router tools through the existing shared admission/Execute/completion/result path described in detailed plan §6;
   - de-duplicates using Claude's tool-use ID.
5. **Local preflight and model table.** Engine/model aliases are selectable without a settings provider. On launch/process acquisition only, validate the local executable/auth status with a 5 s bound. No provider-auth/polling subsystem or credential store. Login stays external.

6. **Goal integration.** The Claude engine records the "goal applied" evidence at the same logical points the pi engine does. The evaluator runs on the helper model.
7. **Context integration.** Exact token counts from Claude's usage reports feed the threshold checks and the context bar. Reset seeding uses the engine's seeding path (component 3).
8. **Usage-limit hold.** Pauses the agent's message lane until the reset time Claude reports.
9. **Web UI.** Accepts the new provider, splits model choices by engine, and hides phase-2 controls for Claude Code workers.

## 5. Alternatives considered

| # | Alternative | Why not |
|---|---|---|
| A | **A third runtime value `claude`** with its own lifecycle | The `CHECK(runtime IN ('rpc','embedded'))` constraint would need a table rebuild. There are about 47 Go branch points, 16 SQL literals and 13 UI checks, and the UI rejects the whole agent snapshot if it sees an unknown runtime. It would also mean re-implementing mail delivery, answer recording, history, reset and goals that the embedded runtime already has. |
| B | **Launch the existing tmux Claude worker from the UI** (`worker_launch.py` plus `claude_sidecar.mjs`) | Depends on tmux and keystroke injection. There is no conversation timeline, only activity dots. Explicitly ruled out. |
| C | **Claude models inside pi-go**, using pi-go's existing Anthropic provider | The cheapest to build, with every embedded feature. But it is API-key billing, it uses pi's tools instead of Claude Code's, and using subscription OAuth tokens in a third-party harness violates Anthropic's terms. It doesn't deliver Claude Code. Could be offered separately later. |
| D | **Long-lived process per worker** | Rejected: idle task admission, lifecycle fencing and in-place switching add complexity. Per-execution processes cost startup time and restart user MCP servers; background tasks cannot survive a turn. |
| E | **A Node sidecar on the Claude Agent SDK** instead of driving the CLI from Go | Adds a Node runtime dependency and a second process hop. The pieces we need are already reachable from Go: streamed messages and events, control interrupt, and MCP. All were verified live (§6). |
| F | **Reset by writing a synthetic Claude transcript and resuming it** | The preserved turns would stay structured. But it depends on Claude Code's undocumented internal transcript format, which can change in any CLI release. A text transcript in the first message is robust and was verified to work. |
| G | **Goal evaluator as a one-shot `claude -p` call** | Every evaluation would cost several seconds of start-up inside a one-minute budget, consume Claude usage, and need a new response adapter. The helper model reuses the existing evaluator code path and credentials unchanged. |
| H | **A stdio MCP subprocess per worker** that proxies to the router | An extra process per worker and an extra hop for no benefit. Claude Code calls an HTTP MCP endpoint directly with a bearer header (verified). |
| I | **A persistent per-worker MCP token** | It would have to be stored, rotated and revoked across router restarts. A per-process token lives exactly as long as the process that uses it, with nothing to persist. |
| J | **Locked-down Claude configuration** (`--setting-sources project --strict-mcp-config`) | More predictable, but it would drop the user's skills, plugins and MCP servers, which are a reason to choose Claude Code. Legacy hook interference is handled by not passing on the tmux and nested-session environment variables instead. |

**Chosen design.** The Claude Code engine inside the embedded runtime, with one process per foreground execution (§4). It satisfies G1–G4, reuses the most existing behavior, and confines new code to the engine, the MCP endpoint, model plumbing and small UI changes.

## 6. Verified facts

Live probes on 2026-10-26 with `claude` 2.1.308:

- **One process handles many turns.** A single `claude -p --input-format stream-json --output-format stream-json` process answers several user messages. Each answer produces its own `result` event, and the process exits cleanly when stdin closes.
- **A text seed works.** A reset-style seed, sent as one message with several text blocks (instruction, scratchpad, transcript, wake prompt), is picked up. A follow-up turn in the same process recalled the seeded state correctly.
- **Mid-turn messages are queued, not folded in.** A message written while a turn runs is echoed back (`--replay-user-messages`) only when Claude starts answering it, after the current turn's `result`, and it gets its own answer and `result`.
- **Interrupt works through the control protocol.** `{"type":"control_request","request":{"subtype":"interrupt"}}` ends the turn. Claude emits the partial assistant message, then `result/error_during_execution`, and the process stays alive for the next input. SIGINT also ends the turn, but the process then exits.
- **The HTTP MCP endpoint works as designed.** Configured with `type: http` and an `Authorization: Bearer` header:
  - the header arrives on every request;
  - tools appear as `mcp__<server>__<tool>`;
  - each `tools/call` carries `_meta["claudecode/toolUseId"]`, which equals the `tool_use` ID in the stream;
  - Claude sends a non-standard `server/discover` call before `initialize`.
- **Context size is reported exactly.** Every assistant event includes usage. Context size = `input_tokens + cache_creation_input_tokens + cache_read_input_tokens`.
- **Baseline cost.** A fresh session with full user configuration in this repository starts at about 42.5k tokens of context.
- **Sign-in status.** `claude auth status --json` reports `loggedIn`, `authMethod` and `subscriptionType`.
- **Rate-limit event.** `rate_limit_event` carries `rate_limit_info.status` and `resetsAt` (Unix seconds).

Live probes on 2026-10-28 with `claude` 2.1.308 (plan Phase 0, S1–S9):

- **Context windows.** Every alias runs with a 1,000,000-token window: `fable` (`claude-fable-5-1`), `opus` (`claude-opus-5-5`), `sonnet` (`claude-sonnet-5-5`) and `haiku` (`claude-haiku-5-5`).
- **Missing session.** `--resume` with an unknown session fails in about a second with "No conversation found with session ID" and exit code 1.
- **Background task finishes while idle.** Claude emits `task_notification` with a summary, then runs a turn of its own: `system/init`, assistant, `result`. No user event precedes it. Background Bash commands and background subagents both behave this way.
- **Background task finishes during a turn.** Claude folds it into the running turn as a replayed user message tagged `origin.kind: "task-notification"`, and the turn still ends with one `result`.
- **Running background tasks.** `system/background_tasks_changed` lists every running background task whenever the set changes.
- **Model switch.** A `set_model` control request switches a running process to another model for the next turn. `--resume` with a different `--model` also works and keeps the conversation.
- **Idle memory.** An idle process with the full user configuration holds about 365 MiB, flat over 5 minutes.
- **Usage limit, from the CLI's schema.** A refused request arrives as an API-error assistant message with `api_error: "usage_limit_reached"` and the limit's `resetsAt`. `rate_limit_event` reports `status: "rejected"`. Not yet seen live.
- **Permissions.** `--permission-mode bypassPermissions` alone runs every tool in headless mode with no prompt and no refusal, including writes into `.claude/`. It does not depend on the `skipDangerousModePermissionPrompt` setting. Without the flag, writes are refused and reported in the stream; nothing waits for an answer.

Still to verify:
- the usage-limit event shapes above, live, when a limit is hit naturally;
- whether the `interrupt_send_now_v1` capability allows true mid-turn steering, as a possible later improvement.

## 7. Acceptance criteria

- **AC1. Local availability.** Known Claude engine/model aliases appear in the existing launch picker. On-demand local executable/auth checks permit signed-in launch and reject missing/signed-out/timed-out/malformed status helpfully, without creating a worker. Normal configuration/login is inherited; no provider settings/auth route, credentials, polling or status events.
- **AC2. Launch.** Launching with a Claude Code model, with and without a worktree, starts a worker whose first turn is the initial instruction. The worker appears in the sidebar like an embedded worker.
- **AC3. Conversation.** A composer message with an image produces a turn in which Claude receives both text and image. Its thinking, tool cards (running then finished) and final answer appear in the timeline. The activity indicator and context bar update. Nothing is mailed automatically.
- **AC4. Mid-turn messages.** Durably accepted mail waits in the existing queue; direct HTTP prompts wait for the lane without durable acceptance before reservation and can be lost on cancellation or restart. Admitted inputs run in later executions and ACK only on their own checked echo. Definitely unsubmitted inputs remain eligible; a write-started input without checked echo is held, including across reset/restart, not automatically replayed.
- **AC5. Router tools.** Each v1 router tool works from a Claude Code worker with embedded-equivalent effects. A repeated call with the same tool-use ID has no second effect. A call bound to an old session is rejected.
- **AC6. Interrupt.** Interrupt ends a running turn within 5 s, keeps the partial output, and leaves the worker idle with its conversation.
- **AC7. Restart, terminate, router restart.** Restart continues the conversation. Terminate stops a running turn and the worker's Claude process within 5 s. A router restart mid-turn produces the interrupted-turn notice and continuation, with no repeated tool effects and no leftover `claude` processes.
- **AC8. Model change.** A Claude Code worker can switch between Claude Code models and keeps its conversation. Cross-engine switches are rejected by both the UI and the router.
- **AC9. Context reset.** Each of the three triggers produces a new epoch whose first Claude message contains the instruction, scratchpad and last N messages, in order, followed by the wake prompt. The epoch marker appears, the context bar drops, and no reset loop occurs when the post-reset context is above the threshold.
- **AC10. Goals.** Setting a goal on a Claude Code worker leads to evaluation after each normal turn. NOT_DONE produces continuation turns, DONE turns the goal off, snooze delays the continuation, and the cap and circuit breaker hold.
- **AC11. Usage limit.** A reported usage limit pauses queued work until reset; retry the interrupted input only when known safe; otherwise hold it as uncertain without blocking later eligible work after the hold.
- **AC12. Isolation.** The Claude process environment lacks `TMUX`, `TMUX_PANE` and `CLAUDECODE`. MCP tokens never appear in argv or logs. Temporary files are removed when the process exits and at startup.
- **AC13. No regression.** Preserve Pi behavior on changed shared paths and existing regressions, with the separately specified reset release/redelivery fix rather than the historical stranded-reservation defect. The scoped green browser run covers Pi RE-008 and Claude CC-005, not every reset/echo race or the expanded Pi matrix. Applicable changed-path E2E and regression gaps remain required; unrelated expanded Pi matrix cases are deferred, not claimed passed. See the boundary coverage report.
- **AC14. Process lifetime.** Each foreground execution uses a fresh process and resumes the conversation. All Run/Continue calls in one admitted foreground scope share its process; the next execution uses a new PID. The old process group and grant are gone before the next execution; unconfirmed cleanup fences admission. No between-turn background execution or in-place model switch is required. All router tools require checked foreground authority.

## 8. Verification status

Implementation is authorized and in progress. Pi seam/shared authority/reset store/coordinator and Claude process/UI/catalog work are implemented with partial focused validation. Reported real catalog-admission browser evidence: `tests/e2e/claude-catalog-admission.spec.ts`, **1 passed**. Supporting scoped UI tests and typecheck passed. These checks do not prove the full Claude lifecycle.

Claude runtime/MCP integration remains in progress. **CC-001–CC-018 are not accepted**, and this specification remains **Draft**. Target acceptance suite: `tests/e2e/claude-code-workers.spec.ts` (planned), with per-AC mapping in plan §13–14. It must exercise headless browser, REST, router-proxy, WebSocket and SQLite with deterministic local Claude/evaluator mocks. Go/Vitest checks support but do not replace that matrix. See the [root implementation status](../../CLAUDE_CODE_ADAPTER_PLAN.md#current-implementation-evidence) for the compact evidence summary. No builds or tests were run for this documentation reconciliation.


## 9. Core Invariants

1. Near-identical UI, not identical engine timing or per-request control. Local login/config is inherited; no Claude provider settings subsystem.
2. Router-controlled submission is admitted first; only checked consumption ACKs. Uncertain writes are held, not replayed. Native CLI work is not individually gated but its process lifetime is bounded to foreground execution.
3. Shared canonical tool producers, live Execute witnesses and checked results own certainty. No receipt-derived authority, duplicate effects or UNKNOWN retries.
4. Reset release is atomic with epoch advance after callback drain. Never release consumed mail; old callbacks cannot affect new epochs.
5. All buffers/processes/files have the bounds/cleanup above; no new accumulating store.

## 10. User Scenarios (Behavioral Contract)

### Scenario: [CC-001] Launch using the host CLI
- **User Goal**: Launch using the host CLI.
- **Given**: Known Claude aliases in the launch picker and a fake locally signed-in CLI, with normal project/user configuration.
- **When**: The user launches with/without a worktree and an initial instruction.
- **Then**: Exactly one worker appears; CLI cwd/config/model and initial instruction are correct. Root/default remains Pi. No provider settings row, auth mutation, credential write or polling/status event is added.

### Scenario: [CC-002] Get a helpful local launch failure
- **User Goal**: Get a helpful local launch failure.
- **Given**: A missing executable, signed-out, malformed or hung fake auth-status response (parameterized).
- **When**: Launch is attempted, then local status is corrected and launch retried.
- **Then**: The first launch fails within the 5 s local check bound with external install/login guidance and no worker; the retry succeeds on a fresh on-demand check. No background polling or retained account identity.

### Scenario: [CC-003] Converse with images and tools
- **User Goal**: Converse with images and tools.
- **Given**: An idle Claude worker and fake CLI capturing stdin.
- **When**: Text/image mail from browser and agent/reminder sources runs through thinking, built-in tool, router tool and final steps.
- **Then**: Text/images arrive in order; checked consumption alone ACKs; timeline shows per-step thinking/tool outcomes/answer, activity and reported context. Nothing is automatically mailed. Raw built-in names do not gain router authority.

### Scenario: [CC-004] Receive queued input without false receipt
- **User Goal**: Receive queued input without false receipt.
- **Given**: A running turn with equal-text distinct messages and deterministic echo/result barriers.
- **When**: More messages arrive during normal/startup/recovery work, or execution ends before their submission or echo.
- **Then**: Accepted mail stays durably queued for later executions. Direct HTTP prompts wait for the lane without durable acceptance before reservation; cancellation or restart can drop an unaccepted request. Each admitted input gets its own ACK only on its exact correlated echo. Definitely unsubmitted reservations are released; write-started inputs without checked echo are held, not redelivered. Equal text never swaps identities.

### Scenario: [CC-005] Keep mail through every reset race
- **User Goal**: Keep mail through every reset race.
- **Given**: Both Pi and Claude workers with reserved unconsumed mail, consumed mail and equal-text inputs.
- **When**: Tool/threshold/browser reset races an echo at explicit barriers, including failed reset rollback, late old callback, repeat reset, expiry and router crash after epoch commit.
- **Then**: Coordinator drains before snapshot; atomic epoch/release deletes eligible old unconsumed attempts, including an attempt for a write-started uncertain message. The durable `messages.claude_stdin_started` bit belongs to the exact message incarnation, not its attempt; deleting that attempt does not clear the message-level hold, so the queued uncertain message stays held across reset and restart until existing expiry. Consumed mail has one ACK and no redelivery; definitely unsubmitted live mail receives a fresh attempt/new epoch, never old ACK. Write-started uncertain mail stays held. Expired mail stays expired. Late callbacks cannot affect replacement reservations/history/usage/effects. Pi extraction baseline tests precede this separate fix.

### Scenario: [CC-006] Run router tools with checked certainty
- **User Goal**: Run router tools with checked certainty.
- **Given**: An admitted user/producer scope and current grant, plus deterministic domain/append barriers.
- **When**: Each offered tool executes over real HTTP MCP; current-execution duplicates with matching or mismatched tool names or arguments and concurrent deliveries follow.
- **Then**: Existing admission/Execute/completion/result checks apply. Each effect occurs at most once; current-execution checked success may return its saved output, and a name or arguments mismatch conflicts. No attachment is written before fresh admission. Missing ID/producer/schema fails before effects; HTTP-before-stdout waits at most 5 s. Successful result is checked before MCP success.

### Scenario: [CC-007] Do not repeat uncertain effects
- **Given** an effectful tool call at a persistence or crash boundary.
- **When** a duplicate arrives or the process/router restarts.
- **Then** an unchecked or uncertain effect is UNKNOWN and never automatically re-executed. Within the current execution, a tool name or arguments mismatch conflicts, and a checked result may be returned. After process exit/restart, an existing historical canonical effect is generically rejected without fresh execution or result reconstruction; a specific mismatch classification is not required. Stale grants are rejected and missing evidence never grants fresh authority.

### Scenario: [CC-008] Require foreground tool authority
- **Given** a current process and an admitted foreground execution, or a stale/absent execution.
- **When** any router tool call arrives, including a read.
- **Then** only a current grant and checked foreground user and assistant producer authorize it. An absent or stale scope fails closed; no between-turn or synthetic notification authority exists.

### Scenario: [CC-009] Stop or recover safely
- **User Goal**: Stop or recover safely.
- **Given**: Partial output and pending input/background child processes.
- **When**: Interrupt, restart, terminate and router crash occur as separate cases.
- **Then**: Interrupt keeps partial output, ends within 5 s and preserves conversation. Restart resumes it; terminate stops process/children within 5 s. Router recovery records interrupted tools/notice, reaps verified leftovers and never repeats uncertain effects. Old output cannot mutate current binding.

Restart implementation contract: an empty restart request for an embedded Claude worker closes/reaps only its engine resources, revokes its private MCP grant and keeps the checked router binding/history. It does not look up an RPC worker. New acquisitions consult a fenced, persisted binding-started bit even after process eviction. Before submitting any user input, a resumed process must answer the control channel. Only the explicit missing-conversation diagnostic permits one fresh-process fallback; any other failure, or failure of that fallback, leaves the input unconsumed. Checked history is context, never authority to repeat an uncertain tool effect. The started bit shares existing session retention and deletion, with no separate ledger.

### Scenario: [CC-010] Choose a model within the engine
- **User Goal**: Choose a model within the engine.
- **Given**: A Claude worker and an accepted Claude model change.
- **When**: The next foreground execution starts, or a cross-engine/default-model selection is attempted.
- **Then**: A new process resumes the conversation with the chosen model. No in-place switch or background survival is promised. UI and server reject cross-engine or Claude root/default selections; Pi choices are unchanged.

### Scenario: [CC-011] Reset context with preserved notes
- **User Goal**: Reset context with preserved notes.
- **Given**: Instruction, pinned/unpinned scratchpad, history and oversized tool output; configurable thresholds.
- **When**: Tool/browser/threshold reset runs, including warning and fresh-session threshold edge cases and CLI compaction.
- **Then**: New epoch seed orders instruction, bounded scratchpad, recent transcript and wake. Seed preserves whole instruction and bounded scratchpad sections and the complete wake, then includes whole recent turns when space permits. Optional history is omitted rather than clipped. Old process stops. Marker/history remain, usage updates, per-model/global precedence holds, warning is once per epoch and fresh-session loop is suppressed visibly. Compaction shows an entry.

If a Claude reset wake already reports usage at or above the effective reset threshold, show the epoch's single warning visibly and suppress further automatic threshold resets in that binding. Keep suppression even if usage grows; rearm only when reported usage falls strictly below the effective reset threshold (including a changed configuration that now provides usable headroom), or the binding/epoch changes. A configuration change that leaves usage at or above the new reset threshold does not rearm it. Explicit resets remain available. Pi threshold behavior is unchanged.

Fresh Claude sessions seed even when checked history is empty. A fresh session created after a missing transcript uses fresh-session headroom, not the previous session's reported token usage; a successfully resumed session still uses its reported usage. Seed history is the current binding's checked entries; after reset this is the existing bounded ported context, and after a missing transcript it is the current checked session, not the full timeline. Claude seed selection counts Unicode code points across the seed and provider-visible wake text, including public mail envelopes. The instruction and bounded scratchpad are mandatory; include the newest contiguous whole conversation turns that fit after those sections and the complete wake. Never clip a section, turn, tool result, or wake. Prepend the seed as a separate text block to the original wake blocks, retaining their order and images. Reject before user input submission when mandatory sections and wake exceed 200k code points, or when the complete rendered input exceeds 6 MiB or model headroom. Do not fall back to wake-only; a mail rejected by these bounds is not consumed or dead-lettered. Once the execution has settled, its unconsumed reservation is released and the dispatcher stops without automatic retry; a later offer may retry it after conditions change.

- **Given**: A fresh Claude session with instruction, bounded scratchpad, history and a Unicode or mixed text/image wake, including public mail.
- **When**: History competes for space, or the mandatory seed and wake exceed a budget or rendered-input limit.
- **Then**: Mandatory sections and wake remain complete and ordered; only whole history turns may be omitted. On rejection no user input is submitted and mail remains unconsumed.
- **E2E follow-up**: `tests/e2e/claude-code-bounds.spec.ts` needs reconciliation with this behavior; not verified by this change.

### Scenario: [CC-012] Continue a checked applied goal
- **User Goal**: Continue a checked applied goal.
- **Given**: An active goal and deterministic usable Pi-default evaluator.
- **When**: Tool/UI changes, NOT_DONE, DONE, snooze, empty turns, aborted/error/unprompted turns and cap cases run.
- **Then**: Only applied checked eligible foreground turns evaluate. NOT_DONE continues after delay/snooze up to cap 20, DONE turns off, empty-turn breaker holds. An idle sweep racing a newly reserved verdict must not bypass its normal continuation delay; repeated sweeps must not postpone it indefinitely. A recovered reservation remains eligible through the existing restart path, and snoozed deadlines remain authoritative. Mid-turn tool changes apply only at next reliable response boundary or submitted input. No goal-set-time receipt. Aborted/error/unprompted turns do not evaluate.

### Scenario: [CC-013] Avoid looping without an evaluator
- **User Goal**: Avoid looping without an evaluator.
- **Given**: Default is missing, Claude, unauthenticated, zero-window or lacks a usable stream.
- **When**: A Claude goal turn completes.
- **Then**: Visible goal evaluation unavailable, no continuation or fallback search. A later usable configured Pi default permits normal evaluation. Pi worker evaluator behavior is unchanged.

### Scenario: [CC-014] Hold usage-limited work
- **User Goal**: Hold usage-limited work.
- **Given**: A scripted rejected usage event, queued mail and goal continuation.
- **When**: Limit occurs with/without reset time, then the clock reaches the hold deadline; also restart the router during hold.
- **Then**: Visible limit is a non-final notice: it does not complete the active mail or mark the turn failed. Queued inputs stay unACKed without attempt increments/dead-letter during the hold, including multiple initial inputs. After the deadline, resume interrupted work before the queue only if known safe; otherwise the uncertain submitted input stays held without automatic replay and later eligible work can proceed. Absent reset time uses 15 min. Hold is in memory and rediscovered after restart. Both supported defensive event shapes work.

### Scenario: [CC-014I] Interrupt a usage-limit hold
- **User Goal**: Stop waiting without losing queued work.
- **Given**: A Claude worker is waiting for a future usage-limit reset and another message is pending.
- **When**: The user clicks Interrupt, including as the reset deadline arrives.
- **Then**: The worker promptly becomes idle without resubmitting the limited prompt. Pending work remains available, and a later turn starts a new process. Without interruption the normal deadline resume remains unchanged.

### Scenario: [CC-015] Inherit configuration without leaking grants
- **User Goal**: Inherit configuration without leaking grants.
- **Given**: Normal CLI settings/skills/plugins/MCP and a per-process private router config.
- **When**: Launch, exit, reset and startup sweep run.
- **Then**: Normal config/login is inherited, TMUX/TMUX_PANE/CLAUDECODE removed; tokens absent from argv/log/history. Config leaves are 0600; files deleted/revoked on exit and swept on startup. At most one process record per worker; bounded stderr/transcript cleanup and missing-transcript reseed tested. No provider credentials/settings are changed.

### Scenario: [CC-016] Bound each foreground process
- **Given**: Consecutive foreground executions and a child started during one execution.
- **When**: The first execution settles and the next starts, including after a model change.
- **Then**: All Run/Continue calls in the first scope use the same PID; the first process and its owned child are stopped and reaped before a fresh process with a new PID resumes the session with the selected model. No idle notification turn runs. Unconfirmed cleanup fences the next execution.

### Scenario: [CC-017] Bound protocol memory and failures
- **User Goal**: Bound protocol memory and failures.
- **Given**: Fake CLI with controlled malformed/unknown events, giant lines, pending inputs and protocol buffers.
- **When**: Count/byte limits are reached independently, or a required record/capability is missing.
- **Then**: 64 correlations/6 MiB input cap leaves additional mail queued and direct HTTP prompts waiting before reservation/write; 16 MiB line, 128 records/16 MiB producer-wait buffer limits fail visibly and stop/reap without false ACK/final. Stderr stays 64 KiB; buffers clear on close. Benign unknown events may be ignored; required malformed output never succeeds. Worker remains available for later retry.

### Scenario: [CC-018] Admit before submission without overstating CLI control
- **User Goal**: Admit before submission without overstating CLI control.
- **Given**: A CLI blocked at stdin, echo and result boundaries; store failures and ownership loss are injectable.
- **When**: Admission denies before write, or echo is consumed while ownership/persistence changes, or unsolicited output arrives outside the foreground execution.
- **Then**: Denial writes nothing/ACKs nothing. Echo checks original bytes/execution/entry before later publication; failure revokes/reaps and prevents final/evaluation. A committed ACK is not undone/replayed; definitely unsubmitted work remains queued and uncertain writes stay held. No receipt on assistant arrival. Unsolicited output outside the foreground execution is rejected, not admitted as a new turn.

## 11. Automated E2E Verification Matrix

The full scenario rows below remain required acceptance targets, not passing claims. Partial catalog evidence is recorded in §8. Each uses headless browser, real UI/REST/router-proxy/WebSocket/router/SQLite and deterministic external CLI/provider/evaluator mocks. No real Claude, LLM or external service. Use explicit barriers, isolated databases and bounded waits. Restart the real router over the same database for crash cases. Assert UI, WS and checked SQLite records; include direct cross-origin controls (credentials omitted) as well as proxy transport. Supporting Go/Vitest tests do not replace this matrix. AC13 requires the applicable changed-path boundary scenarios and existing regressions, including reset release/redelivery; unrelated expanded Pi matrix coverage is deferred. See the boundary coverage report.

| Scenario | AC | Planned suite | Planned test title |
|---|---|---|---|
| CC-001 | AC1–AC2 | `tests/e2e/claude-code-workers.spec.ts` | `launches local Claude without provider settings` |
| CC-002 | AC1 | `tests/e2e/claude-code-workers.spec.ts` | `rejects unavailable local CLI on demand` |
| CC-003 | AC3 | `tests/e2e/claude-code-workers.spec.ts` | `renders images tool steps and final without automatic mail` |
| CC-004 | AC4 | `tests/e2e/claude-code-workers.spec.ts` | `ACKs exact echoes and preserves unconsumed inputs` |
| CC-005 | AC4, AC13 | `tests/e2e/claude-code-workers.spec.ts` | `releases only unconsumed reset reservations and fences late echoes` |
| CC-006 | AC5 | `tests/e2e/claude-code-workers.spec.ts` | `executes router tools through checked witnesses` |
| CC-007 | AC5, AC7 | `tests/e2e/claude-code-workers.spec.ts` | `preserves UNKNOWN without historical result reconstruction` |
| CC-008 | AC5, AC14 | `tests/e2e/claude-code-workers.spec.ts` | `requires checked foreground scope for all router tools` |
| CC-009 | AC6–AC7 | `tests/e2e/claude-code-workers.spec.ts` | `interrupts restarts terminates and recovers without repeated effects` |
| CC-010 | AC8 | `tests/e2e/claude-code-workers.spec.ts` | `partitions models and resumes with selected model` |
| CC-011 | AC9 | `tests/e2e/claude-code-workers.spec.ts` | `preserves bounded reset context without reset loops` |
| CC-012 | AC10 | `tests/e2e/claude-code-workers.spec.ts` | `evaluates only checked applied goals and honors loop limits` |
| CC-013 | AC10 | `tests/e2e/claude-code-workers.spec.ts` | `reports unavailable Pi default evaluator without continuation` |
| CC-014 | AC11 | `tests/e2e/claude-code-workers.spec.ts` | `holds limited work and resumes before queue dispatch` |
| CC-015 | AC12 | `tests/e2e/claude-code-workers.spec.ts` | `inherits CLI config and cleans private process artifacts` |
| CC-016 | AC14 | `tests/e2e/claude-code-workers.spec.ts` | `uses one process per foreground execution and reaps children` |
| CC-017 | AC3–AC5, AC12, AC14 | `tests/e2e/claude-code-workers.spec.ts` | `bounds buffers and fails malformed protocol closed` |
| CC-018 | AC4, AC10, AC13 | `tests/e2e/claude-code-workers.spec.ts` | `gates submission and fences consumed rejection` |

### Scoped lifecycle cleanup regressions

- **Given** a Claude process cannot be safely identified during Close, **when** stop cannot be confirmed, **then** Close returns a bounded error, blocks further Claude admission, retains its durable recovery record, and never signals an unverified group. The child remains owned by its existing waiter until it exits.
- **Given** the local waiter has pinned the exited leader and confirmed no executable group members, **when** it reaps that leader, **then** it may remove only the recovery record matching that exact identity, even if zombie descendants remain. A replacement record must be retained; uncertain confirmation must never remove a record.
- **Given** a process is stopping, **when** another stop or acquisition arrives, **then** stop joins the same cleanup, the old grant is revoked promptly, and no replacement starts until cleanup is confirmed; an unconfirmed stop fences admission.
- **Given** an execution is in local preflight, **when** the manager closes before preflight finishes, **then** acquisition fails rather than starting a process.
- **Given** a protocol error precedes an unconfirmed group cleanup, **when** Close completes, **then** its result still reports the unconfirmed cleanup.

### Pool shutdown and unconfirmed cleanup

- **Given** a Claude start has been admitted but has not yet registered its process, **when** the manager shuts down, **then** shutdown joins that start and any resulting cleanup, revokes its grant, and reports an unconfirmed stop rather than returning success.
- **Given** a Claude process cannot be confirmed stopped, **when** reset, restart, or termination requests cleanup, **then** the old context is not reset or replaced, the agent is not successfully removed, Claude admission remains fenced, and the failure is recorded or returned. A committed termination handoff remains committed and its operation fails through the lifecycle failure path. Shutdown also reports the failure on subsequent calls. Pi lifecycle behavior is unchanged.
- **Given** the leader exits on a platform without independent confirmation that its process group is absent, **when** cleanup waits for it, **then** leader exit alone is not success: the recovery record and admission fence remain.

### Scenario: [CC-020] Claude-reported context window
- **User Goal**: See and enforce the actual context capacity reported by Claude.
- **Given** a Claude worker reports a positive context window for its current model and binding,
- **When** a turn completes or the next turn starts,
- **Then** the context bar, warning/reset thresholds, and next-turn headroom use that window. A report matches the selected alias or the single unambiguous model ID observed in assistant messages for that turn; conflicting matching entries or multiple observed model IDs are not resolved by picking an arbitrary entry. A report for another model is ignored. A new binding or model, or an absent or invalid report, uses the 1,000,000-token fallback until a matching positive report arrives. A previous valid report remains available to later turns of the same binding and model.
- **Given** rendered inputs are submitted or queued in a turn, **when** checking headroom, **then** their bytes are conservatively counted at one token per byte in addition to reported usage and fixed headroom. The existing 6 MiB input cap remains unchanged. Inputs that cannot fit are not submitted.

| Scenario | E2E suite | Test case |
| --- | --- | --- |
| CC-020 | `tests/e2e/claude-code-context-window.spec.ts` | `reported window drives UI and thresholds; missing report falls back` |

| CC-014I | Interrupt usage-limit hold | `tests/e2e/claude-code-workers.spec.ts` | `CC-014I interrupt usage-limit hold preserves pending work` |

### Scenario: uncertain Claude stdin is held
- **Given** queued mail reserved for Claude, **when** a stdin write starts but its exact echo is not durably checked, **then** the message remains unacknowledged and is held from automatic selection and reservation, including after reset or router restart. A crash immediately before the actual write may conservatively hold it.
- A refusal before the durable write-start mark leaves the input eligible. The mark is fenced to the accepted message incarnation and current binding; it is not an ACK. Existing message expiry bounds the hold. Reset, release, runtime changes and dead-letter retry do not clear it; retry of a held dead letter is rejected. A checked echo alone follows the existing ACK path.
- **Given** a held message with an older Pi direct-delivery attempt, **when** that attempt is released live or during recovery, **then** it cannot change the held message or attempt, dead-letter it, or rearm its linked reminder. The manager treats the held disposition as neither a successful release nor a persistence failure: it clears the local direct turn so subsequent work can be admitted, but does not publish delivery failure or schedule a retry; the durable message remains held and unacknowledged. The same applies when a persistence retry encounters the hold. A stale attempt for a different message incarnation cannot dead-letter or rearm the replacement.

### Lifecycle and direct-prompt acceptance clarification

- **Given** a Claude foreground execution ends by success, error, interruption, or early return, **when** its durable execution is settled, **then** its exact process grant has first been revoked and process-group cleanup joined. If cleanup cannot be confirmed, admission is fenced and the execution is not cleared as successfully settled. Pi settlement is unchanged.
- **Given** a newly started Claude process fails its initial control probe before input is submitted, **when** its cleanup cannot be confirmed, **then** the exact grant is revoked, the cleanup failure is returned and prevents durable settlement, and admission remains fenced. This also applies to a missing-conversation resume failure: a fresh-process fallback is allowed only after confirmed cleanup.
- **Given** a claimed Claude restart wake remains after a router restart, **when** its final was saved but cleanup was not durably settled, **then** the wake is unknown and cannot authorize an automatic replay or chained wake. A saved final alone remains sufficient for Pi's existing reconciliation. Fence or storage errors are returned, not bypassed.
- **Given** cleanup is requested, **when** the process does not exit promptly, **then** TERM and KILL escalation and confirmation share one five-second stop budget; unconfirmed cleanup fences admission and preserves recovery evidence.
- **Given** a direct HTTP prompt arrives during an execution, **when** it waits for the lane, **then** it is not yet durably accepted or queued. Cancellation or router crash before reservation may lose that request; callers must not infer acceptance from sending it. The documentation owner should reconcile client retry guidance separately; this change does not add a queue.
- **Given** another user input arrives during a Claude execution, **when** it cannot be independently reserved for a later execution, **then** it is not admitted to the running process's internal steer queue.

### Scenario: cleanup fails before a process is assigned to the turn
- **Given** an existing Claude process for the worker, **when** input admission or reuse preflight fails and its stop cannot be confirmed, **then** its grant is revoked, Claude admission is fenced, and the durable execution remains unsettled. No replacement process or fallback is started.

### Headroom across usage-limit retries
- **Given** Claude has reported positive input usage, **when** a synthetic error or usage-limit notice has no input usage, **then** the last reported usage remains in effect, including after reopening the session.
- **Given** a submitted input hits a usage limit, **when** that same input is retried, **then** headroom accounts for it once, not once per attempt.
- **Given** a headroom rejection occurs after input was consumed, **when** the dispatcher handles the failure, **then** it must not treat the input as an unsubmitted reservation.

### Scenario: terminating tool fences other registered calls
- **Given** a Claude turn has registered multiple tool calls, including context reset or worker self-termination,
- **When** a terminating tool's successful result is checked and saved,
- **Then** no other unexecuted call from that turn may execute, create an effect or result, or publish a result, even if it was registered earlier. A retry of a cached checked result returns the same response; a conflicting retry remains rejected. A failed or unchecked terminating result does not count as a checked terminating result.

### Scenario: checked result exceeds the turn response-cache limit
- **Given** a tool result has been durably checked, **when** adding its response would exceed the 16 MiB aggregate turn cache,
- **Then** the turn visibly fails and stops without executing further calls or repeating effects. The checked outcome remains known and durable, not UNKNOWN; any checked terminating action still runs. The overflowed response is not replayed or reconstructed. Previously cached responses are replayable only while the turn remains healthy; conflicting retries are rejected. An unchecked or crash-uncertain effect remains UNKNOWN.

### Scenario: successful restart reoffers eligible mail
- **Given** accepted eligible mail is offered while a Claude restart admission gate is closed,
- **When** both process stops succeed and the restart releases its serial lane and admission gate,
- **Then** eligible mail is offered again without another incoming event. Mail with uncertain stdin remains held.
- **Failure and synchronous-exit limitation**: A stop failure, reservation failure or replay, or failure to start the restart task retains baseline behavior and does not trigger this reoffer. An offer consumed during those gates may remain stranded until another event. Baseline failure recording and pool-wide closure behavior are unchanged; failure recording can fail and other Claude agents may be affected by a closed pool.
- **Verification**: `TestClaudeRestartReoffersMail`, `TestClaudeRestartReofferSkipsUncertainMail`, and `TestClaudeRestartStopFailureDoesNotReoffer` exercise the manager and SQLite with a fake CLI where applicable. The browser E2E `claude-code-restart-lost-wake.spec.ts` covers two gated enqueue offers and the checked ACK.
