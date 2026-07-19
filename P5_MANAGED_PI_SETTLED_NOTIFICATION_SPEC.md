# P5: Managed Pi Settled Notification

## Scope

An opted-in managed Pi worker tells one Mailbox recipient when it has completed agent work and is actually idle.

This does not change the default worker behavior. It adds no polling, timers, daemon/service, status scraping, global install, or general lifecycle framework.

## Opt-in and delivery

`worker_launch.py` gains a Pi-only option:

```text
--notify-on-settled <mailbox-recipient>
```

The option is off by default. The launcher validates `<mailbox-recipient>` as a Mailbox agent ID, loads the existing explicit `mecha-messaging.js` extension as it already does, and exports the recipient to that process as `MECHA_PI_NOTIFY_ON_SETTLED`. It is invalid for non-Pi agents.

The extension reads that one process-local value. It uses its existing `createMailboxClient().sendReply()` path, which POSTs the normal Mailbox V2 envelope to `MECHA_ROUTER_URL/v1/messages`. The router owns durable queueing and delivery to the recipient. No new transport, extension installation path, or background process is introduced.

## Behavior

The extension starts unarmed. `session_start`, connection, registration, resume, and an already-idle session send nothing.

For an enabled top-level worker session:

1. `agent_start` arms one pending notification.
2. Repeated `agent_start` events before settlement (for example automatic retry) do not add notifications.
3. On `agent_settled`, the extension sends only if it is armed and `ctx.isIdle()` is true. This is the completion boundary: Pi documents `agent_settled` as after automatic retries, compaction retries, and queued continuations. `agent_end` is not sufficient.
4. On a qualifying settled event, the extension synchronously consumes (claims) the pending notification generation before it begins the asynchronous `sendReply()` call. That generation is no longer armed while its send is pending. A later `agent_start` therefore arms a separate generation; the first send's resolution may report only the outcome of its own claimed generation and must not disarm, rearm, or otherwise affect the later one.

If another extension has started work so `ctx.isIdle()` is false at `agent_settled`, nothing is sent yet and the pending notification stays armed for the subsequent settled event. Child subagent sessions remain excluded, matching the existing Mailbox extension behavior.

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

Invalid or absent opt-in configuration leaves notifications disabled; invalid launcher input fails the launch before Pi starts. A `sendReply()` rejection or timeout sends no completion message, shows one error notification in that worker's Pi UI, and does not interrupt, restart, or keep the worker busy. It is not retried by a timer, polling loop, or a new service. The generation was consumed before that attempt, so later agent work can produce its own notification without waiting for or being changed by this send's resolution.

## Focused acceptance tests

1. The Pi launcher omits the opt-in environment when the flag is absent; with `--notify-on-settled supervisor`, it validates the recipient and exports `MECHA_PI_NOTIFY_ON_SETTLED=supervisor` alongside the existing router export. Non-Pi use is rejected.
2. An enabled extension sends no notification at `session_start`, then sends one `sendReply()` call only after `agent_start` followed by `agent_settled` with `ctx.isIdle() === true`. Assert the exact `to`, subject, and body above, with `from` supplied by the existing client identity.
3. `agent_end`, repeated `agent_start` events, and a settled event where `ctx.isIdle()` is false do not send early or duplicate notifications. The following idle `agent_settled` sends exactly one.
4. With no opt-in recipient, no notification is sent for any lifecycle event. Child sessions also send none.
5. When `sendReply()` rejects, assert one Pi UI error, no success message, no retry, and no change to the worker's ability to settle. A later independent agent run may make one new attempt.
6. With a deferred first `sendReply()`, settle one run, start and settle a second run before the first send resolves, then resolve both sends. Assert that each settled run made exactly one attempt (two total), with neither send resolution changing the other generation.
