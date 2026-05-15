#!/usr/bin/env python3
"""Remote watchdog — monitors subordinate health from the head.

Designed to run as a cron job every 30 minutes on the head machine.
For each subordinate with active delegated tasks:
  1. SSH in, check tmux session exists
  2. SSH in, read state, check task freshness
  3. Mail head supervisor about stale/down subordinates

Usage:
  python remote_watchdog.py [--stale-threshold MINUTES]

The stale threshold defaults to 60 minutes. Tasks with no update for longer
than this are flagged.

This script only runs on head machines (role: head in cluster.json).
On subordinate or unconfigured machines it exits silently.
"""

import argparse
import json
import os
import subprocess
import sys
from datetime import datetime

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from _cluster import load_cluster, get_subordinates, ssh_run
from _state import load_state, parse_last_checked


STALE_THRESHOLD_MINUTES = 60


def _check_tmux(hostname):
    """Check if the mecha tmux session is running on a remote host.

    Returns True (running), False (not running), or None (unreachable).
    """
    try:
        result = ssh_run(hostname, "tmux has-session -t mecha", timeout=15)
        return result.returncode == 0
    except (subprocess.TimeoutExpired, subprocess.SubprocessError, OSError, ValueError) as e:
        print(f"  SSH to {hostname} failed: {e}", file=sys.stderr)
        return None


def _get_remote_state(hostname):
    """Fetch the supervisor state from a remote host."""
    try:
        result = ssh_run(
            hostname,
            "python3 ~/.claude/skills/supervisor/tools/state_read.py --json",
        )
        if result.returncode != 0:
            if result.stderr:
                print(f"  state_read.py on {hostname} failed: {result.stderr.strip()}",
                      file=sys.stderr)
            return None
        return json.loads(result.stdout)
    except (subprocess.TimeoutExpired, subprocess.SubprocessError, OSError, ValueError) as e:
        print(f"  Failed to get state from {hostname}: {e}", file=sys.stderr)
        return None
    except json.JSONDecodeError as e:
        print(f"  Invalid JSON from {hostname}: {e}", file=sys.stderr)
        return None


def _find_stale_tasks(remote_state, head_state, host_id, stale_minutes):
    """Find tasks on a subordinate that haven't been updated recently."""
    stale = []
    now = datetime.now().timestamp()
    threshold = stale_minutes * 60

    # Find active tasks on the subordinate that match head's remote-worker entries
    for head_task in head_state.get("tasks", []):
        if head_task.get("state") != "active":
            continue
        if head_task.get("type") != "remote-worker":
            continue
        remote = head_task.get("remote", {})
        if remote.get("host") != host_id:
            continue

        sub_alias = remote.get("subordinate_alias")
        if not sub_alias:
            # We don't know the subordinate alias yet — check head's last_checked
            last = parse_last_checked(head_task.get("last_checked"))
            if last > 0 and (now - last) > threshold:
                stale.append({
                    "head_alias": head_task.get("alias"),
                    "reason": f"No subordinate alias known, head last checked {head_task.get('last_checked')}",
                })
            continue

        # Find matching task on subordinate
        sub_task = None
        if remote_state:
            for t in remote_state.get("tasks", []):
                if t.get("alias") == sub_alias:
                    sub_task = t
                    break

        if sub_task is None:
            stale.append({
                "head_alias": head_task.get("alias"),
                "reason": f"Task '{sub_alias}' not found in subordinate state",
            })
            continue

        last = parse_last_checked(sub_task.get("last_checked"))
        if last > 0 and (now - last) > threshold:
            age_min = int((now - last) / 60)
            stale.append({
                "head_alias": head_task.get("alias"),
                "sub_alias": sub_alias,
                "reason": f"Last checked {age_min} minutes ago ({sub_task.get('last_checked')})",
            })

    return stale


def _send_alert(alerts):
    """Send watchdog alerts to the head supervisor's mailbox."""
    if not alerts:
        return

    body_lines = ["[REMOTE-WATCHDOG] Health check report", ""]
    for alert in alerts:
        body_lines.append(f"- {alert}")
    body = "\n".join(body_lines)

    tools_dir = os.path.dirname(os.path.abspath(__file__))
    mailbox_send = os.path.join(tools_dir, "mailbox_send.py")

    result = subprocess.run(
        [
            "python3", mailbox_send,
            "--to", "supervisor",
            "--from", "remote-watchdog",
            "--subject", "Remote watchdog alert",
            "--body", body,
        ],
        capture_output=True,
        text=True,
    )
    if result.returncode != 0:
        print(f"Warning: failed to send watchdog alert via mailbox: "
              f"{result.stderr.strip() if result.stderr else 'exit ' + str(result.returncode)}",
              file=sys.stderr)


def main():
    parser = argparse.ArgumentParser(
        description="Remote watchdog — monitor subordinate health from the head."
    )
    parser.add_argument(
        "--stale-threshold", type=int, default=STALE_THRESHOLD_MINUTES,
        dest="stale_threshold",
        help=f"Minutes before a task is considered stale (default: {STALE_THRESHOLD_MINUTES})",
    )

    args = parser.parse_args()

    cluster = load_cluster()
    if cluster is None or cluster.get("role") != "head":
        # Not a head machine — nothing to do
        sys.exit(0)

    head_state = load_state()
    subordinates = get_subordinates()

    if not subordinates:
        sys.exit(0)

    # Only check subordinates that have active remote-worker tasks
    active_hosts = set()
    for task in head_state.get("tasks", []):
        if task.get("state") != "active" or task.get("type") != "remote-worker":
            continue
        remote = task.get("remote", {})
        host = remote.get("host")
        if host:
            active_hosts.add(host)

    if not active_hosts:
        sys.exit(0)

    alerts = []

    for sub in subordinates:
        sub_id = sub.get("id")
        if sub_id not in active_hosts:
            continue

        hostname = sub.get("hostname", sub_id)

        # Check tmux session
        tmux_ok = _check_tmux(hostname)
        if tmux_ok is None:
            alerts.append(
                f"{sub_id} ({hostname}): UNREACHABLE — SSH connection failed"
            )
            continue
        elif not tmux_ok:
            alerts.append(
                f"{sub_id} ({hostname}): mecha tmux session not running — "
                "subordinate supervisor may be down"
            )

        # Get remote state and check for stale tasks
        remote_state = _get_remote_state(hostname)
        if remote_state is None:
            alerts.append(
                f"{sub_id} ({hostname}): could not read subordinate state"
            )
            continue

        stale = _find_stale_tasks(
            remote_state, head_state, sub_id, args.stale_threshold
        )
        for s in stale:
            head_alias = s.get("head_alias", "?")
            reason = s.get("reason", "unknown")
            alerts.append(f"{sub_id}: task {head_alias} — {reason}")

    if alerts:
        print(f"Remote watchdog found {len(alerts)} issue(s):")
        for a in alerts:
            print(f"  - {a}")
        _send_alert(alerts)
    else:
        print("Remote watchdog: all subordinates healthy.")


if __name__ == "__main__":
    main()
