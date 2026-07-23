#!/usr/bin/env python3

import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

TOOLS = Path(__file__).parent
SCRIPT = TOOLS / "state_priority.py"


class StatePriorityTests(unittest.TestCase):
    def run_tool(self, state_file, *args):
        return subprocess.run(
            [sys.executable, str(SCRIPT), *args],
            capture_output=True,
            text=True,
            env=os.environ | {"MECHA_STATE_FILE": str(state_file)},
        )

    def state_file(self, directory):
        path = Path(directory) / "state.json"
        path.write_text(json.dumps({"next_task_id": 2, "tasks": [{"id": 1, "state": "active"}], "priorities": []}))
        return path

    def test_upsert_links_known_task(self):
        with tempfile.TemporaryDirectory() as directory:
            path = self.state_file(directory)
            result = self.run_tool(path, "upsert", "--id", "p-1502", "--name", "PR #1502", "--task-id", "1")
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(json.loads(path.read_text())["priorities"], [{
                "id": "p-1502", "name": "PR #1502", "status": "active", "task_ids": [1],
            }])

    def test_upsert_rejects_unknown_task(self):
        with tempfile.TemporaryDirectory() as directory:
            path = self.state_file(directory)
            result = self.run_tool(path, "upsert", "--id", "p", "--name", "bad", "--task-id", "99")
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("unknown task ID", result.stderr)
            self.assertEqual(json.loads(path.read_text())["priorities"], [])

    def test_terminal_status_requires_evidence(self):
        with tempfile.TemporaryDirectory() as directory:
            path = self.state_file(directory)
            self.assertEqual(self.run_tool(path, "upsert", "--id", "p", "--name", "PR", "--task-id", "1").returncode, 0)
            result = self.run_tool(path, "status", "p", "completed")
            self.assertNotEqual(result.returncode, 0)
            priority = json.loads(path.read_text())["priorities"][0]
            self.assertEqual(priority["status"], "active")

            result = self.run_tool(path, "status", "p", "completed", "--evidence", "PR merged")
            self.assertEqual(result.returncode, 0, result.stderr)
            priority = json.loads(path.read_text())["priorities"][0]
            self.assertEqual(priority["completion"]["evidence"], "PR merged")


if __name__ == "__main__":
    unittest.main()
