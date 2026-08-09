import assert from "node:assert/strict";
import { readFileSync, readdirSync } from "node:fs";
import { dirname, join } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

import {
  GROWTH_EXPLAINED_MS,
  COMPACTING_LIMIT_MS,
  IDLE_SILENCE_MS,
  INTERRUPT_GRACE_MS,
  SILENCE_LIMIT_MS,
  createActivityState,
} from "../.claude/skills/supervisor/tools/activity_state.mjs";

const FIXTURES = join(dirname(fileURLToPath(import.meta.url)), "fixtures", "claude-hooks");

// Mirrors the hook table: raw Claude Code hook payloads become the records the reducer consumes.
// Kept in the test so a fixture and the reducer are joined by the same translation the hook and
// the sidecar use, rather than by a hand-written record list that could drift from reality.
function toRecords(rows) {
  const out = [];
  for (const row of rows) {
    const event = row.__probe_event__;
    const p = row.payload ?? {};
    const at = row.__t__ * 1000;
    if (event === "transcript_sample") {
      out.push({ at, kind: "transcript_size", bytes: p.transcript_bytes });
      continue;
    }
    const nested = p.agent_id !== undefined && (event === "PreToolUse" || event === "PostToolUse");
    const base = { at, sessionId: p.session_id };
    const isAgentTool = p.tool_name === "Agent" || p.tool_name === "Task";
    if (event === "SessionStart") out.push({ ...base, kind: "session_start", source: p.source });
    else if (event === "UserPromptSubmit") out.push({ ...base, kind: "turn_start" });
    else if (event === "PreToolUse" && !nested && isAgentTool) {
      out.push({ ...base, kind: "subagent_launch", toolUseId: p.tool_use_id,
        description: p.tool_input?.description, agentType: p.tool_input?.subagent_type });
    } else if (event === "PreToolUse" && !nested) {
      out.push({ ...base, kind: "tool_start", toolUseId: p.tool_use_id });
    } else if (event === "PostToolUse" && !nested && isAgentTool) {
      out.push({ ...base, kind: "subagent_done", toolUseId: p.tool_use_id, subagentId: p.tool_response?.agentId });
    } else if (event === "PostToolUse" && !nested) {
      out.push({ ...base, kind: "tool_end", toolUseId: p.tool_use_id });
    } else if (event === "SubagentStart") {
      out.push({ ...base, kind: "subagent_start", subagentId: p.agent_id, agentType: p.agent_type });
    } else if (event === "SubagentStop") out.push({ ...base, kind: "subagent_stop", subagentId: p.agent_id });
    else if (event === "Stop") out.push({ ...base, kind: "turn_end" });
    else if (event === "PreCompact") out.push({ ...base, kind: "compacting" });
    else if (event === "PostCompact") out.push({ ...base, kind: "compacted" });
    else if (event === "Notification") out.push({ ...base, kind: "notification", notificationType: p.notification_type });
    else if (event === "SessionEnd") out.push({ ...base, kind: "session_end" });
  }
  return out.sort((a, b) => a.at - b.at);
}

function loadFixture(name) {
  const rows = readFileSync(join(FIXTURES, name), "utf8")
    .split("\n").filter(Boolean)
    .map((line) => { try { return JSON.parse(line); } catch { return undefined; } })
    .filter(Boolean);
  return toRecords(rows);
}

// Replays a fixture and reports every state change, so a test asserts on what the machine did
// over real data rather than on a single final value.
function replay(records) {
  const machine = createActivityState();
  const changes = [];
  let previous = machine.state;
  const observe = (at) => {
    const now = machine.tick(at);
    if (now !== previous) { changes.push({ at, state: now }); previous = now; }
  };
  for (const record of records) {
    observe(record.at);
    machine.apply(record, record.at);
    observe(record.at);
  }
  const last = records.at(-1)?.at ?? 0;
  observe(last + INTERRUPT_GRACE_MS + 1);
  return { machine, changes };
}

test("a long tool call stays active for its whole duration", () => {
  // probe10: PreToolUse at t+7.2, transcript flat at 20946 through the command, PostToolUse at
  // t+16.8. The gap between the growth and the next hook record is 8.8s, longer than the
  // interrupt grace, so a rule that only looked forward would settle mid-command. This is the
  // exact case that disproved the earlier forward-only rule.
  const records = loadFixture("probe10-transcript-evidence.jsonl");
  const { changes } = replay(records);
  const toolStart = records.find((r) => r.kind === "tool_start");
  const turnEnd = records.find((r) => r.kind === "turn_end");
  const settledDuringCall = changes.some(
    (c) => c.state === "idle" && c.at > toolStart.at && c.at < turnEnd.at,
  );
  assert.equal(settledDuringCall, false, "went idle while the tool was still running");
  assert.ok(changes.some((c) => c.state === "idle" && c.at >= turnEnd.at), "never settled at turn end");
});

test("an interrupt settles, on evidence no hook provides", () => {
  // probe11: escape mid-command. The transcript grows twice with no hook event behind it and
  // then ~105s of silence. No Stop, no PostToolUse, no Notification ever arrives.
  const records = loadFixture("probe11-interrupt-evidence.jsonl");
  assert.equal(records.some((r) => r.kind === "turn_end"), false, "fixture should contain no Stop");
  const { changes } = replay(records);
  assert.ok(changes.some((c) => c.state === "active"), "never went active");
  assert.ok(changes.some((c) => c.state === "idle"), "never settled after the interrupt");
});

test("compaction holds, and its own summarizer does not end it", () => {
  // Compaction spawns a subagent, so a SubagentStop arrives for an id that never started, with
  // an empty agent_type. Subagent events otherwise mean activity, so treating this one as
  // activity would flip the agent out of compacting ~17s in and release the router's mail pause
  // early. Both compaction fixtures contain that stop.
  for (const name of ["probe7-compact.jsonl", "probe8-postcompact.jsonl"]) {
    const records = loadFixture(name);
    const starts = new Set(records.filter((r) => r.kind === "subagent_start").map((r) => r.subagentId));
    const orphan = records.find((r) => r.kind === "subagent_stop" && !starts.has(r.subagentId));
    assert.ok(orphan, `${name} should contain an unmatched subagent stop`);

    const machine = createActivityState();
    let sawCompacting = false;
    for (const record of records) {
      machine.apply(record, record.at);
      if (record.kind === "compacting") sawCompacting = true;
      if (sawCompacting && record === orphan) {
        assert.equal(machine.state, "compacting", `${name}: orphan stop ended the compacting hold`);
      }
    }
    assert.equal(machine.state, "idle", `${name}: compacting never cleared`);
  }
});

test("an interrupt pressed a second into a turn still settles", () => {
  // probe13: escape ~1.2s after submitting, while the model was thinking and no tool was running.
  // The transcript grew at exactly 2.0s after UserPromptSubmit, which the original 2.0s explained
  // window read as ordinary output, leaving the dot lit for good. This is the case that failed on
  // a live worker, so it is pinned with the capture that produced it.
  const records = loadFixture("probe13-fast-interrupt.jsonl");
  assert.equal(records.some((r) => r.kind === "turn_end"), false, "fixture should contain no Stop");
  const { changes } = replay(records);
  assert.ok(changes.some((c) => c.state === "active"), "never went active");
  const settled = changes.find((c) => c.state === "idle");
  assert.ok(settled, "never settled after the fast interrupt");

  // It must settle on the growth rule, not fall through to the 60s silence backstop. Asserting
  // the timing is what pins the explained window: widen it back to 2.0s and this growth reads as
  // ordinary output, so the dot survives until the backstop instead.
  const growth = records.find((r) => r.kind === "transcript_size" && r.bytes > 20_900);
  assert.ok(settled.at - growth.at <= INTERRUPT_GRACE_MS + 2_000,
    `settled ${Math.round((settled.at - growth.at) / 1000)}s after the growth, so it fell through `
    + "to the silence backstop and the growth was misread as explained");
});

test("silence settles only when no tool is in flight", () => {
  // The backstop must never fire during a long command. A 90 minute eval sweep runs with a tool in
  // flight and a completely flat transcript, which is the failure this whole design exists to
  // prevent.
  const running = createActivityState();
  running.apply({ kind: "turn_start", at: 0, sessionId: "s" }, 0);
  running.apply({ kind: "tool_start", at: 100, toolUseId: "t1" }, 100);
  assert.equal(running.tick(100 + IDLE_SILENCE_MS * 10), "active",
    "settled while a tool was still running");

  const thinking = createActivityState();
  thinking.apply({ kind: "turn_start", at: 0, sessionId: "s" }, 0);
  assert.equal(thinking.tick(IDLE_SILENCE_MS - 1), "active", "settled too early");
  assert.equal(thinking.tick(IDLE_SILENCE_MS + 1), "idle", "never settled with nothing in flight");
});

test("a settle arriving during compaction does not end the hold", () => {
  // The one that protects mail delivery. The router pauses delivery while an agent is compacting
  // and releases it on any non-compacting frame (harness/router.js:1095), so a Stop, a SessionEnd
  // or a notification landing between PreCompact and PostCompact would release the pause early and
  // the worker's queued mail would be delivered into a compacting session.
  //
  // No capture contains this: in both compaction fixtures the Stop happens to arrive before
  // PreCompact. So these records are synthetic, and that is the point. The case is reachable with
  // automatic compaction, which fires mid-turn.
  for (const intruder of [
    { kind: "turn_end" },
    { kind: "session_end" },
    { kind: "notification", notificationType: "idle_prompt" },
    { kind: "notification", notificationType: "permission_prompt" },
    { kind: "tool_start", toolUseId: "t9" },
    { kind: "subagent_stop", subagentId: "never-started" },
  ]) {
    const machine = createActivityState();
    machine.apply({ kind: "turn_start", at: 0, sessionId: "s" }, 0);
    machine.apply({ kind: "compacting", at: 1, sessionId: "s" }, 1);
    assert.equal(machine.state, "compacting");
    machine.apply({ ...intruder, at: 2, sessionId: "s" }, 2);
    assert.equal(machine.state, "compacting",
      `${intruder.kind}/${intruder.notificationType ?? ""} released the compacting hold`);
    machine.apply({ kind: "compacted", at: 3, sessionId: "s" }, 3);
    assert.equal(machine.state, "idle", "compacting never cleared");
  }
});

test("an unmatched subagent stop changes nothing at all", () => {
  // Compaction's summarizer produces exactly this: a stop for an id that never started. Checked
  // outside the compacting state too, because the absorb rule would otherwise hide a bug here.
  const machine = createActivityState();
  machine.apply({ kind: "turn_start", at: 0, sessionId: "s" }, 0);
  machine.apply({ kind: "turn_end", at: 1, sessionId: "s" }, 1);
  assert.equal(machine.state, "idle");
  machine.apply({ kind: "subagent_stop", at: 2, subagentId: "never-started", sessionId: "s" }, 2);
  assert.equal(machine.state, "idle", "an unmatched stop was treated as activity");
  assert.equal(machine.subagentCount, 0);
});

test("a worker blocked on a human settles without losing its subagents", () => {
  // A permission prompt can arrive while subagents are running. Settling on it is right, because
  // the agent is waiting on a person. Clearing the badge is not: their later stops would arrive
  // unmatched and be ignored, so the badge could not recover until the next turn.
  const machine = createActivityState();
  machine.apply({ kind: "turn_start", at: 0, sessionId: "s" }, 0);
  machine.apply({ kind: "subagent_launch", at: 1, toolUseId: "t1", description: "read the logs" }, 1);
  machine.apply({ kind: "subagent_start", at: 2, subagentId: "a1" }, 2);
  assert.equal(machine.subagentCount, 1);

  // A notification type that does not mean "waiting for a person" must not settle. Only
  // idle_prompt and permission_prompt do; anything else Claude Code adds later is not a signal
  // that the agent stopped working.
  machine.apply({ kind: "notification", at: 2.5, notificationType: "some_future_type" }, 2.5);
  assert.equal(machine.state, "active", "an unrelated notification settled the agent");

  machine.apply({ kind: "notification", at: 3, notificationType: "permission_prompt" }, 3);
  assert.equal(machine.state, "idle", "did not settle while blocked on a human");
  assert.equal(machine.subagentCount, 1, "a notification cleared live subagent state");
  assert.deepEqual(machine.subagents(), [{ id: "a1", description: "read the logs" }]);
});

test("a turn boundary clears state an interrupt would otherwise strand", () => {
  // An interrupt leaves a subagent that never reports stopping. If the next turn does not clear
  // it, the phantom returns on every turn for the life of the sidecar. This is reachable without
  // the 30ms race, because retyping a prompt within the grace window cancels the interrupt settle.
  const machine = createActivityState();
  machine.apply({ kind: "turn_start", at: 0, sessionId: "s" }, 0);
  machine.apply({ kind: "subagent_launch", at: 1, toolUseId: "t1", description: "orphan" }, 1);
  machine.apply({ kind: "subagent_start", at: 2, subagentId: "a1" }, 2);
  assert.equal(machine.subagentCount, 1);

  machine.apply({ kind: "turn_start", at: 3_000, sessionId: "s" }, 3_000);
  assert.equal(machine.subagentCount, 0, "phantom subagent survived into the next turn");
});

test("FIFO pairing gives each subagent the description its launch carried", () => {
  // probe2 is the ambiguous case: two subagents of the same type launched together, so ordering
  // is the only thing separating them. tool_response.agentId in the fixture is ground truth.
  const records = loadFixture("probe2.jsonl");
  const machine = createActivityState();
  const truth = new Map();
  const launches = [];
  for (const record of records) {
    if (record.kind === "subagent_launch") launches.push(record);
    if (record.kind === "subagent_done") truth.set(record.subagentId, record.toolUseId);
    machine.apply(record, record.at);
    if (record.kind === "subagent_start") {
      const labels = machine.subagents();
      assert.ok(labels.some((s) => s.id === record.subagentId), "subagent missing from the badge");
    }
  }
  assert.equal(launches.length, 2, "fixture should launch two subagents");
  assert.equal(truth.size, 2, "fixture should carry ground truth for both");
  // Replay again, stopping at the second start, and check the labels against ground truth.
  const check = createActivityState();
  const byId = new Map();
  for (const record of records) {
    check.apply(record, record.at);
    if (record.kind === "subagent_start") {
      for (const s of check.subagents()) byId.set(s.id, s.description);
    }
  }
  for (const [subagentId, toolUseId] of truth) {
    const launch = launches.find((l) => l.toolUseId === toolUseId);
    assert.equal(byId.get(subagentId), launch.description,
      `subagent ${subagentId} was labelled with the wrong launch`);
  }
});

test("an unnamed subagent is counted but not named", () => {
  // The router drops a summary whose description is blank while still counting it, so sending a
  // blank one and omitting it look identical. Omitting is the honest form. No capture reaches
  // this path: compaction's summarizer has an empty agent_type but never starts, so this record
  // is synthetic and says so.
  const machine = createActivityState();
  machine.apply({ kind: "turn_start", at: 0, sessionId: "s" }, 0);
  machine.apply({ kind: "subagent_launch", at: 1, toolUseId: "t1", description: "", agentType: "" }, 1);
  machine.apply({ kind: "subagent_start", at: 2, subagentId: "a1", agentType: "" }, 2);
  assert.equal(machine.subagentCount, 1, "unnamed subagent should still be counted");
  assert.deepEqual(machine.subagents(), [], "a blank description should not be sent");
});

test("a new session id resets everything", () => {
  const machine = createActivityState();
  machine.apply({ kind: "turn_start", at: 0, sessionId: "one" }, 0);
  machine.apply({ kind: "subagent_launch", at: 1, toolUseId: "t1", description: "x", sessionId: "one" }, 1);
  machine.apply({ kind: "subagent_start", at: 2, subagentId: "a1", sessionId: "one" }, 2);
  assert.equal(machine.subagentCount, 1);
  machine.apply({ kind: "turn_start", at: 3, sessionId: "two" }, 3);
  assert.equal(machine.subagentCount, 0, "state carried across a session restart");
});

test("session_start with source compact clears compacting but keeps the subagents", () => {
  const machine = createActivityState();
  machine.apply({ kind: "turn_start", at: 0, sessionId: "s" }, 0);
  machine.apply({ kind: "subagent_launch", at: 1, toolUseId: "t1", description: "still running" }, 1);
  machine.apply({ kind: "subagent_start", at: 2, subagentId: "a1" }, 2);
  machine.apply({ kind: "compacting", at: 3, sessionId: "s" }, 3);
  assert.equal(machine.state, "compacting");
  machine.apply({ kind: "session_start", at: 4, source: "compact", sessionId: "s" }, 4);
  assert.equal(machine.state, "idle", "compact source did not clear compacting");
  assert.equal(machine.subagentCount, 1, "a mid-session compact wiped live subagent state");
});

test("session_start with any other source resets", () => {
  for (const source of ["startup", "resume", "clear", "fork"]) {
    const machine = createActivityState();
    machine.apply({ kind: "turn_start", at: 0, sessionId: "s" }, 0);
    machine.apply({ kind: "subagent_launch", at: 1, toolUseId: "t1", description: "x" }, 1);
    machine.apply({ kind: "subagent_start", at: 2, subagentId: "a1" }, 2);
    machine.apply({ kind: "session_start", at: 3, source, sessionId: "s" }, 3);
    assert.equal(machine.subagentCount, 0, `source ${source} should reset`);
  }
});

test("growth explained by a recent hook record does not settle", () => {
  const machine = createActivityState();
  machine.apply({ kind: "turn_start", at: 0, sessionId: "s" }, 0);
  machine.apply({ kind: "transcript_size", at: 100, bytes: 1_000 }, 100);
  machine.apply({ kind: "tool_start", at: 1_000, toolUseId: "t1" }, 1_000);
  machine.apply({ kind: "transcript_size", at: 1_900, bytes: 2_000 }, 1_900); // 0.9s after
  assert.equal(machine.tick(1_900 + INTERRUPT_GRACE_MS + 1), "active", "settled on explained growth");
});

test("growth with no recent hook record settles after the grace window", () => {
  const machine = createActivityState();
  machine.apply({ kind: "turn_start", at: 0, sessionId: "s" }, 0);
  machine.apply({ kind: "transcript_size", at: 100, bytes: 1_000 }, 100);
  machine.apply({ kind: "tool_start", at: 1_000, toolUseId: "t1" }, 1_000);
  const growth = 1_000 + GROWTH_EXPLAINED_MS + 2_000;
  machine.apply({ kind: "transcript_size", at: growth, bytes: 2_000 }, growth);
  assert.equal(machine.tick(growth + INTERRUPT_GRACE_MS - 1), "active", "settled before the grace window");
  assert.equal(machine.tick(growth + INTERRUPT_GRACE_MS + 1), "idle", "never settled after the grace window");
});

test("an unknown transcript size is not growth", () => {
  // The sampler reports -1 until a record supplies a path. Treating -1 -> 17768 as growth would
  // fake an interrupt on every sidecar start.
  const machine = createActivityState();
  machine.apply({ kind: "turn_start", at: 0, sessionId: "s" }, 0);
  machine.apply({ kind: "tool_start", at: 1_000, toolUseId: "t1" }, 1_000);
  const at = 1_000 + GROWTH_EXPLAINED_MS + 2_000;
  machine.apply({ kind: "transcript_size", at: at - 10, bytes: -1 }, at - 10);
  machine.apply({ kind: "transcript_size", at, bytes: 17_768 }, at);
  assert.equal(machine.tick(at + INTERRUPT_GRACE_MS + 1), "active", "-1 was treated as growth");
});

test("silence past the last-resort bound is reported as exhausted", () => {
  const machine = createActivityState();
  machine.apply({ kind: "turn_start", at: 1_000, sessionId: "s" }, 1_000);
  assert.equal(machine.exhausted(1_000 + SILENCE_LIMIT_MS - 1), false);
  assert.equal(machine.exhausted(1_000 + SILENCE_LIMIT_MS + 1), true);
});

test("every fixture replays without throwing and ends idle or compacting", () => {
  // The general form of the rule this whole design got wrong once: run the rule back over every
  // recording it was derived from, not just the comfortable one.
  const names = readdirSync(FIXTURES).filter((f) => f.endsWith(".jsonl"));
  assert.ok(names.length >= 10, "fixtures are missing");
  for (const name of names) {
    const records = loadFixture(name);
    if (records.length === 0) continue;
    const { machine } = replay(records);
    assert.ok(["idle", "active", "compacting"].includes(machine.state), `${name} ended in ${machine.state}`);
    assert.ok(machine.subagentCount <= 999, `${name} exceeded the subagent ceiling`);
  }
});

test("a compaction that never reports finishing does not pause mail for ever", () => {
  // The router gives compacting no expiry and pauses mail delivery while it lasts, so a lost
  // `compacted` record would leave a worker unreachable for the rest of its session with no
  // symptom on either side. Real compactions measured 17s and 36s, so this bound cannot cut one
  // short.
  const machine = createActivityState();
  machine.apply({ kind: "turn_start", at: 0, sessionId: "s" }, 0);
  machine.apply({ kind: "compacting", at: 1_000, sessionId: "s" }, 1_000);
  assert.equal(machine.tick(1_000 + 60_000), "compacting", "gave up on a compaction still running");
  assert.equal(machine.tick(1_000 + COMPACTING_LIMIT_MS - 1), "compacting", "cut a compaction short");
  assert.equal(machine.tick(1_000 + COMPACTING_LIMIT_MS + 1), "idle",
    "a lost compacted record left mail paused for the session");
});

test("the silence bound never fires while compacting", () => {
  // exhausted() stops the refresh on the assumption the router will expire the agent. That is true
  // for active and false for compacting, so giving up there would strand the mail pause.
  const machine = createActivityState();
  machine.apply({ kind: "turn_start", at: 0, sessionId: "s" }, 0);
  machine.apply({ kind: "compacting", at: 1_000, sessionId: "s" }, 1_000);
  assert.equal(machine.exhausted(1_000 + SILENCE_LIMIT_MS + 1), false,
    "stopped refreshing while compacting, which leaves mail paused");
});
