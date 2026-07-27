#!/usr/bin/env python3
"""Read-only git status for a supervisor task/worktree/current directory."""

from __future__ import annotations

import argparse
import json
import os
import subprocess
import sys
from typing import Any

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from _state import find_task, load_state  # noqa: E402


def git(repo: str, args: list[str]) -> subprocess.CompletedProcess[str]:
    return subprocess.run(["git", "-C", repo, *args], check=False, capture_output=True, text=True)


def output_or_none(result: subprocess.CompletedProcess[str]) -> str | None:
    if result.returncode != 0:
        return None
    return result.stdout.strip()


def ahead_behind(repo: str, upstream: str | None, *, runner=git) -> dict[str, int | None]:
    if not upstream:
        return {"ahead": None, "behind": None}
    result = runner(repo, ["rev-list", "--left-right", "--count", f"{upstream}...HEAD"])
    text = output_or_none(result)
    if not text:
        return {"ahead": None, "behind": None}
    try:
        behind, ahead = [int(part) for part in text.split()]
    except ValueError:
        return {"ahead": None, "behind": None}
    return {"ahead": ahead, "behind": behind}


def git_status(repo: str, *, runner=git) -> dict[str, Any]:
    branch = output_or_none(runner(repo, ["branch", "--show-current"]))
    head = output_or_none(runner(repo, ["rev-parse", "--short", "HEAD"]))
    upstream = output_or_none(runner(repo, ["rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}"]))
    porcelain = output_or_none(runner(repo, ["status", "--porcelain=v1"])) or ""
    recent = output_or_none(runner(repo, ["log", "-1", "--pretty=format:%h %s"]))
    entries = [line for line in porcelain.splitlines() if line]
    return {
        "repo": repo,
        "branch": branch,
        "head": head,
        "upstream": upstream,
        **ahead_behind(repo, upstream, runner=runner),
        "dirty": any(not line.startswith("??") for line in entries),
        "untracked": sum(1 for line in entries if line.startswith("??")),
        "changed": len(entries),
        "recent": recent,
    }


def task_worktree(identifier: str) -> str | None:
    task = find_task(load_state(), int(identifier) if identifier.isdigit() else identifier)
    if task is None:
        return None
    return task.get("worktree") or task.get("project")


def format_text(items: list[dict[str, Any]]) -> str:
    lines = []
    for item in items:
        sync = ""
        if item.get("upstream"):
            sync = f" upstream={item['upstream']} ahead={item.get('ahead')} behind={item.get('behind')}"
        dirty = "dirty" if item.get("dirty") or item.get("untracked") else "clean"
        lines.append(f"{item['repo']}: {item.get('branch')}@{item.get('head')} {dirty} changed={item.get('changed')} untracked={item.get('untracked')}{sync}")
        if item.get("recent"):
            lines.append(f"  {item['recent']}")
    return "\n".join(lines)


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    source = parser.add_mutually_exclusive_group()
    source.add_argument("--task", action="append", help="Task alias or id from supervisor state")
    source.add_argument("--repo", action="append", help="Repository/worktree path")
    parser.add_argument("--json", action="store_true")
    args = parser.parse_args(argv)

    repos: list[str]
    if args.task:
        repos = []
        for task in args.task:
            path = task_worktree(task)
            if path is None:
                print(f"Error: task not found or has no worktree/project: {task}", file=sys.stderr)
                return 1
            repos.append(path)
    else:
        repos = args.repo or [os.getcwd()]

    items = [git_status(os.path.abspath(os.path.expanduser(repo))) for repo in repos]
    if args.json:
        print(json.dumps(items, indent=2, sort_keys=True))
    else:
        print(format_text(items))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
