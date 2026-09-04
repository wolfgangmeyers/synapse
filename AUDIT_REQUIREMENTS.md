# Tmux/router audit requirements

This file is the contract for the native tmux/router audit.

## Required behavior

- The normal Go router launcher enables the audit.
- The audit observes the configured tmux session. Defaults: session `mecha`, delivery target `supervisor`, cadence 30 minutes, and per-run timeout five seconds.
- It inventories existing tmux windows and live router agents. It does not create, rename, start, stop, or otherwise change tmux state.
- A live agent is covered when it has an enabled, nonempty goal or a scheduled recurring reminder. One-shot, inactive, and offline-target reminders do not count.
- It reports counts for tmux windows, live agents, unmapped windows, uncovered agents, active goals, and recurring reminders. A clean audit is silent.
- A finding is one durable mailbox summary from reserved sender `router-audit` to the configured target. The summary excludes agent IDs, window names, goal text, reminder IDs, and reminder bodies.
- At most one unsettled audit message is retained for a target. New findings do not build a queue while that message remains unsettled.
- Tmux, store, sender, ID-source, timeout, and shutdown failures are isolated from lifecycle recovery and emit the stable native-policy diagnostic.
- Policy execution runs outside the lifecycle sweep mutex. Shutdown cancels active policy work and honors its context deadline.
- CLI, Makefile, and supported launcher configuration accept the documented audit target, tmux session, and policy timeout values.

## Acceptance criteria

- Native audit tests cover a clean audit, findings, privacy, configured defaults and overrides, recurring-reminder eligibility, delivery, durable deduplication, cadence, failure isolation, and shutdown.
- Router tests reject external messages whose sender is `router-audit`.
- The router still passes the scoped native audit tests, `go vet ./...`, `gofmt`, and `git diff --check`.
- The audit target is identified by the deterministic target-hash script and recorded in `REVIEW_LEDGER.md`.

## Non-goals

- Managing tmux windows or worker processes.
- Changing agent goals, reminders, leases, lifecycle state, or mailbox delivery state outside the audit message.
- Sending per-agent findings or private agent, goal, or reminder content.
- Replacing supervisor checks, worker ownership, or existing lifecycle recovery.
- Adding a UI, a new external service, or a general policy framework beyond the native router seam needed here.
