#!/usr/bin/env python3
"""Enable or disable idle notifications for a task.

Usage:
  python state_idle_notify.py ALIAS --enable
  python state_idle_notify.py ALIAS --disable
"""

import argparse
import sys
import os

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from _state import locked_state, find_task, now_timestamp


def main():
    parser = argparse.ArgumentParser(
        description="Enable or disable idle notifications for a task."
    )
    parser.add_argument("alias", help="Task alias or ID")

    group = parser.add_mutually_exclusive_group(required=True)
    group.add_argument("--enable", action="store_true",
                       help="Enable idle notifications for the task")
    group.add_argument("--disable", action="store_true",
                       help="Disable idle notifications for the task")

    args = parser.parse_args()

    try:
        identifier = int(args.alias)
    except ValueError:
        identifier = args.alias

    with locked_state() as state:
        task = find_task(state, identifier)
        if not task:
            print(f"Error: Task '{args.alias}' not found", file=sys.stderr)
            sys.exit(1)

        if args.enable:
            task["idle_notify"] = True
            task["last_checked"] = now_timestamp()
            msg = f"Enabled idle notifications for task-{task['id']} ({task['alias']})"
        else:
            task.pop("idle_notify", None)
            task["last_checked"] = now_timestamp()
            msg = f"Disabled idle notifications for task-{task['id']} ({task['alias']})"

    print(msg)


if __name__ == "__main__":
    main()
