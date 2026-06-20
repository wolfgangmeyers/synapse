#!/usr/bin/env python3
"""Tests for set_reminder helper behavior."""

import os
import unittest

import set_reminder


class SetReminderWindowResolutionTest(unittest.TestCase):
    def tearDown(self):
        os.environ.pop("MECHA_AGENT_WINDOW", None)
        os.environ.pop("MECHA_WORKER_WINDOW", None)

    def test_self_alias_uses_agent_window(self):
        os.environ["MECHA_AGENT_WINDOW"] = "supervisor"

        self.assertEqual(set_reminder.resolve_window("me"), ("supervisor", "me"))

    def test_self_alias_uses_worker_window_without_agent_window(self):
        os.environ["MECHA_WORKER_WINDOW"] = "garden-bg"

        self.assertEqual(set_reminder.resolve_window("self"), ("garden-bg", "self"))

    def test_self_alias_falls_back_to_supervisor(self):
        self.assertEqual(set_reminder.resolve_window("current"), ("supervisor", "current"))

    def test_explicit_window_is_unchanged(self):
        self.assertEqual(set_reminder.resolve_window("garden-bg"), ("garden-bg", None))


if __name__ == "__main__":
    unittest.main()
