#!/usr/bin/env python3
"""Retired compatibility entry point for the former Pi extension installer.

The active extension is registered directly from the mecha-developer checkout.
This command deliberately does not inspect or mutate Pi settings, copy files,
or recreate the retired installed artifact.
"""

import sys


RETIREMENT_MESSAGE = (
    "install_pi_extension.py is retired; preserve the registered checkout "
    "source and do not create ~/.mecha-developer/extensions/mecha-messaging.js"
)


def main() -> int:
    """Fail closed so callers cannot recreate the retired installed artifact."""
    raise SystemExit(RETIREMENT_MESSAGE)


if __name__ == "__main__":
    sys.exit(main())
