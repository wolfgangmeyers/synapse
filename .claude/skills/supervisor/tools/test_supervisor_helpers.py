import json
import subprocess
import unittest
from pathlib import Path
import sys

TOOLS = Path(__file__).resolve().parent
sys.path.insert(0, str(TOOLS))

import git_task_status
import pr_status
import state_summary
import worker_tail_summary


class SupervisorHelperTests(unittest.TestCase):
    def test_pr_url_parses_repo_and_number(self):
        parsed = pr_status.parse_pr("https://github.com/acme/app/pull/42", None)
        self.assertEqual(parsed.selector, "42")
        self.assertEqual(parsed.repo, "acme/app")

    def test_pr_check_rollup_flags_failure_before_pending(self):
        rollup = pr_status.check_rollup([
            {"name": "unit", "state": "SUCCESS"},
            {"name": "lint", "state": "FAILURE"},
            {"name": "deploy", "state": "PENDING"},
        ])
        self.assertEqual(rollup["overall"], "failing")
        self.assertEqual(rollup["counts"], {"SUCCESS": 1, "FAILURE": 1, "PENDING": 1})
        self.assertEqual(rollup["failing"], ["lint"])
        self.assertEqual(rollup["pending"], ["deploy"])

    def test_worker_tail_suppresses_idle_ack_noise(self):
        compacted = worker_tail_summary.compact_lines(">\nAcknowledged.\nRunning tests now\n\nNo acknowledgment needed\nTask complete. Ready to be cleaned up.", keep=5)
        self.assertEqual(compacted, ["Running tests now", "Task complete. Ready to be cleaned up."])
        self.assertEqual(worker_tail_summary.state_hint(compacted), "complete")

    def test_state_summary_marks_active_missing_window_as_dangling(self):
        summary = state_summary.summarize({"tasks": [
            {"id": 1, "alias": "live", "state": "active", "window": "live-window"},
            {"id": 2, "alias": "lost", "state": "active", "window": "lost-window", "pr": "PR"},
            {"id": 3, "alias": "done", "state": "completed"},
            {"id": 4, "alias": "blocked", "state": "active", "blocked": "2026-01-01", "window": "blocked-window"},
        ], "priorities": []}, {"live-window"})
        self.assertEqual(summary["counts"], {"active": 2, "completed": 1, "blocked": 1})
        self.assertEqual([item["alias"] for item in summary["dangling"]], ["lost", "blocked"])
        self.assertEqual(summary["dangling"][1]["state"], "blocked")

    def test_git_status_parses_porcelain_and_ahead_behind(self):
        def runner(repo, args):
            key = tuple(args)
            outputs = {
                ("branch", "--show-current"): "feature\n",
                ("rev-parse", "--short", "HEAD"): "abc123\n",
                ("rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}"): "origin/feature\n",
                ("status", "--porcelain=v1"): " M file.py\n?? scratch.txt\n",
                ("log", "-1", "--pretty=format:%h %s"): "abc123 msg",
                ("rev-list", "--left-right", "--count", "origin/feature...HEAD"): "2\t3\n",
            }
            return subprocess.CompletedProcess(["git", *args], 0, outputs[key], "")

        status = git_task_status.git_status("/repo", runner=runner)
        self.assertEqual(status["branch"], "feature")
        self.assertEqual(status["ahead"], 3)
        self.assertEqual(status["behind"], 2)
        self.assertTrue(status["dirty"])
        self.assertEqual(status["untracked"], 1)
        self.assertEqual(status["changed"], 2)


if __name__ == "__main__":
    unittest.main()
