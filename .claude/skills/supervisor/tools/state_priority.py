#!/usr/bin/env python3
"""Create/update and disposition ordered supervisor priorities safely."""

import argparse
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from _state import locked_state

NONTERMINAL = {"active", "blocked"}
TERMINAL = {"completed", "deferred", "cancelled"}
STATUSES = NONTERMINAL | TERMINAL


def find_priority(state, priority_id):
    return next((item for item in state.setdefault("priorities", []) if item.get("id") == priority_id), None)


def validate_tasks(state, task_ids):
    known = {task.get("id") for task in state.get("tasks", [])}
    missing = [task_id for task_id in task_ids if task_id not in known]
    if missing:
        raise ValueError(f"unknown task ID(s): {', '.join(map(str, missing))}")


def build_parser():
    parser = argparse.ArgumentParser(description=__doc__)
    subparsers = parser.add_subparsers(dest="command", required=True)

    upsert = subparsers.add_parser("upsert", help="Create or replace a priority's name, status, and task links")
    upsert.add_argument("--id", required=True)
    upsert.add_argument("--name", required=True)
    upsert.add_argument("--status", choices=sorted(STATUSES), default="active")
    upsert.add_argument("--task-id", action="append", type=int, required=True)

    status = subparsers.add_parser("status", help="Change a priority disposition")
    status.add_argument("id")
    status.add_argument("status", choices=sorted(STATUSES))
    status.add_argument("--evidence")

    subparsers.add_parser("list", help="List priorities")
    return parser


def main(argv=None):
    args = build_parser().parse_args(argv)
    if args.command == "list":
        with locked_state() as state:
            priorities = list(state.setdefault("priorities", []))
        for item in priorities:
            print(f"{item.get('id')}\t{item.get('status')}\t{item.get('name')}\ttasks={item.get('task_ids', [])}")
        return 0

    try:
        with locked_state() as state:
            priority = find_priority(state, args.id)
            if args.command == "upsert":
                task_ids = list(dict.fromkeys(args.task_id))
                validate_tasks(state, task_ids)
                if priority is None:
                    priority = {"id": args.id}
                    state["priorities"].append(priority)
                priority.update({"name": args.name, "status": args.status, "task_ids": task_ids})
                priority.pop("completion", None)
            else:
                if priority is None:
                    raise ValueError(f"priority not found: {args.id}")
                priority["status"] = args.status
                if args.status in TERMINAL:
                    if not args.evidence:
                        raise ValueError("terminal priority status requires --evidence")
                    priority["completion"] = {"evidence": args.evidence}
                else:
                    priority.pop("completion", None)
    except ValueError as error:
        print(f"Error: {error}", file=sys.stderr)
        return 1

    print(f"Updated priority {priority['id']}: {priority['status']} tasks={priority.get('task_ids', [])}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
