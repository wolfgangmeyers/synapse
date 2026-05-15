#!/usr/bin/env python3
"""Create or update a workflow template."""

import argparse
import json
import sys

from _workflows_store import load_workflows, now_iso, save_workflows, store_lock


def main():
    parser = argparse.ArgumentParser(description="Create or update a workflow template.")
    parser.add_argument("--name", required=True, help="Workflow name")
    parser.add_argument("--description", required=True, help="Short description")
    body_group = parser.add_mutually_exclusive_group(required=True)
    body_group.add_argument("--body", help="Workflow body")
    body_group.add_argument(
        "--body-file",
        help="Path to file containing workflow body (use '-' for stdin)",
    )
    args = parser.parse_args()

    args.name = args.name.strip()
    if not args.name:
        parser.error("name cannot be empty")

    if args.body_file:
        try:
            if args.body_file == "-":
                body = sys.stdin.read()
            else:
                with open(args.body_file, "r", encoding="utf-8") as fh:
                    body = fh.read()
        except OSError as exc:
            print(f"Error: cannot read body file: {exc}", file=sys.stderr)
            return 1
    else:
        body = args.body

    try:
        with store_lock():
            workflows = load_workflows()
            now = now_iso()
            existing = next((w for w in workflows if w.get("name") == args.name), None)

            if existing is None:
                workflows.append(
                    {
                        "name": args.name,
                        "description": args.description,
                        "body": body,
                        "created_at": now,
                        "updated_at": now,
                    }
                )
            else:
                existing["description"] = args.description
                existing["body"] = body
                if "created_at" not in existing:
                    existing["created_at"] = now
                existing["updated_at"] = now

            save_workflows(workflows)
    except (json.JSONDecodeError, ValueError, OSError) as exc:
        print(f"Error: {exc}", file=sys.stderr)
        return 1

    print(f"Saved workflow: {args.name}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
