#!/usr/bin/env python3
"""Focused tests for PR watch classification."""

from __future__ import annotations

import sys
import tempfile
import unittest
from pathlib import Path

import watch_prs


def base_pr(**overrides):
    pr = {
        "repo": "Identity-Digital/dnsid",
        "number": 123,
        "title": "Test PR",
        "url": "https://github.com/Identity-Digital/dnsid/pull/123",
        "author_login": "jmelloy",
        "author": {"login": "jmelloy"},
        "createdAt": "2026-06-10T15:00:00Z",
        "updatedAt": "2026-06-10T16:00:00Z",
        "headRefOid": "newhead",
        "isDraft": False,
        "reviewDecision": "APPROVED",
        "mergeStateStatus": "CLEAN",
        "checks": [],
        "reviews": [
            {
                "author": watch_prs.VIEWER,
                "state": "APPROVED",
                "submittedAt": "2026-06-10T15:30:00Z",
                "commitOid": "oldhead",
            }
        ],
        "review_requests": [],
        "comments_meta": [],
        "files": [],
    }
    pr.update(overrides)
    return pr


def prior_state(**overrides):
    prior = {
        "headRefOid": "oldhead",
        "isDraft": False,
        "reviewRequired": False,
        "viewerRequested": False,
        "viewerReviewAt": "2026-06-10T15:30:00Z",
        "viewerReviewCommitOid": "oldhead",
        "badChecks": [],
        "externalReviews": [],
        "externalComments": [],
    }
    prior.update(overrides)
    return prior


class MailboxV2SenderTest(unittest.TestCase):
    def test_send_mail_uses_the_canonical_v2_sender_once(self):
        commands = []
        old_run = watch_prs.run
        old_to = watch_prs.MAILBOX_TO
        old_from = watch_prs.MAILBOX_FROM
        old_router_url = watch_prs.ROUTER_URL
        try:
            watch_prs.run = lambda command: commands.append(command) or ""
            watch_prs.MAILBOX_TO = "supervisor"
            watch_prs.MAILBOX_FROM = "pr-watch"
            watch_prs.ROUTER_URL = "http://127.0.0.1:8765"

            watch_prs.send_mail("PR watch findings", "one finding")
        finally:
            watch_prs.run = old_run
            watch_prs.MAILBOX_TO = old_to
            watch_prs.MAILBOX_FROM = old_from
            watch_prs.ROUTER_URL = old_router_url

        self.assertEqual(
            [[
                "python3", str(watch_prs.MAILBOX_SEND_V2), "--to", "supervisor",
                "--from", "pr-watch", "--subject", "PR watch findings", "--body", "one finding",
                "--router-url", "http://127.0.0.1:8765",
            ]],
            commands,
        )


class MainOneScanTest(unittest.TestCase):
    def test_normal_invocation_runs_one_scan(self):
        calls = []
        old_argv = sys.argv
        old_pass_once = watch_prs.pass_once
        try:
            sys.argv = ["watch_prs.py", "--router-url", "http://127.0.0.1:8765"]
            watch_prs.pass_once = lambda **kwargs: calls.append(kwargs) or ([], None)

            self.assertEqual(0, watch_prs.main())
        finally:
            sys.argv = old_argv
            watch_prs.pass_once = old_pass_once

        self.assertEqual([{"send": True, "dry_run": False}], calls)


class PassOnceDeliveryTest(unittest.TestCase):
    def test_actionable_finding_is_sent_once_to_supervisor(self):
        old_repos = watch_prs.REPOS
        old_fetch_repo = watch_prs.fetch_repo
        old_try_send_mail = watch_prs.try_send_mail
        old_to = watch_prs.MAILBOX_TO
        old_state_dir = watch_prs.STATE_DIR
        sent = []
        try:
            watch_prs.REPOS = ["Identity-Digital/dnsid"]
            watch_prs.MAILBOX_TO = "supervisor"
            watch_prs.fetch_repo = lambda _repo: [base_pr(
                reviewDecision="REVIEW_REQUIRED",
                comments_meta=[{"author": "jmelloy", "createdAt": "2026-06-10T16:00:00Z"}],
            )]
            watch_prs.try_send_mail = lambda subject, body: sent.append((subject, body))
            with tempfile.TemporaryDirectory() as directory:
                watch_prs.configure_paths(Path(directory))
                findings, _ = watch_prs.pass_once()
        finally:
            watch_prs.REPOS = old_repos
            watch_prs.MAILBOX_TO = old_to
            watch_prs.fetch_repo = old_fetch_repo
            watch_prs.try_send_mail = old_try_send_mail
            watch_prs.configure_paths(old_state_dir)

        self.assertEqual(1, len(findings))
        self.assertEqual(1, len(sent))
        self.assertEqual("PR watch findings", sent[0][0])
        self.assertIn("1 actionable PR(s)", sent[0][1])

    def test_clean_scan_writes_its_report_without_sending_mail(self):
        old_repos = watch_prs.REPOS
        old_fetch_repo = watch_prs.fetch_repo
        old_try_send_mail = watch_prs.try_send_mail
        old_state_dir = watch_prs.STATE_DIR
        sent = []
        try:
            watch_prs.REPOS = ["Identity-Digital/dnsid"]
            watch_prs.fetch_repo = lambda _repo: []
            watch_prs.try_send_mail = lambda subject, body: sent.append((subject, body))
            with tempfile.TemporaryDirectory() as directory:
                watch_prs.configure_paths(Path(directory))
                findings, report_path = watch_prs.pass_once()
                self.assertTrue(report_path.exists())
        finally:
            watch_prs.REPOS = old_repos
            watch_prs.fetch_repo = old_fetch_repo
            watch_prs.try_send_mail = old_try_send_mail
            watch_prs.configure_paths(old_state_dir)

        self.assertEqual([], findings)
        self.assertEqual([], sent)


class ClassifyHeadChangeTest(unittest.TestCase):
    def test_head_change_after_wolfgang_review_is_actionable(self):
        finding = watch_prs.classify(base_pr(), prior_state())

        self.assertIsNotNone(finding)
        self.assertIn("head changed after Wolfgang's latest review", finding["reasons"])
        self.assertEqual("re-review updated head", finding["action"])

    def test_head_change_reviewed_on_current_head_is_not_actionable(self):
        finding = watch_prs.classify(
            base_pr(
                reviews=[
                    {
                        "author": watch_prs.VIEWER,
                        "state": "APPROVED",
                        "submittedAt": "2026-06-10T16:05:00Z",
                        "commitOid": "newhead",
                    }
                ]
            ),
            prior_state(),
        )

        self.assertIsNone(finding)

    def test_draft_head_change_without_bad_checks_is_suppressed(self):
        finding = watch_prs.classify(base_pr(isDraft=True), prior_state())

        self.assertIsNone(finding)

    def test_draft_bad_check_behavior_is_unchanged(self):
        finding = watch_prs.classify(
            base_pr(
                isDraft=True,
                checks=[
                    {
                        "name": "test",
                        "workflowName": "ci",
                        "status": "COMPLETED",
                        "conclusion": "FAILURE",
                    }
                ],
            ),
            prior_state(
                badChecks=[
                    {
                        "name": "old-test",
                        "workflowName": "ci",
                        "status": "COMPLETED",
                        "conclusion": "FAILURE",
                    }
                ]
            ),
        )

        self.assertIsNotNone(finding)
        self.assertEqual(["checks need attention: test FAILURE"], finding["reasons"])

    def test_external_review_signal_excludes_review_commit_metadata(self):
        pr = base_pr(
            reviews=[
                {
                    "author": "greptile-apps",
                    "state": "COMMENTED",
                    "submittedAt": "2026-06-10T15:45:00Z",
                    "commitOid": "reviewcommit",
                }
            ]
        )

        self.assertEqual(
            [
                {
                    "author": "greptile-apps",
                    "state": "COMMENTED",
                    "submittedAt": "2026-06-10T15:45:00Z",
                }
            ],
            watch_prs.external_review_signal(pr),
        )


if __name__ == "__main__":
    unittest.main()
