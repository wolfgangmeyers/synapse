#!/usr/bin/env python3
"""Record an asynchronous verification reminder and successor on a task."""

import argparse
from datetime import datetime, timezone
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from _state import find_task, locked_state


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("task")
    parser.add_argument("--reminder-id", required=True)
    parser.add_argument("--successor", required=True)
    parser.add_argument("--remote-verified", choices=("pending", "true", "false"), default="pending")
    args = parser.parse_args(argv)
    try:
        identifier = int(args.task)
    except ValueError:
        identifier = args.task

    verified = {"pending": False, "true": True, "false": False}[args.remote_verified]
    with locked_state() as state:
        task = find_task(state, identifier)
        if task is None:
            print(f"Error: task not found: {args.task}", file=sys.stderr)
            return 1
        task["verification_handoff"] = {
            "reminder_id": args.reminder_id,
            "verification_started_at": datetime.now(timezone.utc).isoformat(timespec="seconds").replace("+00:00", "Z"),
            "remote_verified": verified,
            "successor": args.successor,
        }
    print(f"Updated task-{task.get('id')}: verification reminder {args.reminder_id}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
