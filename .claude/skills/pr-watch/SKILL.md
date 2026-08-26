---
name: pr-watch
description: "Watch assigned GitHub pull requests and run the required review workflow from the dedicated PR-review agent."
---

# PR Watch Skill

This skill runs in the dedicated agent that watches PRs and reviews them.

`watch_prs.py` owns selection and report generation. It cannot execute an agent review or post a review. The receiving agent must run `review-pr` for every backend PR selected in the report. It does not route `toggleglobal/web-app` or `toggleglobal/web-app-lseg` for review.

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

For each selected PR, run the complete `review-pr` skill workflow. Do not post
to GitHub outside that workflow.

Use only mecha-review personas for `robot-review`. Select relevant focus areas
and give each persona two or three related areas. Record a concrete PR-specific
reason for every skipped candidate area in the focus split ledger. Include an
area when there is no reason to skip it. A different persona must validate each
finding before it is included in the review.

All findings are `COMMENT`-only. Never use `REQUEST_CHANGES`. Approve only
when the review evidence supports it. Do not push, merge, undraft, or change PR
metadata.

## What It Watches

By default, discovery is one paginated GitHub search scoped to
`toggleglobal`: open pull requests with `review-requested:<viewer>`. The query
excludes `toggleglobal/web-app` and `toggleglobal/web-app-lseg`, so frontend
pull requests never enter backend review routing.

An explicit `--repo` value or `PR_WATCH_REPOS` value must be an
`owner/name` repository in `toggleglobal`. The watcher rejects other owners.
Explicit repositories are scanned directly, but the same two frontend
repositories remain excluded from routing.

A PR is selected only when Wolfgang did not author it, Dependabot did not
author it, Wolfgang is explicitly requested as a reviewer, and he has not
reviewed it or its current head differs from his latest review.

State is audit evidence. It does not control selection.

## Tuning

Override default organization discovery for one explicit scan. Every
repository must be in `toggleglobal`:

```bash
python3 ~/.claude/skills/pr-watch/tools/watch_prs.py \
  --repo toggleglobal/backend-service \
  --repo toggleglobal/another-backend-service
```

Environment alternatives:

```bash
PR_WATCH_REPOS=toggleglobal/backend-service,toggleglobal/another-backend-service
PR_WATCH_STATE_DIR=~/.mecha-developer/pr-watch
PR_WATCH_VIEWER=wolfgangmeyers
PR_WATCH_MAIL_TO=supervisor
```

## Maintenance

When changing the watcher script, validate it before committing:

```bash
python3 -m py_compile .claude/skills/pr-watch/tools/watch_prs.py
python3 .claude/skills/pr-watch/tools/test_watch_prs.py
python3 .claude/skills/pr-watch/tools/watch_prs.py --help
```
