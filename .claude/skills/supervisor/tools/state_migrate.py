#!/usr/bin/env python3
"""One-time migration script: converts responsibilities.md to supervisor-state.json."""

import json
import os
import re
import sys


FIELD_MAP = {
    "Type": "type",
    "Project": "project",
    "Window": "window",
    "Branch": "branch",
    "Worktree": "worktree",
    "PR": "pr",
    "Linear": "linear",
    "Description": "description",
    "Review status": "review_status",
    "Dev environment": "dev_environment",
    "Resumed from": "resumed_from",
    "Last checked": "last_checked",
    "Last summary": "last_summary",
    "Completed": "completed",
    "Result": "result",
    "Abandoned": "abandoned",
    "Reason": "reason",
}

ALL_JSON_KEYS = [
    "id",
    "alias",
    "state",
    "type",
    "project",
    "window",
    "branch",
    "worktree",
    "pr",
    "linear",
    "description",
    "review_status",
    "dev_environment",
    "resumed_from",
    "last_checked",
    "last_summary",
    "completed",
    "result",
    "abandoned",
    "reason",
]


def parse_responsibilities(text):
    """Parse the markdown responsibilities file and return (tasks, max_id)."""
    tasks = []
    max_id = 0
    current_section = None  # "active", "completed", "abandoned"
    current_task = None

    section_re = re.compile(r"^##\s+(Active|Completed|Abandoned)\s*$", re.IGNORECASE)
    task_re = re.compile(r"^###\s+task-(\d+):\s*(.+)$")
    field_re = re.compile(r"^-\s+\*\*(.+?)\*\*:\s*(.+)$")

    for line in text.splitlines():
        line = line.rstrip()

        # Check for section headers
        m = section_re.match(line)
        if m:
            # Save any in-progress task before switching sections
            if current_task is not None:
                tasks.append(current_task)
                current_task = None
            current_section = m.group(1).lower()
            continue

        # Check for task headers
        m = task_re.match(line)
        if m:
            # Save previous task
            if current_task is not None:
                tasks.append(current_task)
            task_id = int(m.group(1))
            alias = m.group(2).strip()
            if task_id > max_id:
                max_id = task_id
            # Build task dict with all keys defaulting to None
            current_task = {k: None for k in ALL_JSON_KEYS}
            current_task["id"] = task_id
            current_task["alias"] = alias
            current_task["state"] = current_section
            continue

        # Check for field lines
        m = field_re.match(line)
        if m and current_task is not None:
            field_name = m.group(1)
            field_value = m.group(2).strip()
            json_key = FIELD_MAP.get(field_name)
            if json_key is not None:
                current_task[json_key] = field_value
            continue

    # Don't forget the last task
    if current_task is not None:
        tasks.append(current_task)

    return tasks, max_id


def migrate(dry_run=False):
    """Read responsibilities.md and produce supervisor-state.json."""
    input_path = os.path.expanduser("~/.mecha-wolfgang/responsibilities.md")

    if not os.path.isfile(input_path):
        print(f"Error: file not found: {input_path}", file=sys.stderr)
        sys.exit(1)

    with open(input_path, "r", encoding="utf-8") as f:
        text = f.read()

    tasks, max_id = parse_responsibilities(text)

    # Count tasks per section
    counts = {}
    for t in tasks:
        state = t["state"] or "unknown"
        counts[state] = counts.get(state, 0) + 1

    state_obj = {
        "next_task_id": max_id + 1,
        "tasks": tasks,
    }

    output = json.dumps(state_obj, indent=2, ensure_ascii=False) + "\n"

    if dry_run:
        print(output, end="")
    else:
        output_path = os.path.expanduser("~/.mecha-wolfgang/supervisor-state.json")
        os.makedirs(os.path.dirname(output_path), exist_ok=True)
        with open(output_path, "w", encoding="utf-8") as f:
            f.write(output)
        print(f"Wrote {output_path}")

    # Report counts
    total = len(tasks)
    print(f"\nMigrated {total} task(s):", file=sys.stderr)
    for section in ("active", "completed", "abandoned"):
        if section in counts:
            print(f"  {section}: {counts[section]}", file=sys.stderr)


if __name__ == "__main__":
    if len(sys.argv) > 2:
        print(f"Usage: {sys.argv[0]} [--dry-run]", file=sys.stderr)
        sys.exit(1)

    dry_run = False
    if len(sys.argv) == 2:
        if sys.argv[1] == "--dry-run":
            dry_run = True
        else:
            print(f"Usage: {sys.argv[0]} [--dry-run]", file=sys.stderr)
            sys.exit(1)

    migrate(dry_run=dry_run)
