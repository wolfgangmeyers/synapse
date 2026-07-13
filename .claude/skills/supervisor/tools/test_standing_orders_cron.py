#!/usr/bin/env python3
"""Tests for the Mailbox V2 standing-orders job.

Run with:
  python3 .claude/skills/supervisor/tools/test_standing_orders_cron.py
"""

import json
import os
import subprocess
import sys
import tempfile
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


TOOLS_DIR = os.path.dirname(os.path.abspath(__file__))
SCRIPT = os.path.join(TOOLS_DIR, "standing_orders_cron.py")

ISSUE_IDS = [
    "dnsid#1119",
    "dnsid#1120",
    "dnsid#1121",
    "dnsid#1122",
    "dnsid#1123",
    "dnsid#1124",
    "dnsid#1125",
]

SAMPLE_ORDERS = [
    {
        "id": "so-1",
        "priority": "high",
        "title": "No Slack posts without explicit approval.",
        "description": "Do not post directly to Slack channels or DMs without explicit approval.",
    },
    {
        "id": "so-2",
        "priority": "high",
        "title": "Keep internal process out of external communications.",
        "description": (
            "Do not include internal process details in GitHub, Slack, email, or "
            "other external communications. Keep process notes inside supervisor "
            "mailbox reports and internal runbooks. "
            + ("Operational context stays internal. " * 20)
        ),
    },
    {
        "id": "so-3",
        "priority": "normal",
        "title": "Approve minor Jeff PRs when safe.",
        "description": "Approve minor Jeff-authored PRs only when review finds no real blockers.",
    },
    {
        "id": "so-4",
        "priority": "normal",
        "title": "Request jmelloy on local PRs.",
        "description": "For pull requests submitted from this device, request jmelloy as reviewer.",
    },
    {
        "id": "so-5",
        "priority": "high",
        "title": "Use one tracked worker per issue or PR.",
        "description": "Create one tracked worker per non-trivial implementation issue or PR.",
    },
    {
        "id": "so-6",
        "priority": "high",
        "title": "Keep initiatives moving until all agent-accomplishable work is done.",
        "description": (
            "Keep initiatives moving until every agent-accomplishable ticket has "
            "a pushed PR, is merged when approved, or is explicitly blocked. "
            "Current issue set: "
            + ", ".join(ISSUE_IDS)
            + "."
        ),
    },
    {
        "id": "so-7",
        "priority": "high",
        "title": "Prefer Codex workers.",
        "description": "When launching new tracked workers, prefer Codex workers by default.",
    },
]


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


def write_state(path, orders):
    with open(path, "w", encoding="utf-8") as f:
        json.dump({"next_task_id": 1, "tasks": [], "standing_orders": orders}, f)
        f.write("\n")


def assert_body_contains_orders(body):
    assert "Standing orders refresh:" in body
    assert "Source:" not in body
    for order in SAMPLE_ORDERS:
        assert order["id"] in body, f"missing order id {order['id']}"
        assert order["title"] in body, f"missing title for {order['id']}"
        assert order["description"].strip() in body, f"missing description for {order['id']}"
    for issue_id in ISSUE_IDS:
        assert issue_id in body, f"missing issue id {issue_id}"


def test_dry_run_prints_full_message_without_posting_to_router():
    with tempfile.TemporaryDirectory() as tmp:
        state_file = os.path.join(tmp, "supervisor-state.json")
        legacy_mailbox = os.path.join(tmp, "mailboxes")
        write_state(state_file, SAMPLE_ORDERS)

        result = run_script(
            ["--dry-run"],
            {"MECHA_STATE_FILE": state_file, "MECHA_MAILBOX_DIR_OVERRIDE": legacy_mailbox},
        )

        assert result.returncode == 0, result.stderr
        assert "To: supervisor" in result.stdout
        assert "From: standing-orders-cron" in result.stdout
        assert "Subject: Standing orders refresh" in result.stdout
        assert_body_contains_orders(result.stdout)
        assert not os.path.exists(legacy_mailbox), "dry-run must not create a V1 mailbox"


def test_send_posts_a_large_v2_message_without_a_legacy_override():
    with tempfile.TemporaryDirectory() as tmp, RecordingRouter() as router:
        state_file = os.path.join(tmp, "supervisor-state.json")
        legacy_mailbox = os.path.join(tmp, "mailboxes")
        write_state(state_file, SAMPLE_ORDERS)

        result = run_script(
            [],
            {
                "MECHA_STATE_FILE": state_file,
                "MECHA_ROUTER_URL": router.url,
                "MECHA_MAILBOX_DIR_OVERRIDE": legacy_mailbox,
            },
        )

        assert result.returncode == 0, result.stderr
        assert "Sent standing orders refresh to supervisor" in result.stdout
        assert len(router.requests) == 1
        path, message = router.requests[0]
        assert path == "/v1/messages"
        assert message["from"] == "standing-orders-cron"
        assert message["to"] == "supervisor"
        assert message["subject"] == "Standing orders refresh"
        assert len(message["body"]) > 1000, "test body should exercise V2 body delivery"
        assert_body_contains_orders(message["body"])
        assert "created_at" in message
        assert not os.path.exists(legacy_mailbox), "V2 delivery must not write a V1 mailbox"


def main():
    tests = [
        test_dry_run_prints_full_message_without_posting_to_router,
        test_send_posts_a_large_v2_message_without_a_legacy_override,
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
