#!/usr/bin/env python3
"""Focused recovery tests for managed worker runtimes."""

import subprocess
import unittest
from unittest.mock import patch

import worker_recover_all


class WorkerRecoveryTest(unittest.TestCase):
    def test_pi_recovery_uses_recorded_session_and_settled_notification(self):
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
        self.assertEqual(command[command.index("--notify-on-settled") + 1], "supervisor")

    def test_recovery_refuses_unrecorded_session(self):
        task = {
            "alias": "pi-worker",
            "window": "pi-worker",
            "project": "/project",
            "agent": "pi",
        }

        with patch.object(worker_recover_all.os.path, "isdir", return_value=True), \
             patch.object(worker_recover_all.subprocess, "run") as run:
            recovered = worker_recover_all.recover_worker(task)

        self.assertFalse(recovered)
        run.assert_not_called()

    def test_non_worker_task_is_not_selected_by_recover_all(self):
        with patch.object(worker_recover_all, "load_state", return_value={"tasks": [{
            "state": "active",
            "type": "pipeline",
            "window": "pipeline-window",
            "project": "/project",
            "agent": "pi",
            "session": "pi-session-123",
        }]}), \
             patch.object(worker_recover_all.sys, "argv", ["worker_recover_all.py"]), \
             patch.object(worker_recover_all.subprocess, "run") as run:
            result = worker_recover_all.main()

        self.assertEqual(result, 0)
        run.assert_not_called()

    def test_non_pi_recovery_uses_recorded_runtime_and_session_without_settled_notification(self):
        task = {
            "alias": "claude-worker",
            "window": "claude-worker",
            "project": "/project",
            "agent": "claude",
            "session": "claude-session-123",
        }

        with patch.object(worker_recover_all.os.path, "isdir", return_value=True), \
             patch.object(worker_recover_all.subprocess, "run", return_value=subprocess.CompletedProcess([], 0)) as run:
            recovered = worker_recover_all.recover_worker(task)

        self.assertTrue(recovered)
        command = run.call_args.args[0]
        self.assertEqual(command[command.index("--agent") + 1], "claude")
        self.assertEqual(command[command.index("--resume") + 1], "claude-session-123")
        self.assertNotIn("--notify-on-settled", command)


if __name__ == "__main__":
    unittest.main()
