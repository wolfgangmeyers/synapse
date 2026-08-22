#!/usr/bin/env python3
"""Relaunch all active workers from supervisor state.

Usage:
  python worker_relaunch_all.py [--dry-run]

Reads supervisor state, kills existing worker windows, and relaunches them
with each task's recorded runtime and session. For runtimes with verified
Mailbox delivery, it also sends the task description as a recovery handoff.
"""

import argparse
import os
import subprocess
import sys
import time

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from _sidecar import cleanup_sidecar
from _state import load_state
from _tmux_rest import TmuxRestClient


def window_exists(window):
    return TmuxRestClient().exists(window)


def kill_window(window):
    try:
        TmuxRestClient().remove(window)
    except Exception:
        pass
    return cleanup_sidecar(window)


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

    # Recovery is a managed worker lifecycle operation. Never type recovery
    # instructions into a terminal, and never guess a resumable session.
    active_workers = [
        task for task in tasks
        if task.get("state") == "active" and task.get("type") == "worker"
    ]
    skipped_workers = [
        task for task in active_workers
        if not task.get("session")
    ]
    workers = [task for task in active_workers if task not in skipped_workers]

    for task in skipped_workers:
        print(
            f"⚠️  {task.get('alias', '?')}: managed recovery requires "
            "a recorded session path or ID; leaving its window running"
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

    # Kill existing windows. kill_window also sweeps each window's mailbox sidecar —
    # critical here, not just tidy: this relaunches the same window names, so a
    # surviving Claude sidecar would keep that agent ID registered with the router
    # while a different agent owns the window, and mail for the live agent would be
    # handed to the stranger's sidecar.
    print("Killing existing worker windows...")
    leaked = []
    for task in workers:
        window = task.get("window")
        if not window:
            continue

        existed = window_exists(window)
        if not kill_window(window):
            leaked.append(window)
        print(f"  {'✓ Killed' if existed else '-'} {window}{'' if existed else ' (not running)'}")

    if leaked:
        print(
            "\nERROR: a mailbox sidecar survived for: " + ", ".join(leaked) + ".\n"
            "Relaunching these windows would hand their mail to the old sidecar. "
            "Resolve the warnings above, then re-run.",
            file=sys.stderr,
        )
        sys.exit(1)

    print()
    print("Relaunching workers...")
    time.sleep(2)
    failed = False

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
            failed = True
            continue

        work_dir = os.path.expanduser(work_dir)
        if not os.path.isdir(work_dir):
            print(f"  ✗ Skipping {alias}: directory not found: {work_dir}")
            failed = True
            continue

        print(f"  Launching {window} in {work_dir}...")

        agent = task.get("agent") or "pi"
        launch_cmd = [
            "python3",
            os.path.join(os.path.dirname(__file__), "worker_launch.py"),
            "--window", window,
            "--dir", work_dir,
            "--agent", agent,
        ]
        launch_cmd.extend(["--resume", task["session"]])
        if agent == "pi":
            launch_cmd.extend(["--notify-on-settled", "supervisor"])

        result = subprocess.run(launch_cmd, capture_output=True, text=True)

        if result.returncode != 0:
            failed = True
            detail = result.stderr.strip()
            print(
                f"    ✗ Failed to launch (exit {result.returncode})"
                + (f": {detail}" if detail else "; details suppressed")
            )
            continue

        print(f"    ✓ Launched")

        # Wait for the worker runtime to start.
        time.sleep(4)

        # Build task description
        desc = task.get("description", "Continue your work")
        summary = get_worker_summary(task)

        task_text = f"""You are a managed {agent} worker. Rules:
1. Read ~/.agents/skills/worker/SKILL.md and follow it, including its bounded subagent guidance.
2. Maintain your todo list constantly.
3. Reference ~/personas/wolfgang.md for decisions.

Your task: {desc}

Current status: {summary}

Continue where you left off based on git status and recent commits."""

        if agent in {"codex", "cursor"}:
            print(f"    ! {agent} has no launcher-provided Mailbox delivery bridge; verify a separate delivery path before sending follow-up instructions")
            print("    ! Check reminder coverage in state: supervisor_reminder_id and worker_self_reminder_ids where applicable")
            continue

        # Mailbox V2 is the coordination path after lifecycle recovery when the
        # runtime has a verified delivery bridge.
        send_cmd = [
            "python3",
            os.path.join(os.path.dirname(__file__), "mailbox_send.py"),
            "--from", "supervisor",
            "--to", window,
            "--body", task_text,
        ]
        send_result = subprocess.run(send_cmd, capture_output=True, text=True)

        if send_result.returncode != 0:
            failed = True
            detail = send_result.stderr.strip()
            print(
                f"    ✗ Mailbox delivery failed (exit {send_result.returncode})"
                + (f": {detail}" if detail else "; details suppressed")
            )
        else:
            print("    ✓ Sent Mailbox V2 instruction")
        print("    ! Check reminder coverage in state: supervisor_reminder_id and worker_self_reminder_ids")

        time.sleep(1)

    print()
    if failed:
        print(f"! Relaunch completed with failures for one or more of {len(workers)} worker(s).")
        return 1
    print(f"✓ Relaunched {len(workers)} worker(s)")
    print()
    print("Verify workers started:")
    print("  python3 worker_list.py")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
