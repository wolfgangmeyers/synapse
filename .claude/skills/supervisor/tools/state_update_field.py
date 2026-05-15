#!/usr/bin/env python3
"""Update a single field on a task in the supervisor state.

Usage:
  python state_update_field.py <task-id-or-alias> <field> <value>
"""

import sys
import os

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from _state import locked_state, find_task, now_timestamp, REQUIRED_FIELDS, OPTIONAL_FIELDS, ALL_FIELDS

USAGE = f"Usage: {os.path.basename(__file__)} <task-id-or-alias> <field> <value>"


def main():
    if len(sys.argv) != 4:
        print(USAGE, file=sys.stderr)
        sys.exit(1)

    identifier = sys.argv[1]
    field = sys.argv[2]
    value = sys.argv[3]

    # Try to interpret the identifier as an integer task ID.
    try:
        identifier = int(identifier)
    except ValueError:
        pass

    # Disallow updating "id" directly.
    if field == "id":
        print("Error: cannot update 'id'. Task IDs are assigned automatically.", file=sys.stderr)
        sys.exit(1)

    # Disallow updating "state" -- direct user to the dedicated script.
    if field == "state":
        print("Error: cannot update 'state' with this script. Use state_update_state.py instead.", file=sys.stderr)
        sys.exit(1)

    with locked_state() as state:
        task = find_task(state, identifier)

        if task is None:
            print(f"Error: task '{sys.argv[1]}' not found.", file=sys.stderr)
            sys.exit(1)

        task_id = task.get("id", "?")
        alias = task.get("alias", "unknown")

        task[field] = value

    print(f"Updated task-{task_id} ({alias}): {field} = {value}")


if __name__ == "__main__":
    main()
