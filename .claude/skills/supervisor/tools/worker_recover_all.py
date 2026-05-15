#!/usr/bin/env python3
"""Recover all active workers after tmux crash.

Reads supervisor state and relaunches all active workers with --resume,
attempting to reconnect them to their previous sessions.

Usage:
  python worker_recover_all.py [--dry-run]
"""

import argparse
import sys
import os
import subprocess
import time

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from _state import load_state


def recover_worker(task, dry_run=False):
    """Attempt to recover a single worker."""
    alias = task.get("alias")
    window = task.get("window")
    project = task.get("project")
    worktree = task.get("worktree")

    if not window or not project:
        print(f"⚠️  {alias}: Missing window or project, skipping")
        return False

    # Use worktree if available, otherwise project dir
    work_dir = worktree if worktree else project
    work_dir = os.path.expanduser(work_dir)

    if not os.path.isdir(work_dir):
        print(f"⚠️  {alias}: Directory not found: {work_dir}, skipping")
        return False

    print(f"🔄 {alias}: Recovering in {window} at {work_dir}")

    if dry_run:
        print(f"   [DRY RUN] Would launch: --window {window} --dir {work_dir} --resume")
        return True

    # Launch worker without --resume first
    launch_cmd = [
        "python3",
        os.path.join(os.path.dirname(__file__), "worker_launch.py"),
        "--window", window,
        "--dir", work_dir,
        "--skip-permissions"
    ]

    try:
        subprocess.run(launch_cmd, capture_output=True, text=True, check=True)
    except subprocess.CalledProcessError as e:
        print(f"❌ {alias}: Failed to launch window")
        print(f"   Error: {e.stderr}")
        return False

    # Wait for Claude to start
    time.sleep(3)

    # Send interactive resume command
    send_cmd = [
        "python3",
        os.path.join(os.path.dirname(__file__), "worker_send.py"),
        "--window", window,
        "--text", "claude --resume"
    ]

    try:
        subprocess.run(send_cmd, capture_output=True, text=True, check=True)
        print(f"✅ {alias}: Launched with resume picker")
        return True
    except subprocess.CalledProcessError as e:
        print(f"⚠️  {alias}: Launched but resume command failed")
        print(f"   Error: {e.stderr}")
        return True  # Window exists, that's the important part


def main():
    parser = argparse.ArgumentParser(
        description="Recover all active workers after tmux crash"
    )
    parser.add_argument("--dry-run", action="store_true",
                       help="Show what would be recovered without doing it")
    args = parser.parse_args()

    state = load_state()
    tasks = state.get("tasks", [])

    # Get all active tasks with windows
    active_workers = [
        t for t in tasks
        if t.get("state") == "active" and t.get("window")
    ]

    if not active_workers:
        print("No active workers to recover")
        return 0

    print(f"Found {len(active_workers)} active worker(s) to recover")
    print()

    recovered = 0
    failed = 0

    for task in active_workers:
        if recover_worker(task, dry_run=args.dry_run):
            recovered += 1
        else:
            failed += 1
        print()

        # Small delay between launches to avoid overwhelming the system
        if not args.dry_run:
            time.sleep(2)

    print("=" * 60)
    print(f"Recovery complete: {recovered} succeeded, {failed} failed")

    if not args.dry_run and recovered > 0:
        print()
        print("Next steps:")
        print("1. Each worker will show a session picker - select the most recent session")
        print("2. Verify workers reconnected: python3 worker_list.py")
        print("3. Update supervisor state as needed")

    return 0 if failed == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
