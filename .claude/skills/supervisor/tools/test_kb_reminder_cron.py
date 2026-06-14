#!/usr/bin/env python3
"""Tests for kb_reminder_cron.py.

Run with:
  python3 .claude/skills/supervisor/tools/test_kb_reminder_cron.py
"""

import json
import os
import subprocess
import sys
import tempfile


TOOLS_DIR = os.path.dirname(os.path.abspath(__file__))
SCRIPT = os.path.join(TOOLS_DIR, "kb_reminder_cron.py")


def run_script(args, env_overrides):
    env = os.environ.copy()
    env.update(env_overrides)
    return subprocess.run(
        [sys.executable, SCRIPT] + args,
        capture_output=True,
        text=True,
        env=env,
    )


def assert_body_is_kb_reminder(body):
    assert "KB maintenance reminder:" in body
    assert "anything important has happened since the last KB update" in body
    assert "Use the kb skill" in body
    assert "~/.mecha-developer/kb/" in body
    assert "Do not store transient task state" in body


def test_dry_run_prints_reminder_without_writing_mailbox():
    with tempfile.TemporaryDirectory() as tmp:
        mailbox_dir = os.path.join(tmp, "mailboxes")

        result = run_script(
            ["--dry-run"],
            {"MECHA_MAILBOX_DIR_OVERRIDE": mailbox_dir},
        )

        assert result.returncode == 0, result.stderr
        assert "To: supervisor" in result.stdout
        assert "From: kb-reminder-cron" in result.stdout
        assert "Subject: KB maintenance reminder" in result.stdout
        assert_body_is_kb_reminder(result.stdout)
        assert not os.path.exists(mailbox_dir), "dry-run must not create mailbox files"


def test_send_writes_one_supervisor_message_from_kb_sender():
    with tempfile.TemporaryDirectory() as tmp:
        mailbox_dir = os.path.join(tmp, "mailboxes")

        result = run_script(
            [],
            {"MECHA_MAILBOX_DIR_OVERRIDE": mailbox_dir},
        )

        assert result.returncode == 0, result.stderr
        assert "Sent KB maintenance reminder to supervisor" in result.stdout

        supervisor_dir = os.path.join(mailbox_dir, "supervisor")
        files = [f for f in os.listdir(supervisor_dir) if f.endswith(".json")]
        assert len(files) == 1, f"expected one mailbox message, got {files}"

        with open(os.path.join(supervisor_dir, files[0]), encoding="utf-8") as f:
            message = json.load(f)
        assert message["from"] == "kb-reminder-cron"
        assert message["to"] == "supervisor"
        assert message["subject"] == "KB maintenance reminder"
        assert message["read"] is False
        assert_body_is_kb_reminder(message["body"])


def main():
    tests = [
        test_dry_run_prints_reminder_without_writing_mailbox,
        test_send_writes_one_supervisor_message_from_kb_sender,
    ]
    failures = []
    for test in tests:
        try:
            test()
            print(f"PASS {test.__name__}")
        except Exception as exc:
            failures.append((test.__name__, exc))
            print(f"FAIL {test.__name__}: {exc}")

    if failures:
        print()
        print("Failed tests:")
        for name, exc in failures:
            print(f"  {name}: {exc}")
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
