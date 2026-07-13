#!/usr/bin/env python3
"""Recover all active workers after tmux crash.

Reads supervisor state and relaunches active workers with their recorded runtime.
Pi workers resume through their recorded Pi session path or ID.

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
    agent = task.get("agent") or "codex"
    session = task.get("session")

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

    if agent != "pi":
        print(
            f"⚠️  {alias}: legacy runtime recovery is unsupported; restore it through "
            "the managed Pi launcher."
        )
        return False

    if not session:
        print(f"⚠️  {alias}: Pi recovery requires a recorded session path or ID, skipping")
        return False

    if dry_run:
        resume_detail = f" --resume {session}" if agent == "pi" else ""
        print(
            f"   [DRY RUN] Would launch: --window {window} --dir {work_dir} "
            f"--agent {agent}{resume_detail}"
        )
        return True

    # Preserve legacy runtime branches. Pi maps --resume to its --session flag
    # inside worker_launch.py and therefore reconnects without a TUI picker.
    launch_cmd = [
        "python3",
        os.path.join(os.path.dirname(__file__), "worker_launch.py"),
        "--window", window,
        "--dir", work_dir,
        "--agent", agent,
        "--skip-permissions",
    ]
    if agent == "pi":
        launch_cmd.extend(["--resume", session])

    try:
        subprocess.run(launch_cmd, capture_output=True, text=True, check=True)
    except subprocess.CalledProcessError as e:
        print(f"❌ {alias}: Failed to launch window")
        print(f"   Error: {e.stderr}")
        return False

    print(f"✅ {alias}: Resumed Pi session {session}")
    return True


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
        print("1. Pi workers resume their recorded session; legacy workers may show a picker")
        print("2. Verify workers reconnected: python3 worker_list.py")
        print("3. Update supervisor state as needed")

    return 0 if failed == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
