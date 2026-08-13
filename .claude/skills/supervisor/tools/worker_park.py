#!/usr/bin/env python3
"""Park a worker with mandatory verification checks.

This is the ONLY way to park a worker. It uses Mailbox V2 for delivery.
Before sending the park instruction, this script prints a mandatory checklist
that the supervisor must complete.

Usage:
  python3 worker_park.py --window WINDOW --alias ALIAS
"""

import argparse
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

TOOLS_DIR = os.path.dirname(os.path.abspath(__file__))
PARK_MESSAGE = "Ok, parking you for now."


def main():
    parser = argparse.ArgumentParser(description="Park a worker with mandatory verification checks.")
    parser.add_argument("--window", required=True, help="Worker tmux window name")
    parser.add_argument("--alias", required=True, help="Task alias (for state lookup)")
    args = parser.parse_args()

    # Print mandatory checklist BEFORE parking — uses stdout so it appears
    # as tool output (not stderr which may be hidden), matching the mailbox
    # delivery mandatory block pattern.
    print("*** MANDATORY — COMPLETE ALL CHECKS BEFORE READING FURTHER ***")
    print(f"Step 1: Read the task requirements:")
    print(f"   python3 {TOOLS_DIR}/state_query.py --alias {args.alias}")
    print(f"   Does the completion mail match EVERY requirement?")
    print(f"Step 2: Review the diff:")
    print(f"   cd <worktree> && git diff --stat HEAD")
    print(f"   Are only expected files modified?")
    print(f"Step 3: Check evidence in completion mail (test output, screenshots, deferrals)")
    print(f"Step 4: Run explain --overview (MANDATORY — spawn background subagent):")
    print(f"   Run /explain --overview on the worker's changes in the worktree.")
    print(f"   Read the Decisions and Questions sections.")
    print(f"   If concerns are valid, unpark and send the worker back.")
    print(f"Step 5: Set a follow-up reminder for this parked worker.")
    print(f"*** END MANDATORY BLOCK ***")
    print("", file=sys.stderr)

    import subprocess

    result = subprocess.run(
        [
            sys.executable,
            os.path.join(TOOLS_DIR, "mailbox_send.py"),
            "--from", "supervisor",
            "--to", args.window,
            "--body", PARK_MESSAGE,
        ],
        capture_output=True,
        text=True,
    )
    if result.returncode != 0:
        if result.stderr.strip():
            print(f"Error: Mailbox V2 delivery failed: {result.stderr.strip()}", file=sys.stderr)
        sys.exit(result.returncode)

    print(f"Park instruction sent to '{args.window}' through Mailbox V2.")


if __name__ == "__main__":
    main()
