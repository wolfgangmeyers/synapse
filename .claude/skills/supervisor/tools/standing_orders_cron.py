#!/usr/bin/env python3
"""Send the supervisor's standing orders to the supervisor mailbox.

Intended cron entry:
  0 */2 * * * /usr/bin/python3 ~/.claude/skills/supervisor/tools/standing_orders_cron.py >> ~/.mecha-developer/standing-orders-cron.log 2>&1
"""

import argparse
import os
import subprocess
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from _state import load_state


DEFAULT_RECIPIENT = "supervisor"
DEFAULT_SENDER = "standing-orders-cron"
DEFAULT_SUBJECT = "Standing orders refresh"
SEND_SCRIPT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "mailbox_send.py")


def format_standing_orders(orders):
    """Return a mailbox body containing every standing order."""
    lines = ["Standing orders refresh:", ""]
    if not orders:
        lines.append("No standing orders currently configured.")
        return "\n".join(lines)

    for order in orders:
        order_id = order.get("id", "?")
        title = str(order.get("title", "untitled")).strip() or "untitled"
        priority = str(order.get("priority", "normal")).strip().lower()
        priority_suffix = f" [{priority.upper()}]" if priority and priority != "normal" else ""
        lines.append(f"- {order_id}: {title}{priority_suffix}")

        description = str(order.get("description", "")).strip()
        if description:
            lines.append(f"  {description}")

        source = str(order.get("source", "")).strip()
        if source:
            lines.append(f"  Source: {source}")

    return "\n".join(lines)


def send_standing_orders(body, recipient, sender, subject):
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
        "--allow-large-body",
    ]
    return subprocess.run(cmd).returncode


def main():
    parser = argparse.ArgumentParser(
        description="Send supervisor standing orders to the supervisor mailbox."
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

    orders = load_state().get("standing_orders", [])
    body = format_standing_orders(orders)

    if args.dry_run:
        print(f"To: {args.to}")
        print(f"From: {args.sender}")
        print(f"Subject: {args.subject}")
        print()
        print(body)
        return 0

    rc = send_standing_orders(body, args.to, args.sender, args.subject)
    if rc == 0:
        print(f"Sent standing orders refresh to {args.to} ({len(orders)} order(s)).")
    return rc


if __name__ == "__main__":
    sys.exit(main())
