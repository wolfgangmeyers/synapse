#!/usr/bin/env python3
"""Retired V1 reminder cron.

Mailbox V2 router scheduling owns reminder delivery. This command intentionally
performs no JSON reminder read/write and no tmux delivery.
"""

import sys


def main() -> int:
    print(
        "reminder_cron.py is retired. Use the Mailbox V2 router reminder scheduler; "
        "no V1 reminder delivery was performed.",
        file=sys.stderr,
    )
    return 2


if __name__ == "__main__":
    raise SystemExit(main())
