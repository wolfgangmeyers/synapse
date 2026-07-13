#!/usr/bin/env python3
"""Deprecated Mailbox V1 sender.

Use mailbox_send.py for agent coordination. Email requires mailbox_email.py.
"""

import sys


def main() -> int:
    print(
        "mailbox_send.py is retired and performs no delivery. "
        "Use mailbox_send.py for agent messages or mailbox_email.py for email.",
        file=sys.stderr,
    )
    return 2


if __name__ == "__main__":
    raise SystemExit(main())
