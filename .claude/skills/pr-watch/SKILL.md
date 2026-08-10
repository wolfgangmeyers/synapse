---
name: pr-watch
description: "Run and maintain the read-only polling GitHub PR watcher used as a lightweight substitute for the webhook PR review bot. Use when the supervisor needs PR watch triage, PR polling, or maintenance of the watcher."
---

# PR Watch Skill

Run a read-only polling watcher for PRs that may need Wolfgang's attention while the GitHub webhook review bot is not in use.

This is a polling substitute for review-bot intake, not an external communication mechanism. It does not review PRs by itself and does not post to GitHub, Slack, or any other external channel.

## Start The Watcher

The watcher is a one-scan program. Pi receives durable router wakeups and invokes it; do not use `--loop` or a direct polling process.

Launch the deterministic Pi service with the stable Mailbox V2 extension:

```bash
# Run this in a tmux window named pr-watch.
MECHA_ROUTER_URL=http://127.0.0.1:8765 \
pi --provider anthropic --model claude-opus-5 \
  --no-extensions --extension "$HOME/.mecha-developer/extensions/mecha-messaging.js"
```

### Service startup contract

The running `pr-watch` Pi service owns its single durable recurrence. Its initial
prompt must first make it read and use this skill, then run exactly:

```bash
python3 ~/.claude/skills/reminders/tools/reminders.py \
  reconcile-pr-watch --router-url http://127.0.0.1:8765
```

This creates or reconciles exactly one **scheduled** V2 recurrence with target
`pr-watch`, creator `pr-watch`, subject `PR watch wakeup`, and interval
`1800000` milliseconds (30 minutes). It is not a router job: the router only
persists and delivers visible wakeup envelopes. The Pi agent, not its
extension, performs the watcher scan after receiving one.

Reconciliation is fail-closed. It first lists the canonical router state. One
matching scheduled recurrence is retained; duplicate or stale *scheduled*
records owned by this service are canceled before a replacement is created.
Ownership is target `pr-watch` plus creator `pr-watch`, so a legacy recurrence
with a different subject is retired rather than repeatedly waking the watcher.
A due, linked, failed, or otherwise non-scheduled owned record is never
canceled and no replacement is created, because that could overlap a delivery.
A failed or dead-letter record also sends `PR watch error` to `supervisor` for
manual recovery. A deterministic reminder ID makes concurrent startup attempts
idempotent; on an ID conflict the client lists and verifies the other attempt
instead of creating a second recurrence. If the router cannot be listed or
verified, the service reports failure and does not create a reminder.


### Visible wakeup contract

Mailbox V2 is the only scheduler. Never create, replace, or reconcile a
reminder in response to a wakeup; startup reconciliation above is the sole
recurrence-management step. Do not use V1 reminders, cron, `worker_send.py`,
or terminal injection.

When this Pi window visibly receives a Mailbox envelope addressed to
`pr-watch` with subject exactly `PR watch wakeup`, treat it as one instruction:

```bash
python3 ~/.claude/skills/pr-watch/tools/watch_prs.py \
  --router-url http://127.0.0.1:8765
```

Run that command exactly once for that envelope. Do not run a dry-run first,
start a loop, retry it by launching another scan, or scan again merely to
summarize the result. Do not run `watch_prs.py` for any other Mailbox envelope,
including an operational instruction to read this skill. Normal Mailbox
injection/ACK only proves the envelope was delivered to Pi; it does not run the
watcher behind the agent's back.

### Scan reporting contract

Every scan records its report/state locally. Send Mailbox V2 to `supervisor`
only when actionable PRs are found (`PR watch findings`) or a scan fails (`PR
watch error`). Quiet scans are local state only: do not send `PR watch quiet`,
scan-heartbeat, or throttle-status messages to the supervisor.

Do not assume terminal output is a supervisor report. If the script cannot send
its report, send a concise outcome or failure report to `supervisor` yourself
with `~/.claude/skills/supervisor/tools/mailbox_send.py` and the HTTP router
URL. If the supervisor says an existing scan report was missed, do not rescan:
read that scan's existing report and resend its findings once with subject
exactly `PR watch findings`. Do not create a reminder as recovery; leave the
existing V2 recurrence to the router and report the failure.

Prerequisites:

- `gh` is installed and authenticated for the watched repositories.
- The mailbox sender exists at `~/.claude/skills/supervisor/tools/mailbox_send.py`.
- The supervisor window can receive mailbox messages addressed to `supervisor`.

For a read-only check without state writes or mail:

```bash
python3 ~/.claude/skills/pr-watch/tools/watch_prs.py --dry-run
```

## What It Watches

Default repositories:

- `Identity-Digital/dnsid`
- `Identity-Digital/dnsid-infra`
- `Identity-Digital/dnsid-cookbook`
- `Identity-Digital/dnsid-go`
- `Identity-Digital/dnsid-py`
- `Identity-Digital/dnsid-ts`

The watcher flags PRs that look actionable for Wolfgang:

- open PRs by other authors that are ready for review or changed since prior state
- PRs where Wolfgang is requested for review
- PRs where Wolfgang commented/reviewed and the author later responded
- failing checks that look review-relevant
- Greptile or other reviewer activity needing follow-up
- security, auth, OIDC, DNSid SDK, workflow, release, infrastructure, or cookbook changes

It suppresses closed/merged PRs, Wolfgang-authored PRs, same-lane agent PRs when known, ordinary draft PRs, and quiet Dependabot churn unless checks fail or watched areas are touched.

## Mail Behavior

The watcher mails only the supervisor mailbox:

- Subject `PR watch findings` when actionable PRs are found.
- Subject `PR watch error` if the loop itself hits an unhandled exception.
- Do not send quiet, scan-heartbeat, or throttle-status messages.

Messages include the repo/PR, why it is actionable, what changed, and the recommended next action such as launching the review-pr workflow, re-reviewing an existing PR, responding to feedback, or continuing to monitor. For Claude Code workers, the workflow may be invoked as `/review-pr`. For Codex workers, send a plain-language instruction to read `~/code/mecha-review/.claude/skills/review-pr/SKILL.md` and execute that workflow for the PR URL; do not send slash commands to Codex.

Standing-order safety:

- Do not post to Slack.
- Do not post GitHub comments, approvals, change requests, commits, pushes, or merges.
- Keep all external communications technical-only.
- Use mailbox findings to route supervisor action; do not treat this watcher as user-facing notification infrastructure.

## State And Reports

By default, runtime files are written under:

```text
~/.mecha-developer/pr-watch/
  watch-state.json
  last-report.md
  reports/pr-watch-*.md
```

The state file records each open PR's last seen head SHA, review request state, failing-check summary, and external comment/review metadata so repeated passes can distinguish new actionable changes from already-reported ones.

## Tuning

Override the repo list for one explicit scan:

```bash
python3 ~/.claude/skills/pr-watch/tools/watch_prs.py \
  --repo Identity-Digital/dnsid \
  --repo Identity-Digital/dnsid-go
```

Environment alternatives:

```bash
PR_WATCH_REPOS=Identity-Digital/dnsid,Identity-Digital/dnsid-go
PR_WATCH_CADENCE_SECONDS=900
PR_WATCH_HEARTBEAT_SECONDS=3600
PR_WATCH_STATE_DIR=~/.mecha-developer/pr-watch
PR_WATCH_VIEWER=wolfgangmeyers
PR_WATCH_MAIL_TO=supervisor
```

CLI flags can also tune loop cadence, quiet heartbeat cadence, state directory, viewer login, and mailbox destination. Run `--help` for the full option list.

## Maintenance

When changing the watcher script, validate syntax before committing:

```bash
python3 -m py_compile .claude/skills/pr-watch/tools/watch_prs.py
python3 .claude/skills/pr-watch/tools/watch_prs.py --help
```
