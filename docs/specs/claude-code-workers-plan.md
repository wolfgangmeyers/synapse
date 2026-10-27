# Implementation Plan: Claude Code Workers (v1)

Spec: [claude-code-workers-spec.md](claude-code-workers-spec.md) · Status: draft, pending spec approval · Date: 2026-10-26

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
| D3 | One `claude -p` process per embedded turn. It exits once every input written to it has been answered. | §4.3, Alt D |
| D4 | The Claude session ID is the embedded binding's `SessionID`, a UUIDv4 from `managerIDs.Next`. If it isn't a UUID (only in tests, through the `"%s-s%d"` fallback in `ExecuteContextReset`), the engine uses a UUIDv5 derived from it. The first turn on a binding seeds and uses `--session-id`; later turns use `--resume`. A new nullable column records "Claude session started" per binding. | §3.9, §4.3 |
| D5 | The mirror (`embedded_entries`) keeps Claude's raw tool names (`Bash`, `mcp__mecha__goal`). That keeps the goal-call and unknown-effect checks in `AppendMessage` inert for MCP tools. One bare-name normalizer, `embeddedBareToolName` (strips `mcp__mecha__`), is used at every place that compares router tool names for bookkeeping (Phase 1, item 5). History rows show display names through a per-persistence mapping, which is the identity for pi. | §3.3 |
| D6 | The ACK for a mail message is written when Claude echoes it (`--replay-user-messages`). For a mid-turn message that is when Claude starts answering it (verified). Steer matching is strict for this engine: bytes must match, with no pop-the-head fallback. | §3.4 |
| D7 | Router tools are served by a router-hosted Streamable HTTP MCP endpoint. A fresh token is minted for each turn and kept only in memory. The de-duplication key is `_meta["claudecode/toolUseId"]`. A repeated call looks up its earlier effect by derived ID before acting. | §3.5, Alt H/I |
| D8 | The provider-entry duties now inside the pi `StreamFn` move into one helper with the same conditional structure, and both engines call it. For auxiliary requests, notice start, owner renewal and the goal-policy read still run; only the headroom check, the non-mail marker and the receipt are skipped. | §3.10 |
| D9 | The goal evaluator for Claude Code workers is a router-side pi-go model: the router default model (`store.GetDefaultModel`) if it is a pi-go model, else `antigravity/gemini-3.8-flash`. If no evaluator stream can be built, the turn records "goal evaluation unavailable" and schedules no continuation. | §3.10, Alt G |
| D10 | Context size comes from Claude's reported usage. Threshold precedence is: configured per-model override, then the Claude Code built-in defaults, then the global setting, then the system default. A post-reset loop guard covers the rest. | §3.9 |
| D11 | `context_reset` and `mecha_worker_terminate` copy pi's `Terminate`: the turn stops after the tool result, with no final assistant. pi's rule is at `agent/loop.go:580`. Any partial assistant produced by the engine's own interrupt is discarded, not appended. | §3.9 |
| D12 | `claude-code` is a *virtual* provider. Its status and models are merged in at read time and never persisted, because the provider tables have `CHECK provider IN (…5 names)` (`migrations/086_google_auth_methods.sql`). The UI accepts five or six providers, so older routers keep working. | §3.1 |
| D13 | A usage limit holds the agent's lane in memory until `resetsAt`, then resumes the interrupted work. | §3.11 |
| D14 | No automatic replies, matching current embedded behavior (`0fc20bb`). The final answer is recorded as the worker's answer in history, and the system prompt tells Claude to use `mecha_mailbox_send` to message agents. | §3.3 |

## 2. Phase 0: Spikes before code (about half a day)

| ID | Question | How | Feeds |
|---|---|---|---|
| S1 | Which context window does each alias (`fable`, `opus`, `sonnet`, `haiku`) actually run with in `-p`? Is `[1m]` needed? | `system/init` model plus usage on a large prompt, or `/context` | §7 model table |
| S2 | Does `--permission-mode bypassPermissions` run tools in `-p` with no prompt and no refusal? Or do we need `--dangerously-skip-permissions --settings '{"skipDangerousModePermissionPrompt":true}'`, as `worker_launch.py` uses today? | One Bash-tool probe in a scratch dir. Needs Wolfgang's go-ahead, because it runs an unrestricted agent. | §4 argv |
| S3 | What do `rate_limit_event.status` and `result` look like when the limit is actually hit? | Claude Code docs and changelog, plus defensive parsing. Recheck when it happens naturally. | §10 |
| S4 | Does `--resume <id>` with a missing transcript fail fast with a recognizable error? | Probe with a random UUID | §5 resume fallback |
| S5 | Optional: does `interrupt_send_now_v1` allow real mid-turn steering? | Control-protocol probe | later improvement only |

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
  - S2 may replace the permission flags.
  - `cwd` is the agent's working directory.
- **Binary:** `MECHA_CLAUDE_BIN`, else `PATH`, else `~/.local/bin/claude`. The router is often started from a shell whose `PATH` lacks `~/.local/bin`.
- **Environment:** `os.Environ()` minus `TMUX`, `TMUX_PANE`, `CLAUDECODE` and `CLAUDE_CODE_ENTRYPOINT` (AC12).
- **Process group:** `Setpgid`. Reuse the probe and reap pattern in `process.go:~180-240`.
- **Process record:** `<SessionRoot>/claude-proc/<agent>.json` holds `{pid, pgid, started_at, binding}`. It is written after start and removed after exit.
- **stdin:** a single locked writer of JSON lines.
- **stdout:** a scanner with a 16 MiB line limit.
- **stderr:** a 64 KiB ring buffer.
- **`Interrupt()`:** writes `{"type":"control_request","request_id":…,"request":{"subtype":"interrupt"}}` and waits for `control_response`.
- **`Stop()`:** close stdin, wait 5 s, SIGTERM the group, wait 2 s, then SIGKILL the group.
- **Version gate:** the `system/init` `capabilities` must include `interrupt_receipt_v1`. Otherwise fail with "Claude Code CLI too old".

**`managed/claude_stream.go`** decodes events and translates them to `ai` messages:

| Event | Handling |
|---|---|
| `system/init` | Record the model ID. Check `session_id` matches. One arrives per Claude turn. |
| `system/compact_boundary` | Write a history row of kind `compaction` (§8). |
| `system/status`, `post_turn_summary`, `api_retry`, `hook_*` | Ignore; log at debug. |
| `assistant` (main thread, `parent_tool_use_id == null`) | Coalesce consecutive events with the same `message.id` into one `ai.AssistantMessage`: text → `TextContent`, thinking → `ThinkingContent`, `tool_use` → `ToolCall{ID, Name: raw, Arguments}`. Record usage. |
| Stop reason | A message with tool calls gets `StopToolUse`. A text-only message is held until the next event: `result/success` makes it `StopStop`; `result` after a user interrupt makes it `StopAborted`; any other `result` error makes it `StopError` with the result text. An interrupt the engine sent for D11 discards the held or partial message. If a non-`result` event follows a text-only message, which is not expected, its text becomes leading text of the next assistant message. |
| Assistant fields | Always fully populated, because `embeddedCheckedStop` (`store/embedded_restart_wake_origin.go:44-80`) requires them: `api="claude-code"`, `provider="claude-code"`, `model`, the full `usage` with zero costs, and `timestamp`. |
| `user` with `isReplay` | Matches the oldest pending input (FIFO, single writer) and triggers that input's `AppendMessage` (§5). |
| `user` with `tool_result` blocks | `ai.ToolResultMessage{ToolCallID, ToolName: raw name from the tool_use map, Content: text blocks joined (images become "[image]"), IsError}`, passed as a value. |
| `parent_tool_use_id != null` | Subagent internals. Ignored in v1: not mirrored and not counted in usage. |
| `rate_limit_event` | Status other than `allowed` or `allowed_warning` raises the usage-limit signal with `resetsAt` (§10). |
| `result` | Ends one Claude turn. Error subtypes carry the error text. |
| `stream_event`, unknown types | Ignore. |

- **Context tokens** are taken from the last main-thread assistant usage: `input_tokens + cache_creation_input_tokens + cache_read_input_tokens`.
- **Display names** (`historyToolName` for this engine):
  - `Bash→bash`, `Read→read`, `Edit/MultiEdit/NotebookEdit→edit`, `Write→write`, `Grep→grep`, `Glob→glob`, `WebSearch→web_search`, `WebFetch→web_fetch`, `Task/Agent→task`, `TodoWrite→todo_write`;
  - `mcp__mecha__X→X`;
  - any other `mcp__s__t` becomes `s_t`.

**Tests.** Table-driven translator tests over recorded stream fixtures: tool round trip, text-only final, user interrupt partial, D11 discard, error result, coalescing, subagent events ignored, compaction, rate limit. Process tests use the fake binary (§13) for env scrubbing, process-group kill, version gate and stderr cap.

## 5. Phase 3: Claude turn engine

**`managed/claude_engine.go`.** `claudeTurnSession` implements `embeddedTurnSession` for one embedded turn.

**Rendering inputs for Claude**
- Mail user messages are stored as raw `embedded-mail/v1` JSON (`store/embedded_router.go:~1493,2057`).
- Claude's stdin gets text from `unwrapEmbeddedMailText` (`embedded_automatic_fit.go:~173-192`) plus image content blocks loaded from the message's attachments. Reuse the attachment loading in `buildSteerUserMessage` (`embedded_mail.go:~178-251`). This is what AC3's image requirement depends on.
- The mirror always gets the **original** value, so the ACK byte-match holds.
- Plain prompts, continuation prompts and notices are sent as text.

**`RunMessages(ctx, msgs)`**
1. Ensure the process is running. If the binding is not yet started (D4), build the seed (§8) and launch with `--session-id`. Otherwise launch with `--resume`. If resume fails as "not found" (S4), clear the marker, reseed, and relaunch once.
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
7. Any `AppendMessage` error, for example a fence lost to terminate, triggers `Stop()` and returns the error.
8. Return when every written input has produced its `result`. Then close stdin and wait for exit. `CanSteer()` returns false from this point.

**Other methods**
- **`Run(text)`** is `RunMessages([UserMessage{text, ts}])`.
- **`Continue(ctx)`** resends the rendered last durable user entry without appending it. It is used by interrupted wakes and `resumeUnentered`.
- **`CanSteer()`** is true only when all of these hold:
  - the process is running;
  - the first input has been echoed;
  - stdin is still open;
  - the turn is neither a claimed wake nor an interrupted-notice turn (`p.wake == nil && p.notice == nil`).

  Steers during wake or notice turns are rejected at `embedded_session.go:~483-485,539-559`, so those messages wait for normal dispatch (spec §3.4).
- **`TrySteer(msg)`**
  - Rechecks `CanSteer()` under the engine lock.
  - Writes the rendered message to stdin and pushes the original value onto the pending FIFO.
  - The echo arrives when Claude starts answering it. `AppendMessage` then ACKs it through `popSteerAttempt` (strict mode, D6) and switches `p.mailAttempt`.
  - A second `StopStop` final within the same mail, prompt or continuation turn is accepted, because finals are unique per attempt (`store/embedded_reply.go:~86-90`). The fact-check confirmed this.
- **`Abort()`**
  - Sends `Interrupt()`, waits up to 5 s for `result`, otherwise `Stop()`.
  - Returns an error wrapping `context.Canceled`, so `ResetEmbeddedAgentInterrupt` runs.
  - The partial assistant is appended with `StopAborted`, matching pi's synthetic aborted message.
- **`HasUser()`** is true if any user entry exists in `ReopenEmbedded(binding)` or one has been appended in this turn. Without it, the initial instruction would be re-sent on bare and recovery turns.

**Terminate and gear-menu reset**
- In the termination entry, before it waits on `lane.serial` (`supervisor_termination_s5.go`): if the agent is `claude-code` and has an active session, call `Abort()`.
- The gear-menu reset (`embedded_reset.go:~57`) runs `ExecuteContextReset` without `lane.serial`. For `claude-code` agents, first `Abort()` the active session and wait for the turn to exit. Steer reservations are then released by Phase 1 item 7 before the epoch changes.

**System prompt file**
- It contains:
  - a Claude-specific base;
  - the identity block from `buildEmbeddedWorkerSystemPrompt`;
  - how messages arrive (`[Direct message from X]: …`);
  - "your final answer is shown to the user; nothing is sent automatically; use `mecha_mailbox_send` to message other agents";
  - each router tool's prompt guidelines.
- It is written to `<SessionRoot>/claude-mcp/<agent>-<turn>.prompt` (0600) and deleted at turn end.

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
- `mintClaudeMCPGrant(agentID, binding, turnID)` returns a token: 32 random bytes, base64url. Only `sha256(token)` is kept, mapped to the grant.
- The token goes only into the 0600 config file `{"mcpServers":{"mecha":{"type":"http","url":"http://127.0.0.1:<port>/v1/mcp","headers":{"Authorization":"Bearer <token>"}}}}`.
- The grant is revoked at process exit.

**Every tool call does this first:**
- the grant binding must equal `m.embeddedOwners[agent].binding`, otherwise the error "stale session" (AC5);
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

## 7. Phase 5: Model and provider plumbing

**`managed/claude_models.go`.** The model table: ID, CLI alias, display name, context window from S1, and the built-in threshold default (§8).

| ID | Alias | Window (S1 to confirm) |
|---|---|---|
| `claude-code/fable` | `fable` | 1,000,000 |
| `claude-code/opus` | `opus` | 1,000,000 |
| `claude-code/sonnet` | `sonnet` | 1,000,000 |
| `claude-code/haiku` | `haiku` | 200,000 |

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
- **Thresholds (D10).** In `ResolveContextThresholds`, insert the Claude Code built-in tier after the configured per-model overrides and before the global tier.
  - 1M-window models: 10%/20%, which is 100k and 200k tokens, above the ~42.5k baseline.
  - 200k-window models: 50%/60%, which is 100k and 120k tokens.
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
- the epoch marker;
- the context bar drop;
- a mid-turn message reserved before a gear-menu reset is delivered in the new epoch.

Also test: the warning reminder fires once per epoch; the loop guard; threshold precedence; the compaction row.

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
- **Startup sweep.** Runs before `RecoverEmbeddedRoots`:
  - for each `claude-proc/*.json`, if the process is alive with a matching start time, kill its group;
  - delete all the process records and all `claude-mcp/*` files.

**Tests:** usage-limit hold and resume with queued mail intact; the unavailable path; a crash mid-turn; the sweep kills a planted process and removes files; a router restart mid-turn leads to `ClassifyEmbeddedRestart` and the interrupted notice, then the Claude engine resumes with `--resume`, the tool effect is not repeated, and a message arriving during the recovery turn waits for normal dispatch.

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
  - the legacy tmux path is unchanged and separate.
- Note the new `/v1/mcp` route in `router-proxy/README.md`.

## 13. Test strategy

**Fake `claude`, `internal/testfixture/fakeclaude`**
- It is built once per test binary in `TestMain` and selected through `MECHA_CLAUDE_BIN`.
- It speaks stream-json from a JSON script named by `FAKE_CLAUDE_SCRIPT`. Each input's steps can be: thinking, text, `tool_use` (built-in, answered with a canned result), `mcp_call`, `interrupt-wait`, `rate_limit`, `crash`, `hang`, `compact_boundary`, `slow_start`.
- `mcp_call` makes a **real HTTP MCP call** to the router, using `--mcp-config`. It emits `tool_use` before the call and `tool_result` after, so the endpoint, grants and tools are exercised end to end.
- It honours `control_request` interrupt, `--replay-user-messages` echoes (mid-turn inputs are echoed only after the current `result`), `--session-id` and `--resume` (a missing session fails as in S4), and `system/init` with capabilities.
- It logs argv, environment and stdin to a file for assertions: env scrubbing, seed content, image blocks, session flags, and that the token appears nowhere in argv.

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
| the three reset triggers, seed, loop guard, warning reminder, threshold precedence | AC9 |
| goal loop, cap with `goal` calls, evaluator unavailable | AC10 |
| usage-limit hold | AC11 |
| env, token and file hygiene | AC12 |

AC13 is covered by:
- the existing suite, unchanged and green;
- the shared steer-release regression test run on both engines;
- a pi-vs-Claude parity test on the same scripted scenario that compares the resulting history rows (kinds, display names, outcomes).

**UI tests.** Vitest, as listed in §11.

**Live proof (dev-loop Phase 3 and the browser-evidence gate)**
- Run the real router, real `claude` on Haiku, and the UI in the in-app browser.
- Exercise: launch with a worktree; chat with an image; a tool card; the scratchpad tool; gear-menu Reset, checking the seed and that the worker continues from the scratchpad; goal set, then continuation, then DONE; interrupt; model change; terminate.
- Capture screenshots, and record per-turn start latency.

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
- **Per-turn start latency** of a few seconds. It is measured in the live proof, and steering is blocked until the first echo. If latency hurts, the interface allows a long-lived process later (spec Alt D).
- **User configuration side effects.** Full configuration means the user's hooks and plugins run inside workers. This was accepted (spec Alt J).
- **Shared subscription limits** across many Claude workers. The usage-limit hold covers correctness, not capacity.
- **Concurrent edits on `main`.** Three commits landed while this plan was written, one of them deleting a UI component it referenced. Re-check references at the start of each phase.
