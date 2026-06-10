#!/usr/bin/env python3
"""Focused tests for PR watch classification."""

from __future__ import annotations

import unittest

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
