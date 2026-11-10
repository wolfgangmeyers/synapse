# Implementation Plan: Claude Code Workers (v1)

Spec: [claude-code-workers-spec.md](claude-code-workers-spec.md) · Status: Implementation in progress, partial validation; spec remains Draft · Date: 2026-10-26, revised 2026-10-27

- Current revision: one process per foreground execution; earlier long-lived-process design is superseded. Historical probes remain evidence, not requirements.
- Revised 2026-10-28: spikes S1–S4 and S6–S9 run and folded in (§2). Only the optional S5 is still open.
- Router line numbers refer to `main` at `f77c617`. They will drift, so treat a function name as authoritative over its line number.
- UI references name the component and function, because the header files change often.
- Paths are under `router-proxy/internal/` unless they start with `web/`.
- An independent fact-check against `f77c617` has been folded in.

Revision: the [runtime adapter boundary draft](runtime-engine-boundary.spec.md) supersedes the old Phase 1 interface/fixes proposal. Small Pi wrapper first; targeted shared helpers only where Claude needs them; then Claude. Existing SDK-shaped persistence may remain internal. No generic engine framework or broad SDK cleanup is required. The decisions below govern the authorized implementation. Acceptance remains pending.

Primary implementation sequence: [CLAUDE_CODE_ADAPTER_PLAN.md](../../CLAUDE_CODE_ADAPTER_PLAN.md), active **Implementation** under the simple workflow. See its current implementation evidence for passing checks and remaining gaps. This file retains detailed design/probe references; the root plan controls conflicting sequencing.

**Supersession:** The draft [Claude router default contract](claude-router-default.spec.md) supersedes D9, the Pi-only evaluator and default/root exclusions in §§5, 7, 9 and their test summaries. They describe the earlier implementation, not the planned behavior. Unset automatic selection remains Pi-only; explicit saved Claude defaults are planned. Other historical decisions remain subject to their own status.

## 0. Constraints

- Use the user-selected simple dev workflow, not dev-loop or its approval/review gates. Its active step is **Implementation**, authorized by Wolfgang on the `claude-integration` worktree/branch. This documentation update authorizes no commits or push.
- Automated tests never call an LLM or the real `claude`. Behavioral tests run a real router against a fake `claude` binary (§13).
- No new accumulating store without bounded retention (spec §3.13).
- AC13 must hold. Verify the Pi-only seam before adding Claude. Steer-reservation release is a separate behavioral change with its own specification/regression test, not part of the seam. Acceptance of that separate fix remains subject to its regression coverage.

## 1. Key decisions

| ID | Decision | Spec |
|---|---|---|
| D1 | The engine is identified by model provider `claude-code`. Agents stay `runtime='embedded'`. There is no migration of `managed_agents`. | §4.1, Alt A |
| D2 | `runEmbeddedSessionTurnScoped` keeps its shared prologue, prompt dispatch and epilogue. Use a small Pi/Claude adapter handle for execution and lifecycle; preserve Pi setup initially. The old SDK-leaking interface sketch is superseded by the runtime boundary draft. | §4.2 |
| D3 | One fresh process per admitted foreground execution (all Run/Continue calls in that scope share it); the next execution gets a new PID. Stop/reap and revoke its grant before another execution. Resume the conversation with the selected model; no idle process or in-place switching. | Spec §3.4–3.8 |
| D4 | Use the binding session ID for a fresh session or resume; on confirmed missing transcript, reseed once from bounded router context. | Spec §3.4–3.8 |
| D5 | Canonical shared names in checked router-tool producers/results; raw protocol identity stays transient. Use the shared witness path (§6). | See sections |
| D6 | ACK only the exact correlated checked echo. Mid-turn mail and direct prompts are reserved and steered into the running process's stdin (CC-019). Input not yet written stays durably queued for a later execution. Direct HTTP prompts wait for the lane without durable acceptance before reservation and can be dropped on cancellation or restart. A write-started input without a checked echo is held, never automatically redelivered. (The earlier queue-only design is superseded by CC-019.) | Spec §3.4–3.8, CC-019 |
| D7 | Every router tool requires current process grant and checked foreground user/assistant producer; no between-turn authority. | Spec §3.4–3.8 |
| D8 | Pi retains pre-dispatch StreamFn guards. Claude validates before stdin and commits consumption/entry on echo; no unsupported per-request CLI gate (§5). | See sections |
| D9 | Claude evaluates only with a usable configured Pi router default; otherwise visible unavailability and no continuation. | See sections |
| D10 | Context size comes from Claude's reported usage. All four Claude Code models run with a 1M window (S1), so the existing thresholds and precedence apply unchanged and there is no Claude-specific threshold tier. A post-reset loop guard covers thresholds set below a fresh session's size. | §3.9 |
| D11 | `context_reset` and `mecha_worker_terminate` copy pi's `Terminate`: the turn stops after the tool result, with no final assistant. pi's rule is at `agent/loop.go:580`. Any partial assistant produced by the engine's own interrupt is discarded, not appended. | §3.9 |
| D12 | Local CLI engine/model selection only. No provider settings/auth routes, polling, status events or credential changes (§7). | See sections |
| D13 | A usage limit holds the agent's lane in memory until `resetsAt`, then retries the interrupted input only if safe; otherwise holds it and permits later eligible work. | §3.11 |
| D14 | No automatic replies, matching current embedded behavior (`0fc20bb`). The final answer is recorded as the worker's answer in history, and the system prompt tells Claude to use `mecha_mailbox_send` to message agents. | §3.3 |
| D15 | No between-turn notification execution; stop owned process group at foreground completion. | Spec §3.4–3.8 |
| D16 | No idle timeout or background task extension; unconfirmed cleanup fences admission. | Spec §3.4–3.8 |

## 2. Phase 0: Spikes

Run on 2026-10-28 against `claude` 2.1.308, with probes in a scratch directory. S6–S9 describe historical CLI probes, not v1 lifecycle requirements.

| ID | Question | Result | Feeds |
|---|---|---|---|
| S1 | Which context window does each alias run with in `-p`? | **Done.** `modelUsage[...].contextWindow` in `result` is 1,000,000 for every alias: `fable` → `claude-fable-5-1`, `opus` → `claude-opus-5-5`, `sonnet` → `claude-sonnet-5-5`, `haiku` → `claude-haiku-5-5`. No `[1m]` suffix is needed. | §7 model table, D10 |
| S2 | Does `--permission-mode bypassPermissions` run tools in `-p` with no prompt and no refusal? Or do we need `--dangerously-skip-permissions --settings '{"skipDangerousModePermissionPrompt":true}'`, as `worker_launch.py` uses today? | **Done.** `--permission-mode bypassPermissions` is enough. With `--input-format stream-json`, `system/init` reports `permissionMode: "bypassPermissions"`. A Bash `echo`, a Write to a plain file, and both a Write and a Bash redirect into `.claude/` all ran, with no `control_request`, no `permission_denials`, empty stderr and exit 0. Forcing `skipDangerousModePermissionPrompt: false` through `--settings` changed nothing, so headless mode needs neither that setting nor `--dangerously-skip-permissions`. Control run without the flag: the `echo` ran, but every write was refused with a `system/permission_denied` event, an `is_error` tool result and an entry in `result.permission_denials`. Nothing hung, because no permission prompt tool is configured. | §4 argv |
| S3 | What does the stream look like when the usage limit is hit? | **Answered from the CLI's own schema; not yet seen live.** `rate_limit_event.rate_limit_info.status` is `allowed`, `allowed_warning` or `rejected`. A refused request produces an assistant event with `is_api_error_message: true`, `api_error: "usage_limit_reached"` and `api_error_params.rate_limit_info` (`status: "rejected"`, `resetsAt`, `rateLimitType`). These fields are marked internal, so parsing stays defensive (§10). Confirm when a limit is hit naturally. | §10 |
| S4 | Does `--resume <id>` with a missing transcript fail fast with a recognizable error? | **Done.** In about 1 s: stderr `No conversation found with session ID: <id>`, a `result/error_during_execution` with `num_turns: 0`, and exit code 1. | §5 resume fallback |
| S5 | Optional: does `interrupt_send_now_v1` allow real mid-turn steering? | **Open, optional.** | later improvement only |
| S6 | What does a running process do when a background task finishes? | **Done.** *While idle:* `system/task_updated` (`status: completed`), `system/task_notification` (`task_id`, `tool_use_id`, `status`, `summary`, `output_file`), `system/background_tasks_changed` (`tasks: []`), then a turn of Claude's own: `system/init`, assistant, `result/success`. No user event precedes it. *During a turn:* the same `system/*` events, then a replayed user event with `isReplay: true` and `origin: {kind: "task-notification", producer: "session-task", runId}` whose text is a `<task-notification>` block. Claude handles it inside the running turn, which still ends with one `result`. Background subagents behave the same; their internals arrive with `parent_tool_use_id` set, and their own tool runs are marked `owned_by_subagent`. | D15, §4 translator, §5 unprompted turns, fake `claude` |
| S7 | Does Claude Code report which background tasks are still running? | **Done.** `system/background_tasks_changed` carries the full running set (`task_id`, `task_type` such as `local_bash` or `local_agent`, `description`) whenever it changes. The CLI's schema describes it as a level signal: replace the set on each event and reset it to empty when a process starts. A `stop_task` control request also exists. | Historical observation |
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

## 3. Phase 1: Small Pi-backed seam

Follow [runtime-engine-boundary.spec.md](runtime-engine-boundary.spec.md), slices A/B.

1. Wrap the existing constructed Pi session. Keep tools, hooks, ordered persistence and context fitting in place initially. Add only the run/continue, steering, interrupt and snapshot operations actual callers need. Resource close is separate from interrupt and may be a Pi no-op.
2. Replace concrete active-session registry handles and adapt prompt/mail/wake/restart/reset callers. Preserve lock ordering, callback settlement, identity-checked unregistering and workflow tool refresh through a private Pi helper.
3. Keep router input identity/source and original ACK bytes separate from engine-rendered text/images. Acceptance is not consumption. Preserve current Pi callbacks, matching and reservation behavior.
4. Map Pi outcomes/snapshots only where it simplifies shared callers. Existing message/model/persistence types may stay private compatibility code. Do not mandate wholesale SDK removal or session construction changes.
5. Verify existing behavior and the boundary draft's deterministic Playwright matrix. No Claude branch, synthetic Claude model, tool-name normalizer, migration or steer bugfix in this slice.

**Next: targeted shared helpers, not a mandatory event architecture.** Before Claude translation, identify which checked consumption/history/final/provider-entry operations need reuse. Extract a small synchronous error-returning sink only if it avoids duplication; otherwise retain the existing internal persistence bridge. Verify ordering/fences with Pi. Full normalization is optional cleanup, not an entry gate.

**Separate prerequisite for Claude redelivery.** The unconsumed reservation fix is specified below and in CC-005; its regression must pass before Claude claims reset-safe redelivery. It is intentionally excluded from the behavior-preserving seam. Old references below to the separate steer-reservation fix mean this separate work, not part of the Pi-only extraction.

## 4. Phase 2: Claude process and stream translation

**`managed/claude_process.go`**
- **Command line:** `claude -p --input-format stream-json --output-format stream-json --verbose --replay-user-messages --model <alias> --permission-mode bypassPermissions --tools Bash,Read,Write,Edit,Glob,Grep,ToolSearch --mcp-config <file> --append-system-prompt-file <file> (--session-id <uuid> | --resume <uuid>)`.
  - S2 confirmed `--permission-mode bypassPermissions` on its own. Do not pass `--permission-prompt-tool`: without one, any refusal comes back as an error tool result instead of a `control_request` the engine would have to answer.
  - Pass `--tools` with the exact separate argument `Bash,Read,Write,Edit,Glob,Grep,ToolSearch` for every managed Claude foreground worker process, including roots, resumed sessions and children. These native tools run under the process OS authority with `bypassPermissions`; router tool policy does not gate them. The separate noninteractive internal evaluator remains tool-free. Preserve existing worker user/project MCP inheritance and leave root/child strict MCP configuration unchanged.
  - `cwd` is the agent's working directory.
- **Binary:** `MECHA_CLAUDE_BIN`, else `PATH`, else `~/.local/bin/claude`. The router is often started from a shell whose `PATH` lacks `~/.local/bin`.
- **Environment:** `os.Environ()` minus `TMUX`, `TMUX_PANE`, `CLAUDECODE` and `CLAUDE_CODE_ENTRYPOINT` (AC12).
- **Process group:** `Setpgid`. Reuse the probe and reap pattern in `process.go:~180-240`.
- **Process record:** `<SessionRoot>/claude-proc/<agent>.json` holds `{pid, pgid, started_at, binding}`. It is written after start and retained until cleanup is confirmed; remove only the record matching the exact process identity.
- **stdin:** a single locked writer of JSON lines.
- **stdout:** a scanner with a 16 MiB line limit, run by one reader goroutine for the whole life of the process. The reader hands events only to its foreground execution; output outside that scope is rejected.
- **Exit watcher:** on exit, revoke the MCP grant, fail the turn that holds the process if there is one, and confirm group cleanup before removing the matching record and allowing another execution. Retain recovery evidence and fence admission if cleanup is uncertain.
- **stderr:** a 64 KiB ring buffer.
- **`Interrupt()`:** writes `{"type":"control_request","request_id":…,"request":{"subtype":"interrupt"}}` and waits for `control_response`.
- **`Stop()`:** use one end-to-end 5 s budget for interrupt/termination and cleanup: close stdin, promptly escalate verified group signals from SIGTERM to SIGKILL with time reserved for reaping and confirmation. If cleanup cannot be confirmed within the budget, return an error, retain the record and fence admission; never signal an unverified group.
- **Version gate:** the `system/init` `capabilities` must include `interrupt_receipt_v1`. Otherwise fail with "Claude Code CLI too old".

**`managed/claude_stream.go`** decodes events. The table below describes the existing persistence compatibility representation, not a requirement to expose `ai` messages in shared lifecycle APIs. Reuse a small internal bridge/helper where practical:

| Event | Handling |
|---|---|
| `system/init` | Record the model ID. Check `session_id` matches. Validate within the admitted foreground execution only. |
| `system/compact_boundary` | Write a history row of kind `compaction` (§8). |
| `system/background_tasks_changed` | May be observed during foreground execution; it cannot extend process lifetime. |
| `system/task_started` | Remember task IDs marked `owned_by_subagent`, so their notifications are ignored. |
| `system/task_notification` | Handle only within the current foreground execution; never admit an idle notification turn. |
| `system/permission_denied` | Log at warn. It should not occur under `bypassPermissions` (S2), so seeing it means the flag did not take effect. The matching error `tool_result` already shows the refusal in the timeline. |
| `system/status`, `system/thinking_tokens`, `system/task_updated`, `system/task_progress`, `post_turn_summary`, `api_retry`, `hook_*` | Ignore; log at debug. |
| `assistant` (main thread, `parent_tool_use_id == null`) | Coalesce consecutive events with the same `message.id` into one `ai.AssistantMessage`: text → `TextContent`, thinking → `ThinkingContent`, `tool_use` → `ToolCall{ID, Name: canonical shared name for router tools (otherwise raw), Arguments}`. Record usage. |
| Stop reason | A message with tool calls gets `StopToolUse`. A text-only message is held until the next event: `result/success` makes it `StopStop`; `result` after a user interrupt makes it `StopAborted`; any other `result` error makes it `StopError` with the result text. An interrupt the engine sent for D11 discards the held or partial message. If a non-`result` event follows a text-only message, which is not expected, its text becomes leading text of the next assistant message. |
| Assistant fields | Always fully populated, because `embeddedCheckedStop` (`store/embedded_restart_wake_origin.go:44-80`) requires them: `api="claude-code"`, `provider="claude-code"`, `model`, the full `usage` with zero costs, and `timestamp`. |
| `user` with `isReplay` and no `origin` | Matches the oldest pending input (FIFO, single writer) and triggers that input's `AppendMessage` (§5). |
| `user` with `isReplay` and `origin.kind == "task-notification"` | A background task finished during this turn. Append an in-execution notice (D15), not a new initiating user. It does not touch FIFO/ACK/execution-user binding. |
| `user` with neither `isReplay` nor `tool_result` blocks | Ignore. It cannot authorize a turn or effect. |
| `user` with `tool_result` blocks | `ai.ToolResultMessage{ToolCallID, ToolName: the same canonical name as the persisted producer, Content: text blocks joined (images become "[image]"), IsError}`, passed as a value. |
| `parent_tool_use_id != null` | Subagent internals are not mirrored or counted in usage. Their MCP calls get no authority from ignored output: calls without a main-thread checked foreground producer/scope fail. |
| `rate_limit_event` | `rate_limit_info.status == "rejected"` raises the usage-limit signal with `resetsAt` (§10, S3). |
| `assistant` with `is_api_error_message` and `api_error == "usage_limit_reached"` | Raises the usage-limit signal with `api_error_params.rate_limit_info.resetsAt` (S3). It is not appended as an answer. |
| `result` | Ends one Claude turn. Error subtypes carry the error text. |
| `stream_event`, unknown types | Ignore. |

- **Context tokens** are taken from the last main-thread assistant usage: `input_tokens + cache_creation_input_tokens + cache_read_input_tokens`.
- **Display names** (`historyToolName` for this engine):
  - `Bash→bash`, `Read→read`, `Edit/MultiEdit/NotebookEdit→edit`, `Write→write`, `Grep→grep`, `Glob→glob`, `WebSearch→web_search`, `WebFetch→web_fetch`, `Task/Agent→task`, `TodoWrite→todo_write`;
  - `mcp__mecha__X→X`;
  - any other `mcp__s__t` becomes `s_t`.

**Tests.** Table-driven translator tests over recorded stream fixtures: tool round trip, text-only final, user interrupt partial, D11 discard, error result, coalescing, subagent events ignored, compaction, rate limit. Process tests use the fake binary (§13) for env scrubbing, process-group kill, version gate, stderr cap, the exit watcher, and rejection of output outside the foreground execution.

## 5. Phase 3: Claude process manager and turn engine

**Process manager and turn engine (D3).** On each admitted foreground execution, preflight and start a fresh process with the selected model. Resume the binding's established session; only a confirmed missing transcript permits one fresh seeded fallback. Revoke its grant and stop/reap its group on every outcome before another execution starts. Retain recovery records and fence admission if cleanup cannot be confirmed. No idle pool, notification execution or in-place model switch. `claudeTurnSession` owns only that execution.

**Rendering inputs for Claude**
- Mail user messages are stored as raw `embedded-mail/v1` JSON (`store/embedded_router.go:~1493,2057`).
- Claude's stdin gets text from `unwrapEmbeddedMailText` (`embedded_automatic_fit.go:~173-192`) plus image content blocks loaded from the message's attachments. Reuse the attachment loading in `buildSteerUserMessage` (`embedded_mail.go:~178-251`). This is what AC3's image requirement depends on.
- The mirror always gets the **original** value, so the ACK byte-match holds.
- Plain prompts, continuation prompts and notices are sent as text.

**Execution and interruption.** Submit only the foreground input after admission and durable write-start marking. Correlate the exact echo and commit checked consumption before ACK/publication. Mid-turn mail and direct prompts that arrive during a running turn are reserved and written to the same process's stdin (CC-019), and are ACKed only on their exact checked echo. Input not yet written (write rejected, process starting or recovering, or turn ended before the echo) stays durably queued for a later execution. Direct HTTP prompts wait for the lane and are not durably accepted before reservation; cancellation or restart can drop an unaccepted request. Add no new queue. On interrupt preserve checked partial output, stop/reap and return cancellation. Definitely unsubmitted inputs can be released; uncertain writes remain held through reset/restart until expiry. Reset, termination and router shutdown drain callbacks and stop the captured process without affecting a replacement.

**System prompt file**
- It contains:
  - a Claude-specific base;
  - the identity block from `buildEmbeddedWorkerSystemPrompt`;
  - how messages arrive (`[Direct message from X]: …`);
  - "your final answer is shown to the user; nothing is sent automatically; use `mecha_mailbox_send` to message other agents";
  - each router tool's prompt guidelines.
- It is written to `<SessionRoot>/claude-mcp/<agent>-<process>.prompt` (0600) when the process starts and deleted when it exits.

**Later Claude-only migration, if still needed:** `ALTER TABLE embedded_sessions ADD COLUMN engine_session_started_at TEXT;`. Add store getter and setter. Use the next free number at implementation time. `111` is occupied by `111_workflow_state_management_tools.sql`; this checkout currently ends at `123`, so re-scan rather than reserving a number now.

### Checked Claude submission and foreground authority

Pi's `StreamFn` remains a true before-provider-dispatch gate. Claude CLI does not expose an equivalent per-request callback. Split only the minimum shared checks: **before any stdin submission**, under current lane/process binding, renew ownership, check lifecycle/execution admission, input/image/seed bounds and known context headroom. Denial writes no stdin and no ACK. Reserve and correlate input before the serialized write; a write alone never consumes it.

On the exact input echo, synchronously persist the original user bytes and ACK through the checked path, bind the execution user, then commit non-mail entry and eligible goal-applied evidence before accepting later output/finals. The echo proves consumption, not permission to submit retroactively. If checked echo/entry persistence rejects (including ownership changing after submit), stop/reap the process and revoke MCP, discard subsequent output and report the error. Already committed ACK stays committed; a write-started input without checked echo stays held, not eligible for automatic selection. Never automatically replay a consumed input or an UNKNOWN effect. No ordinary final or goal evaluation follows failed entry. Do not refresh entry/receipts merely on assistant arrival. A checked successful goal-tool result applies only at the next observed response boundary, never at goal-set time; absent reliable response-boundary evidence, leave it pending until the next submitted input.

This guarantees router-controlled submission admission and checked publication, not a before-every-request gate for Claude native tools. Native CLI work may begin before echo checking and is trusted only for the bounded foreground process lifetime. Reject unsolicited output outside that execution; never fabricate a notification scope or grant.

### Separate reset-safe reservation fix (after Pi extraction)

Owner: the shared reset coordinator (`ExecuteContextReset` and its store epoch transition), not either adapter, not `popSteerAttempt`, and not an exit watcher. All reset triggers use the same sequence. Mark reset pending under the lane's short admission lock, reject new submit/steer/MCP admissions, interrupt active work and join callbacks; Claude additionally closes/reaps the old process and revokes its grant. Do not wait while holding a callback-needed lock. Browser reset waits outside `lane.serial`, then acquires it; tool/threshold reset already on the lane uses a non-reentrant internal reset helper after callbacks settle.

Only after drain, snapshot preserved context. In the **same immediate SQLite transaction** as `ResetEmbeddedSessionEpoch`, fence the exact old agent/session/epoch/generation/owner, delete only that binding's `embedded_attempts` with no `embedded_acks` and no checked consumed-user entry (using its append key), and install the next epoch. Never delete ACKed/consumed attempts or retarget an attempt. Message incarnation, accepted payload, target snapshot, queued state and expiry remain unchanged. Before stdin, the exact accepted message incarnation gets the durable `messages.claude_stdin_started` bit; it lives on the message row, not the attempt. Deleting an unACKed attempt and its append key does not clear that bit. Thus an uncertain queued message remains held through epoch transition and restart until existing expiry; rollback preserves both attempt and bit. Release definitely unsubmitted eligible reservations; write-started Claude inputs without checked echo remain held, including across reset. Rollback leaves both old binding and reservations unchanged. Clear old correlation only after drain/transaction; acquire new owner, seed/wake, clear reset-pending and offer live queued mail with a fresh attempt.

An echo racing reset has two outcomes: checked append wins before drain/transition (one ACK, preserve consumed history, no redelivery), or transition wins (old callback rejected by binding/attempt fence, zero old ACK, fresh delivery only if definitely unsubmitted; otherwise held). Late callbacks cannot delete a replacement reservation, ACK the new attempt, publish history/usage or start effects. Store fencing is required even after callback join for delayed external events. Restart after a committed transition recovers queued work; no in-memory cleanup dependency. Expired/retargeted messages follow existing expiry rules, never resurrect. Do not change Pi match-then-head correlation in this fix.

Regression: use barriers at reserve, echo/checked append, drain and epoch commit for Pi and Claude. Cover all reset triggers, equal-text distinct inputs, successful consumption vs unconsumed release, failed reset rollback, late old echo, repeated reset, crash after epoch commit, expired mail and new reservation protection. Assert SQLite attempts/ACKs, WS history and actual new input; no sleeps or real LLM.

### Concrete bounds and evaluator choice

Per process: at most 64 pending input correlations and 6 MiB total rendered pending input bytes (including base64 images/seed); reserve capacity before reserving/writing, otherwise leave mail queued and direct HTTP prompts waiting for the lane. Maximum stdout line 16 MiB; stderr ring 64 KiB. HTTP-before-producer buffering is at most 128 records and 16 MiB total, whichever first. Input correlation or byte capacity causes backpressure: leave additional mail queued and direct HTTP prompts waiting before reservation/write. An oversized stdout line or exhausted HTTP-before-producer buffer instead causes visible protocol failure and stop/reap; never drop required events or ACK unread input. Drain/clear all transient maps on settlement/close; no accumulating cache. Tests hit count and byte limits separately.

For Claude goal evaluation use only the currently configured router default if it is a usable Pi model with positive context window and constructible authenticated stream. No search/fallback model, no Antigravity, no new evaluator setting. Missing/unusable default records `goal evaluation unavailable` and schedules no continuation. Pi workers retain their current evaluator behavior.

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

### Narrow shared MCP execution path

Retain SDK-shaped checked persistence internally. Extract the existing `BeforeToolExecute` admission closure and its per-turn witness/result maps just far enough to call from Pi and Claude; do not add an effect framework or new ledger. For Claude router tools translate `mcp__mecha__<name>` to the canonical shared name in **both** persisted assistant calls and results. Keep raw identity only in transient protocol/display metadata. Built-in tool events retain display mapping but cannot acquire router-effect authority.

For each call: authenticate current process/binding, serialize with its admitted turn, renew owner, bind execution user, validate original schema/arguments and workflow policy. Require `_meta["claudecode/toolUseId"]` (no JSON-RPC-ID fallback for effects); scope the ID to binding and process incarnation, use the same canonical ID in assistant producer, effect and result. Persist the actual assistant tool-call producer before admission; if HTTP beats stdout, wait for that producer with a 5 s deadline, never synthesize it. Use the current `RecordEmbeddedToolInvocation` / `StartEmbeddedToolInvocation` / domain admission / completion witness path for addressed effects. Reuse actual tool `Execute` closures and their checked result callbacks, not copied store calls. After domain completion, append the exact result via checked persistence before returning MCP success or publishing successful completion. The current Pi `AppendMessage` writes some presentation history before its checked append; leave that behavior in phase 1, but make the Claude helper return the checked result first and only then project/publish success. Reuse the store validators, not that early presentation ordering. stdout repeats of an MCP result must match the stored canonical payload and must not append/publish it twice. Failed appends cancel further work; domain-committed but unchecked results remain uncertain for replay.

| Tool | Existing shared path to reuse (no direct shortcut) |
|---|---|
| `mecha_mailbox_send`, `mecha_send_image` | Addressed/image Execute, `RecordEmbeddedToolInvocation`, `StartEmbeddedToolInvocation`, `EnqueueEmbeddedToolMail`/`EnqueueEmbeddedToolImage`, actual Host completion and checked result append. No plain `Enqueue`; attachment writes only after fresh admission. |
| `goal`, `goal_snooze` | Existing goal invocation validation/Start/domain/Complete plus `goalResult` and `CheckEmbeddedGoalResult`; never `UpdateEmbeddedBrowserGoal` for an agent tool. A set remains pending until checked application evidence, not forever and not immediately applied. |
| `mecha_reminder` | Existing fresh-intent/admitted reminder Execute and `reminderResult` pre/post-append handling; no direct unadmitted reminder call. |
| `scratchpad`, `mecha_progress`, `mecha_context` | Existing schema/role-checked Execute, checked producer/result persistence; read-only status has no effect intent. |
| `context_reset`, `mecha_worker_terminate` | Existing turn-scoped request/Execute, checked result, then stop turn and shared lifecycle action. |

Duplicate handling runs **before** fresh admission or attachments. Within the current execution, compare the canonical tool name and original arguments for the same tool-use ID; a name or arguments mismatch conflicts. Serialize duplicates and never execute an uncertain effect again. A checked result may be returned within the current execution. After process exit/restart, an existing historical canonical effect is generically rejected: do not execute it afresh or reconstruct its result, and do not require a name/arguments mismatch classification. Missing evidence or stale grants never authorizes execution.

All router tools, including reads, require a current process grant and checked foreground user/assistant producer. Wait at most 5 s for a racing producer; otherwise reject. No idle status exception, background scope or synthetic authority.

**Tests:** real HTTP MCP/router/SQLite through Playwright, all tools, producer-before/after-HTTP races, current-execution duplicates with matching and mismatched tool names or arguments, foreground/absent scope, stale grants, crash before/after domain commit and checked result append. Assert UNKNOWN is never executed again and an existing historical canonical effect is generically rejected without fresh execution or result reconstruction.

## 7. Phase 5: Local CLI and engine/model selection

**`managed/claude_models.go`.** The model table: ID, CLI alias, display name and context window, all from S1. The engine prefers `modelUsage[<model>].contextWindow` from each `result` when it is present, so a CLI-side change shows up without a router release.

| ID | Alias | Resolves to (S1) | Window (S1) |
|---|---|---|---|
| `claude-code/fable` | `fable` | `claude-fable-5-1` | 1,000,000 |
| `claude-code/opus` | `opus` | `claude-opus-5-5` | 1,000,000 |
| `claude-code/sonnet` | `sonnet` | `claude-sonnet-5-5` | 1,000,000 |
| `claude-code/haiku` | `haiku` | `claude-haiku-5-5` | 1,000,000 |

### Local CLI selection and preflight

Claude is installed and logged in on the router host. Login/logout and credentials are managed externally with `claude`; inherit normal CLI user/project configuration. No Claude provider settings row, connect/login/logout route, provider-auth response/event, periodic polling, credential persistence or fixed-provider-count changes.

Keep `claude-code/<alias>` only as an internal engine/model selection key in the existing launch/model pickers. Offer the known aliases without promising current sign-in availability. On launch and each process acquisition before each execution, resolve `MECHA_CLAUDE_BIN`/PATH/local installation and run a bounded local auth-status check (5 s, no prompt). Missing executable, signed-out, malformed or timed-out status produces a helpful launch/turn error directing the user to install/sign in on the host. Do not create a worker on failed launch preflight. No new polling/cache subsystem; no account identity retained. Engine-specific model choices never become the root/default Pi model or cross-engine switches. Automated tests use a fake executable/auth response only.

**Model plumbing:** Add engine/model keys only where launch/catalog/selection parsing requires them. Do not extend persisted provider constraints or provider-auth parsers. Default/root Pi selection excludes Claude aliases. Reject cross-engine changes server-side and in the picker. Use reported context windows where available. Fake local-preflight tests cover missing/signed-out/timeout/malformed status, no worker on failed launch, model aliases, root/default exclusion and no provider-settings changes.

## 8. Phase 6: Context management

- **Tokens.** The proposed snapshot feeds shared reset checks with Claude reported usage; exact helper signatures follow Phase 1, not the obsolete sketch. The deferred epilogue feeds `updateAgentContextUsage` from the same value.
- **Thresholds (D10).** `ResolveContextThresholds` is unchanged. With 1M windows (S1), the defaults of 10% and 20% are 100k and 200k tokens, above the ~42.5k baseline.
- **Loop guard (Claude agents).**
  - After a reset, the first `ContextTokens()` of the new epoch is recorded.
  - If it is at or above the effective reset threshold, suppress automatic resets while usage remains at or above it. Rearm when reported usage falls strictly below the effective threshold (including after a configuration change that provides headroom), or when the binding/epoch changes. A configuration change without headroom does not rearm; explicit reset remains available.
  - A one-time warning history row is written.
- **Seed rendering, `managed/claude_seed.go`.**
  - Count Unicode code points. Preserve the instruction, bounded scratchpad and complete wake, then include the newest contiguous whole turns that fit. The seed and wake together are at most 200k code points. Reject if mandatory content cannot fit; never submit wake-only.
  - Render preserved conversation under its heading. Drop oldest turns whole, not individual messages, tool results or byte suffixes. Prepend the seed as a separate text block to the original ordered wake blocks. Mail entries are unwrapped as in §5. Reject complete rendered input exceeding 6 MiB or model headroom before submission.
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
- a definitely unsubmitted mid-turn message reserved before a gear-menu reset is delivered in the new epoch; a write-started message without a checked echo stays held, not redelivered.

Also test: the warning reminder fires once per epoch; the loop guard; a per-model threshold override on a `claude-code/*` key; the compaction row.

## 9. Phase 7: Goals

- **Receipt.** Follow §5 checked submission/echo evidence. Commit eligible receipt before final append; never on assistant arrival alone.
- **Evaluator (D9).** `evaluateEmbeddedTurn` gets its model and stream from `m.embeddedGoalEvaluatorModel(saved)`:
  - pi agents: the worker's own, unchanged;
  - `claude-code`: only the usable configured Pi router default, with no fallback search. The stream is built by the same provider switch the subagent runner uses, factored out as `embeddedStreamForModel(model)` (`embedded_subagent_runner.go:~364-406`).
  - If no stream can be built, write a "goal evaluation unavailable" history row and schedule nothing. Without this, the evaluator error would become NOT_DONE and loop continuations up to the cap.
  - The model must have `ContextWindow > 0`.
  - The test seam `m.embeddedGoalEvaluator` is unchanged.
- **Circuit breaker.** The empty-turn breaker and `toolExecuted` use `embeddedBareToolName` (later Claude name mapping).
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
  - Append a visible nonterminal limit notice and hold the active lane until `resetsAt`, including deadlines beyond 24 hours. If no reset time was reported, use now + 15 min.
  - Keep later inputs queued during the hold. At the deadline retry the interrupted input first only if known safe; otherwise hold a write-started input without checked echo and allow later eligible inputs and goal continuations to proceed. Never automatically replay the uncertain input or duplicate an ACK or uncertain effect.
  - Interrupt cancels the hold. The hold is in memory only; a router restart uses the normal interrupted-turn recovery path.
- **Availability failures.** At process acquisition the engine runs the bounded on-demand local preflight (§7). If Claude Code is unavailable, the turn fails fast with "Claude Code is not available: <reason>", using the existing error path.
- **Crash or malformed output.**
  - The turn fails.
  - The history `message` stays generic ("Claude Code turn failed"). The stderr tail goes in the error and output fields, which are not pattern-checked; `managedHistoryText` drops rows whose message or companion text contains paths (`store/managed_history.go:~80-105`).
  - Assistant text that precedes tool calls goes through `message_content` in the same way.
- **Execution exit.** Revoke and reap before the next execution starts with `--resume`.
- **Router shutdown.** `Manager.Close` (`supervisor.go`) stops every Claude process in parallel through `stopWorkerProcess`, before the store closes.
- **Startup sweep.** Runs before `RecoverEmbeddedRoots`:
  - verify each recorded process identity and group before signaling verified leftovers; a zombie leader alone does not prove the group is safe;
  - remove only the matching record after confirming no executable group members remain. Leave uncertain groups untouched, retain their records and fence admission; recovery may require operator resolution and router restart. Revoke stale grants without discarding recovery evidence.

**Tests:** usage-limit hold and resume with queued mail intact; the unavailable path; a crash mid-turn; a crash between turns; `Manager.Close` stops active processes; the sweep reaps a verified planted group and removes its matching record, while retaining uncertain records and fencing admission; a router restart mid-turn leads to `ClassifyEmbeddedRestart` and the interrupted notice, then the Claude engine resumes with `--resume`, the tool effect is not repeated, and a message arriving during the recovery turn waits for normal dispatch.

## 11. Phase 9: Web UI (`web/router-ui-react/src`)

- **Launch/model UI only.** Accept the internal Claude engine/model key where needed. No provider-auth parsing changes, provider row, settings/connect/login/logout flow or polling. Existing provider settings remain unchanged. Host installation/login failures appear on launch/turn, with external CLI guidance.

- **`isClaudeCodeAgent(agent)`** goes in `domain/models.ts`. It checks `agent.managed_agent?.model.provider === "claude-code"`.
- **Hide for Claude Code agents in v1:**
  - `components/AgentControlsDropdown.tsx`: in the `controls` array, the Workflow, Inline tools and Worker tools entries;
  - `ConversationHeader.tsx`: the pinned Tool settings button, the inline-tools toggle, the Worker Tools button and dialog, the Workflow button, modal and dropdown, and the subagent metric card and viewer;
  - `AgentRow.tsx`: the subagent badge.
- **Do not hide** the context-threshold editor (`onContextThresholds` in `App.tsx`). The spec keeps it at parity.
- **Model partition.** `ModelPicker.tsx` replaces the `isEmbedded` filter with an engine partition: a Claude Code agent sees only `claude-code/*`, and other agents never see them. It is computed at the header `ModelPicker` call site in `App.tsx`, with a guard in `changeManagedModel`. The launch dialog's picker shows all supported engine/model choices, without an availability promise for local Claude aliases.
- **Tool icons.** Add icons to `CompleteTimelineEntry.tsx` `getToolIcon` for `grep`, `glob`, `web_search`, `web_fetch`, `task` and `todo_write`. Cosmetic.
- **Tests (Vitest).** Run `ls tests/` at implementation time, because files have moved:
  - unchanged provider dialog regression (no Claude row or auth traffic)
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
  - each foreground execution has a fresh `claude` process that is stopped and reaped at completion;
  - the legacy tmux path is unchanged and separate.
- Note the new `/v1/mcp` route in `router-proxy/README.md`.

## 13. Test strategy

**Fake `claude`, `internal/testfixture/fakeclaude`**
- It is built once per test binary in `TestMain` and selected through `MECHA_CLAUDE_BIN`.
- It speaks stream-json from a JSON script named by `FAKE_CLAUDE_SCRIPT`. Each input's steps can be: thinking, text, `tool_use` (unexpected built-in event for defensive translation coverage), `mcp_call`, `interrupt-wait`, `rate_limit`, `crash`, `hang`, `compact_boundary`, `slow_start`.
- `mcp_call` makes a **real HTTP MCP call** to the router, using `--mcp-config`. It emits `tool_use` before the call and `tool_result` after, so the endpoint, grants and tools are exercised end to end.
- It honours `control_request` interrupt, `--replay-user-messages` echoes for submitted inputs, `--session-id` and `--resume` (a missing session fails as in S4), and `system/init` with capabilities.
- It records PID, argv, environment and stdin: all Run/Continue calls within one foreground scope share its PID, and each subsequent execution starts a new process with a new PID. It can spawn a child and exercise successful cleanup and unconfirmed-cleanup fencing; steered inputs (CC-019) are written to the active process; other queued messages wait for the next execution.

**Required behavioral tests (Playwright).** Planned `tests/e2e/claude-code-workers.spec.ts`: headless UI through real REST/router-proxy/WebSocket/router/SQLite boundaries with fake Claude and deterministic local evaluator, no external LLM. The cases below map acceptance criteria to planned coverage, not passing evidence. Go behavioral tests following `embedded_behavioral_lifecycle_test.go` support but do not replace that suite:

| Test | Covers |
|---|---|
| local preflight, launch/refusal, unchanged settings/no polling, implicit default never Claude | AC1, AC2 |
| composer message with image → Claude stdin has text and image; timeline rows, answer row, activity, context usage; no mail sent | AC3 |
| mid-turn mail and prompts steered into the running process and ACKed at exact echo; unwritten input stays durable; direct HTTP prompt waits unaccepted before reservation and may be lost on cancellation/restart; own answer; redelivery only when definitely unsubmitted; uncertain writes held; no steer during start-up or recovery; reservation released across a reset | AC4 |
| every managed foreground worker process receives `--tools Bash,Read,Write,Edit,Glob,Grep,ToolSearch`; the separate noninteractive internal evaluator remains tool-free, user/project MCP inheritance retained, root/child strict MCP configuration unchanged; every router tool through MCP, de-duplication with a new timestamp, stale binding | AC5 |
| interrupt partial | AC6 |
| restart, terminate mid-turn, router restart mid-turn, orphan sweep | AC7 |
| model change within engine, cross-engine rejection | AC8 |
| the three reset triggers, seed, loop guard, warning reminder, per-model threshold override | AC9 |
| goal loop, cap with `goal` calls, evaluator unavailable | AC10 |
| usage-limit hold | AC11 |
| env, token and file hygiene | AC12 |
| fresh process per foreground execution, resumed conversation, child cleanup and cleanup fencing; model choice on next process; all tools rejected outside checked foreground scope | AC14 |

Required AC13 evidence (the scoped 65-test browser run is green; see the boundary coverage report for its exact scope and remaining gaps):
- applicable existing regressions and changed shared paths, without weakened assertions; the entire existing suite is not claimed green;
- the separately specified reset release/redelivery regression on both engines, plus remaining applicable reset/echo race coverage;
- defer unrelated expanded Pi matrix and a broader Pi-vs-Claude comparison; do not claim them passed.

**UI tests.** Vitest, as listed in §11.

**Optional manual smoke check for later implementation (not a workflow gate)**
- Run the real router, real `claude` on Haiku, and the UI in the in-app browser.
- Exercise launch, chat, tools, reset, goal, interrupt, model change and terminate. Confirm successive executions use distinct processes and preserve conversation; record startup latency and child cleanup.

**Coverage:** `go test -coverprofile` for `internal/managed`, `internal/transport` and `internal/store`, and `vitest run --coverage` for the UI.

## 14. Traceability

| AC | Spec | Plan phases | Tests |
|---|---|---|---|
| AC1 | §3.1 | 5, 9 | local executable/auth preflight; launch error; unchanged settings/no polling |
| AC2 | §3.2 | 3, 5 | launch behavioral |
| AC3 | §3.3 | 1, 2, 3 | conversation behavioral; translator |
| AC4 | §3.4 | 1, separate steer fix, 3 | mid-turn behavioral; steer-release regression |
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
1. Phase 1 Pi-only seam, then evaluate/extract only the shared helpers Claude actually needs. Keep the steer fix separate; no commit instruction applies to this docs task.
2. Then phases 2 → 3 → 4 → 5, the core path; the first end-to-end chat works here.
3. Then 6 → 7 → 8.
4. Phase 9 can start once phase 5's API shape is fixed.
5. Phase 10 comes last.

**Risks**
- **CLI protocol drift.** stream-json and the control protocol are only partly documented. Mitigations: the capability gate, fixtures recorded from real output, and the live proof on each CLI upgrade.
- **Shared-code changes.** The adapter and any later provider-entry/history helpers touch code Pi workers run. Normalization and steer release are excluded from the first slice. Mitigations: the existing suite, parity tests, and separate commits.
- **Startup cost.** Each execution pays CLI and configured MCP server startup latency; no idle Claude memory is retained.
- **Background tradeoff.** Work started by Claude cannot survive execution cleanup; no between-turn notification processing is promised.
- **Cold start** of a few seconds may occur on every execution. Mid-turn mail and prompts are steered into a running process when possible; input not yet written waits in the durable router queue, and direct HTTP prompts wait for the lane without durable acceptance before reservation.
- **User configuration side effects.** Full configuration means the user's hooks and plugins run inside workers. This is the settled configuration contract (spec Alt J), not proof of validated integration.
- **Shared subscription limits** across many Claude workers. The usage-limit hold covers correctness, not capacity.
- **Concurrent edits on `main`.** Three commits landed while this plan was written, one of them deleting a UI component it referenced. Re-check references at the start of each phase.

## 16. Validated Plan Review findings

Historical review checkout: `cbd9352c`; line numbers below describe that checkout, not current implementation status. Recheck function references when using these findings.

| Finding | Actual code evidence | Resolution / verification |
|---|---|---|
| Admission after stdin was too late | `managed/embedded_session.go:1563–1634`, `runEmbeddedSessionTurnScoped` StreamFn: notice start, owner renewal, execution binding, headroom, non-mail entry and goal receipt precede actual stream dispatch. `AppendMessage:289–297` binds user only after checked append. | §5 distinguishes pre-submit admission, checked echo/entry and consumed rejection; no per-request CLI gate claim. CC-004/CC-018 verify stdin/ACK/order/failures and foreground authority. Pi guards unchanged in first extraction. |
| MCP direct handlers bypass checked effects | `managed/embedded_session.go:521–532` rejects successful addressed results without a current completion; `:652–660` selects checked tool/goal result append. `:1731–1980` admission records fresh intents/witnesses. `managed/embedded_addressed_tools.go:145–215` calls Start, checked enqueue, Complete. `store/embedded_tool_admission.go:148–213,331–362` fences producer/original args and uses one-shot domain admission. `managed/embedded_goal_tool.go:130–156` completes the actual goal result before callback. | §6 replaces all skipped-ledger recipes with existing admission/Execute/result helpers; canonical names, exact argument conflicts, current-execution duplicate handling vs UNKNOWN, and checked foreground scope. CC-006–CC-008 cover HTTP/producer races, persistence failure and crash windows. No generic framework. |
| Reset strands unconsumed reservations | `managed/embedded_mail.go:137–174`, `trySteerActiveSession`, reserves before queue/Steer; `store/embedded_router.go:1463–1481`, `ReserveEmbeddedMail`, refuses another binding. `NextEmbeddedSteerMail:1796` excludes existing attempts. `managed/embedded_reset.go:57–64` schedules reset without lane serialization; `ExecuteContextReset` at `embedded_context_reset.go:175–245` snapshots then changes epoch. `ResetEmbeddedSessionEpoch` at `store/embedded_router.go:739–783` advances epoch without releasing attempts. | §5 assigns shared coordinator and atomic store release, drain-before-snapshot, old-callback fence and rollback/recovery rules. CC-005 covers both engines and every reset/echo outcome. Separate behavioral fix after RE-008 baseline, not Pi extraction. |
| Local CLI only (new user requirement) | This is a requested scope correction, not a defect in existing production code. Existing CLI probe observations in spec §6 remain historical protocol evidence only. | §7/§11 remove provider-auth/settings/polling design. CC-001/CC-002/CC-015 prove on-demand launch checks, external login/config inheritance and unchanged provider settings. |

Implementation targets (partially implemented; see root status): existing session/admission/tool/reset/store functions above, Claude process/translator/MCP files listed in §§4–6, launch/model selection/UI in §§7/11. Each acceptance scenario in spec §10 maps to an exact planned Playwright test in §11; detailed §13 tables are summaries, not replacement tests. Implementation must add tests first at real owned boundaries. The root status distinguishes reported passing checks from remaining acceptance work. The full CC-001–CC-018 matrix is not accepted.
