# Android Agent Client Specification

Status: proposed technical and product specification

Target: Kotlin and Jetpack Compose Android app

Delivery order: platform/distribution preflight first, Android Emulator end to end second, physical phone over Tailscale third

Implementation status: not started. This document does not authorize implementation.

## 1. Purpose

Build a real Android messaging client for the existing Mecha mailbox router. The app is a stable, long-running user-interaction agent. It can select any currently connected agent, send that agent a normal mailbox message, and receive normal mailbox messages from any agent.

The app is not an admin console. It should feel like a focused professional messaging product. Router details stay behind the product surface.

The first release target is a complete emulator flow against the existing router and two live Pi agents using the normal mailbox extension. Physical-phone access follows only after the emulator flow is reliable.

## 2. Product decisions

The following are requirements, not open design questions:

- Kotlin application code and Jetpack Compose UI.
- Material 3 with a dark-first custom design system.
- One install-scoped mobile `agent_id`, generated once from secure randomness, and a new random `instance_id` for each service lifetime.
- One router-generated random `message_incarnation_id` for each durable queued-message row. It is stable across delivery retries for that row and changes when the same envelope ID is inserted again after the prior row was deleted.
- One durable, hard-bounded Room incarnation-evidence ledger independent of visible message history. Trimming or clearing a conversation cannot remove evidence while router acknowledgement is uncertain.
- Registration through the existing mailbox WebSocket lease protocol as `agent_role: "user_interaction"` and `client_type: "android"`, with correlated ACK confirmation negotiated explicitly.
- Normal mailbox delivery in both directions. There is no mobile-only message path.
- Separate local conversations for each exact claimed sender, with reply enabled only for a valid peer agent ID.
- Durable local conversation cache across app and device-process restarts.
- Explicit clear-one-conversation and clear-all-history actions.
- Bounded message retention, conversation-shell count, preview bytes, home/timeline paging windows, and UI rendering.
- A long-running background connection for MVP only if the foreground-service target-SDK/device/distribution spike in Section 7.1 finds a valid platform and policy path.
- Local Android device notifications for incoming mailbox messages, including while the app is in the background.
- Notification intent is committed with the incoming message and reconciled idempotently across process crashes, notification side effects, and router receipts.
- Notification taps open the correct agent conversation and update unread state.
- No FCM, remote notification service, cloud relay, or backend notification push in MVP. All notifications are posted locally by the Android app after it receives a mailbox frame.
- No pairing token, pairing code, QR code, shared secret, or separate pairing flow. Emulator transport is local development transport. Physical-phone transport trust comes from Tailscale.
- The physical phone must not reach the router over public internet, local Wi-Fi exposure, or Tailscale Funnel.
- The physical release endpoint is the exact allowlisted base `https://donuts.tailcdfd89.ts.net:8443`; endpoint editing is not part of the release UI.
- No raw protocol frames, lease IDs, message IDs, JSON, debug tables, or generic admin forms in the normal UI.

## 3. Existing router analysis

This section describes the current implementation in `harness/router.js`, `harness/db.js`, `protocol/v1/schema.json`, and `pi-extensions/mecha-messaging/index.ts`. It is the integration baseline.

### 3.1 Current transport and endpoints

The router has one Node HTTP server and a `ws` WebSocket server:

- HTTP base URL defaults to `http://127.0.0.1:8765`.
- WebSocket registration is at `/v1/agents`.
- `POST /v1/messages` validates and durably enqueues an envelope.
- `GET /v1/messages/{id}` inspects a message while the router still retains it.
- `GET /v1/health` currently returns only status, protocol version, and schema migration IDs.
- A normal HTTP `GET /v1/agents` is not implemented. It currently returns `404`, even though the earlier router proposal listed it as a diagnostic endpoint.

HTTP and WebSocket can share `/v1/agents`: normal HTTP requests go through the HTTP handler, while WebSocket upgrades go through `WebSocketServer`.

### 3.2 Current envelope

The exact mailbox envelope is:

```json
{
  "id": "client-generated-id",
  "from": "mobile-agent-id",
  "to": "destination-agent-id",
  "subject": "Optional subject",
  "body": "Message body",
  "created_at": "2026-07-14T12:00:00.000Z"
}
```

Unknown fields are rejected. `created_at` is supplied and claimed by the sender. It is not created, corrected, or authenticated by the router. The schema requires the exact UTC-millisecond form `yyyy-MM-dd'T'HH:mm:ss.SSS'Z'`; a semantically equivalent timestamp with a different string representation is a different idempotency value.

The current repository compares an existing message field by field: `id`, `from`, `to`, nullable/absent `subject`, `body`, and the exact `created_at` string. JSON member order and insignificant JSON whitespace are not compared. The app still serializes an outgoing envelope once and persists that UTF-8 JSON so any retry that Section 8.5 permits cannot regenerate the timestamp, change optional-field presence, or change escaping.

This idempotency lasts only while the router retains the message row. A recipient `ack` atomically deletes that row and its attempts. The router keeps no delivered-message tombstone, so a later POST with the same ID can create and deliver a second message. MVP does not add tombstones. The required integration migration instead gives each queued row a router-generated `message_incarnation_id`: identical POSTs while that row exists retain its incarnation, while an insert after deletion receives a fresh incarnation. This is delivery-occurrence identity for inbound deduplication, not a tombstone and not permission for Android to retry an ambiguous outbound POST. Android therefore never retries an attempt once its request body may have started unless it received a response that the protocol defines as a definite non-enqueue. An ambiguous attempt becomes the terminal **delivery unknown** state in Section 8.5.

A successful HTTP response means the router accepted the message durably. It does not mean the destination rendered, read, or acted on it.

Runtime limits are currently:

- 4 KiB UTF-8 body by default.
- 64 KiB absolute body maximum if the router is explicitly configured for it.
- 100 queued messages per recipient by default.
- 256 KiB queued bytes per recipient by default.
- 60 accepted requests per claimed sender per minute by default.
- 512 KiB maximum HTTP request.

The app must stay within the 4 KiB default and still handle `413` and `429` responses.

### 3.3 Current registration and delivery

A client sends `register` with `agent_id` and `instance_id`. The router grants one renewable lease for that `agent_id`:

- Lease duration: 30 seconds by default.
- The existing Pi client renews every 10 seconds.
- A second live instance for the same `agent_id` receives `duplicate_identity` and is disconnected.
- A stale lease can be recovered after expiry.
- Registration immediately offers the oldest queued message.
- Only one message per recipient is in flight at a time.

A delivery frame contains the complete envelope, an attempt number, and a delivery deadline. The current receipt sequence is:

1. `injection_requested`
2. `ack`

The current router sends no positive response to `ack`. It may delete the queued row and lose the socket before Android can observe any later frame, so successful ACK transmission is not ACK certainty. Section 9.3 adds an Android-requested confirmation frame and a bounded recovery check for this uncertainty; legacy Pi receipts keep their current behavior.

The router deletes an acknowledged message and its delivery attempts. Router storage is a queue, not message history. Unclaimed queued messages expire after one hour. Agent resources are removed after one hour disconnected. The app therefore cannot reconstruct its conversation from the router.

Delivery attempts time out after 30 seconds and dead-letter after three failed attempts by default. A client must persist quickly and return receipts before that deadline.

The current router calculates an unclaimed message's `expires_at` as one hour after the sender-claimed `created_at`. That lets a skewed or malicious sender shorten or extend queue retention. Before Android integration, enqueue must instead set `expires_at` to one hour after the router's `firstSeenAt` clock on the first successful insert. The envelope's `created_at` remains unchanged and remains part of the field-by-field idempotency comparison. Existing queued rows keep their stored expiry; this behavioral fix needs regression tests, not a table rewrite.

### 3.4 Current protocol gaps for Android

The existing registration schema has no agent role or client type. It also uses `additionalProperties: false`, so Android cannot add metadata without a schema and router change.

The router also lacks a connected-agent listing. The app cannot implement a correct agent picker by guessing tmux names or reading router SQLite directly.

Envelope `from` is only a one-to-128-character string. Unlike `to`, it does not use the `agentId` grammar. Android therefore cannot treat every claimed sender as a valid reply target or put the raw value into a label, notification identity, deep link, URI, or Android resource name.

The current sender-claimed timestamp also controls queue expiry. Section 3.3 makes the required server-enqueue expiry correction explicit; Android must not ship against the old behavior.

The current delivery frame has only the reusable envelope message ID. After `ack` deletes the row, the same ID can identify a later queued occurrence, while Android currently has no durable occurrence identity with which to distinguish it from a retry. A unique key on a visible message row would still be insufficient because retention and clear actions can delete that row before an uncertain ACK is resolved. Section 9.3 adds the required router-generated message incarnation and ACK confirmation, while Section 8.4 defines a separate bounded evidence ledger and exact duplicate-versus-conflict handling.

JSON parsers on Node and Android can also produce strings containing unpaired UTF-16 surrogates from escaped JSON even when the wire bytes were valid UTF-8. Such strings do not have a strict UTF-8 encoding and cannot safely enter fingerprints, byte accounting, SQLite, or receipts. Section 8.4 validates the strings Android can actually observe. OkHttp's WebSocket text callback does not expose the original frame bytes, so Android does not claim that it can distinguish malformed wire UTF-8 repaired by the library from a literal U+FFFD scalar. Section 9.3 applies the stronger raw-byte gate where the router owns the bytes.

The state name `injection_requested` is Pi-specific. Android does not inject into a model. Renaming the state would create a broad compatibility migration, so MVP keeps the existing frame and defines its Android meaning as "durably stored by the destination client." This is an intentional compatibility compromise.

### 3.5 Existing security boundary

The current router security model is endpoint-reachability trust, not authenticated local-process trust:

- It hard-rejects listener hosts other than `127.0.0.1` and `::1`.
- It has no application authentication or authorization.
- It rejects any HTTP request with an `Origin` header.
- It rejects WebSocket clients that send an `Origin` header.
- It validates exact JSON shapes and caps request, frame, body, queue, and request-rate sizes.
- State files are user-only.
- Message bodies are not included in structured router logs.

Loopback binding prevents remote network routing by itself, but TCP loopback carries no same-user, process, binary, or application identity. Another local OS user or sandboxed process may be able to connect depending on host policy. User-only database permissions do not add identity to the listening socket. ADB reverse likewise forwards a device TCP port; it does not prove which emulator app opened it.

`agent_id`, registration role, `client_type`, and envelope `from` are claimed values. A lease prevents two live WebSocket consumers from owning one destination ID, but `POST /v1/messages` does not prove that the sender owns a lease matching `from`. A safe label prevents control-character and identifier rendering problems; it does not authenticate the displayed sender.

This remains acceptable for MVP only under these trust assumptions:

- Every host process that can reach the Mac loopback listener is inside the development transport trust boundary; there is no same-user guarantee.
- Every emulator process or app that can reach the ADB-reversed device port is inside that development boundary.
- Physical-phone access is restricted by Tailscale ACLs or grants to Wolfgang's approved phone node, and every app able to use Tailscale from that node is inside the physical transport boundary.

Tailscale authenticates and encrypts the node-to-node network path. It does not authenticate an Android app or turn mailbox `from` or `agent_role` into cryptographic identities. The UI must not imply otherwise.

### 3.6 Verified local integration evidence

The concrete environment inspected for this revision is:

- The mailbox router is listening only on `127.0.0.1:8765`.
- Tailscale reports this Mac as `donuts.tailcdfd89.ts.net` with Tailnet IP `100.115.63.19`.
- Tailscale Serve already owns HTTPS 443 and proxies `/` to `http://127.0.0.1:7860`; that handler must remain untouched.
- `100.115.63.19:8765` is already occupied by a different transcription service; that port must remain untouched.
- HTTPS 8443 has no Serve handler and is the dedicated physical-phone port selected by this specification.
- The approved phone visible in the current Tailnet is `note.tailcdfd89.ts.net`, a Galaxy Note20 Ultra 5G, at `100.100.214.15`, owned by `wolfgangmeyers@gmail.com`.
- Android SDK platforms 35 and 36 are installed. The installed emulator image is Android 35 Google APIs for `arm64-v8a`. The existing `call_blocker_api35` AVD is a Pixel 8 profile at 1080 by 2400 and 420 dpi. A Java runtime is not currently available to the Android command-line tools.

These observations choose nonconflicting endpoints and a reproducible emulator baseline. They do not prove the phone's Android build, a legal foreground-service type, or distribution-policy compliance. The preflight spike must collect those remaining facts.

## 4. Android emulator host routing

Android Emulator's `127.0.0.1` is the emulator itself, not the Mac. MVP should use ADB reverse port forwarding:

```text
adb reverse tcp:8765 tcp:8765
```

The debug app then connects to:

```text
http://127.0.0.1:8765
ws://127.0.0.1:8765/v1/agents
```

ADB reverse is the preferred emulator path because it preserves the router's loopback-only listener. It also carries both HTTP and WebSocket traffic over the same raw TCP mapping. No router bind-address change is needed. The mapping is transport plumbing, not same-user or app authentication: any emulator-side process allowed to connect to the reversed port can claim protocol identities.

The Android Emulator special host alias `10.0.2.2` is the fallback. It maps to the development host loopback on the standard Android Emulator. It is less explicit, behaves differently on nonstandard emulators, and requires a separate cleartext host allowance. It must be tested before it is documented as supported.

Debug builds may allow cleartext only for emulator development hosts. Release builds must not have a broad `usesCleartextTraffic=true` policy. Physical-phone traffic uses HTTPS and WSS through Tailscale.

## 5. User experience

### 5.1 First launch

A normal emulator debug build receives only its build-owned loopback router URL. It generates its install-scoped mobile `agent_id` through the same secure first-launch path as release and does not show a pairing screen. A fixed identity may be injected only by an `androidTest`/screenshot fake that is absent from every live installable artifact; Section 8.1 defines that boundary.

The physical-phone build will have a short setup flow:

1. Explain that Tailscale must be connected on the approved phone.
2. Display the fixed Tailnet endpoint for diagnosis; do not make it editable.
3. Display the generated install-scoped mobile address so Wolfgang knows where agents should send messages.
4. Explain local notifications, then request Android notification permission.
5. Run the startup preflight in Section 10.4 and start the mailbox connection only after it passes.

This is connection setup, not pairing. There is no token or code.

### 5.2 Home and conversation list

The home screen is conversation-first:

- Product title and a quiet connection indicator at the top.
- Conversations sorted by unread first, then most recent local activity.
- The home query is paged and bounded under Section 8.6. It never collects every retained conversation into one in-memory list.
- Agent avatar, display name, byte-bounded last-message preview, relative time, connection presence, and unread badge.
- A deliberate "new message" action opens a searchable bottom sheet of currently connected agents.
- Previously used agents remain in conversation history when disconnected. Their rows show an unobtrusive offline state.
- The mobile agent itself does not appear in the new-message picker.

A connected agent with no prior conversation can be selected and messaged. A disconnected historical peer can be opened and read. New sending remains available only when that conversation has a non-null validated peer ID; a null-peer unknown sender is read-only. The UI clearly says an offline send will wait for the router. Router acceptance still may queue the message if the peer disconnected after the last presence refresh.

### 5.3 Conversation detail

The conversation screen contains:

- Agent avatar, humanized name, and presence state in the top app bar.
- A bounded, paged message timeline.
- Date separators and one unread divider where useful.
- Distinct incoming and outgoing message surfaces.
- Optional mailbox subject as a small eyebrow above incoming body text. The composer does not expose a subject form.
- A multiline text composer with a clear send affordance.
- Pending, retrying, accepted, failed, and delivery-unknown local send states.
- A "new messages" affordance when the user is reading older history.
- Clear-conversation in the overflow menu with confirmation.

"Accepted" means accepted by the router. The app must not show read receipts, delivery checkmarks, or semantic completion because the router does not provide those facts.

### 5.4 Empty, loading, offline, and error states

Every main state gets designed copy and layout:

- No conversations: explain that connected agents can be selected, with one primary action.
- No connected agents: keep existing history visible and say agents will appear when connected.
- Initial connection: use a short skeleton/list transition, not a blocking spinner over an empty screen.
- Reconnecting: retain all cached content, show a compact reconnecting chip, and allow local outbox composition.
- Fully offline: show a restrained inline banner and queued-send status. Do not replace the product with an error page.
- Delivery unknown: retain the outgoing bubble, say the router may already have accepted it, and do not present the state as a safe retry. Editing or sending it again requires a separate duplicate-risk warning.
- Identity conflict: explain that this mobile identity is already connected and retry automatically after the old lease can expire.
- Dedup evidence full: keep cached history readable, explain that new messages are paused because replay safety storage could not be reconciled, and offer **Retry storage** plus the separately confirmed Reset app identity action. Do not expose incarnation/ACK terminology.
- Notification permission denied: keep unread behavior in-app and show one dismissible, actionable permission card.
- Router rejection: map protocol errors to user language. Raw status codes and protocol names stay in logs.

## 6. Visual design system

The UI must look intentional in populated, empty, loading, offline, and error states. A stock Material sample with default colors is not acceptable.

### 6.1 Theme and palette

Use a custom Material 3 `ColorScheme` with dark mode as the primary reviewed theme. The starting dark roles are:

- Canvas: near-black neutral (`#0B0D10`).
- Base surface: cool charcoal (`#12161C`).
- Elevated surface: blue-charcoal (`#1A2028`).
- Primary accent: restrained periwinkle (`#8FAEFF`).
- Secondary accent: muted teal (`#74D6C7`).
- Primary text: soft white (`#F2F5F7`).
- Secondary text: cool gray (`#AAB4BF`).
- Error: Material-compatible soft red (`#FFB4AB`).

Final token values must pass automated contrast checks. A light companion theme is required for system-theme support, but dark screenshots are the release baseline. The theme setting supports System, Dark, and Light. Dynamic color stays off in MVP so identity colors, contrast, and screenshot baselines remain stable.

### 6.2 Typography and hierarchy

Use the platform font and a small, deliberate type hierarchy:

- Screen title: Material 3 `headlineSmall` weight.
- Agent name and conversation title: `titleMedium`, semibold.
- Message body and composer: at least 16sp equivalent.
- Metadata and timestamps: `labelMedium` or `bodySmall`, never below 12sp equivalent.
- Connection and unread labels: compact but not condensed into illegibility.

Support Android font scaling through 200% without clipped controls, overlapping badges, or inaccessible composer actions. Long agent IDs and messages wrap or ellipsize intentionally.

### 6.3 Spacing, shape, and touch

Use a 4dp spacing grid:

- 16dp minimum screen-edge padding on phones.
- 12dp to 16dp vertical rhythm between content groups.
- Conversation rows at least 72dp high.
- 40dp agent avatars inside touch rows.
- Every interactive target at least 48dp by 48dp.
- Message bubbles use consistent asymmetric corners and a readable maximum width. They must not span the full screen.
- Surfaces use tonal separation before shadows. Heavy cards and nested outlines are not allowed.

### 6.4 Agent identity treatment

Every conversation has an exact local `sender_key`, but only some conversations have a valid `peer_agent_id`. Identity rendering follows these rules:

- When `peer_agent_id` is non-null, humanize only its ASCII `agentId` characters: split `.`, `_`, and `-`, title-case the tokens, and bound the rendered result. `health-watch-agent` becomes `Health Watch Agent`.
- When `peer_agent_id` is null, never render the raw claimed sender in the normal UI, notification, accessibility label, URI, or intent action. Render `Unknown sender · <fingerprint>`, where `<fingerprint>` is the first eight lowercase hexadecimal characters of SHA-256 over the exact UTF-8 `sender_key`.
- The fingerprint is only a stable disambiguator. It is not authentication.
- Stable one- or two-letter monograms are used only for valid peer IDs. Unknown senders use `?`.
- Select identity colors from a small contrast-tested palette using the exact `sender_key`; do not generate arbitrary RGB values.
- Show connected presence only when the non-null peer ID exactly matches a current discovery result. Presence still does not authenticate `from`.
- Show the exact agent ID as subtle secondary identity only for a valid peer ID and only where needed to disambiguate similar names.

The exact `sender_key` remains a bound Room value, never a SQL fragment or platform identifier. Protocol role, client type, instance ID, lease ID, frame ID, and raw invalid sender values are never presented as identity.

### 6.5 Motion and loading

Motion should explain state changes:

- 150ms to 250ms content transitions.
- Crossfade connection-state labels rather than moving the whole layout.
- Animate insertion of a new conversation or message without shifting the current reading position.
- Use a restrained progress treatment for initial agent discovery and outbox retry.
- Honor the system animator-duration scale and reduced-motion expectations.
- No decorative infinite animation while connected.

### 6.6 Accessibility

- WCAG contrast target: 4.5:1 for normal text, 3:1 for large text and meaningful non-text controls.
- Do not communicate connected, unread, pending, retrying, failed, or delivery-unknown state by color alone.
- Every avatar, status icon, send action, retry action, and notification has a useful content description where it is not already labeled by adjacent text.
- TalkBack traversal follows visual order.
- Message grouping does not collapse sender, direction, time, and send state into an ambiguous announcement.
- Touch targets remain 48dp at large font sizes.

### 6.7 UI anti-patterns

The product UI must not contain:

- Raw JSON, protocol frame names, HTTP codes, router migration IDs, or lease countdowns.
- Tables of agents or messages.
- A dashboard of operational metrics.
- A permanent endpoint/identity form on the home screen.
- Generic full-screen forms where a focused sheet or one-time setup step works.
- Debug log text in error cards.
- Material defaults with no product-specific type, spacing, identity, or state treatment.

Developer diagnostics remain available through Logcat and test tooling, not the primary product surface.

## 7. Android application architecture

Use one application module and boring Android components:

- Jetpack Compose and Material 3 for UI.
- One Activity for navigation and notification tap routing.
- Lifecycle-aware ViewModels exposing immutable UI state.
- Room for conversations, messages, unread state, outbox, and the independent bounded incoming-incarnation evidence ledger.
- DataStore for the install identity, endpoint selection, theme, completed-setup marker, and crash-resumable reset marker.
- Room for the sole notification-preview policy value alongside its durable notification reconciliation generation; there is no DataStore shadow copy.
- OkHttp for HTTP and WebSocket transport, with redirects and every library-level transparent connection retry disabled. Enqueue calls use a one-shot request body; only the durable outbox state machine may create a later call.
- A foreground service for the long-running WebSocket, renewal watchdog, and outbox loop if Section 7.1 passes.
- Android notification APIs through AndroidX compatibility helpers.

Do not add a dependency-injection framework, event bus, sync framework, or second network stack for MVP. Construct the small object graph explicitly.

WorkManager is not a continuous WebSocket runtime, so it is not a substitute for the connection service. The service's ongoing connection notification and all incoming-message notifications are local Android notifications. Neither is remote network push.

### 7.1 Foreground-service, SDK, device, and distribution spike

This is Phase 0 and blocks production Gradle project creation and app implementation. Do not copy a foreground-service type from another app or declare `dataSync`, `connectedDevice`, `specialUse`, or another type by guesswork.

The spike must produce one checked-in decision record with:

1. The exact `minSdk`, `compileSdk`, and `targetSdk`, plus Android Gradle Plugin, Kotlin, JDK, and build-tools versions that can be installed and reproduced. Platforms 35 and 36 and build tools 35.0.1/36.0.0 exist locally, but the missing JDK must be resolved first.
2. The actual Android version, API level, build number, manufacturer power policy, and Tailscale version from the target `note.tailcdfd89.ts.net` Galaxy Note20 Ultra 5G. Tailnet status alone does not provide these facts.
3. The MVP distribution decision. The default is a privately signed APK installed directly on Wolfgang's phone, with no Play Store distribution. Any Play or managed-store plan reopens the policy review.
4. The exact manifest foreground-service declaration and permissions permitted by that target SDK for this use case, with links to the applicable Android and distribution-policy text. If no declared type honestly describes a continuously leased mailbox socket, record that result instead of mislabeling it.
5. A throwaway-device test of start-from-visible-activity, the `startForeground()` deadline, ongoing notification behavior, Home/background operation, screen-off, Doze, Tailscale loss/recovery, process kill, force-stop, and device reboot. The test records what Android actually allows; it does not promise boot restart.
6. A go/no-go decision. If the service cannot run lawfully and reliably under the selected target SDK and distribution path, stop. Redesign delivery or add a real remote notification path in a separate specification before implementing this app.

The spike may use a minimal throwaway harness, but none of it becomes production architecture by default.

## 8. Local data and conversation model

### 8.1 Install-scoped identity

On the first successful DataStore transaction for a new installation, generate:

```text
android-<lowercase UUID>
```

For example, the shape is `android-550e8400-e29b-41d4-a716-446655440000`. The value comes from cryptographically secure UUID randomness, matches the existing ASCII `agentId` grammar, and is written once before any socket or send can start. It is not derived from `ANDROID_ID`, account data, phone number, model, serial number, Tailscale identity, or another hardware identifier.

Persist in no-backup app-private DataStore:

- The generated `agent_id`.
- The build-selected router base URL identifier. Release resolves only to the fixed allowlisted endpoint; it does not persist arbitrary URL text.
- Setup completion and theme.
- An internal `reset_pending` UUID marker only while the crash-resumable reset sequence below is incomplete.

The notification-preview preference is privacy-sensitive notification policy, not install/setup metadata. Its only persisted copy is the Room reconciliation singleton in Section 11.5, initialized to previews disabled. Clear-history preserves that singleton; Reset app identity recreates it at the disabled default.

Keep only in memory:

- `instance_id`: a random UUID created once for one foreground-service lifetime and reused across socket reconnects in that lifetime.
- Current lease ID, advertised lease expiry, renewal request ID, and renewal watchdog deadline.
- Current reconnect attempt.

Missing identity on a genuinely new install generates it. A DataStore read, corruption, or migration error after setup does not silently generate a different address; startup fails closed until the user explicitly resets app storage. Android backup is disabled, so uninstall or Clear storage ends the install identity. App updates preserve it.

There is no arbitrary identity editor in MVP. Live debug and release code have no identity `BuildConfig` field, manifest placeholder, Intent extra, ADB property, hidden setting, or dependency override. Deterministic tests may supply an identity only through test-source-set code or the fake repository used by screenshot tests. That code is not packaged into the normal debug APK or release APK. A normal debug install after Clear storage must exercise secure generation, not a fixture.

An explicit **Reset app identity** action clears both persistence systems through this crash-resumable sequence. There is no attempted cross-store transaction:

1. Acquire one app-wide reset gate. Mark the process resetting, reject new socket/outbox/Room work, stop the service and timers, cancel every in-flight HTTP call, and ignore late callbacks. A crash before the next step has changed no durable data.
2. In one DataStore transaction, write a fresh `reset_pending` UUID while retaining the old identity/setup values. Every startup reads this marker before identity creation, Room open, notification reconciliation, or network startup. When present, it resumes reset and cannot enter normal setup.
3. Cancel the foreground notification and enumerate/cancel every app-owned message notification. Close every Room handle. Idempotently delete the Room database with the platform database API, including its WAL and shared-memory companions, then create/open a fresh current-schema Room database and verify an empty write transaction, empty incarnation evidence, and the notification-preview singleton at `false`. Failure leaves `reset_pending` in place and exposes only reset retry or Android Settings **Clear storage**.
4. Only after the fresh Room verification and notification cancellation succeed, commit one final DataStore transaction that clears every key, including the old identity and `reset_pending`. This transaction is the reset completion point. A crash before it repeats Step 3; a crash after it sees a genuinely empty setup and cannot recover the old identity.
5. Restart the normal first-launch path. It generates and commits a new random install ID before opening any socket. Fresh-start notification sanitation also cancels any stale app-owned notification left by a platform-side race.

If DataStore cannot commit or read the initial marker, do not delete Room independently. The reset action falls back to `ActivityManager.clearApplicationUserData()` and tells the user to use Android Settings **Clear storage** if the platform request fails; normal networking remains blocked. The confirmation states that local history and preferences are deleted, the address changes, and old-address router messages and already accepted outgoing messages cannot be recalled.

### 8.2 Conversation identity

A conversation has an immutable local `conversation_id` (`INTEGER PRIMARY KEY AUTOINCREMENT`) and is uniquely keyed by the exact `sender_key` string:

- Incoming: `sender_key` is the exact envelope `from` string.
- Outgoing conversation creation: `sender_key` is the exact validated envelope `to` agent ID.
- `peer_agent_id` is the same exact value only when `sender_key` validates against protocol `$defs.agentId`; otherwise it is null.

This lets a valid sender and the matching outgoing destination share one conversation without pretending that every legal `from` is a legal agent ID. A null peer ID makes the conversation read-only: reply and retry-to-peer actions are disabled. A valid peer ID permits sending even when presence is stale or offline, under the router's normal queue rules. In either case the sender is still claimed, not authenticated.

A conversation also contains:

- The safe derived label and deterministic identity palette index. These are caches that can be recomputed, never identity keys.
- Last local activity time and insertion sequence.
- Last-message preview, truncated and measured under Section 8.6, plus its exact strict-UTF-8 byte count.
- Unread count.
- Latest known connection state and last presence refresh time for a non-null peer ID.
- A flag indicating that older local messages were trimmed by retention limits.

A conversation remains after the peer disconnects. Router presence does not own local history. Raw `sender_key` values are used only as bound data and are never interpolated into SQL, notification tags, intent actions, paths, or URIs.

### 8.3 Message and exact outgoing envelope

Each local message contains:

- An immutable local `message_row_id` used for Room relationships and timeline identity.
- Router message ID as transport metadata, never as user-facing content. It is the outgoing idempotency key but is not a unique incoming occurrence key.
- A non-null router-generated `message_incarnation_id` for a retained incoming row and null for outgoing rows. It links visible history to the independent evidence ledger but is not itself the replay-deduplication boundary; trimming and clear may delete this row while evidence survives.
- Local conversation ID and direction: incoming or outgoing.
- Exact claimed sender and recipient strings.
- Optional subject and body.
- Exact sender-claimed `created_at` string.
- Local insertion sequence and local received/created time from the device clock.
- Read time for incoming messages.
- Outgoing state: pending, retrying, in-flight, accepted, failed, or delivery unknown.
- Attempt count, next-attempt time, and a bounded diagnostic failure category without a raw response body.
- A link to its active outbox row, if any. That row stores the exact UTF-8 JSON request body serialized once before the Room transaction commits as a `BLOB` with its exact byte count.

The outgoing serializer emits one object in this member order: `id`, `from`, `to`, optional `subject`, `body`, `created_at`. It uses strict UTF-8, no insignificant whitespace, no null `subject`, and an exact UTC timestamp with three fractional digits and `Z`. A permitted retry reuses the persisted BLOB byte for byte. This is stricter than the router requires, but keeps the six idempotency fields stable by construction.

Order the visible conversation by local insertion sequence. Use local received/created time for normal timeline labels. The sender-claimed time may be available in an accessible message-detail surface, clearly labeled as sender time, but it cannot reorder history or drive retention, unread state, retry, or expiry.

### 8.4 Observable Unicode boundary, incarnation evidence, and storage health

Android applies the strongest Unicode gate available at each OkHttp API boundary before any schema-derived string is fingerprinted, normalized, measured, logged, persisted, shown, or used to construct a receipt:

- For bounded HTTP response bodies, read bytes from the response source and decode with a UTF-8 decoder configured to report malformed or unmappable input before JSON parsing.
- OkHttp's WebSocket text API calls `WebSocketListener.onMessage(WebSocket, String)`. It does not expose the original text-frame bytes. Validate that callback `String` as a Unicode-scalar sequence before JSON parsing, then recursively validate every parsed object member name and string value after JSON unescaping. In JVM UTF-16, each high surrogate must be followed by one low surrogate and every low surrogate must be paired. This catches malformed callback strings and escaped lone, reversed, or truncated surrogates produced by JSON parsing.
- Do not claim Android independently verifies the original WebSocket UTF-8 bytes. If the pinned OkHttp version closes or fails a malformed text frame, handle that as transport failure. If it supplies U+FFFD in the callback, the app cannot distinguish library repair from a literal U+FFFD sent by the peer and treats it as the callback scalar. A future byte-level guarantee requires binary protocol frames or a separately reviewed raw-frame WebSocket stack; neither is MVP.
- Do not normalize scalar-valid text. Control, combining, bidirectional, supplementary, noncharacter, and U+FFFD scalars remain exact callback/HTTP input and continue through the hostile-sender rendering rules. JSON Schema string lengths are counted in Unicode scalars after the observable gate, while storage and protocol byte limits use strict UTF-8 bytes. App-owned encoding reports rather than replacing an unpaired surrogate.

Room contains a separate `incoming_incarnation_evidence` table. It has no foreign key to conversation or message history. Each row stores the 36-byte incarnation ID as its primary key, the bounded ASCII router message ID for recovery lookup, a 32-byte SHA-256 envelope evidence digest, and a monotonic local insertion sequence. The digest is domain-separated and covers, in order, the exact strict-UTF-8 bytes of message ID, `from`, `to`, subject-presence marker and subject when present, body, and exact `created_at`; every variable field is prefixed by an unsigned fixed-width byte length. This makes subject absence distinct from empty and avoids tuple concatenation ambiguity without retaining a second message body. The table has a hard limit of 10,000 rows. It is excluded from visible-history counts and `accounted_content_bytes`, but its fixed/bounded columns and row limit are mandatory storage bounds.

For every scalar-valid `message` callback:

1. Validate the protocol version, exact frame shape, router-generated `message_incarnation_id`, envelope, attempt number, and delivery deadline. Verify `message.to` equals the registered install `agent_id`.
2. Derive the envelope evidence digest. Only for a previously unseen incarnation derive `sender_key`, nullable `peer_agent_id`, safe label/fingerprint, preview, and exact UTF-8 byte lengths under Sections 8.2, 6.4, and 8.6.
3. Start one Room transaction and look up `incoming_incarnation_evidence` by incarnation, never by the visible message table.
4. If evidence is absent, require one free evidence slot, then atomically insert the evidence row and visible message; create or update the conversation by exact `sender_key`, including shell admission or eviction; apply unread state; enforce retention bounds; and update durable notification intent under Section 11.5. The evidence commit and visible side effects are one transaction.
5. If evidence exists and both its stored message ID and digest match, treat the frame as a replay even when retention, shell eviction, or clear already removed the visible message or conversation. Do not recreate history or change unread, preview, capacity, conversation activity, or notification revision.
6. If the same incarnation has a different message ID or digest, roll back and take the incarnation-conflict path. Do not overwrite evidence or acknowledge either value.
7. Commit the new-insert or exact-replay result. Send `injection_requested` for the active attempt, then send `ack` for that attempt on the session that negotiated `ack_confirmation: "correlated_v1"`. Wake notification reconciliation after a new insert; notification posting is not on the receipt critical path.
8. Delete the evidence row only after the exact correlated `delivery_acknowledged` frame in Section 9.3 proves that the router deleted that incarnation, or after recovery lookup proves that message ID is absent or now belongs to a different incarnation. Sending `ack`, OkHttp/WebSocket send returning, socket close, elapsed time, visible-history deletion, notification success, or process restart is not settlement evidence.

The router message ID alone never selects the replay path. The same message ID with a different valid incarnation is a new queued occurrence and inserts a new local row even when all envelope fields are identical. An exact-incarnation replay remains invisible after its history was trimmed or cleared. Subject absence and empty subject remain different digest inputs.

Lost ACK confirmation leaves the evidence row durable. A serialized evidence reconciler runs after storage preflight, registration, every uncertain socket close, and when admission reaches the evidence cap. For each retained row, it performs bounded `GET /v1/messages/{message-id}` recovery using the normal strict HTTP/cap rules. HTTP 404 proves that exact queued row no longer exists. HTTP 200 with a shape-valid representation whose message ID matches the lookup and whose `message_incarnation_id` is different proves the old incarnation was deleted before the ID was reused. Either result deletes only the old evidence row in Room. A valid response with the same incarnation retains it for replay; timeout, transport error, 5xx, malformed/oversized body, or any other result is inconclusive and retains it. This use of GET does not resolve outbound acceptance ambiguity: it proves only whether one already-recorded inbound incarnation can still replay.

Never evict uncertain evidence by age, visible-history pressure, or oldest-first policy. If reconciliation cannot free a slot at 10,000 rows, a new incarnation is not persisted or receipted: close the socket and enter a body-free **dedup evidence full** storage-suspension state. Exact replays already represented by the table may still be handled before suspension, but normal reconnect and new delivery remain blocked until recovery proves rows settled or Reset app identity clears the installation. This is the bound that prevents safety from degrading into duplicate visible history.

An invalid observable-scalar delivery is rejected before sender fingerprinting, byte accounting, Room access, notification generation, `injection_requested`, or `ack`. If all ASCII delivery-identity fields can be parsed and validated independently, send exactly one bounded `nack` with reason `invalid_payload` for the active attempt; otherwise close without a receipt. An incarnation conflict follows the same `nack` rule and closes the socket because the server stream is no longer trustworthy. No invalid value or payload-derived exception is logged. A U+FFFD already supplied by OkHttp's text callback is scalar-valid and is not evidence of malformed wire bytes.

A Room open, migration, transaction, disk-full, corruption, or durability error is different: send no receipt and no `nack`, close the WebSocket, cancel lease-renew and reconnect timers, stop the outbox, and enter **storage unavailable**. Automatic reconnect is suspended so the same durable failure cannot consume all three router attempts. The foreground notification and Activity show the persistent local failure without message content. Only app restart or an explicit **Retry storage** action may rerun Room open/migrations and a write transaction; socket and outbox work resume only after that succeeds.

### 8.5 Durable outbox, composer admission, and finite retry schedule

The composer preserves the exact text the user entered. It does not trim, normalize, or silently truncate it. Send is enabled only when `!text.isBlank()`, strict UTF-8 encoding succeeds without replacement for an unpaired surrogate, and the encoded body is at most 4,096 bytes. Leading and trailing whitespace remain part of a valid nonblank message. Newline-only, whitespace-only, empty, malformed-Unicode, and over-limit input stays in the composer with an inline explanation. The UI counts UTF-8 bytes, not UTF-16 code units, and shows the count near the limit. Outgoing subject is absent.

Sending is local-first:

1. Require a non-null validated peer ID, healthy Room state, and valid composer body.
2. Generate one router message ID and one UTC-millisecond `created_at`.
3. Serialize the complete envelope once. Reject it locally if the exact serialized request is larger than 32 KiB (32,768 bytes), even though the router's general request cap is larger.
4. In one Room transaction, run capacity admission under Section 8.6 and persist the outgoing timeline row and outbox row as pending, including the exact request BLOB, its byte count, attempt count zero, and `next_attempt_at` equal to now.
5. Let the foreground service's single outbox worker POST those exact bytes to `/v1/messages`.
6. Mark accepted only for HTTP 200 or 201 with valid JSON whose returned `message` has the same `id`, `from`, `to`, absent subject, `body`, and exact `created_at` as the stored envelope. Extra router state such as `expires_at` is not local delivery proof.

Every app-owned `OkHttpClient` sets `retryOnConnectionFailure(false)`, `followRedirects(false)`, and `followSslRedirects(false)`. No application or network interceptor, authenticator, proxy authenticator, or custom call adapter may clone or replay an enqueue call. Each enqueue attempt constructs a fresh `RequestBody` over the persisted BLOB whose `isOneShot()` is true. One `Call` may therefore produce at most one network request. A permitted app-level retry is a later, newly accounted outbox attempt and a new `Call`/one-shot body over the same exact BLOB; it is never an internal OkHttp retry.

Only one app-wide HTTP send is in flight. Due rows drain by local insertion sequence. The service re-scans on startup, successful preflight, connectivity restoration, registration, **Retry now**, and each retry timer. A connectivity callback can make the oldest network-failed row due once; it is not evidence of reachability. **Retry now** is available only for a still-pending row and consumes the same finite attempt budget.

The router has no durable idempotency tombstones. The client therefore uses an at-most-once-biased ambiguity boundary:

1. In one Room transaction immediately before creating the OkHttp call, change the row from pending/retrying to in-flight and increment its attempt count. The OkHttp event listener records whether request-body transmission began.
2. HTTP 200/201 with the exact matching response marks accepted and deletes the outbox row.
3. `400 invalid_request`, `413 payload_too_large`, and `409 idempotency_conflict` are definite terminal rejections. Mark failed and delete the outbox row.
4. `429 queue_full` and `429 rate_limited` are definite non-enqueue responses under the router contract. They may return the row to pending while the attempt budget remains. Queue-full and missing/invalid rate-limit headers use full-jitter exponential delay with a 1-second base and 60-second cap. Rate limit accepts only an ASCII decimal integer `Retry-After` from 1 through 300 seconds; every other form uses exponential delay.
5. A dispatcher timeout, DNS failure, connect failure, or TLS-handshake failure proven to occur before request-body transmission may return the row to pending while the budget remains.
6. Once request-body transmission may have begun, any outcome without an exact matching 200/201 or one of the three definite terminal rejections or two definite 429 non-enqueue responses is ambiguous. This includes whole-call timeout, socket loss, cancellation, HTTP 3xx/5xx or an unrecognized 4xx, empty/malformed/oversized response JSON, and a mismatched 2xx envelope. Mark **delivery unknown**, delete the outbox row, and never automatically or manually POST that ID again.
7. On process start, any persisted in-flight row becomes delivery unknown before the worker scans. A crash after the pre-call Room transaction is conservatively ambiguous even if no bytes actually left the device.

Automatic retry is finite: no envelope gets more than eight HTTP attempts. Reaching eight only after definite non-enqueue or proven pre-body failures marks the row failed with `retry_exhausted` and deletes its outbox row. There is no same-ID retry from failed or delivery-unknown state. **Edit and send** always creates a new envelope and ID. From delivery unknown it first warns: "This may already have been sent. Sending again could create a duplicate." **Delete** removes the local row. The app does not use `GET /v1/messages/{id}` to resolve ambiguity because `404` means either never accepted or already acknowledged and deleted.

The app does not use WorkManager as a hidden second sender. If the foreground service is not running, no retry guarantee is made. Adding ambiguous retry later requires a separate router design with durable tombstones written atomically with final deletion, bounded retention and capacity admission, and a client retry horizon strictly inside that retention window.

### 8.6 Capacity admission, bounded retention, and rendering

Local history has no time-based expiry. It remains until the user clears it or deterministic capacity eviction applies, under these hard bounds:

- Maximum 1,000 conversation shells app-wide, including shells whose messages were completely trimmed.
- Maximum 1,000 messages per conversation.
- Maximum 10,000 messages app-wide.
- Maximum 10,000 independent incoming-incarnation evidence rows. Confirmed rows are deleted promptly; uncertain rows are never evicted to admit another incarnation.
- Maximum 32 MiB (33,554,432 bytes) of app-wide accounted content.
- Maximum 256 bytes of strict UTF-8 for each stored last-message preview. The shell cap therefore also caps all stored preview text at 256,000 bytes before the lower app-wide content limit is considered.
- Maximum 100 active outbox rows app-wide across pending, retrying, and in-flight states.
- Maximum 1 MiB (1,048,576 bytes) for the sum of exact outbox request BLOB lengths, with the 32 KiB (32,768-byte) per-row limit from Section 8.5.
- Load 50 conversations per home-list database page and keep no more than 150 conversation rows in its active paging window.
- Load 50 messages per timeline database page and keep no more than 150 timeline rows in its active paging window.
- Compose only visible `LazyColumn` items plus normal layout prefetch. No ViewModel or reducer materializes all retained shells or messages.

`accounted_content_bytes` is exact. It counts the bounded denormalized preview and deliberately counts active outgoing content in its message, preview, and request-BLOB representations:

```text
sum(UTF-8 byte length of each message subject, or zero when absent)
+ sum(UTF-8 byte length of each message body)
+ sum(stored strict-UTF-8 byte length of each conversation preview)
+ sum(length of each active outbox request BLOB)
```

SQLite row, index, page, WAL, and fixed incarnation-evidence overhead are not part of that value, so 32 MiB is a content-admission bound rather than a claim about database-file size. Evidence cannot duplicate subject/body text: it stores only the bounded identifiers, 32-byte digest, and sequence from Section 8.4. Store and compare the outbox BLOB length from the serialized byte array; do not recompute it from parsed JSON or character counts.

Reserve inbound capacity by applying lower soft limits to outgoing admission: at most 9,900 message rows and 31 MiB (32,505,856 bytes) of accounted content after inserting a new outgoing row. This leaves exactly 100 message slots and 1 MiB (1,048,576 bytes) unavailable to local composition. The separate 100-row/1-MiB active-outbox caps still apply. In the outgoing Room transaction, trim eligible history first, then reject the new local send without creating a timeline or outbox row if any soft or outbox limit would still be exceeded. The composer text remains available in memory with "Free some local history before sending."

A valid new-incarnation transaction may consume the reserve up to the hard limits. It inserts evidence and the new message, trims visible history in the same transaction, and acknowledges only after the final bounded state commits. Trim oldest read messages first, then the oldest messages regardless of read state. Never trim the newly inserted incoming message or an outgoing message with an active outbox row. Trimming any other incoming message leaves its independent evidence untouched. The active-outbox admission caps guarantee protected sends cannot occupy every row or all accounted bytes. If the database cannot restore the visible-history bounds despite those invariants, roll back, send no receipt, and enter storage unavailable. Evidence-cap failure follows the separate suspension/reconciliation rule in Section 8.4.

Only scalar-valid text from the Section 8.4 gate may enter content accounting or preview derivation. Derive a single-line last-message preview from the body by replacing each `\r`, `\n`, U+0085, U+2028, and U+2029 scalar with one ASCII space, then keep the longest complete Unicode-scalar prefix whose strict UTF-8 encoding is at most 256 bytes. Never split a scalar or store a replacement character. Persist the exact resulting byte count and include it in `accounted_content_bytes`. Layout ellipsis is presentation only and is not persisted. Exactly 256 bytes is accepted; a longer preview is truncated before the Room transaction commits.

If creating a conversation would exceed 1,000 shells, evict exactly one eligible shell in the same Room transaction before inserting the new shell. Choose deterministically by oldest local activity, then insertion sequence, then `conversation_id`, in this order: an empty shell with no active outbox; a shell whose retained messages are all read and which has no active outbox; then any shell with no active outbox. Never evict the conversation being updated, the new incoming/outgoing target, or a shell containing an active outbox row. Eviction cascades its retained messages and byte totals, updates the durable notification reconciliation generation so a stale system notification is canceled, and may remove unread history only in the final fallback tier. If no eligible shell exists, outgoing admission fails without a partial row; incoming insertion rolls back and enters storage unavailable without a receipt.

Apply the same message trim order for per-conversation and app-wide bounds. Keep the conversation shell and show one quiet "Older messages were removed from this device" marker while that shell remains retained; shell eviction removes the row entirely. Recalculate unread counts, preview bytes, and accounted byte totals in the same transaction. Composer drafts are in-memory only in MVP, so they do not create an unbounded second persistent-content store.

### 8.7 Clear-history and deletion semantics

**Clear conversation** confirms: "Delete this local history and stop its pending retries? Messages already accepted by the router may still be delivered." **Clear all history** uses the same warning for every conversation. On confirmation:

1. Mark the affected local conversation IDs as clearing in service memory and cancel matching in-flight OkHttp calls.
2. In one Room transaction, increment the durable reconciliation generation and delete the conversations. Foreign keys cascade to messages, outbox rows, unread state, and per-conversation notification projections; the singleton generation survives deletion as the desired-cancellation record so stale platform notifications remain discoverable work. `incoming_incarnation_evidence` has no conversation foreign key and is not deleted.
3. After commit, wake notification reconciliation and remove the IDs from the in-memory send queue. Direct best-effort cancellation may reduce latency, but it never replaces the durable Section 11.5 path.
4. Ignore late HTTP completion callbacks unless their outbox row still exists. They must never recreate a deleted row.

Cancellation cannot revoke a request already committed by the router. An in-flight message may therefore still reach its recipient if clear cancels the call after request-body transmission. Clear stops future app retries and removes local history; it is not recall. Incoming incarnation evidence also survives clear-one, clear-all, shell eviction, and message trimming until router settlement is proven, so an uncertain replay cannot resurrect cleared history. Clear does not change the install identity, endpoint, setup state, theme, Room-owned notification-preview preference, or connection service. Reset identity is the separate destructive action in Section 8.1.

Logical deletion commits before the UI reports success. SQLite/WAL byte reclamation is not a cryptographic erase guarantee. After clear-all, checkpoint the WAL and run bounded off-main-thread compaction only when the socket is already closed or the database is otherwise idle; compaction is not part of the UI success boundary. Uninstall or Android Clear storage remains the way to delete the entire app sandbox.

### 8.8 Room schema and migrations

Use Room foreign keys with `ON DELETE CASCADE` from conversation to messages, outbox rows, and per-conversation notification projections. `incoming_incarnation_evidence` deliberately has no foreign key to those visible-history tables. A retained incoming message may reference its evidence incarnation, but evidence owns no body and survives deletion of that message. Export every Room schema version into source control and provide explicit forward migrations from every shipped version. Release builds must not use `fallbackToDestructiveMigration`, delete/recreate the database after migration failure, or regenerate install identity because a database migration failed.

Migration tests start from each exported historical schema, populate valid, invalid-sender, unread, accepted, pending/in-flight outbox, delivery-unknown, trimmed-history, shell-eviction, visible incoming-incarnation, independent uncertain evidence, preview-policy, and dirty notification-reconciliation fixtures, migrate to the current schema, and assert exact sender keys, nullable peer IDs, evidence uniqueness and independence, exact request BLOBs and byte counts, preview bytes, capacity totals, local notification IDs, policy/reconciliation revisions, unread counts, and foreign-key behavior. A migration/open failure enters the storage-unavailable suspension in Section 8.4; it does not open the socket.

## 9. Registration and connected-agent discovery

### 9.1 Exact registration schema extension

Add these definitions to `protocol/v1/schema.json`:

```json
{
  "agentRole": {
    "type": "string",
    "enum": ["agent", "user_interaction"]
  },
  "clientType": {
    "type": "string",
    "minLength": 1,
    "maxLength": 32,
    "pattern": "^[a-z][a-z0-9_-]{0,31}$"
  },
  "ackConfirmationMode": {
    "type": "string",
    "const": "correlated_v1"
  }
}
```

The complete changed portion of `registerFrame` is exact:

```json
{
  "type": "object",
  "additionalProperties": false,
  "required": ["version", "frame_id", "type", "agent_id", "instance_id"],
  "properties": {
    "version": { "const": "v1" },
    "frame_id": { "$ref": "#/$defs/frameId" },
    "type": { "const": "register" },
    "agent_id": { "$ref": "#/$defs/agentId" },
    "instance_id": { "$ref": "#/$defs/instanceId" },
    "agent_role": { "$ref": "#/$defs/agentRole" },
    "client_type": { "$ref": "#/$defs/clientType" },
    "ack_confirmation": { "$ref": "#/$defs/ackConfirmationMode" }
  }
}
```

All three new request fields are optional for wire compatibility. The router applies exact defaults when absent:

- `agent_role`: `agent`
- `client_type`: absent/null in the in-memory diagnostic session value; no string default is invented
- `ack_confirmation`: absent/null; successful ACKs retain legacy no-confirmation behavior

Android always sends `agent_role: "user_interaction"`, `client_type: "android"`, and `ack_confirmation: "correlated_v1"`. Current Pi sends none of them and continues to validate. Role and client type are claimed metadata, not authorization, identity proof, delivery priority, or user-facing identity. ACK confirmation is only a transport-capability negotiation.

Add `agent_role` to `registeredFrame.properties` using `$defs.agentRole` and to its required array. Add optional `ack_confirmation` using `$defs.ackConfirmationMode`. The router always returns the effective role, including `agent` for legacy registration, and echoes `ack_confirmation: "correlated_v1"` on every initial and renewal `registered` frame only for a session that requested it. It does not echo `client_type`. Android does not enter connected state unless the initial response echoes the mode. The current Pi asks for no confirmation and receives no `ack_confirmation` property or confirmation frame; fixture and live compatibility tests still prove this rather than assuming it.

The router stores effective role, optional client type, and negotiated ACK-confirmation mode only on the active in-memory session. Registration captures `connected_at` from the lease's existing `acquired_at`. Every successful registration or renewal updates the session's current `lease_expires_at` from the repository result. No database columns are needed for role/discovery metadata.

### 9.2 Exact discovery endpoint

Add normal HTTP `GET /v1/agents` with no request body. Its response schema is:

```json
{
  "$defs": {
    "discoveredAgent": {
      "type": "object",
      "additionalProperties": false,
      "required": ["agent_id", "agent_role", "connected_at", "lease_expires_at"],
      "properties": {
        "agent_id": { "$ref": "https://mecha.local/protocol/v1/schema.json#/$defs/agentId" },
        "agent_role": { "$ref": "https://mecha.local/protocol/v1/schema.json#/$defs/agentRole" },
        "connected_at": { "$ref": "https://mecha.local/protocol/v1/schema.json#/$defs/utcMillis" },
        "lease_expires_at": { "$ref": "https://mecha.local/protocol/v1/schema.json#/$defs/utcMillis" }
      }
    }
  },
  "type": "object",
  "additionalProperties": false,
  "required": ["generated_at", "agents"],
  "properties": {
    "generated_at": { "$ref": "https://mecha.local/protocol/v1/schema.json#/$defs/utcMillis" },
    "agents": {
      "type": "array",
      "items": { "$ref": "#/$defs/discoveredAgent" }
    }
  }
}
```

Example:

```json
{
  "generated_at": "2026-07-14T12:00:00.000Z",
  "agents": [
    {
      "agent_id": "supervisor",
      "agent_role": "agent",
      "connected_at": "2026-07-14T11:55:00.000Z",
      "lease_expires_at": "2026-07-14T12:00:20.000Z"
    }
  ]
}
```

Endpoint behavior is exact:

- Return HTTP 200, `Content-Type: application/json; charset=utf-8`, and `Cache-Control: no-store`.
- Generate `generated_at` from the router clock in exact UTC-millisecond form.
- Include a session only when it is still the current entry for that agent, its socket is open, and its matching repository lease exists and is unexpired at `generated_at`.
- Read role, connected time, and the latest lease expiry from that active session. Never query historical `agents` rows to build presence.
- Return no instance ID, lease ID, client type, sender key, display label, or historical/disconnected agent.
- Sort by exact `agent_id` code-unit order, ascending.
- Include the requesting mobile agent if active; Android filters its own exact ID from the picker.
- Treat the response as a snapshot. It does not promise the agent remains connected after the response.
- Preserve browser `Origin` rejection and normal no-body logging.

Android validates the entire response against this shape before replacing presence. One invalid response leaves the previous snapshot marked stale. It refreshes after registration, foreground resume, reconnect, manual refresh, and every 10 seconds while the home screen is visible. It does not poll presence continuously in the background.

### 9.3 Message incarnation, scalar validity, API, and enqueue expiry

Add this exact definition to `protocol/v1/schema.json`:

```json
{
  "messageIncarnationId": {
    "type": "string",
    "minLength": 36,
    "maxLength": 36,
    "pattern": "^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$"
  }
}
```

Add required property `message_incarnation_id` to `messageFrame`, referencing `$defs.messageIncarnationId`. It is a sibling of `message`, `attempt_number`, and `delivery_expires_at`; it is not part of the sender envelope or the envelope's idempotency comparison.

Add this server-frame definition and include it in `serverFrame.oneOf`:

```json
{
  "deliveryAcknowledgedFrame": {
    "type": "object",
    "additionalProperties": false,
    "required": [
      "version", "frame_id", "type", "request_id", "agent_id",
      "instance_id", "lease_id", "message_id",
      "message_incarnation_id", "attempt_number"
    ],
    "properties": {
      "version": { "const": "v1" },
      "frame_id": { "$ref": "#/$defs/frameId" },
      "type": { "const": "delivery_acknowledged" },
      "request_id": { "$ref": "#/$defs/frameId" },
      "agent_id": { "$ref": "#/$defs/agentId" },
      "instance_id": { "$ref": "#/$defs/instanceId" },
      "lease_id": { "$ref": "#/$defs/leaseId" },
      "message_id": { "$ref": "#/$defs/messageId" },
      "message_incarnation_id": { "$ref": "#/$defs/messageIncarnationId" },
      "attempt_number": { "$ref": "#/$defs/attemptNumber" }
    }
  }
}
```

For a session that negotiated `correlated_v1`, a successful `ack` transaction captures the queued row's incarnation, atomically deletes the row and attempts, then sends exactly one `delivery_acknowledged` whose `request_id` is the ACK frame ID and whose delivery/lease fields match the deleted attempt. Send this confirmation before offering the next queued message on that socket. A frame is settlement evidence only when every field and the current socket generation match. A failed confirmation send does not roll back deletion. Duplicate, stale, timed-out, or invalid ACKs retain existing error behavior and receive no false confirmation. Sessions without the negotiated mode receive no confirmation frame, preserving legacy Pi behavior.

Migration `005_message_incarnation` adds one non-null unique incarnation column to queued messages and backfills every existing row with a distinct cryptographically random lowercase UUIDv4. On every first insert, including server-job insertion, the router generates and stores one new UUIDv4. An identical POST while that row exists returns the same row and incarnation. A conflicting POST while it exists remains `409 idempotency_conflict`. After `ack` deletes the row, a later insert using the same envelope ID gets a new incarnation. Every offer/retry for one row emits its stored incarnation unchanged. Successful `POST /v1/messages` and `GET /v1/messages/{id}` message representations include the stored `message_incarnation_id`; a lookup still returns 404 when no row with that message ID exists. The incarnation is transport-only and is never accepted from an HTTP sender, rendered, or treated as authentication. Existing Pi receive code may ignore the added `message` frame field; schema fixtures and the live compatibility test must prove that it still processes and receipts the message.

Before the router applies JSON Schema, idempotency comparison, logging, or persistence to any HTTP or WebSocket document, it fatally decodes UTF-8 and recursively rejects any member name or string value that is not a Unicode-scalar sequence under Section 8.4. `POST /v1/messages` maps such input to `400 invalid_request` with no candidate row, fingerprint, payload-derived log field, or replacement character. JSON Schema `minLength`/`maxLength` are evaluated as Unicode scalar counts. This custom gate is required even when the JSON parser accepts escaped lone surrogates.

Do not add a mobile send endpoint. Android uses the existing `POST /v1/messages`, and the router offers it through the same queue and WebSocket delivery path as every other mailbox message.

On first insert, `enqueueCandidate` must calculate `expires_at` from the router-supplied `firstSeenAt`, not envelope `created_at`. On an existing identical ID, it returns the already stored row, expiry, and incarnation without extending or changing them. The HTTP response may continue to include `expires_at`; the WebSocket envelope remains unchanged and does not gain a server timestamp. The new confirmation is a receipt result, not message history or a sender-visible delivery claim. This uses the router's existing clock and persistence boundary without inventing sender authentication.

This preserves one delivery model:

```text
Android -> POST /v1/messages -> router queue -> Pi WebSocket -> Pi history
Pi/agent -> POST /v1/messages -> router queue -> Android WebSocket -> Room history
```

## 10. Connection lifecycle and startup preflight

### 10.1 Foreground and background

After setup and a passing Phase 0 decision, the foreground service owns the socket, renewal watchdog, and outbox worker. The Activity can come and go without changing the install identity or clearing history.

The service:

- Starts from a visible Activity under the spike's approved manifest path and posts its ongoing notification within Android's required deadline.
- Connects, sends one registration frame, and accepts connection state only from a schema-valid `registered` response whose `request_id`, agent ID, instance ID, effective role, and `ack_confirmation: "correlated_v1"` match that request.
- Renews and watches the lease under Section 10.2.
- Reconnects with full-jitter exponential backoff, 500-millisecond base and 30-second cap.
- Resets socket backoff only after valid registration, not merely TCP or WebSocket open.
- Uses Android connectivity callbacks to make one reconnect/outbox attempt prompt, but does not treat callbacks as proof that the router is reachable.
- Persists incoming messages before receipt and suspends all network work when Room is unhealthy.
- Maintains the required ongoing local connection notification.

The app's logical agent identity and history are long-running. Android still controls process lifetime. A foreground service improves background continuity but cannot guarantee delivery after force-stop, process prohibition, device shutdown, reboot without an explicit boot feature, or OEM battery intervention.

### 10.2 Socket-establishment and lease-renewal watchdogs

Each connection attempt gets an in-memory generation token and records `socket_attempt_started_elapsed` from Android's monotonic elapsed-realtime clock immediately before `OkHttpClient.newWebSocket`. Arm one 15-second opening deadline and one 20-second overall opening-plus-initial-registration deadline at that point. The opening deadline is satisfied only by `onOpen`; dispatcher wait, DNS, TCP, TLS, and HTTP upgrade consume it. In `onOpen`, send exactly one register frame immediately, record its request ID, and arm a 5-second initial-registration deadline without extending the 20-second overall deadline. The earliest applicable deadline wins.

Only a schema-valid `registered` frame matching that request ID, install agent ID, service instance ID, `agent_role: "user_interaction"`, and `ack_confirmation: "correlated_v1"` cancels the overall and registration deadlines and permits connected state. `onOpen`, unrelated/malformed frames, pongs, or traffic do not reset them. A matching `duplicate_identity` rejection cancels them and enters Section 10.3. Any deadline expiration cancels the WebSocket, invalidates its generation token, clears provisional lease state, and enters normal reconnect. Every callback and timer verifies the generation token, so a late `onOpen`, `registered`, failure, or close from an abandoned attempt cannot mutate the current attempt. These deadlines use no wall-clock timestamp, advertised lease expiry, or Android network callback.

The current deployment contract is a lease of at least 30 seconds and renewal every 10 seconds. Router configuration and integration tests must prove the lease is at least 30 seconds before Android connects.

After each valid initial or renewal `registered` response:

1. Save the returned lease ID and exact advertised `expires_at` for diagnostics.
2. Record `last_lease_confirmation_elapsed` using Android's monotonic elapsed-realtime clock.
3. Clear the matching outstanding renewal request.
4. Schedule the next renewal for 10 seconds later.
5. Arm a hard monotonic watchdog for 20 seconds after the last valid confirmation.

At most one renewal is outstanding. A renewal sends a new `frame_id` and requires a valid `registered` response with that exact `request_id`, current install agent ID, service instance ID, current lease ID, `agent_role: "user_interaction"`, and `ack_confirmation: "correlated_v1"`. A mismatched, stale, malformed, capability-downgraded, or unrelated frame does not reset the watchdog.

If a renewal has no matching response within 5 seconds, or no valid lease confirmation has arrived for 20 monotonic seconds, whichever occurs first, close the socket, clear the in-memory lease, and enter normal reconnect. The watchdog does not depend on wall-clock comparison with `expires_at`, so device/server clock skew cannot keep a dead socket marked connected. A late frame from the closed socket is ignored.

### 10.3 Reconnect, duplicate lease, and suspension

On ordinary socket loss:

- Keep cached conversations available.
- Move connection state to reconnecting.
- Keep local outbox rows pending.
- Open a new socket with the same service `instance_id` while that service remains alive.
- Register again and refresh connected agents.

On `duplicate_identity`:

- Do not generate a surprise new install identity.
- Explain that the address is active elsewhere.
- Close the rejected socket, wait at least 31 seconds from rejection, then resume bounded reconnect attempts.
- Offer the destructive Reset app identity action, not an arbitrary identity editor.
- Do not add automatic forced takeover in MVP.

Storage-unavailable and dedup-evidence-full states are not ordinary disconnects. They cancel reconnect, renewal, presence, and outbox timers and keep the socket closed until the explicit recovery gate in Section 8.4 passes. This prevents one broken or safety-exhausted Room installation from dead-lettering inbound messages or accepting a replay without evidence.

After one hour disconnected, the current router may remove the mobile agent record and cascade-delete waiting router messages. Reconnect creates a fresh router registration. Local Room history is unaffected.

### 10.4 Startup preflight

Run these gates in order before opening the WebSocket:

1. **Reset recovery:** read DataStore before treating missing identity as a new install. If `reset_pending` exists, run the idempotent Section 8.1 reset sequence to completion and restart preflight. No Room, notification, identity generation, or network work may bypass this gate. An unreadable DataStore fails closed into the platform clear-storage path.
2. **Install identity:** atomically read or create the exact install ID under Section 8.1. Validate it against `$defs.agentId`. A post-setup read/corruption error fails closed.
3. **Storage:** open Room, run every required migration, verify foreign keys are enabled, assert evidence count is at most 10,000, and complete a small write transaction. Failure enters storage unavailable. Start durable message-notification reconciliation after this gate, so previously committed notification intent does not depend on router replay.
4. **Endpoint:** select the build-owned base URL, validate it against Section 13.3, derive health/message/discovery/WebSocket URLs without dropping the base path, and reject user info, query, fragment, redirects, cleartext release traffic, or any non-allowlisted host/port.
5. **Router health:** GET `/v1/health` and require HTTP 200, `status: "ok"`, `version: "v1"`, and at least the current ordered migration IDs `001_initial`, `002_delivery_attempt_deadline`, `003_reminders`, `004_server_jobs`, and `005_message_incarnation`. Current health does not expose a code build or capability list, so it cannot prove the role/discovery, ACK-confirmation, or server-expiry changes; deployment contract tests and the negotiated registration gate provide that evidence without inventing another health mechanism.
6. **Evidence recovery:** run one serialized bounded recovery pass over uncertain incarnation evidence using `GET /v1/messages/{id}`. Inconclusive rows remain durable. If the table is full and no row can be proven settled, enter dedup evidence full without opening the socket.
7. **Foreground-service eligibility:** require the Phase 0 go decision for the exact SDK/device/distribution tuple and start only through the approved visible-app path.
8. **Registration:** open WSS/WebSocket; register with exact Android role, client type, and ACK-confirmation mode; require the matching capability-echoing `registered` response; then GET and validate discovery. Only then report connected and start normal outbox draining.

Every health, discovery, and message HTTP call uses OkHttp `callTimeout` of 15 seconds. This is a whole-call deadline covering dispatcher wait, DNS, connect, TLS, request write, server processing, and response read; connect/read/write settings may be shorter but cannot extend it. Stream and cap decoded response bodies before parsing: 32 KiB for health, 256 KiB for discovery, and 64 KiB for message enqueue. An empty, truncated, oversized, malformed, or shape-invalid response fails that operation. The long-lived WebSocket is not subject to a 15-second lifetime; its opening is bounded to 15 monotonic seconds, its opening plus matching initial registration to 20 monotonic seconds, and its registration response to five monotonic seconds after `onOpen` under Section 10.2. Renewal and established-socket liveness use the separate deadlines there.

Notification denial is not a connection-preflight failure. It leaves in-app unread behavior active.

### 10.5 Offline limits

Without a remote notification service, background notification depends on the foreground service retaining or restoring its socket. If the socket is unavailable:

- With the server-enqueue fix, an unclaimed inbound message remains eligible for at most one hour after the router first accepts it, independent of sender clock.
- No local incoming notification can exist until the app receives the message.
- Messages beyond the router's pickup deadline can disappear without a mobile tombstone.
- The UI must not promise complete offline history.

These are router semantics, not app bugs.

## 11. Local Android notifications and unread behavior

### 11.1 Notification channels

Create two local notification channels with stable IDs scoped to the final application ID:

- `mailbox_connection`: low importance, ongoing foreground-service status.
- `agent_messages`: normal/default importance for incoming messages, with user-controlled sound and vibration through Android channel settings.

The app requests `POST_NOTIFICATIONS` in context after explaining why. If permission is denied, incoming messages still persist and become unread in-app. The app does not repeatedly prompt. Channel creation is idempotent and never deletes/recreates a user-customized channel during an app update.

### 11.2 Stable notification identity

Notification identity comes only from immutable local `conversation_id`, never `sender_key`, peer ID, label text, or a hash with collision risk:

- Conversation notification: tag `conversation:<conversation_id>`, numeric ID `1`.
- Group summary: tag `mailbox-summary`, numeric ID `1`.
- Foreground service: tag `mailbox-connection`, numeric ID `1`.

Android distinguishes notifications by the `(tag, id)` pair. Clear, read, update, and migration paths use the same pair. AUTOINCREMENT conversation IDs are not reused within a database, so a stale notification or tap cannot target a newly created conversation after clear.

Each conversation notification uses a `MessagingStyle.Person` whose display name is the safe Section 6.4 label and whose key is `conversation:<conversation_id>`. Do not use the raw claimed sender as `Person.name`, `Person.key`, shortcut ID, locus ID, category, tag, URI, or action.

After the incoming Room transaction commits, the Section 11.5 reconciler applies the persisted desired state:

- Post or update one notification per conversation from current Room state, never by incrementally appending an in-memory frame.
- Include at most the latest stored 256-byte preview and a bounded unread count in `MessagingStyle`; rebuilding the same revision is byte/content equivalent and cannot duplicate a style entry.
- Group multiple conversations under the app summary when appropriate.
- Use `VISIBILITY_PRIVATE` and a redacted public version for lock screens.
- Mark notifications local-only so Android does not bridge message content to companion devices by default.
- Show a bounded preview only when the user has enabled previews.
- Never include a message ID, raw claimed invalid sender, router URL, protocol state, or raw error details.

Do not post a redundant system notification when that exact local conversation ID is visible, resumed, and already positioned at the newest message. If the user is reading older messages, retain unread state and show the in-conversation new-message affordance.

### 11.3 Exact tap routing

Each conversation notification creates an explicit Intent for the single Activity:

- Component is set explicitly to the Activity.
- Package is the app package.
- Action is `<applicationId>.OPEN_CONVERSATION.<conversation_id>`.
- The only routing extra is the local `conversation_id`.
- `PendingIntent.getActivity` uses request code `0` and `FLAG_UPDATE_CURRENT | FLAG_IMMUTABLE`.

The unique action, not the extra, makes each `PendingIntent` a distinct Android identity. The group-summary and connection intents use different fixed actions and open home. There is no exported deep link.

On tap:

1. Bring the single Activity forward.
2. Parse the local ID as a positive database ID and query Room.
3. Open that conversation only if it still exists; otherwise open home and cancel the stale notification.
4. Render persisted history.
5. Mark only incoming messages actually presented after the detail screen is resumed.
6. In the same Room transaction, update only that conversation's desired notification state and increment the reconciliation generation. Wake the reconciler, which cancels or updates only that `(tag, id)` pair.

A malformed, missing, deleted, or unknown local ID opens home. No raw sender value crosses the Intent boundary.

### 11.4 Unread rules

An incoming message is unread when it commits unless its local conversation is currently resumed and at the newest content.

- Home row shows a count badge, capped visually at `99+`.
- Opening a conversation marks the messages actually presented to the user as read.
- Notification tap follows the same rule.
- Returning to home updates the count immediately.
- Read transitions persist desired absence, and clear-conversation, clear-all, and shell eviction persist a newer singleton generation before reporting success.
- Duplicate router delivery does not increment unread or notification revisions twice.
- Notification-post or cancellation failure does not roll back or dead-letter an already persisted message. It leaves bounded reconciliation work dirty.

No remote push token, FCM SDK, backend device registration, or cloud notification payload exists in MVP.

### 11.5 Durable idempotent notification reconciliation

Room is the source of notification intent. Platform notification calls are side effects and are never treated as transactional with message commit or router `ack`.

Keep a bounded notification projection for each retained conversation and one singleton reconciliation row:

- The per-conversation projection stores only immutable `conversation_id`, desired state (`present` or `absent`), `desired_revision`, `applied_revision`, and `applied_policy_revision`. Message text is not copied into this table. Its row is bounded by the 1,000-shell cap and cascades with the conversation.
- The singleton is the sole persisted source for `previews_enabled`, initialized to `false`, plus monotonically increasing `policy_revision`, `desired_generation`, and `applied_generation`. DataStore, saved state, ViewModels, and notification extras contain no second authoritative preference value.
- A settings change runs one Room transaction. If the value actually changes, it writes `previews_enabled`, increments `policy_revision`, and increments `desired_generation` exactly once before the UI reports success. A crash can therefore leave reconciliation dirty but cannot commit a preference without durable work to rewrite existing active notifications.
- Every transaction that changes desired notification presence or content, marks messages read, clears or evicts a conversation, or changes summary membership increments `desired_generation` exactly once. Deleting a conversation removes its projection but leaves the singleton generation dirty. This is the durable cancellation tombstone: reconciliation enumerates active app-owned `conversation:<positive-id>` notifications and cancels any ID that has no retained desired-present projection. It does not accumulate one tombstone row per deleted conversation.

One serialized reconciler runs after Room opens, after each relevant commit, on service start, on Activity resume, and after preview preference, notification permission, or channel state changes. For one Room snapshot containing the generation, preview value, policy revision, dirty projections, and bounded desired-present set, it:

1. Enumerates active app message notifications and reads notification content only from that same Room snapshot. It never reads a cached or DataStore preview value.
2. Treats a projection as content-dirty when `desired_revision != applied_revision` and policy-dirty when `applied_policy_revision != policy_revision`. A dirty `present` row is rebuilt deterministically with or without preview according to the snapshotted Room value. Content-dirty rows are posted even when no active notification exists. A policy-only dirty row is replaced only when its stable pair is currently active; if it is absent because the user dismissed it, reconciliation advances only its policy marker and does not resurrect it.
3. For `absent`, calls `cancel` with the stable pair. It also cancels active conversation tags absent from the desired-present set and replaces or cancels the fixed group summary from the same snapshot.
4. After each successful replacement/cancellation, conditionally advances that projection's `applied_revision` and `applied_policy_revision` only when its desired revision, policy revision, and desired state still match the snapshot. A policy-only dismissed row advances `applied_policy_revision` under the same compare-and-set condition without calling `notify`.
5. Advances singleton `applied_generation` to the snapshotted generation only when the desired generation and policy revision are still equal and no operation through either remains dirty. Otherwise it loops on the newer state.

Disabling previews rewrites every currently active conversation notification to remove message content; enabling them rewrites active notifications from persisted bounded previews. A crash after the preference transaction but before `notify`, or after `notify` but before applied markers, converges on restart from Room alone. `notify` replaces an existing notification with the same pair and `cancel` is idempotent. A posting failure retains the dirty revision for a later wake; permission denial records desired absence rather than retrying forever. User dismissal is not dirty work and a preview-policy change does not resurrect it; only a later incoming message creates the content revision that may post again.

The incoming Room transaction commits both projection and independent incarnation evidence before either receipt. The receipt path never waits for NotificationManager:

- Crash after commit but before notification or receipts: startup reconciliation sees dirty durable intent; router replay consults evidence rather than visible history and wakes the same work.
- Crash after notification but before its applied marker: reconciliation replaces the same `(tag, id)` with the same persisted content.
- Crash after router processes `ack` but before Android receives `delivery_acknowledged` or posts the notification: startup reconciliation still posts from Room, while the retained evidence is later settled by recovery GET if no replay exists.
- Crash after notification but before `ack`: replay changes neither unread nor desired revision and returns receipts for the active attempt.
- Crash after read, clear, trim, or shell eviction commits but before cancellation or ACK settlement: the dirty notification generation removes stale UI side effects, and independent evidence prevents replay from recreating deleted history.

The ongoing foreground-service notification is owned by service lifecycle and is outside this message-notification projection.

## 12. Error handling

### 12.1 HTTP send errors

Map router outcomes to product behavior under the exact ambiguity boundary in Section 8.5:

- `400 invalid_request`: mark failed and show "This message couldn't be sent." Editing creates a new envelope and ID.
- `413 payload_too_large`: mark failed, retain the local body for editing, and show "This message is too long."
- `409 idempotency_conflict`: mark failed, stop retry, and log only the bounded local category.
- `429 rate_limited`: retain pending only within the eight-attempt budget and apply the exact bounded `Retry-After` rule.
- `429 queue_full`: retain pending only within the same budget and show "This agent can't accept messages right now."
- DNS, connect, TLS, dispatcher, or timeout failure proven before request-body transmission: retain pending only within the same budget.
- Any timeout, socket loss, cancellation, 3xx/5xx, unknown 4xx, or invalid/mismatched/empty/oversized response after request-body transmission may have been accepted. Mark delivery unknown and do not retry that ID.

Never show or log the raw router response body. "Failed" means a definite rejection or exhausted sequence of definite non-enqueues. "Delivery unknown" means the app cannot tell whether the router accepted the message.

### 12.2 WebSocket errors

- OkHttp transport failure while reading a text frame, invalid server JSON, any unpaired surrogate observable in the callback JSON/member names/values, or unsupported version: apply the exact no-receipt/safe-identity-`nack` rule in Section 8.4 when a valid delivery identity is available, then close and reconnect. Do not report byte-level invalid-UTF-8 detection unless OkHttp itself surfaced that transport failure. Log metadata without content.
- Invalid message envelope: send `nack` when a valid delivery identity is available, then keep the socket unless the protocol is no longer trustworthy. A reused incarnation with any changed envelope field is untrustworthy and closes.
- Lease not active: close, discard the in-memory lease, and register again.
- Duplicate identity: use the dedicated product state.
- Local storage full, corruption, open failure, transaction failure, or migration failure: send no receipt or `nack`; close and suspend the socket, renewal, reconnect, presence, and outbox until the storage recovery gate passes.
- Incarnation-evidence cap with no conclusively reclaimable row: send no receipt or `nack`, close and suspend new delivery, and let **Retry storage** rerun bounded GET reconciliation. Never evict an uncertain row to recover.
- Notification permission or posting failure: acknowledge after persistence and retain unread state.

### 12.3 App and router restart

- App restart reruns reset recovery before normal preflight, creates a new service `instance_id`, and registers the same install-scoped `agent_id` unless a completed explicit reset removed it. It never regenerates identity because Room or DataStore failed.
- Router restart causes normal WebSocket reconnect. Router SQLite retains queued messages.
- A crash after local insert but before `ack` replays the router message. The stable queued-row incarnation plus independent digest evidence deduplicates it locally even if visible history was later trimmed or cleared; notification intent is reconciled rather than incremented.
- A crash after router processes `ack` but before confirmation or notification still posts from durable Room intent on restart. The evidence row remains until correlated confirmation or recovery GET proves the router row absent/different.
- A crash after a notification side effect but before its applied marker safely repeats the same fixed-identity replacement or cancellation.
- A crash with an in-flight HTTP row becomes delivery unknown on restart and never retries that ID. Pending rows for which no attempt started may still drain normally.
- A crash after clear or shell eviction cannot recreate deleted outbox rows, visible incoming history, or a permanent stale notification; late callbacks update only rows that still exist, evidence absorbs inbound replay, and reconciliation cancels absent notification IDs.
- A crash during Reset app identity either leaves old data untouched before `reset_pending`, or resumes the marker-driven Room deletion/fresh-open/DataStore-finalize sequence before any normal startup.

## 13. Privacy and security

### 13.1 Data at rest

- Store conversations only in the app-private Android sandbox.
- Rely on Android file-based device encryption for MVP. Do not add SQLCipher without a separate threat-model decision.
- Disable Android cloud backup and device-to-device transfer for Room, DataStore identity/setup, exact outbox JSON, endpoints, and message content. Put identity/setup in no-backup storage as defense in depth.
- Do not write bodies, subjects, raw claimed invalid senders, exact envelope JSON, or notification previews to Logcat, analytics, crash breadcrumbs, or router logs. Do not pass an envelope, raw response, validation exception message, or other payload-derived `Throwable.message` to a logging API.
- Apply the transactional logical deletion, callback suppression, WAL checkpoint, and bounded compaction rules in Section 8.7. Do not claim cryptographic erasure from SQLite row deletion.

### 13.2 Notification privacy

- Lock-screen public content says only that a new agent message arrived.
- Message preview is off for lock-screen public content.
- The user can disable unlocked notification previews without disabling unread notifications.
- Connection notification contains endpoint status, not message content or the full router URL.

### 13.3 Canonical endpoints, allowlists, and path derivation

Emulator debug default:

```text
base       http://127.0.0.1:8765
health     http://127.0.0.1:8765/v1/health
messages   http://127.0.0.1:8765/v1/messages
discovery  http://127.0.0.1:8765/v1/agents
socket     ws://127.0.0.1:8765/v1/agents
```

ADB reverse maps that exact base to the Mac loopback listener. Cleartext permission is debug-only and scoped to loopback. A separately named debug fallback may allow `10.0.2.2:8765` only after its test gate passes. No debug path permits a LAN host. Neither route authenticates the emulator app or host OS user; the development boundary trusts every process that can reach the mapped endpoint.

Physical release is fixed:

```text
base       https://donuts.tailcdfd89.ts.net:8443
health     https://donuts.tailcdfd89.ts.net:8443/v1/health
messages   https://donuts.tailcdfd89.ts.net:8443/v1/messages
discovery  https://donuts.tailcdfd89.ts.net:8443/v1/agents
socket     wss://donuts.tailcdfd89.ts.net:8443/v1/agents
```

Release URL validation allows only scheme `https`, lowercase canonical host `donuts.tailcdfd89.ts.net`, explicit port 8443, and an empty or `/` base path. User info, query, fragment, another port, IP literal, Unicode lookalike, noncanonical host, and cleartext are rejected. Derive each HTTP route by appending relative path segments to the validated base. Derive WebSocket by changing only `https` to `wss` and appending the same `v1/agents` segments. Do not resolve a leading `/v1/...` against a potentially prefixed arbitrary URL or replace the host from configuration. Redirects stay disabled; a 3xx is a connection failure.

Configure a dedicated Tailscale Serve HTTPS listener on 8443 that proxies to `http://127.0.0.1:8765`. This leaves the existing HTTPS-443 root proxy to `127.0.0.1:7860` and the unrelated Tailscale-IP listener on 8765 unchanged. Do not use a path mount or depend on path-prefix rewriting.

The initial Tailnet allowlist is the observed phone node `note.tailcdfd89.ts.net` (`100.100.214.15/32`) as source and this Mac `donuts.tailcdfd89.ts.net` (`100.115.63.19/32`) TCP 8443 as destination. A broad same-user or whole-tailnet rule does not satisfy the gate. Commit and inspect the narrow Tailnet ACL/grant before physical testing, then verify a second Tailnet node cannot connect. If the phone is replaced, update the reviewed policy and release fixture; do not silently broaden it.

Keep the router on loopback. Do not enable Funnel, bind the router to `0.0.0.0`, expose Wi-Fi/LAN, or add a pairing secret. The release app trusts only the normal Android trust store for the Tailnet HTTPS certificate. Certificate bypass, trust-all TLS, user-installed debug CAs, and an HTTP `Origin` header are forbidden.

## 14. Observability

### 14.1 Android diagnostics

Log structured, body-free events in debug builds:

- Connection state transition.
- Registration success/rejection category.
- Presence refresh success/count.
- Incoming message ID hash, local conversation ID when available, attempt number, `sender_valid: true|false`, deduplicated flag, and persistence result.
- Outgoing message ID hash, local conversation ID, HTTP phase/outcome category, request-body-started boolean, and retry count.
- Notification desired/applied revision, reconciliation outcome, posted/suppressed/permission-denied category keyed only by local conversation ID, plus the body-free singleton generation.
- Room migration, capacity rejection, accounted-byte, preview-byte, shell-eviction, and retention-trim counts.

Invalid `from` is hostile log input. Android logs only the boolean validity and the local conversation ID after persistence. They never log the raw value, its derived label or fingerprint, a validator data dump, a Unicode-escaped copy, or an exception message containing it. Logging entry points at the protocol boundary accept only the listed preselected scalar fields, not an envelope, response body, arbitrary map, or `Throwable`. Release logging is minimal and contains no message content, exact agent identities, full endpoints, lease IDs, raw frames, or claimed sender values.

The normal UI has no diagnostics dashboard. A future explicit support export may expose bounded metadata, but it is not MVP.

### 14.2 Router diagnostics

Extend body-free router events to include:

- Registration role and bounded client type where safe.
- Connected-agent list request count, not returned identities in a new body-bearing log.
- Existing enqueue, delivery, timeout, nack, ack, and disconnect transitions.

The envelope `from` field is not an `agentId` and is hostile log input. Remove it from `message_enqueued` and do not add it to any router event, even when a particular value happens to match `agentId`. Router payload-validation paths log only bounded error codes and validated recipient/message metadata where already allowed. They never log raw payloads, validator data, or payload-derived exception messages. Body-free is not enough if an invalid sender can forge lines, add bidi controls, or disclose its text.

Do not log mobile notification content or local unread state on the router.

## 15. Validation and emulator end-to-end plan

### 15.1 Test-lane split

Keep deterministic contract tests separate from the live two-Pi and visual gates:

1. **Router/Node contract tests**
   - Legacy Pi register without metadata/capability and Android register with exact role, client type, and `ack_confirmation: "correlated_v1"` both validate. Legacy responses omit the mode; Android initial and renewal responses echo it.
   - Every `registered` frame contains the effective role and preserves request correlation. Android refuses a missing or downgraded ACK-confirmation echo.
   - Discovery includes only open, current, unexpired sessions; sorts exactly; updates renewal expiry; defaults legacy role; excludes client/instance/lease IDs; rejects browser Origin; and returns no-store.
   - Past and future sender-claimed timestamps cannot change the one-hour server-enqueue expiry. An identical retry while the row exists keeps its original expiry. Any changed idempotency field conflicts.
   - Migration `005_message_incarnation` backfills distinct valid UUIDv4 values. A first insert creates one incarnation; identical POST and delivery retries preserve it; every `message` frame and successful POST/GET representation requires it; `ack` deletes the row; GET then returns 404; and a later POST of the same envelope ID creates a different incarnation. Legacy Pi receives and receipts the extended frame unchanged.
   - A negotiated successful ACK commits deletion before exactly one fully correlated `delivery_acknowledged`, and the confirmation is sent before the next offer. Drop the confirmation after deletion to prove no rollback or false replay guarantee. Stale/duplicate/failed ACKs get no confirmation. A legacy Pi ACK gets no new frame.
   - The no-tombstone boundary remains explicit: incarnation distinguishes queued occurrences but does not retain outbound idempotency after `ack`. Tests fail if it is accepted from the sender, derived from the envelope ID, reused after deletion, or treated as permission to retry an ambiguous Android POST.
   - Every defined 400/409/413/429 response leaves the submitted candidate uncommitted. Queue-full and rate-limit tests assert no candidate row before Android treats those responses as safe to retry.
   - Raw invalid UTF-8 and escaped lone-high, lone-low, reversed, and truncated surrogate fixtures in object names, `from`, subject, and body return `400 invalid_request` before schema/idempotency/database work. Database row counts and captured logs remain unchanged. Valid surrogate pairs, literal U+FFFD, controls, bidi scalars, and supplementary scalars remain exact; scalar-count length boundaries are proved independently of UTF-8 byte boundaries.
   - An adversarial `from` containing newline, JSON delimiters, control characters, bidi controls, and a unique secret marker never appears in any captured router event field or payload-derived error. Every serialized event remains one parseable record.
   - HTTP and WebSocket serialization fixtures use exact UTC milliseconds and reject extras.
   - A second ordinary local process can connect to the loopback listener and claim protocol identity without OS-user proof. The test treats loopback/Origin/file permissions as reachability and browser/data protections, never same-user authentication.

2. **Android local JVM tests**
   - Install identity generation happens once, survives update/restart, and fails closed on post-setup DataStore error. Reset state-machine tests crash before marker commit, after marker commit, after Room delete, after fresh-Room verification, and before/after final DataStore clear; each restart either preserves the complete old install or resumes to one empty install with one new ID, never a mixed Room/DataStore state. An unreadable marker store invokes only the platform clear-data fallback. A normal installable debug APK has no identity override and generates securely after Clear storage; only the test artifact can construct the fixed identity fixture.
   - Release/debug endpoint allowlists and all five derived URLs match Section 13.3; redirects and prefix-dropping cases fail.
   - Valid IDs, scalar-valid arbitrary Unicode/control/bidirectional `from` values, empty-looking labels, fingerprints, and nullable peer IDs follow Sections 6.4 and 8.2.
   - The observable-string scalar validator rejects unpaired UTF-16 in an OkHttp callback string and escaped lone-high, lone-low, reversed, truncated, and high-followed-by-non-low surrogates after JSON parsing at the start, middle, and end of every member-name/value position. Spies prove rejection occurs before sender fingerprinting, normalization, strict-UTF-8 accounting, logging, repository calls, notification reducers, and receipt construction. Valid pairs count as one scalar/four UTF-8 bytes, adjacent supplementary scalars stay distinct, and callback U+FFFD is preserved without claiming whether it was literal wire input or library repair. Separate HTTP-byte tests prove fatal decoding before parsing.
   - Empty, whitespace-only, leading/trailing-whitespace, newline-only, combining, emoji, four-byte UTF-8, exactly-4,096-byte, 4,097-byte, and unpaired-surrogate composer fixtures prove the exact preserve/reject rules. The envelope serializer proves exact member order, omission, escaping, UTC milliseconds, 32-KiB request admission, and byte-for-byte reuse only on permitted retries.
   - Retry classification distinguishes pre-body from post-body failure, enforces one-in-flight ordering and eight total attempts, parses only bounded integer `Retry-After`, and makes in-flight process recovery, 3xx/5xx, timeout/socket loss, and malformed/mismatched 2xx delivery unknown after body start.
   - OkHttp configuration tests prove transparent retry and both redirect modes are disabled, enqueue bodies report one-shot, and no interceptor or authenticator supplies a replay path. A permitted app retry creates a new accounted `Call` over the byte-identical persisted BLOB.
   - The shared HTTP client proves the 15-second whole-call timeout and exact response caps; no stage-specific timeout can extend the call.
   - A virtual elapsed-realtime clock proves a socket still opening at 14,999 ms is eligible and is canceled at 15,000 ms; `onOpen` sends one register frame; no matching registration at 5,000 ms after open or 20,000 ms after attempt start cancels the attempt, whichever is first. Matching registration before the deadline succeeds. Pongs, unrelated frames, wall-clock jumps, connectivity callbacks, and late callbacks from an invalidated generation neither extend a deadline nor mutate the next attempt.
   - Android diagnostic-sink tests feed invalid senders and payload-derived exceptions containing newline, JSON delimiters, controls, bidi text, escaped forms, and a unique secret marker. Parsed events contain only allowed scalar fields, and captured output contains neither source text nor a payload-derived exception message.
   - Renewal request correlation and 5/20-second monotonic watchdog deadlines close stale established sockets independently of the 15/5/20-second establishment deadlines.
   - Notification tags, actions, local-ID parsing, stale taps, unread reducers, and clear-history copy are deterministic.
   - Envelope-evidence digest fixtures prove domain separation, fixed-width length prefixes, subject absence versus empty, deterministic output, and a changed digest for each changed id/from/to/subject/body/created-at field.

3. **Android instrumented Room tests**
   - Incoming evidence, visible history, and notification intent commit before both success receipts. First delivery of `(message ID A, incarnation I)` inserts one evidence and one message row. Exact replay of I changes no evidence, visible row, unread count, preview, capacity total, or notification revision and still receipts the active attempt. After trimming, shell eviction, clear-one, and clear-all delete the visible row, replay of I still changes no visible state and cannot resurrect history. Reusing I with each envelope field changed in turn rolls back and follows the conflict path. `(message ID A, incarnation J)` inserts a second occurrence, proving message ID alone is not unique.
   - Invalid-scalar frames with an independently valid ASCII delivery identity produce exactly one `invalid_payload` `nack`, no `injection_requested`/`ack`, and byte-for-byte unchanged Room/capacity/notification state. An invalid delivery identity closes with no receipt. Valid supplementary scalars persist, fingerprint, preview, and account with exact strict-UTF-8 bytes.
   - Exact sender key, visible incoming incarnation, independent evidence digest, and nullable peer ID survive restart.
   - Capacity tests fill each boundary exactly and one unit over: 1,000 conversation shells, 1,000 messages per conversation, 10,000 messages app-wide, 10,000 evidence rows, 32 MiB accounted content, 256-byte previews, 100 active outbox rows, 1 MiB exact outbox BLOBs, 32 KiB per BLOB, and the 9,900-row/31-MiB outgoing soft limits. SQL byte totals match the source byte arrays. At the evidence cap, conclusive 404/different-incarnation recovery frees only proven rows; otherwise new delivery gets no receipt and enters dedup evidence full without evicting the oldest row.
   - Shell churn across more than 1,000 unique valid and invalid senders evicts in the exact tier/order, never evicts an active-outbox shell or new target, keeps IDs monotonic, cancels evicted notification identities through reconciliation, and never leaves more than 1,000 shell rows. Preview tests cover 255, 256, and 257 bytes plus a multibyte scalar crossing the boundary.
   - Incoming transactions can consume the reserved 100 rows/1 MiB, trim in the required order, and never trim the new insert or an active outbox message. Outgoing admission never consumes the reserve and creates no partial timeline row when rejected.
   - Home and timeline paging tests load 50 rows per page, retain at most 150 active rows, preserve sort order through inserts/evictions, and never expose an all-rows repository method.
   - Outbox order, attempt metadata, exact serialized BLOBs, and pending versus in-flight recovery survive process recreation. Persisted in-flight becomes delivery unknown before any scan.
   - Notification/evidence tests crash after incoming commit, after `notify`/`cancel` but before applied revision, after ACK send but before router processing, after router deletion but before `delivery_acknowledged`, and after read/clear/trim/eviction commit before cancellation. Restart/replay produces no history resurrection, one unread increment at most, one stable notification pair with no duplicate style entries, eventual evidence settlement/stale cancellation, and converged revisions/generation.
   - With multiple active notifications, commit previews off and crash before reconciliation: restart removes every preview and converges policy/applied generations from Room alone. Repeat previews on, and crash after replacement but before applied markers: restart performs idempotent replacement with persisted bounded previews. Assert there is no preview key in DataStore/saved state, clear-one/all preserves the Room value, Reset restores false, and a policy-only pass never resurrects a user-dismissed pair.
   - Clear-one/all cascades visible state correctly, ignores late callbacks, preserves uncertain incarnation evidence, setup identity, and Room preview policy, and keeps unrelated conversation notifications.
   - Reset crash hooks around every Section 8.1 boundary prove the marker survives until notifications are canceled and fresh Room is verified, evidence is cleared only as part of full reset, final DataStore clear is atomic, and no old ID starts with new/partial Room state.
   - Every exported Room schema migrates populated fixtures without destructive fallback.
   - Open/migration/disk-full/transaction failures suspend socket and outbox without a receipt or reconnect loop.

4. **Android scripted transport integration tests**
   - Run OkHttp against a controlled HTTP/WebSocket server for malformed frames, exact response-size boundaries, whole-call stalls in every phase, mismatched 2xx envelopes, 400/409/413/429/5xx, delayed renewal replies, disconnects, duplicate identity, and replay.
   - Stall dispatcher/DNS/TCP/TLS/HTTP upgrade past the 15-second monotonic opening limit, then separately open before 15 seconds and withhold or mismatch registration through the five-second/20-second limits. In every case the server observes cancellation and one later backoff attempt, never connected state. Deliver late callbacks from the abandoned socket and prove the new generation is unchanged.
   - Characterize raw malformed WebSocket UTF-8 against the pinned OkHttp version: record whether it fails/closes before `onMessage` or reaches the callback as repaired text, and assert only the corresponding transport/callback behavior. Do not credit the app with byte-level detection. Separately send callback-valid JSON with escaped unpaired surrogates, exact incarnation replays, same-incarnation conflicts, and same-message-ID/new-incarnation frames; assert the exact close/`nack`/success-receipt and unchanged-versus-new Room behavior from Section 8.4.
   - Prove the ambiguity race directly: accept a POST, let a simulated recipient acknowledge/delete it, then drop or corrupt the HTTP response. Android marks delivery unknown, performs no second POST for that ID after timer, reconnect, process restart, connectivity callback, or user action, and shows the duplicate-risk warning before edit-and-send.
   - Prove one `Call` produces at most one server-observed request with a stale pooled socket, disconnect before body, disconnect during body, and retry-like 408, 421, and 503 responses. OkHttp performs no transparent follow-up. Only a later app-level transition after a proven pre-body failure or defined 429 non-enqueue creates another `Call`, always with the exact BLOB and incremented durable attempt count; attempt eight terminates.
   - Prove negotiated ACK settlement with a controlled router: normal correlation deletes evidence; a dropped confirmation after router deletion leaves evidence across restart; GET 404 or a different incarnation then deletes it; GET same-incarnation and every inconclusive response retain it; and replay before ACK settlement never recreates cleared/trimmed history.
   - Prove notification convergence with a controlled NotificationManager facade and router: crash at commit/notify/ACK-confirmation boundaries, restart without router replay after ACK, and replay before ACK all converge to the same fixed notification and Room revisions.
   - These tests prove client state machines. They do not substitute for the actual router.

5. **Compose semantics and screenshot tests**
   - Use the deterministic visual fixture in Section 15.2 with no live network.
   - Check TalkBack order, labels, 48dp targets, 200% font scale, keyboard/insets, composer byte/blank errors, delivery-unknown warning, contrast, and screenshot baselines.

6. **Live router plus two live Pi agents**
   - Run Section 15.3 with the real router, real Pi mailbox extension, and two distinct live Pi sessions. Neither Pi may be replaced by a mock or ad hoc WebSocket client.

7. **Actual-phone gate**
   - Run Section 16 after every earlier lane passes.

### 15.2 Pinned AVD and visual-data fixture

The Android SDK exists at `~/Library/Android/sdk`; its tools are not on the shell PATH, and the command-line tools currently fail because no Java runtime is installed. Fix and pin the JDK in Phase 0.

Create a dedicated `mecha_client_api35` AVD from the already installed `system-images;android-35;google_apis;arm64-v8a` image. Pin these properties in a checked-in fixture manifest:

- Pixel 8 hardware profile.
- API 35 Google APIs, `arm64-v8a`.
- 1080 by 2400 pixels, 420 dpi, portrait.
- 2 GiB RAM, 6 GiB data partition.
- `en-US`, UTC, default display size, font scale 1.0 for baseline captures.
- Dark theme, dynamic color off, 24-hour clock, animations disabled for screenshot capture.
- Cold boot from a wiped named snapshot for each baseline run.

Use animation scale 1.0 for the separate human motion review. Use font scale 2.0 only for the large-font artifact, then restore 1.0. Do not capture goldens from an arbitrary developer device or an AVD named `latest`.

The screenshot-only repository uses a fixed clock and exact deterministic data:

- Current time `2026-07-14T12:00:00.000Z`.
- Install ID `android-00000000-0000-4000-8000-000000000001`.
- Connected peers `supervisor` and `health-watch-agent`.
- Offline historical peer `pr-watch`.
- Invalid claimed sender `\u202esupervisor\u0007` (right-to-left override plus bell), rendered only as `Unknown sender · 9130ea4a`.
- Read, unread, pending, retrying, accepted, failed, delivery-unknown, trimmed-history, identity-conflict, storage-unavailable, reconnecting, and offline fixture rows.
- Long body/ID samples and optional subject, with no production secrets or live mailbox data.

The fixed identity and fake repository are allowed only in test-source-set deterministic UI/visual tests. Packaging tests inspect the normal debug and release artifacts to prove the fixture provider and fixed identity are absent. The fake cannot satisfy transport, persistence, notification, or live-agent acceptance.

### 15.3 Live two-Pi setup and functional flow

1. Start a disposable persistent router database on `127.0.0.1:8765` from the exact revision containing registration/discovery, negotiated ACK confirmation, strict scalar validation, `005_message_incarnation`, GET incarnation recovery, and server-enqueue expiry changes.
2. Start two real Pi sessions in distinct tmux windows with unique valid IDs, using the exact checkout package registrations in Goal-first order: `pi-extensions/mecha-goal/`, then `pi-extensions/mecha-messaging/`. Messaging retains the generated `mailbox-client.js`. Use the exact router base. Record the two IDs in the test artifact instead of assuming a stale worker. Verify each reaches `registered` through router events and appears in discovery as role `agent`. The installer registers standalone Goal first, then Messaging; run this two-package setup only after source and installer integration.
3. Start `mecha_client_api35`, install the debug app, and run `~/Library/Android/sdk/platform-tools/adb reverse tcp:8765 tcp:8765`.
4. Wipe app data so the app generates a fresh install-scoped ID. Record it, grant notification permission through the product flow, and start the approved foreground service.
5. Verify discovery lists both Pi IDs and filters the mobile ID.
6. Select Pi A and send a nonce-bearing normal message. Verify it appears as one durable custom mailbox entry in Pi A's persisted session and triggers the normal Pi turn path. Verify Pi B did not receive it.
7. From Pi A, use its normal mailbox reply path to send a nonce-bearing message to the recorded mobile ID. Verify one local mobile row and no duplicate notification.
8. From Pi B, send a different unsolicited nonce-bearing mailbox message through its normal reply path. Verify a separate conversation, safe Pi B label, and unread badge.
9. Put the app behind Home while the service remains active. Send another normal reply from Pi A. Verify the per-conversation local notification and private lock-screen form.
10. Tap Pi A's notification. Verify the Intent's local ID opens Pi A rather than Pi B and changes only Pi A unread state.
11. Remove ADB reverse. Cached history stays usable, connection becomes offline, and a local send to Pi A remains pending with persisted exact bytes.
12. Restore ADB reverse. Verify reconnect registration, presence refresh, one ordered outbox drain using the original ID/body/timestamp, and queued inbound pickup.
13. Restart the app process. Verify identity, exact sender grouping, history, unread state, outbox state, and notification identity survive.
14. Use a debug-only Android transport fault hook to close its socket after Room commit and before either receipt. Do not add a router production endpoint. Verify normal router timeout/replay preserves the same incarnation and produces one local row, one unread increment, one reconciled notification, and receipts for the new attempt.
15. Repeat that fault, then clear the conversation before reconnect. Verify the same-incarnation replay consults retained evidence, returns receipts, and does not recreate the conversation, unread count, preview, or notification. Separately let the router process ACK/delete but drop `delivery_acknowledged`; restart and prove GET 404 settles evidence without visible-history dependency.
16. Use test-only crash hooks at notification boundaries: after commit before notify, after notify before its applied marker, after ACK send before router processing, and after router deletion before confirmation/notify. Restart after each. Verify the same `(tag, id)` and bounded preview converge without duplicate style entries, including the no-replay case after processed ACK. With active Pi A and Pi B notifications, toggle previews off and crash after the Room policy commit; restart and verify both existing notifications are redacted and the Room policy generation converges without a DataStore copy.
17. Trigger a delayed initial registration response past five monotonic seconds and separately stall opening past 15 seconds; verify neither attempt reports connected and late callbacks are ignored. Then trigger a delayed renewal response past 5 seconds and verify the established-socket watchdog closes/reconnects before the router lease window. Restore normal replies and verify recovery.
18. Put Room into the instrumented failure mode before an inbound delivery. Verify the socket suspends with no receipt/reconnect storm; remove the fault, choose Retry storage, and verify one recovered delivery.
19. Queue a send, begin HTTP, then clear Pi A. Verify the warning, canceled retry, absent local row after late completion, and the honest possibility that the router already accepted it. Pi B remains untouched.
20. Clear all. Verify all local conversations, outbox rows, unread counts, and message notifications disappear through durable reconciliation while uncertain incarnation evidence, install identity/setup, and the connection service remain. Replaying represented incarnations cannot resurrect history.
21. Seed old identity/history/evidence, invoke Reset app identity, and crash once after `reset_pending` and once after fresh Room verification but before final DataStore clear. Each restart resumes reset before network startup. Verify one new identity appears only after an empty current-schema Room database, preview default false, empty evidence, and stale-notification cancellation are durable.

A passing test records router events, both Pi session IDs and durable message nonces, emulator screen/video evidence, and Android assertions without capturing message bodies in general logs.

### 15.4 Background limitations test

Also prove the boundary honestly:

- Background delivery works after pressing Home with notification permission granted and the foreground service active.
- Force-stopping the app prevents local notification delivery until the app is opened again.
- A router message queued during a short force-stop arrives on reconnect.
- A message accepted more than one hour before reconnect can expire under server-enqueue time. No test claims delivery beyond that boundary.
- Packet and dependency inspection finds no FCM or remote push registration.

### 15.5 Visual acceptance artifacts

Capture these from the pinned fixture:

- `home-dark-populated.png`: at least three peers, connected/offline presence, unread/read rows, and safe unknown-sender label.
- `home-dark-empty.png`: no conversations with connected-agent action.
- `agent-picker-dark.png`: searchable connected-agent sheet.
- `conversation-dark-populated.png`: incoming/outgoing, optional subject, pending send, and local timestamp hierarchy.
- `conversation-dark-unread.png`: unread divider and new-message affordance.
- `reconnecting-dark.png`: cached content retained with reconnect state.
- `offline-dark.png`: local outbox and nonblocking offline treatment.
- `identity-conflict-dark.png`: dedicated conflict state without protocol text.
- `storage-unavailable-dark.png`: persistent storage suspension without raw SQLite text.
- `dedup-evidence-full-dark.png`: replay-safety storage suspension with retry/reset actions and no protocol terminology.
- `unknown-sender-dark.png`: null peer ID, safe label, and disabled reply.
- `delivery-unknown-dark.png`: ambiguous outgoing state and duplicate-risk edit/send warning without a retry affordance.
- `clear-pending-confirmation-dark.png`: explicit stop-retry/no-recall warning.
- `notification-background.png`: local incoming notification while app is backgrounded.
- `notification-tap-destination.png`: correct conversation after tap.
- `home-dark-large-font.png`: 200% font scale without clipping.
- `home-light-populated.png`: light companion theme sanity check.
- `composer-ime-dark.png`: visible keyboard with unobscured composer/send action.

Visual acceptance requires automated image dimensions/baselines and a human emulator review. Review hierarchy, contrast, text clipping, touch density, message width, identity consistency, empty-state composition, motion, keyboard/inset behavior, notification privacy, and tap flow. Raw protocol fields, unsafe claimed-sender text, or an admin-console appearance fail the review.

## 16. Physical-phone Tailscale follow-up

Physical phone is a separate phase after Phase 0, all deterministic lanes, the pinned visual gate, and the live two-Pi emulator gate pass.

Keep the router bound to loopback. Add only a dedicated Tailscale Serve HTTPS listener on port 8443, proxying to `http://127.0.0.1:8765`. The planned incremental Serve command is:

```text
tailscale serve --https=8443 --bg http://127.0.0.1:8765
```

Before applying it, export current Serve status. After applying it, prove that the existing `donuts.tailcdfd89.ts.net:443` `/` handler still points to `127.0.0.1:7860` and the unrelated `100.115.63.19:8765` transcription listener is unchanged. Do not use `serve reset`, a path mount, port 443, or port 8765.

Physical steps:

1. Complete the exact-device Phase 0 record for `note.tailcdfd89.ts.net` and install the privately signed release candidate directly on that phone.
2. Commit the narrow source `100.100.214.15/32` to destination `100.115.63.19/32:8443` Tailnet ACL/grant. Verify another Tailnet node is denied before proceeding.
3. Verify from the phone that the fixed HTTPS health, discovery, and message URLs work and that WSS upgrades at the exact URL in Section 13.3. OkHttp sends no `Origin` header.
4. Verify the release app refuses endpoint edits, redirects, IP-literal substitutions, port 443, port 8765, cleartext, and certificate bypass.
5. Run registration and renewal long enough to cross many 30-second lease windows. Delay one renewal response and prove the 5/20-second watchdog recovery.
6. Repeat the full two-Pi send/reply split, safe notification identity/tap routing, clear-pending race, storage suspension/recovery, restart, and one-hour server-enqueue expiry checks.
7. Verify Home/background, lock screen, screen off, Doze, OEM battery policy, Wi-Fi-to-cellular transition, DERP/direct Tailscale transitions, Tailscale disconnect/reconnect, process kill, force-stop, and reboot boundary on the recorded phone build.
8. Verify the endpoint is unavailable when the phone leaves the Tailnet, Funnel is disabled, the router still listens only on loopback, and no broad Tailnet source can reach 8443.

No physical release is accepted because `curl` works once. The exact target SDK/service declaration/distribution tuple, long-running socket, renewal watchdog, outbox, Room suspension, and local notification flow must work on the actual recorded phone build.

## 17. Alternatives considered

### 17.1 ADB reverse versus `10.0.2.2`

Use ADB reverse first. It preserves loopback and is explicit. Keep `10.0.2.2` as a tested fallback only.

### 17.2 Bind router to LAN or Tailscale IP

Rejected. It weakens the existing loopback invariant, conflicts with desktop client URL validation, and creates cleartext/reachability problems. Tailscale Serve or an equivalent tailnet-only local reverse proxy is smaller and safer.

### 17.3 Add pairing token authentication

Rejected by requirement. Tailscale is transport trust for the physical phase. Emulator transport is ADB-local. The remaining claimed-sender limitation is documented rather than hidden behind a second UX.

### 17.4 Add FCM or a notification backend

Rejected for MVP. It adds cloud identity, tokens, backend state, privacy surface, and a second delivery path. MVP uses a foreground service and local Android notifications.

### 17.5 Use WorkManager for the WebSocket

Rejected. WorkManager is for deferrable finite work, not a continuously leased socket. It can be considered later for bounded cleanup, not delivery.

### 17.6 Send outbound messages over a new WebSocket frame

Rejected for MVP. Existing producers use HTTP enqueue and WebSocket receive. Android should follow that split. A sender-bound frame could improve claimed-sender integrity later, but it is not required under the stated transport trust model.

### 17.7 Store history on the router

Rejected. The router intentionally deletes acknowledged bodies. Mobile history belongs in Room and remains independent per device.

### 17.8 One combined chat transcript

Rejected. It loses sender context and breaks notification routing and unread behavior. Conversation identity is the exact local sender key plus immutable local conversation ID; reply capability is the separate nullable peer agent ID.

### 17.9 Unbounded local history

Rejected. Router bodies can be large, unique peers can accumulate empty shells, denormalized previews can duplicate content, and Compose must not materialize an unlimited home list or timeline. Explicit shell/message counts, preview/content bytes, deterministic eviction, database pages, and active render windows are required.

### 17.10 Dynamic-color-only design

Rejected. It makes agent identity, contrast, screenshots, and dark-mode polish unpredictable. Use a deliberate palette first.

### 17.11 Retry ambiguous sends without router tombstones

Rejected for MVP. Exact envelope bytes prevent field drift but do not deduplicate after the recipient acknowledges and the router deletes its only row. Android stops in delivery-unknown state instead. A future retry-after-ack design must add a durable digest tombstone atomically with every acknowledged deletion, define a bounded retention window longer than the client's retry horizon, reject new enqueue admission rather than evict a live tombstone early when its capacity is full, and test restart, expiry, conflict, and capacity behavior before the client enables those retries.

## 18. Acceptance criteria

MVP is accepted when:

1. Phase 0 records a reproducible SDK/JDK/toolchain tuple, actual phone build, truthful foreground-service declaration, direct-APK distribution path, and go decision.
2. The app is Kotlin and Jetpack Compose with no XML screen implementation, unnecessary framework, WorkManager socket, FCM, or second sender.
3. A new live debug or release install generates exactly one valid random `android-<UUID>` identity; restart/update preserves it, backup/uninstall behavior is documented, and post-setup DataStore failure never rotates it silently. Reset writes its DataStore marker before Room destruction, resumes before every normal startup gate, verifies fresh Room before atomically clearing DataStore, and passes every crash point without mixed old/new state. Fixed identities and providers exist only in test artifacts and are absent from installable APKs.
4. Debug reaches only loopback through ADB reverse. Release accepts only `https://donuts.tailcdfd89.ts.net:8443`, derives the four HTTP/WSS routes exactly, and rejects redirects, cleartext, and non-allowlisted endpoints. The security contract and tests explicitly treat loopback/ADB reverse as unauthenticated reachability with no same-user or app guarantee.
5. Legacy and Android registration fixtures validate under the exact schema. Android sends `user_interaction`/`android` and requests `correlated_v1`; old Pi omits all three; every registration response returns the effective role; and Android enters connected state only when initial and renewal responses echo the ACK-confirmation mode.
6. Discovery returns only open, current, unexpired sessions with exact role/timestamps and no client, instance, or lease ID. Presence sorting, staleness, self-filtering, Origin rejection, and no-store pass tests.
7. Router unclaimed expiry is exactly one hour after first server enqueue, unaffected by past/future sender timestamps. Migration `005_message_incarnation` gives every queued row one unique UUIDv4 that stays fixed across identical POSTs, GET, and delivery retries; negotiated successful ACK deletes before a fully correlated confirmation; lost confirmation does not undo deletion; legacy Pi behavior is unchanged; `ack` still leaves no server tombstone; and a later enqueue of the same message ID receives a fresh incarnation.
8. The composer preserves valid nonblank input, enforces the exact 4,096-byte strict-UTF-8 body and 32-KiB serialized-request limits, and persists exact UTC-millisecond envelope bytes once. Every HTTP operation has the 15-second whole-call deadline and response cap. OkHttp transparent retries and redirects are disabled, every enqueue body is one-shot, and one `Call` produces at most one network request. Only a new app-level `Call` after a proven pre-body failure or defined 429 non-enqueue retries the exact BLOB, for at most eight accounted attempts. Any potentially committed attempt without an exact success becomes delivery unknown and never POSTs that ID again.
9. Android fatally decodes bounded HTTP bytes and validates every Unicode string it can observe before schema lengths, sender fingerprints, UTF-8 accounting, logging, persistence, notification work, or receipts. Callback/parsed unpaired surrogates create no durable state or success receipt. OkHttp text callbacks expose no raw frame bytes, so tests characterize malformed wire UTF-8 without claiming app-level detection; callback U+FFFD remains valid and indistinguishable from library repair. Scalar-valid control/bidirectional senders remain exact `sender_key` values but never reach unsafe product/platform identities.
10. Independent bounded incarnation evidence, visible incoming history, and desired notification revisions commit to Room before both receipts. Exact-incarnation replay consults evidence and never recreates history removed by trim, shell eviction, clear-one, or clear-all. Same ID with a new incarnation is a new occurrence; same incarnation with changed digest evidence is rejected. Evidence is deleted only by correlated router confirmation or conclusive GET absence/different-incarnation proof. ACK/confirmation/GET/replay/crash tests converge idempotently.
11. Every Room open/migration/write/disk-full failure sends no receipt or `nack`, closes and suspends the socket/outbox/reconnect loop, and resumes only after proven storage recovery. The 10,000-row evidence cap never evicts uncertainty; unresolved exhaustion enters dedup evidence full without accepting a new incarnation.
12. The WebSocket opening deadline is 15 monotonic seconds, matching initial registration is due within five monotonic seconds of `onOpen` and 20 monotonic seconds of attempt start, and stale-generation callbacks are inert. After registration, the separate 10-second renewal loop correlates responses and the 5/20-second monotonic watchdog reconnects before the current 30-second lease can silently lapse.
13. The durable single-worker outbox preserves order, exact request BLOBs and byte counts, request-body-started ambiguity, retry metadata, the eight-attempt ceiling, delivery-unknown recovery, edit-as-new-ID semantics, and no hidden retry when the foreground service is absent.
14. Room schema exports, forward migrations from every shipped schema, evidence uniqueness/independence and 10,000-row cap, the Room-only preview-policy singleton/revisions, foreign-key cascades that deliberately exclude evidence, the 1,000-shell/message/content/outbox bounds, 256-byte previews, deterministic shell eviction, exact accounted-byte totals, 100-row/1-MiB inbound reserve, outgoing soft admission, bounded 50/150 home and timeline windows, process recreation, and the prohibition on destructive fallback pass instrumentation tests.
15. Clear-one, clear-all, and reset-identity confirmations and transactions match Sections 8.1 and 8.7. Pending retries stop, late callbacks cannot recreate data, clear preserves uncertain inbound evidence without preserving visible history, unrelated conversations survive clear-one, reset clears Room/evidence/DataStore only through the marker sequence, and already accepted router sends are never described as recalled.
16. Notification `(tag, id)`, `Person`, explicit Intent action, stale-tap fallback, group summary, lock-screen privacy, local-only flag, unread updates, and deletion all use immutable local conversation identity rather than claimed sender text. Room is the only preview-preference source; a transactional policy revision causes all existing active notifications to be rebuilt/redacted crash-safely, while dismissed notifications stay dismissed. The bounded projection plus singleton generation survives preference/commit/notify/ACK-confirmation crash windows, replaces rather than appends on replay, cancels stale evicted/deleted IDs, and never gates router receipts on NotificationManager.
17. The fixed API-35 Pixel 8 AVD fixture and deterministic data produce every required screenshot. Automated semantics/contrast/dimensions and human dark/light, 200% font, motion, keyboard/inset, TalkBack, and tap-flow reviews pass.
18. Two distinct live Pi sessions using the normal extension both appear in real discovery, each receives the intended mobile send in durable Pi history, and each sends a normal reply that appears exactly once in the correct mobile conversation. Live lost-receipt, lost-confirmation, clear-before-replay, and recovery-GET cases produce no duplicate or resurrected visible history.
19. Background Home delivery, notification denial, force-stop, short offline pickup, one-hour server-enqueue expiry, duplicate identity, queue/rate/body errors, storage suspension, and no-remote-push boundary are represented and tested honestly.
20. Router and Android adversarial logging tests prove that bodies, subjects, exact envelope JSON, notification previews, raw/escaped invalid sender values, and payload-derived exception messages never enter diagnostic events. Router `message_enqueued` omits `from`. The normal UI contains no raw frames, transport-only IDs, JSON, debug tables, or admin-console layout.
21. Physical rollout remains blocked until the exact 8443 Serve listener preserves existing services, the phone-only Tailnet rule denies another node, and the recorded Galaxy Note20 build passes the complete foreground-service, watchdog, outbox, Room, notification, network-transition, Doze, and privacy gate.
22. The router remains loopback-only and no path exposes it through LAN, public internet, Funnel, port 443 replacement, or Tailscale port 8765. Neither loopback, ADB reverse, Origin rejection, nor user-only state permissions are represented as same-user/process/app authentication.

## 19. Non-goals

MVP does not include:

- An AI model or agent runtime inside the Android app.
- Starting, stopping, renaming, or administering desktop agents.
- Router queue inspection or dead-letter administration.
- Remote push, FCM, APNs, cloud relay, or a new backend.
- Public internet access or Tailscale Funnel.
- Pairing tokens, QR pairing, or account login.
- Attachments, images, voice messages, markdown execution, or file transfer.
- Message editing, deletion recall, reactions, typing indicators, read receipts, or presence guarantees.
- Full-text search.
- Tablet, foldable, Wear OS, widget, or desktop Compose layouts.
- Multi-router or multi-account support.
- End-to-end message encryption beyond ADB/Tailscale/TLS transport and Android device encryption.
- Guaranteed delivery after app force-stop, device power-off, OEM process kill, or the router's one-hour server-enqueue-based unclaimed expiry.
- Replacing existing Pi history. Messages sent to Pi continue into each agent's existing long-running session and history.

## 20. Remaining decisions and pre-implementation blockers

The review findings resolved identity shape, endpoint, phone node, AVD, transport, persistence, retry, notification, clear, and migration behavior. These narrower facts still need evidence before implementation reaches them:

1. **Phase 0 SDK tuple:** select exact `minSdk`, `compileSdk`, `targetSdk`, Android Gradle Plugin, Kotlin, JDK, and build-tools versions after installing a working JDK. The local machine has SDK platforms 35/36 and the API-35 image, but that does not choose a supported build tuple.
2. **Actual phone build and service declaration:** collect Android/API/build and OEM power-policy facts from the observed Galaxy Note20 Ultra 5G. Then identify the truthful foreground-service type/permissions for that exact target SDK, or stop with no-go. This cannot be inferred from Tailnet status.
3. **Application ID and product name:** choose these before channel IDs, Intent action prefixes, signing, and release artifacts become shipped contracts. The mailbox `agent_id` no longer depends on this decision.
4. **Tailnet policy application:** the exact phone/Mac/8443 tuple is selected, but the narrow ACL/grant must be committed and proven to deny a second node. Do not substitute a broad same-user rule.
5. **Reboot behavior:** MVP starts the service only from a visible app flow. Boot-time restart remains a separate feature and is not implied by this specification.

MVP distribution is a privately signed APK installed directly on Wolfgang's phone. Moving to Play or another managed store requires a new distribution and foreground-service policy decision.
