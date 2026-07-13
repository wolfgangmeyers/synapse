#!/usr/bin/env python3
"""Request worker context compaction through Mailbox V2.

Usage:
  python worker_compact.py --window WINDOW
"""

import argparse
import os
import subprocess
import sys


def main():
    parser = argparse.ArgumentParser(
        description="Request context compaction through Mailbox V2."
    )
    parser.add_argument("--window", required=True, help="Mailbox V2 agent ID")
    args = parser.parse_args()

    tool = os.path.join(os.path.dirname(os.path.abspath(__file__)), "mailbox_send.py")
    result = subprocess.run(
        [
            sys.executable,
            tool,
            "--from", "supervisor",
            "--to", args.window,
            "--body", "Compact your context, then reply by mail when you are ready for the next phase.",
        ],
        capture_output=True,
        text=True,
    )
    if result.returncode != 0:
        print(f"Error: Mailbox V2 delivery failed: {result.stderr.strip()}", file=sys.stderr)
        return result.returncode

    print(f"Requested context compaction from '{args.window}' through Mailbox V2.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
