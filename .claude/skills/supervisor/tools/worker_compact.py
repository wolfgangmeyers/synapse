#!/usr/bin/env python3
"""Send /compact to a worker to compress its context.

Usage:
  python worker_compact.py --window WINDOW

Convenience wrapper that sends /compact to a tmux window.
Equivalent to: worker_send.py --window WINDOW --text "/compact"
but without the 500-char checks, park rejection, or [Supervisor] prefix logic.

Examples:
  python worker_compact.py --window worker-1
  python worker_compact.py --window review-3645
"""

import argparse
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from _tmux import send_text_and_enter, window_exists


def main():
    parser = argparse.ArgumentParser(
        description="Send /compact to a worker window to compress context."
    )
    parser.add_argument("--window", required=True, help="Tmux window name")

    args = parser.parse_args()

    if not window_exists(args.window):
        print(f"Error: window '{args.window}' does not exist", file=sys.stderr)
        sys.exit(1)

    send_text_and_enter(args.window, "/compact")
    print(f"Sent /compact to '{args.window}'.")

    # Reminders
    print()
    print("--- Reminders ---")
    print("1. Wait ~5 seconds for compaction to complete.")
    print("2. Then send the next phase instructions or check on the worker.")


if __name__ == "__main__":
    main()
