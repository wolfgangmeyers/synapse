import importlib.util
import io
import json
import sys
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from unittest import mock

MODULE_PATH = Path(__file__).with_name("managed_root_control.py")
SPEC = importlib.util.spec_from_file_location("managed_root_control", MODULE_PATH)
managed = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = managed
SPEC.loader.exec_module(managed)


class FakeReminderAPI:
    def __init__(self):
        self.calls = []
        self.reminders = []
        self.failure_status = None
        self.malformed = False
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), self.handler())
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()

    def handler(self):
        api = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *_args):
                pass

            def send_json(self, status, payload):
                raw = json.dumps(payload).encode()
                self.send_response(status)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(raw)))
                self.end_headers()
                self.wfile.write(raw)

            def do_GET(self):
                api.calls.append(("GET", self.path, None))
                if self.path == "/v1/reminders":
                    self.send_json(200, {"reminders": api.reminders})
                else:
                    reminder_id = self.path.rsplit("/", 1)[-1]
                    item = next((row for row in api.reminders if row["id"] == reminder_id), None)
                    self.send_json(200 if item else 404, {"reminder": item} if item else {"error": {"message": "not found"}})

            def do_POST(self):
                length = int(self.headers["Content-Length"])
                payload = json.loads(self.rfile.read(length))
                api.calls.append(("POST", self.path, payload))
                if api.failure_status is not None:
                    self.send_json(api.failure_status, {"error": {"message": "endpoint failed"}})
                    return
                if api.malformed:
                    self.send_json(201, {"reminder": {"id": payload["id"]}})
                    return
                api.reminders.append(payload)
                self.send_json(201, {"reminder": payload})

            def do_PATCH(self):
                length = int(self.headers["Content-Length"])
                payload = json.loads(self.rfile.read(length))
                api.calls.append(("PATCH", self.path, payload))
                reminder_id = self.path.rsplit("/", 1)[-1]
                item = next(row for row in api.reminders if row["id"] == reminder_id)
                item.update(payload)
                self.send_json(200, {"reminder": item})

            def do_DELETE(self):
                api.calls.append(("DELETE", self.path, None))
                reminder_id = self.path.rsplit("/", 1)[-1]
                api.reminders[:] = [row for row in api.reminders if row["id"] != reminder_id]
                self.send_response(204)
                self.end_headers()

        return Handler

    @property
    def url(self):
        return f"http://127.0.0.1:{self.server.server_port}"

    def close(self):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=2)


class ManagedReminderAdapterTests(unittest.TestCase):
    def setUp(self):
        self.api = FakeReminderAPI()
        self.addCleanup(self.api.close)
        self.fence = {"agent_id": "managed-root"}
        self.settings = {}

    def execute(self, action):
        with mock.patch.dict(managed.os.environ, {"MECHA_ROUTER_URL": self.api.url, "MECHA_AGENT_ID": "wrong-agent"}, clear=False), mock.patch.object(managed.time, "time", return_value=1893456000.0):
            return managed.execute_supervisor("reminder.control", action, "ignored-argument", self.settings, "request-1", self.fence)

    def execute_via_run(self, action):
        request = {"request_id": "request-1", "operation_id": "reminder.control", "arguments": action, "fence": self.fence}
        with tempfile.TemporaryDirectory() as home, mock.patch.dict(managed.os.environ, {"MECHA_ROUTER_URL": self.api.url, "MECHA_AGENT_ID": "wrong-agent"}, clear=False), mock.patch.object(managed.time, "time", return_value=1893456000.0), mock.patch.object(managed.sys, "stdin", io.StringIO(json.dumps(request))), mock.patch.object(managed.sys, "stdout", io.StringIO()) as output:
            self.assertEqual(managed.run("execute", Path(home)), 0)
            return json.loads(output.getvalue())

    def test_one_shot_and_recurring_create_return_persisted_rows(self):
        one_shot = self.execute({"action": "create", "message": "one", "subject": "S", "delayMinutes": 1})
        recurring = self.execute({"action": "create", "message": "many", "intervalMinutes": 5})
        self.assertEqual(one_shot["status"], "ok")
        self.assertEqual(recurring["status"], "ok")
        self.assertEqual(len(self.api.reminders), 2)
        self.assertEqual(set(self.api.calls[0][2]), {"id", "target", "creator", "body", "subject", "created_at", "next_due_at", "interval_ms"})
        self.assertEqual(self.api.calls[0][2]["target"], "managed-root")
        self.assertEqual(self.api.calls[0][2]["creator"], "managed-root")
        self.assertEqual(self.api.calls[0][2]["body"], "one")
        self.assertEqual(self.api.calls[0][2]["subject"], "S")
        self.assertEqual(self.api.reminders[0]["target"], "managed-root")
        self.assertEqual(self.api.reminders[0]["creator"], "managed-root")
        self.assertEqual(self.api.reminders[0]["next_due_at"], "2030-01-01T00:01:00.000Z")
        self.assertIsNone(self.api.reminders[0]["interval_ms"])
        self.assertEqual(self.api.reminders[1]["next_due_at"], "2030-01-01T00:05:00.000Z")
        self.assertEqual(self.api.reminders[1]["interval_ms"], 300000)
        self.assertEqual(one_shot["details"]["reminder"]["id"], self.api.reminders[0]["id"])

    def test_list_filters_and_mutations_read_ownership_first(self):
        self.api.reminders[:] = [
            {"id": "own", "target": "managed-root", "creator": "managed-root", "body": "old", "created_at": "2030-01-01T00:00:00.000Z", "next_due_at": "2030-01-01T00:01:00.000Z", "interval_ms": None},
            {"id": "foreign", "target": "other", "creator": "other", "body": "hidden", "created_at": "2030-01-01T00:00:00.000Z", "next_due_at": "2030-01-01T00:01:00.000Z", "interval_ms": None},
        ]
        listed = self.execute({"action": "list"})
        self.assertEqual([row["id"] for row in listed["details"]["reminders"]], ["own"])
        updated = self.execute({"action": "update", "reminderId": "own", "message": "new"})
        deleted = self.execute({"action": "delete", "reminderId": "own"})
        self.assertEqual(updated["status"], "ok")
        self.assertEqual(deleted["status"], "ok")
        self.assertEqual([(method, path) for method, path, _ in self.api.calls], [
            ("GET", "/v1/reminders"), ("GET", "/v1/reminders/own"), ("PATCH", "/v1/reminders/own"),
            ("GET", "/v1/reminders/own"), ("DELETE", "/v1/reminders/own"),
        ])
        self.assertEqual(self.api.calls[2][2], {"body": "new"})

    def test_absolute_create_normalizes_to_utc(self):
        created = self.execute({"action": "create", "message": "absolute", "scheduledAt": "2030-01-01T01:00:00+01:00"})
        self.assertEqual(created["status"], "ok")
        self.assertEqual(self.api.calls[0][2]["next_due_at"], "2030-01-01T00:00:00.000Z")
        self.assertIsNone(self.api.calls[0][2]["interval_ms"])

    def test_foreign_reminder_cannot_be_mutated(self):
        self.api.reminders[:] = [{"id": "foreign", "target": "other", "creator": "other", "body": "hidden", "created_at": "2030-01-01T00:00:00.000Z", "next_due_at": "2030-01-01T00:01:00.000Z", "interval_ms": None}]
        response = self.execute_via_run({"action": "update", "reminderId": "foreign", "message": "redirect"})
        self.assertEqual(response["status"], "error")
        self.assertEqual([method for method, _, _ in self.api.calls], ["GET"])

    def test_invalid_timing_unknown_target_and_bad_endpoint_response_are_errors(self):
        for action in (
            {"action": "create", "message": "bad"},
            {"action": "create", "message": "bad", "delayMinutes": 1, "intervalMinutes": 2},
            {"action": "create", "message": "bad", "scheduledAt": "2030-01-01T00:00:00"},
            {"action": "create", "message": "bad", "delayMinutes": 1, "window": "other"},
        ):
            self.assertEqual(self.execute_via_run(action)["status"], "error")
        self.assertEqual(self.api.calls, [])

        self.api.failure_status = 503
        self.assertEqual(self.execute_via_run({"action": "create", "message": "bad", "delayMinutes": 1})["status"], "error")
        self.assertEqual(len(self.api.reminders), 0)

        self.api.failure_status = None
        self.api.malformed = True
        self.assertEqual(self.execute_via_run({"action": "create", "message": "bad", "delayMinutes": 1})["status"], "error")
        self.assertEqual(len(self.api.reminders), 0)

    def test_request_ledger_replays_without_second_post(self):
        request = {"request_id": "request-1", "operation_id": "reminder.control", "arguments": {"action": "create", "message": "one", "delayMinutes": 1}, "fence": self.fence}
        with tempfile.TemporaryDirectory() as home, mock.patch.dict(managed.os.environ, {"MECHA_ROUTER_URL": self.api.url, "MECHA_AGENT_ID": "wrong-agent"}, clear=False), mock.patch.object(managed.time, "time", return_value=1893456000.0):
            outputs = []
            with mock.patch.object(managed.sys, "stdin", io.StringIO(json.dumps(request))), mock.patch.object(managed.sys, "stdout", io.StringIO()) as output:
                self.assertEqual(managed.run("execute", Path(home)), 0)
                outputs.append(output.getvalue())
            with mock.patch.object(managed.sys, "stdin", io.StringIO(json.dumps(request))), mock.patch.object(managed.sys, "stdout", io.StringIO()) as output:
                self.assertEqual(managed.run("execute", Path(home)), 0)
                outputs.append(output.getvalue())
        self.assertEqual(len(self.api.reminders), 1)
        self.assertEqual(json.loads(outputs[0]), json.loads(outputs[1]))
        self.assertEqual([method for method, _, _ in self.api.calls], ["POST"])


if __name__ == "__main__":
    unittest.main()
