#!/usr/bin/env python3
"""Archive completed tasks older than a given age.

Usage:
  python state_archive_completed.py [--age DAYS] [--confirmed]

Without --confirmed, performs a dry run showing what would be archived.
"""

import argparse
import json
import os
import sys
from datetime import datetime, timedelta

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from _state import STATE_FILE, locked_state

ARCHIVE_FILE = os.path.expanduser("~/.mecha-wolfgang/supervisor-state-archive.json")


def parse_date(s):
    """Try common date formats."""
    if not s:
        return None
    for fmt in ("%Y-%m-%d %H:%M", "%Y-%m-%d %H:%M:%S", "%Y-%m-%d"):
        try:
            return datetime.strptime(s, fmt)
        except ValueError:
            continue
    return None


def main():
    parser = argparse.ArgumentParser(description="Archive old completed tasks.")
    parser.add_argument(
        "--age",
        type=int,
        default=7,
        help="Archive completed tasks older than this many days (default: 7)",
    )
    parser.add_argument(
        "--confirmed",
        action="store_true",
        help="Actually archive (without this flag, dry run only)",
    )
    args = parser.parse_args()

    cutoff = datetime.now() - timedelta(days=args.age)

    def partition(tasks):
        to_archive, to_keep = [], []
        for task in tasks:
            if task["state"] != "completed":
                to_keep.append(task)
                continue
            completed_date = parse_date(task.get("completed"))
            if completed_date and completed_date < cutoff:
                to_archive.append(task)
            else:
                to_keep.append(task)
        return to_archive, to_keep

    if not args.confirmed:
        with open(STATE_FILE) as f:
            state = json.load(f)
        to_archive, _ = partition(state["tasks"])
        if not to_archive:
            print(f"No completed tasks older than {args.age} days to archive.")
            return
        print(f"[DRY RUN] Archiving {len(to_archive)} completed tasks (older than {args.age} days):\n")
        for task in to_archive:
            print(f"  {task['alias']:25s} completed {task.get('completed', '?')}")
        print(f"\nRe-run with --confirmed to archive these {len(to_archive)} tasks.")
        return

    with locked_state() as state:
        to_archive, to_keep = partition(state["tasks"])
        if not to_archive:
            print(f"No completed tasks older than {args.age} days to archive.")
            return

        print(f"Archiving {len(to_archive)} completed tasks (older than {args.age} days):\n")
        for task in to_archive:
            print(f"  {task['alias']:25s} completed {task.get('completed', '?')}")

        if os.path.exists(ARCHIVE_FILE):
            with open(ARCHIVE_FILE) as f:
                archive = json.load(f)
        else:
            archive = {"archived_at": None, "tasks": []}
        archive["tasks"].extend(to_archive)
        archive["archived_at"] = datetime.now().strftime("%Y-%m-%d %H:%M:%S")

        state["tasks"] = to_keep

        tmp_archive = ARCHIVE_FILE + ".tmp"
        with open(tmp_archive, "w") as f:
            json.dump(archive, f, indent=2)
            f.flush()
            os.fsync(f.fileno())
        os.replace(tmp_archive, ARCHIVE_FILE)

    print(f"\nDone. Archived {len(to_archive)} tasks. {len(to_keep)} tasks remain in state file.")


if __name__ == "__main__":
    main()
