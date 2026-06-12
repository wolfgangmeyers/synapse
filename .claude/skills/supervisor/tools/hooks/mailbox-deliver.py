#!/usr/bin/env python3
"""Mailbox delivery hook for Claude Code.

Fires as a PostToolUse hook — invoked after each tool call. Checks the agent's
mailbox for unread messages. If any exist, injects them into the agent's context
via stdout (Claude Code shows PostToolUse hook stdout to the model on exit 0).
Marks all delivered messages as read.

Agent identity is resolved in priority order:
  MECHA_WORKER_WINDOW — set by worker_launch.py for managed workers
  MECHA_AGENT_WINDOW  — set manually for the supervisor session

Sessions with neither env var are silently ignored (non-agent context).

The mailbox base directory defaults to ~/.mecha-developer/mailboxes/ and can
be overridden with MECHA_MAILBOX_DIR_OVERRIDE (used by tests).

PostToolUse hook stdin fields: tool_name, tool_input, tool_response, session_id

Output format: JSON with hookSpecificOutput.additionalContext — plain stdout
goes to verbose mode only and is NOT shown to the model.

Exit codes:
  0 - always (hook never blocks)
"""

import json
import os
import sys
from datetime import datetime, timezone


MAILBOX_DIR = os.environ.get(
    "MECHA_MAILBOX_DIR_OVERRIDE",
    os.path.expanduser("~/.mecha-developer/mailboxes"),
)
SYSTEM_SENDERS_CONFIG = os.environ.get(
    "MECHA_MAILBOX_SYSTEM_SENDERS_CONFIG",
    os.path.expanduser("~/.mecha-developer/mailbox-system-senders.json"),
)
LOG_FILE = os.path.expanduser("~/.mecha-developer/mailbox-hook.log")
DEFAULT_SYSTEM_SENDERS = {
    "reminders",
    "standing-orders-cron",
    "watchdog",
    "memory-monitor",
    "system",
    "user",
}


def log(msg):
    """Append a timestamped line to the hook log."""
    try:
        os.makedirs(os.path.dirname(LOG_FILE), exist_ok=True)
        ts = datetime.now(timezone.utc).strftime("%Y-%m-%d %H:%M:%S")
        with open(LOG_FILE, "a") as f:
            f.write(f"{ts} {msg}\n")
    except OSError:
        pass


def get_agent_window():
    """Return the agent's window name, or None if not a managed agent session."""
    return os.environ.get("MECHA_WORKER_WINDOW") or os.environ.get("MECHA_AGENT_WINDOW")


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
            print(f"[mailbox] Warning: corrupt message file (skipping): {fpath}", file=sys.stderr)
        except OSError as e:
            print(f"[mailbox] Warning: could not read {fpath}: {e}", file=sys.stderr)

    messages.sort(key=lambda x: x[1].get("timestamp", ""))
    return messages


def mark_read(fpath, msg):
    """Mark a message as read by updating its file in place."""
    msg["read"] = True
    try:
        with open(fpath, "w", encoding="utf-8") as f:
            json.dump(msg, f, indent=2)
            f.write("\n")
    except OSError as e:
        # Log but don't block — hook always exits 0. If mark-read fails, the cron
        # will attempt delivery again (duplicate) rather than silently losing the message.
        print(f"[mailbox] Warning: could not mark {fpath!r} as read: {e}", file=sys.stderr)


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
        log(f"could not load system senders config {SYSTEM_SENDERS_CONFIG!r}: {e}")
        return senders

    configured = raw.get("senders", []) if isinstance(raw, dict) else raw
    if not isinstance(configured, list):
        log(f"invalid system senders config {SYSTEM_SENDERS_CONFIG!r}: expected list or object with senders list")
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


def format_messages(messages, is_supervisor=False):
    """Format messages for injection into agent context."""
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
    system_sender_names = system_senders()
    sender_hint = next((s for s in senders if s not in system_sender_names), None)

    lines = []

    # System/non-task senders do not need state_update_status.
    if is_supervisor and sender_hint:
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
        f"[MAILBOX] {count} new {noun}:",
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
        lines.append("")

    if not is_supervisor:
        lines.append("Tip: If you're mid-task, spawn a background subagent to handle these messages so you don't lose your current work.")

    return "\n".join(lines).rstrip()


def main():
    # Read (and discard) hook input from stdin — required so stdin isn't left open.
    try:
        json.load(sys.stdin)
    except (json.JSONDecodeError, ValueError):
        pass  # Malformed input is silently ignored

    agent_window = get_agent_window()
    if not agent_window:
        log("skip: no agent window (MECHA_WORKER_WINDOW/MECHA_AGENT_WINDOW not set)")
        sys.exit(0)

    mailbox_path = os.path.join(MAILBOX_DIR, agent_window)
    messages = read_unread_messages(mailbox_path)

    if not messages:
        log(f"checked: {agent_window} — no unread messages")
        sys.exit(0)

    # Mark all as read before printing — if we crash mid-output, messages stay read.
    for fpath, msg in messages:
        mark_read(fpath, msg)

    log(f"injecting: {len(messages)} message(s) for {agent_window} via additionalContext")
    output = {
        "hookSpecificOutput": {
            "hookEventName": "PostToolUse",
            "additionalContext": format_messages(messages, is_supervisor=(agent_window == "supervisor")),
        }
    }
    print(json.dumps(output))
    sys.exit(0)


if __name__ == "__main__":
    main()
