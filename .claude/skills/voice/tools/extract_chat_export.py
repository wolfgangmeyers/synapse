#!/usr/bin/env python3
"""Extract authored text from chat export files into voice-analysis chunks."""

from __future__ import annotations

import argparse
import json
import sys
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

from _common import chunk_text, emit_record, iter_files

TEXT_FIELDS = ("text", "content", "message", "body")
AUTHOR_FIELDS = ("user", "username", "sender_name", "author")
TIMESTAMP_FIELDS = ("ts", "timestamp", "timestamp_ms", "created_at")


def normalize_text(value: str) -> str:
    return " ".join(value.replace("\u00a0", " ").split())


def parse_timestamp(value: Any) -> str | None:
    if value in (None, ""):
        return None
    if isinstance(value, str):
        try:
            if value.isdigit():
                value = int(value)
            else:
                return value
        except ValueError:
            return value
    if not isinstance(value, (int, float)):
        return None
    if value > 10_000_000_000:
        value = value / 1000.0
    return datetime.fromtimestamp(value, tz=timezone.utc).isoformat().replace("+00:00", "Z")


def author_matches(author: str | None, author_names: set[str]) -> bool:
    if not author_names or not author:
        return True
    author_casefold = author.casefold()
    return any(author_casefold == candidate for candidate in author_names)


def load_json(path: Path) -> Any:
    return json.loads(path.read_text(errors="ignore"))


def emit_chat_record(
    text: str,
    path: Path,
    field: str,
    author: str | None,
    timestamp: str | None,
    index: int,
    max_chars: int,
) -> int:
    text = normalize_text(text)
    if len(text) < 8 or not any(char.isalpha() for char in text):
        return 0

    emitted = 0
    for chunk_index, chunk in enumerate(chunk_text(text, max_chars=max_chars), start=1):
        emit_record(
            chunk,
            {
                "source_type": "chat-export",
                "source": str(path),
                "date": timestamp,
                "context": {
                    "author": author,
                    "field": field,
                    "record_index": index,
                    "chunk_index": chunk_index,
                },
            },
        )
        emitted += 1
    return emitted


def extract_file(path: Path, author_names: set[str], max_chars: int) -> int:
    try:
        payload = load_json(path)
    except json.JSONDecodeError as exc:
        print(f"Error: failed to parse {path}: {exc}", file=sys.stderr)
        return 0

    if not isinstance(payload, list):
        payload = payload.get("messages") if isinstance(payload, dict) else None

    if not isinstance(payload, list):
        return 0

    emitted = 0
    for index, item in enumerate(payload):
        if not isinstance(item, dict):
            continue
        author = None
        for field in AUTHOR_FIELDS:
            value = item.get(field)
            if isinstance(value, str) and value.strip():
                author = value.strip()
                break
        if not author_matches(author, author_names):
            continue

        timestamp = None
        for field in TIMESTAMP_FIELDS:
            timestamp = parse_timestamp(item.get(field))
            if timestamp:
                break

        for field in TEXT_FIELDS:
            value = item.get(field)
            if isinstance(value, str):
                emitted += emit_chat_record(
                    value, path, field, author, timestamp, index, max_chars
                )
                break
    return emitted


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("paths", nargs="+", help="Chat export files or directories")
    parser.add_argument(
        "--author-name",
        action="append",
        default=[],
        help="Only include records from this author name. May be repeated.",
    )
    parser.add_argument("--max-chars", type=int, default=4000)
    args = parser.parse_args()

    try:
        files = list(iter_files(args.paths, {".json"}))
    except FileNotFoundError as exc:
        print(f"Error: {exc}", file=sys.stderr)
        return 1

    if not files:
        print("Error: no chat export JSON files found", file=sys.stderr)
        return 1

    author_names = {value.casefold() for value in args.author_name if value.strip()}
    emitted = 0
    for path in files:
        emitted += extract_file(path, author_names, args.max_chars)

    if emitted == 0:
        print("Error: no chat messages matched the provided filters", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())

