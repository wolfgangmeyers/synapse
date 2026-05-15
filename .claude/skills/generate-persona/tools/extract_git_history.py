#!/usr/bin/env python3
"""Normalize git history for one author into persona-analysis chunks."""

from __future__ import annotations

import argparse
import subprocess
import sys

from _common import chunk_text, emit_record

FIELD_SEP = "\x1f"


def list_commit_hashes(repo: str, author: str, max_count: int | None) -> list[str]:
    cmd = [
        "git",
        "-C",
        repo,
        "rev-list",
        "HEAD",
        "--author",
        author,
    ]
    if max_count is not None:
        cmd.append(f"--max-count={max_count}")
    result = subprocess.run(cmd, capture_output=True, text=True)
    if result.returncode != 0:
        print(result.stderr.strip() or "git rev-list failed", file=sys.stderr)
        raise SystemExit(result.returncode)
    return [line.strip() for line in result.stdout.splitlines() if line.strip()]


def git_metadata(repo: str, commit_hash: str) -> tuple[str, str, str]:
    fmt = f"%aI{FIELD_SEP}%s{FIELD_SEP}%b"
    cmd = [
        "git",
        "-C",
        repo,
        "show",
        "--no-patch",
        f"--format={fmt}",
        commit_hash,
    ]
    result = subprocess.run(cmd, capture_output=True, text=True)
    if result.returncode != 0:
        print(result.stderr.strip() or "git show metadata failed", file=sys.stderr)
        raise SystemExit(result.returncode)

    authored_at, subject, body = result.stdout.rstrip("\n").split(FIELD_SEP, 2)
    return authored_at, subject, body


def git_patch(repo: str, commit_hash: str) -> str:
    cmd = [
        "git",
        "-C",
        repo,
        "show",
        "--format=",
        "--patch",
        "--no-ext-diff",
        commit_hash,
    ]
    result = subprocess.run(cmd, capture_output=True, text=True)
    if result.returncode != 0:
        print(result.stderr.strip() or "git show failed", file=sys.stderr)
        raise SystemExit(result.returncode)
    return result.stdout


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--author", required=True, help="Author name or email pattern")
    parser.add_argument("--repo", default=".", help="Repository path")
    parser.add_argument("--max-count", type=int, default=50)
    parser.add_argument("--max-chars", type=int, default=4000)
    args = parser.parse_args()

    commit_hashes = list_commit_hashes(args.repo, args.author, args.max_count)
    emitted = 0
    for commit_hash in commit_hashes:
        authored_at, subject, body = git_metadata(args.repo, commit_hash)
        patch = git_patch(args.repo, commit_hash).strip()
        text = "\n\n".join(
            part for part in [f"Subject: {subject}", body.strip(), patch.strip()] if part
        )
        for index, chunk in enumerate(chunk_text(text, max_chars=args.max_chars), start=1):
            emit_record(
                chunk,
                {
                    "source_type": "git-history",
                    "source": args.repo,
                    "date": authored_at,
                    "context": {
                        "repo": args.repo,
                        "author": args.author,
                        "commit": commit_hash,
                        "chunk_index": index,
                        "subject": subject,
                    },
                },
            )
            emitted += 1

    if emitted == 0:
        print("Error: no git history chunks emitted", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
