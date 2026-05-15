#!/usr/bin/env python3
"""Remove (null out) fields on a task in the supervisor state.

Usage:
  python state_remove_field.py <task-id-or-alias> <field> [<field> ...]
"""

import sys
import os

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from _state import locked_state, find_task, now_timestamp, REQUIRED_FIELDS, OPTIONAL_FIELDS, ALL_FIELDS

# Fields that are managed structurally and cannot be removed.
NON_REMOVABLE_FIELDS = {"id", "state"} | set(REQUIRED_FIELDS)

# Fields that can be set to null.
REMOVABLE_FIELDS = set(OPTIONAL_FIELDS) | {
    "last_checked",
    "last_summary",
    "completed",
    "result",
    "abandoned",
    "reason",
}

USAGE = f"Usage: {os.path.basename(__file__)} <task-id-or-alias> <field> [<field> ...]"


def main():
    if len(sys.argv) < 3:
        print(USAGE, file=sys.stderr)
        sys.exit(1)

    identifier = sys.argv[1]
    fields = sys.argv[2:]

    # Try to interpret the identifier as an integer task ID.
    try:
        identifier = int(identifier)
    except ValueError:
        pass

    # Validate fields first before taking the lock.
    for field in fields:
        if field in NON_REMOVABLE_FIELDS:
            removable_sorted = sorted(REMOVABLE_FIELDS)
            print(
                f"Error: cannot remove field '{field}'. "
                f"Removable fields: {', '.join(removable_sorted)}",
                file=sys.stderr,
            )
            sys.exit(1)
        if field not in REMOVABLE_FIELDS:
            removable_sorted = sorted(REMOVABLE_FIELDS)
            print(
                f"Error: unknown or non-removable field '{field}'. "
                f"Removable fields: {', '.join(removable_sorted)}",
                file=sys.stderr,
            )
            sys.exit(1)

    with locked_state() as state:
        task = find_task(state, identifier)

        if task is None:
            print(f"Error: task '{sys.argv[1]}' not found.", file=sys.stderr)
            sys.exit(1)

        task_id = task.get("id", "?")
        alias = task.get("alias", "unknown")

        for field in fields:
            task[field] = None

    for field in fields:
        print(f"Cleared task-{task_id} ({alias}): {field} = null")


if __name__ == "__main__":
    main()
