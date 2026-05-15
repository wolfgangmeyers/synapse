#!/usr/bin/env python3
"""Delete one or more workflow templates."""

import argparse
import json
import sys

from _workflows_store import load_workflows, save_workflows, store_lock


def main():
    parser = argparse.ArgumentParser(description="Delete workflows.")
    scope = parser.add_mutually_exclusive_group(required=True)
    scope.add_argument("--name", action="append", help="Workflow name to delete")
    scope.add_argument("--all", action="store_true", help="Delete all workflows")
    parser.add_argument(
        "--confirmed",
        action="store_true",
        help="Actually perform deletion (required)",
    )
    args = parser.parse_args()

    try:
        with store_lock():
            workflows = load_workflows()

            all_names = [w.get("name", "") for w in workflows]
            if args.all:
                to_delete = all_names
                requested = set(all_names)
            else:
                requested = set(args.name or [])
                to_delete = [name for name in all_names if name in requested]

            missing = sorted(name for name in requested if name not in set(all_names))
            for name in missing:
                print(f"Warning: workflow not found: {name}", file=sys.stderr)

            if not args.confirmed:
                joined = ", ".join(to_delete) if to_delete else "(none)"
                print(f"Would delete: {joined}")
                print("Pass --confirmed to proceed. User confirmation is required before deletion.")
                return 0

            if not args.all and not to_delete:
                print("Nothing deleted.", file=sys.stderr)
                return 1

            if args.all:
                remaining = []
            else:
                delete_set = set(to_delete)
                remaining = [w for w in workflows if w.get("name") not in delete_set]

            save_workflows(remaining)
    except (json.JSONDecodeError, ValueError, OSError) as exc:
        print(f"Error: {exc}", file=sys.stderr)
        return 1

    for name in to_delete:
        print(f"Deleted: {name}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
