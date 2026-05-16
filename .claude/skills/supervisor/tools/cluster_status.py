#!/usr/bin/env python3
"""Show cluster-wide status overview.

Usage:
  python cluster_status.py [--json] [--check-health]

Displays all machines in the cluster, their roles, active tasks, and capacity.
With --check-health, SSHes into each subordinate to verify connectivity and
tmux session status (slower but more accurate).

Examples:
  # Quick overview from local state only
  python cluster_status.py

  # Full health check with SSH probes
  python cluster_status.py --check-health

  # JSON output for programmatic use
  python cluster_status.py --json
"""

import argparse
import json
import os
import subprocess
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from _cluster import load_cluster, get_subordinates, ssh_run
from _state import load_state


def _count_remote_tasks_for_host(state, host_id):
    """Count active remote-worker tasks targeting a specific host."""
    count = 0
    for task in state.get("tasks", []):
        if task.get("state") != "active":
            continue
        if task.get("type") != "remote-worker":
            continue
        remote = task.get("remote", {})
        if remote.get("host") == host_id:
            count += 1
    return count


def _count_local_active(state):
    """Count active local worker tasks."""
    count = 0
    for task in state.get("tasks", []):
        if task.get("state") != "active":
            continue
        if task.get("type") == "remote-worker":
            continue
        count += 1
    return count


def _check_subordinate_health(hostname):
    """Check if a subordinate is reachable and has a mecha tmux session."""
    health = {"reachable": False, "tmux_running": False}

    try:
        result = ssh_run(hostname, "tmux has-session -t mecha", timeout=15)
        health["reachable"] = True
        health["tmux_running"] = result.returncode == 0
    except (subprocess.TimeoutExpired, subprocess.SubprocessError, OSError, ValueError) as e:
        health["reachable"] = False
        health["error"] = str(e)

    return health


def main():
    parser = argparse.ArgumentParser(
        description="Show cluster-wide status overview."
    )
    parser.add_argument(
        "--json", action="store_true", dest="json_mode",
        help="Output as JSON",
    )
    parser.add_argument(
        "--check-health", action="store_true", dest="check_health",
        help="SSH into subordinates to verify health (slower)",
    )

    args = parser.parse_args()

    cluster = load_cluster()
    if cluster is None:
        print("No cluster configuration found (~/.mecha-developer/cluster.json).")
        print("Operating in single-machine mode.")
        sys.exit(0)

    state = load_state()
    role = cluster.get("role", "unknown")
    hostname = cluster.get("hostname", "unknown")

    # Build cluster overview
    overview = {
        "this_machine": {
            "hostname": hostname,
            "role": role,
            "active_local_tasks": _count_local_active(state),
        },
        "subordinates": [],
    }

    if role == "head":
        for sub in get_subordinates():
            sub_id = sub.get("id", "unknown")
            sub_info = {
                "id": sub_id,
                "hostname": sub.get("hostname", sub_id),
                "capabilities": sub.get("capabilities", []),
                "max_workers": sub.get("max_workers", 0),
                "delegated_tasks": _count_remote_tasks_for_host(state, sub_id),
            }

            if args.check_health:
                sub_info["health"] = _check_subordinate_health(
                    sub.get("hostname", sub_id)
                )

            overview["subordinates"].append(sub_info)

    elif role == "subordinate":
        overview["head"] = cluster.get("head", "unknown")
        overview["capabilities"] = cluster.get("capabilities", [])
        overview["max_workers"] = cluster.get("max_workers", 0)

    # Collect remote-worker tasks for display
    remote_tasks = []
    for task in state.get("tasks", []):
        if task.get("type") == "remote-worker":
            remote_tasks.append({
                "alias": task.get("alias"),
                "state": task.get("state"),
                "host": task.get("remote", {}).get("host"),
                "project": task.get("project"),
                "description": task.get("description"),
                "last_checked": task.get("last_checked"),
                "last_summary": task.get("last_summary"),
            })
    overview["remote_tasks"] = remote_tasks

    if args.json_mode:
        print(json.dumps(overview, indent=2))
        return

    # Render markdown
    print(f"# Cluster Status")
    print()
    print(f"**This machine**: {hostname} (role: {role})")
    print(f"**Active local tasks**: {overview['this_machine']['active_local_tasks']}")
    print()

    if role == "head" and overview["subordinates"]:
        print("## Subordinates")
        print()
        for sub in overview["subordinates"]:
            delegated = sub["delegated_tasks"]
            max_w = sub["max_workers"]
            caps = ", ".join(sub["capabilities"]) if sub["capabilities"] else "none listed"
            print(f"### {sub['id']} ({sub['hostname']})")
            print(f"- **Capabilities**: {caps}")
            print(f"- **Capacity**: {delegated}/{max_w} workers")

            if "health" in sub:
                h = sub["health"]
                if not h["reachable"]:
                    print(f"- **Health**: UNREACHABLE")
                elif not h["tmux_running"]:
                    print(f"- **Health**: reachable but no mecha tmux session")
                else:
                    print(f"- **Health**: OK")
            print()

    elif role == "subordinate":
        print(f"**Head supervisor**: {overview.get('head', 'unknown')}")
        caps = ", ".join(overview.get("capabilities", []))
        print(f"**Capabilities**: {caps or 'none listed'}")
        print(f"**Max workers**: {overview.get('max_workers', 0)}")
        print()

    if remote_tasks:
        print("## Remote Tasks (delegated to subordinates)")
        print()
        for rt in remote_tasks:
            print(f"### {rt['alias']} [{rt['state']}]")
            print(f"- **Host**: {rt['host']}")
            print(f"- **Project**: {rt['project']}")
            print(f"- **Description**: {rt['description']}")
            if rt["last_checked"]:
                print(f"- **Last checked**: {rt['last_checked']}")
            if rt["last_summary"]:
                print(f"- **Last summary**: {rt['last_summary']}")
            print()
    elif role == "head":
        print("## Remote Tasks")
        print()
        print("_No tasks delegated to subordinates_")
        print()


if __name__ == "__main__":
    main()
