---
name: pr-watch
description: "Watch assigned GitHub pull requests and run the required review workflow from the dedicated PR-review agent."
---

# PR Watch Skill

This skill runs the end-to-end requested-review loop: scan, dispatch, review, and
GitHub disposition. `pr-watch` owns the work. The supervisor receives final
results and genuine exceptions only.

`watch_prs.py` owns selection, current-head state, and report generation. For
every selected PR, this agent must autonomously launch or hand off the required
`review-pr` workflow using `gpt-5.6-luna`, then record the resulting GitHub
disposition. It does not route the configured organization's `web-app` or
`web-app-lseg` repositories for review.

## Scan

Run one scan when this agent receives a PR watch wakeup:

```bash
python3 ~/.claude/skills/pr-watch/tools/watch_prs.py
```

The command writes the full report to stdout and persists it under
`~/.mecha-developer/pr-watch/`. Read the stdout report. Do not run a second
scan just to summarize it.

The scan sends Mailbox V2 only for actionable findings or errors. Quiet scans
remain in local report evidence.

For a read-only check without state writes or mail:

```bash
python3 ~/.claude/skills/pr-watch/tools/watch_prs.py --dry-run
```

## Review

For each selected PR, autonomously launch or hand off the complete `review-pr`
skill workflow with `gpt-5.6-luna`. Do not post to GitHub outside that workflow.
The handoff must include the repository, PR number, and selected head SHA.

Track the selected head through the review. Before posting, verify that the PR
still has that exact head. If it changed, do not post a stale disposition:
record the new head and dispatch a fresh review instead.

Use only mecha-review personas for `robot-review`. Select relevant focus areas
and give each persona two or three related areas. Record a concrete PR-specific
reason for every skipped candidate area in the focus split ledger. Include an
area when there is no reason to skip it. A different persona must validate each
finding before it is included in the review. Never review a PR Wolfgang
authored.

Deliver an actual GitHub disposition for every completed review: `COMMENT` for
validated non-blocking findings, or `APPROVE` when the evidence supports it.
Never use `REQUEST_CHANGES`. Do not push, merge, undraft, or change PR metadata.
Release PRs are special: never submit `REQUEST_CHANGES`; report true product
safety blockers as a non-blocking `COMMENT`, and handle mechanical release
artifact fixes through the authorized release workflow.

## What It Watches

By default, discovery is one paginated GitHub search scoped to the GitHub
organization configured in `~/AGENTS.md`: open pull requests with
`review-requested:<viewer>`. The query excludes that organization's `web-app`
and `web-app-lseg` repositories, so frontend pull requests never enter backend
review routing.

An explicit `--repo` value or `PR_WATCH_REPOS` value must be an
`owner/name` repository in the GitHub organization configured in
`~/AGENTS.md`. The watcher rejects other owners.
Explicit repositories are scanned directly, but the same two frontend
repositories remain excluded from routing.

A PR is selected only when Wolfgang did not author it, Dependabot did not
author it, Wolfgang is explicitly requested as a reviewer, and he has not
reviewed it or its current head differs from his latest review.

State is audit evidence. It does not control selection.

## Tuning

Override default organization discovery for one explicit scan. Every
repository must be in the GitHub organization configured in `~/AGENTS.md`.
Replace `<configured-org>` with that organization:

```bash
python3 ~/.claude/skills/pr-watch/tools/watch_prs.py \
  --repo <configured-org>/backend-service \
  --repo <configured-org>/another-backend-service
```

Environment alternatives:

```bash
PR_WATCH_REPOS=<configured-org>/backend-service,<configured-org>/another-backend-service
PR_WATCH_STATE_DIR=~/.mecha-developer/pr-watch
PR_WATCH_VIEWER=wolfgangmeyers
PR_WATCH_MAIL_TO=supervisor
```

Mailbox is for final dispositions, changed-head re-dispatches, and genuine
exceptions. Do not send routine requested-review work to the supervisor for
dispatch.

## Maintenance

When changing the watcher script, validate it before committing:

```bash
python3 -m py_compile .claude/skills/pr-watch/tools/watch_prs.py
python3 .claude/skills/pr-watch/tools/test_watch_prs.py
python3 .claude/skills/pr-watch/tools/watch_prs.py --help
```
