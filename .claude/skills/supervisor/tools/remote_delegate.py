#!/usr/bin/env python3
"""Delegate a task to a subordinate supervisor.

Usage:
  python remote_delegate.py --host HOST --project PROJECT --description DESC
      [--purpose P] [--requirements R] [--alias-hint HINT]

Steps:
  1. Sends a structured task message to the subordinate's supervisor mailbox
  2. Creates a remote-worker entry in the head's local state
  3. Returns the local alias for tracking

Examples:
  python remote_delegate.py --host devbox-2 --project <project-name> \\
      --description "Fix login timeout bug" \\
      --purpose "Users experiencing 30s timeouts" \\
      --requirements "1. Add timeout config. 2. Write tests. 3. PR to main."
"""

import argparse
import os
import re
import shlex
import subprocess
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from _cluster import load_cluster, find_subordinate, resolve_hostname, ssh_run, make_remote_block
from _state import locked_state, create_task


def _make_alias(host_id, description):
    """Generate a local alias like 'devbox2-login-fix' from host and description."""
    # Sanitize host_id: replace non-alphanumeric with nothing
    host_part = re.sub(r"[^a-z0-9]", "", host_id.lower())

    # Take first 2-3 meaningful words from description
    words = re.findall(r"[a-z]+", description.lower())
    desc_part = "-".join(words[:3]) if words else "task"

    return f"{host_part}-{desc_part}"


def _build_delegation_message(project, description, purpose, requirements, local_alias):
    """Build the structured task delegation message body."""
    lines = [
        "[HEAD-TASK] task delegation",
        "",
        f"Project: {project}",
        f"Description: {description}",
    ]
    if purpose:
        lines.append(f"Purpose: {purpose}")
    if requirements:
        lines.extend(["Requirements:", requirements])
    lines.extend([
        "",
        "Report back via mailbox when:",
        "- Task started (with alias and branch)",
        "- Task blocked (with reason)",
        "- Task complete (with PR URL and summary)",
        "",
        f"Head task reference: {local_alias}",
    ])
    return "\n".join(lines)


def main():
    parser = argparse.ArgumentParser(
        description="Delegate a task to a subordinate supervisor."
    )
    parser.add_argument(
        "--host", required=True,
        help="Subordinate host ID (from cluster.json) or hostname",
    )
    parser.add_argument("--project", required=True, help="Project path or name")
    parser.add_argument("--description", required=True, help="Task description")
    parser.add_argument("--purpose", default=None, help="Why this task exists")
    parser.add_argument("--requirements", default=None, help="Detailed requirements")
    parser.add_argument(
        "--alias-hint", default=None, dest="alias_hint",
        help="Override the auto-generated alias",
    )

    args = parser.parse_args()

    # Validate host is a known subordinate (warn if not)
    sub = find_subordinate(args.host)
    if sub is None:
        cluster = load_cluster()
        if cluster is not None and cluster.get("role") == "head":
            print(f"Warning: '{args.host}' is not a known subordinate in cluster.json. "
                  "Proceeding with hostname as-is.", file=sys.stderr)

    # Resolve hostname
    hostname = resolve_hostname(args.host)

    # Generate local alias
    local_alias = args.alias_hint or _make_alias(args.host, args.description)

    # Determine sender
    cluster = load_cluster()
    sender = "head"
    if cluster is not None:
        sender = cluster.get("hostname", "head")

    # Build delegation message
    body = _build_delegation_message(
        args.project, args.description, args.purpose,
        args.requirements, local_alias,
    )

    # Send via SSH
    remote_cmd = (
        "python3 ~/.claude/skills/supervisor/tools/mailbox_send.py"
        " --to supervisor"
        f" --from {shlex.quote(sender)}"
        f" --subject {shlex.quote('Task delegation from head')}"
        f" --body {shlex.quote(body)}"
    )

    print(f"Delegating task to {args.host} ({hostname})...")

    try:
        result = ssh_run(hostname, remote_cmd)
    except ValueError as e:
        print(f"Error: {e}", file=sys.stderr)
        sys.exit(1)
    except (subprocess.TimeoutExpired, OSError) as e:
        print(f"SSH connection failed to {hostname}: {e}", file=sys.stderr)
        sys.exit(1)

    if result.returncode != 0:
        print(f"Remote mailbox_send.py failed (exit {result.returncode}):", file=sys.stderr)
        if result.stderr:
            print(result.stderr, file=sys.stderr)
        sys.exit(1)

    with locked_state() as state:
        task = create_task(
            state,
            alias=local_alias,
            task_type="remote-worker",
            project=args.project,
            description=args.description,
            purpose=args.purpose,
            requirements=args.requirements,
            remote=make_remote_block(args.host, hostname),
            last_summary="Task delegated, awaiting subordinate acknowledgment",
        )

    print("\nTask delegated successfully.")
    print(f"  Local alias: {local_alias}")
    print(f"  Local task ID: task-{task.get('id')}")
    print(f"  Remote host: {args.host} ({hostname})")
    print(f"  Project: {args.project}")
    print("\nThe subordinate's supervisor will receive the task via mailbox.")
    print(f"Monitor with: python remote_state.py --host {args.host}")


if __name__ == "__main__":
    main()
