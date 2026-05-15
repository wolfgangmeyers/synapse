#!/usr/bin/env python3
"""Block or unblock a task in the supervisor state.

Usage:
  python state_block.py ALIAS --block "reason for being blocked"
  python state_block.py ALIAS --unblock
"""

import argparse
import sys
import os

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from _state import locked_state, find_task, now_timestamp


def main():
    parser = argparse.ArgumentParser(
        description="Block or unblock a task in the supervisor state."
    )
    parser.add_argument("alias", help="Task alias or ID")

    group = parser.add_mutually_exclusive_group(required=True)
    group.add_argument("--block", metavar="REASON",
                       help="Block the task with the given reason")
    group.add_argument("--unblock", action="store_true",
                       help="Unblock the task")

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

        if args.block:
            task["blocked"] = now_timestamp()
            task["blocked_reason"] = args.block
            task["last_checked"] = now_timestamp()
            msg = f"Blocked task-{task['id']} ({task['alias']}): {args.block}"
        else:
            task["blocked"] = None
            task["blocked_reason"] = None
            task["last_checked"] = now_timestamp()
            msg = f"Unblocked task-{task['id']} ({task['alias']})"

    print(msg)


if __name__ == "__main__":
    main()
