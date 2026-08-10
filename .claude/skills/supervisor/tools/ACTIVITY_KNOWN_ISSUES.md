# Activity indicators: known issues

Open problems in `hooks/claude-activity.sh`, `activity_tail.mjs`, `activity_state.mjs` and the
activity parts of `claude_sidecar.mjs`, as of 2026-08-09. Each one says what it breaks, what it
looks like from outside, and what the evidence is, because every item here presents as working.

Read `tests/fixtures/claude-hooks/README.md` first. The fixtures are real captured payloads, and
most of these are testable against them.

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
(`claude_sidecar.mjs:19`, `:410`), and publishes on its first tick. So relaunching the worker is
enough. It stays paused for good only for a client that registers and sends no activity at all.

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
