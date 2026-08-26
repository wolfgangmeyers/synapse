# Reminder lock control and WebUI parity

## Product decision

The router remains the sole owner of reminder records, CRUD, scheduling, one-shot and recurring behavior, delay, interval, next-due calculation, delivery, retry, the reminder CLI, and the HTTP API. The WebUI uses those router-owned APIs for reminder creation and editing. It does not create a second browser store or scheduler.

Creating a reminder always uses the existing normal POST flow. The create form has an optional default-off lock toggle, but POST carries no lock field or action. After an accepted POST, the WebUI sends a separate `set` control when requested. If that control fails, the reminder remains created and the UI reports the lock failure. Editing body and, when shown, subject uses the existing PATCH flow. A lock is a separate `set` or `clear` action on an existing reminder.

The selected-agent conversation header owns the reminder button, matching the existing goal flag placement. With no selected agent it is disabled. With no active reminders it uses a gray/muted clock and opens the create flow; with active reminders it opens the reminder list/editor. Each agent-list row has a display-only clock indicator when that agent has active, enabled reminders. The row indicator has no action. The header control remains keyboard accessible.

The target extension owns only an ephemeral, in-memory lock guard for native update and delete. The guard disappears on extension restart or reconnect and remains separate from reminder creation and router-owned reminder state. It is not database state, a migration, an import, a creation attribute, or router authority.

Router-backed WebUI/API create, update, and delete remain permitted while the target extension has a lock. Native update and delete are rejected only while the current target extension has the matching lock. Native create, list, and unrelated operations retain current behavior.

## Existing seams and lane boundary

Use these seams. Do not replace the existing reminder implementation:

- `protocol/v1/schema.json:331-424`: mirror the goal control frame definitions and `clientFrame`/`serverFrame` unions. New lock request, forwarded request, and result objects use `additionalProperties: false` and existing bounded identifiers.
- `harness/router.js:763-768`: `matchesSessionLease` is the source identity check. `harness/router.js:1061-1212` is the goal relay pattern for source role, exact target instance and lease, pending entries, forwarding, result checks, and correlation. `harness/router.js:1908-1915` is `sessionHasActiveLease`; `harness/router.js:1966-1982` is expiry and disconnect cleanup.
- `pi-extensions/mailbox-client.js`: receive and queue forwarded reminder-lock controls, invoke the direct callback, send correlated results, and reset local state on disconnect. `pi-extensions/mecha-messaging.js` creates the lock state and wires the callback and lifecycle reset; `pi-extensions/operational-tools.js` applies the native update/delete guard. Keep reminder lock state separate from goal state and session history.
- `harness/router.js`, `harness/db.js`, `pi-extensions/operational-tools.js`, and `web/router-ui/reminders.js`: retain router reminder ownership/read/CRUD, native `mecha_reminder` behavior, and countdown/recurrence rendering.
- `tools/router_web_ui.mjs`: `createBrowserClient`, the registered `user_interaction` session, configured lease renewal, `browser.on("message")`, router sends, pending maps, timeout, connection generation, and reconnect cleanup are the WebUI reminder-read and lock-relay seams. Add a narrow server relay for router-owned reminder POST/PATCH/DELETE only if direct browser requests cannot reach the selected router.
- `web/router-ui/app.js`: `sendCommand`, `handleEvent`, `state.reminders`, and reminder rendering are the browser seams. The browser reads rows from `GET /v1/reminders`, sends normal create/edit requests to the router-owned HTTP paths, and relays lock controls. It never stores, schedules, or locks reminders locally.

The router may read `repository.reminderFor(reminder_id)` to prove that the selected reminder belongs to `target_agent_id`. That lookup is an ownership check, not a lock lookup. The router must not persist, cache, infer, expose, or enforce lock state.

### Reminder-incarnation freshness fence

An item `GET /v1/reminders/:id` may return the ephemeral `x-reminder-incarnation` response header. The header is metadata for the live process-local freshness fence. It is not a lock field, reminder state, persistence, schema or migration, or public JSON field. The router generates one value for each live reminder row, preserves it across `PATCH`, and replaces it after delete/recreate, including when the ID is reused. Missing reminders do not receive the header.

The target extension records the header value with the process-local lock entry and compares it during the native update/delete guard alongside the approved reminder identity tuple: `id`, `creator`, and `created_at`. A mismatch discards the stale entry and fails open to the existing native operation. If the header is unavailable in a test or other direct in-process fixture, the extension may use the captured reminder object reference only as a continuity fallback; that reference is not a second persistence or authority mechanism.

## Lock control contract

The browser sends only the target agent, target instance, reminder ID, action, and unique control ID:

```json
{
  "type": "reminder_lock_control",
  "controlId": "<unique-control>",
  "targetAgentId": "<target-agent>",
  "targetInstanceId": "<target-instance>",
  "reminderId": "<reminder-id>",
  "action": "set"
}
```

The WebUI server supplies `agent_id`, `instance_id`, and `lease_id` from its current router session. Browser-supplied source identity is ignored. The server session must have role exactly `user_interaction`.

The normalized v1 request carries the server-bound source identity, target identity, `control_id`, `reminder_id`, and `action`. The forwarded target request adds the target's current lease and `expires_at`. The target result carries target agent, instance, lease, `source_agent_id`, control ID, reminder ID, `accepted`, and a stable `error_code` when rejected. The router forwards the result without interpreting lock state.

The router validates in this order:

1. `matchesSessionLease` proves the live source agent, instance, and lease. The source role is exactly `user_interaction`.
2. `reminderFor(reminder_id)` returns a current row whose `target` equals `target_agent_id`.
3. `sessions.get(target_agent_id)` is open, has the requested `target_instance_id`, and passes `sessionHasActiveLease`.
4. The router records the exact source and target agent/instance/lease tuple and forwards only to that target session.

There is no cross-target lock, unlock, or status operation. Missing reminders, wrong owners, wrong instances, stale leases, malformed frames, oversized frames, disconnected sessions, send failures, expired controls, and duplicate controls fail without changing reminder data or extension lock state.

Bounds and fencing mirror the goal/model control limits:

- v1 frame, control, instance, and lease IDs use the existing 1-128-character frame ID bounds. Agent IDs use the existing protocol agent bounds. Reminder IDs use `assertReminderId`: 1-99 characters and `^[A-Za-z0-9][A-Za-z0-9._-]{0,98}$`.
- `action` is exactly `set` or `clear`. Lock request, forwarded request, and result schemas reject extra properties.
- Use the existing 64 KiB WebSocket frame cap, 30-second pending TTL, 256 total pending controls, 16 per source, and WebUI pending-control cap and timeout. No reminder body travels in a lock frame.
- A control ID is unique per source agent while pending. A result must match the pending source agent, target agent, target instance, target lease, control ID, and reminder ID. Consume the pending entry before forwarding the result so delayed and duplicate results cannot apply twice.
- Check expiry immediately before the target mutates its in-memory map. The router also removes expired pending entries before accepting a result.

## Target extension behavior

Maintain a process-local map keyed by reminder ID. Each entry contains the target agent, the approved identity tuple (`id`, `creator`, and `created_at`), and the optional reminder-incarnation value from the item `GET` header. Check the target separately so a target change invalidates the lock; do not add a lock field or lock record to the router. `set` and `clear` are idempotent.

The native adapter in `pi-extensions/operational-tools.js` checks the map immediately before its existing router-backed update or delete. It reads the current reminder through the existing path and requires the current target, approved identity tuple, and reminder-incarnation value to match. A matching entry returns `reminder_locked` without calling PATCH or DELETE. If the row is absent, the target changed, the identity tuple changed, the incarnation changed, or continuity cannot be proved, remove the stale entry and allow the existing native CRUD call. This is fail-open for continuity uncertainty; an old lock must never block a different reminder. A direct captured-object reference is permitted only as the test/in-process fallback when the metadata header is unavailable.

A router/WebUI content, schedule, or same-target update keeps the same reminder identity and incarnation and does not clear the lock. A target update invalidates the old target's entry; the new target has no lock until a separate `set`. Stale entries are discarded lazily during the next native guard/read, so cleanup is not necessarily immediate. Router/WebUI delete remains allowed and removes the row. Delete followed by recreate with the same reminder ID may retain the approved identity tuple when the caller supplies the same `id`, `creator`, and `created_at`, but the new row always receives a new ephemeral incarnation, so the combined fence differs. The old entry is discarded on that next guard/read and the new reminder is not blocked.

Clear all entries when the extension process or connection lifecycle restarts. Never reconstruct them from reminder rows, schedule, delivery, retry, caller identity, or router replay.

## Lifecycle, reconnect, and result handling

Use goal control's exact source/target validation, role check, lease check, pending bounds, control ID uniqueness, expiry, disconnect cleanup, connection-generation fencing, and result correlation. Apply the lifecycle distinction required by ephemeral locks:

- A WebUI reconnect gets a new source instance and lease. Reject its old pending controls and do not replay them.
- A target disconnect or lease expiry removes pending relay entries, reports the existing target/lease failure to a live source, and clears that extension's in-memory lock map. A reconnect requires a fresh `set` or `clear`.
- Goal control can replay durable desired goal state for the same process instance. Reminder lock controls must not be persisted or replayed. No reconnect path restores a lock.
- The target checks its current agent/instance/lease and `expires_at` immediately before changing the map. The router and WebUI discard results from an old source connection, target session, lease, or connection generation.
- Router restart clears pending relay entries. Extension restart/reconnect clears only extension lock entries. A stale result cannot affect a replacement instance.

## WebUI behavior

Keep the router-backed reminder UI and add normal CRUD plus independent lock controls:

- The selected-agent conversation header has a reminder icon button beside the goal flag. With active reminders it opens the reminder list. With none it is muted gray and opens a simple create form. It is disabled when no agent is selected. Agent-list rows show a display-only clock indicator for active, enabled reminders; the indicator does not open or change anything.
- The create form has an optional default-off lock toggle. It submits a normal one-shot or recurring reminder to `POST /v1/reminders`, including the router's existing target, body, optional subject, `created_at`, `next_due_at`/delay, and `interval_ms` contract. POST carries no lock field or action. An accepted POST is followed by a separate `set` control when selected; a failed control leaves the reminder created and reports the failure.
- Existing reminders can edit body and optional subject through `PATCH /v1/reminders/:id`. The WebUI preserves router responses and errors, shows pending state, and leaves schedule fields under the existing router contract.
- Render current router rows, including one-shot/recurring labels and `next_due_at` countdowns through `formatReminderCountdown` and `reminderRecurrenceLabel`. The browser does not schedule, persist, or synthesize reminder state.
- Add set/clear controls to each existing reminder. They call only the lock relay and leave the reminder visible. Native ephemeral locks never gate WebUI POST, PATCH, or DELETE.
- Show plain pending, empty, validation, not-found, and request-error states. All controls are real buttons or form controls with labels and keyboard access.

## Explicit non-goals

- No extension reminder store, database, scheduler, occurrence history, import, cutover, or authority handoff.
- No lock persistence, migration, lock column, lock table, router lock map, router lock policy, CRUD enforcement, scheduler behavior, delivery behavior, retry behavior, or lock field in reminder responses. The `x-reminder-incarnation` header is ephemeral freshness metadata only; it is not a public JSON field or persisted reminder data.
- No removal, quarantine, or restriction of existing HTTP, CLI, WebUI, native router-backed, create, list, update, or delete paths. The WebUI explicitly adds normal POST creation and PATCH text editing through those existing paths.
- No new permission, role, or access-control type. `user_interaction` remains the existing WebUI boundary.
- No change to normal reminder creation semantics, one-shot/recurring behavior, delay, interval, next-due, delivery, retry, message, agent, mailbox, or unrelated UI behavior.

## Acceptance tests and proof

Use focused cases in the existing reminder/control suites: `tests/router_ui_reminders.test.mjs`, `tests/router_reminder_patch.test.mjs`, and `tests/operational_native_tools.test.mjs`, with protocol/router/client coverage beside the existing goal-control tests.

Acceptance must prove:

1. Separate WebUI set and clear use server-bound `user_interaction` identity, exact target agent/instance/lease, bounded frames, unique control IDs, expiry, disconnect cleanup, delayed-result fencing, and correlated results.
2. The selected-agent conversation header has one keyboard-accessible reminder button beside the goal flag; it is disabled with no selected agent, muted gray with no active reminders, and opens create/list as appropriate. Agent-list rows show a display-only clock for active, enabled reminders and provide no reminder action.
3. Normal one-shot and recurring WebUI creation has a default-off optional lock toggle, sends the existing POST payload without a lock action or lock field, and retains current success, validation, conflict, not-found, and error behavior. An accepted POST is followed by a separate `set`; a failed `set` leaves the reminder created and reports failure.
4. WebUI text editing sends body and optional subject through existing PATCH, retains pending/error handling, and succeeds while the target extension lock is set. Delete remains available through existing DELETE. Cover delay, interval, `next_due_at`, recurring rearm, scheduler, delivery, retry, CLI, and HTTP paths.
5. Existing router-backed WebUI/API POST, PATCH, and DELETE paths succeed while the target extension lock is set. HTTP/WebUI API PATCH and DELETE bypass the native guard. This includes content, schedule, and target updates and delete/recreate with the same ID.
6. Native update/delete return `reminder_locked` only for the current matching lock. Clear releases both. Native create, list, and unrelated operations remain unchanged.
7. Target changes and same-ID delete/recreate discard stale entries. A new target or new row is not blocked by an old entry.
8. Extension restart and reconnect clear the lock. Old source/target leases, pending controls, and delayed results cannot mutate the new lifecycle.
9. Router reminder rows, CRUD responses, scheduler, delivery, retry, and CLI contain no lock state. Item `GET` may carry only the ephemeral `x-reminder-incarnation` freshness header: it remains stable across `PATCH` and changes after delete/recreate, while never appearing in public JSON. No lock column/table/migration or router lock map exists. This proves the lock lives only in target-extension memory.
