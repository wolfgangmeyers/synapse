# Mailbox v2: Pi Agent Messaging Harness

Status: proposed technical specification  
Target repository: `mecha-developer`  
Validated against: `@earendil-works/pi-coding-agent` 0.80.6

## 1. Purpose

Preserve the **mailbox abstraction**—addressed, durable, asynchronous messages—while replacing its file, hook, polling, and cron implementation with one local mailbox service. Pi clients use WebSocket delivery.

Mailbox v2 is the new system, not a compatibility feature layered beside the old mailbox. Posting always writes the message durably first. If the destination client is connected, the service offers it immediately. If not, it holds the message until that agent connects again.

An incoming message must:

- be addressed to a stable agent identity;
- appear visibly in Pi's conversation without touching the bottom editor;
- enter model context;
- trigger work when the agent is idle;
- be queued at the next agent-call boundary when the agent is busy;
- survive temporary agent and router outages;
- have explicit delivery state rather than treating a file read as delivery.

Agents continue to run in tmux. Tmux remains a process/container boundary and operator interface, but it is not a Mailbox v2 transport.

This specification also defines mailbox retirement and reminders v2. It does not propose replacing terminal-control operations until Pi-native equivalents are proven.

## 2. Scope

### In scope

- One per-user local router on macOS.
- A Pi extension that subscribes to messages for one agent identity.
- One command-line sender for Pi mailbox messages.
- Durable local messages, acknowledgements, retries, and reconnects.
- Migration of current mailbox producers.
- Reminders v2 using the router rather than mailbox files.
- Installation, startup, diagnostics, tests, rollout, and retirement gates.

### Out of scope

- A distributed broker, multi-host federation, cloud service, or internet listener.
- Replacing tmux as the agent process host.
- Email transport redesign.
- Automatically claiming that an agent acted on a message. Transport acknowledgement proves correlated Pi message emission, not semantic completion or guaranteed disk flush.
- Replacing transcript-sensitive control operations before equivalent behavior is verified.
- Implementing any component in this specification.

## 3. Primary-source findings

### 3.1 Verified Pi APIs

The following APIs exist in the installed Pi version. They are not proposed harness abstractions.

#### Visible, persistent, model-visible message

```ts
pi.sendMessage(
  {
    customType: "mecha-agent-message",
    content: "...",
    display: true,
  },
  options,
);
```

`pi.sendMessage()` submits a `CustomMessage`. Once Pi consumes it, `display: true` renders it in the conversation and the message participates in LLM context. It does not write into the bottom editor. A custom renderer may be registered with `pi.registerMessageRenderer(customType, renderer)`.

Persistence needs qualification: while Pi is busy, Mailbox v2 **immediately renders the compact, truncated queue preview in Pi's status line**. The full `CustomMessage` waits in Pi's in-memory steering/follow-up queue and is not yet added to the conversation, model context, or session history until Pi drains that queue. Disk persistence of the eventual Pi conversation entry also depends on Pi session persistence being enabled; `--no-session` cannot provide it.

Sources:

- Pi `docs/extensions.md`, lines 1378-1399.
- Pi `docs/session-format.md`, lines 260-271.
- Pi `dist/modes/interactive/interactive-mode.js`, lines 2579-2582.

`pi.appendEntry()` is not a substitute: it creates durable TUI content that does not enter LLM context. `ctx.ui.notify()`, `setStatus()`, and `setWidget()` are also presentation APIs, not conversation-message APIs.

#### Idle delivery and turn triggering

When `ctx.isIdle()` is true:

```ts
pi.sendMessage(message, { triggerTurn: true });
```

This adds the custom message and immediately triggers an LLM response. `pi.sendUserMessage()` also triggers a turn, but it creates a user-role message. The harness should use `sendMessage()` so transport-originated messages remain distinguishable from human prompts.

Sources:

- Pi `docs/extensions.md`, lines 1006-1008 and 1395-1425.

#### Busy delivery

When the agent is streaming:

```ts
pi.sendMessage(message, { deliverAs: "steer" });
```

`steer` delivers after the current assistant turn and its tool calls, before the next LLM call. Pi also supports `followUp` and `nextTurn`, but Mailbox v2 does not expose those choices. Every mailbox message uses `steer`.

While a busy message is waiting in Pi's in-memory steer/follow-up queue, Mailbox v2 renders its compact preview immediately through Pi's TUI status API. Only the full conversation message waits until Pi emits it at the next agent-call boundary.

Sources:

- Pi `docs/extensions.md`, lines 1395-1425.
- Pi `dist/core/extensions/types.d.ts`, lines 895-906.
- Pi `dist/core/agent-session.js`, lines 1047-1085.

Pi also exposes a `context` event and `before_agent_start`, but neither is the right transport primitive. `context` transformations are not persisted; `before_agent_start` is not an asynchronous mid-stream hook.

Sources:

- Pi `docs/extensions.md`, lines 513-548 and 640-649.

#### Lifecycle

Long-lived sockets, watchers, timers, and processes must not start in the extension factory. Connect during `session_start` or lazily, and clean up idempotently during `session_shutdown`.

`session_shutdown.reason` is one of `quit`, `reload`, `new`, `resume`, or `fork`.

Sources:

- Pi `docs/extensions.md`, lines 180-223 and 499-508.
- Pi `dist/core/extensions/types.d.ts`, lines 453-459.
- Pi `examples/extensions/mac-system-theme.ts`, lines 28-45.

#### Settled state

`ctx.isIdle()` distinguishes idle from active operation. `agent_settled` means no automatic continuation remains and is useful for diagnostics or deferred work. It does not identify which routed message was semantically handled.

Source: Pi `docs/extensions.md`, lines 551-563 and 1006-1008.

### 3.2 Pi constraints

- There is no documented API that mutates an already-open provider stream. Busy delivery takes effect at the next agent-call boundary.
- `pi.sendMessage()` is typed as `void`. Returning normally proves only that the extension invoked the wrapper; it does not prove queue insertion, rendering, session append, model receipt, or completion.
- Busy messages remain volatile inside Pi until its steering/follow-up queue is drained. A quit, reload, or crash in that interval can lose the Pi-side copy.
- Extension errors are routed through Pi's extension error handling; there is no returned delivery receipt. The harness must keep the router lease until it observes a correlated Pi event, as described in section 9.
- Pi has no first-class API for supervising a background server. Node built-ins or finite process execution must be used.
- The SDK's in-process `eventBus` is not an external transport.
- RPC has `prompt`, `steer`, and `follow_up`, but no equivalent of extension `sendMessage()` for a visible custom message.

These constraints are acceptable. “Drop into the active context” means queue at Pi's next model-call boundary, not modify tokens already being streamed.

### 3.3 Relevant Pi sources read

Documentation root:

`/Users/wolfgang/.nvm/versions/node/v26.2.0/lib/node_modules/@earendil-works/pi-coding-agent`

Relevant primary sources include:

- `README.md`
- `docs/extensions.md`
- `docs/sdk.md`
- `docs/tui.md`
- `docs/session-format.md`
- `docs/rpc.md`
- `examples/extensions/file-trigger.ts`
- `examples/extensions/mac-system-theme.ts`
- `examples/extensions/subagent/index.ts`
- `dist/core/extensions/types.d.ts`
- `dist/core/agent-session.js`

The `file-trigger.ts` example demonstrates out-of-band `sendMessage(..., { triggerTurn: true })`, but it does not clean up its watcher and should not be copied as a lifecycle template.

## 4. Decision

Build one loopback-only **Mailbox v2 service** (called the router in implementation-oriented sections) with:

- HTTP POST for one-shot producers;
- WebSocket subscriptions for live Pi extensions;
- SQLite for messages, delivery state, agent leases, and reminders;
- a per-user `launchd` service on macOS;
- one Pi extension per Pi process/session;
- one migration-safe sender CLI named `mailbox_send.py`, with `mailbox_send.py` becoming the stable v2 facade after cutover.

This is a local durable mailbox with live subscriptions, not a general pub/sub platform. The mailbox is the product abstraction; WebSocket is only its connected-client delivery mechanism.

## 5. Alternatives considered

### 5.1 WebSocket-only server

Every producer would open a WebSocket to send one message.

Advantages: one protocol.  
Disadvantages: unnecessary connection lifecycle in cron jobs and shell scripts; harder CLI error handling.

Disposition: reject. Use HTTP for commands and WebSocket for subscriptions.

### 5.2 Unix-domain socket with JSON lines

Advantages: filesystem permissions provide a strong local boundary; no TCP port.  
Disadvantages: requires custom reconnect/framing/client code; Pi and Python clients cannot use the same standard WebSocket tooling; interactive diagnostics are worse.

Disposition: defer. Loopback HTTP is simpler for the local-only v1. A Unix socket can replace the listener later without changing the envelope if stronger local isolation becomes necessary.

### 5.3 Reuse mailbox files and add a Pi watcher

Advantages: smallest immediate change.  
Disadvantages: retains racing consumers, inferred delivery, unbounded files, polling, and split transports. It does not solve reminders reliability.

Disposition: reject. Producers and consumers migrate directly to Mailbox v2; there is no compatibility delivery transport.

### 5.4 Optional non-Pi sidecar adapter

A future adapter may support a legacy interactive harness without changing the router. It would run as a separate background process, register the legacy agent identity over the same WebSocket protocol, receive messages, and inject them through that harness's supported interface. For a terminal-only harness, the adapter could target one configured tmux pane, use bracketed paste or `load-buffer`/`paste-buffer`, and submit Enter with tested timing.

This is feasible, but acknowledgement is necessarily weaker when the harness exposes no event confirming that the message reached conversation/model context. Such an adapter may acknowledge only the fact it can actually observe—for example, successful terminal submission—and must label that limitation. It must never be built into the router, add tmux fields to the core envelope, or cause the Pi path to branch on recipient type.

Disposition: nice to have, out of scope for v1. Build it only when a concrete legacy integration is needed, as an independently installed client of the clean Mailbox v2 protocol.

### 5.5 Server lifecycle: `nohup`, detached subprocess, or `launchd`

- `nohup`: easy, but has weak ownership, duplicate-process races, stale PID handling, and no restart policy.
- detached Node/Python subprocess: slightly better control, but the extension becomes an accidental supervisor and must solve the same races.
- `launchd`: native per-user lifecycle, restart, logs, one service identity, and explicit health/start commands.

Disposition: use `launchd`. The extension does not daemonize the router itself. On connection refusal it runs a finite helper that calls non-destructive `launchctl kickstart gui/$UID/<label>`, then retries with bounded backoff. `kickstart -k` is reserved for an explicit operator repair command after a failed health check; disconnected clients must not kill a router that is already starting. Installation must bootstrap the LaunchAgent once. If the service is not installed, the extension shows an actionable error rather than silently spawning an unmanaged daemon.

## 6. Architecture

```text
mailbox_send.py ─────HTTP POST──┐
reminders v2 scheduler ──SQLite─────┤
watchdog/email/other producers ─────┤
                                   ▼
                         local mecha router
                     HTTP + WebSocket + SQLite
                         │              │
                         │ WebSocket
                         ▼
                  Pi extension
                         │
                 pi.sendMessage()
                         │
                 visible Pi context
```

### 6.1 Proposed components and paths

Names are proposed and may change during implementation review.

```text
mecha-developer/
  harness/
    router.py                 # Mailbox v2 HTTP/WebSocket service and scheduling loop
    db.py                     # SQLite schema and transactions
    protocol.py               # shared validation and state constants
    launchd.py                # finite install/status/kickstart helpers
  pi-extensions/
    mecha-messaging.ts        # Pi extension
  tools/
    mailbox_send.py        # migration-safe Mailbox v2 sender CLI
    agent_router.py           # install/start/stop/status/doctor CLI
  .claude/skills/reminders-v2/
    SKILL.md
    tools/reminders.py        # set/list/delete/delete-all compatibility CLI
  tests/
    harness/
```

`worker_send.py` is not part of the target system. The worktree implementation migrates all supported workflows to Pi-native messaging and removes it at cutover.

## 7. Identity and routing

### 7.1 Agent identity

`agent_id` is initially the tmux window name. This preserves the useful existing contract and makes the recipient visible to operators.

Identity is the current tmux pane's window name, resolved with `TMUX_PANE` and
`tmux display-message -p -t <pane> #{window_name}`. The name must satisfy the
agent-ID grammar; missing tmux context, lookup failure, or an invalid name fails
closed.

No environment override or fallback to literal `supervisor`, `self`, or the
terminal title is allowed. The current `--window self` misrouting demonstrates
why implicit fallback is unsafe.

### 7.2 Session identity and leases

Each extension connection generates a random `instance_id`. It registers:

- `agent_id`;
- `instance_id`;
- process ID;
- tmux pane/window metadata when available;
- client type and protocol version;
- connection timestamp.

The router grants a renewable lease for `agent_id`. A second live claim is rejected unless it explicitly replaces a stale lease. Silent fan-out to duplicate identities is forbidden.

The router records `first_seen_at`, `last_connected_at`, and `disconnected_at` for each identity. Connection registration updates `last_connected_at`; routine heartbeats renew only the live lease. A periodic cleanup transaction removes an agent's queued messages, delivery attempts, reminders, leases, and agent record once it has been disconnected for one hour. Resources targeting an identity that has never connected are removed one hour after the first such resource was created. Connected identities are never removed.

### 7.3 Local trust boundary

Authentication is intentionally out of scope for v1. This is a local development harness running entirely as one macOS user.

The router binds to its configured listen address, rejects non-JSON requests, and accepts arbitrary `Host` header authorities after binding. Its HTTP API accepts browser requests with arbitrary `Origin` values, including preflight requests, and returns the existing wildcard, no-credential CORS headers. The `/v1/agents` WebSocket accepts arbitrary browser `Origin` values as well. These are transport decisions, not authentication: the local router trusts processes running as the same user to post messages and claim sender identities. Database and state files remain user-only.

The separate browser UI retains its host and origin boundary. It serves requests only for loopback or its configured allowed host, accepts `/ui/events` only for the matching same-origin HTTP(S) origin, and keeps the existing REST relay behavior. Browser UI policy must not be inferred from the router API policy.

### 7.4 Existing Web UI and mobile-app compatibility

Phase 3 in `harness_phases.md` preserves the existing v1 router contract for Web UI and mobile consumers. Legacy Web UI and mobile screens continue to use their current REST and HTTP routes, envelopes, responses, relay behavior, `/v1/agents` registration, and frame behavior without source, configuration, or behavior changes.

The expanded worker-activity view is the sole Phase 3 contract exception. It may receive additive optional data needed for that view, including narrowly necessary managed status, history, or live updates. An addition must preserve the route inventory in `router/contract/manifest.json`, the HTTP outcomes in `router/contract/http-outcomes.json`, and the frame/envelope definitions in `router/contract/frame-cases.json` and `protocol/v1/schema.json`: existing routes and fields retain their current semantics and requirements; legacy consumers can ignore any new optional data. Do not make broad client changes or use this exception for unrelated endpoints, responses, frames, registration, or screen behavior.

The local unauthenticated v1 decision remains in force. Phase 3 does not add browser or mobile authentication, pairing, credentials, token checks, or origin-based authorization. The Web UI keeps its separate same-origin policy. The router continues to accept the documented wildcard CORS and arbitrary-Origin WebSocket behavior for the local same-user trust boundary.

The `from` field is a claimed local agent/service name or the normalized sender email address. It is diagnostic routing context, not authenticated identity.

Inbound email keeps the existing allow-list behavior: accept `wolfgangmeyers@gmail.com` and sender addresses under `agents.meyers.life`; reject and log other senders before creating a mailbox message. Accepted email routes to the configured supervisor mailbox, initially `supervisor`. If that supervisor identity is unavailable, the message remains queued rather than being redirected or dropped.

If the router later listens beyond loopback or crosses user/machine boundaries, authentication becomes a prerequisite for that expansion—not speculative v1 machinery.

## 8. Protocol

### 8.1 Message envelope

The HTTP endpoint (`/v1/messages`) versions the contract; the payload does not repeat it.

```json
{
  "id": "019f...",
  "from": "mecha-harness",
  "to": "research-1",
  "subject": "Optional short subject",
  "body": "Message body or artifact pointer",
  "created_at": "2026-07-10T02:00:00.000Z"
}
```

That is the complete v1 envelope. `subject` is optional; every other field is required. `id` is generated by the producer when practical and otherwise by the server. It is the idempotency key. Time is timezone-aware UTC.

Every message behaves the same way: deliver as `steer`; when Pi is idle, trigger a turn. There is no delivery option, wake policy, message kind, origin object, metadata bag, or task alias in v1.

Accepted inbound email uses the same envelope:

```json
{
  "id": "imap-<uidvalidity>-<uid>",
  "from": "wolfgangmeyers@gmail.com",
  "to": "supervisor",
  "subject": "Email subject",
  "body": "Normalized email body",
  "created_at": "2026-07-10T02:00:00.000Z"
}
```

The email poller applies the existing sender allow list before enqueueing. The email address itself identifies the source; a second `origin` structure would be redundant.

### 8.2 Size and validation

- Default body limit: 4 KiB.
- Absolute body limit: 64 KiB for explicitly configured local producers.
- Batch and per-recipient queued-byte limits are enforced.
- Large reports remain files; messages carry an absolute path and summary.
- Unknown fields are rejected so accidental schema growth is visible.

The current 1,000-character mailbox guard is too small for all producers and does not bound batches. V1 uses explicit per-message and per-recipient limits instead.

### 8.3 HTTP endpoints

Proposed minimum API:

- `POST /v1/messages` — validate and enqueue idempotently.
- `GET /v1/messages/{id}` — inspect state.
- `POST /v1/messages/{id}/ack` — normally used over WebSocket and retained for diagnostics.
- `GET /v1/health` — process, database, schema, and scheduler health.
- `GET /v1/agents` — loopback-only diagnostic lease list.
- CRUD endpoints under `/v1/reminders`.

A successful `POST` means durable mailbox acceptance, not client delivery. The database transaction commits before any WebSocket offer is attempted.

Delivery rule:

1. Persist the addressed message as `queued` with a fixed `expires_at = created_at + 1 hour`.
2. If the recipient currently owns a connected lease, offer the oldest queued message immediately over its WebSocket.
3. If the recipient is disconnected or has never registered, hold the message until that identity connects or the message expires.
4. On successful registration, immediately replay unexpired queued messages in durable enqueue order.
5. When an unclaimed message reaches `expires_at`, atomically delete it and its delivery-attempt rows. Expiration is not a dead letter and does not retain a tombstone.
6. Once a client has claimed a message lease, it has been picked up; normal acknowledgement/retry semantics apply rather than expiring it mid-injection.
7. Remove no message merely because a socket disconnected or an offer was attempted; correlated acknowledgement or unclaimed expiry deletes it.

### 8.4 WebSocket frames

Client to router:

- `register`
- `lease_renew`
- `ack`
- `nack`
- `ping`

Router to client:

- `registered`
- `message`
- `lease_rejected`
- `error`
- `pong`

Every frame includes `version` and a frame ID. Message frames include the complete envelope and a delivery-attempt number.

## 9. Delivery semantics

### 9.1 State machine

```text
queued -> leased -> injection_requested -> acknowledged -> deleted
             |                |
             +------timeout---+----timeout----+
             |                                |
             v                                v
           queued                         dead_letter
```

Recommended persisted names:

- `queued`: durable, no client owns the attempt.
- `leased`: sent to one registered instance until a deadline.
- `injection_requested`: extension validated the frame and invoked `pi.sendMessage()`, but Pi has not yet emitted the correlated custom message. This state remains leased and retryable.
- `acknowledged`: after correlating Pi's `message_end`, the extension observed the matching custom-message ID on the persisted current session branch and in its JSONL session file. This is a transactional transition used only to complete delivery; the server immediately deletes the message and its delivery-attempt rows in the same transaction.
- `dead_letter`: retry limit exceeded. This is the only terminal message state retained for operator action.

If implementation research cannot correlate a routed custom message reliably through `message_end`, v1 must not claim durable Pi delivery. It must either add a verified correlation mechanism using documented events/session entries or keep the message leased and expose the limitation as a release blocker.

### 9.2 Pi extension behavior

On a valid `message` frame:

1. Reject unsupported versions or invalid payloads with `nack`.
2. Deduplicate by message ID using router state and Pi session history, not an in-memory cache alone. On connection/replay, inspect the current persistent session branch for a prior `CustomMessage` carrying the router ID before reinjecting. The implementation must verify the exact documented session-entry accessor before this guarantee is enabled.
3. Render the sender and subject in a stable `CustomMessage` shape.
4. Snapshot `ctx.isIdle()` only for metrics; do not rely on it for correctness.
5. Always call `pi.sendMessage(customMessage, { deliverAs: "steer", triggerTurn: true })`. Pi ignores `triggerTurn` while streaming and uses `deliverAs` there; if the agent becomes idle between the state check and call, `triggerTurn` prevents the message from being stranded without a turn.
6. Record/send `injection_requested` after invoking the wrapper, but do not acknowledge delivery yet.
9. While one or more messages are awaiting Pi emission, update a single compact status line. Proposed format:

   ```text
   mailbox: 2 queued · alice: Deploy finished; check the…
   ```

   Show the oldest queued message's sender plus a whitespace-normalized preview. Truncate to a conservative configurable cap (default 80 display columns) with an ellipsis and rely on Pi's status rendering for narrower terminals; never wrap, expand the transcript, or show more than one body preview. If the subject or body is empty, show only the available sender, subject, and count. The full message remains durable in the mailbox and appears normally once emitted.
10. Listen for Pi's `message_end` event and correlate the emitted `CustomMessage` by the embedded router message ID. Do **not** acknowledge from that handler: in the validated Pi source, extension `message_end` handlers run before `SessionManager.appendCustomMessageEntry()` persists the entry. Defer one macrotask, then verify the ID on the documented current session branch (`getBranch()`), verify `isPersisted()` and `getSessionFile()`, and read that documented JSONL session file for the same `custom_message`/`details.message_id` entry. Only this observable durable session checkpoint may send `ack`.

Pi has an additional source-validated constraint: a new persisted session does not create or flush its JSONL file until it contains an assistant message. The first custom mailbox message can therefore reach `message_end` while it exists only in the in-memory branch. When the branch contains the ID but the file checkpoint is not yet observable, the extension retains the delivery and keeps at most one checkpoint timer per pending message (currently every 250 ms) until the file contains the entry or the session shuts down. It neither acknowledges nor reinjects during that interval. On acknowledgement, the router deletes the persisted message and attempts rather than retaining mailbox history, and the extension restores its connected status indicator; `session_shutdown` clears that indicator.

A redelivery first checks the same durable current-branch history. If found, it must not call `sendMessage()` again; it sends `injection_requested` for the current delivery attempt and then `ack` after the durable-history checkpoint. If an earlier injection is still pending in memory but the session file has not flushed yet, the extension updates that pending delivery to the new attempt and records `injection_requested` without a second Pi injection; the checkpoint acknowledges the latest attempt after persistence. If history exists but there is no pending local injection and the persistence check cannot be made, it must neither reinject nor acknowledge. Expected initial-file absence remains a status-only condition; an unreadable or malformed existing session file must also raise one visible diagnostic for that message. The router lease remains retryable. This avoids both an unsafe acknowledgement and a duplicate visible message during a session-file failure.

Because `sendMessage()` returns `void`, wrapper return is not an acceptance signal. A correlated `message_end` proves that Pi consumed and emitted the message; the branch-plus-file check proves Pi session persistence under Pi's documented session-storage contract. It does not prove semantic handling, provider receipt, or durability against an operating-system power loss beyond Pi's own session guarantees. The implementation and regression tests must retain this ordering and initial-session-flush check against the installed Pi version before shipping.

Exactly-once visible replay cannot be guaranteed under Pi `--no-session`. In that mode the extension must not acknowledge an unverified delivery; the router keeps it retryable and diagnostic mode may visibly redeliver after a crash. Production harness mode therefore requires Pi session persistence. If event correlation or durable history lookup cannot be proven with documented APIs, the corresponding durability acceptance criteria remain blocked.

The custom message content should include a compact, model-readable header, for example:

```text
[Agent message from mecha-harness]
Subject: Check router behavior
Message-ID: 019f...

<body>
```

The renderer may make this visually distinct, but the model-visible text must remain understandable without the renderer.

### 9.3 Ordering

Messages are ordered per recipient by durable enqueue sequence, not client timestamps. Only one leased message per recipient is required initially. This avoids reordering during reconnects and keeps backpressure simple.

No global ordering is promised.

### 9.4 Retry and reconnect

- The extension reconnects with exponential backoff plus jitter, capped at 30 seconds.
- On refusal, it requests `launchd` kickstart once per backoff window.
- A lease expires if no acknowledgement arrives.
- Expired messages return to `queued`.
- Duplicate delivery is possible after a crash. Message IDs make delivery effectively once for a healthy extension and at-least-once across failures.
- The extension must not acknowledge on wrapper return or directly from `message_end`; it acknowledges only after the correlated-message durable session checkpoint described in section 9.2.

### 9.5 Backpressure

Initial limits should be configuration values with conservative defaults:

- maximum queued messages per recipient;
- maximum queued bytes per recipient;
- maximum delivery attempts;
- maximum message age;
- maximum sender request rate.

When a recipient count/byte limit is exceeded:

- every new message is rejected with HTTP `429` and a structured `queue_full` error; existing messages retain order;
- queued messages are never evicted automatically;
- an oversized individual request is rejected with HTTP `413`;
- rate limits return HTTP `429` with `Retry-After`.

The router never evicts or silently drops ordinary agent messages or reminders.

## 10. Router lifecycle on macOS

### 10.1 Installation

`agent_router.py install`:

1. creates the state directory with user-only permissions;
2. initializes user-only state and database files;
3. installs a versioned per-user LaunchAgent plist;
4. bootstraps it with `launchctl bootstrap gui/$UID <plist>`;
5. waits for `/v1/health`;
6. prints paths for logs, database, and diagnostics.

### 10.2 Extension startup

During `session_start`:

1. resolve `agent_id` or fail visibly;
2. connect to the loopback WebSocket;
3. on connection refusal, execute the finite kickstart helper;
4. retry with bounded backoff;
5. register and obtain the identity lease.

The extension must not block Pi startup indefinitely. After a short initial budget it continues reconnecting in the background and displays a status indicator.

### 10.3 Shutdown

During `session_shutdown`, idempotently:

- stop reconnect and durable-checkpoint timers;
- release the lease when possible;
- close the WebSocket;
- remove status UI;
- retain no process-global listener.

The router remains running as a per-user service.

## 11. New sender CLI

Migration name: `mailbox_send.py`.

Minimum interface:

```text
mailbox_send.py --to <agent-id> --body <text> [--subject <text>]
mailbox_send.py --to <agent-id> --body-file <path> [--subject <text>]
```

Behavior:

- infer the sender from explicit harness environment using one shared precedence rule;
- reject missing or ambiguous sender identity;
- generate an idempotency ID;
- POST to the router;
- print the message ID and durable state;
- return non-zero for validation, startup, or enqueue failures;
- optionally request router kickstart before one bounded retry.

Do not infer email transport from an `@` in the recipient. `mailbox_send.py --to` accepts agent IDs only and rejects email addresses with an error that names the explicit email command. Outbound email uses a separate `mailbox_email.py --to <address> ...` command; inbound Purelymail remains a separate adapter that enqueues accepted mail to `supervisor`.

This intentionally changes the current `mailbox_send.py` behavior, which switches to SMTP when `--to` contains `@`. Migration is incomplete until every caller, skill, prompt, and test that relies on that behavior is updated. The repository audit must include at least:

- the mailbox skill's reply instructions and generated email reply hints;
- supervisor and worker skill examples;
- Purelymail polling and reply handling;
- mailbox cron/hook output that currently tells an agent to reply with `mailbox_send.py --to <sender-email>`;
- watchdog, PR watch, reminders, standing orders, KB reminders, memory monitor, remote delegation, and any other direct `mailbox_send.py` caller;
- installer/package copies of those skills and tools;
- tests and fixtures covering email-address recipients.

Agent-message callers migrate to the Mailbox v2 facade. Actual outbound-email callers migrate to `mailbox_email.py`. Ambiguous callers must be classified explicitly rather than preserving `@` inference behind another wrapper.

### 11.1 Full Pi migration

The target system is Pi-only. `worker_send.py` and its mixed mailbox/terminal-control contract are removed rather than retained alongside Mailbox v2. Workflows that currently send authorization phrases, slash commands, cancellation keys, menu choices, `/clear`, or `/exit` must either use a documented Pi API with tested equivalent semantics or be redesigned so terminal injection is unnecessary. A workflow without a safe Pi-native replacement blocks its own migration; it does not justify adding terminal control to the mailbox core.

After producer migration, `mailbox_send.py` may become the stable facade over v2 and the temporary suffix can disappear. It sends mailbox messages only.

## 12. Mailbox v2 replacement and mailbox retirement

### 12.1 Existing contracts retained

Retain conceptually:

- tmux window name as initial `agent_id`;
- `id`, `from`, `to`, optional `subject`, `body`, and UTC timestamp;
- stable IDs for imported email;
- explicit system-sender classification;
- a simple string `from` value that can be a local identity or an allowed email address;
- file pointers for large artifacts;
- recipient isolation and deterministic per-recipient order.

Mailbox v2 preserves the abstraction but replaces these legacy implementation details:

- one permanent JSON file per message;
- the ambiguous `read` boolean;
- hook/tmux delivery races;
- polling as the primary live-delivery mechanism;
- inconsistent and ambiguous `--from` handling;
- inconsistent identity precedence and literal `supervisor` assumptions.

### 12.2 WebSocket-only consumer migration

Mailbox v2 has one delivery path: the addressed Pi extension's WebSocket connection. Disconnected and not-yet-registered recipients retain queued messages until they connect. The router never types messages into tmux.

During migration, existing `mailbox_send.py` may become a compatibility facade over `mailbox_send.py`; after cutover it becomes the normal v2 CLI entry point. This preserves producer command compatibility without preserving the old delivery mechanism. Explicit SMTP behavior remains separate.

Required producer/consumer migration matrix:

| Producer | `from` value | Recipient handling |
|---|---|---|
| Pi/worker/supervisor | agent ID | WebSocket delivery to the addressed Pi identity |
| reminders v2 | `reminders` | WebSocket delivery to the addressed Pi identity |
| watchdog/PR watch | service name | WebSocket delivery to the addressed Pi identity |
| Purelymail | allowed normalized email address | supervisor mailbox; same steer/wake behavior as every message |
| remote delegation | remote adapter/agent name | explicit Pi recipient |

Every mailbox consumer must migrate to the Pi extension before the file mailbox is retired. There is no legacy-consumer bridge.

### 12.3 Producer migration inventory

Migrate direct writers and callers including:

- worker and supervisor status mail;
- Purelymail ingress;
- reminders; handoff workflows must be redesigned around documented Pi APIs before migration;
- standing-order and KB maintenance jobs;
- watchdog and remote watchdog;
- memory monitor;
- PR watcher;
- remote delegation and SSH forwarding.

Purelymail should preserve its deterministic IMAP-derived message IDs so retries remain idempotent. Preserve the existing sender allow list exactly: `wolfgangmeyers@gmail.com` and addresses under `agents.meyers.life`. Disallowed senders are logged and marked handled according to the existing email-ingress policy, but never enter Mailbox v2.

### 12.4 Retirement sequence

1. Ship router, protocol, sender, Pi extension, and diagnostics.
2. Change each producer to enqueue through the router.
3. Migrate reminders to v2.
4. Prove Pi active, idle, disconnected, restart, retry, ordering, and deduplication behavior.
5. Verify every mailbox consumer uses the Pi extension.
6. Verify no producer writes directly under `~/.mecha-developer/mailboxes/`.
7. Drain all unread legacy files and archive only if required for audit.
8. Remove mailbox cron entries and Claude mailbox hook registration.
9. Remove the mailbox-file implementation, hook symlink, and stale requirements.

### 12.5 Objective mailbox retirement criteria

File mailbox delivery may be removed only when all are true:

- a repository-wide search and runtime audit find zero direct mailbox-file writers;
- no active agent type depends on the Claude hook or mailbox cron;
- all queued router messages have explicit terminal state;
- active and idle Pi acceptance tests pass;
- offline replay, router restart, extension reconnect, lease expiry, and duplicate-ID tests pass;
- Purelymail, watchdog, PR watch, reminders, standing orders, KB reminders, memory monitor, remote delegation, and all skill instructions have migration tests or validated documentation checks;
- repository and installed-skill audits find no instruction or caller that uses `mailbox_send.py --to <email-address>`; outbound email uses `mailbox_email.py` explicitly;
- allowed email senders route to the configured supervisor identity and remain durable if that identity is unavailable; disallowed senders never enter Mailbox v2;
- pending v1 reminders are imported or explicitly classified;
- every active mailbox consumer has a tested Pi extension path; rollback restores the file mailbox as a whole rather than introducing a second delivery transport.

## 13. Reminders v2

### 13.1 Design

Reminders live in the router SQLite database and are scheduled by the router process. There is no reminders-to-mailbox handoff.

Before migration begins, replace the installed v1 `set_reminder.py` entry point with a fail-closed shim or v2 client, disable all v1 writers, and verify by process/repository audit that nothing can recreate `reminders.json`. Stopping cron alone is insufficient because a waiting v1 CLI can acquire the file lock after import.

A reminder record includes:

- reminder ID;
- target `agent_id`;
- message body and optional subject;
- next due time in UTC;
- optional fixed recurrence interval;
- creation time and creator identity;
- state (`scheduled`, `due`, `message_queued`, `acknowledged`, `canceled`, `failed`);
- linked router message ID and occurrence number;
- retry/attempt metadata.

### 13.2 Transactional firing

For a due one-shot reminder, one SQLite transaction:

1. verifies the reminder is still scheduled;
2. creates the router message with a deterministic ID derived from the reminder/firing;
3. records the linked message ID;
4. marks the reminder `message_queued`.

When the linked message is acknowledged, the server deletes both the delivered mailbox row and the completed one-shot reminder in the same transaction. Completed reminders therefore do not accumulate either. If its message expires before pickup, the one-shot reminder is deleted with it. If the linked message becomes `dead_letter`, the reminder becomes `failed` in the same transaction and remains visible until retried or explicitly dismissed. A crash before or after commit cannot silently lose the reminder; deterministic IDs prevent duplicate logical messages.

For a recurring reminder:

1. Fire only when the target currently has a live WebSocket lease.
2. If connected and no prior occurrence is outstanding, enqueue one deterministic occurrence and advance `next_due_at` by the interval in the same transaction.
3. If disconnected but `last_connected_at` is less than one hour old, enqueue nothing, skip that occurrence, and advance `next_due_at`; missed occurrences are never replayed or caught up.
4. If `last_connected_at` is at least one hour old, delete the recurring reminder and all other resources for that agent.
5. If the identity has never connected, delete the recurring reminder and all other resources for that target on its first due evaluation instead of firing it.
6. A recurring reminder remains scheduled after an occurrence is acknowledged; only the delivered message row is deleted. Occurrences never overlap.

Cancellation is allowed only in `scheduled` or `due` before a linked message is committed. After `message_queued`, `delete` returns a conflict containing the linked message state; it does not pretend to recall a delivery. A separate explicit message-cancel operation may be designed later.

### 13.3 Offline targets

A one-shot reminder creates a normal message, which may wait up to one hour for pickup. It is deleted if still unclaimed at expiry.

A recurring reminder never creates a queued message for a disconnected target. Recent disconnection skips only that occurrence. Never-connected or hour-stale targets cause the recurrence and the agent's remaining resources to be deleted.

### 13.4 CLI compatibility

Preserve useful v1 operations:

```text
reminders set --window <agent-id> --message "..." --in <duration>
reminders set --window <agent-id> --message "..." --at <ISO datetime>
reminders set --window <agent-id> --message "..." --every <duration>
reminders list
reminders delete <id>
reminders delete-all
```

Changes:

- `--window self|me|current` resolves only from an unambiguous current-agent environment value; it never falls back to `supervisor`;
- output always prints the resolved target;
- new absolute times must include an offset or timezone;
- `--every` accepts a positive fixed duration; cron-expression syntax is intentionally out of scope;
- deletion races return the actual state instead of pretending a due delivery was canceled;
- `list` exposes both reminder and linked-message state.

### 13.5 Migration from v1

1. Replace/disable the v1 set CLI and every other v1 writer; prove new v1 writes fail closed.
2. Stop the old reminder cron and verify no scheduler process remains.
3. Hold the existing reminders file lock.
4. Read and validate `~/.mecha-developer/reminders.json`.
5. Resolve each target explicitly; quarantine ambiguous `self`/fallback targets.
6. Interpret legacy naive timestamps using the documented current local-time behavior, then store UTC.
7. Import each reminder with a deterministic migration ID.
8. Verify counts and due times.
9. Rename the old file to a timestamped read-only migration artifact and verify no writer recreates it.
10. Start v2 scheduling.

Legacy `"delivery": "tmux"` handoff reminders are not imported. The Pi-only replacement must define and test handoff semantics using documented Pi APIs before those workflows are enabled.

### 13.6 Reminders v2 acceptance criteria

- Setting, listing, deleting, and deleting all reminders preserve the useful CLI contract.
- Relative and timezone-aware absolute times are tested.
- `self` resolution cannot target a different window silently.
- Due reminder creation and message enqueue are one transaction.
- Router crash at every transaction boundary causes neither loss nor duplicate logical firing.
- One-shot reminder messages wait no more than one hour for pickup.
- Recurring reminders fire only for currently connected targets, skip rather than replay missed occurrences, never overlap, and remain scheduled after successful occurrences.
- A recurring reminder for a never-connected or hour-stale identity is deleted without firing.
- A reminder is not complete until its message is acknowledged; completed one-shot reminders are deleted.
- Cancel/due concurrency has deterministic tested behavior.
- Duplicate scheduler execution is idempotent.
- Corrupt or malformed records are isolated rather than stopping all scheduling.
- Import proves source count, destination count, target, and due-time equivalence.
- Old cron and every v1 writer are disabled before v2 takes ownership; attempts to use the old CLI fail closed or route to v2.
- A linked dead-letter moves the reminder to visible `failed` state.
- Cancellation after message enqueue returns a conflict and cannot report false success.
- No v2 delivery writes a mailbox file.

## 14. Persistence

Use one SQLite database in `~/.mecha-developer/harness/` with WAL mode, foreign keys, and migrations.

Minimum tables:

- `messages`
- `delivery_attempts`
- `agent_leases`
- `agents` (first/last connection timestamps)
- `reminders`
- `schema_migrations`

SQLite is sufficient because this is one local user and one router process. It provides transactions needed by reminders and lease/retry state without introducing Redis or another service.

Mailbox rows are queue state, not history:

- every message has a fixed one-hour pickup deadline;
- expiry atomically deletes an unclaimed message and its delivery-attempt rows;
- acknowledgement atomically deletes a picked-up message and its delivery-attempt rows;
- no delivered or expired message body, envelope, or tombstone is retained in SQLite;
- dead letters remain only until an operator retries or dismisses them;
- diagnostics expose active queue size, dead-letter count, and oldest queue age.

Structured logs may record IDs and state transitions, but not message bodies. Logs are bounded by normal rotation and are not used as a second mailbox archive.

## 15. Error handling and observability

### 15.1 Logs

Structured logs include:

- message ID, sender, recipient, and state transition;
- connection, disconnection, last-connected update, and lease events;
- message expiry and agent-resource cleanup by ID, without bodies;
- retry count and reason;
- reminder ID and linked message ID;
- startup/kickstart failures;
- queue limits and dead letters.

Do not log message bodies by default. Logs must rotate.

Retention limits. All rotation uses one shared implementation (`harness/log_rotation.js`): the live file is renamed to `<log>.1` once it reaches the limit and the previous `.1` is discarded, so a log costs at most two generations. The size is checked before each append, so each generation can exceed its limit by its own final record.

- The router event log (`--log`, defaulting to `<database>.jsonl` and deployed as `~/.mecha-developer/router.jsonl`) rotates at 16 MiB, bounding it at 32 MiB plus one record per generation. Sizing rationale: delivery events run about 0.35 MB/day, but `agent_lease_renewed` adds roughly 1.5 MB per connected agent per day and dominates the file, so the retained window is about three days at three connected agents and about a day at ten. If worker counts grow, raise the limit or suppress the lease heartbeat.
- Each per-agent Claude sidecar log (`~/.mecha-developer/claude-sidecars/<agent-id>.log`) rotates at 256 KiB, so one agent's live plus rotated log stays under 512 KiB. The number of such files is one per agent ID and is not itself pruned.
- For the router event log, rotation is best effort and never costs a record: if the rotate step fails the event is still appended, and the first failure is recorded in-band as `router_error` with `error_code: log_rotation_failed`, so a log past its bound is diagnosable rather than merely large. The sidecar log does not have this guard — a rotation failure there propagates to the caller.
- A log already larger than its limit when rotation is introduced is moved wholesale into the first rotated generation, so the bound applies from the second rotation onward.
- `lifecycle_sweep_completed` is emitted when at least one of its counts is non-zero, plus an all-zero heartbeat at most once an hour. The sweep runs every second, so recording every idle pass added ~75k lines a day and no signal; recording none of them made an idle router and a router whose sweep loop had wedged look identical in the log. Any record resets the hour, so a busy router never pays for a heartbeat on top of its real records, and "no `lifecycle_sweep_completed` for over an hour" is a liveness failure. Per-item events (`message_expired`, `delivery_timed_out`, `reminder_*`, `server_job_*`) still record the underlying work.

### 15.2 Metrics and diagnostics

`agent_router.py status` and `/v1/health` report:

- router PID/version/schema;
- active agents, lease ages, and last-connected times;
- queued/leased/dead counts by recipient;
- oldest queued age and next message expiry;
- one-shot and recurring reminders scheduled/due/failed;
- connected recipients, queued counts, lease expiry, retries, and dead letters;
- database and log sizes;
- last startup or migration error.

The Pi extension uses one minimal `ctx.ui.setStatus()` line for connection and busy-queue state. It shows the queue count and a single truncated oldest-message preview, never a multiline widget. Messages themselves use `sendMessage(display: true)` once Pi emits them.

### 15.3 User-visible failures

- Missing identity: visible Pi error with exact configuration needed. If the diagnostic must survive session replacement, write a session entry with `pi.appendEntry()` and an entry renderer; `ctx.ui.notify()` alone is transient.
- Router unavailable: status indicator plus bounded restart attempts.
- Duplicate identity: show both instance IDs and refuse ambiguous delivery.
- Dead letter: notify the sender and expose it in diagnostics.

## 16. Security and trust boundaries

- Listen only on loopback.
- Store the database and state directory with user-only permissions.
- Validate every frame and request against one shared schema.
- Cap body, queue, and rate sizes before persistence.
- Never execute message content.
- Render incoming content as data, not markup with executable behavior.
- Treat claimed sender fields as display/routing labels, not security identities.
- Keep email ingress explicit and sanitize headers separately.
- Do not place authorization phrases on the ordinary message route until transcript and security semantics are deliberately redesigned.

The local macOS user remains trusted. Protecting against malicious same-user processes would require a different capability and sandbox model and is out of scope.

## 17. Testing strategy

### 17.1 Protocol and database tests

- exact six-field envelope validation and rejection of unknown fields;
- loopback-only binding and rejection of browser CORS requests;
- email allow-list enforcement and default supervisor routing;
- duplicate enqueue idempotency;
- per-recipient ordering;
- leases, one-hour message expiry, ack, nack, and dead-letter transitions;
- connected/recently-disconnected/stale/never-connected recurring-reminder behavior;
- atomic hour-stale agent resource cleanup;
- queue byte/count limits;
- transaction rollback and restart recovery;
- retention and migration behavior.

### 17.2 Pi extension tests

Use a controllable test router and Pi integration fixture:

- idle message calls `sendMessage(..., { triggerTurn: true })`;
- busy message calls `sendMessage(..., { deliverAs: "steer", triggerTurn: true })`;
- while busy, one status line shows queue count and a terminal-width-truncated oldest-message preview;
- multiple queued messages do not add lines or render full bodies;
- status clears after the final correlated message emission;
- `display: true` appears in transcript and content reaches the next model context;
- bottom editor contents remain unchanged;
- duplicate IDs render once;
- reconnect re-registers identity;
- shutdown closes sockets and timers;
- duplicate live identity is rejected visibly.

A live acceptance test in tmux must demonstrate both idle and active cases. Mock tests alone do not satisfy this requirement.

### 17.3 Router lifecycle tests

- clean install and uninstall of LaunchAgent;
- connection refusal triggers kickstart;
- concurrent clients do not launch duplicate routers;
- router crash restarts under `launchd`;
- logs rotate;
- upgrades preserve queued messages and reminders.

### 17.4 Compatibility tests

Port the useful cases from current `mailbox_test.py`:

- recipient isolation;
- system sender classification;
- email ingress identity;
- stable ordering and IDs;
- large-body/file-pointer policy.

Add end-to-end tests for every migrated producer and prove that one logical message takes exactly one route. Compatibility tests must verify that disconnected and not-yet-registered identities remain queued until reconnect or one-hour expiry, reconnect delivers in order, router restart preserves the queue and deadlines, and no mailbox path invokes tmux input. They must also prove that Mailbox v2 rejects email-address recipients, `mailbox_email.py` sends explicitly addressed email, inbound allowed email routes to `supervisor`, and skill-generated reply instructions select the explicit email command.

## 18. Overall acceptance criteria

The initial implementation is ready for opt-in use when:

1. A Pi agent in a tmux window registers under its exact window identity.
2. `mailbox_send.py` durably posts a mailbox message and reports its ID.
3. A connected idle Pi agent receives the newly posted message immediately, displays it without changing the bottom editor, and starts a turn.
4. A connected busy Pi agent receives the newly posted message immediately, shows a one-line truncated queue preview, and consumes it at the next agent-call boundary without corrupting current input. Queue visibility remains one line regardless of depth.
5. A message posted while its recipient is disconnected remains durable and replays in order immediately after that recipient reconnects.
6. With Pi session persistence enabled, router and Pi restarts do not lose queued state; acknowledged messages are absent because successful delivery deletes them.
7. With Pi session persistence enabled, duplicate delivery attempts and crash-after-render/before-ack replay create one visible Pi message. `--no-session` is explicitly reported as at-least-once/diagnostic mode.
8. Duplicate agent identities fail visibly rather than fan out.
9. The router starts through `launchd` when the extension finds it unavailable.
10. Transport status distinguishes queued, leased, injection-requested, and dead-letter states; acknowledgement is observable as the transition that removes the message.
11. Reminders v2 passes the criteria in section 13.6.
12. The mailbox retirement gates in section 12.5 are machine-checkable and pass before removal.
13. Every enabled workflow has migrated to a tested Pi-native path; unsupported non-Pi and terminal-control paths are removed at cutover.
14. No implementation depends on undocumented Pi APIs.
15. No mailbox message is delivered through tmux; disconnected and unknown identities remain queued until the addressed Pi extension connects or the fixed one-hour pickup deadline expires.
16. Unclaimed messages are deleted at one hour without retained bodies or tombstones.
17. Recurring reminders fire only for connected agents; stale or never-connected targets are cleaned up according to section 13.2.
18. One hour after disconnection, an agent has no remaining queue, reminder, lease, attempt, or agent-record resources.

## 19. Rollout

### Phase 1: protocol and router

Implement schema, SQLite state machine, HTTP enqueue, WebSocket subscription, loopback-only binding, diagnostics, and LaunchAgent management. Test without changing producers.

### Phase 2: Pi opt-in

Install the extension for one named Pi agent. Prove live idle, busy, reconnect, and restart behavior while Pi continues to run in tmux. Keep the file mailbox available only as rollback during migration.

### Phase 3: sender and backwards compatibility

Add `mailbox_send.py`. Move a small set of non-critical producers to router-first delivery and prove disconnected recipients remain queued until WebSocket reconnection or one-hour expiry.

### Phase 4: reminders v2

Implement scheduler and CLI, migrate pending reminders under lock, and disable v1 cron after reconciliation.

### Phase 5: producer migration

Move email ingress, watchdog, PR watch, standing orders, remote delegation, and worker status producers. Track direct mailbox writes as a failing audit.

### Phase 6: retirement

After the observation period and all section 12.5 gates pass, remove file mailbox hooks, cron, and `worker_send.py`.

## 20. Unresolved decisions

These require review or implementation evidence:

1. **Implementation language:** Python minimizes integration work with current mecha tools; TypeScript could share types with the Pi extension. Recommendation: Python router plus generated JSON Schema, with TypeScript validation generated or tested against shared fixtures. Do not add a cross-language framework.
2. **WebSocket dependency:** Python standard library has no WebSocket server. Prefer an already-installed dependency if present; otherwise compare one small dependency against implementing the router in Node. Do not hand-roll WebSocket framing.
3. **Acknowledgement correlation:** verify that `message_end` exposes the emitted custom message and embedded router message ID reliably in extension code. If it does not, find another documented post-consumption/session-entry event. Wrapper return alone is never an acknowledgement.
4. **Lease replacement:** decide whether an operator command may forcibly replace a live duplicate or whether only lease expiry can do so. Default should reject.
5. **Retention periods and queue limits:** choose values from observed mailbox volume rather than guesses.
6. **Migration observation period:** choose the minimum period covering every scheduled producer, including infrequent jobs.
7. **Remote agents:** this v1 router is local only. Existing SSH forwarding may enqueue into each remote machine's local router; network federation is not justified yet.
8. **Handoff control:** determine whether documented Pi APIs can implement clear/resume safely. Until proven, the handoff workflow is unsupported rather than implemented through terminal injection.

## 21. Implementation guardrails

- Use documented Pi APIs only.
- Keep one router and one durable store.
- Do not add Redis, NATS, Kafka, or cloud infrastructure.
- Keep ordinary messages separate from terminal control.
- Never dual-deliver a logical message.
- Make every migration reversible until its acceptance gates pass.
- Treat injection request, Pi message emission, visible transcript rendering, model context, disk-session persistence, and semantic task completion as distinct facts.
