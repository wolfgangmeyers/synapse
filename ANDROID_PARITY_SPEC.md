# Android Mailbox parity specification

Status: Phase 1 specification

Target: the Kotlin and Jetpack Compose Android Mailbox client

Source of truth: the current router Web UI in `web/router-ui-react/`, `web/router-ui-react`, and the current router protocol. The implementation must match the Web UI's end-user behavior and visual language. Android may change layout for a small screen. It may not remove, reinterpret, or postpone a Web UI feature.

## 1. Goal

Bring the Android client on current `main` to functional and visual parity with the current router Web UI:

- model catalog and model switching;
- goal editing, goal locks, snooze settings, and outcome counts;
- reminder create, edit, delete, recurrence, countdowns, and ephemeral locks;
- prompt-template list, create, edit, delete, use, token substitution, preview, import, export, and paste;
- message arrivals;
- compacting and versioned lifecycle state;
- safe Markdown messages;
- image attachment caching and zoom;
- optimistic send state;
- per-router identity and history;
- HTTPS/WSS transport where configured.

The Android client continues to support its existing foreground connection, connected-agent filtering, activity and subagent indicators, conversation history, text and image messages, router configuration, identity, drafts, and local notifications.

This specification covers the product and verification contract. It does not change the live router and does not authorize implementation in this Phase 1 document.

## 2. Evidence and baseline

The baseline is commit `ce701fe6f` (`Repair router transport safety invariants`). A direct inspection of `app/src/main/java/life/meyers/mecha/MainActivity.kt` and the Android tests at that commit finds no model, goal, reminder, or prompt-template UI or state. The current Web UI does contain those surfaces:

- `web/router-ui-react/index.html` declares model and prompt pickers plus goal, reminder, arrival, and prompt-template dialogs.
- `web/router-ui-react/src/` handles lifecycle snapshots and updates, model catalogs and controls, goal controls, reminder CRUD and locks, arrivals, optimistic messages, history, prompt-template operations, safe Markdown, and rendering.
- `web/router-ui-react/src/app.css` defines the reviewed palette, spacing, shapes, typography, loading states, indicators, dialogs, and responsive behavior.

The missing Android parity work exists only in non-merged commits `3deb7bd15` (`Implement Android WebUI parity state safety`) and `420d99a94` (`Implement Android goal-control and orbit parity`). Those commits are evidence and implementation history. They are not the current source of truth and must not be assumed to match the current Web UI or router contract.

## 3. Product rules

1. The router protocol and current Web UI behavior are authoritative. Android uses the existing HTTP and WebSocket APIs and frame types. It does not add an Android-only API or a mobile-only message path.
2. Every request and event is scoped by router URL, local agent identity, selected peer, and, where provided, target instance. A late response cannot change the current router, conversation, dialog, or model selection.
3. The client displays the same user-visible state distinctions as the Web UI: connecting, connected, reconnecting, unavailable, pending, accepted, rejected, and timed out. It does not claim success from a socket write or an uncorrelated response.
4. Human-readable errors stay in the product surface. Raw JSON, frame names, HTTP response bodies, lease IDs, and stack traces stay out of normal UI.
5. Router state is authoritative for model, goal, reminder, lifecycle, and history data. Local state may be optimistic while a request is pending, but a stale snapshot must not overwrite a newer correlated result.
6. Keep the current one-Activity Compose structure. Add typed state, parsing, and transport boundaries incrementally within that Activity. Extract a component only when a demonstrated lifecycle or testability need requires it. Reuse the current persistence mechanisms. Do not add an event bus, second network stack, or speculative service layer.

## 4. User-visible behavior

### 4.1 Router and identity

The Routers surface keeps the current Web UI behavior:

- add, edit, select, and remove saved routers;
- optional router alias;
- normalize `http://` and `https://` endpoints without credentials, query, fragment, or arbitrary path state;
- derive `ws://` for an isolated cleartext debug router and `wss://` for HTTPS;
- keep the active router, agent identity, selected peer, drafts, and history scoped per router;
- clear feature dialogs, pending controls, and presence when switching routers;
- preserve each router's local history when another router is selected.

The Android identity is shown in the same quiet status treatment as the Web UI. Identity is a claimed mailbox value, not authentication.

Endpoint policy is explicit. Release builds accept configured `https://` router bases and derive `wss://` for the WebSocket; they do not permit cleartext traffic. Debug builds permit cleartext only to loopback (`127.0.0.1`, `localhost`, or `::1`) for the isolated emulator router. The endpoint is supplied by the isolated test runtime or user configuration. This document does not name a live endpoint. The manifest, debug network-security configuration, and built-artifact checks must prove this split.

Registration uses the current mailbox registration protocol and Android client metadata. After registration establishes a lease, Android fetches `GET /v1/capabilities` over the current HTTP base with a 1,000 ms timeout. It enables lifecycle sync only when the response is successful, `subagent_lifecycle_v2` is exactly `true`, the socket is open, and the captured connection generation, agent ID, instance ID, and lease ID still match current state. It then sends `subagent_sync_enable` with `version`, `frame_id`, `type`, `agent_id`, `instance_id`, `lease_id`, and `after_version` when the local lifecycle version is greater than zero. A capability timeout, 404, malformed response, false capability, disconnect, router switch, reconnect, or lease replacement keeps the legacy refresh path and sends no enable frame. A delayed fetch cannot send with an old lease or generation.

### 4.2 Home and connected agents

The home screen preserves the Web UI hierarchy:

- Mecha title and subtitle;
- router selector;
- connection status card;
- Connected agents heading and count;
- filter field matching raw IDs, humanized names, and visible subagent descriptions;
- one row per currently connected peer, excluding the local identity;
- humanized names, deterministic avatar colors, working/compacting state, subagent count, and expandable subagent descriptions;
- goal and reminder indicators on rows;
- arrival indicator on the matching agent row;
- refresh action.

An empty list, no-match filter, reconnecting connection, and router error each get a designed inline state. Cached history remains available while presence is stale or unavailable. Presence comes from the router discovery snapshot and lifecycle events. Android does not infer connected agents from old history.

On a phone, the Web UI sidebar becomes the home screen and the conversation becomes a separate Compose destination. This is a layout change only. The same controls and state remain available.

### 4.3 Conversation and messages

A conversation contains:

- peer avatar, humanized name, presence, activity, compacting, subagent, goal, reminder, model, and prompt controls;
- history loaded from the router and merged with locally received messages without losing newer local entries;
- incoming and outgoing bubbles with the same direction, spacing, timestamp, and maximum readable width as the Web UI;
- optimistic outgoing rendering with a pending/sending state, then the Web UI-equivalent accepted or failed state;
- the current draft, including a bounded per-router/per-peer draft;
- text send and image attachment actions.

The message body is rendered as safe Markdown with the Web UI's behavior:

- raw HTML is text, not executable markup;
- links are limited to the Web UI's allowed HTTP, HTTPS, and mailto forms;
- code, lists, paragraphs, line breaks, and inline formatting remain readable;
- malformed or unsupported Markdown remains safe plain text;
- Markdown image syntax does not cause an unbounded or untrusted network fetch.

Attachments use the existing router message representation. Android shows bounded thumbnails, uses an app-private bounded image cache, and opens an image zoom surface on tap. Failed or oversized image loads show a local placeholder and do not break the message. Image selection, paste, and removal preserve the Web UI's three-image and per-image/total size limits.

A new incoming message is persisted before it is acknowledged to the router. Duplicate delivery does not duplicate history or notifications. An incoming message can open the matching conversation from its arrival surface or local notification. Any existing Android-only local history behavior remains unchanged; adding read/unread or clear-history controls is outside this port because the current Web UI does not provide them.

### 4.4 Message arrivals

A `message_arrival` event from the supported source kinds (`agent`, `discord`, or `reminder`) creates one temporary arrival indicator for the target agent. A later arrival for that agent replaces the existing indicator. The indicator:

- identifies the sender using the same humanized display treatment;
- shows subject or bounded preview;
- opens a message dialog with safe Markdown and metadata;
- can be dismissed;
- expires after the Web UI's 60-second visible interval;
- is removed when the router or active per-router scope changes.

An arrival is not a second conversation message and does not create duplicate history.

### 4.5 Model catalog and switching

For a selected connected agent, Android displays the current model and the available model catalog using the Web UI's compact picker hierarchy.

- The catalog is loaded from the current agent snapshot/catalog events.
- Search matches provider, model ID, and display name.
- Each option shows the same useful model name and `provider/id` fallback.
- A truncated catalog says how many models are shown.
- An empty catalog is distinct from a catalog still loading.
- The picker is hidden or disabled under the same conditions as the Web UI.
- Selecting a model records a pending control scoped to the exact target agent and instance.
- The visible selection is provisional until a matching `model_control_result` confirms it.
- An accepted result updates the current model and catalog projection.
- A confirmed rejection says that the model change was rejected. A timeout, disconnect, stale instance, or missing correlated result says that the change was not confirmed.
- A late result, result for another target, duplicate result, or older catalog cannot mutate the current selection.

Android sends the existing `model_control` frame through the existing registration/lease fields. It does not call an invented model endpoint or treat the model label as an authorization decision.

### 4.6 Goals, locks, snooze, and outcomes

The selected agent's goal control opens from the conversation header and the row indicator. The editor matches the Web UI and includes:

- Goal on/off;
- Markdown goal text, required and limited to 4,000 Unicode characters when enabled;
- Goal lock on/off;
- Snooze on/off;
- snooze duration;
- optional snooze turn limit;
- optional snooze budget;
- snooze refill yield and elapsed settings;
- completed and not-done outcome counts;
- Save and Cancel.

The goal dialog is a draft. Cancel restores the last confirmed state. Save sends the existing `goal_control` frame with the target agent and exact target instance. The goal indicator changes only from confirmed state, except for the same pending visual treatment used by the Web UI.

Failure behavior matches the Web UI's user language:

- invalid goal/settings keeps the editor open and identifies the field problem;
- target instance changed asks the user to close and reopen the editor;
- target disconnected says the agent is no longer connected;
- unavailable router capability, timeout, reconnect, or lost confirmation leaves the last confirmed goal unchanged.

Goal lock and snooze are settings of the goal control. Outcome counts are displayed state from the router. Android does not invent local outcome counts.

### 4.7 Reminders, recurrence, countdowns, and ephemeral locks

The reminder control opens from the conversation header or the agent row indicator. It supports:

- list active reminders in `scheduled`, `due`, or `message_queued` state;
- create a reminder with optional subject, body, delay, recurring toggle, and optional lock-after-create;
- edit subject and body;
- delete with confirmation;
- show one-shot versus recurring status;
- show a live countdown with `Due now` or the Web UI's days/hours/minutes/seconds format;
- set or clear the native reminder lock.

Create validates reminder text and a delay from 1 through 10,080 minutes. Recurrence uses the same delay interval as the Web UI. CRUD results are correlated by request ID. The list refreshes after accepted changes and remains usable while a request is pending.

Reminder locks are ephemeral control state for the current target instance. They do not become a second durable reminder record. A lock control is disabled while pending, and its result is accepted only when control ID, reminder ID, target agent, target instance, and source scope match. Disconnect, target replacement, or router reconnect clears lock state and pending controls. If reminder creation succeeds but lock setup fails, the UI says that the reminder was created and the lock was not applied.

### 4.8 Prompt templates

The prompt picker appears for a selected peer. The manager and use dialog match the Web UI's control hierarchy.

The manager supports:

- list templates;
- create and edit name/body;
- delete with confirmation;
- import the versioned `mecha-prompt-templates` JSON format;
- export the same format;
- sequential import with conflict detection and explicit overwrite confirmation;
- partial-import progress and failure status;
- refresh after CRUD and reconnect.

Validation matches `prompt-templates.js` and the router contract: non-empty name and body, no NUL characters, name at most 128 UTF-8 bytes, body at most 64 KiB, no duplicate names within one import, and no more than 100 stored templates. Template body whitespace is preserved. A failed or timed-out import reports how many templates completed and leaves the list refreshable.

Use opens a dialog for the first-use-order, distinct `${token}` names. Each token has an input. The preview updates as values change. Substitution:

- replaces only complete `${name}` tokens;
- preserves repeated tokens and their order;
- preserves case;
- leaves unknown tokens and malformed token text unchanged;
- treats values as literal text.

Paste inserts the preview into the composer at the selection captured when the prompt picker opened. It replaces the selected range, places the caret after the inserted text, saves the draft, and does not send a message. A template with no tokens goes directly to the paste-ready preview.

### 4.9 Lifecycle and compacting state

Android consumes the same lifecycle snapshot, update, and replay events as the Web UI. It keeps the highest accepted lifecycle version per router/agent scope. Older snapshots and updates cannot erase newer subagent state. A snapshot removes subagent details only for agents whose state is covered by that snapshot version.

The visual states match the Web UI:

- active/working uses the teal working indicator;
- compacting uses the purple compacting indicator and says delivery is queued;
- subagents use the blue expandable indicator and bounded descriptions;
- an agent restart replaces instance-scoped controls, model state, goal state, and reminder locks with the new instance's state.

Motion is restrained and honors reduced-motion settings. Android must not use an infinite decorative animation to imply a state that the router did not report.

## 5. Visual parity

Use the current Web UI tokens from `web/router-ui-react/styles.css` as the Android theme baseline:

| Role | Value |
|---|---|
| Ink | `#0B0D14` |
| Canvas | `#0B0D10` |
| Surface | `#151925` |
| Raised surface | `#202638` |
| Primary | `#9DA8FF` |
| Secondary | `#78E0CD` |
| Text | `#F2F4FF` |
| Muted text | `#B5BCD0` |
| Danger | `#FFB4AB` |
| Purple compacting | `#C084FC` |
| Yellow arrival | `#FFD166` |
| Divider | `rgba(181,188,208,0.16)` |

Use the platform font, 16sp-equivalent body text, the Web UI's clear title/metadata hierarchy, rounded 14–20dp controls and bubbles, and a 4dp spacing grid. Preserve the Web UI's 48dp minimum touch target. Keep the same visual meanings:

- periwinkle outgoing/send/primary actions;
- charcoal incoming surfaces;
- teal connected/working/reminder state;
- purple compacting state;
- yellow message arrival;
- pink goal indicator;
- red only for errors and destructive actions.

The phone adaptation may:

- put home and conversation on separate destinations;
- use a top app-bar overflow menu, bottom sheet, or full-width dialog for controls that are beside the Web UI header;
- stack composer controls and make dialogs vertically scrollable;
- preserve a readable bubble width and keyboard-safe layout.

It may not:

- replace feature controls with a generic settings page;
- hide model, goal, reminder, or prompt controls behind unrelated navigation;
- change colors to stock dynamic Material colors;
- turn the conversation into a table or dashboard;
- remove empty, loading, offline, pending, or failure states;
- use a WebView as the product UI.

Support system font scaling without clipped controls, overlapping indicators, or a hidden send action. TalkBack order follows the visual order. State is not communicated by color alone.

## 6. Protocol and state architecture

The Android repository should expose typed operations and immutable state for these existing protocol families:

- registration, lease renewal, capability discovery, lifecycle-sync negotiation, discovery, activity, lifecycle sync, and reconnect;
- conversation history and `conversation_message`/`message` delivery;
- `POST /v1/messages` sending and the existing receipt sequence;
- `agent_model_catalog`, `model_control`, and `model_control_result`;
- `agent_goal_state`, `goal_control`, and `goal_control_result`;
- `GET /v1/reminders`, `reminder_crud`, and `reminder_lock_control`;
- `GET/POST/PATCH/DELETE /v1/prompt-templates`;
- `message_arrival`;
- `delivery_acknowledged` and any currently negotiated conversation-sync frames.

The implementation must preserve the current Web UI's correlation fields and target-instance checks. It must use a connection generation token and a per-operation pending map. On router switch, reconnect, lease replacement, or target instance change, pending operations are settled as unconfirmed and removed from the active UI scope.

Local persistence is per router and per install identity. History, drafts, selected peer, and feature projections must not leak between routers. The current Web UI's message rule is authoritative for this task: retain at most 200 messages per conversation and prune messages older than 30 days, evicting oldest remaining messages when both rules apply. This resolves any conflicting source or older-commit value. Bounds and eviction/rejection rules for every new collection are:

| Collection | Bound | Eviction or rejection |
|---|---:|---|
| Message history | 200 per conversation; 30 days | Prune by age, then keep the newest 200, matching the history helpers under `web/router-ui-react/src/`. |
| New draft entries | Existing Android limit of 50 per router; prune entries older than 30 days | Evict oldest entries. Keep this existing Android behavior; the Web UI's separate draft helper uses 200 entries. |
| Model summaries | 1,000 per peer | De-duplicate, then keep the first 1,000, matching the state helpers under `web/router-ui-react/src/` and the protocol schema. |
| Subagent summaries | 999 per peer | Keep the first 999; reject or mark an oversized payload invalid before mutation. |
| Prompt templates | 100 stored templates | Reject new/imported entries at capacity. Never evict user templates. |
| Active reminders in an Android projection | 1,000 per router response | Reject an oversized response as invalid. Do not silently hide reminders. |
| Pending model, goal, and reminder-lock operations | 100 per operation type | Settle the oldest as unconfirmed with the Web UI capacity error before adding the new operation. |
| Pending reminder CRUD operations | 100 | Settle the oldest as unconfirmed before adding the new operation. |
| History requests | One active request per router/identity/peer | Replace the older request and ignore its result. |
| Arrivals | One per target agent | Replace the prior arrival and expire it after 60 seconds. |
| Selected images | 3 per message; 768 KiB each; 2,304 KiB total | Reject additions over the Web UI limits. |
| Cached image thumbnails | 100 entries, 32 MiB, 30 days | Evict least-recently-used entries, then oldest expired entries. |
| Expanded subagent rows | 999 per active router | Drop the oldest expanded key when the bound is reached. |

No feature may create an unbounded local queue. Existing Android collections keep their current behavior unless this table names a parity collection.

All protocol parsing validates shape and bounds before state mutation. Payload-derived raw errors and message bodies are not logged. HTTP redirects, unexpected response shapes, malformed frames, and unsupported capabilities become scoped user-visible failures with retry or reconnect behavior.

## 7. Isolation and verification environment

Every implementation and verification run uses an isolated router runtime:

- a temporary router database;
- its own log and attachment directory;
- its own router HTTP/WebSocket port;
- its own Web UI port;
- its own test agent identities and data;
- no reads, writes, restarts, or traffic against the live router.

The unchanged `web/router-ui-react` runs against that isolated router. Browser Console Bridge opens a fresh isolated task tab and proves the Web UI before Android comparison. The run records the isolated tab, exact router/UI revisions, process ownership, ports, database and attachment paths, screenshots, and browser console output. The live router remains untouched.

## 8. Test requirements

### 8.1 Protocol tests

Automated Android tests cover every supported operation and result, including:

- registration, renewal, disconnect, reconnect, duplicate identity, and lease replacement;
- discovery filtering and stale snapshots;
- history merge, high-water reconciliation, duplicate messages, and incarnation-aware keys;
- optimistic send, accepted send, rejected send, timeout, disconnect, and late result;
- model catalog loading, filtering, truncation, empty/deferred catalog, accepted switch, confirmed rejection, timeout, stale instance, duplicate result, and older snapshot;
- goal set/off, every settings field, invalid input, target replacement, disconnect, timeout, and stale result;
- reminder list/create/update/delete, one-shot and recurring countdowns, lock set/clear, create-then-lock failure, lock timeout, and target replacement;
- prompt-template CRUD, validation, duplicate-name conflict, import/export round trip, conflict overwrite, partial import, timeout, token parsing, substitution, preview, selection replacement, and reconnect;
- arrival replacement, dismissal, expiry, safe Markdown, and scope changes;
- capability fetch success, false/unsupported capability, 404, malformed response, timeout, delayed response after reconnect/router switch, lease replacement during fetch, and generation/lease-fenced `subagent_sync_enable`; lifecycle version ordering, compacting, subagent expansion, and restart replacement;
- malformed JSON, unsupported frame, oversized values, invalid URLs, unavailable capabilities, and storage/network failures.

Tests assert user-visible state and exact outgoing protocol fields. Tests do not accept an uncorrelated response as success.

Transport artifact tests are explicit. `RouterAliasTest` rejects cleartext non-loopback endpoints and derives `ws://` only for debug loopback; HTTPS derives `wss://`. A manifest/network test reads `app/src/main/AndroidManifest.xml` and the planned debug-only `app/src/debug/res/xml/network_security_config.xml`: release has no broad cleartext allowance, debug allows only loopback, and neither artifact contains a live endpoint. Run `./gradlew :app:processDebugMainManifest :app:processReleaseMainManifest`; inspect the merged manifests and run `aapt2 dump xmltree` on each built APK to assert the same policy. The release artifact test also fails if cleartext transport or a hard-coded router host is present. These tests use fixture URLs only.

### 8.2 Differential tests

The canonical fixtures live under `tests/fixtures/android-parity/*.json`. Each fixture contains `id`, `initial`, `action`, `event`, `expected.web`, `expected.android`, and `expected.outgoing`. The shared scenario runner drives the Web UI adapter and Android adapter from the same fixture data; it does not use a live browser or router.

The Kotlin adapter is a pure test-source adapter. It accepts one fixture and returns JSON with exactly `{ "id", "projection", "outgoing", "error" }`; `projection` contains visible feature availability and state, `outgoing` is an ordered array of typed HTTP/WebSocket operations, and `error` is a normalized product error or `null`. The JavaScript adapter returns the same shape. Only UUIDs, frame IDs, message IDs, and timestamps use explicit placeholders. Body text, field presence, ordering, error category, and transitions remain exact.

Run the Android adapter with `./gradlew :app:runAndroidParityFixtures --args="--fixtures $PWD/tests/fixtures/android-parity --output $PWD/app/build/reports/android-parity/android.json"`. The task must fail on a missing fixture, adapter error, schema mismatch, or unexpected normalization.

The runner compares:

- visible feature availability;
- agent, model, goal, reminder, prompt, lifecycle, arrival, history, draft, and pending states;
- request/frame type, target, instance, control ID, and operation ordering;
- error disposition and whether local optimistic state is retained or reverted.

Only generated UUIDs, frame IDs, message IDs, and timestamps may be normalized. Body text, field presence, ordering, error category, and state transitions are compared exactly. Each scenario includes an initial state, user action, router response/event, expected Web UI state, expected Android state, and a deliberate stale or failure case where applicable.

### 8.3 Isolated Web UI proof

Browser Console Bridge must exercise the isolated Web UI for:

- router add/edit/alias/select/remove;
- agent filtering, activity, subagents, compacting, lifecycle replay, and arrivals;
- conversation history, Markdown, images, and optimistic send;
- model search, catalog truncation, accepted switch, rejected switch, and uncertain result;
- goal editing, locks, snooze settings, and outcome counts;
- reminder CRUD, recurrence, countdown, lock set/clear, and stale target;
- prompt-template CRUD, use, token substitution, preview, paste, import, export, conflict overwrite, and import failure;
- reconnect, stale requests, and scope changes.

The proof includes DOM assertions and screenshots for populated, empty, loading, offline, pending, and failure states. Browser console output contains no unexpected errors.

### 8.4 Android emulator and Compose proof

Instrumented Compose tests use `androidx.test.ext.junit.runners.AndroidJUnit4` with `androidx.test.runner.AndroidJUnitRunner`, `androidx.test:runner`, `androidx.test:rules`, `androidx.test.ext:junit`, `androidx.compose.ui:ui-test-junit4`, and debug `androidx.compose.ui:ui-test-manifest`. Use `createAndroidComposeRule<MainActivity>()` with a fake typed transport and deterministic state; do not open a router in these tests. The screenshot helper calls `captureToImage()` on named semantic roots and writes PNGs under `app/build/reports/android-parity/screenshots/<checkpoint>-<width>x<height>.png`. It records the screen size, font scale, theme, fixture ID, and Web UI screenshot/checkpoint beside each image.

Run unit and Compose checks with `./gradlew :app:testDebugUnitTest`, then run instrumentation with `./gradlew :app:connectedDebugAndroidTest -Pandroid.testInstrumentationRunnerArguments.class=life.meyers.mecha.AndroidParityComposeTest`. For emulator proof, capture the actual device screen with `adb exec-out screencap -p > app/build/reports/android-parity/screenshots/<checkpoint>-device.png` after the test has asserted the isolated fixture state. The test fails when required semantics, labels, target sizes, or screenshot files are missing.

Exercise the same scenarios through the actual Compose UI and capture screenshots at the same checkpoints used for the Web UI. Verify:

- first launch and router setup;
- connection and reconnect;
- agent filtering and lifecycle indicators;
- conversation history, Markdown, image attach/cache/zoom, draft, send, arrival, and notification tap;
- model, goal, reminder, and prompt-template controls;
- keyboard, back navigation, dialogs/sheets, empty states, and error recovery;
- large font scaling and TalkBack labels for the feature controls.

Compare the named Android and Web UI checkpoints. Allow only screen-size layout changes: separate home/conversation placement, stacked composer controls, and scrollable dialogs. Palette, hierarchy, labels, indicator meanings, control order, spacing intent, and content density remain the same. The emulator run proves the app connected to the isolated fixture router and not a live endpoint.
## 9. Non-goals

- Changing, restarting, migrating, or adding endpoints to the live router.
- Changing the router protocol, router Web UI, relay, database schema, or server behavior to make Android easier.
- Adding a mobile-only message path or a second source of truth for model, goal, reminder, lifecycle, or prompt state.
- Replacing the Web UI design with a new Android design system.
- Removing features because the phone is small. Layout adaptation is the only permitted product adaptation.
- Adding unrelated authentication, pairing, cloud sync, remote notification infrastructure, analytics, or account management.
- Reusing old Android commits as an unreviewed wholesale merge.
- Creating `PLAN.md` or changing any file as part of this Phase 1 specification.

## 10. Alternatives and recommendation

### A. Cherry-pick `3deb7bd15` and `420d99a94`

This is the shortest route to visible code. It also imports large changes built against an older baseline, including a monolithic `MainActivity.kt`, tests, and assumptions that may not match the current router or Web UI. It makes stale behavior look authoritative and leaves the full prompt-template and current visual parity audit unresolved.

### B. Rebuild all parity behavior from the current Web UI

This keeps the current source of truth clear and produces a cleaner state model. It costs more initial work and discards useful implementation and test ideas from the two old commits unless they are deliberately recovered.

### C. Hybrid migration: recommended

Use the current Web UI files and router protocol as the contract. Read the two old commits for reusable state-safety logic and test cases. Port only verified pieces into small typed Kotlin state and transport boundaries within the current Compose Activity. Extract components only when a demonstrated need requires it. Rebuild the Compose surfaces and visual tokens against current `styles.css`, `app.js`, `prompt-templates.js`, `reminders.js`, and `markdown.js`. Add differential scenarios before accepting each feature.

Do not cherry-pick either old commit wholesale. This keeps the useful parity work while avoiding a large stale merge and makes every behavior traceable to current Web UI evidence.

## 11. Acceptance criteria

The implementation is ready only when all of these are true:

1. Every current Web UI feature listed in Section 1 is available in Android and works through the existing router APIs/protocols.
2. Existing Android connection, filtering, activity, subagents, history, messages, router configuration, identity, drafts, and notifications still work per their current behavior.
3. Model, goal, reminder, and prompt operations use exact correlation and target-instance checks. Stale responses cannot mutate active state.
4. Messages render with the Web UI's safe Markdown rules. Image attachments support bounded cache, thumbnail rendering, and zoom.
5. Optimistic send, arrivals, compacting, lifecycle versions, reconnect, empty states, and failure states match the Web UI's user-visible dispositions.
6. Router profiles, identities, history, drafts, selected peers, and feature state are isolated per router. HTTPS/WSS is used for secure configured endpoints, and only the isolated loopback debug runtime may use cleartext.
7. Android uses the Web UI's current colors, type hierarchy, spacing, shapes, labels, control hierarchy, and state indicators. Only small-screen placement and scrolling differ.
8. Protocol tests cover every operation and failure family in Section 8.1.
9. Differential tests compare Android with the current Web UI against an isolated router and normalize only generated values explicitly allowed by Section 8.2.
10. Browser Console Bridge proves the isolated Web UI with the required DOM assertions, screenshots, and zero unexpected console errors.
11. The Android emulator launches and exercises the real Compose surfaces against the isolated router, with screenshots and accessibility checks for the required scenarios.
12. The final diff contains only the approved Android implementation and its tests. The live router and unrelated working-tree changes remain untouched.
