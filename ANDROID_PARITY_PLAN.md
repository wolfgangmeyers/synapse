# Android Mailbox Parity Implementation Plan

## Scope and source of truth

This plan implements `ANDROID_PARITY_SPEC.md` from baseline `ce701fe6f` (`Repair router transport safety invariants`). It does not change the router, Web UI, protocol, live services, or any existing working-tree file.

The current Android implementation is concentrated in:

- `app/src/main/java/life/meyers/mecha/MainActivity.kt`: Activity-owned connection generation, lease renewal, discovery, history, drafts, notifications, router profiles, Compose routing, home, conversation, and existing image attachment UI.
- `app/src/main/java/life/meyers/mecha/ImageAttachments.kt`: bounded image input, JPEG conversion, three-image/768 KiB limits, and clipboard URI filtering.
- `app/src/test/java/life/meyers/mecha/{AgentFilterTest,ConversationHistoryTest,DraftStoreTest,ImageAttachmentsTest,RouterAliasTest}.kt`: current pure Kotlin coverage.
- `app/build.gradle.kts`: one application module, Compose Material 3, lifecycle runtime, OkHttp, JUnit, and JaCoCo; Compose test dependencies and the instrumentation runner are planned additions.
- `app/src/main/AndroidManifest.xml`: one `singleTask` `MainActivity`, `adjustResize`, notification and internet permissions, and currently broad cleartext allowance.

The Web UI contract is in:

- `web/router-ui-react/src/`: the TypeScript React implementation, including state, transport, persistence, controls, rendering, history, reminders, prompt templates, and safe Markdown behavior.
- `web/router-ui-react/index.html` and `web/router-ui-react/src/app.css`: control hierarchy, styling, and accessibility identifiers.
- `protocol/v1/schema.json`, `contract/manifest.json`, `contract/v1.md`, and the current router/Go transport code: wire shapes and available HTTP/WebSocket operations.

The old commits are evidence only:

- `3deb7bd15` has useful state-safety helpers and tests (`deliveryGenerationMatches`, `shouldSendDeliveryAck`, `leaseRenewalCanSend`, `modelControlSendMatches`, model catalog and reminder ideas), but it is an older monolithic `MainActivity.kt` built against an older Web UI.
- `420d99a94` has useful goal correlation, numeric validation, target-instance replacement, and activity-orbit ideas, plus `GoalControlTest.kt`. Its goal model includes fields that are not in the current parity contract; those tests must be rewritten against the current `goalState` schema.

Do not cherry-pick either commit. Recover individual invariants only after comparing them with the current Web UI and `protocol/v1/schema.json`.

## Implementation shape

Keep `MainActivity` and the current Compose patterns as the application shell. Do not begin with an architecture rewrite. The current source already has Activity-owned state, `SharedPreferences`-backed profiles/drafts/history, and bounded message helpers.

Use small typed additions as each slice needs them:

- Keep screen composition in `MainActivity.kt`, initially adding typed state and pure functions beside the existing symbols.
- Add a small `MailboxProtocol.kt` only when JSON parsing/building becomes large enough to obscure validation. It should contain typed models, strict bounds, frame builders, and frame parsers; it must not own UI state.
- Add a small `MailboxTransport.kt` only when the existing OkHttp callbacks cannot express connection generation, one-shot request handling, and test fakes clearly. It should expose one HTTP/WebSocket boundary and a fake implementation for unit tests.
- Add a small `ScopedPersistence.kt` only when the current preference-key code cannot safely support the additional scopes and bounds. Preserve the existing keys and migration behavior. A full database migration is not justified by source evidence at planning time.
- Add `MarkdownRenderer.kt` only if a reviewed Android Markdown library is selected. The fallback must be safe plain text. Do not use a WebView.
- Add `ImageCache.kt` only when the existing `RemoteImage` needs durable bounded cache and zoom behavior.

Keep operation boundaries typed and explicit: parsers return validated values, state updates return scoped projections, and pending records carry immutable target data. Keep those boundaries in the Activity until a demonstrated lifecycle or testability need requires a narrow extraction. Do not add an event bus, second network stack, dependency-injection framework, or speculative service layer.

Every new collection has a numeric bound, an eviction/rejection rule, and a test. The current Web UI is the authority for message history: 200 messages per conversation with 30-day age pruning. This overrides conflicting values from older commits or other source notes. Preserve the existing Android draft bound of 50 entries per router as existing behavior; it is distinct from the Web UI draft helper's 200-entry bound. Other planned bounds are: 1,000 model summaries per peer; 999 subagent summaries per peer; 100 stored prompt templates; 1,000 active reminders in one response; 100 pending operations per model/goal/reminder-lock type and 100 pending reminder CRUD operations; one history request per router/identity/peer; one arrival per target agent; three selected images per message, 768 KiB each, 2,304 KiB total; 100 cached thumbnails capped at 32 MiB and 30 days; and 999 expanded subagent keys per active router. Each bound is tested for its stated eviction, replacement, expiry, or rejection behavior.

## Vertical slices

Each slice below has a compile/test gate that uses pure Kotlin fakes or local fixtures. No gate requires a live router. A slice may be merged into the implementation branch only after its gate passes and the previous slices still pass.

### Slice 0: workspace and contract freeze

**Purpose:** protect the requested file boundary and establish the exact contract before Android changes.

**Planned files:** implementation branch metadata only; no product source changes.

**Actions:**

1. Record the baseline commit, current Android files, current Web UI files, protocol schema, and old commit SHAs in the implementation task notes.
2. Preserve the existing untracked files `ANDROID_PARITY_SPEC.md`, `PLAN.md`, `PROMPT_TEMPLATE_TIMESTAMPS_SPEC.md`, and `SPEC.md`. They are not part of this plan's implementation diff.
3. Build a behavior ledger from Sections 4, 5, 6, 8, 9, and 11 of `ANDROID_PARITY_SPEC.md`. Every row in the ledger must name a current Web UI symbol, Android symbol/file, unit test, and emulator/browser checkpoint.
4. Treat `web/router-ui-react` and `protocol/v1/schema.json` as the wire reference. Do not infer a new endpoint from a UI label.

**Gate:** `git status --short --untracked-files=all` is saved before implementation and the final diff contains only approved Android files and tests. No router-proxy file is touched.

### Slice 1: typed connection, URL, identity, and scope foundation

**Current Android seams:** `RouterConfig`, `normalizeRouterEndpointCandidate`, `parseRouterConfig`, `upsertRouterConfig`, `activeRouterAfterRouterSave`, `routerHttp`, `routerWs`, `connect`, `cancelConnection`, `startLeaseRenewal`, `scheduleReconnect`, `saveAgentId`, `switchRouter`, and `removeRouter` in `MainActivity.kt`.

**Web/protocol references:** `state.ownerScope`, `state.peerScope`, `state.draftScope`, `state.matchesActiveScope`, `app.connectUiSocket`, `app.handleEvent`, `app.switchRouter`, `app.setStatus`, `web/router-ui-react` registration/reconnect code, and schema `register`, `registered`, `lease_rejected`, `error`, and `lease_renew` frames.

**Implementation:**

- Introduce a typed connection scope containing canonical router base URL, install agent ID, instance ID, connection generation, lease ID, and socket identity. Every callback captures and checks that scope before mutating state.
- Extend router profile parsing to preserve `http` versus `https` while rejecting credentials, query strings, fragments, and arbitrary paths. Canonical HTTP profiles derive `ws://`; HTTPS profiles derive `wss://`. Existing `host:port` records migrate to the isolated cleartext debug form without losing alias/history/drafts.
- Keep profile, active-router, identity, selected-peer, history, draft, feature projections, and pending controls keyed by router and install identity. Switching routers closes dialogs, clears presence and pending controls, resets lifecycle version and arrivals, restores the selected router's draft/history, then starts a new connection generation.
- Register with `version`, `frame_id`, `type`, `agent_id`, `instance_id`, `agent_role: "user_interaction"`, `client_type: "android"`, and `ack_confirmation: "correlated_v1"`. Accept `registered` only when its request/connection generation and identity match. Renew only through the current socket, generation, lease, and instance.
- After a matching `registered`, fetch `GET /v1/capabilities` from the same canonical HTTP base with a 1,000 ms timeout. Capture generation, agent, instance, and lease before the fetch. Send `subagent_sync_enable` only when the response is successful, `subagent_lifecycle_v2 === true`, the socket is open, and every captured value still matches. Include `version`, `frame_id`, `type`, `agent_id`, `instance_id`, `lease_id`, and `after_version` when the current lifecycle version is greater than zero. Re-fetch on each new connection generation. A false/absent capability, 404, malformed response, timeout, disconnect, router switch, reconnect, or lease replacement keeps the legacy refresh path and sends no enable frame.
- Map states to the Web UI's connecting, connected, reconnecting, unavailable/error, pending, and uncertain language. Do not show raw frame names, lease IDs, JSON, or response bodies.
- Apply the endpoint policy in the planned artifacts: release accepts HTTPS/WSS only; debug cleartext is limited to loopback and the isolated emulator route. Do not add a live endpoint or a broad `usesCleartextTraffic` allowance.

**Tests:** extend `RouterAliasTest` for scheme preservation, URL rejection, HTTP-to-WS and HTTPS-to-WSS derivation, legacy record migration, alias retention, and per-router identity. Add `ConnectionScopeTest.kt` for stale socket, stale scheduled reconnect, stale lease renewal, duplicate identity, lease replacement, disconnect, malformed frame, and router-switch callbacks. Add capability tests for successful lifecycle enable, false/unsupported capability, 404, malformed JSON, timeout, delayed response after reconnect/router switch, lease replacement during fetch, wrong socket, stale `after_version`, and exact generation/lease-fenced frame fields. Extend `tests/android_router_contract.test.mjs` for registration metadata, capability fetch/timeout wiring, and exact `subagent_sync_enable` fields.

Add `AndroidTransportSecurityTest.kt` or equivalent source test for release HTTPS/WSS and debug loopback-only URL admission. Add an artifact test that reads the manifest and planned `app/src/debug/res/xml/network_security_config.xml`, runs `./gradlew :app:processDebugMainManifest :app:processReleaseMainManifest`, and uses `aapt2 dump xmltree` on both APKs. It must prove that only debug allows loopback cleartext, release has no broad cleartext setting or cleartext network-security rule, and neither artifact contains a hard-coded live host. Use fixture URLs only.

**Gate:** `./gradlew :app:testDebugUnitTest` and the existing Node contract/QOL tests pass using fakes only. A fake socket can deliver frames out of order and proves stale callbacks do not alter current scope.

### Slice 2: bounded persistence and presence/home projection

**Current Android seams:** `DraftStore`, `historyFor`, `persistHistory`, `appendMessage`, `hydrateHistory`, `knownPeers`, `persistKnownPeers`, `PeerHome`, `PeerRow`, `PeerActivityIndicators`, `SubagentIndicator`, `WorkingIndicator`, `Avatar`, `StatusDot`, `visibleConnectedPeers`, `normalizedSubagentSummaries`, and `peerStatusFromJson`.

**Web references:** `app.renderAgents`, `app.selectPeer`, `app.renderConversation`, `app.updateAgentActivity`, `app.applyLifecycleSnapshotEvent`, `app.applyLifecycleUpdateEvent`, `app.mergeAgentSnapshot`, `app.humanName`, `app.subagentBadgeMarkup`, `app.arrivalIndicatorMarkup`, and the `.agent-row`, `.working`, `.compacting`, and `.subagent-indicator` rules in `styles.css`.

**Implementation:**

- Preserve the home hierarchy: Mecha title/subtitle, router selector, connection card, connected-agent count/filter, discovered peers only, refresh action, and inline empty/no-match/reconnecting/error states.
- Keep the current filtering behavior and add visible subagent descriptions to filtering. Exclude the local identity and do not repopulate presence from old history.
- Add typed peer projections with instance ID, activity (`active`, `compacting`, idle), bounded subagent summaries, goal projection, reminder presence, model projection, and arrival projection. A discovery snapshot replaces presence; a live event may add/update a currently valid peer but must not make old history a connected peer.
- Implement lifecycle snapshot/update/replay version ordering from `state.applyLifecycleSnapshot`, `upsertLifecycleAgent`, and `mergeAgentSnapshot`. An older snapshot cannot erase newer subagent details; a covered snapshot can clear only covered agents. A changed peer instance clears instance-scoped controls/projections.
- Preserve history while presence is stale/unavailable. Use the current Web UI rule of at most 200 messages per conversation and prune messages older than 30 days; age-prune first, then evict the oldest remaining messages. This rule is the direct task authority over conflicting source values.
- Bound every projection created here: accept at most 1,000 discovered peers in one snapshot, 999 subagent summaries per peer, and 999 expanded-subagent keys per active router. Reject oversized payloads or evict the oldest expansion key before state mutation. Keep one active history request per router/identity/peer and ignore the replaced request's result.
- Keep dialog state and pending controls out of the home when router, lease, or peer scope changes.

**Tests:** extend `AgentFilterTest` for descriptions, empty presence, no-match state, instance replacement, and lifecycle ordering. Add `LifecycleProjectionTest.kt` covering stale snapshot/update/replay, snapshot clearing, compacting, subagent expansion, restart replacement, the 999-summary bound, and expanded-key eviction. Add `ScopedPersistenceTest.kt` for router/identity/peer isolation, the authoritative 200-row/30-day message pruning, the existing 50-entry Android draft bound, one-request-per-peer replacement, peer/snapshot bounds, and removal of one router's data without touching another.

**Gate:** unit tests compile and pass with JSON fixtures; Compose rendering tests, if added, use a static `PeerStatus` list and never open a socket.

### Slice 3: shared conversation history, arrivals, and safe message rendering

**Current Android seams:** `ChatMessage`, `chatMessage`, `receiveConversationMessage`, `settleDelivery`, `mergeConversationMessages`, `reconcileConversationMessages`, `MessageBubble`, `Conversation`, `postInboundNotification`, `onNewIntent`, `conversationId`, `RemoteImage`, and `loadBoundedImage`.

**Web references:** `app.handleEvent` branches for `history`, `conversation_message`, `message`, and `message_arrival`; `app.appendMessage`, `app.settleOutgoing`, `app.renderMessage`, `app.showMessageArrival`, `app.hideMessageArrival`, `app.showArrivedMessageDialog`; `history.messageKey`, `mergeMessages`, `reconcileHistoryMessages`; `markdown.markdownToSafeHtml`, `markdown.renderMarkdownBody`, and `markdown.textWithoutMarkdownImages`.

**Implementation:**

- Parse and validate conversation/history messages, including sequence, router incarnation, sender/recipient, timestamps, optional subject, body bounds, and attachment references before state mutation.
- Preserve high-water reconciliation and incarnation-aware keys. Merge router history with locally received messages without dropping newer local entries or duplicating retries.
- Persist an incoming message before sending its `injection_requested`/`ack` receipt. Handle duplicate delivery as one local message and one notification decision. Validate the active lease/generation/target before sending receipts.
- Do not add read/unread or clear-history parity controls. The current Web UI has neither; retain any existing Android-only behavior unchanged.
- Add arrival state keyed by target agent. Accept only supported source kinds (`agent`, `discord`, `reminder`), replace a prior arrival for the same agent, show sender/subject/bounded preview, open a safe Markdown dialog, dismiss, expire after 60 seconds, and clear on scope changes. Arrival state is separate from history.
- Render safe Markdown in Compose. Raw HTML becomes text, only HTTP/HTTPS/mailto links are actionable, code/lists/paragraphs/line breaks remain readable, malformed Markdown falls back safely, and Markdown image syntax does not fetch an untrusted URL. Use a small tested renderer or a parser-to-Compose representation; do not use a WebView.
- Preserve incoming/outgoing direction, readable bubble width, timestamp, optional subject, pending/status language, and keyboard-safe composer layout.
- Add a bounded app-private image cache keyed by router/identity/peer/message/attachment. Store history thumbnails, never persist data URLs in the message history, cap cache bytes/count/age, and show a local placeholder for failed or oversized loads. Tapping a thumbnail opens a zoom surface. Keep the existing three-image and total/per-image limits in `ImageAttachments.kt`.

**Tests:** extend `ConversationHistoryTest` for duplicate/replay, high-water, late local merge, incarnation replacement, authoritative 200-row/30-day retention, and persist-before-receipt ordering. Add `MessageStateTest.kt` for arrival replacement/dismissal/expiry/scope reset, notification deduplication, and late intent. Add explicit tests for the one-per-agent arrival bound. Add `MarkdownSafetyTest.kt` covering raw HTML, allowed/disallowed links, code/lists/line breaks, malformed Markdown, and image syntax. Extend `ImageAttachmentsTest` for the 3-image/768 KiB/2,304 KiB limits, bounded cache, thumbnail fallback, zoom selection, and failed/oversized remote images. Add `NotificationIntentTest.kt` for exact peer routing and per-router conversation IDs.

**Gate:** all parsing/state tests pass with fixture JSON; Compose tests use fake image/cache results and fake notifications. No test depends on a router, WebSocket, or Android network.

### Slice 4: optimistic send and delivery disposition

**Current Android seams:** `send`, `updateDraft`, `draftFor`, `DraftStore.clearIfUnchanged`, `appendMessage`, `ChatMessage`, and the direct `POST /v1/messages` in `MainActivity.kt`.

**Web references:** `app.sendDraft`, `app.pendingMessageBody`, `app.settleOutgoing`, `state.canSendDraft`, `state.settlePendingMessages`, `state.replacePendingMessage`, and `web/router-ui-react` `sendMessage`.

**Implementation:**

- Add a local pending outgoing bubble before network work starts. Keep the composer draft and image selection scoped to router/identity/peer. Clear only the unchanged sent draft after accepted handling.
- Use a typed one-operation pending map containing local ID, router ID, router/identity/peer scope, target instance if applicable, exact body, and request generation. A response cannot settle a different operation.
- Serialize the existing envelope once with exact `id`, `from`, `to`, optional subject omission, body, and UTC-millisecond `created_at`. Enforce strict UTF-8 and current router limits before sending. Do not silently trim or rewrite the user's body.
- Distinguish accepted, definite rejected, timeout, disconnect, unavailable, and unconfirmed states. Socket write success is not acceptance. Preserve failed/uncertain bubbles with actionable local text and never retry an ambiguous POST automatically.
- Preserve the existing HTTP endpoint and attachment representation. Use HTTPS when configured and disable transparent redirects/retries. The implementation may keep direct OkHttp calls until a fake transport is required; then move only the request boundary to `MailboxTransport.kt`.

**Tests:** add `OutgoingMessageTest.kt` for admission, exact serialization, UTF-8 byte limits, optimistic insertion, accepted/rejected/timeout/disconnect, stale result, duplicate result, unchanged-draft clearing, and image metadata. Add fake OkHttp/transport tests for redirects, retry disabling, and one-shot request bodies. Extend `tests/android_router_contract.test.mjs` to assert the existing envelope fields and receipt ordering.

**Gate:** Gradle unit tests pass with a fake HTTP transport. The fake can return responses after router switch/disconnect and proves the old response cannot clear a new draft or settle a new pending message.

### Slice 5: model catalog and switching

**Current Android additions:** typed `ModelSummary`, catalog projection, pending model control, and picker behavior in `MainActivity.kt` or the narrow protocol/state helper selected in Slice 1. Compose surface belongs in `Conversation` beside the peer header.

**Web references:** `state.normalizeModelSummary`, `normalizeModelSummaries`, `mergeAgentModelProjection`, `mergeAgentModelSnapshot`, `matchingPendingModelControl`, `consumePendingModelControl`, `modelControlStatusTitle`; `app.updateAgentModelCatalog`, `app.mergeAgentSnapshot`, `app.selectModel`, `app.renderModelOptions`, `app.renderModelControls`, `app.showModelControlStatus`, and `web/router-ui-react` model control forwarding.

**Implementation:**

- Parse provider, model ID, optional display name, catalog count, and truncation with the Web UI's bounds and de-duplication. Keep loading, empty, and truncated catalog states distinct. Keep at most 1,000 model summaries per peer; retain the first 1,000 after de-duplication and reject malformed oversized payloads before state mutation.
- Add a compact picker with provider/model fallback labels, search over provider/ID/display name, accessible option labels, and a note such as `Showing N of M models.` Hide/disable it under the same availability conditions as the Web UI.
- Send the existing `model_control` frame with source agent/instance/lease, target agent/instance, control ID, `set_model`, and `{provider,id}`. Track pending by control ID and target instance.
- Show provisional selection only while the matching operation is pending. Accept a result only when control ID, target peer, target instance where present, source scope, and pending entry match. A confirmed accepted result requires a valid current model. Rejection uses the Web UI's confirmed rejection code set; timeout/disconnect/stale/unknown results say not confirmed.
- Keep an accepted model result newer than an older catalog snapshot. Defer an empty catalog that arrives while a model switch is pending, then apply it after the matching result. A changed peer instance replaces the model projection and pending state.

**Tests:** add `ModelCatalogTest.kt` for normalization, the 1,000-entry bound, search, truncation, loading/empty distinction, projection merge, older snapshot protection, deferred empty catalog, accepted/rejected/timeout/stale/duplicate result, and exact outgoing frame fields. Recover and adapt the old model assertions from `3deb7bd15`; do not copy its old assumptions. Add source-level assertions to `tests/android_qol_parity.test.mjs` only for stable public wiring, not implementation text.

**Gate:** pure model tests and fake-transport tests pass; the Compose picker test uses static catalog data and does not require a live model-capable agent.

### Slice 6: goals, locks, snooze, and outcomes

**Current Android additions:** typed full goal state, goal draft, pending control record, target-instance checks, goal header/row actions, and `GoalDialog`/small Compose controls in `MainActivity.kt`.

**Web/protocol references:** `app.DEFAULT_GOAL_STATE`, `normalizeGoalProjection`, `normalizeGoalStateValue`, `goalDraftFromState`, `renderGoalDialog`, `updateGoalDraftFromInput`, `sendGoalControl`, `handleGoalControlResult`, `goalControlErrorMessage`, `goalOutcomeCountsLabel`; schema `goalState`, `goalControlFrame`, `goalControlResultFrame`, and current `agent_goal_state`.

**Implementation:**

- Represent exactly the current protocol fields: enabled, goal (up to 4,000 Unicode characters), goal lock, snooze enabled, duration, optional turn limit, optional budget, refill yield, refill elapsed, and `COMPLETED`/`NOT_DONE` outcome counts. Do not import old `blocking`, `BLOCKED`, or waiting-for-authorization fields unless current schema evidence changes.
- Open the editor from the conversation header and row indicator. Keep a draft separate from last confirmed state. Cancel restores confirmed state. Save sends `set` with the exact goal/settings or `off` with an empty goal and all settings.
- Validate required goal text and every numeric bound locally; leave the editor open on invalid input. Use the Web UI's error language for unavailable capability, target restart, disconnect, timeout, router reconnect, and rejected changes.
- Track pending by control ID, source scope, target agent, target instance, and operation generation. Update visible goal indicators and full state only from a matching accepted result or authoritative scoped state event. A stale result cannot close the dialog or replace the draft.
- Use the pink goal meaning from `styles.css` and keep the controls 48dp minimum, scrollable, keyboard-safe, and accessible at large font size.

**Tests:** add/update `GoalControlTest.kt` from the old `420d99a94` test ideas, rewritten to the current schema. Cover set/off, each setting, goal length, numeric/Unicode validation, defaults, outcome counts, exact frame fields, capability/error mapping, target replacement, disconnect, timeout, stale/duplicate results, cancel behavior, and confirmed-only indicator changes.

**Gate:** pure Kotlin goal tests, fake socket result tests, and a static Compose dialog test pass without router access.

### Slice 7: reminders, recurrence, countdown, and ephemeral locks

**Current Android additions:** typed `ReminderSummary`, reminder list/editor state, pending CRUD and lock maps, countdown timer, and reminder dialogs in `MainActivity.kt`.

**Web/protocol references:** `app.reminderRowsForPeer`, `renderReminderDialog`, `renderReminderItem`, `renderReminderEditor`, `sendReminderCrud`, `sendReminderLockControl`, `handleReminderCrudResult`, `handleReminderLockControlResult`, `clearPendingReminderLockControls`, `clearReminderLockState`; `reminders.formatReminderCountdown` and `reminderRecurrenceLabel`; `web/router-ui-react.sendReminderCrud`; contract HTTP routes `/v1/reminders` and `/v1/reminders/{id}`; schema lock frames.

**Implementation:**

- Load active reminders from `GET /v1/reminders` and filter to `scheduled`, `due`, and `message_queued`, enabled targets matching the selected peer. Accept at most 1,000 reminders in one response; reject an oversized response before replacing the projection so the UI does not silently omit reminders.
- Provide list, create, edit, delete-confirmation, subject/body, delay, recurring toggle, and optional lock-after-create. Validate body and delay 1–10,080 minutes. Preserve interval semantics exactly. Use HTTP GET/POST/PATCH/DELETE routes with bounded response parsing.
- Show one-shot/recurring status and live countdown every second using the exact days/hours/minutes/seconds and `Due now` wording from `reminders.js`. Clear the timer when the dialog closes or scope changes.
- Correlate CRUD by request ID; cap pending reminder CRUD at 100 and settle the oldest as unconfirmed before adding a new request. Refresh after accepted changes and leave the dialog usable while pending. Correlate lock by control ID, reminder ID, target agent, target instance, and router scope; cap pending locks at 100 and settle the oldest on overflow. Lock is ephemeral UI state and is cleared on reconnect, disconnect, target replacement, and router switch.
- Implement create-then-lock sequencing. If creation succeeds and lock setup fails, say that the reminder was created and lock setup failed. Do not create a second durable reminder record for the lock.

**Tests:** add `ReminderStateTest.kt` for active-state filtering and the 1,000-reminder response bound, create/update/delete fields, delay/recurrence validation, countdown boundaries and invalid dates, CRUD stale/duplicate results, the 100-request pending bound and oldest eviction, lock set/clear, the 100-lock pending bound, pending disablement, target replacement, timeout/disconnect, and create-then-lock failure. Fake HTTP and WebSocket responses cover every disposition.

**Gate:** unit tests and static Compose dialog tests pass. Countdown tests use an injected clock; no wall-clock sleeps or live service are required.

### Slice 8: prompt templates, token substitution, preview, import, and export

**Current Android additions:** typed prompt template list/editor/use/import state, prompt picker, manager, dialogs, selection capture, and CRUD transport calls in `MainActivity.kt` or `MailboxTransport.kt`.

**Web references:** all functions in `prompt-templates.js`; `app.renderPromptControls`, `renderPromptOptions`, `selectPromptTemplate`, `renderPromptTemplateDialog`, `renderPromptTemplateToolbar`, `exportPromptTemplates`, `handlePromptTemplateImportFile`, `renderPromptTemplateImportConfirmation`, `renderPromptTemplateEditor`, `renderPromptUseDialog`, and `pastePromptTemplate`; contract prompt-template HTTP routes and current server validation.

**Implementation:**

- Load and refresh templates through `GET /v1/prompt-templates`; create/update/delete through the exact existing routes and request payload `{name, body}`. Keep loading, empty, pending, and failure states. Keep at most 100 stored templates. Reject an import or create that would exceed capacity; never evict an existing user template. Cap pending template requests at 100 and settle the oldest as unconfirmed before adding another.
- Validate non-empty name/body, no NUL, name at most 128 UTF-8 bytes, body at most 64 KiB, no more than 100 stored templates, and no duplicate names inside one import. Preserve body whitespace exactly.
- Match the versioned `mecha-prompt-templates` export format and version 1. Export only that format. Import parses before network work, reports malformed/empty/oversized input, detects conflicts, asks for explicit overwrite, and runs create/update sequentially with bounded progress, timeout, partial-completion text, and refresh.
- Parse distinct `${token}` names in first-use order. Substitute complete tokens only; preserve repeated tokens/order/case, unknown tokens, malformed token text, and literal values. A no-token template opens directly to paste-ready preview.
- Capture the composer selection when the picker opens. Paste replaces only that range, places the caret after inserted text, saves the scoped draft, and never sends.
- Keep prompt controls in the conversation header or a small-screen bottom sheet/dialog. Do not replace them with a generic settings page.

**Tests:** add `PromptTemplateTest.kt` for validation, the 100-template capacity and pending-request bound, exact format, export/import round trip, duplicate conflicts, overwrite flow, partial import/timeout, CRUD correlation, token order/substitution, unknown/malformed tokens, selection replacement, caret position, draft persistence, no-token paste, and reconnect. Add fixture parity tests against the JavaScript helper outputs for the same inputs; generated IDs/timestamps are the only normalized values.

**Gate:** pure helper/transport tests pass, and Compose dialog tests exercise list/editor/use states with fake responses.

### Slice 9: visual parity, accessibility, and phone layout

**Current Android seams:** `MechaApp`, `PeerHome`, `PeerRow`, `Conversation`, `MessageBubble`, `RouterManagement`, `UsernameDialog`, indicators, and theme color copy in `MainActivity.kt`; manifest `adjustResize`.

**Web references:** all IDs and hierarchy in `index.html`, token values and state classes in `styles.css`, `renderAgents`, `renderConversation`, and every dialog renderer in `app.js`.

**Implementation:**

- Set the custom Material 3 scheme to the parity values: Ink `#0B0D14`, Canvas `#0B0D10`, Surface `#151925`, Raised `#202638`, Primary `#9DA8FF`, Secondary `#78E0CD`, Text `#F2F4FF`, Muted `#B5BCD0`, Danger `#FFB4AB`, compacting `#C084FC`, arrival `#FFD166`, divider equivalent to `rgba(181,188,208,0.16)`. Keep dynamic Material colors off.
- Match title/metadata hierarchy, 16sp-equivalent body text, 4dp rhythm, 14–20dp shapes, 48dp touch targets, incoming charcoal/outgoing periwinkle surfaces, teal working/reminder, purple compacting, yellow arrivals, pink goals, and red destructive/error use.
- Add missing compacting indicator and queued-delivery copy. Use finite/restrained motion and honor reduced-motion/system animator settings. Existing `synchronizedHaloPulse` must not imply a state that the router did not report.
- Keep home and conversation as separate phone destinations through the existing conditional Activity/Compose routing unless a navigation dependency is proven necessary. Stack composer controls, make dialogs vertically scrollable, and keep keyboard-safe send visible. Do not use a WebView, table, stock dynamic colors, or generic settings page.
- Add semantic labels and TalkBack order for status, peer rows, indicators, model/goal/reminder/prompt controls, image thumbnails/zoom, send, dismiss, and failure/retry actions. State must not be color-only. Verify 200% font scaling without clipping or overlap.

**Tests:** add Compose screenshot/accessibility tests for populated, empty, loading, offline, pending, and failure states; large font scale; keyboard/back navigation; dialog/sheet focus order; TalkBack labels; and all feature controls. Keep screenshots stable against explicit colors and dimensions.

**Gate:** `:app:testDebugUnitTest` plus Compose UI tests pass without a router. Any screenshot baseline records Android screen size and the corresponding Web UI checkpoint.

### Slice 10: protocol hardening and bounded failure handling

**Current Android seams:** all JSON parsing in `MainActivity.kt`, all OkHttp calls, `ImageAttachments.kt`, persistence helpers, and new typed helpers from prior slices.

**Protocol references:** every Android-relevant `oneOf` in `protocol/v1/schema.json`, especially registration/lease, `message`, `conversation_message`, `agent_activity`, `agent_model_catalog`, goal, reminder lock, arrivals, delivery acknowledgement, and lifecycle snapshot/update/replay frames.

**Implementation:**

- Validate object shape, required fields, string lengths, numeric ranges, enum values, strict UTF-8/scalar validity, and attachment counts before state mutation. Ignore unsupported frames safely and show scoped retry/reconnect language.
- Keep payload-derived raw errors, message bodies, lease IDs, frame IDs, and stack traces out of normal UI and logs. Map known error codes to short product messages.
- Add connection generation and operation pending maps for every feature. On router switch, reconnect, lease replacement, or target instance change, settle pending operations as unconfirmed, remove them from the active UI scope, and prevent late callbacks from mutating current state.
- Ensure HTTP redirects, unexpected shapes, malformed frames, oversized values, unavailable capabilities, storage errors, and network failures become scoped retry/reconnect/storage states. Do not acknowledge an unpersisted delivery.
- Add explicit bounds to all new maps, queues, catalog entries, templates, reminders in memory, history, drafts, images, and cached thumbnails. Add cleanup tests for every retention path.

**Tests:** add `ProtocolParsingTest.kt` for all supported operations/results and malformed/oversized inputs listed in parity Section 8.1. Add `CorrelationFenceTest.kt` for every pending map and every scope fence. Add a transient focused coverage run over changed pure helpers; do not use coverage as a substitute for compilation or behavior tests.

**Gate:** Gradle compile plus focused unit tests and JaCoCo report pass. The fake transport exercises every error/failure family without a router.

## Differential scenarios

Create a shared, deterministic scenario fixture format under the test sources or `tests/` without changing the router/Web UI. Each scenario contains:

1. Initial router/peer/model/goal/reminder/template/lifecycle/history/draft state.
2. One user action.
3. One router response or event, including a deliberate stale, duplicate, timeout, disconnect, malformed, or target-replacement case where applicable.
4. Expected Web UI projection and Android projection.
5. Expected outgoing frame/request type, target, instance, control ID, request ID, and operation ordering.
6. Explicit normalization only for generated UUIDs, frame/message IDs, and timestamps.

The scenario set must cover router profile add/edit/alias/select/remove; discovery/filter/activity/subagents/compacting/lifecycle replay; history merge/clear/read/unread; message arrival; Markdown/images; optimistic send outcomes; model catalog/search/truncation/switch/rejection/uncertainty; every goal field and failure; reminder CRUD/recurrence/countdown/lock/stale target; prompt CRUD/use/tokens/paste/import/export/conflicts/partial failure; reconnect and scope changes.

Implement the Android side as a pure state adapter driven by the same fixture events. The React Web UI side uses the exported pure helpers under `web/router-ui-react/src/`; it must not call a live browser or router in unit tests. Compare body text, field presence, order, error category, and state transitions exactly.

**Differential gate:** The Android and React test suites run their shared contract scenarios independently; a passing scenario does not waive the emulator/browser proof below.

## Isolated router, Web UI, and emulator verification

### Environment creation

1. Allocate a unique task directory, temporary SQLite database, attachment directory, router HTTP/WebSocket port, Web UI port, emulator AVD/device identity, and task-named processes. Never use the live router or its database/attachments.
2. Start the current router implementation against the temporary database. Start unchanged `web/router-ui-react` against that router and the task Web UI port. If a proxy is required for the emulator, start a task-owned proxy with its own listen port and verify its upstream is the task router.
3. Record exact router/UI revisions, process command/cwd ownership, ports, database and attachment paths, and emulator endpoint. Prove that the app's endpoint is the isolated endpoint before each run. Keep the live router untouched.
4. Open a fresh Browser Console Bridge tab for the task. Do not reuse a live-router tab or browser storage.

### Web UI proof first

Run the unchanged Web UI through Browser Console Bridge and capture DOM assertions plus screenshots for:

- router add/edit/alias/select/remove;
- populated, empty, loading, reconnecting/offline, and failure agent states;
- filter, activity, subagents, compacting, lifecycle replay, goal/reminder indicators, and arrivals;
- history, Markdown/link/image behavior, image cache/open, optimistic send, and read state;
- model search, catalog truncation, accepted/rejected/uncertain result;
- goal fields, lock, snooze, outcome counts, validation, stale target, and timeout;
- reminder list/create/edit/delete, recurrence, live countdown, lock set/clear, and create-then-lock failure;
- prompt CRUD/use/token substitution/preview/paste/import/export/conflict overwrite/partial failure;
- reconnect, stale result, duplicate event, target replacement, and router scope changes.

The browser run must show no unexpected console errors. Save the exact DOM assertions, screenshots, isolated tab identity, and process ownership in task notes. Use `styles.css` and the Web UI screenshot as the visual reference for Android.

### Android emulator proof

1. Build the current Android app from the exact implementation head and install it on the task emulator. Use the isolated cleartext debug route only through the documented emulator transport (for example an explicitly task-owned ADB reverse mapping); secure configured routes use HTTPS/WSS.
2. Confirm from app-visible state and task logs that the endpoint is the task router. Do not accept a healthy port alone as proof of ownership.
3. Run the same scenario checkpoints through actual Compose surfaces: first launch/router setup; connection/reconnect; filter/presence/activity/lifecycle; conversation/history/Markdown/images/cache/zoom; draft/send/read/arrival/notification tap; model/goal/reminder/prompt controls; keyboard/back/dialog/sheet/error recovery; large font scale and TalkBack labels.
4. Capture Android screenshots at the same semantic checkpoints as the Web UI. Compare palette, labels, hierarchy, indicator meaning, control order, spacing intent, content density, and state disposition. Allow only phone layout changes: separate home/conversation destination, stacked composer, and scrollable dialogs.
5. Test malformed, delayed, duplicate, rejected, disconnected, and target-replaced responses through the fake/isolated router harness. Confirm no stale response changes the current router, peer, dialog, draft, model, goal, reminder lock, lifecycle state, unread count, or notification.
6. Shut down only task-owned processes after the verification run, and verify that the live router, its files, ports, and unrelated processes were not read, written, restarted, or stopped. Keep a verified task runtime alive during review if the implementation branch needs follow-up screenshots.

## Router-proxy dirty-file preservation and rollback

The parity implementation does not require changing `router-proxy/`. A pre-existing dirty router-proxy file is never a reason to reset, stash, clean, checkout, or overwrite the worktree.

Before implementation and before each isolated-runtime operation:

```text
git status --short --untracked-files=all
git diff -- router-proxy
git diff --cached -- router-proxy
git ls-files --others --exclude-standard -- router-proxy
```

Save any pre-existing tracked diff and untracked-file list outside the repository, for example under a task-specific `/tmp` directory. Record the file hashes and diff boundaries. Do not include those files in Android commits. Do not run a repository-wide formatter or cleanup command that can rewrite them.

If a proxy is needed for verification, use a task-owned process and port. Prefer a separate temporary worktree/copy for any proxy build artifact or experimental change. The upstream must be the isolated router. Never point it at the live router to make Android testing easier.

If an implementation command changes a router-proxy file unexpectedly:

1. Stop the command and preserve the new diff and file bytes.
2. Compare it to the pre-existing snapshot and identify only the implementation-owned accidental bytes.
3. Revert only those accidental bytes using the saved snapshot or a narrowly scoped reverse patch. Leave the pre-existing dirty bytes exactly as they were.
4. Re-run `git status` and both router-proxy diffs. A clean Android diff is required before continuing.
5. If ownership cannot be separated safely, abandon the task-owned workspace and continue in a fresh Android-only worktree; do not destroy the dirty source.

At final handoff, report the pre-existing router-proxy paths and confirm they remain byte-for-byte unchanged. The Android diff, tests, and generated verification artifacts must not contain router-proxy changes.

## Final readiness checklist

- Every behavior in parity Sections 1 and 4 has a current Web UI symbol, Android file/symbol, automated test, and isolated browser/emulator checkpoint.
- Current Android connection, router profiles/aliases, identity, filtering, activity/subagents, history, messages, drafts, notifications, and images still pass.
- Model, goal, reminder, lifecycle, arrival, prompt, message, and receipt operations use exact scope, correlation, lease, and target-instance checks.
- Safe Markdown, bounded image cache/zoom, optimistic send/read/unread, arrivals, compacting, lifecycle ordering, reconnect, empty/loading/offline/pending/failure states, and router isolation are covered.
- HTTPS/WSS is used for secure configured routers; cleartext exists only for the isolated emulator route.
- All local stores and pending collections have explicit bounds and tested retention/cleanup.
- Pure protocol/state tests compile and pass without live-router access. Differential fixtures compare Android and Web UI with only the allowed generated-value normalization.
- Browser Console Bridge proves the unchanged Web UI on the isolated router with required DOM assertions, screenshots, and no unexpected console errors.
- The emulator proves actual Compose behavior, screenshots, keyboard/back/dialog behavior, large-font behavior, and TalkBack labels against the same isolated router.
- Final source/test diff contains only approved Android work. Existing dirty files, including any `router-proxy` work, remain preserved.
