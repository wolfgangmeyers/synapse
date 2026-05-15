#!/usr/bin/env python3
"""Query the supervisor state with filters.

Usage: python state_query.py [--state STATE] [--type TYPE] [--project PROJECT]
                             [--id ID] [--alias ALIAS]

All flags are optional and combinable (AND logic). At least one filter must
be provided.

  --state STATE      Filter by state (active/completed/abandoned)
  --type TYPE        Filter by type (worker/pipeline/direct)
  --project PROJECT  Filter by project (substring match, case-insensitive)
  --id ID            Filter by task ID (integer)
  --alias ALIAS      Filter by alias (exact match)
"""

import argparse
import sys
import os

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from _state import load_state, render_task


def main():
    parser = argparse.ArgumentParser(
        description="Query supervisor state with filters.",
        usage="%(prog)s [--state STATE] [--type TYPE] [--project PROJECT] "
              "[--id ID] [--alias ALIAS]",
    )
    parser.add_argument("--state", help="Filter by state (active/completed/abandoned)")
    parser.add_argument("--type", help="Filter by type (worker/pipeline/direct)")
    parser.add_argument("--project", help="Filter by project (substring match, case-insensitive)")
    parser.add_argument("--id", type=int, help="Filter by task ID (integer)")
    parser.add_argument("--alias", help="Filter by alias (exact match)")

    args = parser.parse_args()

    # Require at least one filter.
    if not any([args.state, args.type, args.project, args.id is not None, args.alias]):
        parser.print_usage(sys.stderr)
        print("error: at least one filter must be provided", file=sys.stderr)
        sys.exit(1)

    state = load_state()
    tasks = state.get("tasks", [])

    # Apply filters (AND logic).
    matches = []
    for task in tasks:
        if args.state and task.get("state") != args.state:
            continue
        if args.type and task.get("type") != args.type:
            continue
        if args.project and args.project.lower() not in (task.get("project") or "").lower():
            continue
        if args.id is not None and task.get("id") != args.id:
            continue
        if args.alias and task.get("alias") != args.alias:
            continue
        matches.append(task)

    if not matches:
        print("No matching tasks.")
        sys.exit(0)

    # Sort by task ID for consistent output.
    matches.sort(key=lambda t: t.get("id", 0))

    output_lines = []
    for task in matches:
        output_lines.extend(render_task(task, show_state=True))
        output_lines.append("")

    # Strip trailing blank lines, add single final newline.
    while output_lines and output_lines[-1] == "":
        output_lines.pop()
    output_lines.append("")

    print("\n".join(output_lines))


if __name__ == "__main__":
    main()
