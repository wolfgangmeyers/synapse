import assert from "node:assert/strict";
import { existsSync, mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import {
  createActivityTailer,
  markerPath,
  toRecord,
} from "../.claude/skills/supervisor/tools/activity_tail.mjs";

function scratch() {
  return mkdtempSync(join(tmpdir(), "activity-tail-"));
}

function writeEvent(inbox, name, event, payload) {
  writeFileSync(join(inbox, name), `${JSON.stringify({ v: 1, event, payload })}\n`, "utf8");
}

test("the marker names the agent and the live pid, and is removed on stop", () => {
  const dir = scratch();
  const tailer = createActivityTailer({ agentId: "worker-x", dir, pane: "%42", pid: 4242, onRecord: () => {} });
  tailer.start();
  const marker = markerPath(dir, "%42");
  assert.equal(readFileSync(marker, "utf8").trim(), "worker-x 4242");
  // The hook writes into this directory, so it has to exist before the marker does.
  assert.ok(existsSync(join(dir, "worker-x.d")));
  tailer.stop();
  assert.equal(existsSync(marker), false, "marker outlived the sidecar");
  rmSync(dir, { recursive: true, force: true });
});

test("records are consumed oldest first and deleted", async () => {
  // Ordering is load-bearing: subagent labels come from pairing each launch with the next start
  // in arrival order, and those arrive about 30ms apart.
  const dir = scratch();
  const seen = [];
  const tailer = createActivityTailer({ agentId: "w", dir, pane: "%1", onRecord: (r) => seen.push(r) });
  tailer.start();
  const inbox = tailer.inbox;
  writeEvent(inbox, "100.json", "PreToolUse", { tool_name: "Agent", tool_use_id: "t1", tool_input: { description: "first" } });
  await new Promise((r) => setTimeout(r, 5));
  writeEvent(inbox, "101.json", "SubagentStart", { agent_id: "a1", agent_type: "Explore" });
  await new Promise((r) => setTimeout(r, 5));
  writeEvent(inbox, "102.json", "Stop", {});
  tailer.tick();
  assert.deepEqual(seen.filter((r) => r.kind !== "transcript_size").map((r) => r.kind),
    ["subagent_launch", "subagent_start", "turn_end"]);
  assert.deepEqual(readdirSync(inbox).filter((f) => f.endsWith(".json")), [],
    "consumed files were not deleted");
  tailer.stop();
  rmSync(dir, { recursive: true, force: true });
});


test("a file that never parses is dropped and does not wedge the queue", () => {
  const dir = scratch();
  const seen = [];
  const tailer = createActivityTailer({ agentId: "w", dir, pane: "%1", onRecord: (r) => seen.push(r) });
  tailer.start();
  writeFileSync(join(tailer.inbox, "1.json"), "{not json at all}\n", "utf8");
  writeEvent(tailer.inbox, "2.json", "Stop", {});
  tailer.tick();
  assert.deepEqual(seen.filter((r) => r.kind !== "transcript_size").map((r) => r.kind), ["turn_end"]);
  assert.deepEqual(readdirSync(tailer.inbox).filter((f) => f.endsWith(".json")), []);
  tailer.stop();
  rmSync(dir, { recursive: true, force: true });
});

test("a half-written file is left until it parses, then consumed", () => {
  const dir = scratch();
  const seen = [];
  const tailer = createActivityTailer({ agentId: "w", dir, pane: "%1", onRecord: (r) => seen.push(r) });
  tailer.start();
  const partial = join(tailer.inbox, "1.json");
  writeFileSync(partial, '{"v":1,"event":"Stop","payl', "utf8"); // no trailing newline yet
  tailer.tick();
  assert.deepEqual(seen.filter((r) => r.kind !== "transcript_size"), [], "consumed a torn file");
  assert.ok(existsSync(partial), "torn file was deleted before it could be completed");
  writeFileSync(partial, `${JSON.stringify({ v: 1, event: "Stop", payload: {} })}\n`, "utf8");
  tailer.tick();
  assert.deepEqual(seen.filter((r) => r.kind !== "transcript_size").map((r) => r.kind), ["turn_end"]);
  tailer.stop();
  rmSync(dir, { recursive: true, force: true });
});

test("nested subagent tool calls are not counted as the main agent's", () => {
  // probe1 and probe2 contain exactly these records: a subagent's own Bash calls, carrying a
  // top-level agent_id. Counting them would inflate the main agent's in-flight set.
  assert.equal(toRecord(JSON.stringify({ event: "PreToolUse", payload: { agent_id: "a1", tool_name: "Bash", tool_use_id: "x" } }), 0), undefined);
  assert.equal(toRecord(JSON.stringify({ event: "PostToolUse", payload: { agent_id: "a1", tool_name: "Bash", tool_use_id: "x" } }), 0), undefined);
  assert.equal(toRecord(JSON.stringify({ event: "PreToolUse", payload: { tool_name: "Bash", tool_use_id: "x" } }), 0).kind, "tool_start");
});

test("the legacy Task tool name is still recognised as a subagent launch", () => {
  const record = toRecord(JSON.stringify({
    event: "PreToolUse",
    payload: { tool_name: "Task", tool_use_id: "t1", tool_input: { description: "old name" } },
  }), 0);
  assert.equal(record.kind, "subagent_launch");
  assert.equal(record.description, "old name");
});

test("the transcript is not sampled until a record names one", () => {
  const dir = scratch();
  const seen = [];
  const tailer = createActivityTailer({ agentId: "w", dir, pane: "%1", onRecord: (r) => seen.push(r) });
  tailer.start();
  tailer.tick();
  assert.deepEqual(seen, [], "sampled a transcript it had not been told about");

  const transcript = join(dir, "session.jsonl");
  writeFileSync(transcript, "x".repeat(120), "utf8");
  writeEvent(tailer.inbox, "1.json", "UserPromptSubmit", { transcript_path: transcript, session_id: "s" });
  tailer.tick();
  const samples = seen.filter((r) => r.kind === "transcript_size");
  assert.equal(samples.at(-1).bytes, 120);
  tailer.stop();
  rmSync(dir, { recursive: true, force: true });
});

test("an unreadable transcript reports -1 rather than throwing", () => {
  const dir = scratch();
  const seen = [];
  const tailer = createActivityTailer({ agentId: "w", dir, pane: "%1", onRecord: (r) => seen.push(r) });
  tailer.start();
  const transcript = join(dir, "gone.jsonl");
  writeFileSync(transcript, "abc", "utf8");
  writeEvent(tailer.inbox, "1.json", "UserPromptSubmit", { transcript_path: transcript, session_id: "s" });
  tailer.tick();
  rmSync(transcript, { force: true });
  tailer.tick();
  assert.equal(seen.filter((r) => r.kind === "transcript_size").at(-1).bytes, -1);
  tailer.stop();
  rmSync(dir, { recursive: true, force: true });
});
