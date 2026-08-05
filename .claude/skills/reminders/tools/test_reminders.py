#!/usr/bin/env python3
"""Reminder creation tests."""

import argparse
from contextlib import redirect_stderr, redirect_stdout
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


import reminders


def update_args(**overrides):
    base = dict(
        id="abc",
        window=None,
        message=None,
        subject=None,
        relative_duration=None,
        absolute_time=None,
        recurrence=None,
        router_url="http://127.0.0.1:8765",
    )
    base.update(overrides)
    return argparse.Namespace(**base)


class UpdateReminderTest(unittest.TestCase):
    def _patch_json(self, payloads):
        def factory(_url, payload):
            payloads.append((_url, payload))
            return 200, {"reminder": {**payload, "id": "abc", "state": "scheduled", "target": "worker", "next_due_at": "2030-01-01T00:00:00.000Z"}}
        return factory

    def test_update_every_recomputes_next_due_at_and_sends_interval(self):
        payloads = []
        args = update_args(recurrence="10m", message="new body")
        with patch.object(reminders, "patch_json", side_effect=self._patch_json(payloads)):
            self.assertEqual(reminders.update_reminder(args), 0)
        url, payload = payloads[0]
        self.assertTrue(url.endswith("/v1/reminders/abc"))
        self.assertEqual(payload["interval_ms"], 10 * 60 * 1_000)
        self.assertIn("next_due_at", payload)
        self.assertEqual(payload["body"], "new body")
        self.assertNotIn("target", payload)

    def test_update_every_and_at_combine_interval_and_explicit_due_time(self):
        payloads = []
        args = update_args(recurrence="10m", absolute_time="2030-01-01T00:00:00Z")
        with patch.object(reminders, "patch_json", side_effect=self._patch_json(payloads)):
            self.assertEqual(reminders.update_reminder(args), 0)
        _url, payload = payloads[0]
        self.assertEqual(payload["interval_ms"], 10 * 60 * 1_000)
        self.assertEqual(payload["next_due_at"], "2030-01-01T00:00:00.000Z")

    def test_update_in_makes_the_reminder_one_shot(self):
        payloads = []
        args = update_args(relative_duration="5m")
        with patch.object(reminders, "patch_json", side_effect=self._patch_json(payloads)):
            self.assertEqual(reminders.update_reminder(args), 0)
        _url, payload = payloads[0]
        self.assertIsNone(payload["interval_ms"])
        self.assertIn("next_due_at", payload)

    def test_update_at_leaves_interval_unchanged(self):
        payloads = []
        args = update_args(absolute_time="2030-01-01T00:00:00Z")
        with patch.object(reminders, "patch_json", side_effect=self._patch_json(payloads)):
            self.assertEqual(reminders.update_reminder(args), 0)
        _url, payload = payloads[0]
        self.assertEqual(payload, {"next_due_at": "2030-01-01T00:00:00.000Z"})

    def test_update_body_only_sends_just_body(self):
        payloads = []
        args = update_args(message="ping")
        with patch.object(reminders, "patch_json", side_effect=self._patch_json(payloads)):
            self.assertEqual(reminders.update_reminder(args), 0)
        _url, payload = payloads[0]
        self.assertEqual(payload, {"body": "ping"})

    def test_update_window_resolves_target(self):
        payloads = []
        args = update_args(window="worker")
        with patch.object(reminders, "patch_json", side_effect=self._patch_json(payloads)):
            self.assertEqual(reminders.update_reminder(args), 0)
        _url, payload = payloads[0]
        self.assertEqual(payload, {"target": "worker"})

    def test_update_without_fields_is_rejected(self):
        args = update_args()
        with redirect_stderr(StringIO()):
            self.assertEqual(reminders.update_reminder(args), 2)

    def test_update_every_conflicts_with_in(self):
        args = update_args(recurrence="10m", relative_duration="5m")
        with redirect_stderr(StringIO()):
            self.assertEqual(reminders.update_reminder(args), 2)

    def test_update_404_reports_not_found_and_exits_nonzero(self):
        args = update_args(message="x")
        with patch.object(reminders, "patch_json", return_value=(404, {"error": {"code": "not_found"}})):
            stderr = StringIO()
            with redirect_stderr(stderr):
                self.assertEqual(reminders.update_reminder(args), 1)
        self.assertIn("not found", stderr.getvalue())

    def test_update_success_prints_status_line(self):
        payloads = []
        args = update_args(message="x")
        with patch.object(reminders, "patch_json", side_effect=self._patch_json(payloads)):
            stdout = StringIO()
            with redirect_stdout(stdout):
                self.assertEqual(reminders.update_reminder(args), 0)
        self.assertIn("Reminder abc is scheduled for worker at 2030-01-01T00:00:00.000Z", stdout.getvalue())

    def test_main_dispatches_update_subcommand(self):
        args = update_args(message="x")
        with patch.object(reminders, "update_reminder", return_value=0) as update_reminder:
            self.assertEqual(reminders.main(["update", "abc", "--message", "x", "--router-url", args.router_url]), 0)
        update_reminder.assert_called_once()
        self.assertEqual(update_reminder.call_args.args[0].id, "abc")


if __name__ == "__main__":
    unittest.main()
