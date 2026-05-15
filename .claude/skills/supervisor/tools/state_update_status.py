#!/usr/bin/env python3
"""Update a task's last_summary and last_checked fields."""

import sys
import os

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from _state import locked_state, find_task, now_timestamp

USAGE = f"Usage: {os.path.basename(__file__)} <task-id-or-alias> <status-message>"


def parse_identifier(raw):
    """Return an int if *raw* looks like an integer, otherwise a string."""
    try:
        return int(raw)
    except ValueError:
        return raw


def main():
    if len(sys.argv) != 3:
        print(USAGE, file=sys.stderr)
        sys.exit(1)

    identifier = parse_identifier(sys.argv[1])
    message = sys.argv[2]

    with locked_state() as state:
        task = find_task(state, identifier)

        if task is None:
            print(f"Error: task not found: {sys.argv[1]}", file=sys.stderr)
            sys.exit(1)

        ts = now_timestamp()

        old_summary = task.get("last_summary")
        old_checked = task.get("last_checked", ts)
        if "journal" not in task or not isinstance(task.get("journal"), list):
            task["journal"] = []
        if old_summary:
            task["journal"].append({"timestamp": old_checked, "entry": old_summary})

        task["last_summary"] = message
        task["last_checked"] = ts

        task_id = task.get("id", "?")
        alias = task.get("alias", "unknown")

    print(f"Updated task-{task_id} ({alias}): last_summary set, last_checked = {ts}")


if __name__ == "__main__":
    main()
