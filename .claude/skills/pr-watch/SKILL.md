---
name: pr-watch
description: "Run and maintain the polling GitHub PR watcher that deterministically routes assigned PRs into the review workflow. Use when the supervisor needs PR watch triage, PR polling, or maintenance of the watcher."
---

# PR Watch Skill

Run a polling watcher that deterministically routes eligible PRs into Wolfgang's required review workflow while the GitHub webhook review bot is not in use.

`watch_prs.py` owns selection and report generation. It cannot execute an agent review or post a review. The receiving agent must run `review-pr` for every PR selected in the report.

Review disposition guardrail: never use `REQUEST_CHANGES`. Approve when the review evidence supports approval; otherwise post a `COMMENT` review.

## Reuse The Canonical Service

The durable service identity and tmux window are both exactly lowercase `pr-watch`. When this skill is invoked, first find and reuse an existing canonical `pr-watch` worker/service. Invoking the skill is not a request to provision another worker. Do not create a new worker, especially one named `PR Watch`, merely because someone asked to use this skill. Provision `pr-watch` only when the canonical worker/service is genuinely absent and a durable watcher is explicitly needed.

## Start The Watcher

The watcher is a one-scan program. Pi receives durable router wakeups and invokes it; do not use `--loop` or a direct polling process.

Launch the deterministic Pi service with Pi's default configured extensions:

```bash
# Run this in a tmux window named pr-watch.
MECHA_ROUTER_URL=http://127.0.0.1:8765 \
pi --provider openai-codex --model gpt-5.6-terra
```

Normal Pi launches pass neither `--extension` nor `--no-extensions`/`-ne`; those
flags are reserved for deliberate extension-test tools. The Mailbox V2 extension
remains available through Pi settings.

### Service startup contract

Under normal provisioning, the running `pr-watch` Pi service owns its single
durable recurrence. Its initial prompt must first make it read and use this
skill, then run exactly:

```bash
python3 ~/.claude/skills/reminders/tools/reminders.py \
  reconcile-pr-watch --router-url http://127.0.0.1:8765
```

This creates or reconciles exactly one **scheduled** V2 recurrence. Under
normal provisioning, its target (the worker that receives the wakeup) is
`pr-watch`; its creator (the service that owns the recurrence) is also
`pr-watch`. The subject is `PR watch wakeup`, and the interval is `1800000`
milliseconds (30 minutes). It is not a router job: the router only persists and
delivers visible wakeup envelopes. The Pi agent, not its extension, performs
the watcher scan after receiving one.

Wolfgang may instead explicitly designate an existing review-agent window to
own the recurrence. In that case, do not create a `pr-watch` worker: the
designated window reconciles the recurrence and dispatches `PR watch wakeup`
to its own configured agent identity as both target and creator.

Reconciliation is fail-closed. It first lists the canonical router state. One
matching scheduled recurrence is retained; duplicate or stale *scheduled*
records owned by this service are canceled before a replacement is created.
Under normal provisioning, ownership is the exact pair target `pr-watch` plus
creator `pr-watch`, so a legacy recurrence with a different subject is retired
rather than repeatedly waking the watcher. For an explicitly designated
existing review-agent window, use that window's configured agent identity for
both fields. Do not use the requester's identity, `supervisor`, or a
display-name variant such as `PR Watch` for either field.
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

When the canonical `pr-watch` service visibly receives a Mailbox envelope
addressed to `pr-watch`, or an explicitly designated existing review-agent
window visibly receives one addressed to its own configured agent identity,
with subject exactly `PR watch wakeup`, treat it as one instruction:

```bash
python3 ~/.claude/skills/pr-watch/tools/watch_prs.py \
  --router-url http://127.0.0.1:8765
```

Run that command exactly once for that envelope. Do not run a dry-run first,
start a loop, retry it by launching another scan, or scan again merely to
summarize the result. Then read that scan's report and run `review-pr` for every
selected PR. Do not run `watch_prs.py` for any other Mailbox envelope, including
an operational instruction to read this skill. Normal Mailbox injection/ACK only
proves the envelope was delivered to Pi; it does not run the watcher behind the
agent's back.

During each routed review, use only mecha-review personas for `robot-review`.
Select only focus areas relevant to the PR, and give each persona two or three
related areas. In the focus split ledger, document a concrete PR-specific
justification for every candidate area that is skipped. Include an area when no
such justification exists. A different persona must validate every finding
before it is included in the review. Never submit `REQUEST_CHANGES`; submit
`APPROVE` when the evidence supports it, otherwise submit `COMMENT`.

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

For every open PR, `watch_prs.py` applies one current-state eligibility
predicate. It selects a PR only when all of these are true:

- Wolfgang did not author it.
- Dependabot did not author it.
- Wolfgang is explicitly requested as a reviewer.
- Wolfgang has not submitted a review, or the current head SHA differs from the
  commit SHA of his latest review.

Prior scan state does not gate selection. State is retained only as auditable
report history. The report records each selected PR and its precise eligibility
reason.

## Mail Behavior

The watcher mails only the supervisor mailbox:

- Subject `PR watch findings` when actionable PRs are found.
- Subject `PR watch error` if the loop itself hits an unhandled exception.
- Do not send quiet, scan-heartbeat, or throttle-status messages.

Messages include the repo/PR, the precise eligibility reason, current head SHA,
and the required next action: run `review-pr` for that PR. The receiving agent
must review every selected PR. For Claude Code workers, the workflow may be
invoked as `/review-pr`. For Codex workers, send a plain-language instruction to
read `~/code/mecha-review/.claude/skills/review-pr/SKILL.md` and execute that
workflow for the PR URL; do not send slash commands to Codex.

The script routes PRs through mailbox findings; it cannot execute the required
agent review. The receiving agent follows the review disposition guardrail:
never `REQUEST_CHANGES`, `APPROVE` when evidence supports it, otherwise
`COMMENT`.

Standing-order safety:

- Do not post to Slack.
- Keep all external communications technical-only.
- Use mailbox findings to route review work; do not treat this watcher as user-facing notification infrastructure.

## State And Reports

By default, runtime files are written under:

```text
~/.mecha-developer/pr-watch/
  watch-state.json
  last-report.md
  reports/pr-watch-*.md
```

The state file records each open PR's last seen head SHA, review request state, and review metadata as auditable report history. It does not determine eligibility.

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
