#!/usr/bin/env python3
"""Add a new task to the supervisor state.

Usage:
  python state_add.py --alias ALIAS --type TYPE --project PROJECT --description DESC
      [--purpose P] [--window W] [--branch B] [--worktree W] [--pr URL]
      [--linear L] [--dev-environment DE]
"""

import argparse
import sys
import os

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from _state import locked_state, find_task, now_timestamp, REQUIRED_FIELDS, OPTIONAL_FIELDS, ALL_FIELDS
from _cluster import make_remote_block


def main():
    parser = argparse.ArgumentParser(
        description="Add a new task to the supervisor state."
    )
    parser.add_argument("--alias", required=True, help="Short alias for the task")
    parser.add_argument("--type", required=True, help="Task type (e.g. feature, bugfix)")
    parser.add_argument("--project", required=True, help="Project path or name")
    parser.add_argument("--description", required=True, help="Task description")
    parser.add_argument("--window", default=None, help="Tmux window name")
    parser.add_argument("--branch", default=None, help="Git branch name")
    parser.add_argument("--worktree", default=None, help="Git worktree path")
    parser.add_argument("--pr", default=None, help="Pull request URL")
    parser.add_argument("--linear", default=None, help="Linear issue identifier")
    parser.add_argument("--dev-environment", default=None, dest="dev_environment",
                        help="Dev environment identifier")
    parser.add_argument("--purpose", default=None,
                        help="Why this task exists — what problem it solves or goal it achieves")
    parser.add_argument("--requirements", default=None,
                        help="Detailed markdown requirements for what the worker should accomplish")
    parser.add_argument("--agent", default=None,
                        help="Agent type running the task: 'claude' or 'codex' (omit for supervisor/direct tasks)")
    parser.add_argument("--remote-host", default=None, dest="remote_host",
                        help="Remote host ID for remote-worker tasks")
    parser.add_argument("--remote-hostname", default=None, dest="remote_hostname",
                        help="SSH hostname for remote-worker tasks (defaults to remote-host)")
    parser.add_argument("--remote-alias", default=None, dest="remote_alias",
                        help="Task alias on the remote subordinate")
    parser.add_argument("--remote-task-id", default=None, dest="remote_task_id", type=int,
                        help="Task ID on the remote subordinate")

    args = parser.parse_args()

    # Validate type/remote-host consistency
    if args.type == "remote-worker" and not args.remote_host:
        print("Error: --type remote-worker requires --remote-host", file=sys.stderr)
        sys.exit(1)
    if args.remote_host and args.type != "remote-worker":
        print(f"Warning: --remote-host provided but --type is '{args.type}', not 'remote-worker'",
              file=sys.stderr)

    with locked_state() as state:
        task_id = state.get("next_task_id", 1)

        task = {
            "id": task_id,
            "alias": args.alias,
            "state": "active",
            "type": args.type,
            "project": args.project,
            "description": args.description,
            "window": args.window,
            "branch": args.branch,
            "worktree": args.worktree,
            "pr": args.pr,
            "linear": args.linear,
            "agent": args.agent,
            "dev_environment": args.dev_environment,
            "purpose": args.purpose,
            "requirements": args.requirements,
            "last_checked": now_timestamp(),
            "last_summary": None,
            "completed": None,
            "result": None,
            "abandoned": None,
            "reason": None,
        }

        if args.remote_host:
            task["remote"] = make_remote_block(
                host_id=args.remote_host,
                hostname=args.remote_hostname or args.remote_host,
                subordinate_alias=args.remote_alias,
                subordinate_task_id=args.remote_task_id,
            )

        state["tasks"].append(task)
        state["next_task_id"] = task_id + 1

    print(f"Added task-{task_id}: {args.alias} ({args.type} in {args.project})")


if __name__ == "__main__":
    main()
