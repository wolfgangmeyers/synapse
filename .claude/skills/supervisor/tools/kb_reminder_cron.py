#!/usr/bin/env python3
"""Retired compatibility utility for sending a KB maintenance reminder.

Do not schedule this script with cron. The router-owned
``kb-maintenance-reminder`` job is the canonical scheduler. This utility
remains only for explicit, compatible manual invocation.
"""

import argparse
import os
import subprocess
import sys


DEFAULT_RECIPIENT = "supervisor"
DEFAULT_SENDER = "kb-reminder-cron"
DEFAULT_SUBJECT = "KB maintenance reminder"
SEND_SCRIPT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "mailbox_send.py")

DEFAULT_BODY = """KB maintenance reminder:

Check whether anything important has happened since the last KB update.
Use the kb skill to update ~/.mecha-developer/kb/ for durable knowledge:
major decisions, project outcomes, changed operating rules, external-system
details, reusable investigations, and non-obvious lessons.

Do not store transient task state, raw logs, or facts better read directly from
current code. Prefer updating existing KB topics over creating duplicates.
"""


def send_reminder(body, recipient, sender, subject):
    cmd = [
        sys.executable,
        SEND_SCRIPT,
        "--to",
        recipient,
        "--from",
        sender,
        "--subject",
        subject,
        "--body",
        body,
    ]
    return subprocess.run(cmd).returncode


def main():
    parser = argparse.ArgumentParser(
        description=(
            "Retired compatibility utility; do not schedule it. "
            "The router-owned kb-maintenance-reminder job is canonical."
        )
    )
    parser.add_argument(
        "--dry-run",
        action="store_true",
        help="Print the message that would be sent without writing to any mailbox.",
    )
    parser.add_argument("--to", default=DEFAULT_RECIPIENT, help="Recipient mailbox window.")
    parser.add_argument("--sender", default=DEFAULT_SENDER, help="Mailbox sender identity.")
    parser.add_argument("--subject", default=DEFAULT_SUBJECT, help="Mailbox subject.")
    args = parser.parse_args()

    if args.dry_run:
        print(f"To: {args.to}")
        print(f"From: {args.sender}")
        print(f"Subject: {args.subject}")
        print()
        print(DEFAULT_BODY)
        return 0

    rc = send_reminder(DEFAULT_BODY, args.to, args.sender, args.subject)
    if rc == 0:
        print(f"Sent KB maintenance reminder to {args.to}.")
    return rc


if __name__ == "__main__":
    sys.exit(main())
