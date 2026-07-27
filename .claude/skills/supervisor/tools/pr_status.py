#!/usr/bin/env python3
"""Compact GitHub PR head/check status for one or more PRs.

Examples:
  python3 pr_status.py https://github.com/org/repo/pull/123
  python3 pr_status.py --repo org/repo 123 124 --json
"""

from __future__ import annotations

import argparse
import json
import re
import subprocess
import sys
from dataclasses import dataclass
from typing import Any

PR_URL = re.compile(r"github\.com/(?P<owner>[^/]+)/(?P<repo>[^/]+)/pull/(?P<number>\d+)")
FIELDS = "number,url,title,state,isDraft,headRefName,headRefOid,baseRefName,mergeStateStatus,reviewDecision"


@dataclass(frozen=True)
class PullRequest:
    selector: str
    repo: str | None = None


def parse_pr(selector: str, repo: str | None) -> PullRequest:
    match = PR_URL.search(selector)
    if match:
        return PullRequest(selector=match.group("number"), repo=f"{match.group('owner')}/{match.group('repo')}")
    return PullRequest(selector=selector, repo=repo)


def run_gh(args: list[str]) -> subprocess.CompletedProcess[str]:
    return subprocess.run(["gh", *args], check=False, capture_output=True, text=True)


def gh_json(args: list[str], *, runner=run_gh) -> Any:
    result = runner(args)
    if result.returncode != 0:
        raise RuntimeError((result.stderr or result.stdout or "gh command failed").strip())
    try:
        return json.loads(result.stdout or "null")
    except json.JSONDecodeError as error:
        raise RuntimeError(f"gh returned invalid JSON: {error}") from error


def check_rollup(checks: list[dict[str, Any]]) -> dict[str, Any]:
    counts: dict[str, int] = {}
    failing: list[str] = []
    pending: list[str] = []
    for check in checks:
        state = str(check.get("state") or check.get("conclusion") or "UNKNOWN").upper()
        counts[state] = counts.get(state, 0) + 1
        name = str(check.get("name") or check.get("workflow") or "unnamed")
        if state in {"FAILURE", "FAILED", "ERROR", "CANCELLED", "TIMED_OUT", "ACTION_REQUIRED"}:
            failing.append(name)
        elif state not in {"SUCCESS", "PASSED", "SKIPPED", "NEUTRAL"}:
            pending.append(name)
    if failing:
        overall = "failing"
    elif pending:
        overall = "pending"
    elif checks:
        overall = "successful"
    else:
        overall = "none"
    return {"overall": overall, "counts": counts, "failing": failing, "pending": pending}


def pr_status(pr: PullRequest, *, runner=run_gh) -> dict[str, Any]:
    view_args = ["pr", "view", pr.selector, "--json", FIELDS]
    check_args = ["pr", "checks", pr.selector, "--json", "name,state,workflow,link,bucket"]
    if pr.repo:
        view_args += ["--repo", pr.repo]
        check_args += ["--repo", pr.repo]
    view = gh_json(view_args, runner=runner)
    checks = gh_json(check_args, runner=runner)
    rollup = check_rollup(checks if isinstance(checks, list) else [])
    return {
        "repo": pr.repo,
        "number": view.get("number"),
        "url": view.get("url"),
        "title": view.get("title"),
        "state": view.get("state"),
        "draft": view.get("isDraft"),
        "base": view.get("baseRefName"),
        "head": view.get("headRefName"),
        "head_sha": view.get("headRefOid"),
        "merge_state": view.get("mergeStateStatus"),
        "review_decision": view.get("reviewDecision"),
        "checks": rollup,
    }


def format_text(items: list[dict[str, Any]]) -> str:
    lines: list[str] = []
    for item in items:
        sha = (item.get("head_sha") or "")[:8]
        checks = item.get("checks", {})
        flags = []
        if item.get("draft"):
            flags.append("draft")
        if item.get("review_decision"):
            flags.append(f"review={item['review_decision']}")
        if item.get("merge_state"):
            flags.append(f"merge={item['merge_state']}")
        prefix = f"PR #{item.get('number')} {item.get('state')} {item.get('head')}@{sha}"
        lines.append(" | ".join(part for part in [prefix, ", ".join(flags), f"checks={checks.get('overall')} {checks.get('counts', {})}"] if part))
        if checks.get("failing"):
            lines.append("  failing: " + ", ".join(checks["failing"][:8]))
        if checks.get("pending"):
            lines.append("  pending: " + ", ".join(checks["pending"][:8]))
    return "\n".join(lines)


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("prs", nargs="+", help="PR URL, number, or gh-compatible selector")
    parser.add_argument("--repo", help="owner/repo for numeric PRs")
    parser.add_argument("--json", action="store_true", help="Emit machine-readable JSON")
    args = parser.parse_args(argv)

    try:
        items = [pr_status(parse_pr(selector, args.repo)) for selector in args.prs]
    except RuntimeError as error:
        print(f"Error: {error}", file=sys.stderr)
        return 1
    if args.json:
        print(json.dumps(items, indent=2, sort_keys=True))
    else:
        print(format_text(items))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
