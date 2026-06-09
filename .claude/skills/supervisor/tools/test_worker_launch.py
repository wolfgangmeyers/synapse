#!/usr/bin/env python3
"""Tests for worker_launch helper behavior."""

import contextlib
import io
import json
import os
import pathlib
import tempfile
import textwrap
import unittest
from unittest.mock import patch

import worker_launch


class WorkerLaunchReminderTest(unittest.TestCase):
    def setUp(self):
        self.tmpdir = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmpdir.cleanup)
        self.tmp_path = pathlib.Path(self.tmpdir.name)

    def _write_tool(self, body):
        tool = self.tmp_path / "set_reminder.py"
        tool.write_text(textwrap.dedent(body), encoding="utf-8")
        return tool

    def test_post_launch_reminder_invokes_existing_tool(self):
        args_file = self.tmp_path / "args.json"
        tool = self._write_tool(
            f"""
            import json
            import sys

            with open({str(args_file)!r}, "w", encoding="utf-8") as fh:
                json.dump(sys.argv[1:], fh)
            print("Reminder set: r-test")
            """
        )

        stdout = io.StringIO()
        with contextlib.redirect_stdout(stdout):
            reminder_id = worker_launch._schedule_post_launch_reminder(
                "worker-1",
                "launched",
                reminder_tool=tool,
                supervisor_window="supervisor",
            )

        self.assertEqual(reminder_id, "r-test")
        self.assertIn("Post-launch verification reminder set: r-test", stdout.getvalue())

        argv = json.loads(args_file.read_text(encoding="utf-8"))
        self.assertEqual(argv[0:2], ["--window", "supervisor"])
        self.assertEqual(argv[2], "--message")
        self.assertIn("worker-1", argv[3])
        self.assertIn("accepted its task instructions", argv[3])
        self.assertIn("not merely that the agent process launched", argv[3])
        self.assertEqual(argv[4:6], ["--in", worker_launch.POST_LAUNCH_REMINDER_DELAY])

    def test_post_launch_reminder_warns_when_tool_is_missing(self):
        missing_tool = self.tmp_path / "missing.py"

        stderr = io.StringIO()
        with contextlib.redirect_stderr(stderr):
            reminder_id = worker_launch._schedule_post_launch_reminder(
                "worker-2",
                "resumed",
                reminder_tool=missing_tool,
                supervisor_window="supervisor",
            )

        self.assertIsNone(reminder_id)
        warning = stderr.getvalue()
        self.assertIn("Warning: could not set post-launch verification reminder", warning)
        self.assertIn("reminder tool not found", warning)
        self.assertIn("Set it manually:", warning)
        self.assertIn("worker-2", warning)

    def test_post_launch_reminder_warns_when_tool_fails(self):
        tool = self._write_tool(
            """
            import sys

            print("no reminder for you", file=sys.stderr)
            sys.exit(7)
            """
        )

        stderr = io.StringIO()
        with contextlib.redirect_stderr(stderr):
            reminder_id = worker_launch._schedule_post_launch_reminder(
                "worker-3",
                "launched",
                reminder_tool=tool,
                supervisor_window="supervisor",
            )

        self.assertIsNone(reminder_id)
        warning = stderr.getvalue()
        self.assertIn("exited 7", warning)
        self.assertIn("stderr: no reminder for you", warning)
        self.assertIn("Set it manually:", warning)
        self.assertIn("worker-3", warning)

    def test_post_launch_reminder_warns_when_tool_times_out(self):
        tool = self._write_tool("")

        stderr = io.StringIO()
        with patch.object(
            worker_launch.subprocess,
            "run",
            side_effect=worker_launch.subprocess.TimeoutExpired(
                cmd="set_reminder.py",
                timeout=worker_launch.POST_LAUNCH_REMINDER_TIMEOUT_SECONDS,
            ),
        ), contextlib.redirect_stderr(stderr):
            reminder_id = worker_launch._schedule_post_launch_reminder(
                "worker-timeout",
                "launched",
                reminder_tool=tool,
                supervisor_window="supervisor",
            )

        self.assertIsNone(reminder_id)
        warning = stderr.getvalue()
        self.assertIn("timed out", warning)
        self.assertIn("Set it manually:", warning)
        self.assertIn("worker-timeout", warning)

    def test_post_launch_reminder_warns_when_tool_cannot_start(self):
        tool = self._write_tool("")

        stderr = io.StringIO()
        with patch.object(
            worker_launch.subprocess,
            "run",
            side_effect=OSError("permission denied"),
        ), contextlib.redirect_stderr(stderr):
            reminder_id = worker_launch._schedule_post_launch_reminder(
                "worker-oserror",
                "launched",
                reminder_tool=tool,
                supervisor_window="supervisor",
            )

        self.assertIsNone(reminder_id)
        warning = stderr.getvalue()
        self.assertIn("permission denied", warning)
        self.assertIn("Set it manually:", warning)
        self.assertIn("worker-oserror", warning)

    def test_supervisor_window_can_come_from_environment(self):
        tool = self._write_tool(
            """
            import sys

            if sys.argv[sys.argv.index('--window') + 1] != "supervisor-alt":
                sys.exit(3)
            print("Reminder set: r-env")
            """
        )

        old_value = os.environ.get("MECHA_SUPERVISOR_WINDOW")
        os.environ["MECHA_SUPERVISOR_WINDOW"] = "supervisor-alt"
        self.addCleanup(self._restore_env, "MECHA_SUPERVISOR_WINDOW", old_value)

        stdout = io.StringIO()
        with contextlib.redirect_stdout(stdout):
            reminder_id = worker_launch._schedule_post_launch_reminder(
                "worker-4",
                "launched",
                reminder_tool=tool,
            )

        self.assertEqual(reminder_id, "r-env")

    def test_main_schedules_post_launch_reminder_after_successful_launch(self):
        calls = self._run_main_with_stubs("worker-main-launch")

        self.assertEqual(calls, [("worker-main-launch", "launched")])

    def test_main_schedules_post_launch_reminder_after_successful_resume(self):
        calls = self._run_main_with_stubs(
            "worker-main-resume",
            "--resume",
            "session-123",
        )

        self.assertEqual(calls, [("worker-main-resume", "resumed")])

    def _run_main_with_stubs(self, window, *extra_args):
        calls = []
        argv = [
            "worker_launch.py",
            "--window",
            window,
            "--dir",
            self.tmpdir.name,
            "--agent",
            "claude",
            *extra_args,
        ]

        def record_reminder(worker_window, action):
            calls.append((worker_window, action))

        with patch.object(worker_launch.sys, "argv", argv), \
             patch.object(worker_launch, "window_exists", return_value=False), \
             patch.object(worker_launch, "ensure_session", return_value=False), \
             patch.object(worker_launch, "new_window"), \
             patch.object(worker_launch, "send_text"), \
             patch.object(worker_launch, "send_enter"), \
             patch.object(worker_launch.time, "sleep"), \
             patch.object(worker_launch.os, "makedirs"), \
             patch.object(worker_launch.os.path, "exists", return_value=True), \
             patch.object(worker_launch, "_schedule_post_launch_reminder", record_reminder), \
             contextlib.redirect_stdout(io.StringIO()):
            worker_launch.main()

        return calls

    def _restore_env(self, key, value):
        if value is None:
            os.environ.pop(key, None)
        else:
            os.environ[key] = value


if __name__ == "__main__":
    unittest.main()
