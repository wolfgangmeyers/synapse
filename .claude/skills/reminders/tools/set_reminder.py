#!/usr/bin/env python3
"""Compatibility entry point for setting a Mailbox V2 reminder.

Prefer `reminders.py set ...`. This wrapper accepts the former set_reminder.py
arguments but delegates them to the durable V2 router client.
"""

from __future__ import annotations

import sys

import reminders


def main(argv: list[str] | None = None) -> int:
    return reminders.main(["set", *(sys.argv[1:] if argv is None else argv)])


if __name__ == "__main__":
    raise SystemExit(main())
