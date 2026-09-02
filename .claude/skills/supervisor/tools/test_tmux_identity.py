#!/usr/bin/env python3
"""Focused tests for tmux-derived agent identity."""

import subprocess
import unittest

import _tmux
from _tmux import current_agent_id, new_window


class CurrentAgentIdTest(unittest.TestCase):
    def test_new_window_is_created_detached_to_avoid_stealing_focus(self):
        calls = []

        def fake_run(args, check=True, capture=True):
            calls.append((args, check, capture))
            return subprocess.CompletedProcess(args, 0, stdout="")

        original_run = _tmux.run
        original_ensure_session = _tmux.ensure_session
        try:
            _tmux.run = fake_run
            _tmux.ensure_session = lambda: False
            new_window("worker")
        finally:
            _tmux.run = original_run
            _tmux.ensure_session = original_ensure_session

        self.assertEqual(calls, [(["new-window", "-d", "-t", f"={_tmux.SESSION}", "-n", "worker"], True, True)])

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

    def test_capture_pane_uses_read_only_mecha_target_and_timeout(self):
        calls = []

        def fake_run(args, check=True, capture=True, timeout=None):
            calls.append((args, check, capture, timeout))
            return subprocess.CompletedProcess(args, 0, stdout="pane output")

        original_run = _tmux.run
        try:
            _tmux.run = fake_run
            self.assertEqual(_tmux.capture_pane("worker", lines=80, timeout=1.5), "pane output")
        finally:
            _tmux.run = original_run

        self.assertEqual(calls, [(["capture-pane", "-t", "mecha:worker", "-p", "-S", "-80"], False, True, 1.5)])

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
