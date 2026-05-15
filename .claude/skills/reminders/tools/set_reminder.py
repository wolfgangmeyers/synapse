#!/usr/bin/env python3
"""Set a one-shot reminder that fires via mailbox after a delay.

Usage:
  python3 set_reminder.py --window supervisor --message "Check PR" --in 10m
  python3 set_reminder.py --window worker1 --message "Run tests" --in 1h30m
  python3 set_reminder.py --window supervisor --message "Standup" --at "2026-03-06T15:00:00"
"""

import argparse
import re
import sys
import time
from datetime import datetime, timedelta, timezone

from _reminders import locked_reminders


def parse_duration(s):
    """Parse duration string like 10m, 1h30m, 2h, 45s, 1h30m20s into timedelta."""
    pattern = r'^(?:(\d+)h)?(?:(\d+)m)?(?:(\d+)s)?$'
    m = re.match(pattern, s.strip())
    if not m or not any(m.groups()):
        print(f"Error: invalid duration '{s}'. Use formats like 10m, 1h30m, 2h, 45s", file=sys.stderr)
        sys.exit(1)
    hours = int(m.group(1) or 0)
    minutes = int(m.group(2) or 0)
    seconds = int(m.group(3) or 0)
    return timedelta(hours=hours, minutes=minutes, seconds=seconds)


def main():
    parser = argparse.ArgumentParser(description="Set a reminder")
    parser.add_argument("--window", required=True, help="Target window name")
    parser.add_argument("--message", required=True, help="Reminder message")
    parser.add_argument("--in", dest="in_duration", metavar="DURATION",
                        help="Relative time (e.g. 10m, 1h30m)")
    parser.add_argument("--at", dest="at_time", metavar="ISO",
                        help="Absolute ISO 8601 datetime (e.g. 2026-03-06T15:00:00)")
    args = parser.parse_args()

    if args.in_duration and args.at_time:
        print("Error: specify either --in or --at, not both", file=sys.stderr)
        sys.exit(1)
    if not args.in_duration and not args.at_time:
        print("Error: one of --in or --at is required", file=sys.stderr)
        sys.exit(1)

    now = datetime.now(timezone.utc)

    if args.in_duration:
        delta = parse_duration(args.in_duration)
        fire_at = now + delta
    else:
        try:
            fire_at = datetime.fromisoformat(args.at_time)
        except ValueError:
            print(f"Error: invalid ISO datetime '{args.at_time}'. Use format 2026-03-06T15:00:00", file=sys.stderr)
            sys.exit(1)

    reminder_id = f"r-{time.time_ns()}"
    reminder = {
        "id": reminder_id,
        "window": args.window,
        "message": args.message,
        "fire_at": fire_at.isoformat(),
        "created_at": now.isoformat(),
    }

    with locked_reminders() as reminders:
        reminders.append(reminder)

    print(f"Reminder set: {reminder_id}")
    print(f"Window:       {args.window}")
    print(f"Fires at:     {fire_at.strftime('%Y-%m-%d %H:%M:%S')}")
    print(f"Message:      {args.message}")


if __name__ == "__main__":
    main()
