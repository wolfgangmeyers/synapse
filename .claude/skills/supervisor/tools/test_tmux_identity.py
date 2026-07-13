#!/usr/bin/env python3
"""Focused tests for tmux-derived agent identity."""

import subprocess
import unittest

from _tmux import current_agent_id


class CurrentAgentIdTest(unittest.TestCase):
    def test_resolves_a_valid_current_window_name(self):
        calls = []

        def tmux_run(args, check):
            calls.append((args, check))
            return subprocess.CompletedProcess(args, 0, stdout="worker_1\n")

        self.assertEqual(
            current_agent_id({"TMUX": "/tmp/tmux-1/default,1,0", "TMUX_PANE": "%7"}, tmux_run),
            "worker_1",
        )
        self.assertEqual(calls, [(["display-message", "-p", "-t", "%7", "#{window_name}"], False)])

    def test_fails_closed_without_tmux_context(self):
        self.assertIsNone(current_agent_id({"TMUX": "x"}))
        self.assertIsNone(current_agent_id({"TMUX_PANE": "%7"}))
        self.assertIsNone(current_agent_id({"TMUX": "x", "TMUX_PANE": "invalid"}))

    def test_rejects_invalid_window_names_and_lookup_failures(self):
        environment = {"TMUX": "x", "TMUX_PANE": "%7"}
        self.assertIsNone(
            current_agent_id(
                environment,
                lambda args, check: subprocess.CompletedProcess(args, 0, stdout="bad name\n"),
            )
        )
        self.assertIsNone(
            current_agent_id(
                environment,
                lambda args, check: subprocess.CompletedProcess(args, 1, stdout="worker-1\n"),
            )
        )


if __name__ == "__main__":
    unittest.main()
