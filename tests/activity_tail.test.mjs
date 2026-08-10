import assert from "node:assert/strict";
import { existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, renameSync, rmSync, statSync, utimesSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import {
  createActivityTailer,
  isProcessAlive,
  markerPath,
  sweepStaleArtifacts,
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

// A pane can hold two sidecars. worker_launch.py starts the managed sidecar inside the worker's own
// pane, so its marker path is byte-identical to the one a presence sidecar in that pane would use,
// and for a while both are live. Removing a marker written by the other one leaves the pane with no
// marker at all: the hook then exits before reading stdin, nothing rewrites the file, and that
// worker's indicator is dead for the rest of its session with nothing logged.
test("a sidecar leaves a pane marker that has been taken over by another agent", () => {
  const dir = scratch();
  const marker = markerPath(dir, "%7");
  const tailer = createActivityTailer({ agentId: "session.worker-x.99", dir, pane: "%7", pid: 99, onRecord: () => {} });
  tailer.start();
  assert.equal(readFileSync(marker, "utf8").trim(), "session.worker-x.99 99");
  // The managed sidecar finishes connecting and takes the pane over.
  writeFileSync(marker, "worker-x 4242\n", "utf8");
  tailer.stop();
  assert.equal(readFileSync(marker, "utf8").trim(), "worker-x 4242",
    "removed a marker naming another agent, so that worker's hook stops writing for the session");
  rmSync(dir, { recursive: true, force: true });
});

test("a presence tailer does not take over a marker held by a live owner", () => {
  const dir = scratch();
  const marker = markerPath(dir, "%7");
  // process.pid is a real live pid, so this needs no aliveImpl double.
  writeFileSync(marker, `worker-x ${process.pid}\n`, "utf8");
  const tailer = createActivityTailer({
    agentId: "session.worker-x.99", dir, pane: "%7", pid: 99,
    onRecord: () => {},
  });
  tailer.start();
  assert.equal(readFileSync(marker, "utf8").trim(), `worker-x ${process.pid}`,
    "a presence sidecar clobbered a live managed sidecar's marker");
  tailer.stop();
  assert.equal(readFileSync(marker, "utf8").trim(), `worker-x ${process.pid}`,
    "a presence sidecar removed a marker it never wrote");
  rmSync(dir, { recursive: true, force: true });
});

test("a presence tailer takes over a stale marker even when the sweep could not remove it", () => {
  // The sweep is best-effort: it swallows its own failures and skips a marker it cannot read. So
  // "anything the sweep left is live" is an inference, not a fact, and yielding on it would leave
  // that session dark for its whole life with nothing written anywhere. The liveness of the owner
  // is checked directly.
  const dir = scratch();
  const marker = markerPath(dir, "%7");
  writeFileSync(marker, "worker-x 4242\n", "utf8");
  const tailer = createActivityTailer({
    agentId: "session.worker-x.99", dir, pane: "%7", pid: 99,
    aliveImpl: () => false, onRecord: () => {},
    // readdirSync throwing is what a sweep that cannot run looks like from here.
    fs: { mkdirSync, readdirSync: () => { throw new Error("EACCES"); }, readFileSync, renameSync, rmSync, statSync, writeFileSync },
  });
  tailer.start();
  assert.equal(readFileSync(marker, "utf8").trim(), "session.worker-x.99 99",
    "yielded to a marker whose owner is gone, so nothing writes for this session");
  rmSync(dir, { recursive: true, force: true });
});

test("a presence tailer with no marker at all writes one, which is the case the feature exists for", () => {
  // The most common real case: a session Wolfgang opened by hand in a pane nothing else is watching.
  // Yielding here would leave it with no marker, so the hook writes nothing and its dot never lights.
  const dir = scratch();
  const tailer = createActivityTailer({
    agentId: "session.ad-hoc.99", dir, pane: "%7", pid: 99,
    onRecord: () => {},
  });
  tailer.start();
  assert.equal(readFileSync(markerPath(dir, "%7"), "utf8").trim(), "session.ad-hoc.99 99",
    "an ad hoc session wrote no marker, so its activity is never recorded");
  tailer.stop();
  assert.equal(existsSync(markerPath(dir, "%7")), false, "left its own marker behind");
  rmSync(dir, { recursive: true, force: true });
});

test("a presence tailer writes when the marker already names itself", () => {
  const dir = scratch();
  const marker = markerPath(dir, "%7");
  writeFileSync(marker, `session.worker-x.99 ${process.pid}\n`, "utf8");
  const tailer = createActivityTailer({
    agentId: "session.worker-x.99", dir, pane: "%7", pid: 99,
    onRecord: () => {},
  });
  tailer.start();
  assert.equal(readFileSync(marker, "utf8").trim(), "session.worker-x.99 99",
    "yielded to its own marker left by an earlier process and never refreshed the pid");
  rmSync(dir, { recursive: true, force: true });
});

test("a managed tailer takes over a pane marker held by a live foreign owner", () => {
  // The asymmetry is deliberate. A managed sidecar that yielded would never write a marker, and the
  // presence sidecar holding it removes it on the way out, leaving the worker dark for the session.
  const dir = scratch();
  const marker = markerPath(dir, "%7");
  writeFileSync(marker, `session.worker-x.99 ${process.pid}\n`, "utf8");
  const tailer = createActivityTailer({ agentId: "worker-x", dir, pane: "%7", pid: 4242, onRecord: () => {} });
  tailer.start();
  assert.equal(readFileSync(marker, "utf8").trim(), "worker-x 4242",
    "a managed sidecar yielded its own pane marker and would receive no hook records");
  rmSync(dir, { recursive: true, force: true });
});

test("a marker write that cannot be renamed into place leaves no temporary file behind", () => {
  // rename can fail for real reasons: a full disk, or a temporary name and a target that end up on
  // different filesystems. The half-written file must not be left where the sweep or the next
  // sidecar can find it, and the failure must reach the caller rather than being swallowed into a
  // sidecar that thinks it is being watched.
  const dir = scratch();
  const tailer = createActivityTailer({
    agentId: "worker-x", dir, pane: "%7", pid: 4242, onRecord: () => {},
    fs: {
      mkdirSync, readdirSync, readFileSync, rmSync, statSync, writeFileSync,
      renameSync: () => { const error = new Error("cross-device link"); error.code = "EXDEV"; throw error; },
    },
  });

  assert.throws(() => tailer.start(), /cross-device link/,
    "a marker that was never written reported success, so the hook writes for a sidecar that is not watching");
  assert.deepEqual(readdirSync(dir).filter((name) => name.includes(".watch")), [],
    "left a temporary marker in the directory the sweep and the next sidecar read");
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

test("a marker naming a dead process is swept, and a live one is left alone", () => {
  // A stale marker is worse than no marker: the hook sees the file and never respawns a sidecar,
  // and sees the dead pid and never writes. That pane goes quiet for ever with no log.
  const dir = scratch();
  writeFileSync(join(dir, "pane%1.watch"), "session.a.111\n", "utf8");
  writeFileSync(join(dir, "pane%2.watch"), "worker-b 222\n", "utf8");
  const alive = (pid) => pid === 222;
  const removed = sweepStaleArtifacts({
    dir, io: { readdirSync, readFileSync, rmSync, mkdirSync, statSync, writeFileSync }, aliveImpl: alive, self: "x",
  });
  assert.equal(existsSync(join(dir, "pane%1.watch")), false, "dead marker was not swept");
  assert.equal(existsSync(join(dir, "pane%2.watch")), true, "swept a live worker's marker");
  assert.equal(removed.length, 1);
  rmSync(dir, { recursive: true, force: true });
});

test("a temporary marker left by a killed sidecar is swept, and a live one is left alone", () => {
  // The marker is written to a temporary name and renamed. A process killed between those two steps
  // leaves the temporary behind, and nothing else in this directory ever looks at a .tmp file, so
  // the sweep is the only thing that bounds it.
  const dir = scratch();
  writeFileSync(join(dir, "pane%1.watch.111.tmp"), "session.a.111 111\n", "utf8");
  writeFileSync(join(dir, "pane%2.watch.222.tmp"), "worker-b 222\n", "utf8");
  sweepStaleArtifacts({
    dir, io: { readdirSync, readFileSync, renameSync, rmSync, mkdirSync, statSync, writeFileSync },
    aliveImpl: (pid) => pid === 222, self: "x",
  });
  assert.equal(existsSync(join(dir, "pane%1.watch.111.tmp")), false, "a temporary marker whose writer is gone was kept for ever");
  assert.equal(existsSync(join(dir, "pane%2.watch.222.tmp")), true, "removed a temporary marker mid-write");
  rmSync(dir, { recursive: true, force: true });
});

test("an empty marker is stale to the sweep and names nobody to the tailer", () => {
  // A truncated marker can only come from something outside this code now that writes go through a
  // rename, but the two readers must still disagree about it in the right direction: the sweep
  // clears it, and the tailer neither yields to it nor claims to own it.
  const dir = scratch();
  const marker = markerPath(dir, "%7");
  writeFileSync(marker, "", "utf8");
  const tailer = createActivityTailer({ agentId: "session.a.99", dir, pane: "%7", pid: 99, onRecord: () => {} });
  tailer.start();
  assert.equal(readFileSync(marker, "utf8").trim(), "session.a.99 99",
    "yielded to a marker that names nobody, so this session is dark for its whole life");
  rmSync(dir, { recursive: true, force: true });

  const swept = scratch();
  writeFileSync(markerPath(swept, "%8"), "   \n", "utf8");
  // The real isProcessAlive, deliberately: an empty marker parses to pid NaN, and whether that
  // counts as alive is the whole question. A fake that answered true would be answering it.
  sweepStaleArtifacts({
    dir: swept, io: { readdirSync, readFileSync, renameSync, rmSync, mkdirSync, statSync, writeFileSync },
    self: "x",
  });
  assert.equal(existsSync(markerPath(swept, "%8")), false,
    "a marker naming nobody was skipped, so it suppresses that pane for ever");
  rmSync(swept, { recursive: true, force: true });
});

test("a presence inbox whose process is gone is removed, a worker's never is", () => {
  // Presence ids carry a pid, so one directory is created per Claude session. Nothing removed
  // them, and this runs in every session on the machine.
  const dir = scratch();
  mkdirSync(join(dir, "session.win.111.d"), { recursive: true });
  mkdirSync(join(dir, "session.win.222.d"), { recursive: true });
  mkdirSync(join(dir, "supervisor.d"), { recursive: true });
  const alive = (pid) => pid === 222;
  sweepStaleArtifacts({
    dir, io: { readdirSync, readFileSync, rmSync, mkdirSync, statSync, writeFileSync }, aliveImpl: alive, self: "x",
  });
  assert.equal(existsSync(join(dir, "session.win.111.d")), false, "dead presence inbox was kept");
  assert.equal(existsSync(join(dir, "session.win.222.d")), true, "removed a live session's inbox");
  assert.equal(existsSync(join(dir, "supervisor.d")), true, "removed a managed worker's inbox");
  rmSync(dir, { recursive: true, force: true });
});

test("the sweep never removes the inbox of the sidecar running it", () => {
  const dir = scratch();
  mkdirSync(join(dir, "session.win.999.d"), { recursive: true });
  sweepStaleArtifacts({
    dir, io: { readdirSync, readFileSync, rmSync, mkdirSync, statSync, writeFileSync },
    aliveImpl: () => false, self: "session.win.999",
  });
  assert.equal(existsSync(join(dir, "session.win.999.d")), true, "swept its own inbox out from under itself");
  rmSync(dir, { recursive: true, force: true });
});

test("a process that exists but belongs to someone else counts as alive", () => {
  // kill(pid, 0) raises EPERM for a process owned by another user. Treating that as dead would
  // delete a live session's artifacts.
  const eperm = () => { const e = new Error("not permitted"); e.code = "EPERM"; throw e; };
  const esrch = () => { const e = new Error("no such process"); e.code = "ESRCH"; throw e; };
  assert.equal(isProcessAlive(123, eperm), true, "EPERM was read as a dead process");
  assert.equal(isProcessAlive(123, esrch), false);
  assert.equal(isProcessAlive(0), false);
  assert.equal(isProcessAlive(Number.NaN), false);
});

test("a torn file stops the pass so newer records cannot overtake it", () => {
  // The subagent badge depends on each launch being paired with the next start in arrival order.
  // Skipping a torn file and delivering the newer ones behind it reorders the stream, and a launch
  // arriving after its own start mislabels every subagent for the rest of the turn.
  const dir = scratch();
  const seen = [];
  const tailer = createActivityTailer({ agentId: "w", dir, pane: "%1", onRecord: (r) => seen.push(r) });
  tailer.start();
  const torn = join(tailer.inbox, "1.json");
  writeFileSync(torn, '{"v":1,"event":"PreToolUse","payl', "utf8");
  writeFileSync(join(tailer.inbox, "2.json"),
    `${JSON.stringify({ v: 1, event: "SubagentStart", payload: { agent_id: "a1" } })}\n`, "utf8");
  // Pin the arrival order explicitly. Two files written back to back can land on mtimes close
  // enough to tie, and a tie makes the sort order arbitrary, which would make this test flaky
  // rather than wrong.
  const older = new Date(Date.now() - 2_000);
  const newer = new Date(Date.now() - 1_000);
  utimesSync(torn, older, older);
  utimesSync(join(tailer.inbox, "2.json"), newer, newer);

  tailer.tick();
  const delivered = seen.filter((r) => r.kind !== "transcript_size");
  assert.deepEqual(delivered, [], "a newer record overtook a torn one and reordered the stream");
  assert.ok(existsSync(join(tailer.inbox, "2.json")), "the newer record was consumed out of order");

  // The hook writes each file once, so a completed file keeps the mtime of when it was created.
  // Rewriting it in the test would make it newer than the file behind it, which is an artifact of
  // the test rather than anything the system does.
  const createdAt = older;
  writeFileSync(torn,
    `${JSON.stringify({ v: 1, event: "PreToolUse", payload: { tool_name: "Agent", tool_use_id: "t1", tool_input: { description: "first" } } })}\n`, "utf8");
  utimesSync(torn, createdAt, createdAt);
  tailer.tick();
  assert.deepEqual(seen.filter((r) => r.kind !== "transcript_size").map((r) => r.kind),
    ["subagent_launch", "subagent_start"], "records did not arrive in order once the file completed");
  tailer.stop();
  rmSync(dir, { recursive: true, force: true });
});

test("a record that can never parse is reported, not silently dropped", () => {
  const dir = scratch();
  const dropped = [];
  const tailer = createActivityTailer({
    agentId: "w", dir, pane: "%1", onRecord: () => {}, onDropped: (f) => dropped.push(f),
  });
  tailer.start();
  writeFileSync(join(tailer.inbox, "1.json"), "{ not json }\n", "utf8");
  tailer.tick();
  assert.equal(dropped.length, 1, "an unparsable record vanished with no trace");
  tailer.stop();
  rmSync(dir, { recursive: true, force: true });
});
