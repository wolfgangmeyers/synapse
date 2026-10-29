# Claude Code Workers (v1)

Status: draft for review · Date: 2026-10-26, revised 2026-10-27 (one long-lived Claude process per worker) · Plan: [claude-code-workers-plan.md](claude-code-workers-plan.md)

## 1. Summary

Router-managed workers can run on Claude Code (the `claude` CLI) as well as on pi-go. A Claude Code worker is launched from the same dialog, appears in the same sidebar, and offers the same conversation experience as an embedded worker:
- live timeline, activity indicator and context bar;
- interrupt and model change;
- context reset that keeps the scratchpad and recent turns;
- scratchpad, progress and reminders;
- goals with auto-continuation;
- restart, terminate and worktrees.

A Claude Code worker is an embedded worker with a different turn engine. The router runs `claude` headless for each worker and keeps that process running while the worker is in use. Later turns start without delay, and background work Claude starts keeps running between turns. After an idle period the router stops the process, and the next message starts it again on the same conversation. Everything Claude does is recorded in the same conversation store, history and live UI feed that embedded workers use. Router tools are offered to Claude through an MCP endpoint hosted by the router.

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

### 3.1 Availability
- **Detecting the CLI.** The router detects whether the host's `claude` CLI is installed and signed in. It checks at startup, every 5 minutes, and when the provider dialog opens. When the status changes, the provider row and model list update live.
- **Provider dialog.** It shows a **Claude Code** row with one of three states:
  - "Signed in (Claude CLI, \<plan\>)"
  - "Not installed"
  - "Not signed in — run `claude login` on the router host"

  The row has no sign-in button in v1.
- **Model list.** When Claude Code is available, the list includes Claude Code models (Fable, Opus, Sonnet, Haiku), labelled as Claude Code models. In v1 they cannot be set as the router's default model.
- **When it becomes unavailable.** Claude Code models disappear from the list. An existing Claude Code worker's next turn fails visibly (§3.11).

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
- A message that arrives mid-turn is handed to the running Claude process straight away.
- Claude answers it after the current model turn finishes, as a turn of its own within the same run. It counts as received when Claude starts that turn. If the run ends before then, the message stays undelivered and is delivered again later, so it is never lost.
- Its final answer appears in the conversation like any other answer.
- Exceptions: while the Claude process is still starting, and during a recovery turn after a router restart, mid-turn messages are not handed over. They wait and are delivered as normal turns afterwards.
- This is the one intentional UX difference in v1: embedded workers fold such a message into the running turn instead.
- Interrupt stops the current turn. Messages already handed to Claude are still answered.

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
- **Calls between turns.** Background work can call router tools while no turn is running. Those calls work, except `goal_snooze`, `context_reset` and `mecha_worker_terminate`, which act on a turn and are rejected outside one.
- **No double effects.** If the same tool call is delivered twice, its effect happens once. This covers mail, images, reminders and goal changes.

### 3.6 Claude Code configuration
- **What the worker loads.** It runs in its working directory with the user's normal Claude Code configuration (user and project settings, CLAUDE.md, skills, plugins, user MCP servers), plus the router tools.
- **What the worker does not inherit.** Terminal-multiplexer variables (`TMUX`, `TMUX_PANE`) and nested-session markers (`CLAUDECODE`). Legacy hooks therefore treat the process as a non-agent context.
- **System prompt additions:**
  - the worker's identity (agent name, message address, working directory);
  - how messages arrive, and that nothing is sent automatically (use `mecha_mailbox_send`);
  - guidance on using the router tools.
- **Model.** The worker runs on the Claude Code model selected for it.

### 3.7 Process lifetime, interrupt, restart, terminate, router restart
- **One process per worker.** Each Claude Code worker has at most one `claude` process. It starts with the worker's first turn, or with the first turn after it was stopped, and keeps running between turns. Turns on a running process start without start-up delay.
- **Idle shutdown.** After 15 minutes with no turn, the router stops the process. The timeout is configurable on the router host. Nothing changes in the UI: the worker stays idle with its conversation. Its next message starts a new process that resumes the same Claude session, which takes a few seconds.
- **Background work.** Commands and subagents that Claude starts in the background keep running between turns while the process runs. They stop when the process stops: on idle shutdown, context reset, restart, terminate or router restart. Claude Code reports which background tasks are running, and idle shutdown waits while any are, for up to 2 hours past the timeout.
- **Background task finishes between turns.** Claude starts a turn by itself to look at the result. It appears in the timeline as a turn of its own: a "Background task finished" notice with the task's summary, followed by Claude's work and answer.
- **Background task finishes during a turn.** Claude handles it inside that turn. The same notice appears in that turn, and no extra turn is added.
- **Interrupt.** Stops the current turn within a few seconds. Output produced so far stays in the timeline. The worker becomes idle and keeps its conversation, and its process keeps running.
- **Restart.** Stops the worker's process. The same conversation continues from the next turn.
- **Terminate.** Stops any running turn and the worker's process promptly, then removes the worker as for embedded workers. The worktree cleanup option is unchanged.
- **Router restart.**
  - A running turn is shown as interrupted, and its tool cards read "Interrupted by restart".
  - Claude processes left over from before the restart are stopped, idle ones included, along with their background work.
  - On startup the worker gets the same interrupted-turn notice embedded workers get, and continues its conversation.
  - No tool effect is automatically repeated.

### 3.8 Model change
- The header model picker on a Claude Code worker lists only Claude Code models. A change takes effect from the next turn, and the conversation is kept.
- The running process switches model in place, so background work keeps running. Only if that switch fails does the router restart the process on the new model, which stops its background work (§3.7).
- Embedded pi-go workers never list Claude Code models.
- The router rejects any switch between engines.

### 3.9 Context reset
The triggers and the result are the same as for embedded workers.

**Triggers**
- **The worker calls `context_reset`.** It can pass an optional reason and a window of 1–100 messages (default 20). The worker's turn stops, then the reset runs.
- **Warning threshold crossed.** The worker gets a system reminder, once per epoch, telling it to save its notes to the scratchpad and call `context_reset`.
- **Reset threshold crossed.** The reset runs automatically after the turn.
- **The gear menu's Reset.**

**Result**
- A new session epoch starts. The worker's new context contains, in this order:
  1. the initial instruction;
  2. up to 20 of the most recent scratchpad entries, with pinned entries marked;
  3. the last N messages of the previous context;
  4. the reset wake prompt.
- The worker continues immediately.
- For Claude Code, the worker's old process is stopped and a new one starts on the fresh session, so background work it started stops.
- The timeline shows a "Context reset (Epoch N)" marker, and the history before it stays visible.
- The context bar drops to the new size.
- For Claude Code, the preserved messages arrive as a transcript in the first message of a fresh Claude session, and long tool outputs in it are shortened. Embedded workers keep them as structured messages.

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
- Turns Claude starts on its own between turns (§3.7) are not evaluated.
- **Who evaluates.** A router-side pi-go model, not the Claude worker: the router's default model, or Gemini 3.8 Flash if no default is set. Evaluation therefore uses no Claude usage and finishes within the existing time limit.
- **No evaluator available.** If no evaluator model is usable, the router records a visible "goal evaluation unavailable" entry and schedules no continuation. It does not loop on NOT_DONE.
- **Mid-turn changes.** A goal changed during a turn takes effect from the next model response, as for embedded workers.

### 3.11 Usage limits and failures
- **Claude usage limit.**
  - The turn in progress ends with a visible entry in the conversation: "Claude usage limit reached; resuming at \<time\>".
  - The worker pauses. Messages and goal continuations that haven't started stay queued; they are not failed and not retried early.
  - At the reset time, the worker resumes. It first continues the interrupted work, then works through the queue.
- **Other failures.** These include a missing CLI, expired sign-in, a crash or malformed output.
  - The turn ends with a visible error in the conversation, as with embedded workers.
  - The worker stays alive.
- **Process cleanup.**
  - A Claude process is stopped by idle shutdown, a session change, restart, terminate, or router shutdown.
  - If a process exits unexpectedly between turns, the next turn starts a new one.
  - On startup the router stops any Claude processes left over from before a crash or restart.

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
| Mid-turn messages folded into the running turn | ✓ | Answered right after the current turn |
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
  - Temporary MCP config files and process records are deleted when the process exits and swept at router startup. There is at most one process record per worker.
  - Idle processes are bounded by the idle timeout (§3.7).

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
                                     claude -p (stream-json, one per worker)
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
3. **Claude Code process manager and turn engine.**
   - The process manager keeps at most one `claude` process per worker, with JSON streamed in and out. It starts the process when a turn needs one and keeps it between turns. It stops the process after the idle timeout (deferred while background tasks run), on a session change, and on restart, terminate or router shutdown. Model changes are applied to the running process.
   - For each turn the engine:
     - sends the turn's input to the running process;
     - translates Claude's events into the shared conversation sink;
     - relays messages that arrive mid-turn;
     - handles interrupt and termination;
     - ends the turn once every input it sent has been answered, leaving the process running.
   - Output that arrives between turns is recorded through a turn of its own.
   - A process started on a new session (a new agent, after a reset, or after transcript loss) is seeded from the router's stored conversation. A process started on an existing session resumes it.
4. **Router MCP endpoint.** A Streamable HTTP MCP server inside the router. It:
   - authenticates the per-process token and maps it to the worker's current process and session;
   - runs router tools through the same store operations the embedded tools use, without the pi-only effect ledger;
   - de-duplicates using Claude's tool-use ID.
5. **Claude CLI probe and model table.**
   - The probe detects installation and sign-in, and feeds both the provider status and the model list.
   - The table maps each Claude Code model to the CLI model name and its context window.
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
| D | **A new `claude` process for every turn**, stopped when the turn ends | Turn boundaries are slightly simpler. But every turn pays several seconds of start-up, the user's MCP servers restart every turn, and anything Claude starts in the background is killed when the turn ends. Recovery after a router restart is no simpler: either way the router stops leftover processes and resumes the session in a new one. This was the first draft's choice and was dropped in review on 2026-10-27. |
| E | **A Node sidecar on the Claude Agent SDK** instead of driving the CLI from Go | Adds a Node runtime dependency and a second process hop. The pieces we need are already reachable from Go: streamed messages and events, control interrupt, and MCP. All were verified live (§6). |
| F | **Reset by writing a synthetic Claude transcript and resuming it** | The preserved turns would stay structured. But it depends on Claude Code's undocumented internal transcript format, which can change in any CLI release. A text transcript in the first message is robust and was verified to work. |
| G | **Goal evaluator as a one-shot `claude -p` call** | Every evaluation would cost several seconds of start-up inside a one-minute budget, consume Claude usage, and need a new response adapter. The helper model reuses the existing evaluator code path and credentials unchanged. |
| H | **A stdio MCP subprocess per worker** that proxies to the router | An extra process per worker and an extra hop for no benefit. Claude Code calls an HTTP MCP endpoint directly with a bearer header (verified). |
| I | **A persistent per-worker MCP token** | It would have to be stored, rotated and revoked across router restarts. A per-process token lives exactly as long as the process that uses it, with nothing to persist. |
| J | **Locked-down Claude configuration** (`--setting-sources project --strict-mcp-config`) | More predictable, but it would drop the user's skills, plugins and MCP servers, which are a reason to choose Claude Code. Legacy hook interference is handled by not passing on the tmux and nested-session environment variables instead. |

**Chosen design.** The Claude Code engine inside the embedded runtime, with one long-lived process per worker and idle shutdown (§4). It satisfies G1–G4, reuses the most existing behavior, and confines new code to the engine, the MCP endpoint, model plumbing and small UI changes.

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

- **AC1. Availability.** With `claude` installed and signed in, the provider dialog shows Claude Code as signed in and the model list contains the Claude Code models. With it missing or signed out, it shows the matching state, the models are absent, and launch is refused.
- **AC2. Launch.** Launching with a Claude Code model, with and without a worktree, starts a worker whose first turn is the initial instruction. The worker appears in the sidebar like an embedded worker.
- **AC3. Conversation.** A composer message with an image produces a turn in which Claude receives both text and image. Its thinking, tool cards (running then finished) and final answer appear in the timeline. The activity indicator and context bar update. Nothing is mailed automatically.
- **AC4. Mid-turn messages.** A message sent during a turn is marked received when Claude starts it and gets its own answer after the current turn. If the run ends first, it is redelivered and never stuck, including across a context reset. During start-up and recovery turns it waits for normal delivery.
- **AC5. Router tools.** Each v1 router tool works from a Claude Code worker with embedded-equivalent effects. A repeated call with the same tool-use ID has no second effect. A call bound to an old session is rejected.
- **AC6. Interrupt.** Interrupt ends a running turn within 5 s, keeps the partial output, and leaves the worker idle with its conversation.
- **AC7. Restart, terminate, router restart.** Restart continues the conversation. Terminate stops a running turn and the worker's Claude process within 5 s. A router restart mid-turn produces the interrupted-turn notice and continuation, with no repeated tool effects and no leftover `claude` processes.
- **AC8. Model change.** A Claude Code worker can switch between Claude Code models and keeps its conversation. Cross-engine switches are rejected by both the UI and the router.
- **AC9. Context reset.** Each of the three triggers produces a new epoch whose first Claude message contains the instruction, scratchpad and last N messages, in order, followed by the wake prompt. The epoch marker appears, the context bar drops, and no reset loop occurs when the post-reset context is above the threshold.
- **AC10. Goals.** Setting a goal on a Claude Code worker leads to evaluation after each normal turn. NOT_DONE produces continuation turns, DONE turns the goal off, snooze delays the continuation, and the cap and circuit breaker hold.
- **AC11. Usage limit.** A reported usage limit pauses the worker until the reset time without failing or dead-lettering queued messages, then resumes.
- **AC12. Isolation.** The Claude process environment lacks `TMUX`, `TMUX_PANE` and `CLAUDECODE`. MCP tokens never appear in argv or logs. Temporary files are removed when the process exits and at startup.
- **AC13. No regression.** All existing embedded pi-go tests pass unchanged, and embedded workers behave as before, with one intentional exception. Mid-turn message reservations that were never consumed are released when the turn ends. Today, if a reset happens in between, such a message blocks all later mail to that agent. The fix is in shared code and applies to pi workers too.
- **AC14. Process lifetime.**
  - Consecutive turns use the same Claude process.
  - After the idle timeout with no turn, the process exits and the worker stays idle. The next message starts a new process that resumes the conversation.
  - A background command started in one turn is still running in the next turn if that turn comes before the idle timeout. It stops when the process stops.
  - Idle shutdown waits while a background task is running, for at most 2 hours past the timeout.
  - A background task that finishes between turns produces a turn of its own in the timeline, opened by the "Background task finished" notice. One that finishes during a turn adds the notice to that turn and no extra turn.
  - A model change switches the running process in place.
  - Context reset, restart and terminate each stop the old process.
  - Between turns, `goal_snooze`, `context_reset` and `mecha_worker_terminate` are rejected and the other router tools work.
