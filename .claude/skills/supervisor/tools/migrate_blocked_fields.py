#!/usr/bin/env python3
"""Add blocked and blocked_reason fields to all existing tasks."""

import sys
import os

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from _state import locked_state


def main():
    with locked_state() as state:
        tasks = state.get("tasks", [])

        updated = 0
        for task in tasks:
            if "blocked" not in task:
                task["blocked"] = None
                updated += 1
            if "blocked_reason" not in task:
                task["blocked_reason"] = None

    if updated > 0:
        print(f"Migrated {updated} tasks to include blocked fields")
    else:
        print("All tasks already have blocked fields")


if __name__ == "__main__":
    main()
