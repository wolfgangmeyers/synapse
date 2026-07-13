#!/usr/bin/env python3
"""Tests for the Mailbox V2 KB maintenance reminder job.

Run with:
  python3 .claude/skills/supervisor/tools/test_kb_reminder_cron.py
"""

import json
import os
import subprocess
import sys
import tempfile
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


TOOLS_DIR = os.path.dirname(os.path.abspath(__file__))
SCRIPT = os.path.join(TOOLS_DIR, "kb_reminder_cron.py")


class RecordingRouter:
    def __init__(self):
        self.requests = []
        outer = self

        class Handler(BaseHTTPRequestHandler):
            def do_POST(self):
                length = int(self.headers["Content-Length"])
                message = json.loads(self.rfile.read(length))
                outer.requests.append((self.path, message))
                payload = json.dumps({"message": {"id": message["id"], "state": "queued"}}).encode()
                self.send_response(201)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(payload)))
                self.end_headers()
                self.wfile.write(payload)

            def log_message(self, _format, *_args):
                pass

        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)

    @property
    def url(self):
        return f"http://127.0.0.1:{self.server.server_port}"

    def __enter__(self):
        self.thread.start()
        return self

    def __exit__(self, *_args):
        self.server.shutdown()
        self.thread.join()
        self.server.server_close()


def run_script(args, env_overrides):
    return subprocess.run(
        [sys.executable, SCRIPT] + args,
        capture_output=True,
        text=True,
        env={**os.environ, **env_overrides},
    )


def assert_body_is_kb_reminder(body):
    assert "KB maintenance reminder:" in body
    assert "anything important has happened since the last KB update" in body
    assert "Use the kb skill" in body
    assert "~/.mecha-developer/kb/" in body
    assert "Do not store transient task state" in body


def test_dry_run_prints_reminder_without_posting_to_router():
    with tempfile.TemporaryDirectory() as tmp:
        legacy_mailbox = os.path.join(tmp, "mailboxes")
        result = run_script(["--dry-run"], {"MECHA_MAILBOX_DIR_OVERRIDE": legacy_mailbox})

        assert result.returncode == 0, result.stderr
        assert "To: supervisor" in result.stdout
        assert "From: kb-reminder-cron" in result.stdout
        assert "Subject: KB maintenance reminder" in result.stdout
        assert_body_is_kb_reminder(result.stdout)
        assert not os.path.exists(legacy_mailbox), "dry-run must not create a V1 mailbox"


def test_send_posts_one_v2_message_from_kb_sender():
    with tempfile.TemporaryDirectory() as tmp, RecordingRouter() as router:
        legacy_mailbox = os.path.join(tmp, "mailboxes")
        result = run_script(
            [],
            {
                "MECHA_ROUTER_URL": router.url,
                "MECHA_MAILBOX_DIR_OVERRIDE": legacy_mailbox,
            },
        )

        assert result.returncode == 0, result.stderr
        assert "Sent KB maintenance reminder to supervisor" in result.stdout
        assert len(router.requests) == 1
        path, message = router.requests[0]
        assert path == "/v1/messages"
        assert message["from"] == "kb-reminder-cron"
        assert message["to"] == "supervisor"
        assert message["subject"] == "KB maintenance reminder"
        assert_body_is_kb_reminder(message["body"])
        assert "created_at" in message
        assert not os.path.exists(legacy_mailbox), "V2 delivery must not write a V1 mailbox"


def main():
    tests = [
        test_dry_run_prints_reminder_without_posting_to_router,
        test_send_posts_one_v2_message_from_kb_sender,
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
        print("\nFailed tests:")
        for name, exc in failures:
            print(f"  {name}: {exc}")
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
