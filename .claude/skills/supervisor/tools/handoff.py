#!/usr/bin/env python3
"""Context teleport: /clear a tmux window and schedule a reminder to resume.

Mechanical script — the caller owns the handoff file content. This script:
1. Validates the handoff file exists and is non-empty
2. Schedules a reminder (10s in the future) that tells the agent to read
   the handoff file
3. Sends /clear to the target window

The reminder is delivered via the existing reminder_cron.py → mailbox
pipeline. The script does NOT send the resume prompt directly — while a
script holds the tmux input, the window can't process new input anyway.

Usage:
  python handoff.py --handoff-file <path> --window <name>

Exit codes:
  0 — success
  1 — argparse/usage error (default)
  2 — handoff file missing, unreadable, or empty
  3 — target tmux window does not exist
  4 — tmux send-keys failure

Examples:
  python handoff.py --handoff-file /tmp/handoff.md --window supervisor
"""

import argparse
import os
import pathlib
import subprocess
import sys
import time
from datetime import datetime, timedelta, timezone

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from _tmux import send_text_and_enter, window_exists

REMINDERS_TOOLS = pathlib.Path(__file__).parent.parent.parent / "reminders" / "tools"
sys.path.insert(0, str(REMINDERS_TOOLS))
from _reminders import locked_reminders


EXIT_FILE_BAD = 2
EXIT_WINDOW_MISSING = 3
EXIT_TMUX_FAIL = 4


def main():
    parser = argparse.ArgumentParser(
        description="Clear a Claude Code window and schedule a reminder to read the handoff file."
    )
    parser.add_argument(
        "--handoff-file",
        required=True,
        help="Path to the handoff markdown file (must exist and be non-empty)",
    )
    parser.add_argument(
        "--window",
        required=True,
        help="Tmux window name in the mecha session",
    )
    args = parser.parse_args()

    # 1. Validate handoff file: exists, readable, non-empty
    handoff_path = os.path.abspath(os.path.expanduser(args.handoff_file))
    if not os.path.isfile(handoff_path):
        print(f"Error: handoff file not found: {handoff_path}", file=sys.stderr)
        sys.exit(EXIT_FILE_BAD)
    if not os.access(handoff_path, os.R_OK):
        print(f"Error: handoff file not readable: {handoff_path}", file=sys.stderr)
        sys.exit(EXIT_FILE_BAD)
    if os.path.getsize(handoff_path) == 0:
        print(f"Error: handoff file is empty: {handoff_path}", file=sys.stderr)
        sys.exit(EXIT_FILE_BAD)

    # 2. Validate target window exists
    if not window_exists(args.window):
        print(
            f"Error: tmux window 'mecha:{args.window}' does not exist",
            file=sys.stderr,
        )
        sys.exit(EXIT_WINDOW_MISSING)

    # 3. Schedule reminder FIRST (before /clear), so it's queued even if
    #    the script is interrupted after sending /clear.
    reminder_delay_sec = 10
    fire_at = datetime.now(timezone.utc) + timedelta(seconds=reminder_delay_sec)
    reminder = {
        "id": f"r-handoff-{int(time.time() * 1000)}",
        "window": args.window,
        "message": (
            f"MANDATORY: You have no context. Read the file {handoff_path} right now "
            f"using the Read tool. It contains everything you need to resume. "
            f"Do NOT ask questions — just read that file and follow the bootstrap "
            f"instructions inside it."
        ),
        "fire_at": fire_at.isoformat(),
        "delivery": "tmux",
        "created_at": datetime.now(timezone.utc).isoformat(),
    }
    try:
        with locked_reminders() as reminders:
            reminders.append(reminder)
        print(f"Reminder scheduled for {reminder_delay_sec}s: {reminder['id']}")
    except Exception as e:
        print(f"Error: could not schedule reminder: {e}", file=sys.stderr)
        sys.exit(1)

    # 4. Send /clear
    try:
        send_text_and_enter(args.window, "/clear")
    except subprocess.CalledProcessError as e:
        print(f"Error: tmux send-keys failed: {e}", file=sys.stderr)
        if e.stderr:
            print(e.stderr, file=sys.stderr)
        sys.exit(EXIT_TMUX_FAIL)

    print(
        f"Handoff dispatched to mecha:{args.window}, handoff file: {handoff_path}"
    )


if __name__ == "__main__":
    main()
