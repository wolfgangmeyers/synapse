# Discord Gateway Bridge Plan

## Outcome and scope

Add one local, managed bridge later. Discord channel messages become durable Mailbox V2 messages for `supervisor`; a supervisor reply addressed to the bridge becomes a Discord reply in the originating channel or thread.

Non-goals: no Discord app, bot, credential, package, connection, router protocol change, Pi-extension change, install, or live test in this task. This is not a general Discord client, slash-command integration, multi-user authorization layer, or web service.

## Smallest shape

Run one Node process, `discord-gateway-bridge`, as a normal local Mailbox V2 agent. Reuse the installed `ws` package, `fetch`, and `node:sqlite`; do not add a Discord SDK.

```
Discord Gateway MESSAGE_CREATE
  -> discord-gateway-bridge
  -> POST localhost /v1/messages
  -> Mailbox V2 delivery to supervisor

supervisor
  -> Mailbox V2 message to discord-gateway-bridge
  -> Discord REST Create Message
  -> originating channel or thread
```

The router stays loopback-only and unchanged. The bridge owns the remote Discord connection. It is the only component that sees the bot token.

## Existing local contracts to use

- `POST /v1/messages` accepts a durable envelope and returns the created/current message (`harness/router.js`, `enqueueHttpMessage`; `harness/protocol.js`, `createEnvelope`). The repository applies its configured admission body limit (4 KiB by default). The bridge must bound the complete constructed body to that limit, not just the Discord content.
- The bridge registers as `discord-gateway-bridge` on the router WebSocket, renews its lease, and receives normal message deliveries. `createMailboxClient` already implements that protocol and reconnect behavior in `pi-extensions/mecha-messaging.js`.
- The bridge sends `injection_requested` when it has durably accepted an outbound supervisor message, then ACKs only after that durable handoff. It sends no receipt for an inbound Discord event; the HTTP enqueue result is the inbound acceptance boundary.
- The envelope schema permits `subject` (256 characters) and rejects unknown fields (`protocol/v1/schema.json`). Keep Discord correlation in a versioned subject, not a router schema extension.

Implementation should move the transport-only `createMailboxClient` into a small harness module used by both the Pi extension and bridge. Do not copy the client or modify the mailbox protocol.

## Message contract and correlation

### Inbound

For each allowed `MESSAGE_CREATE`, create one Mailbox envelope:

- `from`: `discord-gateway-bridge`
- `to`: `supervisor`
- `subject`: `discord.inbound.v1`
- `body`: fixed bridge-authored header with an opaque reply token, Discord IDs, and explicitly delimited untrusted author/content fields

The reply token is the generated Mailbox message ID. Before any HTTP enqueue, the bridge persists a bounded inbound-outbox row containing the complete already-bounded envelope plus `{channel_id, source_message_id, guild_id?}`. It reuses the same envelope ID for a recovered duplicate, retries the HTTP enqueue until accepted, then deletes the envelope body and retains only the route mapping. The supervisor does not need the bot token or a Discord snowflake to reply.

### Outbound

A supervisor reply is a normal Mailbox message:

- `from`: exactly `supervisor`
- `to`: `discord-gateway-bridge`
- `subject`: `discord.reply.v1/<reply-token>`
- `body`: reply text

The bridge rejects every other sender or subject. It resolves the token locally, sends `POST /channels/{channel_id}/messages`, and includes a `message_reference` for `source_message_id`; sending to the incoming channel ID also preserves thread replies because Discord threads have their own channel IDs.

Use a dedicated `allowed_mentions` policy that cannot turn Discord content into unsolicited role, user, or everyone pings. Never treat inbound content as bridge instructions.

## Discord behavior

1. On startup, get the Gateway Bot URL and session-start limit, then connect to Gateway v10 with JSON encoding.
2. Heartbeat from the received Hello interval. Identify with only `GUILDS`, `GUILD_MESSAGES`, and `MESSAGE_CONTENT`. This is the smallest intent set for configured guild-channel messages. `MESSAGE_CONTENT` is privileged and must be enabled when the app/bot is created.
3. Accept `MESSAGE_CREATE` only when it is from an allowlisted guild/channel, has a non-bot author, and has message content. Ignore DMs, webhooks, bot messages, and all unconfigured channels.
4. Keep the READY session ID, sequence, and resume URL in memory. Resume after a resumable disconnect; honor Gateway reconnect/invalid-session instructions. After a non-resumable loss or process restart, Identify again only within the supplied session-start limit and max-concurrency rules.
5. Serialize outbound REST sends. Honor Discord response headers and 429 `retry_after`, including global rate limits. Use bounded exponential retries for transient network/5xx failures. Permanent 4xx failures become a concise Mailbox failure notice to `supervisor` with IDs/status, never token or content.
6. Split an accepted Mailbox reply into Unicode-safe Discord content chunks no larger than Discord's documented message-content limit. The first chunk carries the source-message reference. The complete bridge-authored inbound body (header, metadata, and content) is UTF-8 bounded to the configured Mailbox limit and marked as truncated when necessary.

## Durable bridge state and retention

Use one mode-0600 local SQLite file owned by the bridge, outside the repository. No content is retained after delivery.

- `inbound_outbox`: complete already-sanitized Mailbox envelope plus Discord route, retained only until router acceptance. Admit at most 100 rows; retry while the router is unavailable or rate-limited. When full, stop accepting new matching Gateway events, record the condition, and send one rate-limited failure notice after the router recovers. Discord has no per-event application ACK, so this is an explicit bounded-overflow loss boundary, not a silent drop.
- `inbound_routes`: reply token and Discord IDs only; expire after 7 days; keep at most 1,024 newest rows.
- `outbound`: router message ID, route, pending reply text, retry schedule, and eventual Discord message ID. Admit at most 100 pending rows. If full, NACK the router delivery as `injection_failed` (so its normal retry/dead-letter path applies) and emit one rate-limited supervisor notice; never ACK an unrecorded reply. Remove reply text immediately after success or terminal failure; retain only IDs/status for 7 days and 1,024 rows to deduplicate delivery replays.
- Prune on startup and after every state change. Test expiry, capacity, overflow notice, and recovery.

The durable outbound record is the bridge's acceptance point: persist it transactionally before its router ACK. This gives the bridge a recoverable rate-limit queue without changing router delivery semantics. It is still at-least-once across an ambiguous Discord HTTP response; do not promise exactly-once external delivery. Preserve the response ID when known and surface ambiguous outcomes rather than silently claiming success.

## Lifecycle, configuration, and security

A later install adds one user-managed service with restart-on-failure and graceful shutdown: stop accepting new Gateway work, retain pending outbound rows, close the Gateway, and release the router client. Its configuration is a local allowlist and token-secret reference. Validate startup configuration before connecting.

The Discord bot token is a local secret: read once from the future local secret mechanism, never command line, repository, state, test fixture, router envelope, or log. Discord event text and author metadata are untrusted input. Normalize/size-limit it before the Mailbox envelope; logs contain only stable IDs, state transitions, and status codes.

This stays inside the existing trusted-local boundary. The loopback router is not exposed to Discord or a browser.

## Credential-free test plan

1. Unit-test Gateway state transitions with recorded fixtures: Hello/heartbeat, READY, RESUMED, reconnect, invalid session, sequence handling, session-start backoff, intent/event filtering, and self-message rejection.
2. Test complete inbound-envelope byte bounding, allowlist checks, inbound-outbox recovery after router timeout/429/restart, idempotent duplicate Gateway events, bounded overflow behavior, and no user-controlled `from`, `to`, subject, or routing data.
3. Test the bridge against a local fake Mailbox router: registration/renewal, durable outbound handoff before ACK, replay after restart, rejection of non-supervisor outbound senders, missing/expired reply tokens, full-outbox NACK/dead-letter behavior, bounded retention, and one failure notice per incident.
4. Test the fake Discord REST adapter: reply/thread destination, chunking, allowed-mentions policy, 429/global-rate-limit delay, transient retry, permanent failure notice, and ambiguous response disposition.
5. Run the focused suites plus syntax/check commands. Review this plan and the eventual diff for requirements, security, failure handling, data retention, and scope before any commit.

## One human gate

Only after the credential-free implementation and review are complete: Wolfgang creates/configures the Discord app and bot (including the privileged intent, a least-privilege bot install into one test guild/channel, and local token placement), then authorizes one live end-to-end test. No other approval or credential request is needed before that gate.

## Primary sources

- Discord Gateway: https://docs.discord.com/developers/events/gateway
- Discord Gateway events, including `MESSAGE_CREATE`: https://docs.discord.com/developers/events/gateway-events
- Discord application configuration and privileged intents: https://docs.discord.com/developers/events/gateway#gateway-intents
- Discord Channels resource, Create Message: https://docs.discord.com/developers/resources/channel#create-message
- Discord rate limits: https://docs.discord.com/developers/topics/rate-limits
- Discord permissions: https://docs.discord.com/developers/topics/permissions
