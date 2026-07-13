#!/usr/bin/env python3
"""Compatibility entry point for Mailbox V2 reminder management.

Prefer `reminders.py list|delete|delete-all`. This wrapper accepts the former
manage_reminders.py commands but delegates them to the durable V2 router client.
"""

from __future__ import annotations

import sys

import reminders


def main(argv: list[str] | None = None) -> int:
    return reminders.main(sys.argv[1:] if argv is None else argv)


if __name__ == "__main__":
    raise SystemExit(main())
