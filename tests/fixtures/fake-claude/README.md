# Local fake Claude CLI

Supporting fixture for `CLAUDE_CODE_ADAPTER_PLAN.md` phase 3 and detailed plan
§4/§13. Not a runtime adapter or router E2E test. Only literal loopback HTTP
MCP calls and a fixed Node child are allowed. No external network, models,
credential APIs, arbitrary commands or production MCP server implementation.

Set `MECHA_CLAUDE_BIN` to the absolute path of executable `claude.mjs`.
Requires Node on PATH; no build or package installation. Go tests can use
`exec.Command(binary, args...)`; Playwright can pass the same environment to
the router. Use a fresh private temporary directory per test.

## Script contract (v1)

`FAKE_CLAUDE_SCRIPT` names a JSON file (maximum 1 MiB). Omit for default
signed-in auth and a text answer per input. Root fields:

- `auth`: `{loggedIn:false}`, `{raw:"malformed",exit_code:2}`, or `{hang:true}`.
  Default status is `{loggedIn:true,authMethod:"claude.ai",subscriptionType:"max"}`.
- `startup`: steps before stdin processing (default empty).
- `turns`: array of step arrays, consumed FIFO per user. Missing turn defaults
  to `[{text:"fake answer"}]`. Empty turn emits only init, echo, result.
- `resume_missing`: fail only `--resume`, with probe stderr, error result and exit 1.
- `refuse_model`: return correlated error for `set_model`.
- `capabilities`: override init capabilities (default `interrupt_receipt_v1`).
- `interrupt_output`: exactly two bounded strings emitted as distinct assistant events before an active turn acknowledges interrupt. Records their emission for deterministic termination testing.
- `steer_mid_turn` (boolean, default off): a user line written while a turn is active is echoed immediately (with `--replay-user-messages`, unless `replay_control` is `skip`) and recorded as `{kind:"steer_echo",input_id}`, then held for the active turn. The active turn consumes held input at its next `steer` step (same process, same turn, no new `start`), recording `{kind:"steer_absorbed",turn_input_id,input_id}`. Held input not absorbed before the turn ends or is interrupted becomes ordinary pending input for a later execution. Default off keeps the queued behavior (echo only after the current `result`). This models the steering requirement; it is not evidence of real Claude CLI behavior, which spec §6 still lists as to-verify.

Steps have exactly one of these keys:

| Step | Meaning |
|---|---|
| `text`, `thinking` | String, emitted as an assistant content block |
| `tool` | `{id,name,input,result,is_error}`: assistant tool_use then user tool_result; no actual tool execution |
| `mcp_call` | `{id,name,input?}`: real loopback HTTP `tools/call`, not canned success (see below) |
| `spawn_child` | `{}`: one fixed Node child, same process group, max lifetime 60 seconds; emits PID after readiness |
| `emit` | Exact JSON event, no rewriting; supports background/subagent/compaction/error protocol cases |
| `raw` | Exact stdout line followed by newline; string or `{text:"x",repeat:16777217}` (expanded max 32 MiB) for oversized scanner tests without large scripts |
| `stderr` | Exact stderr bytes |
| `wait` | `"interrupt"`: pause turn until control interrupt; queued users are not echoed yet |
| `hang` | `true`: stall until process killed; stdin controls still read |
| `crash` | Integer exit code (1–255), immediate exit |
| `rate_limit` | `{resetsAt,rateLimitType?}`: rejected event, API-error assistant, error result |
| `steer` | String: consumes all inputs steered into the active turn so far (`steer_mid_turn`), emitting one assistant text per input. No-op when none are held |

Example:

```json
{"turns":[[{"text":"partial"},{"wait":"interrupt"}],[{"text":"second"}]]}
```

CLI uses the documented `-p --input-format stream-json --output-format
stream-json --verbose --replay-user-messages --model sonnet
--permission-mode bypassPermissions --session-id <uuid>` flags; also accepts
`--resume`, `--mcp-config`, `--append-system-prompt-file`. Files passed in CLI
flags are recorded; only `--mcp-config` is read, lazily when `mcp_call` runs.
Prompt files are never opened/executed. `auth status --json` works separately.
Unknown flags/config keys fail visibly (exit 2).

Each user turn emits system/init, an unchanged input with `isReplay:true`
(only with replay flag), scripted steps and result. UUID and image/text blocks
are preserved. Process stays alive until EOF. Interrupt uses
`{type:"control_request",request_id:"id",request:{subtype:"interrupt"}}`;
response is `{type:"control_response",response:{subtype:"success",request_id:"id",response:{}}}`,
then active turn gets result/error_during_execution. Model switch uses
`subtype:"set_model",model:"haiku"`; success resolves the model and emits a
non-replay local-command user event. Next turn reports the new model.
Aliases match installed probe docs (historical CLI 2.1.308), not a promise
about future real CLI versions. No transcript persistence is simulated:
resume succeeds unless explicitly configured missing.

For background scenarios, script exact system/task_notification,
background_tasks_changed and task_updated records using `emit`. For idle
unprompted completion, follow with explicit init/assistant/result records.
For in-turn completion emit the replay user with task-notification origin.
These raw events supply notification protocol evidence. Use `spawn_child`
separately for real process-group lifetime assertions.

## Evidence and retention

`FAKE_CLAUDE_RECORD` is a caller-owned JSONL path, appended across invocations
so preflight/reuse/restart can be counted. Start record:
`{kind:"start",argv,cwd,env}`; stdin: `{kind:"stdin",line}` (original line
without newline). Env includes only TMUX, TMUX_PANE, CLAUDECODE,
CLAUDE_CODE_ENTRYPOINT and FAKE_CLAUDE_* configuration keys used here.
Never records ambient credentials. Input/argv may contain test secrets: do
not use real credentials. Records are mode 0600. Default combined file cap
8 MiB (`FAKE_CLAUDE_RECORD_LIMIT` may lower it); overflow fails closed with
exit 2, never silently drops evidence. One stdin line max 16 MiB, at most
64 pending users / 6 MiB queued bytes. Overflow also fails closed.
No persistent fixture cache, transcript store or automatic archive.
Caller must kill/reap on timeout and remove its temporary directory in
finally/cleanup. With `spawn_child`, send signals to the **process group**,
not just the parent, especially for SIGKILL. Parent normal exit, crash step,
SIGTERM and SIGINT kill/reap its child before exiting. When a child exists,
handled SIGTERM/SIGINT exit with codes 143/130; without a child, original
signal termination behavior is preserved. Child also self-exits after 60 s
if an assertion failure kills only the parent. No archive is created.

Run supporting subprocess tests: `node --test tests/fake_claude.test.mjs`.
Tests use stdout records as barriers, not sleeps. Router/browser/SQLite E2E
coverage belongs to the adapter integration and is not claimed here.

## Real local MCP boundary

Pass a file path through `--mcp-config` containing the standard shape:

```json
{"mcpServers":{"mecha":{"type":"http","url":"http://127.0.0.1:8765/v1/mcp","headers":{"Authorization":"Bearer test-token"}}}}
```

Only `mcpServers.mecha` is used; other servers are ignored. Config read is
bounded to 1 MiB. URL must use plain `http:` with literal `127.0.0.1` or
`[::1]`; no DNS, remote hosts, embedded credentials, fragments, redirects
or proxies. Headers are passed through (up to 32, 8192 characters each),
validated by Node HTTP rules. Host/content-length/transfer-encoding/connection
are fixture-owned and cannot be overridden. No config or headers are recorded.
Use test tokens only. Authorization is not invented or obtained elsewhere.

Script example:

```json
{"turns":[[{"mcp_call":{"id":"call-1","name":"scratchpad","input":{"action":"read"}}},{"text":"done"}]]}
```

`name` is the canonical unprefixed MCP name. The fixture emits/flushed the
actual assistant producer `tool_use` with name `mcp__mecha__scratchpad`, ID
`call-1` and the actual arguments **before opening HTTP**. For each call it
POSTs `initialize` with protocol version `2025-03-26`, sends the
`notifications/initialized` notification (expecting HTTP 202), then calls
`tools/list` and `ping` before POSTing:

```json
{"jsonrpc":"2.0","id":"mcp-fake-N","method":"tools/call","params":{"name":"scratchpad","arguments":{"action":"read"},"_meta":{"claudecode/toolUseId":"call-1"}}}
```

JSON-RPC request ID is deterministic and distinct from the tool-use ID.
The server must reply HTTP 200 with JSON, matching JSON-RPC ID and `result`
containing an array `content` and optional boolean `isError`. Requests include
`MCP-Protocol-Version: 2025-03-26`; no SSE or session headers are simulated.
Producer is
written first but a real router must still handle HTTP/stdout scheduling races.

Returned `result.content` is emitted unchanged (including text/image blocks)
as the user `tool_result.content`; `result.isError` maps to `is_error`
(default false). No canned effect success. MCP `isError:true` is a real tool
error result and does not itself crash the conversation. Structured sibling
fields outside content/isError are not mapped into Claude content blocks.
HTTP failures (including redirects), JSON-RPC errors/mismatched IDs, malformed
or oversized responses and connection/deadline failures exit 2 with a generic
MCP diagnostic and **no successful result**. Response body max 1 MiB; total
request deadline 5 s. Interrupt cancels in-flight HTTP, discards late output,
and leaves subsequent inputs usable; no automatic HTTP retry.

## Controlled child boundary

`{"spawn_child":{}}` starts the current Node executable with a fixed program,
no shell, no inherited credentials, no arbitrary script/arguments, no detached
group. At most one child per fake process, bounded to 60 s. It survives normal
turn results and stays in the parent's process group. After IPC readiness the
fixture writes bounded record
`{kind:"child",pid,parent_pid,max_lifetime_ms:60000}` and emits
`{type:"system",subtype:"fixture_child",pid,parent_pid,max_lifetime_ms:60000}`.
The IPC channel is then closed. `ignore_sighup: true` makes both fake processes
ignore SIGHUP for Linux orphaned stopped-group recovery tests; SIGCONT still
resumes them and their normal 60-second bound remains. This event is fixture metadata, not a claimed
real Claude event. Group termination tests should launch the parent with
`Setpgid` (Go) or `detached:true` (Node POSIX), observe this event, then signal
the negative parent PID and wait/reap. Tests also kill the known child in
cleanup if an assertion fails. No background notification is fabricated from
the child exit; use explicit `emit` steps for that independent protocol.

## Opt-in per-execution scripts

For a CLI process per execution, `turns` alone restarts at its first entry on
 each process. Instead set `executions_by_input` to select a whole process-local
 turn script from the first user input:

```json
{"executions_by_input":[
  {"contains":"first task marker","turns":[[{"text":"first answer"}]]},
  {"contains":"second task marker","turns":[[{"text":"second answer"}]]}
]}
```

The first user message's JSON-serialized `message.content` must contain exactly
one nonempty marker. No match or multiple matches fails with exit 2. The chosen
rule remains fixed for later inputs in that process; its turns advance FIFO and
exhaustion uses the normal `fake answer` fallback. Up to 64 rules are allowed.
This option cannot be combined with `turns` or `turns_by_input`. Auth status
never selects a rule; no persistent counter or shared state is needed. Use
unique test-only markers and a fresh script per test. Without this option all
existing selection and process-local behavior is unchanged.
