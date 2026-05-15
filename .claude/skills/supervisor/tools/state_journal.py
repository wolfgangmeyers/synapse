#!/usr/bin/env python3
"""View the status journal for a specific task.

Usage:
  python state_journal.py <task-id-or-alias>
  python state_journal.py <task-id-or-alias> --last N
"""

import sys
import os

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from _state import load_state, find_task

USAGE = f"Usage: {os.path.basename(__file__)} <task-id-or-alias> [--last N]"


def parse_identifier(raw):
    """Return an int if *raw* looks like an integer, otherwise a string."""
    try:
        return int(raw)
    except ValueError:
        return raw


def main():
    if len(sys.argv) < 2 or len(sys.argv) > 4:
        print(USAGE, file=sys.stderr)
        sys.exit(1)

    identifier = parse_identifier(sys.argv[1])
    last_n = None

    if len(sys.argv) >= 3:
        if sys.argv[2] == "--last" and len(sys.argv) == 4:
            try:
                last_n = int(sys.argv[3])
            except ValueError:
                print(f"Error: --last requires an integer, got: {sys.argv[3]}", file=sys.stderr)
                sys.exit(1)
        else:
            print(USAGE, file=sys.stderr)
            sys.exit(1)

    state = load_state()
    task = find_task(state, identifier)

    if task is None:
        print(f"Error: task not found: {sys.argv[1]}", file=sys.stderr)
        sys.exit(1)

    task_id = task.get("id", "?")
    alias = task.get("alias", "unknown")
    journal = task.get("journal", [])

    if not journal:
        print(f"task-{task_id} ({alias}): No journal entries.")
        return

    entries = journal
    if last_n is not None:
        entries = journal[-last_n:]

    total = len(journal)
    showing = len(entries)

    if last_n is not None and showing < total:
        print(f"task-{task_id} ({alias}): Showing last {showing} of {total} journal entries\n")
    else:
        print(f"task-{task_id} ({alias}): {total} journal entries\n")

    for entry in entries:
        ts = entry.get("timestamp", "???")
        text = entry.get("entry", "")
        print(f"  [{ts}]  {text}")


if __name__ == "__main__":
    main()
