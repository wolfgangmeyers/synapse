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
