#!/usr/bin/env python3
"""Focused relaunch tests for managed worker recovery."""

import contextlib
import io
import subprocess
import unittest
from unittest.mock import patch

import worker_relaunch_all


class WorkerRelaunchTest(unittest.TestCase):
    def _run(self, task, sidecar_clean=True, run_results=None):
        self.killed = []

        def kill_window(window):
            self.killed.append(window)
            return sidecar_clean

        run_patch = {"return_value": subprocess.CompletedProcess([], 0, stdout="", stderr="")}
        if run_results is not None:
            run_patch = {"side_effect": run_results}
        with patch.object(worker_relaunch_all.sys, "argv", ["worker_relaunch_all.py"]), \
             patch.object(worker_relaunch_all, "load_state", return_value={"tasks": [task]}), \
             patch.object(worker_relaunch_all, "kill_window", kill_window), \
             patch.object(worker_relaunch_all, "window_exists", lambda _window: True), \
             patch.object(worker_relaunch_all.os.path, "isdir", return_value=True), \
             patch.object(worker_relaunch_all.subprocess, "run", **run_patch) as run, \
             patch.object(worker_relaunch_all.time, "sleep"), \
             contextlib.redirect_stdout(io.StringIO()) as stdout, \
             contextlib.redirect_stderr(io.StringIO()) as stderr:
            try:
                self.return_code = worker_relaunch_all.main()
            finally:
                self.stdout = stdout.getvalue()
                self.stderr = stderr.getvalue()
        return [call.args[0] for call in run.call_args_list]

    def command_with(self, commands, needle):
        for command in commands:
            if any(needle in str(part) for part in command):
                return command
        self.fail(f"no command containing {needle!r} in {commands}")

    def test_pi_relaunch_passes_recorded_session_settled_notification_and_mailbox(self):
        commands = self._run(
            {
                "state": "active",
                "type": "worker",
                "window": "pi-worker",
                "alias": "pi-worker",
                "project": "/project",
                "agent": "pi",
                "session": "pi-session-123",
            }
        )

        launch = self.command_with(commands, "worker_launch.py")
        mailbox = self.command_with(commands, "mailbox_send.py")
        self.assertEqual(self.killed, ["pi-worker"])
        self.assertEqual(launch[launch.index("--agent") + 1], "pi")
        self.assertEqual(launch[launch.index("--resume") + 1], "pi-session-123")
        self.assertEqual(launch[launch.index("--notify-on-settled") + 1], "supervisor")
        self.assertEqual(mailbox[mailbox.index("--to") + 1], "pi-worker")

    def test_relaunch_without_session_leaves_window_running(self):
        commands = self._run(
            {
                "state": "active",
                "type": "worker",
                "window": "pi-worker",
                "alias": "pi-worker",
                "project": "/project",
                "agent": "pi",
            }
        )
        self.assertEqual(commands, [])

    def test_non_pi_relaunch_uses_recorded_runtime_and_session_without_settled_notification(self):
        commands = self._run(
            {
                "state": "active",
                "type": "worker",
                "window": "claude-worker",
                "alias": "claude-worker",
                "project": "/project",
                "agent": "claude",
                "session": "claude-session-123",
            }
        )

        launch = self.command_with(commands, "worker_launch.py")
        mailbox = self.command_with(commands, "mailbox_send.py")
        self.assertEqual(launch[launch.index("--agent") + 1], "claude")
        self.assertEqual(launch[launch.index("--resume") + 1], "claude-session-123")
        self.assertNotIn("--notify-on-settled", launch)
        self.assertEqual(mailbox[mailbox.index("--to") + 1], "claude-worker")

    def test_codex_relaunch_skips_mailbox_followup_without_verified_bridge(self):
        commands = self._run(
            {
                "state": "active",
                "type": "worker",
                "window": "codex-worker",
                "alias": "codex-worker",
                "project": "/project",
                "agent": "codex",
                "session": "codex-session-123",
            }
        )

        launch = self.command_with(commands, "worker_launch.py")
        self.assertEqual(launch[launch.index("--agent") + 1], "codex")
        self.assertEqual(launch[launch.index("--resume") + 1], "codex-session-123")
        self.assertFalse(any("mailbox_send.py" in command for command in commands for command in [" ".join(map(str, command))]))

    def test_cursor_relaunch_skips_mailbox_followup_without_verified_bridge(self):
        commands = self._run(
            {
                "state": "active",
                "type": "worker",
                "window": "cursor-worker",
                "alias": "cursor-worker",
                "project": "/project",
                "agent": "cursor",
                "session": "cursor-session-123",
            }
        )

        launch = self.command_with(commands, "worker_launch.py")
        self.assertEqual(launch[launch.index("--agent") + 1], "cursor")
        self.assertEqual(launch[launch.index("--resume") + 1], "cursor-session-123")
        self.assertFalse(any("mailbox_send.py" in command for command in commands for command in [" ".join(map(str, command))]))

    def test_suppressed_mailbox_failure_is_visible_and_nonzero(self):
        commands = self._run(
            {
                "state": "active",
                "type": "worker",
                "window": "pi-worker",
                "alias": "pi-worker",
                "project": "/project",
                "agent": "pi",
                "session": "pi-session-123",
            },
            run_results=[
                subprocess.CompletedProcess([], 0, stdout="", stderr=""),
                subprocess.CompletedProcess([], 1, stdout="", stderr=""),
            ],
        )

        self.assertEqual(self.return_code, 1)
        self.assertIn("Mailbox delivery failed (exit 1); details suppressed", self.stdout)
        self.assertNotIn("✓ Relaunched", self.stdout)
        self.assertIn("mailbox_send.py", " ".join(map(str, self.command_with(commands, "mailbox_send.py"))))

    def test_relaunch_aborts_when_a_sidecar_survives_its_window(self):
        with self.assertRaises(SystemExit) as raised:
            self._run(
                {
                    "state": "active",
                    "type": "worker",
                    "window": "pi-worker",
                    "alias": "pi-worker",
                    "project": "/project",
                    "agent": "pi",
                    "session": "pi-session-123",
                },
                sidecar_clean=False,
            )
        self.assertEqual(raised.exception.code, 1)
        self.assertIn("mailbox sidecar survived", self.stderr)
        self.assertIn("pi-worker", self.stderr)


if __name__ == "__main__":
    unittest.main()
