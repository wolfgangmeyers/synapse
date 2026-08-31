# Activity indicators: known issues

Open problems in `hooks/claude-activity.sh`, `activity_tail.mjs`, `activity_state.mjs` and the
activity parts of `claude_sidecar.mjs`, as of 2026-08-09, with the duplicate-registration sections
added on 2026-08-10. Each one says what it breaks, what it looks like from outside, and what the
evidence is, because every item here presents as working. One section is marked Fixed and kept: its
residuals are open, and its reasoning is what someone changing that code needs.

Read `tests/fixtures/claude-hooks/README.md` first. The fixtures are real captured payloads, and
most of these are testable against them.

The dated source citations in this document refer to the legacy Node router where they name
`harness/router.js`. The supported router is Go; use `tools/run_router.sh` and the current Go
transport sources when checking live behavior. Keep Node references only for explicit rollback or
differential testing.

**Test anything protocol-shaped against the running router, not against the schema file.** On
2026-08-09 the router rejected every `compacting` activity frame with `invalid_frame` and closed
the socket. `protocol/v1/schema.json` was correct and had been for a day. The stale thing was the
process: the router had been running since 21:47:42 on 2026-08-08 and the commit that added
compaction to the schema landed at 21:56:02, eight minutes and twenty seconds later. Reading the
file would have confirmed the frame was valid and been useless. Sending one frame per state at the
live router and watching what came back is what found it.

It takes these frames now. `~/.mecha-developer/router.jsonl` records `router_started` at
`2026-08-09T19:55:56.423Z` and no `router_error` of any code after it. The last one is at
`19:51:21`. That log records UTC while the times above are local, so `19:55:56Z` is 12:55:56 on
2026-08-09 here.

## A sidecar that exits mid-compaction pauses that agent's mail until something reports activity

**What breaks.** Mail delivery for that agent pauses, and stays paused until any client sends the
router a non-compacting activity frame under that agent id.

`stop()` closes the socket without publishing a final settled frame. The router deliberately skips
clearing activity for a compacting agent on disconnect (`harness/router.js:295`) and gives that
state no expiry (`:1100`), so the next registration for that id inherits `deliveryPaused: true`
(`:409`). The ten-minute bound in `activity_state.mjs` does not help, because the process holding
that state is gone.

**How long it lasts.** Until the next ordinary activity frame. For a relaunched worker that is about
a second. Every activity frame sets `deliveryPaused` from the state it carries (`:1095`), and both
exits of `updateAgentActivity` clear the pause and offer the queued messages when the previous state
was compacting (`:1108` and `:1115`). A new sidecar ticks every `ACTIVITY_POLL_MS`, 1000 ms
(`claude_sidecar.mjs:19`, and the activity interval in `runCli`), and publishes on its first tick.
So relaunching the worker is enough. It stays paused for good only for a client that registers and
sends no activity at all.

**What it looks like.** An agent showing compacting with nothing running. Mail to it is accepted and
queued, and `mailbox_send.py` reports success, so the sender sees nothing wrong until the queue
drains on the next relaunch.

**How the window was measured.** Probed on 2026-08-09 against a router started on a spare port with
its own database. An agent sent `compacting` and disconnected. A message posted to it queued. A new
session for the same id registered and sat for 1507 ms with nothing delivered. One `settled` frame
released it and the message arrived 33 ms later. An `active` frame released it the same way, 31 ms
after.

The five minute case: a session registered, renewed its lease, and sent no activity frame at all.
The message stayed queued the whole time. One frame at the end released it in 30 ms.

`tests/shared_conversation_sync.test.mjs:447` covers the `settled` release and runs on every
`npm test`. The `active` release and the client that sends nothing are measured here only.

Reaching this state needs a sidecar to stop between `PreCompact` and `PostCompact`, a ~20 to 40
second window per compaction based on `probe7-compact.jsonl` and `probe8-postcompact.jsonl`.

**The compacting path first became reachable in production at the 2026-08-09 restart.** The router
rejected every one of these frames from 2026-08-08 until then, and it accepts them now. The pause
and its release are measured, as above. The absorbing state in `activity_state.mjs` that protects
the compacting hold comes from reading the router source and the fixtures, so treat that part as
unobserved the first time it runs for real.

## Two Claude sessions sharing one pane

**What breaks.** The agent's dot goes out while it is still working, and its subagent badge
empties, which is the exact failure the feature exists to prevent.

A headless `claude -p` launched from a worker's own Bash tool inherits `TMUX_PANE`. It finds the
worker's marker, so it writes its own events into the worker's inbox under the worker's agent id.
Three things then go wrong at once. The child's `Stop` settles the worker's dot. The child's
`session_id` differs, so the session guard clears the running subagents on every interleaved
record. And the child's `transcript_path` becomes the file the interrupt detector samples, so
growth in the child's transcript is read as activity in the parent's.

**What it looks like.** A busy worker showing idle, or flickering between the two, with its
subagent badge emptying mid-turn. Nothing logs. The indicator looks broken rather than wrong.

**Why it is real and not a design worry.** The worker that built this ran dozens of headless
`claude -p` sessions inside its own pane on 2026-08-09, for every probe in the fixtures directory.
Any worker that uses headless sessions hits this on its first one.

**Where to start.** The marker names one agent per pane, and the hook has no way to tell which
session it is running under. `session_id` is on every payload and is the thing that distinguishes
them.

## `MECHA_ACTIVITY_DIR` is read from two different environments

**What breaks.** The hook writes to one directory and the sidecar reads another, so no activity
ever appears.

The sidecar's copy comes from whatever launched it, and the hook's comes from the Claude session's
environment. `--activity-dir` overrides only the sidecar's.

**What it looks like.** Exactly like a healthy but permanently idle agent. No marker where the
sidecar expects one, no records, no error, no log.

**Why it is real.** Two processes, two environments, one variable, and nothing compares them.

## The pane marker guards a pane, and a duplicate registration is about a window

**Fixed on 2026-08-10. Kept because the residuals below are open and because the reasoning is what
someone changing this code will need.**

Every Claude session self-registers a presence sidecar at `SessionStart`, so a managed worker showed
up twice: once as its mailbox agent and once as its own session, at a cost of two processes each.
Six existed at one time on 2026-08-10, listed in `~/.mecha-developer/router.jsonl` as
`session.supervisor.36687`, `session.claude-activity-indicators.37066`, `session.van-756-rfc.37503`,
`session.van-758-project-model.49106`, `session.van-768-calendar-org.49288` and
`session.van-770-eval-gate.36934`.

**What the six actually show.** Every managed counterpart was registered and renewing its lease at
the moment its duplicate appeared, by between 0.4 and 8.4 seconds. Measured from `router.jsonl`,
each presence id against the last event of its managed counterpart before it: `supervisor` 1.97 s,
`claude-activity-indicators` 8.36 s, `van-756-rfc` 0.38 s, `van-758-project-model` 1.90 s,
`van-768-calendar-org` 1.45 s, `van-770-eval-gate` 2.31 s. So a check against the router at startup
would have suppressed all six.

Router times here are UTC, because that is what `router.jsonl` records. Process and commit times are
local, seven hours behind. The same split is noted at the top of this page.

**Why the marker did not stop them is a weaker claim than it looks.** Two of the six are explained
exactly: the `supervisor` sidecar (pid 77377) started 2026-07-28 15:55 local and the
`claude-activity-indicators` sidecar (pid 90186) started 2026-08-09 09:10 local, while the code that
writes a marker was authored in `fe22132` at 2026-08-09 10:44 local. Both processes predate it, so
neither could ever have written one, and neither `pane%1.watch` nor `pane%60.watch` exists. Both
were launched from their own panes, `%1` and `%60`, so an explanation in terms of a sidecar started
from someone else's pane does not apply to them. For the other four, whose duplicates appeared at
17:35 and 17:56 UTC, the sidecars running then have since been replaced and their markers cannot be
recovered, so why those panes held no marker is **not established**.

What is established is structural and does not depend on the six. The marker is keyed to a pane and
the duplication is about a window. It is written only after the managed sidecar connects, so a
`SessionStart` inside that window can arrive before it exists. It is absent for any sidecar started
before `fe22132`, and for any started from a pane other than the one it watches. And two sidecars in
one pane write the same path.

A presence sidecar now asks the router directly, on `GET /v1/agents`, whether an agent with its tmux
window name is connected: before registering, and every 10 s after. The marker check stays. It is
free, it catches the common case earlier, and it is what stops an unwatched session doing any work.
A managed worker launched through `worker_launch.py` on 2026-08-10 never spawned a presence sidecar
at all, because the managed sidecar won the race and wrote `pane%130.watch` first, so the hook
stopped it before the router was asked. That worker appeared once, with one sidecar process, and
`presence-suppressed.log` holds no record for it.

**A suppressed session leaves one line in
`~/.mecha-developer/claude-sidecars/presence-suppressed.log` and nothing else.** No router entry, no
process, no marker, and no log of its own, because a presence sidecar removes its own log on every
exit it controls. A `SIGKILL` leaves that log behind, so the shared file is the only one always
written. A router that could not answer the question is recorded there too, once per process,
because otherwise an unreachable router is indistinguishable from a free window and the check is
silently off.

**Once suppressed, nothing brings that session back.** No presence sidecar respawns for the pane
until the next `SessionStart`. If the managed sidecar dies while its Claude keeps running, that
window now has no representation at all, where before this change the duplicate happened to cover
it. That is the intended trade, since a dead managed sidecar also means no mail and is the louder
failure.

## Marker ownership when two sidecars hold one pane

`worker_launch.py` starts the managed sidecar inside the worker's own pane, so its marker path is
byte-identical to the one a presence sidecar in that pane writes. For the seconds before the
presence sidecar notices the managed one, both are live and both want that file.

A sidecar removes the marker only while it still names that sidecar, and a presence sidecar does not
take over a marker held by a live foreign owner. A managed sidecar still takes one over
unconditionally, because one that yielded would never write a marker at all and the presence sidecar
holding it removes it on the way out. Either rule alone still loses the file.

The marker is written to a temporary name and renamed, because every ownership decision here and in
`sweepStaleArtifacts` is a read of it and `writeFileSync` truncates before it writes. A concurrent
reader could otherwise see an empty marker, read it as owned by nobody with pid `NaN`, and delete a
live owner's marker as stale. A temporary left behind by a killed process is swept on the pid in its
own name.

**What that leaves uncovered.** A managed sidecar restarted by hand from a pane belonging to some
other agent still takes that pane's marker and removes it on exit, and the agent that owned the pane
goes dark for the rest of its session. That is not new and not a regression; it is the same
pane-versus-window confusion as above, seen from the other side, and it is why the recovery command
in the machine-local `~/CLAUDE.md` should be run from the pane it is recovering.

`stop()` reads the marker and then removes it, which is a check and then an act rather than one
atomic step, so a managed sidecar renaming its marker into place between the two loses it. The
window is microseconds against a poll of up to 10 s, and there is no atomic compare-and-delete to
reach for.

And a presence sidecar that yields the marker at startup never tries again, because `start()` runs
once. If the owner it yielded to exits and removes the file, that session stays registered with an
indicator that can never light. Re-asserting the marker on the existing one-second tick, but only
when it is missing, would close that and the hand-restart case above. It was left out as scope.

## A second pane in a managed worker's window is now suppressed

`tmuxWindowName()` resolves `#{window_name}`, so every pane in a window answers with the same name.
A Claude session opened by hand in a second pane of a managed worker's window therefore matches that
worker's agent id and stands down. Before this change it appeared as its own entry. The dotted id
space keeps two ad hoc sessions apart, but it does nothing here, because the collision is an ad hoc
session against a managed one.

The same holds across tmux sessions. Coverage compares a bare window name, and window names are
unique only within a session, so a hand-opened Claude in a window named after a live managed worker
is suppressed even from a different tmux session. Managed workers all live in `mecha`, so also
reading `#{session_name}` and requiring a match would narrow it.

## A presence session whose window name cannot be resolved can never be suppressed

`tmuxWindowName()` returns nothing when `tmux display-message` fails, and the session then registers
as `session.session.<pid>`. It can never equal a managed agent id, so it is never suppressed, and it
yields its pane marker to any live managed owner, so nothing ever writes activity for it. A
duplicate entry with a dot that cannot light. No capture has produced one; it follows from the code.

## A presence sidecar outlives its session

**What breaks.** A `session.*` agent stays registered in the router UI after Claude has exited,
holding a lease, for as long as the pane lives.

`session_end` settles the indicator but does not stop the process. The only exit is the pane
disappearing.

**What it looks like.** Idle agents in the UI for sessions that ended. Harmless until there are
many.

## A failed spawn is indistinguishable from no sidecar yet

**What breaks.** Nothing appears, and there is nothing to look at to find out why.

The `SessionStart` spawn discards stdout and stderr and depends on bare `node` resolving on the
hook's PATH, which Claude Code builds for a non-login shell. A missing `node`, or a `$sidecar` path
that does not resolve, produces the same observable state as a session that simply has not started
one yet.

## Mail addressed to a presence id is acked and discarded

**What breaks.** A message sent to a `session.*` id is silently destroyed. The sender sees a normal
delivery.

Presence clients build no injector, so there is nothing to paste it into. Acking is what stops the
router redelivering it for ever, so the discard is deliberate, but it is only visible in that
sidecar's own log.

**Mitigating.** Presence ids are dotted and nothing addresses them by hand.

## Two places the spec promises a log and the code has none

`SPEC-activity-indicators.md` says the six-hour silence bound logs when it fires, and that the
sidecar logs when it cannot stat the transcript. Neither does.

**What breaks.** These are the only two signals that separate a legitimate bound from a broken
one. An unstattable transcript silently turns interrupt detection off, and the six-hour bound then
looks like normal operation.

## The hook writes in place rather than writing and renaming

A torn read is possible because the record is written with `>` rather than to a temporary name and
renamed. The tailer handles it: a file that does not parse and does not end in a newline stops the
pass, so nothing overtakes it. That makes torn reads harmless rather than impossible, and the
largest payloads (11742 bytes measured, `PostToolUse` for the Agent tool) are the likeliest to be
caught mid-write.
