#!/usr/bin/env python3
"""Send a message to a running worker.

Usage:
  python worker_send.py --window WINDOW --text TEXT [--escape] [--no-enter] [--delay SECONDS]

Sends text to a tmux window with proper timing. Handles the critical
sleep-between-text-and-Enter pattern that Claude Code and Codex require.

For long instructions (over 500 chars), write them to TASK.md in the worktree
first, then send a short pointer:
  python worker_send.py --window WINDOW --text "Read TASK.md for your instructions."

Modes:
  Default:    Send text, wait 1s, press Enter (normal message)
  --escape:   Send Escape first (hard intervention — cancel in-flight op)
  --no-enter: Send text only, don't press Enter (for multi-part messages)

Examples:
  # Normal message
  python worker_send.py --window worker-1 --text "Your task: fix the login bug"

  # Hard intervention (cancel + correct)
  python worker_send.py --window worker-1 --escape --text "Stop. This is too complex."

  # First message to a Codex worker (needs longer delay)
  python worker_send.py --window worker-1 --text "Read TASK.md for your instructions." --delay 3

  # Send skill command
  python worker_send.py --window worker-1 --text "/robot-review"

  # Send /exit
  python worker_send.py --window worker-1 --text "/exit"

  # Send /compact
  python worker_send.py --window worker-1 --text "/compact"
"""

import argparse
import json
import os
import sys
import time
from datetime import datetime, timezone

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from _tmux import (
    send_text_and_enter,
    send_escape,
    window_exists,
)

# Messages that must NOT be prefixed (hook-matched phrases, slash commands)
_NO_PREFIX_PATTERNS = {"git authorized", "gh authorized"}

SEND_TIMESTAMPS_PATH = os.path.expanduser("~/.mecha-wolfgang/worker_send_timestamps.json")


def _should_prefix(text):
    """Return True if this message should get the [Supervisor] prefix."""
    stripped = text.strip()
    # Slash commands are interpreted literally by Claude Code
    if stripped.startswith("/"):
        return False
    # Hook-matched authorization phrases must be exact
    if stripped.lower() in _NO_PREFIX_PATTERNS:
        return False
    return True


def _record_send_timestamp(window):
    """Record when the supervisor last sent a message to this window."""
    try:
        with open(SEND_TIMESTAMPS_PATH) as f:
            timestamps = json.load(f)
    except (FileNotFoundError, json.JSONDecodeError):
        timestamps = {}

    timestamps[window] = datetime.now(timezone.utc).isoformat()

    os.makedirs(os.path.dirname(SEND_TIMESTAMPS_PATH), exist_ok=True)
    with open(SEND_TIMESTAMPS_PATH, "w") as f:
        json.dump(timestamps, f, indent=2)


def main():
    parser = argparse.ArgumentParser(
        description="Send a message to a worker in a tmux window."
    )
    parser.add_argument("--window", required=True, help="Tmux window name")
    parser.add_argument("--text", required=True, help="Text to send to the worker")
    parser.add_argument(
        "--escape",
        action="store_true",
        help="Send Escape before the message (hard intervention)",
    )
    parser.add_argument(
        "--no-enter",
        action="store_true",
        help="Don't press Enter after sending text",
    )
    parser.add_argument(
        "--delay",
        type=float,
        default=None,
        help="Seconds to wait between text and Enter (default: 1 for Claude, use 3+ for Codex first message)",
    )
    parser.add_argument(
        "--cr",
        action="store_true",
        help="Send C-m (carriage return) instead of Enter",
    )
    parser.add_argument(
        "--lf",
        action="store_true",
        help="Send C-j (line feed) instead of Enter",
    )
    args = parser.parse_args()

    if not window_exists(args.window):
        print(f"Error: window '{args.window}' does not exist", file=sys.stderr)
        sys.exit(1)

    # Reject park messages — must use worker_park.py for the mandatory checklist
    text_lower = args.text.lower()
    # Don't block auth phrases or slash commands
    is_auth = text_lower.strip() in _NO_PREFIX_PATTERNS
    is_slash = args.text.strip().startswith("/")
    if not is_auth and not is_slash and "park" in text_lower:
            print(
                "Error: Messages containing 'park' must go through worker_park.py.\n"
                "\n"
                "This ensures the mandatory verification checklist is completed.\n"
                "Use:\n"
                f"  python3 {os.path.join(os.path.dirname(os.path.abspath(__file__)), 'worker_park.py')} "
                f"--window {args.window} --alias <ALIAS>\n",
                file=sys.stderr,
            )
            sys.exit(1)

    MAX_DIRECT_TEXT = 500
    if len(args.text) > MAX_DIRECT_TEXT:
        print(
            f"Error: --text is {len(args.text)} chars (limit: {MAX_DIRECT_TEXT}).\n"
            "\n"
            "Long instructions must be written to TASK.md in the worktree first,\n"
            "then sent as a short pointer to avoid tmux input buffer issues:\n"
            "\n"
            "  1. Write full instructions to <worktree>/TASK.md\n"
            f"  2. python3 worker_send.py --window {args.window} \\\n"
            f"       --text \"Read TASK.md for your instructions.\"",
            file=sys.stderr,
        )
        sys.exit(1)

    # Prefix non-command messages with [Supervisor] so the watchdog can
    # distinguish supervisor messages from user-typed input in captures.
    text_to_send = args.text
    if _should_prefix(args.text):
        text_to_send = f"[Supervisor] {args.text}"

    if args.escape:
        send_escape(args.window)
        time.sleep(2)  # Give Claude time to cancel the in-flight operation
        print(f"Sent Escape to '{args.window}' (cancelled in-flight operation).")

    if args.no_enter:
        from _tmux import send_text
        send_text(args.window, text_to_send)
        print(f"Sent text to '{args.window}' (no Enter).")
    else:
        kwargs = {}
        if args.delay is not None:
            kwargs["delay"] = args.delay
        if args.cr:
            kwargs["use_cr"] = True
        if args.lf:
            kwargs["use_lf"] = True
        send_text_and_enter(args.window, text_to_send, **kwargs)
        mode = "hard intervention" if args.escape else "message"
        print(f"Sent {mode} to '{args.window}'.")

    _record_send_timestamp(args.window)

    # Reminders
    print()
    print("--- Reminders ---")
    print(f"1. Update supervisor state with what you sent:")
    print(f"   python3 state_update_status.py <alias> \"<what you told the worker>\"")
    if args.text == "/exit":
        print(f"2. After ~2 seconds, kill the window:")
        print(f"   python3 worker_kill.py --window {args.window}")
        print(f"3. Update task state to completed or note worker exited.")
    elif args.text == "/compact":
        print(f"2. Wait ~5 seconds for compaction, then send the next phase instructions.")
    else:
        print(f"2. Consider setting a follow-up reminder if appropriate:")
        print(f"   /reminders set --window supervisor --in <duration> --message \"Check {args.window} — ...\"")

    # Park keyword detection — requirements must be verified before parking is treated as final
    text_lower = args.text.lower()
    if any(kw in text_lower for kw in ("parking", "park you", "parked", "park now", "stand by", "sit tight", "wait for")):
        print()
        print("--- REQUIREMENTS CHECK (park/wait message detected) ---")
        print("Before this park is final, verify requirements are met:")
        print(f"  python3 state_query.py --alias <alias>   # check 'requirements' field")
        print("  Or read TASK.md in the worktree directly.")
        print("  For iterative tasks: is the loop termination condition satisfied?")
        print("  ('repeat until', 'keep going until', 'until every scenario...')")
        print("  If NOT fully met: redirect the worker — do not leave it parked.")


if __name__ == "__main__":
    main()
