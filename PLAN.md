# Task 250: Android Goal-Control Parity Plan

## Baseline

PR #28 is inspection-only for this worker: preserve its current ancestry and head. Do not rebase, force-push, rewrite history, change its base, or change draft/ready state. Do not commit or push. The Android-only border animation is now in scope; WebUI and router files remain untouched.

Implement against WebUI at `origin/main` commit `999e2e0518bf9f66f56ae32287ae147b5bb17443`. Its full repository tree is `d8f671d1ed3e4b1a543b3efe4d95d39a0349bae3`; the WebUI subtree used for parity is `5d8e535c04951f393cc56fe0b340d95f2117d67a`. There is a WebUI diff: HEAD `3deb7bd15206c0456cdb58719a525a4027d6dfd5` has the older read-only-dialog WebUI tree `37b9b21fe76cb2638bec4b5929d7c343c629607b`; `origin/main` has the editable source-target WebUI subtree `5d8e535c04951f393cc56fe0b340d95f2117d67a`. Use `origin/main` for parity only. Do not edit `web/router-ui/**` or any other WebUI file.

## 1. Lock the behavior contract first

Read the source target before implementation:

- `web/router-ui/app.js`: header and row flags, editor draft defaults, typed set/off frame, pending result handling, error strings, and instance checks.

Create a literal error mapping table from this exact `origin/main` source. It must quote and test each source string for unavailable controls, rejected saves, timeout, disconnect, blank/invalid goal, and invalid settings. Android may use the same string or a documented equivalent only where the WebUI maps the category differently; each test must identify the originating WebUI literal. Do not substitute category-only assertions or Android `HEAD` strings.
- `web/router-ui/index.html` and `web/router-ui/styles.css`: exact labels and control hierarchy.
- `protocol/v1/schema.json` and `harness/router.js`: current goal-control frame and result contract.

Record the target commit and tree in the implementation evidence. Stop if `origin/main` changed. The documented WebUI drift is expected; WebUI files remain untouched.

## 2. Add Android goal-control state and transport

**Change:** `app/src/main/java/life/meyers/mecha/MainActivity.kt`

- Add a full validated goal-state model and WebUI-equivalent defaults for a compact goal projection that has no full state yet.
- Handle `agent_goal_state` and `goal_control_result` alongside the existing activity and model-control frames.
- Add pending goal-control ID, target worker/instance, draft, status, timeout, and reconnect/router-switch/selection cleanup.
- Send the existing v1 `goal_control` frame only under the current socket, lease, target worker, and target-instance fence. Include every settings field.
- Match results to the pending request before mutating UI state. Acceptance requires every fence: `source_agent_id ==` Android's active `agentId`; result `agent_id` and `instance_id ==` the captured target; control ID match; an active Android lease/session; and the current socket plus connection generation. Reconnects, router switches, lease/session loss, replacement instances, selection changes, timeouts, and dialog closes clear pending state. Stale or replaced results cannot change confirmed state, compact projection, draft, or editor visibility. Accepted results replace the worker state and close the editor. Rejected or timed-out results retain the draft and show the mapped WebUI message.
- Preserve current discovery and live-update rules. A replacement instance clears all runtime state before an incoming projection can populate it.

## 3. Replace the read-only dialog with the shared editor

**Change:** `app/src/main/java/life/meyers/mecha/MainActivity.kt`

- Put the icon-only goal control in the selected-worker chat header. Match the WebUI's muted `Set goal for <worker>`, pink `Edit goal for <worker>`, and no-selection disabled labels.
- Change an enabled row flag from read-only viewing to the same editor. Keep its `Show goal` semantics. Do not make it select the row.
- Render the full WebUI control set, labels, defaults, outcome counts, 4,000-character bound, input rules, Cancel, Save, and `Saving…` state.
- Keep Cancel local. Save with Goal OFF sends typed `off` and empty text. Save with Goal ON rejects blank trimmed text before sending.
- Use a scrollable form with pinned actions on narrow screens. Keep accessible content descriptions and 48dp touch targets.

## 4. Preserve markers and add the working border animation

**Change:** `app/src/main/java/life/meyers/mecha/MainActivity.kt`

- Keep the existing working, compacting, and subagent markers and all state wiring.
- Keep the working marker as a solid teal 22dp marker in its established 26dp slot. Remove the working marker's radial pulse.
- Add one reusable Compose border wrapper for working agent cards and the conversation header. Draw a continuously moving roughly quarter-perimeter segment with both ends faded.
- Keep the border animation separate from the marker. It must not replace or resize the marker, change colors/semantics, or alter 48dp controls.
- Use the existing `peer.active` and `peerStatus.active` values. Do not change activity transport or state meanings.

## 5. Test before and after each seam

**Change:** `app/src/test/java/life/meyers/mecha/GoalControlTest.kt` (new)

Cover normalization, compact-to-full defaults, set/off validation, full settings serialization, timeout cleanup, and replacement-instance clearing. Assert the literal `origin/main` WebUI error string and Android mapping for unavailable, rejected, timeout, disconnect, blank/invalid goal, and invalid settings. Exercise each correlation fence independently: Android `source_agent_id`, target `agent_id`, target `instance_id`, control ID, active lease/session, socket, and connection generation. Prove stale and replaced results cannot mutate any goal state or editor state.

**Change:** `app/src/test/java/life/meyers/mecha/AgentFilterTest.kt`

Extend the existing projection and instance tests for full goal state, live goal updates, and no stale goal inheritance.

**Change:** `tests/android_qol_parity.test.mjs`

Add source-level guards for the two entry points, the shared editor, all required labels/actions, the goal-control frame fields, and the WebUI-parity indicator call sites.

**Change:** `tests/android_router_contract.test.mjs`

This file retains its existing router behavior tests and adds a pinned `origin/main` schema/field contract test for the Android-role goal-control frames and matched result/state fields. The added pinned test does not drive `createAgentRouter` or prove live Android-role router behavior because this branch intentionally cannot change its older protocol/router production files. The recorded live correlation in `/tmp/task-250-evidence.md` covers the accepted Android set flow.

### Plan correction: emulator evidence

Real Android set acceptance was correlated with the router. The editor labels, scrolling, and Cancel behavior were run on the real emulator. Real off/restore was intentionally not run because the supervisor required preserving Wolfgang’s directed live goal; this handoff makes no off-flow claim. `connectedDebugAndroidTest` is unsafe on the shared emulator because AGP UTP unconditionally uses `uninstall_after_test: true`, which uninstalls the shared app and its data. `GoalControlEmulatorTest.kt` and Gradle instrumentation dependencies are intentionally absent to preserve that shared runtime. A future isolated emulator or non-destructive runner configuration is required before adding them back.

Coverage exception: focused JaCoCo covers pure happy-path helpers and key fences: core normalization/defaults, serialization, replacement, hydration, and error mappings. It does not JVM-cover malformed or alternate goal-normalization branches, settlement-rejection branches, or Compose/live WebSocket/capability callbacks. Compose and live paths have manual real-emulator evidence. Instrumentation is omitted because AGP UTP uninstalls the shared app and its data.

## 6. Required evidence

Run focused Kotlin and Node tests. Then run the full relevant Gradle and Node suites, static checks, and `git diff --check`.

The required focused goal/state coverage command is:

`JAVA_HOME=/opt/homebrew/opt/openjdk@17 ./gradlew :app:testDebugUnitTest :app:jacocoDebugUnitTestReport`

It must pass. Changed goal/state production lines need meaningful coverage with no blocking gaps. Inspect the focused JaCoCo result and cover changed branches, or document why a changed branch is unreachable. The coverage exception above applies to stateful runtime callbacks; focused JaCoCo covers the listed pure helpers, and accepted live behavior is evidenced through emulator/router E2E.

For the live acceptance run:

1. The live router at `127.0.0.1:8765` is healthy and has migrations through `019_goal_control_desired_state`. It likely belongs to `/Users/wolfgang/code/mecha-developer`. Before E2E, freshly verify listener ownership, process working directory, repository HEAD, and migration state. Do not call it a disposable router without that evidence.
2. Start a real Pi target worker using the normal goal extension. Record its agent and instance IDs in the private run record.
3. Start the documented Pixel 8 API-35 emulator, install the debug APK, and use ADB reverse to the verified loopback router. Record the AVD name, emulator process and device serial, installed package and process, and exact ADB reverse mapping. Prove that this task owns the emulator and app runtime. Do not stop, reuse, modify, or claim another task's runtime.
4. Perform and preserve router correlation for the real Android set acceptance flow. Exercise the editor labels, scrolling, and Cancel behavior. Do not run real off/restore when preserving Wolfgang’s directed live goal is required; record that explicit acceptance gap and make no off-flow claim.
5. Capture Android worker-list and chat-header screenshots at useful orbit frames. Show the old solid teal marker together with the orbiting border segment. Review the marker placement, quarter-perimeter segment, and faded line ends.
6. Capture matching WebUI states from the exact `origin/main` WebUI. Put each WebUI/Android pair side-by-side in the review evidence. State the source commit, verified router state, worker ID, and capture times.

Screenshots stay outside git unless separately requested. Do not create placeholders or describe planned captures as completed evidence.

Before handoff, inspect the final Android-only diff: `AndroidManifest.xml` exported components; cleartext/network configuration and permissions; goal text or identifiers in logs; and dependency changes. Resolve or explicitly surface any finding. Acceptance requires no unrelated changes or security regressions in these areas.

## Expected implementation files

Task review artifacts: `SPEC.md` and `PLAN.md` are changed for this task to record the target and plan. They are separate from shipped Android files.

- `app/src/main/java/life/meyers/mecha/MainActivity.kt`
- `app/src/test/java/life/meyers/mecha/GoalControlTest.kt` (new)
- `app/src/test/java/life/meyers/mecha/AgentFilterTest.kt`
- `tests/android_qol_parity.test.mjs`
- `tests/android_router_contract.test.mjs`

No WebUI file, router file, protocol file, Pi extension, migration, commit, push, or branch-history change is part of this task. PR #28 ancestry/head, base, and draft/ready state remain unchanged.
