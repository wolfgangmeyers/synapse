#!/usr/bin/env python3
"""Reminder creation tests."""

import argparse
from contextlib import redirect_stderr
from io import StringIO
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

        self.assertRegex(payloads[0]["id"], r"^[A-Za-z0-9]{21}$")


class PrWatchReconciliationTest(unittest.TestCase):
    args = argparse.Namespace(router_url="http://127.0.0.1:8765")

    @staticmethod
    def canonical(reminder_id=reminders.PR_WATCH_REMINDER_ID):
        return {
            "id": reminder_id,
            "target": reminders.PR_WATCH_TARGET,
            "creator": reminders.PR_WATCH_CREATOR,
            "subject": reminders.PR_WATCH_SUBJECT,
            "body": reminders.PR_WATCH_BODY,
            "interval_ms": reminders.PR_WATCH_INTERVAL_MS,
            "state": "scheduled",
            "linked_message_id": None,
        }

    def test_cancels_legacy_wrong_subject_before_creating_canonical_recurrence(self):
        records = [{**self.canonical("legacy-pr-watch"), "subject": "Run one PR watch scan now", "interval_ms": 900_000}]
        canceled = []
        created = []

        def delete(_router, reminder_id):
            canceled.append(reminder_id)
            records[:] = [record for record in records if record["id"] != reminder_id]

        def post(_url, payload):
            created.append(payload)
            return 201, {"reminder": {**payload, "state": "scheduled"}}

        with (patch.object(reminders, "list_router_reminders", side_effect=lambda _router: records.copy()),
              patch.object(reminders, "delete_router_reminder", side_effect=delete),
              patch.object(reminders, "post_json", side_effect=post)):
            self.assertEqual(reminders.reconcile_pr_watch(self.args), 0)

        self.assertEqual(canceled, ["legacy-pr-watch"])
        self.assertEqual(created[0]["subject"], reminders.PR_WATCH_SUBJECT)
        self.assertEqual(created[0]["interval_ms"], 900_000)

    def test_cancels_duplicate_scheduled_pr_watch_recurrence(self):
        records = [self.canonical(), self.canonical("z-duplicate")]
        canceled = []

        def delete(_router, reminder_id):
            canceled.append(reminder_id)
            records[:] = [record for record in records if record["id"] != reminder_id]

        with (patch.object(reminders, "list_router_reminders", side_effect=lambda _router: records.copy()),
              patch.object(reminders, "delete_router_reminder", side_effect=delete),
              patch.object(reminders, "post_json") as post):
            self.assertEqual(reminders.reconcile_pr_watch(self.args), 0)

        self.assertEqual(canceled, ["z-duplicate"])
        post.assert_not_called()

    def test_retains_one_canonical_scheduled_recurrence(self):
        canonical = self.canonical()
        with (patch.object(reminders, "list_router_reminders", return_value=[canonical]),
              patch.object(reminders, "delete_router_reminder") as delete,
              patch.object(reminders, "post_json") as post):
            self.assertEqual(reminders.reconcile_pr_watch(self.args), 0)

        delete.assert_not_called()
        post.assert_not_called()

    def test_failed_dead_letter_recurrence_notifies_supervisor_and_fails(self):
        failed = {
            **self.canonical("legacy-pr-watch"),
            "subject": "Run one PR watch scan now",
            "state": "failed",
            "linked_message_id": "message-1",
            "linked_message_state": "dead_letter",
        }
        sent = []

        def post(_url, payload):
            sent.append(payload)
            return 201, {"message": {"id": payload["id"], "state": "queued"}}

        with (redirect_stderr(StringIO()),
              patch.object(reminders, "list_router_reminders", return_value=[failed]),
              patch.object(reminders, "delete_router_reminder") as delete,
              patch.object(reminders, "post_json", side_effect=post),
              patch.object(reminders, "nanoid", return_value="notification-id")):
            self.assertEqual(reminders.reconcile_pr_watch(self.args), 1)

        delete.assert_not_called()
        self.assertEqual(sent[0]["to"], "supervisor")
        self.assertEqual(sent[0]["subject"], "PR watch error")
        self.assertIn("legacy-pr-watch", sent[0]["body"])


if __name__ == "__main__":
    unittest.main()
