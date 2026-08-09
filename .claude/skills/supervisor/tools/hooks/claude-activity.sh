#!/bin/sh
# Activity hook for Claude Code. Records one event so the sidecar can drive the router's
# working dot and subagent badge. Claude Code emits no activity of its own, so without this
# every Claude agent shows a dead indicator while the Pi extension's agents show a live one.
#
# Invoked as: claude-activity.sh <HookEventName>
#
# Three properties matter more than anything this file does:
#
#   1. It never makes a network call and never blocks on anything but one file write. Every
#      Claude Code hook runs on the critical path of the thing it fires on, including
#      PostToolUse, which the documentation wrongly calls fire-and-forget. Measured: three
#      echo commands with a 3-second hook took 33.7 seconds.
#   2. It exits 0 on every path. A missing router, a dead sidecar, a full disk and a corrupt
#      payload are all invisible to the agent.
#   3. It writes one file per invocation. Appending to a shared file corrupts under
#      concurrency: the largest real payload is 11742 bytes (PostToolUse for the Agent tool
#      carries the whole subagent result), far above the PIPE_BUF size below which a write is
#      atomic. Eight processes appending 11.7 KB records destroyed 255 of 320 lines.
#
# The sidecar writes the marker naming itself and its pid while it holds the agent's lease,
# and removes it on exit. No marker, or a marker whose process is gone, means nothing is
# reading, so this exits before it even reads stdin. That is what keeps an unwatched session
# at roughly 3.7 ms instead of 5.7, and it is why nothing rotates or caps this directory:
# it cannot grow while no live sidecar is consuming it.

: "${MECHA_ACTIVITY_DIR:=$HOME/.mecha-developer/claude-activity}"

# Identity is the pane, not the window name. A bare `tmux display-message -p '#{window_name}'`
# returns the session's *active* window rather than the calling pane's, which silently yields
# another agent's name. The sidecar already resolved this correctly, so read its answer.
[ -n "$TMUX_PANE" ] || exit 0

marker="$MECHA_ACTIVITY_DIR/pane${TMUX_PANE}.watch"

# A session with nothing watching it cannot appear in the router UI, because appearing means
# holding a slot with the router and only managed workers get one. So on SessionStart, and only
# then, start a presence sidecar for this pane. It registers under a dotted id that no managed
# worker can have, and it builds no injector, so it can show activity and can never become a
# terminal that mail is pasted into.
#
# Once per session and never on the per-tool-call path. If a live sidecar is already watching this
# pane, this does nothing.
if [ "$1" = "SessionStart" ] && [ ! -f "$marker" ]; then
  self="$0"
  link=$(readlink "$self" 2>/dev/null) && [ -n "$link" ] && self="$link"
  sidecar="$(dirname "$self")/../claude_sidecar.mjs"
  if [ -f "$sidecar" ]; then
    nohup node "$sidecar" --presence-only >/dev/null 2>&1 &
  fi
fi

[ -f "$marker" ] || exit 0

# `agent` is the sidecar's id; `pid` lets a marker left behind by a killed sidecar be ignored.
# `kill -0` is a shell builtin, so this costs no process.
read -r agent pid < "$marker" || exit 0
[ -n "$agent" ] || exit 0
[ -n "$pid" ] || exit 0
kill -0 "$pid" 2>/dev/null || exit 0

# $( ) strips the trailing newline the payload arrives with, which would otherwise split the
# record across two lines. The payload is written through unparsed: the fields worth having,
# notably the subagent description, are the ones most likely to contain quotes and escapes,
# and the sidecar has a real JSON parser.
payload=$(cat)
[ -n "$payload" ] || exit 0

printf '{"v":1,"event":"%s","payload":%s}\n' "$1" "$payload" \
  > "$MECHA_ACTIVITY_DIR/$agent.d/$$.json" 2>/dev/null

exit 0
