# Worker Goal Visibility and Wakeup Plan

This plan implements the approved `SPEC.md` in the smallest ordered seams. It preserves the unrelated WebUI worker changes already present in the worktree and does not change Pi settings or runtime panes.

## 1. Goal EventBus contract and TDD

**Files:**
- `tests/pi_extension_goal.test.mjs`
- `pi-extensions/goal.js`

**Order:** Add failing tests first, then implement the neutral cached/request-replay EventBus contract.

**Tests prove:**
- Goal restore/set/update/off emits a bounded `{ enabled, goal }` projection without requiring a subscriber.
- No subscriber does not throw or change persistence, evaluator scheduling, or existing command/tool results.
- Both extension load orders converge: a subscriber requesting state receives the current restored/set/off projection; a goal loaded after the subscriber publishes its restored state.
- Set/update persists before exactly one deferred self-wake; off emits no wake; unavailable/throwing self-wake leaves persisted state and does not invoke the evaluator or duplicate the wake.

**Implementation:** Keep `goal.js` authoritative for normalization and branch state. Add only a neutral Pi EventBus channel, cached projection, request/replay response, and the existing-session deferred `pi.sendMessage` wake after persistence. Do not import `mecha-messaging.js` or create a router client.

## 2. Mailbox activity transport

**Files:**
- `pi-extensions/mecha-messaging.js`
- `pi-extensions/mailbox-client.js` (only if the client frame contract requires the optional field)
- the existing Mailbox/messaging tests covering activity publication

**Order:** Run the new goal bridge tests against the current code, then add failing transport tests before implementation.

**Tests prove:**
- The messaging extension subscribes to the neutral goal EventBus channel and requests/replays current state.
- Once the bridge is active, every activity publication carries the cached enabled or explicit-off `goal` projection, while retaining `agent_id`, `instance_id`, `lease_id`, `activity_state`, `subagent_count`, and `subagents`.
- Set/update/off changes are published on the next activity update; the extension remains safe when no goal extension exists.

**Implementation:** Cache the latest optional goal projection in the messaging extension and attach it to the existing activity publication. Keep all existing lifecycle and reconnect behavior unchanged.

## 3. Router validation and projection

**Files:**
- `harness/router.js`
- `protocol/v1/schema.json` (if the protocol schema owns the activity frame)
- focused router tests, primarily `tests/router_web_ui.test.mjs` and the existing router contract test for activity frames

**Order:** Add protocol/router failing tests before changing normalization or projection.

**Tests prove:**
- Valid goal projections are stored as `agent.goal`, included in `/v1/agents`, and forwarded in `agent_activity` updates.
- Explicit `{ enabled: false, goal: "" }` clears the goal.
- An activity frame with `goal` absent preserves a valid projection for the current incarnation, but never creates one or carries a projection into a new/replaced incarnation.
- Unknown fields, non-boolean `enabled`, non-string goal, and goals over 4,000 characters are rejected/ignored without changing activity fields or clobbering a valid prior goal.
- Agent incarnation/disconnect replacement clears stale goal state.

**Implementation:** Extend the existing activity frame and agent snapshot projection with the optional validated field only. Preserve all existing lease, incarnation, activity, subagent, replay, and error semantics.

## 4. WebUI state, indicator, dialog, and color treatment

**Files:**
- `web/router-ui/app.js`
- `web/router-ui/index.html` (only if a dedicated dialog element is required)
- `web/router-ui/styles.css`
- No new `web/router-ui/goal.js` helper unless the existing app/reminder code proves one is genuinely necessary
- `tests/router_ui_goal.test.mjs` (new focused UI source/behavior tests)
- existing `tests/router_web_ui.test.mjs` where event-to-state behavior is covered

**Order:** Add failing state/control/dialog tests before implementation; keep the unrelated existing reminder changes untouched.

**Tests prove:**
- Initial snapshots and live activity updates replace per-agent goal state, including clear-on-off and replacement.
- Enabled goals render one compact accessible flag/goal control beside the reminder clock; absent/disabled goals render none.
- Click and Enter/Space activate the dialog without selecting the agent row; close/Escape works; worker identity and escaped goal text are shown.
- The existing reminder clock remains teal and the goal indicator/dialog accent is pink; colors are supplementary to non-color labels/symbols, remain readable against existing surfaces, and retain focus-visible/accessibility styling.

**Implementation:** Reuse the existing compact reminder indicator/dialog interaction pattern. Add a distinct goal symbol/label and per-agent dialog state. Render goal text through `textContent`/escaped markup, never raw HTML. Add the smallest CSS variables/classes needed for teal reminder and pink goal treatments.

## 5. Focused integration and regression evidence

**Files/commands:**
- `tests/pi_extension_goal.test.mjs`
- messaging/router/UI focused tests above
- `tests/router_web_ui.test.mjs`
- package test scripts from `package.json`

**Tests prove:**
- A goal set in the extension persists, emits the cached status projection, reaches the existing activity/snapshot path, and appears as the correct WebUI indicator/dialog state.
- With the bridge active, enabled and explicit-off projections are present on every activity publication; absent goal fields preserve only the current incarnation and never resurrect an old one.
- The set/update wake occurs once after persistence and does not directly invoke the evaluator.
- Existing reminders, activity/subagent indicators, goal evaluation, mailbox delivery, and unrelated router UI behavior regressions remain green.

Run focused tests first, then the relevant full test command. Record any unrelated full-suite limitation without changing unrelated worker files.

## Ordering dependencies and non-goals

- Event contract must be stable before messaging can transport it.
- Messaging payload must be stable before router validation/projection tests can pass.
- Router projection must be stable before WebUI state/control tests can exercise live events.
- Integration evidence follows all four seams.
- Non-goals: no new goal-setting WebUI control, no new router endpoint, no transcript inference, no settings/extension registration changes, no changes to evaluator semantics, and no edits to unrelated WebUI worker work.
