#!/usr/bin/env python3
"""Relaunch all active workers from supervisor state.

Usage:
  python worker_relaunch_all.py [--dry-run]

Reads supervisor state, kills existing worker windows, and relaunches them
with each task's recorded runtime, Pi session semantics, and task descriptions.
"""

import argparse
import os
import subprocess
import sys
import time

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from _state import load_state


def get_worker_summary(task):
    """Generate a brief summary of worker status from state."""
    summary = task.get("last_summary", "")
    if not summary:
        summary = task.get("description", "No description")
    return summary


def main():
    parser = argparse.ArgumentParser(description="Relaunch all active workers")
    parser.add_argument("--dry-run", action="store_true", help="Show what would be done without doing it")
    args = parser.parse_args()

    state = load_state()
    tasks = state.get("tasks", [])

    # Recovery is a managed Pi lifecycle operation. Never type recovery
    # instructions into a terminal, and never guess a resumable session.
    active_workers = [
        task for task in tasks
        if task.get("state") == "active" and task.get("type") == "worker"
    ]
    skipped_workers = [
        task for task in active_workers
        if task.get("agent") != "pi" or not task.get("session")
    ]
    workers = [task for task in active_workers if task not in skipped_workers]

    for task in skipped_workers:
        print(
            f"⚠️  {task.get('alias', '?')}: managed recovery requires a Pi worker "
            "with a recorded session path or ID; leaving its window running"
        )

    if not workers:
        if not skipped_workers:
            print("No active workers to relaunch.")
        return

    print(f"Found {len(workers)} active worker(s) to relaunch:")
    for task in workers:
        window = task.get("window", "?")
        alias = task.get("alias", "?")
        project = task.get("project", "?")
        print(f"  - {alias} ({window}) in {project}")
    print()

    if args.dry_run:
        print("Dry run - no changes made.")
        return

    # Kill existing windows
    print("Killing existing worker windows...")
    for task in workers:
        window = task.get("window")
        if not window:
            continue

        try:
            subprocess.run(
                ["tmux", "kill-window", "-t", f"mecha:{window}"],
                capture_output=True,
                text=True,
                check=True
            )
            print(f"  ✓ Killed {window}")
        except subprocess.CalledProcessError:
            print(f"  - {window} (not running)")

    print()
    print("Relaunching workers...")
    time.sleep(2)

    # Relaunch each worker
    for task in workers:
        window = task.get("window")
        alias = task.get("alias")
        worktree = task.get("worktree")
        project = task.get("project")

        # Determine working directory (prefer worktree, fallback to project)
        work_dir = worktree if worktree else project
        if not work_dir:
            print(f"  ✗ Skipping {alias}: no worktree or project path")
            continue

        work_dir = os.path.expanduser(work_dir)
        if not os.path.isdir(work_dir):
            print(f"  ✗ Skipping {alias}: directory not found: {work_dir}")
            continue

        print(f"  Launching {window} in {work_dir}...")

        agent = "pi"
        launch_cmd = [
            "python3",
            os.path.join(os.path.dirname(__file__), "worker_launch.py"),
            "--window", window,
            "--dir", work_dir,
            "--agent", agent,
        ]
        if agent == "pi" and task.get("session"):
            launch_cmd.extend(["--resume", task["session"]])

        result = subprocess.run(launch_cmd, capture_output=True, text=True)

        if result.returncode != 0:
            print(f"    ✗ Failed to launch: {result.stderr.strip()}")
            continue

        print(f"    ✓ Launched")

        # Wait for the worker runtime to start.
        time.sleep(4)

        # Build task description
        desc = task.get("description", "Continue your work")
        summary = get_worker_summary(task)

        task_text = f"""You are a Pi worker. Rules:
1. Work directly in focused stages; do not use subagents.
2. Maintain your todo list constantly.
3. Reference ~/personas/wolfgang.md for decisions.

Your task: {desc}

Current status: {summary}

Continue where you left off based on git status and recent commits."""

        # Mailbox V2 is the only coordination path after lifecycle recovery.
        send_cmd = [
            "python3",
            os.path.join(os.path.dirname(__file__), "mailbox_send.py"),
            "--from", "supervisor",
            "--to", window,
            "--body", task_text,
        ]
        send_result = subprocess.run(send_cmd, capture_output=True, text=True)

        if send_result.returncode != 0:
            print(f"    ✗ Mailbox delivery failed: {send_result.stderr.strip()}")
        else:
            print("    ✓ Sent Mailbox V2 instruction")

        time.sleep(1)

    print()
    print(f"✓ Relaunched {len(workers)} worker(s)")
    print()
    print("Verify workers started:")
    print("  python3 worker_list.py")


if __name__ == "__main__":
    main()
