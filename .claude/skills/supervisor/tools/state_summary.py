#!/usr/bin/env python3
"""Read-only compact supervisor state summary for active/dangling work."""

from __future__ import annotations

import argparse
import json
import os
import sys
from typing import Any

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from _state import load_state  # noqa: E402
from _tmux_rest import TmuxRestClient  # noqa: E402

NONTERMINAL = {"active", "blocked", "parked"}


def live_window_names() -> set[str]:
    return {window.get("name") for window in TmuxRestClient().list() if isinstance(window, dict) and isinstance(window.get("name"), str)}


def summarize(state: dict[str, Any], windows: set[str]) -> dict[str, Any]:
    tasks = state.get("tasks", [])
    items: list[dict[str, Any]] = []
    counts: dict[str, int] = {}
    dangling: list[dict[str, Any]] = []
    for task in tasks:
        status = "blocked" if task.get("blocked") else task.get("state", "active")
        counts[status] = counts.get(status, 0) + 1
        if status not in NONTERMINAL:
            continue
        window = task.get("window")
        window_live = bool(window and window in windows)
        item = {
            "id": task.get("id"),
            "alias": task.get("alias"),
            "state": status,
            "window": window,
            "window_live": window_live,
            "pr": task.get("pr"),
            "worktree": task.get("worktree"),
            "branch": task.get("branch"),
            "summary": task.get("last_summary"),
            "blocked_reason": task.get("blocked_reason"),
            "verification_handoff": task.get("verification_handoff"),
        }
        items.append(item)
        if not window_live and status in {"active", "blocked"}:
            dangling.append(item)
    priorities = state.get("priorities", [])
    return {"counts": counts, "priorities": priorities, "tasks": items, "dangling": dangling}


def format_text(summary: dict[str, Any], *, show_all: bool) -> str:
    lines = ["# Compact supervisor state", f"counts: {summary['counts']}"]
    priorities = [p for p in summary.get("priorities", []) if p.get("status") in {"active", "blocked"}]
    if priorities:
        lines.append("\n## Active priorities")
        for priority in priorities:
            lines.append(f"- {priority.get('id')}: {priority.get('status')} — {priority.get('name')} tasks={priority.get('task_ids', [])}")
    if summary.get("dangling"):
        lines.append("\n## Active/blocked records without a live window")
        for task in summary["dangling"]:
            lines.append(f"- task-{task['id']} {task['alias']} ({task['state']}): window={task.get('window')!r} pr={task.get('pr') or '-'}")
    lines.append("\n## Nonterminal tasks")
    for task in summary.get("tasks", []):
        if not show_all and task.get("state") == "parked":
            continue
        live = "live" if task.get("window_live") else "no-window"
        pr = f" pr={task['pr']}" if task.get("pr") else ""
        lines.append(f"- task-{task['id']} {task['alias']} [{task['state']}/{live}]{pr}")
        reason = task.get("blocked_reason") or task.get("summary")
        if reason:
            one_line = " ".join(str(reason).split())
            lines.append(f"  {one_line[:240]}")
        handoff = task.get("verification_handoff")
        if isinstance(handoff, dict):
            lines.append(f"  handoff: reminder={handoff.get('reminder_id')} successor={handoff.get('successor')}")
        elif handoff:
            lines.append(f"  handoff: {str(handoff)[:180]}")
    return "\n".join(lines)


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--json", action="store_true")
    parser.add_argument("--all", action="store_true", help="Include parked tasks in text output")
    args = parser.parse_args(argv)
    summary = summarize(load_state(), live_window_names())
    if args.json:
        print(json.dumps(summary, indent=2, sort_keys=True))
    else:
        print(format_text(summary, show_all=args.all))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
