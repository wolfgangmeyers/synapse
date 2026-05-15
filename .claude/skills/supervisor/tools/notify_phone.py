#!/usr/bin/env python3
"""Send a notification to Wolfgang's phone via SSH + termux-notification.

Usage:
  python notify_phone.py --title TITLE --content CONTENT

Requires SSH key access to the phone (note.tailcdfd89.ts.net) on port 8022.

Examples:
  python notify_phone.py --title "Build done" --content "All tests passed"
  python notify_phone.py --title "Worker blocked" --content "worker-3 needs input"
"""

import argparse
import shlex
import subprocess
import sys


PHONE_HOST = "note.tailcdfd89.ts.net"
PHONE_PORT = "8022"
PHONE_USER = "u0_a633"


def main():
    parser = argparse.ArgumentParser(
        description="Send a notification to Wolfgang's phone."
    )
    parser.add_argument("--title", required=True, help="Notification title")
    parser.add_argument("--content", required=True, help="Notification body text")
    args = parser.parse_args()

    cmd = [
        "ssh",
        "-o", "StrictHostKeyChecking=no",
        "-o", "ConnectTimeout=5",
        "-p", PHONE_PORT,
        f"{PHONE_USER}@{PHONE_HOST}",
        f"termux-notification --title {shlex.quote(args.title)} --content {shlex.quote(args.content)}",
    ]

    try:
        result = subprocess.run(cmd, capture_output=True, text=True, timeout=15)
        if result.returncode == 0:
            print(f"Notification sent to phone: {args.title}")
        else:
            print(f"Error sending notification (exit {result.returncode}):", file=sys.stderr)
            if result.stderr:
                print(result.stderr.strip(), file=sys.stderr)
            sys.exit(1)
    except subprocess.TimeoutExpired:
        print("Error: SSH connection to phone timed out", file=sys.stderr)
        sys.exit(1)
    except FileNotFoundError:
        print("Error: ssh command not found", file=sys.stderr)
        sys.exit(1)


if __name__ == "__main__":
    main()
