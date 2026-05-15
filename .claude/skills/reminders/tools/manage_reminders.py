#!/usr/bin/env python3
"""List, delete, or clear all reminders.

Usage:
  python3 manage_reminders.py list
  python3 manage_reminders.py delete r-<id>
  python3 manage_reminders.py delete-all
"""

import sys

from _reminders import locked_reminders


def cmd_list(reminders):
    if not reminders:
        print("No reminders set.")
        return
    sorted_reminders = sorted(reminders, key=lambda r: r["fire_at"])
    id_w, win_w, time_w = 20, 10, 21
    header = f"{'ID':<{id_w}} {'WINDOW':<{win_w}} {'FIRES AT':<{time_w}} MESSAGE"
    print(header)
    for r in sorted_reminders:
        fire_at = r["fire_at"].replace("T", " ")[:19]
        print(f"{r['id']:<{id_w}} {r['window']:<{win_w}} {fire_at:<{time_w}} {r['message']}")


def cmd_delete(reminders, reminder_id):
    new_list = [r for r in reminders if r["id"] != reminder_id]
    if len(new_list) == len(reminders):
        print(f"Not found: {reminder_id}", file=sys.stderr)
        sys.exit(1)
    reminders[:] = new_list
    return f"Deleted: {reminder_id}"


def cmd_delete_all(reminders):
    count = len(reminders)
    reminders.clear()
    return f"Deleted {count} reminder(s)."


def main():
    if len(sys.argv) < 2:
        print("Usage: manage_reminders.py list|delete <id>|delete-all", file=sys.stderr)
        sys.exit(1)

    command = sys.argv[1]
    if command == "list":
        with locked_reminders(save=False) as reminders:
            cmd_list(reminders)
    elif command == "delete":
        if len(sys.argv) < 3:
            print("Usage: manage_reminders.py delete <id>", file=sys.stderr)
            sys.exit(1)
        with locked_reminders() as reminders:
            message = cmd_delete(reminders, sys.argv[2])
        print(message)
    elif command == "delete-all":
        with locked_reminders() as reminders:
            message = cmd_delete_all(reminders)
        print(message)
    else:
        print(f"Unknown command: {command}. Use list, delete, or delete-all", file=sys.stderr)
        sys.exit(1)


if __name__ == "__main__":
    main()
