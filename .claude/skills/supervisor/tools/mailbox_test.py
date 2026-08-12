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
                response = outer.response if outer.response is not None else {
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


class DedupeRouter:
    def __init__(self):
        self.requests = []
        self.responses = []
        outer = self

        class Handler(BaseHTTPRequestHandler):
            def do_POST(self):
                length = int(self.headers["Content-Length"])
                payload = json.loads(self.rfile.read(length))
                outer.requests.append((self.path, payload))
                if len(outer.requests) == 1:
                    status = 201
                    response = {"message": {"id": payload["id"], "state": "queued"}}
                else:
                    status = 200
                    response = {
                        "message": {"id": outer.requests[0][1]["id"], "state": "queued"},
                        "conversation_message": {"id": payload["id"]},
                    }
                outer.responses.append(response)
                encoded = json.dumps(response).encode("utf-8")
                self.send_response(status)
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

    def test_accepts_a_batched_router_response(self):
        response = {"message": {"id": "existing-message", "state": "queued", "batched": True}}
        with RecordingRouter(response=response) as router:
            result = run_send(
                ["--to", "worker-1", "--from", "supervisor", "--body", "Status", "--router-url", router.url]
            )

            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn("Message existing-message accepted: queued", result.stdout)

    def test_accepts_a_deduplicated_router_response(self):
        with DedupeRouter() as router:
            first = run_send(
                ["--to", "worker-1", "--from", "supervisor", "--body", "Status", "--router-url", router.url]
            )
            second = run_send(
                ["--to", "worker-1", "--from", "supervisor", "--body", "Status", "--router-url", router.url]
            )

            self.assertEqual(first.returncode, 0, first.stderr)
            self.assertEqual(second.returncode, 0, second.stderr)
            first_id = router.requests[0][1]["id"]
            second_id = router.requests[1][1]["id"]
            self.assertNotEqual(first_id, second_id)
            self.assertEqual(router.responses[1]["message"]["id"], first_id)
            self.assertEqual(router.responses[1]["conversation_message"]["id"], second_id)
            self.assertNotEqual(
                router.responses[1]["message"]["id"],
                router.responses[1]["conversation_message"]["id"],
            )
            self.assertIn(f"Message {first_id} accepted: queued", second.stdout)

    def test_accepts_router_message_id_contract(self):
        for message_id in ("A", "a" * 128, "a.b_c-d"):
            with self.subTest(message_id=message_id), RecordingRouter(response={"message": {"id": message_id, "state": "queued"}}) as router:
                result = run_send(
                    ["--to", "worker-1", "--from", "supervisor", "--body", "Status", "--router-url", router.url]
                )

                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertIn(f"Message {message_id} accepted: queued", result.stdout)

    def test_accepts_all_router_message_states(self):
        for state in ("queued", "leased", "injection_requested", "acknowledged", "dead_letter"):
            with self.subTest(state=state), RecordingRouter(response={"message": {"id": "delivery", "state": state}}) as router:
                result = run_send(
                    ["--to", "worker-1", "--from", "supervisor", "--body", "Status", "--router-url", router.url]
                )

                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertIn(f"accepted: {state}", result.stdout)

    def test_rejects_malformed_success_responses(self):
        responses = [
            {},
            {"message": None},
            {"message": {"id": 42, "state": "queued"}},
            {"message": {"id": "", "state": "queued"}},
            {"message": {"id": "-delivery", "state": "queued"}},
            {"message": {"id": "a" * 129, "state": "queued"}},
            {"message": {"id": "delivery"}},
            {"message": {"id": "delivery", "state": None}},
            {"message": {"id": "delivery", "state": ""}},
            {"message": {"id": "delivery", "state": "unknown"}},
        ]
        for response in responses:
            with self.subTest(response=response), RecordingRouter(response=response) as router:
                result = run_send(
                    ["--to", "worker-1", "--from", "supervisor", "--body", "Status", "--router-url", router.url]
                )

                self.assertEqual(result.returncode, 1)
                self.assertIn("Router returned an invalid acceptance response.", result.stderr)

    def test_reports_router_rejections(self):
        response = {"error": {"code": "queue_full", "message": "Recipient queue is full."}}
        with RecordingRouter(status=429, response=response) as router:
            result = run_send(
                ["--to", "worker-1", "--from", "supervisor", "--body", "Status", "--router-url", router.url]
            )

            self.assertEqual(result.returncode, 1)
            self.assertIn("Router rejected message: queue_full", result.stderr)
            self.assertIn("Back off and wait for the recipient mailbox to clear", result.stderr)

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
