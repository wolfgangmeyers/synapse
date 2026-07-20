#!/usr/bin/env python3
"""Send a watchdog alert body from a file.

This helper lets the Codex watchdog agent write captured tmux analysis to a
temporary file and send it without interpolating untrusted pane text through a
shell command line.
"""

import argparse
import os
from pathlib import Path
import subprocess
import sys


MAX_BODY_BYTES = 64 * 1024
DEFAULT_SEND_TIMEOUT_SECONDS = 30


def main():
    parser = argparse.ArgumentParser(description="Send a watchdog mailbox alert.")
    parser.add_argument("--body-file", required=True, help="Path to alert body text")
    parser.add_argument("--subject", default="Stuck windows", help="Mailbox subject")
    args = parser.parse_args()

    workdir = os.environ.get("MECHA_WATCHDOG_WORKDIR")
    if not workdir:
        print("Error: MECHA_WATCHDOG_WORKDIR is not set", file=sys.stderr)
        return 1

    try:
        workdir_path = Path(workdir).resolve(strict=True)
    except OSError as exc:
        print(f"Error: cannot resolve MECHA_WATCHDOG_WORKDIR: {exc}", file=sys.stderr)
        return 1
    body_path = Path(args.body_file)
    if body_path.is_symlink():
        print("Error: body file must not be a symlink", file=sys.stderr)
        return 1
    try:
        resolved_body_path = body_path.resolve(strict=True)
    except OSError as exc:
        print(f"Error: cannot resolve body file: {exc}", file=sys.stderr)
        return 1

    try:
        resolved_body_path.relative_to(workdir_path)
    except ValueError:
        print("Error: body file must be inside MECHA_WATCHDOG_WORKDIR", file=sys.stderr)
        return 1

    try:
        stat = resolved_body_path.stat()
    except OSError as exc:
        print(f"Error: cannot stat body file: {exc}", file=sys.stderr)
        return 1

    if not resolved_body_path.is_file():
        print("Error: body file is not a regular file", file=sys.stderr)
        return 1
    if stat.st_size > MAX_BODY_BYTES:
        print(
            f"Error: body file exceeds {MAX_BODY_BYTES} byte limit",
            file=sys.stderr,
        )
        return 1

    with open(resolved_body_path, "r", encoding="utf-8", errors="replace") as f:
        body = f.read()

    if not body.strip():
        print("Error: body file is empty", file=sys.stderr)
        return 1

    try:
        timeout = int(
            os.environ.get(
                "MECHA_WATCHDOG_MAIL_TIMEOUT_SECONDS",
                str(DEFAULT_SEND_TIMEOUT_SECONDS),
            )
        )
    except ValueError:
        timeout = DEFAULT_SEND_TIMEOUT_SECONDS
    if timeout < 1:
        timeout = DEFAULT_SEND_TIMEOUT_SECONDS

    supervisor_tools = Path(__file__).resolve().parents[2] / "supervisor" / "tools"
    mailbox_send = str(supervisor_tools / "mailbox_send.py")
    argv = [
        sys.executable,
        mailbox_send,
        "--to",
        "supervisor",
        "--from",
        "watchdog",
        "--subject",
        args.subject,
        "--body",
        body,
    ]
    env = os.environ.copy()
    if "MECHA_MAILBOX_DIR_OVERRIDE" not in env and env.get("MECHA_STATE_DIR"):
        env["MECHA_MAILBOX_DIR_OVERRIDE"] = os.path.join(
            env["MECHA_STATE_DIR"],
            "mailboxes",
        )
    try:
        status = subprocess.run(argv, env=env, timeout=timeout).returncode
    except subprocess.TimeoutExpired:
        print(
            f"Error: mailbox_send.py timed out after {timeout} seconds",
            file=sys.stderr,
        )
        return 1
    if status == 0:
        try:
            resolved_body_path.unlink()
        except OSError as exc:
            print(
                f"Warning: alert sent but body file could not be removed: {exc}",
                file=sys.stderr,
            )
    return status


if __name__ == "__main__":
    sys.exit(main())
