#!/usr/bin/env python3
"""Mailbox delivery cron job.

Runs every 10 seconds via system cron (achieved with 6 staggered per-minute entries).
Scans all agent mailboxes for unread messages older than MESSAGE_STALE_THRESHOLD
seconds and delivers them directly to the agent's tmux window unconditionally.

For active agents the PreToolUse hook will usually deliver fresh messages before
they go stale. The cron job is the fallback for idle agents and for any messages
the hook missed.

Crontab entry (run every 10 seconds):
  * * * * * /usr/bin/python3 ~/.claude/skills/supervisor/tools/mailbox_cron.py >> ~/.mecha-developer/mailbox-cron.log 2>&1
  * * * * * sleep 10; /usr/bin/python3 ~/.claude/skills/supervisor/tools/mailbox_cron.py >> ~/.mecha-developer/mailbox-cron.log 2>&1
  * * * * * sleep 20; /usr/bin/python3 ~/.claude/skills/supervisor/tools/mailbox_cron.py >> ~/.mecha-developer/mailbox-cron.log 2>&1
  * * * * * sleep 30; /usr/bin/python3 ~/.claude/skills/supervisor/tools/mailbox_cron.py >> ~/.mecha-developer/mailbox-cron.log 2>&1
  * * * * * sleep 40; /usr/bin/python3 ~/.claude/skills/supervisor/tools/mailbox_cron.py >> ~/.mecha-developer/mailbox-cron.log 2>&1
  * * * * * sleep 50; /usr/bin/python3 ~/.claude/skills/supervisor/tools/mailbox_cron.py >> ~/.mecha-developer/mailbox-cron.log 2>&1

The mailbox base directory defaults to ~/.mecha-developer/mailboxes/ and can
be overridden with MECHA_MAILBOX_DIR_OVERRIDE (used by tests).
"""

import json
import logging
import os
import sys
import fcntl
from contextlib import contextmanager
from datetime import datetime, timezone

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from _tmux import send_text_and_enter, window_exists


MAILBOX_DIR = os.environ.get(
    "MECHA_MAILBOX_DIR_OVERRIDE",
    os.path.expanduser("~/.mecha-developer/mailboxes"),
)
SYSTEM_SENDERS_CONFIG = os.environ.get(
    "MECHA_MAILBOX_SYSTEM_SENDERS_CONFIG",
    os.path.expanduser("~/.mecha-developer/mailbox-system-senders.json"),
)
LOG_FILE = os.path.expanduser("~/.mecha-developer/mailbox-cron.log")
LOCK_FILE = os.path.expanduser("~/.mecha-developer/.mailbox-cron-delivery.lock")
DEFAULT_SYSTEM_SENDERS = {"reminders", "watchdog", "memory-monitor", "system", "user"}

# A message must be unread for at least this many seconds before the cron delivers it.
# Below this age, the message is "fresh" and the PreToolUse hook will deliver it soon.
MESSAGE_STALE_THRESHOLD = 30  # seconds


def setup_logging():
    os.makedirs(os.path.dirname(LOG_FILE), exist_ok=True)
    logging.basicConfig(
        filename=LOG_FILE,
        level=logging.INFO,
        format="%(asctime)s %(levelname)s %(message)s",
        datefmt="%Y-%m-%d %H:%M:%S",
    )


class CronLockUnavailable(Exception):
    """Raised when another mailbox_cron process already holds the delivery lock."""


@contextmanager
def cron_delivery_lock():
    """Hold the global cron delivery lock for the context body."""
    os.makedirs(os.path.dirname(LOCK_FILE), exist_ok=True)
    lock = open(LOCK_FILE, "a", encoding="utf-8")
    try:
        try:
            fcntl.flock(lock.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            lock.close()
            raise CronLockUnavailable
        yield
    finally:
        if not lock.closed:
            lock.close()  # closing the fd releases the flock lock automatically


def read_unread_messages(mailbox_path):
    """Return list of (filepath, message_dict) for unread messages, sorted by timestamp."""
    if not os.path.isdir(mailbox_path):
        return []

    messages = []
    for fname in os.listdir(mailbox_path):
        if not fname.endswith(".json"):
            continue
        fpath = os.path.join(mailbox_path, fname)
        try:
            with open(fpath, "r", encoding="utf-8") as f:
                msg = json.load(f)
            if not msg.get("read", False):
                messages.append((fpath, msg))
        except json.JSONDecodeError:
            logging.warning("Corrupt message file (skipping): %s", fpath)
        except OSError as e:
            logging.warning("Could not read message file %s: %s", fpath, e)

    messages.sort(key=lambda x: x[1].get("timestamp", ""))
    return messages


def message_age_seconds(msg):
    """Return the age of a message in seconds.

    Returns float('inf') on any parse error so that malformed-timestamp messages
    are treated as maximally stale and still delivered, rather than silently stuck.
    """
    ts = msg.get("timestamp", "")
    try:
        dt = datetime.fromisoformat(ts.replace("Z", "+00:00"))
        now = datetime.now(timezone.utc)
        return (now - dt).total_seconds()
    except (ValueError, AttributeError, TypeError):
        logging.warning("Could not parse message timestamp %r — treating as stale", ts)
        return float("inf")


def mark_read(fpath, msg):
    """Mark a message as read by updating its file in place."""
    msg["read"] = True
    try:
        with open(fpath, "w", encoding="utf-8") as f:
            json.dump(msg, f, indent=2)
            f.write("\n")
    except OSError as e:
        logging.warning("Could not mark message read %s: %s", fpath, e)


def format_timestamp(ts):
    """Format an ISO timestamp as YYYY-MM-DD HH:MM:SS. Returns ts unchanged on error."""
    try:
        dt = datetime.fromisoformat(ts.replace("Z", "+00:00"))
        return dt.strftime("%Y-%m-%d %H:%M:%S")
    except (ValueError, AttributeError):
        return ts


def message_sender_hint(msg):
    return msg.get("from_alias") or msg.get("from", "")


def is_email_message(msg):
    return "@" in msg.get("from", "")


def has_email_sender(messages):
    return any(is_email_message(msg) for _, msg in messages)


def system_senders():
    """Return mailbox senders that should not require supervisor task updates."""
    senders = set(DEFAULT_SYSTEM_SENDERS)
    try:
        with open(SYSTEM_SENDERS_CONFIG, "r", encoding="utf-8") as f:
            raw = json.load(f)
    except FileNotFoundError:
        return senders
    except (OSError, json.JSONDecodeError, TypeError) as e:
        logging.warning("Could not load system senders config %r: %s", SYSTEM_SENDERS_CONFIG, e)
        return senders

    configured = raw.get("senders", []) if isinstance(raw, dict) else raw
    if not isinstance(configured, list):
        logging.warning(
            "Invalid system senders config %r: expected list or object with senders list",
            SYSTEM_SENDERS_CONFIG,
        )
        return senders
    for sender in configured:
        if isinstance(sender, str) and sender.strip():
            senders.add(sender.strip())
    return senders


def smtp_reply_block():
    return [
        "*** MANDATORY — IF YOU REPLY, USE SMTP ***",
        "The sender is reachable only via the mail system. To respond, run:",
        '   python3 .claude/skills/supervisor/tools/mailbox_send.py --to <sender-email-above> --from <self> --subject "..." --body "..."',
        "Replies typed at the terminal DO NOT reach the sender. Decide if a reply is warranted; if yes, this is the only valid path.",
        "*** END MANDATORY BLOCK ***",
    ]


def format_delivery_text(messages, agent_name=""):
    """Format messages for direct tmux delivery."""
    count = len(messages)
    noun = "message" if count == 1 else "messages"

    # Collect unique non-email senders to pre-fill the state update command.
    # Email-originated messages use from_alias for display/reply routing, but that
    # alias is not a supervisor task alias and must not drive state updates.
    senders = []
    seen = set()
    for _, msg in messages:
        if is_email_message(msg):
            continue
        s = message_sender_hint(msg)
        if s and s not in seen:
            senders.append(s)
            seen.add(s)
    sender_hint = senders[0] if senders else None

    lines = []

    # System/non-task senders do not need state_update_status.
    if agent_name == "supervisor" and sender_hint and sender_hint not in system_senders():
        lines += [
            "*** MANDATORY — DO THIS BEFORE READING THE MESSAGE BELOW ***",
            "Step 1: Run state update now:",
            f'   python3 state_update_status.py {sender_hint} "<one-line summary>"',
            "Step 2: Read the message.",
            "Step 3: Act on it.",
            "*** END MANDATORY BLOCK ***",
            "",
        ]

    lines += [
        f"[MAILBOX] {count} unread {noun} while you were idle:",
        "",
    ]

    for i, (_, msg) in enumerate(messages, 1):
        lines.append(f"--- Message {i} of {count} ---")
        sender = msg.get("from", "unknown")
        ts_display = format_timestamp(msg.get("timestamp", "unknown"))
        lines.append(f"From: {sender} | Sent: {ts_display}")
        if msg.get("subject"):
            lines.append(f"Subject: {msg['subject']}")
        lines.append(msg.get("body", ""))
        lines.append("")

    if has_email_sender(messages):
        lines += smtp_reply_block()

    return "\n".join(lines).rstrip()


def deliver_to_agent(agent_name, messages):
    """Send messages directly to the agent's tmux window. Returns True if delivered."""
    if not window_exists(agent_name):
        logging.warning(
            "Window '%s' not found — cannot deliver %d message(s)",
            agent_name,
            len(messages),
        )
        return False

    text = format_delivery_text(messages, agent_name=agent_name)
    try:
        send_text_and_enter(agent_name, text)
    except Exception as e:
        logging.error(
            "Failed to send messages to window '%s': %s — messages remain unread",
            agent_name,
            e,
        )
        return False

    for fpath, msg in messages:
        mark_read(fpath, msg)

    logging.info(
        "Delivered %d message(s) to '%s' via tmux",
        len(messages),
        agent_name,
    )
    return True


def check_agent(agent_name, mailbox_path):
    """Check one agent's mailbox and deliver stale unread messages unconditionally."""
    messages = read_unread_messages(mailbox_path)
    if not messages:
        return

    # Only deliver messages that have been sitting unread past the idle threshold.
    stale = [
        (fpath, msg)
        for fpath, msg in messages
        if message_age_seconds(msg) >= MESSAGE_STALE_THRESHOLD
    ]

    if not stale:
        logging.debug(
            "Agent '%s': %d unread message(s), none older than %ds yet",
            agent_name,
            len(messages),
            MESSAGE_STALE_THRESHOLD,
        )
        return

    logging.info(
        "Agent '%s': %d unread message(s), %d stale (>%ds) — checking window",
        agent_name,
        len(messages),
        len(stale),
        MESSAGE_STALE_THRESHOLD,
    )
    deliver_to_agent(agent_name, stale)


def run():
    setup_logging()

    try:
        with cron_delivery_lock():
            run_locked()
    except CronLockUnavailable:
        logging.info("Another mailbox_cron run is active — skipping")


def run_locked():
    """Run one mailbox scan while the global cron delivery lock is held."""
    if not os.path.isdir(MAILBOX_DIR):
        logging.info("No mailbox directory — nothing to do")
        return

    agents = [
        name
        for name in os.listdir(MAILBOX_DIR)
        if os.path.isdir(os.path.join(MAILBOX_DIR, name))
    ]

    if not agents:
        logging.info("No agent mailboxes found — nothing to do")
        return

    logging.info("Checking %d agent mailbox(es): %s", len(agents), ", ".join(sorted(agents)))

    for agent_name in agents:
        mailbox_path = os.path.join(MAILBOX_DIR, agent_name)
        try:
            check_agent(agent_name, mailbox_path)
        except Exception as e:
            logging.error("Error checking mailbox for '%s': %s", agent_name, e)


if __name__ == "__main__":
    run()
