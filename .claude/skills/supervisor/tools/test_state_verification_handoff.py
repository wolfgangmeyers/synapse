#!/usr/bin/env python3

import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

SCRIPT = Path(__file__).with_name("state_verification_handoff.py")


class StateVerificationHandoffTests(unittest.TestCase):
    def test_records_structured_handoff(self):
        with tempfile.TemporaryDirectory() as directory:
            state_file = Path(directory) / "state.json"
            state_file.write_text(json.dumps({"tasks": [{"id": 4, "alias": "ci", "state": "active"}]}))
            result = subprocess.run(
                [sys.executable, str(SCRIPT), "ci", "--reminder-id", "r-1", "--successor", "CI watcher"],
                capture_output=True,
                text=True,
                env=os.environ | {"MECHA_STATE_FILE": str(state_file)},
            )
            self.assertEqual(result.returncode, 0, result.stderr)
            handoff = json.loads(state_file.read_text())["tasks"][0]["verification_handoff"]
            self.assertEqual(handoff["reminder_id"], "r-1")
            self.assertEqual(handoff["successor"], "CI watcher")
            self.assertFalse(handoff["remote_verified"])
            self.assertTrue(handoff["verification_started_at"].endswith("Z"))


if __name__ == "__main__":
    unittest.main()
