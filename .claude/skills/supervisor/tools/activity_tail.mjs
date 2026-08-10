// Reads what the hook wrote and feeds the activity state machine.
//
// Owns all file access, so activity_state.mjs stays a pure reducer. Also owns the marker that
// tells the hook a live sidecar is watching: without it the hook exits before reading stdin, so
// the marker is what stops an unwatched session from writing files nobody reads, and what stops
// this directory from growing when nothing is consuming it.

import { mkdirSync, readdirSync, readFileSync, renameSync, rmSync, statSync, writeFileSync } from "node:fs";
import { kill } from "node:process";
import { join } from "node:path";

export const DEFAULT_ACTIVITY_DIR = "~/.mecha-developer/claude-activity";
export const POLL_INTERVAL_MS = 1_000;

// Maps a raw Claude Code hook payload onto a record for the reducer. This is the other half of
// the hook table: the hook writes the event name and the payload untouched, and the parsing that
// would have been fragile in shell happens here with a real JSON parser.
export function toRecord(line, nowMs) {
  let parsed;
  try {
    parsed = JSON.parse(line);
  } catch {
    return undefined;
  }
  const event = parsed?.event;
  const p = parsed?.payload ?? {};
  if (typeof event !== "string") return undefined;

  // Hook events fired from inside a subagent carry a top-level agent_id. They are that
  // subagent's own tool calls, tracked by its start and stop, and counting them in the main
  // agent's badge would be wrong.
  const nested = p.agent_id !== undefined && (event === "PreToolUse" || event === "PostToolUse");
  const base = { at: nowMs, sessionId: p.session_id, transcriptPath: p.transcript_path };
  // The tool is `Agent` in 2.1.226 and was `Task` in earlier versions.
  const isAgentTool = p.tool_name === "Agent" || p.tool_name === "Task";

  switch (event) {
    case "SessionStart": return { ...base, kind: "session_start", source: p.source };
    case "UserPromptSubmit": return { ...base, kind: "turn_start" };
    case "Stop": return { ...base, kind: "turn_end" };
    case "SessionEnd": return { ...base, kind: "session_end" };
    case "PreCompact": return { ...base, kind: "compacting" };
    case "PostCompact": return { ...base, kind: "compacted" };
    case "Notification": return { ...base, kind: "notification", notificationType: p.notification_type };
    case "SubagentStart": return { ...base, kind: "subagent_start", subagentId: p.agent_id, agentType: p.agent_type };
    case "SubagentStop": return { ...base, kind: "subagent_stop", subagentId: p.agent_id };
    case "PreToolUse":
      if (nested) return undefined;
      return isAgentTool
        ? { ...base, kind: "subagent_launch", toolUseId: p.tool_use_id,
            description: p.tool_input?.description, agentType: p.tool_input?.subagent_type }
        : { ...base, kind: "tool_start", toolUseId: p.tool_use_id };
    case "PostToolUse":
      if (nested) return undefined;
      return isAgentTool
        ? { ...base, kind: "subagent_done", toolUseId: p.tool_use_id, subagentId: p.tool_response?.agentId }
        : { ...base, kind: "tool_end", toolUseId: p.tool_use_id };
    default: return undefined;
  }
}

export function expandHome(path, home = process.env.HOME) {
  if (path === "~") return home;
  if (path?.startsWith("~/")) return join(home, path.slice(2));
  return path;
}

export function markerPath(dir, pane) {
  return join(dir, `pane${pane}.watch`);
}

// A marker holds "<agent id> <pid>". Parsed in one place because three readers depend on the same
// two fields: the sweep, the tailer's ownership rules, and the hook, which has its own copy in
// shell that cannot be shared. Returns undefined only when the file cannot be read; a marker that
// is present but malformed comes back with whatever it holds, so the sweep can treat it as stale
// rather than skipping it for ever.
export function readMarker(io, path) {
  let contents;
  try {
    contents = io.readFileSync(path, "utf8");
  } catch {
    return undefined;
  }
  const [owner, ownerPid] = contents.trim().split(/\s+/);
  return { owner, pid: Number.parseInt(ownerPid, 10) };
}

// A presence agent id carries the pid of the process that made it, so a self-registered session
// creates a new inbox directory and a new log file every time one starts. Nothing removed them,
// and this is about to run in every Claude session on the machine, so the growth is unbounded and
// gets worse the more the feature is used. The repo requires a bounded path before shipping.
//
// Removal is keyed on the pid being dead, never on age, so a live session is never touched.
export function isProcessAlive(pid, killImpl = kill) {
  if (!Number.isInteger(pid) || pid <= 0) return false;
  try {
    killImpl(pid, 0);
    return true;
  } catch (error) {
    // EPERM means it exists and belongs to someone else. Only ESRCH means gone.
    return error?.code === "EPERM";
  }
}

export function sweepStaleArtifacts({ dir, io, aliveImpl = isProcessAlive, self }) {
  const removed = [];
  let names;
  try {
    names = io.readdirSync(dir);
  } catch {
    return removed;
  }
  // A marker is written to a temporary name and renamed. A process killed between those two steps
  // leaves the temporary behind, and nothing else ever looks at it, so this is the only thing that
  // would bound it. The pid is in the name, exactly as it is in the marker itself.
  for (const name of names) {
    const temporary = /\.(\d+)\.tmp$/.exec(name);
    if (temporary === null) continue;
    if (aliveImpl(Number.parseInt(temporary[1], 10))) continue;
    try { io.rmSync(join(dir, name), { force: true }); removed.push(join(dir, name)); } catch { /* next sweep */ }
  }
  const livePresenceIds = new Set();
  for (const name of names) {
    if (!name.endsWith(".watch")) continue;
    const full = join(dir, name);
    const held = readMarker(io, full);
    if (held === undefined) continue;
    if (aliveImpl(held.pid)) {
      livePresenceIds.add(held.owner);
      continue;
    }
    // A marker naming a dead process suppresses that pane for ever: the hook sees the file and
    // never respawns, and sees the dead pid and never writes.
    try { io.rmSync(full, { force: true }); removed.push(full); } catch { /* next sweep */ }
  }
  // Inbox directories belonging to presence sessions whose process is gone. Worker inboxes are
  // never touched: their ids cannot contain a dot.
  for (const name of names) {
    if (!name.endsWith(".d")) continue;
    const agentId = name.slice(0, -2);
    if (!agentId.startsWith("session.") || agentId === self) continue;
    if (livePresenceIds.has(agentId)) continue;
    const pid = Number.parseInt(agentId.split(".").pop(), 10);
    if (aliveImpl(pid)) continue;
    try { io.rmSync(join(dir, name), { recursive: true, force: true }); removed.push(join(dir, name)); } catch { /* next sweep */ }
  }
  return removed;
}

export function createActivityTailer({
  agentId,
  dir,
  pane = process.env.TMUX_PANE,
  onRecord,
  onDropped = () => {},
  now = () => Date.now(),
  pid = process.pid,
  aliveImpl = isProcessAlive,
  fs: io = { mkdirSync, readdirSync, readFileSync, renameSync, rmSync, statSync, writeFileSync },
} = {}) {
  if (typeof agentId !== "string" || agentId.length === 0) throw new TypeError("agentId is required");
  if (typeof onRecord !== "function") throw new TypeError("onRecord is required");
  const root = expandHome(dir ?? DEFAULT_ACTIVITY_DIR);
  const inbox = join(root, `${agentId}.d`);
  // A self-registered session's id is dotted and a managed worker's is its tmux window name, which
  // cannot contain a dot. The two validators in pi-extensions/mailbox-client.js are complements, so
  // this is enforced rather than a convention. It is the same test the inbox rule in stop() uses,
  // deliberately: one fact about this sidecar, read one way, so the two rules cannot drift apart.
  const isPresence = agentId.includes(".");
  let transcriptPath;
  let started = false;

  // The marker is keyed on the pane, and a pane can hold two sidecars: worker_launch.py starts the
  // managed sidecar inside the worker's own pane, so its marker path is byte-identical to the one a
  // presence sidecar in that pane would write. The file itself is the record of who holds it, read
  // fresh each time rather than remembered, because the other sidecar can take it over in between.
  //
  // Losing the marker is not a small thing: the hook exits before reading stdin when there is none
  // (hooks/claude-activity.sh), nothing rewrites it, and that agent's indicator is dead for the rest
  // of its session with nothing logged.
  // An empty or truncated marker needs no special case and does not get one. It parses to an empty
  // owner and pid NaN, so it matches no agent id and is never alive: this sidecar neither yields to
  // it nor claims to own it, and the sweep clears it as stale. A guard here would be a branch no
  // test could distinguish.
  const markerOwner = () => readMarker(io, markerPath(root, pane));

  function start() {
    io.mkdirSync(inbox, { recursive: true });
    // Bounded retention, run once per sidecar start rather than on a timer. Nothing here is
    // removed on age: only artifacts whose owning process is gone.
    try {
      sweepStaleArtifacts({ dir: root, io, aliveImpl, self: agentId });
    } catch {
      // Cleanup must never stop a sidecar starting.
    }
    // Written last, so the hook never sees a marker before the directory it writes into exists.
    // The pid is what lets a marker left by a killed sidecar be ignored rather than driving
    // writes forever into a file nobody reads.
    //
    // A presence sidecar yields to a live foreign owner. Liveness is checked here rather than
    // inferred from the sweep above having left the file alone: that sweep swallows its own
    // failures and skips a marker it cannot read, so yielding on the inference would leave this
    // session dark for its whole life. A managed sidecar never yields, because one that did would
    // never write a marker at all, and the presence sidecar holding it removes it on the way out.
    if (pane && !(isPresence && holdsForeignLiveMarker())) writeMarker();
    started = true;
  }

  // Written to a temporary name and renamed, because two sidecars can share a pane and every
  // ownership decision here and in sweepStaleArtifacts is a read of this file. writeFileSync
  // truncates before it writes, so a concurrent reader can see an empty marker, read it as owned by
  // nobody with pid NaN, and delete a live owner's marker as stale. rename is atomic, so a reader
  // sees either the old contents or the new ones. Losing the marker has no repair path: start() is
  // the only writer and it runs once, so the pane stays unwatched for the rest of that session.
  function writeMarker() {
    const target = markerPath(root, pane);
    const temporary = `${target}.${pid}.tmp`;
    try {
      io.writeFileSync(temporary, `${agentId} ${pid}\n`, "utf8");
      io.renameSync(temporary, target);
    } catch (error) {
      try { io.rmSync(temporary, { force: true }); } catch { /* the sweep does not read .tmp files */ }
      throw error;
    }
  }

  function holdsForeignLiveMarker() {
    const held = markerOwner();
    if (held === undefined || held.owner === agentId) return false;
    return aliveImpl(held.pid);
  }

  function stop() {
    if (!started) return;
    started = false;
    try {
      // Only a marker that still names this agent. The other sidecar in this pane may have taken it
      // over since start(), and removing it there is the failure described above.
      if (pane && markerOwner()?.owner === agentId) io.rmSync(markerPath(root, pane), { force: true });
    } catch {
      // A marker we cannot remove is covered by the pid check on the hook side.
    }
    // A presence session's inbox is per-process and disposable. A worker's is not.
    if (isPresence) {
      try { io.rmSync(inbox, { recursive: true, force: true }); } catch { /* the sweep gets it */ }
    }
  }

  // Ordering is the feature: subagent labels come from pairing each launch to the next start in
  // arrival order, and those arrive about 30ms apart. mtime is nanosecond-resolution here and
  // separated 400 files created back to back with no ties.
  function drain() {
    let names;
    try {
      names = io.readdirSync(inbox);
    } catch {
      return; // the directory can vanish under us; the next tick recreates nothing and moves on
    }
    const files = [];
    for (const name of names) {
      if (!name.endsWith(".json")) continue;
      const full = join(inbox, name);
      try {
        files.push({ full, mtimeNs: io.statSync(full, { bigint: true }).mtimeNs });
      } catch {
        // Written and removed between readdir and stat. Nothing to consume.
      }
    }
    files.sort((a, b) => (a.mtimeNs < b.mtimeNs ? -1 : a.mtimeNs > b.mtimeNs ? 1 : 0));
    for (const file of files) {
      let contents;
      try {
        contents = io.readFileSync(file.full, "utf8");
      } catch {
        continue;
      }
      const record = toRecord(contents, now());
      // A file still being written parses on a later tick. Stop the whole pass rather than
      // skipping ahead: the files after it are newer, and delivering them first would put records
      // out of arrival order. Ordering is what pairs each subagent launch with its start, so
      // reordering here silently attaches the wrong task description to a running subagent for the
      // rest of the turn.
      if (record === undefined && !contents.endsWith("\n")) return;
      try {
        io.rmSync(file.full, { force: true });
      } catch {
        // Leaving it means replaying it; the reducer is idempotent enough for a duplicate turn
        // marker, and a file we cannot delete is a disk problem worth surfacing elsewhere.
      }
      if (record === undefined) {
        // Dropped rather than retried, but never silently: a lost Stop leaves the dot lit until
        // the quiet rule, and a lost PostCompact would have paused mail before that was bounded.
        onDropped(file.full);
        continue;
      }
      if (typeof record.transcriptPath === "string") transcriptPath = record.transcriptPath;
      onRecord(record);
    }
  }

  // The transcript is read for a byte count and nothing else. That is the whole reason this can
  // depend on an undocumented file at all: a schema change cannot break a size comparison.
  function sampleTranscript() {
    if (transcriptPath === undefined) return; // nothing to sample until a record names one
    let bytes = -1;
    try {
      bytes = io.statSync(transcriptPath).size;
    } catch {
      bytes = -1; // deleted, rotated, or unreachable: interrupts stop being detectable
    }
    onRecord({ kind: "transcript_size", at: now(), bytes });
  }

  return {
    start,
    stop,
    tick() { drain(); sampleTranscript(); },
    get inbox() { return inbox; },
    get marker() { return pane ? markerPath(root, pane) : undefined; },
    get transcriptPath() { return transcriptPath; },
  };
}
