#!/usr/bin/env python3
"""Shared helpers for generate-persona adapter scripts.

Adapter scripts import this module via ``from _common import ...``.
This works because Python adds the script's directory to sys.path when
invoked as ``python3 path/to/script.py``.  Do not import these modules
as a package — they are standalone CLI scripts.
"""

from __future__ import annotations

import json
import sys
from pathlib import Path
from typing import Iterable


def emit_record(text: str, metadata: dict) -> None:
    record = {
        "text": text.strip(),
        "metadata": metadata,
    }
    if not record["text"]:
        return
    try:
        json.dump(record, sys.stdout, ensure_ascii=True)
        sys.stdout.write("\n")
    except BrokenPipeError:
        raise SystemExit(0)


def chunk_text(text: str, max_chars: int = 4000) -> list[str]:
    text = text.strip()
    if not text:
        return []

    if len(text) <= max_chars:
        return [text]

    paragraphs = [part.strip() for part in text.split("\n\n") if part.strip()]
    chunks: list[str] = []
    current: list[str] = []
    current_len = 0

    for paragraph in paragraphs:
        extra = len(paragraph) + (2 if current else 0)
        if current and current_len + extra > max_chars:
            chunks.append("\n\n".join(current))
            current = [paragraph]
            current_len = len(paragraph)
            continue
        current.append(paragraph)
        current_len += extra

    if current:
        chunks.append("\n\n".join(current))

    if chunks:
        return chunks

    return [text[i : i + max_chars] for i in range(0, len(text), max_chars)]


def iter_files(paths: Iterable[str], allowed_suffixes: set[str]) -> Iterable[Path]:
    for raw_path in paths:
        path = Path(raw_path).expanduser()
        if path.is_file():
            if path.suffix.lower() in allowed_suffixes:
                yield path
            continue
        if not path.exists():
            raise FileNotFoundError(f"path not found: {path}")
        for child in sorted(path.rglob("*")):
            if child.is_file() and child.suffix.lower() in allowed_suffixes:
                yield child
