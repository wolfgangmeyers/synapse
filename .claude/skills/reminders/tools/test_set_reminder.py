#!/usr/bin/env python3
"""Tests for the legacy reminder entry point's V2 delegation."""

import unittest
from unittest.mock import patch

import set_reminder


class SetReminderCompatibilityTest(unittest.TestCase):
    def test_delegates_legacy_arguments_to_the_v2_set_command(self):
        with patch.object(set_reminder.reminders, "main", return_value=0) as main:
            result = set_reminder.main(["--window", "supervisor", "--message", "Check PR", "--in", "10m"])

        self.assertEqual(result, 0)
        main.assert_called_once_with([
            "set", "--window", "supervisor", "--message", "Check PR", "--in", "10m",
        ])


if __name__ == "__main__":
    unittest.main()
