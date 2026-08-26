---
name: pr-watch
description: "Watch assigned GitHub pull requests and run the required review workflow from the dedicated PR-review agent."
---

# PR Watch Skill

This skill runs in the dedicated agent that watches PRs and reviews them.

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

Default repositories:

- `Roger-Labs-Inc/roger-emr`
- `Roger-Labs-Inc/Roger-Docs-API`
- `Roger-Labs-Inc/steve-bot`
- `Roger-Labs-Inc/App`
- `Roger-Labs-Inc/interview-mobile-eng`
- `Roger-Labs-Inc/arq-ui`

A PR is selected only when Wolfgang did not author it, Dependabot did not
author it, Wolfgang is explicitly requested as a reviewer, and he has not
reviewed it or its current head differs from his latest review.

State is audit evidence. It does not control selection.

## Tuning

Override repositories for one scan:

```bash
python3 ~/.claude/skills/pr-watch/tools/watch_prs.py \
  --repo Roger-Labs-Inc/roger-emr \
  --repo Roger-Labs-Inc/arq-ui
```

Environment alternatives:

```bash
PR_WATCH_REPOS=Roger-Labs-Inc/roger-emr,Roger-Labs-Inc/arq-ui
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
