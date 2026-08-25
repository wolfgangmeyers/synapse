#!/usr/bin/env python3
"""Focused tests for alias-only managed worker lifecycle actions."""

import json
import os
import tempfile
import unittest
from unittest import mock

import worker_lifecycle


class WorkerLifecycleTest(unittest.TestCase):
    def setUp(self):
        self.temporary_directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary_directory.cleanup)
        self.state_file = os.path.join(self.temporary_directory.name, "supervisor-state.json")
        self.worktree = os.path.join(self.temporary_directory.name, "worktree")
        os.mkdir(self.worktree)
        self.state_patch = mock.patch.object(worker_lifecycle.state_store, "STATE_FILE", self.state_file)
        self.state_patch.start()
        self.addCleanup(self.state_patch.stop)

    def task(self, **overrides):
        task = {
            "id": 7,
            "alias": "worker-7",
            "state": "active",
            "type": "worker",
            "project": "example",
            "description": "Example task",
            "window": "old-window",
            "worktree": self.worktree,
            "agent": "pi",
            "session": "session-7",
            "branch": "feature/example",
            "dev_environment": "example-services",
            "last_checked": "2026-01-01 00:00",
        }
        task.update(overrides)
        return task

    def write_state(self, tasks):
        worker_lifecycle.state_store.save_state({
            "next_task_id": 20,
            "tasks": tasks,
            "standing_orders": [],
        })

    def read_tasks(self):
        with open(self.state_file, "r", encoding="utf-8") as state_file:
            return json.load(state_file)["tasks"]

    def test_launch_uses_only_state_recorded_values_and_updates_window_after_success(self):
        task = self.task()
        self.write_state([task])
        with mock.patch.object(worker_lifecycle, "active_identity", return_value=False), mock.patch.object(
            worker_lifecycle, "run_script"
        ) as run_script:
            worker_lifecycle.launch("worker-7")

        launch_argv = [
            "--window", "worker-7",
            "--dir", self.worktree,
            "--agent", "pi",
            "--resume", "session-7",
            "--notify-on-settled", "supervisor",
        ]
        run_script.assert_called_once_with(worker_lifecycle.WORKER_LAUNCH, launch_argv)
        self.assertTrue({"--extension", "--no-extensions", "-ne"}.isdisjoint(launch_argv))
        saved = self.read_tasks()[0]
        self.assertEqual(saved["window"], "worker-7")
        self.assertEqual(saved["worktree"], self.worktree)
        self.assertEqual(saved["session"], "session-7")
        self.assertEqual(saved["dev_environment"], "example-services")

    def test_launch_without_session_uses_recorded_non_pi_runtime(self):
        self.write_state([self.task(agent="codex", session=None)])
        with mock.patch.object(worker_lifecycle, "active_identity", return_value=False), mock.patch.object(
            worker_lifecycle, "run_script"
        ) as run_script:
            worker_lifecycle.launch("worker-7")

        run_script.assert_called_once_with(worker_lifecycle.WORKER_LAUNCH, [
            "--window", "worker-7",
            "--dir", self.worktree,
            "--agent", "codex",
        ])

    def test_launch_refuses_active_alias_or_recorded_window(self):
        self.write_state([self.task()])
        for active_window in ("worker-7", "old-window"):
            with self.subTest(active_window=active_window), mock.patch.object(
                worker_lifecycle,
                "active_identity",
                side_effect=lambda window, active_window=active_window: window == active_window,
            ), mock.patch.object(worker_lifecycle, "run_script") as run_script:
                with self.assertRaisesRegex(worker_lifecycle.LifecycleError, "already active"):
                    worker_lifecycle.launch("worker-7")
                run_script.assert_not_called()

    def test_launch_fails_closed_for_ambiguous_or_incomplete_state(self):
        cases = [
            ([self.task(), self.task(id=8, state="completed")], "ambiguous"),
            ([self.task(worktree=None)], "no recorded worktree"),
            ([self.task(worktree=os.path.join(self.temporary_directory.name, "missing"))], "does not exist"),
            ([self.task(agent=None)], "no valid recorded runtime"),
            ([self.task(agent="shell")], "no valid recorded runtime"),
            ([self.task(session="")], "recorded worker session is invalid"),
            ([self.task(state="completed")], "not an active managed worker"),
            ([self.task(type="direct")], "not an active managed worker"),
        ]
        for tasks, message in cases:
            with self.subTest(message=message):
                self.write_state(tasks)
                with mock.patch.object(worker_lifecycle, "active_identity", return_value=False), mock.patch.object(
                    worker_lifecycle, "run_script"
                ) as run_script:
                    with self.assertRaisesRegex(worker_lifecycle.LifecycleError, message):
                        worker_lifecycle.launch("worker-7")
                    run_script.assert_not_called()

    def test_failed_launch_does_not_update_state(self):
        self.write_state([self.task()])
        with mock.patch.object(worker_lifecycle, "active_identity", return_value=False), mock.patch.object(
            worker_lifecycle,
            "run_script",
            side_effect=worker_lifecycle.LifecycleError("launch failed"),
        ):
            with self.assertRaisesRegex(worker_lifecycle.LifecycleError, "launch failed"):
                worker_lifecycle.launch("worker-7")
        saved = self.read_tasks()[0]
        self.assertEqual(saved["window"], "old-window")
        self.assertEqual(saved["last_checked"], "2026-01-01 00:00")

    def test_cleanup_kills_only_recorded_window_then_updates_state(self):
        self.write_state([self.task()])
        with mock.patch.object(worker_lifecycle, "run_script") as run_script:
            worker_lifecycle.cleanup("worker-7")

        run_script.assert_called_once_with(worker_lifecycle.WORKER_KILL, ["--window", "old-window"])
        saved = self.read_tasks()[0]
        self.assertEqual(saved["window"], worker_lifecycle.CLEANED_WINDOW)
        self.assertEqual(saved["worktree"], self.worktree)
        self.assertEqual(saved["branch"], "feature/example")
        self.assertEqual(saved["session"], "session-7")
        self.assertEqual(saved["dev_environment"], "example-services")
        self.assertEqual(saved["state"], "active")

    def test_failed_cleanup_does_not_update_state(self):
        self.write_state([self.task()])
        with mock.patch.object(
            worker_lifecycle,
            "run_script",
            side_effect=worker_lifecycle.LifecycleError("kill failed"),
        ):
            with self.assertRaisesRegex(worker_lifecycle.LifecycleError, "kill failed"):
                worker_lifecycle.cleanup("worker-7")
        saved = self.read_tasks()[0]
        self.assertEqual(saved["window"], "old-window")
        self.assertEqual(saved["last_checked"], "2026-01-01 00:00")

    def test_cleanup_refuses_missing_or_non_worker_window(self):
        for window in (None, worker_lifecycle.CLEANED_WINDOW, "bad.window"):
            with self.subTest(window=window):
                self.write_state([self.task(window=window)])
                with mock.patch.object(worker_lifecycle, "run_script") as run_script:
                    with self.assertRaisesRegex(worker_lifecycle.LifecycleError, "no valid recorded window"):
                        worker_lifecycle.cleanup("worker-7")
                    run_script.assert_not_called()


if __name__ == "__main__":
    unittest.main()
