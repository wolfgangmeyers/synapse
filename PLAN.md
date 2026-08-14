# Durable subagent lifecycle plan

## Scope

Implement `SPEC.md` in three compatible releases. The first makes router state durable. The second lets the Pi extension use it while keeping legacy activity alive. The third completes immediate rollout and retires legacy child-state authority at the compatibility deadline.

The producer epoch belongs to a parent process/instance across replacement leases. A reconnecting parent reuses its epoch only after authenticated registration and a complete snapshot. A new process or parent instance starts a new epoch. Lease fencing rejects writes from a replaced socket. Retry identity stays stable, so retries remain idempotent.

This plan replaces the current extension-only fix. The relevant current code is:

- `pi-extensions/mecha-messaging.js`: native lifecycle events, polling, local tombstones, and heartbeat.
- `pi-extensions/mailbox-client.js`: registration, socket reconnect, and fire-and-forget `sendActivity`.
- `harness/router.js`: in-memory `activityByAgentId`, TTL sweep, `/v1/agents`, and activity broadcast.
- `harness/db.js`: SQLite repository and numbered migration list.
- `protocol/v1/schema.json`: strict client/server frame schemas.
- `tools/router_web_ui.mjs` and `web/router-ui/app.js`: router relay and browser activity state.

## Non-goals

- Do not change mailbox delivery, lease, reminder, message, conversation, image, sentinel, or Pi-native subagent execution behavior.
- Do not persist prompts, messages, tool data, file paths, errors, or credentials as lifecycle descriptions.
- Do not use a process-local map as the authoritative v2 state.
- Do not require all active extensions or web clients to reload in the first release.
- Do not delay rollout for 14 days. Fourteen days is the maximum legacy fallback for unreachable sessions after the compatible router/UI release.
- Do not remove existing activity frames until the compatibility maximum has finished.

## Release 1: durable router and version-aware readers

### 1. Add migration and repository API

Add `harness/migrations/017_subagent_lifecycle.sql`, register it in `harness/db.js`, and add repository methods that own lifecycle writes and reads. Migration 017 is additive and atomic: it creates only v2 tables and indexes in one SQLite transaction. Old router code ignores those tables. The rollout takes a verified database backup before applying it; rollback restores that backup and starts the prior router, never an in-place downgrade.

Tables and indexes follow `SPEC.md`:

- lifecycle-owned producer identity fields: bounded `parent_agent_id`, `instance_id`, `parent_session_id`, and epoch, with no foreign key to `agents`, active leases, or another short-lived presence table;
- materialized child lifecycle rows (native `queued` and `running` both map to visible `running`);
- terminal tombstones, including an unknown child that receives a terminal first;
- append-only accepted-transition ledger with global monotonic `router_version`;
- indexes for parent state, replay cursor, terminal pruning, and event identity.

Lifecycle pruning owns producer retention. It runs independently of parent-agent cleanup and removes a disconnected producer only after seven days and after its tombstones and ledger rows are eligible for their own retention rules.

Repository methods:

- `applySubagentLifecycleSnapshot(...)`
- `applySubagentLifecycleEvent(...)`
- `subagentLifecycleSnapshot(...)`
- `replaySubagentLifecycle(...)`
- `pruneSubagentLifecycle(...)`
- read helpers used by the connected-agent snapshot

Each accepted lifecycle transition uses the existing immediate-transaction pattern. It validates authenticated registration, `agent_id`/`instance_id`/`parent_session_id` epoch identity, the socket's current lease fence, sequence, event ID, limits, and privacy-safe metadata before changing producer, one ledger row for that transition, materialized state, and router version together. A complete snapshot that matches state is accepted as a no-op and changes none of them. The same epoch can rebind to a replacement lease only after the matching authenticated registration submits a complete snapshot. A replaced socket is rejected before it can write.

It returns an explicit disposition (`applied`, `accepted_noop`, `duplicate`, `stale`, or `conflict`) plus the durable version/snapshot. Every non-transition outcome receives an idempotent acknowledgment plus only a bounded diagnostics counter. It creates no ledger row or router version.

Do not expose the raw SQLite handle to `router.js`. Keep SQL, migration, validation, and transaction boundaries in `db.js`.

### 2. Add v2 protocol without changing old frames

Extend `protocol/v1/schema.json` and `harness/protocol.js` for these new frame families:

- capability response data;
- `subagent_sync_enable`;
- producer snapshot and lifecycle event frames;
- post-commit lifecycle acknowledgment;
- reader resync/replay request;
- durable snapshot, replay, and update server frames.

Use separate v2 frame types. Every producer frame requires `agent_id`, `instance_id`, `parent_session_id`, `lease_id`, epoch, sequence, and event ID. The router rejects a changed epoch that claims an existing parent identity. Do not add required fields or unknown properties to `register`, `activity`, `agent_activity`, or existing registered responses. Old strict readers must continue to parse their current frames.

Add a loopback-only `GET /v1/capabilities` in `harness/router.js`. It returns v2 support and protocol limits. Existing routers naturally return 404. Capability discovery happens before any client sends a new frame.

### 3. Make router state authoritative

In `harness/router.js`:

1. Keep current `activityByAgentId` and TTL behavior as the legacy fallback in Release 1.
2. Route v2 frames to repository methods only after authenticated registration and current-socket lease fencing. Match the parent instance with `agent_id`, `instance_id`, `parent_session_id`, and epoch.
3. Permit a replacement lease to rebind that epoch only when the matching registered connection submits a complete snapshot. Reject old-socket writes even when their payload is otherwise valid.
4. Send correlated v2 acknowledgments only after repository return confirms commit. Duplicate, stale, conflict, and accepted-no-op outcomes get an idempotent acknowledgment plus bounded diagnostic counter. They create no ledger row or version.
5. Broadcast ordered v2 updates only after commit.
6. On v2 reader enablement, registration, reconnect, and explicit resync, send a complete durable snapshot.
7. Add replay from `after_version`; return a snapshot if the cursor predates retained ledger data.
8. Make `connectedAgentsSnapshot()` select v2 subagents/count/version for a parent with v2 authority. Otherwise use legacy in-memory activity fields.
9. At startup, verify migration 017 before advertising or accepting v2. A new router on a pre-017 database fails v2 closed with a clear migration error and never builds an in-memory v2 fallback.
10. Run lifecycle retention from `sweepMessageLifecycles()` and include only counts/reason codes in its existing event log.

The current `activityByAgentId` map must not receive v2 child state. It remains only for parent activity/compaction and legacy producers during the rollout.

### 4. Make relay and browser version-aware

In `tools/router_web_ui.mjs`:

- capability-check the selected router before enabling v2 after registration;
- forward v2 snapshot, update, replay, and version fields to the browser;
- request replay after reconnect when it has a cursor; accept a router snapshot fallback;
- keep the existing HTTP `/v1/agents` refresh and legacy activity relay for old routers.

In `web/router-ui/state.js` and `web/router-ui/app.js`:

- track the latest router snapshot version and per-agent subagent version;
- merge snapshots without allowing an older snapshot to erase newer state;
- ignore stale live updates;
- make `updateAgentActivity` upsert an unknown agent before rendering;
- make a versioned zero-count update clear summaries and expanded state;
- retain the current escaping and badge markup.

Do not recreate the deleted `web/router-ui/subagents.js` helper. Keep the changes in the existing state/app modules unless a small pure helper becomes necessary for deterministic version tests.

### 5. Release 1 tests

Add or extend tests in these files:

- `tests/protocol.test.mjs`: valid/invalid v2 frames, strict legacy compatibility, IDs, sequences, and metadata limits.
- `tests/mailbox_v2_delivery.test.mjs`: router transaction dispositions, committed acknowledgment order, same-epoch replacement-lease rebind, old-socket write fencing, rejection of a changed epoch with the old parent identity, new-instance epoch replacement, unknown-terminal tombstones, replay, restart, and legacy fallback.
- `tests/router_web_ui.test.mjs`: relay snapshot/replay, unknown-agent upsert, lower-version rejection, snapshot/live ordering, queued-child visibility, and clearing zero.
- `tests/router_event_log_retention.test.mjs`: lifecycle ledger/tombstone/producer retention, sweep logging, bounded diagnostics, and live-state preservation.
- `tests/subagent_lifecycle_rollout.test.mjs`: captured-command, backup, atomic-switchover, compatibility-window, and recovery-path fixtures.

Use fake clocks and temporary SQLite files. Include a direct router restart test that closes repository/router, reopens the same database, registers a reader, and proves the durable snapshot has the pre-restart child state. Test a duplicate start, finish, and snapshot with a lost acknowledgment: each has one ledger row/version, while duplicate, stale, conflict, and accepted-no-op outcomes have only their idempotent acknowledgment and bounded diagnostic. Test terminal-before-start and then a late same-epoch start: the tombstone wins. Test `queued` mapping to visible `running` until terminal or an accepted authoritative snapshot omits it.

Implement the exact test file and `node:test` names in `SPEC.md`'s deterministic acceptance matrix. Do not rename or consolidate them without updating that table. Add migration compatibility tests: old router code on a database migrated to 017 ignores the v2 tables and retains legacy behavior. Where the test harness can open a pre-017 database without auto-migrating it, a new router must expose a clear migration-017 v2 error and refuse v2 frames.

## Release 2: extension dual-send and retry journal

### 6. Upgrade the mailbox client

In `pi-extensions/mailbox-client.js`:

- capability-check with a short bounded timeout before enabling v2;
- retain the stable parent `instance_id`, `parent_session_id`, and epoch across ordinary socket reconnects and replacement leases within that process;
- expose correlated lifecycle send/ack support;
- retain a bounded in-memory retry journal keyed by event ID and stable client sequence: at most 256 items, 262,144 UTF-8 serialized-frame bytes total, and 65,536 bytes per item;
- retry unacknowledged frames after reconnect and remove only on matching router acknowledgment;
- at ten minutes from first enqueue, or before either cap would be exceeded, atomically replace all pending entries with one newest complete snapshot and record a bounded `journal_expired` or `journal_collapsed` diagnostic without descriptions;
- fail v2 closed when a frame or required replacement snapshot exceeds 65,536 bytes: do not send, truncate, or journal it; clear the journal, record `journal_oversize`, and wait for reconnect plus a valid complete snapshot;
- expose the current router version and a resync/replay request to the extension.

The journal charges the canonical UTF-8 v2 envelope, not an object-size estimate. Its replacement snapshot must fit both caps and is never partial. A new process or parent instance creates a new `instance_id`/`parent_session_id` identity and producer epoch, then sends its first full snapshot. That is the durable repair path. A reconnect of the same instance retains its epoch and retry identity. It must never write a prompt or full description to logs or an unbounded local file.

### 7. Upgrade Pi lifecycle production

In `pi-extensions/mecha-messaging.js`:

- create one opaque producer epoch for each parent process/instance and retain it across ordinary reconnects and replacement leases;
- send `agent_id`, `instance_id`, `parent_session_id`, the active lease, epoch, monotonic client sequence, and stable event ID with every v2 frame;
- assign a monotonic client sequence and stable event ID before placing a v2 item in the journal;
- keep current native-service reconciliation, parent-bound service selection, terminal classifications, and bounded local read diagnostics;
- map native `queued` and `running` records to visible v2 `running` state. Keep them visible until a terminal native state or an authoritative complete snapshot omits them;
- emit a complete snapshot on registration, v2 enablement, local start, terminal lifecycle event, service reconciliation change, reconnect, compaction resume, and heartbeat;
- retain snapshots while the parent is settled and children may run;
- dual-send the current legacy `sendActivity` frame while v2 is supported;
- use v2 as the local source for child data after enablement, but do not change parent active/settled/compacting behavior;
- stop treating a sent frame as success before its v2 acknowledgment arrives.

A complete snapshot has precedence over a collection of pending deltas when journal pressure occurs. Never derive a child description from a prompt, native error, or tool result.

### 8. Release 2 tests

Extend `tests/mailbox_v2_delivery.test.mjs` with controllable fake sockets/client clocks for:

- initial snapshot after registration;
- changes and settled heartbeats;
- native start/finish missed by events and repaired by poll/snapshot;
- duplicate, stale, conflict, and accepted-no-op acknowledgments with no ledger row or version;
- lost acknowledgment, reconnect retry, 256-item collapse, 262,144-byte collapse, ten-minute expiry replacement, and fail-closed handling for a 65,537-byte single item;
- same-instance epoch rebind after a replacement lease, old-socket write fencing, rejection of a changed epoch with the old identity, and stale epoch after a new process/instance;
- terminal-before-start tombstones, late-start rejection, terminal `steered`, and every other terminal status;
- native `queued` visible as running until terminal or accepted authoritative omission;
- description boundary/privacy tests;
- legacy-only router fallback and dual-send to a v2 router;
- the previous parent-bound service-handle regression.

Keep `tests/pi_extension_standalone.test.mjs` as a regression suite and run it unchanged unless an explicit v2 standalone contract requires a narrow addition.

## Release 3: live rollout and compatibility deadline

### 9. Switch the live router faithfully

Do this as soon as Releases 1 and 2 are compatible. Do not wait 14 days.

1. Inspect the running router process. Record the executable, full arguments, working directory, and service wrapper that started it. This is the replacement command; do not substitute a convenient development command.
2. Identify the live SQLite database. Make and verify a SQLite backup before stopping the router.
3. Use one controlled switchover: stop the old router, apply additive migration 017 in its SQLite transaction, and immediately start the updated router with the recorded live command. Do not let old and new routers write the database together.
4. If the migration or new start fails, restore the verified backup and start the prior router with its recorded command. Do not downgrade the new schema in place.
5. Check the restarted router's `GET /v1/capabilities`. It must advertise `subagent_lifecycle_v2` before clients enable v2.

### 10. Reload current sessions now

Install the canonical extension through the repository's supported installation path. Then reload or reconnect the important current Pi parent sessions, router relay, and browser UI immediately. For each reloaded parent, diagnostics must identify the current agent/instance as v2 after its complete snapshot. A reloaded session must not remain in the legacy-producer list.

Run a real end-to-end check on a reloaded parent: start a child, confirm the badge appears, finish it, confirm the badge clears, reconnect the parent socket, and confirm the durable snapshot restores the correct badge. Check relay/browser reconnection too.

Sessions that cannot be reached may use legacy fallback. Record their agent/instance and cutoff. This is the only reason to keep the fallback, and it expires no later than 14 days after the compatible router/UI release.

### 11. Remove child-state legacy authority at the deadline

At the deadline, in a separate small release:

- stop extension dual-send of legacy child count/summaries;
- keep legacy `activity` only for parent activity and compaction compatibility;
- make the router ignore legacy `subagent_count` and `subagents` for durable v2 child state;
- retain v2 snapshots/replay until all supported readers have completed the same compatibility maximum;
- remove legacy code and tests only after diagnostics show no supported legacy producers.

A stale session at cutoff receives a zero child count until it reloads. It is visible in diagnostics during the fallback period. Do not silently let stale legacy activity overwrite a v2 parent.

## Review and validation

Run focused tests first:

```sh
node --test tests/protocol.test.mjs tests/mailbox_v2_delivery.test.mjs tests/router_web_ui.test.mjs tests/router_event_log_retention.test.mjs tests/subagent_lifecycle_rollout.test.mjs tests/pi_extension_standalone.test.mjs
npm run check
npm test
```

Then run a bounded manual correlation test against an isolated router database:

1. Open a Pi parent and web UI relay on isolated ports.
2. Register the parent and capture its initial v2 snapshot/version.
3. Start a child, observe v2 update and badge, then finish it and observe tombstone/zero badge.
4. Restart only the isolated router and reconnect parent/UI. Confirm durable snapshot restores the correct state.
5. Simulate one lost acknowledgment and reconnect. Confirm the retry is duplicate-safe.
6. Capture versioned browser events in both orders: newer live update before stale snapshot, and snapshot before update.
7. Check router diagnostics and database test fixtures contain no description or prompt text beyond the allowed synthetic label.

Before any implementation handoff, review the final diff against `SPEC.md` and the acceptance matrix. Every changed file must map to one of the release steps above. Do not commit, push, rebase, or modify production code as part of this planning task.
