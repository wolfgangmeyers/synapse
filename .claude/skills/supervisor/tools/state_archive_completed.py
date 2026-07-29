#!/usr/bin/env python3
"""Archive completed tasks older than a given age.

Usage:
  python state_archive_completed.py [--age DAYS] [--confirmed]

Without --confirmed, performs a dry run showing what would be archived.

The archive file is capped at ARCHIVE_MAX_TASKS tasks; the oldest completed
tasks beyond the cap are dropped on each write and listed as they are dropped.

Only tasks whose state is `completed` and whose `completed` date parses are ever
archived. A run that archives nothing reports that on stderr, leaves stdout empty,
and still exits 0 — check the message, not the status.
"""

import argparse
import json
import os
import sys
from datetime import datetime, timedelta

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from _state import STATE_FILE, locked_state

ARCHIVE_FILE = os.path.expanduser("~/.mecha-developer/supervisor-state-archive.json")

# Retention: the archive keeps at most this many tasks. Archiving trades a bounded
# state file for an archive that would otherwise grow forever, so the newest
# ARCHIVE_MAX_TASKS entries are kept and older ones are dropped on every write.
# Real tasks serialize to a median of ~6 KB each (journals dominate; the largest
# measured was 12 KB), so this bounds the archive at roughly 1.2 MB, 2.5 MB worst case.
ARCHIVE_MAX_TASKS = 200


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


def task_identity(task):
    """Identity used to keep re-archiving the same task from duplicating it.

    A crash between the archive write and the state-file write leaves a task in both
    files; the next run would otherwise append a second copy that also consumes the
    cap.
    """
    return (task.get("id"), task.get("alias"), task.get("completed"))


def prune_archive(tasks, max_tasks=ARCHIVE_MAX_TASKS):
    """Return (kept, dropped) with at most max_tasks newest-completed tasks.

    `dropped` is the list of discarded tasks, so callers can report exactly what was
    lost. Duplicates are collapsed first, keeping the last copy. Sorting is stable and
    tasks with an unusable `completed` date sort oldest, so they are dropped first and
    insertion order breaks ties.
    """
    deduped = list({task_identity(task): task for task in tasks}.values())
    if max_tasks <= 0:
        return [], deduped
    if len(deduped) <= max_tasks:
        return deduped, []
    ordered = sorted(deduped, key=lambda task: parse_date(task.get("completed")) or datetime.min)
    return ordered[-max_tasks:], ordered[:-max_tasks]


def report_nothing_to_archive(tasks, age):
    """Say plainly that nothing changed, so a no-op does not read as a success."""
    completed = [task for task in tasks if task.get("state") == "completed"]
    dateless = [task for task in completed if parse_date(task.get("completed")) is None]
    message = f"Nothing archived. {len(completed)} completed task(s) in the state file"
    if len(completed) == len(dateless):
        message += "." if not completed else ", none with a usable `completed` date."
    else:
        message += f", none of them completed more than {age} days ago."
        if age > 0:
            message += " Use a smaller --age to archive recent ones."
    if dateless and completed != dateless:
        message += (
            f" ({len(dateless)} have no usable `completed` date and cannot be "
            "archived at any --age.)"
        )
    print(message, file=sys.stderr)


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
            report_nothing_to_archive(state["tasks"], args.age)
            return
        print(f"[DRY RUN] Archiving {len(to_archive)} completed tasks (older than {args.age} days):\n")
        for task in to_archive:
            print(f"  {task['alias']:25s} completed {task.get('completed', '?')}")
        print(f"\nRe-run with --confirmed to archive these {len(to_archive)} tasks.")
        return

    with locked_state() as state:
        to_archive, to_keep = partition(state["tasks"])
        if not to_archive:
            report_nothing_to_archive(state["tasks"], args.age)
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
        archive["tasks"], dropped = prune_archive(archive["tasks"], ARCHIVE_MAX_TASKS)
        archive["archived_at"] = datetime.now().strftime("%Y-%m-%d %H:%M:%S")
        if dropped:
            print(
                f"\nArchive capped at {ARCHIVE_MAX_TASKS} tasks: "
                f"dropped {len(dropped)} oldest completed task(s), permanently:"
            )
            for task in dropped:
                print(f"  {task.get('alias', '?'):25s} completed {task.get('completed', '?')}")
            # A task dropped on the same run that archived it was never in the archive
            # at all: this write is the only copy and it is being discarded, while the
            # state file is about to lose it too. That is not history rolling off.
            incoming = {task_identity(task) for task in to_archive}
            just_archived = [task for task in dropped if task_identity(task) in incoming]
            if just_archived:
                print(
                    f"\nWARNING: {len(just_archived)} of those were archived by this run "
                    "and are now gone from both the state file and the archive. Archive "
                    f"more often, or raise ARCHIVE_MAX_TASKS above {ARCHIVE_MAX_TASKS}, "
                    "to keep them.",
                    file=sys.stderr,
                )

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
