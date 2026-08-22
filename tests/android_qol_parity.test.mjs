import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const activityPath = new URL("../app/src/main/java/life/meyers/mecha/MainActivity.kt", import.meta.url);

function humanName(id) {
  const parts = id.split(/[._-]+/).filter(Boolean);
  return parts.map((part) => `${part[0].toUpperCase()}${part.slice(1)}`).join(" ") || id;
}

function visibleConnectedPeers(peers, filter) {
  const normalizedFilter = filter.trim().toLowerCase();
  return peers.filter((peer) => normalizedFilter === "" || `${peer} ${humanName(peer)}`.toLowerCase().includes(normalizedFilter));
}

test("Android connected-agent filter matches raw IDs and human names", () => {
  const peers = ["supervisor", "android-qol-parity", "wolfgang.web", "note_agent"];
  assert.deepEqual(visibleConnectedPeers(peers, ""), peers);
  assert.deepEqual(visibleConnectedPeers(peers, "qol"), ["android-qol-parity"]);
  assert.deepEqual(visibleConnectedPeers(peers, "QOL PARITY"), ["android-qol-parity"]);
  assert.deepEqual(visibleConnectedPeers(peers, "wolfgang web"), ["wolfgang.web"]);
  assert.deepEqual(visibleConnectedPeers(peers, "missing"), []);
});

test("Android source wires router aliases like the Web UI", async () => {
  const source = await readFile(activityPath, "utf8");
  assert.match(source, /internal data class RouterConfig\(\s*val alias: String,\s*val endpoint: String,/);
  assert.match(source, /val displayName: String get\(\) = alias\.ifBlank \{ endpoint \}/);
  assert.match(source, /internal fun parseRouterConfig\(stored: String\): RouterConfig\?/);
  assert.match(source, /internal fun upsertRouterConfig\([\s\S]*previousEndpoint: String\? = null,/);
  assert.match(source, /private var editingRouterEndpoint by mutableStateOf<String\?>\(null\)/);
  assert.match(source, /activeRouterAfterRouterSave\(activeRouter, previousEndpoint, endpoint\)/);
  assert.match(source, /label = \{ Text\("Alias"\) \}/);
  assert.match(source, /if \(editingEndpoint == null\) "Add router" else "Save router"/);
  assert.match(source, /router\.displayName/);
  assert.match(source, /router\.alias\.isNotBlank\(\)[\s\S]*router\.endpoint/);
  assert.match(source, /Icon\(Icons\.Default\.Edit, "Edit router", tint = Periwinkle\)/);
  assert.match(source, /Icon\(Icons\.Default\.Delete, "Remove router", tint = Color\(0xFFFFA4A0\)\)/);
});

test("Android connected card mirrors the WebUI status and router detail", async () => {
  const source = await readFile(activityPath, "utf8");
  assert.ok(source.includes('connection = "Connected as $owner"'));
  assert.match(source, /connectionDisplayState\(connection, routerError, peers\.isNotEmpty\(\)\)/);
  assert.match(source, /ConnectionDisplayState\.CONNECTED/);
  assert.match(source, /"Open routers to connect\."/);
  assert.match(source, /"Reconnecting"/);
  assert.match(source, /"Error"/);
  assert.match(source, /ConnectionDisplayState\.RECONNECTING,\s*ConnectionDisplayState\.ERROR\s*->\s*"\$\{[^}]+\.ifBlank\s*\{[^}]+\}\}\s*·\s*\$[A-Za-z_]\w*"/);
});

test("Android source wires the core web QOL parity behavior", async () => {
  const source = await readFile(activityPath, "utf8");
  assert.match(source, /private var agentFilter by mutableStateOf\(""\)/);
  assert.match(source, /internal fun visibleConnectedPeers\(peers: List<PeerStatus>, filter: String\): List<PeerStatus>/);
  assert.match(source, /"\$\{peer\.id\} \$\{humanName\(peer\.id\)\} \$descriptions"\.lowercase\(\)\.contains\(normalizedFilter\)/);
  assert.match(source, /val filteredPeers = visibleConnectedPeers\(peers, agentFilter\)/);
  assert.match(source, /Text\(filteredPeers\.size\.toString\(\), style = MaterialTheme\.typography\.bodySmall, color = TextMuted\)/);
  assert.match(source, /internal fun formatMessageTimestamp\(createdAt: String\): String/);
  assert.match(source, /MessageTimestampFormatter\.format\(Instant\.parse\(createdAt\)\)/);
  assert.match(source, /horizontalArrangement = Arrangement\.spacedBy\(8\.dp\)/);
  assert.match(source, /label = \{ Text\("Filter connected agents"\) \}/);
  assert.match(source, /Text\("No connected agents match"/);
  assert.match(source, /agentFilter = ""\n        selectedPeer = peer/);
  assert.match(source, /if \(response\.isSuccessful\) \{[\s\S]*drafts\.clearIfUnchanged\(router, recipient, text\)[\s\S]*refreshPeers\(\)/);
});

test("Android source wires the approved operational visibility slice", async () => {
  const source = await readFile(activityPath, "utf8");
  assert.match(source, /\.put\("agent_role", "user_interaction"\)/);
  assert.match(source, /private const val AGENT_REFRESH_INTERVAL_MS = 10_000L/);
  assert.match(source, /scheduleAtFixedRate\(AGENT_REFRESH_INTERVAL_MS, AGENT_REFRESH_INTERVAL_MS\)[\s\S]*discoverPeers\(generation, router, owner\)/);
  assert.match(source, /"http:\/\/\$router\/v1\/reminders"/);
  assert.match(source, /val payload = if \(responseSuccessful\) \{[\s\S]*?JSONObject\(body\)\.optJSONArray\("reminders"\)[\s\S]*?\} else \{\s*null\s*\}/);
  assert.match(source, /runOnUiThread \{[\s\S]*?val next = reminderRefreshResult\(\s*current = reminders,[\s\S]*?payload = payload,[\s\S]*?\)/);
  assert.match(source, /val refreshError = reminderRefreshError\(responseSuccessful = responseSuccessful, payload = payload\)/);
  assert.match(source, /reminderError = refreshError/);
  assert.match(source, /activeRemindersForPeer/);
  assert.match(source, /goalDialog/);
  assert.match(source, /reminderDialog/);
  assert.match(source, /ModelPicker/);
  assert.match(source, /"agent_model_catalog"/);
  assert.match(source, /"model_control_result"/);
  assert.match(source, /activityState == "compacting"/);
  assert.match(source, /Model change not confirmed/);
});

test("Android WebSocket connection state frames use the UI generation guard", async () => {
  const source = await readFile(activityPath, "utf8");
  const onMessage = source.match(/override fun onMessage\(webSocket: WebSocket, text: String\) \{[\s\S]*?\n            \}\n\n            override fun onClosing/)?.[0];
  assert.ok(onMessage, "WebSocket text-message callback should be present");
  assert.match(onMessage, /catch \(_:\s*Exception\) \{[\s\S]*?runOnUiThread \{[\s\S]*?if \(generation != connectionGeneration\.get\(\)\)[\s\S]*?connection = "Router sent an invalid response"/);
  assert.match(onMessage, /"registered" -> runOnUiThread \{[\s\S]*?if \(generation != connectionGeneration\.get\(\)\) return@runOnUiThread[\s\S]*?val registeredLeaseId = frame\.optString\("lease_id"\)[\s\S]*?startLeaseRenewal\(webSocket, generation, registeredLeaseId, router, owner\)[\s\S]*?discoverPeers\(generation, router, owner\)[\s\S]*?refreshReminders\(generation, router\)[\s\S]*?hydrateHistory/);
  assert.match(onMessage, /"lease_rejected" -> runOnUiThread \{[\s\S]*?if \(generation != connectionGeneration\.get\(\)\) return@runOnUiThread[\s\S]*?connection = "Username is already connected elsewhere"/);
  assert.match(onMessage, /"error" -> runOnUiThread \{[\s\S]*?if \(generation != connectionGeneration\.get\(\)\) return@runOnUiThread[\s\S]*?modelControlRequestMatchesPending/);
});

test("Android registration drops a reconnect-invalidated socket before sending", async () => {
  const source = await readFile(activityPath, "utf8");
  const onOpen = source.match(/override fun onOpen\(webSocket: WebSocket, response: Response\) \{[\s\S]*?\n            \}\n\n            override fun onMessage/);
  assert.ok(onOpen, "WebSocket open callback should be present");
  assert.match(onOpen[0], /synchronized\(connectionGenerationLock\) \{\s*if \(generation != connectionGeneration\.get\(\)\) return@synchronized\s*webSocket\.send\(/);
});

test("Android delivery drops a reconnect-invalidated socket before injection", async () => {
  const source = await readFile(activityPath, "utf8");
  const settleDelivery = source.match(/private fun settleDelivery\([\s\S]*?\n    \}\n\n    private fun historyFor/);
  assert.ok(settleDelivery, "delivery settlement should be present");
  assert.match(settleDelivery[0], /val injectionSent = synchronized\(connectionGenerationLock\) \{[\s\S]*?val capturedLeaseId = leaseId[\s\S]*?deliveryGenerationMatches\([\s\S]*?currentGeneration = connectionGeneration\.get\(\)[\s\S]*?socketIsCurrent = socket === webSocket[\s\S]*?capturedLeaseId = capturedLeaseId[\s\S]*?currentLeaseId = leaseId[\s\S]*?\)\s*\) return@synchronized false[\s\S]*?webSocket\.send\([\s\S]*?type\", \"injection_requested\"/);
  assert.match(settleDelivery[0], /if \(!injectionSent\) return/);
  assert.match(settleDelivery[0], /synchronized\(connectionGenerationLock\) \{[\s\S]*?shouldSendDeliveryAck\([\s\S]*?currentLeaseId = leaseId/);
});

test("Android connection, registration, and reminder publication stay race-safe", async () => {
  const source = await readFile(activityPath, "utf8");
  assert.match(source, /val replacedSocket = synchronized\(connectionGenerationLock\) \{[\s\S]*?val nextSocket = client\.newWebSocket[\s\S]*?if \(generation != connectionGeneration\.get\(\)\) \{[\s\S]*?nextSocket\.close[\s\S]*?socket\.also \{ socket = nextSocket \}/);
  assert.match(source, /private fun startLeaseRenewal\([\s\S]*?\): Boolean = synchronized\(connectionGenerationLock\) \{[\s\S]*?socket !== webSocket[\s\S]*?leaseTimer = timer/);
  assert.match(source, /synchronized\(connectionGenerationLock\) \{[\s\S]*?leaseRenewalCanSend\([\s\S]*?webSocket\.send\(/);
  assert.match(source, /val injectionSent = synchronized\(connectionGenerationLock\) \{[\s\S]*?deliveryGenerationMatches\([\s\S]*?webSocket\.send\([\s\S]*?injection_requested[\s\S]*?shouldSendDeliveryAck\([\s\S]*?injectionSent = injectionSent/);
  assert.match(source, /val router = activeRouter\s+val owner = agentId\s+if \(leaseId\.isNotBlank\(\)\) discoverPeers\(generation, router, owner\)/);
  assert.match(source, /private fun discoverPeers\(generation: Int, router: String, owner: String\)[\s\S]*?"http:\/\/\$router\/v1\/agents"/);
  assert.match(source, /private fun refreshReminders\(generation: Int, router: String\)[\s\S]*?"http:\/\/\$router\/v1\/reminders"/);
  assert.match(source, /runOnUiThread \{[\s\S]*?generation != connectionGeneration\.get\(\) \|\| requestGeneration != reminderRequestGeneration\.get\(\)[\s\S]*?val next = reminderRefreshResult\([\s\S]*?current = reminders/);
});

test("PeerRow keeps peer selection separate from indicator and chevron controls", async () => {
  const source = await readFile(activityPath, "utf8");
  const peerRow = source.match(/private fun PeerRow\([\s\S]*?\n}\n\n@Composable\nprivate fun PeerActivityIndicators/)?.[0];
  assert.ok(peerRow, "PeerRow source should be present");
  assert.doesNotMatch(peerRow, /Card\(\s*modifier = Modifier[^\n]*clickable/);
  assert.match(peerRow, /Row\(\s*Modifier\.weight\(1f\)\.clickable\(onClick = onClick\)/);
  assert.match(peerRow, /IconButton\(onClick = onGoal, modifier = Modifier\.size\(38\.dp\)\)/);
  assert.match(peerRow, /IconButton\(onClick = onReminders, modifier = Modifier\.size\(38\.dp\)\)/);
  assert.match(peerRow, /IconButton\(onClick = onClick, modifier = Modifier\.size\(38\.dp\)\)/);
  assert.match(peerRow, /Icon\(Icons\.Default\.Flag, "Show goal", tint = Color\(0xFFF5B2D1\)\)/);
  assert.match(peerRow, /Icon\(Icons\.Default\.AccessTime, "Show reminders", tint = Teal\)/);
  assert.match(peerRow, /Icon\(Icons\.Default\.ChevronRight, "Open conversation"/);
});
