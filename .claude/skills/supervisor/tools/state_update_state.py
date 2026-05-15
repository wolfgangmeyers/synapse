#!/usr/bin/env python3
"""Move a task to a new state (active/completed/abandoned)."""

import argparse
import sys
import os

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from _state import locked_state, find_task, now_timestamp


def parse_identifier(raw):
    """Return an int if *raw* looks like an integer, otherwise a string."""
    try:
        return int(raw)
    except ValueError:
        return raw


def main():
    parser = argparse.ArgumentParser(
        description="Move a task to a new state (active/completed/abandoned)."
    )
    parser.add_argument(
        "identifier",
        help="Task ID (integer) or alias (string)",
    )
    parser.add_argument(
        "state",
        choices=["active", "completed", "abandoned"],
        help="New state for the task",
    )
    parser.add_argument(
        "--result",
        default=None,
        help="Result description (required when moving to completed)",
    )
    parser.add_argument(
        "--reason",
        default=None,
        help="Reason for abandonment (required when moving to abandoned)",
    )

    args = parser.parse_args()

    # Validate required flags for specific states
    if args.state == "completed" and args.result is None:
        parser.error("--result is required when moving to completed")
    if args.state == "abandoned" and args.reason is None:
        parser.error("--reason is required when moving to abandoned")

    identifier = parse_identifier(args.identifier)
    with locked_state() as state:
        task = find_task(state, identifier)

        if task is None:
            print(f"Error: task not found: {args.identifier}", file=sys.stderr)
            sys.exit(1)

        ts = now_timestamp()

        if args.state == "completed":
            task["completed"] = ts
            task["result"] = args.result
            task["abandoned"] = None
            task["reason"] = None
        elif args.state == "abandoned":
            task["abandoned"] = ts
            task["reason"] = args.reason
            task["completed"] = None
            task["result"] = None
        elif args.state == "active":
            task["completed"] = None
            task["result"] = None
            task["abandoned"] = None
            task["reason"] = None

        task["state"] = args.state

        task_id = task.get("id", "?")
        alias = task.get("alias", "unknown")

    print(f"Moved task-{task_id} ({alias}) to {args.state}")


if __name__ == "__main__":
    main()
