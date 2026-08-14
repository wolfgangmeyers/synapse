# Durable subagent lifecycle state

## Problem

The current path treats subagent activity as a short-lived UI hint. `pi-extensions/mecha-messaging.js` derives a count from Pi lifecycle events and a native-service poll, then `mailbox-client.js` sends an unauthenticated-for-ordering `activity` frame. `harness/router.js` keeps that frame in `activityByAgentId`, an in-memory map with a 15-second TTL. The router loses it on restart and accepts whichever valid frame arrives last. The web UI can also lose a live update: `updateAgentActivity` only maps existing rows, so an update for an agent absent from its last `/v1/agents` refresh is dropped.

Polling fixed some missing Pi lifecycle events. It cannot make an ephemeral, last-write-wins transport reliable across router restart, reconnect, duplicate delivery, or out-of-order frames.

## Outcome

The router owns durable, idempotent subagent lifecycle state. A parent extension sends ordered lifecycle events plus complete snapshots. Each accepted state transition commits its ledger row and materialized state in one SQLite transaction, then receives an acknowledgment. Duplicate, stale, conflict, and accepted-no-op outcomes receive only an idempotent acknowledgment and bounded diagnostic counter. Router snapshots and replay let an observer recover after a restart or reconnect. The web UI applies each parent agent's newest version and creates a row when a live update arrives first.

The current `activity` frame stays available during a bounded rollout. It is no longer authoritative for subagent state after a parent has completed the new sync handshake.

No mailbox message delivery, lease semantics, reminders, conversation history, or native Pi subagent behavior changes.

## Data model

Add an additive, atomic SQLite migration, `017_subagent_lifecycle.sql`, after `016_delivery_batches.sql`. It creates only the v2 tables and indexes in one transaction. Old router code ignores these new tables, so it can run against a database already at 017. Before applying 017, take a SQLite backup. Rollback restores that backup and restarts the prior router; it never tries to reverse v2 rows in place.

### Lifecycle-owned producer identity

The lifecycle tables have no foreign key to `agents`, active leases, or any short-lived presence row. They retain immutable producer identity after the parent disconnects or its presence row is removed.

Every producer, state, and ledger row stores these bounded identity fields: `parent_agent_id` (128 UTF-8 bytes), `instance_id` (128 bytes), `parent_session_id` (128 bytes), and UUID `producer_epoch` (36 ASCII bytes). The tuple is immutable after the first accepted snapshot. `lease_id` is a current write fence, not lifecycle ownership.

### `subagent_lifecycle_producers`

One current producer row per parent agent:

- the immutable producer identity tuple;
- the currently fenced `lease_id`;
- `highest_client_sequence`, `last_event_id`, and `last_snapshot_sequence`;
- `authority` (`legacy` or `v2`);
- `last_seen_at`, `created_at`, and `updated_at`.

An epoch belongs to the parent process/instance, not to one lease. On a replacement lease, the router may rebind the same epoch only when the authenticated registration matches the stored identity tuple and then submits a complete snapshot. The rebind updates the fence atomically. A new process or parent instance creates a new identity tuple and epoch. It can replace the old producer only through its first complete snapshot. A different epoch using an existing identity tuple is rejected. Any nonterminal child omitted by an accepted replacement snapshot becomes terminal with reason `producer_replaced`. Old-epoch frames cannot reappear.

### `subagent_lifecycle_state`

One materialized row per parent/subagent pair:

- `parent_agent_id`, `subagent_id` (composite primary key);
- the immutable producer identity tuple that last changed the row;
- sanitized `type` and `description`;
- `state` (`running` or terminal);
- `started_at`, `updated_at`, `terminal_at`, and terminal reason;
- `router_version` of the accepted transition.

Only `running` rows appear in the normal agent snapshot. Terminal rows remain as tombstones for the retention interval so delayed or duplicated events cannot resurrect them.

### `subagent_lifecycle_events`

Append-only accepted-transition ledger:

- `router_version INTEGER PRIMARY KEY` from the transaction-owned sequence;
- the immutable producer identity tuple, subagent, event ID, client sequence, and event kind;
- bounded sanitized metadata needed to reproduce the transition;
- `received_at`.

A ledger row and router version exist only when a valid event or snapshot changes lifecycle state. A snapshot that matches materialized state is acknowledged as an accepted no-op and creates neither. A reconciling snapshot writes one ledger row for each transition it causes. Uniqueness covers `(parent_agent_id, producer_epoch, event_id)` and `(parent_agent_id, producer_epoch, client_sequence)`. The event ID detects a replay even when a client retries with a new sequence by mistake. The sequence detects conflicting reuse. Duplicate, stale, conflict, and accepted-no-op outcomes receive an idempotent acknowledgment plus a bounded diagnostic counter. They create no ledger row and no router version. Diagnostics contain no descriptions or source text.

The single `router_version` is monotonically increasing across accepted lifecycle transitions. It orders the ledger, live state, snapshots, and replay responses. It is not wall-clock time.

## Producer protocol

New lifecycle sync frames are optional additions to the v1 WebSocket schema. They are used only after capability discovery. Existing register, lease renew, delivery, and legacy activity frames stay byte-compatible.

### Capability discovery and enablement

A new loopback `GET /v1/capabilities` reports `subagent_lifecycle_v2`. Old routers return their existing 404. A new extension or web relay only sends new frames after that capability is present.

After ordinary registration, a v2 reader sends `subagent_sync_enable`. The router replies with its durable snapshot. A v2 producer sends its current complete snapshot immediately after registration, after an accepted enablement response, after any local lifecycle change, after every reconnect, and on the existing activity heartbeat. The heartbeat remains bounded by the current interval; snapshots replace state and do not append unchanged children as new lifecycle transitions.

### Producer fields

Every v2 lifecycle frame contains:

- current `agent_id`, `instance_id`, `parent_session_id`, and `lease_id`
- `producer_epoch`
- strictly increasing `client_sequence` within that epoch
- stable `event_id` for retries
- a complete snapshot or one lifecycle change

The router fences every write to the authenticated socket registration and its current lease. Once a lease is replaced, the old socket cannot write, even if it presents a valid old epoch, sequence, or event ID. A reconnect using the same parent instance preserves its epoch and retry journal; a new process or parent instance uses a new epoch.

A snapshot also contains the complete current nonterminal-child set. It is a reconciliation assertion, not a delta. Native `queued` and `running` children both map to visible lifecycle state `running`; a child becomes visible when queued and remains visible until a terminal native status or omission from an accepted authoritative snapshot. A child absent from an accepted current-epoch snapshot becomes terminal with reason `snapshot_omitted`.

Descriptions are UI labels, not prompt text. Producers trim whitespace, cap values before transmission, and never send user prompts, tool input/output, model messages, file paths, credentials, or error stacks. The router validates the same limits and stores only the safe fields already intended for the badge: ID, optional type, and a short description.

### Order and idempotency rules

For the current parent/epoch:

1. A frame with an existing event ID and identical payload receives an idempotent duplicate acknowledgment. It identifies the original applied version and current snapshot version. A bounded diagnostic counter is the only new record.
2. A client sequence below the committed high-water mark receives an idempotent stale acknowledgment with the current snapshot/version and a bounded diagnostic counter.
3. A client sequence equal to the high-water mark with a different event ID or payload receives a conflict acknowledgment and a bounded diagnostic counter.
4. A later sequence is accepted only after schema, authenticated registration, lease fence, epoch identity, and snapshot-completeness checks pass. If it changes no lifecycle state, it receives an accepted-no-op acknowledgment and a bounded diagnostic counter.
5. A terminal event always writes a tombstone, including when it arrives before any start for that child. A late start, heartbeat, or snapshot cannot revive that ID in the same epoch.
6. On a replacement lease, the existing epoch may rebind only after the matching authenticated registration and a complete snapshot. The accepted snapshot is authoritative for omitted children.
7. A new epoch from a new parent instance/session may introduce the same child ID only through its first complete snapshot. It supersedes the old epoch atomically. A new epoch claiming the old parent identity is rejected.

Retries retain their event ID and sequence, so a lost acknowledgment is idempotent. A retry creates no second ledger row or router version.

### Retry journal limits

The extension keeps at most 256 unacknowledged items and 262,144 bytes. Byte accounting uses the UTF-8 byte length of each canonical serialized v2 frame, including its envelope. A single item may use at most 65,536 bytes. Entries retain only event ID, sequence, enqueue time, and sanitized payload. They expire after 10 minutes from first enqueue.

Before admitting an item that would exceed either journal limit, or when the oldest entry reaches 10 minutes, the extension atomically replaces all unacknowledged entries with one newest complete snapshot using a new event ID and later sequence. It records a bounded `journal_collapsed` or `journal_expired` diagnostic. The replacement snapshot must satisfy the one-item and total-byte limits. No partial snapshot is sent.

A frame or required replacement snapshot over 65,536 bytes fails v2 closed: the extension does not enqueue, send, truncate, or claim synchronization for it; it clears the journal, records a bounded `journal_oversize` diagnostic, and disables v2 for that connection until a reconnect can submit a valid complete snapshot. Legacy dual-send may continue during the compatibility window, but it does not overwrite a parent already marked v2. An extension restart creates a new epoch, so its first valid complete snapshot repairs durable router state.

The router sends a lifecycle acknowledgment only after the transaction commits. A lost acknowledgment is safe because the event ID and sequence make the retry duplicate-safe.

## Router behavior

`harness/router.js` uses repository methods for all v2 lifecycle state. It does not retain a second in-memory subagent authority.

Each accepted lifecycle transition commits in one immediate SQLite transaction:

1. producer identity, active lease fence, and high-water state;
2. one applied ledger entry for that transition;
3. its running-state row or terminal tombstone;
4. the next router version.

Only after that commit does the router send the producer acknowledgment and publish the update. Duplicate, stale, conflict, accepted-no-op, failed-registration, old-lease, and stale-epoch frames leave lifecycle state unchanged. Each receives its idempotent acknowledgment and increments only its bounded diagnostics counter. It creates no ledger row or router version.

The router builds `/v1/agents` from active leases plus durable running state. Each agent carries `subagent_version`; the response carries `activity_snapshot_version`. The legacy activity state can still supply parent `active`, `idle`, and `compacting` during rollout. V2 owns `subagent_count` and `subagents` once a producer is marked `v2`.

V2 readers receive:

- a complete `subagent_state_snapshot` after enablement, registration, reconnect, and explicit resync;
- an ordered `subagent_state_update` after each committed mutation;
- `subagent_state_replay` when the requested `after_version` remains in the retained ledger; otherwise a complete snapshot.

The router sends normal legacy `agent_activity` frames unchanged throughout the rollout so old readers continue to work. It sends v2 frames only to readers that enabled v2. A reader may request replay after a disconnect using its last router version.

The router records bounded counters and reason codes for accepted events, duplicates, stale events, conflicts, replay fallback, journal-pressure snapshots, and retention pruning. Diagnostics contain IDs, versions, counts, and reason codes only. They do not contain descriptions or source text.

At v2 startup, the router verifies that migration 017 is present before advertising `subagent_lifecycle_v2` or accepting a v2 frame. A new router pointed at an old database fails v2 closed with a clear migration-017 error; it does not fall back to an in-memory v2 store. Legacy activity can continue where the normal router startup path permits it.

## Retention

Lifecycle data has explicit bounded retention:

- running state remains while the parent has a valid lease and continues to heartbeat;
- at seven days after `last_seen_at`, a producer without a live lease has each remaining running child converted to a `producer_expired` terminal tombstone; the tombstone fences delayed old-epoch frames for its normal 24-hour interval;
- terminal tombstones remain for 24 hours;
- ledger records remain for 7 days or the newest 100,000 rows, whichever is smaller;
- producer rows without a live lease remain for 7 days after `last_seen_at`; lifecycle pruning removes them after their expired-child tombstones and ledger rows are eligible for pruning, independently of any parent-agent or lease cleanup;
- every router lifecycle sweep runs the prune operation and emits counts.

The migration adds indexes for parent/version replay, terminal pruning, and event identity lookup. Retention tests must prove delayed terminal/start replays are rejected during the tombstone window and that data is removed at the stated bounds.

## Web UI behavior

The router web relay forwards snapshot version, per-agent version, v2 update, replay, and resync events. It capability-checks before enabling v2 and continues its current legacy refresh path for an old router.

The browser stores a per-agent `subagent_version` and a last applied router snapshot version.

- It ignores a lower per-agent version.
- It applies an equal version only when the payload is identical.
- It replaces an agent's subagent state from an accepted snapshot.
- It upserts an unknown agent when a live v2 or legacy activity update arrives before the next agents list.
- A snapshot newer than the browser's last snapshot version may remove rows absent from that snapshot. Older snapshots cannot erase newer live state.
- Zero is authoritative. It clears the badge, descriptions, and expanded state.

Existing badge layout and safe HTML escaping remain unchanged.

## Compatibility and rollout

### Stage 1: router, schema, repository, and readers

Ship the migration, capability endpoint, v2 schema, repository transaction methods, durable snapshot/replay, retention, router relay support, and browser version-aware readers. The new router continues to accept legacy `activity` frames and publishes legacy frames unchanged. No extension needs to reload for this stage to be safe.

### Stage 2: extension dual-send

A new extension capability-checks the router. With v2 support, it enables v2, sends durable snapshots/events with acknowledgments and retry journal, and also sends the old activity frame. With an old router it sends only the old frame. Once the router has accepted the first v2 snapshot for a parent, v2 is the router's only subagent authority for that parent; duplicate legacy frames cannot overwrite it.

### Stage 3: immediate rollout, then compatibility maximum

Fourteen days is a compatibility maximum, not a wait before rollout. As soon as the compatible router, relay, and browser UI release is live, install the canonical extension and reload the important current Pi parent sessions and connected UI/relay sessions. Each reloaded parent must reconnect, send its complete v2 snapshot, and appear as v2 in diagnostics. Sessions that cannot be reached continue on the legacy fallback for no more than 14 days from the compatible release.

Router diagnostics list connected legacy producers by agent and instance without descriptions. They must show no legacy producer for every reloaded session. At the deadline, ship the removal release: the extension stops dual-send, and the router keeps legacy activity only for parent activity/compaction. Legacy `subagent_count` and `subagents` are ignored. Do not remove v2 replay readers until all supported UI clients have had the same compatibility maximum.

### Live rollout procedure

1. Inspect the live router process and record its exact executable, arguments, working directory, and service wrapper. Use that same start command for the replacement.
2. Identify the live SQLite database and make a verified SQLite backup before stopping the router.
3. Take the router through one controlled switchover: stop the old process, apply additive migration 017 atomically, then immediately restart the updated router with the recorded command. Do not run an old and new router against the database at the same time.
4. If migration or restart fails, restore the verified backup and restart the prior router with its recorded command. Do not attempt an in-place downgrade.
5. Check `GET /v1/capabilities` on the restarted router before installing or enabling v2 clients.
6. Install the canonical extension, then reload or reconnect the important Pi parent sessions, relay, and browser UI.
7. For a reloaded parent, observe a real child start, finish, and socket reconnect. The badge must appear, clear, and recover from the durable snapshot after reconnect.
8. Check diagnostics after the reloads. Every reloaded parent is v2; only unreachable sessions may remain legacy during the 14-day maximum.

## Deterministic acceptance matrix and traceability

The named R1 `node:test` cases below are required. R1 exercises lifecycle snapshots directly through the router and repository, including behavior Stage 2 will produce later. Stage 2 remains quarantined: its rows are explicit deferred contracts, not claimed test coverage. Release 3's controlled test uses a temporary home, SQLite database, and captured-process fixture. The live runbook remains required after it passes.

| Case | Expected result | Deterministic test file and name | Stage |
| --- | --- | --- | --- |
| Start then finish | A running row and badge appear. The terminal transition removes it from snapshots and creates a tombstone. | `tests/mailbox_v2_delivery.test.mjs` — `persists start and terminal lifecycle transitions` | R1 |
| Duplicate start, finish, snapshot, or lost acknowledgment retry | One committed transition/version. A duplicate receives an idempotent acknowledgment plus diagnostic counter. It creates no ledger row or version. | `tests/mailbox_v2_delivery.test.mjs` — `acknowledges duplicate lifecycle frames without a ledger row or version` | R1 |
| Matching complete snapshot | Accepted no-op acknowledgment plus diagnostic counter. No lifecycle state, ledger row, or version changes. | `tests/mailbox_v2_delivery.test.mjs` — `acknowledges a matching snapshot without a ledger row or version` | R1 |
| Out-of-order delta | A lower sequence gets a stale acknowledgment plus diagnostic counter. It creates no ledger row or version and cannot alter the badge. | `tests/mailbox_v2_delivery.test.mjs` — `acknowledges stale lower-sequence lifecycle frames without mutation` | R1 |
| Same sequence or event ID with different payload | Conflict acknowledgment plus diagnostic counter. No state, ledger, or version mutation. | `tests/mailbox_v2_delivery.test.mjs` — `rejects conflicting lifecycle identity reuse without mutation` | R1 |
| Terminal before start | Router writes an unknown-child tombstone. A later same-epoch start cannot resurrect it. | `tests/mailbox_v2_delivery.test.mjs` — `keeps terminal-first unknown-child tombstones terminal` | R1 |
| Late start after terminal | Tombstone wins for the epoch. | `tests/mailbox_v2_delivery.test.mjs` — `rejects a late start after a terminal tombstone` | R1 |
| Queued native child | A normalized queued child is visible as `running` until terminal or accepted authoritative omission. | `tests/mailbox_v2_delivery.test.mjs` — `keeps queued native children visible as running` | R1 |
| Replacement lease, same parent instance | Matching registration and complete snapshot rebind the epoch. Old-socket writes are fenced. | `tests/mailbox_v2_delivery.test.mjs` — `rebinds a same-instance epoch only through a fenced replacement snapshot` | R1 |
| Changed epoch with old parent identity | Router rejects it. | `tests/mailbox_v2_delivery.test.mjs` — `rejects a changed epoch for an existing producer identity` | R1 |
| New process or parent instance | Its first complete snapshot may replace the old epoch. | `tests/mailbox_v2_delivery.test.mjs` — `replaces a producer only from a new-instance first snapshot` | R1 |
| Reused child ID in a new epoch | Replacement snapshot may create it. Old-epoch events stay stale. | `tests/mailbox_v2_delivery.test.mjs` — `allows a reused child id only in a new-epoch replacement snapshot` | R1 |
| Missed Pi start or finish event | Next heartbeat complete snapshot converges router state. | `tests/mailbox_v2_delivery.test.mjs` — `repairs missed native lifecycle events with a heartbeat snapshot` | R1 |
| Parent settles while a child runs | A settled parent heartbeat leaves its durable child visible. | `tests/mailbox_v2_delivery.test.mjs` — `keeps lifecycle snapshots active while a settled parent has a child` | R1 |
| Router restart | SQLite state and router version survive. Registration/enablement restores reader state. | `tests/mailbox_v2_delivery.test.mjs` — `restores durable lifecycle state after router restart` | R1 |
| Socket reconnect with stable retry identity | Retrying the same lifecycle event after reconnect is duplicate-safe and creates no second transition. | `tests/mailbox_v2_delivery.test.mjs` — `retries journal entries across reconnect without a second transition` | R1 |
| Replay cursor retained | Router returns ordered transitions after the cursor. | `tests/mailbox_v2_delivery.test.mjs` — `replays retained lifecycle transitions after a cursor` | R1 |
| Replay cursor pruned | Router returns a full durable snapshot and current version. | `tests/router_event_log_retention.test.mjs` — `falls back to a snapshot for a pruned lifecycle cursor` | R1 |
| Live update before UI agents refresh | UI upserts the unknown parent and renders the badge. | `tests/router_web_ui.test.mjs` — `upserts an unknown parent from a live lifecycle update` | R1 |
| Older live frame after newer UI state | UI ignores it. | `tests/router_web_ui.test.mjs` — `ignores an older live lifecycle update` | R1 |
| Older snapshot after newer live frame | UI ignores it and preserves the badge. | `tests/router_web_ui.test.mjs` — `does not let an older snapshot erase newer live lifecycle state` | R1 |
| Newer zero-count snapshot | UI clears count, descriptions, and expansion. | `tests/router_web_ui.test.mjs` — `clears lifecycle badge state from a newer zero-count snapshot` | R1 |
| New router with old extension/UI | Legacy activity behavior remains unchanged. | `tests/subagent_lifecycle_rollout.test.mjs` — `preserves legacy activity for an old lifecycle client` | R1 |
| New extension/UI with old router | Capability check falls back cleanly to legacy behavior. | Deferred: Stage 2 extension capability client | R2 quarantined |
| New extension/UI with new router | V2 is authoritative. Legacy dual-send cannot overwrite it. | Deferred: Stage 2 extension dual-send | R2 quarantined |
| Old router with database at 017 | Old code ignores additive v2 tables and retains legacy behavior. | `tests/subagent_lifecycle_rollout.test.mjs` — `keeps old-router legacy behavior on a database at migration 017` | R1 |
| New router with database before 017 | V2 is unavailable with a migration-017 error. No in-memory fallback. | `tests/subagent_lifecycle_rollout.test.mjs` — `fails v2 closed when migration 017 is missing` | R1 |
| Live compatible rollout | Captured command, backup, atomic migration/restart, capability, isolated extension installation, and rollback pass. | `tests/subagent_lifecycle_rollout.test.mjs` — `validates installed router and client rollout in an isolated home` | R3 |
| Unreachable session during compatibility maximum | Only that session may use legacy fallback for at most 14 days. | Deferred: Stage 3 compatibility-window behavior | R3 deferred |
| Stale session at legacy cutoff | Router reports it during the window, then ignores its legacy child count until reload. | Deferred: Stage 3 cutoff behavior | R3 deferred |
| Retention sweep | A disconnected running child becomes a bounded terminal tombstone at seven days; its producer follows after the tombstone interval. Live running state remains. | `tests/router_event_log_retention.test.mjs` — `expires disconnected running children into tombstones at seven days, then removes their producer` | R1 |
| Unsafe or oversize description | Producer/router reject or truncate before persistence. Logs and diagnostics contain no description text. | `tests/protocol.test.mjs` — `enforces lifecycle metadata limits without logging source text` | R1 |
| Journal item limit | Entry 257 atomically collapses the journal into one complete snapshot. | Deferred: Stage 2 retry journal | R2 quarantined |
| Journal byte limit | A frame that would exceed 262,144 serialized bytes collapses the journal into one complete snapshot. | Deferred: Stage 2 retry journal | R2 quarantined |
| Journal expiry | The oldest entry at 10 minutes is replaced with one complete snapshot. | Deferred: Stage 2 retry journal | R2 quarantined |
| Oversized single journal item | A frame over 65,536 serialized bytes fails v2 closed. It is not sent, truncated, or journaled. | Deferred: Stage 2 retry journal | R2 quarantined |
