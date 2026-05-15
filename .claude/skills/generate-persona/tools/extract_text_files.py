#!/usr/bin/env python3
"""Normalize plain text files into persona-analysis chunks."""

from __future__ import annotations

import argparse
import sys

from _common import chunk_text, emit_record, iter_files


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("paths", nargs="+", help="Files or directories to scan")
    parser.add_argument("--max-chars", type=int, default=4000)
    args = parser.parse_args()

    emitted = 0
    try:
        files = list(iter_files(args.paths, {".txt", ".md", ".rst", ".log"}))
    except FileNotFoundError as exc:
        print(f"Error: {exc}", file=sys.stderr)
        return 1

    if not files:
        print("Error: no matching text files found", file=sys.stderr)
        return 1

    for path in files:
        text = path.read_text(errors="ignore")
        for index, chunk in enumerate(chunk_text(text, max_chars=args.max_chars), start=1):
            emit_record(
                chunk,
                {
                    "source_type": "text-files",
                    "source": str(path),
                    "context": {
                        "path": str(path),
                        "chunk_index": index,
                    },
                },
            )
            emitted += 1

    return 0 if emitted else 1


if __name__ == "__main__":
    sys.exit(main())
