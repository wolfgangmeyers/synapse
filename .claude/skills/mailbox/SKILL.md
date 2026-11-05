---
name: mailbox
description: Mailbox V2 async mailbox — Mailbox V2 router delivery formats, mandatory pre-read blocks, and tools. Load whenever you act as supervisor or handle worker mail; companion to the supervisor skill.
---

# Mailbox Skill

Mailbox V2 sends durable agent messages through the local router. In a Pi session with the Mailbox extension, use the native top-level `mecha_mailbox_send` tool first. It accepts `recipient`, `body`, and an optional `subject`, and returns the accepted message ID and state. It sends through the extension's existing Mailbox V2 HTTP client and does not run shell commands.

Use the repository script only when the native tool is unavailable (for example, Claude sidecars, bare Codex, or older Pi sessions). From the repository root:

```bash
python3 tools/mailbox_send.py --from <verified-agent-id> --to <agent> --body "<text>"
```

Use only your verified sender agent ID; do not invent or impersonate an identity. `--from` is required outside a valid current tmux pane or when its identity cannot be resolved. In a valid tmux pane, you may omit it to use the current window name, subject to the identity restrictions below. From another working directory, use the path to `tools/mailbox_send.py` in your mecha-developer checkout.

The script defaults to the local HTTP router; an explicitly configured invalid router URL is rejected. It prints the accepted message ID and actual router state. Router acceptance does not prove the recipient consumed the message.

## Load ordering

**If you are the supervisor**, load **`supervisor/SKILL.md` first, then this skill** (or read both before taking supervisor actions). The supervisor skill orchestrates workers and state; this skill defines **what counts as mail** and **how to process delivered messages**.

## Delivery formats

Mailbox delivery format depends on the verified runtime integration:

- Pi workers use the managed Mailbox V2 extension.
- Claude workers use the launcher-managed Mailbox sidecar.
- Direct chat delivery may appear as a mailbox block in the conversation.
- Bare Codex launched by `worker_launch.py` has no launcher-provided Mailbox delivery bridge.

Do not assume a sent message reached a worker from router acceptance alone. For critical handoffs, verify receipt with a worker reply, runtime delivery evidence, or `worker_capture.py`.

## When mailbox content appears in chat

Treat any delivered mailbox content in chat as a real delivery, whether it appears as a `[MAILBOX]` block, a `Mailbox message from ...` block, or another verified runtime-delivered format.

1. If the delivery includes a mandatory pre-read/state-update instruction, run the exact `state_update_status.py ...` command shown before treating the message body as processed.
2. Then read the message body.
3. Then act: reply, redirect, park, verify, or update state as needed.

Skipping a mandatory pre-read update when one is present is a protocol violation because state and message handling are now out of order.

## Reply discipline: preserve the channel

When Wolfgang sends a message by mailbox, assume he may be communicating from his phone or another remote UI. If a response is needed, reply by mailbox instead of printing the substantive response only in the terminal/console. Keep the answer in the same channel he used.

## Discord replies

Replies to Discord use `to: pioneer-square` and subject `pioneer-square.route.v1:<route>` with the displayed canonical route. Omitted or malformed routes are rejected; never rely on the configured inbound channel.

## Reply discipline: avoid acknowledgment loops

Do **not** reply to messages that are only acknowledgments, confirmations, or promises to stay parked, unless the message also includes a new instruction, new evidence, a blocker, a question, or a state transition that needs routing. Examples that normally need **no reply**: "Acknowledged", "Confirmed", "I'll stay parked", "I'll wait for review", or restating an instruction back to the supervisor.

When closing a loop, send at most one acknowledgment. If the next message is another pure acknowledgment, do not answer it; update state only if needed.

## Update discipline: avoid noisy small updates

Mailbox is for durable coordination, not streaming every tiny step. Avoid frequent small progress messages that only say you are continuing, starting a command, waiting, or making expected incremental progress.

Send mail when it changes what the recipient can do or needs to know: a blocker, a decision point, a meaningful status transition, a completed validation result, a push/head change, a scoped plan before risky work, or a final/parked handoff. Batch related facts into one useful message instead of several small ones.

If a supervisor or worker has already asked you to stay parked or continue, do not send periodic "still parked" or "still working" messages unless new evidence, a delay/blocker, or a requested checkpoint is due.

## Canonical implementation references

The checked-in Mailbox V2 behavior is implemented in the supervisor router/tools and exercised by the mailbox tests in this repository. Do not rely on an external standalone mailbox spec file unless it exists in the checkout you are reading.

## Tools (supervisor tools path)

| Script | Purpose |
|--------|---------|
| `mecha_mailbox_send` | Primary Pi top-level tool for posting a message through the Mailbox V2 router |
| `mailbox_send.py` | Fallback sender for runtimes without the native Pi tool |

Repository fallback: `tools/mailbox_send.py` (relative to the mecha-developer checkout).

## Supervisor window identity

Agents derive their mailbox identity from the current tmux pane's window name. Name the supervisor window `supervisor` and each worker window with its intended agent ID; names must use letters, digits, `_`, or `-` and start with a letter or digit.

## See also

- **`supervisor`** skill — state, workers, `state_update_status.py`
- **`worker`** skill — worker mailbox reply patterns
