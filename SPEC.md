# Worker Goal Visibility and Wakeup

## Behavior

- The WebUI agent list shows a distinct goal indicator for each worker that currently has an enabled goal, alongside the existing reminder indicator.
- The indicator is a compact per-agent control in the agent name line, uses a flag/goal symbol with an accessible label, and is keyboard-activatable with Enter or Space. Activation opens the existing native dialog pattern, showing the worker identity and current goal text; Escape/close returns focus without selecting the row.
- The existing reminder clock remains teal, and the new goal indicator is pink. Color is supplementary only: accessible labels, symbols, focus-visible styling, and dialog text remain understandable without color, and both treatments meet the existing surface contrast requirements.
- Workers without an enabled goal do not show the indicator. Goal text is never rendered as HTML.
- Goal state is refreshed from the router's live agent state. Initial snapshots, live updates, off transitions, and worker replacement clear stale goal text; an update for a new incarnation cannot inherit the previous worker's goal.
- Setting or updating a goal persists the normalized goal first, then makes exactly one best-effort self-wakeup through the existing deferred `pi.sendMessage` wake mechanism with the current session as target. The wakeup tells the worker the goal was set and prompts it to begin work. `/goal off` does not self-wake. A set/update wakeup does not directly invoke the evaluator or create a second wakeup.
- Existing `/goal status`, `/goal off`, goal evaluation, reminder behavior, mailbox delivery, and WebUI interactions remain unchanged outside this addition.

## Verified transport and ownership

The existing worker status transport is the Mailbox v2 activity frame emitted by `pi-extensions/mailbox-client.js::activity(activityState, { subagentCount, subagents })`. Its current payload is:

```json
{
  "type": "activity",
  "agent_id": "<worker>",
  "instance_id": "<incarnation>",
  "lease_id": "<lease>",
  "activity_state": "active|settled|compacting",
  "subagent_count": 0,
  "subagents": []
}
```

The router projects this to the WebUI's `agent_activity` event with `agent`, `activityState`, `subagentCount`, and `subagents`, and includes the corresponding agent object in the initial `/v1/agents` snapshot. No existing goal channel or goal payload was found. The implementation adds one optional `goal` projection to the router's agent record and to activity updates:

```json
{ "goal": { "enabled": true, "goal": "<text>" } }
```

The `goal` key is owned by the goal extension; `mecha-messaging` only transports the latest projection. Disabled state is explicit and always clears text: `{ "goal": { "enabled": false, "goal": "" } }`. The router forwards this field in both the activity update and `/v1/agents` snapshot; the WebUI replaces the stored projection rather than merging stale goal text.

The neutral Pi EventBus (`pi.events.on(channel, handler)` / `pi.events.emit(channel, data)`) is the optional bridge. `goal.js` does not import or call `mecha-messaging.js`, access settings, or create a second router connection. `mecha-messaging.js` subscribes to the goal-state channel and adds the projection to its next activity frame. If Mailbox is absent or disconnected, the bridge is inert and goal lifecycle/evaluation/wakeup behavior still works unchanged.

## Standalone, bootstrap, and ordering invariants

- `goal.js` is fully functional with `mecha-messaging` absent: branch-local persistence, `/goal` commands/tool, evaluator scheduling, owner suppression, and self-wakeup behavior do not depend on a subscriber, a router, or settings. Emitting the optional status event with no listener is a no-op and must not throw.
- The bridge uses a bounded in-process cache and request/replay handshake. The goal extension maintains its current normalized projection and listens for a neutral state-request event. On goal extension initialization it restores branch state and emits the current projection. A Mailbox subscriber requests the current projection immediately after subscribing; the goal extension replies with the cached projection. On every set/update/off/restore transition, the goal extension updates the cache before emitting. Thus a restored or set goal cannot disappear merely because either extension loaded first, and a newly attached subscriber receives the current enabled or explicit off state.
- The handshake is source-level only: it does not alter Pi settings, extension registration, router configuration, or Mailbox connection behavior. It is best-effort UI publication; no UI state is treated as authoritative.
- The goal set/update wake uses the existing deferred `pi.sendMessage` path with `{ deliverAs: "steer", triggerTurn: true }` and the current session as the implicit target. The message is scheduled after persistence; one set/update schedules one wake. If the current session is unavailable or `sendMessage` throws, the persisted goal remains and the command/tool reports the existing operation result; no evaluator call or retry loop is created. `/goal off` never schedules this wake.

## Data validation and incarnation rules

- The source of truth remains the branch-local `goal-state` entry in `goal.js`.
- The published projection is either enabled with normalized text or disabled with an empty string; arbitrary fields are ignored.
- Goal text uses the existing `normalizeGoal` contract: string input, trim, and a maximum of 4,000 characters. The activity/client/router boundary accepts only an object with boolean `enabled` and string `goal` of at most 4,000 characters; unknown fields or malformed/oversized goal objects are rejected/ignored as a goal update without altering `activity_state`, `subagent_count`, or `subagents`.
- The optional projection is stored as `agent.goal` in the router's agent record and carried as `goal` on activity updates. The goal extension owns its value; Mailbox owns transport; router owns validation and replacement semantics; WebUI owns presentation.
- Every activity update carries the current `instance_id` through the router's existing lease/incarnation handling. A snapshot or update that replaces an agent must replace the full `agent.goal` projection; off, disconnect, and replacement clear it. Invalid goal input must not clobber a valid existing projection unless a valid explicit off projection arrives.

## High-level design

`goal.js` remains authoritative for persistence, normalization, evaluator lifecycle, and set/update/off behavior. It emits a neutral cached/request-replay Pi EventBus goal-state notification without importing the mailbox extension. `mecha-messaging.js` subscribes to that optional notification, retains the latest projection, and translates it into the optional activity payload without changing its existing lifecycle fields. The router validates/forwards the optional projection with existing agent snapshots and activity updates. The WebUI stores the projection per agent, renders the compact accessible indicator/dialog, and replaces state on each snapshot/update.

## Alternatives considered

1. **Infer goals from conversation history.** This is ambiguous, stale, cannot reliably represent `/goal off`, and exposes more transcript data than the UI needs.
2. **Poll each worker directly from the WebUI.** This adds a new per-worker request path, polling load, and race windows, and duplicates the router's existing snapshot/update lifecycle.
3. **Create a separate goal WebSocket/API channel.** This adds another protocol and lifecycle to keep consistent with worker presence and incarnation state.
4. **Publish a bounded optional projection through the existing activity path via a cached/request-replay neutral Pi EventBus (chosen).** This reuses the verified snapshot/update transport, supports initial load and live changes, keeps `goal.js` independent of Mailbox implementation, and preserves standalone behavior when no subscriber exists.

The chosen design keeps the extension authoritative while letting the existing Mailbox/router path remain the sole worker-status transport.

## Acceptance tests

- Goal extension standalone: with no EventBus subscriber, restore/set/update/off, evaluator scheduling, and self-wakeup behave as before; the optional emit does not throw or alter results.
- Bootstrap/order: goal loaded before Mailbox, Mailbox loaded before goal, and a new subscriber after a restored/set/off state all converge to the current enabled or explicit off projection through cache plus request/replay; no state is lost between restore/set and subscription.
- Goal extension wake: persistence is observable before one deferred `pi.sendMessage` call; set and update each produce exactly one self-wake, off produces none; self-wake failure leaves persisted state and produces no evaluator/duplicate wake.
- Mailbox transport/router: goal projection is attached to activity frames, preserved in `agent_activity`, stored as `agent.goal` in `/v1/agents`, and cleared on valid off, disconnect, or replacement without changing existing activity fields; malformed, unknown, or oversized goal fields are ignored/rejected without clobbering valid goal or activity fields.
- WebUI: initial goal projection renders the indicator; missing/disabled projection renders none; live set/update/off/replacement updates replace the row state; clicking and Enter/Space open the dialog with escaped goal text and accessible labeling; close/Escape works without selecting the row.
- Regression: existing goal, mailbox activity, router snapshot/update, reminder, and WebUI tests continue to pass.
