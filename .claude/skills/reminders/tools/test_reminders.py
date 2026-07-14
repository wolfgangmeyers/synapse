#!/usr/bin/env python3
"""Reminder creation tests."""

import argparse
import unittest
from unittest.mock import patch

import reminders


class ReminderIdTest(unittest.TestCase):
    def test_new_reminders_use_url_safe_nanoids(self):
        payloads = []
        args = argparse.Namespace(
            window="worker",
            creator="supervisor",
            relative_duration="5m",
            absolute_time=None,
            recurrence=None,
            message="Check this.",
            subject=None,
            router_url="http://127.0.0.1:8765",
        )

        def post_json(_url, payload):
            payloads.append(payload)
            return 201, {"reminder": {**payload, "state": "scheduled"}}

        with patch.object(reminders, "post_json", side_effect=post_json):
            self.assertEqual(reminders.set_reminder(args), 0)

        self.assertRegex(payloads[0]["id"], r"^[A-Za-z0-9_-]{21}$")


if __name__ == "__main__":
    unittest.main()
