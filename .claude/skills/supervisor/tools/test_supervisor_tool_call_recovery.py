#!/usr/bin/env python3
"""Tests for supervisor orphan-tool-result recovery."""

import contextlib
import importlib.util
import io
import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest import mock


MODULE_PATH = Path(__file__).with_name("supervisor_tool_call_recovery.py")
SPEC = importlib.util.spec_from_file_location("supervisor_tool_call_recovery", MODULE_PATH)
recovery = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = recovery
SPEC.loader.exec_module(recovery)


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
            "content": [{
                "type": "toolCall",
                "id": f"{call_id}|fc_ok",
                "name": "bash",
                "arguments": {"command": "true"},
            }],
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


class SupervisorToolCallRecoveryTests(unittest.TestCase):
    def test_single_missing_tool_call_error_does_not_recover(self):
        with tempfile.TemporaryDirectory() as directory:
            session = write_session(Path(directory) / "one.jsonl", [
                {"type": "session", "timestamp": "2026-08-01T00:00:00.000Z"},
                {"type": "compaction", "id": "compaction", "timestamp": "2026-08-01T00:00:02.000Z"},
                tool_result(),
                missing_tool_call_error(),
            ])
            self.assertIsNone(recovery.detect_orphan_tool_result_crash(session))

    def test_repeated_error_without_orphan_proof_reports_warning(self):
        with tempfile.TemporaryDirectory() as directory:
            session = write_session(Path(directory) / "matched.jsonl", [
                {"type": "session", "timestamp": "2026-08-01T00:00:00.000Z"},
                assistant_tool_call("call_orphan"),
                tool_result("call_orphan"),
                missing_tool_call_error("call_orphan", "error-1"),
                missing_tool_call_error("call_orphan", "error-2"),
            ])
            evidence = recovery.detect_orphan_tool_result_crash(session)
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
            self.assertIsNone(recovery.detect_orphan_tool_result_crash(session))

    def test_repeated_orphan_tool_result_error_is_strict_positive(self):
        with tempfile.TemporaryDirectory() as directory:
            session = write_session(Path(directory) / "corrupt.jsonl", [
                {"type": "session", "timestamp": "2026-08-01T00:00:00.000Z"},
                {"type": "compaction", "id": "compaction", "timestamp": "2026-08-01T00:00:02.000Z"},
                tool_result("call_broken"),
                missing_tool_call_error("call_broken", "error-1"),
                missing_tool_call_error("call_broken", "error-2"),
            ])
            evidence = recovery.detect_orphan_tool_result_crash(session)
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
            self.assertTrue(recovery.newer_healthy_supervisor_session_exists(corrupt, root))

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
            self.assertFalse(recovery.session_looks_like_supervisor(root / "new-healthy-unrecognized.jsonl"))
            self.assertTrue(recovery.newer_healthy_supervisor_session_exists(corrupt, root))

    def test_recovery_command_targets_only_supervisor_window(self):
        evidence = {"session_path": "/tmp/corrupt.jsonl", "call_id": "call_broken"}
        with tempfile.TemporaryDirectory() as directory, \
                mock.patch.object(recovery, "RECOVERY_SENTINEL", Path(directory) / "sentinel.json"), \
                mock.patch.object(recovery, "RECOVERY_LOG", Path(directory) / "recovery.log"), \
                mock.patch.object(recovery, "newer_healthy_supervisor_session_exists", return_value=False), \
                mock.patch.object(recovery.subprocess, "run") as run:
            self.assertTrue(recovery.recover_supervisor_from_orphan_crash(evidence))
        run.assert_called_once()
        argv = run.call_args.args[0]
        self.assertEqual(argv[:4], ["tmux", "respawn-window", "-k", "-t"])
        self.assertEqual(argv[4], "mecha:supervisor")
        self.assertNotIn("kill-session", argv)
        self.assertNotIn("kill-window", argv)

    def test_recovery_command_uses_managed_supervisor_runtime(self):
        command = recovery.recovery_tmux_command({
            "session_path": "/tmp/corrupt.jsonl",
            "call_id": "call_broken",
        })
        self.assertIn("--provider openai-codex", command)
        self.assertIn("--model gpt-5.6-terra", command)
        self.assertIn("MECHA_PI_MODEL_KEY=openai-codex/gpt-5.6-terra", command)

    def test_dry_run_does_not_write_recovery_state(self):
        evidence = {"session_path": "/tmp/corrupt.jsonl", "call_id": "call_broken"}
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            sentinel = root / "nested" / "sentinel.json"
            log = root / "nested" / "recovery.log"
            with mock.patch.object(recovery, "RECOVERY_SENTINEL", sentinel), \
                    mock.patch.object(recovery, "RECOVERY_LOG", log), \
                    mock.patch.object(recovery, "newer_healthy_supervisor_session_exists", return_value=False):
                output = io.StringIO()
                with contextlib.redirect_stdout(output):
                    self.assertTrue(recovery.recover_supervisor_from_orphan_crash(
                        evidence, dry_run=True
                    ))
            self.assertIn("--name supervisor", output.getvalue())
            self.assertFalse(sentinel.exists())
            self.assertFalse(sentinel.with_suffix(sentinel.suffix + ".lock").exists())
            self.assertFalse(log.exists())

    def test_failed_recovery_does_not_write_success_sentinel(self):
        evidence = {"session_path": "/tmp/corrupt.jsonl", "call_id": "call_broken"}
        stderr = io.StringIO()
        with tempfile.TemporaryDirectory() as directory, \
                mock.patch.object(recovery, "RECOVERY_SENTINEL", Path(directory) / "sentinel.json"), \
                mock.patch.object(recovery, "RECOVERY_LOG", Path(directory) / "recovery.log"), \
                mock.patch.object(recovery, "newer_healthy_supervisor_session_exists", return_value=False), \
                mock.patch.object(recovery.subprocess, "run", side_effect=recovery.subprocess.CalledProcessError(1, ["tmux"])), \
                contextlib.redirect_stderr(stderr):
            self.assertFalse(recovery.recover_supervisor_from_orphan_crash(evidence))
            sentinel = json.loads((Path(directory) / "sentinel.json").read_text())
        self.assertEqual(sentinel["status"], "failed")
        self.assertFalse(recovery.recovery_already_attempted(
            evidence, now=sentinel["attempted_at_epoch"] + recovery.RECOVERY_DEBOUNCE_SECONDS + 1
        ))

    def test_corrupt_sentinel_shape_does_not_crash_recovery_check(self):
        evidence = {"session_path": "/tmp/corrupt.jsonl", "call_id": "call_broken"}
        with tempfile.TemporaryDirectory() as directory, \
                mock.patch.object(recovery, "RECOVERY_SENTINEL", Path(directory) / "sentinel.json"):
            (Path(directory) / "sentinel.json").write_text("[]")
            self.assertFalse(recovery.recovery_already_attempted(evidence))

    def test_non_utf8_sentinel_does_not_crash_recovery_check(self):
        evidence = {"session_path": "/tmp/corrupt.jsonl", "call_id": "call_broken"}
        with tempfile.TemporaryDirectory() as directory, \
                mock.patch.object(recovery, "RECOVERY_SENTINEL", Path(directory) / "sentinel.json"):
            (Path(directory) / "sentinel.json").write_bytes(b"\xff\xfe\xfd")
            self.assertFalse(recovery.recovery_already_attempted(evidence))

    def test_recovery_handoff_contains_corrupted_session_path_and_call_id(self):
        evidence = {"session_path": "/tmp/corrupted-supervisor.jsonl", "call_id": "call_broken"}
        prompt = recovery.recovery_handoff_prompt(evidence)
        self.assertIn("/tmp/corrupted-supervisor.jsonl", prompt)
        self.assertIn("call_broken", prompt)
        self.assertIn("Do not resume", prompt)

    def test_run_recovery_stops_after_detected_crash_even_if_recovery_fails(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            session = write_session(root / "corrupt.jsonl", [
                {"type": "session", "timestamp": "2026-08-01T00:00:00.000Z"},
                {"type": "message", "message": {"role": "user", "content": [{"type": "text", "text": "Load supervisor mode."}]}},
                tool_result("call_broken"),
                missing_tool_call_error("call_broken", "error-1"),
                missing_tool_call_error("call_broken", "error-2"),
            ])
            with mock.patch.object(recovery, "RECOVERY_SENTINEL", root / "sentinel.json"), \
                    mock.patch.object(recovery, "RECOVERY_LOG", root / "recovery.log"), \
                    mock.patch.object(recovery, "recent_supervisor_sessions", return_value=[session]), \
                    mock.patch.object(recovery, "newer_healthy_supervisor_session_exists", return_value=False), \
                    mock.patch.object(recovery.subprocess, "run", side_effect=recovery.subprocess.CalledProcessError(1, ["tmux"])):
                self.assertEqual(recovery.run_recovery(), 1)


if __name__ == "__main__":
    unittest.main()
