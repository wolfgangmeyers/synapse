#!/usr/bin/env python3
"""Send a handoff-file pointer through Mailbox V2.

Usage:
  python handoff.py --handoff-file <path> --window <agent-id>
"""

import argparse
import os
import subprocess
import sys

EXIT_FILE_BAD = 2


def main():
    parser = argparse.ArgumentParser(
        description="Deliver a handoff-file pointer through Mailbox V2."
    )
    parser.add_argument("--handoff-file", required=True)
    parser.add_argument("--window", required=True, help="Registered Mailbox V2 agent ID")
    args = parser.parse_args()

    handoff_path = os.path.abspath(os.path.expanduser(args.handoff_file))
    if not os.path.isfile(handoff_path) or not os.access(handoff_path, os.R_OK):
        print(f"Error: handoff file is not readable: {handoff_path}", file=sys.stderr)
        return EXIT_FILE_BAD
    if os.path.getsize(handoff_path) == 0:
        print(f"Error: handoff file is empty: {handoff_path}", file=sys.stderr)
        return EXIT_FILE_BAD

    body = (
        f"Read {handoff_path} now. It contains the handoff context and next actions. "
        "Reply by mail when you have resumed."
    )
    result = subprocess.run(
        [
            sys.executable,
            os.path.join(os.path.dirname(os.path.abspath(__file__)), "mailbox_send.py"),
            "--from", "supervisor",
            "--to", args.window,
            "--body", body,
        ],
        capture_output=True,
        text=True,
    )
    if result.returncode != 0:
        if result.stderr.strip():
            print(f"Error: Mailbox V2 delivery failed: {result.stderr.strip()}", file=sys.stderr)
        return result.returncode

    print(f"Handoff pointer sent to {args.window} through Mailbox V2.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
