# Sentinel runtime recommendation

## Scope

**Outcome:** identify the supported Pi completion boundary for the isolated child.

**Non-goals:** no runtime retry, code change, install, router mutation, or model run.

## Finding

The current child closes RPC stdin immediately after submitting its prompt:

- `pi-extensions/mecha-messaging.js:243` discards stdout and stderr.
- `pi-extensions/mecha-messaging.js:247` calls `child.stdin.end(...)`.

That is incompatible with Pi RPC mode. In Pi `0.80.10`, stdin EOF calls `shutdown()` (`dist/modes/rpc/rpc-mode.js:619-621`), which disposes the runtime and exits `0` (`:559-575`). The prompt response only means preflight accepted the request; it does not mean a model result exists (`docs/rpc.md:45-76`). This explains the repeatable accepted-turn/settled/exit pattern. The current launcher both requests shutdown and throws away the only supported result/error channel.

The existing automatic relay is also looking at the wrong completion signal. `agent_settled` means Pi has no automatic continuation left. It carries no result (`docs/extensions.md:551-564`). A result is carried by the assistant `turn_end.message` or `agent_end.messages`; model failures are also represented as an assistant message with `stopReason: "error"` and `errorMessage` (`pi-agent-core/dist/agent.js:338-353`).

## Recommendation

Keep `--mode rpc`. It is Pi's supported subprocess-isolation protocol. Replace the fire-and-forget launch shape with a small child-local RPC host:

1. Spawn Pi with all three streams piped. Keep stdin open after writing one JSONL `prompt` command with a request id. Do not end stdin until terminal handling is complete.
2. Drain stdout continuously with Pi's strict LF-only JSONL framing. Do not use `readline`; Pi explicitly says it is not protocol-compliant (`docs/rpc.md:28-37`). Treat stdout as private transport data: discard user events immediately and never log/persist raw frames.
3. Require the prompt `response` with the matching id. `success: false`, malformed JSON, child exit, or `extension_error` is terminal failure.
4. Record the final assistant message from `turn_end`/`agent_end`, including `stopReason` and `errorMessage`. Wait for `agent_settled`, then accept only a final assistant message with non-empty text and `stopReason: "stop"` (or an explicitly approved non-error terminal reason). `agent_settled` without that message is failure, not success.
5. Relay that bounded assistant text through the fixed ordinary child-send path, then close stdin and wait for process exit. Keep the result only in the private runner long enough to send it; do not inject it into the parent Pi session or Sentinel lifecycle data.

Pi also exposes `get_last_assistant_text` as an RPC command after settlement (`docs/rpc.md:722-740`; `rpc-mode.js:499-501`). It is useful as a consistency check, not as the primary signal: it does not carry `stopReason` or `errorMessage`, and a future multi-turn child could make “last” ambiguous.

## Required review tests

Use a fake JSONL child, not a model run, to prove:

- stdin remains writable through settlement, then is closed;
- assistant success relays once only after `agent_settled`;
- an assistant error, missing assistant, malformed frame, stdout EOF, stderr/exit, and prompt rejection are all terminal failures with no relay;
- user frames containing the opaque prompt are discarded and never reach logs, session entries, lifecycle records, or diagnostics;
- the parser handles LF framing and fragmented stream chunks.

The isolated profile currently also disables all built-in tools (`mecha-messaging.js:230`). That cannot satisfy the watchdog child requirements. Keep it as a separate policy decision after the completion host is reviewed; do not hide it inside this fix.
