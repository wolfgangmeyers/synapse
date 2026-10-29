# Implementation Plan: Claude Code Workers (v1)

Spec: [claude-code-workers-spec.md](claude-code-workers-spec.md) · Status: draft, pending spec approval · Date: 2026-10-26, revised 2026-10-27

- Revised 2026-10-27: one long-lived `claude` process per worker with idle shutdown, replacing one process per turn (D3, D15, D16, spikes S6–S9, §5).
- Revised 2026-10-28: spikes S1–S4 and S6–S9 run and folded in (§2). Only the optional S5 is still open.
- Router line numbers refer to `main` at `f77c617`. They will drift, so treat a function name as authoritative over its line number.
- UI references name the component and function, because the header files change often.
- Paths are under `router-proxy/internal/` unless they start with `web/`.
- An independent fact-check against `f77c617` has been folded in.

## 0. Constraints

- Follow the dev-loop workflow: spec approval, then plan review, then TDD, then coverage, then review. Work on `main` in `mecha-developer`.
- Automated tests never call an LLM or the real `claude`. Behavioral tests run a real router against a fake `claude` binary (§13).
- No new accumulating store without bounded retention (spec §3.13).
- AC13 must hold. The engine seam (Phase 1) lands as its own commit, with the full existing suite green, before any Claude code exists. The single intentional shared behavior change, the steer-reservation release (Phase 1, item 7), lands as a separate commit with its own regression test.

## 1. Key decisions

| ID | Decision | Spec |
|---|---|---|
| D1 | The engine is identified by model provider `claude-code`. Agents stay `runtime='embedded'`. There is no migration of `managed_agents`. | §4.1, Alt A |
| D2 | `runEmbeddedSessionTurnScoped` keeps its shared prologue, prompt dispatch and epilogue. Only session construction branches. Call sites use a small `embeddedTurnSession` interface instead of `*coding.Session`. | §4.2 |
| D3 | One long-lived `claude -p` process per Claude Code worker, owned by the Manager and shared by that worker's turns. A turn borrows it and gives it back once every input written during the turn has its `result`; stdin stays open. The process is stopped after an idle timeout (default 15 min, `MECHA_CLAUDE_IDLE_TIMEOUT`), on a binding change, and on restart, terminate and router shutdown. A model change uses the `set_model` control request on the running process (S8). | §3.7, §3.8, §4.3, Alt D |
| D4 | The Claude session ID is the embedded binding's `SessionID`, a UUIDv4 from `managerIDs.Next`. If it isn't a UUID (only in tests, through the `"%s-s%d"` fallback in `ExecuteContextReset`), the engine uses a UUIDv5 derived from it. A process started on a binding that has not started is seeded and uses `--session-id`. A process started on a started binding uses `--resume`. Turns on a running process only write input. A new nullable column records "Claude session started" per binding. | §3.9, §4.3 |
| D5 | The mirror (`embedded_entries`) keeps Claude's raw tool names (`Bash`, `mcp__mecha__goal`). That keeps the goal-call and unknown-effect checks in `AppendMessage` inert for MCP tools. One bare-name normalizer, `embeddedBareToolName` (strips `mcp__mecha__`), is used at every place that compares router tool names for bookkeeping (Phase 1, item 5). History rows show display names through a per-persistence mapping, which is the identity for pi. | §3.3 |
| D6 | The ACK for a mail message is written when Claude echoes it (`--replay-user-messages`). For a mid-turn message that is when Claude starts answering it (verified). Steer matching is strict for this engine: bytes must match, with no pop-the-head fallback. | §3.4 |
| D7 | Router tools are served by a router-hosted Streamable HTTP MCP endpoint. A fresh token is minted for each process and kept only in memory. Calls are accepted while that process runs on the current binding, between turns too, except the turn-scoped tools (`goal_snooze`, `context_reset`, `mecha_worker_terminate`), which need a turn to hold the process. The de-duplication key is `_meta["claudecode/toolUseId"]`. A repeated call looks up its earlier effect by derived ID before acting. | §3.5, Alt H/I |
| D8 | The provider-entry duties now inside the pi `StreamFn` move into one helper with the same conditional structure, and both engines call it. For auxiliary requests, notice start, owner renewal and the goal-policy read still run; only the headroom check, the non-mail marker and the receipt are skipped. | §3.10 |
| D9 | The goal evaluator for Claude Code workers is a router-side pi-go model: the router default model (`store.GetDefaultModel`) if it is a pi-go model, else `antigravity/gemini-3.8-flash`. If no evaluator stream can be built, the turn records "goal evaluation unavailable" and schedules no continuation. | §3.10, Alt G |
| D10 | Context size comes from Claude's reported usage. All four Claude Code models run with a 1M window (S1), so the existing thresholds and precedence apply unchanged and there is no Claude-specific threshold tier. A post-reset loop guard covers thresholds set below a fresh session's size. | §3.9 |
| D11 | `context_reset` and `mecha_worker_terminate` copy pi's `Terminate`: the turn stops after the tool result, with no final assistant. pi's rule is at `agent/loop.go:580`. Any partial assistant produced by the engine's own interrupt is discarded, not appended. | §3.9 |
| D12 | `claude-code` is a *virtual* provider. Its status and models are merged in at read time and never persisted, because the provider tables have `CHECK provider IN (…5 names)` (`migrations/086_google_auth_methods.sql`). The UI accepts five or six providers, so older routers keep working. | §3.1 |
| D13 | A usage limit holds the agent's lane in memory until `resetsAt`, then resumes the interrupted work. | §3.11 |
| D14 | No automatic replies, matching current embedded behavior (`0fc20bb`). The final answer is recorded as the worker's answer in history, and the system prompt tells Claude to use `mecha_mailbox_send` to message agents. | §3.3 |
| D15 | When a background task finishes while no turn holds the process, Claude starts a turn of its own (S6). The router records it through an *unprompted turn*, which runs on the agent's lane through the normal shared prologue and epilogue, writes no input, and records events until `result`. Its user entry is a notification entry rendered from the task's `task_notification` summary. A task that finishes during a turn arrives in that turn as a replayed user event with `origin.kind == "task-notification"`, and is appended as the same kind of notification entry. Notification entries never match inputs, ACKs or steers. Unprompted turns get no goal evaluation. | §3.7, §3.10 |
| D16 | Idle shutdown waits while the last `system/background_tasks_changed` lists any task (S7), for at most 2 hours past the idle timeout. | §3.7 |

## 2. Phase 0: Spikes

Run on 2026-10-28 against `claude` 2.1.308, with probes in a scratch directory. Re-run S6–S8 as a smoke check on CLI upgrades.

| ID | Question | Result | Feeds |
|---|---|---|---|
| S1 | Which context window does each alias run with in `-p`? | **Done.** `modelUsage[...].contextWindow` in `result` is 1,000,000 for every alias: `fable` → `claude-fable-5-1`, `opus` → `claude-opus-5-5`, `sonnet` → `claude-sonnet-5-5`, `haiku` → `claude-haiku-5-5`. No `[1m]` suffix is needed. | §7 model table, D10 |
| S2 | Does `--permission-mode bypassPermissions` run tools in `-p` with no prompt and no refusal? Or do we need `--dangerously-skip-permissions --settings '{"skipDangerousModePermissionPrompt":true}'`, as `worker_launch.py` uses today? | **Done.** `--permission-mode bypassPermissions` is enough. With `--input-format stream-json`, `system/init` reports `permissionMode: "bypassPermissions"`. A Bash `echo`, a Write to a plain file, and both a Write and a Bash redirect into `.claude/` all ran, with no `control_request`, no `permission_denials`, empty stderr and exit 0. Forcing `skipDangerousModePermissionPrompt: false` through `--settings` changed nothing, so headless mode needs neither that setting nor `--dangerously-skip-permissions`. Control run without the flag: the `echo` ran, but every write was refused with a `system/permission_denied` event, an `is_error` tool result and an entry in `result.permission_denials`. Nothing hung, because no permission prompt tool is configured. | §4 argv |
| S3 | What does the stream look like when the usage limit is hit? | **Answered from the CLI's own schema; not yet seen live.** `rate_limit_event.rate_limit_info.status` is `allowed`, `allowed_warning` or `rejected`. A refused request produces an assistant event with `is_api_error_message: true`, `api_error: "usage_limit_reached"` and `api_error_params.rate_limit_info` (`status: "rejected"`, `resetsAt`, `rateLimitType`). These fields are marked internal, so parsing stays defensive (§10). Confirm when a limit is hit naturally. | §10 |
| S4 | Does `--resume <id>` with a missing transcript fail fast with a recognizable error? | **Done.** In about 1 s: stderr `No conversation found with session ID: <id>`, a `result/error_during_execution` with `num_turns: 0`, and exit code 1. | §5 resume fallback |
| S5 | Optional: does `interrupt_send_now_v1` allow real mid-turn steering? | **Open, optional.** | later improvement only |
| S6 | What does a running process do when a background task finishes? | **Done.** *While idle:* `system/task_updated` (`status: completed`), `system/task_notification` (`task_id`, `tool_use_id`, `status`, `summary`, `output_file`), `system/background_tasks_changed` (`tasks: []`), then a turn of Claude's own: `system/init`, assistant, `result/success`. No user event precedes it. *During a turn:* the same `system/*` events, then a replayed user event with `isReplay: true` and `origin: {kind: "task-notification", producer: "session-task", runId}` whose text is a `<task-notification>` block. Claude handles it inside the running turn, which still ends with one `result`. Background subagents behave the same; their internals arrive with `parent_tool_use_id` set, and their own tool runs are marked `owned_by_subagent`. | D15, §4 translator, §5 unprompted turns, fake `claude` |
| S7 | Does Claude Code report which background tasks are still running? | **Done.** `system/background_tasks_changed` carries the full running set (`task_id`, `task_type` such as `local_bash` or `local_agent`, `description`) whenever it changes. The CLI's schema describes it as a level signal: replace the set on each event and reset it to empty when a process starts. A `stop_task` control request also exists. | D16 |
| S8 | Can a running process switch models in place? Does `--resume` with a different `--model` work? | **Done.** `{"type":"control_request","request":{"subtype":"set_model","model":"sonnet"}}` returns `control_response` success with the resolved model, and the next turn runs on it. The CLI also emits a non-replay user event recording a `/model` local command, which the translator ignores. `--resume <id> --model haiku` on a Sonnet session works and keeps the conversation. | §5 model change |
| S9 | How much memory does an idle process hold with full user configuration? | **Done.** About 365 MiB RSS, flat from 15 s to 5 min idle, with Wolfgang's full configuration (12 MCP servers, all remote or in-process, and 5 plugins) and no child processes. Ten idle workers cost about 3.6 GiB until their idle timeouts. | idle default, risks |

Already verified (spec §6):
- stream-json works across several turns;
- seed recall works;
- the control interrupt works;
- mid-turn echo timing;
- MCP bearer header and `toolUseId` in `_meta`;
- `server/discover`;
- usage fields;
- the 42.5k baseline;
- `claude auth status --json`;
- `rate_limit_event` fields.

## 3. Phase 1: Engine seam and shared fixes

**1. New `managed/embedded_engine.go`**
```go
type embeddedTurnSession interface {
    Run(ctx context.Context, text string) (*coding.RunResult, error)
    RunMessages(ctx context.Context, msgs []agent.AgentMessage) (*coding.RunResult, error)
    Continue(ctx context.Context) error
    // CanSteer reports whether a steer would be accepted now; checked before
    // the steer reservation is written. TrySteer may still refuse (race).
    CanSteer() bool
    TrySteer(msg agent.AgentMessage) bool
    Abort()
    LastAssistantText() string
    ContextTokens() int
    HasUser() bool
}
```
`piTurnSession` wraps `*coding.Session` with explicit methods, because `Run` is variadic (`coding/session.go:781`):
- `CanSteer`/`TrySteer` always return true and call `Steer`, so pi behavior is unchanged;
- `Abort` guards a nil `Agent`, because test helpers build `&coding.Session{}`;
- `ContextTokens` is `embeddedProjectedTokens(History())`;
- `HasUser` is computed from `History()`, as at `embedded_session.go:~1950`.

**2. Switch these call sites to the interface**
- `lane.activeSession` and `m.activeEmbeddedSessions`
- `InterruptAgent`
- `trySteerActiveSession`: check `CanSteer()` before `ReserveEmbeddedMail`; if `TrySteer` returns false, release the reservation and fall back to normal dispatch.
- `steerPrompt`: if `TrySteer` returns false, run as a normal prompt after the turn.
- `runClaimedWake`
- `nudgeEmbeddedSessionIfNeeded`
- `embedded_restart_router.go`
- `embedded_workflow_transition.go`: `SetTools` behind a `piTurnSession` type assertion.
- `checkContextResetAfterTurn`: takes `(tokens int, model *ai.Model)` instead of a session.
- the turn's deferred epilogue: reads `ContextTokens()` and the resolved model.
- the `hasUser` computation: uses `HasUser()`.

**3. Provider-entry helper (D8).** Move the provider-entry block from the pi `StreamFn` (`embedded_session.go:~1474-1520`) into `m.enterEmbeddedProvider(ctx, p, wake, auxiliary bool) (*store.EmbeddedGoalAppliedReceipt, error)`. Keep today's exact conditions:
- notice start always runs;
- `RenewEmbeddedOwner` always runs;
- `ManagedGoalPolicyFor` always runs;
- headroom check, non-mail marker and receipt are skipped when `auxiliary`.

The pi `StreamFn` calls the helper, so pi is unchanged.

**4. History tool-name mapping.** Add `historyToolName func(string) string` to `embeddedPersistence`. It defaults to the identity. Apply it where `AppendMessage` writes tool rows and in the tool-start row (§5).

**5. Bare-name normalizer.** `embeddedBareToolName(name)` strips `mcp__mecha__`; it is the identity for pi names. Use it at:
- the `toolExecuted` exclusions in `AppendMessage` (`~:344`) and `BeforeToolExecute` (`~:1653`). Otherwise calling `goal` resets `consecutiveContinuations` and the continuation cap never trips.
- the synthesis-nudge check (`~:985`)
- the "already mailed" suppression (`store/embedded_reply.go:~179`)
- the empty-turn circuit breaker's `goal`/`goal_snooze` counting (`~:2203-2231`)

Leave the raw name at the goal-call check (`~:459`).

**6. Branch point.** Immediately after the role lookup in `runEmbeddedSessionTurnScoped`:
- `claude-code` agents build a `claudeTurnSession` (§5).
- They skip tools, `StreamFn`, `NewSession`, `BeforeToolCall` and `BeforeToolExecute`.
- They skip `p.restore()` in favour of setting the head only: the last entry of `ReopenEmbedded(binding)`, or `(0, "genesis")`.
- They skip `TransformContext`; the engine renders mail itself (§5).
- They rejoin at active registration.

**7. Shared fix: release unconsumed steer reservations.** This is the one intentional shared behavior change (spec AC13).
- In the turn's deferred epilogue, for every attempt still in `p`'s steer queue (reserved through `trySteerActiveSession` and never appended or ACKed), delete its `embedded_attempts` row with a new store function, `ReleaseEmbeddedSteerAttempt(binding, attemptID)`. It deletes only un-ACKed rows held by that binding.
- Then call `OfferDirect`.
- Today these rows survive. After a reset changes the binding, `ReserveEmbeddedMail` fails with "reserved by another binding" (`store/embedded_router.go:~1440`), and because `NextEmbeddedMail` returns the oldest queued message, all later mail to the agent is blocked until that message expires.
- Regression test: a steer is reserved, the turn ends before consumption, and a reset follows. The message is delivered in the new epoch and later mail flows. Run it on the pi engine as well.

**Tests.** `go test ./...` green with no test edits beyond type changes, which proves AC13 for the seam (items 1–6). Item 7 lands as its own commit with its regression test.

## 4. Phase 2: Claude process and stream translation

**`managed/claude_process.go`**
- **Command line:** `claude -p --input-format stream-json --output-format stream-json --verbose --replay-user-messages --model <alias> --permission-mode bypassPermissions --mcp-config <file> --append-system-prompt-file <file> (--session-id <uuid> | --resume <uuid>)`.
  - S2 confirmed `--permission-mode bypassPermissions` on its own. Do not pass `--permission-prompt-tool`: without one, any refusal comes back as an error tool result instead of a `control_request` the engine would have to answer.
  - `cwd` is the agent's working directory.
- **Binary:** `MECHA_CLAUDE_BIN`, else `PATH`, else `~/.local/bin/claude`. The router is often started from a shell whose `PATH` lacks `~/.local/bin`.
- **Environment:** `os.Environ()` minus `TMUX`, `TMUX_PANE`, `CLAUDECODE` and `CLAUDE_CODE_ENTRYPOINT` (AC12).
- **Process group:** `Setpgid`. Reuse the probe and reap pattern in `process.go:~180-240`.
- **Process record:** `<SessionRoot>/claude-proc/<agent>.json` holds `{pid, pgid, started_at, binding}`. It is written after start and removed after exit, so it exists for the life of the process.
- **stdin:** a single locked writer of JSON lines.
- **stdout:** a scanner with a 16 MiB line limit, run by one reader goroutine for the whole life of the process. The reader hands events to the turn that holds the process, or to the idle handler when no turn does (D15).
- **Exit watcher:** on any exit, revoke the MCP grant, delete the process's files and record, fail the turn that holds the process if there is one, and log an exit that happened between turns.
- **stderr:** a 64 KiB ring buffer.
- **`Interrupt()`:** writes `{"type":"control_request","request_id":…,"request":{"subtype":"interrupt"}}` and waits for `control_response`.
- **`Stop()`:** close stdin, wait 5 s, SIGTERM the group, wait 2 s, then SIGKILL the group.
- **Version gate:** the `system/init` `capabilities` must include `interrupt_receipt_v1`. Otherwise fail with "Claude Code CLI too old".

**`managed/claude_stream.go`** decodes events and translates them to `ai` messages:

| Event | Handling |
|---|---|
| `system/init` | Record the model ID. Check `session_id` matches. One arrives per Claude turn. If no turn holds the process, it starts an unprompted turn (§5, D15). |
| `system/compact_boundary` | Write a history row of kind `compaction` (§8). |
| `system/background_tasks_changed` | Replace the process's running-task set (D16). The set starts empty when the process starts. |
| `system/task_started` | Remember task IDs marked `owned_by_subagent`, so their notifications are ignored. |
| `system/task_notification` | For a task not owned by a subagent: if no turn holds the process, keep its `summary` for the unprompted turn's notification entry (D15). During a turn, ignore it, because the replayed user event carries it. |
| `system/permission_denied` | Log at warn. It should not occur under `bypassPermissions` (S2), so seeing it means the flag did not take effect. The matching error `tool_result` already shows the refusal in the timeline. |
| `system/status`, `system/thinking_tokens`, `system/task_updated`, `system/task_progress`, `post_turn_summary`, `api_retry`, `hook_*` | Ignore; log at debug. |
| `assistant` (main thread, `parent_tool_use_id == null`) | Coalesce consecutive events with the same `message.id` into one `ai.AssistantMessage`: text → `TextContent`, thinking → `ThinkingContent`, `tool_use` → `ToolCall{ID, Name: raw, Arguments}`. Record usage. |
| Stop reason | A message with tool calls gets `StopToolUse`. A text-only message is held until the next event: `result/success` makes it `StopStop`; `result` after a user interrupt makes it `StopAborted`; any other `result` error makes it `StopError` with the result text. An interrupt the engine sent for D11 discards the held or partial message. If a non-`result` event follows a text-only message, which is not expected, its text becomes leading text of the next assistant message. |
| Assistant fields | Always fully populated, because `embeddedCheckedStop` (`store/embedded_restart_wake_origin.go:44-80`) requires them: `api="claude-code"`, `provider="claude-code"`, `model`, the full `usage` with zero costs, and `timestamp`. |
| `user` with `isReplay` and no `origin` | Matches the oldest pending input (FIFO, single writer) and triggers that input's `AppendMessage` (§5). |
| `user` with `isReplay` and `origin.kind == "task-notification"` | A background task finished during this turn. Append a notification entry (D15). It does not touch the input FIFO. |
| `user` with neither `isReplay` nor `tool_result` blocks | Ignore. An example is the `/model` local-command record emitted after `set_model` (S8). |
| `user` with `tool_result` blocks | `ai.ToolResultMessage{ToolCallID, ToolName: raw name from the tool_use map, Content: text blocks joined (images become "[image]"), IsError}`, passed as a value. |
| `parent_tool_use_id != null` | Subagent internals. Ignored in v1: not mirrored and not counted in usage. |
| `rate_limit_event` | `rate_limit_info.status == "rejected"` raises the usage-limit signal with `resetsAt` (§10, S3). |
| `assistant` with `is_api_error_message` and `api_error == "usage_limit_reached"` | Raises the usage-limit signal with `api_error_params.rate_limit_info.resetsAt` (S3). It is not appended as an answer. |
| `result` | Ends one Claude turn. Error subtypes carry the error text. |
| `stream_event`, unknown types | Ignore. |

- **Context tokens** are taken from the last main-thread assistant usage: `input_tokens + cache_creation_input_tokens + cache_read_input_tokens`.
- **Display names** (`historyToolName` for this engine):
  - `Bash→bash`, `Read→read`, `Edit/MultiEdit/NotebookEdit→edit`, `Write→write`, `Grep→grep`, `Glob→glob`, `WebSearch→web_search`, `WebFetch→web_fetch`, `Task/Agent→task`, `TodoWrite→todo_write`;
  - `mcp__mecha__X→X`;
  - any other `mcp__s__t` becomes `s_t`.

**Tests.** Table-driven translator tests over recorded stream fixtures: tool round trip, text-only final, user interrupt partial, D11 discard, error result, coalescing, subagent events ignored, compaction, rate limit. Process tests use the fake binary (§13) for env scrubbing, process-group kill, version gate, stderr cap, the exit watcher, and the reader handing events to the idle handler when no turn holds the process.

## 5. Phase 3: Claude process manager and turn engine

**`managed/claude_worker_process.go`: the process manager (D3)**
- `m.claudeProcs` maps an agent ID to at most one `*claudeWorkerProcess`. It holds the process, its binding, model alias, MCP grant, files, state (`starting`, `held`, `idle`, `stopping`) and idle timer.
- **`acquire(ctx, agent, binding, model)`** is called by the turn engine on the agent's lane.
  - A running process on the same binding is reused. If its model differs from the agent's model, `acquire` sends `set_model` and waits for the `control_response` (S8). If that fails, it stops the process and starts a new one with `--resume --model <new>`.
  - A process on another binding (any reset trigger, or transcript loss) is stopped first.
  - Otherwise a new process starts. If the binding has not started (D4), the engine builds the seed (§8) and starts with `--session-id`; otherwise it uses `--resume`. If resume fails as "not found" (S4: stderr `No conversation found with session ID`, or `result/error_during_execution` with `num_turns: 0` before any echo), it clears the marker, reseeds and relaunches once.
  - It cancels the idle timer and marks the process `held`.
- **`release(proc)`** at turn end marks the process `idle` and arms the idle timer.
- **Idle timer (D3, D16).** On expiry, under the manager lock: if the process is still `idle`, mark it `stopping` and call `Stop()`. If the running-task set is not empty, re-arm instead, up to the 2-hour cap. An `acquire` that finds a `stopping` process waits for it to exit, then starts a new one.
- **`stopWorkerProcess(agent)`** is the one stop path, used by reset, restart, terminate, cross-binding changes, a failed `set_model`, and router shutdown.
- **Unprompted turns (D15).**
  - When the stdout reader sees a main-thread `system/init` while the process is `idle`, it buffers that and later events and calls `m.startEmbeddedUnpromptedTurn(agent)`. That runs `runEmbeddedSessionTurnScoped` on the lane with an unprompted marker, the way goal continuations start. Subagent events (`parent_tool_use_id` set) and `system/task_*` bookkeeping never start one.
  - The engine first appends the notification entry: `[Background task finished] <summary>`, one line per `task_notification` kept since the process went idle. It then replays the buffered events and reads until `result`.
  - **Notification entries.** These and the mid-turn `task-notification` replays (§4) are appended through the plain-prompt path of `AppendMessage`, with no mail ACK or steer pop. They are rendered as a notice row in the timeline.
  - If a normal dispatch takes the lane first, its turn records an unprompted segment that precedes its own input's echo the same way, before its input.
  - `evaluateEmbeddedTurn` skips unprompted turns.

**`managed/claude_engine.go`.** `claudeTurnSession` implements `embeddedTurnSession` for one embedded turn. It borrows the worker's process from the process manager.

**Rendering inputs for Claude**
- Mail user messages are stored as raw `embedded-mail/v1` JSON (`store/embedded_router.go:~1493,2057`).
- Claude's stdin gets text from `unwrapEmbeddedMailText` (`embedded_automatic_fit.go:~173-192`) plus image content blocks loaded from the message's attachments. Reuse the attachment loading in `buildSteerUserMessage` (`embedded_mail.go:~178-251`). This is what AC3's image requirement depends on.
- The mirror always gets the **original** value, so the ACK byte-match holds.
- Plain prompts, continuation prompts and notices are sent as text.

**`RunMessages(ctx, msgs)`**
1. `acquire` the worker's process. A seed is built only when a new process starts on a binding that has not started.
2. Write each rendered input to stdin. The first input on a seeded session is one user message: the seed blocks followed by the input's blocks.
3. On each echo, call `p.AppendMessage(ctx, original value)`. That writes the mail ACK and the strict steer ACK through the existing paths; the caller has already set `p.mailMessage` and `p.mail`, as in today's mail branch. Then:
   - call `m.enterEmbeddedProvider` (D8);
   - after the first echo, mark the binding started and allow steering;
   - keep the returned receipt in a turn-scoped variable for `evaluateEmbeddedTurn`.
4. For each translated assistant message: call `enterEmbeddedProvider` (receipt refresh), then `p.AppendMessage`. For each tool call, write the tool-start history row, ported from the `BeforeToolExecute` block (`embedded_session.go:~1640-1667`):
   - move `currentTurnAssistantText` into `toolCompanionTexts[id]`;
   - set `toolExecuted` unless `embeddedBareToolName` is `goal`, `goal_snooze` or `context_reset`;
   - write `RecordManagedCompleteHistoryWithPresentationAndInput(..., "Running "+display, "tool:<agent>:<id>", Outcome "active", input)`;
   - publish it.
5. Append tool results with `p.AppendMessage`. That updates the same history row to succeeded or failed.
6. If the result is from a turn-ending tool (D11) and not an error: send `Interrupt()`, drain to `result`, discard any partial assistant, and return success.
7. Any `AppendMessage` error, for example a fence lost to terminate, triggers `stopWorkerProcess` and returns the error.
8. Return when every input written during this turn has produced its `result`, then `release` the process. stdin stays open. `CanSteer()` returns false from this point.

**Other methods**
- **`Run(text)`** is `RunMessages([UserMessage{text, ts}])`.
- **`Continue(ctx)`** resends the rendered last durable user entry without appending it. It is used by interrupted wakes and `resumeUnentered`.
- **`CanSteer()`** is true only when all of these hold:
  - the process is running;
  - the first input has been echoed;
  - the turn has not reached step 8;
  - the turn is neither a claimed wake nor an interrupted-notice turn (`p.wake == nil && p.notice == nil`).

  Steers during wake or notice turns are rejected at `embedded_session.go:~483-485,539-559`, so those messages wait for normal dispatch (spec §3.4).
- **`TrySteer(msg)`**
  - Rechecks `CanSteer()` under the engine lock.
  - Writes the rendered message to stdin and pushes the original value onto the pending FIFO.
  - The echo arrives when Claude starts answering it. `AppendMessage` then ACKs it through `popSteerAttempt` (strict mode, D6) and switches `p.mailAttempt`.
  - A second `StopStop` final within the same mail, prompt or continuation turn is accepted, because finals are unique per attempt (`store/embedded_reply.go:~86-90`). The fact-check confirmed this.
- **`Abort()`**
  - Sends `Interrupt()` and waits up to 5 s for `result`. The process then stays running. If no `result` arrives in time, it calls `stopWorkerProcess`.
  - Returns an error wrapping `context.Canceled`, so `ResetEmbeddedAgentInterrupt` runs.
  - The partial assistant is appended with `StopAborted`, matching pi's synthetic aborted message.
- **`HasUser()`** is true if any user entry exists in `ReopenEmbedded(binding)` or one has been appended in this turn. Without it, the initial instruction would be re-sent on bare and recovery turns.

**Terminate, restart and reset**
- **Terminate.** In the termination entry, before it waits on `lane.serial` (`supervisor_termination_s5.go`): if the agent is `claude-code`, `Abort()` any active session, then call `stopWorkerProcess`. This also covers an idle process.
- **Restart.** The per-agent restart path calls `stopWorkerProcess`. Find it from the UI's restart action at implementation time.
- **Gear-menu reset.** It (`embedded_reset.go:~57`) runs `ExecuteContextReset` without `lane.serial`. For `claude-code` agents, first `Abort()` the active session and wait for the turn to exit. Steer reservations are then released by Phase 1 item 7 before the epoch changes.
- **Every reset trigger.** The reset changes the binding, so the wake turn's `acquire` stops the old process and starts a seeded one.

**System prompt file**
- It contains:
  - a Claude-specific base;
  - the identity block from `buildEmbeddedWorkerSystemPrompt`;
  - how messages arrive (`[Direct message from X]: …`);
  - "your final answer is shown to the user; nothing is sent automatically; use `mecha_mailbox_send` to message other agents";
  - each router tool's prompt guidelines.
- It is written to `<SessionRoot>/claude-mcp/<agent>-<process>.prompt` (0600) when the process starts and deleted when it exits.

**Migration `store/migrations/111_embedded_engine_session.sql`:** `ALTER TABLE embedded_sessions ADD COLUMN engine_session_started_at TEXT;`. Add store getter and setter. Use the next free number at implementation time.

## 6. Phase 4: Router MCP endpoint and tools

**Transport, `transport/mcp_server_http.go`.** Route `/v1/mcp`. Reuse the JSON-RPC structs in `mcp/types.go`.

| Request | Response |
|---|---|
| `POST` from a non-loopback `RemoteAddr` | 403 |
| Missing or unknown `Authorization: Bearer` | 401 |
| `GET` or `DELETE` | 405 |
| Body over 6 MiB | 413 |
| `initialize` | The requested `protocolVersion` if supported, else the latest supported; `capabilities.tools`; and a `Mcp-Session-Id` header |
| `notifications/*` | 202 |
| `ping`, `server/discover` | `{}` |
| `tools/list` | The tool list |
| `tools/call` | `{content, isError}` |
| Anything else, or a batch | -32601 / -32600 |

**Grants, `managed/claude_mcp.go`.**
- `mintClaudeMCPGrant(agentID, binding, processID)` returns a token when the process starts: 32 random bytes, base64url. Only `sha256(token)` is kept, mapped to the grant.
- The token goes only into the 0600 config file `{"mcpServers":{"mecha":{"type":"http","url":"http://127.0.0.1:<port>/v1/mcp","headers":{"Authorization":"Bearer <token>"}}}}`.
- The grant is revoked at process exit.

**Every tool call does this first:**
- the grant binding must equal `m.embeddedOwners[agent].binding`, and the grant's process must be the agent's current process and not `stopping`; otherwise the error is "stale session" (AC5);
- `goal_snooze`, `context_reset` and `mecha_worker_terminate` also need the process to be `held` by a turn; otherwise the error is "only available during a turn" (AC14). Other tools work between turns, for example from background subagents;
- `RenewEmbeddedOwner`;
- `admitWorkflowStateTool` with the bare name;
- `schema.Check(args)`;
- a 60 s deadline.

The de-duplication key is `k = _meta["claudecode/toolUseId"]`, or `sha256(Mcp-Session-Id|jsonrpc id)`. Derived ID = `hex(sha256(agent|session|epoch|k))[:32]`. **Look up before acting.** `Enqueue` replay compares `CreatedAt` (`store/delivery.go:~922`), so a redelivered call with a new timestamp would conflict instead of replaying. If a message with the derived ID already exists, return the earlier result without calling `Enqueue` or writing attachments.

**Tools, `managed/claude_mcp_tools.go`.** Schemas are reused from the existing tool constructors where they can be separated out. No schema has a top-level `oneOf`.

| Tool | Handler | Skipped pi ledger pieces |
|---|---|---|
| `mecha_mailbox_send` | `embeddedAddressedArguments`, then look up the derived ID, then `store.Enqueue(Envelope{ID: derived, From: agent, To, Subject, Body}, Conversation{Body})`, then `publishEmbeddedToolMail` | `admitted`, `StartEmbeddedToolInvocation`, `EnqueueEmbeddedToolMail` |
| `mecha_send_image` | Look up the derived ID **before** writing attachments. Then the read/decode logic from `embedded_send_image.go`, `attachments.WriteBatch`, `Enqueue` with attachments (pattern in `transport/http.go:~618-660`), and publish | `admitted`, `EnqueueEmbeddedToolImage` |
| `scratchpad`, `mecha_progress`, `mecha_context` | Existing `Execute` with the current owner | none |
| `mecha_reminder` | `embeddedReminderArguments`, then `store.EmbeddedOwnReminderCall(ctx, b, k, raw)` (replays on the same key), then `publishRemindersChanged` | `RecoverEmbeddedEffect` |
| `goal` | status: `ManagedGoalPolicyFor`. set/off: the validation from `embeddedGoalCommand`, `SubstituteAndExpandText`, the workflow-lock check, then `store.UpdateEmbeddedBrowserGoal(ctx, b, cfg, op{RequestID:k}, …)`, then reset the lane goal counters. Do **not** use `ApplyEmbeddedGoalCall`, which leaves the goal pending forever. | `admitted`, `CheckEmbeddedGoalInvocation`, effect intent |
| `goal_snooze` | Turn = the active persistence's `turnSeq`/`turnDigest` plus `DesiredRevision`. Then `embeddedGoalSnoozeArguments`, `store.SaveEmbeddedGoalSnoozePreference(ctx, b, turn, k, raw)` (idempotent on `k`), `SetManagedGoalSnoozeUntil`. | same |
| `context_reset` | Validate `reason`/`window` as `embeddedContextResetTool` does. Set `m.contextResetRequested[agent]`. The engine ends the turn (D11). | none |
| `mecha_worker_terminate` | `requestWorkerSelfTermination`. The engine ends the turn. Workers only. | none |

**Tests.**
- Transport: auth, loopback, methods, size limit, `server/discover`, unknown method.
- Each tool through the real endpoint with a real store: effects; a repeated `toolUseId` with a different request timestamp returns the first result with no second effect (mail, image, reminder, goal); a stale binding rejected; workflow admission.
- Between turns: mail and scratchpad work, the three turn-scoped tools are rejected, and a call from a stopped process is rejected.

## 7. Phase 5: Model and provider plumbing

**`managed/claude_models.go`.** The model table: ID, CLI alias, display name and context window, all from S1. The engine prefers `modelUsage[<model>].contextWindow` from each `result` when it is present, so a CLI-side change shows up without a router release.

| ID | Alias | Resolves to (S1) | Window (S1) |
|---|---|---|---|
| `claude-code/fable` | `fable` | `claude-fable-5-1` | 1,000,000 |
| `claude-code/opus` | `opus` | `claude-opus-5-5` | 1,000,000 |
| `claude-code/sonnet` | `sonnet` | `claude-sonnet-5-5` | 1,000,000 |
| `claude-code/haiku` | `haiku` | `claude-haiku-5-5` | 1,000,000 |

**CLI probe.**
- `claude --version` and `claude auth status --json` give `{installed, version, loggedIn, authMethod, subscriptionType, err}`.
- It runs at startup, every 5 minutes, and on `GET /v1/provider-auth`.
- On a status change, the router publishes the existing `provider_auth_state` live frame, and the UI reloads providers and models on that frame. Verify that UI path, and add the reload if it is missing.
- The email and organization fields are never read into router state.

**Virtual provider (D12).** Add `claude-code` to the known-provider lists without persisting it:
- `store/managed.go`: names and labels, validation, the required set
- `managed/auth.go`
- `transport/managed_http.go`
- `transport/ui_live.go`
- `CommitManagedProviderSnapshot` skips it.

The `/v1/provider-auth` row uses the shape `providerStatus` requires (`web/.../http-client.ts`):
- `{provider:"claude-code", label:"Claude Code", authenticated, status, updated_at, account_label?, error?}`
- signed in: `authenticated:true`, `status:"authenticated"`, `account_label:"Claude CLI (<subscriptionType>)"`
- otherwise: `authenticated:false`, `status:"signed_out"`, `error:"Claude CLI not installed"` or `"Not signed in — run claude login on the router host"`
- `updated_at` is the probe time.

Login and logout for `claude-code` return 409 with that guidance.

**Catalog and model handling**
- **Catalog:** append the table's models while signed in, in `StoreCatalogSource.Catalog` on **both** paths, including the persisted-snapshot early return (`models.go:~126-132`).
- **Implicit default:** `SelectModel`'s "first available model" fallback (`models.go:~98-102`) skips `claude-code` models, so an omitted model, or the root, never gets one.
- **Admission:** `admitEmbeddedModel` accepts `claude-code` when the alias is known and the probe says signed in.
- **Resolution:** `ResolveModel` returns a synthetic `ai.Model{Provider:"claude-code", Api:"claude-code", ContextWindow}` from the table. The non-existent `Api` makes any accidental pi-go stream fail closed.
- **Engine switches:** reject cross-engine changes in `reserveEmbeddedModelSelection` and on the restart path.
- **Model changes within Claude Code** take effect at the next `acquire`, through `set_model` on the running process (§5, S8).
- **Default model:** reject `claude-code` as the default model on the server.
- **Subagents:** `resolveSubagentModel` refuses a `claude-code` parent, as a guard; Claude workers are not offered subagent tools.

**Tests.**
- Probe parsing for signed in, signed out, not installed and a malformed reply.
- The periodic refresh and frame on status change.
- The provider-auth response holds six providers, and a five-provider response is still accepted.
- Catalog merge on both paths.
- The implicit default never picks `claude-code`.
- Launch is refused when unavailable.
- Cross-engine switch and default-model rejection.
- `ResolveModel` windows.

## 8. Phase 6: Context management

- **Tokens.** `checkContextResetAfterTurn(tokens, model)` gets `ContextTokens()`, the exact usage, for Claude agents. The deferred epilogue feeds `updateAgentContextUsage` from the same value.
- **Thresholds (D10).** `ResolveContextThresholds` is unchanged. With 1M windows (S1), the defaults of 10% and 20% are 100k and 200k tokens, above the ~42.5k baseline.
- **Loop guard (Claude agents).**
  - After a reset, the first `ContextTokens()` of the new epoch is recorded.
  - If it is at or above the reset threshold, automatic reset is suppressed for that epoch.
  - A one-time warning history row is written.
- **Seed rendering, `managed/claude_seed.go`.**
  - The input is the binding's `ReopenEmbedded` entries that exist before this turn.
  - Leading user entries (genesis, scratchpad block) are emitted verbatim as their own text blocks, with mail entries unwrapped as in §5.
  - The remaining (ported) messages go under a "Recent conversation preserved across context reset" heading:
    - user and assistant text verbatim;
    - tool calls as `→ <display>(<args, ≤500 chars>)`;
    - tool results at ≤2,000 chars with a truncation note.
  - The input follows as the last blocks.
  - The total is capped at 200k characters, dropping the oldest ported messages first.
  - The same path handles a new launch (no entries), a reset epoch, and transcript loss (S4 fallback).
- **Claude compaction.** `ExecuteContextReset` is unchanged. A `system/compact_boundary` writes a `compaction` history row, and the context bar updates on the next usage.
- **Threshold editor.** It stays available for Claude Code agents (spec §3.12), and per-model overrides apply to `claude-code/*` keys.

**Tests (fake claude).** For each of the three triggers, check:
- a new epoch;
- the seed's block order and content;
- `--session-id` with the new ID;
- the old process stopped and a new one started;
- the epoch marker;
- the context bar drop;
- a mid-turn message reserved before a gear-menu reset is delivered in the new epoch.

Also test: the warning reminder fires once per epoch; the loop guard; a per-model threshold override on a `claude-code/*` key; the compaction row.

## 9. Phase 7: Goals

- **Receipt.** The Claude engine calls `enterEmbeddedProvider` after each input echo and before each assistant append (§5, steps 3 and 4). The receipt must be committed before the final assistant append, because the eligibility SQL rejects a user entry that already has a later `stop`.
- **Evaluator (D9).** `evaluateEmbeddedTurn` gets its model and stream from `m.embeddedGoalEvaluatorModel(saved)`:
  - pi agents: the worker's own, unchanged;
  - `claude-code`: the router default model if it is a pi-go provider, else `antigravity/gemini-3.8-flash`. The stream is built by the same provider switch the subagent runner uses, factored out as `embeddedStreamForModel(model)` (`embedded_subagent_runner.go:~364-406`).
  - If no stream can be built, write a "goal evaluation unavailable" history row and schedule nothing. Without this, the evaluator error would become NOT_DONE and loop continuations up to the cap.
  - The model must have `ContextWindow > 0`.
  - The test seam `m.embeddedGoalEvaluator` is unchanged.
- **Circuit breaker.** The empty-turn breaker and `toolExecuted` use `embeddedBareToolName` (Phase 1, item 5).
- **Continuation.** `dispatchEmbeddedGoalContinuation`, then `startEmbeddedGoalContinuationWithPrompt`, then `runEmbeddedSessionTurnWithContinuation`. This is unchanged; the Claude engine receives the continuation prompt through `RunMessages`.
- **Unchanged paths:** the idle kickoffs and the goal editor HTTP and browser paths.

**Tests (fake claude, fake evaluator through the test seam):**
- set a goal: a NOT_DONE turn leads to a continuation turn, then DONE turns the goal off;
- snooze delays the continuation;
- the cap of 20 trips even when the worker calls `goal` each turn;
- the empty-turn breaker;
- a goal set mid-turn through MCP applies from the next response;
- a goal set from the UI editor;
- no evaluation after an aborted or error turn;
- no evaluator available leads to the visible entry and no continuation.

## 10. Phase 8: Usage limits, failures, process cleanup

- **Usage limit (D13).**
  - On the signal, the engine stops the turn and appends a `StopError` final, "Claude usage limit reached; resuming at HH:MM", which shows in the timeline.
  - The lane's `heldUntil` is set to `resetsAt`. If no reset time was reported, it is now + 15 min.
  - A history row of kind `state` is written.
  - `dispatchEmbeddedMail` and goal-continuation start both check `heldUntil`. Held work stays queued, is not ACKed, and its attempt counters do not advance, so nothing is dead-lettered.
  - At `heldUntil`, a timer runs one resume turn ("The usage limit has lifted. Continue where you left off."), then calls `OfferDirect`.
  - The hold is in memory only. After a router restart, the next turn rediscovers the limit and holds again.
- **Availability failures.** At turn start the engine checks the cached probe. If Claude Code is unavailable, the turn fails fast with "Claude Code is not available: <reason>", using the existing error path.
- **Crash or malformed output.**
  - The turn fails.
  - The history `message` stays generic ("Claude Code turn failed"). The stderr tail goes in the error and output fields, which are not pattern-checked; `managedHistoryText` drops rows whose message or companion text contains paths (`store/managed_history.go:~80-105`).
  - Assistant text that precedes tool calls goes through `message_content` in the same way.
- **Exit between turns.** The exit watcher cleans up and logs. The next turn's `acquire` starts a new process with `--resume`.
- **Router shutdown.** `Manager.Close` (`supervisor.go`) stops every Claude process in parallel through `stopWorkerProcess`, before the store closes.
- **Startup sweep.** Runs before `RecoverEmbeddedRoots`:
  - for each `claude-proc/*.json`, if the process is alive with a matching start time, kill its group;
  - delete all the process records and all `claude-mcp/*` files.

**Tests:** usage-limit hold and resume with queued mail intact; the unavailable path; a crash mid-turn; a crash between turns; `Manager.Close` stops idle processes; the sweep kills a planted process and removes files; a router restart mid-turn leads to `ClassifyEmbeddedRestart` and the interrupted notice, then the Claude engine resumes with `--resume`, the tool effect is not repeated, and a message arriving during the recovery turn waits for normal dispatch.

## 11. Phase 9: Web UI (`web/router-ui-react/src`)

- **Accept the provider.**
  - `transport/http-client.ts`: `providerName()` and the `providerStatus` labels table (add `"claude-code": ["Claude Code"]`).
  - `providerAuth()` accepts five or six unique known providers, so older routers still work.
  - `protocol/guards.ts` `isProviderName`.
  - `protocol/types.ts` `ManagedProvider`.
- **Provider row.** `components/ProviderAuthDialog.tsx`: add the `providerLabels` entry and a status-only Claude Code row modelled on the Cursor row, with host sign-in guidance and no Connect button.
- **Default-model list.** Exclude `claude-code` from both places it is rendered in the same dialog.
- **`isClaudeCodeAgent(agent)`** goes in `domain/models.ts`. It checks `agent.managed_agent?.model.provider === "claude-code"`.
- **Hide for Claude Code agents in v1:**
  - `components/AgentControlsDropdown.tsx`: in the `controls` array, the Workflow, Inline tools and Worker tools entries;
  - `ConversationHeader.tsx`: the pinned Tool settings button, the inline-tools toggle, the Worker Tools button and dialog, the Workflow button, modal and dropdown, and the subagent metric card and viewer;
  - `AgentRow.tsx`: the subagent badge.
- **Do not hide** the context-threshold editor (`onContextThresholds` in `App.tsx`). The spec keeps it at parity.
- **Model partition.** `ModelPicker.tsx` replaces the `isEmbedded` filter with an engine partition: a Claude Code agent sees only `claude-code/*`, and other agents never see them. It is computed at the header `ModelPicker` call site in `App.tsx`, with a guard in `changeManagedModel`. The launch dialog's picker shows all available models.
- **Tool icons.** Add icons to `CompleteTimelineEntry.tsx` `getToolIcon` for `grep`, `glob`, `web_search`, `web_fetch`, `task` and `todo_write`. Cosmetic.
- **Tests (Vitest).** Run `ls tests/` at implementation time, because files have moved:
  - `tests/component/provider-auth.test.tsx`
  - `model-picker.test.tsx`
  - `ConversationHeader.test.tsx`
  - the `AgentControlsDropdown` test (create it if absent)
  - `managed-agent-dialog.test.tsx`
  - `agent-row.test.tsx`
  - `complete-timeline-entry.test.tsx`
  - `tests/unit/protocol-guards.test.ts`
  - `http-validation.test.ts`
  - `models.test.ts`
  - `tests/transport/http-contract.test.ts`
  - `ui-live.test.ts`

## 12. Phase 10: Docs

- Update the "Worker Runtime" paragraph in `CLAUDE.md` and `AGENTS.md`. It should cover:
  - router-managed Claude Code workers, launched from the UI by choosing a Claude Code model;
  - `claude` must be on the router host (`MECHA_CLAUDE_BIN`) and signed in;
  - each worker keeps one `claude` process, stopped after `MECHA_CLAUDE_IDLE_TIMEOUT` (default 15m) with no turn;
  - the legacy tmux path is unchanged and separate.
- Note the new `/v1/mcp` route in `router-proxy/README.md`.

## 13. Test strategy

**Fake `claude`, `internal/testfixture/fakeclaude`**
- It is built once per test binary in `TestMain` and selected through `MECHA_CLAUDE_BIN`.
- It speaks stream-json from a JSON script named by `FAKE_CLAUDE_SCRIPT`. Each input's steps can be: thinking, text, `tool_use` (built-in, answered with a canned result), `mcp_call`, `interrupt-wait`, `rate_limit`, `crash`, `hang`, `compact_boundary`, `slow_start`.
- `mcp_call` makes a **real HTTP MCP call** to the router, using `--mcp-config`. It emits `tool_use` before the call and `tool_result` after, so the endpoint, grants and tools are exercised end to end.
- It honours `control_request` interrupt, `--replay-user-messages` echoes (mid-turn inputs are echoed only after the current `result`), `--session-id` and `--resume` (a missing session fails as in S4), and `system/init` with capabilities.
- It stays alive across inputs until stdin closes, and models life between turns with the event shapes recorded in S6–S8:
  - `background` starts a real child process (`sleep`) and emits `task_started` and `background_tasks_changed`. Tests can see the child survive between turns and stop with the process.
  - When a background child exits while idle, the fake emits `task_updated`, `task_notification` and `background_tasks_changed`, then a turn of its own (`system/init`, assistant, `result`).
  - When a background child exits during a turn, the fake emits a `task-notification` user replay inside that turn.
  - It answers `set_model` with a `control_response` and emits the `/model` local-command user event. A script flag makes it refuse instead.
- It logs argv, environment and stdin to a file for assertions: env scrubbing, seed content, image blocks, session flags, and that the token appears nowhere in argv. It writes one record per process start, so tests can assert process reuse.

**Behavioral tests (Go).** A full router with a real store and the fake `claude`, following `embedded_behavioral_lifecycle_test.go`. Each one drives the HTTP and ui-live APIs:

| Test | Covers |
|---|---|
| availability, launch, refused launch, implicit default never Claude | AC1, AC2 |
| composer message with image → Claude stdin has text and image; timeline rows, answer row, activity, context usage; no mail sent | AC3 |
| mid-turn message → ACK at echo, own answer; redelivery when the run dies first; no steer during start-up or recovery; reservation released across a reset | AC4 |
| every router tool through MCP, de-duplication with a new timestamp, stale binding | AC5 |
| interrupt partial | AC6 |
| restart, terminate mid-turn, router restart mid-turn, orphan sweep | AC7 |
| model change within engine, cross-engine rejection | AC8 |
| the three reset triggers, seed, loop guard, warning reminder, per-model threshold override | AC9 |
| goal loop, cap with `goal` calls, evaluator unavailable | AC10 |
| usage-limit hold | AC11 |
| env, token and file hygiene | AC12 |
| process reuse across turns; idle shutdown, then resume on the next message; idle shutdown deferred while a background task runs, and the 2-hour cap; a background child survives between turns and stops with the process; a task finishing while idle produces an unprompted turn with its notification entry, including racing a dispatch; a task finishing mid-turn produces a notification entry and no extra turn; model change through `set_model`, and the restart fallback when it is refused; reset, restart and terminate stop the old process; turn-scoped tools rejected between turns | AC14 |

AC13 is covered by:
- the existing suite, unchanged and green;
- the shared steer-release regression test run on both engines;
- a pi-vs-Claude parity test on the same scripted scenario that compares the resulting history rows (kinds, display names, outcomes).

**UI tests.** Vitest, as listed in §11.

**Live proof (dev-loop Phase 3 and the browser-evidence gate)**
- Run the real router, real `claude` on Haiku, and the UI in the in-app browser.
- Exercise: launch with a worktree; chat with an image; a tool card; the scratchpad tool; a background `bash` command that is still running at the next turn; gear-menu Reset, checking the seed and that the worker continues from the scratchpad; goal set, then continuation, then DONE; interrupt; model change; idle shutdown with a short timeout, then a message that resumes; terminate.
- Capture screenshots. Record cold-start and warm-turn latency and idle process memory (S9).

**Coverage:** `go test -coverprofile` for `internal/managed`, `internal/transport` and `internal/store`, and `vitest run --coverage` for the UI.

## 14. Traceability

| AC | Spec | Plan phases | Tests |
|---|---|---|---|
| AC1 | §3.1 | 5, 9 | probe, provider-auth, catalog; UI provider-auth |
| AC2 | §3.2 | 3, 5 | launch behavioral |
| AC3 | §3.3 | 1, 2, 3 | conversation behavioral; translator |
| AC4 | §3.4 | 1 (items 2, 7), 3 | mid-turn behavioral; steer-release regression |
| AC5 | §3.5 | 4 | MCP transport and tool tests |
| AC6 | §3.7 | 2, 3 | interrupt behavioral |
| AC7 | §3.7, §3.11 | 3, 8 | terminate, restart, sweep behavioral |
| AC8 | §3.8 | 5, 9 | model change; UI model-picker |
| AC9 | §3.9 | 1, 3, 6 | reset behavioral; seed unit |
| AC10 | §3.10 | 1, 4, 7 | goal behavioral |
| AC11 | §3.11 | 8 | usage-limit behavioral |
| AC12 | §3.6, §3.13 | 2, 4, 8 | hygiene behavioral |
| AC13 | §2 G3 | 1 | existing suite; parity and steer-release tests |
| AC14 | §3.5, §3.7 | 2, 3, 4, 8 | lifetime behavioral; MCP between-turn tests |

## 15. Order, sizing, risks

**Order.**
1. Phase 0, then Phase 1: items 1–6 in one commit, item 7 in its own commit.
2. Then phases 2 → 3 → 4 → 5, the core path; the first end-to-end chat works here.
3. Then 6 → 7 → 8.
4. Phase 9 can start once phase 5's API shape is fixed.
5. Phase 10 comes last.

**Risks**
- **CLI protocol drift.** stream-json and the control protocol are only partly documented. Mitigations: the capability gate, fixtures recorded from real output, and the live proof on each CLI upgrade.
- **Shared-code changes.** The interface switch, provider-entry helper, bare-name normalizer and steer release all touch code pi workers run. Mitigations: the existing suite, parity tests, and separate commits.
- **Idle processes hold memory.** Each worker's process stays up until the idle timeout, at about 365 MiB each (S9). A user configuration with local stdio MCP servers adds their processes on top. The timeout is configurable, and a later version can cap the number of idle processes.
- **Life between turns** depends on `task_*` and `background_tasks_changed` events and on the `set_model` control request. They were verified on 2.1.308 (S6–S8), but they are not in the public docs. Mitigations: recorded fixtures, the fake `claude` modelled on them, and re-running the S6–S8 probes on CLI upgrades.
- **Cold start** of a few seconds remains for the first turn after idle shutdown or a reset. Steering is blocked until the first echo.
- **User configuration side effects.** Full configuration means the user's hooks and plugins run inside workers. This was accepted (spec Alt J).
- **Shared subscription limits** across many Claude workers. The usage-limit hold covers correctness, not capacity.
- **Concurrent edits on `main`.** Three commits landed while this plan was written, one of them deleting a UI component it referenced. Re-check references at the start of each phase.
