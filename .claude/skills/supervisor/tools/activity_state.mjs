// Turns the hook's event stream into the router's three activity states.
//
// A pure reducer: no timers, no file access, no clock of its own. The caller supplies `nowMs`.
// Everything here was derived from real captures of Claude Code 2.1.226 and replayed back over
// them; the fixtures in tests/fixtures/claude-hooks are those captures.

export const MAX_TRACKED = 999; // matches the router's subagent ceiling

// Growth in the session transcript is caused by a write that a hook also reports, so it lands
// just *after* that hook record. Measured across the captures: legitimate growth trails its
// record by 0.1s, 0.3s and 0.9s; growth after an interrupt trails the last record by 3.9s and
// 4.9s. Anything inside this window is explained by the record before it.
export const GROWTH_EXPLAINED_MS = 2_000;

// An interrupt fires no hook at all: no Stop, no PostToolUse, no Notification. Unexplained
// transcript growth is the only evidence it happened. Any hook record cancels the timer.
export const INTERRUPT_GRACE_MS = 8_000;

// Last resort, for when the transcript cannot be read at all and interrupts are undetectable.
// Sized against the longest real command here, a ~90 minute eval sweep, with a wide margin.
export const SILENCE_LIMIT_MS = 6 * 60 * 60 * 1_000;

const MEANS_ACTIVE = new Set([
  "turn_start", "tool_start", "tool_end", "subagent_launch", "subagent_start", "subagent_done",
]);
const MEANS_SETTLED = new Set(["turn_end", "session_end"]);
const SETTLING_NOTIFICATIONS = new Set(["idle_prompt", "permission_prompt"]);

export function createActivityState() {
  let state = "idle";
  let sessionId;
  let lastHookMs;
  let lastSize;
  let pendingGrowthMs;
  const running = new Map();   // subagent id -> label shown on the badge
  const queue = [];            // launches awaiting their subagent_start, first in first out
  const bindings = new Map();  // subagent id -> the tool_use_id that launched it
  const inflight = new Set();  // main-agent tool_use_ids with no matching end

  // Turn-scoped bookkeeping. Cleared at a turn boundary and never by a notification: a
  // permission prompt fires ~6s into an approval block, which can arrive while subagents are
  // still running, and clearing then would strand them (their later stops arrive unmatched and
  // are ignored, so the badge could not recover until the next turn).
  function clearTurn() {
    running.clear();
    queue.length = 0;
    bindings.clear();
    inflight.clear();
    pendingGrowthMs = undefined;
  }

  function cap(collection) {
    while (collection.size > MAX_TRACKED) collection.delete(collection.keys().next().value);
  }

  function apply(record, nowMs) {
    const isHook = record.kind !== "transcript_size";
    if (isHook) {
      lastHookMs = nowMs;
      pendingGrowthMs = undefined; // any hook record cancels a pending interrupt
      if (record.sessionId !== undefined) {
        if (sessionId !== undefined && record.sessionId !== sessionId) {
          state = "idle";
          clearTurn();
        }
        sessionId = record.sessionId;
      }
    }

    // Compacting absorbs everything except its own exits. The router pauses mail delivery while
    // an agent is compacting (harness/router.js:1095) and gives that state no expiry, and any
    // non-compacting frame releases the pause. So a stray settle here would end the pause early,
    // which is the failure the whole compacting path exists to respect.
    if (state === "compacting" && record.kind !== "compacted" && record.kind !== "session_start") {
      return;
    }

    switch (record.kind) {
      case "transcript_size": {
        const size = record.bytes;
        if (size >= 0 && lastSize !== undefined && lastSize >= 0 && size > lastSize
          && state === "active") {
          const gap = lastHookMs === undefined ? Infinity : nowMs - lastHookMs;
          if (gap > GROWTH_EXPLAINED_MS) pendingGrowthMs = nowMs;
        }
        if (size >= 0) lastSize = size; // -1 means the transcript path is not known yet
        return;
      }
      case "session_start":
        // `compact` arrives mid-session, ~30ms after the compaction summarizer's stop, carrying
        // the same session id. It clears compacting and nothing else. Every other source, which
        // is startup / resume / clear / fork, is a new session. Written as "compact clears,
        // anything else resets" so a source added by a future release resets rather than
        // falling through with no defined behavior.
        state = "idle";
        if (record.source !== "compact") clearTurn();
        return;
      case "compacting":
        state = "compacting";
        return;
      case "compacted":
        state = "idle";
        return;
      case "subagent_stop": {
        // Compaction runs its summarizer as a subagent, so a stop arrives for an id that never
        // started, carrying an empty agent_type. It must change nothing at all, including the
        // activity state: treating it as activity would flip the agent out of compacting and
        // release the mail pause early.
        if (!running.has(record.subagentId)) return;
        running.delete(record.subagentId);
        state = "active";
        return;
      }
      case "subagent_launch":
        queue.push({ toolUseId: record.toolUseId, description: record.description, agentType: record.agentType });
        while (queue.length > MAX_TRACKED) queue.shift();
        state = "active";
        return;
      case "subagent_start": {
        const launch = queue.shift();
        // The router drops any summary whose description is blank (normalizeSubagents,
        // harness/router.js:1033) while still counting it, so an unnamed subagent shows as part
        // of the count with no label. Omitting the summary produces the same display honestly.
        const label = launch?.description || record.agentType || undefined;
        running.set(record.subagentId, label);
        if (launch?.toolUseId) bindings.set(record.subagentId, launch.toolUseId);
        cap(running);
        cap(bindings);
        state = "active";
        return;
      }
      case "subagent_done": {
        // tool_response.agentId is ground truth for which launch produced which subagent.
        // SubagentStop always arrives first, so the finished one is already off the badge; this
        // repairs a still-running sibling whose label was swapped with it. It fixes a swapped
        // pair and not a three-way rotation.
        const truth = record.subagentId;
        const claimed = bindings.get(truth);
        if (truth && claimed && record.toolUseId && claimed !== record.toolUseId) {
          for (const [otherId, otherToolUseId] of bindings) {
            if (otherId !== truth && otherToolUseId === record.toolUseId) {
              bindings.set(otherId, claimed);
              bindings.set(truth, record.toolUseId);
              if (running.has(otherId)) running.set(otherId, running.get(truth) ?? running.get(otherId));
              break;
            }
          }
        }
        bindings.delete(truth);
        state = "active";
        return;
      }
      case "notification":
        // A worker blocked on a plan approval or a question looks exactly like hard work at the
        // hook level: a tool started, nothing completed, and the transcript stops moving
        // (measured holding at 30596 bytes across ten samples). It is waiting on a person, so
        // idle is the honest reading. The bookkeeping is deliberately left alone.
        if (SETTLING_NOTIFICATIONS.has(record.notificationType)) state = "idle";
        return;
      default:
        if (MEANS_SETTLED.has(record.kind)) {
          state = "idle";
          clearTurn();
          return;
        }
        if (MEANS_ACTIVE.has(record.kind)) {
          if (record.kind === "turn_start") clearTurn();
          if (record.kind === "tool_start" && record.toolUseId) inflight.add(record.toolUseId);
          if (record.kind === "tool_end" && record.toolUseId) inflight.delete(record.toolUseId);
          state = "active";
        }
    }
  }

  // Time-driven transitions. Separate from apply() so the caller can tick without a record.
  function tick(nowMs) {
    if (state === "active" && pendingGrowthMs !== undefined
      && nowMs - pendingGrowthMs >= INTERRUPT_GRACE_MS) {
      state = "idle";
      clearTurn();
    }
    return state;
  }

  function silentFor(nowMs) {
    const since = Math.max(lastHookMs ?? 0, pendingGrowthMs ?? 0);
    return since === 0 ? 0 : nowMs - since;
  }

  return {
    apply,
    tick,
    get state() { return state; },
    get subagentCount() { return running.size; },
    subagents() {
      return [...running.entries()]
        .filter(([, label]) => typeof label === "string" && label.trim().length > 0)
        .map(([id, label]) => ({ id, description: label }));
    },
    inflightCount: () => inflight.size,
    exhausted: (nowMs) => silentFor(nowMs) > SILENCE_LIMIT_MS,
  };
}
