#!/usr/bin/env python3
"""Send a message to an agent's mailbox.

Usage:
  python mailbox_send.py --to WINDOW --body TEXT [--subject SUBJECT] [--from WINDOW]

--from defaults to MECHA_WORKER_WINDOW (workers) or MECHA_AGENT_WINDOW (supervisor).
Workers and the supervisor should never pass --from explicitly — let it default to
the real window identity set by worker_launch.py. If neither env var is set, this
script will exit with an error — do not pass --from as a workaround. Instead,
investigate why the env var is missing: check that the session was launched via
worker_launch.py and that the tmux window name matches the expected value.

Creates a message file in <MAILBOX_DIR>/<to>/<uuid>.json. The recipient
will see the message the next time they make a tool call (via the PreToolUse
hook) or when the idle cron job wakes them up.

The mailbox base directory defaults to ~/.mecha-developer/mailboxes/ and can
be overridden with MECHA_MAILBOX_DIR_OVERRIDE (used by tests).

Examples:
  # Worker sending to supervisor (--from inferred from MECHA_WORKER_WINDOW)
  python mailbox_send.py --to supervisor --body "Phase 1 complete, ready for review"

  # Supervisor sending to a worker (--from inferred from MECHA_AGENT_WINDOW)
  python mailbox_send.py --to worker-1 --body "Please check the lint errors before pushing"

  # With optional subject
  python mailbox_send.py --to worker-2 --subject "Urgent" --body "Stop — the approach needs to change"

  # --from is only required if both env vars are absent (e.g., in tests)
  python mailbox_send.py --to supervisor --from test-harness --body "Test message"
"""

import argparse
from email.message import EmailMessage
import smtplib
import ssl
import json
import os
import re
import sys
import uuid
from datetime import datetime, timezone


MAILBOX_DIR = os.environ.get(
    "MECHA_MAILBOX_DIR_OVERRIDE",
    os.path.expanduser("~/.mecha-developer/mailboxes"),
)

# SMTP config path — overridable for tests.
MAIL_CONFIG = os.environ.get(
    "MECHA_MAIL_CONFIG",
    os.path.expanduser("~/.mecha-developer/mail.json"),
)

MAX_MAILBOX_BODY_CHARS = 1000

# Valid agent names: alphanumeric, hyphens, underscores. No slashes, no dots.
_AGENT_NAME_RE = re.compile(r"^[a-zA-Z0-9][a-zA-Z0-9_-]*$")


def _validate_agent_name(name, field):
    if not _AGENT_NAME_RE.match(name):
        print(
            f"Error: invalid {field} agent name '{name}'. "
            "Must be alphanumeric with hyphens or underscores.",
            file=sys.stderr,
        )
        sys.exit(1)


def _is_email(recipient):
    """True if recipient looks like an email address."""
    return "@" in recipient


def _validate_mailbox_body_size(body):
    if len(body) <= MAX_MAILBOX_BODY_CHARS:
        return

    print(
        f"Error: mailbox body is {len(body)} characters "
        f"(limit: {MAX_MAILBOX_BODY_CHARS}).\n"
        "\n"
        "Large reports must be written to a file, then sent as a short pointer "
        "with the file path.\n"
        "Example:\n"
        "  python3 mailbox_send.py --to supervisor "
        '--body "Report written to /path/to/report.md"',
        file=sys.stderr,
    )
    sys.exit(1)


def _send_via_email(args):
    """Send message body via SMTP and log an audit copy."""
    subject = args.subject or args.body.split("\n", 1)[0]
    try:
        with open(MAIL_CONFIG, "r", encoding="utf-8") as f:
            config = json.load(f)
        message = EmailMessage()
        message["From"] = config["address"]
        message["To"] = args.to
        message["Subject"] = subject
        message.set_content(args.body)

        context = ssl.create_default_context()
        with smtplib.SMTP_SSL(
            config["smtp_host"],
            int(config.get("smtp_port", 465)),
            timeout=30,
            context=context,
        ) as smtp:
            smtp.login(config["address"], config["password"])
            smtp.send_message(message)
    except (KeyError, OSError, json.JSONDecodeError, smtplib.SMTPException) as e:
        print(f"Error: SMTP email send failed: {e}", file=sys.stderr)
        sys.exit(1)

    # Log audit copy in sender's mailbox (marked read — not for delivery).
    msg_id = str(uuid.uuid4())
    timestamp = datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
    audit_msg = {
        "id": msg_id,
        "from": args.from_agent,
        "to": args.to,
        "body": args.body,
        "timestamp": timestamp,
        "read": True,
    }
    if args.subject is not None:
        audit_msg["subject"] = args.subject

    sender_dir = os.path.join(MAILBOX_DIR, args.from_agent)
    os.makedirs(sender_dir, exist_ok=True)
    audit_file = os.path.join(sender_dir, f"{msg_id}.json")
    with open(audit_file, "w", encoding="utf-8") as f:
        json.dump(audit_msg, f, indent=2)
        f.write("\n")

    print(f"Email sent to {args.to}. Audit logged in {args.from_agent}'s mailbox.")


def main():
    parser = argparse.ArgumentParser(
        description="Send a message to an agent's mailbox."
    )
    default_from = os.environ.get("MECHA_WORKER_WINDOW") or os.environ.get("MECHA_AGENT_WINDOW")
    parser.add_argument("--to", required=True, help="Recipient agent window name or email address")
    parser.add_argument(
        "--from",
        required=default_from is None,
        default=default_from,
        dest="from_agent",
        help="Sender agent window name (defaults to MECHA_WORKER_WINDOW / MECHA_AGENT_WINDOW)",
    )
    parser.add_argument("--body", required=True, help="Message body text")
    parser.add_argument("--subject", default=None, help="Optional subject line")

    args = parser.parse_args()

    # --from is always an agent name
    _validate_agent_name(args.from_agent, "--from")

    if _is_email(args.to):
        _send_via_email(args)
        return

    _validate_agent_name(args.to, "--to")
    _validate_mailbox_body_size(args.body)

    msg_id = str(uuid.uuid4())
    # Include Z suffix so downstream parsers know this is UTC (not local/naive time).
    timestamp = datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")

    msg = {
        "id": msg_id,
        "from": args.from_agent,
        "to": args.to,
        "body": args.body,
        "timestamp": timestamp,
        "read": False,
    }
    if args.subject is not None:
        msg["subject"] = args.subject

    mailbox_path = os.path.join(MAILBOX_DIR, args.to)
    os.makedirs(mailbox_path, exist_ok=True)

    msg_file = os.path.join(mailbox_path, f"{msg_id}.json")
    with open(msg_file, "w", encoding="utf-8") as f:
        json.dump(msg, f, indent=2)
        f.write("\n")

    print(f"Message posted to {args.to}'s mailbox.")
    print(f"  ID:   {msg_id}")
    print(f"  From: {args.from_agent}")
    if args.subject:
        print(f"  Subject: {args.subject}")
    preview = args.body if len(args.body) <= 80 else args.body[:77] + "..."
    print(f"  Body: {preview}")
    print()
    print("The recipient will see this message on their next tool call (hook delivery)")
    print("or within 3 minutes if idle (cron delivery).")


if __name__ == "__main__":
    main()
