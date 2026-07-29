#!/usr/bin/env python3
"""Capture terminal output from a worker's tmux window.

Usage:
  python worker_capture.py --window WINDOW [--lines N] [--tail N]

Examples:
  python worker_capture.py --window worker-1
  python worker_capture.py --window worker-1 --lines 100
  python worker_capture.py --window worker-1 --tail 20
"""

import argparse
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from _tmux import capture_pane, window_exists


def build_parser():
    """Return the argument parser. Exposed so docs can be checked against it."""
    parser = argparse.ArgumentParser(
        description="Capture terminal output from a worker's tmux window."
    )
    parser.add_argument("--window", required=True, help="Tmux window name")
    parser.add_argument(
        "--lines",
        type=int,
        default=50,
        help="Lines of scrollback to capture (default: 50)",
    )
    parser.add_argument(
        "--tail",
        type=int,
        default=None,
        help="Only show the last N lines of output",
    )
    return parser


def main():
    args = build_parser().parse_args()

    if not window_exists(args.window):
        print(f"Error: window '{args.window}' does not exist", file=sys.stderr)
        sys.exit(1)

    output = capture_pane(args.window, lines=args.lines)
    if output is None:
        print(f"Error: failed to capture pane for '{args.window}'", file=sys.stderr)
        sys.exit(1)

    lines = output.rstrip("\n").splitlines()

    if args.tail and args.tail < len(lines):
        lines = lines[-args.tail:]

    print("*** MANDATORY — DO THIS BEFORE READING THE OUTPUT BELOW ***")
    print(f"Step 1: Run state update now:")
    print(f'   python3 state_update_status.py {args.window} "<one-line summary>"')
    print(f"Step 2: Read the output.")
    print(f"Step 3: If status changed (blocked/completed/abandoned), update state again.")
    print(f"Step 4: If the worker claims to be done, verify independently before shutting down.")
    print("*** END MANDATORY BLOCK ***")
    print()
    print("\n".join(lines))


if __name__ == "__main__":
    main()
