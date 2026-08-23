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

test("Task 250 wires both Android goal entry points to one complete fenced editor", async () => {
  const source = await readFile(activityPath, "utf8");
  for (const label of [
    "Select an agent to set a goal",
    "Set what this agent should work toward.",
    "What should this agent work toward?",
    "Blocking",
    "Goal lock",
    "Snooze settings",
    "Allow snoozing",
    "Snooze duration (seconds)",
    "Snooze turn limit (optional)",
    "Snooze budget (minutes, optional)",
    "Snooze refill yield (seconds)",
    "Snooze refill time (seconds)",
    "Saving…",
  ]) assert.ok(source.includes(label), `missing goal-editor label: ${label}`);
  assert.match(source, /GoalDialog\(/);
  assert.match(source, /"Show goal for \$\{humanName\(peerId\)\}"/);
  assert.match(source, /val label = peer\?\.let \{\s*if \(hasGoal\) "Edit goal for \$\{humanName\(it\.id\)\}" else "Set goal for \$\{humanName\(it\.id\)\}"\s*\} \?: "Select an agent to set a goal"/);
  assert.match(source, /Icon\(\s*Icons\.Default\.Flag,\s*label,\s*tint =/);
  const peerHome = source.match(/private fun PeerHome\([\s\S]*?\n}\n\n@Composable\nprivate fun PeerRow/)?.[0];
  const conversation = source.match(/private fun Conversation\([\s\S]*?\n}\n\n@Composable\nprivate fun ModelPicker/)?.[0];
  assert.ok(peerHome, "PeerHome source should be present");
  assert.ok(conversation, "Conversation source should be present");
  assert.match(peerHome, /GoalHeaderButton\(peer = null, onGoal = \{\}\)/);
  assert.match(conversation, /Row\(verticalAlignment = Alignment\.CenterVertically\) \{[\s\S]*?GoalHeaderButton\(peer = peerStatus, onGoal = onGoal\)[\s\S]*?PeerActivityIndicators/);
  assert.equal([...conversation.matchAll(/GoalHeaderButton\(peer = peerStatus, onGoal = onGoal\)/g)].length, 1);
  assert.match(source, /onGoal = ::openGoalDialog/);
  assert.match(source, /"agent_goal_state" -> runOnUiThread/);
  assert.match(source, /"goal_control_result" -> runOnUiThread/);
  assert.match(source, /internal fun goalControlFrame\([\s\S]*?\.put\("type", "goal_control"\)[\s\S]*?\.put\("agent_id", sourceAgentId\)[\s\S]*?\.put\("instance_id", sourceInstanceId\)[\s\S]*?\.put\("lease_id", leaseId\)[\s\S]*?\.put\("target_agent_id", targetAgentId\)[\s\S]*?\.put\("target_instance_id", targetInstanceId\)[\s\S]*?\.put\("control_id", controlId\)[\s\S]*?\.put\("action", if \(state\.enabled\) "set" else "off"\)[\s\S]*?\.put\("goal", if \(state\.enabled\) state\.goal else ""\)[\s\S]*?\.put\("settings",/);
  assert.match(source, /"blocking"[\s\S]*?"goal_lock_enabled"[\s\S]*?"snooze_enabled"[\s\S]*?"snooze_duration_ms"[\s\S]*?"snooze_turn_limit"[\s\S]*?"snooze_budget_cap_ms"[\s\S]*?"snooze_refill_yield_ms"[\s\S]*?"snooze_refill_elapsed_ms"/);
  assert.match(source, /internal fun settleGoalControlResult\([\s\S]*?frame\.optString\("source_agent_id"\) == pending\.sourceAgentId[\s\S]*?frame\.optString\("agent_id"\) == pending\.targetAgentId[\s\S]*?frame\.optString\("instance_id"\) == pending\.targetInstanceId[\s\S]*?frame\.optString\("control_id"\) == pending\.controlId/);
  assert.match(source, /clearGoalControlForReconnect\(\)/);
});

test("Goal editor keeps its bounded form scroll separate from pinned 48dp actions", async () => {
  const source = await readFile(activityPath, "utf8");
  const goalDialog = source.match(/private fun GoalDialog\([\s\S]*?\n}\n\n@Composable\nprivate fun GoalToggle/)?.[0];
  assert.ok(goalDialog, "GoalDialog source should be present");
  assert.match(goalDialog, /Dialog\(onDismissRequest = onDismiss\)[\s\S]*?Surface\([\s\S]*?heightIn\(max = 600\.dp\)/);
  assert.doesNotMatch(goalDialog, /AlertDialog\(/);
  assert.match(goalDialog, /\.weight\(1f, fill = false\)[\s\S]*?\.verticalScroll\(scrollState\)[\s\S]*?\.padding\(bottom = 24\.dp\)/);
  assert.match(goalDialog, /HorizontalDivider\(color = SurfaceRaised\)\s+Row\([\s\S]*?TextButton\([\s\S]*?heightIn\(min = 48\.dp\)[\s\S]*?Button\([\s\S]*?heightIn\(min = 48\.dp\)/);
});

test("Android goal toggles mirror WebUI state labels and lock icons", async () => {
  const source = await readFile(activityPath, "utf8");
  const goalToggle = source.match(/private fun GoalToggle\([\s\S]*?\n}\n\n@Composable\nprivate fun GoalNumberField/)?.[0];
  assert.ok(goalToggle, "GoalToggle source should be present");
  assert.doesNotMatch(goalToggle, /Switch\(/);
  assert.match(goalToggle, /\.toggleable\([\s\S]*?value = checked,[\s\S]*?enabled = enabled,[\s\S]*?role = Role\.Switch/);
  assert.match(goalToggle, /heightIn\(min = 48\.dp\)/);
  assert.match(goalToggle, /Text\([\s\S]*?if \(checked\) "ON" else "OFF"/);
  assert.match(goalToggle, /Icons\.Default\.Lock/);
  assert.match(goalToggle, /Icons\.Default\.LockOpen/);
  assert.match(goalToggle, /contentDescription = "\$label: \$stateText"/);
  assert.doesNotMatch(goalToggle, /Text\(stateText/);
});

test("Task 250 replaces live peer projections before the instance fence and only hydrates clean drafts", async () => {
  const source = await readFile(activityPath, "utf8");
  const activity = source.match(/private fun receiveAgentActivity\([\s\S]*?\n    private fun receiveAgentModelCatalog/)?.[0];
  const modelCatalog = source.match(/private fun receiveAgentModelCatalog\([\s\S]*?\n    private fun applyDeferredEmptyModelCatalogs/)?.[0];
  const goalState = source.match(/private fun receiveAgentGoalState\([\s\S]*?\n    private fun receiveGoalControlResult/)?.[0];
  assert.ok(activity, "receiveAgentActivity source should be present");
  assert.ok(modelCatalog, "receiveAgentModelCatalog source should be present");
  assert.ok(goalState, "receiveAgentGoalState source should be present");
  assert.match(activity, /replaceLivePeerProjectionForNewInstance\(peer, current, frame\)\) return[\s\S]*?acceptsLivePeerInstance/);
  assert.match(modelCatalog, /replaceLivePeerProjectionForNewInstance\(peer, current, frame\)\) return[\s\S]*?acceptsLivePeerInstance/);
  assert.match(goalState, /replaceLivePeerProjectionForNewInstance\(peer, current, frame\)\) return[\s\S]*?acceptsLivePeerInstance/);
  assert.match(source, /fun goalControlReplacementCleanup\([\s\S]*?livePeerInstanceWasReplaced\(previousInstanceId, incomingInstanceId\)[\s\S]*?pending\?\.targetAgentId == peer && pending\.targetInstanceId == previousInstanceId[\s\S]*?editorPeer == peer && editorInstanceId == previousInstanceId/);
  assert.match(goalState, /goalDraftShouldHydrateFromFullState\([\s\S]*?goalDraft = state[\s\S]*?goalDraftDirty = false/);
  assert.match(source, /private fun updateGoalDraft[\s\S]*?goalDraftDirty = true/);
  assert.match(source, /private fun closeGoalDialog[\s\S]*?goalDraftDirty = false/);
});

test("Android goal controls fail closed and status markers use the WebUI gradient sweep", async () => {
  const source = await readFile(activityPath, "utf8");
  assert.match(source, /fun goalControlCapabilityMaySend\(goalControlAvailable: Boolean\?\): Boolean = goalControlAvailable == true/);
  assert.match(source, /fun goalControlCapabilityError\([\s\S]*?!goalControlCapabilityMaySend\(goalControlAvailable\)[\s\S]*?!blocking && goalControlBlockingAvailable != true/);
  assert.match(source, /goalControlBlockingAvailable != true/);
  assert.match(source, /private fun clearGoalCapabilities\(\)[\s\S]*?goalControlCapabilitiesReset\(\)[\s\S]*?goalControlAvailable = reset\.available[\s\S]*?goalControlBlockingAvailable = reset\.blockingAvailable/);
  assert.match(source, /fun goalControlCapabilitiesReset\(\): GoalControlCapabilities = GoalControlCapabilities\(\)/);
  assert.match(source, /clearGoalControlForReconnect\(\)[\s\S]*?clearGoalCapabilities\(\)/);
  assert.match(source, /fun goalControlReplacementCleanup\([\s\S]*?clearPending[\s\S]*?clearEditor/);
  assert.match(source, /fun safeGoalInteger\([\s\S]*?MAX_SAFE_GOAL_INTEGER/);
  assert.match(source, /"invalid_settings" -> "Check the goal settings and try again\."/);
  assert.doesNotMatch(source, /"invalid_settings", "invalid_goal_settings"/);
  assert.match(source, /private fun animatedStatusGradient\(/);
  assert.match(source, /Brush\.linearGradient\(/);
  assert.match(source, /working-avatar/);
  assert.match(source, /private fun OrbitingBorder\(/);
  assert.match(source, /drawFeatheredOrbitingBorder\(progress, radius, strokeWidth, orbitColor\)/);
  assert.match(source, /animation = tween\(900, easing = LinearEasing\)/);
});

test("Android activity status restores the radial pulse and colors the orbit for every active mode", async () => {
  const source = await readFile(activityPath, "utf8");
  const working = source.match(/private fun WorkingIndicator\([\s\S]*?\n}\n\n@Composable\nprivate fun OrbitingBorder/)?.[0];
  const orbit = source.match(/private fun OrbitingBorder\([\s\S]*?\n}\n\nprivate fun DrawScope\.drawFeatheredOrbitingBorder/)?.[0];
  const peerRow = source.match(/private fun PeerRow\([\s\S]*?\n}\n\n@Composable\nprivate fun PeerActivityIndicators/)?.[0];
  const conversation = source.match(/private fun Conversation\([\s\S]*?\n}\n\n@Composable\nprivate fun ModelPicker/)?.[0];
  assert.ok(working, "WorkingIndicator source should be present");
  assert.ok(orbit, "OrbitingBorder source should be present");
  assert.ok(peerRow, "PeerRow source should be present");
  assert.ok(conversation, "Conversation source should be present");
  assert.match(working, /synchronizedHaloPulse\("working-pulse"\)/);
  assert.match(working, /PulsingDot\([\s\S]*?Teal,[\s\S]*?7\.dp,[\s\S]*?10\.dp,[\s\S]*?outerGradientColors/);
  assert.match(source, /Box\(Modifier\.size\(markerSize\), contentAlignment = Alignment\.Center\)/);
  assert.match(source, /Box\(Modifier\.size\(size\)\.clip\(CircleShape\)\.background\(color\)\)/);
  assert.match(orbit, /drawWithCache/);
  assert.match(orbit, /rememberInfiniteTransition/);
  assert.match(orbit, /animation = tween\(900, easing = LinearEasing\)/);
  assert.match(source, /val segment = perimeter \* 0\.25f/);
  assert.match(source, /PathEffect\.dashPathEffect/);
  assert.match(source, /StrokeCap\.Round/);
  assert.match(source, /Nested dashes share one start point/);
  assert.match(source, /val centerProgress = \(index \+ 1\)\.toFloat\(\) \/ featherSteps/);
  assert.match(source, /val alpha = 0\.04f \+ centerProgress \* 0\.26f/);
  assert.match(source, /internal enum class ActivityOrbit[\s\S]*?WORKING\(Teal\)[\s\S]*?SUBAGENT\(Color\(0xFF60A5FA\)\)[\s\S]*?COMPACTING\(Color\(0xFFC084FC\)\)/);
  assert.match(source, /internal fun activityOrbitFor\(peer: PeerStatus\): ActivityOrbit\? = when \{[\s\S]*?peer\.compacting[\s\S]*?peer\.active[\s\S]*?peer\.subagentCount > 0/);
  assert.match(peerRow, /val orbit = activityOrbitFor\(peer\)[\s\S]*?OrbitingBorder\([\s\S]*?active = orbit != null[\s\S]*?orbitColor = orbit\?\.color/);
  assert.match(conversation, /val orbit = activityOrbitFor\(peerStatus\)[\s\S]*?OrbitingBorder\([\s\S]*?active = orbit != null[\s\S]*?orbitColor = orbit\?\.color/);
  assert.match(source, /drawFeatheredOrbitingBorder\([\s\S]*?color: Color[\s\S]*?color = color\.copy\(alpha = alpha\)/);
});

test("PeerRow keeps peer selection separate from indicator and chevron controls", async () => {
  const source = await readFile(activityPath, "utf8");
  const peerRow = source.match(/private fun PeerRow\([\s\S]*?\n}\n\n@Composable\nprivate fun PeerActivityIndicators/)?.[0];
  assert.ok(peerRow, "PeerRow source should be present");
  assert.doesNotMatch(peerRow, /Card\(\s*modifier = Modifier[^\n]*clickable/);
  assert.match(peerRow, /Row\(\s*Modifier\.weight\(1f\)\.clickable\(onClick = onClick\)/);
  assert.match(peerRow, /IconButton\(onClick = onGoal, modifier = Modifier\.size\(48\.dp\)\)/);
  assert.match(peerRow, /IconButton\(onClick = onReminders, modifier = Modifier\.size\(38\.dp\)\)/);
  assert.match(peerRow, /IconButton\(onClick = onClick, modifier = Modifier\.size\(38\.dp\)\)/);
  assert.match(peerRow, /Icons\.Default\.Flag,[\s\S]*?"Show goal for \$\{humanName\(peerId\)\}",[\s\S]*?tint = Color\(0xFFF5B2D1\)/);
  assert.match(peerRow, /Icon\(Icons\.Default\.AccessTime, "Show reminders", tint = Teal\)/);
  assert.match(peerRow, /Icon\(Icons\.Default\.ChevronRight, "Open conversation"/);
});
