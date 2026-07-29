#!/usr/bin/env python3
"""Focused relaunch tests for Mailbox V2 Pi recovery."""

import contextlib
import io
import subprocess
import unittest
from unittest.mock import patch

import worker_relaunch_all


class WorkerRelaunchTest(unittest.TestCase):
    def _run(self, task, sidecar_clean=True):
        # kill_window is patched because it now sweeps the mailbox sidecar (and would
        # otherwise reach real tmux and real processes). self.killed records the windows
        # it was asked to end.
        self.killed = []

        def kill_window(window):
            self.killed.append(window)
            return sidecar_clean

        with patch.object(worker_relaunch_all.sys, "argv", ["worker_relaunch_all.py"]), \
             patch.object(worker_relaunch_all, "load_state", return_value={"tasks": [task]}), \
             patch.object(worker_relaunch_all, "kill_window", kill_window), \
             patch.object(worker_relaunch_all, "window_exists", lambda _window: True), \
             patch.object(worker_relaunch_all.os.path, "isdir", return_value=True), \
             patch.object(
                 worker_relaunch_all.subprocess,
                 "run",
                 return_value=subprocess.CompletedProcess([], 0, stdout="", stderr=""),
             ) as run, \
             patch.object(worker_relaunch_all.time, "sleep"), \
             contextlib.redirect_stdout(io.StringIO()), \
             contextlib.redirect_stderr(io.StringIO()) as stderr:
            try:
                worker_relaunch_all.main()
            finally:
                # Set even when main() exits non-zero, so the abort path can assert on it.
                self.stderr = stderr.getvalue()
        return [call.args[0] for call in run.call_args_list]

    def command_with(self, commands, needle):
        for command in commands:
            if any(needle in str(part) for part in command):
                return command
        self.fail(f"no command containing {needle!r} in {commands}")

    def test_pi_relaunch_passes_recorded_session_and_uses_mailbox_v2(self):
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
        self.assertIn("mailbox_send.py", mailbox[1])
        self.assertEqual(mailbox[mailbox.index("--from") + 1], "supervisor")
        self.assertEqual(mailbox[mailbox.index("--to") + 1], "pi-worker")
        self.assertNotIn("worker_send.py", " ".join(mailbox))

    def test_pi_relaunch_without_session_leaves_window_running(self):
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

    def test_legacy_relaunch_requires_managed_pi_lifecycle(self):
        commands = self._run(
            {
                "state": "active",
                "type": "worker",
                "window": "legacy-worker",
                "alias": "legacy-worker",
                "project": "/project",
                "agent": "codex",
            }
        )
        self.assertEqual(commands, [])

    def test_relaunch_aborts_when_a_sidecar_survives_its_window(self):
        # Relaunching reuses the same window name. A surviving sidecar would keep that
        # agent ID registered with the router while a different agent owns the window,
        # so mail for the live agent lands in the old sidecar. Refuse to relaunch.
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
