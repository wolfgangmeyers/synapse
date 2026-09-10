# P5: Managed Pi Settled Notification

## Scope

A managed Pi worker can tell one Mailbox recipient when an enabled top-level agent run remains settled past a cooldown.

This keeps notifications off by default. It adds no polling, daemon/service, status scraping, global install, or general lifecycle framework.

## Opt-in and delivery

`worker_launch.py` gains a Pi-only option:

```text
--notify-on-settled <mailbox-recipient>
```

The option configures a possible recipient but does not enable notifications by itself. The launcher validates `<mailbox-recipient>` as a Mailbox agent ID and exports the recipient to that process as `MECHA_PI_NOTIFY_ON_SETTLED`. Managed Pi launches use `--no-extensions` and explicitly load the canonical `pi-subagents` and `mecha-messaging` entrypoints. Other runtimes retain their normal extension policy. It is invalid for non-Pi agents. The agent enables or disables delivery at runtime with the `mecha_settled_notifications` tool.

The extension reads that one process-local value. It uses its existing `createMailboxClient().sendReply()` path, which POSTs the normal Mailbox V2 envelope to `MECHA_ROUTER_URL/v1/messages`. The router owns durable queueing and delivery to the recipient. No new transport, extension installation path, or background process is introduced.

## Behavior

The extension starts disabled and unarmed. `session_start`, connection, registration, resume, and an already-idle session send nothing. `mecha_settled_notifications` supports `status`, `enable`, and `disable`; enabling requires a configured recipient.

For an enabled top-level worker session:

1. `agent_start` arms one pending notification.
2. Repeated `agent_start` events before settlement do not add notifications.
3. On `agent_settled`, the extension schedules the notification after a 120-second cooldown. `agent_settled` is the completion boundary; `agent_end` is not sufficient.
4. New parent activity or child activity before the cooldown expires cancels that idle generation.
5. The extension consumes the pending notification before scheduling the asynchronous `sendReply()` call, so a later `agent_start` can arm one separate run.

The settled event is the direct contract; the extension does not inspect `ctx.isIdle()`. Child subagent sessions remain excluded, matching the existing Mailbox extension behavior.

## Exact Mailbox message

For a worker whose tmux/Mailbox identity is `<worker>` and configured recipient is `<recipient>`, the extension submits exactly:

```json
{
  "id": "<new UUID>",
  "from": "<worker>",
  "to": "<recipient>",
  "subject": "Pi worker settled",
  "body": "Worker <worker> has settled and is idle.",
  "created_at": "<current ISO-8601 UTC timestamp>"
}
```

These are the complete notification metadata fields. The recipient sees the existing Mailbox custom-message metadata unchanged: `message_id`, `from`, `to`, and `subject`. The notification claims router acceptance, not that the recipient has read it.

## Failure behavior

Invalid or absent recipient configuration leaves notifications disabled; invalid launcher input fails the launch before Pi starts. Runtime `disable` clears pending notifications and cancels scheduled sends. A `sendReply()` rejection or timeout sends no completion message, shows one error notification in that worker's Pi UI, and does not interrupt, restart, or keep the worker busy. Later agent work can produce its own notification only after notifications are enabled.

## Focused acceptance tests

1. The Pi launcher omits the recipient environment when the flag is absent; with `--notify-on-settled supervisor`, it validates the recipient and exports `MECHA_PI_NOTIFY_ON_SETTLED=supervisor` alongside the existing router export. Non-Pi use is rejected.
2. A configured extension reports disabled status by default, can enable/disable with `mecha_settled_notifications`, and refuses enable when no recipient is configured.
3. An enabled extension sends no notification at `session_start`, then sends one `sendReply()` call only after `agent_start`, `agent_settled`, and the 120-second cooldown, without calling or requiring `ctx.isIdle()`. Assert the exact `to`, subject, and body above, with `from` supplied by the existing client identity.
4. `agent_end` and repeated `agent_start` events do not send early or duplicate notifications; the following `agent_settled` sends exactly one.
5. With no opt-in recipient, no notification is sent for any lifecycle event. Child sessions also send none.
6. When `sendReply()` rejects, assert one Pi UI error, no success message, no retry, and no change to the worker's ability to settle. A later independent agent run may make one new attempt.
7. With a deferred first `sendReply()`, settle one run, start and settle a second run before the first send resolves, then resolve both sends. Assert that each armed run made exactly one attempt (two total).
