#!/usr/bin/env python3
"""Retired worker-message shim.

Use mailbox_send.py for all worker coordination. If the recipient is not
registered or running, restore it through the managed Pi launcher; never type
instructions into a terminal or tmux pane.
"""

import sys


def main() -> int:
    print(
        "worker_send.py is retired and performs no delivery. Use mailbox_send.py "
        "for all coordination. If the recipient is not registered or running, "
        "restore it through the managed Pi launcher; do not use terminal injection.",
        file=sys.stderr,
    )
    return 2


if __name__ == "__main__":
    raise SystemExit(main())
