# Discord Gateway Bridge Plan

## Outcome and scope

Add a local bridge later. Each bridge instance connects one configured Discord channel to `supervisor` through Mailbox V2:

```
configured Discord channel
  -> configured bridge agent
  -> supervisor

supervisor
  -> configured bridge agent
  -> route's Discord channel
```

Every accepted Discord message becomes one durable Mailbox message for `supervisor`. Every normal Mailbox message from `supervisor` to that instance's configured agent ID requires a routed subject and posts its body to that route's Discord channel. Inbound filtering remains restricted to the configured channel.

Non-goals: no Discord app, bot, credential, package, connection, router protocol change, install, or production/shared-runtime live test in this task. No reply tokens, message references, channel maps, central registry, or instance coordination. This is not a general Discord client, slash-command integration, multi-user authorization layer, or web service.

## Instance-local configuration

One user-managed service instance has three local files or values:

- `MECHA_DISCORD_CONFIG`: mode-0600 JSON file with exactly `agentId`, `channelId`, and absolute `statePath`.
- `MECHA_DISCORD_TOKEN_FILE`: mode-0600 token file.
- `MECHA_ROUTER_URL`: existing loopback router base URL.

`agentId` must pass the existing Mailbox agent-ID validator. `channelId` is the only configured Discord channel. `statePath` is mandatory, absolute, and distinct from the instance config and token file. There is no shared default state path. This makes two concurrent instances ordinary independent local agents: each needs a unique agent ID, channel ID, token/config path, and SQLite path. The bridge validates the local values it can see. It does not invent a registry to coordinate services.

The router stays loopback-only and unchanged. The bridge owns the remote Discord connection. It is the only component that sees its bot token.

## Message contract

### Inbound

For each allowed `MESSAGE_CREATE`, persist a bounded inbound-outbox record before HTTP enqueue. The envelope is:

- `from`: configured bridge `agentId`
- `to`: `supervisor`
- `subject`: `discord.inbound.v1`
- `body`: fixed bridge-authored text that marks the author and content as untrusted

The body has no Discord IDs, reply token, route metadata, or reply instruction. The complete body is UTF-8 bounded to the configured Mailbox limit and marked when content is truncated. After router acceptance, delete the envelope body. Retain only the source-message ID for bounded duplicate suppression. This is not a reply route.

### Outbound

The bridge accepts only a normal Mailbox message with:

- `from`: exactly `supervisor`
- `to`: exactly this instance's configured `agentId`
- `subject`: exactly `pioneer-square.route.v1:pioneer-square:#<lowercase channel-name>:<17–20 decimal digit channel-id>`
- `body`: message text

Missing or unrelated route metadata is rejected with `missing_discord_route`. A subject with the route prefix but invalid syntax is rejected with `malformed_discord_route`. Invalid routes are NACKed once and never admitted. Persist a valid message before ACKing its router delivery, then post the body to the route's channel. Do not add a Discord message reference. A message for another bridge ID is rejected even if it came from `supervisor`.

Use `allowed_mentions` that disables user, role, and everyone mentions. Never treat Discord content as bridge instructions.

## Discord behavior

1. On startup, get the Gateway Bot URL and session-start limit, then connect to Gateway v10 with JSON encoding.
2. Heartbeat from the received Hello interval. Identify with only `GUILDS`, `GUILD_MESSAGES`, and `MESSAGE_CONTENT`. `MESSAGE_CONTENT` is privileged and must be enabled when the app/bot is created.
3. Accept `MESSAGE_CREATE` only from the configured channel, with a non-bot author and content. Ignore DMs, webhooks, bot messages, and every other channel.
4. Keep READY session state in memory, resume when possible, and respect the supplied session-start limit.
5. Serialize outbound REST sends. Honor 429 and transient failure retries. Permanent failures and ambiguous outcomes become concise Mailbox notices to `supervisor`, sent from the configured bridge ID. Never include token or content.
6. Split outbound text into Unicode-safe chunks within Discord's documented content limit. Every chunk goes to the route's channel.

## Durable state and retention

Use one mode-0600 SQLite file at the configured instance-local `statePath`. No content remains after delivery.

- `inbound_outbox`: complete sanitized envelope until router acceptance. Admit at most 100 rows. When full, drop matching events at that explicit boundary and send a rate-limited failure notice after recovery.
- `inbound_deliveries`: source-message IDs only for duplicate suppression. Expire after 7 days and keep at most 1,024 rows.
- `outbound`: mailbox message ID, routed Discord channel ID, pending body, retry schedule, and terminal Discord status. Admit at most 100 pending rows. New rows require a channel ID. Full state NACKs the router delivery as `injection_failed`. Clear the body after success or terminal failure. Retain terminal IDs and status for 7 days and at most 1,024 rows for delivery replay deduplication.
- Schema version 3 adds nullable `channel_id`. Migration preserves valid destinations. Pending rows without a destination are atomically terminal-failed and raise one pending `discord_route_missing` incident; terminal rows remain terminal.

Prune on startup and after every state change. The durable outbound record is the acceptance point before the router ACK. External delivery remains at-least-once after an ambiguous Discord HTTP outcome.

## Lifecycle and security

A later install adds one service per configured instance with restart-on-failure and graceful shutdown: retain pending rows, close Gateway and router client, then exit. The token is read once from its local secret file and never appears in command line, repository, state, test fixture, router envelope, or log. Discord author/content are untrusted input. Normalize and size-limit them before creating the Mailbox envelope. Logs contain only stable IDs, state transitions, and status codes.

This stays inside the existing trusted-local boundary. The loopback router is not exposed to Discord or a browser.

## Credential-free test plan

1. Test Gateway state transitions, heartbeat, reconnect, intent/event filtering, and self-message rejection.
2. Test complete inbound byte bounding, one-channel filtering, inbound recovery, duplicate suppression, overflow notices, and absence of Discord route data from supervisor-facing bodies.
3. Test durable outbound handoff before ACK, replay after restart, rejection of non-supervisor and wrong-recipient messages, route parsing and rejection, routed output, configured-channel inbound filtering, mention safety, bounded retention, and failure notices.
4. Test two configured instances: distinct agent IDs only accept their own deliveries and post only to their own configured channels.
5. Test config validation: required valid agent ID, exactly one channel ID, explicit safe state path, protected local config/token files, and no shared default state path.
6. Run focused suites plus full syntax/check commands. Review the plan and diff for requirements, failure handling, retention, and scope before commit.

## One human gate

Only after the credential-free implementation and review are complete: Wolfgang creates/configures the Discord app and bot (including the privileged intent, a least-privilege bot install into one test guild/channel, and a separate local config, state path, and token file for each instance), then authorizes one live end-to-end test. No other approval or credential request is needed before that gate.

## Primary sources

- Discord Gateway: https://docs.discord.com/developers/events/gateway
- Discord Gateway events, including `MESSAGE_CREATE`: https://docs.discord.com/developers/events/gateway-events
- Discord application configuration and privileged intents: https://docs.discord.com/developers/events/gateway#gateway-intents
- Discord Channels resource, Create Message: https://docs.discord.com/developers/resources/channel#create-message
- Discord rate limits: https://docs.discord.com/developers/topics/rate-limits
- Discord permissions: https://docs.discord.com/developers/topics/permissions
