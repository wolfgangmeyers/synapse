#!/usr/bin/env python3
"""Focused state_add validation tests for managed Pi workers."""

import json
import os
import pathlib
import subprocess
import sys
import tempfile
import unittest


TOOLS_DIR = pathlib.Path(__file__).parent
STATE_ADD = TOOLS_DIR / "state_add.py"


class StateAddTest(unittest.TestCase):
    def _run(self, state_file, *args):
        environment = os.environ | {"MECHA_STATE_FILE": str(state_file)}
        return subprocess.run(
            [sys.executable, str(STATE_ADD), *args],
            capture_output=True,
            text=True,
            env=environment,
        )

    def test_records_pi_session(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            state_file = pathlib.Path(tmpdir) / "state.json"
            result = self._run(
                state_file,
                "--alias", "pi-worker",
                "--type", "worker",
                "--project", "/project",
                "--description", "Pi worker",
                "--agent", "pi",
                "--session", "pi-session-123",
            )

            self.assertEqual(result.returncode, 0, result.stderr)
            task = json.loads(state_file.read_text(encoding="utf-8"))["tasks"][0]
            self.assertEqual(task["agent"], "pi")
            self.assertEqual(task["session"], "pi-session-123")

    def test_rejects_unknown_agent(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            result = self._run(
                pathlib.Path(tmpdir) / "state.json",
                "--alias", "bad-agent",
                "--type", "worker",
                "--project", "/project",
                "--description", "Bad agent",
                "--agent", "unknown",
            )

        self.assertEqual(result.returncode, 2)
        self.assertIn("invalid choice", result.stderr)

    def test_rejects_session_for_legacy_agent(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            result = self._run(
                pathlib.Path(tmpdir) / "state.json",
                "--alias", "legacy-worker",
                "--type", "worker",
                "--project", "/project",
                "--description", "Legacy worker",
                "--agent", "codex",
                "--session", "not-a-pi-session",
            )

        self.assertEqual(result.returncode, 2)
        self.assertIn("--session requires --agent pi", result.stderr)


if __name__ == "__main__":
    unittest.main()
