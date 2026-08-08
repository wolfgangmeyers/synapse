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


def write_session(path, records):
    path.write_text("\n".join(json.dumps(record) for record in records) + "\n")
    return path


def assistant_tool_call(call_id="call_ok", record_id="assistant-call"):
    return {
        "type": "message",
        "id": record_id,
        "timestamp": "2026-08-01T00:00:01.000Z",
        "message": {
            "role": "assistant",
            "content": [{"type": "toolCall", "id": f"{call_id}|fc_ok", "name": "bash", "arguments": {"command": "true"}}],
        },
    }


def tool_result(call_id="call_orphan", line_id="tool-result"):
    return {
        "type": "message",
        "id": line_id,
        "parentId": "compaction",
        "timestamp": "2026-08-01T00:00:03.000Z",
        "message": {
            "role": "toolResult",
            "toolCallId": f"{call_id}|fc_orphan",
            "toolName": "bash",
            "content": [{"type": "text", "text": "ok"}],
        },
    }


def missing_tool_call_error(call_id="call_orphan", line_id="error", suffix=""):
    return {
        "type": "message",
        "id": line_id,
        "timestamp": "2026-08-01T00:00:04.000Z",
        "message": {
            "role": "assistant",
            "content": [],
            "stopReason": "error",
            "errorMessage": f"No tool call found for function call output with call_id {call_id}.{suffix}",
        },
    }


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
            "interval_ms": 30 * 60 * 1_000, "body": "General priority sweep",
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
        with mock.patch.object(monitor, "maybe_recover_supervisor_tool_call_crash", return_value=False), mock.patch.object(monitor, "load_priority_evidence", return_value=(None, None, "router reminder evidence unavailable: refused")), contextlib.redirect_stderr(stderr):
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

    def test_single_missing_tool_call_error_does_not_recover(self):
        with tempfile.TemporaryDirectory() as directory:
            session = write_session(Path(directory) / "one.jsonl", [
                {"type": "session", "timestamp": "2026-08-01T00:00:00.000Z"},
                {"type": "compaction", "id": "compaction", "timestamp": "2026-08-01T00:00:02.000Z"},
                tool_result(),
                missing_tool_call_error(),
            ])
            self.assertIsNone(monitor.detect_orphan_tool_result_crash(session))

    def test_repeated_error_without_orphan_proof_alerts_without_recovery(self):
        with tempfile.TemporaryDirectory() as directory:
            session = write_session(Path(directory) / "matched.jsonl", [
                {"type": "session", "timestamp": "2026-08-01T00:00:00.000Z"},
                assistant_tool_call("call_orphan"),
                tool_result("call_orphan"),
                missing_tool_call_error("call_orphan", "error-1"),
                missing_tool_call_error("call_orphan", "error-2"),
            ])
            evidence = monitor.detect_orphan_tool_result_crash(session)
            self.assertIsNotNone(evidence)
            self.assertIn("orphan proof failed", evidence["alert"])

    def test_same_call_id_with_non_identical_errors_does_not_recover(self):
        with tempfile.TemporaryDirectory() as directory:
            session = write_session(Path(directory) / "different-errors.jsonl", [
                {"type": "session", "timestamp": "2026-08-01T00:00:00.000Z"},
                tool_result("call_broken"),
                missing_tool_call_error("call_broken", "error-1", " first"),
                missing_tool_call_error("call_broken", "error-2", " second"),
            ])
            self.assertIsNone(monitor.detect_orphan_tool_result_crash(session))

    def test_repeated_orphan_tool_result_error_is_strict_positive(self):
        with tempfile.TemporaryDirectory() as directory:
            session = write_session(Path(directory) / "corrupt.jsonl", [
                {"type": "session", "timestamp": "2026-08-01T00:00:00.000Z"},
                {"type": "compaction", "id": "compaction", "timestamp": "2026-08-01T00:00:02.000Z"},
                tool_result("call_broken"),
                missing_tool_call_error("call_broken", "error-1"),
                missing_tool_call_error("call_broken", "error-2"),
            ])
            evidence = monitor.detect_orphan_tool_result_crash(session)
            self.assertEqual(evidence["call_id"], "call_broken")
            self.assertEqual(evidence["orphan_tool_result_line"], 3)
            self.assertEqual(evidence["compaction_line"], 2)
            self.assertNotIn("alert", evidence)

    def test_newer_healthy_supervisor_session_suppresses_recovery(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            corrupt = write_session(root / "corrupt.jsonl", [
                {"type": "session", "timestamp": "2026-08-01T00:00:00.000Z"},
                tool_result("call_broken"),
                missing_tool_call_error("call_broken", "error-1"),
                missing_tool_call_error("call_broken", "error-2"),
            ])
            write_session(root / "fresh.jsonl", [
                {"type": "session", "timestamp": "2026-08-01T00:05:00.000Z"},
                {"type": "message", "message": {"role": "user", "content": [{"type": "text", "text": "Load the supervisor skill and operate as the supervisor."}]}},
                assistant_tool_call("call_ok"),
                {"type": "message", "message": {"role": "assistant", "content": [{"type": "text", "text": "healthy"}]}},
            ])
            self.assertTrue(monitor.newer_healthy_supervisor_session_exists(corrupt, root))

    def test_newer_healthy_session_with_unfamiliar_prompt_suppresses_recovery(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            corrupt = write_session(root / "old-corrupt.jsonl", [
                {"type": "session", "timestamp": "2026-08-01T00:00:00.000Z"},
                {"type": "message", "message": {"role": "user", "content": [{"type": "text", "text": "Load supervisor mode."}]}},
                tool_result("call_broken"),
                missing_tool_call_error("call_broken", "error-1"),
                missing_tool_call_error("call_broken", "error-2"),
            ])
            write_session(root / "new-healthy-unrecognized.jsonl", [
                {"type": "session", "timestamp": "2026-08-01T00:05:00.000Z"},
                {"type": "message", "message": {"role": "user", "content": [{"type": "text", "text": "Continue the ops hub from local state."}]}},
                assistant_tool_call("call_ok"),
                {"type": "message", "message": {"role": "assistant", "content": [{"type": "text", "text": "healthy"}]}},
            ])
            self.assertFalse(monitor.session_looks_like_supervisor(root / "new-healthy-unrecognized.jsonl"))
            self.assertTrue(monitor.newer_healthy_supervisor_session_exists(corrupt, root))

    def test_recovery_command_targets_only_supervisor_window(self):
        evidence = {"session_path": "/tmp/corrupt.jsonl", "call_id": "call_broken"}
        with tempfile.TemporaryDirectory() as directory, mock.patch.object(monitor, "RECOVERY_SENTINEL", Path(directory) / "sentinel.json"), mock.patch.object(monitor, "RECOVERY_LOG", Path(directory) / "recovery.log"), mock.patch.object(monitor, "newer_healthy_supervisor_session_exists", return_value=False), mock.patch.object(monitor.subprocess, "run") as run:
            self.assertTrue(monitor.recover_supervisor_from_orphan_crash(evidence))
        run.assert_called_once()
        argv = run.call_args.args[0]
        self.assertEqual(argv[:4], ["tmux", "respawn-window", "-k", "-t"])
        self.assertEqual(argv[4], "mecha:supervisor")
        self.assertNotIn("kill-session", argv)
        self.assertNotIn("kill-window", argv)

    def test_failed_recovery_does_not_write_success_sentinel(self):
        evidence = {"session_path": "/tmp/corrupt.jsonl", "call_id": "call_broken"}
        stderr = io.StringIO()
        with tempfile.TemporaryDirectory() as directory, mock.patch.object(monitor, "RECOVERY_SENTINEL", Path(directory) / "sentinel.json"), mock.patch.object(monitor, "RECOVERY_LOG", Path(directory) / "recovery.log"), mock.patch.object(monitor, "newer_healthy_supervisor_session_exists", return_value=False), mock.patch.object(monitor.subprocess, "run", side_effect=monitor.subprocess.CalledProcessError(1, ["tmux"])), contextlib.redirect_stderr(stderr):
            self.assertFalse(monitor.recover_supervisor_from_orphan_crash(evidence))
            sentinel = json.loads((Path(directory) / "sentinel.json").read_text())
        self.assertEqual(sentinel["status"], "failed")
        self.assertFalse(monitor.recovery_already_attempted(evidence, now=sentinel["attempted_at_epoch"] + monitor.RECOVERY_DEBOUNCE_SECONDS + 1))

    def test_corrupt_sentinel_shape_does_not_crash_recovery_check(self):
        evidence = {"session_path": "/tmp/corrupt.jsonl", "call_id": "call_broken"}
        with tempfile.TemporaryDirectory() as directory, mock.patch.object(monitor, "RECOVERY_SENTINEL", Path(directory) / "sentinel.json"):
            (Path(directory) / "sentinel.json").write_text("[]")
            self.assertFalse(monitor.recovery_already_attempted(evidence))

    def test_non_utf8_sentinel_does_not_crash_recovery_check(self):
        evidence = {"session_path": "/tmp/corrupt.jsonl", "call_id": "call_broken"}
        with tempfile.TemporaryDirectory() as directory, mock.patch.object(monitor, "RECOVERY_SENTINEL", Path(directory) / "sentinel.json"):
            (Path(directory) / "sentinel.json").write_bytes(b"\xff\xfe\xfd")
            self.assertFalse(monitor.recovery_already_attempted(evidence))

    def test_recovery_handoff_contains_corrupted_session_path_and_call_id(self):
        evidence = {"session_path": "/tmp/corrupted-supervisor.jsonl", "call_id": "call_broken"}
        prompt = monitor.recovery_handoff_prompt(evidence)
        self.assertIn("/tmp/corrupted-supervisor.jsonl", prompt)
        self.assertIn("call_broken", prompt)
        self.assertIn("Do not resume", prompt)

    def test_run_monitor_stops_after_detected_crash_even_if_recovery_fails(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            session = write_session(root / "corrupt.jsonl", [
                {"type": "session", "timestamp": "2026-08-01T00:00:00.000Z"},
                {"type": "message", "message": {"role": "user", "content": [{"type": "text", "text": "Load supervisor mode."}]}},
                tool_result("call_broken"),
                missing_tool_call_error("call_broken", "error-1"),
                missing_tool_call_error("call_broken", "error-2"),
            ])
            stdout = io.StringIO()
            stderr = io.StringIO()
            with mock.patch.object(monitor, "RECOVERY_SENTINEL", root / "sentinel.json"), mock.patch.object(monitor, "RECOVERY_LOG", root / "recovery.log"), mock.patch.object(monitor, "recent_supervisor_sessions", return_value=[session]), mock.patch.object(monitor, "newer_healthy_supervisor_session_exists", return_value=False), mock.patch.object(monitor, "load_priority_evidence") as load_priority_evidence, mock.patch.object(monitor.subprocess, "run", side_effect=monitor.subprocess.CalledProcessError(1, ["tmux"])), contextlib.redirect_stdout(stdout), contextlib.redirect_stderr(stderr):
                self.assertEqual(monitor.run_monitor(), 1)
            load_priority_evidence.assert_not_called()


    def test_default_sweep_cadence_bounds_are_thirty_minutes(self):
        self.assertEqual(monitor.sweep_cadence_bounds(), (30 * 60 * 1_000, 30 * 60 * 1_000))
        self.assertEqual(monitor.describe_cadence(30 * 60 * 1_000, 30 * 60 * 1_000), "30 minute")

    def test_describe_cadence_for_a_single_value_window(self):
        self.assertEqual(monitor.describe_cadence(30 * 60 * 1_000, 30 * 60 * 1_000), "30 minute")

    def test_invalid_sweep_cadence_env_overrides_fall_back_to_defaults(self):
        with mock.patch.dict(os.environ, {"MECHA_SWEEP_CADENCE_MIN_MS": "garbage", "MECHA_SWEEP_CADENCE_MAX_MS": "-5"}):
            self.assertEqual(monitor.sweep_cadence_bounds(), (30 * 60 * 1_000, 30 * 60 * 1_000))

    def _clean_active_state(self):
        # Active priority with a fully-handed-off task: with a conforming sweep
        # and a live verification reminder this produces no findings.
        return {
            "priorities": [{"id": "p-1", "name": "PR", "status": "active", "task_ids": [12]}],
            "tasks": [{
                "id": 12, "state": "active", "type": "worker",
                "verification_handoff": {
                    "reminder_id": "ci", "verification_started_at": "2026-07-23T00:00:00Z",
                    "remote_verified": False, "successor": "CI watcher",
                },
            }],
            "priority_control": {},
        }

    def test_env_override_widens_general_sweep_cadence_window(self):
        state = self._clean_active_state()
        sweep = {"id": "sweep", "target": "supervisor", "state": "scheduled",
                 "interval_ms": 30 * 60 * 1_000, "body": "General priority sweep"}
        ci = {"id": "ci", "target": "supervisor", "state": "scheduled"}
        # The default 30-minute window accepts the cost-reduced sweep.
        self.assertEqual(monitor.priority_liveness_findings(state, [sweep, ci]), [])
        # Narrowing the ceiling rejects it; the "exactly one" requirement still holds.
        with mock.patch.dict(os.environ, {"MECHA_SWEEP_CADENCE_MAX_MS": str(10 * 60 * 1_000)}):
            self.assertTrue(any("general sweep reminder" in item for item in monitor.priority_liveness_findings(state, [sweep, ci])))

    def test_env_override_min_raises_general_sweep_floor(self):
        state = self._clean_active_state()
        sweep = {"id": "sweep", "target": "supervisor", "state": "scheduled",
                 "interval_ms": 5 * 60 * 1_000, "body": "General priority sweep"}
        ci = {"id": "ci", "target": "supervisor", "state": "scheduled"}
        # 5-minute sweep is now too frequent by default.
        self.assertTrue(any("general sweep reminder" in item for item in monitor.priority_liveness_findings(state, [sweep, ci])))
        # Lowering the floor back to 5 minutes accepts it.
        with mock.patch.dict(os.environ, {"MECHA_SWEEP_CADENCE_MIN_MS": str(5 * 60 * 1_000)}):
            findings = monitor.priority_liveness_findings(state, [sweep, ci])
        self.assertEqual(findings, [])


if __name__ == "__main__":
    unittest.main()
