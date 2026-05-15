#!/usr/bin/env python3
"""Get a workflow by name and print full details."""

import argparse
import json
import sys

from _workflows_store import load_workflows


def main():
    parser = argparse.ArgumentParser(description="Get a workflow by name.")
    parser.add_argument("name", help="Workflow name")
    args = parser.parse_args()
    name = args.name.strip()

    try:
        workflows = load_workflows()
    except (json.JSONDecodeError, ValueError, OSError) as exc:
        print(f"Error: {exc}", file=sys.stderr)
        return 1

    workflow = next((w for w in workflows if w.get("name") == name), None)
    if workflow is None:
        print(f"Error: workflow not found: {name}", file=sys.stderr)
        return 1

    print(f"Name:        {workflow.get('name', '')}")
    print(f"Description: {workflow.get('description', '')}")
    print(f"Updated:     {workflow.get('updated_at', '')}")
    print()
    print("--- Body ---")
    print(workflow.get("body", ""))
    return 0


if __name__ == "__main__":
    sys.exit(main())
