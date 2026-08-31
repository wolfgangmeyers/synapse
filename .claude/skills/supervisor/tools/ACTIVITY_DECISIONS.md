# Activity indicators: decisions and what they replaced

Why the activity path is built the way it is. `hooks/claude-activity.sh`, `activity_tail.mjs`,
`activity_state.mjs` and the activity parts of `claude_sidecar.mjs` say what happens. This says what
else was on the table and why it lost, which is what someone needs who wants to change one of them.

Taken from the plan and spec for this work before those files were cleared. Everything here was
decided on 2026-08-09 against Claude Code 2.1.226, apart from the sections dated later. The
measurements behind most of it are in `tests/fixtures/claude-hooks/`, and its README maps each probe
to what it shows.

Decisions that were made and then overturned are marked **Reversed**, with the original reasoning
kept. Read those first. The first answer was reasonable every time and the evidence still went the
other way.

The `harness/router.js` citations below record legacy Node-router behavior from this dated decision
record. The supported router is Go; use `tools/run_router.sh` and the current Go transport sources
for live behavior. The Node implementation remains only an explicit rollback/differential path.

## Getting activity to the router

**An HTTP activity route on the router. Rejected.** `harness/router.js` has no token, header or
credential check on any route. An activity endpoint there would let any local process claim any
agent id and drive another worker's indicator.

**The hook opening its own websocket. Rejected.** `receiveActivity` at `harness/router.js:489`
checks the socket session's `agentId`, `instanceId` and `leaseId` against the frame and closes the
socket with code 4003 on a mismatch. The sidecar holds that lease, and it is the process that
delivers mail. The hook would have to take the lease away from it.

**The state machine lives in the sidecar and not in the hook.** The hook sits on the agent's
critical path, is measured in milliseconds, and runs once per event with no memory of the last one.
The sidecar is long-lived, already connected, and already the only process the router accepts
activity from.

## What the hook writes

**POSIX shell, not Python. Reversed.** The first design was one Python script measured at 22 ms per
invocation. Most of that was the interpreter starting, and 3 ms more was a `tmux display-message`
subprocess inside `current_agent_id()`. Shell plus a marker file that names the agent took it to
5.7 ms with a sidecar watching and 3.7 ms with none.

**Identity comes from the marker and not from tmux.** The sidecar already knows its agent id,
because `worker_launch.py` passes it as `--agent-id`. Writing it into the marker means the hook
never shells out to tmux at all.

**The hook checks for a live sidecar before it does anything else.** It reads a marker naming the
sidecar and its pid, and `kill -0` on that pid is a shell builtin that forks nothing. The point was
that unwatched sessions did no work. That value shrank once every session started its own sidecar,
because then there are almost none. The check stays because it still covers sessions that cannot
have a sidecar, the window before one starts, and a sidecar that died.

**One file per hook invocation. Reversed.** The spec called for one append-only JSON Lines file per
agent. The largest real payload is 11742 bytes, from `PostToolUse` for the Agent tool, and six of
the eight event types exceed `PIPE_BUF`, so nothing here is atomic by the standard. Eight processes
appending 11.7 KB records to one file destroyed 255 of 320 lines, split around 1 KB and interleaved.
Sixteen concurrent hooks writing their own files produced sixteen intact files, at a cost of 0.3 ms.

The near miss is worth keeping. An earlier version of that test used a 674-byte payload, which was
simply the first one measured. It passes clean against the design that corrupts.

**Extracting only the fields the consumer needs, so records fit inside the atomic write size.
Rejected.** Small records would be atomic by the standard and not by construction, which is the
better guarantee. It needs JSON parsing in shell, and the field most worth having is the subagent
description, which is the field most likely to carry quotes and escapes.

**A lock around the shared file. Reversed.** Revision 2 added one because concurrent hook processes
were racing during rotation, along with a 256 KiB cap and the rotation itself. One file per process
removed the contention, and the lock, the cap and the rotation all left with it. The file cannot
grow while nothing is consuming it: the sidecar deletes what it reads, and the hook writes nothing
when no sidecar is watching.

## Knowing when a turn ends

**Inferring idle from silence. Rejected.** A ten-minute shell command emits no hook events while the
agent works hardest, so a timeout would report idle during exactly the tasks someone wants to watch.
Idle comes from `Stop`, and for an interrupted turn it comes from the transcript growing. Neither is
a timer on how long work may take.

**Scraping the tmux pane for the spinner. Rejected.** It needs no install step, which is its only
virtue. It parses ANSI output that changes with every Claude Code release, breaks when the pane is
scrolled or resized, and cannot see subagent task text.

**Reading Claude's transcript as the only source, with no hook. Rejected, then partly adopted.** Two
objections. The sidecar cannot find the transcript on its own, because the session id appears only
inside hook payloads. And reconstructing what the agent is doing means parsing an internal format
with no compatibility promise, which a Claude Code release could break silently.

Both objections are answered for the narrower use the design makes of it. There is a hook, and every
payload carries `transcript_path`, so the sidecar learns which file is its own from the stream it
already reads. Nothing parses the file, because the sidecar compares its size to the last size it
saw. Reconstructing behavior needs the schema to hold. A byte count survives a renamed field, a new
record type and a reshaped schema, and still answers the one question being asked: did this turn
advance.

**The growth rule looks backward, over a 2-second window. Reversed.** The first version looked only
forward: growth, then no hook record within 8 seconds, means an interrupt.
`probe10-transcript-evidence.jsonl` disproves it. Growth lands at t+8.0 and the next hook record is
`PostToolUse` at t+16.8, so the timer would have fired at t+16.0 and put the dot out 0.8 seconds
before the command finished. That is the failure the whole design exists to prevent, produced by the
mechanism meant to prevent it, in the capture that was supposed to prove it worked.

Measured across both preserved captures, legitimate growth trails the hook record that caused it by
0.1, 0.3 and 0.9 seconds. Interrupt growth came 3.9 and 4.9 seconds after the last record. So growth
within 2 seconds of a preceding hook record is explained by it, and growth whose most recent hook
record is older than that starts the 8-second interrupt timer.

**A transcript size of -1 is not a sample.** The sampler reports -1 until a record has supplied a
path, and `probe10` opens with four of those. Treating -1 as a size makes the first real reading
look like growth.

**The six-hour silence bound stays even though the transcript signal makes it unreachable.** It
covers the one case where the transcript cannot be stat'd at all: deleted, rotated by Claude Code,
or on a filesystem the sidecar cannot reach. Interrupts are undetectable then, and the dot would
stay lit forever. It is sized against the longest real command on this machine, a `pnpm` eval sweep
of about 90 minutes, with a four-times margin. It is not a disguised version of the timeout rejected
above, because it fires only when the one bit is unavailable.

**A worker blocked on a human settles.** Blocking on `ExitPlanMode` or `AskUserQuestion` looks
identical to hard work at the hook level: a `PreToolUse` with no `PostToolUse`, and a transcript
that holds still, measured at 30596 bytes across ten samples over 20 seconds. `Notification` is what
separates them, firing about 6 seconds in with `permission_prompt`, and there is an `idle_prompt`
variant. Idle is the honest reading, because the worker is waiting for Wolfgang. A lit dot there is
worse than a dead one, since it gives him no reason to look at it.

Nothing depends on `Notification` firing. `idle_prompt` did not appear in the two minutes after an
interrupt, so it does not solve that case, and every promised behavior holds if the event never
fires again.

## Subagents

**Labeling subagents by type and not by task text.** This was the fallback if no correlation existed
between a launch and a subagent. The correlation is real. Agent `PreToolUse` carries
`tool_input.description`, `SubagentStart` follows about 30 ms later, and the two are strictly
interleaved one to one. `PostToolUse` returns `tool_response.agentId` as ground truth and confirmed
the pairing in both probes, including two subagents of the same type launched in one message, where
ordering is the only thing separating them. So the badge carries the text a person actually wrote,
and `agent_type` stays as the fallback for an empty description.

**Correction on completion repairs a swapped pair and does not repair a three-way rotation.**
`subagent_done` carries the ground truth for which launch produced which subagent, so a wrong
pairing can be swapped back. What that buys is a still-running sibling whose label was swapped with
the one that just finished. One swap cannot fix a rotation of three, and the third stays wrong. No
probe has ever produced a mispairing, so the whole correction step is insurance.

**Bindings outlive `subagent_stop`.** `SubagentStop` always arrives before the Agent `PostToolUse`,
in both subagent probes for all four subagents. So a subagent leaves the running set on its stop,
which is what the badge reads, and its binding is held until `subagent_done` arrives or the turn
settles. Revision 2 omitted this and could not have been implemented as written.

**Both labels empty means counted with no name.** The router drops any summary whose description
trims to empty (`normalizeSubagents`, `harness/router.js:1033`), while `nextCount` takes the larger
of the count and the surviving summaries. A blank-labeled summary and an omitted one produce the
same display, and omitting is the honest version. No capture has produced this case, so its test is
synthetic and says so.

**Tools in flight are a set keyed by `tool_use_id`. Reversed.** Revision 2 used a boolean. The agent
runs tool calls in parallel, so the boolean cleared on the first of several completions.

**The pending queue, the running set and the binding map are capped at 999 and drop the oldest.**
999 matches the router's own subagent ceiling. Their only other bound is a settle, and an interrupt
is exactly what suppresses settles, so the cap is the bound that actually holds.

## Compaction

**Compacting must have a clearing event, and two are kept.** The router sets
`session.deliveryPaused = true` when an agent goes compacting (`harness/router.js:1095`), gives that
state `Number.POSITIVE_INFINITY` expiry (`:1100`), and explicitly skips clearing it when the socket
closes (`:295`). A compacting frame with nothing after it pauses that worker's mail permanently, and
killing the sidecar does not recover it. `PostCompact` clears it, and `SessionStart` with
`source: "compact"` clears it as a second path. Either alone is enough. Both are kept because they
cost one table row and the failure they prevent is the worst one in this design.

**`PostCompact` was found by reading the shipped binary, then probed before anything leaned on it.**
The hooks documentation does not mention it. The same read gave the full `SessionStart` `source`
list: `startup`, `resume`, `clear`, `compact`, `fork`. A schema in a binary says a thing was built,
so the probe is what showed it fires.

**An unmatched `subagent_stop` changes nothing at all, including the activity state. Reversed.**
Revision 2 treated every subagent event as activity. Compaction runs its summarizer as a subagent
and emits a `SubagentStop` whose `agent_id` never had a start, carrying `agent_type: ""`. Under
revision 2 that stop would have flipped the agent out of compacting 17 seconds in and released the
mail pause early. Measured: `PreCompact` at t+31.1, the unmatched stop at t+48.2, `session_start` at
t+48.2. So the no-op has to cover the state transition and not only the count.

**`session_start` is read by its `source`. Reversed.** Revision 2 reset everything on any
`session_start`, which would have wiped the subagent state of a session that was only compacting.
`source: "compact"` arrives mid-session with the same `session_id` and clears the compacting state
alone. Every other source is a new session and resets everything. The rule is written as "compact
clears, anything else resets" so a source added by a future release resets and does not fall through
with no defined transition.

**While compacting, every settle record is ignored.** A settled frame sent while the router holds
the agent in compacting takes the branch at `harness/router.js:1107-1110`, which sets
`deliveryPaused = false` and calls `offerQueuedMessages`. So a `Stop`, a `SessionEnd` or an
`idle_prompt` landing between `PreCompact` and `PostCompact` releases the mail pause early. Both
compaction captures happen to show `Stop` before `PreCompact`, so the evidence never disproved this
path, and automatic compaction is where it would bite.

## Turn boundaries

**`turn_start`, `turn_end` and `session_end` clear the running set, the pending queue and the
in-flight set. A notification does not.** An interrupted subagent never reports stopping, so it
stays in the running set. Without the reset, one interrupt corrupts every later turn for the life of
the sidecar. The exact shape is worth stating, because the obvious version is wrong: the sidecar
settles with a count of 1, the router takes its `nextCount > 0` branch even for idle
(`harness/router.js:1096`), and the phantom is broadcast and then swept 15 seconds later. The badge
does clear while the worker sits idle. It comes back on the next turn and every turn after.

Clearing on a notification would be wrong for the same reason. A permission prompt fires about 6
seconds into any approval block, including one that arrives while subagents are still running.
Clearing there wipes live state, their later stops are then unmatched and ignored, and the badge
cannot recover until the next turn.

**`turn_start` clears as well as `turn_end`,** because the interrupt settle can be canceled by any
hook record inside its 8-second window. Pressing escape and retyping a prompt within 8 seconds is an
ordinary thing to do, and without the reset that sequence carries the interrupted turn's phantom
subagents and in-flight ids into the next turn.

## The tailer

**On start it consumes whatever is already in the directory, oldest first. Reversed twice.** The
first version started at end of file, which left a sidecar restarted mid-turn dark until the next
hook event. For a 90-minute command that is 90 minutes of the exact failure this design exists to
prevent, and restarting a sidecar by hand is a documented routine. Revision 2 replaced it with a
hand-built replay to the last turn boundary. Consuming the directory achieves the same thing with no
replay code, because the sidecar deletes each file once it has read it.

**The tailer owns all file I/O, including the transcript size sample. Reversed.** The previous
revision sampled the transcript inside the state machine while calling that state machine pure,
which was a contradiction. The sample is now a `transcript_size` record fed in like any other.

**Ordering is by mtime.** Nanosecond resolution here, which separates events arriving 30 ms apart
with room to spare.

**Nothing is emitted before the first record is seen.** The transcript path arrives on hook records,
so a fresh sidecar that has not identified its transcript cannot settle an agent on it.

**Every activity callback catches at its own top.** `runCli` parks on `await new Promise(() => {})`
(`claude_sidecar.mjs:181`), so an uncaught throw in a timer callback ends the process and takes mail
delivery with it. `sendActivity` throws when the socket is between reconnects
(the generated support client at `~/.mecha-developer/extensions/pi-extensions/mecha-messaging/mailbox-client.js`), so this is reachable.

## Installing

**The global settings edit was deferred, then approved. Reversed.** The spec left
`~/.claude/settings.json` untouched, because it affects every Claude session on this machine and
Wolfgang had not approved it. Every measurement ran through `claude --settings <file>`, which loads
a settings file for one session only. He approved the global install on 2026-08-09 before being
asked, against a measured cost of about 6 ms per tool call and roughly 2 seconds across 300.

**Every Claude session is in scope, not only managed workers.** Wolfgang asked for this directly on
2026-08-09. A session appears in the web UI only while something holds its lease with the router,
and that is the sidecar, so `SessionStart` starts one when the marker is absent. Sessions outside
tmux are left out, because identity is the tmux window name and there is nothing to display them
under.

**Registration goes through the installer and not a hand-written settings edit.**
`.claude/skills/install/SKILL.md` already symlinks hook scripts into `~/.claude/hooks/` and merges
entries into `~/.claude/settings.json`.

**The installer's dedupe matches the script name anywhere in the command.** The existing check asks
whether a registered command ends with the script name. These commands end with the event name, so a
check copied from it never matches. Every `/install` run would then append another eleven entries to
a file Wolfgang wrote by hand, without limit and with no symptom, and the duplicates multiply the
per-tool-call cost he agreed to.

**The events are named and never counted.** A count in two documents goes stale, and this one
already did: the spec said eight and the plan said twelve against a table of eleven. Dropping
`PostCompact` and `SessionStart` leaves a compacted worker's mail paused indefinitely, so a test
asserts the installer's registered set equals the hook table.

## Measurement

**The session-level latency comparison was abandoned as inconclusive.** The same task run with the
hooks off and with all eleven registered differed by more than the effect being measured, because
model latency varies by seconds between identical runs. The direct per-invocation timing is the
number. The session comparison happened to come out faster with the hooks on, which is noise.

**An earlier draft said hooks on one event run serially and put the cost at roughly double.
Reversed.** That came from a reviewer's claim repeated without checking, on a page whose whole point
is that the documentation was wrong three times. Three 2-second hooks on `PostToolUse` all ran from
t+0.00 to t+2.00 (`probe9-multihook.jsonl`). An event costs its slowest hook. The events themselves
are serial with each other.

**Fixtures are a deliverable.** The captures moved out of the scratchpad into
`tests/fixtures/claude-hooks/` and are committed, so the hook tests feed what Claude Code really
sent.

## Not registering twice

Added 2026-08-10, after six presence sessions were found duplicating six managed agents. The problem
and its evidence are on the known-issues page; this is why the fix is shaped the way it is.

**Ask the router.** The defect is two entries in the router's agent list, and the router is the only
thing that knows what is in that list. A presence sidecar reads `GET /v1/agents` and looks for its
own tmux window name.

**Scan for a managed sidecar process instead. Rejected.** It closes the startup race completely,
since `worker_launch.py` backgrounds the sidecar before it starts the agent, so the process exists
before Claude does. It was rejected because it answers a different question. A sidecar process that
is running and has not registered, or has given up registering, would suppress the presence session
and leave it invisible, which is the failure the feature exists to prevent, and both of those states
are already on the known-issues page. It also matches on a command line, so it breaks quietly when
an argument moves.

**Check the marker harder. Rejected.** The marker is the guard that already failed. It is keyed to a
pane rather than a window, it is written only after the managed sidecar connects, and both sidecars
in one pane write the same path.

**Put the check in the hook. Rejected.** The hook's first rule is that it never makes a network
call, and it fires once, so it cannot notice a managed sidecar that appears later.

**Make the router refuse a presence id whose embedded window is connected. Rejected.** It puts a
naming convention into the protocol layer, needs a router restart to deploy, and leaves the presence
process running and holding nothing.

**Exact id equality, never a prefix or a substring.** The router's list holds presence ids too. A
substring test would let two ad hoc sessions in identically named windows suppress each other, which
is the collision the dotted id space exists to keep apart.

**Unavailable is not absent.** A router that cannot be reached, a non-2xx, an unparseable body: all
register. A session that cannot appear is worse than a duplicate, and a router that will not answer
this GET will not accept the registration either, so the existing 15 s connect timeout ends it
anyway. The caller is told which of the two happened, because otherwise a check that is silently off
looks exactly like a healthy one.

### The ordering question, and why the answer is a repeated check

A managed worker starts its sidecar and its agent from one shell command, so a `SessionStart` can
fire before the managed sidecar has registered. The check before connecting then reads "not covered"
and the presence session registers. That window is not closed, it is corrected: the same question is
asked again every 10 s, and the session stands down the first time the answer is yes.

10 s was chosen because nothing else bounds it. `connectedAgentsSnapshot` (`harness/router.js:1140`,
filtering at `:1145`) lists every session with an open socket and a live lease, so a duplicate stays
visible for as long as it is connected.

**Nothing observed justifies the repeated check, and it is kept anyway. Read that before removing
it.** The six duplicates do not support it: every managed counterpart was registered and renewing
when its duplicate appeared, so the startup check alone would have caught all six. Nor does anything
else in the retained log. Sweeping `router.jsonl.1` and `router.jsonl` together, 2026-08-09T01:21Z
to 2026-08-10T19:12Z, no managed agent ever registered while a presence session for the same window
was live. The four `van-*` presence sessions disconnected at 18:04:21, 18:04:22, 18:04:24 and
18:10:19 UTC and their managed sidecars re-registered 16 to 22 seconds afterwards, which is a window
being torn down and relaunched rather than a sidecar restarting under a live session.

What justifies it is structural. The launch race above follows from `worker_launch.py`, and the
documented sidecar restart can be run at any time, including under a session that has already
registered. Both follow from the code. Neither has been caught happening, and this page says so
rather than borrowing the six as evidence for something they do not show.

### Marker ownership, and why the two rules are asymmetric

Both sidecars in a shared pane write the same marker path, so a sidecar removes the marker only
while it still names that sidecar, and a presence sidecar does not take over a marker held by a live
foreign owner. Either rule alone still loses the marker: with only the first, the presence sidecar
clobbers the file and then owns what it deletes; with only the second, its exit removes the file
unconditionally. Losing it is not cosmetic, because the hook exits before reading stdin when there
is no marker, nothing rewrites it, and that worker's indicator is dead for the rest of its session
with nothing logged.

**A managed sidecar never yields,** because one that did would never write a marker at all, and the
presence sidecar holding it removes it on the way out. The reason is not that two managed sidecars
cannot share a pane. They can, and the residual that follows is on the known-issues page.

**Presence is read off the agent id, not carried as a separate flag.** A presence id is dotted and a
worker id cannot be, and the two validators in the generated support client at `~/.mecha-developer/extensions/pi-extensions/mecha-messaging/mailbox-client.js` are complements, so
this is enforced rather than a convention. The inbox rule in `stop()` already read it that way. A
flag alongside it would have been a second axis for one fact, and a caller that forgot to pass it
would have made a managed sidecar yield its own marker with every test still green.

**Liveness is checked, not inferred from the sweep.** `start()` sweeps markers whose owning process
is gone before it writes, so it is tempting to treat whatever survived as live. The sweep swallows
its own failures and skips a marker it cannot read, so a presence sidecar reasoning that way would
yield to a dead owner and go dark for its whole life. `isProcessAlive` is called on the owner
directly.

**The marker is written to a temporary name and renamed.** Every ownership decision is a read of
that file, and `writeFileSync` truncates before it writes, so a concurrent reader could see an empty
marker, parse it as owned by nobody with pid `NaN`, and delete a live owner's marker as stale. There
is no repair path: `start()` is the only writer and it runs once. An empty marker gets no special
case anywhere, because it matches no agent id and is never alive, so every reader already reaches
the right answer and a guard would be a branch no test could distinguish.

### A suppressed session still says so

A presence log is removed when its process exits, so a suppressed session would otherwise leave
nothing at all behind and the "nothing logs" complaint on the known-issues page would gain another
entry. One shared, size-capped file, `claude-sidecars/presence-suppressed.log`, written through the
existing `createBodyFreeLogger` under a fixed id, so retention is the same two-generation rotation
every other log here uses rather than a new mechanism.

More than one process writes it, which is not unprecedented: the fatal handler falls back to the id
`claude-sidecar` for any sidecar launched without `--agent-id`, and every presence sidecar is.
Appends are `O_APPEND`, so records stay intact. `rotateSizeCappedLog` is check-then-rename, so two
processes crossing the cap together lose both generations rather than one: the first renames the
live file over `.1`, and the second removes that `.1` and finds nothing left to rename. At one
record per suppressed session against 256 KiB that is worth stating rather than worth solving.
Writing it is wrapped, because standing down must not depend on being able to say so.

## Left open

**Splitting the lease from the mail injection.** Auto-starting a sidecar for every session makes
every session a mailbox recipient, because the sidecar's other job is pasting delivered mail into
the pane. For a worker that is the point. For a session Wolfgang opened to read something, having
text typed into it is not obviously wanted. Showing activity needs the lease, receiving mail needs
the injection, and one process does both today. This was with the supervisor when the plan was
written. Nothing in the hook or the state machine depends on the answer.

## Declined

**Making the injector confirm a paste landed before pressing Enter.** A message was lost when
Claude Code's workspace trust dialog swallowed the paste and the injector's Enter answered the
dialog. The proposed fix was to capture the pane after pasting, confirm the text was in the input
box, and refuse to acknowledge when it was not, so the router redelivered.

It was started and then stopped on 2026-08-09. The launcher now marks its work directory trusted
before starting the agent, which removes the trust dialog, and that dialog is the only case anyone
reproduced. Other full-screen selectors are expected to swallow a paste the same way, and none was
demonstrated. So the change would have added a pane capture to the path every worker's message
takes, on every message, to insure against a case nobody could name.

What remains uncovered: a session sitting on some other modal when mail arrives. The symptom is a
message that every layer reports as delivered and no agent ever reads. If that turns up, this is
the fix, and the cheaper detection is the activity receipt described in the known issues page,
which needs no pane capture and costs nothing per message.

**Update, 2026-08-10.** The uncovered case turned up the same day, by a different route than
expected. Two worker panes wedged: mail was delivered and acknowledged, the text landed in the input
box, and nothing submitted it. Enter, Escape and a redraw sent to those panes all arrived as literal
characters, so the terminal input was broken while the agent process stayed alive and idle. Both
workers sat unreachable for about forty minutes and every send reported success.

This is not a modal. The outcome is identical: a message every layer reports as delivered that no
agent reads. A pane check after pasting would have caught it, because the injected text was sitting
visible in the box and unsubmitted.

Recorded here rather than reopening the decision. Anyone reversing it now has a second confirmed
case and does not have to argue from the trust dialog alone.
