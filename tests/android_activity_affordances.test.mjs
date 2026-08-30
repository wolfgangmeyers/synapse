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

test("Android maps the orbit to working state only", () => {
  assert.match(source, /internal enum class ActivityOrbit\(val color: Color\) \{\s*WORKING\(Teal\),\s*\}/);
  assert.match(source, /internal fun activityOrbitFor\(peer: PeerStatus\): ActivityOrbit\? =\s*if \(peer\.active\) ActivityOrbit\.WORKING else null/);
  assert.doesNotMatch(source, /ActivityOrbit\.(SUBAGENT|COMPACTING)/);
});

test("Android wraps the conversation header and agent row with the working orbit", () => {
  const row = section("private fun PeerRow(", "private fun PeerActivityIndicators(");
  const conversation = section("private fun Conversation(", "private fun ConversationHeaderOverflowMenu(");

  assert.match(row, /val orbit = activityOrbitFor\(peer\)/);
  assert.match(row, /OrbitingBorder\([\s\S]*label = "peer-card-\$\{peer\.id\}"/);
  assert.match(row, /Avatar\(peerId\)/);
  assert.match(conversation, /val orbit = activityOrbitFor\(peerStatus\)/);
  assert.match(conversation, /OrbitingBorder\([\s\S]*label = "conversation-header-\$\{peerStatus\.id\}"/);
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
