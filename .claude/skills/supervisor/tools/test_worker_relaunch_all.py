#!/usr/bin/env python3
"""Focused relaunch tests for Mailbox V2 Pi recovery."""

import contextlib
import io
import subprocess
import unittest
from unittest.mock import patch

import worker_relaunch_all


class WorkerRelaunchTest(unittest.TestCase):
    def _run(self, task):
        with patch.object(worker_relaunch_all.sys, "argv", ["worker_relaunch_all.py"]), \
             patch.object(worker_relaunch_all, "load_state", return_value={"tasks": [task]}), \
             patch.object(worker_relaunch_all.os.path, "isdir", return_value=True), \
             patch.object(
                 worker_relaunch_all.subprocess,
                 "run",
                 return_value=subprocess.CompletedProcess([], 0, stdout="", stderr=""),
             ) as run, \
             patch.object(worker_relaunch_all.time, "sleep"), \
             contextlib.redirect_stdout(io.StringIO()):
            worker_relaunch_all.main()
        return [call.args[0] for call in run.call_args_list]

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

        launch = commands[1]
        mailbox = commands[2]
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


if __name__ == "__main__":
    unittest.main()
