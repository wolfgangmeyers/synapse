#!/usr/bin/env python3
"""Query a remote subordinate's supervisor state via SSH.

Usage:
  python remote_state.py --host HOST [--json] [--task ALIAS]

SSHes into the remote machine and runs state_read.py. Optionally filters
to a specific task by alias.

Examples:
  # Get full state from a subordinate
  python remote_state.py --host devbox-2

  # Get JSON state for programmatic use
  python remote_state.py --host devbox-2 --json

  # Query a specific task
  python remote_state.py --host devbox-2 --task login-fix
"""

import argparse
import json
import os
import subprocess
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from _cluster import resolve_hostname, ssh_run
from _state import render_task


def main():
    parser = argparse.ArgumentParser(
        description="Query a remote subordinate's supervisor state via SSH."
    )
    parser.add_argument(
        "--host", required=True,
        help="Remote host ID (from cluster.json) or hostname",
    )
    parser.add_argument(
        "--json", action="store_true", dest="json_mode",
        help="Output raw JSON instead of markdown",
    )
    parser.add_argument(
        "--task", default=None,
        help="Filter to a specific task by alias",
    )

    args = parser.parse_args()

    hostname = resolve_hostname(args.host)

    remote_cmd = "python3 ~/.claude/skills/supervisor/tools/state_read.py --json"

    print(f"Querying state from {hostname}...", file=sys.stderr)

    try:
        result = ssh_run(hostname, remote_cmd)
    except ValueError as e:
        print(f"Error: {e}", file=sys.stderr)
        sys.exit(1)
    except (subprocess.TimeoutExpired, OSError) as e:
        print(f"SSH connection failed to {hostname}: {e}", file=sys.stderr)
        sys.exit(1)

    if result.returncode != 0:
        print(f"Remote state_read.py failed (exit {result.returncode}):", file=sys.stderr)
        if result.stderr:
            print(result.stderr, file=sys.stderr)
        sys.exit(1)

    # Parse the JSON response
    try:
        state = json.loads(result.stdout)
    except json.JSONDecodeError as e:
        print(f"Failed to parse remote state JSON: {e}", file=sys.stderr)
        if result.stdout:
            print(f"Raw output: {result.stdout[:500]}", file=sys.stderr)
        sys.exit(1)

    # Filter to specific task if requested
    if args.task:
        tasks = state.get("tasks", [])
        matched = [t for t in tasks if t.get("alias") == args.task]
        if not matched:
            print(f"No task with alias '{args.task}' found on {hostname}.", file=sys.stderr)
            sys.exit(1)
        state["tasks"] = matched

    if args.json_mode:
        print(json.dumps(state, indent=2))
    else:
        # Render using _state.py's render_task for consistent output
        tasks = state.get("tasks", [])
        print(f"# State from {hostname}")
        print()
        if not tasks:
            print("_No tasks_")
        for task in tasks:
            lines = render_task(task, show_state=True)
            print("\n".join(lines))
            print()


if __name__ == "__main__":
    main()
