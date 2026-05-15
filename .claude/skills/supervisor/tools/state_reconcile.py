#!/usr/bin/env python3
"""State reconciliation — agent-based worker evaluation.

Simple orchestration script. For each active worker with new tmux activity,
launches a Claude agent to intelligently evaluate the worker's state.

The agent does the hard work:
  - Reads terminal output and understands context
  - Writes an intelligent status summary to the journal
  - Can mark tasks blocked if it decides human intervention is needed

This script just handles timestamps, filtering, and launching agents.

Usage:
  python state_reconcile.py              # Run evaluation
  python state_reconcile.py --dry-run    # Show what would be evaluated
  python state_reconcile.py --threshold 120  # Custom staleness threshold
"""

import json
import os
import subprocess
import sys
from datetime import datetime

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import _state
from _state import parse_last_checked

TOOLS_DIR = os.path.dirname(os.path.abspath(__file__))
PROJECT_DIR = os.path.normpath(os.path.join(TOOLS_DIR, "..", "..", "..", ".."))

# Resolve claude CLI path — cron doesn't have nvm in PATH
CLAUDE_BIN = os.environ.get("CLAUDE_BIN") or "claude"
for candidate in [
    os.path.expanduser("~/.config/nvm/versions/node/v22.21.1/bin/claude"),
    os.path.expanduser("~/.local/bin/claude"),
    "/usr/local/bin/claude",
]:
    if os.path.isfile(candidate):
        CLAUDE_BIN = candidate
        break


def log(msg):
    """Log with timestamp to stdout (cron redirects to log file)."""
    ts = datetime.now().strftime("%Y-%m-%d %H:%M:%S")
    print(f"[{ts}] {msg}", flush=True)


def get_window_activity():
    """Get tmux window activity timestamps. Returns dict of window -> unix ts."""
    result = subprocess.run(
        [sys.executable, os.path.join(TOOLS_DIR, "worker_activity.py")],
        capture_output=True, text=True, check=True,
    )
    return json.loads(result.stdout)


def capture_terminal(window, lines=80):
    """Capture terminal output from a tmux window."""
    result = subprocess.run(
        [
            sys.executable,
            os.path.join(TOOLS_DIR, "worker_capture.py"),
            "--window", window,
            "--lines", str(lines),
        ],
        capture_output=True, text=True, check=True,
    )
    return result.stdout


def build_prompt(task, terminal_output):
    """Build the evaluation prompt for the Claude agent."""
    alias = task.get("alias", "unknown")
    project = task.get("project", "unknown")
    description = task.get("description", "")
    last_summary = task.get("last_summary", "")
    blocked = task.get("blocked")
    blocked_reason = task.get("blocked_reason", "")
    pr = task.get("pr", "")

    journal = task.get("journal", [])
    recent = journal[-5:] if isinstance(journal, list) else []
    journal_text = "\n".join(
        f"  [{e.get('timestamp', '?')}] {e.get('entry', '?')}"
        for e in recent
    ) or "  (none)"

    update_tool = os.path.join(TOOLS_DIR, "state_update_status.py")
    block_tool = os.path.join(TOOLS_DIR, "state_block.py")

    return f"""Evaluate worker '{alias}' and update its status.

TASK: {description}
PROJECT: {project}
PR: {pr or 'none'}
CURRENTLY BLOCKED: {blocked or 'no'} {blocked_reason}
LAST SUMMARY: {last_summary or '(none)'}

RECENT JOURNAL:
{journal_text}

CURRENT TERMINAL OUTPUT (last 80 lines):
---
{terminal_output}
---

YOUR JOB:
1. Read the terminal output and understand what the worker is doing
2. Write a concise 1-2 sentence status summary
3. Run: python3 {update_tool} {alias} "<your summary>"
4. If the worker clearly can't progress without a human (waiting for PR review, needs a decision, permission denied, stuck in a loop), block it:
   python3 {block_tool} {alias} --block "<reason>"
5. If the worker was blocked but has clearly resumed working, unblock it:
   python3 {block_tool} {alias} --unblock

Be concise. One status update call, optionally one block/unblock call. That's it."""


def evaluate_worker(task, terminal_output, dry_run=False):
    """Launch a Claude agent to evaluate a worker. Returns True on success."""
    alias = task.get("alias", "unknown")
    prompt = build_prompt(task, terminal_output)

    if dry_run:
        log(f"[dry-run] Would evaluate {alias} ({len(terminal_output)} chars of terminal)")
        return True

    try:
        result = subprocess.run(
            [
                CLAUDE_BIN, "-p", prompt,
                "--dangerously-skip-permissions",
                "--model", "haiku",
                "--max-budget-usd", "0.05",
                "--no-session-persistence",
            ],
            capture_output=True,
            text=True,
            timeout=60,
            cwd=PROJECT_DIR,
            env={**os.environ, "CLAUDECODE": ""},  # unset to avoid nesting check
        )
        if result.stdout.strip():
            # Log first 200 chars of agent response
            log(f"  {alias}: {result.stdout.strip()[:200]}")
        if result.returncode != 0 and result.stderr.strip():
            log(f"  {alias} stderr: {result.stderr.strip()[:200]}")
        return result.returncode == 0
    except subprocess.TimeoutExpired:
        log(f"  {alias}: agent timed out (60s)")
        return False
    except FileNotFoundError:
        log(f"  ERROR: claude CLI not found")
        return False


def main():
    import argparse

    parser = argparse.ArgumentParser(
        description="Agent-based state reconciliation"
    )
    parser.add_argument(
        "--threshold", type=int, default=60,
        help="Staleness threshold in seconds (default: 60)",
    )
    parser.add_argument(
        "--dry-run", action="store_true",
        help="Show what would be evaluated without launching agents",
    )
    args = parser.parse_args()

    log(f"Starting (threshold={args.threshold}, dry_run={args.dry_run})")

    # Load state
    state = _state.load_state()
    tasks = state.get("tasks", [])

    # Get window activity
    try:
        activity = get_window_activity()
    except Exception as e:
        log(f"Failed to get window activity: {e}")
        return

    # Find tasks needing evaluation
    evaluated = []
    skipped = []
    errors = []

    for task in tasks:
        if task.get("state") != "active":
            continue
        window = task.get("window")
        if not window or window not in activity:
            continue

        alias = task.get("alias", "unknown")
        last_checked_ts = parse_last_checked(task.get("last_checked"))

        if activity[window] <= last_checked_ts + args.threshold:
            skipped.append(alias)
            continue

        # Capture terminal
        try:
            terminal_output = capture_terminal(window)
        except Exception as e:
            log(f"Failed to capture {alias}: {e}")
            errors.append(alias)
            continue

        # Launch agent to evaluate
        log(f"Evaluating {alias}...")
        success = evaluate_worker(task, terminal_output, dry_run=args.dry_run)

        if success:
            evaluated.append(alias)
        else:
            errors.append(alias)

    # Summary
    if evaluated:
        log(f"Evaluated: {', '.join(evaluated)}")
    if skipped:
        log(f"Skipped (no new activity): {', '.join(skipped)}")
    if errors:
        log(f"Errors: {', '.join(errors)}")
    if not evaluated and not errors:
        log("Nothing to evaluate.")


if __name__ == "__main__":
    main()
