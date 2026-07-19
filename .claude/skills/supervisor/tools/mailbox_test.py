#!/usr/bin/env python3
"""Focused Mailbox V2 client tests.

These tests use an ephemeral loopback HTTP server. They neither contact the
managed router nor create Mailbox V1 filesystem messages.
"""

import json
import os
import re
import subprocess
import sys
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path


TOOLS_DIR = Path(__file__).resolve().parent
SEND_SCRIPT = TOOLS_DIR / "mailbox_send.py"
sys.path.insert(0, str(TOOLS_DIR))
import mailbox_send


class RecordingRouter:
    def __init__(self, status=201, response=None):
        self.requests = []
        self.status = status
        self.response = response
        outer = self

        class Handler(BaseHTTPRequestHandler):
            def do_POST(self):
                length = int(self.headers["Content-Length"])
                payload = json.loads(self.rfile.read(length))
                outer.requests.append((self.path, payload))
                response = outer.response or {
                    "message": {"id": payload["id"], "state": "queued"}
                }
                encoded = json.dumps(response).encode("utf-8")
                self.send_response(outer.status)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(encoded)))
                self.end_headers()
                self.wfile.write(encoded)

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


def run_send(args, env=None):
    return subprocess.run(
        [sys.executable, str(SEND_SCRIPT), *args],
        capture_output=True,
        text=True,
        env={**os.environ, **(env or {})},
    )


class MailboxSendV2Test(unittest.TestCase):
    def test_nanoid_helper_generates_unique_protocol_safe_ids(self):
        generated = [mailbox_send.nanoid() for _ in range(100)]

        self.assertEqual(len(set(generated)), len(generated))
        self.assertTrue(all(re.fullmatch(r"[A-Za-z0-9]{21}", value) for value in generated))

    def test_posts_a_v2_envelope_without_a_filesystem_mailbox(self):
        with tempfile.TemporaryDirectory() as tmp, RecordingRouter() as router:
            legacy_mailbox = Path(tmp) / "mailboxes"
            result = run_send(
                [
                    "--to", "worker-1",
                    "--from", "supervisor",
                    "--subject", "Task update",
                    "--body", "x" * 1_500,
                    "--router-url", router.url,
                ],
                {"MECHA_MAILBOX_DIR_OVERRIDE": str(legacy_mailbox)},
            )

            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(len(router.requests), 1)
            path, message = router.requests[0]
            self.assertEqual(path, "/v1/messages")
            self.assertEqual(message["from"], "supervisor")
            self.assertEqual(message["to"], "worker-1")
            self.assertEqual(message["subject"], "Task update")
            self.assertEqual(message["body"], "x" * 1_500)
            self.assertIn("created_at", message)
            self.assertRegex(message["id"], r"^[A-Za-z0-9]{21}$")
            self.assertIn(f"Message {message['id']} accepted: queued", result.stdout)
            self.assertFalse(legacy_mailbox.exists())

    def test_derives_sender_from_the_current_tmux_window(self):
        with tempfile.TemporaryDirectory() as tmp, RecordingRouter() as router:
            tmux = Path(tmp) / "tmux"
            tmux.write_text("#!/bin/sh\nprintf '%s\\n' worker_1\n", encoding="utf-8")
            tmux.chmod(0o755)
            result = run_send(
                ["--to", "supervisor", "--body", "Status", "--router-url", router.url],
                {
                    "TMUX": "/tmp/tmux-test/default,1,0",
                    "TMUX_PANE": "%7",
                    "PATH": f"{tmp}:{os.environ['PATH']}",
                },
            )

            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(router.requests[0][1]["from"], "worker_1")

    def test_reports_router_rejections(self):
        response = {"error": {"code": "queue_full", "message": "Recipient queue is full."}}
        with RecordingRouter(status=429, response=response) as router:
            result = run_send(
                ["--to", "worker-1", "--from", "supervisor", "--body", "Status", "--router-url", router.url]
            )

            self.assertEqual(result.returncode, 1)
            self.assertIn("Router rejected message: queue_full", result.stderr)

    def test_allows_exact_non_loopback_router_urls(self):
        self.assertEqual(
            mailbox_send.router_endpoint("https://example.com", "/v1/messages"),
            "https://example.com/v1/messages",
        )

    def test_rejects_non_agent_recipients_and_v1_large_body_override(self):
        non_agent = run_send(["--to", "person@example.com", "--from", "supervisor", "--body", "Status"])
        legacy_flag = run_send(
            ["--to", "worker-1", "--from", "supervisor", "--body", "Status", "--allow-large-body"]
        )

        self.assertEqual(non_agent.returncode, 2)
        self.assertIn("only sends to agent IDs", non_agent.stderr)
        self.assertEqual(legacy_flag.returncode, 2)
        self.assertIn("unrecognized arguments: --allow-large-body", legacy_flag.stderr)


if __name__ == "__main__":
    unittest.main()
