# Task 250: Android Goal-Control Parity

## Target

Android must expose the same goal controls as the WebUI at local `origin/main` commit `999e2e0518bf9f66f56ae32287ae147b5bb17443`. Its full repository tree is `d8f671d1ed3e4b1a543b3efe4d95d39a0349bae3`; the WebUI subtree used for parity is `5d8e535c04951f393cc56fe0b340d95f2117d67a`. That WebUI is the source of truth for goal-control states, labels, actions, validation, and result handling.

There is intentional WebUI source drift: Android `HEAD` `3deb7bd15206c0456cdb58719a525a4027d6dfd5` has WebUI tree `37b9b21fe76cb2638bec4b5929d7c343c629607b`, while `origin/main` has the editable source-target WebUI subtree `5d8e535c04951f393cc56fe0b340d95f2117d67a`. HEAD contains the older read-only goal dialog. Use `origin/main` for parity only. Leave all WebUI files untouched.

The Android layout may use Android controls and adapt to narrow screens. It must not change the meaning or remove a WebUI control.

## Supervisor acceptance criteria

- Preserve PR #28 exactly as found: retain its current ancestry and head. The worker must not rebase, force-push, rewrite history, change the base, or change draft/ready state. The worker must not commit or push.
- Keep the documented `origin/main` WebUI drift above. The exact error strings and category mapping come from that `origin/main` target, not Android `HEAD` or a paraphrase.
- Implement the Android-only orbiting border animation in this lane. Do not change WebUI, router, or protocol files.

## Current gap

Android receives the compact `goal` projection and shows an enabled-goal flag in a worker row. Its flag opens a read-only dialog. The Android conversation header has no goal button. It does not consume `agent_goal_state` or `goal_control_result`, send `goal_control`, retain a pending goal request, or expose the full goal editor.

Android already renders activity, compacting, and subagent status. Keep those indicators and their state wiring unchanged. The working marker remains a solid teal 22dp marker in its existing slot; the new visual is a separate border animation.

## Required behavior

### Goal entry points

- The selected worker's Android chat header has one icon-only flag. With no enabled goal it is muted and its accessible label is `Set goal for <worker>`. With an enabled goal it is pink and its label is `Edit goal for <worker>`. No selected worker leaves it disabled with `Select an agent to set a goal`.
- An enabled worker row has the pink flag with the accessible label and title equivalent `Show goal for <worker>` / `Show goal`. It opens the same editor. A disabled or absent goal has no row flag.
- Tapping any goal flag must not also select a worker, open a conversation, or invoke a neighboring control.

### Editor

The Android editor has the same title, labels, values, and actions as the WebUI:

- `Goal` ON/OFF control.
- `What should this agent work toward?` Markdown text input. It keeps the entered text, has a 4,000-character limit, and is disabled while Goal is OFF.
- `Blocking` ON/OFF control.
- `Goal lock` lock/unlock control.
- `Snooze settings`: `Allow snoozing`; `Snooze duration (seconds)`; `Snooze turn limit (optional)` with a maximum of 20; `Snooze budget (minutes, optional)`; `Snooze refill yield (seconds)`; and `Snooze refill time (seconds)`.
- The current `Outcomes: blocked …, completed …, not done …, waiting …` line.
- `Cancel` and `Save` actions. The actions remain usable after the form scrolls on a small screen.

`Cancel` closes without sending or changing confirmed state. Saving Goal OFF sends the typed `off` action and an empty goal. It is not a separate destructive action. Saving Goal ON requires nonblank trimmed goal text. Android must display the WebUI's user-facing messages for unavailable controls, unsupported blocking changes, invalid settings or goal, a timed-out control, a disconnected router, an unready target, and a rejected save. It shows `Saving…` while its own request is pending.

### Router state and control contract

The current router protocol is already the contract. Android must use it directly, with no WebUI relay or new endpoint:

- Read the compact `goal` projection from `/v1/agents` and `agent_activity` for worker-list state.
- Read full `goal_state` from `agent_goal_state` and `goal_control_result` for editor state and outcome counts.
- Send a v1 `goal_control` frame using Android's active user-interaction lease, the selected target's current agent and instance IDs, a fresh control ID, the typed `set` or `off` action, and every existing settings field.
- Accept a result only when all correlation fences match the pending Android request: the result `source_agent_id` equals Android's active `agentId`; the result `agent_id` and `instance_id` equal the captured target; the control ID matches; Android still has its active lease/session; and the result arrived on the current socket and connection generation. A replacement worker, reconnect, router switch, selection change, timeout, lease/session loss, or dialog close clears the pending request. Stale or replaced results cannot mutate confirmed state, compact state, draft state, or editor visibility.
- On an accepted result, replace the target's full goal state, update its compact projection, close the editor, and let the row/header reflect the new state. On rejection, retain the draft and show the mapped error.
- Preserve current instance fencing. A new worker incarnation must not inherit a previous full goal state, draft result, activity state, subagent list, model state, or goal flag.

This feature requires a router that advertises `goal_control_v1` and a target worker with the existing goal-control support. Android sends no fallback command when either side lacks that support.

### Activity indicators and border animation

Keep the existing working, compacting, and subagent-count markers, including expanded subagent items, in the connected-worker list and chat header. Preserve their colors, labels, tap behavior, state meanings, and working-state wiring. The working marker remains a solid teal 22dp marker in its existing 26dp slot. Remove the working marker's radial pulse.

When an agent is working, draw a separate roughly quarter-perimeter line segment around the agent list/card border and conversation header border. Move it continuously around the perimeter and fade both ends so it reads as a snake. The border must not replace or resize the old marker. It is active only for the existing working state.

## High-level design

Keep goal state and control ownership in `MainActivity.kt`, where Android already owns router frames, worker projections, dialog state, and model-control correlation.

Add a validated full Android goal-state model alongside the existing compact `GoalProjection`. Normalize full state exactly enough to reject malformed values without erasing a valid state. Use the compact projection for list presence. Use the full state when available, otherwise construct the same WebUI defaults around the compact projection for the editor.

Add a single Compose goal editor state for both entry points. It holds a local draft, target worker and instance, pending control identity, status text, and timeout. It sends the existing WebSocket frame only after the final lease, socket, target, and instance check succeeds. Android's existing model-control fencing is the pattern for correlation and reconnect cleanup.

Render the new working border through one reusable Compose wrapper. Keep marker call sites separate so the wrapper cannot replace, resize, or change their semantics.

## Alternatives considered

1. Keep Android's read-only goal dialog. It cannot set, edit, turn off, lock, block, or configure snoozing.
2. Send slash commands as mailbox messages. This bypasses the router's typed control protocol, desired-state replay, target-instance fence, and accepted-result contract.
3. Add an Android-specific REST endpoint. The router already has a leased WebSocket control path. A second path would split validation and result behavior.
4. Copy Web HTML or use a WebView. It would not match Android interaction patterns and would add a second client runtime.
5. Replace the old working marker with the border animation. This loses the established marker and its placement.
6. Change WebUI, router, protocol, or working-state transport. The border is an Android visual addition only.

Chosen approach: native Compose controls over the existing WebSocket contract, plus one reusable Android border-animation wrapper alongside the existing markers.

## Non-goals

- No WebUI, router, protocol, Pi extension, migration, permission, or branch-history change.
- No WebUI, router, protocol, or working-state transport change for the border animation.
- No new goal setting, evaluation, wakeup, blocking, snooze, or lock semantics.
- No new Android role, router endpoint, local credential, or test-only production backdoor.
- No redesign of the worker list, conversation page, reminders, model picker, or activity-state transport.
- No committed screenshots, placeholder evidence, or claims that an emulator run occurred before it does.
- No physical-device rollout. The existing API-35 emulator target is sufficient for this task.

## Immutable target assumptions

- `origin/main` at the commit and tree named above is the only WebUI comparison target. If the ref moves, re-read the new commit and revise this spec before implementation.
- The documented HEAD-to-`origin/main` WebUI drift is expected. It is comparison input only. Web files stay untouched.
- The current target owns `goal_control`, `goal_control_result`, `agent_goal_state`, `goal_control_v1`, and `goal_control_blocking_v1`. Task 250 consumes those contracts.
- The target worker remains connected under the exact instance ID shown in discovery while a control is pending.
- Goal text remains bounded to 4,000 characters. Existing positive-integer and optional-null snooze limits remain authoritative.
- Before E2E, freshly verify live router ownership: the listener process, working directory, repository HEAD, and migration state through `019_goal_control_desired_state`. Do not call it a disposable router without that evidence.
- The real emulator uses the documented Pixel 8 API-35 AVD, OpenJDK 17, and ADB reverse to the verified loopback router. Screenshot review covers the active WebUI-parity indicator.

## Required validation and final guard

- Error coverage must assert the exact, user-visible `origin/main` WebUI source strings and their Android mapping for unavailable controls, rejected saves, control timeout, router disconnect, and validation errors (blank/invalid goal and invalid settings). Tests must name the source literal for every category. Broad assertions such as "shows an error" do not meet this requirement.
- Run the focused Android coverage command: `JAVA_HOME=/opt/homebrew/opt/openjdk@17 ./gradlew :app:testDebugUnitTest :app:jacocoDebugUnitTestReport`. Tests must pass. Changed goal/state production lines need meaningful coverage with no blocking gaps; report uncovered changed branches and either cover them or record why they are unreachable.
- Emulator evidence must record the AVD name, emulator process and device serial, installed application package and process, and the exact `adb reverse` mapping. It must also prove ownership of that runtime. Do not stop, reuse, alter, or claim another task's emulator or app runtime.
- Before handoff, inspect the final Android-only diff for security and unrelated changes: `AndroidManifest.xml` exported components; cleartext/network configuration and permissions; goal text or identifiers in logs; and dependency changes. There must be no unrelated change or security regression in these areas.
