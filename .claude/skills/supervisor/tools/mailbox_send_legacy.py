#!/usr/bin/env python3
"""Deprecated Mailbox V1 sender.

Use mailbox_send.py for agent coordination.
"""

import sys


def main() -> int:
    print(
        "mailbox_send.py is retired and performs no delivery. "
        "Use mailbox_send.py for agent messages.",
        file=sys.stderr,
    )
    return 2


if __name__ == "__main__":
    raise SystemExit(main())
