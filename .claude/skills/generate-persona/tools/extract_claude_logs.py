#!/usr/bin/env python3
"""Normalize Claude Code chat logs into persona-analysis chunks."""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

from _common import emit_record


def extract_text(content) -> str:
    if isinstance(content, str):
        return content.strip()
    if not isinstance(content, list):
        return ""

    parts: list[str] = []
    for block in content:
        if not isinstance(block, dict):
            continue
        block_type = block.get("type")
        if block_type == "text" and isinstance(block.get("text"), str):
            parts.append(block["text"].strip())
    return "\n\n".join(part for part in parts if part)


def human_message_text(entry: dict) -> str:
    if entry.get("type") != "user":
        return ""
    message = entry.get("message")
    if isinstance(message, dict):
        return extract_text(message.get("content"))
    return extract_text(entry.get("content"))


def infer_project(path: Path) -> str:
    parent = path.parent.name
    return parent or "unknown"


def process_file(path: Path, min_human_messages: int) -> int:
    human_count = 0
    records: list[tuple[str, dict]] = []

    with path.open() as handle:
        for line in handle:
            line = line.strip()
            if not line:
                continue
            try:
                entry = json.loads(line)
            except json.JSONDecodeError:
                continue

            text = human_message_text(entry)
            if not text:
                continue
            human_count += 1
            records.append(
                (
                    text,
                    {
                        "source_type": "claude-chat-logs",
                        "source": str(path),
                        "date": entry.get("timestamp"),
                        "context": {
                            "session_file": path.name,
                            "project": infer_project(path),
                            "speaker": "human",
                        },
                    },
                )
            )

    if human_count < min_human_messages:
        return 0

    for text, metadata in records:
        emit_record(text, metadata)
    return len(records)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("paths", nargs="+", help="JSONL files or directories containing them")
    parser.add_argument("--min-human-messages", type=int, default=5)
    args = parser.parse_args()

    files: list[Path] = []
    for raw_path in args.paths:
        path = Path(raw_path).expanduser()
        if path.is_file() and path.suffix == ".jsonl":
            files.append(path)
            continue
        if path.is_dir():
            files.extend(sorted(p for p in path.rglob("*.jsonl") if "subagents" not in p.parts))
            continue
        print(f"Error: path not found or not a .jsonl source: {path}", file=sys.stderr)
        return 1

    if not files:
        print("Error: no Claude log files found", file=sys.stderr)
        return 1

    total_emitted = 0
    for path in files:
        total_emitted += process_file(path, args.min_human_messages)

    if total_emitted == 0:
        print("Error: no qualifying Claude log chunks emitted", file=sys.stderr)
        return 1

    return 0


if __name__ == "__main__":
    sys.exit(main())
