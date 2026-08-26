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


class FetchRepoPaginationTest(unittest.TestCase):
    def test_fetch_repo_follows_every_open_pr_page(self):
        calls = []
        pages = [
            {
                "data": {
                    "repository": {
                        "pullRequests": {
                            "nodes": [
                                {
                                    "number": 1,
                                    "title": "First",
                                    "url": "https://example.test/1",
                                    "author": {"login": "author-one"},
                                    "updatedAt": "2026-06-10T16:00:00Z",
                                    "createdAt": "2026-06-10T15:00:00Z",
                                    "headRefOid": "head-one",
                                    "mergeStateStatus": "CLEAN",
                                    "reviewDecision": "REVIEW_REQUIRED",
                                    "isDraft": False,
                                    "statusCheckRollup": {"contexts": {"nodes": []}},
                                    "latestReviews": {"nodes": []},
                                    "reviewRequests": {"nodes": []},
                                }
                            ],
                            "pageInfo": {"hasNextPage": True, "endCursor": "page-two"},
                        }
                    }
                }
            },
            {
                "data": {
                    "repository": {
                        "pullRequests": {
                            "nodes": [
                                {
                                    "number": 2,
                                    "title": "Second",
                                    "url": "https://example.test/2",
                                    "author": {"login": "author-two"},
                                    "updatedAt": "2026-06-10T16:00:00Z",
                                    "createdAt": "2026-06-10T15:00:00Z",
                                    "headRefOid": "head-two",
                                    "mergeStateStatus": "CLEAN",
                                    "reviewDecision": "REVIEW_REQUIRED",
                                    "isDraft": False,
                                    "statusCheckRollup": {"contexts": {"nodes": []}},
                                    "latestReviews": {"nodes": []},
                                    "reviewRequests": {"nodes": []},
                                }
                            ],
                            "pageInfo": {"hasNextPage": False, "endCursor": None},
                        }
                    }
                }
            },
        ]
        old_gh_json = watch_prs.gh_json
        old_comment_meta = watch_prs.comment_meta
        try:
            watch_prs.gh_json = lambda args: calls.append(args) or pages.pop(0)
            watch_prs.comment_meta = lambda _repo, _number: ([], [])
            prs = watch_prs.fetch_repo("Identity-Digital/dnsid")
        finally:
            watch_prs.gh_json = old_gh_json
            watch_prs.comment_meta = old_comment_meta

        self.assertEqual([1, 2], [pr["number"] for pr in prs])
        self.assertEqual(2, len(calls))
        self.assertTrue(all(call[:2] == ["api", "graphql"] for call in calls))
        self.assertTrue(all("--limit" not in call for call in calls))
        self.assertNotIn("cursor=page-two", calls[0])
        self.assertIn("cursor=page-two", calls[1])


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
                reviews=[],
                review_requests=[watch_prs.VIEWER],
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

    def test_mail_body_includes_each_finding_reason_and_full_head_sha(self):
        first_sha = "a" * 40
        second_sha = "b" * 40
        body = watch_prs.mail_body(
            [
                {
                    "repo": "Identity-Digital/dnsid",
                    "number": 101,
                    "reasons": ["Wolfgang is requested and has not reviewed"],
                    "head_sha": first_sha,
                    "action": "run review-pr workflow",
                },
                {
                    "repo": "Identity-Digital/dnsid-go",
                    "number": 102,
                    "reasons": ["head changed after Wolfgang's latest review"],
                    "head_sha": second_sha,
                    "action": "run review-pr workflow",
                },
            ],
            [],
            Path("/tmp/pr-watch-report.md"),
            "2026-06-10T16:00:00Z",
            False,
        )

        self.assertIn("dnsid#101: Wolfgang is requested and has not reviewed", body)
        self.assertIn(first_sha, body)
        self.assertIn("dnsid-go#102: head changed after Wolfgang's latest review", body)
        self.assertIn(second_sha, body)
        self.assertEqual(2, body.count("run review-pr workflow"))

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


class ClassifyReviewEligibilityTest(unittest.TestCase):
    def test_requested_pr_without_wolfgang_review_is_actionable(self):
        finding = watch_prs.classify(
            base_pr(reviews=[], review_requests=[watch_prs.VIEWER])
        )

        self.assertIsNotNone(finding)
        self.assertEqual(["Wolfgang is requested and has not reviewed"], finding["reasons"])
        self.assertEqual("run review-pr workflow", finding["action"])

    def test_requested_pr_with_updated_head_is_actionable(self):
        finding = watch_prs.classify(base_pr(review_requests=[watch_prs.VIEWER]))

        self.assertIsNotNone(finding)
        self.assertEqual(
            ["head changed after Wolfgang's latest review"], finding["reasons"]
        )

    def test_requested_pr_with_latest_review_missing_commit_oid_is_not_actionable(self):
        finding = watch_prs.classify(
            base_pr(
                review_requests=[watch_prs.VIEWER],
                reviews=[
                    {
                        "author": watch_prs.VIEWER,
                        "state": "APPROVED",
                        "submittedAt": "2026-06-10T16:05:00Z",
                        "commitOid": "",
                    }
                ],
            )
        )

        self.assertIsNone(finding)

    def test_requested_pr_reviewed_at_current_head_is_not_actionable(self):
        finding = watch_prs.classify(
            base_pr(
                review_requests=[watch_prs.VIEWER],
                reviews=[
                    {
                        "author": watch_prs.VIEWER,
                        "state": "APPROVED",
                        "submittedAt": "2026-06-10T16:05:00Z",
                        "commitOid": "newhead",
                    }
                ],
            )
        )

        self.assertIsNone(finding)

    def test_explicitly_requested_draft_uses_current_head_eligibility(self):
        stale_head = watch_prs.classify(
            base_pr(isDraft=True, review_requests=[watch_prs.VIEWER])
        )
        current_head = watch_prs.classify(
            base_pr(
                isDraft=True,
                review_requests=[watch_prs.VIEWER],
                reviews=[
                    {
                        "author": watch_prs.VIEWER,
                        "state": "APPROVED",
                        "submittedAt": "2026-06-10T16:05:00Z",
                        "commitOid": "newhead",
                    }
                ],
            )
        )

        self.assertIsNotNone(stale_head)
        self.assertEqual(
            ["head changed after Wolfgang's latest review"], stale_head["reasons"]
        )
        self.assertIsNone(current_head)

    def test_latest_wolfgang_review_controls_current_head_eligibility(self):
        finding = watch_prs.classify(
            base_pr(
                review_requests=[watch_prs.VIEWER],
                reviews=[
                    {
                        "author": watch_prs.VIEWER,
                        "state": "APPROVED",
                        "submittedAt": "2026-06-10T15:30:00Z",
                        "commitOid": "oldhead",
                    },
                    {
                        "author": watch_prs.VIEWER,
                        "state": "APPROVED",
                        "submittedAt": "2026-06-10T16:05:00Z",
                        "commitOid": "newhead",
                    },
                ],
            )
        )

        self.assertIsNone(finding)

    def test_unrequested_pr_is_not_actionable(self):
        self.assertIsNone(watch_prs.classify(base_pr(reviews=[])))

    def test_wolfgang_authored_pr_is_not_actionable(self):
        self.assertIsNone(
            watch_prs.classify(
                base_pr(author_login=watch_prs.VIEWER, review_requests=[watch_prs.VIEWER])
            )
        )

    def test_dependabot_pr_is_not_actionable(self):
        for author_login in ("dependabot", "dependabot[bot]", "app/dependabot"):
            with self.subTest(author_login=author_login):
                self.assertIn(author_login, watch_prs.QUIET_BOT_AUTHORS)
                self.assertIsNone(
                    watch_prs.classify(
                        base_pr(
                            author_login=author_login,
                            review_requests=[watch_prs.VIEWER],
                        )
                    )
                )


if __name__ == "__main__":
    unittest.main()
