#!/usr/bin/env python3
"""Focused recovery tests for Pi and preserved legacy runtime branches."""

import contextlib
import io
import subprocess
import unittest
from unittest.mock import patch

import worker_recover_all


class WorkerRecoveryTest(unittest.TestCase):
    def test_pi_recovery_uses_recorded_session_at_launch(self):
        task = {
            "alias": "pi-worker",
            "window": "pi-worker",
            "project": "/project",
            "agent": "pi",
            "session": "pi-session-123",
        }

        with patch.object(worker_recover_all.os.path, "isdir", return_value=True), \
             patch.object(worker_recover_all.subprocess, "run", return_value=subprocess.CompletedProcess([], 0)) as run, \
             patch.object(worker_recover_all.time, "sleep"):
            recovered = worker_recover_all.recover_worker(task)

        self.assertTrue(recovered)
        command = run.call_args.args[0]
        self.assertIn("--agent", command)
        self.assertEqual(command[command.index("--agent") + 1], "pi")
        self.assertEqual(command[command.index("--resume") + 1], "pi-session-123")
        self.assertNotIn("worker_send.py", " ".join(command))

    def test_pi_recovery_refuses_unrecorded_session(self):
        task = {
            "alias": "pi-worker",
            "window": "pi-worker",
            "project": "/project",
            "agent": "pi",
        }
        stdout = io.StringIO()

        with patch.object(worker_recover_all.os.path, "isdir", return_value=True), \
             patch.object(worker_recover_all.subprocess, "run") as run, \
             contextlib.redirect_stdout(stdout):
            recovered = worker_recover_all.recover_worker(task)

        self.assertFalse(recovered)
        run.assert_not_called()
        self.assertIn("requires a recorded session", stdout.getvalue())

    def test_legacy_recovery_requires_managed_pi_lifecycle(self):
        task = {
            "alias": "legacy-worker",
            "window": "legacy-worker",
            "project": "/project",
            "agent": "claude",
        }
        stdout = io.StringIO()

        with patch.object(worker_recover_all.os.path, "isdir", return_value=True), \
             patch.object(worker_recover_all.subprocess, "run") as run, \
             contextlib.redirect_stdout(stdout):
            recovered = worker_recover_all.recover_worker(task)

        self.assertFalse(recovered)
        run.assert_not_called()
        self.assertIn("managed Pi launcher", stdout.getvalue())


if __name__ == "__main__":
    unittest.main()
