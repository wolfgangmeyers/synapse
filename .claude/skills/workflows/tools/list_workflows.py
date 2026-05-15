#!/usr/bin/env python3
"""List saved workflow templates."""

import json
import sys

from _workflows_store import load_workflows


def main():
    try:
        workflows = load_workflows()
    except (json.JSONDecodeError, ValueError, OSError) as exc:
        print(f"Error: {exc}", file=sys.stderr)
        return 1

    if not workflows:
        print("No workflows defined yet.")
        return 0

    width = max(len(w.get("name", "")) for w in workflows) + 2
    for workflow in workflows:
        name = workflow.get("name", "")
        description = workflow.get("description", "")
        print(f"{name:<{width}}{description}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
