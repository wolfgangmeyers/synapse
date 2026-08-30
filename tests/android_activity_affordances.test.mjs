import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const activityPath = new URL("../app/src/main/java/life/meyers/mecha/MainActivity.kt", import.meta.url);
const source = await readFile(activityPath, "utf8");

function section(start, end) {
  const startIndex = source.indexOf(start);
  assert.notEqual(startIndex, -1, `missing source section: ${start}`);
  const endIndex = source.indexOf(end, startIndex + start.length);
  assert.notEqual(endIndex, -1, `missing source section boundary: ${end}`);
  return source.slice(startIndex, endIndex);
}

test("Android maps working, subagent, and compacting states to their orbit colors", () => {
  assert.match(source, /internal enum class ActivityOrbit\(val color: Color\) \{[\s\S]*WORKING\(Teal\),[\s\S]*SUBAGENT\(Color\(0xFF60A5FA\)\),[\s\S]*COMPACTING\(Color\(0xFFC084FC\)\),[\s\S]*\}/);
  assert.match(source, /internal fun activityOrbitFor\(peer: PeerStatus\): ActivityOrbit\? = when \{[\s\S]*peer\.compacting -> ActivityOrbit\.COMPACTING[\s\S]*peer\.active -> ActivityOrbit\.WORKING[\s\S]*peer\.subagentCount > 0 -> ActivityOrbit\.SUBAGENT[\s\S]*else -> null[\s\S]*\}/);
});

test("Android wraps the conversation header and agent row with the mapped orbit", () => {
  const row = section("private fun PeerRow(", "private fun PeerActivityIndicators(");
  const conversation = section("private fun Conversation(", "private fun ConversationHeaderOverflowMenu(");

  assert.match(row, /val orbit = activityOrbitFor\(peer\)/);
  assert.match(row, /OrbitingBorder\([\s\S]*active = orbit != null,[\s\S]*orbitColor = orbit\?\.color \?: Teal,[\s\S]*label = "peer-card-\$\{peer\.id\}"/);
  assert.match(row, /Avatar\(peerId\)/);
  assert.match(conversation, /val orbit = activityOrbitFor\(peerStatus\)/);
  assert.match(conversation, /OrbitingBorder\([\s\S]*active = orbit != null,[\s\S]*orbitColor = orbit\?\.color \?: Teal,[\s\S]*label = "conversation-header-\$\{peerStatus\.id\}"/);
});

test("Android keeps the compacting label semantics while adding a pulsing dot", () => {
  const compacting = section("private fun CompactingIndicator(", "private fun WorkingIndicator(");
  const row = section("private fun PeerRow(", "private fun PeerActivityIndicators(");
  const conversation = section("private fun Conversation(", "private fun ConversationHeaderOverflowMenu(");

  assert.match(compacting, /val pulse = synchronizedHaloPulse\("compacting-pulse"\)/);
  assert.match(compacting, /val dotSize = if \(compact\) 7\.dp else 8\.dp/);
  assert.match(compacting, /\.semantics\(mergeDescendants = true\) \{ contentDescription = "Compacting" \}/);
  assert.match(compacting, /\.background\(Color\(0xFF7E22CE\)/);
  assert.match(compacting, /PulsingDot\(Periwinkle, pulse, dotSize, 10\.dp\)/);
  assert.match(compacting, /Text\("Compacting"/);
  assert.doesNotMatch(compacting, /if \(compact\) \{[\s\S]*return/);
  assert.match(row, /CompactingIndicator\(compact = true\)/);
  assert.match(conversation, /CompactingIndicator\(compact = true\)/);
});

test("Android conversation header has no letter avatar", () => {
  const conversation = section("private fun Conversation(", "private fun ConversationHeaderOverflowMenu(");
  assert.doesNotMatch(conversation, /\bAvatar\(/);
});
