#!/usr/bin/env python3
"""Send a mailbox message to a remote machine via SSH.

Usage:
  python remote_send.py --host HOST --to RECIPIENT --body TEXT [--from SENDER] [--subject SUBJECT]

Runs mailbox_send.py on the remote machine via SSH. The remote machine must
be reachable over Tailscale SSH and have mecha-wolfgang installed.

Examples:
  # Send a task to a subordinate's supervisor
  python remote_send.py --host devbox-2 --to supervisor --from head --body "New task: fix login bug"

  # Subordinate reporting back to head
  python remote_send.py --host wolfgangs-laptop --to supervisor --from devbox-2 --body "Task complete"
"""

import argparse
import os
import re
import shlex
import subprocess
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from _cluster import load_cluster, resolve_hostname, ssh_run

# Same pattern used by mailbox_send.py for agent name validation
_AGENT_NAME_RE = re.compile(r"^[a-zA-Z0-9][a-zA-Z0-9_-]*$")


def main():
    parser = argparse.ArgumentParser(
        description="Send a mailbox message to a remote machine via SSH."
    )
    parser.add_argument(
        "--host", required=True,
        help="Remote host ID (from cluster.json) or hostname",
    )
    parser.add_argument("--to", required=True, help="Recipient mailbox name")
    parser.add_argument("--body", required=True, help="Message body text")
    parser.add_argument(
        "--from", dest="from_agent", default=None,
        help="Sender name (defaults to this machine's hostname)",
    )
    parser.add_argument("--subject", default=None, help="Optional subject line")

    args = parser.parse_args()

    # Validate agent names locally before SSH round-trip
    if not _AGENT_NAME_RE.match(args.to):
        print(f"Error: invalid --to name '{args.to}'. "
              "Must be alphanumeric with hyphens or underscores.", file=sys.stderr)
        sys.exit(1)

    hostname = resolve_hostname(args.host)

    # Determine sender
    sender = args.from_agent
    if sender is None:
        cluster = load_cluster()
        if cluster is not None:
            sender = cluster.get("hostname", "unknown")
        else:
            sender = "unknown"

    if not _AGENT_NAME_RE.match(sender):
        print(f"Error: invalid --from name '{sender}'. "
              "Must be alphanumeric with hyphens or underscores.", file=sys.stderr)
        sys.exit(1)

    # Build the remote mailbox_send.py command
    remote_cmd = (
        "python3 ~/.claude/skills/supervisor/tools/mailbox_send.py"
        f" --to {shlex.quote(args.to)}"
        f" --from {shlex.quote(sender)}"
        f" --body {shlex.quote(args.body)}"
    )
    if args.subject:
        remote_cmd += f" --subject {shlex.quote(args.subject)}"

    print(f"Sending message to {args.to}@{hostname} via SSH...")

    try:
        result = ssh_run(hostname, remote_cmd)
    except ValueError as e:
        print(f"Error: {e}", file=sys.stderr)
        sys.exit(1)
    except (subprocess.TimeoutExpired, OSError) as e:
        print(f"SSH connection failed: {e}", file=sys.stderr)
        sys.exit(1)

    if result.returncode != 0:
        print(f"Remote command failed (exit {result.returncode}):", file=sys.stderr)
        if result.stderr:
            print(result.stderr, file=sys.stderr)
        sys.exit(1)

    # Show remote output
    if result.stdout:
        print(result.stdout.rstrip())

    print(f"\nMessage delivered to {args.to}@{hostname}.")


if __name__ == "__main__":
    main()
