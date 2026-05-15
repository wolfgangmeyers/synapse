#!/usr/bin/env python3
"""List active tasks with their windows for supervisor to check.

This is a helper tool that provides structured task data for the supervisor
to evaluate with parallel subagents. The supervisor dispatches one subagent
per active worker to intelligently determine if it's blocked.

Usage:
  python state_check_blocked.py

Output: JSON array of tasks to check, each with:
  - id: Task ID
  - alias: Task alias
  - window: Tmux window name
  - last_summary: Last known status
  - already_blocked: Boolean indicating if manually blocked

The supervisor should:
1. Run this tool to get the list
2. Launch parallel subagents (one per task) to capture and analyze each worker
3. Each subagent determines: blocked (yes/no) and reason
4. Supervisor aggregates results and reports/takes action
"""

import json
import sys
import os

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from _state import load_state


def main():
    state = load_state()
    tasks = state.get("tasks", [])

    # Get all active tasks that have windows (i.e., running workers)
    active_workers = []
    for task in tasks:
        if task.get("state") == "active" and task.get("window"):
            active_workers.append({
                "id": task.get("id"),
                "alias": task.get("alias"),
                "window": task.get("window"),
                "last_summary": task.get("last_summary"),
                "already_blocked": bool(task.get("blocked")),
                "blocked_reason": task.get("blocked_reason") if task.get("blocked") else None,
            })

    # Output as JSON for easy parsing
    print(json.dumps(active_workers, indent=2))


if __name__ == "__main__":
    main()
