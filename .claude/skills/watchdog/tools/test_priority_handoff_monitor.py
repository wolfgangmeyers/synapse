#!/usr/bin/env python3
"""Isolated fixtures for the Priority Handoff Monitor."""

import contextlib
import importlib.util
import io
import json
import os
from pathlib import Path
import sys
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from unittest import mock


MODULE_PATH = Path(__file__).with_name("priority_handoff_monitor.py")
SPEC = importlib.util.spec_from_file_location("priority_handoff_monitor", MODULE_PATH)
monitor = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = monitor
SPEC.loader.exec_module(monitor)


@contextlib.contextmanager
def router_fixture(reminders):
    class Handler(BaseHTTPRequestHandler):
        canceled = []

        def do_GET(self):
            if self.path != "/v1/reminders":
                self.send_error(404)
                return
            body = json.dumps({"reminders": reminders}).encode()
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def do_DELETE(self):
            reminder_id = self.path.rsplit("/", 1)[-1]
            Handler.canceled.append(reminder_id)
            self.send_response(204)
            self.end_headers()

        def log_message(self, *_args):
            pass

    server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        yield f"http://127.0.0.1:{server.server_port}", Handler.canceled
    finally:
        server.shutdown()
        thread.join()
        server.server_close()


class PriorityHandoffMonitorTests(unittest.TestCase):
    def test_every_actionable_finding_names_owner_action_and_proof(self):
        state = {
            "priorities": [{"id": "p-1", "name": "release", "status": "active", "task_ids": [9]}],
            "tasks": [{"id": 9, "state": "completed", "verification_handoff": {"reminder_id": "missing"}}],
            "priority_control": {"watchdog_reminder_id": "legacy-control"},
        }
        findings = monitor.priority_liveness_findings(state, [])
        self.assertTrue(findings)
        for finding in findings:
            self.assertTrue(finding.startswith(("WARN owner=", "FAIL owner=")), finding)
            self.assertIn("; action=", finding)
            self.assertIn("; resolved-when=", finding)

    def test_delivery_tasks_without_priority_or_general_sweep_fail_loudly(self):
        state = {
            "priorities": [],
            "tasks": [{"id": 12, "state": "active", "type": "worker"}],
            "priority_control": {},
        }
        findings = monitor.priority_liveness_findings(state, [])
        self.assertTrue(any("no active priority is recorded" in item for item in findings))
        self.assertTrue(any("general sweep reminder" in item for item in findings))

    def test_active_priority_requires_linked_task_handoff_and_live_reminder(self):
        state = {
            "priorities": [{"id": "p-1502", "name": "PR #1502", "status": "active", "task_ids": [12]}],
            "tasks": [{"id": 12, "state": "active", "type": "worker"}],
            "priority_control": {},
        }
        sweep = {
            "id": "sweep", "target": "supervisor", "state": "scheduled",
            "interval_ms": 300_000, "body": "General priority sweep",
        }
        findings = monitor.priority_liveness_findings(state, [sweep])
        self.assertEqual(len(findings), 1)
        self.assertIn("has no verification handoff", findings[0])

        state["tasks"][0]["verification_handoff"] = {
            "reminder_id": "ci", "verification_started_at": "2026-07-23T00:00:00Z",
            "remote_verified": False, "successor": "CI watcher",
        }
        ci = {"id": "ci", "target": "supervisor", "state": "scheduled"}
        self.assertEqual(monitor.priority_liveness_findings(state, [sweep, ci]), [])

    def test_terminal_legacy_ids_are_ignored_only_after_exact_retirement(self):
        failed = {"id": "legacy-dead", "target": "watchdog", "state": "failed", "linked_message_state": "dead_letter"}
        state = {
            "priorities": [], "tasks": [],
            "priority_control": {monitor.RETIREMENT_KEY: {monitor.RETIREMENT_ID_KEY: ["legacy-dead"]}},
        }
        self.assertEqual(monitor.priority_liveness_findings(state, [failed]), [])
        failed["id"] = "current-dead"
        finding = monitor.priority_liveness_findings(state, [failed])[0]
        self.assertTrue(finding.startswith("FAIL owner=supervisor"))
        self.assertIn("investigate the current Watchdog delivery", finding)
        self.assertIn("resolved-when=", finding)

    def test_retirement_plan_is_bounded_and_never_creates_a_reminder(self):
        state = {"priorities": [], "tasks": [], "priority_control": {"watchdog_reminder_id": "legacy-reference"}}
        reminders = [
            {"id": "failed", "target": "watchdog", "state": "failed", "linked_message_state": "dead_letter"},
            {"id": "scheduled", "target": "watchdog", "state": "scheduled", "linked_message_id": None, "linked_message_state": None},
        ]
        plan = monitor.legacy_control_plan(state, reminders)
        self.assertEqual(plan["retire_ids"], ["failed", "scheduled"])
        self.assertEqual(plan["cancel_ids"], ["scheduled"])
        self.assertTrue(plan["clear_reference"])
        self.assertNotIn("create", plan)

    def test_reconcile_uses_isolated_state_and_router_fixtures(self):
        reminders = [
            {"id": "failed", "target": "watchdog", "state": "failed", "linked_message_state": "dead_letter"},
            {"id": "scheduled", "target": "watchdog", "state": "scheduled", "linked_message_id": None, "linked_message_state": None},
        ]
        with tempfile.TemporaryDirectory() as directory, router_fixture(reminders) as (router_url, canceled):
            state_file = Path(directory) / "supervisor-state.json"
            state_file.write_text(json.dumps({"priorities": [], "tasks": [], "priority_control": {"watchdog_reminder_id": "stale"}}))
            stdout = io.StringIO()
            with contextlib.redirect_stdout(stdout):
                self.assertEqual(monitor.reconcile_retired_controls(str(state_file), router_url), 0)
            state = json.loads(state_file.read_text())
            control = state["priority_control"]
            self.assertNotIn("watchdog_reminder_id", control)
            self.assertEqual(control[monitor.RETIREMENT_KEY][monitor.RETIREMENT_ID_KEY], ["failed", "scheduled"])
            self.assertEqual(canceled, ["scheduled"])
            self.assertIn("RETIRED: legacy Watchdog controls", stdout.getvalue())
            self.assertIn("action=clear/ignore their liveness reference, not recreate a reminder", stdout.getvalue())
            self.assertIn("resolved-when=", stdout.getvalue())

    def test_schedule_reconciliation_replaces_only_priority_runtime(self):
        crontab = (
            "*/30 * * * * python3 /old/watchdog.py\n"
            "*/5 * * * * python3 /old/watchdog.py --priority-only # mecha-priority-watchdog\n"
            "@daily /bin/true\n"
        )
        reconciled = monitor.reconcile_schedule(crontab)
        self.assertIn("*/30 * * * * python3 /old/watchdog.py", reconciled)
        self.assertNotIn("--priority-only", reconciled)
        self.assertEqual(reconciled.count(monitor.SCHEDULE_MARKER), 1)
        self.assertIn("@daily /bin/true", reconciled)

    def test_schedule_install_writes_one_new_marker(self):
        listed = mock.Mock(returncode=0, stdout="*/5 * * * * python3 /old/watchdog.py --priority-only\n", stderr="")
        applied = mock.Mock(returncode=0, stdout="", stderr="")
        with mock.patch.object(monitor.subprocess, "run", side_effect=[listed, applied]) as run:
            monitor.install_schedule()
        installed = run.call_args_list[1].kwargs["input"]
        self.assertEqual(installed.count(monitor.SCHEDULE_MARKER), 1)
        self.assertNotIn("--priority-only", installed)

    def test_monitor_failure_has_a_useful_remediation(self):
        stderr = io.StringIO()
        with mock.patch.object(monitor, "load_priority_evidence", return_value=(None, None, "router reminder evidence unavailable: refused")), contextlib.redirect_stderr(stderr):
            self.assertEqual(monitor.run_monitor(), 1)
        output = stderr.getvalue()
        self.assertIn("FAIL owner=local operator", output)
        self.assertIn("action=restore readable supervisor state and reachable Mailbox router", output)
        self.assertIn("resolved-when=", output)

    def test_install_guidance_is_portable_and_complete(self):
        install_skill = MODULE_PATH.parents[2] / "install" / "SKILL.md"
        documentation = install_skill.read_text()
        self.assertIn("Install the Priority Handoff Monitor", documentation)
        self.assertIn("MECHA_ROUTER_URL", documentation)
        self.assertIn("--install-schedule", documentation)
        self.assertIn("--reconcile-retired-controls", documentation)
        self.assertIn("--remove-schedule", documentation)
        self.assertIn("priority-handoff-monitor.log", documentation)
        self.assertIn("no Git-hook requirement", documentation)
        self.assertNotIn("/Users/wolfgang", documentation)

    def test_log_cap_is_bounded(self):
        with tempfile.NamedTemporaryFile() as log_file:
            log_file.write(b"x" * (monitor.LOG_MAX_BYTES + 1))
            log_file.flush()
            with mock.patch.dict(os.environ, {"MECHA_PRIORITY_HANDOFF_MONITOR_LOG": log_file.name}):
                monitor.cap_log_file()
            self.assertLessEqual(os.path.getsize(log_file.name), monitor.LOG_MAX_BYTES // 2)


if __name__ == "__main__":
    unittest.main()
