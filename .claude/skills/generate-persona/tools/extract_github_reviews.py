#!/usr/bin/env python3
"""Normalize GitHub PR review comments and issue comments into persona-analysis chunks.

Fetches from two endpoints:
- repos/{repo}/pulls/comments — inline PR review comments
- repos/{repo}/issues/comments — general issue/PR-level comments

Note: The pulls/{N}/reviews endpoint is blocked by hook policies in some
environments. The two endpoints above capture the bulk of review signal.
"""

from __future__ import annotations

import argparse
import json
import subprocess
import sys
from datetime import datetime, timedelta, timezone

from _common import emit_record


def iso_cutoff(months: int) -> str:
    # approximate: 30 days per month
    cutoff = datetime.now(timezone.utc) - timedelta(days=30 * months)
    return cutoff.date().isoformat()


def _fetch_paginated(repo: str, endpoint: str) -> list[dict]:
    """Fetch paginated comments from a GitHub API endpoint.

    Args:
        repo: owner/repo string
        endpoint: API path under repos/{repo}/ (e.g. "pulls/comments" or "issues/comments")
    """
    cmd = [
        "gh",
        "api",
        f"repos/{repo}/{endpoint}",
        "--paginate",
        "--slurp",
    ]
    result = subprocess.run(cmd, capture_output=True, text=True)
    if result.returncode != 0:
        print(result.stderr.strip() or f"gh api ({endpoint}) failed", file=sys.stderr)
        raise SystemExit(result.returncode)

    pages = json.loads(result.stdout)
    comments: list[dict] = []
    for page in pages:
        if isinstance(page, list):
            comments.extend(comment for comment in page if isinstance(comment, dict))
    return comments


def _process_comments(
    comments: list[dict],
    username: str,
    repo: str,
    cutoff: str,
    url_field: str,
    number_key: str,
    include_path: bool,
) -> int:
    """Filter and emit records from a list of GitHub comments.

    Args:
        comments: Raw comment dicts from the GitHub API.
        username: GitHub username to filter by.
        repo: owner/repo string for metadata.
        cutoff: ISO date string; comments before this are skipped.
        url_field: Key in the comment dict containing the parent URL
                   (e.g. "pull_request_url" or "issue_url").
        number_key: Metadata key for the extracted number
                    (e.g. "pr_number" or "issue_number").
        include_path: Whether to include file path in text and metadata
                      (True for inline PR comments, False for issue comments).
    """
    emitted = 0
    for comment in comments:
        login = ((comment.get("user") or {}).get("login") or "").lower()
        created_at = comment.get("created_at") or ""
        if login != username.lower():
            continue
        if created_at[:10] and created_at[:10] < cutoff:
            continue

        body = (comment.get("body") or "").strip()
        if not body:
            continue

        parent_url = comment.get(url_field) or ""
        number = parent_url.rstrip("/").split("/")[-1] if parent_url else ""

        label = "PR" if include_path else "Issue/PR"
        text_parts = [f"{label}: {number}"]
        if include_path:
            text_parts.append(f"Path: {comment.get('path') or ''}")
        text_parts.append(f"Comment: {body}")
        text = "\n".join(text_parts)

        context: dict = {
            "repo": repo,
            "username": username,
            number_key: number,
            "html_url": comment.get("html_url"),
        }
        if include_path:
            context["path"] = comment.get("path") or ""

        emit_record(
            text,
            {
                "source_type": "github-pr-reviews",
                "source": f"{username}@{repo}",
                "date": created_at,
                "context": context,
            },
        )
        emitted += 1

    return emitted


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("username", help="GitHub username")
    parser.add_argument("repo", help="owner/repo")
    parser.add_argument("--months", type=int, default=18)
    args = parser.parse_args()

    cutoff = iso_cutoff(args.months)
    emitted = 0

    # Inline PR review comments
    pr_comments = _fetch_paginated(args.repo, "pulls/comments")
    emitted += _process_comments(
        pr_comments, args.username, args.repo, cutoff,
        url_field="pull_request_url", number_key="pr_number", include_path=True,
    )

    # Issue-level comments (general PR/issue feedback)
    issue_comments = _fetch_paginated(args.repo, "issues/comments")
    emitted += _process_comments(
        issue_comments, args.username, args.repo, cutoff,
        url_field="issue_url", number_key="issue_number", include_path=False,
    )

    if emitted == 0:
        print("Error: no matching review comments found", file=sys.stderr)
        return 1

    return 0


if __name__ == "__main__":
    sys.exit(main())
