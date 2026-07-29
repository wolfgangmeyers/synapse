#!/usr/bin/env python3
"""Regression tests: the completed-task archive must stay bounded.

Archiving moves tasks out of a bounded state file. Without a cap on the archive
that just relocates unbounded growth, which the repo's data-retention rule forbids.
"""

import contextlib
import io
import json
import os
import pathlib
import sys
import tempfile
import unittest
from datetime import datetime, timedelta
from unittest.mock import patch

import _state
import state_archive_completed


def completed_task(index, days_ago):
    completed = datetime.now() - timedelta(days=days_ago)
    return {
        "id": index,
        "alias": f"task-{index}",
        "type": "worker",
        "project": "mecha-developer",
        "description": "done",
        "state": "completed",
        "completed": completed.strftime("%Y-%m-%d %H:%M:%S"),
    }


class PruneArchiveTest(unittest.TestCase):
    def test_no_op_below_the_cap(self):
        tasks = [completed_task(index, days_ago=30) for index in range(5)]
        kept, dropped = state_archive_completed.prune_archive(tasks, max_tasks=10)
        self.assertEqual(dropped, [])
        self.assertEqual(kept, tasks)

    def test_caps_at_the_limit_and_keeps_the_newest(self):
        # Oldest first: task-0 completed 20 days ago, task-19 one day ago.
        tasks = [completed_task(index, days_ago=20 - index) for index in range(20)]
        kept, dropped = state_archive_completed.prune_archive(tasks, max_tasks=5)
        self.assertEqual([task["alias"] for task in dropped], [f"task-{index}" for index in range(15)])
        self.assertEqual([task["alias"] for task in kept], [f"task-{index}" for index in range(15, 20)])

    def test_tasks_with_an_unusable_date_are_dropped_first(self):
        tasks = [
            {"alias": "no-date", "state": "completed"},
            {"alias": "bad-date", "state": "completed", "completed": "last tuesday"},
            completed_task(1, days_ago=30),
            completed_task(2, days_ago=1),
        ]
        kept, dropped = state_archive_completed.prune_archive(tasks, max_tasks=2)
        self.assertEqual([task["alias"] for task in dropped], ["no-date", "bad-date"])
        self.assertEqual([task["alias"] for task in kept], ["task-1", "task-2"])

    def test_a_zero_cap_keeps_nothing_rather_than_everything(self):
        tasks = [completed_task(index, days_ago=30) for index in range(5)]
        kept, dropped = state_archive_completed.prune_archive(tasks, max_tasks=0)
        self.assertEqual(kept, [])
        self.assertEqual(len(dropped), 5)

    def test_re_archiving_the_same_task_does_not_duplicate_it(self):
        # A crash between the archive write and the state write leaves a task in both
        # files; the next run must not append a second copy that also eats the cap.
        task = completed_task(1, days_ago=30)
        kept, dropped = state_archive_completed.prune_archive([task, dict(task)], max_tasks=10)
        self.assertEqual(len(kept), 1)
        self.assertEqual(dropped, [])

    def test_the_default_cap_is_enforced(self):
        tasks = [completed_task(index, days_ago=1) for index in range(state_archive_completed.ARCHIVE_MAX_TASKS + 3)]
        kept, dropped = state_archive_completed.prune_archive(tasks)
        self.assertEqual(len(kept), state_archive_completed.ARCHIVE_MAX_TASKS)
        self.assertEqual(len(dropped), 3)


class ArchiveFileGrowthTest(unittest.TestCase):
    """The end-to-end path: repeated archiving must not grow the file forever."""

    def setUp(self):
        self.tmpdir = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmpdir.cleanup)
        tmp_path = pathlib.Path(self.tmpdir.name)
        self.state_file = str(tmp_path / "supervisor-state.json")
        self.archive_file = str(tmp_path / "supervisor-state-archive.json")
        patches = [
            patch.object(_state, "STATE_FILE", self.state_file),
            patch.object(state_archive_completed, "STATE_FILE", self.state_file),
            patch.object(state_archive_completed, "ARCHIVE_FILE", self.archive_file),
        ]
        for active in patches:
            active.start()
            self.addCleanup(active.stop)

    def write_state(self, tasks):
        with open(self.state_file, "w", encoding="utf-8") as handle:
            json.dump({"next_task_id": len(tasks) + 1, "tasks": tasks, "standing_orders": []}, handle)

    def archive(self, argv):
        out = io.StringIO()
        err = io.StringIO()
        with patch.object(sys, "argv", argv):
            with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
                state_archive_completed.main()
        return out.getvalue(), err.getvalue()

    def read_archive(self):
        with open(self.archive_file, encoding="utf-8") as handle:
            return json.load(handle)

    def test_repeated_archiving_keeps_the_archive_at_the_cap(self):
        cap = 4
        with patch.object(state_archive_completed, "ARCHIVE_MAX_TASKS", cap):
            for round_index in range(5):
                self.write_state([
                    completed_task(round_index * 3 + offset, days_ago=100 - (round_index * 3 + offset))
                    for offset in range(3)
                ])
                stdout, _ = self.archive(["state_archive_completed.py", "--confirmed"])
                self.assertIn("Archiving 3 completed tasks", stdout)
                self.assertLessEqual(len(self.read_archive()["tasks"]), cap)

        archive = self.read_archive()
        self.assertEqual(len(archive["tasks"]), cap)
        # The newest completions survive; the oldest are dropped.
        self.assertEqual([task["alias"] for task in archive["tasks"]], ["task-11", "task-12", "task-13", "task-14"])

    def test_dropping_old_tasks_is_reported_not_silent(self):
        with patch.object(state_archive_completed, "ARCHIVE_MAX_TASKS", 2):
            self.write_state([completed_task(index, days_ago=90 - index) for index in range(5)])
            stdout, _ = self.archive(["state_archive_completed.py", "--confirmed"])
        self.assertIn("Archive capped at 2 tasks", stdout)
        self.assertIn("dropped 3 oldest completed task(s)", stdout)
        # Naming them is the difference between "history rolled off" and "which ones?"
        for alias in ("task-0", "task-1", "task-2"):
            self.assertIn(alias, stdout)

    def test_dropping_tasks_this_run_just_archived_is_called_out_as_data_loss(self):
        # The archive is the only remaining copy of a task the state file is about to
        # drop. Pruning it on the same write destroys it, which must not read like
        # ordinary history ageing out.
        with patch.object(state_archive_completed, "ARCHIVE_MAX_TASKS", 2):
            self.write_state([completed_task(index, days_ago=90 - index) for index in range(5)])
            stdout, stderr = self.archive(["state_archive_completed.py", "--confirmed"])
        self.assertIn("WARNING", stderr)
        self.assertIn("archived by this run", stderr)
        self.assertIn("gone from both the state file and the archive", stderr)
        self.assertIn("3 of those", stderr)

    def test_rolling_off_older_history_is_not_flagged_as_data_loss(self):
        with patch.object(state_archive_completed, "ARCHIVE_MAX_TASKS", 3):
            self.write_state([completed_task(index, days_ago=90 - index) for index in range(3)])
            self.archive(["state_archive_completed.py", "--confirmed"])
            self.write_state([completed_task(index, days_ago=50 - index) for index in range(3, 6)])
            stdout, stderr = self.archive(["state_archive_completed.py", "--confirmed"])
        self.assertIn("dropped 3 oldest completed task(s)", stdout)
        self.assertNotIn("WARNING", stderr)

    def test_archiving_still_removes_the_tasks_from_the_state_file(self):
        self.write_state([completed_task(1, days_ago=90), completed_task(2, days_ago=1)])
        stdout, _ = self.archive(["state_archive_completed.py", "--confirmed"])
        self.assertIn("Archived 1 tasks", stdout)
        with open(self.state_file, encoding="utf-8") as handle:
            remaining = json.load(handle)["tasks"]
        self.assertEqual([task["alias"] for task in remaining], ["task-2"])
        self.assertEqual([task["alias"] for task in self.read_archive()["tasks"]], ["task-1"])

    def test_archiving_nothing_does_not_read_as_success(self):
        self.write_state([completed_task(1, days_ago=1)])
        stdout, stderr = self.archive(["state_archive_completed.py", "--confirmed"])
        self.assertEqual(stdout, "")
        self.assertIn("Nothing archived", stderr)
        self.assertNotIn("Done", stderr)
        self.assertFalse(os.path.exists(self.archive_file))

    def test_dry_run_reports_nothing_archived_the_same_way(self):
        self.write_state([completed_task(1, days_ago=1)])
        stdout, stderr = self.archive(["state_archive_completed.py"])
        self.assertEqual(stdout, "")
        self.assertIn("Nothing archived", stderr)
        self.assertIn("1 completed task(s)", stderr)

    def test_dateless_completed_tasks_are_not_described_as_merely_too_recent(self):
        # A task with no usable `completed` date can never be archived at any --age,
        # so telling the operator to lower --age would be wrong.
        self.write_state([
            {"alias": "no-date", "state": "completed"},
            completed_task(2, days_ago=1),
        ])
        _, stderr = self.archive(["state_archive_completed.py"])
        self.assertIn("cannot be archived at any --age", stderr)

        self.write_state([{"alias": "no-date", "state": "completed"}])
        _, stderr = self.archive(["state_archive_completed.py"])
        self.assertIn("none with a usable `completed` date", stderr)
        self.assertNotIn("Use a smaller --age", stderr)


if __name__ == "__main__":
    unittest.main()
