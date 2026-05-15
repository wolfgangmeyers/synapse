#!/usr/bin/env python3
"""Extract authored text from Facebook JSON exports into voice-analysis chunks."""

from __future__ import annotations

import argparse
import json
import sys
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

from _common import chunk_text, emit_record, iter_files

TEXT_FIELDS = {
    "body",
    "caption",
    "comment",
    "content",
    "description",
    "message",
    "post",
    "text",
}
AUTHOR_FIELDS = ("sender_name", "author", "name", "owner", "actor")
TIMESTAMP_FIELDS = ("timestamp_ms", "timestamp", "created_at", "creation_timestamp")


def _fb_decode(text: str) -> str:
    """Decode Facebook's mojibake encoding (latin-1 bytes stored as unicode escapes)."""
    try:
        return text.encode("latin-1").decode("utf-8")
    except (UnicodeDecodeError, UnicodeEncodeError):
        return text


def normalize_text(value: str) -> str:
    return " ".join(_fb_decode(value).replace("\u00a0", " ").split())


def is_meaningful_text(text: str, min_chars: int) -> bool:
    """Check if already-normalized text is meaningful. Caller must normalize first."""
    if len(text) < min_chars:
        return False
    if text.startswith("http://") or text.startswith("https://"):
        return False
    return any(char.isalpha() for char in text)


def parse_timestamp(value: Any) -> str | None:
    if value in (None, ""):
        return None
    if isinstance(value, str):
        return value
    if not isinstance(value, (int, float)):
        return None
    if value > 10_000_000_000:
        value = value / 1000.0
    return datetime.fromtimestamp(value, tz=timezone.utc).isoformat().replace("+00:00", "Z")


def extract_author(node: dict[str, Any], inherited_author: str | None) -> str | None:
    for field in AUTHOR_FIELDS:
        value = node.get(field)
        if isinstance(value, str) and value.strip():
            return value.strip()
    from_value = node.get("from")
    if isinstance(from_value, dict):
        name = from_value.get("name")
        if isinstance(name, str) and name.strip():
            return name.strip()
    return inherited_author


def extract_timestamp(node: dict[str, Any], inherited_timestamp: str | None) -> str | None:
    for field in TIMESTAMP_FIELDS:
        parsed = parse_timestamp(node.get(field))
        if parsed:
            return parsed
    return inherited_timestamp


def author_matches(author: str | None, author_names: set[str]) -> bool:
    if not author_names or not author:
        return True
    author_casefold = author.casefold()
    return any(author_casefold == candidate for candidate in author_names)


def iter_candidates(
    node: Any,
    json_path: str,
    inherited_author: str | None,
    inherited_timestamp: str | None,
    author_names: set[str],
) -> list[dict[str, Any]]:
    candidates: list[dict[str, Any]] = []

    if isinstance(node, dict):
        author = extract_author(node, inherited_author)
        timestamp = extract_timestamp(node, inherited_timestamp)

        for field, value in node.items():
            if isinstance(value, str) and field in TEXT_FIELDS and author_matches(author, author_names):
                candidates.append(
                    {
                        "text": value,
                        "field": field,
                        "author": author,
                        "date": timestamp,
                        "json_path": f"{json_path}.{field}",
                    }
                )

        for key, value in node.items():
            child_path = f"{json_path}.{key}"
            candidates.extend(
                iter_candidates(value, child_path, author, timestamp, author_names)
            )
        return candidates

    if isinstance(node, list):
        for index, value in enumerate(node):
            child_path = f"{json_path}[{index}]"
            candidates.extend(
                iter_candidates(value, child_path, inherited_author, inherited_timestamp, author_names)
            )

    return candidates


def extract_file(path: Path, max_chars: int, min_chars: int, author_names: set[str]) -> int:
    try:
        payload = json.loads(path.read_text(errors="ignore"))
    except json.JSONDecodeError as exc:
        print(f"Error: failed to parse {path}: {exc}", file=sys.stderr)
        return 0

    emitted = 0
    seen: set[tuple[str, str, str | None]] = set()
    for candidate in iter_candidates(payload, "$", None, None, author_names):
        normalized = normalize_text(candidate["text"])
        if not is_meaningful_text(normalized, min_chars=min_chars):
            continue
        dedupe_key = (normalized, candidate["field"], candidate["date"])
        if dedupe_key in seen:
            continue
        seen.add(dedupe_key)
        for index, chunk in enumerate(chunk_text(normalized, max_chars=max_chars), start=1):
            emit_record(
                chunk,
                {
                    "source_type": "facebook-json",
                    "source": str(path),
                    "date": candidate["date"],
                    "context": {
                        "author": candidate["author"],
                        "field": candidate["field"],
                        "json_path": candidate["json_path"],
                        "chunk_index": index,
                    },
                },
            )
            emitted += 1
    return emitted


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("paths", nargs="+", help="Facebook export files or directories")
    parser.add_argument(
        "--author-name",
        action="append",
        default=[],
        help="Only include records from this author name. May be repeated.",
    )
    parser.add_argument("--max-chars", type=int, default=4000)
    parser.add_argument("--min-chars", type=int, default=20)
    args = parser.parse_args()

    try:
        files = list(iter_files(args.paths, {".json"}))
    except FileNotFoundError as exc:
        print(f"Error: {exc}", file=sys.stderr)
        return 1

    if not files:
        print("Error: no JSON files found", file=sys.stderr)
        return 1

    author_names = {value.casefold() for value in args.author_name if value.strip()}
    emitted = 0
    for path in files:
        emitted += extract_file(path, args.max_chars, args.min_chars, author_names)

    if emitted == 0:
        print("Error: no Facebook writing samples found", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())

