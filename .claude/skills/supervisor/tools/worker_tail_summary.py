#!/usr/bin/env python3
"""Capture compact worker tails while suppressing common idle/noise lines."""

from __future__ import annotations

import argparse
import os
import re
import sys
from typing import Iterable

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from _tmux import capture_pane, list_windows, window_exists  # noqa: E402

NOISE_PATTERNS = [
    re.compile(r"^\s*$"),
    re.compile(r"^\s*(esc to interrupt|ctrl[- ]c|press .+ to|use .* to)\b", re.I),
    re.compile(r"^\s*[>›❯]\s*$"),
    re.compile(r"^\s*no acknowledgment needed", re.I),
    re.compile(r"^\s*(acknowledged|sounds good|got it|will do|thanks)[.!\s]*$", re.I),
    re.compile(r"^\s*\[[0-9:./ -]+\]\s*$"),
]

STATE_HINTS = [
    ("blocked", re.compile(r"\b(blocked|stuck|need help|cannot proceed|failed|error:)\b", re.I)),
    ("complete", re.compile(r"\b(task complete|ready to be cleaned up|completed)\b", re.I)),
    ("awaiting", re.compile(r"\b(awaiting|waiting for|ready for review|authorization)\b", re.I)),
    ("working", re.compile(r"\b(running|implementing|testing|next:)\b", re.I)),
]


def is_noise(line: str) -> bool:
    return any(pattern.search(line) for pattern in NOISE_PATTERNS)


def compact_lines(text: str, *, keep: int) -> list[str]:
    lines = [line.rstrip() for line in text.splitlines()]
    meaningful = [line for line in lines if not is_noise(line)]
    if not meaningful:
        meaningful = [line for line in lines if line.strip()]
    return meaningful[-keep:]


def state_hint(lines: Iterable[str]) -> str:
    joined = "\n".join(lines)
    for label, pattern in STATE_HINTS:
        if pattern.search(joined):
            return label
    return "unknown"


def known_windows() -> list[str]:
    return [name for _, name in list_windows()]


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("windows", nargs="*", help="Worker window names; defaults to all tmux windows except supervisor")
    parser.add_argument("--lines", type=int, default=160, help="Scrollback lines to capture before compaction")
    parser.add_argument("--keep", type=int, default=20, help="Meaningful lines to print per worker")
    parser.add_argument("--include-supervisor", action="store_true")
    args = parser.parse_args(argv)

    windows = args.windows or known_windows()
    if not args.include_supervisor:
        windows = [window for window in windows if window != "supervisor"]
    if not windows:
        print("No worker windows found.")
        return 0

    exit_code = 0
    for index, window in enumerate(windows):
        if index:
            print()
        if not window_exists(window):
            print(f"## {window}\nmissing window")
            exit_code = 1
            continue
        captured = capture_pane(window, lines=args.lines)
        if captured is None:
            print(f"## {window}\nfailed to capture")
            exit_code = 1
            continue
        lines = compact_lines(captured, keep=args.keep)
        print(f"## {window} — hint: {state_hint(lines)}")
        print("\n".join(lines) if lines else "(no meaningful output)")
    return exit_code


if __name__ == "__main__":
    raise SystemExit(main())
