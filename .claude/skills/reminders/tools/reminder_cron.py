#!/usr/bin/env python3
"""Cron job: fire due reminders via mailbox and remove them.

Runs every minute via cron. Loops internally every 5 seconds for ~55
seconds so that short-delay reminders (e.g. handoff safety-net at 5s)
fire promptly without needing sub-minute crontab entries.
"""

import fcntl
import pathlib
import subprocess
import sys
import time
from datetime import datetime, timezone

LOG_FILE = pathlib.Path.home() / ".mecha-wolfgang" / "reminder-cron.log"
LOCK_FILE = pathlib.Path.home() / ".mecha-wolfgang" / ".reminder-cron.lock"

# supervisor/tools is sibling of reminders/tools: go up 2 dirs then into supervisor/tools
SUPERVISOR_TOOLS = pathlib.Path(__file__).parent.parent.parent / "supervisor" / "tools"
MAILBOX_SEND = SUPERVISOR_TOOLS / "mailbox_send.py"

# For direct-to-tmux delivery, import send_text_and_enter from supervisor tools
sys.path.insert(0, str(SUPERVISOR_TOOLS))
from _tmux import send_text_and_enter
from _reminders import locked_reminders, save_reminders


def _parse_fire_at(s):
    """Parse fire_at, handling both naive (legacy local) and aware (UTC) datetimes."""
    dt = datetime.fromisoformat(s)
    if dt.tzinfo is None:
        # Legacy reminder stored in local time — assume local and convert to UTC
        dt = dt.astimezone(timezone.utc)
    return dt


def log(message):
    LOG_FILE.parent.mkdir(parents=True, exist_ok=True)
    ts = datetime.now(timezone.utc).isoformat(timespec="seconds")
    with open(LOG_FILE, "a") as f:
        f.write(f"{ts} {message}\n")


def fire_due():
    """Check for due reminders and fire them. Returns True if any were fired."""
    with locked_reminders(save=False) as reminders:
        if not reminders:
            return False

        now = datetime.now(timezone.utc)
        due = [r for r in reminders if _parse_fire_at(r["fire_at"]) <= now]
        if not due:
            return False

    fired_ids = set()

    for reminder in due:
        delivery = reminder.get("delivery", "mailbox")
        try:
            if delivery == "tmux":
                send_text_and_enter(reminder["window"], reminder["message"])
            else:
                subprocess.run([
                    sys.executable,
                    str(MAILBOX_SEND),
                    "--to", reminder["window"],
                    "--from", "reminders",
                    "--subject", f"REMINDER: {reminder['message'][:60]}",
                    "--body", reminder["message"],
                ], check=True, capture_output=True)
            log(f"fired[{delivery}] {reminder['id']} -> {reminder['window']}: {reminder['message']}")
            fired_ids.add(reminder["id"])
        except Exception as e:
            log(f"error firing {reminder['id']} -> {reminder['window']}: {e}")

    if fired_ids:
        with locked_reminders(save=False) as reminders:
            reminders[:] = [r for r in reminders if r["id"] not in fired_ids]
            save_reminders(reminders)
        return True
    return False


def main():
    # Acquire an exclusive lock so overlapping cron invocations exit immediately.
    LOCK_FILE.parent.mkdir(parents=True, exist_ok=True)
    lock_fd = open(LOCK_FILE, "w")
    try:
        fcntl.flock(lock_fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except OSError:
        # Another instance is already running — exit silently.
        return

    try:
        # Loop every 5 seconds for ~55 seconds so short-delay reminders
        # (e.g. handoff safety-net) fire within seconds, not minutes.
        for i in range(11):  # 11 × 5s = 55s (leaves 5s buffer before next cron)
            fire_due()
            time.sleep(5)
        fire_due()  # Final check at ~55s
    finally:
        fcntl.flock(lock_fd, fcntl.LOCK_UN)
        lock_fd.close()


if __name__ == "__main__":
    main()
