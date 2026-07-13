#!/usr/bin/env python3
"""Install the canonical Mailbox V2 Pi extension at its stable local path."""

from pathlib import Path
import shutil

SOURCE = Path(__file__).resolve().parents[4] / "pi-extensions" / "mecha-messaging.js"
TARGET = Path.home() / ".mecha-developer" / "extensions" / "mecha-messaging.js"


def main() -> int:
    if not SOURCE.is_file():
        raise SystemExit(f"Missing canonical extension source: {SOURCE}")
    TARGET.parent.mkdir(parents=True, exist_ok=True)
    # Always remove the installed artifact before copying so Pi cannot retain a
    # stale extension through a symlink or an earlier copied version.
    if TARGET.exists() or TARGET.is_symlink():
        TARGET.unlink()
    shutil.copy2(SOURCE, TARGET)
    print(f"Pi extension installed: {TARGET} (source: {SOURCE})")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
